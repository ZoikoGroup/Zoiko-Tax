package taxation_test

import (
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/taxation"
)

// North Carolina's counties. A retail sale of equipment sourced to a county
// bears the 4.75% State rate plus the county's local rate, and its 0.50%
// transit rate where it has one. Telecommunications service bears the 7%
// combined general rate collected by the State, the same in every county.
//
// Expected values are the combined rates NCDOR publishes in "Current Sales
// and Use Tax Rates", effective July 1, 2026:
// https://www.ncdor.gov/taxes-forms/sales-and-use-tax/sales-and-use-tax-rates/current-sales-and-use-tax-rates

func ncCountySale(county, service string) sale {
	return usCitySale("us-nc-"+county+"-county", service)
}

func TestNCCountiesStackLocalAndTransitOnTheStateRateForEquipment(t *testing.T) {
	e := global(t)
	type charge struct{ component, amount string }
	cases := []struct {
		name    string
		county  string
		charges []charge
		total   string
	}{
		{"Mecklenburg: 4.75% State + 2.00% local + 1.00% additional + 0.50% transit", "mecklenburg", []charge{
			{"us-nc.sales-goods", "4.75"},
			{"us-nc-mecklenburg-county.local-goods", "2.00"},
			{"us-nc-mecklenburg-county.additional-goods", "1.00"},
			{"us-nc-mecklenburg-county.transit-goods", "0.50"},
		}, "108.25"},
		{"Wake: 4.75% State + 2.00% local + 0.50% transit", "wake", []charge{
			{"us-nc.sales-goods", "4.75"},
			{"us-nc-wake-county.local-goods", "2.00"},
			{"us-nc-wake-county.transit-goods", "0.50"},
		}, "107.25"},
		{"Buncombe: 4.75% State + 2.25% local", "buncombe", []charge{
			{"us-nc.sales-goods", "4.75"},
			{"us-nc-buncombe-county.local-goods", "2.25"},
		}, "107.00"},
		{"Alamance: 4.75% State + 2.00% local", "alamance", []charge{
			{"us-nc.sales-goods", "4.75"},
			{"us-nc-alamance-county.local-goods", "2.00"},
		}, "106.75"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := determine(t, e, ncCountySale(c.county, "EQUIPMENT_SALE").doc(t), oct2026)
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

func TestNCTelecomServiceIsTheStateRateInEveryCounty(t *testing.T) {
	e := global(t)
	for _, county := range []string{"mecklenburg", "wake", "durham", "alamance"} {
		r := determine(t, e, ncCountySale(county, "MOBILE_VOICE").doc(t), oct2026)
		if r.State != taxation.StateResolved {
			t.Fatalf("%s: state %s (%s)", county, r.State, r.Reason)
		}
		eq(t, county+" mobile voice total", r.Totals.CustomerTotal, "107.00")
	}
}

func TestNCInternetAccessIsExemptInCountiesToo(t *testing.T) {
	r := determine(t, global(t), ncCountySale("mecklenburg", "MOBILE_DATA").doc(t), oct2026)
	eq(t, "Mecklenburg mobile data total", r.Totals.CustomerTotal, "100.00")
}

func TestNCCountyLocalTaxesCiteTheNCDORTable(t *testing.T) {
	r := determine(t, global(t), ncCountySale("orange", "EQUIPMENT_SALE").doc(t), oct2026)
	for _, id := range []string{"us-nc-orange-county.local-goods", "us-nc-orange-county.transit-goods"} {
		c := component(t, r, "L1", id)
		if c.Source != "SRC-ZOIKO-US-NC-COUNTY-2026" || !strings.Contains(c.Citation, "ncdor.gov") {
			t.Errorf("%s cites %q: %q", id, c.Source, c.Citation)
		}
	}
}

// Every county's combined rate on a 100.00 equipment sale, against the NCDOR
// table. A county missing from the pack, or a rate typed wrong, fails here.
func TestNCAllHundredCountiesMatchThePublishedCombinedRate(t *testing.T) {
	combined := map[string]string{
		"Alamance": "6.75", "Alexander": "7.00", "Alleghany": "7.00", "Anson": "7.00", "Ashe": "7.00",
		"Avery": "6.75", "Beaufort": "6.75", "Bertie": "7.00", "Bladen": "6.75", "Brunswick": "6.75",
		"Buncombe": "7.00", "Burke": "6.75", "Cabarrus": "7.00", "Caldwell": "6.75", "Camden": "6.75",
		"Carteret": "6.75", "Caswell": "6.75", "Catawba": "7.00", "Chatham": "7.00", "Cherokee": "7.00",
		"Chowan": "6.75", "Clay": "7.00", "Cleveland": "6.75", "Columbus": "6.75", "Craven": "6.75",
		"Cumberland": "7.00", "Currituck": "6.75", "Dare": "6.75", "Davidson": "7.00", "Davie": "6.75",
		"Duplin": "7.00", "Durham": "7.50", "Edgecombe": "7.00", "Forsyth": "7.00", "Franklin": "6.75",
		"Gaston": "7.00", "Gates": "6.75", "Graham": "7.00", "Granville": "6.75", "Greene": "7.00",
		"Guilford": "6.75", "Halifax": "7.00", "Harnett": "7.00", "Haywood": "7.00", "Henderson": "6.75",
		"Hertford": "7.00", "Hoke": "6.75", "Hyde": "6.75", "Iredell": "6.75", "Jackson": "7.00",
		"Johnston": "6.75", "Jones": "7.00", "Lee": "7.00", "Lenoir": "6.75", "Lincoln": "7.00",
		"Macon": "6.75", "Madison": "7.00", "Martin": "7.00", "McDowell": "6.75", "Mecklenburg": "8.25",
		"Mitchell": "6.75", "Montgomery": "7.00", "Moore": "7.00", "Nash": "6.75", "New Hanover": "7.00",
		"Northampton": "6.75", "Onslow": "7.00", "Orange": "7.50", "Pamlico": "6.75", "Pasquotank": "7.00",
		"Pender": "6.75", "Perquimans": "6.75", "Person": "6.75", "Pitt": "7.00", "Polk": "6.75",
		"Randolph": "7.00", "Richmond": "6.75", "Robeson": "7.00", "Rockingham": "7.00", "Rowan": "7.00",
		"Rutherford": "7.00", "Sampson": "7.00", "Scotland": "6.75", "Stanly": "7.00", "Stokes": "6.75",
		"Surry": "7.00", "Swain": "7.00", "Transylvania": "6.75", "Tyrrell": "6.75", "Union": "6.75",
		"Vance": "6.75", "Wake": "7.25", "Warren": "6.75", "Washington": "7.00", "Watauga": "6.75",
		"Wayne": "6.75", "Wilkes": "7.00", "Wilson": "6.75", "Yadkin": "6.75", "Yancey": "6.75",
	}
	if len(combined) != 100 {
		t.Fatalf("the table has %d counties, not 100", len(combined))
	}
	e := global(t)
	for name, rate := range combined {
		slug := strings.ReplaceAll(strings.ToLower(name), " ", "-")
		r := determine(t, e, ncCountySale(slug, "EQUIPMENT_SALE").doc(t), oct2026)
		if r.State != taxation.StateResolved {
			t.Errorf("%s: state %s (%s)", name, r.State, r.Reason)
			continue
		}
		// Every combined rate is under 10%, so 100.00 at r% is "10" followed by r.
		eq(t, name+" total", r.Totals.CustomerTotal, "10"+rate)
	}
}

// Rate history from the NCDOR historical table. The only combined-rate
// changes since October 1, 2020 are Alleghany (6.75 → 7.00 on 2022-10-01),
// Washington (6.75 → 7.00 on 2024-07-01) and Mecklenburg (7.25 → 8.25 on
// 2026-07-01, the 1% Additional County Rate).
func TestNCCountyRatesFollowTheirHistory(t *testing.T) {
	e := global(t)
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }
	cases := []struct {
		county string
		at     time.Time
		total  string
	}{
		{"mecklenburg", day(2026, 6, 30), "107.25"},
		{"mecklenburg", day(2026, 7, 1), "108.25"},
		{"alleghany", day(2022, 9, 30), "106.75"},
		{"alleghany", day(2022, 10, 1), "107.00"},
		{"washington", day(2024, 6, 30), "106.75"},
		{"washington", day(2024, 7, 1), "107.00"},
		// The pack claims North Carolina from 2022-01-01, State and county alike.
		{"wake", day(2022, 1, 1), "107.25"},
	}
	for _, c := range cases {
		r := determine(t, e, ncCountySale(c.county, "EQUIPMENT_SALE").doc(t), c.at)
		if r.State != taxation.StateResolved {
			t.Errorf("%s on %s: state %s (%s)", c.county, c.at.Format(time.DateOnly), r.State, r.Reason)
			continue
		}
		eq(t, c.county+" equipment total on "+c.at.Format(time.DateOnly), r.Totals.CustomerTotal, c.total)
	}
}
