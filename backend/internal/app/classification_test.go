package app_test

import (
	"context"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway"
	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
)

// fakeModels records the invocation and answers with a proposal.
type fakeModels struct {
	seen []ai.Invocation
	err  error
}

func (f *fakeModels) Suggest(context.Context, ai.Invocation) (ai.AiSuggestion, error) {
	return ai.AiSuggestion{}, nil
}

func (f *fakeModels) Extract(context.Context, ai.Invocation) (ai.AiExtraction, error) {
	return ai.AiExtraction{}, nil
}

func (f *fakeModels) ProposeClassification(_ context.Context, inv ai.Invocation) (ai.AiClassificationProposal, error) {
	f.seen = append(f.seen, inv)
	if f.err != nil {
		return ai.AiClassificationProposal{}, f.err
	}
	pid, _ := ai.NewAiClassificationProposalID("01920a4d-5a6b-7c8d-9e0f-1a2b3c4d5e6f")
	return ai.NewAiClassificationProposal(pid, inv.SubjectRef, "ontology:telecom/voice/mobile", "0.97", ai.Provenance{
		UseCase: inv.UseCase, ModelProfile: "model:m", ProviderProfile: "provider:p", PromptProfile: "prompt:q",
		Region: "local", DataClass: "P0", RiskTier: inv.RiskTier, AuthorityOutcome: inv.AuthorityOutcome, AiTrainVersion: "ai-1",
	})
}

func TestProposalUsesTheFixedGovernanceContext(t *testing.T) {
	models := &fakeModels{}
	p, err := app.NewClassificationService(models).Propose(as(security.RoleAnalyst),
		app.ProposeInput{SubjectRef: "sku:PLAN-UNL-5G", Description: "Unlimited 5G plan"})
	if err != nil {
		t.Fatal(err)
	}
	inv := models.seen[0]
	if inv.UseCase != app.ClassificationReviewUseCase || inv.AuthorityOutcome != "A1" || inv.RiskTier != "T1" ||
		len(inv.DataClasses) != 1 || inv.DataClasses[0] != "P0" {
		t.Fatalf("governance %+v", inv)
	}
	if p.ProposedCode() != "ontology:telecom/voice/mobile" || p.Reviewed() {
		t.Fatalf("proposal %+v", p)
	}
}

func TestProposalRefusesWithoutARoleOrAnInput(t *testing.T) {
	svc := app.NewClassificationService(&fakeModels{})
	if _, err := svc.Propose(as(security.RoleAdmin), app.ProposeInput{SubjectRef: "x", Description: "y"}); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an administrator asked for a proposal: %v", err)
	}
	if _, err := svc.Propose(as(security.RoleOperator), app.ProposeInput{SubjectRef: " ", Description: "y"}); errs.CategoryOf(err) != errs.CategoryValidation {
		t.Fatalf("an empty subject: %v", err)
	}
}

func TestProposalPassesGatewayRefusalsThrough(t *testing.T) {
	refused := errs.New(errs.CategoryUnsupported, gateway.ReasonGatewayNotConfigured, "not here")
	_, err := app.NewClassificationService(&fakeModels{err: refused}).Propose(as(security.RoleOperator),
		app.ProposeInput{SubjectRef: "x", Description: "y"})
	if errs.CategoryOf(err) != errs.CategoryUnsupported {
		t.Fatalf("a not-configured Gateway: %v", err)
	}
}
