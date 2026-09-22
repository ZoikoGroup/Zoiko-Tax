package rule_test

import (
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

// A minimal but real bundle: take a net amount, apply a rate, round at the
// line, emit it. This is the shape of almost every tax computation, and every
// test below works against it.
func vatBundle(t *testing.T) rule.Manifest {
	t.Helper()
	const version, semantic = "2026.01.0", "rule:test/vat/standard-rate"
	return rule.Manifest{
		BundleID:  "pack:test@2026.01.0",
		Digest:    "zt1:0000000000000000000000000000000000000000000000000000000000000000",
		IRVersion: rule.IRVersion,
		Constants: []rule.Constant{
			{Name: "standard-rate", Type: rule.TypeRate, Value: "0.20", Basis: string(fiscal.RateBasisNet)},
		},
		Nodes: []rule.Node{
			{ID: "net", Op: rule.OpInput, Type: rule.TypeMoney, Field: "net",
				RuleVersion: version, RuleSemanticID: semantic},
			{ID: "rate", Op: rule.OpConst, Type: rule.TypeRate, Const: "standard-rate",
				RuleVersion: version, RuleSemanticID: semantic},
			{ID: "tax", Op: rule.OpApplyRate, Type: rule.TypeMoney, Args: []rule.NodeID{"net", "rate"},
				Policy:      policy(t, "HALF_UP", 2, "LINE"),
				RuleVersion: version, RuleSemanticID: semantic},
			{ID: "out", Op: rule.OpEmit, Type: rule.TypeMoney, Args: []rule.NodeID{"tax"}, Emit: "tax",
				RuleVersion: version, RuleSemanticID: semantic},
		},
		Roots: []rule.NodeID{"out"},
	}
}

func policy(t *testing.T, mode string, scale int, basis string) *fiscal.RoundingPolicy {
	t.Helper()
	p := fiscaltest.Policy(t, fiscal.RoundingMode(mode), int32(scale), fiscal.RoundingBasis(basis))
	return &p
}

func frame(t *testing.T, net string) rule.Frame {
	t.Helper()
	m, err := fiscal.ParseMoney(net, "EUR")
	if err != nil {
		t.Fatalf("ParseMoney: %v", err)
	}
	return rule.Frame{
		DecisionTime: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC),
		EventTime:    time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Money:        map[string]fiscal.Money{"net": m},
	}
}

func TestEvaluateAppliesTheContentsRate(t *testing.T) {
	b, err := rule.Load(vatBundle(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	result, err := rule.Evaluate(b, frame(t, "100.00"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	tax, ok := result.Emitted["tax"]
	if !ok {
		t.Fatal("no tax emitted")
	}
	if got := tax.Money.CanonicalString(); got != "20.00" {
		t.Errorf("tax is %q, want %q", got, "20.00")
	}
}

// ADR-0005 §2.7: the trace names the rule version and the rounding policy for
// every step that rounds. A decision that cannot name its rounding does not
// replay (ADR-0002 §5.1 control 3).
func TestTraceNamesRuleVersionAndRoundingPolicy(t *testing.T) {
	b, err := rule.Load(vatBundle(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	result, err := rule.Evaluate(b, frame(t, "12.34"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	var rounded int
	for _, step := range result.Trace {
		if step.RuleVersion == "" || step.RuleSemanticID == "" {
			t.Errorf("step %s names no rule", step.Node)
		}
		if step.Op == rule.OpApplyRate {
			rounded++
			if step.Policy != "HALF_UP@2/LINE" {
				t.Errorf("step %s policy is %q, want %q", step.Node, step.Policy, "HALF_UP@2/LINE")
			}
		}
	}
	if rounded != 1 {
		t.Errorf("%d rounding steps in the trace, want 1", rounded)
	}
}

// The property the whole execution model exists to provide: one input, one
// answer, every time, including the order of the trace.
//
// Go randomises map iteration, so a topological sort that walked the node map
// would produce a different trace order on different runs of the same process.
// This asserts it does not.
func TestEvaluationIsDeterministic(t *testing.T) {
	b, err := rule.Load(vatBundle(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	first, err := rule.Evaluate(b, frame(t, "99.99"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	for i := 0; i < 50; i++ {
		next, err := rule.Evaluate(b, frame(t, "99.99"))
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if len(next.Trace) != len(first.Trace) {
			t.Fatalf("trace length changed between runs: %d then %d", len(first.Trace), len(next.Trace))
		}
		for j := range first.Trace {
			if first.Trace[j].Node != next.Trace[j].Node || first.Trace[j].Output != next.Trace[j].Output {
				t.Fatalf("trace step %d changed between runs: %+v then %+v", j, first.Trace[j], next.Trace[j])
			}
		}
	}
}

// Loading the same manifest twice must produce the same evaluation order. A
// bundle rebuilt on a second replica has to trace identically to the first, or
// two replicas serving one cell disagree about what happened.
func TestLoadOrderIsStableAcrossBundles(t *testing.T) {
	a, err := rule.Load(vatBundle(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c, err := rule.Load(vatBundle(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ra, err := rule.Evaluate(a, frame(t, "1.00"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	rc, err := rule.Evaluate(c, frame(t, "1.00"))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	for i := range ra.Trace {
		if ra.Trace[i].Node != rc.Trace[i].Node {
			t.Fatalf("two loads of one manifest evaluated in different orders at step %d: %s vs %s",
				i, ra.Trace[i].Node, rc.Trace[i].Node)
		}
	}
}

// ADR-0002 §2.2 and §5.1 control 5: a node that applies a rate without naming a
// rounding policy is invalid content. The runtime refuses the bundle rather
// than choosing a mode.
func TestBundleWithoutRoundingPolicyIsRefused(t *testing.T) {
	m := vatBundle(t)
	for i := range m.Nodes {
		if m.Nodes[i].ID == "tax" {
			m.Nodes[i].Policy = nil
		}
	}
	_, err := rule.Load(m)
	if err == nil {
		t.Fatal("a bundle applying a rate with no rounding policy was accepted")
	}
	if !strings.Contains(err.Error(), "rounding policy") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// ADR-0005 §2.8: a bundle the runtime cannot execute is refused whole. There is
// no partial activation and no degrading.
func TestIncompatibleIRVersionIsRefused(t *testing.T) {
	m := vatBundle(t)
	m.IRVersion = rule.IRVersion + 1
	if _, err := rule.Load(m); err == nil {
		t.Fatal("a bundle requiring a newer IR version was accepted")
	}
}

func TestCyclicBundleIsRefused(t *testing.T) {
	m := vatBundle(t)
	// Make "net" depend on the emit node, closing a loop.
	for i := range m.Nodes {
		if m.Nodes[i].ID == "net" {
			m.Nodes[i].Op = rule.OpRound
			m.Nodes[i].Args = []rule.NodeID{"out"}
			m.Nodes[i].Policy = policy(t, "HALF_UP", 2, "LINE")
		}
	}
	_, err := rule.Load(m)
	if err == nil {
		t.Fatal("a cyclic bundle was accepted")
	}
	if !strings.Contains(err.Error(), "cyclic") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestDanglingReferenceIsRefused(t *testing.T) {
	m := vatBundle(t)
	for i := range m.Nodes {
		if m.Nodes[i].ID == "tax" {
			m.Nodes[i].Args = []rule.NodeID{"net", "no-such-node"}
		}
	}
	if _, err := rule.Load(m); err == nil {
		t.Fatal("a bundle referencing an unknown node was accepted")
	}
}

// ADR-0016 §2.4: an unregistered reason code in content would produce a
// decision nobody can interpret.
func TestUnregisteredReasonCodeIsRefused(t *testing.T) {
	m := vatBundle(t)
	m.Nodes = append(m.Nodes, rule.Node{
		ID: "refuse", Op: rule.OpRefuse, Type: rule.TypeReason,
		Reason:      errs.ReasonCode("NOT_A_REGISTERED_CODE"),
		RuleVersion: "2026.01.0", RuleSemanticID: "rule:test/refuse",
	})
	if _, err := rule.Load(m); err == nil {
		t.Fatal("a bundle naming an unregistered reason code was accepted")
	}
}

// ADR-0016 §2.1: a refusal is a decision with evidence, not an error. The
// trace up to the refusal is kept, because "why did it refuse" is answered by
// the steps that led there.
func TestRefusalIsADecisionWithATrace(t *testing.T) {
	m := vatBundle(t)
	m.Nodes = append(m.Nodes, rule.Node{
		ID: "zz-refuse", Op: rule.OpRefuse, Type: rule.TypeReason,
		Reason:      errs.ReasonJurisdictionUnsupported,
		RuleVersion: "2026.01.0", RuleSemanticID: "rule:test/refuse",
	})
	b, err := rule.Load(m)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	result, err := rule.Evaluate(b, frame(t, "10.00"))
	if err != nil {
		t.Fatalf("a refusal was reported as an error: %v", err)
	}
	if !result.Refused {
		t.Fatal("the evaluation did not report a refusal")
	}
	if result.Reason != errs.ReasonJurisdictionUnsupported {
		t.Errorf("reason is %q, want %q", result.Reason, errs.ReasonJurisdictionUnsupported)
	}
	if len(result.Trace) == 0 {
		t.Error("a refusal produced no trace, so it cannot be explained")
	}
}

// An absent input is refused rather than defaulted: a rule reading a field the
// transaction did not carry is a rule applied to a transaction it was not
// written for, and a zero would hide that.
func TestMissingInputIsRefused(t *testing.T) {
	b, err := rule.Load(vatBundle(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	empty := rule.Frame{DecisionTime: time.Now().UTC(), EventTime: time.Now().UTC()}
	if _, err := rule.Evaluate(b, empty); err == nil {
		t.Fatal("an evaluation with no input succeeded")
	}
}

// A cell with no bundle is CategoryUnavailable, not an internal error: it will
// load one, and retry is genuinely safe.
func TestNoBundleIsRetryable(t *testing.T) {
	_, err := rule.Evaluate(nil, rule.Frame{})
	if err == nil {
		t.Fatal("evaluating with no bundle succeeded")
	}
	if !errs.IsCategory(err, errs.CategoryUnavailable) {
		t.Errorf("category is %s, want UNAVAILABLE", errs.CategoryOf(err))
	}
}

func TestHolderPublishesAtomically(t *testing.T) {
	var h rule.Holder
	if h.Current() != nil {
		t.Fatal("a fresh holder reported a bundle")
	}
	b, err := rule.Load(vatBundle(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	h.Publish(b)
	if h.Current() != b {
		t.Error("the published bundle is not the current one")
	}
}
