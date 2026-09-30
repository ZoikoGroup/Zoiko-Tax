package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/idempotency"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// idempotency_record — ADR-0013
// ---------------------------------------------------------------------------
//
// This is the one table on the fiscal path the application may UPDATE and
// DELETE (000004's grant), and every statement that does so names the state it
// expects. A Complete that finds the record already settled, or a Release that
// finds it settled, changes nothing — so a settled record, once written, is as
// immutable here as a decision is by grant.

const (
	// ON CONFLICT DO NOTHING rather than catching 23505: the primary key
	// refusing the insert is the expected answer for every retry, and an
	// expected answer is not an error to classify.
	sqlIdempotencyInsert = `
		INSERT INTO ztax.idempotency_record (
			tenant_id, endpoint, idempotency_key, request_digest, state, created_at, expires_at)
		VALUES ($1, $2, $3, $4, 'PENDING', $5, $6)
		ON CONFLICT (tenant_id, endpoint, idempotency_key) DO NOTHING`

	sqlIdempotencyGet = `
		SELECT request_digest, state, response_status, response_body, response_digest, result_ref,
		       created_at, completed_at, expires_at
		FROM   ztax.idempotency_record
		WHERE  tenant_id = $1 AND endpoint = $2 AND idempotency_key = $3`

	sqlIdempotencyComplete = `
		UPDATE ztax.idempotency_record
		SET    state = $4, response_status = $5, response_body = $6, response_digest = $7,
		       result_ref = $8, completed_at = $9
		WHERE  tenant_id = $1 AND endpoint = $2 AND idempotency_key = $3 AND state = 'PENDING'`

	sqlIdempotencyRelease = `
		DELETE FROM ztax.idempotency_record
		WHERE  tenant_id = $1 AND endpoint = $2 AND idempotency_key = $3 AND state = 'PENDING'`

	sqlIdempotencyExpire = `
		DELETE FROM ztax.idempotency_record
		WHERE  tenant_id = $1 AND endpoint = $2 AND idempotency_key = $3 AND expires_at <= $4`
)

// IdempotencyRepo implements port.IdempotencyRepository.
type IdempotencyRepo struct{ s *Store }

// Idempotency returns the idempotency repository.
func (s *Store) Idempotency() *IdempotencyRepo { return &IdempotencyRepo{s: s} }

var _ port.IdempotencyRepository = (*IdempotencyRepo)(nil)

// scope checks the key's tenant against the one in the context.
func scope(ctx context.Context, key idempotency.Key) (id.TenantID, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return id.TenantID{}, err
	}
	if key.TenantID != tenant {
		return id.TenantID{}, errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch,
			"The idempotency key belongs to a tenant outside the caller's scope.")
	}
	return tenant, nil
}

// Insert writes a PENDING record, reporting false when one already exists.
func (r *IdempotencyRepo) Insert(ctx context.Context, rec idempotency.Record) (bool, error) {
	tenant, err := scope(ctx, rec.Key)
	if err != nil {
		return false, err
	}
	if rec.State != idempotency.StatePending {
		return false, errs.New(errs.CategoryInternal, errs.ReasonInternal,
			"An idempotency record is inserted PENDING and settled afterwards.")
	}
	tag, err := r.s.db(ctx).Exec(ctx, sqlIdempotencyInsert,
		tenant.UUID(), rec.Key.Endpoint, rec.Key.Value, rec.RequestDigest.String(), rec.CreatedAt, rec.ExpiresAt)
	if err != nil {
		return false, mapError(err, "insert idempotency record")
	}
	return tag.RowsAffected() == 1, nil
}

// Get reads a record, verifying a settled body against its digest.
func (r *IdempotencyRepo) Get(ctx context.Context, key idempotency.Key) (idempotency.Record, error) {
	tenant, err := scope(ctx, key)
	if err != nil {
		return idempotency.Record{}, err
	}
	var (
		requestDigest, state string
		status               *int32
		body                 []byte
		bodyDigest           *string
		resultRef            *uuid.UUID
		rec                  = idempotency.Record{Key: key}
	)
	err = r.s.db(ctx).QueryRow(ctx, sqlIdempotencyGet, tenant.UUID(), key.Endpoint, key.Value).Scan(
		&requestDigest, &state, &status, &body, &bodyDigest, &resultRef,
		&rec.CreatedAt, &rec.CompletedAt, &rec.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return idempotency.Record{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound,
			"No idempotency record exists for that key.")
	}
	if err != nil {
		return idempotency.Record{}, mapError(err, "read idempotency record")
	}

	if rec.RequestDigest, err = parseStoredDigest(requestDigest, "request_digest"); err != nil {
		return idempotency.Record{}, err
	}
	rec.State = idempotency.State(state)
	rec.CreatedAt, rec.ExpiresAt = rec.CreatedAt.UTC(), rec.ExpiresAt.UTC()
	if rec.CompletedAt != nil {
		at := rec.CompletedAt.UTC()
		rec.CompletedAt = &at
	}
	if resultRef != nil {
		ref := id.NewDecisionID(*resultRef)
		rec.ResultRef = &ref
	}

	if rec.Settled() {
		if status == nil || bodyDigest == nil {
			return idempotency.Record{}, errs.New(errs.CategoryInternal, errs.ReasonEvidenceIntegrity,
				"A settled idempotency record carries no response to replay.")
		}
		want, err := parseStoredDigest(*bodyDigest, "response_digest")
		if err != nil {
			return idempotency.Record{}, err
		}
		// The replay is verbatim, so the bytes are checked before anyone is
		// handed them. A body that no longer matches its digest is not a
		// response; it is an integrity incident.
		if !canonical.SumBytes(body).Equal(want) {
			return idempotency.Record{}, errs.New(errs.CategoryInternal, errs.ReasonEvidenceIntegrity,
				"A stored idempotent response no longer matches its digest.")
		}
		rec.ResponseStatus = int(*status)
		rec.ResponseBody = body
	}
	return rec, nil
}

// Complete settles a PENDING record.
func (r *IdempotencyRepo) Complete(ctx context.Context, rec idempotency.Record) error {
	tenant, err := scope(ctx, rec.Key)
	if err != nil {
		return err
	}
	if !rec.Settled() || rec.CompletedAt == nil || rec.ResponseBody == nil {
		return errs.New(errs.CategoryInternal, errs.ReasonInternal,
			"An idempotency record is completed with a settled state, a response and a completion time.")
	}
	if rec.ResponseStatus < 100 || rec.ResponseStatus > 599 {
		return errs.New(errs.CategoryInternal, errs.ReasonInternal,
			"An idempotency record is completed with an HTTP status.")
	}
	var ref *uuid.UUID
	if rec.ResultRef != nil {
		u := rec.ResultRef.UUID()
		ref = &u
	}
	status := int32(rec.ResponseStatus) // #nosec G115 -- bounded to 100..599 above
	tag, err := r.s.db(ctx).Exec(ctx, sqlIdempotencyComplete,
		tenant.UUID(), rec.Key.Endpoint, rec.Key.Value,
		string(rec.State), status, rec.ResponseBody, canonical.SumBytes(rec.ResponseBody).String(),
		ref, *rec.CompletedAt)
	if err != nil {
		return mapError(err, "complete idempotency record")
	}
	if tag.RowsAffected() != 1 {
		// The record was not PENDING, or not there. Either way the caller
		// executed without holding the key, which the insert exists to make
		// impossible — so this is a defect, and the transaction must not commit.
		return errs.New(errs.CategoryInternal, errs.ReasonInternal,
			"The idempotency record was not PENDING when the request completed.")
	}
	return nil
}

// Release deletes a PENDING record.
func (r *IdempotencyRepo) Release(ctx context.Context, key idempotency.Key) error {
	tenant, err := scope(ctx, key)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlIdempotencyRelease, tenant.UUID(), key.Endpoint, key.Value)
	return mapError(err, "release idempotency record")
}

// Expire deletes an expired record.
func (r *IdempotencyRepo) Expire(ctx context.Context, key idempotency.Key, now time.Time) (bool, error) {
	tenant, err := scope(ctx, key)
	if err != nil {
		return false, err
	}
	tag, err := r.s.db(ctx).Exec(ctx, sqlIdempotencyExpire, tenant.UUID(), key.Endpoint, key.Value, now)
	if err != nil {
		return false, mapError(err, "expire idempotency record")
	}
	return tag.RowsAffected() == 1, nil
}
