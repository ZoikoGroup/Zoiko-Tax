package determination_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/determination"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The golden scenarios of ZTAX-DET-001 §14.1, one test per requirement where a
// requirement has a scenario of its own. Test names carry the requirement id
// so docs/requirements.yaml can point at exactly the test that verifies it.
//
// Every figure below is worked by hand in the comment beside it. None is a
// snapshot of what the code produced.

const eur fiscal.Currency = "EUR"

func money(t *testing.T, s string) fiscal.Money { return fiscaltest.Money(t, s, eur) }
func moneyP(t *testing.T, s string) *fiscal.Money {
	m := money(t, s)
	return &m
}
func rateP(t *testing.T, s string) *fiscal.Rate {
	r := fiscaltest.Rate(t, s, fiscal.RateBasisNet)
	return &r
}
func line2(t *testing.T) fiscal.RoundingPolicy {
	return fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2)
}

// adValorem is a LINE_NET proportional component rounded at the line.
func adValorem(t *testing.T, id, rate string) determination.Component {
	return determination.Component{
		ID: determination.ComponentID(id), JurisdictionID: "jurisdiction:xx", TaxType: "VAT",
		Base:   determination.Base{Kind: determination.BaseLineNet},
		Rate:   determination.Rate{Kind: determination.RateAdValorem, Proportion: rateP(t, rate)},
		Policy: line2(t),
	}
}

func compile(t *testing.T, s determination.Schedule) *determination.Plan {
	t.Helper()
	p, err := determination.Compile(s)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return p
}

func evaluate(t *testing.T, p *determination.Plan, lines ...determination.Line) determination.Determination {
	t.Helper()
	d, err := p.Evaluate(determination.Document{Currency: eur, Lines: lines})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return d
}

func all(ids ...string) []determination.ComponentID {
	out := make([]determination.ComponentID, len(ids))
	for i, s := range ids {
		out[i] = determination.ComponentID(s)
	}
	return out
}

func amountOf(t *testing.T, l determination.LineResult, id string) string {
	t.Helper()
	for _, c := range l.Components {
		if string(c.Component) == id {
			return c.Amount.CanonicalString()
		}
	}
	t.Fatalf("line %s has no component %s", l.Key, id)
	return ""
}

func hasStep(d determination.Determination, step, contains string) bool {
	for _, s := range d.Trace {
		if s.Step == step && strings.Contains(s.Detail, contains) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// build-time refusals
// ---------------------------------------------------------------------------

func refuses(t *testing.T, s determination.Schedule, want string) {
	t.Helper()
	_, err := determination.Compile(s)
	if err == nil {
		t.Fatalf("Compile accepted a schedule that should be refused (%s)", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Compile refused with %q, want it to mention %q", err, want)
	}
}

func TestDETREQ0011CyclicGraphRefusedAtBuild(t *testing.T) {
	a := adValorem(t, "a", "0.1")
	a.Base = determination.Base{Kind: determination.BaseNetPlusComponents, Components: all("b")}
	b := adValorem(t, "b", "0.1")
	b.Base = determination.Base{Kind: determination.BaseNetPlusComponents, Components: all("a")}
	refuses(t, determination.Schedule{Components: []determination.Component{a, b}}, "cycle")
}

func TestDETREQ0009BracketModeIsNeverInferred(t *testing.T) {
	c := adValorem(t, "b", "0.1")
	c.Rate = determination.Rate{Kind: determination.RateBracket, Bracket: &determination.Bracket{
		Tiers: []determination.Tier{{From: money(t, "0"), Rate: *rateP(t, "0.1")}},
	}}
	refuses(t, determination.Schedule{Components: []determination.Component{c}}, "MARGINAL or CLIFF")
}

func TestDETREQ0010MarginalTiersAscendFromZero(t *testing.T) {
	c := adValorem(t, "b", "0.1")
	c.Rate = determination.Rate{Kind: determination.RateBracket, Bracket: &determination.Bracket{
		Mode: determination.BracketMarginal,
		Tiers: []determination.Tier{
			{From: money(t, "0"), Rate: *rateP(t, "0.1")},
			{From: money(t, "500"), Rate: *rateP(t, "0.2")},
			{From: money(t, "500"), Rate: *rateP(t, "0.3")},
		},
	}}
	refuses(t, determination.Schedule{Components: []determination.Component{c}}, "strictly ascending")
}

func TestDETREQ0008EveryRateCarriesAPolicy(t *testing.T) {
	c := adValorem(t, "a", "0.1")
	c.Policy = fiscal.RoundingPolicy{}
	refuses(t, determination.Schedule{Components: []determination.Component{c}}, "no rounding policy")
}

func TestDETREQ0004CompoundingBaseNamesItsComponents(t *testing.T) {
	c := adValorem(t, "a", "0.1")
	c.Base = determination.Base{Kind: determination.BaseComponentSum}
	refuses(t, determination.Schedule{Components: []determination.Component{c}}, "names no components")
}

func TestDETREQ0003BaseDeclaresItsKind(t *testing.T) {
	c := adValorem(t, "a", "0.1")
	c.Base = determination.Base{}
	refuses(t, determination.Schedule{Components: []determination.Component{c}}, "base kind")
}

func TestDETREQ0007EveryRateDeclaresItsBasis(t *testing.T) {
	// A proportion is a fiscal.Rate, which cannot be built without a basis.
	if _, err := fiscal.ParseRate("0.06", ""); err == nil {
		t.Fatal("a rate parsed without a basis")
	}
	c := adValorem(t, "a", "0.1")
	c.Rate.Proportion = nil
	refuses(t, determination.Schedule{Components: []determination.Component{c}}, "exactly one value")
}

func TestDETREQ0021InclusiveFlatNeedsDeclaredSubtractionOrder(t *testing.T) {
	flat := adValorem(t, "flat", "0")
	flat.Rate = determination.Rate{Kind: determination.RateFlat, Flat: moneyP(t, "4.00")}
	flat.Inclusive = true
	vat := adValorem(t, "vat", "0.21")
	vat.Inclusive = true
	p := line2(t)
	refuses(t, determination.Schedule{Components: []determination.Component{flat, vat}, ExtractionPolicy: &p},
		"not in the declared subtraction order")
}

func TestCompoundingOnACoarseComponentIsRefused(t *testing.T) {
	b := adValorem(t, "b", "0.1")
	b.Policy = fiscaltest.Policy(t, fiscal.RoundHalfUp, 2, fiscal.BasisDocument)
	a := adValorem(t, "a", "0.1")
	a.Base = determination.Base{Kind: determination.BaseNetPlusComponents, Components: all("b")}
	refuses(t, determination.Schedule{Components: []determination.Component{a, b}}, "line-rounded")
}

// ---------------------------------------------------------------------------
// ordering
// ---------------------------------------------------------------------------

func TestDETREQ0012TopologicalOrderBreaksTiesOnComponentID(t *testing.T) {
	// c compounds on both; a and b are independent and tie.
	c := adValorem(t, "c", "0.1")
	c.Base = determination.Base{Kind: determination.BaseComponentSum, Components: all("b", "a")}
	p := compile(t, determination.Schedule{Components: []determination.Component{c, adValorem(t, "b", "0.1"), adValorem(t, "a", "0.1")}})
	got := p.Order()
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("order = %v, want [a b c]", got)
	}
}

func TestDETREQ0013OrderIsNeverInferredFromJurisdictionLevel(t *testing.T) {
	// A city-level id that sorts first evaluates first; nothing about the
	// jurisdiction moves the federal component ahead of it.
	city := adValorem(t, "a-city", "0.01")
	city.JurisdictionID = "jurisdiction:us-ca/la"
	fed := adValorem(t, "z-federal", "0.05")
	fed.JurisdictionID = "jurisdiction:us"
	got := compile(t, determination.Schedule{Components: []determination.Component{fed, city}}).Order()
	if got[0] != "a-city" {
		t.Fatalf("order = %v; a hierarchy default crept in", got)
	}
}

// ---------------------------------------------------------------------------
// exclusive calculation
// ---------------------------------------------------------------------------

func TestDETREQ0015CompoundingUsesRoundedAmounts(t *testing.T) {
	// b = 10.03 * 0.15 = 1.5045 -> 1.50 at HALF_UP/2.
	// a = 1 * b on COMPONENT_SUM(b) at scale 4: compounding on the rounded
	// amount gives 1.5000; compounding on the raw value would give 1.5045.
	b := adValorem(t, "b", "0.15")
	a := adValorem(t, "a", "1")
	a.Base = determination.Base{Kind: determination.BaseComponentSum, Components: all("b")}
	a.Policy = fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 4)
	p := compile(t, determination.Schedule{Components: []determination.Component{a, b}})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "10.03"), Applicable: all("a", "b")})
	if got := amountOf(t, d.Lines[0], "a"); got != "1.5000" {
		t.Fatalf("a = %s, want 1.5000 (compounded on b's rounded 1.50)", got)
	}
}

func TestDETREQ0016ExclusiveRoundsExactlyOnce(t *testing.T) {
	p := compile(t, determination.Schedule{Components: []determination.Component{adValorem(t, "vat", "0.075")}})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "10.05"), Applicable: all("vat")})
	c := d.Lines[0].Components[0]
	// raw 0.75375 carried at full precision; one rounding to 0.75.
	if c.Raw.CanonicalString() != "0.75375" || c.Amount.CanonicalString() != "0.75" {
		t.Fatalf("raw %s amount %s, want 0.75375 and 0.75", c.Raw.CanonicalString(), c.Amount.CanonicalString())
	}
}

func TestThreeLevelDiamondCompound(t *testing.T) {
	// a, b on net 100: 10.00 and 5.00. c on COMPONENT_SUM(a,b) at 0.1: 1.50.
	// d on NET_PLUS_COMPONENTS(a,c) at 0.01: (100 + 10 + 1.50) * 0.01 = 1.115 -> 1.12.
	c := adValorem(t, "c", "0.1")
	c.Base = determination.Base{Kind: determination.BaseComponentSum, Components: all("a", "b")}
	dd := adValorem(t, "d", "0.01")
	dd.Base = determination.Base{Kind: determination.BaseNetPlusComponents, Components: all("a", "c")}
	p := compile(t, determination.Schedule{Components: []determination.Component{dd, c, adValorem(t, "b", "0.05"), adValorem(t, "a", "0.1")}})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "100.00"), Applicable: all("a", "b", "c", "d")})
	for id, want := range map[string]string{"a": "10.00", "b": "5.00", "c": "1.50", "d": "1.12"} {
		if got := amountOf(t, d.Lines[0], id); got != want {
			t.Errorf("%s = %s, want %s", id, got, want)
		}
	}
}

func TestDETREQ0014InapplicableComponentIsZeroAndRecorded(t *testing.T) {
	a := adValorem(t, "a", "0.1")
	a.Base = determination.Base{Kind: determination.BaseNetPlusComponents, Components: all("b")}
	p := compile(t, determination.Schedule{Components: []determination.Component{a, adValorem(t, "b", "0.5")}})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "100.00"), Applicable: all("a")})
	if got := amountOf(t, d.Lines[0], "a"); got != "10.00" {
		t.Fatalf("a = %s, want 10.00 on the bare net", got)
	}
	if !hasStep(d, determination.StepZeroSubstituted, "b did not apply") {
		t.Fatal("the zero substitution for b is not in the trace")
	}
}

func TestDETREQ0005BaseAdjustmentOrderIsDeclared(t *testing.T) {
	discount := determination.BaseAdjustment{Kind: determination.AdjustSubtractProportion, Name: "discount", Proportion: rateP(t, "0.1")}
	exemption := determination.BaseAdjustment{Kind: determination.AdjustSubtractAmount, Name: "exemption", Amount: moneyP(t, "50")}
	run := func(adj ...determination.BaseAdjustment) string {
		c := adValorem(t, "a", "1")
		c.Base.Adjustments = adj
		p := compile(t, determination.Schedule{Components: []determination.Component{c}})
		d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "1000.00"), Applicable: all("a")})
		return amountOf(t, d.Lines[0], "a")
	}
	// 1000 * 0.9 - 50 = 850; (1000 - 50) * 0.9 = 855. Same steps, two laws.
	if a, b := run(discount, exemption), run(exemption, discount); a != "850.00" || b != "855.00" {
		t.Fatalf("discount-first %s, exemption-first %s; want 850.00 and 855.00", a, b)
	}
}

func TestDETREQ0006BaseFloorsAtZeroUnlessPermitted(t *testing.T) {
	exemption := determination.BaseAdjustment{Kind: determination.AdjustSubtractAmount, Name: "exemption", Amount: moneyP(t, "50")}
	c := adValorem(t, "a", "0.1")
	c.Base.Adjustments = []determination.BaseAdjustment{exemption}
	p := compile(t, determination.Schedule{Components: []determination.Component{c}})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "30.00"), Applicable: all("a")})
	if got := amountOf(t, d.Lines[0], "a"); got != "0.00" {
		t.Fatalf("floored a = %s, want 0.00", got)
	}
	if !hasStep(d, determination.StepFloor, "floors at zero") {
		t.Fatal("the floor is not in the trace")
	}

	c.Base.AllowNegative = true
	p = compile(t, determination.Schedule{Components: []determination.Component{c}})
	d = evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "30.00"), Applicable: all("a")})
	if got := amountOf(t, d.Lines[0], "a"); got != "-2.00" {
		t.Fatalf("permitted-negative a = %s, want -2.00", got)
	}
}

func TestEachRateKind(t *testing.T) {
	tiers := []determination.Tier{{From: money(t, "0"), Rate: *rateP(t, "0.1")}, {From: money(t, "1000"), Rate: *rateP(t, "0.2")}}
	marginal := adValorem(t, "marginal", "0")
	marginal.Rate = determination.Rate{Kind: determination.RateBracket, Bracket: &determination.Bracket{Mode: determination.BracketMarginal, Tiers: tiers}}
	cliff := adValorem(t, "cliff", "0")
	cliff.Rate = determination.Rate{Kind: determination.RateBracket, Bracket: &determination.Bracket{Mode: determination.BracketCliff, Tiers: tiers}}
	specific := adValorem(t, "specific", "0")
	specific.Base = determination.Base{Kind: determination.BaseQuantity}
	specific.Rate = determination.Rate{Kind: determination.RateSpecific, PerUnit: moneyP(t, "0.25")}
	flat := adValorem(t, "flat", "0")
	flat.Rate = determination.Rate{Kind: determination.RateFlat, Flat: moneyP(t, "3.50")}

	p := compile(t, determination.Schedule{Components: []determination.Component{marginal, cliff, specific, flat}})
	q, err := fiscal.ParseQuantity("3", "EA")
	if err != nil {
		t.Fatal(err)
	}
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "1500.00"), Quantity: &q,
		Applicable: all("marginal", "cliff", "specific", "flat")})
	// MARGINAL: 1000*0.1 + 500*0.2 = 200. CLIFF: 1500*0.2 = 300.
	// SPECIFIC: 3 * 0.25 = 0.75. FLAT: 3.50.
	for id, want := range map[string]string{"marginal": "200.00", "cliff": "300.00", "specific": "0.75", "flat": "3.50"} {
		if got := amountOf(t, d.Lines[0], id); got != want {
			t.Errorf("%s = %s, want %s", id, got, want)
		}
	}
}

func TestSpecificWithoutQuantityIsRefused(t *testing.T) {
	specific := adValorem(t, "specific", "0")
	specific.Base = determination.Base{Kind: determination.BaseQuantity}
	specific.Rate = determination.Rate{Kind: determination.RateSpecific, PerUnit: moneyP(t, "0.25")}
	p := compile(t, determination.Schedule{Components: []determination.Component{specific}})
	if _, err := p.Evaluate(determination.Document{Currency: eur, Lines: []determination.Line{
		{Key: "1", Amount: money(t, "1"), Applicable: all("specific")},
	}}); err == nil {
		t.Fatal("a per-unit component evaluated with no quantity")
	}
}

// ---------------------------------------------------------------------------
// inclusive extraction
// ---------------------------------------------------------------------------

func inclusive(c determination.Component) determination.Component {
	c.Inclusive = true
	return c
}

func requireExact(t *testing.T, gross string, l determination.LineResult) {
	t.Helper()
	tax, err := l.Tax(eur)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := l.Net.Add(tax)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := sum.Cmp(money(t, gross)); c != 0 {
		t.Fatalf("net %s + tax %s = %s, want %s: extraction left a residual", l.Net, tax, sum, gross)
	}
}

func TestInclusiveExtractionOneComponent(t *testing.T) {
	ext := line2(t)
	p := compile(t, determination.Schedule{Components: []determination.Component{inclusive(adValorem(t, "vat", "0.21"))}, ExtractionPolicy: &ext})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "121.00"), Applicable: all("vat")})
	if d.Lines[0].Net.CanonicalString() != "100.00" || amountOf(t, d.Lines[0], "vat") != "21.00" {
		t.Fatalf("net %s vat %s, want 100.00 and 21.00", d.Lines[0].Net, amountOf(t, d.Lines[0], "vat"))
	}
	requireExact(t, "121.00", d.Lines[0])
}

func TestDETREQ0017InclusiveExtractionIsExactWhereNaiveRatesLeaveAResidual(t *testing.T) {
	// Gross 100.00, two inclusive rates on net, 0.10 and 0.11. M = 1.21.
	// N = 100 / 1.21 = 82.6446... -> 82.64. T = 17.36.
	// Naive: 82.64 * 0.10 = 8.264 -> 8.26 and 82.64 * 0.11 = 9.0904 -> 9.09,
	// which sum with N to 99.99 — a one-cent residual. Extraction cannot.
	ext := line2(t)
	p := compile(t, determination.Schedule{
		Components:       []determination.Component{inclusive(adValorem(t, "a", "0.10")), inclusive(adValorem(t, "b", "0.11"))},
		ExtractionPolicy: &ext,
	})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "100.00"), Applicable: all("a", "b")})
	requireExact(t, "100.00", d.Lines[0])
	if d.Lines[0].Net.CanonicalString() != "82.64" {
		t.Fatalf("net = %s, want 82.64", d.Lines[0].Net)
	}
}

func TestDETREQ0019ExtractedTaxIsComputedBySubtraction(t *testing.T) {
	// Same line: T = 100.00 - 82.64 = 17.36, recorded as a subtraction in the
	// trace, never as a rate application.
	ext := line2(t)
	p := compile(t, determination.Schedule{
		Components:       []determination.Component{inclusive(adValorem(t, "a", "0.10")), inclusive(adValorem(t, "b", "0.11"))},
		ExtractionPolicy: &ext,
	})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "100.00"), Applicable: all("a", "b")})
	if !hasStep(d, determination.StepExtract, "T = 17.36") {
		t.Fatal("the extraction trace does not record T = G - N")
	}
	for _, s := range d.Trace {
		if s.Step == determination.StepRate {
			t.Fatalf("an inclusive component had a rate applied: %+v", s)
		}
	}
}

func TestDETREQ0020ExtractedTaxAllocatesByLargestRemainder(t *testing.T) {
	// T = 17.36 over m = 0.10, 0.11: shares 8.2666.. and 9.0933..; 1735 units
	// placed, 1 residual, to the larger remainder (a's .67): 8.27 and 9.09.
	ext := line2(t)
	p := compile(t, determination.Schedule{
		Components:       []determination.Component{inclusive(adValorem(t, "b", "0.11")), inclusive(adValorem(t, "a", "0.10"))},
		ExtractionPolicy: &ext,
	})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "100.00"), Applicable: all("a", "b")})
	if a, b := amountOf(t, d.Lines[0], "a"), amountOf(t, d.Lines[0], "b"); a != "8.27" || b != "9.09" {
		t.Fatalf("a %s b %s, want 8.27 and 9.09", a, b)
	}
}

func TestDETREQ0018InclusiveCompoundingMultiplier(t *testing.T) {
	// a on net 0.10, b on net+a 0.05: m_a = 0.10, m_b = 0.05 * 1.10 = 0.055,
	// M = 1.155 (never rounded). Gross 115.50 -> N 100.00, T 15.50, split
	// 0.10 : 0.055 -> 10.00 and 5.50.
	ext := line2(t)
	b := inclusive(adValorem(t, "b", "0.05"))
	b.Base = determination.Base{Kind: determination.BaseNetPlusComponents, Components: all("a")}
	p := compile(t, determination.Schedule{Components: []determination.Component{inclusive(adValorem(t, "a", "0.10")), b}, ExtractionPolicy: &ext})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "115.50"), Applicable: all("a", "b")})
	if !hasStep(d, determination.StepMultiplier, "m = 0.0550") {
		t.Fatal("m_b is not carried at full precision as 0.0550")
	}
	if d.Lines[0].Net.CanonicalString() != "100.00" || amountOf(t, d.Lines[0], "a") != "10.00" || amountOf(t, d.Lines[0], "b") != "5.50" {
		t.Fatalf("net %s a %s b %s", d.Lines[0].Net, amountOf(t, d.Lines[0], "a"), amountOf(t, d.Lines[0], "b"))
	}
	requireExact(t, "115.50", d.Lines[0])
}

func TestInclusiveFlatSubtractedBeforeExtraction(t *testing.T) {
	// Gross 125.00 less an inclusive 4.00 levy = 121.00, then VAT 21%:
	// N = 100.00, VAT 21.00, levy 4.00.
	ext := line2(t)
	levy := adValorem(t, "levy", "0")
	levy.Rate = determination.Rate{Kind: determination.RateFlat, Flat: moneyP(t, "4.00")}
	p := compile(t, determination.Schedule{
		Components:                []determination.Component{inclusive(levy), inclusive(adValorem(t, "vat", "0.21"))},
		ExtractionPolicy:          &ext,
		InclusiveSubtractionOrder: all("levy"),
	})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "125.00"), Applicable: all("levy", "vat")})
	if d.Lines[0].Net.CanonicalString() != "100.00" || amountOf(t, d.Lines[0], "vat") != "21.00" || amountOf(t, d.Lines[0], "levy") != "4.00" {
		t.Fatalf("net %s vat %s levy %s", d.Lines[0].Net, amountOf(t, d.Lines[0], "vat"), amountOf(t, d.Lines[0], "levy"))
	}
	requireExact(t, "125.00", d.Lines[0])
}

func TestMixedInclusiveAndExclusiveOnOneLine(t *testing.T) {
	// Inclusive VAT extracted first: 121.00 -> 100.00 + 21.00. Then an
	// exclusive 2% levy on the extracted net: 2.00.
	ext := line2(t)
	p := compile(t, determination.Schedule{
		Components:       []determination.Component{inclusive(adValorem(t, "vat", "0.21")), adValorem(t, "levy", "0.02")},
		ExtractionPolicy: &ext,
	})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "121.00"), Applicable: all("vat", "levy")})
	if amountOf(t, d.Lines[0], "levy") != "2.00" {
		t.Fatalf("levy = %s, want 2.00 on the extracted net", amountOf(t, d.Lines[0], "levy"))
	}
}

// ---------------------------------------------------------------------------
// rounding points
// ---------------------------------------------------------------------------

func TestDETREQ0023DocumentRoundingAllocatesBackExactly(t *testing.T) {
	// Three lines of 1.05 at 10%: raw 0.105 each, 0.315 in all -> 0.32 at the
	// document. Allocated back by raw weight: 10, 10, 10 units placed, 2
	// residual to the first two on the ordinal tie-break: 0.11, 0.11, 0.10.
	// Rounding each line instead would give 0.33.
	vat := adValorem(t, "vat", "0.1")
	vat.Policy = fiscaltest.Policy(t, fiscal.RoundHalfUp, 2, fiscal.BasisDocument)
	p := compile(t, determination.Schedule{Components: []determination.Component{vat}})
	d := evaluate(t, p,
		determination.Line{Key: "1", Amount: money(t, "1.05"), Applicable: all("vat")},
		determination.Line{Key: "2", Amount: money(t, "1.05"), Applicable: all("vat")},
		determination.Line{Key: "3", Amount: money(t, "1.05"), Applicable: all("vat")},
	)
	var got []string
	total := money(t, "0")
	for _, l := range d.Lines {
		got = append(got, amountOf(t, l, "vat"))
		total, _ = total.Add(l.Components[0].Amount)
	}
	if strings.Join(got, ",") != "0.11,0.11,0.10" || total.CanonicalString() != "0.32" {
		t.Fatalf("lines %v total %s, want 0.11,0.11,0.10 and 0.32", got, total.CanonicalString())
	}
}

func TestDETREQ0022RoundingOnlyAtDeclaredPoints(t *testing.T) {
	// A JURISDICTION_TOTAL component is not rounded at the line at all: its
	// line raw survives to the pool, and the only ROUND steps in the trace
	// belong to the line-rounded component.
	j := adValorem(t, "j", "0.1")
	j.Policy = fiscaltest.Policy(t, fiscal.RoundHalfUp, 2, fiscal.BasisJurisdictionTotal)
	p := compile(t, determination.Schedule{Components: []determination.Component{j, adValorem(t, "l", "0.1")}})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "1.05"), Applicable: all("j", "l")})
	for _, s := range d.Trace {
		if s.Step == determination.StepRound && s.Component == "j" {
			t.Fatalf("j was rounded at the line: %+v", s)
		}
	}
	if !hasStep(d, determination.StepCoarseRound, "jurisdiction:jurisdiction:xx") {
		t.Fatal("j's jurisdiction pool was never rounded")
	}
}

func TestDocumentNetBaseAllocatesByLineNet(t *testing.T) {
	// A 1% document levy on 300.00 of net = 3.00, back to lines 100/200 as
	// 1.00 and 2.00.
	levy := adValorem(t, "levy", "0.01")
	levy.Base = determination.Base{Kind: determination.BaseDocumentNet}
	levy.Policy = fiscaltest.Policy(t, fiscal.RoundHalfUp, 2, fiscal.BasisTaxComponent)
	p := compile(t, determination.Schedule{Components: []determination.Component{levy}})
	d := evaluate(t, p,
		determination.Line{Key: "1", Amount: money(t, "100.00"), Applicable: all("levy")},
		determination.Line{Key: "2", Amount: money(t, "200.00"), Applicable: all("levy")},
	)
	if a, b := amountOf(t, d.Lines[0], "levy"), amountOf(t, d.Lines[1], "levy"); a != "1.00" || b != "2.00" {
		t.Fatalf("levy lines %s, %s; want 1.00, 2.00", a, b)
	}
}

// ---------------------------------------------------------------------------
// determinism and adjustments
// ---------------------------------------------------------------------------

func TestEvaluationIsByteIdenticalOnReplay(t *testing.T) {
	ext := line2(t)
	p := compile(t, determination.Schedule{
		Components:       []determination.Component{inclusive(adValorem(t, "a", "0.10")), inclusive(adValorem(t, "b", "0.11")), adValorem(t, "c", "0.02")},
		ExtractionPolicy: &ext,
	})
	line := determination.Line{Key: "1", Amount: money(t, "100.00"), Applicable: all("a", "b", "c")}
	first, err := canonical.Encode(evaluate(t, p, line).Canonical())
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonical.Encode(evaluate(t, p, line).Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("two evaluations of one input differ")
	}
}

func TestReversalNetsToZero(t *testing.T) {
	p := compile(t, determination.Schedule{Components: []determination.Component{adValorem(t, "vat", "0.21"), adValorem(t, "levy", "0.013")}})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "99.99"), Applicable: all("vat", "levy")})
	pos, err := determination.NetPosition(d, determination.Reverse(d))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range pos {
		if !v.IsZero() {
			t.Fatalf("%+v nets to %s after reversal", k, v)
		}
	}
}

func TestDETREQ0031PartialCreditAllocatesAndNeverReappliesRates(t *testing.T) {
	// Original: vat 21.00 and levy 2.00 on 100.00. Credit 10.00 of tax,
	// weighted 21:2 -> 9.130.. and 0.869..: 913 + 86 = 999 units, 1 residual
	// to the larger remainder (levy's .95): -9.13 and -0.87.
	p := compile(t, determination.Schedule{Components: []determination.Component{adValorem(t, "vat", "0.21"), adValorem(t, "levy", "0.02")}})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "100.00"), Applicable: all("vat", "levy")})
	credit, err := determination.PartialCredit(d, money(t, "10.00"), line2(t))
	if err != nil {
		t.Fatal(err)
	}
	if v, l := amountOf(t, credit.Lines[0], "vat"), amountOf(t, credit.Lines[0], "levy"); v != "-9.13" || l != "-0.87" {
		t.Fatalf("credit vat %s levy %s, want -9.13 and -0.87", v, l)
	}
	if _, err := determination.PartialCredit(d, money(t, "23.01"), line2(t)); err == nil {
		t.Fatal("a credit larger than the tax charged was accepted")
	}
}

func TestDETREQ0033AdjustmentChainSumsToItsPosition(t *testing.T) {
	// Charge 21.00, credit 10.00 of it, then reverse the credit: the chain
	// stands at 21.00 again.
	p := compile(t, determination.Schedule{Components: []determination.Component{adValorem(t, "vat", "0.21")}})
	d := evaluate(t, p, determination.Line{Key: "1", Amount: money(t, "100.00"), Applicable: all("vat")})
	credit, err := determination.PartialCredit(d, money(t, "10.00"), line2(t))
	if err != nil {
		t.Fatal(err)
	}
	pos, err := determination.NetPosition(d, credit, determination.Reverse(credit))
	if err != nil {
		t.Fatal(err)
	}
	if got := pos[determination.PositionKey{Line: "1", Component: "vat"}]; got.CanonicalString() != "21.00" {
		t.Fatalf("chain position %s, want 21.00", got.CanonicalString())
	}
}
