package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/batch"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// batch_job, batch_item, batch_item_result — migration 000015
// ---------------------------------------------------------------------------

const (
	sqlJobColumns = `tenant_id, job_id, operation, status, item_count, requested_at, requested_by,
		started_at, finished_at, lease_until`
	sqlJobInsert = `INSERT INTO ztax.batch_job (` + sqlJobColumns + `)
		VALUES ($1, $2, $3, 'QUEUED', $4, $5, $6, NULL, NULL, NULL)`
	sqlItemInsert = `INSERT INTO ztax.batch_item (tenant_id, job_id, item_index, business_key, request)
		VALUES ($1, $2, $3, $4, $5)`
	sqlJobByID = `SELECT ` + sqlJobColumns + ` FROM ztax.batch_job WHERE tenant_id = $1 AND job_id = $2`
	sqlJobKeys = `SELECT item_index, business_key FROM ztax.batch_item
		WHERE tenant_id = $1 AND job_id = $2 ORDER BY item_index`
	sqlJobResults = `SELECT item_index, status, decision_id, reason_code, recorded_at FROM ztax.batch_item_result
		WHERE tenant_id = $1 AND job_id = $2 ORDER BY item_index`
	// The oldest job nobody holds, leased in the statement that finds it.
	sqlJobClaim = `UPDATE ztax.batch_job j
		SET status = 'RUNNING', lease_until = $2, started_at = COALESCE(j.started_at, $1)
		FROM (SELECT tenant_id, job_id FROM ztax.batch_job
		      WHERE status = 'QUEUED' OR (status = 'RUNNING' AND lease_until < $1)
		      ORDER BY requested_at LIMIT 1 FOR UPDATE SKIP LOCKED) next
		WHERE j.tenant_id = next.tenant_id AND j.job_id = next.job_id
		RETURNING j.tenant_id, j.job_id, j.operation, j.status, j.item_count, j.requested_at, j.requested_by,
		j.started_at, j.finished_at, j.lease_until`
	sqlJobRenew = `UPDATE ztax.batch_job SET lease_until = $3
		WHERE tenant_id = $1 AND job_id = $2 AND status = 'RUNNING'`
	sqlJobPending = `SELECT i.item_index, i.business_key, i.request FROM ztax.batch_item i
		WHERE i.tenant_id = $1 AND i.job_id = $2
		  AND NOT EXISTS (SELECT 1 FROM ztax.batch_item_result r
		                  WHERE r.tenant_id = i.tenant_id AND r.job_id = i.job_id AND r.item_index = i.item_index)
		ORDER BY i.item_index`
	sqlResultInsert = `INSERT INTO ztax.batch_item_result
		(tenant_id, job_id, item_index, status, decision_id, reason_code, recorded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant_id, job_id, item_index) DO NOTHING`
	sqlJobComplete = `UPDATE ztax.batch_job SET status = 'COMPLETED', finished_at = $3, lease_until = NULL
		WHERE tenant_id = $1 AND job_id = $2 AND status = 'RUNNING'`
)

// BatchRepo implements port.BatchRepository.
type BatchRepo struct{ s *Store }

// Batches returns the batch repository.
func (s *Store) Batches() *BatchRepo { return &BatchRepo{s: s} }

var _ port.BatchRepository = (*BatchRepo)(nil)

// Create writes the job and its items in the caller's transaction.
func (r *BatchRepo) Create(ctx context.Context, j batch.Job, items []batch.Item) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if j.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The job belongs to a tenant outside the caller's scope.")
	}
	if len(items) != j.ItemCount || len(items) == 0 || len(items) > batch.MaxItems {
		return batchFault(nil, "A job's item count is its items.")
	}
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlJobInsert, tenant.UUID(), j.ID.UUID(), string(j.Operation), j.ItemCount,
		j.RequestedAt.UTC(), optUser(j.RequestedBy)); err != nil {
		return mapError(err, "insert batch job")
	}
	for i, it := range items {
		if it.Index != i || it.Job != j.ID {
			return batchFault(nil, "Batch items are numbered from zero in order.")
		}
		if _, err := db.Exec(ctx, sqlItemInsert, tenant.UUID(), j.ID.UUID(), it.Index, it.BusinessKey, it.Request); err != nil {
			return mapError(err, "insert batch item")
		}
	}
	return nil
}

// Job returns a job with its items' keys and its results.
func (r *BatchRepo) Job(ctx context.Context, jobID id.JobID) (batch.Job, []batch.Item, []batch.Result, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return batch.Job{}, nil, nil, err
	}
	db := r.s.db(ctx)
	j, err := scanJob(db.QueryRow(ctx, sqlJobByID, tenant.UUID(), jobID.UUID()))
	if err != nil {
		return batch.Job{}, nil, nil, err
	}
	rows, err := db.Query(ctx, sqlJobKeys, tenant.UUID(), jobID.UUID())
	if err != nil {
		return batch.Job{}, nil, nil, mapError(err, "read batch items")
	}
	var items []batch.Item
	for rows.Next() {
		it := batch.Item{Job: jobID}
		if err := rows.Scan(&it.Index, &it.BusinessKey); err != nil {
			rows.Close()
			return batch.Job{}, nil, nil, mapError(err, "scan batch item")
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return batch.Job{}, nil, nil, mapError(err, "read batch items")
	}

	rows, err = db.Query(ctx, sqlJobResults, tenant.UUID(), jobID.UUID())
	if err != nil {
		return batch.Job{}, nil, nil, mapError(err, "read batch results")
	}
	defer rows.Close()
	var results []batch.Result
	for rows.Next() {
		var (
			res      = batch.Result{Job: jobID}
			status   string
			decision *uuid.UUID
			reason   *string
		)
		if err := rows.Scan(&res.Index, &status, &decision, &reason, &res.RecordedAt); err != nil {
			return batch.Job{}, nil, nil, mapError(err, "scan batch result")
		}
		res.Status = batch.ResultStatus(status)
		res.RecordedAt = res.RecordedAt.UTC()
		if decision != nil {
			d := id.NewDecisionID(*decision)
			res.Decision = &d
		}
		res.Reason = errs.ReasonCode(deref(reason))
		results = append(results, res)
	}
	return j, items, results, mapError(rows.Err(), "read batch results")
}

// ClaimNext leases the oldest unheld job across the cell.
func (r *BatchRepo) ClaimNext(ctx context.Context, now, leaseUntil time.Time) (batch.Job, bool, error) {
	// Cell-wide: the oldest job of any tenant, whose work then runs as that
	// tenant's.
	ctx = CellScope(ctx)
	j, err := scanJob(r.s.db(ctx).QueryRow(ctx, sqlJobClaim, now.UTC(), leaseUntil.UTC()))
	if errs.IsCategory(err, errs.CategoryNotFound) {
		return batch.Job{}, false, nil
	}
	if err != nil {
		return batch.Job{}, false, err
	}
	return j, true, nil
}

// Renew extends a held lease.
func (r *BatchRepo) Renew(ctx context.Context, jobID id.JobID, leaseUntil time.Time) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	tag, err := r.s.db(ctx).Exec(ctx, sqlJobRenew, tenant.UUID(), jobID.UUID(), leaseUntil.UTC())
	if err != nil {
		return mapError(err, "renew batch lease")
	}
	if tag.RowsAffected() != 1 {
		return errs.New(errs.CategoryConflict, errs.ReasonOptimisticConflict, "The job is no longer running.")
	}
	return nil
}

// Pending returns the items with no result, in order.
func (r *BatchRepo) Pending(ctx context.Context, jobID id.JobID) ([]batch.Item, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlJobPending, tenant.UUID(), jobID.UUID())
	if err != nil {
		return nil, mapError(err, "read pending batch items")
	}
	defer rows.Close()
	var out []batch.Item
	for rows.Next() {
		it := batch.Item{Job: jobID}
		if err := rows.Scan(&it.Index, &it.BusinessKey, &it.Request); err != nil {
			return nil, mapError(err, "scan pending batch item")
		}
		out = append(out, it)
	}
	return out, mapError(rows.Err(), "read pending batch items")
}

// AppendResult records an item's outcome once.
func (r *BatchRepo) AppendResult(ctx context.Context, res batch.Result) (bool, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	if err := res.Validate(); err != nil {
		return false, batchFault(err, "The batch result is not well-formed.")
	}
	tag, err := r.s.db(ctx).Exec(ctx, sqlResultInsert, tenant.UUID(), res.Job.UUID(), res.Index, string(res.Status),
		optUUID(res.Decision), optString(string(res.Reason)), res.RecordedAt.UTC())
	if err != nil {
		return false, mapError(err, "insert batch result")
	}
	return tag.RowsAffected() == 1, nil
}

// Complete marks a running job completed.
func (r *BatchRepo) Complete(ctx context.Context, jobID id.JobID, at time.Time) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlJobComplete, tenant.UUID(), jobID.UUID(), at.UTC())
	return mapError(err, "complete batch job")
}

func scanJob(row pgx.Row) (batch.Job, error) {
	var (
		tenantUUID, jobUUID      uuid.UUID
		j                        batch.Job
		operation, status        string
		requestedBy              *uuid.UUID
		started, finished, lease *time.Time
	)
	if err := row.Scan(&tenantUUID, &jobUUID, &operation, &status, &j.ItemCount, &j.RequestedAt, &requestedBy,
		&started, &finished, &lease); err != nil {
		return batch.Job{}, mapError(err, "scan batch job")
	}
	j.TenantID = id.NewTenantID(tenantUUID)
	j.ID = id.NewJobID(jobUUID)
	j.Operation = batch.Operation(operation)
	j.Status = batch.Status(status)
	j.RequestedAt = j.RequestedAt.UTC()
	if requestedBy != nil {
		j.RequestedBy = id.NewUserID(*requestedBy)
	}
	for _, p := range []struct {
		src *time.Time
		dst *time.Time
	}{{started, &j.StartedAt}, {finished, &j.FinishedAt}, {lease, &j.LeaseUntil}} {
		if p.src != nil {
			*p.dst = p.src.UTC()
		}
	}
	return j, nil
}

// batchFault is an internal error: a batch record no correct writer
// produces.
func batchFault(err error, msg string) error { return obligationFault(err, msg) }
