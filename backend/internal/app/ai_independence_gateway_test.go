package app_test

// J1: with a REAL gateway.Client (breaker included) in front of a transport
// that is down, the classify call ends as "gateway unavailable" and the
// deterministic path (Quote, Commit) is not affected.
//
// ai_independence_test.go already proves the same thing with a hand-written
// double and a structural check that DeterminationService takes no
// port.ModelGateway. This file adds the real client, a hanging transport and
// an open breaker. It does not repeat the import ban: fiscal code cannot import
// internal/domain/ai (archtest/fiscal_boundary_test.go and the depguard rule
// fiscal-never-imports-ai in backend/.golangci.yml).

import (
	"context"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway"
	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway/gatewaytest"
	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

const independenceCell = "eu-west-1"

// downGateway is a real gateway.Client over a fake transport.
func downGateway(t *testing.T, transport *gatewaytest.Fake, breaker *gateway.Breaker) *gateway.Client {
	t.Helper()
	pol, err := ai.NewPolicy([]ai.UseCasePolicy{{
		UseCase:              app.ClassificationReviewUseCase,
		Owner:                "lane-l",
		MaxAuthority:         ai.AuthorityOutcomeA1,
		MaxRiskTier:          ai.RiskTierT1,
		PermittedRegions:     []string{independenceCell},
		PermittedDataClasses: []privacy.Class{privacy.P0},
	}}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := gateway.New(gateway.Config{
		Transport: transport,
		Policy:    gateway.StaticPolicy{P: pol},
		Recorder:  &gatewaytest.Recorder{},
		Clock:     clock.System{},
		IDs:       &idgen.Sequential{},
		Region:    independenceCell,
		Deadline:  50 * time.Millisecond,
		Breaker:   breaker,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func downErr() error {
	return errs.New(errs.CategoryUnavailable, gateway.ReasonGatewayUnavailable, "the gateway is down")
}

// bounded is a caller context that fails a hang: the test must finish inside it.
func bounded(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(as(security.RoleOperator), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

var independenceInput = app.ProposeInput{SubjectRef: "sku:PLAN-UNL-5G", Description: "Unlimited 5G plan"}

// wantUnavailable checks both layers: the client's own error, and what the
// classification service turns it into (review with no suggestion).
func wantUnavailable(ctx context.Context, t *testing.T, client *gateway.Client) {
	t.Helper()
	_, err := client.ProposeClassification(ctx, ai.Invocation{
		UseCase: app.ClassificationReviewUseCase, AuthorityOutcome: "A1", RiskTier: "T1",
		DataClasses: []privacy.Class{"P0"}, SubjectRef: "sku:PLAN-UNL-5G", Input: []byte(`{}`),
	})
	if errs.CategoryOf(err) != errs.CategoryUnavailable || errs.ReasonOf(err) != gateway.ReasonGatewayUnavailable {
		t.Fatalf("client: %v (%s / %s), want unavailable / gateway unavailable", err, errs.CategoryOf(err), errs.ReasonOf(err))
	}
	o, err := app.NewClassificationService(client).Propose(ctx, independenceInput)
	if err != nil || o.Disposition != app.DispositionReviewRequired || o.ReviewReason != gateway.ReasonGatewayUnavailable {
		t.Fatalf("service: %+v, %v; want review required with the gateway unavailable reason", o, err)
	}
}

func deterministicStillWorks(t *testing.T, h *harness, idem string) {
	t.Helper()
	if _, err := h.svc.Quote(as(security.RoleAnalyst), app.QuoteInput{
		EventTime: eventAt, Input: lineInput(t, "100.00", "3", false), Accumulators: readSet(t, "9999.99"),
	}); err != nil {
		t.Fatalf("quote with the gateway down: %v", err)
	}
	if _, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, idem, "100.00")); err != nil {
		t.Fatalf("commit with the gateway down: %v", err)
	}
}

func TestClassifyEndsUnavailableWhenTheGatewayIsDown(t *testing.T) {
	transport := &gatewaytest.Fake{Err: downErr()}
	wantUnavailable(bounded(t), t, downGateway(t, transport, nil))
	if len(transport.Calls()) == 0 {
		t.Fatal("the transport was never reached, so the test proved nothing")
	}
}

func TestClassifyEndsUnavailableWhenTheGatewayHangs(t *testing.T) {
	transport := &gatewaytest.Fake{Respond: func(ctx context.Context, _ gateway.Call) (gateway.Reply, error) {
		<-ctx.Done()
		return gateway.Reply{}, ctx.Err()
	}}
	ctx := bounded(t)
	start := time.Now()
	wantUnavailable(ctx, t, downGateway(t, transport, nil))
	if ctx.Err() != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("the call did not end inside its deadline (took %s)", time.Since(start))
	}
}

func TestDeterministicServiceNeverCallsTheDownGateway(t *testing.T) {
	transport := &gatewaytest.Fake{Err: downErr()}
	// The classification service is built over the down gateway, as in the
	// real wiring; the deterministic service is then used beside it.
	_ = app.NewClassificationService(downGateway(t, transport, nil))
	deterministicStillWorks(t, newHarness(t), "idem-a")
	if n := len(transport.Calls()); n != 0 {
		t.Fatalf("the deterministic path called the gateway %d time(s)", n)
	}
}

func TestDeterministicServiceWorksWhileTheBreakerIsOpen(t *testing.T) {
	const threshold = 5
	transport := &gatewaytest.Fake{Err: downErr()}
	client := downGateway(t, transport, gateway.NewBreaker(threshold, time.Hour))
	for range threshold {
		wantUnavailable(bounded(t), t, client)
	}
	// wantUnavailable calls the transport once per client call and once per
	// service call, so count what reached it before the next round.
	reached := len(transport.Calls())
	if reached != threshold {
		t.Fatalf("the transport saw %d calls before the breaker opened, want %d", reached, threshold)
	}
	// The breaker is open: refused without reaching the transport, same ending.
	wantUnavailable(bounded(t), t, client)
	if n := len(transport.Calls()); n != reached {
		t.Fatalf("the open breaker still let calls through: %d, then %d", reached, n)
	}
	deterministicStillWorks(t, newHarness(t), "idem-b")
	if n := len(transport.Calls()); n != reached {
		t.Fatalf("the deterministic path called the gateway: %d, then %d", reached, n)
	}
}
