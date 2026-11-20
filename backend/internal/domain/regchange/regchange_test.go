package regchange_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/regchange"
)

var (
	now     = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	analyst = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000a1"))
	second  = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000a2"))
	third   = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000a3"))
)

func date(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func change() regchange.Change {
	c := regchange.Change{
		ID:           id.NewRegulatoryChangeID(uuid.MustParse("01920000-0000-7000-8000-00000000c001")),
		Jurisdiction: "US-NY", Kind: regchange.KindRateChange, Title: "NY excise on mobile voice rises",
		Source: regchange.Source{Authority: "NY DTF", Citation: "N.Y. Tax Law §186-e", Edition: "2026-11",
			DocumentSHA256: strings.Repeat("a", 64)},
		PublishedOn: date("2026-11-01"), EffectiveFrom: date("2027-01-01"), DiscoveredAt: now.Add(-time.Hour),
		DetectedBy: regchange.DetectedByHuman, TaxTypes: []string{"SALES_TAX"}, CreatedBy: analyst,
	}
	c.SourceKey, c.DedupeKey, _ = c.Keys()
	return c
}

func opened(t *testing.T, c regchange.Change) regchange.Change {
	t.Helper()
	events, err := regchange.Open(c, now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	f, err := regchange.Fold(c, events)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	return f
}

func apply(t *testing.T, c regchange.Change) func(regchange.Event, error) regchange.Change {
	t.Helper()
	return func(e regchange.Event, err error) regchange.Change {
		t.Helper()
		if err != nil {
			t.Fatalf("act: %v", err)
		}
		f, err := regchange.Fold(c, append(slices.Clone(c.History), e))
		if err != nil {
			t.Fatalf("fold: %v", err)
		}
		return f
	}
}

// Doc 03 §08 "422 no source/effective date": a change without the captured
// source's digest, an effective date or a monitor's alert is refused.
func TestAChangeNeedsItsSourceAndItsClocks(t *testing.T) {
	cases := map[string]func(*regchange.Change){
		"no digest":           func(c *regchange.Change) { c.Source.DocumentSHA256 = "" },
		"no effective date":   func(c *regchange.Change) { c.EffectiveFrom = time.Time{} },
		"published tomorrow":  func(c *regchange.Change) { c.PublishedOn = date("2026-11-21") },
		"seen before publish": func(c *regchange.Change) { c.DiscoveredAt = date("2026-10-01") },
		"monitor no alert":    func(c *regchange.Change) { c.DetectedBy = regchange.DetectedByMonitor },
		"backdated forwards":  func(c *regchange.Change) { c.Kind = regchange.KindBackdatedRule },
		"bad kind":            func(c *regchange.Change) { c.Kind = "RUMOUR" },
		"http source":         func(c *regchange.Change) { c.Source.URL = "http://example.test" },
	}
	for name, mutate := range cases {
		c := change()
		mutate(&c)
		if err := c.Validate(now); errs.CategoryOf(err) != errs.CategoryValidation {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := change().Validate(now); err != nil {
		t.Fatal(err)
	}
}

// WF-05: a multi-effect source is one change per kind, linked by its source
// key; the same source edition in a different case is the same source.
func TestDedupeKeysAreSourceIdentityVersionAndKind(t *testing.T) {
	a := change()
	b := change()
	b.Kind = regchange.KindBaseChange
	b.SourceKey, b.DedupeKey, _ = b.Keys()
	if !a.SourceKey.Equal(b.SourceKey) || a.DedupeKey.Equal(b.DedupeKey) {
		t.Fatal("two effects of one source share the source key and not the dedupe key")
	}
	c := change()
	c.Source.Authority = "ny dtf"
	c.Title = "A different headline"
	c.SourceKey, c.DedupeKey, _ = c.Keys()
	if !c.DedupeKey.Equal(a.DedupeKey) {
		t.Fatal("the same source edition is the same dedupe key")
	}
	d := change()
	d.Source.Edition = "2026-12"
	d.SourceKey, d.DedupeKey, _ = d.Keys()
	if d.DedupeKey.Equal(a.DedupeKey) {
		t.Fatal("a new edition is a new change")
	}
}

// Z03-AT-09: every change ends with a disposition and rationale, or stays on
// a tracked hold; IN_IMPACT_ASSESSMENT is reached only by an assessment.
func TestTriageHoldAndDisposition(t *testing.T) {
	c := opened(t, change())
	if _, err := c.Transition(regchange.Act{To: regchange.StatusTriaged, Reason: "x", Version: 1}, regchange.Closing{}, analyst, now); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("triage without priority: %v", err)
	}
	if _, err := c.Transition(regchange.Act{To: regchange.StatusTriaged, Priority: regchange.PriorityMaterial, Version: 1}, regchange.Closing{}, analyst, now); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("triage without rationale: %v", err)
	}
	if _, err := c.Transition(regchange.Act{To: regchange.StatusTriaged, Priority: regchange.PriorityMaterial, Reason: "x", Version: 7}, regchange.Closing{}, analyst, now); errs.ReasonOf(err) != regchange.ReasonVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	c = apply(t, c)(c.Transition(regchange.Act{To: regchange.StatusTriaged, Priority: regchange.PriorityMaterial, Reason: "Material NY change", Version: 1}, regchange.Closing{}, analyst, now))
	if p, _ := c.Priority(); p != regchange.PriorityMaterial || c.Status != regchange.StatusTriaged {
		t.Fatalf("%s %s", c.Status, p)
	}
	if _, err := c.Transition(regchange.Act{To: regchange.StatusInAssessment, Reason: "x", Version: 2}, regchange.Closing{}, analyst, now); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("assessment named: %v", err)
	}
	c = apply(t, c)(c.Transition(regchange.Act{To: regchange.StatusOnHold, Hold: regchange.HoldCounselQuestion, Reason: "Ask counsel", Version: 2}, regchange.Closing{}, analyst, now))
	if h, _ := c.Hold(); h != regchange.HoldCounselQuestion {
		t.Fatal(h)
	}
	if _, err := c.Transition(regchange.Act{To: regchange.StatusNoImpact, Reason: "x", Version: 3}, regchange.Closing{}, analyst, now); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("decided while held: %v", err)
	}
	c = apply(t, c)(c.Transition(regchange.Act{To: regchange.StatusTriaged, Reason: "Counsel answered", Version: 3}, regchange.Closing{}, analyst, now))
	aid := id.NewImpactAssessmentID(uuid.MustParse("01920000-0000-7000-8000-00000000a551"))
	e, started, err := c.StartAssessment(aid, analyst, now)
	if err != nil || !started {
		t.Fatal(err)
	}
	c = apply(t, c)(e, nil)
	if _, again, _ := c.StartAssessment(aid, analyst, now); again {
		t.Fatal("a second assessment does not restart")
	}
	closeAs := func(d regchange.Disposition, cl regchange.Closing) error {
		_, err := c.Transition(regchange.Act{To: regchange.StatusClosed, Disposition: d, Reason: "Decided", Version: c.Version()}, cl, analyst, now)
		return err
	}
	if err := closeAs(regchange.DispositionChangeNeeded, regchange.Closing{HasAssessment: true}); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("change needed without an accepted recommendation: %v", err)
	}
	if err := closeAs(regchange.DispositionNotApplicable, regchange.Closing{HasAssessment: true}); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("CLOSED as not applicable: %v", err)
	}
	c = apply(t, c)(c.Transition(regchange.Act{To: regchange.StatusClosed, Disposition: regchange.DispositionChangeNeeded, Reason: "Handed off", Version: c.Version()},
		regchange.Closing{HasAssessment: true, HasAcceptedRecommendation: true}, analyst, now))
	if d, ok := c.Disposition(); !ok || d != regchange.DispositionChangeNeeded {
		t.Fatal(d)
	}
	if err := closeAs(regchange.DispositionMonitor, regchange.Closing{HasAssessment: true}); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("closed twice: %v", err)
	}
}

func TestANewChangeMayBeDismissedButNotAssessedUntriaged(t *testing.T) {
	c := opened(t, change())
	if _, _, err := c.StartAssessment(id.NewImpactAssessmentID(uuid.New()), analyst, now); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("assess untriaged: %v", err)
	}
	c = apply(t, c)(c.Transition(regchange.Act{To: regchange.StatusNoImpact, Reason: "Not telecom", Version: 1}, regchange.Closing{}, analyst, now))
	if d, _ := c.Disposition(); d != regchange.DispositionNotApplicable || !c.Status.Final() {
		t.Fatal(c.Status)
	}
}

func TestFoldRefusesAnInventedHistory(t *testing.T) {
	c := change()
	bad := [][]regchange.Event{
		{{Seq: 1, Kind: regchange.EventTriaged, Status: regchange.StatusTriaged}},
		{{Seq: 1, Kind: regchange.EventRecorded, Status: regchange.StatusNew}, {Seq: 2, Kind: regchange.EventClosed, Status: regchange.StatusClosed, Disposition: regchange.DispositionMonitor}},
		{{Seq: 1, Kind: regchange.EventRecorded, Status: regchange.StatusNew}, {Seq: 3, Kind: regchange.EventTriaged, Status: regchange.StatusTriaged, Priority: regchange.PriorityUrgent}},
	}
	for i, events := range bad {
		if _, err := regchange.Fold(c, events); err == nil {
			t.Errorf("history %d folded", i)
		}
	}
}

func content() regchange.ContentSnapshot {
	return regchange.ContentSnapshot{BundleID: "global-telecom", BundleDigest: "zt1:" + strings.Repeat("b", 64), At: date("2027-01-01"),
		Components: []regchange.ContentComponent{
			{ID: "us-ny-sales", Regime: "us", Jurisdiction: "jurisdiction:us-ny", Authority: "authority:us-ny-dtf", TaxType: "SALES_TAX", Name: "NY sales tax", From: "2020-01-01", Citation: "NY Tax Law §1105"},
			{ID: "us-ny-mctd", Regime: "us", Jurisdiction: "jurisdiction:us-ny-mctd", Authority: "authority:us-ny-dtf", TaxType: "SALES_TAX", Name: "MCTD surcharge", From: "2020-01-01", Citation: "NY Tax Law §1109"},
			{ID: "us-ny-excise", Regime: "us", Jurisdiction: "jurisdiction:us-ny", Authority: "authority:us-ny-dtf", TaxType: "TELECOM_EXCISE", Name: "NY excise", From: "2020-01-01", Citation: "NY Tax Law §186-e"},
			{ID: "us-nj-sales", Regime: "us", Jurisdiction: "jurisdiction:us-nj", Authority: "authority:us-nj", TaxType: "SALES_TAX", Name: "NJ sales tax", From: "2020-01-01", Citation: "NJ"},
		}}
}

func inputs() regchange.Inputs {
	return regchange.Inputs{Change: change(), Content: content(),
		Registrations: []regchange.RegistrationRef{
			{ID: "r-ny", Scheme: "US_STATE_SALES_TAX_PERMIT", Jurisdiction: "US-NY", Authority: "NY DTF", Status: "ACTIVE", Version: 5},
			{ID: "r-ny-old", Scheme: "US_STATE_SALES_TAX_PERMIT", Jurisdiction: "US-NY", Authority: "NY DTF", Status: "REVOKED", Version: 6},
			{ID: "r-ca", Scheme: "US_STATE_SALES_TAX_PERMIT", Jurisdiction: "US-CA", Authority: "CDTFA", Status: "ACTIVE", Version: 5},
		},
		Obligations: []regchange.ObligationRef{
			{ID: "o-1", BusinessKey: "ny-2027-01", Jurisdiction: "jurisdiction:us-ny", Type: "SALES_TAX_RETURN", Status: "OPEN", PeriodStart: date("2027-01-01"), PeriodEnd: date("2027-01-31")},
			{ID: "o-0", BusinessKey: "ny-2026-10", Jurisdiction: "jurisdiction:us-ny", Type: "SALES_TAX_RETURN", Status: "OPEN", PeriodStart: date("2026-10-01"), PeriodEnd: date("2026-10-31")},
		}}
}

// WF-09: the assessment is a pure function — the order inputs arrive in
// does not matter, the same inputs give the same digest, and every item
// carries its basis.
func TestAssessmentIsDeterministicAndExplained(t *testing.T) {
	in := inputs()
	a, err := regchange.Assess(in)
	if err != nil {
		t.Fatal(err)
	}
	refs := []string{}
	for _, it := range a.Items {
		refs = append(refs, string(it.Kind)+":"+it.Ref)
		if it.Basis == "" {
			t.Errorf("%s has no basis", it.Ref)
		}
	}
	want := []string{"CONTENT_COMPONENT:us-ny-mctd", "CONTENT_COMPONENT:us-ny-sales", "TENANT_REGISTRATION:r-ny", "TENANT_OBLIGATION:o-1"}
	if !slices.Equal(refs, want) {
		t.Fatalf("items %v", refs)
	}
	if a.Severity != regchange.SeverityHigh || a.Retroactive {
		t.Fatalf("%s %v", a.Severity, a.Retroactive)
	}
	shuffled := inputs()
	slices.Reverse(shuffled.Content.Components)
	slices.Reverse(shuffled.Registrations)
	slices.Reverse(shuffled.Obligations)
	b, _ := regchange.Assess(shuffled)
	if !a.InputsDigest.Equal(b.InputsDigest) || !a.AssessmentDigest.Equal(b.AssessmentDigest) {
		t.Fatal("the same inputs in another order gave another digest")
	}
	changed := inputs()
	changed.Registrations[0].Version = 6
	c, _ := regchange.Assess(changed)
	if c.InputsDigest.Equal(a.InputsDigest) || c.AssessmentDigest.Equal(a.AssessmentDigest) {
		t.Fatal("a different input gave the same digest")
	}
	stored := regchange.Assessment{Result: a}
	if err := stored.Verify(); err != nil {
		t.Fatal(err)
	}
	stored.Items = slices.Clone(stored.Items)
	stored.Items[0].Severity = regchange.SeverityLow
	if stored.Verify() == nil {
		t.Fatal("an altered stored assessment verified")
	}
}

// Doc 03 §06: a new levy with no matching content is a coverage gap of high
// severity; a backdated rule reaching into an open period is critical.
func TestCoverageGapAndRetrospectivePeriods(t *testing.T) {
	in := inputs()
	in.Change.Kind, in.Change.TaxTypes = regchange.KindNewLevy, []string{"DIGITAL_ACCESS_FEE"}
	in.Registrations, in.Obligations = nil, nil
	a, _ := regchange.Assess(in)
	if len(a.Items) != 1 || a.Items[0].Kind != regchange.ItemCoverageGap || a.Severity != regchange.SeverityHigh {
		t.Fatalf("%+v", a)
	}

	in = inputs()
	in.Change.Kind, in.Change.EffectiveFrom = regchange.KindBackdatedRule, date("2026-09-01")
	a, _ = regchange.Assess(in)
	if !a.Retroactive || a.Severity != regchange.SeverityCritical {
		t.Fatalf("%s %v", a.Severity, a.Retroactive)
	}
	found := false
	for _, it := range a.Items {
		if it.Ref == "o-0" {
			found = it.Retrospective && it.Severity == regchange.SeverityCritical
		}
	}
	if !found {
		t.Fatal("the October period is not retrospective")
	}
}

func assessment(t *testing.T) regchange.Assessment {
	t.Helper()
	r, err := regchange.Assess(inputs())
	if err != nil {
		t.Fatal(err)
	}
	return regchange.Assessment{ID: id.NewImpactAssessmentID(uuid.MustParse("01920000-0000-7000-8000-00000000a551")), Result: r}
}

func recommendation(t *testing.T, a regchange.Assessment, origin regchange.Origin, p *regchange.AIProvenance, c regchange.Content) (regchange.Recommendation, error) {
	t.Helper()
	r := regchange.Recommendation{ID: id.NewReviewRecommendationID(uuid.MustParse("01920000-0000-7000-8000-00000000ec01")),
		Assessment: a.ID, AssessmentDigest: a.AssessmentDigest, Origin: origin, AI: p, CreatedBy: analyst}
	e, err := regchange.Draft(r, c, a, analyst, now)
	if err != nil {
		return regchange.Recommendation{}, err
	}
	return regchange.FoldRecommendation(r, []regchange.RecEvent{e})
}

func recApply(t *testing.T, r regchange.Recommendation) func(regchange.RecEvent, error) regchange.Recommendation {
	t.Helper()
	return func(e regchange.RecEvent, err error) regchange.Recommendation {
		t.Helper()
		if err != nil {
			t.Fatalf("act: %v", err)
		}
		f, err := regchange.FoldRecommendation(r, append(slices.Clone(r.History), e))
		if err != nil {
			t.Fatalf("fold: %v", err)
		}
		return f
	}
}

func update() regchange.Content {
	return regchange.Content{Action: regchange.ActionUpdateRule, Rationale: "Rate rises 1 January",
		Targets: []regchange.Target{{Kind: regchange.ItemContentComponent, Ref: "us-ny-sales"}}}
}

// A recommendation is about what its assessment found, with targets of the
// kind its action needs.
func TestRecommendationContentIsBoundToItsAssessment(t *testing.T) {
	a := assessment(t)
	bad := []regchange.Content{
		{Action: regchange.ActionUpdateRule, Rationale: "x", Targets: []regchange.Target{{Kind: regchange.ItemContentComponent, Ref: "us-nj-sales"}}},
		{Action: regchange.ActionUpdateRule, Rationale: "x", Targets: []regchange.Target{{Kind: regchange.ItemTenantRegistration, Ref: "r-ny"}}},
		{Action: regchange.ActionNotifyCustomers, Rationale: "x"},
		{Action: regchange.ActionRetrospectiveReassessment, Rationale: "x", Targets: []regchange.Target{{Kind: regchange.ItemTenantObligation, Ref: "o-1"}}},
		{Action: regchange.ActionUpdateRule, Targets: []regchange.Target{{Kind: regchange.ItemContentComponent, Ref: "us-ny-sales"}}},
		{Action: "PUBLISH_PACK", Rationale: "x"},
	}
	for i, c := range bad {
		if _, err := recommendation(t, a, regchange.OriginHuman, nil, c); errs.CategoryOf(err) != errs.CategoryValidation {
			t.Errorf("content %d: %v", i, err)
		}
	}
	if _, err := recommendation(t, a, regchange.OriginHuman, nil, regchange.Content{Action: regchange.ActionNoAction, Rationale: "Below threshold"}); err != nil {
		t.Fatal(err)
	}
}

// AIGOV-001 §4, §14: an AI-assisted draft records complete provenance and is
// at most A1 PROPOSE.
func TestAIAssistedRecommendationsCarryProvenance(t *testing.T) {
	a := assessment(t)
	p := &regchange.AIProvenance{UseCase: "regulatory-change-recommendation", ModelProfile: "model-x", ProviderProfile: "provider-y",
		PromptProfile: "rcr-v1", AiTrainVersion: "2026.11", AuthorityOutcome: regchange.AuthorityPropose, Draft: "Update the NY rate."}
	if _, err := recommendation(t, a, regchange.OriginAIAssisted, nil, update()); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("no provenance: %v", err)
	}
	for _, outcome := range []regchange.AuthorityOutcome{"A3", "A5"} {
		q := *p
		q.AuthorityOutcome = outcome
		if _, err := recommendation(t, a, regchange.OriginAIAssisted, &q, update()); errs.ReasonOf(err) != errs.ReasonInvalidValue {
			t.Fatalf("%s: %v", outcome, err)
		}
	}
	if _, err := recommendation(t, a, regchange.OriginHuman, p, update()); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("human with provenance: %v", err)
	}
	if _, err := recommendation(t, a, regchange.OriginAIAssisted, p, update()); err != nil {
		t.Fatal(err)
	}
}

// WF-02: a specialist recommends, an independent person reviews, and
// acceptance is a hand-off reference — on the current assessment only.
func TestFourEyesReviewAndHandOff(t *testing.T) {
	a := assessment(t)
	r, err := recommendation(t, a, regchange.OriginHuman, nil, update())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Decide(regchange.DecisionAccept, "ok", 1, a.ID, second, now); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("review of a draft: %v", err)
	}
	r = recApply(t, r)(r.Recommend("Specialist recommends", 1, a.ID, second, now))
	if _, err := r.Decide(regchange.DecisionAccept, "ok", 2, a.ID, second, now); errs.ReasonOf(err) != regchange.ReasonMakerChecker {
		t.Fatalf("recommender reviews: %v", err)
	}
	if _, err := r.Decide(regchange.DecisionAccept, "ok", 2, a.ID, analyst, now); errs.ReasonOf(err) != regchange.ReasonMakerChecker {
		t.Fatalf("author reviews: %v", err)
	}
	newer := id.NewImpactAssessmentID(uuid.New())
	if _, err := r.Decide(regchange.DecisionAccept, "ok", 2, newer, third, now); errs.ReasonOf(err) != regchange.ReasonAssessmentSuperseded {
		t.Fatalf("superseded: %v", err)
	}
	r = recApply(t, r)(r.Decide(regchange.DecisionRequestChanges, "Cite the rate", 2, a.ID, third, now))
	c := update()
	c.Rationale = "Rate rises to 4.5% on 1 January"
	r = recApply(t, r)(r.Revise(c, a, 3, second, now))
	r = recApply(t, r)(r.Recommend("Revised", 4, a.ID, analyst, now))
	if _, err := r.Decide(regchange.DecisionAccept, "ok", 5, a.ID, second, now); errs.ReasonOf(err) != regchange.ReasonMakerChecker {
		t.Fatalf("reviser reviews: %v", err)
	}
	r = recApply(t, r)(r.Decide(regchange.DecisionAccept, "Agreed", 5, a.ID, third, now))
	ref, ok := r.HandoffRef()
	if !ok || !strings.HasPrefix(ref, "ztx-rcr:"+r.ID.String()+":") {
		t.Fatal(ref)
	}
	if cur, _ := r.Current(); cur.Rationale != c.Rationale {
		t.Fatal(cur.Rationale)
	}
	if _, err := r.Withdraw("late", 6, analyst, now); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("withdraw accepted: %v", err)
	}
}
