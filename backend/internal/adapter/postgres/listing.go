package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// Latest-first listings
// ---------------------------------------------------------------------------
//
// Decisions, refunds, fiscal documents, reconciliation runs and batch jobs
// were each readable only by identifier. These are the reads behind their
// listings. Every one is a SELECT scoped to the tenant in the context and
// capped by capLimit; the indexes they sort by are migration 000028's, and
// tax_decision's (tenant_id, recorded_at, decision_id) from 000005.

const (
	// The successor is read alongside each row, so a listing can say which
	// decisions are current without a second query per decision. A decision
	// has at most one successor (tax_decision_one_successor, 000005).
	sqlDecisionList = `SELECT ` + sqlDecisionColumns + `,
		(SELECT s.decision_id FROM ztax.tax_decision s
		 WHERE s.tenant_id = d.tenant_id AND s.supersedes_id = d.decision_id LIMIT 1)
		FROM ztax.tax_decision d
		WHERE d.tenant_id = $1
		  AND ($2 = '' OR d.business_key = $2)
		  AND ($3 = '' OR d.outcome = $3)
		ORDER BY d.recorded_at DESC, d.decision_id DESC
		LIMIT $4`

	sqlRefundRecent = `SELECT refund_id FROM ztax.refund
		WHERE tenant_id = $1 ORDER BY requested_at DESC, refund_id DESC LIMIT $2`
	sqlDocumentRecent = `SELECT document_id FROM ztax.fiscal_document
		WHERE tenant_id = $1 ORDER BY recorded_at DESC, document_id DESC LIMIT $2`
	sqlReconRunRecent = `SELECT run_id FROM ztax.recon_run
		WHERE tenant_id = $1 ORDER BY ran_at DESC, run_id DESC LIMIT $2`
	sqlBatchJobRecent = `SELECT job_id FROM ztax.batch_job
		WHERE tenant_id = $1 ORDER BY requested_at DESC, job_id DESC LIMIT $2`
)

// withExtra scans a row's leading columns with an existing scanner and the
// columns after them into extra, so a listing can add a column to a shape
// scanDecision already reads without a second copy of it.
type withExtra struct {
	row   scanner
	extra []any
}

func (w withExtra) Scan(dest ...any) error { return w.row.Scan(append(dest, w.extra...)...) }

// List returns decisions latest recorded first, with each one's successor.
func (r *DecisionRepo) List(ctx context.Context, f port.DecisionFilter) ([]port.DecisionListing, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlDecisionList,
		tenant.UUID(), f.BusinessKey, string(f.Outcome), capLimit(f.Limit))
	if err != nil {
		return nil, mapError(err, "list decisions")
	}
	defer rows.Close()
	var out []port.DecisionListing
	for rows.Next() {
		var successor *uuid.UUID
		rec, err := scanDecision(withExtra{row: rows, extra: []any{&successor}})
		if err != nil {
			return nil, err
		}
		l := port.DecisionListing{Record: rec}
		if successor != nil {
			s := id.NewDecisionID(*successor)
			l.SupersededBy = &s
		}
		out = append(out, l)
	}
	return out, mapError(rows.Err(), "list decisions")
}

// recentIDs runs one of the identifier listings above.
func recentIDs[T any](ctx context.Context, s *Store, query, what string, limit int, wrap func(uuid.UUID) T) ([]T, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db(ctx).Query(ctx, query, tenant.UUID(), capLimit(limit))
	if err != nil {
		return nil, mapError(err, what)
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var u uuid.UUID
		if err := rows.Scan(&u); err != nil {
			return nil, mapError(err, what)
		}
		out = append(out, wrap(u))
	}
	return out, mapError(rows.Err(), what)
}

// Recent returns the most recently requested refunds, latest first.
func (r *RefundRepo) Recent(ctx context.Context, limit int) ([]id.RefundID, error) {
	return recentIDs(ctx, r.s, sqlRefundRecent, "list refunds", limit, id.NewRefundID)
}

// Recent returns the most recently committed documents, latest first.
func (r *DocumentRepo) Recent(ctx context.Context, limit int) ([]id.FiscalDocumentID, error) {
	return recentIDs(ctx, r.s, sqlDocumentRecent, "list documents", limit, id.NewFiscalDocumentID)
}

// Recent returns the most recent reconciliation runs, latest first.
func (r *ReconRepo) Recent(ctx context.Context, limit int) ([]id.ReconciliationID, error) {
	return recentIDs(ctx, r.s, sqlReconRunRecent, "list reconciliation runs", limit, id.NewReconciliationID)
}

// Recent returns the most recently requested batch jobs, latest first.
func (r *BatchRepo) Recent(ctx context.Context, limit int) ([]id.JobID, error) {
	return recentIDs(ctx, r.s, sqlBatchJobRecent, "list batch jobs", limit, id.NewJobID)
}
