package subscription_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subscription"
)

var (
	maker   = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000a1"))
	checker = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000a2"))
	today   = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	day     = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
)

func plan(t *testing.T, code string, quotas ...subscription.Quota) subscription.Plan {
	t.Helper()
	p := subscription.Plan{ID: id.NewSubscriptionPlanID(uuid.New()), PlanCode: code, PlanName: "Pilot", ContractRef: "MSA-1",
		TermStart: day, Quotas: quotas, CreatedBy: maker, CreatedAt: today}
	events, err := subscription.Open(p, maker, today)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if p, err = subscription.Fold(p, events); err != nil {
		t.Fatalf("fold: %v", err)
	}
	return p
}

func approve(t *testing.T, p subscription.Plan, at time.Time) subscription.Plan {
	t.Helper()
	e, err := p.Apply(subscription.ActApprove, p.Version(), "", checker, at)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	p, err = subscription.Fold(p, append(p.History, e))
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	return p
}

func TestPlanApprovalIsASecondAdministrators(t *testing.T) {
	p := plan(t, "PILOT", subscription.Quota{Meter: subscription.MeterCommittedDecisions, Included: 10, Overage: subscription.OverageAllowed})
	if _, err := p.Apply(subscription.ActApprove, p.Version(), "", maker, today); errs.ReasonOf(err) != "MAKER_CHECKER" {
		t.Fatalf("self-approval: %v", err)
	}
	if _, ok := subscription.InForce([]subscription.Plan{p}, day); ok {
		t.Fatal("a proposed plan is in force")
	}
	p = approve(t, p, today)
	if got, ok := subscription.InForce([]subscription.Plan{p}, day); !ok || got.ID != p.ID {
		t.Fatal("the approved plan is not in force")
	}
	if _, err := p.Apply(subscription.ActReject, p.Version(), "late", checker, today); err == nil {
		t.Fatal("an approved revision moved again")
	}
}

func TestPlanValidation(t *testing.T) {
	base := subscription.Plan{PlanCode: "P", PlanName: "P", ContractRef: "C", TermStart: day}
	cases := map[string][]subscription.Quota{
		"none":      nil,
		"twice":     {{Meter: subscription.MeterShadowRuns, Overage: subscription.OverageAllowed}, {Meter: subscription.MeterShadowRuns, Overage: subscription.OverageAllowed}},
		"negative":  {{Meter: subscription.MeterShadowRuns, Included: -1, Overage: subscription.OverageAllowed}},
		"unmetered": {{Meter: "API_CALLS", Overage: subscription.OverageAllowed}},
	}
	for name, q := range cases {
		b := base
		b.Quotas = q
		if err := b.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	back := base
	back.Quotas = []subscription.Quota{{Meter: subscription.MeterShadowRuns, Overage: subscription.OverageAllowed}}
	back.TermStart = day.AddDate(0, 0, -1)
	if _, err := subscription.Open(back, maker, today); err == nil {
		t.Fatal("a backdated plan opened")
	}
}

func TestLaterApprovalSupersedes(t *testing.T) {
	q := subscription.Quota{Meter: subscription.MeterShadowRuns, Included: 1, Overage: subscription.OverageAllowed}
	a := approve(t, plan(t, "A", q), today)
	b := approve(t, plan(t, "B", q), today.Add(time.Hour))
	if got, _ := subscription.InForce([]subscription.Plan{b, a}, day); got.PlanCode != "B" {
		t.Fatalf("in force: %s", got.PlanCode)
	}
}

func TestUsageAgainstQuotaIsExact(t *testing.T) {
	p := approve(t, plan(t, "P",
		subscription.Quota{Meter: subscription.MeterCommittedDecisions, Included: 3, Overage: subscription.OverageAllowed},
		subscription.Quota{Meter: subscription.MeterFiscalDocuments, Included: 2, Overage: subscription.OverageHardLimit},
		subscription.Quota{Meter: subscription.MeterFilingSubmissions, Included: 5, Overage: subscription.OverageAllowed},
	), today)
	lines := subscription.Assess([]subscription.Count{
		{Meter: subscription.MeterCommittedDecisions, Count: 3},
		{Meter: subscription.MeterFiscalDocuments, Count: 7},
		{Meter: subscription.MeterFilingSubmissions, Count: 1},
	}, &p)
	want := []struct {
		state           subscription.UsageState
		remain, overage int64
	}{
		{subscription.UsageAtQuota, 0, 0},
		{subscription.UsageOverQuota, 0, 5},
		{subscription.UsageWithinQuota, 4, 0},
		{subscription.UsageNotContracted, 0, 0},
	}
	if len(lines) != len(want) {
		t.Fatalf("%d lines", len(lines))
	}
	for i, w := range want {
		l := lines[i]
		if l.State != w.state || l.Remaining != w.remain || l.Overage != w.overage {
			t.Errorf("%s: %+v", l.Definition.Meter, l)
		}
	}
	if lines[3].Count.Digest != subscription.BasisDigest(nil) {
		t.Error("an uncounted meter does not carry the empty digest")
	}
	for _, l := range subscription.Assess(nil, nil) {
		if l.State != subscription.UsageNoPlan {
			t.Errorf("no plan: %s", l.State)
		}
	}
}

func TestBasisDigestIsOrderIndependentAndReproducible(t *testing.T) {
	a := subscription.BasisDigest([]string{"b", "a", "c"})
	sum := sha256.Sum256([]byte("a\nb\nc"))
	if a != hex.EncodeToString(sum[:]) || a != subscription.BasisDigest([]string{"c", "b", "a"}) {
		t.Fatalf("digest %s", a)
	}
	// One dropped identity changes the digest even when a count would not
	// be compared (MTR-07).
	if subscription.BasisDigest([]string{"a", "b"}) == subscription.BasisDigest([]string{"a", "d"}) {
		t.Fatal("different populations digest alike")
	}
}

func TestPeriodWindow(t *testing.T) {
	w, err := subscription.PeriodWindow("2026-12")
	if err != nil || !w.From.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) || !w.To.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v %v", w, err)
	}
	if _, err := subscription.PeriodWindow("2026-13"); err == nil {
		t.Fatal("month 13 parsed")
	}
}
