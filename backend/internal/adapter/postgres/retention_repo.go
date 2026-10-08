package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/retention"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// retention_policy, legal_hold, legal_hold_event — migration 000020
// ---------------------------------------------------------------------------

const (
	sqlPolicyColumns = `policy_id, version, record_class, country_code, years, trigger_kind, effective_from,
		citation, recorded_at, recorded_by`
	sqlPolicyInsert = `INSERT INTO ztax.retention_policy (tenant_id, ` + sqlPolicyColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	sqlPolicyList = `SELECT ` + sqlPolicyColumns + ` FROM ztax.retention_policy
		WHERE tenant_id = $1 ORDER BY policy_id, version`
	sqlHoldLock   = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
	sqlHoldInsert = `INSERT INTO ztax.legal_hold (tenant_id, hold_id, matter, placed_at, placed_by)
		VALUES ($1, $2, $3, $4, $5)`
	sqlHoldEventInsert = `INSERT INTO ztax.legal_hold_event (tenant_id, hold_id, seq, kind, legal_entity_id,
		business_keys, decision_ids, event_from, event_to, reason, recorded_at, recorded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	sqlHoldHeaders = `SELECT hold_id, matter FROM ztax.legal_hold WHERE tenant_id = $1`
	sqlHoldEvents  = `SELECT hold_id, seq, kind, legal_entity_id, business_keys, decision_ids, event_from, event_to,
		reason, recorded_at, recorded_by
		FROM ztax.legal_hold_event WHERE tenant_id = $1`
)

// RetentionRepo implements port.RetentionRepository.
type RetentionRepo struct{ s *Store }

// Retention returns the retention repository.
func (s *Store) Retention() *RetentionRepo { return &RetentionRepo{s: s} }

var _ port.RetentionRepository = (*RetentionRepo)(nil)

// AppendPolicy records a policy version.
func (r *RetentionRepo) AppendPolicy(ctx context.Context, p retention.Policy) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return retentionFault(err, "The retention policy is not well-formed.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlPolicyInsert, tenant.UUID(), p.ID, p.Version, string(p.Class), p.Country, p.Years,
		string(p.Trigger), p.EffectiveFrom.UTC(), p.Citation, p.RecordedAt.UTC(), p.RecordedBy.UUID())
	return mapError(err, "insert retention policy")
}

// Policies returns every policy version of the tenant.
func (r *RetentionRepo) Policies(ctx context.Context) ([]retention.Policy, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlPolicyList, tenant.UUID())
	if err != nil {
		return nil, mapError(err, "read retention policies")
	}
	defer rows.Close()
	var out []retention.Policy
	for rows.Next() {
		var (
			p              retention.Policy
			class, trigger string
			by             uuid.UUID
		)
		if err := rows.Scan(&p.ID, &p.Version, &class, &p.Country, &p.Years, &trigger, &p.EffectiveFrom,
			&p.Citation, &p.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan retention policy")
		}
		p.Class, p.Trigger, p.RecordedBy = retention.RecordClass(class), retention.Trigger(trigger), id.NewUserID(by)
		p.EffectiveFrom, p.RecordedAt = p.EffectiveFrom.UTC(), p.RecordedAt.UTC()
		out = append(out, p)
	}
	return out, mapError(rows.Err(), "read retention policies")
}

// LockHolds takes the tenant's hold lock for the transaction.
func (r *RetentionRepo) LockHolds(ctx context.Context) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlHoldLock, tenant.String()+"/legal-hold")
	return mapError(err, "lock legal holds")
}

// CreateHold records a hold and its first event.
func (r *RetentionRepo) CreateHold(ctx context.Context, h retention.Hold, placed retention.HoldEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if h.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The hold belongs to a tenant outside the caller's scope.")
	}
	if placed.Kind != retention.EventPlaced || placed.Seq != 1 || placed.Hold != h.ID {
		return retentionFault(nil, "A hold is created by its PLACED event.")
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlHoldInsert, tenant.UUID(), h.ID.UUID(), h.Matter,
		placed.RecordedAt.UTC(), placed.RecordedBy.UUID()); err != nil {
		return mapError(err, "insert legal hold")
	}
	return r.AppendHoldEvent(ctx, placed)
}

// AppendHoldEvent records one hold event.
func (r *RetentionRepo) AppendHoldEvent(ctx context.Context, e retention.HoldEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	var from, to *time.Time
	if !e.Scope.EventFrom.IsZero() {
		f, t := e.Scope.EventFrom.UTC(), e.Scope.EventTo.UTC()
		from, to = &f, &t
	}
	var le *uuid.UUID
	if !e.Scope.LegalEntity.IsZero() {
		u := e.Scope.LegalEntity.UUID()
		le = &u
	}
	keys := e.Scope.BusinessKeys
	if keys == nil {
		keys = []string{}
	}
	decisions := make([]uuid.UUID, len(e.Scope.Decisions))
	for i, d := range e.Scope.Decisions {
		decisions[i] = d.UUID()
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlHoldEventInsert, tenant.UUID(), e.Hold.UUID(), e.Seq, string(e.Kind), le,
		keys, decisions, from, to, e.Reason, e.RecordedAt.UTC(), e.RecordedBy.UUID())
	return mapError(err, "insert legal hold event")
}

// Hold returns one hold.
func (r *RetentionRepo) Hold(ctx context.Context, holdID id.LegalHoldID) (retention.Hold, error) {
	holds, err := r.holds(ctx, &holdID)
	if err != nil {
		return retention.Hold{}, err
	}
	if len(holds) == 0 {
		return retention.Hold{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such legal hold.")
	}
	return holds[0], nil
}

// Holds returns every hold of the tenant, newest first.
func (r *RetentionRepo) Holds(ctx context.Context) ([]retention.Hold, error) {
	return r.holds(ctx, nil)
}

type holdHeader struct {
	id     id.LegalHoldID
	matter string
}

func (r *RetentionRepo) holds(ctx context.Context, only *id.LegalHoldID) ([]retention.Hold, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	headerSQL, eventSQL := sqlHoldHeaders+` ORDER BY placed_at DESC, hold_id`, sqlHoldEvents+` ORDER BY hold_id, seq`
	args := []any{tenant.UUID()}
	if only != nil {
		headerSQL, eventSQL = sqlHoldHeaders+` AND hold_id = $2`, sqlHoldEvents+` AND hold_id = $2 ORDER BY seq`
		args = append(args, only.UUID())
	}
	db := r.s.db(ctx)

	var headers []holdHeader
	rows, err := db.Query(ctx, headerSQL, args...)
	if err != nil {
		return nil, mapError(err, "read legal holds")
	}
	for rows.Next() {
		var (
			u uuid.UUID
			h holdHeader
		)
		if err := rows.Scan(&u, &h.matter); err != nil {
			rows.Close()
			return nil, mapError(err, "scan legal hold")
		}
		h.id = id.NewLegalHoldID(u)
		headers = append(headers, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read legal holds")
	}

	events := map[id.LegalHoldID][]retention.HoldEvent{}
	rows, err = db.Query(ctx, eventSQL, args...)
	if err != nil {
		return nil, mapError(err, "read legal hold events")
	}
	defer rows.Close()
	for rows.Next() {
		var (
			e            retention.HoldEvent
			holdUUID, by uuid.UUID
			kind         string
			le           *uuid.UUID
			decisions    []uuid.UUID
			from, to     *time.Time
		)
		if err := rows.Scan(&holdUUID, &e.Seq, &kind, &le, &e.Scope.BusinessKeys, &decisions, &from, &to,
			&e.Reason, &e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan legal hold event")
		}
		e.Hold, e.Kind, e.RecordedBy, e.RecordedAt = id.NewLegalHoldID(holdUUID), retention.EventKind(kind), id.NewUserID(by), e.RecordedAt.UTC()
		if le != nil {
			e.Scope.LegalEntity = id.NewLegalEntityID(*le)
		}
		for _, d := range decisions {
			e.Scope.Decisions = append(e.Scope.Decisions, id.NewDecisionID(d))
		}
		if len(e.Scope.BusinessKeys) == 0 {
			e.Scope.BusinessKeys = nil
		}
		if from != nil && to != nil {
			e.Scope.EventFrom, e.Scope.EventTo = from.UTC(), to.UTC()
		}
		events[e.Hold] = append(events[e.Hold], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read legal hold events")
	}

	out := make([]retention.Hold, 0, len(headers))
	for _, h := range headers {
		held, err := retention.Fold(h.id, tenant, h.matter, events[h.id])
		if err != nil {
			return nil, retentionFault(err, "A stored legal hold's history does not fold.")
		}
		out = append(out, held)
	}
	return out, nil
}

// retentionFault is an internal error: a retention record no correct writer
// produces.
func retentionFault(err error, msg string) error { return obligationFault(err, msg) }
