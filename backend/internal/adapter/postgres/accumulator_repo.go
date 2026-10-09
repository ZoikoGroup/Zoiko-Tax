package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/accumulator"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// accumulators — ADR-0004
// ---------------------------------------------------------------------------
//
// This file is the only place in the estate that locks an accumulator
// snapshot. ADR-0004 §5.1 control 1 makes a FOR UPDATE on
// accumulator_snapshot anywhere else a build failure, and
// TestNoAccumulatorLockOutsideTheRepository is the gate: it scans every
// non-test Go file and allows the lock clause beside the table name in this
// file and no other.
//
// The serialization point is a row lock under READ COMMITTED, not SERIALIZABLE
// (§3.1) and not an advisory lock (§4.2): the lock is on the row it protects,
// it is released by the transaction's outcome rather than by a session's
// lifetime, and the wait for it is visible to pg_locks while it happens.

// DefaultAccumulatorLockTimeout bounds the wait for one accumulator lock.
//
// It sits below the connection-level backstop in DefaultConfig (3s) so that a
// pathological key is reported by the statement that is actually waiting on
// it, rather than by whichever statement next runs into the connection
// setting. W2's load test (ADR-0004 control 5) is what should set it properly;
// until then it is a ceiling chosen to fail before a client gives up.
const DefaultAccumulatorLockTimeout = 2 * time.Second

const (
	// lock_timeout, set explicitly for the rest of the transaction
	// (ADR-0004 control 4). set_config with is_local = true is SET LOCAL in a
	// form that takes a bind parameter, so the value is never spliced into
	// SQL text. It is set on every LockAll rather than trusted from the
	// connection, because a connection-level setting is configuration that
	// can drift, and this is the one wait in the commit path that is a
	// designed serialization point.
	sqlAccumulatorLockTimeout = `SELECT set_config('lock_timeout', $1, true)`

	// A snapshot must exist before it can be locked. Creating it is part of
	// acquiring it: ON CONFLICT DO NOTHING waits for a concurrent creator of
	// the same key to finish rather than racing it, so two first commits on a
	// new key serialize on the insert and then on the lock, in the same order
	// as every later commit. updated_at is NULL because nothing has
	// contributed; it is never now() (ADR-0003 §2.5).
	sqlAccumulatorEnsure = `
		INSERT INTO ztax.accumulator_snapshot
			(tenant_id, accumulator_key, running_total, currency, last_seq, crossed_thresholds, updated_at)
		VALUES ($1, $2, 0, $3, 0, '[]'::jsonb, NULL)
		ON CONFLICT (tenant_id, accumulator_key) DO NOTHING`

	sqlAccumulatorSnapshotColumns = `running_total, currency, last_seq, crossed_thresholds, updated_at`

	// The serialization point of ADR-0004 §2.2. One key per statement, issued
	// in accumulator.LockOrder order by LockAll, so the order locks are taken
	// in is decided by Go and nowhere else. A single statement over several
	// keys would lock in whatever order the executor produced rows — a plan
	// choice, not a property anybody reviewed.
	sqlAccumulatorLock = `SELECT ` + sqlAccumulatorSnapshotColumns + `
		FROM   ztax.accumulator_snapshot
		WHERE  tenant_id = $1 AND accumulator_key = $2
		FOR UPDATE`

	// The quote read of §2.7: same row, no lock, READ COMMITTED.
	sqlAccumulatorReadUnlocked = `SELECT ` + sqlAccumulatorSnapshotColumns + `
		FROM   ztax.accumulator_snapshot
		WHERE  tenant_id = $1 AND accumulator_key = $2`

	// Replaces a locked snapshot. currency is a predicate, not an assignment —
	// the column is outside the UPDATE grant of migration 000007 — and
	// last_seq <= $4 refuses to move a snapshot backwards, which no correct
	// caller does and a caller that skipped the lock might.
	sqlAccumulatorSave = `
		UPDATE ztax.accumulator_snapshot
		SET    running_total = $3, last_seq = $4, crossed_thresholds = $5, updated_at = $6
		WHERE  tenant_id = $1 AND accumulator_key = $2 AND currency = $7 AND last_seq <= $4`

	// ADR-0004 §2.4. The conflict target is named, so that only the
	// once-per-decision constraint means "already applied". A collision on
	// the primary key — two contributions claiming one seq — is not a retry;
	// it is two writers that both believed they held the lock, and it raises.
	//
	// ON CONFLICT rather than catching the unique violation, because in
	// PostgreSQL a failed statement aborts the transaction, and the caller
	// needs that transaction to read the existing row and return the original
	// result. The constraint is still what decides: DO NOTHING is the
	// constraint's verdict, delivered without the abort.
	sqlContributionInsert = `
		INSERT INTO ztax.contribution_event
			(tenant_id, accumulator_key, seq, source_decision_id, amount, currency, event_time, recorded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT ON CONSTRAINT contribution_event_once_per_decision DO NOTHING`

	sqlContributionColumns = `seq, source_decision_id, amount, currency, event_time, recorded_at`

	sqlContributionByDecision = `SELECT ` + sqlContributionColumns + `
		FROM   ztax.contribution_event
		WHERE  tenant_id = $1 AND accumulator_key = $2 AND source_decision_id = $3`

	sqlContributionLog = `SELECT ` + sqlContributionColumns + `
		FROM   ztax.contribution_event
		WHERE  tenant_id = $1 AND accumulator_key = $2
		ORDER  BY seq`

	sqlCrossingInsert = `
		INSERT INTO ztax.threshold_crossing
			(tenant_id, accumulator_key, threshold_id, crossed_at_seq, source_decision_id,
			 running_total, currency, recorded_at, threshold_amount, comparison)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	// The crossing's "before" total is not stored: it is the "after" total
	// less the contribution that crossed, and the log already holds that
	// amount. Reading it back from the log rather than from a second column
	// means the two cannot disagree.
	sqlCrossingLog = `
		SELECT tc.threshold_id, tc.threshold_amount, tc.comparison, tc.crossed_at_seq,
		       tc.source_decision_id, tc.running_total, tc.currency, tc.recorded_at, ce.amount
		FROM   ztax.threshold_crossing tc
		JOIN   ztax.contribution_event ce
		  ON   ce.tenant_id = tc.tenant_id AND ce.accumulator_key = tc.accumulator_key AND ce.seq = tc.crossed_at_seq
		WHERE  tc.tenant_id = $1 AND tc.accumulator_key = $2
		ORDER  BY tc.crossed_at_seq, tc.threshold_id`
)

// constraintContributionSeq is the primary key on contribution_event.
const constraintContributionSeq = "contribution_event_pkey"

// AccumulatorRepo implements port.AccumulatorRepository.
type AccumulatorRepo struct {
	s           *Store
	lockTimeout time.Duration
}

// Accumulators returns the accumulator repository.
func (s *Store) Accumulators() *AccumulatorRepo {
	return &AccumulatorRepo{s: s, lockTimeout: DefaultAccumulatorLockTimeout}
}

var _ port.AccumulatorRepository = (*AccumulatorRepo)(nil)

// WithLockTimeout returns a copy that waits at most d for each accumulator
// lock. A non-positive d is replaced by the default: lock_timeout = 0 means
// "wait forever" in PostgreSQL, which is the hang control 4 exists to rule
// out.
func (r *AccumulatorRepo) WithLockTimeout(d time.Duration) *AccumulatorRepo {
	if d <= 0 {
		d = DefaultAccumulatorLockTimeout
	}
	c := *r
	c.lockTimeout = d
	return &c
}

// LockAll locks each named accumulator, in canonical order, creating any that
// do not exist yet.
func (r *AccumulatorRepo) LockAll(ctx context.Context, refs []accumulator.Ref) ([]accumulator.Snapshot, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	if !inTx(ctx) {
		// Outside a transaction each lock would be released at the end of its
		// own statement, and the caller would apply contributions to totals
		// another commit is free to change underneath it.
		return nil, fmt.Errorf("postgres: AccumulatorRepo.LockAll called outside a transaction")
	}

	// Resolve the currency each key was asked for before any lock is taken,
	// so a malformed request fails holding nothing.
	currencies := make(map[accumulator.Key]fiscal.Currency, len(refs))
	keys := make([]accumulator.Key, 0, len(refs))
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return nil, err
		}
		if prior, seen := currencies[ref.Key]; seen && prior != ref.Currency {
			return nil, errs.Invalid("currency", errs.ReasonCurrencyMismatch,
				fmt.Sprintf("Accumulator %s was named in both %s and %s.", ref.Key, prior, ref.Currency))
		}
		currencies[ref.Key] = ref.Currency
		keys = append(keys, ref.Key)
	}

	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlAccumulatorLockTimeout, millis(r.lockTimeout)); err != nil {
		return nil, mapError(err, "set accumulator lock timeout")
	}

	// Every acquisition goes through LockOrder (ADR-0004 §2.3). Ensure and
	// lock are interleaved per key rather than batched — creating key b
	// before waiting for key a would hold b out of order.
	ordered := accumulator.LockOrder(keys)
	out := make([]accumulator.Snapshot, 0, len(ordered))
	for _, key := range ordered {
		currency := currencies[key]
		if _, err := db.Exec(ctx, sqlAccumulatorEnsure, tenant.UUID(), key.String(), string(currency)); err != nil {
			return nil, mapError(err, "create accumulator snapshot")
		}
		snap, err := scanSnapshot(key, db.QueryRow(ctx, sqlAccumulatorLock, tenant.UUID(), key.String()))
		if err != nil {
			return nil, err
		}
		if got := snap.Total.Currency(); got != currency {
			return nil, errs.Invalid("currency", errs.ReasonCurrencyMismatch,
				fmt.Sprintf("Accumulator %s is denominated in %s, not %s.", key, got, currency))
		}
		out = append(out, snap)
	}
	return out, nil
}

// AppendContribution writes a contribution, reporting false if the decision
// already contributed to the key.
func (r *AccumulatorRepo) AppendContribution(ctx context.Context, e accumulator.Event) (bool, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	if err := e.Validate(); err != nil {
		return false, err
	}
	if e.Seq < 1 {
		return false, fmt.Errorf("postgres: contribution to %s has seq %d; the log starts at 1", e.Key, e.Seq)
	}
	amount, err := MoneyValue(e.Amount)
	if err != nil {
		return false, err
	}
	tag, err := r.s.db(ctx).Exec(ctx, sqlContributionInsert,
		tenant.UUID(), e.Key.String(), e.Seq, e.SourceDecisionID.UUID(),
		amount, string(e.Amount.Currency()), e.EventTime, e.RecordedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == codeUniqueViolation && pgErr.ConstraintName == constraintContributionSeq {
			return false, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal,
				"Two contributions claimed one position in an accumulator log; the accumulator lock was not held.")
		}
		return false, mapError(err, "insert contribution")
	}
	return tag.RowsAffected() == 1, nil
}

// Contribution reads the contribution a decision made to a key.
func (r *AccumulatorRepo) Contribution(ctx context.Context, key accumulator.Key, decisionID id.DecisionID) (accumulator.Event, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return accumulator.Event{}, err
	}
	return scanContribution(key, r.s.db(ctx).QueryRow(ctx, sqlContributionByDecision,
		tenant.UUID(), key.String(), decisionID.UUID()))
}

// SaveSnapshot replaces a locked snapshot.
func (r *AccumulatorRepo) SaveSnapshot(ctx context.Context, snap accumulator.Snapshot) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if err := snap.Validate(); err != nil {
		return err
	}
	total, err := MoneyValue(snap.Total)
	if err != nil {
		return err
	}
	crossed, err := json.Marshal(snap.Crossed)
	if err != nil {
		return fmt.Errorf("postgres: encode crossed thresholds: %w", err)
	}
	var updatedAt *time.Time
	if !snap.UpdatedAt.IsZero() {
		updatedAt = &snap.UpdatedAt
	}
	tag, err := r.s.db(ctx).Exec(ctx, sqlAccumulatorSave,
		tenant.UUID(), snap.Key.String(), total, snap.LastSeq, crossed, updatedAt, string(snap.Total.Currency()))
	if err != nil {
		return mapError(err, "save accumulator snapshot")
	}
	if tag.RowsAffected() != 1 {
		// No row, a different currency, or a stored last_seq ahead of this
		// one. Each means the snapshot was not the one LockAll returned, and
		// writing anyway would lose a contribution from the total.
		return errs.New(errs.CategoryInternal, errs.ReasonInternal,
			fmt.Sprintf("Accumulator %s could not be saved: the stored snapshot is missing, in another currency, or ahead of seq %d.",
				snap.Key, snap.LastSeq))
	}
	return nil
}

// AppendCrossing records a threshold crossing.
func (r *AccumulatorRepo) AppendCrossing(ctx context.Context, c accumulator.Crossing) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if err := c.Key.Validate(); err != nil {
		return err
	}
	if err := c.Threshold.Validate(); err != nil {
		return err
	}
	if c.Seq < 1 || c.SourceDecisionID.IsZero() || c.RecordedAt.IsZero() {
		return fmt.Errorf("postgres: crossing of %s on %s does not name the contribution that caused it", c.Threshold.ID, c.Key)
	}
	after, err := MoneyValue(c.After)
	if err != nil {
		return err
	}
	limit, err := MoneyValue(c.Threshold.Limit)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlCrossingInsert,
		tenant.UUID(), c.Key.String(), string(c.Threshold.ID), c.Seq, c.SourceDecisionID.UUID(),
		after, string(c.After.Currency()), c.RecordedAt, limit, string(c.Threshold.Comparison))
	return mapError(err, "insert threshold crossing")
}

// Events reads the whole log for a key, in sequence order.
func (r *AccumulatorRepo) Events(ctx context.Context, key accumulator.Key) ([]accumulator.Event, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlContributionLog, tenant.UUID(), key.String())
	if err != nil {
		return nil, mapError(err, "read contribution log")
	}
	defer rows.Close()
	var out []accumulator.Event
	for rows.Next() {
		e, err := scanContribution(key, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, mapError(rows.Err(), "read contribution log")
}

// Crossings reads every recorded crossing for a key, in sequence order.
func (r *AccumulatorRepo) Crossings(ctx context.Context, key accumulator.Key) ([]accumulator.Crossing, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlCrossingLog, tenant.UUID(), key.String())
	if err != nil {
		return nil, mapError(err, "read threshold crossings")
	}
	defer rows.Close()
	var out []accumulator.Crossing
	for rows.Next() {
		var (
			thresholdID, comparison, currency string
			limit, after, amount              Numeric
			decisionUUID                      uuid.UUID
			c                                 = accumulator.Crossing{Key: key}
		)
		if err := rows.Scan(&thresholdID, &limit, &comparison, &c.Seq,
			&decisionUUID, &after, &currency, &c.RecordedAt, &amount); err != nil {
			return nil, mapError(err, "scan threshold crossing")
		}
		cur := fiscal.Currency(currency)
		if c.Threshold.Limit, err = limit.Money(cur); err != nil {
			return nil, err
		}
		if c.After, err = after.Money(cur); err != nil {
			return nil, err
		}
		contributed, err := amount.Money(cur)
		if err != nil {
			return nil, err
		}
		if c.Before, err = c.After.Sub(contributed); err != nil {
			return nil, err
		}
		c.Threshold.ID = accumulator.ThresholdID(thresholdID)
		c.Threshold.Comparison = accumulator.Comparison(comparison)
		c.SourceDecisionID = id.NewDecisionID(decisionUUID)
		c.RecordedAt = c.RecordedAt.UTC()
		out = append(out, c)
	}
	return out, mapError(rows.Err(), "read threshold crossings")
}

// ReadUnlocked reads a snapshot without the lock, for a quote.
func (r *AccumulatorRepo) ReadUnlocked(ctx context.Context, ref accumulator.Ref) (accumulator.Observation, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return accumulator.Observation{}, err
	}
	if err := ref.Validate(); err != nil {
		return accumulator.Observation{}, err
	}
	snap, err := scanSnapshot(ref.Key, r.s.db(ctx).QueryRow(ctx, sqlAccumulatorReadUnlocked, tenant.UUID(), ref.Key.String()))
	if errs.IsCategory(err, errs.CategoryNotFound) {
		// No commit has created the row yet, so nothing has contributed. A
		// quote does not create it: a read path that wrote would turn the
		// quote's hot path into a writer for no legal benefit (§2.7).
		if snap, err = accumulator.Empty(ref); err != nil {
			return accumulator.Observation{}, err
		}
	} else if err != nil {
		return accumulator.Observation{}, err
	}
	if got := snap.Total.Currency(); got != ref.Currency {
		return accumulator.Observation{}, errs.Invalid("currency", errs.ReasonCurrencyMismatch,
			fmt.Sprintf("Accumulator %s is denominated in %s, not %s.", ref.Key, got, ref.Currency))
	}
	return accumulator.Observation{Key: snap.Key, Total: snap.Total, ObservedSeq: snap.LastSeq}, nil
}

func scanSnapshot(key accumulator.Key, row scanner) (accumulator.Snapshot, error) {
	var (
		total     Numeric
		currency  string
		crossed   []byte
		updatedAt *time.Time
		snap      = accumulator.Snapshot{Key: key}
	)
	if err := row.Scan(&total, &currency, &snap.LastSeq, &crossed, &updatedAt); err != nil {
		return accumulator.Snapshot{}, mapError(err, "scan accumulator snapshot")
	}
	var err error
	if snap.Total, err = total.Money(fiscal.Currency(currency)); err != nil {
		return accumulator.Snapshot{}, err
	}
	if err := json.Unmarshal(crossed, &snap.Crossed); err != nil {
		return accumulator.Snapshot{}, errs.Wrap(fmt.Errorf("postgres: crossed_thresholds: %w", err),
			errs.CategoryInternal, errs.ReasonInternal, "A stored accumulator snapshot is malformed.")
	}
	if snap.Crossed == nil {
		snap.Crossed = []accumulator.ThresholdID{}
	}
	if updatedAt != nil {
		snap.UpdatedAt = updatedAt.UTC()
	}
	// The snapshot is about to become the base of a fiscal commit. One that
	// contradicts itself is refused here rather than built on, and the remedy
	// is a rebuild from the log (ADR-0004 §2.5).
	if err := snap.Validate(); err != nil {
		return accumulator.Snapshot{}, err
	}
	return snap, nil
}

func scanContribution(key accumulator.Key, row scanner) (accumulator.Event, error) {
	var (
		amount       Numeric
		currency     string
		decisionUUID uuid.UUID
		e            = accumulator.Event{Contribution: accumulator.Contribution{Key: key}}
	)
	if err := row.Scan(&e.Seq, &decisionUUID, &amount, &currency, &e.EventTime, &e.RecordedAt); err != nil {
		return accumulator.Event{}, mapError(err, "scan contribution")
	}
	var err error
	if e.Amount, err = amount.Money(fiscal.Currency(currency)); err != nil {
		return accumulator.Event{}, err
	}
	e.SourceDecisionID = id.NewDecisionID(decisionUUID)
	// pgx hands timestamptz back in the process's local zone; the estate is
	// UTC throughout (ADR-0011 P2).
	e.EventTime, e.RecordedAt = e.EventTime.UTC(), e.RecordedAt.UTC()
	return e, nil
}
