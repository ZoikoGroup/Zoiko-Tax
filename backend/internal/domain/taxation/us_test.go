package taxation_test

import (
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/taxation"
)

// The United States by state, against cases worked by hand from each
// state's own publication. State-level taxes only; the pack is RESEARCH, so
// these prove the content is applied as written, not that it is the law.

var usStates = []string{
	"AL", "AK", "AZ", "AR", "CA", "CO", "CT", "DE", "DC", "FL", "GA", "HI", "ID", "IL", "IN", "IA", "KS",
	"KY", "LA", "ME", "MD", "MA", "MI", "MN", "MS", "MO", "MT", "NE", "NV", "NH", "NJ", "NM", "NY", "NC",
	"ND", "OH", "OK", "OR", "PA", "RI", "SC", "SD", "TN", "TX", "UT", "VT", "VA", "WA", "WV", "WI", "WY",
}

func usSale(state, service string) sale {
	return sale{
		seller:   taxation.Place{Country: "US"},
		evidence: usedAt(taxation.Place{Subdivision: "US-" + state}),
		service:  service, currency: "USD", amount: "100.00",
	}
}

func TestEveryStateIsCovered(t *testing.T) {
	e := global(t)
	for _, st := range usStates {
		services := []string{"MOBILE_VOICE", "MOBILE_DATA", "FIXED_BROADBAND", "EQUIPMENT_SALE"}
		if st == "HI" {
			// Hawaii's voice taxes could not be verified; see below.
			services = services[1:]
		}
		for _, service := range services {
			r := determine(t, e, usSale(st, service).doc(t), oct2026)
			if r.State != taxation.StateResolved {
				t.Errorf("%s %s: %s (%s)", st, service, r.State, r.Reason)
			}
		}
	}
}

func TestHawaiiVoiceIsUnsupported(t *testing.T) {
	// Intrastate calls bear the public service company tax, built into the
	// carrier's rates, and only an apportioned interstate share bears GET.
	// Neither could be pinned down, so the pack declares no voice component.
	r := determine(t, global(t), usSale("HI", "MOBILE_VOICE").doc(t), oct2026)
	if r.State == taxation.StateResolved {
		t.Fatalf("Hawaii mobile voice resolved: %+v", r.Totals)
	}
}

func TestIllinoisExciseRoseInJuly2025(t *testing.T) {
	before := determine(t, global(t), usSale("IL", "MOBILE_VOICE").doc(t), time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC))
	eq(t, "June 2025", component(t, before, "L1", "us-il.tet").Amount, "7.00")
	after := determine(t, global(t), usSale("IL", "MOBILE_VOICE").doc(t), oct2026)
	eq(t, "October 2026", component(t, after, "L1", "us-il.tet").Amount, "8.65")
}

func TestUSRatesByState(t *testing.T) {
	e := global(t)
	cases := []struct {
		name      string
		sale      sale
		component string
		amount    string
		total     string
	}{
		{"Texas mobile voice: 6.25% sales tax", usSale("TX", "MOBILE_VOICE"), "us-tx.sales-telecom", "6.25", "106.25"},
		{"Virginia VoIP: 5% communications sales tax", usSale("VA", "VOIP"), "us-va.csut", "5.00", "105.00"},
		{"New Hampshire fixed voice: 7% CST", usSale("NH", "FIXED_VOICE"), "us-nh.cst", "7.00", "107.00"},
		{"Washington equipment: 6.5% sales tax", usSale("WA", "EQUIPMENT_SALE"), "us-wa.sales-goods", "6.50", "106.50"},
		{"New Mexico mobile voice: 4.875% GRT", usSale("NM", "MOBILE_VOICE"), "us-nm.grt-telecom", "4.88", "104.88"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := determine(t, e, c.sale.doc(t), oct2026)
			if r.State != taxation.StateResolved {
				t.Fatalf("state %s (%s)", r.State, r.Reason)
			}
			comp := component(t, r, "L1", c.component)
			eq(t, c.component, comp.Amount, c.amount)
			eq(t, "customer total", r.Totals.CustomerTotal, c.total)
			if comp.Source != "SRC-ZOIKO-US-STATE-TELECOM-2026" || comp.Citation == "" {
				t.Errorf("%s cites %q", c.component, comp.Source)
			}
		})
	}
}

func TestFloridaStacksCommunicationsAndGrossReceiptsTaxes(t *testing.T) {
	// Mobile: 4.92% CST + 2.37% GRT + 0.15% GRT = 7.44%.
	r := determine(t, global(t), usSale("FL", "MOBILE_VOICE").doc(t), oct2026)
	eq(t, "CST", component(t, r, "L1", "us-fl.cst").Amount, "4.92")
	eq(t, "GRT", component(t, r, "L1", "us-fl.grt").Amount, "2.37")
	eq(t, "additional GRT", component(t, r, "L1", "us-fl.grt-additional").Amount, "0.15")
	eq(t, "total", r.Totals.CustomerTotal, "107.44")

	// A residential landline is exempt from the CST and the 0.15%.
	r = determine(t, global(t), usSale("FL", "FIXED_VOICE").doc(t), oct2026)
	eq(t, "residential total", r.Totals.CustomerTotal, "102.37")
}

func TestNewYorkExciseIsTheProvidersCost(t *testing.T) {
	r := determine(t, global(t), usSale("NY", "MOBILE_VOICE").doc(t), oct2026)
	excise := component(t, r, "L1", "us-ny.excise-186e-mobile")
	if excise.Treatment != taxation.TreatmentProviderCost {
		t.Fatalf("§186-e is %s", excise.Treatment)
	}
	eq(t, "excise", excise.Amount, "2.90")
	eq(t, "customer total", r.Totals.CustomerTotal, "104.00")
	eq(t, "provider accrual", r.Totals.ProviderAccrual, "2.90")
}

func TestInternetAccessIsExemptEverywhere(t *testing.T) {
	e := global(t)
	for _, st := range []string{"TX", "NY", "FL", "WA", "CA"} {
		r := determine(t, e, usSale(st, "MOBILE_DATA").doc(t), oct2026)
		c := component(t, r, "L1", "us-"+lower(st)+".internet-access")
		if c.LegalReason != "EXEMPT" {
			t.Errorf("%s internet access: legal reason %q", st, c.LegalReason)
		}
		eq(t, st+" total", r.Totals.CustomerTotal, "100.00")
	}
}

func TestStatesWithoutATelecomTaxSaySo(t *testing.T) {
	// California does not tax telecommunications services; Oregon has no
	// sales tax at all. Both are a recorded zero with a reason, not silence.
	for _, st := range []string{"CA", "OR"} {
		r := determine(t, global(t), usSale(st, "MOBILE_VOICE").doc(t), oct2026)
		c := component(t, r, "L1", "us-"+lower(st)+".telecom-untaxed")
		if c.LegalReason != "OUT_OF_SCOPE" {
			t.Errorf("%s: legal reason %q", st, c.LegalReason)
		}
		eq(t, st+" total", r.Totals.CustomerTotal, "100.00")
	}
}

func TestTheUSNeedsTheState(t *testing.T) {
	s := sale{seller: taxation.Place{Country: "US"}, evidence: usedAt(taxation.Place{Country: "US"}),
		service: "MOBILE_VOICE", currency: "USD", amount: "100.00"}
	unresolved(t, determine(t, global(t), s.doc(t), oct2026), taxation.StateReviewRequired, errs.ReasonJurisdictionLevelRequired)
}

func TestUnverifiedServicesAreUnsupportedNotZero(t *testing.T) {
	// Delaware's treatment of VoIP could not be verified, so the pack has no
	// component for it and the engine says so rather than charging nothing.
	r := determine(t, global(t), usSale("DE", "VOIP").doc(t), oct2026)
	if r.State == taxation.StateResolved {
		t.Fatalf("Delaware VoIP resolved: %+v", r.Totals)
	}
}

// Local taxes: a place named by its city bears the state's taxes and the
// city's on top. The pack models New York City, Chicago and Los Angeles only.

func usCitySale(city, service string) sale {
	return sale{
		seller:   taxation.Place{Country: "US"},
		evidence: usedAt(taxation.Place{Jurisdiction: "jurisdiction:" + city}),
		service:  service, currency: "USD", amount: "100.00",
	}
}

func TestUSCitiesStackLocalOnStateTaxes(t *testing.T) {
	e := global(t)
	type charge struct{ component, amount string }
	cases := []struct {
		name    string
		city    string
		charges []charge
		total   string
	}{
		// 4% State + 4.5% City + 0.375% MCTD = 8.875%; the §186-e excise is
		// the provider's cost and stays off the bill.
		{"New York City mobile voice", "us-ny-new-york-city", []charge{
			{"us-ny.sales-telecom", "4.00"},
			{"us-ny-new-york-city.sales-telecom", "4.50"},
			{"us-ny-new-york-city.mctd", "0.38"},
		}, "108.88"},
		// 8.65% Illinois excise + 7% Chicago telecommunications tax.
		{"Chicago mobile voice", "us-il-chicago", []charge{
			{"us-il.tet", "8.65"},
			{"us-il-chicago.telecom", "7.00"},
		}, "115.65"},
		// California taxes no telecommunications; the City's 9% CUT does.
		{"Los Angeles mobile voice", "us-ca-los-angeles", []charge{
			{"us-ca-los-angeles.cut", "9.00"},
		}, "109.00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := determine(t, e, usCitySale(c.city, "MOBILE_VOICE").doc(t), oct2026)
			if r.State != taxation.StateResolved {
				t.Fatalf("state %s (%s)", r.State, r.Reason)
			}
			for _, ch := range c.charges {
				eq(t, ch.component, component(t, r, "L1", ch.component).Amount, ch.amount)
			}
			eq(t, "customer total", r.Totals.CustomerTotal, c.total)
		})
	}
}

func TestLocalTaxesCiteTheirOwnSource(t *testing.T) {
	r := determine(t, global(t), usCitySale("us-il-chicago", "MOBILE_VOICE").doc(t), oct2026)
	c := component(t, r, "L1", "us-il-chicago.telecom")
	if c.Source != "SRC-ZOIKO-US-LOCAL-TELECOM-2026" || c.Citation == "" {
		t.Errorf("Chicago cites %q", c.Source)
	}
}

func TestAStateWithoutItsCityBearsStateTaxesOnly(t *testing.T) {
	// The engine never geocodes: a Chicago customer named only as US-IL is
	// taxed as Illinois, so the caller must name the city to get its tax.
	r := determine(t, global(t), usSale("IL", "MOBILE_VOICE").doc(t), oct2026)
	eq(t, "Illinois total", r.Totals.CustomerTotal, "108.65")
}

func TestInternetAccessIsExemptInCitiesToo(t *testing.T) {
	e := global(t)
	for _, city := range []string{"us-ny-new-york-city", "us-il-chicago", "us-ca-los-angeles"} {
		r := determine(t, e, usCitySale(city, "MOBILE_DATA").doc(t), oct2026)
		eq(t, city+" total", r.Totals.CustomerTotal, "100.00")
	}
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
