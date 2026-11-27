package app_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/regchange"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// memRegChanges is the three regulatory change ledgers in memory,
// append-only, refusing what the unique indexes of migration 000038 refuse.
type memRegChanges struct {
	mu        sync.Mutex
	changes   []regchange.Change
	events    map[id.RegulatoryChangeID][]regchange.Event
	assessed  []regchange.Assessment
	recs      []regchange.Recommendation
	recEvents map[id.ReviewRecommendationID][]regchange.RecEvent
}

func (m *memRegChanges) Lock(context.Context, string) error { return nil }

func (m *memRegChanges) CreateChange(_ context.Context, c regchange.Change, events []regchange.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.changes {
		if o.IntakeKey == c.IntakeKey || o.DedupeKey.Equal(c.DedupeKey) {
			return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "unique")
		}
	}
	if m.events == nil {
		m.events = map[id.RegulatoryChangeID][]regchange.Event{}
	}
	m.changes = append(m.changes, c)
	m.events[c.ID] = slices.Clone(events)
	return nil
}

func (m *memRegChanges) AppendChangeEvents(_ context.Context, cid id.RegulatoryChangeID, events []regchange.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range events {
		if len(m.events[cid])+1 != e.Seq {
			return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "seq")
		}
		m.events[cid] = append(m.events[cid], e)
	}
	return nil
}

func (m *memRegChanges) folded(match func(regchange.Change) bool) []regchange.Change {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []regchange.Change
	for _, c := range m.changes {
		if match(c) {
			f, err := regchange.Fold(c, m.events[c.ID])
			if err != nil {
				panic(err)
			}
			out = append(out, f)
		}
	}
	slices.Reverse(out)
	return out
}

func (m *memRegChanges) Change(_ context.Context, cid id.RegulatoryChangeID) (regchange.Change, error) {
	got := m.folded(func(c regchange.Change) bool { return c.ID == cid })
	if len(got) == 0 {
		return regchange.Change{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
	}
	return got[0], nil
}

func (m *memRegChanges) ChangeByIntakeKey(_ context.Context, key string) (regchange.Change, bool, error) {
	got := m.folded(func(c regchange.Change) bool { return c.IntakeKey == key })
	return first(got)
}

func (m *memRegChanges) ChangeByDedupeKey(_ context.Context, key canonical.Digest) (regchange.Change, bool, error) {
	got := m.folded(func(c regchange.Change) bool { return c.DedupeKey.Equal(key) })
	return first(got)
}

func first(got []regchange.Change) (regchange.Change, bool, error) {
	if len(got) == 0 {
		return regchange.Change{}, false, nil
	}
	return got[0], true, nil
}

func (m *memRegChanges) ChangesBySource(_ context.Context, key canonical.Digest) ([]regchange.Change, error) {
	return m.folded(func(c regchange.Change) bool { return c.SourceKey.Equal(key) }), nil
}

func (m *memRegChanges) Changes(context.Context, int) ([]regchange.Change, error) {
	return m.folded(func(regchange.Change) bool { return true }), nil
}

func (m *memRegChanges) CreateAssessment(_ context.Context, a regchange.Assessment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assessed = append(m.assessed, a)
	return nil
}

func (m *memRegChanges) assessments(match func(regchange.Assessment) bool) []regchange.Assessment {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []regchange.Assessment
	for i := len(m.assessed) - 1; i >= 0; i-- {
		if match(m.assessed[i]) {
			out = append(out, m.assessed[i])
		}
	}
	return out
}

func (m *memRegChanges) Assessment(_ context.Context, aid id.ImpactAssessmentID) (regchange.Assessment, error) {
	got := m.assessments(func(a regchange.Assessment) bool { return a.ID == aid })
	if len(got) == 0 {
		return regchange.Assessment{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
	}
	return got[0], nil
}

func (m *memRegChanges) AssessmentsOf(_ context.Context, cid id.RegulatoryChangeID) ([]regchange.Assessment, error) {
	return m.assessments(func(a regchange.Assessment) bool { return a.Change == cid }), nil
}

func (m *memRegChanges) Assessments(context.Context, int) ([]regchange.Assessment, error) {
	return m.assessments(func(regchange.Assessment) bool { return true }), nil
}

func (m *memRegChanges) CreateRecommendation(_ context.Context, r regchange.Recommendation, e regchange.RecEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recEvents == nil {
		m.recEvents = map[id.ReviewRecommendationID][]regchange.RecEvent{}
	}
	m.recs = append(m.recs, r)
	m.recEvents[r.ID] = []regchange.RecEvent{e}
	return nil
}

func (m *memRegChanges) AppendRecommendationEvent(_ context.Context, rid id.ReviewRecommendationID, e regchange.RecEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.recEvents[rid])+1 != e.Seq {
		return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "seq")
	}
	m.recEvents[rid] = append(m.recEvents[rid], e)
	return nil
}

func (m *memRegChanges) foldedRecs(match func(regchange.Recommendation) bool) []regchange.Recommendation {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []regchange.Recommendation
	for _, r := range m.recs {
		if match(r) {
			f, err := regchange.FoldRecommendation(r, m.recEvents[r.ID])
			if err != nil {
				panic(err)
			}
			out = append(out, f)
		}
	}
	slices.Reverse(out)
	return out
}

func (m *memRegChanges) Recommendation(_ context.Context, rid id.ReviewRecommendationID) (regchange.Recommendation, error) {
	got := m.foldedRecs(func(r regchange.Recommendation) bool { return r.ID == rid })
	if len(got) == 0 {
		return regchange.Recommendation{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
	}
	return got[0], nil
}

func (m *memRegChanges) RecommendationByIntakeKey(_ context.Context, key string) (regchange.Recommendation, bool, error) {
	got := m.foldedRecs(func(r regchange.Recommendation) bool { return r.IntakeKey == key })
	if len(got) == 0 {
		return regchange.Recommendation{}, false, nil
	}
	return got[0], true, nil
}

func (m *memRegChanges) RecommendationsOf(_ context.Context, cid id.RegulatoryChangeID) ([]regchange.Recommendation, error) {
	return m.foldedRecs(func(r regchange.Recommendation) bool { return r.Change == cid }), nil
}

func (m *memRegChanges) Recommendations(context.Context, int) ([]regchange.Recommendation, error) {
	return m.foldedRecs(func(regchange.Recommendation) bool { return true }), nil
}

// fixedCoverage is a content bundle's coverage, settable per test.
type fixedCoverage struct{ snap *regchange.ContentSnapshot }

func (f fixedCoverage) CoverageAt(t time.Time) (regchange.ContentSnapshot, error) {
	if f.snap == nil {
		return regchange.ContentSnapshot{}, errs.New(errs.CategoryUnavailable, errs.ReasonNoContentBundle, "none")
	}
	s := *f.snap
	s.At = t
	return s, nil
}

// fixedRegistrations and fixedObligations serve listings only.
type fixedRegistrations struct {
	port.RegistrationRepository
	regs *[]registration.Registration
}

func (f fixedRegistrations) Registrations(context.Context, int) ([]registration.Registration, error) {
	return slices.Clone(*f.regs), nil
}

type fixedObligations struct {
	port.ObligationRepository
	obls []obligation.Obligation
}

func (f fixedObligations) List(context.Context, port.ObligationFilter) ([]obligation.Obligation, error) {
	return f.obls, nil
}

var (
	rcOperator = ctbPerson("01920000-0000-7000-8000-0000000000f1", security.RoleOperator)
	rcAnalyst  = ctbPerson("01920000-0000-7000-8000-0000000000f2", security.RoleAnalyst)
	rcAnalyst2 = ctbPerson("01920000-0000-7000-8000-0000000000f3", security.RoleAnalyst)
	rcAnalyst3 = ctbPerson("01920000-0000-7000-8000-0000000000f4", security.RoleAnalyst)
	rcAuditor  = ctbPerson("01920000-0000-7000-8000-0000000000f5", security.RoleAuditor)
	rcAdmin    = ctbPerson("01920000-0000-7000-8000-0000000000f6", security.RoleAdmin)
)

type rcHarness struct {
	svc   *app.RegulatoryChangeService
	store *memRegChanges
	regs  *[]registration.Registration
}

func newRegChanges(t *testing.T) rcHarness {
	t.Helper()
	snap := &regchange.ContentSnapshot{BundleID: "global-telecom", BundleDigest: "zt1:" + strings.Repeat("b", 64),
		Components: []regchange.ContentComponent{
			{ID: "us-ny-excise", Regime: "us", Jurisdiction: "jurisdiction:us-ny", Authority: "authority:us-ny-dtf", TaxType: "TELECOM_EXCISE", Name: "NY excise", From: "2020-01-01", Citation: "NY Tax Law §186-e"},
			{ID: "us-nj-sales", Regime: "us", Jurisdiction: "jurisdiction:us-nj", Authority: "authority:us-nj", TaxType: "SALES_TAX", Name: "NJ sales", From: "2020-01-01", Citation: "NJ"},
		}}
	regs := &[]registration.Registration{}
	store := &memRegChanges{}
	clk := &settableClock{t: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)}
	svc := app.NewRegulatoryChangeService(store, fixedCoverage{snap: snap}, fixedRegistrations{regs: regs}, fixedObligations{}, noTx{}, clk, &idgen.Sequential{})
	return rcHarness{svc: svc, store: store, regs: regs}
}

func nyChange(key string) app.ChangeInput {
	return app.ChangeInput{IntakeKey: key, Jurisdiction: "us-ny", Kind: regchange.KindRateChange, Title: "NY excise rises",
		Source:      regchange.Source{Authority: "NY DTF", Citation: "N.Y. Tax Law §186-e", Edition: "TB-2026-11", DocumentSHA256: strings.Repeat("a", 64)},
		PublishedOn: day("2026-11-01"), EffectiveFrom: day("2027-01-01"), DetectedBy: regchange.DetectedByHuman, TaxTypes: []string{"telecom_excise"}}
}

// Doc 03 §08: intake is idempotent on its key, deduplicated by source
// identity, version and kind, and a conflicting dedupe is refused.
func TestAChangeIsRecordedOnceBySourceEditionAndKind(t *testing.T) {
	h := newRegChanges(t)
	v, created, err := h.svc.RecordChange(rcOperator, nyChange("k-1"))
	if err != nil || !created || v.Status != regchange.StatusNew || v.Jurisdiction != "US-NY" || !slices.Equal(v.TaxTypes, []string{"TELECOM_EXCISE"}) {
		t.Fatalf("record: %v %v %+v", created, err, v.Change)
	}
	if again, created, err := h.svc.RecordChange(rcOperator, nyChange("k-2")); err != nil || created || again.ID != v.ID {
		t.Fatalf("dedupe: %v %v", created, err)
	}
	changed := nyChange("k-3")
	changed.Title = "A different reading"
	if _, _, err := h.svc.RecordChange(rcOperator, changed); errs.ReasonOf(err) != regchange.ReasonDedupeConflict {
		t.Fatalf("conflict: %v", err)
	}
	if _, _, err := h.svc.RecordChange(rcOperator, changed); errs.ReasonOf(err) != regchange.ReasonDedupeConflict {
		t.Fatalf("conflict again: %v", err)
	}
	other := nyChange("k-1")
	other.Kind = regchange.KindBaseChange
	if _, _, err := h.svc.RecordChange(rcOperator, other); errs.ReasonOf(err) != errs.ReasonIdempotencyKeyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	other.IntakeKey = "k-4"
	linked, created, err := h.svc.RecordChange(rcOperator, other)
	if err != nil || !created {
		t.Fatal(err)
	}
	got, _ := h.svc.Change(rcAuditor, linked.ID)
	if !slices.Equal(got.Related, []id.RegulatoryChangeID{v.ID}) {
		t.Fatalf("related %v", got.Related)
	}
	if _, err := h.svc.Changes(rcAdmin, regchange.Filter{}, 0); errs.CategoryOf(err) != errs.CategoryPolicy {
		t.Fatalf("admin reads: %v", err)
	}
	if _, _, err := h.svc.RecordChange(rcAuditor, nyChange("k-5")); errs.CategoryOf(err) != errs.CategoryPolicy {
		t.Fatalf("auditor records: %v", err)
	}
}

// WF-05 → WF-09 → WF-02 end to end: triage, a deterministic assessment, an
// AI-assisted draft recommended by one person and accepted by another,
// and closure as CHANGE_NEEDED — the change is handed off, not made.
func TestChangeAssessRecommendAndHandOff(t *testing.T) {
	h := newRegChanges(t)
	c, _, err := h.svc.RecordChange(rcOperator, nyChange("k-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.AssessChange(rcAnalyst, c.ID); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("assess untriaged: %v", err)
	}
	if _, err := h.svc.TransitionChange(rcOperator, c.ID, regchange.Act{To: regchange.StatusTriaged, Priority: regchange.PriorityUrgent, Reason: "x", Version: 1}); errs.CategoryOf(err) != errs.CategoryPolicy {
		t.Fatalf("operator triages: %v", err)
	}
	if _, err := h.svc.TransitionChange(rcAnalyst, c.ID, regchange.Act{To: regchange.StatusTriaged, Priority: regchange.PriorityMaterial, Reason: "Covered levy", Version: 1}); err != nil {
		t.Fatal(err)
	}
	a, created, err := h.svc.AssessChange(rcAnalyst, c.ID)
	if err != nil || !created || a.Severity != regchange.SeverityHigh || len(a.Items) != 1 || a.Items[0].Ref != "us-ny-excise" {
		t.Fatalf("assess: %v %v %+v", created, err, a.Result)
	}
	again, created, err := h.svc.AssessChange(rcAnalyst, c.ID)
	if err != nil || created || again.ID != a.ID {
		t.Fatalf("reassess on the same inputs: %v %v", created, err)
	}
	if got, _ := h.svc.Change(rcAnalyst, c.ID); got.Status != regchange.StatusInAssessment || got.CurrentAssessment != a.ID {
		t.Fatalf("%s", got.Status)
	}

	in := app.RecommendationInput{IntakeKey: "r-1", Assessment: a.ID, Origin: regchange.OriginAIAssisted,
		AI: &regchange.AIProvenance{UseCase: "regulatory-change-recommendation", ModelProfile: "m", ProviderProfile: "p",
			PromptProfile: "rcr-v1", AiTrainVersion: "2026.11", AuthorityOutcome: regchange.AuthorityPropose, Draft: "Update the rate."},
		Content: regchange.Content{Action: regchange.ActionUpdateRule, Rationale: "Rate rises 1 January",
			Targets: []regchange.Target{{Kind: regchange.ItemContentComponent, Ref: "us-ny-excise"}}}}
	r, created, err := h.svc.DraftRecommendation(rcAnalyst, in)
	if err != nil || !created || r.Status != regchange.RecDraft {
		t.Fatalf("draft: %v %v", created, err)
	}
	if same, created, err := h.svc.DraftRecommendation(rcAnalyst, in); err != nil || created || same.ID != r.ID {
		t.Fatalf("draft replay: %v %v", created, err)
	}
	if _, err := h.svc.TransitionChange(rcAnalyst, c.ID, regchange.Act{To: regchange.StatusClosed, Disposition: regchange.DispositionChangeNeeded, Reason: "x", Version: 3}); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("close before acceptance: %v", err)
	}
	if _, err := h.svc.RecommendRecommendation(rcAnalyst2, r.ID, "Checked", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ReviewRecommendation(rcAnalyst2, r.ID, regchange.DecisionAccept, "ok", 2); errs.ReasonOf(err) != regchange.ReasonMakerChecker {
		t.Fatalf("recommender reviews: %v", err)
	}
	if _, err := h.svc.ReviewRecommendation(rcAnalyst, r.ID, regchange.DecisionAccept, "ok", 2); errs.ReasonOf(err) != regchange.ReasonMakerChecker {
		t.Fatalf("author reviews: %v", err)
	}
	done, err := h.svc.ReviewRecommendation(rcAnalyst3, r.ID, regchange.DecisionAccept, "Independent agreement", 2)
	if err != nil {
		t.Fatal(err)
	}
	if ref, ok := done.HandoffRef(); !ok || !strings.HasPrefix(ref, "ztx-rcr:"+r.ID.String()) {
		t.Fatal(ref)
	}
	closed, err := h.svc.TransitionChange(rcAnalyst, c.ID, regchange.Act{To: regchange.StatusClosed, Disposition: regchange.DispositionChangeNeeded, Reason: "Handed off", Version: 3})
	if err != nil || closed.Status != regchange.StatusClosed || closed.Accepted != 1 {
		t.Fatalf("close: %v %+v", err, closed)
	}
}

// A reassessment on changed inputs supersedes the old assessment: a
// recommendation made on it can no longer be recommended or reviewed.
func TestAReassessmentSupersedesOpenRecommendations(t *testing.T) {
	h := newRegChanges(t)
	c, _, _ := h.svc.RecordChange(rcOperator, nyChange("k-1"))
	if _, err := h.svc.TransitionChange(rcAnalyst, c.ID, regchange.Act{To: regchange.StatusTriaged, Priority: regchange.PriorityMaterial, Reason: "x", Version: 1}); err != nil {
		t.Fatal(err)
	}
	a, _, _ := h.svc.AssessChange(rcAnalyst, c.ID)
	r, _, err := h.svc.DraftRecommendation(rcAnalyst, app.RecommendationInput{IntakeKey: "r-1", Assessment: a.ID, Origin: regchange.OriginHuman,
		Content: regchange.Content{Action: regchange.ActionMonitor, Rationale: "Watch"}})
	if err != nil {
		t.Fatal(err)
	}
	*h.regs = append(*h.regs, registration.Registration{ID: id.NewRegistrationID(uuid.MustParse("01920000-0000-7000-8000-0000000000dd")), Scheme: registration.SchemeUSStateTelecom,
		Jurisdiction: "US-NY", Authority: "NY PSC", Status: registration.StatusActive,
		History: []registration.Event{{Seq: 1}}})
	b, created, err := h.svc.AssessChange(rcAnalyst, c.ID)
	if err != nil || !created || b.ID == a.ID || len(b.Items) != 2 {
		t.Fatalf("reassess: %v %v %d", created, err, len(b.Items))
	}
	if _, err := h.svc.RecommendRecommendation(rcAnalyst2, r.ID, "late", 1); errs.ReasonOf(err) != regchange.ReasonAssessmentSuperseded {
		t.Fatalf("superseded: %v", err)
	}
	got, _ := h.svc.Recommendation(rcAuditor, r.ID)
	if !got.Stale() {
		t.Fatal("not stale")
	}
	if _, _, err := h.svc.DraftRecommendation(rcAnalyst, app.RecommendationInput{IntakeKey: "r-2", Assessment: a.ID, Origin: regchange.OriginHuman,
		Content: regchange.Content{Action: regchange.ActionMonitor, Rationale: "Watch"}}); errs.ReasonOf(err) != regchange.ReasonAssessmentSuperseded {
		t.Fatalf("draft on old: %v", err)
	}
	if _, err := h.svc.WithdrawRecommendation(rcAnalyst, r.ID, "Superseded", 1); err != nil {
		t.Fatal(err)
	}
	views, _ := h.svc.Assessments(rcAuditor, c.ID, "", 0)
	if len(views) != 2 || !views[0].Current || views[1].Current {
		t.Fatalf("%+v", views)
	}
}
