package ontology_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ontology"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

var (
	t2026   = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2027   = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	when    = time.Date(2027, 3, 15, 0, 0, 0, 0, time.UTC)
	tenantA = id.NewTenantID(uuid.MustParse("00000000-0000-7000-8000-00000000000a"))
	tenantB = id.NewTenantID(uuid.MustParse("00000000-0000-7000-8000-00000000000b"))
)

const (
	mobileVoice ontology.ID = "ontology:telecom/voice/mobile"
	mobileData  ontology.ID = "ontology:telecom/data/mobile"
	broadband   ontology.ID = "ontology:telecom/data/fixed-broadband"
	handset     ontology.ID = "ontology:equipment/handset/sale"
)

func node(nid ontology.ID, fam ontology.Family, version int, from time.Time, to *time.Time) ontology.Node {
	return ontology.Node{
		ID: nid, Version: version, Family: fam, Name: string(nid), Definition: "A communications service: " + string(nid),
		Owner: "lane-g", Lifecycle: ontology.LifecycleActive, Nature: ontology.NatureService,
		ValidFrom: from, ValidTo: to, RecordedAt: from,
	}
}

func release(t *testing.T) *ontology.Ontology {
	t.Helper()
	v1End := t2027
	o, err := ontology.New("ont-2027.1", []ontology.Node{
		node(mobileVoice, ontology.FamilyMobileVoice, 1, t2026, &v1End),
		node(mobileVoice, ontology.FamilyMobileVoice, 2, t2027, nil),
		node(mobileData, ontology.FamilyMobileData, 1, t2026, nil),
		node(broadband, ontology.FamilyFixedBroadband, 1, t2026, nil),
		func() ontology.Node {
			n := node(handset, ontology.FamilyEquipmentSale, 1, t2026, nil)
			n.Nature, n.Definition = ontology.NatureGoodSale, "A handset sold outright."
			return n
		}(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestCLSREQ0002IDsAreNeverRepointedAtAnotherThing(t *testing.T) {
	end := t2027
	_, err := ontology.New("bad", []ontology.Node{
		node(mobileVoice, ontology.FamilyMobileVoice, 1, t2026, &end),
		node(mobileVoice, ontology.FamilyFixedBroadband, 2, t2027, nil),
	}, nil)
	if err == nil {
		t.Fatal("an id changed family between versions")
	}
}

func TestCLSREQ0003DefinitionsCarryNoTaxConclusion(t *testing.T) {
	n := node(mobileVoice, ontology.FamilyMobileVoice, 1, t2026, nil)
	n.Definition = "Mobile voice, exempt from sales tax in most places."
	if _, err := ontology.New("bad", []ontology.Node{n}, nil); err == nil || !strings.Contains(err.Error(), "taxed") {
		t.Fatalf("a definition with a tax conclusion was accepted: %v", err)
	}
}

func TestCLSREQ0094To0099VersionsAreValidTimedAndHistoryStaysResolvable(t *testing.T) {
	o := release(t)
	v, ok := o.At(mobileVoice, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	if !ok || v.Version != 1 {
		t.Fatalf("2026 resolved v%d", v.Version)
	}
	v, ok = o.At(mobileVoice, when)
	if !ok || v.Version != 2 || v.RecordedAt.IsZero() {
		t.Fatalf("2027 resolved v%d", v.Version)
	}
	end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if _, err := ontology.New("bad", []ontology.Node{
		node(mobileVoice, ontology.FamilyMobileVoice, 1, t2026, nil),
		node(mobileVoice, ontology.FamilyMobileVoice, 2, end, nil),
	}, nil); err == nil {
		t.Fatal("two versions overlapping in valid time were accepted")
	}
}

func TestCLSREQ0095DeprecatedNodesNameAReplacement(t *testing.T) {
	n := node(mobileVoice, ontology.FamilyMobileVoice, 1, t2026, nil)
	n.Lifecycle = ontology.LifecycleDeprecated
	if _, err := ontology.New("bad", []ontology.Node{n}, nil); err == nil {
		t.Fatal("a deprecated node with no replacement was accepted")
	}
}

func TestCLSREQ0016To0021TechnologyIsAQualifierNotAFamily(t *testing.T) {
	for _, q := range []ontology.Qualifier{
		ontology.Qualifier4G, ontology.Qualifier5G, ontology.QualifierFiber, ontology.QualifierFWA,
		ontology.QualifierVoLTE, ontology.QualifierVoNR, ontology.QualifierLTEM, ontology.QualifierNBIoT, ontology.QualifierESIM,
	} {
		if !q.Valid() || ontology.Family(q).Valid() {
			t.Errorf("%s: qualifier %t, family %t", q, q.Valid(), ontology.Family(q).Valid())
		}
	}
	// 5G voice is mobile voice with a qualifier.
	c := ontology.ComponentInstance{ID: "c1", Node: mobileVoice, NodeVersion: 2, Charge: ontology.ChargeUsage,
		Qualifiers: []ontology.Qualifier{ontology.Qualifier5G, ontology.QualifierVoNR}}
	if err := c.Validate(release(t), when); err != nil {
		t.Fatal(err)
	}
}

func TestCLSREQ0022To0031OntologyCoversTheLaunchFamilies(t *testing.T) {
	for _, f := range []ontology.Family{
		ontology.FamilyRoamingRetail, ontology.FamilyRoamingWholesale, ontology.FamilyInterconnect, ontology.FamilyWholesaleAccess,
		ontology.FamilyVoIP, ontology.FamilySIPTrunking, ontology.FamilyUCaaS, ontology.FamilyCCaaS, ontology.FamilyCPaaS,
		ontology.FamilyIoTConnectivity, ontology.FamilySubscriptionIdentity, ontology.FamilySatellite,
		ontology.FamilyMobileVoice, ontology.FamilyMessaging, ontology.FamilyMobileData, ontology.FamilyFixedVoice,
		ontology.FamilyEquipmentSale, ontology.FamilyEquipmentRental, ontology.FamilyInstallation,
		ontology.FamilyProfessionalServices, ontology.FamilyContentMedia, ontology.FamilySoftware,
	} {
		if !f.Valid() {
			t.Errorf("family %s missing", f)
		}
	}
}

func TestCLSREQ0041To0044ChargesAreIndependentAndAdjustmentsAreNotSupplies(t *testing.T) {
	o := release(t)
	discount := ontology.ComponentInstance{ID: "d1", Node: mobileVoice, NodeVersion: 2, Charge: ontology.ChargeDiscount}
	if err := discount.Validate(o, when); err == nil {
		t.Fatal("a discount classified as mobile voice")
	}
	discount = ontology.ComponentInstance{ID: "d1", Charge: ontology.ChargeDiscount, Adjusts: "c1"}
	if err := discount.Validate(o, when); err != nil {
		t.Fatal(err)
	}
	if ontology.ChargeRecurringAccess == ontology.ChargeUsage || !ontology.ChargeCredit.Adjusting() {
		t.Fatal("charge types collapsed")
	}
}

func TestCLSREQ0037To0040IdentityAttributesAreNotClassification(t *testing.T) {
	o := release(t)
	a := ontology.ComponentInstance{ID: "c1", Node: mobileVoice, NodeVersion: 2, Charge: ontology.ChargeUsage,
		Identity: ontology.ResourceIdentity{E164: "+15551230000", IMSI: "310150123456789", IP: "192.0.2.1"}}
	b := a
	b.Identity = ontology.ResourceIdentity{E164: "+445550000000"}
	if a.Validate(o, when) != nil || b.Validate(o, when) != nil || a.Node != b.Node {
		t.Fatal("a resource identity changed what the component is")
	}
}

// ---------------------------------------------------------------------------
// catalog mappings
// ---------------------------------------------------------------------------

var sku = ontology.ExternalCode{System: "tenant-catalog", SystemVersion: "2027.1", Code: "PLAN-UNL-5G"}

func mapping(mid string, tenant id.TenantID, target ontology.ID, src ontology.MappingSource) ontology.CatalogMapping {
	return ontology.CatalogMapping{ID: mid, Version: 1, Tenant: tenant, External: sku, Target: target, Charge: ontology.ChargeRecurringAccess,
		Source: src, Status: ontology.MappingActive, EffectiveFrom: t2026}
}

func TestCLSREQ0053And0057MappingsAreTenantScopedAndNeverLeak(t *testing.T) {
	maps := []ontology.CatalogMapping{mapping("m-b", tenantB, mobileData, ontology.SourceTenant)}
	if r := ontology.Resolve(tenantA, sku, when, maps); r.Status != ontology.StatusUnsupported {
		t.Fatalf("tenant A resolved through tenant B's mapping: %s", r.Status)
	}
	if r := ontology.Resolve(tenantB, sku, when, maps); r.Status != ontology.StatusResolved {
		t.Fatalf("tenant B's own mapping: %s", r.Status)
	}
}

func TestCLSREQ0054And0056MappingsAreEffectiveDated(t *testing.T) {
	m := mapping("m-a", tenantA, mobileData, ontology.SourceTenant)
	m.EffectiveFrom = t2027
	if r := ontology.Resolve(tenantA, sku, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), []ontology.CatalogMapping{m}); r.Status != ontology.StatusUnsupported {
		t.Fatalf("a 2027 mapping answered for 2026: %s", r.Status)
	}
}

func TestCLSREQ0059And0062PrecedenceIsDeterministicAndCollisionsConflict(t *testing.T) {
	tmpl := mapping("m-tmpl", id.TenantID{}, broadband, ontology.SourceVendorTemplate)
	tmpl.Promoted = true
	own := mapping("m-own", tenantA, mobileData, ontology.SourceTenant)
	r := ontology.Resolve(tenantA, sku, when, []ontology.CatalogMapping{tmpl, own})
	if r.Status != ontology.StatusResolved || r.Chosen.Target != mobileData {
		t.Fatalf("tenant mapping did not outrank the template: %+v", r)
	}
	clash := mapping("m-own-2", tenantA, broadband, ontology.SourceTenant)
	r = ontology.Resolve(tenantA, sku, when, []ontology.CatalogMapping{own, clash})
	if r.Status != ontology.StatusConflicted || len(r.Candidates) != 2 || r.Chosen != nil {
		t.Fatalf("two tenant mappings to different nodes: %+v", r)
	}
}

func TestCLSREQ0061And0082ProposalsNeverResolveOnTheirOwn(t *testing.T) {
	inc := mapping("m-inc", tenantA, mobileData, ontology.SourceIncumbent)
	ai := mapping("m-ai", tenantA, mobileData, ontology.SourceAIProposal)
	ai.Evidence = "airm:classifier@2027.03"
	r := ontology.Resolve(tenantA, sku, when, []ontology.CatalogMapping{inc, ai})
	if r.Status != ontology.StatusReviewRequired || r.Chosen != nil {
		t.Fatalf("proposals resolved: %+v", r)
	}
}

func TestCLSREQ0080UnmappedIsUnsupportedNotOtherNonTelecom(t *testing.T) {
	r := ontology.Resolve(tenantA, sku, when, nil)
	if r.Status != ontology.StatusUnsupported || r.Chosen != nil {
		t.Fatalf("an unmapped product resolved to %+v", r)
	}
}

func TestCLSREQ0106To0108ImportsStageActivateWholeAndReportImpact(t *testing.T) {
	o := release(t)
	current := []ontology.CatalogMapping{mapping("m-old", tenantA, mobileVoice, ontology.SourceTenant)}
	good := mapping("m-new", tenantA, mobileData, ontology.SourceTenant)
	good.Status = ontology.MappingStaged
	good.EffectiveFrom = t2026
	// Same code, same tier, different target: the import's impact is a
	// conflict, which the impact list reports as a change to no answer.
	bad := mapping("m-bad", tenantA, "ontology:does/not/exist", ontology.SourceTenant)
	bad.Status = ontology.MappingStaged
	if _, _, err := (ontology.Import{Mappings: []ontology.CatalogMapping{good, bad}}).Activate(o, current, tenantA, when); err == nil {
		t.Fatal("an import with an invalid mapping activated")
	}
	if r := ontology.Resolve(tenantA, sku, when, append(current, good)); r.Status == ontology.StatusResolved && r.Chosen.ID == "m-new" {
		t.Fatal("a staged mapping was used to classify")
	}
	activated, changes, err := (ontology.Import{Mappings: []ontology.CatalogMapping{good}}).Activate(o, current, tenantA, when)
	if err != nil || len(activated) != 1 || activated[0].Status != ontology.MappingActive {
		t.Fatalf("activate: %v %+v", err, activated)
	}
	if len(changes) != 1 || changes[0].Before != mobileVoice {
		t.Fatalf("impact %+v", changes)
	}
}

func TestCLSREQ0086MappingsTargetExistingNodesOnly(t *testing.T) {
	m := mapping("m-ai", tenantA, "ontology:invented/by/a/model", ontology.SourceAIProposal)
	m.Evidence = "airm:x"
	if err := m.Validate(release(t)); err == nil {
		t.Fatal("a mapping to a node the ontology does not declare validated")
	}
}

func TestCLSREQ0103And0104TMF620IdsStayExternalCodes(t *testing.T) {
	codes, err := ontology.TMF620Codes(ontology.TMF620{CatalogVersion: "v4.1", ProductSpecification: "PS-123",
		ProductOffering: "PO-9", ProductOfferingPrice: "POP-1"})
	if err != nil || len(codes) != 3 {
		t.Fatalf("codes %v %v", codes, err)
	}
	for _, c := range codes {
		if !strings.HasPrefix(c.System, "tmf620:") || c.SystemVersion != "v4.1" || strings.HasPrefix(c.Code, "ontology:") {
			t.Errorf("code %+v", c)
		}
	}
}

// ---------------------------------------------------------------------------
// bundles
// ---------------------------------------------------------------------------

func eur(t *testing.T, s string) *fiscal.Money {
	m := fiscaltest.Money(t, s, "EUR")
	return &m
}

func TestCLSREQ0045To0049BundleDecomposesPreservingTheLine(t *testing.T) {
	// A 50.00 bundle of voice (standalone 30) and data (standalone 40):
	// 50 * 30/70 = 21.428.. and 28.571..; 2142 + 2857 = 4999 units, one to
	// the larger remainder (voice's .86): 21.43 and 28.57.
	b := ontology.Bundle{LineRef: "inv-1/line-3", Amount: *eur(t, "50.00"), Method: "STANDALONE_SELLING_PRICE", Provenance: "pack:xa/2027.1",
		Parts: []ontology.BundlePart{
			{Component: ontology.ComponentInstance{ID: "voice", Node: mobileVoice}, Standalone: eur(t, "30.00")},
			{Component: ontology.ComponentInstance{ID: "data", Node: mobileData}, Standalone: eur(t, "40.00")},
		}}
	lines, err := ontology.Decompose(b, fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2))
	if err != nil {
		t.Fatal(err)
	}
	if lines[0].Amount.String() != "21.43" || lines[1].Amount.String() != "28.57" {
		t.Fatalf("shares %s, %s", lines[0].Amount, lines[1].Amount)
	}
	for _, l := range lines {
		if l.LineRef != "inv-1/line-3" || l.Method == "" || l.Provenance == "" {
			t.Fatalf("line lost its lineage: %+v", l)
		}
	}
}

func TestCLSREQ0050And0051BundleGraphIsAcyclicAndNeverInventsAllocation(t *testing.T) {
	b := &ontology.Bundle{LineRef: "x", Amount: *eur(t, "10"), Method: "SSP", Provenance: "p"}
	b.Parts = []ontology.BundlePart{{Bundle: b, Standalone: eur(t, "1")}}
	if _, err := ontology.Decompose(*b, fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2)); err == nil {
		t.Fatal("a bundle containing itself decomposed")
	}
	noValue := ontology.Bundle{LineRef: "y", Amount: *eur(t, "10"), Method: "SSP", Provenance: "p",
		Parts: []ontology.BundlePart{{Component: ontology.ComponentInstance{ID: "a"}}}}
	if _, err := ontology.Decompose(noValue, fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2)); err == nil {
		t.Fatal("an allocation was invented for a part with no standalone value")
	}
}

func TestCLSREQ0047InseparableTreatmentNeedsPackAuthority(t *testing.T) {
	b := ontology.Bundle{LineRef: "z", Amount: *eur(t, "10"), Method: "SSP", Provenance: "p", Inseparable: true}
	if _, err := ontology.Decompose(b, fiscaltest.LinePolicy(t, fiscal.RoundHalfUp, 2)); err == nil || !strings.Contains(err.Error(), "no country-pack authority") {
		t.Fatalf("an inseparable bundle with no authority: %v", err)
	}
}
