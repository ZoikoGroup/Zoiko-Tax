package app_test

// This file proves that the deterministic tax path (Quote, Commit) does not
// depend on the AI plane, in two complementary ways.
//
// ZTAX-AIGOV-REQ-0085 requires that Zoiko be able to suspend an AIUseCase
// independently of the deterministic tax service. That is only true if the
// deterministic service has no path to the AI gateway at all -- a suspended
// or unreachable gateway cannot affect something that never calls it.
//
// The strong proof is structural, not behavioral: app.NewDeterminationService
// takes no port.ModelGateway, and DeterminationService holds no field that
// could reach one (TestDeterminationServiceTakesNoModelGateway, below). A
// behavioral test alone could never rule out a future change wiring the
// gateway in through some other call in the same request; the structural
// test is a tripwire that forces whoever makes that change to come here and
// decide whether REQ-0085 still holds. TestDeterministicFlowIndependentOfGateway
// is the behavioral companion: it shows the same thing end to end, with the
// AI plane actually failing every call.

import (
	"context"
	"reflect"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway"
	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// alwaysUnavailableGateway is a port.ModelGateway double that fails every
// call the way the real client fails when the Python Gateway is unreachable
// (gateway.go's own CategoryUnavailable/ReasonGatewayUnavailable mapping),
// and counts how many times it was called. Named distinctly from
// classification_test.go's fakeModels, which is a different kind of double
// (it succeeds and records invocations; this one always fails).
type alwaysUnavailableGateway struct {
	calls int
}

func (g *alwaysUnavailableGateway) unavailable() error {
	return errs.New(errs.CategoryUnavailable, gateway.ReasonGatewayUnavailable,
		"the model gateway is unavailable")
}

func (g *alwaysUnavailableGateway) Suggest(context.Context, ai.Invocation) (ai.AiSuggestion, error) {
	g.calls++
	return ai.AiSuggestion{}, g.unavailable()
}

func (g *alwaysUnavailableGateway) Extract(context.Context, ai.Invocation) (ai.AiExtraction, error) {
	g.calls++
	return ai.AiExtraction{}, g.unavailable()
}

func (g *alwaysUnavailableGateway) ProposeClassification(
	context.Context, ai.Invocation,
) (ai.AiClassificationProposal, error) {
	g.calls++
	return ai.AiClassificationProposal{}, g.unavailable()
}

var _ port.ModelGateway = (*alwaysUnavailableGateway)(nil)

func TestDeterministicFlowIndependentOfGateway(t *testing.T) {
	gw := &alwaysUnavailableGateway{}
	cls := app.NewClassificationService(gw)
	h := newHarness(t)

	quote := func() error {
		_, err := h.svc.Quote(as(security.RoleAnalyst), app.QuoteInput{
			EventTime: eventAt, Input: lineInput(t, "100.00", "3", false), Accumulators: readSet(t, "9999.99"),
		})
		return err
	}

	// Quote and Commit succeed before the AI plane is touched at all.
	if err := quote(); err != nil {
		t.Fatalf("quote before any AI call: %v", err)
	}
	if _, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, "k-1", "100.00")); err != nil {
		t.Fatalf("commit before any AI call: %v", err)
	}

	// The AI plane is down: Propose sends the item to human review with the
	// gateway's own unavailable reason -- nothing manufactures a proposal.
	o, err := cls.Propose(as(security.RoleOperator), app.ProposeInput{
		SubjectRef: "sku:PLAN-UNL-5G", Description: "Unlimited 5G plan",
	})
	if err != nil || o.Disposition != app.DispositionReviewRequired || o.ReviewReason != gateway.ReasonGatewayUnavailable {
		t.Fatalf("propose with the gateway down: got %+v, %v", o, err)
	}

	// Quote and Commit still succeed after the AI failure: the AI plane
	// being down has no bearing on the deterministic path at all. A fresh
	// idempotency key, since "k-1" is now spent.
	if err := quote(); err != nil {
		t.Fatalf("quote after the AI plane failed: %v", err)
	}
	if _, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, "k-2", "100.00")); err != nil {
		t.Fatalf("commit after the AI plane failed: %v", err)
	}

	if gw.calls != 1 {
		t.Fatalf("the gateway was called %d time(s); want exactly 1 (the Propose call)", gw.calls)
	}
}

// modelGatewayType is port.ModelGateway as a reflect.Type, computed once.
var modelGatewayType = reflect.TypeOf((*port.ModelGateway)(nil)).Elem()

// typeDependsOnModelGateway reports whether t is port.ModelGateway, implements
// it, or (recursively, through struct fields, up to a shallow depth) contains
// something that does. The depth cap is defensive only: Go's own field graph
// here is a handful of levels deep at most, and this stops a future
// self-referential or deeply nested type from making the check run away.
func typeDependsOnModelGateway(t reflect.Type, depth int) bool {
	if t == nil || depth > 4 {
		return false
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Implements(modelGatewayType) || reflect.PointerTo(t).Implements(modelGatewayType) {
		return true
	}
	if t.Kind() == reflect.Struct {
		for i := 0; i < t.NumField(); i++ {
			if typeDependsOnModelGateway(t.Field(i).Type, depth+1) {
				return true
			}
		}
	}
	return false
}

func TestDeterminationServiceTakesNoModelGateway(t *testing.T) {
	const failureMessage = "DeterminationService must not depend on the AI gateway; " +
		"see REQ-0085. If you are wiring AI into Quote/Commit, stop and ask the architecture owners."

	ctorType := reflect.TypeOf(app.NewDeterminationService)
	for i := 0; i < ctorType.NumIn(); i++ {
		if pt := ctorType.In(i); typeDependsOnModelGateway(pt, 0) {
			t.Fatalf("%s\nconstructor parameter %d (%s) involves port.ModelGateway", failureMessage, i, pt)
		}
	}

	svcType := reflect.TypeOf(app.DeterminationService{})
	for i := 0; i < svcType.NumField(); i++ {
		f := svcType.Field(i)
		if typeDependsOnModelGateway(f.Type, 0) {
			t.Fatalf("%s\nfield %q (%s) involves port.ModelGateway", failureMessage, f.Name, f.Type)
		}
	}
}
