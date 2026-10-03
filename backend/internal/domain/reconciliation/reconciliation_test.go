package reconciliation_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/reconciliation"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

var resolver = id.NewUserID(uuid.MustParse("00000000-0000-7000-8000-000000000004"))

func eur(t *testing.T, s string) *fiscal.Money {
	m := fiscaltest.Money(t, s, "EUR")
	return &m
}

func penny(t *testing.T) reconciliation.Policy {
	return reconciliation.Policy{Tolerances: []reconciliation.Tolerance{
		{Stage: reconciliation.R6SubledgerToGL, Currency: "EUR", Absolute: *eur(t, "0.01")},
	}}
}

func cmp(t *testing.T, stage reconciliation.Stage, exp, obs *fiscal.Money, statutory bool) reconciliation.Item {
	t.Helper()
	it, err := reconciliation.Compare(stage, "doc-1/vat", exp, obs, penny(t), statutory)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func TestFINREQ0071To0077EveryStageIsModelledAndTraced(t *testing.T) {
	if len(reconciliation.Stages) != 7 {
		t.Fatalf("%d stages", len(reconciliation.Stages))
	}
	tr := reconciliation.EndToEnd(map[reconciliation.Stage]reconciliation.StageResult{
		reconciliation.R1CalculatedToDocument: {Stage: reconciliation.R1CalculatedToDocument, Available: true,
			Items: []reconciliation.Item{cmp(t, reconciliation.R1CalculatedToDocument, eur(t, "21.00"), eur(t, "21.01"), true)}},
		reconciliation.R5ReturnToRemittance: {Stage: reconciliation.R5ReturnToRemittance, Available: true,
			Items: []reconciliation.Item{cmp(t, reconciliation.R5ReturnToRemittance, eur(t, "21.00"), eur(t, "21.00"), true)}},
	})
	// R7: a clean R5 does not hide the broken R1, and the four stages with
	// no data are reported as unavailable rather than left out.
	if tr.FirstBreak == nil || *tr.FirstBreak != reconciliation.R1CalculatedToDocument {
		t.Fatalf("first break %v", tr.FirstBreak)
	}
	if len(tr.Stages) != 6 || len(tr.Unavailable) != 4 {
		t.Fatalf("stages %d unavailable %v", len(tr.Stages), tr.Unavailable)
	}
}

func TestFINREQ0078And0081ToleranceMatchIsNotAMatchAndKeepsItsVariance(t *testing.T) {
	exact := cmp(t, reconciliation.R6SubledgerToGL, eur(t, "21.00"), eur(t, "21.00"), false)
	near := cmp(t, reconciliation.R6SubledgerToGL, eur(t, "21.00"), eur(t, "21.01"), false)
	if exact.Status != reconciliation.StatusMatched || near.Status != reconciliation.StatusToleranceMatch {
		t.Fatalf("exact %s near %s", exact.Status, near.Status)
	}
	if near.Variance == nil || near.Variance.String() != "0.01" {
		t.Fatalf("tolerance match variance %v", near.Variance)
	}
}

func TestFINREQ0080ToleranceIsExplicitByStage(t *testing.T) {
	// The same cent at R1, which has no tolerance, is unmatched.
	it := cmp(t, reconciliation.R1CalculatedToDocument, eur(t, "21.00"), eur(t, "21.01"), false)
	if it.Status != reconciliation.StatusUnmatched {
		t.Fatalf("R1 status %s", it.Status)
	}
}

func TestFINREQ0082StatutoryExactnessOverridesTolerance(t *testing.T) {
	it := cmp(t, reconciliation.R6SubledgerToGL, eur(t, "21.00"), eur(t, "21.01"), true)
	if it.Status == reconciliation.StatusToleranceMatch {
		t.Fatal("a statutory comparison was tolerance-matched")
	}
}

func TestFINREQ0079StatusesCoverMissingPartialAndUnmatched(t *testing.T) {
	if s := cmp(t, reconciliation.R2DocumentToCollection, eur(t, "121.00"), nil, false).Status; s != reconciliation.StatusMissing {
		t.Errorf("missing: %s", s)
	}
	if s := cmp(t, reconciliation.R2DocumentToCollection, nil, eur(t, "121.00"), false).Status; s != reconciliation.StatusUnmatched {
		t.Errorf("unexpected: %s", s)
	}
	if s := cmp(t, reconciliation.R2DocumentToCollection, eur(t, "121.00"), eur(t, "60.00"), false).Status; s != reconciliation.StatusPartial {
		t.Errorf("partial: %s", s)
	}
}

func TestFINREQ0031And0032NativeCurrencyFirstAndFXClassifiedSeparately(t *testing.T) {
	usd := fiscaltest.Money(t, "23.00", "USD")
	it := cmp(t, reconciliation.R4SubledgerToReturn, eur(t, "21.00"), &usd, false)
	if it.Status != reconciliation.StatusConflicted || it.Cause != reconciliation.CauseFX {
		t.Fatalf("cross-currency item %s / %s", it.Status, it.Cause)
	}
}

func TestFINREQ0083RootCauseTaxonomyIsComplete(t *testing.T) {
	for _, c := range []reconciliation.RootCause{
		reconciliation.CauseClassification, reconciliation.CauseJurisdiction, reconciliation.CauseTaxRule, reconciliation.CauseRounding,
		reconciliation.CauseFX, reconciliation.CauseDocument, reconciliation.CauseCollection, reconciliation.CausePosting,
		reconciliation.CauseReturn, reconciliation.CauseRemittance, reconciliation.CauseGL, reconciliation.CauseTiming, reconciliation.CauseData,
	} {
		if !c.Valid() {
			t.Errorf("%s missing", c)
		}
	}
}

func TestFINREQ0084And0086ResolutionIsRecordedAndAICannotResolveMaterialExceptions(t *testing.T) {
	it := cmp(t, reconciliation.R1CalculatedToDocument, eur(t, "21.00"), eur(t, "21.01"), true)
	if _, err := reconciliation.Resolve(it, reconciliation.Resolution{Actor: reconciliation.ActorHuman, Resolver: resolver}); err == nil {
		t.Fatal("a resolution with no reason, action or evidence was accepted")
	}
	ai := reconciliation.Resolution{Actor: reconciliation.ActorAI, Reason: "rounding", Action: reconciliation.ActionAdjust,
		Cause: reconciliation.CauseRounding, Evidence: []string{"ev:1"}, At: time.Now(), Material: true, AutoPolicy: "a3:rounding"}
	if _, err := reconciliation.Resolve(it, ai); err == nil {
		t.Fatal("an AI resolved a material exception")
	}
	ok, err := reconciliation.Resolve(it, reconciliation.Resolution{Actor: reconciliation.ActorHuman, Resolver: resolver, Reason: "document re-rendered",
		Action: reconciliation.ActionExternalCorrection, Cause: reconciliation.CauseDocument, Evidence: []string{"ev:2"}, At: time.Now()})
	if err != nil || ok.Status != reconciliation.StatusResolved || ok.Variance.String() != "0.01" {
		t.Fatalf("human resolution %v %+v", err, ok)
	}
}

func TestFINREQ0087DuplicatesAreDetected(t *testing.T) {
	d := reconciliation.Duplicates([]reconciliation.Observation{
		{Key: "pay-1", Source: "psp"}, {Key: "pay-2", Source: "psp"}, {Key: "pay-1", Source: "erp"},
	})
	if len(d) != 1 || d[0] != "pay-1" {
		t.Fatalf("duplicates %v", d)
	}
}
