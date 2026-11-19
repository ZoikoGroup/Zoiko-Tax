package app

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/classification"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ClassificationDecisionService records what a reviewer decided about an AI
// classification proposal. The proposal is advisory and not stored, so the
// request carries what the record needs of it. The reviewer and the tenant come
// only from the session. Until the named signers are decided, the roles that may
// propose may also decide.
type ClassificationDecisionService struct {
	store port.ClassificationDecisionRepository
	audit port.AuditRepository
	tx    port.TxManager
	clock clock.Clock
	ids   idgen.Generator
}

// NewClassificationDecisionService wires the service.
func NewClassificationDecisionService(store port.ClassificationDecisionRepository, audit port.AuditRepository,
	tx port.TxManager, clk clock.Clock, ids idgen.Generator) *ClassificationDecisionService {
	return &ClassificationDecisionService{store: store, audit: audit, tx: tx, clock: clk, ids: ids}
}

var classificationDecisionRoles = []security.Role{security.RoleOperator, security.RoleAnalyst}

// DecisionInput is one decision. It holds no reviewer and no tenant.
type DecisionInput struct {
	ProposalID   uuid.UUID
	SubjectRef   string
	ProposedCode string
	Action       classification.Action
	Reason       string
	Note         string
	ModifiedCode string
	EvidenceID   string
}

// Record saves a decision. A second decision for the same proposal is a
// conflict, and nothing is changed.
func (s *ClassificationDecisionService) Record(ctx context.Context, in DecisionInput) (classification.ReviewDecision, error) {
	sc, err := requirePerson(ctx, classificationDecisionRoles...)
	if err != nil {
		return classification.ReviewDecision{}, err
	}
	d := classification.ReviewDecision{
		TenantID: sc.Tenant(), ProposalID: in.ProposalID,
		SubjectRef: strings.TrimSpace(in.SubjectRef), ProposedCode: strings.TrimSpace(in.ProposedCode),
		Action: in.Action, Reason: strings.TrimSpace(in.Reason), Note: strings.TrimSpace(in.Note),
		ModifiedCode: strings.TrimSpace(in.ModifiedCode), EvidenceID: strings.TrimSpace(in.EvidenceID),
		ReviewerID: sc.Subject(), DecidedAt: s.clock.Now().UTC().Truncate(time.Microsecond),
	}
	if err := d.Validate(); err != nil {
		return classification.ReviewDecision{}, err
	}
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return classification.ReviewDecision{}, err
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	if err := s.store.Lock(txCtx, d.ProposalID); err != nil {
		return classification.ReviewDecision{}, err
	}
	switch _, err := s.store.Decision(txCtx, d.ProposalID); {
	case err == nil:
		return classification.ReviewDecision{}, errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists,
			"A decision for this proposal is already recorded.")
	case errs.CategoryOf(err) != errs.CategoryNotFound:
		return classification.ReviewDecision{}, err
	}
	if err := s.store.Create(txCtx, d); err != nil {
		return classification.ReviewDecision{}, err
	}
	if err := s.record(txCtx, d); err != nil {
		return classification.ReviewDecision{}, err
	}
	if err := tx.Commit(txCtx); err != nil {
		return classification.ReviewDecision{}, err
	}
	return d, nil
}

// Decision returns the decision for a proposal. Another tenant's decision is
// reported as not found, whatever the store returned.
func (s *ClassificationDecisionService) Decision(ctx context.Context, proposalID uuid.UUID) (classification.ReviewDecision, error) {
	sc, err := requirePerson(ctx, classificationDecisionRoles...)
	if err != nil {
		return classification.ReviewDecision{}, err
	}
	d, err := s.store.Decision(ctx, proposalID)
	if err != nil {
		return classification.ReviewDecision{}, err
	}
	if d.TenantID != sc.Tenant() {
		return classification.ReviewDecision{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No decision is recorded for this proposal.")
	}
	return d, nil
}

// record writes the audit entry. It holds the action and nothing free-text.
func (s *ClassificationDecisionService) record(ctx context.Context, d classification.ReviewDecision) error {
	auditID, err := idgen.AuditID(s.ids)
	if err != nil {
		return internal(err, "The decision could not be recorded.")
	}
	body, err := canonical.Encode(canonical.Object(canonical.F("action", canonical.String(string(d.Action)))))
	if err != nil {
		return internal(err, "The decision could not be recorded.")
	}
	actor := d.ReviewerID
	return s.audit.Append(ctx, port.AuditRecord{
		ID: auditID, TenantID: d.TenantID, ActorUserID: &actor,
		Action:      "classification.decision." + strings.ToLower(string(d.Action)),
		SubjectType: "classification_proposal", SubjectID: d.ProposalID.String(), Detail: body, RecordedAt: d.DecidedAt,
	})
}
