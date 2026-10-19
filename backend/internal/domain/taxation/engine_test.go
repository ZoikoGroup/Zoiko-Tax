package taxation_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/taxation"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
	"github.com/zoikogroup/zoikotax/backend/internal/goldentest"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The oracle for every expected figure here is the playbook text
// (ZTAX-ENG-RESET-001 §7, §8, §9, §29), worked by hand — never the output of
// the function under test (playbook §20, "keep the oracle independent").
// The content is the shipped training pack, decoded by the production
// decoder, so these tests prove the content and the engine together.

const trainingProfile = "../../../../content/packs/training-telecom/taxation.json"

func engine(t *testing.T) *taxation.Engine {
	t.Helper()
	p, err := taxation.DecodeProfile(goldentest.ReadFile(t, trainingProfile))
	if err != nil {
		t.Fatal(err)
	}
	e, err := taxation.Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

var taxPoint = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func money(t *testing.T, amount string, cur fiscal.Currency) fiscal.Money {
	return fiscaltest.Money(t, amount, cur)
}

func qty(t *testing.T, v, unit string) *fiscal.Quantity {
	t.Helper()
	q, err := fiscal.ParseQuantity(v, fiscal.Unit(unit))
	if err != nil {
		t.Fatal(err)
	}
	return &q
}

// bundle is playbook §7's input: TELCO-A sells a tax-exclusive bundle for
// USD 100.00, allocated 40 voice / 50 data / 10 device, one active line.
func bundle(t *testing.T) taxation.Document {
	return taxation.Document{
		Type: taxation.DocumentInvoice, Currency: "USD", PriceMode: taxation.PriceExclusive,
		Seller:   taxation.Party{ID: "TELCO-A", Establishment: &taxation.Place{Country: "XT"}},
		Customer: taxation.Party{ID: "C1"},
		Evidence: []taxation.Evidence{{Type: "SERVICE_ADDRESS", Source: "SELLER_RECORDED", Place: taxation.Place{Country: "XT"}}},
		Lines: []taxation.Line{{
			Key: "L1", SKU: "BUNDLE-1", Amount: money(t, "100.00", "USD"),
			Parts: []taxation.Part{
				{Key: "voice", Service: "MOBILE_VOICE", Amount: money(t, "40.00", "USD"), Quantity: qty(t, "1", "LINE")},
				{Key: "data", Service: "MOBILE_DATA", Amount: money(t, "50.00", "USD")},
				{Key: "device", Service: "EQUIPMENT_SALE", Amount: money(t, "10.00", "USD")},
			},
		}},
	}
}

func determine(t *testing.T, e *taxation.Engine, d taxation.Document, at time.Time) taxation.Result {
	t.Helper()
	r, err := e.Determine(d, at)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func component(t *testing.T, r taxation.Result, item, id string) taxation.ComponentResult {
	t.Helper()
	for _, it := range r.Items {
		if it.Key != item {
			continue
		}
		for _, c := range it.Components {
			if c.ID == id {
				return c
			}
		}
	}
	t.Fatalf("no component %s on %s", id, item)
	return taxation.ComponentResult{}
}

func eq(t *testing.T, what string, got fiscal.Money, want string) {
	t.Helper()
	if got.CanonicalString() != want {
		t.Errorf("%s = %s, want %s", what, got.CanonicalString(), want)
	}
}

func TestMixedBundleKeepsTheTotalsApart(t *testing.T) {
	r := determine(t, engine(t), bundle(t), taxPoint)
	if r.State != taxation.StateResolved || r.Reason != string(errs.ReasonNotAuthoritative) {
		t.Fatalf("state %s (%s)", r.State, r.Reason)
	}
	eq(t, "voice tax", component(t, r, "L1/voice", "xt.voice-tax").Charged, "3.20")
	eq(t, "emergency charge", component(t, r, "L1/voice", "xt.emergency-charge").Charged, "0.75")
	eq(t, "device tax", component(t, r, "L1/device", "xt.device-tax").Charged, "0.50")

	data := component(t, r, "L1/data", "xt.data-tax")
	eq(t, "data tax", data.Amount, "0.00")
	if data.LegalReason != taxation.ReasonZeroRated {
		t.Errorf("data tax is zero for %q; a legal zero carries its reason", data.LegalReason)
	}

	contribution := component(t, r, "L1/voice", "xt.sector-contribution")
	eq(t, "sector contribution", contribution.Amount, "0.80")
	eq(t, "sector contribution charged", contribution.Charged, "0.00")
	if contribution.Treatment != taxation.TreatmentProviderCost || contribution.Responsibility.EconomicBearer != taxation.PartySeller {
		t.Errorf("contribution is %s borne by %s; the provider bears it", contribution.Treatment, contribution.Responsibility.EconomicBearer)
	}
	if b := contribution.Base; b == nil || b.CanonicalString() != "40.00" {
		t.Errorf("contribution base %v; the voice regulatory base is 40.00 even though invoice taxes total 4.45", b)
	}

	emergency := component(t, r, "L1/voice", "xt.emergency-charge")
	if emergency.Unit != "LINE" || emergency.Responsibility.Incidence != taxation.PartyBuyer || emergency.Responsibility.Collection != taxation.PartySeller {
		t.Errorf("emergency charge %+v: per LINE, imposed on the subscriber, collected by the seller", emergency)
	}

	tot := r.Totals
	eq(t, "net", tot.Net, "100.00")
	eq(t, "customer-recoverable", tot.CustomerTax, "4.45")
	eq(t, "customer total", tot.CustomerTotal, "104.45")
	eq(t, "provider accrual", tot.ProviderAccrual, "0.80")
	eq(t, "reverse charge", tot.ReverseCharge, "0.00")

	eq(t, "emitted CUSTOMER_TAX", r.Emitted[taxation.SlotCustomerTax], "4.45")
	eq(t, "emitted PROVIDER_ACCRUAL", r.Emitted[taxation.SlotProviderAccrual], "0.80")
	eq(t, "emitted XT_SECTOR_CONTRIBUTION", r.Emitted["XT_SECTOR_CONTRIBUTION"], "0.80")
	eq(t, "emitted XT_DATA_TAX", r.Emitted["XT_DATA_TAX"], "0.00")
}

func TestResaleCertificateExemptsOnlyTheVoiceTax(t *testing.T) {
	d := bundle(t)
	d.Certificates = []taxation.Certificate{{
		ID: "CERT-1", Type: "RESALE", Holder: "C1", Jurisdiction: "jurisdiction:xt",
		ValidFrom: "2026-01-01", ValidTo: "2027-01-01", Verification: taxation.VerificationOK,
	}}
	r := determine(t, engine(t), d, taxPoint)

	voice := component(t, r, "L1/voice", "xt.voice-tax")
	if voice.Treatment != taxation.TreatmentExempt || voice.Exemption == nil || voice.Exemption.Certificate != "CERT-1" {
		t.Fatalf("voice tax %+v; the certificate exempts it", voice)
	}
	// The suppressed assessment and its reason are retained (§29).
	if voice.Exemption.Assessed == nil || voice.Exemption.Assessed.CanonicalString() != "3.20" {
		t.Errorf("suppressed assessment %v, want 3.20", voice.Exemption.Assessed)
	}
	eq(t, "customer-recoverable", r.Totals.CustomerTax, "1.25")
	eq(t, "customer total", r.Totals.CustomerTotal, "101.25")
	eq(t, "provider accrual", r.Totals.ProviderAccrual, "0.80")
}

func TestCertificatesThatMustNotSuppressTheCharge(t *testing.T) {
	cases := map[string]taxation.Certificate{
		"expired":           {ValidFrom: "2025-01-01", ValidTo: "2026-01-01", Holder: "C1", Jurisdiction: "jurisdiction:xt", Verification: taxation.VerificationOK},
		"wrong holder":      {ValidFrom: "2026-01-01", Holder: "SOMEONE-ELSE", Jurisdiction: "jurisdiction:xt", Verification: taxation.VerificationOK},
		"another authority": {ValidFrom: "2026-01-01", Holder: "C1", Jurisdiction: "jurisdiction:xq", Verification: taxation.VerificationOK},
		"unverified":        {ValidFrom: "2026-01-01", Holder: "C1", Jurisdiction: "jurisdiction:xt", Verification: taxation.VerificationNone},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			c.ID, c.Type = "CERT-X", "RESALE"
			d := bundle(t)
			d.Certificates = []taxation.Certificate{c}
			r := determine(t, engine(t), d, taxPoint)
			eq(t, "voice tax", component(t, r, "L1/voice", "xt.voice-tax").Charged, "3.20")
			eq(t, "customer total", r.Totals.CustomerTotal, "104.45")
			found := false
			for _, s := range r.Trace {
				if s.Step == taxation.StepExemption && strings.Contains(s.Detail, "CERT-X") {
					found = true
				}
			}
			if !found {
				t.Error("the refused certificate is not in the trace; a refusal must be visible")
			}
		})
	}
}

func TestUnavailableVerificationIsHeldNotAssumed(t *testing.T) {
	d := bundle(t)
	d.Certificates = []taxation.Certificate{{
		ID: "CERT-1", Type: "RESALE", Holder: "C1", Jurisdiction: "jurisdiction:xt",
		ValidFrom: "2026-01-01", Verification: taxation.VerificationDown,
	}}
	r := determine(t, engine(t), d, taxPoint)
	unresolved(t, r, taxation.StateReviewRequired, errs.ReasonExemptionUnverified)
}

// unresolved asserts an explicit state that cannot be mistaken for a zero
// tax: no totals, nothing emitted, so no fiscal effect follows a commit.
func unresolved(t *testing.T, r taxation.Result, state string, reason errs.ReasonCode) {
	t.Helper()
	if r.State != state || r.Reason != string(reason) {
		t.Fatalf("state %s (%s), want %s (%s)", r.State, r.Reason, state, reason)
	}
	if r.Totals != nil || len(r.Emitted) != 0 {
		t.Errorf("an unresolved document carries totals %v and emits %v", r.Totals, r.Emitted)
	}
}

func TestMissingLocationIsNeverAZero(t *testing.T) {
	d := bundle(t)
	d.Evidence = nil
	r := determine(t, engine(t), d, taxPoint)
	unresolved(t, r, taxation.StateReviewRequired, errs.ReasonSitusEvidenceInsufficient)
	if needed := r.Items[0].NeededEvidence; len(needed) == 0 || needed[0] != "PRIMARY_PLACE_OF_USE" {
		t.Errorf("needed evidence %v; the decision names what would have resolved it", needed)
	}
}

func TestWeakEvidenceAloneDoesNotResolve(t *testing.T) {
	// An IP country is WEAK under the consumer rule and below its minimum:
	// country, IP and SIM are not interchangeable location keys (§14).
	d := bundle(t)
	d.Evidence = []taxation.Evidence{{Type: "IP_GEOLOCATION", Source: "NETWORK_DERIVED", Place: taxation.Place{Country: "XT"}}}
	unresolved(t, determine(t, engine(t), d, taxPoint), taxation.StateReviewRequired, errs.ReasonSitusEvidenceInsufficient)
}

func TestUnknownSKUIsHeldForClassification(t *testing.T) {
	d := bundle(t)
	d.Lines[0].Parts[0].Service = ""
	unresolved(t, determine(t, engine(t), d, taxPoint), taxation.StateReviewRequired, errs.ReasonClassificationRequired)
}

func TestPerUnitChargeNeedsItsUnit(t *testing.T) {
	d := bundle(t)
	d.Lines[0].Parts[0].Quantity = qty(t, "1", "MINUTE")
	unresolved(t, determine(t, engine(t), d, taxPoint), taxation.StateReviewRequired, errs.ReasonQuantityRequired)
}

func TestUncoveredCountryIsUnsupported(t *testing.T) {
	d := bundle(t)
	d.Evidence[0].Place = taxation.Place{Country: "ZZ"}
	unresolved(t, determine(t, engine(t), d, taxPoint), taxation.StateUnsupported, errs.ReasonJurisdictionUnsupported)
}

func TestStrongEvidenceAbroadIsNotPassedOverForWeakerEvidenceAtHome(t *testing.T) {
	// The service is used in ZZ, which no content declares; the billing
	// address is in XT. Resolving on the billing address would tax the
	// supply where it did not happen.
	d := bundle(t)
	d.Evidence = []taxation.Evidence{
		{Type: "SERVICE_ADDRESS", Source: "SELLER_RECORDED", Place: taxation.Place{Country: "ZZ"}},
		{Type: "BILLING_ADDRESS", Source: "CUSTOMER_ASSERTED", Place: taxation.Place{Country: "XT"}},
	}
	unresolved(t, determine(t, engine(t), d, taxPoint), taxation.StateUnsupported, errs.ReasonJurisdictionUnsupported)

	// The other way round, a weaker item abroad does not unseat a stronger
	// one at home.
	d.Evidence[0].Place, d.Evidence[1].Place = taxation.Place{Country: "XT"}, taxation.Place{Country: "ZZ"}
	if r := determine(t, engine(t), d, taxPoint); r.State != taxation.StateResolved {
		t.Fatalf("state %s (%s)", r.State, r.Reason)
	}
}

func TestUncoveredServiceIsUnsupported(t *testing.T) {
	d := bundle(t)
	d.Lines[0].Parts[2].Service = "FIXED_BROADBAND"
	unresolved(t, determine(t, engine(t), d, taxPoint), taxation.StateUnsupported, errs.ReasonServiceNotCovered)
}

// broadband is a 100.00 fixed-broadband line in a state of XQ.
func broadband(t *testing.T, place taxation.Place, amount string, mode string) taxation.Document {
	return taxation.Document{
		Type: taxation.DocumentInvoice, Currency: "USD", PriceMode: mode,
		Seller:   taxation.Party{ID: "TELCO-Q", Establishment: &taxation.Place{Country: "XQ"}},
		Customer: taxation.Party{ID: "C2"},
		Evidence: []taxation.Evidence{{Type: "FIXED_LINE_LOCATION", Source: "SELLER_RECORDED", Place: place}},
		Lines:    []taxation.Line{{Key: "B1", Service: "FIXED_BROADBAND", Amount: money(t, amount, "USD")}},
	}
}

func TestTaxOnTax(t *testing.T) {
	// §9: A = 10% × 100 = 10.00; B = 5% × (100 + A) = 5.50; total 115.50.
	r := determine(t, engine(t), broadband(t, taxation.Place{Subdivision: "XQ-ST"}, "100.00", taxation.PriceExclusive), taxPoint)
	if r.State != taxation.StateResolved {
		t.Fatalf("state %s (%s)", r.State, r.Reason)
	}
	eq(t, "A", component(t, r, "B1", "xq.national-tax").Amount, "10.00")
	b := component(t, r, "B1", "xq-st.state-levy")
	eq(t, "B", b.Amount, "5.50")
	if b.Base == nil || b.Base.CanonicalString() != "110.00" {
		t.Errorf("B's base %v, want 110.00: net plus A", b.Base)
	}
	eq(t, "total", r.Totals.CustomerTotal, "115.50")
}

func TestCountryOnlyWhereTheStateLeviesIsHeld(t *testing.T) {
	r := determine(t, engine(t), broadband(t, taxation.Place{Country: "XQ"}, "100.00", taxation.PriceExclusive), taxPoint)
	unresolved(t, r, taxation.StateReviewRequired, errs.ReasonJurisdictionLevelRequired)
}

// vatDoc is a voice line sold by a seller established in XV.
func vatDoc(t *testing.T, customer taxation.Party, evidence []taxation.Evidence, cur fiscal.Currency, amount, mode string) taxation.Document {
	return taxation.Document{
		Type: taxation.DocumentInvoice, Currency: cur, PriceMode: mode,
		Seller:   taxation.Party{ID: "TELCO-V", Establishment: &taxation.Place{Country: "XV"}},
		Customer: customer, Evidence: evidence,
		Lines: []taxation.Line{{Key: "V1", Service: "MOBILE_VOICE", Amount: money(t, amount, cur)}},
	}
}

func TestInclusivePriceIsExtractedNotAdded(t *testing.T) {
	// §9: gross 120.00 at a single 20% inclusive tax is base 100.00 and tax
	// 20.00 — never 20% × 120 added on.
	d := vatDoc(t, taxation.Party{ID: "C3"},
		[]taxation.Evidence{{Type: "SERVICE_ADDRESS", Source: "SELLER_RECORDED", Place: taxation.Place{Country: "XV"}}},
		"EUR", "120.00", taxation.PriceInclusive)
	r := determine(t, engine(t), d, taxPoint)
	if r.State != taxation.StateResolved {
		t.Fatalf("state %s (%s)", r.State, r.Reason)
	}
	vat := component(t, r, "V1", "xv.vat")
	eq(t, "VAT", vat.Amount, "20.00")
	if !vat.Inclusive {
		t.Error("the VAT is not marked inclusive")
	}
	eq(t, "net", r.Totals.Net, "100.00")
	eq(t, "customer total", r.Totals.CustomerTotal, "120.00")
}

func TestInclusivePricingWithoutDeclaredExtractionIsUnsupported(t *testing.T) {
	// XW declares no extraction policy.
	d := vatDoc(t, taxation.Party{ID: "C3"},
		[]taxation.Evidence{{Type: "SERVICE_ADDRESS", Source: "SELLER_RECORDED", Place: taxation.Place{Country: "XW"}}},
		"EUR", "125.00", taxation.PriceInclusive)
	r := determine(t, engine(t), d, taxPoint)
	if r.State != taxation.StateUnsupported || r.Reason != string(errs.ReasonInclusivePricingUnsupported) || r.Totals != nil {
		t.Fatalf("state %s (%s), totals %v", r.State, r.Reason, r.Totals)
	}
}

// §8A: the same telecom service, different business/consumer status and
// establishment evidence, different place of supply and different party
// accounting for the tax.
func TestPlaceOfSupplyIsADecisionNotADropdown(t *testing.T) {
	e := engine(t)
	business := taxation.Party{
		ID: "B1", Establishment: &taxation.Place{Country: "XW"},
		VAT: &taxation.VATIdentification{Country: "XW", Validation: "VALIDATED"},
	}
	billing := []taxation.Evidence{{Type: "BILLING_ADDRESS", Source: "CUSTOMER_ASSERTED", Place: taxation.Place{Country: "XV"}}}

	b2b := determine(t, e, vatDoc(t, business, billing, "EUR", "100.00", taxation.PriceExclusive), taxPoint)
	it := b2b.Items[0]
	if it.CustomerRole != "B2B" || it.PlaceOfSupply != "jurisdiction:xw" || it.Supply != taxation.SupplyCrossBorder {
		t.Fatalf("B2B: role %s, place %s, supply %s", it.CustomerRole, it.PlaceOfSupply, it.Supply)
	}
	vat := component(t, b2b, "V1", "xw.vat")
	if vat.Treatment != taxation.TreatmentReverseCharge || vat.Responsibility.Remittance != taxation.PartyBuyer {
		t.Fatalf("B2B cross-border VAT is %s remitted by %s; the customer accounts for it", vat.Treatment, vat.Responsibility.Remittance)
	}
	eq(t, "self-assessed", vat.Amount, "25.00")
	eq(t, "charged", vat.Charged, "0.00")
	eq(t, "customer total", b2b.Totals.CustomerTotal, "100.00")
	eq(t, "reverse charge disclosed", b2b.Totals.ReverseCharge, "25.00")
	if _, ok := b2b.Emitted["XW_VAT"]; ok {
		t.Error("a reverse-charged amount is emitted as the seller's XW VAT liability")
	}

	// A consumer with no VAT identification, whose service is used in XW.
	consumer := taxation.Party{ID: "P1"}
	used := []taxation.Evidence{{Type: "SERVICE_ADDRESS", Source: "SELLER_RECORDED", Place: taxation.Place{Country: "XW"}}}
	b2c := determine(t, e, vatDoc(t, consumer, used, "EUR", "100.00", taxation.PriceExclusive), taxPoint)
	vat = component(t, b2c, "V1", "xw.vat")
	if b2c.Items[0].CustomerRole != "B2C" || vat.Treatment != taxation.TreatmentCustomerCharge {
		t.Fatalf("B2C: role %s, treatment %s", b2c.Items[0].CustomerRole, vat.Treatment)
	}
	eq(t, "B2C charged", vat.Charged, "25.00")

	// An unvalidated VAT number does not make a business: the pack treats
	// UNDETERMINED as B2C, and says so in content.
	unvalidated := business
	unvalidated.VAT = &taxation.VATIdentification{Country: "XW", Validation: "UNVALIDATED"}
	r := determine(t, e, vatDoc(t, unvalidated, used, "EUR", "100.00", taxation.PriceExclusive), taxPoint)
	if r.Items[0].CustomerRole != "B2C" || component(t, r, "V1", "xw.vat").Treatment != taxation.TreatmentCustomerCharge {
		t.Errorf("unvalidated VAT: role %s", r.Items[0].CustomerRole)
	}

	// Without establishment or billing evidence, the business rule falls to
	// its next declared item, the validated VAT identification's country.
	thin := business
	thin.Establishment = nil
	thin.VAT = &taxation.VATIdentification{Country: "XW", Validation: "VALIDATED"}
	r = determine(t, e, vatDoc(t, thin, nil, "EUR", "100.00", taxation.PriceExclusive), taxPoint)
	if r.Items[0].PlaceOfSupply != "jurisdiction:xw" {
		t.Errorf("a validated VAT identification alone sources a business to %s", r.Items[0].PlaceOfSupply)
	}
}

func TestCurrencyDoesNotChangeJurisdiction(t *testing.T) {
	e := engine(t)
	used := []taxation.Evidence{{Type: "SERVICE_ADDRESS", Source: "SELLER_RECORDED", Place: taxation.Place{Country: "XW"}}}
	for _, cur := range []fiscal.Currency{"EUR", "USD", "JPY"} {
		r := determine(t, e, vatDoc(t, taxation.Party{ID: "P1"}, used, cur, "100", taxation.PriceExclusive), taxPoint)
		if r.Items[0].PlaceOfSupply != "jurisdiction:xw" {
			t.Errorf("in %s the place of supply is %s", cur, r.Items[0].PlaceOfSupply)
		}
	}
}

func TestRateChangesAtLocalMidnightNotUTC(t *testing.T) {
	// XV is Pacific/Auckland: 1 January 2027 begins at 11:00 UTC on
	// 31 December 2026.
	e := engine(t)
	used := []taxation.Evidence{{Type: "SERVICE_ADDRESS", Source: "SELLER_RECORDED", Place: taxation.Place{Country: "XV"}}}
	d := vatDoc(t, taxation.Party{ID: "P1"}, used, "EUR", "100.00", taxation.PriceExclusive)

	before := determine(t, e, d, time.Date(2026, 12, 31, 10, 59, 59, 0, time.UTC))
	after := determine(t, e, d, time.Date(2026, 12, 31, 11, 0, 0, 0, time.UTC))
	eq(t, "before cutover", component(t, before, "V1", "xv.vat").Amount, "20.00")
	eq(t, "after cutover", component(t, after, "V1", "xv.vat").Amount, "22.00")
	if v := component(t, after, "V1", "xv.vat").Version; v != "2027-01-01" {
		t.Errorf("version %s; the decision names the rate version it applied", v)
	}
}

func TestCreditNoteReversesWithTheSameRules(t *testing.T) {
	d := bundle(t)
	d.Type = taxation.DocumentCreditNote
	d.Lines[0].Amount = money(t, "-100.00", "USD")
	d.Lines[0].Parts[0].Amount = money(t, "-40.00", "USD")
	d.Lines[0].Parts[0].Quantity = qty(t, "-1", "LINE")
	d.Lines[0].Parts[1].Amount = money(t, "-50.00", "USD")
	d.Lines[0].Parts[2].Amount = money(t, "-10.00", "USD")
	r := determine(t, engine(t), d, taxPoint)
	eq(t, "customer total", r.Totals.CustomerTotal, "-104.45")
	eq(t, "provider accrual", r.Totals.ProviderAccrual, "-0.80")
}

func TestAllocationMustAccountForTheLineExactly(t *testing.T) {
	d := bundle(t)
	d.Lines[0].Parts[2].Amount = money(t, "9.99", "USD")
	if _, err := engine(t).Determine(d, taxPoint); errs.CategoryOf(err) != errs.CategoryValidation {
		t.Fatalf("an allocation of 99.99 against 100.00 gave %v; it is refused, never absorbed", err)
	}
}

func TestDeterministicAndRoundTrips(t *testing.T) {
	e := engine(t)
	d := bundle(t)
	d.Certificates = []taxation.Certificate{{
		ID: "CERT-1", Type: "RESALE", Holder: "C1", Jurisdiction: "jurisdiction:xt",
		ValidFrom: "2026-01-01", Verification: taxation.VerificationOK,
	}}
	a, err := canonical.Encode(determine(t, e, d, taxPoint).Canonical())
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonical.Encode(determine(t, e, d, taxPoint).Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two determinations of one document differ")
	}
	if _, err := taxation.DecodeResult(a); err != nil {
		t.Fatalf("the result does not round-trip: %v", err)
	}
	docBytes, err := canonical.Encode(d.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	back, err := taxation.DecodeDocument(docBytes)
	if err != nil {
		t.Fatalf("the document does not round-trip: %v", err)
	}
	c, err := canonical.Encode(determine(t, e, back, taxPoint).Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, c) {
		t.Fatal("the decoded document determines differently from the original")
	}
}

func TestProfileRoundTripsThroughItsCanonicalForm(t *testing.T) {
	p, err := taxation.DecodeProfile(goldentest.ReadFile(t, trainingProfile))
	if err != nil {
		t.Fatal(err)
	}
	first, err := canonical.Encode(p.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	again, err := taxation.DecodeProfile(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonical.Encode(again.Canonical())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("the profile's canonical form does not reproduce itself")
	}
}

func TestContentDefectsRefuseTheProfile(t *testing.T) {
	cases := map[string]func(p *taxation.Profile){
		"zero rate without a legal reason": func(p *taxation.Profile) {
			p.Components[1].Versions[0].LegalReason = ""
		},
		"overlapping treatments": func(p *taxation.Profile) {
			c := &p.Components[7]
			c.Applies = append(c.Applies, taxation.ApplyDecl{Supply: "ANY", Treatment: "CUSTOMER_CHARGE", Responsibility: "seller-collects"})
		},
		"overlapping versions": func(p *taxation.Profile) {
			p.Components[7].Versions[0].To = "2027-06-01"
		},
		"unanswered responsibility role": func(p *taxation.Profile) {
			p.Responsibility[0].Remittance = ""
		},
		"component in an undeclared jurisdiction": func(p *taxation.Profile) {
			p.Components[0].Jurisdiction = "jurisdiction:nowhere"
		},
		"compounding cycle": func(p *taxation.Profile) {
			p.Components[5].Base = taxation.BaseDecl{Kind: "NET_PLUS_COMPONENTS", Components: []string{"xq-st.state-levy"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := taxation.DecodeProfile(goldentest.ReadFile(t, trainingProfile))
			if err != nil {
				t.Fatal(err)
			}
			mutate(&p)
			if _, err := taxation.Compile(p); err == nil {
				t.Fatal("the profile compiled")
			}
		})
	}
}
