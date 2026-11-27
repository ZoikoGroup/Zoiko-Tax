package app_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/entitlement"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/legal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/market"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subscription"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// memEntitlements is the grant and plan registers in memory: append-only,
// refusing a second record under an intake key as migration 000039's
// unique indexes do.
type memEntitlements struct {
	mu          sync.Mutex
	grants      []entitlement.Grant
	grantEvents map[id.EntitlementID][]entitlement.Event
	plans       []subscription.Plan
	planEvents  map[id.SubscriptionPlanID][]subscription.Event
}

func newMemEntitlements() *memEntitlements {
	return &memEntitlements{grantEvents: map[id.EntitlementID][]entitlement.Event{}, planEvents: map[id.SubscriptionPlanID][]subscription.Event{}}
}

func (m *memEntitlements) Lock(context.Context, string) error { return nil }

func (m *memEntitlements) CreateGrant(_ context.Context, g entitlement.Grant, events []entitlement.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.grants {
		if o.IntakeKey == g.IntakeKey {
			return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "intake key")
		}
	}
	m.grants = append(m.grants, g)
	m.grantEvents[g.ID] = slices.Clone(events)
	return nil
}

func (m *memEntitlements) AppendGrantEvents(_ context.Context, gid id.EntitlementID, events []entitlement.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range events {
		if len(m.grantEvents[gid])+1 != e.Seq {
			return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "seq")
		}
		m.grantEvents[gid] = append(m.grantEvents[gid], e)
	}
	return nil
}

func (m *memEntitlements) folded(match func(entitlement.Grant) bool) []entitlement.Grant {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []entitlement.Grant
	for _, g := range m.grants {
		if match(g) {
			f, err := entitlement.Fold(g, m.grantEvents[g.ID])
			if err != nil {
				panic(err)
			}
			out = append(out, f)
		}
	}
	slices.Reverse(out)
	return out
}

func (m *memEntitlements) Grant(_ context.Context, gid id.EntitlementID) (entitlement.Grant, error) {
	got := m.folded(func(g entitlement.Grant) bool { return g.ID == gid })
	if len(got) == 0 {
		return entitlement.Grant{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
	}
	return got[0], nil
}

func (m *memEntitlements) GrantByIntakeKey(_ context.Context, key string) (entitlement.Grant, bool, error) {
	got := m.folded(func(g entitlement.Grant) bool { return g.IntakeKey == key })
	if len(got) == 0 {
		return entitlement.Grant{}, false, nil
	}
	return got[0], true, nil
}

func (m *memEntitlements) Grants(_ context.Context, limit int) ([]entitlement.Grant, error) {
	got := m.folded(func(entitlement.Grant) bool { return true })
	if len(got) > limit {
		got = got[:limit]
	}
	return got, nil
}

func (m *memEntitlements) CreatePlan(_ context.Context, p subscription.Plan, events []subscription.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.plans {
		if o.IntakeKey == p.IntakeKey {
			return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "intake key")
		}
	}
	m.plans = append(m.plans, p)
	m.planEvents[p.ID] = slices.Clone(events)
	return nil
}

func (m *memEntitlements) AppendPlanEvent(_ context.Context, pid id.SubscriptionPlanID, e subscription.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.planEvents[pid])+1 != e.Seq {
		return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "seq")
	}
	m.planEvents[pid] = append(m.planEvents[pid], e)
	return nil
}

func (m *memEntitlements) foldedPlans(match func(subscription.Plan) bool) []subscription.Plan {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []subscription.Plan
	for _, p := range m.plans {
		if match(p) {
			f, err := subscription.Fold(p, m.planEvents[p.ID])
			if err != nil {
				panic(err)
			}
			out = append(out, f)
		}
	}
	slices.Reverse(out)
	return out
}

func (m *memEntitlements) Plan(_ context.Context, pid id.SubscriptionPlanID) (subscription.Plan, error) {
	got := m.foldedPlans(func(p subscription.Plan) bool { return p.ID == pid })
	if len(got) == 0 {
		return subscription.Plan{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
	}
	return got[0], nil
}

func (m *memEntitlements) PlanByIntakeKey(_ context.Context, key string) (subscription.Plan, bool, error) {
	got := m.foldedPlans(func(p subscription.Plan) bool { return p.IntakeKey == key })
	if len(got) == 0 {
		return subscription.Plan{}, false, nil
	}
	return got[0], true, nil
}

func (m *memEntitlements) Plans(_ context.Context, limit int) ([]subscription.Plan, error) {
	got := m.foldedPlans(func(subscription.Plan) bool { return true })
	if len(got) > limit {
		got = got[:limit]
	}
	return got, nil
}

// memUsage counts fixed rows per meter, by their instant.
type memUsage struct {
	rows map[subscription.Meter][]port.UsageBasisRow
}

func (u memUsage) Basis(_ context.Context, m subscription.Meter, from, to time.Time, limit int) (subscription.Count, []port.UsageBasisRow, error) {
	var ids []string
	var in []port.UsageBasisRow
	for _, r := range u.rows[m] {
		if !r.At.Before(from) && r.At.Before(to) {
			ids, in = append(ids, r.ID), append(in, r)
		}
	}
	slices.SortFunc(in, func(a, b port.UsageBasisRow) int {
		if a.ID < b.ID {
			return -1
		}
		return 1
	})
	if len(in) > limit {
		in = in[:limit]
	}
	return subscription.Count{Meter: m, Count: int64(len(ids)), Digest: subscription.BasisDigest(ids)}, in, nil
}

func (u memUsage) Counts(ctx context.Context, from, to time.Time) ([]subscription.Count, error) {
	var out []subscription.Count
	for _, d := range subscription.Definitions {
		c, _, _ := u.Basis(ctx, d.Meter, from, to, 0)
		out = append(out, c)
	}
	return out, nil
}

var (
	entAdmin  = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000e1"))
	entAdmin2 = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000e2"))
)

type entHarness struct {
	svc   *app.EntitlementService
	store *memEntitlements
	clock *settableClock
}

func newEntHarness() *entHarness {
	h := &entHarness{store: newMemEntitlements(), clock: &settableClock{t: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)}}
	usage := memUsage{rows: map[subscription.Meter][]port.UsageBasisRow{
		subscription.MeterCommittedDecisions: {
			{ID: "b", At: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)},
			{ID: "a", At: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)},
			{ID: "z", At: time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC)},
		},
	}}
	catalogue := market.Catalogue{Version: "test", Markets: []market.Profile{
		{Country: "GB", Name: "United Kingdom", Research: market.EvidenceReviewed, AsOf: "2026-10-30"},
		{Country: "FR", Name: "France", Research: market.ResearchOnly, AsOf: "2026-10-30"},
	}}
	content := func(time.Time) map[string][]string { return map[string][]string{"GB": {"MOBILE_VOICE"}} }
	h.svc = app.NewEntitlementService(h.store, usage, ctbEntities{def: ctbEntity}, noTx{}, h.clock, &idgen.Sequential{}).
		WithCoverage(catalogue, content, false, legal.Matrix{}, nil)
	return h
}

func (h *entHarness) create(t *testing.T, key, jurisdiction string, op entitlement.Operation) app.GrantView {
	t.Helper()
	v, created, err := h.svc.CreateEntitlement(person(entAdmin, security.RoleAdmin), app.EntitlementInput{
		IntakeKey: key, Jurisdiction: jurisdiction, Service: "MOBILE_VOICE", Mode: entitlement.ModeNative, Operation: op,
		EffectiveFrom: h.clock.Now(), ContractRef: "MSA-1", Submit: true,
	})
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	return v
}

func TestEntitlementLifecycleThroughTheService(t *testing.T) {
	h := newEntHarness()
	v := h.create(t, "k1", "GB", entitlement.OpQuote)
	if v.Evaluation.Effective != entitlement.EffectivePending || v.Lifecycle != entitlement.LifecycleInReview {
		t.Fatalf("created: %+v", v.Evaluation)
	}

	// Idempotent intake: same key, same request returns the same grant;
	// a different request is a key reuse.
	again, created, err := h.svc.CreateEntitlement(person(entAdmin, security.RoleAdmin), app.EntitlementInput{
		IntakeKey: "k1", Jurisdiction: "GB", Service: "MOBILE_VOICE", Mode: entitlement.ModeNative, Operation: entitlement.OpQuote,
		EffectiveFrom: h.clock.Now(), ContractRef: "MSA-1", Submit: true,
	})
	if err != nil || created || again.ID != v.ID {
		t.Fatalf("retry: %v %v", created, err)
	}
	_, _, err = h.svc.CreateEntitlement(person(entAdmin, security.RoleAdmin), app.EntitlementInput{
		IntakeKey: "k1", Jurisdiction: "FR", Service: "MOBILE_VOICE", Mode: entitlement.ModeNative, Operation: entitlement.OpQuote,
		EffectiveFrom: h.clock.Now(), ContractRef: "MSA-1",
	})
	if errs.ReasonOf(err) != errs.ReasonIdempotencyKeyReuse {
		t.Fatalf("key reuse: %v", err)
	}

	// Only ADMIN records; any role reads.
	if _, _, err := h.svc.CreateEntitlement(person(entAdmin, security.RoleOperator), app.EntitlementInput{IntakeKey: "k9"}); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("operator recorded: %v", err)
	}
	if _, err := h.svc.Entitlement(person(entAdmin, security.RoleAuditor), v.ID); err != nil {
		t.Fatalf("auditor read: %v", err)
	}

	act := entitlement.Act{Action: entitlement.ActApprove, ExpectedVersion: v.Version(), Reason: "signed"}
	if _, err := h.svc.ActOnEntitlement(person(entAdmin, security.RoleAdmin), v.ID, act); errs.ReasonOf(err) != entitlement.ReasonMakerChecker {
		t.Fatalf("self approval: %v", err)
	}
	got, err := h.svc.ActOnEntitlement(person(entAdmin2, security.RoleAdmin), v.ID, act)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	// GB has content for MOBILE_VOICE: a quote right is ALLOW.
	if got.Evaluation.Decision != entitlement.DecisionAllow || got.Evaluation.Effective != entitlement.EffectiveActive {
		t.Fatalf("approved quote: %+v", got.Evaluation)
	}
}

func TestEntitlementsShowCoverageMismatches(t *testing.T) {
	h := newEntHarness()
	for _, c := range []struct {
		key, j string
		op     entitlement.Operation
	}{{"r", "FR", entitlement.OpQuote}, {"f", "GB", entitlement.OpFile}, {"c", "GB", entitlement.OpCommit}} {
		v := h.create(t, c.key, c.j, c.op)
		if _, err := h.svc.ActOnEntitlement(person(entAdmin2, security.RoleAdmin), v.ID,
			entitlement.Act{Action: entitlement.ActApprove, ExpectedVersion: v.Version(), Reason: "signed"}); err != nil {
			t.Fatalf("approve %s: %v", c.key, err)
		}
	}
	list, err := h.svc.Entitlements(person(entAdmin, security.RoleAnalyst), app.EntitlementFilter{Mismatch: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Grants) != 3 {
		t.Fatalf("%d mismatches", len(list.Grants))
	}
	want := map[string]entitlement.Reason{"FR": entitlement.ReasonContentNotActive}
	for _, g := range list.Grants {
		if g.Evaluation.Effective != entitlement.EffectiveRestricted || g.Evaluation.Decision != entitlement.DecisionDeny {
			t.Errorf("%s %s: %+v", g.Jurisdiction, g.Operation, g.Evaluation)
		}
		if r, ok := want[g.Jurisdiction]; ok && g.Evaluation.Findings[0].Reason != r {
			t.Errorf("FR: %+v", g.Evaluation.Findings)
		}
	}
	// The markets the grants were read against: FR stays RESEARCH_ONLY.
	for _, m := range list.Markets {
		if m.Country == "FR" && m.Status != market.StatusResearchOnly {
			t.Errorf("FR became %s", m.Status)
		}
	}
}

func TestPlansAndUsage(t *testing.T) {
	h := newEntHarness()
	admin := person(entAdmin, security.RoleAdmin)
	p, created, err := h.svc.ProposePlan(admin, app.PlanInput{
		IntakeKey: "p1", PlanCode: "pilot", PlanName: "Pilot", ContractRef: "MSA-1", TermStart: h.clock.Now(),
		Quotas: []subscription.Quota{{Meter: subscription.MeterCommittedDecisions, Included: 1, Overage: subscription.OverageAllowed}},
	})
	if err != nil || !created || p.Status != subscription.StatusProposed || p.PlanCode != "PILOT" {
		t.Fatalf("propose: %v %+v", err, p)
	}
	rep, err := h.svc.Usage(person(entAdmin, security.RoleAuditor), "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Plan != nil || rep.Lines[0].State != subscription.UsageNoPlan || rep.Lines[0].Count.Count != 2 {
		t.Fatalf("before approval: %+v", rep.Lines[0])
	}
	if _, err := h.svc.ActOnPlan(admin, p.ID, subscription.ActApprove, 1, ""); errs.ReasonOf(err) != "MAKER_CHECKER" {
		t.Fatalf("self approval: %v", err)
	}
	if _, err := h.svc.ActOnPlan(person(entAdmin2, security.RoleAdmin), p.ID, subscription.ActApprove, 1, ""); err != nil {
		t.Fatal(err)
	}
	rep, err = h.svc.Usage(admin, "")
	if err != nil {
		t.Fatal(err)
	}
	l := rep.Lines[0]
	if rep.Plan == nil || l.State != subscription.UsageOverQuota || l.Overage != 1 || l.Count.Digest != subscription.BasisDigest([]string{"a", "b"}) {
		t.Fatalf("after approval: %+v", l)
	}
	if rep.Complete {
		t.Fatal("the open month reads complete")
	}
	b, err := h.svc.Basis(admin, "2026-11", subscription.MeterCommittedDecisions, 1)
	if err != nil || b.Count.Count != 2 || len(b.Rows) != 1 || b.Rows[0].ID != "a" || !b.Truncated {
		t.Fatalf("basis: %v %+v", err, b)
	}
	if _, err := h.svc.Usage(admin, "2027-01"); err == nil {
		t.Fatal("a future month was metered")
	}
}
