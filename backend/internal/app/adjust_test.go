package app_test

import (
	"os"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// publishHigherRate releases a bundle whose standard rate is 23% instead of
// 21%, making it the active content.
func (h *harness) publishHigherRate(t *testing.T) *rule.Bundle {
	t.Helper()
	src, err := os.ReadFile(workedPack)
	if err != nil {
		t.Fatal(err)
	}
	later := strings.Replace(string(src), `const standard rate  "0.2100"`, `const standard rate  "0.2300"`, 1)
	if later == string(src) {
		t.Fatal("the pack no longer has the line this test edits; update the test")
	}
	next, err := rule.Load(compileManifest(t, later))
	if err != nil {
		t.Fatal(err)
	}
	h.holder.Publish(next)
	h.library.Add(next)
	return next
}

func adjustInput(t *testing.T, key string, prior evidence.Decision, net string) app.CommitInput {
	in := commitInput(t, key, net)
	in.Determination.Supersedes = &prior.ID
	return in
}

func TestDETREQ0030AdjustmentEvaluatesAgainstTheOriginalBundle(t *testing.T) {
	h := newHarness(t)
	original, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, "c-1", "100.00"))
	if err != nil {
		t.Fatal(err)
	}
	prior, err := h.svc.Decision(system(), *original.ResultRef)
	if err != nil {
		t.Fatal(err)
	}

	// Content moves on: 23% is now active.
	h.publishHigherRate(t)

	adj, err := h.svc.Adjust(as(security.RoleOperator), adjustInput(t, "a-1", evidence.Decision{ID: prior.Record.DecisionID}, "50.00"))
	if err != nil {
		t.Fatal(err)
	}
	if adj.Status != 201 {
		t.Fatalf("adjust: %d %s", adj.Status, adj.Body)
	}
	// The worked pack's standard rate on 50.00: 21% is 10.50; 23% would be
	// 11.50. The correction is made under the content that made the original.
	if !strings.Contains(string(adj.Body), `"vat":"10.50"`) {
		t.Fatalf("adjustment was not evaluated under the original bundle: %s", adj.Body)
	}
	rec, err := h.svc.Decision(system(), *adj.ResultRef)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Record.BundleDigest != prior.Record.BundleDigest {
		t.Fatalf("adjustment recorded bundle %s; the original was %s", rec.Record.BundleDigest, prior.Record.BundleDigest)
	}
	if rec.Record.Supersedes == nil || *rec.Record.Supersedes != prior.Record.DecisionID {
		t.Fatal("the adjustment does not supersede the original")
	}
}

func TestAdjustRefusesWhenTheOriginalBundleIsNotLoaded(t *testing.T) {
	h := newHarness(t)
	original, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, "c-1", "100.00"))
	if err != nil {
		t.Fatal(err)
	}
	next := h.publishHigherRate(t)
	only := &rule.Library{}
	only.Add(next)
	bare := app.NewDeterminationService(h.holder, only, h.decisions, h.store, noTx{}, h.clock, &idgen.Sequential{}, trains).
		WithIdempotency(app.NewIdempotency(h.idem, noTx{}, h.clock))
	_, err = bare.Adjust(as(security.RoleOperator), adjustInput(t, "a-1", evidence.Decision{ID: *original.ResultRef}, "50.00"))
	if errs.ReasonOf(err) != errs.ReasonNoContentBundle {
		t.Fatalf("adjusting with the original bundle unloaded: %v; want NO_CONTENT_BUNDLE, not a re-evaluation under current content", err)
	}
}

func TestAdjustRequiresTheDecisionItCorrects(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.Adjust(as(security.RoleOperator), commitInput(t, "a-1", "50.00"))
	if errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("an adjustment with no supersedes: %v", err)
	}
}

func TestAdjustKeysAreScopedApartFromCommitKeys(t *testing.T) {
	h := newHarness(t)
	original, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, "k-1", "100.00"))
	if err != nil {
		t.Fatal(err)
	}
	// The same key value under the other endpoint is a different key
	// (ADR-0013 §2.2): this is not a replay of the commit.
	adj, err := h.svc.Adjust(as(security.RoleOperator), adjustInput(t, "k-1", evidence.Decision{ID: *original.ResultRef}, "50.00"))
	if err != nil || adj.Replayed {
		t.Fatalf("adjust under a commit's key value: %v replayed=%t", err, adj.Replayed)
	}
	again, err := h.svc.Adjust(as(security.RoleOperator), adjustInput(t, "k-1", evidence.Decision{ID: *original.ResultRef}, "50.00"))
	if err != nil || !again.Replayed || *again.ResultRef != *adj.ResultRef {
		t.Fatalf("an adjust retry was not a replay: %v %+v", err, again)
	}
}

func TestAdjustAuthorization(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.Adjust(as(security.RoleAnalyst), commitInput(t, "a-1", "50.00"))
	if errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an analyst adjusted: %v", err)
	}
}
