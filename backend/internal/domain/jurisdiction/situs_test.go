package jurisdiction_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/jurisdiction"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The ZTAX-JUR-001 §12.2 corpus. The geography is synthetic — countries XA
// and XB are ISO 3166 user-assigned codes — because JUR-001 names no real
// jurisdiction and neither may its tests: what is under test is the
// algorithm, and a real place would invite reading the fixture as content.
//
//	XA ─ north (STATE) ─┬─ alpha (CITY)   lon 0..10   (v2: 0..12, annexing 10..12)
//	                    └─ beta  (CITY)   lon 10..20  (v2: 12..20)
//	XA ─ transit (SPECIAL)                lon 8..14, members alpha and beta
//	XB ─ east (STATE)

var (
	t2026  = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2027  = time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	t2028  = time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
	event  = time.Date(2027, 3, 15, 12, 0, 0, 0, time.UTC)
	seenAt = time.Date(2027, 3, 15, 11, 0, 0, 0, time.UTC)
)

const (
	xa      jurisdiction.ID = "jurisdiction:xa"
	north   jurisdiction.ID = "jurisdiction:xa/north"
	alpha   jurisdiction.ID = "jurisdiction:xa/north/alpha"
	beta    jurisdiction.ID = "jurisdiction:xa/north/beta"
	transit jurisdiction.ID = "jurisdiction:xa/transit"
	xb      jurisdiction.ID = "jurisdiction:xb"
	east    jurisdiction.ID = "jurisdiction:xb/east"
)

func node(id, parent jurisdiction.ID, level jurisdiction.Level, country string) jurisdiction.Node {
	return jurisdiction.Node{
		Jurisdiction:  jurisdiction.Jurisdiction{ID: id, Parent: parent, Level: level, Name: string(id), Country: country, Timezone: "Etc/UTC"},
		EffectiveFrom: t2026,
	}
}

func graph(t *testing.T) *jurisdiction.Graph {
	t.Helper()
	g, err := jurisdiction.NewGraph("graph-2027.01", []jurisdiction.Node{
		node(xa, "", jurisdiction.LevelCountry, "XA"),
		node(north, xa, jurisdiction.LevelState, "XA"),
		node(alpha, north, jurisdiction.LevelCity, "XA"),
		node(beta, north, jurisdiction.LevelCity, "XA"),
		node(transit, "", jurisdiction.LevelSpecial, "XA"),
		node(xb, "", jurisdiction.LevelCountry, "XB"),
		node(east, xb, jurisdiction.LevelState, "XB"),
	}, []jurisdiction.Membership{{Special: transit, Members: []jurisdiction.ID{alpha, beta}}})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func rect(lonFrom, lonTo string) jurisdiction.Polygon {
	return jurisdiction.Polygon{Outer: jurisdiction.Ring{
		{Latitude: "0", Longitude: lonFrom}, {Latitude: "0", Longitude: lonTo},
		{Latitude: "10", Longitude: lonTo}, {Latitude: "10", Longitude: lonFrom},
	}}
}

func dataset(t *testing.T, version, alphaTo string, extra ...jurisdiction.Boundary) *jurisdiction.BoundaryDataset {
	t.Helper()
	bs := append([]jurisdiction.Boundary{
		{Jurisdiction: north, Level: jurisdiction.LevelState, Polygons: []jurisdiction.Polygon{rect("0", "20")}},
		{Jurisdiction: alpha, Level: jurisdiction.LevelCity, Polygons: []jurisdiction.Polygon{rect("0", alphaTo)}},
		{Jurisdiction: beta, Level: jurisdiction.LevelCity, Polygons: []jurisdiction.Polygon{rect(alphaTo, "20")}},
		{Jurisdiction: transit, Level: jurisdiction.LevelSpecial, Polygons: []jurisdiction.Polygon{rect("8", "14")}},
	}, extra...)
	d, err := jurisdiction.NewBoundaryDataset("boundary:xa", version, "synthetic test geography", t2026, bs)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func strong(types ...jurisdiction.EvidenceType) map[jurisdiction.EvidenceType]jurisdiction.ReliabilityClass {
	m := map[jurisdiction.EvidenceType]jurisdiction.ReliabilityClass{}
	for _, ty := range types {
		m[ty] = jurisdiction.ReliabilityStrong
	}
	return m
}

func precedenceRule(order ...jurisdiction.EvidenceType) jurisdiction.SitusRule {
	return jurisdiction.SitusRule{
		ID: "precedence", Mode: jurisdiction.ModePrecedence, Order: order,
		MinimumReliability: jurisdiction.ReliabilityStrong, Reliability: strong(order...),
	}
}

func quorumRule(required int, level jurisdiction.Level, eligible ...jurisdiction.EvidenceType) jurisdiction.SitusRule {
	return jurisdiction.SitusRule{
		ID: "quorum", Mode: jurisdiction.ModeQuorum, Required: required, AgreementLevel: level,
		Eligible: eligible, Reliability: strong(eligible...),
	}
}

func spatialRule(ds string) jurisdiction.SitusRule {
	return jurisdiction.SitusRule{
		ID: "spatial", Mode: jurisdiction.ModeSpatial, Dataset: ds, OnOutside: jurisdiction.OutsideInsufficient,
		CoordinateSource: []jurisdiction.EvidenceType{jurisdiction.EvidenceDeviceCoordinates},
		Reliability:      strong(jurisdiction.EvidenceDeviceCoordinates),
	}
}

func resolver(t *testing.T, rule jurisdiction.SitusRule, datasets ...*jurisdiction.BoundaryDataset) *jurisdiction.Resolver {
	t.Helper()
	rs, err := jurisdiction.NewRuleSet([]jurisdiction.Binding{{Rule: rule}}, jurisdiction.RoleB2C)
	if err != nil {
		t.Fatal(err)
	}
	r := &jurisdiction.Resolver{Graph: graph(t), Rules: rs, Datasets: map[string]*jurisdiction.BoundaryDataset{}}
	for _, d := range datasets {
		r.Datasets[d.Ref().String()] = d
	}
	return r
}

func at(ty jurisdiction.EvidenceType, id jurisdiction.ID) jurisdiction.SitusEvidence {
	return jurisdiction.SitusEvidence{Type: ty, Source: jurisdiction.SourceSellerRecorded, CollectedAt: seenAt, Jurisdiction: id}
}

func country(ty jurisdiction.EvidenceType, cc string) jurisdiction.SitusEvidence {
	return jurisdiction.SitusEvidence{Type: ty, Source: jurisdiction.SourceNetworkDerived, CollectedAt: seenAt, Country: cc}
}

func point(lat, lon string) jurisdiction.SitusEvidence {
	return jurisdiction.SitusEvidence{
		Type: jurisdiction.EvidenceDeviceCoordinates, Source: jurisdiction.SourceNetworkDerived, CollectedAt: seenAt,
		Point: &jurisdiction.Point{Latitude: lat, Longitude: lon},
	}
}

func resolve(t *testing.T, r *jurisdiction.Resolver, items ...jurisdiction.SitusEvidence) jurisdiction.SitusResolution {
	t.Helper()
	res, err := r.Resolve(jurisdiction.Request{EventTime: event, Evidence: items, Covered: []string{"XA", "XB"}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return res
}

func ids(res jurisdiction.SitusResolution) string {
	var s []string
	for _, j := range res.Applicable {
		s = append(s, string(j.ID))
	}
	return strings.Join(s, " ")
}

// ---------------------------------------------------------------------------
// graph and datasets
// ---------------------------------------------------------------------------

func TestJURREQ0001EveryNodeCarriesIdentityLevelCountryTimezoneAndPeriod(t *testing.T) {
	for name, mutate := range map[string]func(*jurisdiction.Node){
		"no timezone": func(n *jurisdiction.Node) { n.Timezone = "" },
		"no period":   func(n *jurisdiction.Node) { n.EffectiveFrom = time.Time{} },
		"bad country": func(n *jurisdiction.Node) { n.Country = "xa" },
		"bad level":   func(n *jurisdiction.Node) { n.Level = "PROVINCE" },
		"bad id":      func(n *jurisdiction.Node) { n.ID = "xa" },
	} {
		n := node(xa, "", jurisdiction.LevelCountry, "XA")
		mutate(&n)
		if _, err := jurisdiction.NewGraph("v", []jurisdiction.Node{n}, nil); err == nil {
			t.Errorf("%s: graph accepted", name)
		}
	}
}

func TestJURREQ0002AClosedJurisdictionStillResolvesHistorically(t *testing.T) {
	closed := node(east, xb, jurisdiction.LevelState, "XB")
	closed.EffectiveTo = &t2028
	g, err := jurisdiction.NewGraph("v", []jurisdiction.Node{node(xb, "", jurisdiction.LevelCountry, "XB"), closed}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Chain(east, event); err != nil {
		t.Fatalf("a decision from before closure no longer resolves: %v", err)
	}
	if _, ok := g.At(east, t2028.Add(time.Hour)); ok {
		t.Fatal("a closed jurisdiction resolves after its closure")
	}
	if _, ok := g.Node(east); !ok {
		t.Fatal("closing a jurisdiction deleted it")
	}
}

func TestGraphRefusesAParentAtTheSameOrInnerLevel(t *testing.T) {
	_, err := jurisdiction.NewGraph("v", []jurisdiction.Node{
		node(xa, "", jurisdiction.LevelCountry, "XA"),
		node(north, alpha, jurisdiction.LevelState, "XA"),
		node(alpha, north, jurisdiction.LevelCity, "XA"),
	}, nil)
	if err == nil {
		t.Fatal("a cycle through a state whose parent is a city was accepted")
	}
}

func TestJURREQ0003SpatialResolutionRecordsTheDatasetVersion(t *testing.T) {
	ds := dataset(t, "2027.01", "10")
	res := resolve(t, resolver(t, spatialRule(ds.Ref().String()), ds), point("5", "3"))
	if res.Outcome != jurisdiction.OutcomeResolved || res.Dataset == nil || res.Dataset.Version != "2027.01" {
		t.Fatalf("outcome %s dataset %+v", res.Outcome, res.Dataset)
	}
	if !res.Dataset.Digest.Equal(ds.Ref().Digest) {
		t.Fatal("the recorded digest is not the dataset's")
	}
}

func TestJURREQ0004ReplayResolvesAgainstTheRecordedDatasetVersion(t *testing.T) {
	// Alpha annexes lon 10..12 in 2028.01. A point at lon 11 was in beta
	// under the 2027 dataset. A replay pinned to 2027.01 must still say beta,
	// whatever the current dataset says.
	v1, v2 := dataset(t, "2027.01", "10"), dataset(t, "2028.01", "12")
	historic := resolve(t, resolver(t, spatialRule(v1.Ref().String()), v1, v2), point("5", "11"))
	current := resolve(t, resolver(t, spatialRule(v2.Ref().String()), v1, v2), point("5", "11"))
	if !strings.Contains(ids(historic), string(beta)) || strings.Contains(ids(historic), string(alpha)) {
		t.Fatalf("pinned to 2027.01, resolved %s", ids(historic))
	}
	if !strings.Contains(ids(current), string(alpha)) {
		t.Fatalf("pinned to 2028.01, resolved %s", ids(current))
	}
}

func TestJURREQ0005ADatasetVersionIsImmutable(t *testing.T) {
	poly := rect("0", "10")
	bs := []jurisdiction.Boundary{{Jurisdiction: alpha, Level: jurisdiction.LevelCity, Polygons: []jurisdiction.Polygon{poly}}}
	d, err := jurisdiction.NewBoundaryDataset("boundary:xa", "1", "test", t2026, bs)
	if err != nil {
		t.Fatal(err)
	}
	digest := d.Ref().Digest
	// Mutate what was passed in, and what came back out.
	poly.Outer[1].Longitude = "99"
	got := d.Boundaries()
	got[0].Polygons[0].Outer[2].Longitude = "99"
	again, _ := jurisdiction.NewBoundaryDataset("boundary:xa", "1", "test", t2026,
		[]jurisdiction.Boundary{{Jurisdiction: alpha, Level: jurisdiction.LevelCity, Polygons: []jurisdiction.Polygon{rect("0", "10")}}})
	if !again.Ref().Digest.Equal(digest) {
		t.Fatal("the dataset changed after construction")
	}
	in, err := d.Containing(jurisdiction.Point{Latitude: "5", Longitude: "50"})
	if err != nil || len(in) != 0 {
		t.Fatalf("a mutation reached the sealed dataset: %v %v", in, err)
	}
}

func TestJURREQ0013OverlappingBoundariesAreADatasetDefect(t *testing.T) {
	overlap := jurisdiction.Boundary{Jurisdiction: "jurisdiction:xa/north/rogue", Level: jurisdiction.LevelCity,
		Polygons: []jurisdiction.Polygon{rect("2", "4")}}
	ds := dataset(t, "bad", "10", overlap)
	res := resolve(t, resolver(t, spatialRule(ds.Ref().String()), ds), point("5", "3"))
	if res.Outcome != jurisdiction.OutcomeDatasetDefect || !res.Incident {
		t.Fatalf("outcome %s incident %t, want DATASET_DEFECT and an incident", res.Outcome, res.Incident)
	}
}

func TestSpecialDistrictOverlappingTwoCities(t *testing.T) {
	ds := dataset(t, "2027.01", "10")
	for lon, city := range map[string]jurisdiction.ID{"9": alpha, "13": beta} {
		res := resolve(t, resolver(t, spatialRule(ds.Ref().String()), ds), point("5", lon))
		got := ids(res)
		if !strings.Contains(got, string(city)) || !strings.Contains(got, string(transit)) {
			t.Errorf("lon %s resolved %s; want %s and the transit district", lon, got, city)
		}
	}
}

func TestPointOutsideAllBoundaries(t *testing.T) {
	ds := dataset(t, "2027.01", "10")
	res := resolve(t, resolver(t, spatialRule(ds.Ref().String()), ds), point("50", "50"))
	if res.Outcome != jurisdiction.OutcomeInsufficientEvidence {
		t.Fatalf("outcome %s", res.Outcome)
	}
	rule := spatialRule(ds.Ref().String())
	rule.OnOutside, rule.Parent = jurisdiction.OutsideFallBackToParent, north
	res = resolve(t, resolver(t, rule, ds), point("50", "50"))
	if res.Outcome != jurisdiction.OutcomeResolved || !strings.Contains(ids(res), string(north)) {
		t.Fatalf("fallback: outcome %s applicable %s", res.Outcome, ids(res))
	}
}

// ---------------------------------------------------------------------------
// rules
// ---------------------------------------------------------------------------

func TestJURREQ0006UnknownEvidenceTypeRefusesTheBundle(t *testing.T) {
	rule := precedenceRule("SATELLITE_FIX")
	if _, err := jurisdiction.NewRuleSet([]jurisdiction.Binding{{Rule: rule}}, jurisdiction.RoleB2C); err == nil {
		t.Fatal("a rule reading an invented evidence type was accepted")
	}
	if err := (jurisdiction.SitusEvidence{Type: "SATELLITE_FIX", Source: jurisdiction.SourceNetworkDerived, CollectedAt: seenAt, Country: "XA"}).Validate(); err == nil {
		t.Fatal("evidence of an invented type was accepted")
	}
}

func TestJURREQ0007ReliabilityIsAssignedByTheRule(t *testing.T) {
	rule := precedenceRule(jurisdiction.EvidenceBillingAddress, jurisdiction.EvidenceIPGeolocation)
	delete(rule.Reliability, jurisdiction.EvidenceIPGeolocation)
	if _, err := jurisdiction.NewRuleSet([]jurisdiction.Binding{{Rule: rule}}, jurisdiction.RoleB2C); err == nil {
		t.Fatal("a rule reading a type it gives no reliability was accepted")
	}
	// The same type is usable under one rule and not under another.
	weak := precedenceRule(jurisdiction.EvidenceIPGeolocation)
	weak.Reliability[jurisdiction.EvidenceIPGeolocation] = jurisdiction.ReliabilityWeak
	if res := resolve(t, resolver(t, weak), country(jurisdiction.EvidenceIPGeolocation, "XA")); res.Outcome != jurisdiction.OutcomeInsufficientEvidence {
		t.Fatalf("a WEAK item satisfied a STRONG minimum: %s", res.Outcome)
	}
	if res := resolve(t, resolver(t, precedenceRule(jurisdiction.EvidenceIPGeolocation)), country(jurisdiction.EvidenceIPGeolocation, "XA")); res.Outcome != jurisdiction.OutcomeResolved {
		t.Fatalf("a STRONG item did not resolve: %s", res.Outcome)
	}
}

func TestJURREQ0012QuorumDeclaresItsAgreementLevel(t *testing.T) {
	rule := quorumRule(2, "", jurisdiction.EvidenceBillingAddress, jurisdiction.EvidenceIPGeolocation)
	if _, err := jurisdiction.NewRuleSet([]jurisdiction.Binding{{Rule: rule}}, jurisdiction.RoleB2C); err == nil {
		t.Fatal("a quorum rule with no agreement level was accepted")
	}
}

func TestJURREQ0014EqualSpecificityRefusesTheBundle(t *testing.T) {
	a := precedenceRule(jurisdiction.EvidenceBillingAddress)
	a.ID = "a"
	b := precedenceRule(jurisdiction.EvidenceServiceAddress)
	b.ID = "b"
	_, err := jurisdiction.NewRuleSet([]jurisdiction.Binding{
		{Ontology: "ontology:telecom/voice", Rule: a},
		{Role: jurisdiction.RoleB2B, Rule: b},
	}, jurisdiction.RoleB2C)
	if err == nil || !strings.Contains(err.Error(), "equal specificity") {
		t.Fatalf("two rules tying on (voice, B2B) were accepted: %v", err)
	}
	// Disjoint selectors at equal specificity are fine.
	if _, err := jurisdiction.NewRuleSet([]jurisdiction.Binding{
		{Role: jurisdiction.RoleB2C, Rule: a}, {Role: jurisdiction.RoleB2B, Rule: b},
	}, jurisdiction.RoleB2C); err != nil {
		t.Fatalf("disjoint selectors refused: %v", err)
	}
}

// ---------------------------------------------------------------------------
// modes
// ---------------------------------------------------------------------------

func TestJURREQ0010PrecedenceStopsAtTheFirstUsableItem(t *testing.T) {
	// The PPU says alpha; three later items agree on beta. Precedence does not
	// become a vote.
	r := resolver(t, precedenceRule(jurisdiction.EvidencePrimaryPlaceOfUse, jurisdiction.EvidenceServiceAddress, jurisdiction.EvidenceBillingAddress))
	res := resolve(t, r,
		at(jurisdiction.EvidenceServiceAddress, beta), at(jurisdiction.EvidenceBillingAddress, beta),
		at(jurisdiction.EvidencePrimaryPlaceOfUse, alpha))
	if res.Outcome != jurisdiction.OutcomeResolved || !strings.Contains(ids(res), string(alpha)) || strings.Contains(ids(res), string(beta)) {
		t.Fatalf("outcome %s applicable %s; want alpha and not beta", res.Outcome, ids(res))
	}
}

func TestPrecedenceAndQuorumReachDifferentAnswersOnOneConflict(t *testing.T) {
	// Billing says XA; IP and bank say XB.
	items := []jurisdiction.SitusEvidence{
		at(jurisdiction.EvidenceBillingAddress, alpha),
		country(jurisdiction.EvidenceIPGeolocation, "XB"),
		country(jurisdiction.EvidenceBankLocation, "XB"),
	}
	p := resolve(t, resolver(t, precedenceRule(jurisdiction.EvidenceBillingAddress, jurisdiction.EvidenceIPGeolocation)), items...)
	q := resolve(t, resolver(t, quorumRule(2, jurisdiction.LevelCountry,
		jurisdiction.EvidenceBillingAddress, jurisdiction.EvidenceIPGeolocation, jurisdiction.EvidenceBankLocation)), items...)
	if !strings.Contains(ids(p), string(xa)) || ids(q) != string(xb) {
		t.Fatalf("precedence %s, quorum %s; want XA and XB", ids(p), ids(q))
	}
}

func TestJURREQ0011QuorumDistinguishesInsufficientFromAmbiguous(t *testing.T) {
	rule := quorumRule(2, jurisdiction.LevelCountry,
		jurisdiction.EvidenceBillingAddress, jurisdiction.EvidenceIPGeolocation,
		jurisdiction.EvidenceBankLocation, jurisdiction.EvidenceSIMCountry)
	thin := resolve(t, resolver(t, rule),
		at(jurisdiction.EvidenceBillingAddress, alpha), country(jurisdiction.EvidenceIPGeolocation, "XB"))
	split := resolve(t, resolver(t, rule),
		at(jurisdiction.EvidenceBillingAddress, alpha), country(jurisdiction.EvidenceSIMCountry, "XA"),
		country(jurisdiction.EvidenceIPGeolocation, "XB"), country(jurisdiction.EvidenceBankLocation, "XB"))
	if thin.Outcome != jurisdiction.OutcomeInsufficientEvidence || split.Outcome != jurisdiction.OutcomeAmbiguous {
		t.Fatalf("thin %s, split %s; want INSUFFICIENT_EVIDENCE and AMBIGUOUS", thin.Outcome, split.Outcome)
	}
}

func TestQuorumAgreementIsJudgedAtTheDeclaredLevel(t *testing.T) {
	// Alpha and beta agree at COUNTRY level and contradict at CITY level.
	items := []jurisdiction.SitusEvidence{at(jurisdiction.EvidenceBillingAddress, alpha), at(jurisdiction.EvidenceFixedLineLocation, beta)}
	types := []jurisdiction.EvidenceType{jurisdiction.EvidenceBillingAddress, jurisdiction.EvidenceFixedLineLocation}
	if res := resolve(t, resolver(t, quorumRule(2, jurisdiction.LevelCountry, types...)), items...); res.Outcome != jurisdiction.OutcomeResolved {
		t.Fatalf("country-level agreement: %s", res.Outcome)
	}
	if res := resolve(t, resolver(t, quorumRule(2, jurisdiction.LevelCity, types...)), items...); res.Outcome != jurisdiction.OutcomeInsufficientEvidence {
		t.Fatalf("city-level contradiction: %s", res.Outcome)
	}
}

func TestJURREQ0009AbsentEvidenceIsNeverSubstituted(t *testing.T) {
	// The rule reads SERVICE_ADDRESS only. A billing address is present, and
	// it is not a service address.
	res := resolve(t, resolver(t, precedenceRule(jurisdiction.EvidenceServiceAddress)), at(jurisdiction.EvidenceBillingAddress, alpha))
	if res.Outcome != jurisdiction.OutcomeInsufficientEvidence {
		t.Fatalf("outcome %s; a billing address stood in for a service address", res.Outcome)
	}
}

func TestJURREQ0025InsufficientEvidenceNamesWhatWouldSatisfyTheRule(t *testing.T) {
	res := resolve(t, resolver(t, precedenceRule(jurisdiction.EvidenceServiceAddress, jurisdiction.EvidenceBillingAddress)))
	if len(res.NeededEvidence) != 2 || res.NeededEvidence[0] != jurisdiction.EvidenceServiceAddress {
		t.Fatalf("needed %v", res.NeededEvidence)
	}
}

func TestJURREQ0024EveryRefusalIsARecordedOutcomeNotAnError(t *testing.T) {
	r := resolver(t, precedenceRule(jurisdiction.EvidenceServiceAddress))
	res, err := r.Resolve(jurisdiction.Request{EventTime: event, Covered: []string{"XA"}})
	if err != nil {
		t.Fatalf("an insufficient-evidence resolution returned an error: %v", err)
	}
	if res.Outcome != jurisdiction.OutcomeInsufficientEvidence {
		t.Fatalf("outcome %s", res.Outcome)
	}
	uncovered, err := r.Resolve(jurisdiction.Request{EventTime: event, Evidence: []jurisdiction.SitusEvidence{at(jurisdiction.EvidenceServiceAddress, east)}})
	if err != nil || uncovered.Outcome != jurisdiction.OutcomeUnsupported || len(uncovered.Applicable) == 0 {
		t.Fatalf("uncovered: %v %s %v", err, uncovered.Outcome, uncovered.Applicable)
	}
}

// ---------------------------------------------------------------------------
// evidence, ordering, trace
// ---------------------------------------------------------------------------

func TestJURREQ0008UnusedEvidenceIsRecorded(t *testing.T) {
	res := resolve(t, resolver(t, precedenceRule(jurisdiction.EvidenceServiceAddress)),
		at(jurisdiction.EvidenceServiceAddress, alpha), country(jurisdiction.EvidenceIPGeolocation, "XB"), country(jurisdiction.EvidenceBankLocation, "XA"))
	if len(res.Winning) != 1 || len(res.Unused) != 2 {
		t.Fatalf("winning %d unused %d; want 1 and 2", len(res.Winning), len(res.Unused))
	}
}

func TestJURREQ0023ApplicableSetIsOutermostFirst(t *testing.T) {
	ds := dataset(t, "2027.01", "10")
	res := resolve(t, resolver(t, spatialRule(ds.Ref().String()), ds), point("5", "9"))
	want := strings.Join([]string{string(xa), string(north), string(alpha), string(transit)}, " ")
	if ids(res) != want {
		t.Fatalf("applicable %s, want %s", ids(res), want)
	}
}

func TestJURREQ0026ResolutionIsIndependentOfEvidenceOrder(t *testing.T) {
	r := resolver(t, quorumRule(2, jurisdiction.LevelCountry,
		jurisdiction.EvidenceBillingAddress, jurisdiction.EvidenceIPGeolocation, jurisdiction.EvidenceBankLocation))
	a := []jurisdiction.SitusEvidence{at(jurisdiction.EvidenceBillingAddress, alpha), country(jurisdiction.EvidenceIPGeolocation, "XA"), country(jurisdiction.EvidenceBankLocation, "XB")}
	b := []jurisdiction.SitusEvidence{a[2], a[0], a[1]}
	ea, _ := canonical.Encode(resolve(t, r, a...).Canonical())
	eb, _ := canonical.Encode(resolve(t, r, b...).Canonical())
	if !bytes.Equal(ea, eb) {
		t.Fatal("two orders of the same evidence resolved to different bytes")
	}
}

func TestJURREQ0027EveryResolutionEmitsATrace(t *testing.T) {
	res := resolve(t, resolver(t, precedenceRule(jurisdiction.EvidenceServiceAddress)), at(jurisdiction.EvidenceServiceAddress, alpha))
	steps := map[string]bool{}
	for _, s := range res.Trace {
		steps[s.Step] = true
	}
	for _, want := range []string{jurisdiction.StepRole, jurisdiction.StepSelect, jurisdiction.StepWin, jurisdiction.StepHierarchy, jurisdiction.StepOutcome} {
		if !steps[want] {
			t.Errorf("trace has no %s step", want)
		}
	}
}

// ---------------------------------------------------------------------------
// PPU, roaming, place of supply
// ---------------------------------------------------------------------------

func TestJURREQ0015PPUResolvesAtEventTimeNotDecisionTime(t *testing.T) {
	moved := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	ppus := []jurisdiction.PrimaryPlaceOfUse{
		{Jurisdiction: alpha, From: t2026, To: &moved, Source: jurisdiction.SourceCustomerAsserted, CollectedAt: t2026},
		// Declared in June, effective from March: the customer told us late.
		{Jurisdiction: beta, From: moved, Source: jurisdiction.SourceCustomerAsserted, CollectedAt: t2027},
	}
	g := graph(t)
	for _, p := range ppus {
		if err := jurisdiction.ValidatePPU(g, p); err != nil {
			t.Fatal(err)
		}
	}
	before, ok, err := jurisdiction.PPUEvidenceAt(ppus, time.Date(2027, 2, 10, 0, 0, 0, 0, time.UTC))
	if err != nil || !ok || before.Jurisdiction != alpha {
		t.Fatalf("February: %v %t %s", err, ok, before.Jurisdiction)
	}
	after, ok, err := jurisdiction.PPUEvidenceAt(ppus, event)
	if err != nil || !ok || after.Jurisdiction != beta {
		t.Fatalf("mid-March: %v %t %s", err, ok, after.Jurisdiction)
	}
	if err := jurisdiction.ValidatePPU(g, jurisdiction.PrimaryPlaceOfUse{Jurisdiction: "jurisdiction:xa/nowhere", From: t2026,
		Source: jurisdiction.SourceCustomerAsserted, CollectedAt: t2026}); err == nil {
		t.Fatal("a PPU naming a jurisdiction that does not exist was recorded")
	}
}

func roamingRule(sourcing jurisdiction.RoamingSourcing) jurisdiction.SitusRule {
	r := precedenceRule(jurisdiction.EvidenceNetworkLocation, jurisdiction.EvidenceSIMCountry)
	r.Roaming = sourcing
	return r
}

func roamingItems() []jurisdiction.SitusEvidence {
	return []jurisdiction.SitusEvidence{at(jurisdiction.EvidenceNetworkLocation, east), country(jurisdiction.EvidenceSIMCountry, "XA")}
}

func TestJURREQ0017RoamingIsRecordedAsADerivedFact(t *testing.T) {
	res := resolve(t, resolver(t, roamingRule(jurisdiction.RoamingVisited)), roamingItems()...)
	if res.Roaming == nil || !res.Roaming.Roaming || res.Roaming.Home != "XA" || res.Roaming.Visited != "XB" {
		t.Fatalf("roaming %+v", res.Roaming)
	}
	if ids(res) != string(xb)+" "+string(east) {
		t.Fatalf("visited sourcing resolved %s", ids(res))
	}
	home := resolve(t, resolver(t, roamingRule(jurisdiction.RoamingHome)), roamingItems()...)
	if ids(home) != string(xa) {
		t.Fatalf("home sourcing resolved %s", ids(home))
	}
}

func TestJURREQ0018RoamingSourcingHasNoRuntimeDefault(t *testing.T) {
	res := resolve(t, resolver(t, roamingRule("")), roamingItems()...)
	if res.Outcome != jurisdiction.OutcomeUnsupported {
		t.Fatalf("roaming under a rule with no declared sourcing: %s", res.Outcome)
	}
}

func TestJURREQ0019ResolutionNeverApportions(t *testing.T) {
	res := resolve(t, resolver(t, roamingRule(jurisdiction.RoamingBoth)), roamingItems()...)
	if ids(res) != strings.Join([]string{string(xa), string(xb), string(east)}, " ") {
		t.Fatalf("BOTH resolved %s; want both claims whole", ids(res))
	}
}

func vat(status jurisdiction.VATValidation) jurisdiction.SitusEvidence {
	return jurisdiction.SitusEvidence{Type: jurisdiction.EvidenceVATIdentification, Source: jurisdiction.SourceThirdPartyVerified,
		CollectedAt: seenAt, Country: "XA", VATValidation: status}
}

func TestJURREQ0020UndeterminedIsARoleAndItsTreatmentIsContent(t *testing.T) {
	if _, err := jurisdiction.NewRuleSet(nil, ""); err == nil {
		t.Fatal("a rule set that does not say how UNDETERMINED is treated was accepted")
	}
	b2c := precedenceRule(jurisdiction.EvidenceBillingAddress)
	b2c.ID = "b2c"
	und := precedenceRule(jurisdiction.EvidenceServiceAddress)
	und.ID = "undetermined"
	rs, err := jurisdiction.NewRuleSet([]jurisdiction.Binding{
		{Role: jurisdiction.RoleB2C, Rule: b2c}, {Role: jurisdiction.RoleUndetermined, Rule: und},
	}, jurisdiction.RoleUndetermined)
	if err != nil {
		t.Fatal(err)
	}
	r := &jurisdiction.Resolver{Graph: graph(t), Rules: rs}
	res, err := r.Resolve(jurisdiction.Request{EventTime: event, Covered: []string{"XA"},
		Evidence: []jurisdiction.SitusEvidence{at(jurisdiction.EvidenceServiceAddress, alpha)}})
	if err != nil {
		t.Fatal(err)
	}
	if res.RuleID != "undetermined" || res.Supply.CustomerRole != jurisdiction.RoleUndetermined {
		t.Fatalf("rule %s role %s; UNDETERMINED was collapsed into B2C", res.RuleID, res.Supply.CustomerRole)
	}
}

func TestJURREQ0021VATValidationStatusIsRecordedIncludingUnavailability(t *testing.T) {
	r := resolver(t, precedenceRule(jurisdiction.EvidenceServiceAddress))
	for status, role := range map[jurisdiction.VATValidation]jurisdiction.CustomerRole{
		jurisdiction.VATValidated:             jurisdiction.RoleB2B,
		jurisdiction.VATUnvalidated:           jurisdiction.RoleUndetermined,
		jurisdiction.VATValidationUnavailable: jurisdiction.RoleUndetermined,
	} {
		res := resolve(t, r, at(jurisdiction.EvidenceServiceAddress, alpha), vat(status))
		if res.Supply.VATValidation != status || res.Supply.CustomerRole != role {
			t.Errorf("%s: recorded %s role %s", status, res.Supply.VATValidation, res.Supply.CustomerRole)
		}
	}
}

func TestPlaceOfSupplyRecordsCrossBorder(t *testing.T) {
	res := resolve(t, resolver(t, precedenceRule(jurisdiction.EvidenceServiceAddress)),
		at(jurisdiction.EvidenceServiceAddress, alpha), at(jurisdiction.EvidenceSellerEstablishment, east))
	if res.Supply.PlaceOfSupply != alpha || res.Supply.SupplierJurisdiction != east || !res.Supply.CrossBorder {
		t.Fatalf("supply %+v", res.Supply)
	}
}
