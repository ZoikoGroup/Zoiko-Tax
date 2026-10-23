package taxation_test

import (
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/taxation"
	"github.com/zoikogroup/zoikotax/backend/internal/goldentest"
)

// The global-telecom pack against cases worked by hand from each authority's
// published standard rate. The pack is RESEARCH: these tests prove that the
// engine applies the content as written and that the content says what its
// citations say; they do not certify the law (playbook §20).

const globalProfile = "../../../../content/packs/global-telecom/taxation.json"

func global(t *testing.T) *taxation.Engine {
	t.Helper()
	p, err := taxation.DecodeProfile(goldentest.ReadFile(t, globalProfile))
	if err != nil {
		t.Fatal(err)
	}
	e, err := taxation.Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

type sale struct {
	seller   taxation.Place
	customer taxation.Party
	evidence []taxation.Evidence
	service  string
	currency fiscal.Currency
	amount   string
}

func (s sale) doc(t *testing.T) taxation.Document {
	if s.customer.ID == "" {
		s.customer.ID = "CUST-1"
	}
	return taxation.Document{
		Type: taxation.DocumentInvoice, Currency: s.currency, PriceMode: taxation.PriceExclusive,
		Seller: taxation.Party{ID: "SELLER", Establishment: &s.seller}, Customer: s.customer, Evidence: s.evidence,
		Lines: []taxation.Line{{Key: "L1", Service: s.service, Amount: money(t, s.amount, s.currency)}},
	}
}

func usedAt(p taxation.Place) []taxation.Evidence {
	return []taxation.Evidence{{Type: "SERVICE_ADDRESS", Source: "SELLER_RECORDED", Place: p}}
}

func business(cc string) taxation.Party {
	return taxation.Party{
		ID: "BIZ-" + cc, Establishment: &taxation.Place{Country: cc},
		VAT: &taxation.VATIdentification{Country: cc, Validation: "VALIDATED"},
	}
}

var oct2026 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func TestGlobalRatesByLocation(t *testing.T) {
	e := global(t)
	cases := []struct {
		name      string
		sale      sale
		component string
		treatment string
		amount    string
		total     string
	}{
		{"German consumer, German seller: 19%",
			sale{seller: taxation.Place{Country: "DE"}, evidence: usedAt(taxation.Place{Country: "DE"}), service: "MOBILE_VOICE", currency: "EUR", amount: "100.00"},
			"de.vat", taxation.TreatmentCustomerCharge, "19.00", "119.00"},
		{"French consumer of a German seller: French VAT where the customer belongs (Art. 58)",
			sale{seller: taxation.Place{Country: "DE"}, evidence: usedAt(taxation.Place{Country: "FR"}), service: "FIXED_BROADBAND", currency: "EUR", amount: "100.00"},
			"fr.vat", taxation.TreatmentCustomerCharge, "20.00", "120.00"},
		{"French business of a German seller: reverse charge (Art. 44, 196)",
			sale{seller: taxation.Place{Country: "DE"}, customer: business("FR"), service: "SIP_TRUNKING", currency: "EUR", amount: "250.00"},
			"fr.vat", taxation.TreatmentReverseCharge, "50.00", "250.00"},
		{"German business of a German seller: charged domestically",
			sale{seller: taxation.Place{Country: "DE"}, customer: business("DE"), service: "SIP_TRUNKING", currency: "EUR", amount: "250.00"},
			"de.vat", taxation.TreatmentCustomerCharge, "47.50", "297.50"},
		{"UK consumer of a US seller: UK VAT",
			sale{seller: taxation.Place{Country: "US"}, evidence: usedAt(taxation.Place{Country: "GB"}), service: "VOIP", currency: "GBP", amount: "10.00"},
			"gb.vat", taxation.TreatmentCustomerCharge, "2.00", "12.00"},
		{"Hungary's 27%",
			sale{seller: taxation.Place{Country: "HU"}, evidence: usedAt(taxation.Place{Country: "HU"}), service: "MOBILE_DATA", currency: "HUF", amount: "10000"},
			"hu.vat", taxation.TreatmentCustomerCharge, "2700.00", "12700.00"},
		{"Switzerland at 8.1% since 2024",
			sale{seller: taxation.Place{Country: "CH"}, evidence: usedAt(taxation.Place{Country: "CH"}), service: "MOBILE_VOICE", currency: "CHF", amount: "100.00"},
			"ch.vat", taxation.TreatmentCustomerCharge, "8.10", "108.10"},
		{"Australia's GST",
			sale{seller: taxation.Place{Country: "AU"}, evidence: usedAt(taxation.Place{Country: "AU"}), service: "FIXED_BROADBAND", currency: "AUD", amount: "89.99"},
			"au.gst", taxation.TreatmentCustomerCharge, "9.00", "98.99"},
		{"Singapore at 9% since 2024",
			sale{seller: taxation.Place{Country: "SG"}, evidence: usedAt(taxation.Place{Country: "SG"}), service: "MOBILE_DATA", currency: "SGD", amount: "50.00"},
			"sg.gst", taxation.TreatmentCustomerCharge, "4.50", "54.50"},
		{"A domestic sale in Japan",
			sale{seller: taxation.Place{Country: "JP"}, evidence: usedAt(taxation.Place{Country: "JP"}), service: "MOBILE_VOICE", currency: "JPY", amount: "3000"},
			"jp.consumption-tax", taxation.TreatmentCustomerCharge, "300.00", "3300.00"},
		{"Ontario: 13% HST",
			sale{seller: taxation.Place{Country: "CA"}, evidence: usedAt(taxation.Place{Subdivision: "CA-ON"}), service: "MOBILE_VOICE", currency: "CAD", amount: "100.00"},
			"ca-on.hst", taxation.TreatmentCustomerCharge, "13.00", "113.00"},
		{"Nova Scotia: 14% HST from April 2025",
			sale{seller: taxation.Place{Country: "CA"}, evidence: usedAt(taxation.Place{Subdivision: "CA-NS"}), service: "MOBILE_VOICE", currency: "CAD", amount: "100.00"},
			"ca-ns.hst", taxation.TreatmentCustomerCharge, "14.00", "114.00"},
		{"Alberta: GST only",
			sale{seller: taxation.Place{Country: "CA"}, evidence: usedAt(taxation.Place{Subdivision: "CA-AB"}), service: "MOBILE_DATA", currency: "CAD", amount: "60.00"},
			"ca-ab.gst", taxation.TreatmentCustomerCharge, "3.00", "63.00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := determine(t, e, c.sale.doc(t), oct2026)
			if r.State != taxation.StateResolved {
				t.Fatalf("state %s (%s)", r.State, r.Reason)
			}
			comp := component(t, r, "L1", c.component)
			if comp.Treatment != c.treatment {
				t.Errorf("treatment %s, want %s", comp.Treatment, c.treatment)
			}
			eq(t, c.component, comp.Amount, c.amount)
			eq(t, "customer total", r.Totals.CustomerTotal, c.total)
			if comp.Source == "" || comp.Citation == "" {
				t.Error("a component in force carries no source or citation")
			}
		})
	}
}

func TestQuebecStacksGSTAndQST(t *testing.T) {
	// 5% GST and 9.975% QST, each on the net: 5.00 + 9.98 (9.975 rounded
	// half up) = 14.98 on 100.00.
	s := sale{seller: taxation.Place{Country: "CA"}, evidence: usedAt(taxation.Place{Subdivision: "CA-QC"}),
		service: "FIXED_BROADBAND", currency: "CAD", amount: "100.00"}
	r := determine(t, global(t), s.doc(t), oct2026)
	eq(t, "GST", component(t, r, "L1", "ca-qc.gst").Amount, "5.00")
	qst := component(t, r, "L1", "ca-qc.qst")
	eq(t, "QST", qst.Amount, "9.98")
	if qst.Authority != "authority:ca-qc-revenu-quebec" {
		t.Errorf("QST is remitted to %s", qst.Authority)
	}
	eq(t, "total", r.Totals.CustomerTotal, "114.98")
}

func TestCanadaNeedsTheProvince(t *testing.T) {
	s := sale{seller: taxation.Place{Country: "CA"}, evidence: usedAt(taxation.Place{Country: "CA"}),
		service: "MOBILE_VOICE", currency: "CAD", amount: "100.00"}
	unresolved(t, determine(t, global(t), s.doc(t), oct2026), taxation.StateReviewRequired, errs.ReasonJurisdictionLevelRequired)
}

func TestTerritoriesOutsideTheVATAreaAreNotTaxedAsTheMainland(t *testing.T) {
	for _, code := range []string{"ES-CN", "FI-01", "PT-20", "FR-974"} {
		t.Run(code, func(t *testing.T) {
			s := sale{seller: taxation.Place{Country: code[:2]}, evidence: usedAt(taxation.Place{Subdivision: code}),
				service: "MOBILE_VOICE", currency: "EUR", amount: "100.00"}
			unresolved(t, determine(t, global(t), s.doc(t), oct2026), taxation.StateUnsupported, errs.ReasonJurisdictionUnsupported)
		})
	}
	// An ordinary subdivision of a country taxed uniformly is the country.
	s := sale{seller: taxation.Place{Country: "DE"}, evidence: usedAt(taxation.Place{Subdivision: "DE-BY"}),
		service: "MOBILE_VOICE", currency: "EUR", amount: "100.00"}
	if r := determine(t, global(t), s.doc(t), oct2026); r.State != taxation.StateResolved {
		t.Fatalf("Bavaria: %s (%s)", r.State, r.Reason)
	}
}

func TestUncoveredCountriesAreUnsupported(t *testing.T) {
	for _, cc := range []string{"MX", "IN", "BR"} {
		s := sale{seller: taxation.Place{Country: cc}, evidence: usedAt(taxation.Place{Country: cc}),
			service: "MOBILE_VOICE", currency: "USD", amount: "100.00"}
		unresolved(t, determine(t, global(t), s.doc(t), oct2026), taxation.StateUnsupported, errs.ReasonJurisdictionUnsupported)
	}
}

func TestCrossBorderWhereOnlyDomesticIsCoveredIsUnsupported(t *testing.T) {
	// The pack does not model cross-border telecom supplies into Japan; it
	// says so rather than charging or zero-rating them.
	s := sale{seller: taxation.Place{Country: "US"}, evidence: usedAt(taxation.Place{Country: "JP"}),
		service: "MOBILE_VOICE", currency: "JPY", amount: "3000"}
	unresolved(t, determine(t, global(t), s.doc(t), oct2026), taxation.StateUnsupported, errs.ReasonNoApplicableContent)
}

func TestCrossBorderEquipmentIsUnsupported(t *testing.T) {
	s := sale{seller: taxation.Place{Country: "DE"}, evidence: usedAt(taxation.Place{Country: "FR"}),
		service: "EQUIPMENT_SALE", currency: "EUR", amount: "300.00"}
	unresolved(t, determine(t, global(t), s.doc(t), oct2026), taxation.StateUnsupported, errs.ReasonNoApplicableContent)
}

func TestFinlandsRateChangeAtHelsinkiMidnight(t *testing.T) {
	// 25.5% from 1 September 2024, which began at 21:00 UTC on 31 August.
	s := sale{seller: taxation.Place{Country: "FI"}, evidence: usedAt(taxation.Place{Country: "FI"}),
		service: "MOBILE_DATA", currency: "EUR", amount: "100.00"}
	e := global(t)
	before := determine(t, e, s.doc(t), time.Date(2024, 8, 31, 20, 59, 59, 0, time.UTC))
	after := determine(t, e, s.doc(t), time.Date(2024, 8, 31, 21, 0, 0, 0, time.UTC))
	eq(t, "before", component(t, before, "L1", "fi.vat").Amount, "24.00")
	eq(t, "after", component(t, after, "L1", "fi.vat").Amount, "25.50")
}

func TestBeforeTheContentBeginsIsNotAZero(t *testing.T) {
	s := sale{seller: taxation.Place{Country: "DE"}, evidence: usedAt(taxation.Place{Country: "DE"}),
		service: "MOBILE_VOICE", currency: "EUR", amount: "100.00"}
	r := determine(t, global(t), s.doc(t), time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC))
	unresolved(t, r, taxation.StateUnsupported, errs.ReasonNoApplicableContent)
}

func TestCoverageListsEveryRegimeWithItsSources(t *testing.T) {
	cov := global(t).Coverage(oct2026)
	if len(cov) != 39 {
		t.Fatalf("%d regimes", len(cov))
	}
	for _, r := range cov {
		if len(r.Components) == 0 {
			t.Errorf("%s has no component in force", r.ID)
		}
		for _, c := range r.Components {
			if c.Source == "" || c.Citation == "" {
				t.Errorf("%s carries no source", c.ID)
			}
		}
	}
}
