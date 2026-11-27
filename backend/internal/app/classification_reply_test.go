package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway"
	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway/gatewaytest"
	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

const replyCell = "eu-west-1"

// replyClock is a fixed clock for the real Gateway client below.
type replyClock struct{}

func (replyClock) Now() time.Time { return time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC) }

// proposeThroughTheRealClient runs Propose against the real Gateway client, with
// only the transport faked, so the reply checks run exactly as they do in the
// cell: the client builds the proposal, and the service checks the code.
func proposeThroughTheRealClient(t *testing.T, deadline time.Duration, transport *gatewaytest.Fake) (app.ProposeOutcome, error) {
	t.Helper()
	p, err := ai.NewPolicy([]ai.UseCasePolicy{{
		UseCase:              app.ClassificationReviewUseCase,
		Owner:                "lane-l",
		MaxAuthority:         ai.AuthorityOutcomeA1,
		MaxRiskTier:          ai.RiskTierT2,
		PermittedRegions:     []string{replyCell},
		PermittedDataClasses: []privacy.Class{privacy.P0},
	}}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	client, err := gateway.New(gateway.Config{
		Transport: transport,
		Policy:    gateway.StaticPolicy{P: p},
		Recorder:  &gatewaytest.Recorder{},
		Clock:     replyClock{},
		IDs:       &idgen.Sequential{},
		Region:    replyCell,
		Deadline:  deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	return app.NewClassificationService(client).Propose(
		security.Into(context.Background(), security.System(tenantID)),
		app.ProposeInput{SubjectRef: "sku:PLAN-UNL-5G", Description: "Unlimited 5G plan"})
}

func replyWithCode(code string) gateway.Reply {
	return gateway.Reply{
		ModelProfile: "model:m", ProviderProfile: "provider:p", PromptProfile: "prompt:q",
		AiTrainVersion: "ai-1", ProposedCode: code, Confidence: "0.9",
	}
}

// An empty code and a code of only spaces are two different answers: the first
// is "no code" (the proposal cannot be built), the second reaches the ontology
// check and fails it. The screen words them differently, so the two reasons
// must stay apart. A valid code goes through.
func TestEmptyAndBlankCodesAreTwoDifferentReviewReasons(t *testing.T) {
	cases := []struct {
		name       string
		code       string
		wantReview bool
		wantReason errs.ReasonCode
	}{
		{"empty code", "", true, ai.ReasonGatewayReplyUnevidenced},
		{"spaces only", "   ", true, ai.ReasonProposalNotAnOntologyNode},
		{"tab and newline only", "\t\n", true, ai.ReasonProposalNotAnOntologyNode},
		{"a valid code", "ontology:telecom/voice/mobile", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := proposeThroughTheRealClient(t, time.Second, &gatewaytest.Fake{Reply: replyWithCode(c.code)})
			if err != nil {
				t.Fatalf("a reply with this code failed the request: %v", err)
			}
			if !c.wantReview {
				if o.Disposition != app.DispositionProposed || o.Proposal.ProposedCode() != c.code || o.ReviewReason != "" {
					t.Fatalf("outcome %+v, want a proposal for %q", o, c.code)
				}
				return
			}
			if o.Disposition != app.DispositionReviewRequired || o.ReviewReason != c.wantReason {
				t.Fatalf("outcome %+v, want REVIEW_REQUIRED with %s", o, c.wantReason)
			}
			if o.Proposal.ProposedCode() != "" {
				t.Fatalf("a discarded code reached the reviewer: %q", o.Proposal.ProposedCode())
			}
		})
	}
}

// A deadline that passes is the same outcome as an AI that cannot be reached: a
// review with the unavailable reason, not an error, a panic or an empty
// proposal.
func TestADeadlineThatPassesGoesToReviewAsUnavailable(t *testing.T) {
	hang := &gatewaytest.Fake{Respond: func(ctx context.Context, _ gateway.Call) (gateway.Reply, error) {
		<-ctx.Done()
		return gateway.Reply{}, ctx.Err()
	}}
	const deadline = 20 * time.Millisecond

	// Run in the test goroutine: a hang is caught by go test -timeout. The real
	// clock times the call, because the Gateway's deadline is a context deadline,
	// not the injected clock.
	start := time.Now()
	o, err := proposeThroughTheRealClient(t, deadline, hang)
	took := time.Since(start)

	if err != nil {
		t.Fatalf("a deadline failed the request: %v", err)
	}
	if o.Disposition != app.DispositionReviewRequired || o.ReviewReason != ai.ReasonGatewayUnavailable {
		t.Fatalf("outcome %+v, want REVIEW_REQUIRED with %s", o, ai.ReasonGatewayUnavailable)
	}
	if o.Proposal.ProposedCode() != "" {
		t.Fatalf("an empty reply carried a code: %q", o.Proposal.ProposedCode())
	}
	// The call waited for the deadline, and did not wait much longer: a deadline
	// that was ignored or set far too long fails here.
	if took < deadline {
		t.Fatalf("the call took %s, less than the %s deadline", took, deadline)
	}
	if took >= 2*time.Second {
		t.Fatalf("the call took %s, want under 2s for a %s deadline", took, deadline)
	}
}

// A transport that fails outright is the same outcome as a deadline.
func TestATransportFailureGoesToReviewAsUnavailable(t *testing.T) {
	o, err := proposeThroughTheRealClient(t, time.Second, &gatewaytest.Fake{Err: errors.New("connection refused")})
	if err != nil {
		t.Fatalf("a transport failure failed the request: %v", err)
	}
	if o.Disposition != app.DispositionReviewRequired || o.ReviewReason != ai.ReasonGatewayUnavailable {
		t.Fatalf("outcome %+v, want REVIEW_REQUIRED with %s", o, ai.ReasonGatewayUnavailable)
	}
}
