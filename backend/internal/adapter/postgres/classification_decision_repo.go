package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/classification"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// classification_decision — migration 000032
// ---------------------------------------------------------------------------

const (
	sqlClassificationDecisionInsert = `INSERT INTO ztax.classification_decision
		(tenant_id, proposal_id, subject_ref, proposed_code, action, reason, note, modified_code, evidence_id, reviewer_id, decided_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	sqlClassificationDecisionSelect = `SELECT subject_ref, proposed_code, action, reason, note, modified_code, evidence_id, reviewer_id, decided_at
		FROM ztax.classification_decision WHERE tenant_id = $1 AND proposal_id = $2`
	sqlClassificationDecisionLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
)

// ClassificationDecisionRepo implements port.ClassificationDecisionRepository.
// It writes the one table and nothing else, and the table has no UPDATE or
// DELETE grant.
type ClassificationDecisionRepo struct{ s *Store }

// ClassificationDecisions returns the classification decision repository.
func (s *Store) ClassificationDecisions() *ClassificationDecisionRepo {
	return &ClassificationDecisionRepo{s: s}
}

var _ port.ClassificationDecisionRepository = (*ClassificationDecisionRepo)(nil)

// Lock takes the proposal's lock for the transaction.
func (r *ClassificationDecisionRepo) Lock(ctx context.Context, proposalID uuid.UUID) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlClassificationDecisionLock, tenant.String()+"/classification-decision/"+proposalID.String())
	return mapError(err, "lock classification decision")
}

// Create records a decision. The primary key (tenant, proposal) makes a second
// decision a conflict.
func (r *ClassificationDecisionRepo) Create(ctx context.Context, d classification.ReviewDecision) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if d.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The decision belongs to a tenant outside the caller's scope.")
	}
	if err := d.Validate(); err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlClassificationDecisionInsert, tenant.UUID(), d.ProposalID, d.SubjectRef,
		d.ProposedCode, string(d.Action), d.Reason, optString(d.Note), optString(d.ModifiedCode), optString(d.EvidenceID),
		d.ReviewerID.UUID(), d.DecidedAt.UTC())
	return mapError(err, "insert classification decision")
}

// Decision returns the tenant's decision for a proposal.
func (r *ClassificationDecisionRepo) Decision(ctx context.Context, proposalID uuid.UUID) (classification.ReviewDecision, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return classification.ReviewDecision{}, err
	}
	var (
		d                        = classification.ReviewDecision{TenantID: tenant, ProposalID: proposalID}
		action                   string
		note, modified, evidence *string
		reviewer                 uuid.UUID
		decidedAt                time.Time
	)
	if err := r.s.db(ctx).QueryRow(ctx, sqlClassificationDecisionSelect, tenant.UUID(), proposalID).Scan(
		&d.SubjectRef, &d.ProposedCode, &action, &d.Reason, &note, &modified, &evidence, &reviewer, &decidedAt); err != nil {
		return classification.ReviewDecision{}, mapError(err, "read classification decision")
	}
	d.Action = classification.Action(action)
	d.Note, d.ModifiedCode, d.EvidenceID = deref(note), deref(modified), deref(evidence)
	d.ReviewerID = id.NewUserID(reviewer)
	d.DecidedAt = decidedAt.UTC()
	return d, nil
}
