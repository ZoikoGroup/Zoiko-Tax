package app

import (
	"context"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Latest-first listings of what was previously readable only by identifier.
//
// Decisions are listed from their index rows alone: a listing never opens an
// evidence object, so it cannot be slowed or failed by the object store. The
// other four list the same views their single reads return, one read per
// item, so a listed refund, document, run or job is exactly what opening it
// shows. That costs a read per item, which is why their page is capped well
// below the decisions'.

// ItemListCap bounds a listing that reads each item in full.
const ItemListCap = 100

func itemLimit(n int) int {
	if n <= 0 || n > ItemListCap {
		return ItemListCap
	}
	return n
}

// knownOutcomes are the outcomes a decision can record.
var knownOutcomes = map[evidence.Outcome]bool{
	evidence.OutcomeAuthoritative: true, evidence.OutcomeAdvisory: true, evidence.OutcomeAmbiguous: true,
	evidence.OutcomeConflicted: true, evidence.OutcomeUnsupported: true, evidence.OutcomeReviewRequired: true,
}

// Decisions lists recorded decisions, latest first.
func (s *DeterminationService) Decisions(ctx context.Context, f port.DecisionFilter) ([]port.DecisionListing, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return nil, err
	}
	if f.Outcome != "" && !knownOutcomes[f.Outcome] {
		return nil, errs.Invalid("outcome", errs.ReasonInvalidValue, "That is not a decision outcome.")
	}
	return s.decisions.List(ctx, f)
}

// Refunds lists refunds, latest requested first, each with its history.
func (s *RefundService) Refunds(ctx context.Context, limit int) ([]RefundView, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return nil, err
	}
	ids, err := s.refunds.Recent(ctx, itemLimit(limit))
	if err != nil {
		return nil, err
	}
	out := make([]RefundView, 0, len(ids))
	for _, refundID := range ids {
		v, err := s.Refund(ctx, refundID)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Documents lists fiscal documents, latest committed first, each with its
// status history.
func (s *DocumentService) Documents(ctx context.Context, limit int) ([]DocumentView, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return nil, err
	}
	if err := s.wired(); err != nil {
		return nil, err
	}
	ids, err := s.docs.Recent(ctx, itemLimit(limit))
	if err != nil {
		return nil, err
	}
	out := make([]DocumentView, 0, len(ids))
	for _, documentID := range ids {
		v, err := s.Document(ctx, documentID)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Runs lists reconciliation runs, latest first, each with its items and
// their resolutions.
func (s *ReconciliationService) Runs(ctx context.Context, limit int) ([]RunView, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return nil, err
	}
	ids, err := s.recon.Recent(ctx, itemLimit(limit))
	if err != nil {
		return nil, err
	}
	out := make([]RunView, 0, len(ids))
	for _, runID := range ids {
		v, err := s.RunByID(ctx, runID)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Jobs lists batch jobs, latest requested first, each with its items'
// results so far.
func (s *BatchService) Jobs(ctx context.Context, limit int) ([]JobView, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return nil, err
	}
	ids, err := s.batches.Recent(ctx, itemLimit(limit))
	if err != nil {
		return nil, err
	}
	out := make([]JobView, 0, len(ids))
	for _, jobID := range ids {
		v, err := s.Job(ctx, jobID)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
