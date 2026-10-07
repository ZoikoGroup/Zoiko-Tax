package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/webhook"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// webhooks — migration 000014
// ---------------------------------------------------------------------------

const (
	sqlWebhookInsert = `INSERT INTO ztax.webhook_subscription
		(tenant_id, webhook_id, url, event_types, description, created_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
	sqlWebhookStatusInsert = `INSERT INTO ztax.webhook_status
		(tenant_id, webhook_id, seq, status, recorded_at, recorded_by)
		VALUES ($1, $2, $3, $4, $5, $6)`
	//nolint:gosec // G101: a statement naming the sealed-secret column, not a credential
	sqlWebhookSecretInsert = `INSERT INTO ztax.webhook_secret
		(tenant_id, webhook_id, version, sealed_secret, created_at, created_by, retires_previous_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	// A subscription with its latest status.
	sqlWebhookState = `SELECT w.tenant_id, w.webhook_id, w.url, w.event_types, w.description, w.created_at,
		w.created_by, s.seq, s.status, s.recorded_at, s.recorded_by
		FROM ztax.webhook_subscription w
		JOIN ztax.webhook_status s ON s.tenant_id = w.tenant_id AND s.webhook_id = w.webhook_id
		WHERE w.tenant_id = $1
		  AND s.seq = (SELECT max(x.seq) FROM ztax.webhook_status x
		               WHERE x.tenant_id = w.tenant_id AND x.webhook_id = w.webhook_id)`
	sqlWebhookByID    = sqlWebhookState + ` AND w.webhook_id = $2`
	sqlWebhookList    = sqlWebhookState + ` ORDER BY w.created_at, w.webhook_id LIMIT $2`
	sqlWebhookMatches = sqlWebhookState + ` AND s.status = 'ACTIVE' AND $2 = ANY (w.event_types)
		ORDER BY w.webhook_id`
	sqlWebhookSecrets = `SELECT webhook_id, version, sealed_secret, created_at, created_by, retires_previous_at
		FROM ztax.webhook_secret WHERE tenant_id = $1 AND webhook_id = $2 ORDER BY version`
	sqlWebhookLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	sqlDeliveryColumns = `tenant_id, delivery_id, webhook_id, event_id, event_type, body, status, attempts,
		next_attempt_at, created_at, delivered_at, replay_of, replayed_by`
	sqlDeliveryInsert = `INSERT INTO ztax.webhook_delivery (` + sqlDeliveryColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (tenant_id, webhook_id, event_id) WHERE replay_of IS NULL DO NOTHING`
	sqlDeliveryByID = `SELECT ` + sqlDeliveryColumns + ` FROM ztax.webhook_delivery
		WHERE tenant_id = $1 AND delivery_id = $2`
	sqlDeliveryList = `SELECT ` + sqlDeliveryColumns + ` FROM ztax.webhook_delivery
		WHERE tenant_id = $1 AND webhook_id = $2 AND ($3 = '' OR status = $3)
		ORDER BY created_at DESC, delivery_id DESC LIMIT $4`
	sqlDeliveryAttempts = `SELECT delivery_id, attempt, started_at, duration_ms, status_code, error
		FROM ztax.webhook_attempt WHERE tenant_id = $1 AND delivery_id = $2 ORDER BY attempt`
	// The lease: due rows, skipping any another dispatcher holds, pushed
	// forward in the statement that claims them.
	sqlDeliveryClaim = `UPDATE ztax.webhook_delivery d SET next_attempt_at = $2
		FROM (SELECT tenant_id, delivery_id FROM ztax.webhook_delivery
		      WHERE status = 'PENDING' AND next_attempt_at <= $1
		      ORDER BY next_attempt_at LIMIT $3 FOR UPDATE SKIP LOCKED) due
		WHERE d.tenant_id = due.tenant_id AND d.delivery_id = due.delivery_id
		RETURNING ` + `d.tenant_id, d.delivery_id, d.webhook_id, d.event_id, d.event_type, d.body, d.status,
		d.attempts, d.next_attempt_at, d.created_at, d.delivered_at, d.replay_of, d.replayed_by`
	sqlAttemptInsert = `INSERT INTO ztax.webhook_attempt
		(tenant_id, delivery_id, attempt, started_at, duration_ms, status_code, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
	// Only a PENDING delivery moves, and only from the attempt count the
	// dispatcher read: a delivery another dispatcher already settled is left
	// alone.
	sqlDeliverySettle = `UPDATE ztax.webhook_delivery
		SET status = $3, attempts = $4, next_attempt_at = $5, delivered_at = $6
		WHERE tenant_id = $1 AND delivery_id = $2 AND status = 'PENDING' AND attempts = $4 - 1`
	sqlDeliveryBury = `UPDATE ztax.webhook_delivery SET status = 'DEAD', next_attempt_at = NULL
		WHERE tenant_id = $1 AND delivery_id = $2 AND status = 'PENDING'`
)

const webhookListCap = 500

// WebhookRepo implements port.WebhookRepository.
type WebhookRepo struct{ s *Store }

// Webhooks returns the webhook repository.
func (s *Store) Webhooks() *WebhookRepo { return &WebhookRepo{s: s} }

var _ port.WebhookRepository = (*WebhookRepo)(nil)

// Create writes the subscription, its first status and its first secret.
func (r *WebhookRepo) Create(ctx context.Context, sub webhook.Subscription, first webhook.StatusChange, secret port.SealedSecret) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if sub.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The webhook belongs to a tenant outside the caller's scope.")
	}
	if first.Seq != 1 || first.Status != webhook.StatusActive || secret.Version != 1 {
		return webhookFault(nil, "A webhook is created ACTIVE with secret version 1.")
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlWebhookInsert, tenant.UUID(), sub.ID.UUID(), sub.URL, sub.EventTypes,
		optString(sub.Description), sub.CreatedAt.UTC(), optUser(sub.CreatedBy)); err != nil {
		return mapError(err, "insert webhook")
	}
	if err := r.AppendStatus(ctx, first); err != nil {
		return err
	}
	return r.AppendSecret(ctx, secret)
}

// Lock takes the subscription's lock until the transaction ends.
func (r *WebhookRepo) Lock(ctx context.Context, webhookID id.WebhookID) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlWebhookLock, tenant.String()+"/webhook/"+webhookID.String())
	return mapError(err, "lock webhook")
}

// ByID returns a subscription and its current status.
func (r *WebhookRepo) ByID(ctx context.Context, webhookID id.WebhookID) (port.WebhookState, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return port.WebhookState{}, err
	}
	return scanWebhookState(r.s.db(ctx).QueryRow(ctx, sqlWebhookByID, tenant.UUID(), webhookID.UUID()))
}

// List returns the tenant's subscriptions, oldest first.
func (r *WebhookRepo) List(ctx context.Context, limit int) ([]port.WebhookState, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > webhookListCap {
		limit = webhookListCap
	}
	return r.states(ctx, sqlWebhookList, tenant.UUID(), limit)
}

// Matching returns the ACTIVE subscriptions receiving an event type.
func (r *WebhookRepo) Matching(ctx context.Context, eventType string) ([]webhook.Subscription, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	states, err := r.states(ctx, sqlWebhookMatches, tenant.UUID(), eventType)
	if err != nil {
		return nil, err
	}
	out := make([]webhook.Subscription, len(states))
	for i, s := range states {
		out[i] = s.Subscription
	}
	return out, nil
}

func (r *WebhookRepo) states(ctx context.Context, sql string, args ...any) ([]port.WebhookState, error) {
	rows, err := r.s.db(ctx).Query(ctx, sql, args...)
	if err != nil {
		return nil, mapError(err, "list webhooks")
	}
	defer rows.Close()
	var out []port.WebhookState
	for rows.Next() {
		st, err := scanWebhookState(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, mapError(rows.Err(), "list webhooks")
}

// AppendStatus writes the next status.
func (r *WebhookRepo) AppendStatus(ctx context.Context, c webhook.StatusChange) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if c.Seq < 1 || !c.Status.Valid() || c.RecordedAt.IsZero() {
		return webhookFault(nil, "The webhook status is not well-formed.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlWebhookStatusInsert, tenant.UUID(), c.Webhook.UUID(), c.Seq, string(c.Status),
		c.RecordedAt.UTC(), optUser(c.RecordedBy))
	return conflictOnDuplicate(err, "insert webhook status", "The webhook changed since it was read; read it again and retry.")
}

// Secrets returns every sealed version, oldest first.
func (r *WebhookRepo) Secrets(ctx context.Context, webhookID id.WebhookID) ([]port.SealedSecret, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlWebhookSecrets, tenant.UUID(), webhookID.UUID())
	if err != nil {
		return nil, mapError(err, "read webhook secrets")
	}
	defer rows.Close()
	var out []port.SealedSecret
	for rows.Next() {
		var (
			whUUID    uuid.UUID
			s         port.SealedSecret
			createdBy *uuid.UUID
			retires   *time.Time
		)
		if err := rows.Scan(&whUUID, &s.Version, &s.Sealed, &s.CreatedAt, &createdBy, &retires); err != nil {
			return nil, mapError(err, "scan webhook secret")
		}
		s.Webhook = id.NewWebhookID(whUUID)
		s.CreatedAt = s.CreatedAt.UTC()
		if createdBy != nil {
			s.CreatedBy = id.NewUserID(*createdBy)
		}
		if retires != nil {
			s.RetiresPreviousAt = retires.UTC()
		}
		out = append(out, s)
	}
	return out, mapError(rows.Err(), "read webhook secrets")
}

// AppendSecret writes the next sealed version.
func (r *WebhookRepo) AppendSecret(ctx context.Context, s port.SealedSecret) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if s.Version < 1 || len(s.Sealed) == 0 || (s.Version == 1) != s.RetiresPreviousAt.IsZero() {
		return webhookFault(nil, "The webhook secret version is not well-formed.")
	}
	var retires *time.Time
	if !s.RetiresPreviousAt.IsZero() {
		t := s.RetiresPreviousAt.UTC()
		retires = &t
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlWebhookSecretInsert, tenant.UUID(), s.Webhook.UUID(), s.Version, s.Sealed,
		s.CreatedAt.UTC(), optUser(s.CreatedBy), retires)
	return conflictOnDuplicate(err, "insert webhook secret", "The webhook's secret was rotated since it was read; read it again and retry.")
}

// InsertDelivery writes a delivery, once per event and subscription unless
// it is a replay.
func (r *WebhookRepo) InsertDelivery(ctx context.Context, d webhook.Delivery) (bool, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	if d.TenantID != tenant {
		return false, errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The delivery belongs to a tenant outside the caller's scope.")
	}
	if d.Status != webhook.DeliveryPending || d.NextAttemptAt.IsZero() || len(d.Body) == 0 {
		return false, webhookFault(nil, "A delivery is created PENDING, scheduled, and with a body.")
	}
	tag, err := r.s.db(ctx).Exec(ctx, sqlDeliveryInsert, tenant.UUID(), d.ID.UUID(), d.Webhook.UUID(), d.EventID.UUID(),
		d.EventType, d.Body, string(d.Status), d.Attempts, d.NextAttemptAt.UTC(), d.CreatedAt.UTC(), nil,
		optUUID(d.ReplayOf), optUser(d.ReplayedBy))
	if err != nil {
		return false, mapError(err, "insert webhook delivery")
	}
	return tag.RowsAffected() == 1, nil
}

// Delivery returns one delivery and its attempts.
func (r *WebhookRepo) Delivery(ctx context.Context, deliveryID id.DeliveryID) (webhook.Delivery, []webhook.Attempt, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return webhook.Delivery{}, nil, err
	}
	d, err := scanDelivery(r.s.db(ctx).QueryRow(ctx, sqlDeliveryByID, tenant.UUID(), deliveryID.UUID()))
	if err != nil {
		return webhook.Delivery{}, nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlDeliveryAttempts, tenant.UUID(), deliveryID.UUID())
	if err != nil {
		return webhook.Delivery{}, nil, mapError(err, "read webhook attempts")
	}
	defer rows.Close()
	var attempts []webhook.Attempt
	for rows.Next() {
		var (
			dUUID      uuid.UUID
			a          webhook.Attempt
			durationMS int
			status     *int
			msg        *string
		)
		if err := rows.Scan(&dUUID, &a.N, &a.StartedAt, &durationMS, &status, &msg); err != nil {
			return webhook.Delivery{}, nil, mapError(err, "scan webhook attempt")
		}
		a.Delivery = id.NewDeliveryID(dUUID)
		a.StartedAt = a.StartedAt.UTC()
		a.Duration = time.Duration(durationMS) * time.Millisecond
		if status != nil {
			a.StatusCode = *status
		}
		a.Error = deref(msg)
		attempts = append(attempts, a)
	}
	return d, attempts, mapError(rows.Err(), "read webhook attempts")
}

// Deliveries lists a subscription's deliveries, newest first.
func (r *WebhookRepo) Deliveries(ctx context.Context, webhookID id.WebhookID, status webhook.DeliveryStatus, limit int) ([]webhook.Delivery, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > webhookListCap {
		limit = webhookListCap
	}
	return r.deliveries(ctx, "list webhook deliveries", sqlDeliveryList, tenant.UUID(), webhookID.UUID(), string(status), limit)
}

// ClaimDue leases due deliveries across the cell.
func (r *WebhookRepo) ClaimDue(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]webhook.Delivery, error) {
	return r.deliveries(ctx, "claim webhook deliveries", sqlDeliveryClaim, now.UTC(), now.Add(lease).UTC(), limit)
}

func (r *WebhookRepo) deliveries(ctx context.Context, op, sql string, args ...any) ([]webhook.Delivery, error) {
	rows, err := r.s.db(ctx).Query(ctx, sql, args...)
	if err != nil {
		return nil, mapError(err, op)
	}
	defer rows.Close()
	var out []webhook.Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, mapError(rows.Err(), op)
}

// RecordAttempt writes an attempt and settles the delivery it moved.
func (r *WebhookRepo) RecordAttempt(ctx context.Context, a webhook.Attempt, d webhook.Delivery) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if a.N != d.Attempts || a.Delivery != d.ID || !d.Status.Valid() {
		return webhookFault(nil, "The attempt does not belong to the delivery it settles.")
	}
	msg := a.Error
	if len(msg) > webhook.MaxAttemptError {
		msg = msg[:webhook.MaxAttemptError]
	}
	var status *int
	if a.StatusCode != 0 {
		status = &a.StatusCode
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlAttemptInsert, tenant.UUID(), a.Delivery.UUID(), a.N, a.StartedAt.UTC(),
		int(a.Duration/time.Millisecond), status, optString(msg)); err != nil {
		return conflictOnDuplicate(err, "insert webhook attempt", "Another dispatcher recorded this attempt.")
	}
	var next, delivered *time.Time
	if d.Status == webhook.DeliveryPending {
		t := d.NextAttemptAt.UTC()
		next = &t
	}
	if d.Status == webhook.DeliveryDelivered {
		t := d.DeliveredAt.UTC()
		delivered = &t
	}
	tag, err := r.s.db(ctx).Exec(ctx, sqlDeliverySettle, tenant.UUID(), d.ID.UUID(), string(d.Status), d.Attempts, next, delivered)
	if err != nil {
		return mapError(err, "settle webhook delivery")
	}
	if tag.RowsAffected() != 1 {
		return errs.New(errs.CategoryConflict, errs.ReasonOptimisticConflict, "The delivery was settled by another dispatcher.")
	}
	return nil
}

// Bury dead-letters a pending delivery.
func (r *WebhookRepo) Bury(ctx context.Context, deliveryID id.DeliveryID) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlDeliveryBury, tenant.UUID(), deliveryID.UUID())
	return mapError(err, "bury webhook delivery")
}

func scanWebhookState(row pgx.Row) (port.WebhookState, error) {
	var (
		tenantUUID, whUUID    uuid.UUID
		st                    port.WebhookState
		description           *string
		createdBy, recordedBy *uuid.UUID
		status                string
	)
	if err := row.Scan(&tenantUUID, &whUUID, &st.Subscription.URL, &st.Subscription.EventTypes, &description,
		&st.Subscription.CreatedAt, &createdBy, &st.Status.Seq, &status, &st.Status.RecordedAt, &recordedBy); err != nil {
		return port.WebhookState{}, mapError(err, "scan webhook")
	}
	st.Subscription.ID = id.NewWebhookID(whUUID)
	st.Subscription.TenantID = id.NewTenantID(tenantUUID)
	st.Subscription.Description = deref(description)
	st.Subscription.CreatedAt = st.Subscription.CreatedAt.UTC()
	if createdBy != nil {
		st.Subscription.CreatedBy = id.NewUserID(*createdBy)
	}
	st.Status.Webhook = st.Subscription.ID
	st.Status.Status = webhook.Status(status)
	st.Status.RecordedAt = st.Status.RecordedAt.UTC()
	if recordedBy != nil {
		st.Status.RecordedBy = id.NewUserID(*recordedBy)
	}
	return st, nil
}

func scanDelivery(row pgx.Row) (webhook.Delivery, error) {
	var (
		tenantUUID, dUUID, whUUID, eventUUID uuid.UUID
		d                                    webhook.Delivery
		status                               string
		next, delivered                      *time.Time
		replayOf, replayedBy                 *uuid.UUID
	)
	if err := row.Scan(&tenantUUID, &dUUID, &whUUID, &eventUUID, &d.EventType, &d.Body, &status, &d.Attempts,
		&next, &d.CreatedAt, &delivered, &replayOf, &replayedBy); err != nil {
		return webhook.Delivery{}, mapError(err, "scan webhook delivery")
	}
	d.TenantID = id.NewTenantID(tenantUUID)
	d.ID = id.NewDeliveryID(dUUID)
	d.Webhook = id.NewWebhookID(whUUID)
	d.EventID = id.NewOutboxID(eventUUID)
	d.Status = webhook.DeliveryStatus(status)
	d.CreatedAt = d.CreatedAt.UTC()
	if next != nil {
		d.NextAttemptAt = next.UTC()
	}
	if delivered != nil {
		d.DeliveredAt = delivered.UTC()
	}
	if replayOf != nil {
		r := id.NewDeliveryID(*replayOf)
		d.ReplayOf = &r
	}
	if replayedBy != nil {
		d.ReplayedBy = id.NewUserID(*replayedBy)
	}
	return d, nil
}

// conflictOnDuplicate maps a key collision on an append to an optimistic
// conflict: the writer decided against a history that has since grown.
func conflictOnDuplicate(err error, op, detail string) error {
	if err == nil {
		return nil
	}
	mapped := mapError(err, op)
	if errs.ReasonOf(mapped) == errs.ReasonAlreadyExists {
		return errs.Wrap(err, errs.CategoryConflict, errs.ReasonOptimisticConflict, detail)
	}
	return mapped
}

// webhookFault is an internal error: a webhook record no correct writer
// produces.
func webhookFault(err error, msg string) error { return obligationFault(err, msg) }
