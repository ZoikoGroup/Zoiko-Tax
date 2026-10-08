package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// tax_decision — ADR-0003
// ---------------------------------------------------------------------------
//
// Every statement here is an INSERT or a SELECT. The application role holds no
// UPDATE or DELETE on this table (000001's default grant), so the append-only
// rule of ADR-0003 §2.1 is the database's rather than this file's — this file
// merely has no way to ask for anything else.

const (
	sqlDecisionInsert = `
		INSERT INTO ztax.tax_decision (
			tenant_id, decision_id, business_key, valid_from, valid_to, recorded_at, supersedes_id,
			event_time, outcome, reason_code,
			bundle_id, bundle_digest, ir_version, canon_profile,
			train_app, train_content, train_ai, train_adapter, train_infra, train_schema, train_migration,
			input_digest, input_canonical, trace, envelope_digest, result_digest)
		VALUES ($1, $2, $3, $4, NULL, $5, $6, $7, $8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)`

	sqlDecisionColumns = `
		tenant_id, decision_id, business_key, valid_from, recorded_at, supersedes_id,
		event_time, outcome, reason_code,
		bundle_id, bundle_digest, ir_version, canon_profile,
		train_app, train_content, train_ai, train_adapter, train_infra, train_schema, train_migration,
		input_digest, envelope_digest, result_digest`

	sqlDecisionByID = `SELECT ` + sqlDecisionColumns + `
		FROM ztax.tax_decision WHERE tenant_id = $1 AND decision_id = $2`

	// ADR-0003 §2.3, verbatim in shape: the version current at decision time,
	// for an event at event time, found by ordering rather than by a closed
	// range. There is no recorded_to column to close, so nothing is ever
	// updated to make history readable.
	sqlDecisionAsOf = `SELECT DISTINCT ON (business_key) ` + sqlDecisionColumns + `
		FROM   ztax.tax_decision
		WHERE  tenant_id    = $1
		  AND  business_key = $2
		  AND  recorded_at <= $3
		  AND  valid_from  <= $4
		  AND  (valid_to IS NULL OR valid_to > $4)
		ORDER  BY business_key, recorded_at DESC, decision_id DESC`

	sqlDecisionHistory = `SELECT ` + sqlDecisionColumns + `
		FROM ztax.tax_decision WHERE tenant_id = $1 AND business_key = $2
		ORDER BY recorded_at, decision_id`

	// The current versions — those nothing supersedes — of the decisions whose
	// event fell in [from, to): the population a reconciliation compares.
	sqlDecisionCurrentInWindow = `SELECT ` + sqlDecisionColumns + `
		FROM ztax.tax_decision d
		WHERE d.tenant_id = $1 AND d.event_time >= $2 AND d.event_time < $3
		  AND NOT EXISTS (SELECT 1 FROM ztax.tax_decision s
		                  WHERE s.tenant_id = d.tenant_id AND s.supersedes_id = d.decision_id)
		ORDER BY d.event_time, d.decision_id`

	// The seal's leaves, in evidence.LeafOrder. The half-open interval is the
	// period's own: a decision recorded exactly at the boundary belongs to the
	// period that starts there, never to both.
	sqlDecisionSealLeaves = `
		SELECT decision_id, recorded_at, result_digest
		FROM   ztax.tax_decision
		WHERE  tenant_id = $1 AND recorded_at >= $2 AND recorded_at < $3
		ORDER  BY recorded_at, decision_id`
)

// DecisionRepo implements port.DecisionRepository.
type DecisionRepo struct{ s *Store }

// Decisions returns the decision repository.
func (s *Store) Decisions() *DecisionRepo { return &DecisionRepo{s: s} }

var _ port.DecisionRepository = (*DecisionRepo)(nil)

// Append inserts a decision's index row.
func (r *DecisionRepo) Append(ctx context.Context, rec evidence.Record) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if rec.TenantID != tenant {
		// The record was built for a different tenant from the one in scope.
		// Writing it under either would be wrong, so neither happens.
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch,
			"The decision belongs to a tenant outside the caller's scope.")
	}
	var supersedes *uuid.UUID
	if rec.Supersedes != nil {
		u := rec.Supersedes.UUID()
		supersedes = &u
	}
	t := rec.Trains
	_, err = r.s.db(ctx).Exec(ctx, sqlDecisionInsert,
		tenant.UUID(), rec.DecisionID.UUID(), rec.BusinessKey, rec.ValidFrom, rec.RecordedAt, supersedes,
		rec.EventTime, string(rec.Outcome), string(rec.Reason),
		rec.BundleID, rec.BundleDigest, rec.IRVersion, rec.CanonProfile,
		t.App, t.Content, t.AI, t.Adapter, t.Infra, t.Schema, t.Migration,
		rec.InputDigest.String(), rec.InputCanonical, rec.TraceCanonical,
		rec.EnvelopeDigest.String(), rec.ResultDigest.String())
	return mapError(err, "insert decision")
}

// ByID reads one decision's index row.
func (r *DecisionRepo) ByID(ctx context.Context, decisionID id.DecisionID) (evidence.Record, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return evidence.Record{}, err
	}
	return scanDecision(r.s.db(ctx).QueryRow(ctx, sqlDecisionByID, tenant.UUID(), decisionID.UUID()))
}

// AsOf reads the version of a business key current at decisionTime for an
// event at eventTime.
func (r *DecisionRepo) AsOf(ctx context.Context, businessKey string, decisionTime, eventTime time.Time) (evidence.Record, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return evidence.Record{}, err
	}
	return scanDecision(r.s.db(ctx).QueryRow(ctx, sqlDecisionAsOf, tenant.UUID(), businessKey, decisionTime, eventTime))
}

// History reads every version of a business key, oldest first.
func (r *DecisionRepo) History(ctx context.Context, businessKey string) ([]evidence.Record, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlDecisionHistory, tenant.UUID(), businessKey)
	if err != nil {
		return nil, mapError(err, "read decision history")
	}
	defer rows.Close()
	var out []evidence.Record
	for rows.Next() {
		rec, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, mapError(rows.Err(), "read decision history")
}

// CurrentInWindow returns the current decisions whose event fell in
// [from, to).
func (r *DecisionRepo) CurrentInWindow(ctx context.Context, from, to time.Time) ([]evidence.Record, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlDecisionCurrentInWindow, tenant.UUID(), from.UTC(), to.UTC())
	if err != nil {
		return nil, mapError(err, "read current decisions")
	}
	defer rows.Close()
	var out []evidence.Record
	for rows.Next() {
		rec, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, mapError(rows.Err(), "read current decisions")
}

// SealLeaves reads the leaves for a period.
func (r *DecisionRepo) SealLeaves(ctx context.Context, from, to time.Time) ([]evidence.SealLeaf, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlDecisionSealLeaves, tenant.UUID(), from, to)
	if err != nil {
		return nil, mapError(err, "read seal leaves")
	}
	defer rows.Close()
	var out []evidence.SealLeaf
	for rows.Next() {
		var (
			decisionUUID uuid.UUID
			recordedAt   time.Time
			result       string
		)
		if err := rows.Scan(&decisionUUID, &recordedAt, &result); err != nil {
			return nil, mapError(err, "scan seal leaf")
		}
		digest, err := parseStoredDigest(result, "result_digest")
		if err != nil {
			return nil, err
		}
		out = append(out, evidence.SealLeaf{
			DecisionID:   id.NewDecisionID(decisionUUID),
			RecordedAt:   recordedAt.UTC(),
			ResultDigest: digest,
		})
	}
	return out, mapError(rows.Err(), "read seal leaves")
}

func scanDecision(row scanner) (evidence.Record, error) {
	var (
		tenantUUID, decisionUUID uuid.UUID
		supersedes               *uuid.UUID
		outcome                  string
		reason                   *string
		input, envelope, result  string
		rec                      evidence.Record
		t                        = &rec.Trains
	)
	err := row.Scan(
		&tenantUUID, &decisionUUID, &rec.BusinessKey, &rec.ValidFrom, &rec.RecordedAt, &supersedes,
		&rec.EventTime, &outcome, &reason,
		&rec.BundleID, &rec.BundleDigest, &rec.IRVersion, &rec.CanonProfile,
		&t.App, &t.Content, &t.AI, &t.Adapter, &t.Infra, &t.Schema, &t.Migration,
		&input, &envelope, &result)
	if err != nil {
		return evidence.Record{}, mapError(err, "scan decision")
	}
	rec.TenantID = id.NewTenantID(tenantUUID)
	rec.DecisionID = id.NewDecisionID(decisionUUID)
	if supersedes != nil {
		s := id.NewDecisionID(*supersedes)
		rec.Supersedes = &s
	}
	rec.Outcome = evidence.Outcome(outcome)
	if reason != nil {
		rec.Reason = errs.ReasonCode(*reason)
	}
	// pgx hands timestamptz back in the process's local zone. Evidence is UTC
	// throughout (ADR-0011 P2), so it is normalised here once.
	rec.ValidFrom, rec.RecordedAt, rec.EventTime = rec.ValidFrom.UTC(), rec.RecordedAt.UTC(), rec.EventTime.UTC()

	for _, d := range []struct {
		raw, column string
		dst         *canonical.Digest
	}{
		{input, "input_digest", &rec.InputDigest},
		{envelope, "envelope_digest", &rec.EnvelopeDigest},
		{result, "result_digest", &rec.ResultDigest},
	} {
		parsed, err := parseStoredDigest(d.raw, d.column)
		if err != nil {
			return evidence.Record{}, err
		}
		*d.dst = parsed
	}
	return rec, nil
}

// parseStoredDigest reads a digest column. The CHECK constraint guarantees the
// prefix, not the length or the hex; a column that fails here was written by
// something other than this code.
func parseStoredDigest(raw, column string) (canonical.Digest, error) {
	d, err := canonical.ParseDigest(raw)
	if err != nil {
		return canonical.Digest{}, errs.Wrap(fmt.Errorf("postgres: %s: %w", column, err),
			errs.CategoryInternal, errs.ReasonEvidenceIntegrity,
			"A stored digest is malformed.")
	}
	return d, nil
}

// ---------------------------------------------------------------------------
// evidence_period_seal — ADR-0011 §2.5
// ---------------------------------------------------------------------------

const (
	sqlSealColumns = `tenant_id, seal_id, cell, period_start, period_end, leaf_count,
		merkle_root, seal_object_digest, key_id, sealed_at`

	sqlSealInsert = `INSERT INTO ztax.evidence_period_seal (` + sqlSealColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	sqlSealByID = `SELECT ` + sqlSealColumns + `
		FROM ztax.evidence_period_seal WHERE tenant_id = $1 AND seal_id = $2`

	sqlSealList = `SELECT ` + sqlSealColumns + `
		FROM ztax.evidence_period_seal WHERE tenant_id = $1 ORDER BY period_start DESC LIMIT $2`

	sqlSealOverlapping = `SELECT ` + sqlSealColumns + `
		FROM   ztax.evidence_period_seal
		WHERE  tenant_id = $1 AND period_start < $3 AND period_end > $2
		ORDER  BY period_start`

	// A transaction-scoped advisory lock keyed on the tenant. It is released
	// at commit or rollback, so it cannot be leaked by a crashed sealer.
	sqlSealLock = `SELECT pg_advisory_xact_lock(hashtextextended('ztax.evidence_period_seal:' || $1::text, 0))`
)

// SealRepo implements port.SealRepository.
type SealRepo struct{ s *Store }

// Seals returns the seal repository.
func (s *Store) Seals() *SealRepo { return &SealRepo{s: s} }

var _ port.SealRepository = (*SealRepo)(nil)

// LockSealing takes the per-tenant sealing lock for the enclosing transaction.
func (r *SealRepo) LockSealing(ctx context.Context) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if !inTx(ctx) {
		// Outside a transaction the lock would be released at the end of
		// this one statement, and the caller would believe it held a lock it
		// does not.
		return fmt.Errorf("postgres: LockSealing called outside a transaction")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlSealLock, tenant.String())
	return mapError(err, "lock sealing")
}

// Overlapping reports seals whose period intersects [from, to).
func (r *SealRepo) Overlapping(ctx context.Context, from, to time.Time) ([]evidence.SealRecord, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlSealOverlapping, tenant.UUID(), from, to)
	if err != nil {
		return nil, mapError(err, "read overlapping seals")
	}
	defer rows.Close()
	var out []evidence.SealRecord
	for rows.Next() {
		rec, err := scanSeal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, mapError(rows.Err(), "read overlapping seals")
}

// Append inserts a seal's index row.
func (r *SealRepo) Append(ctx context.Context, rec evidence.SealRecord) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if rec.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch,
			"The seal belongs to a tenant outside the caller's scope.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlSealInsert,
		tenant.UUID(), rec.SealID.UUID(), rec.Cell, rec.PeriodStart, rec.PeriodEnd, rec.LeafCount,
		rec.MerkleRoot.String(), rec.SealObjectDigest.String(), rec.KeyID, rec.SealedAt)
	return mapError(err, "insert seal")
}

// ByID reads one seal's index row.
func (r *SealRepo) ByID(ctx context.Context, sealID id.SealID) (evidence.SealRecord, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return evidence.SealRecord{}, err
	}
	return scanSeal(r.s.db(ctx).QueryRow(ctx, sqlSealByID, tenant.UUID(), sealID.UUID()))
}

// List returns the tenant's seals, latest period first.
func (r *SealRepo) List(ctx context.Context, limit int) ([]evidence.SealRecord, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlSealList, tenant.UUID(), limit)
	if err != nil {
		return nil, mapError(err, "list seals")
	}
	defer rows.Close()
	var out []evidence.SealRecord
	for rows.Next() {
		rec, err := scanSeal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, mapError(rows.Err(), "list seals")
}

func scanSeal(row scanner) (evidence.SealRecord, error) {
	var (
		tenantUUID, sealUUID uuid.UUID
		root, object         string
		rec                  evidence.SealRecord
	)
	if err := row.Scan(&tenantUUID, &sealUUID, &rec.Cell, &rec.PeriodStart, &rec.PeriodEnd, &rec.LeafCount,
		&root, &object, &rec.KeyID, &rec.SealedAt); err != nil {
		return evidence.SealRecord{}, mapError(err, "scan seal")
	}
	rec.TenantID = id.NewTenantID(tenantUUID)
	rec.SealID = id.NewSealID(sealUUID)
	rec.PeriodStart, rec.PeriodEnd, rec.SealedAt = rec.PeriodStart.UTC(), rec.PeriodEnd.UTC(), rec.SealedAt.UTC()
	var err error
	if rec.MerkleRoot, err = parseStoredDigest(root, "merkle_root"); err != nil {
		return evidence.SealRecord{}, err
	}
	if rec.SealObjectDigest, err = parseStoredDigest(object, "seal_object_digest"); err != nil {
		return evidence.SealRecord{}, err
	}
	return rec, nil
}
