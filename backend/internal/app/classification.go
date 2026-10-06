package app

import (
	"context"
	"strings"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
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

// Propose asks the Gateway for a proposal. The use case, authority and risk
// tier are fixed here rather than taken from the caller: a caller choosing its
// own governance context would be choosing its own scrutiny.
func (s *ClassificationService) Propose(ctx context.Context, in ProposeInput) (ai.AiClassificationProposal, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst); err != nil {
		return ai.AiClassificationProposal{}, err
	}
	in.SubjectRef, in.Description = strings.TrimSpace(in.SubjectRef), strings.TrimSpace(in.Description)
	if in.SubjectRef == "" || len(in.SubjectRef) > 512 {
		return ai.AiClassificationProposal{}, errs.Invalid("subjectRef", errs.ReasonMissingField, "A subject reference of at most 512 characters is required.")
	}
	if in.Description == "" || len(in.Description) > 2000 {
		return ai.AiClassificationProposal{}, errs.Invalid("description", errs.ReasonMissingField, "A description of at most 2000 characters is required.")
	}
	input, err := canonical.Encode(canonical.Object(
		canonical.F("subjectRef", canonical.String(in.SubjectRef)),
		canonical.F("description", canonical.String(in.Description)),
	))
	if err != nil {
		return ai.AiClassificationProposal{}, internal(err, "The proposal request could not be encoded.")
	}
	return s.models.ProposeClassification(ctx, ai.Invocation{
		UseCase:          ClassificationReviewUseCase,
		AuthorityOutcome: ai.AuthorityOutcome("A1"),
		RiskTier:         ai.RiskTier("T1"),
		// Catalog text: the contract classifies both fields P0, and nothing
		// about a customer travels in this call.
		DataClasses: []privacy.Class{"P0"},
		SubjectRef:  in.SubjectRef,
		Input:       input,
	})
}
