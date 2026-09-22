package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/outbox"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

const (
	sqlOutboxInsert = `
		INSERT INTO ztax.outbox (id, tenant_id, aggregate_key, event_type, schema_ref, payload, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	// The relay's claim query.
	//
	// FOR UPDATE SKIP LOCKED is the whole of the coordination strategy
	// (ADR-0014 §2.2): relay instances run concurrently without a leader,
	// without a lease, and without blocking each other — a second relay simply
	// walks past the rows the first is holding.
	//
	// ORDER BY created_at keeps delivery roughly in order, and "roughly" is the
	// honest word: ordering is guaranteed per aggregate_key by the broker's
	// partitioning, not by this query.
	sqlOutboxClaim = `
		SELECT id, tenant_id, aggregate_key, event_type, schema_ref, payload, created_at
		FROM   ztax.outbox
		WHERE  published_at IS NULL
		ORDER  BY created_at
		LIMIT  $1
		FOR UPDATE SKIP LOCKED`

	sqlOutboxMarkPublished = `
		UPDATE ztax.outbox SET published_at = $2, attempts = attempts + 1, last_error = NULL
		WHERE id = $1`

	sqlOutboxRecordFailure = `
		UPDATE ztax.outbox SET attempts = attempts + 1, last_error = $2
		WHERE id = $1`

	// The lag SLI. ADR-0014 §2.11 makes this correctness-adjacent rather than a
	// performance metric: a relay that has fallen behind is a system where
	// committed state is not yet visible externally.
	sqlOutboxLag = `
		SELECT COALESCE(EXTRACT(EPOCH FROM (now() - MIN(created_at))), 0)
		FROM   ztax.outbox
		WHERE  published_at IS NULL`
)

// OutboxRepo reads and writes outbox rows.
type OutboxRepo struct{ s *Store }

// Outbox returns the outbox repository.
func (s *Store) Outbox() *OutboxRepo { return &OutboxRepo{s: s} }

// Append writes an event.
//
// It takes no transaction argument and needs none: the context carries one when
// the caller opened it, which is always, because ADR-0014 §2.1 requires the row
// to be written in the same transaction as the change it describes. A caller
// that forgot would write outside a transaction and the event would be
// published for a change that never committed — so Append is never called
// except from inside a use case that has already begun one.
func (r *OutboxRepo) Append(ctx context.Context, e outbox.Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	payload, err := canonical.Encode(e.Payload)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlOutboxInsert,
		e.ID.UUID(), e.TenantID.UUID(), e.AggregateKey, e.Type, e.SchemaRef, payload, e.CreatedAt)
	return mapError(err, "insert outbox event")
}

// Claim locks up to limit unpublished rows for this relay instance.
//
// The rows stay locked until the caller's transaction ends, so the caller must
// publish and mark within that transaction. That coupling is deliberate: a
// relay that claimed rows, committed, and then published would have a window
// where a crash loses the claim and the rows are never retried.
func (r *OutboxRepo) Claim(ctx context.Context, limit int) ([]outbox.Event, error) {
	rows, err := r.s.db(ctx).Query(ctx, sqlOutboxClaim, limit)
	if err != nil {
		return nil, mapError(err, "claim outbox events")
	}
	defer rows.Close()

	var out []outbox.Event
	for rows.Next() {
		var (
			eventUUID  uuid.UUID
			tenantUUID uuid.UUID
			payload    []byte
			e          outbox.Event
		)
		if err := rows.Scan(&eventUUID, &tenantUUID, &e.AggregateKey, &e.Type, &e.SchemaRef, &payload, &e.CreatedAt); err != nil {
			return nil, mapError(err, "scan outbox event")
		}
		e.ID = id.NewOutboxID(eventUUID)
		e.TenantID = id.NewTenantID(tenantUUID)
		// The payload was canonical when it was written and is republished
		// verbatim. Re-encoding it here would mean the delivered bytes attest
		// to this build's serializer rather than to what was committed.
		e.Payload = canonical.Raw(payload)
		out = append(out, e)
	}
	return out, mapError(rows.Err(), "claim outbox events")
}

// MarkPublished records a successful delivery.
func (r *OutboxRepo) MarkPublished(ctx context.Context, eventID id.OutboxID, at time.Time) error {
	_, err := r.s.db(ctx).Exec(ctx, sqlOutboxMarkPublished, eventID.UUID(), at)
	return mapError(err, "mark outbox published")
}

// RecordFailure counts a failed delivery without marking the row published, so
// the next pass retries it.
func (r *OutboxRepo) RecordFailure(ctx context.Context, eventID id.OutboxID, reason string) error {
	// Truncated: last_error is an operational breadcrumb, and a driver error
	// from a broker can be very long.
	if len(reason) > 500 {
		reason = reason[:500]
	}
	_, err := r.s.db(ctx).Exec(ctx, sqlOutboxRecordFailure, eventID.UUID(), reason)
	return mapError(err, "record outbox failure")
}

// Lag returns the age of the oldest unpublished event.
func (r *OutboxRepo) Lag(ctx context.Context) (time.Duration, error) {
	var seconds float64
	if err := r.s.db(ctx).QueryRow(ctx, sqlOutboxLag).Scan(&seconds); err != nil {
		return 0, mapError(err, "read outbox lag")
	}
	return time.Duration(seconds * float64(time.Second)), nil
}
