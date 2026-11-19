package app_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/contribution"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/workpaper"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// memWorkpapers is the workpaper store in memory: append-only, one
// workpaper per filer, duty and period, and builds and events in sequence,
// as the keys of migration 000032 are.
type memWorkpapers struct {
	mu      sync.Mutex
	headers []workpaper.Workpaper
	events  map[id.WorkpaperID][]workpaper.Event
	builds  map[id.WorkpaperID][]workpaper.Build
}

func (m *memWorkpapers) Lock(context.Context, string) error { return nil }

func (m *memWorkpapers) Create(_ context.Context, w workpaper.Workpaper, e workpaper.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range m.headers {
		if h.LegalEntity == w.LegalEntity && h.Duty == w.Duty && h.Period == w.Period {
			return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "exists")
		}
	}
	if m.events == nil {
		m.events, m.builds = map[id.WorkpaperID][]workpaper.Event{}, map[id.WorkpaperID][]workpaper.Build{}
	}
	m.headers = append(m.headers, w)
	m.events[w.ID] = []workpaper.Event{e}
	return nil
}

func (m *memWorkpapers) fold(h workpaper.Workpaper) workpaper.Workpaper {
	w, err := workpaper.Fold(h, m.events[h.ID], m.builds[h.ID])
	if err != nil {
		panic(err)
	}
	return w
}

func (m *memWorkpapers) ByKey(_ context.Context, le id.LegalEntityID, duty contribution.Regime, period string) (workpaper.Workpaper, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range m.headers {
		if h.LegalEntity == le && h.Duty == duty && h.Period == period {
			return m.fold(h), true, nil
		}
	}
	return workpaper.Workpaper{}, false, nil
}

func (m *memWorkpapers) Workpaper(_ context.Context, wid id.WorkpaperID) (workpaper.Workpaper, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range m.headers {
		if h.ID == wid {
			return m.fold(h), nil
		}
	}
	return workpaper.Workpaper{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
}

func (m *memWorkpapers) List(_ context.Context, f workpaper.Filter) ([]workpaper.Workpaper, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []workpaper.Workpaper
	for _, h := range m.headers {
		w := m.fold(h)
		if (f.Period == "" || w.Period == f.Period) && (f.Duty == "" || w.Duty == f.Duty) && (f.Status == "" || w.Status == f.Status) {
			out = append(out, w)
		}
	}
	return out, nil
}

func (m *memWorkpapers) AppendBuild(_ context.Context, wid id.WorkpaperID, b workpaper.Build) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.builds[wid])+1 != b.Version {
		return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "version")
	}
	// What the store keeps is the canonical document; read it back the way
	// the store does, checked against its hash.
	body, err := workpaper.EncodeOutput(*b.Output)
	if err != nil {
		return err
	}
	out, err := workpaper.DecodeOutput(body, b.OutputHash)
	if err != nil {
		return err
	}
	b.Output = &out
	m.builds[wid] = append(m.builds[wid], b)
	return nil
}

func (m *memWorkpapers) AppendEvents(_ context.Context, wid id.WorkpaperID, events []workpaper.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range events {
		if len(m.events[wid])+1 != e.Seq {
			return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "seq")
		}
		m.events[wid] = append(m.events[wid], e)
	}
	return nil
}

type memPopulation struct{ balances []subledger.ManifestBalance }

func (m *memPopulation) Population(context.Context, id.LegalEntityID, string) (port.PeriodPopulation, error) {
	return port.PeriodPopulation{Balances: m.balances}, nil
}

type wpHarness struct {
	svc       *app.WorkpaperService
	ledger    *app.ProviderContributionService
	store     *memWorkpapers
	subledger *memPopulation
}

func newWorkpapers(t *testing.T) wpHarness {
	t.Helper()
	contribs := &memContributions{}
	clk := &settableClock{t: time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)}
	h := wpHarness{store: &memWorkpapers{}, subledger: &memPopulation{}}
	h.ledger = app.NewProviderContributionService(contribs, ctbEntities{def: ctbEntity}, noTx{}, clk, &idgen.Sequential{})
	h.svc = app.NewWorkpaperService(h.store, contribs, h.subledger, ctbEntities{def: ctbEntity}, noTx{}, clk, &idgen.Sequential{})
	return h
}

var (
	wpAuditor  = ctbPerson("01920000-0000-7000-8000-0000000000b5", security.RoleAuditor)
	wpEvidence = &workpaper.Evidence{SHA256: strings.Repeat("0f", 32), Filename: "evidence.pdf", MediaType: "application/pdf"}
)

func usfWorkpaper() app.WorkpaperInput {
	return app.WorkpaperInput{Duty: contribution.RegimeUSUSF, Period: "2026-09", Authority: "USAC", ReportingCurrency: "USD",
		Materiality: "500.00", ContentRelease: "usf-499q@2026.3"}
}

func mustAct(ctx context.Context, t *testing.T, h wpHarness, wid id.WorkpaperID, a app.WorkpaperAct) app.WorkpaperView {
	t.Helper()
	v, err := h.svc.Act(ctx, wid, a)
	if err != nil {
		t.Fatalf("%s: %v", a.Action, err)
	}
	return v
}

// OBL-07: one workpaper per filer, duty and period; a build is idempotent on
// its inputs; freeze locks the snapshot; a second person approves; a new
// source event revokes the approval on the next build.
func TestAWorkpaperIsBuiltFrozenAndApproved(t *testing.T) {
	h := newWorkpapers(t)
	if _, _, err := h.ledger.Record(ctbOperator, usf("E-1", "1000.00", "100.00")); err != nil {
		t.Fatal(err)
	}
	w, created, err := h.svc.Create(ctbOperator, usfWorkpaper())
	if err != nil || !created || w.LegalEntity != ctbEntity.ID || w.Status != workpaper.StatusDraft {
		t.Fatalf("create: created %v status %s, %v", created, w.Status, err)
	}
	again, created, err := h.svc.Create(ctbAnalyst, usfWorkpaper())
	if err != nil || created || again.ID != w.ID {
		t.Fatalf("a repeated create: created %v, %v", created, err)
	}
	changed := usfWorkpaper()
	changed.Authority = "FCC"
	if _, _, err := h.svc.Create(ctbOperator, changed); errs.ReasonOf(err) != workpaper.ReasonWorkpaperExists {
		t.Fatalf("a different header: %v", err)
	}

	v, created, err := h.svc.Build(ctbOperator, w.ID, 1)
	if err != nil || !created || v.Status != workpaper.StatusDraft {
		t.Fatalf("build: created %v status %s exceptions %+v, %v", created, v.Status, v.Exceptions, err)
	}
	o := v.Latest().Output
	if o.Totals.GrossEligible != "1000.00" || o.Totals.Excluded != "100.00" || len(o.Revenue[0].Sources) != 1 {
		t.Fatalf("output %+v", o.Totals)
	}
	same, created, err := h.svc.Build(ctbAnalyst, w.ID, 0)
	if err != nil || created || len(same.Builds) != 1 || len(same.History) != len(v.History) {
		t.Fatalf("a rebuild on the same inputs recorded something: created %v builds %d, %v", created, len(same.Builds), err)
	}
	if _, _, err := h.svc.Build(ctbOperator, w.ID, 1); errs.ReasonOf(err) != workpaper.ReasonStale {
		t.Fatalf("a stale build: %v", err)
	}

	v = mustAct(ctbAnalyst, t, h, w.ID, app.WorkpaperAct{Action: workpaper.ActionFreeze, ExpectedSeq: 2})
	if v.Status != workpaper.StatusReadyForReview || v.FrozenBuild != 1 {
		t.Fatalf("freeze: %s", v.Status)
	}
	// A source moves after the freeze: the snapshot stands, a rebuild is
	// refused, and the source check says so.
	if _, _, err := h.ledger.Record(ctbOperator, usf("E-2", "5.00", "0.00")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.Build(ctbOperator, w.ID, 0); errs.ReasonOf(err) != workpaper.ReasonSnapshotLocked {
		t.Fatalf("rebuilt a frozen workpaper: %v", err)
	}
	check, err := h.svc.SourceCheck(wpAuditor, w.ID)
	if err != nil || check.Matches || check.FrozenVersion != 1 {
		t.Fatalf("source check: %+v, %v", check, err)
	}
	if _, err := h.svc.Act(ctbOperator, w.ID, app.WorkpaperAct{Action: workpaper.ActionApprove, BuildVersion: 1}); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an operator approved: %v", err)
	}
	if _, err := h.svc.Act(ctbAnalyst, w.ID, app.WorkpaperAct{Action: workpaper.ActionApprove, BuildVersion: 1}); errs.ReasonOf(err) != workpaper.ReasonSegregationOfDuties {
		t.Fatalf("the freezer approved: %v", err)
	}
	v = mustAct(ctbAnalyst2, t, h, w.ID, app.WorkpaperAct{Action: workpaper.ActionApprove, BuildVersion: 1, Note: "Tied out"})
	if v.Status != workpaper.StatusApproved {
		t.Fatalf("approve: %s", v.Status)
	}

	v, created, err = h.svc.Build(ctbOperator, w.ID, 0)
	if err != nil || !created || v.Status != workpaper.StatusDraft || len(v.Builds) != 2 {
		t.Fatalf("rebuild after approval: created %v status %s, %v", created, v.Status, err)
	}
	if k := v.History[len(v.History)-2].Kind; k != workpaper.EventApprovalRevoked {
		t.Fatalf("the approval was not revoked first: %s", k)
	}
	if v.Latest().Output.Totals.GrossEligible != "1005.00" {
		t.Fatalf("gross %s", v.Latest().Output.Totals.GrossEligible)
	}
}

// OBL-07 "Source missing": a period with no source event is DATA_INCOMPLETE
// and never reaches READY_FOR_REVIEW until two people confirm, with
// evidence, that it is a nil period.
func TestASourceMissingPeriodNeedsTwoPeopleToFreeze(t *testing.T) {
	h := newWorkpapers(t)
	w, _, err := h.svc.Create(ctbOperator, usfWorkpaper())
	if err != nil {
		t.Fatal(err)
	}
	v, _, err := h.svc.Build(ctbOperator, w.ID, 0)
	if err != nil || v.Status != workpaper.StatusDataIncomplete || len(v.Exceptions) != 1 {
		t.Fatalf("status %s, %v", v.Status, err)
	}
	if _, err := h.svc.Act(ctbOperator, w.ID, app.WorkpaperAct{Action: workpaper.ActionFreeze}); errs.ReasonOf(err) != workpaper.ReasonNotReady {
		t.Fatalf("froze with no source: %v", err)
	}
	x := v.Exceptions[0]
	v = mustAct(ctbOperator, t, h, w.ID, app.WorkpaperAct{Action: workpaper.ActionProposeResolution, ExceptionKey: x.Key,
		Fingerprint: x.Fingerprint, Note: "Nil period: the filer had no USF-eligible revenue in September", Evidence: wpEvidence})
	if !v.Exceptions[0].Pending() || !slices.Contains(v.Blockers, workpaper.BlockerResolutionPending) {
		t.Fatalf("pending: %+v %v", v.Exceptions[0], v.Blockers)
	}
	if _, err := h.svc.Act(ctbOperator, w.ID, app.WorkpaperAct{Action: workpaper.ActionDecideResolution, ExceptionKey: x.Key,
		Decision: workpaper.DecisionApproved}); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an operator decided: %v", err)
	}
	v = mustAct(ctbAnalyst, t, h, w.ID, app.WorkpaperAct{Action: workpaper.ActionDecideResolution, ExceptionKey: x.Key,
		Decision: workpaper.DecisionApproved, Note: "Checked the billing extract"})
	if v.Status != workpaper.StatusDraft || len(v.Blockers) != 0 {
		t.Fatalf("after confirmation: %s %v", v.Status, v.Blockers)
	}
	v = mustAct(ctbOperator, t, h, w.ID, app.WorkpaperAct{Action: workpaper.ActionFreeze})
	if v.Status != workpaper.StatusReadyForReview {
		t.Fatalf("freeze: %s", v.Status)
	}
	v = mustAct(ctbAnalyst, t, h, w.ID, app.WorkpaperAct{Action: workpaper.ActionReturnToDraft, Note: "Wait for the October import"})
	if v.Status != workpaper.StatusDraft || v.FrozenBuild != 0 {
		t.Fatalf("return: %s", v.Status)
	}
}

// A material adjustment blocks the freeze until a second person decides it,
// and an approved one needs a rebuild before the freeze.
func TestAMaterialAdjustmentBlocksTheFreeze(t *testing.T) {
	h := newWorkpapers(t)
	if _, _, err := h.ledger.Record(ctbOperator, usf("E-1", "1000.00", "0.00")); err != nil {
		t.Fatal(err)
	}
	w, _, _ := h.svc.Create(ctbOperator, usfWorkpaper())
	if _, _, err := h.svc.Build(ctbOperator, w.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Act(ctbOperator, w.ID, app.WorkpaperAct{Action: workpaper.ActionProposeAdjustment,
		Adjustment: &app.AdjustmentInput{Kind: workpaper.KindAdjustment, Category: "Billing correction", Amount: "-600.00",
			Reason: "Credit notes"}}); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("an adjustment with no evidence: %v", err)
	}
	if _, err := h.svc.Act(ctbOperator, w.ID, app.WorkpaperAct{Action: workpaper.ActionProposeAdjustment,
		Adjustment: &app.AdjustmentInput{Kind: workpaper.KindAdjustment, Category: "x", Amount: "-6e2", Reason: "r"},
		Evidence:   wpEvidence}); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("an amount that is not a decimal string: %v", err)
	}
	v := mustAct(ctbOperator, t, h, w.ID, app.WorkpaperAct{Action: workpaper.ActionProposeAdjustment,
		Adjustment: &app.AdjustmentInput{Kind: workpaper.KindAdjustment, Category: "Billing correction", Amount: "-600.00",
			Reason: "Credit notes issued in October for September usage"}, Evidence: wpEvidence})
	if !slices.Contains(v.Blockers, workpaper.BlockerAdjustmentPending) || !v.Adjustments[0].Material {
		t.Fatalf("blockers %v", v.Blockers)
	}
	if _, err := h.svc.Act(ctbOperator, w.ID, app.WorkpaperAct{Action: workpaper.ActionFreeze}); errs.ReasonOf(err) != workpaper.ReasonNotReady {
		t.Fatalf("froze with a pending adjustment: %v", err)
	}
	v = mustAct(ctbAnalyst, t, h, w.ID, app.WorkpaperAct{Action: workpaper.ActionDecideAdjustment, AdjustmentNumber: 1,
		Decision: workpaper.DecisionApproved})
	if !slices.Equal(v.Blockers, []workpaper.Blocker{workpaper.BlockerBasisChanged}) {
		t.Fatalf("blockers after decision %v", v.Blockers)
	}
	v, _, err := h.svc.Build(ctbOperator, w.ID, 0)
	if err != nil || v.Latest().Output.Totals.ReportableBase != "400.00" || len(v.Blockers) != 0 {
		t.Fatalf("after rebuild: %+v %v, %v", v.Latest().Output.Totals, v.Blockers, err)
	}
}

// OBL-07 "Currency mismatch": a documented FX rate and conversion basis are
// required; they are documented in the API's own form.
func TestACurrencyMismatchIsClearedByADocumentedRate(t *testing.T) {
	h := newWorkpapers(t)
	in := usf("C-1", "100.00", "0.00")
	in.Currency = "CAD"
	if _, _, err := h.ledger.Record(ctbOperator, in); err != nil {
		t.Fatal(err)
	}
	w, _, _ := h.svc.Create(ctbOperator, usfWorkpaper())
	v, _, err := h.svc.Build(ctbOperator, w.ID, 0)
	if err != nil || v.Status != workpaper.StatusDataIncomplete || v.Exceptions[0].Code != workpaper.ExceptionCurrencyMismatch {
		t.Fatalf("status %s, %v", v.Status, err)
	}
	if _, err := h.svc.Act(ctbOperator, w.ID, app.WorkpaperAct{Action: workpaper.ActionDocumentFX,
		FX: &app.FXInput{Currency: "CAD", Rate: "-1", RateSource: "BoC", RateDate: "2026-09-30", RoundingMode: "HALF_EVEN", Scale: 2,
			ConversionBasis: "Period-end"}}); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("a negative rate: %v", err)
	}
	mustAct(ctbOperator, t, h, w.ID, app.WorkpaperAct{Action: workpaper.ActionDocumentFX,
		FX: &app.FXInput{Currency: "cad", Rate: "0.73", RateSource: "Bank of Canada", RateDate: "2026-09-30", RoundingMode: "HALF_EVEN",
			Scale: 2, ConversionBasis: "Period-end rate; each schedule line converted and rounded once"}})
	v, _, err = h.svc.Build(ctbOperator, w.ID, 0)
	if err != nil || v.Status != workpaper.StatusDraft || v.Latest().Output.Totals.GrossEligible != "73.00" {
		t.Fatalf("after documenting: %s %+v, %v", v.Status, v.Latest().Output.Totals, err)
	}
}

// Subledger suspense in the period is an exception of the workpaper.
func TestSubledgerSuspenseIsAnException(t *testing.T) {
	h := newWorkpapers(t)
	if _, _, err := h.ledger.Record(ctbOperator, usf("E-1", "10.00", "0.00")); err != nil {
		t.Fatal(err)
	}
	h.subledger.balances = []subledger.ManifestBalance{{Account: subledger.AccountCustomerGLBridge, Currency: "USD",
		Debits: fiscaltest.Money(t, "12.00", "USD"), Credits: fiscaltest.Money(t, "10.00", "USD")}}
	w, _, _ := h.svc.Create(ctbOperator, usfWorkpaper())
	v, _, err := h.svc.Build(ctbOperator, w.ID, 0)
	if err != nil || len(v.Exceptions) != 1 || v.Exceptions[0].Code != workpaper.ExceptionGLBridgeDifference || len(v.Latest().Output.Subledger) != 1 {
		t.Fatalf("exceptions %+v, %v", v.Exceptions, err)
	}
}

// ADMIN reads no workpaper; an AUDITOR reads and does not prepare.
func TestWorkpaperRoles(t *testing.T) {
	h := newWorkpapers(t)
	if _, err := h.svc.Workpapers(ctbAdmin, workpaper.Filter{}); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("admin listed: %v", err)
	}
	if _, _, err := h.svc.Create(wpAuditor, usfWorkpaper()); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an auditor opened a workpaper: %v", err)
	}
	if _, err := h.svc.Workpapers(wpAuditor, workpaper.Filter{Period: "2026-9"}); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("a bad period filter: %v", err)
	}
	if _, err := h.svc.Act(ctbOperator, id.WorkpaperID{}, app.WorkpaperAct{Action: workpaper.ActionBuild}); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("BUILD through actions: %v", err)
	}
}
