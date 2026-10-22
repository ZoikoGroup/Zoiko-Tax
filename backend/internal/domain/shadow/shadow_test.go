package shadow_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/shadow"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

// Lane M's shadow comparison against ZTAX-SHD-001's register. Test names
// carry the requirement id each verifies, so docs/requirements.yaml can point
// at it.

var (
	decision = id.NewDecisionID(uuid.MustParse("00000000-0000-7000-8000-000000000040"))
	analyst  = id.NewUserID(uuid.MustParse("00000000-0000-7000-8000-000000000005"))
	when     = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
)

func eur(t *testing.T, s string) fiscal.Money { return fiscaltest.Money(t, s, "EUR") }

func policy(t *testing.T, tolerance, materiality string) shadow.Policy {
	t.Helper()
	p := shadow.Policy{Currency: "EUR", ComponentMap: map[string]string{"VAT": "TAX_VAT", "ECO": "TAX_ECO_LEVY"},
		Tolerance: eur(t, tolerance), Materiality: eur(t, materiality)}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	return p
}

func zoiko(t *testing.T, vat, eco string) map[string]fiscal.Money {
	return map[string]fiscal.Money{"TAX_VAT": eur(t, vat), "TAX_ECO_LEVY": eur(t, eco)}
}

func compare(t *testing.T, p shadow.Policy, obs shadow.Observation, z map[string]fiscal.Money) shadow.Case {
	t.Helper()
	c, err := shadow.Compare(p, obs, decision, z)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSHDREQ0039ExactAndEquivalentMatchesAreDistinct(t *testing.T) {
	p := policy(t, "0.01", "1.00")
	exact := compare(t, p, shadow.Observation{CaseRef: "A", Total: eur(t, "21.04"),
		Components: map[string]fiscal.Money{"VAT": eur(t, "21.00"), "ECO": eur(t, "0.04")}}, zoiko(t, "21.00", "0.04"))
	if exact.State != shadow.VarianceExact {
		t.Fatalf("exact: %s", exact.State)
	}
	near := compare(t, p, shadow.Observation{CaseRef: "B", Total: eur(t, "21.05"),
		Components: map[string]fiscal.Money{"VAT": eur(t, "21.01"), "ECO": eur(t, "0.04")}}, zoiko(t, "21.00", "0.04"))
	if near.State != shadow.VarianceEquivalent {
		t.Fatalf("within tolerance: %s", near.State)
	}
}

func TestSHDREQ0044ToleranceNeverChangesAnAmount(t *testing.T) {
	p := policy(t, "0.01", "1.00")
	c := compare(t, p, shadow.Observation{CaseRef: "B", Total: eur(t, "21.05"),
		Components: map[string]fiscal.Money{"VAT": eur(t, "21.01"), "ECO": eur(t, "0.04")}}, zoiko(t, "21.00", "0.04"))
	if c.ComparatorTotal.CanonicalString() != "21.05" || c.ZoikoTotal.CanonicalString() != "21.04" || c.Variance.CanonicalString() != "-0.01" {
		t.Fatalf("totals %s / %s / %s were changed by the tolerance", c.ComparatorTotal.CanonicalString(), c.ZoikoTotal.CanonicalString(), c.Variance.CanonicalString())
	}
}

func TestSHDREQ0028UnmappedCodesStayUnmapped(t *testing.T) {
	p := policy(t, "0.00", "1.00")
	c := compare(t, p, shadow.Observation{CaseRef: "C", Total: eur(t, "21.04"),
		Components: map[string]fiscal.Money{"VAT": eur(t, "21.00"), "MYSTERY_FEE": eur(t, "0.04")}}, zoiko(t, "21.00", "0.04"))
	var unmapped bool
	for _, cc := range c.Components {
		if cc.ExternalCode == "MYSTERY_FEE" {
			unmapped = cc.Slot == shadow.Unmapped && cc.State == shadow.VarianceCategory
		}
	}
	if !unmapped || c.State != shadow.VarianceCategory {
		t.Fatalf("an unmapped code was not kept explicit: %+v", c)
	}
}

func TestSHDREQ0030And0047TotalsDoNotHideOrNetComponentErrors(t *testing.T) {
	p := policy(t, "0.00", "0.50")
	// The comparator moves 1.00 from VAT to the levy: the totals agree.
	c := compare(t, p, shadow.Observation{CaseRef: "D", Total: eur(t, "21.04"),
		Components: map[string]fiscal.Money{"VAT": eur(t, "20.00"), "ECO": eur(t, "1.04")}}, zoiko(t, "21.00", "0.04"))
	if c.Variance == nil || !c.Variance.IsZero() {
		t.Fatalf("totals should agree: %v", c.Variance)
	}
	if c.State != shadow.VarianceMonetary || !c.Material {
		t.Fatalf("equal totals hid opposite component errors: state %s material %v", c.State, c.Material)
	}
}

func TestSHDREQ0042And0021ADifferentCurrencyIsUncomparable(t *testing.T) {
	p := policy(t, "0.00", "1.00")
	c := compare(t, p, shadow.Observation{CaseRef: "E", Total: fiscaltest.Money(t, "21.04", "USD")}, zoiko(t, "21.00", "0.04"))
	if c.State != shadow.VarianceUncomparable || c.Material {
		t.Fatalf("state %s", c.State)
	}
}

func TestSHDREQ0023And0045And0046UnpairedAndWeightedMetrics(t *testing.T) {
	p := policy(t, "0.00", "1.00")
	matched := compare(t, p, shadow.Observation{CaseRef: "A", Total: eur(t, "21.04"),
		Components: map[string]fiscal.Money{"VAT": eur(t, "21.00"), "ECO": eur(t, "0.04")}}, zoiko(t, "21.00", "0.04"))
	off := compare(t, p, shadow.Observation{CaseRef: "B", Total: eur(t, "121.04"),
		Components: map[string]fiscal.Money{"VAT": eur(t, "121.00"), "ECO": eur(t, "0.04")}}, zoiko(t, "21.00", "0.04"))
	missing := eur(t, "5.00")
	unpaired := shadow.Unpaired("Z", shadow.PairingUnpairedComparator, id.DecisionID{}, &missing, nil)
	m, err := shadow.Summarise(p, []shadow.Case{matched, off, unpaired})
	if err != nil {
		t.Fatal(err)
	}
	if m.Cases != 3 || m.Paired != 2 || m.Exact != 1 || m.Monetary != 1 || m.Population != 1 {
		t.Fatalf("counts %+v", m)
	}
	if m.ComparedValue.CanonicalString() != "142.08" || m.AbsoluteVariance.CanonicalString() != "100.00" {
		t.Fatalf("value-weighted %s / %s", m.ComparedValue.CanonicalString(), m.AbsoluteVariance.CanonicalString())
	}
	if unpaired.State != shadow.VariancePopulation {
		t.Fatal("an unpaired record was reported as an amount difference")
	}
}

func TestSHDREQ0005And0068AMatchClaimsOnlyWhatTheComparatorSupports(t *testing.T) {
	for typ, want := range map[shadow.ComparatorType]shadow.Claim{
		shadow.ComparatorCommercialEngine: shadow.ClaimMatchesComparator,
		shadow.ComparatorHistoricalBilled: shadow.ClaimMatchesComparator,
		shadow.ComparatorGoldenVector:     shadow.ClaimVerifiedCorrect,
		shadow.ComparatorAuthority:        shadow.ClaimVerifiedCorrect,
		shadow.ComparatorPriorRelease:     shadow.ClaimRegression,
	} {
		if got := typ.Claim(); got != want {
			t.Fatalf("%s claims %s, want %s", typ, got, want)
		}
	}
}

func TestSHDREQ0009AnUnknownVersionIsRecordedAndReported(t *testing.T) {
	c := shadow.Comparator{Name: "Incumbent", Type: shadow.ComparatorCommercialEngine, Provider: "Vendor"}
	if err := c.Validate(); err == nil {
		t.Fatal("a comparator with no stated version was accepted")
	}
	c.Version = "UNKNOWN"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	m := shadow.Metrics{Cases: 1, Paired: 1, Exact: 1}
	found := false
	for _, b := range shadow.Blockers(c, m, nil) {
		found = found || b == "The comparator's version is unknown."
	}
	if !found {
		t.Fatal("an unknown comparator version was not reported")
	}
}

func discrepancy(t *testing.T, sev shadow.Severity) shadow.Discrepancy {
	t.Helper()
	d, err := shadow.FoldDiscrepancy(shadow.Discrepancy{}, []shadow.Event{{Seq: 1, State: shadow.StateNew, Severity: sev, RootCause: "UNKNOWN",
		Note: "opened", RecordedAt: when, RecordedBy: analyst}})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSHDREQ0061And0062ASev1BlocksCutoverWhateverTheMatchRate(t *testing.T) {
	c := shadow.Comparator{Name: "Golden", Type: shadow.ComparatorGoldenVector, Provider: "QA", Version: "2026.10"}
	m := shadow.Metrics{Cases: 10000, Paired: 10000, Exact: 9999, Monetary: 1, Material: 1}
	blockers := shadow.Blockers(c, m, []shadow.Discrepancy{discrepancy(t, shadow.SeverityOne)})
	if len(blockers) == 0 {
		t.Fatal("an unresolved SHD-SEV-1 did not block cutover at a 99.99% match rate")
	}
}

func TestSHDREQ0006And0055And0059DiscrepanciesAreTriagedNotAssumedDefects(t *testing.T) {
	d := discrepancy(t, shadow.SeverityTwo)
	if d.RootCause != "UNKNOWN" {
		t.Fatal("a new discrepancy was attributed before triage")
	}
	if _, err := d.Move(shadow.StateTriaged, "", "", "looked", analyst, when); err == nil {
		t.Fatal("triage without a severity and a root cause was accepted")
	}
	if _, err := d.Move(shadow.StateTriaged, shadow.SeverityTwo, "NOT_A_CAUSE", "x", analyst, when); err == nil {
		t.Fatal("an untyped root cause was accepted")
	}
	if _, err := d.Move(shadow.StateClosed, "", "", "done", analyst, when); err == nil {
		t.Fatal("a discrepancy closed straight from NEW")
	}
	e, err := d.Move(shadow.StateTriaged, shadow.SeverityTwo, "LEGAL_AMBIGUITY", "two readings of the rule", analyst, when)
	if err != nil {
		t.Fatal(err)
	}
	d, err = shadow.FoldDiscrepancy(d, append(d.History, e))
	if err != nil {
		t.Fatal(err)
	}
	if !shadow.CanTransition(d.State, shadow.StateLegalAmbiguity) || shadow.CanTransition(shadow.StateLegalAmbiguity, shadow.StateFixPending) {
		t.Fatal("legal ambiguity is not kept apart from a software defect")
	}
}
