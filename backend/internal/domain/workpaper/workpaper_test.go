package workpaper_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/contribution"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/workpaper"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

var (
	entity   = id.NewLegalEntityID(uuid.MustParse("01920000-0000-7000-8000-00000000e001"))
	other    = id.NewLegalEntityID(uuid.MustParse("01920000-0000-7000-8000-00000000e002"))
	preparer = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000b1"))
	reviewer = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000b2"))
	at       = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	evidence = &workpaper.Evidence{SHA256: strings.Repeat("ab", 32), Filename: "audit.pdf", MediaType: "application/pdf"}
)

var seq uint64

func event(t *testing.T, source, cur, eligible, excluded, category string) contribution.Contribution {
	t.Helper()
	seq++
	u := uuid.MustParse("01920000-0000-7000-8000-000000000000")
	u[15] = byte(seq)
	c := contribution.Contribution{
		ID: id.NewContributionID(u), Source: contribution.SourceKey{EventID: source, Version: 1},
		SourceType: contribution.SourceBSSInvoice, Regime: contribution.RegimeUSUSF, Period: "2026-09", LegalEntity: entity,
		ClassificationRef: category, SitusRef: "US", SourceRef: "INV-" + source, RuleVersion: "usf@1",
		EligibleBase: fiscaltest.Money(t, eligible, fiscal.Currency(cur)), ExcludedBase: fiscaltest.Money(t, excluded, fiscal.Currency(cur)),
		Status: contribution.StatusAccepted,
	}
	if excluded != "0.00" {
		c.ExclusionReason = "International-only"
	}
	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}
	return c
}

func opened(t *testing.T) workpaper.Workpaper {
	t.Helper()
	w := workpaper.Workpaper{LegalEntity: entity, Duty: contribution.RegimeUSUSF, Period: "2026-09", Authority: "USAC",
		ReportingCurrency: "USD", Materiality: fiscaltest.Money(t, "500.00", "USD"), InitialRelease: "usf@2026.3"}
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	w, err := workpaper.Fold(w, []workpaper.Event{workpaper.Created(preparer, at)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// apply folds events into w, as the store would after appending them.
func apply(t *testing.T, w workpaper.Workpaper, events ...workpaper.Event) workpaper.Workpaper {
	t.Helper()
	out, err := workpaper.Fold(w, append(slices.Clone(w.History), events...), w.Builds)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// build assembles w from src and records the build as by.
func build(t *testing.T, w workpaper.Workpaper, src workpaper.Sources, by id.UserID) workpaper.Workpaper {
	t.Helper()
	a, err := workpaper.Assemble(w, src)
	if err != nil {
		t.Fatal(err)
	}
	b := workpaper.Build{Version: len(w.Builds) + 1, InputsHash: a.InputsHash, OutputHash: a.OutputHash, BasisDigest: a.BasisDigest,
		FormulaVersion: workpaper.FormulaVersion, ContentRelease: w.ContentRelease, BuiltAt: at, BuiltBy: by, Output: &a.Output}
	events, err := w.RecordBuild(b, by, at)
	if err != nil {
		t.Fatal(err)
	}
	w.Builds = append(w.Builds, b)
	return apply(t, w, events...)
}

func codes(w workpaper.Workpaper) []string {
	var out []string
	for _, e := range w.Exceptions() {
		out = append(out, e.Key)
	}
	return out
}

// OBL-07 "Build deterministic": the same inputs in any order give the same
// output and hashes, and the stored document round-trips to its digest.
func TestAssemblyIsDeterministicAndSealed(t *testing.T) {
	w := opened(t)
	events := []contribution.Contribution{
		event(t, "E-1", "USD", "1000.00", "100.00", "usf:interstate"),
		event(t, "E-2", "USD", "250.50", "0.00", "usf:international"),
		event(t, "E-3", "USD", "40.25", "0.00", "usf:interstate"),
	}
	a1, err := workpaper.Assemble(w, workpaper.Sources{Contributions: events})
	if err != nil {
		t.Fatal(err)
	}
	reversed := slices.Clone(events)
	slices.Reverse(reversed)
	a2, err := workpaper.Assemble(w, workpaper.Sources{Contributions: reversed})
	if err != nil {
		t.Fatal(err)
	}
	if !a1.InputsHash.Equal(a2.InputsHash) || !a1.OutputHash.Equal(a2.OutputHash) {
		t.Fatalf("arrival order changed the build: %s/%s vs %s/%s", a1.InputsHash, a1.OutputHash, a2.InputsHash, a2.OutputHash)
	}
	body, err := workpaper.EncodeOutput(a1.Output)
	if err != nil {
		t.Fatal(err)
	}
	back, err := workpaper.DecodeOutput(body, a1.OutputHash)
	if err != nil {
		t.Fatalf("the stored document does not round-trip: %v", err)
	}
	if back.Totals == nil || back.Totals.ReportableBase != "1290.75" {
		t.Fatalf("totals after round trip: %+v", back.Totals)
	}
	tampered := strings.Replace(string(body), "1290.75", "1290.74", 1)
	if _, err := workpaper.DecodeOutput([]byte(tampered), a1.OutputHash); err == nil {
		t.Fatal("a tampered document was accepted")
	}
}

// The schedules: categories with lineage to each source event, excluded
// bases, counts, and a reconciliation to the ledger's own control totals.
func TestSchedulesTieToControlTotalsWithLineage(t *testing.T) {
	w := opened(t)
	events := []contribution.Contribution{
		event(t, "E-1", "USD", "1000.00", "100.00", "usf:interstate"),
		event(t, "E-2", "USD", "250.50", "0.00", "usf:international"),
		event(t, "E-3", "USD", "40.25", "0.00", "usf:interstate"),
	}
	foreign := event(t, "X-1", "USD", "999.00", "0.00", "usf:interstate")
	foreign.LegalEntity = other
	a, err := workpaper.Assemble(w, workpaper.Sources{Contributions: append(events, foreign)})
	if err != nil {
		t.Fatal(err)
	}
	o := a.Output
	if len(o.Revenue) != 2 || o.Revenue[1].Category != "usf:interstate" || o.Revenue[1].Amount != "1040.25" || o.Revenue[1].Count != 2 {
		t.Fatalf("revenue: %+v", o.Revenue)
	}
	if refs := o.Revenue[1].Sources; len(refs) != 2 || refs[0].SourceEventID != "E-1" || refs[0].ContributionID != events[0].ID.String() {
		t.Fatalf("lineage: %+v", refs)
	}
	if len(o.Excluded) != 1 || o.Excluded[0].Amount != "100.00" {
		t.Fatalf("excluded: %+v", o.Excluded)
	}
	if o.Volumes.Accepted != 3 || len(o.Volumes.BySource) != 1 || o.Volumes.BySource[0].Eligible != "1290.75" {
		t.Fatalf("volumes: %+v", o.Volumes)
	}
	for _, r := range o.Reconciliation {
		if r.Status == workpaper.ReconDifference {
			t.Fatalf("a schedule does not tie: %+v", r)
		}
	}
	if len(o.Exceptions) != 0 || o.Totals.GrossEligible != "1290.75" || o.Totals.Excluded != "100.00" {
		t.Fatalf("exceptions %+v totals %+v", o.Exceptions, o.Totals)
	}
}

// OBL-07 "Source missing" and a held import: both block READY_FOR_REVIEW.
func TestMissingOrHeldSourceIsDataIncomplete(t *testing.T) {
	w := build(t, opened(t), workpaper.Sources{}, preparer)
	if w.Status != workpaper.StatusDataIncomplete || !slices.Equal(codes(w), []string{workpaper.ExceptionSourceMissing}) {
		t.Fatalf("status %s exceptions %v", w.Status, codes(w))
	}
	if _, err := w.Freeze("", preparer, at); errs.ReasonOf(err) != workpaper.ReasonNotReady {
		t.Fatalf("froze with material data absent: %v", err)
	}

	held := event(t, "E-9", "USD", "10.00", "0.00", "usf:interstate")
	held.Status = contribution.StatusQuarantined
	w2 := build(t, opened(t), workpaper.Sources{Contributions: []contribution.Contribution{held}}, preparer)
	if !slices.Equal(codes(w2), []string{workpaper.ExceptionSourceIncomplete + "/USD"}) {
		t.Fatalf("a held import: %v", codes(w2))
	}
	key := w2.Exceptions()[0].Key
	if _, err := w2.ProposeResolution(key, "", "explained", evidence, preparer, at); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("a held import was resolved by explanation: %v", err)
	}
}

// OBL-07 "Currency mismatch": no totals and an exception until a rate and
// its conversion basis are documented; then each line converts once.
func TestCurrencyMismatchNeedsADocumentedRate(t *testing.T) {
	src := workpaper.Sources{Contributions: []contribution.Contribution{
		event(t, "C-1", "CAD", "100.00", "0.00", "ncf:telecom"),
		event(t, "C-2", "CAD", "33.33", "0.00", "ncf:telecom"),
	}}
	w := build(t, opened(t), src, preparer)
	if !slices.Equal(codes(w), []string{workpaper.ExceptionCurrencyMismatch + "/CAD"}) || w.Latest().Output.Totals != nil {
		t.Fatalf("exceptions %v totals %+v", codes(w), w.Latest().Output.Totals)
	}
	fx := workpaper.FXRate{Currency: "CAD", Rate: fiscaltest.Rate(t, "0.7300", fiscal.RateBasisNet), Source: "Bank of Canada",
		RateDate: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), RoundingMode: fiscal.RoundHalfEven, Scale: 2,
		Basis: "Period-end rate, each schedule line converted and rounded once"}
	e, err := w.DocumentFX(fx, preparer, at)
	if err != nil {
		t.Fatal(err)
	}
	w = apply(t, w, e)
	if !slices.Contains(w.Blockers(), workpaper.BlockerBasisChanged) {
		t.Fatalf("a new rate did not require a rebuild: %v", w.Blockers())
	}
	w = build(t, w, src, preparer)
	o := w.Latest().Output
	if w.Status != workpaper.StatusDraft || len(o.Exceptions) != 0 {
		t.Fatalf("status %s exceptions %v", w.Status, codes(w))
	}
	// 133.33 CAD x 0.73 = 97.3309, half-even at 2 = 97.33.
	if o.Revenue[0].Converted != "97.33" || o.Totals.GrossEligible != "97.33" || len(o.Conversions) != 1 {
		t.Fatalf("converted %q totals %+v conversions %+v", o.Revenue[0].Converted, o.Totals, o.Conversions)
	}
	if _, err := w.DocumentFX(workpaper.FXRate{Currency: "USD"}, preparer, at); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("a rate for the reporting currency: %v", err)
	}
}

// Open suspense in the filer's subledger is an exception, resolvable by
// two people with evidence; the resolution names the very difference.
func TestSuspenseResolutionIsFourEyed(t *testing.T) {
	src := workpaper.Sources{
		Contributions: []contribution.Contribution{event(t, "E-1", "USD", "10.00", "0.00", "usf:interstate")},
		Subledger: []subledger.ManifestBalance{{Account: subledger.AccountSuspenseException, Currency: "USD",
			Debits: fiscaltest.Money(t, "5.00", "USD"), Credits: fiscaltest.Money(t, "0.00", "USD")}},
	}
	w := build(t, opened(t), src, preparer)
	if w.Status != workpaper.StatusDataIncomplete {
		t.Fatalf("status %s", w.Status)
	}
	x := w.Exceptions()[0]
	if x.Code != workpaper.ExceptionSuspenseOpen || x.Difference != "-5.00" {
		t.Fatalf("exception %+v", x)
	}
	if _, err := w.ProposeResolution(x.Key, x.Fingerprint, "Explained", nil, preparer, at); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("a resolution with no evidence: %v", err)
	}
	e, err := w.ProposeResolution(x.Key, x.Fingerprint, "Suspense is retail tax awaiting mapping; not a contribution", evidence, preparer, at)
	if err != nil {
		t.Fatal(err)
	}
	w = apply(t, w, e)
	if _, err := w.DecideResolution(x.Key, workpaper.DecisionApproved, "", preparer, at); errs.ReasonOf(err) != workpaper.ReasonSegregationOfDuties {
		t.Fatalf("self-confirmation: %v", err)
	}
	d, err := w.DecideResolution(x.Key, workpaper.DecisionApproved, "Checked the mapping ticket", reviewer, at)
	if err != nil {
		t.Fatal(err)
	}
	w = apply(t, w, d)
	if w.Status != workpaper.StatusDraft || !w.Exceptions()[0].Resolved() || len(w.Blockers()) != 0 {
		t.Fatalf("status %s blockers %v", w.Status, w.Blockers())
	}
}

// Freeze, approval by a different person, and revocation on a basis change.
func TestApprovalIsOfTheFrozenBuildByAnotherPerson(t *testing.T) {
	src := workpaper.Sources{Contributions: []contribution.Contribution{event(t, "E-1", "USD", "10.00", "0.00", "usf:interstate")}}
	w := build(t, opened(t), src, preparer)
	f, err := w.Freeze("", preparer, at)
	if err != nil {
		t.Fatal(err)
	}
	w = apply(t, w, f)
	if w.Status != workpaper.StatusReadyForReview || w.FrozenBuild != 1 {
		t.Fatalf("status %s frozen %d", w.Status, w.FrozenBuild)
	}
	if _, err := w.RecordBuild(workpaper.Build{Version: 2}, preparer, at); errs.ReasonOf(err) != workpaper.ReasonSnapshotLocked {
		t.Fatalf("rebuilt a frozen snapshot: %v", err)
	}
	if _, err := w.ProposeAdjustment(workpaper.AdjustmentProposal{}, preparer, at); errs.ReasonOf(err) != workpaper.ReasonSnapshotLocked {
		t.Fatalf("adjusted a frozen snapshot: %v", err)
	}
	if _, err := w.Approve(1, "", preparer, at); errs.ReasonOf(err) != workpaper.ReasonSegregationOfDuties {
		t.Fatalf("self-approval: %v", err)
	}
	if _, err := w.Approve(2, "", reviewer, at); errs.ReasonOf(err) != workpaper.ReasonStale {
		t.Fatalf("approval of another build: %v", err)
	}
	ap, err := w.Approve(1, "Tied out to the 499-Q draft", reviewer, at)
	if err != nil {
		t.Fatal(err)
	}
	w = apply(t, w, ap)
	if w.Status != workpaper.StatusApproved {
		t.Fatalf("status %s", w.Status)
	}
	cb, err := w.ChangeBasis("usf@2026.4", "USAC revised the contribution factor basis", preparer, at)
	if err != nil {
		t.Fatal(err)
	}
	w = apply(t, w, cb)
	if w.Status != workpaper.StatusRevoked || !slices.Contains(w.Blockers(), workpaper.BlockerBasisChanged) {
		t.Fatalf("status %s blockers %v", w.Status, w.Blockers())
	}
	w = build(t, w, src, preparer)
	if w.Status != workpaper.StatusDraft || len(w.Builds) != 2 {
		t.Fatalf("after rebuild: %s, %d builds", w.Status, len(w.Builds))
	}
}

// A rebuild of an approved workpaper with changed inputs revokes the
// approval before recording the new build.
func TestRebuildRevokesAnApproval(t *testing.T) {
	src := workpaper.Sources{Contributions: []contribution.Contribution{event(t, "E-1", "USD", "10.00", "0.00", "usf:interstate")}}
	w := build(t, opened(t), src, preparer)
	f, _ := w.Freeze("", preparer, at)
	w = apply(t, w, f)
	ap, _ := w.Approve(1, "", reviewer, at)
	w = apply(t, w, ap)
	src.Contributions = append(src.Contributions, event(t, "E-2", "USD", "1.00", "0.00", "usf:interstate"))
	w = build(t, w, src, preparer)
	kinds := []workpaper.EventKind{}
	for _, e := range w.History[len(w.History)-2:] {
		kinds = append(kinds, e.Kind)
	}
	if !slices.Equal(kinds, []workpaper.EventKind{workpaper.EventApprovalRevoked, workpaper.EventBuilt}) || w.Status != workpaper.StatusDraft {
		t.Fatalf("history %v status %s", kinds, w.Status)
	}
}

// A material adjustment waits for a second person; one below the
// threshold is approved as proposed. Both carry evidence.
func TestMaterialAdjustmentsAreFourEyed(t *testing.T) {
	w := opened(t)
	small, err := w.ProposeAdjustment(workpaper.AdjustmentProposal{Kind: workpaper.KindAllowance, Category: "Bad debt",
		Amount: fiscaltest.Money(t, "50.00", "USD"), Reason: "Write-offs", Evidence: evidence}, preparer, at)
	if err != nil {
		t.Fatal(err)
	}
	w = apply(t, w, small)
	big, err := w.ProposeAdjustment(workpaper.AdjustmentProposal{Kind: workpaper.KindAdjustment, Category: "Billing correction",
		Amount: fiscaltest.Money(t, "-500.00", "USD"), Reason: "Credit notes", Evidence: evidence}, preparer, at)
	if err != nil {
		t.Fatal(err)
	}
	w = apply(t, w, big)
	if w.Adjustments[0].State != workpaper.AdjustmentApproved || w.Adjustments[1].State != workpaper.AdjustmentProposed || !w.Adjustments[1].Material {
		t.Fatalf("adjustments %+v", w.Adjustments)
	}
	if _, err := w.ProposeAdjustment(workpaper.AdjustmentProposal{Kind: workpaper.KindAllowance, Category: "x",
		Amount: fiscaltest.Money(t, "-1.00", "USD"), Reason: "r", Evidence: evidence}, preparer, at); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("a negative allowance: %v", err)
	}
	if _, err := w.DecideAdjustment(2, workpaper.DecisionApproved, "", preparer, at); errs.ReasonOf(err) != workpaper.ReasonSegregationOfDuties {
		t.Fatalf("self-decision: %v", err)
	}
	d, err := w.DecideAdjustment(2, workpaper.DecisionApproved, "", reviewer, at)
	if err != nil {
		t.Fatal(err)
	}
	w = apply(t, w, d)
	src := workpaper.Sources{Contributions: []contribution.Contribution{event(t, "E-1", "USD", "1000.00", "0.00", "usf:interstate")}}
	w = build(t, w, src, preparer)
	o := w.Latest().Output
	// 1000.00 - 50.00 allowance + -500.00 adjustment.
	if o.Totals.ReportableBase != "450.00" || len(o.Allowances) != 1 || len(o.Adjustments) != 1 {
		t.Fatalf("totals %+v", o.Totals)
	}
	bridge := o.Reconciliation[len(o.Reconciliation)-1]
	if bridge.Code != workpaper.ReconReportableBridge || bridge.Status != workpaper.ReconBridged || bridge.Difference != "-550.00" {
		t.Fatalf("bridge %+v", bridge)
	}
}
