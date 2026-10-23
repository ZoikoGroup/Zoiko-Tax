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
	// code is what the model proposes; empty means a valid ontology node.
	code string
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
	code := f.code
	if code == "" {
		code = "ontology:telecom/voice/mobile"
	}
	pid, _ := ai.NewAiClassificationProposalID("01920a4d-5a6b-7c8d-9e0f-1a2b3c4d5e6f")
	return ai.NewAiClassificationProposal(pid, inv.SubjectRef, code, "0.97", ai.Provenance{
		UseCase: inv.UseCase, ModelProfile: "model:m", ProviderProfile: "provider:p", PromptProfile: "prompt:q",
		Region: "local", DataClass: "P0", RiskTier: inv.RiskTier, AuthorityOutcome: inv.AuthorityOutcome, AiTrainVersion: "ai-1",
	})
}

func TestProposalUsesTheFixedGovernanceContext(t *testing.T) {
	models := &fakeModels{}
	o, err := app.NewClassificationService(models).Propose(as(security.RoleAnalyst),
		app.ProposeInput{SubjectRef: "sku:PLAN-UNL-5G", Description: "Unlimited 5G plan"})
	if err != nil {
		t.Fatal(err)
	}
	inv := models.seen[0]
	if inv.UseCase != app.ClassificationReviewUseCase || inv.AuthorityOutcome != "A1" || inv.RiskTier != "T1" ||
		len(inv.DataClasses) != 1 || inv.DataClasses[0] != "P0" {
		t.Fatalf("governance %+v", inv)
	}
	if o.Disposition != app.DispositionProposed || o.ReviewReason != "" || o.SubjectRef != "sku:PLAN-UNL-5G" {
		t.Fatalf("outcome %+v", o)
	}
	if p := o.Proposal; p.ProposedCode() != "ontology:telecom/voice/mobile" || p.Reviewed() {
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

// C9: when the AI plane cannot help, the item goes to a person without a
// suggestion. It is an outcome, not an error, so the caller carries on.
func TestProposalGoesToReviewWhenTheAIPlaneCannotHelp(t *testing.T) {
	cases := []struct {
		name   string
		models *fakeModels
		want   errs.ReasonCode
	}{
		{"not configured", &fakeModels{err: errs.New(errs.CategoryUnsupported, gateway.ReasonGatewayNotConfigured, "not here")},
			ai.ReasonGatewayNotConfigured},
		{"unavailable", &fakeModels{err: errs.New(errs.CategoryUnavailable, gateway.ReasonGatewayUnavailable, "down")},
			ai.ReasonGatewayUnavailable},
		{"unevidenced", &fakeModels{err: errs.New(errs.CategoryInternal, gateway.ReasonGatewayReplyUnevidenced, "no provenance")},
			ai.ReasonGatewayReplyUnevidenced},
		// The '#' a Markdown heading leaves behind, and a code from no grammar.
		{"heading, not a node", &fakeModels{code: "#ontology:telecom/voice/mobile"}, ai.ReasonProposalNotAnOntologyNode},
		{"no grammar", &fakeModels{code: "GST-DIGITAL-SERVICE"}, ai.ReasonProposalNotAnOntologyNode},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := app.NewClassificationService(c.models).Propose(as(security.RoleOperator),
				app.ProposeInput{SubjectRef: " sku:PLAN-UNL-5G ", Description: "y"})
			if err != nil {
				t.Fatalf("an AI plane that cannot help failed the request: %v", err)
			}
			if o.Disposition != app.DispositionReviewRequired || o.ReviewReason != c.want || o.SubjectRef != "sku:PLAN-UNL-5G" {
				t.Fatalf("outcome %+v, want REVIEW_REQUIRED with %s", o, c.want)
			}
			if o.Proposal.ProposedCode() != "" {
				t.Fatalf("a discarded proposal reached the reviewer: %q", o.Proposal.ProposedCode())
			}
		})
	}
}

// A governance refusal is a decision, not an outage: it stays an error.
func TestProposalPassesGovernanceRefusalsThrough(t *testing.T) {
	for _, reason := range []errs.ReasonCode{ai.ReasonKillSwitchEngaged, ai.ReasonResidencyRefused, ai.ReasonMalformedContext} {
		refused := ai.Decision{Reason: reason, Detail: "refused"}.Err()
		_, err := app.NewClassificationService(&fakeModels{err: refused}).Propose(as(security.RoleOperator),
			app.ProposeInput{SubjectRef: "x", Description: "y"})
		if errs.ReasonOf(err) != reason {
			t.Fatalf("%s: %v", reason, err)
		}
	}
}
