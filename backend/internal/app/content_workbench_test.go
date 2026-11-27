package app_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/crosswalk"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rulechange"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// memCrosswalks is the crosswalk library in memory, append-only.
type memCrosswalks struct {
	mu       sync.Mutex
	walks    []crosswalk.Crosswalk // identity only
	versions map[id.CrosswalkID][]crosswalk.Version
	events   map[id.CrosswalkID]map[int][]crosswalk.Event
}

func (m *memCrosswalks) Lock(context.Context, string) error { return nil }

func (m *memCrosswalks) CreateCrosswalk(_ context.Context, c crosswalk.Crosswalk) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.walks = append(m.walks, c)
	return nil
}

func (m *memCrosswalks) CreateVersion(_ context.Context, cid id.CrosswalkID, v crosswalk.Version) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.versions == nil {
		m.versions, m.events = map[id.CrosswalkID][]crosswalk.Version{}, map[id.CrosswalkID]map[int][]crosswalk.Event{}
	}
	if m.events[cid] == nil {
		m.events[cid] = map[int][]crosswalk.Event{}
	}
	m.events[cid][v.Number] = slices.Clone(v.History)
	v.History = nil
	m.versions[cid] = append(m.versions[cid], v)
	return nil
}

func (m *memCrosswalks) AppendEvent(_ context.Context, cid id.CrosswalkID, n int, e crosswalk.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events[cid][n])+1 != e.Seq {
		return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "seq")
	}
	m.events[cid][n] = append(m.events[cid][n], e)
	return nil
}

func (m *memCrosswalks) folded(match func(crosswalk.Crosswalk) bool) []crosswalk.Crosswalk {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []crosswalk.Crosswalk
	for _, c := range m.walks {
		vs := m.versions[c.ID]
		c.Versions = nil
		for _, v := range vs {
			f, err := crosswalk.Fold(v, m.events[c.ID][v.Number])
			if err != nil {
				panic(err)
			}
			c.Versions = append(c.Versions, f)
		}
		if match(c) {
			out = append(out, c)
		}
	}
	slices.Reverse(out)
	return out
}

func (m *memCrosswalks) Crosswalk(_ context.Context, cid id.CrosswalkID) (crosswalk.Crosswalk, error) {
	got := m.folded(func(c crosswalk.Crosswalk) bool { return c.ID == cid })
	if len(got) == 0 {
		return crosswalk.Crosswalk{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
	}
	return got[0], nil
}

func (m *memCrosswalks) CrosswalkByIdentity(_ context.Context, v, n, r string) (crosswalk.Crosswalk, bool, error) {
	got := m.folded(func(c crosswalk.Crosswalk) bool { return c.Vendor == v && c.Namespace == n && c.VendorRelease == r })
	if len(got) == 0 {
		return crosswalk.Crosswalk{}, false, nil
	}
	return got[0], true, nil
}

func (m *memCrosswalks) CrosswalkByIntakeKey(_ context.Context, key string) (crosswalk.Crosswalk, bool, error) {
	got := m.folded(func(c crosswalk.Crosswalk) bool {
		return slices.ContainsFunc(c.Versions, func(v crosswalk.Version) bool { return v.IntakeKey == key })
	})
	if len(got) == 0 {
		return crosswalk.Crosswalk{}, false, nil
	}
	return got[0], true, nil
}

func (m *memCrosswalks) Crosswalks(_ context.Context, limit int) ([]crosswalk.Crosswalk, error) {
	got := m.folded(func(crosswalk.Crosswalk) bool { return true })
	if len(got) > limit {
		got = got[:limit]
	}
	return got, nil
}

// memRuleChanges is the rule approval queue in memory, append-only.
type memRuleChanges struct {
	mu      sync.Mutex
	changes []rulechange.Proposal
	events  map[id.RuleChangeID][]rulechange.Event
}

func (m *memRuleChanges) Lock(context.Context, string) error { return nil }

func (m *memRuleChanges) CreateRuleChange(_ context.Context, p rulechange.Proposal, events []rulechange.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.events == nil {
		m.events = map[id.RuleChangeID][]rulechange.Event{}
	}
	m.changes = append(m.changes, p)
	m.events[p.ID] = slices.Clone(events)
	return nil
}

func (m *memRuleChanges) AppendRuleChangeEvent(_ context.Context, cid id.RuleChangeID, e rulechange.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events[cid])+1 != e.Seq {
		return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "seq")
	}
	m.events[cid] = append(m.events[cid], e)
	return nil
}

func (m *memRuleChanges) folded(match func(rulechange.Proposal) bool) []rulechange.Proposal {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []rulechange.Proposal
	for _, p := range m.changes {
		if !match(p) {
			continue
		}
		f, err := rulechange.Fold(p, m.events[p.ID])
		if err != nil {
			panic(err)
		}
		out = append(out, f)
	}
	slices.Reverse(out)
	return out
}

func (m *memRuleChanges) RuleChange(_ context.Context, cid id.RuleChangeID) (rulechange.Proposal, error) {
	got := m.folded(func(p rulechange.Proposal) bool { return p.ID == cid })
	if len(got) == 0 {
		return rulechange.Proposal{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
	}
	return got[0], nil
}

func (m *memRuleChanges) RuleChangeByIntakeKey(_ context.Context, key string) (rulechange.Proposal, bool, error) {
	got := m.folded(func(p rulechange.Proposal) bool { return p.IntakeKey == key })
	if len(got) == 0 {
		return rulechange.Proposal{}, false, nil
	}
	return got[0], true, nil
}

func (m *memRuleChanges) RuleChanges(_ context.Context, limit int) ([]rulechange.Proposal, error) {
	got := m.folded(func(rulechange.Proposal) bool { return true })
	if len(got) > limit {
		got = got[:limit]
	}
	return got, nil
}

var (
	cwOperator = ctbPerson("01920000-0000-7000-8000-0000000000f1", security.RoleOperator)
	cwAnalyst  = ctbPerson("01920000-0000-7000-8000-0000000000f2", security.RoleAnalyst)
	cwAnalyst2 = ctbPerson("01920000-0000-7000-8000-0000000000f3", security.RoleAnalyst)
	cwAuditor  = ctbPerson("01920000-0000-7000-8000-0000000000f4", security.RoleAuditor)
)

func cwClock() *settableClock {
	return &settableClock{t: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)}
}

func sureTax(key string, entries ...crosswalk.Entry) app.CrosswalkInput {
	return app.CrosswalkInput{Vendor: "suretax", Namespace: "trans_type", VendorRelease: "2026.10", Title: "SureTax codes",
		Version: app.CrosswalkVersionInput{IntakeKey: key, Rights: crosswalk.Rights{Basis: crosswalk.RightsCustomerSupplied,
			Reference: "CFG-2026-10", ExpiresOn: day("2027-12-31")}, EffectiveFrom: day("2026-10-01"), Entries: entries, Submit: true}}
}

func local(code string) crosswalk.Entry {
	return crosswalk.Entry{ExternalCode: code, Description: "Local exchange", Match: crosswalk.MatchExact,
		ServiceTarget: "ontology:telecom/voice/fixed/local"}
}

// Doc 02 WF-06/WF-07: written in review, idempotent on its key, unique per
// vendor release, approved by a second person, and a later approved version
// supersedes the earlier one, which still reproduces its translation.
func TestCrosswalkLifecycle(t *testing.T) {
	svc := app.NewCrosswalkService(&memCrosswalks{}, noTx{}, cwClock(), &idgen.Sequential{})
	v, created, err := svc.CreateCrosswalk(cwOperator, sureTax("k-1", local("010101")))
	if err != nil || !created || v.Vendor != "SURETAX" || v.Versions[0].Status != crosswalk.StatusInReview {
		t.Fatalf("create: %v %v %+v", created, err, v.Crosswalk)
	}
	if again, created, err := svc.CreateCrosswalk(cwOperator, sureTax("k-1", local("010101"))); err != nil || created || again.ID != v.ID {
		t.Fatalf("replay: %v %v", created, err)
	}
	if _, _, err := svc.CreateCrosswalk(cwOperator, sureTax("k-1", local("010102"))); errs.ReasonOf(err) != errs.ReasonIdempotencyKeyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	if _, _, err := svc.CreateCrosswalk(cwOperator, sureTax("k-2", local("010101"))); errs.ReasonOf(err) != errs.ReasonAlreadyExists {
		t.Fatalf("second crosswalk for one release: %v", err)
	}
	if got, err := svc.LookupCrosswalk(cwAuditor, v.ID, "010101", time.Time{}, 0); err != nil || got.Resolution != crosswalk.ResolutionUnmapped {
		t.Fatalf("lookup before approval: %v %v", got.Resolution, err)
	}
	if _, err := svc.TransitionCrosswalkVersion(cwOperator, v.ID, 1, crosswalk.StatusApproved, "ok", 2); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("operator approves: %v", err)
	}
	// The analyst who wrote a version does not approve it.
	mine, _, err := svc.AddCrosswalkVersion(cwAnalyst, v.ID, sureTax("k-3", local("010101")).Version)
	if errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("second open version: %v %+v", err, mine)
	}
	v, err = svc.TransitionCrosswalkVersion(cwAnalyst, v.ID, 1, crosswalk.StatusApproved, "Checked against export", 2)
	if err != nil || v.Versions[0].Status != crosswalk.StatusApproved {
		t.Fatalf("approve: %v", err)
	}
	if got, err := svc.LookupCrosswalk(cwAuditor, v.ID, "010101", time.Time{}, 0); err != nil || got.Resolution != crosswalk.ResolutionMapped || got.Version != 1 {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	v2 := sureTax("k-4", local("010101"), local("010102")).Version
	v, _, err = svc.AddCrosswalkVersion(cwAnalyst, v.ID, v2)
	if err != nil || len(v.Versions) != 2 || v.Versions[1].Status != crosswalk.StatusInReview {
		t.Fatalf("version 2: %v", err)
	}
	if _, err := svc.TransitionCrosswalkVersion(cwAnalyst, v.ID, 2, crosswalk.StatusApproved, "ok", 2); errs.ReasonOf(err) != crosswalk.ReasonMakerChecker {
		t.Fatalf("author approves own version: %v", err)
	}
	v, err = svc.TransitionCrosswalkVersion(cwAnalyst2, v.ID, 2, crosswalk.StatusApproved, "ok", 2)
	if err != nil || v.Versions[0].Status != crosswalk.StatusSuperseded || v.Versions[1].Status != crosswalk.StatusApproved {
		t.Fatalf("supersede: %v", err)
	}
	if got, _ := svc.LookupCrosswalk(cwAuditor, v.ID, "010102", time.Time{}, 1); got.Resolution != crosswalk.ResolutionUnmapped {
		t.Fatalf("historical version gained a code: %s", got.Resolution)
	}
	if got, _ := svc.LookupCrosswalk(cwAuditor, v.ID, "010102", time.Time{}, 0); got.Resolution != crosswalk.ResolutionMapped {
		t.Fatalf("current version: %s", got.Resolution)
	}
}

func ny(key string) app.RuleChangeInput {
	return app.RuleChangeInput{IntakeKey: key, Pack: "US-NY-TELECOM", BaseVersion: "3.2.0", Rule: "ZTAX-RULE-NY-MCTD",
		Jurisdiction: "us-ny", ChangeType: rulechange.ChangeRate, Summary: "MCTD rate", Detail: "0.34% to 0.36%",
		Citation:    rulechange.Citation{Source: "NY-186-C", Reference: "§186-c(1)", Edition: "2026-09", Authority: content.AuthorityBindingPrimary},
		PublishedOn: day("2026-09-15"), EffectiveFrom: day("2027-01-01"), OriginRef: "feed:item-42"}
}

// WF-02: proposed, recommended by a second person, approved for release by
// a third; nothing more. A revision supersedes a change sent back.
func TestRuleChangeQueue(t *testing.T) {
	svc := app.NewRuleChangeService(&memRuleChanges{}, noTx{}, cwClock(), &idgen.Sequential{})
	p, created, err := svc.ProposeRuleChange(cwAnalyst, ny("r-1"))
	if err != nil || !created || p.Status != rulechange.StatusProposed || p.Jurisdiction != "US-NY" || p.OriginRef != "feed:item-42" {
		t.Fatalf("propose: %v %+v", err, p.Proposal)
	}
	if again, created, err := svc.ProposeRuleChange(cwAnalyst, ny("r-1")); err != nil || created || again.ID != p.ID {
		t.Fatalf("replay: %v", err)
	}
	if _, err := svc.TransitionRuleChange(cwAnalyst, p.ID, rulechange.StatusRecommended, "ok", 1); errs.ReasonOf(err) != rulechange.ReasonMakerChecker {
		t.Fatalf("self-review: %v", err)
	}
	p, err = svc.RecordRuleChangeTestEvidence(cwOperator, p.ID, rulechange.TestEvidence{Ref: "GS-1101"}, 1)
	if err != nil || p.TestEvidence == nil {
		t.Fatalf("evidence: %v", err)
	}
	p, err = svc.TransitionRuleChange(cwAnalyst2, p.ID, rulechange.StatusRecommended, "Matches statute", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TransitionRuleChange(cwAnalyst2, p.ID, rulechange.StatusApprovedForRelease, "ok", 3); errs.ReasonOf(err) != rulechange.ReasonMakerChecker {
		t.Fatalf("recommender approves: %v", err)
	}
	analyst3 := ctbPerson("01920000-0000-7000-8000-0000000000f5", security.RoleAnalyst)
	p, err = svc.TransitionRuleChange(analyst3, p.ID, rulechange.StatusApprovedForRelease, "Confirmed", 3)
	if err != nil || p.Status != rulechange.StatusApprovedForRelease {
		t.Fatalf("approve: %v", err)
	}

	q, _, err := svc.ProposeRuleChange(cwAnalyst, ny("r-2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.TransitionRuleChange(cwAnalyst2, q.ID, rulechange.StatusChangesRequested, "Cite the regulation too", 1); err != nil {
		t.Fatal(err)
	}
	rev := ny("r-3")
	rev.Supersedes = q.ID
	if _, _, err := svc.ProposeRuleChange(cwAnalyst, rev); err != nil {
		t.Fatal(err)
	}
	if old, _ := svc.RuleChange(cwAuditor, q.ID); old.Status != rulechange.StatusSuperseded {
		t.Fatalf("old status %s", old.Status)
	}
	list, err := svc.RuleChanges(cwAuditor, rulechange.Filter{Status: rulechange.StatusApprovedForRelease}, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %d %v", len(list), err)
	}
}
