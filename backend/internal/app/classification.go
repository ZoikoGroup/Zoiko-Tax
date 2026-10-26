package app

import (
	"context"
	"slices"
	"strings"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/classification"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ClassificationReviewUseCase is the governed AI use case that proposes an
// ontology mapping for human review (ZTAX-CLS-REQ-0082). It is registered in
// the AI-train registry both halves of the Gateway boundary load.
const ClassificationReviewUseCase ai.AiUseCase = "classification-review"

// ClassificationService asks the AI plane for advisory mapping proposals.
//
// Everything it returns is an ai.AiClassificationProposal: advisory by type,
// with no conversion to a classification decision anywhere in this service
// (ADR-0006 §2.6). Confirming a proposal is a human act recorded elsewhere.
type ClassificationService struct {
	models port.ModelGateway
}

// NewClassificationService wires the service.
func NewClassificationService(models port.ModelGateway) *ClassificationService {
	return &ClassificationService{models: models}
}

// ProposeInput is one item to map.
type ProposeInput struct {
	SubjectRef  string
	Description string
}

// Disposition is what the AI plane contributed to one item.
type Disposition string

// The dispositions.
const (
	// DispositionProposed carries a proposal for a person to confirm.
	DispositionProposed Disposition = "PROPOSED"
	// DispositionReviewRequired carries none: the AI plane could not help,
	// and a person classifies the item without a suggestion.
	DispositionReviewRequired Disposition = "REVIEW_REQUIRED"
)

// ProposeOutcome is the answer for one item. Either way a person decides; the
// disposition says only whether they start from a suggestion.
type ProposeOutcome struct {
	SubjectRef  string
	Disposition Disposition
	// Proposal is set only when Disposition is DispositionProposed.
	Proposal ai.AiClassificationProposal
	// ReviewReason is set only when Disposition is DispositionReviewRequired.
	ReviewReason errs.ReasonCode
}

// reviewWithoutAI are the Gateway outcomes after which the item goes to a
// person with no suggestion: the AI plane is absent, down, or answered
// without provenance. A governance refusal is not among them — the kill
// switch, residency and authority are decisions to be shown as refusals, not
// outages to be worked around.
var reviewWithoutAI = []errs.ReasonCode{
	ai.ReasonGatewayNotConfigured,
	ai.ReasonGatewayUnavailable,
	ai.ReasonGatewayReplyUnevidenced,
}

// Propose asks the Gateway for a proposal. The use case, authority and risk
// tier are fixed here rather than taken from the caller: a caller choosing its
// own governance context would be choosing its own scrutiny.
//
// When the AI plane cannot help, the item still goes to review (ADR-0006
// §2.4): the outcome is DispositionReviewRequired with the reason, not an
// error, so the work it was assisting carries on without it.
func (s *ClassificationService) Propose(ctx context.Context, in ProposeInput) (ProposeOutcome, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst); err != nil {
		return ProposeOutcome{}, err
	}
	in.SubjectRef, in.Description = strings.TrimSpace(in.SubjectRef), strings.TrimSpace(in.Description)
	if in.SubjectRef == "" || len(in.SubjectRef) > 512 {
		return ProposeOutcome{}, errs.Invalid("subjectRef", errs.ReasonMissingField, "A subject reference of at most 512 characters is required.")
	}
	if in.Description == "" || len(in.Description) > 2000 {
		return ProposeOutcome{}, errs.Invalid("description", errs.ReasonMissingField, "A description of at most 2000 characters is required.")
	}
	input, err := canonical.Encode(canonical.Object(
		canonical.F("subjectRef", canonical.String(in.SubjectRef)),
		canonical.F("description", canonical.String(in.Description)),
	))
	if err != nil {
		return ProposeOutcome{}, internal(err, "The proposal request could not be encoded.")
	}
	p, err := s.models.ProposeClassification(ctx, ai.Invocation{
		UseCase:          ClassificationReviewUseCase,
		AuthorityOutcome: ai.AuthorityOutcome("A1"),
		RiskTier:         ai.RiskTier("T1"),
		// Catalog text: the contract classifies both fields P0, and nothing
		// about a customer travels in this call.
		DataClasses: []privacy.Class{"P0"},
		SubjectRef:  in.SubjectRef,
		Input:       input,
	})
	if err != nil {
		if reason := errs.ReasonOf(err); slices.Contains(reviewWithoutAI, reason) {
			return reviewRequired(in.SubjectRef, reason), nil
		}
		return ProposeOutcome{}, err
	}
	// The AI can only point at a node the ontology grammar can name; anything
	// else is discarded rather than put in front of a reviewer as a suggestion.
	if classification.OntologyID(p.ProposedCode()).Validate() != nil {
		return reviewRequired(in.SubjectRef, ai.ReasonProposalNotAnOntologyNode), nil
	}
	return ProposeOutcome{SubjectRef: p.SubjectRef(), Disposition: DispositionProposed, Proposal: p}, nil
}

func reviewRequired(subjectRef string, reason errs.ReasonCode) ProposeOutcome {
	return ProposeOutcome{SubjectRef: subjectRef, Disposition: DispositionReviewRequired, ReviewReason: reason}
}
