package market_test

import (
	"os"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/market"
)

func TestTheShippedCatalogueParses(t *testing.T) {
	b, err := os.ReadFile("../../../../content/markets/catalogue.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := market.ParseCatalogue(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Markets) != 59 {
		t.Fatalf("%d profiles, want the master's 59", len(c.Markets))
	}
	if c.Markets[0].Country > c.Markets[1].Country {
		t.Fatalf("profiles are not ordered by country")
	}
}

func TestParseCatalogueRefuses(t *testing.T) {
	profile := `{"country":"GB","name":"United Kingdom","region":"Europe","research":"RESEARCH_ONLY","sources":[],"asOf":"2026-10-30"}`
	cases := map[string]string{
		"no version":     `{"version":"","markets":[]}`,
		"unknown field":  `{"version":"v","markets":[],"live":true}`,
		"bad country":    `{"version":"v","markets":[` + strings.Replace(profile, `"GB"`, `"gb"`, 1) + `]}`,
		"duplicate":      `{"version":"v","markets":[` + profile + `,` + profile + `]}`,
		"unknown state":  `{"version":"v","markets":[` + strings.Replace(profile, "RESEARCH_ONLY", "PRODUCTION", 1) + `]}`,
		"no as-of":       `{"version":"v","markets":[` + strings.Replace(profile, "2026-10-30", "", 1) + `]}`,
		"activation set": `{"version":"v","markets":[` + strings.Replace(profile, `"asOf"`, `"activation":"ACTIVE","asOf"`, 1) + `]}`,
	}
	for name, doc := range cases {
		if _, err := market.ParseCatalogue([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAssessFailsTowardsTheWeakerState(t *testing.T) {
	cat := market.Catalogue{Version: "v", Markets: []market.Profile{
		{Country: "GB", Name: "United Kingdom", Region: "Europe", Research: market.EvidenceReviewed},
		{Country: "MX", Name: "Mexico", Region: "North America", Research: market.ResearchOnly},
	}}
	content := map[string][]string{"GB": {"VOICE", "DATA"}, "EE": {"DATA"}}

	byCountry := func(ms []market.Market) map[string]market.Market {
		out := map[string]market.Market{}
		for _, m := range ms {
			out[m.Country] = m
		}
		return out
	}

	// An advisory cell has no ACTIVE market, whatever the gate says.
	advisory := byCountry(market.Assess(market.Facts{Catalogue: cat, Content: content, FilingReady: map[string]bool{"GB": true}}))
	if m := advisory["GB"]; m.Status != market.StatusPilot || m.Reason != market.ReasonAdvisoryCell {
		t.Errorf("GB in an advisory cell: %+v", m)
	}
	if m := advisory["MX"]; m.Status != market.StatusResearchOnly || m.Reason != market.ReasonNoContent || len(m.Services) != 0 {
		t.Errorf("a researched country without content: %+v", m)
	}
	if m := advisory["EE"]; m.Status != market.StatusPilot || m.Name != "" || m.Research != "" {
		t.Errorf("a country only content names: %+v", m)
	}
	if _, ok := advisory["FR"]; ok {
		t.Errorf("a country neither record names was reported")
	}

	live := market.Assess(market.Facts{Catalogue: cat, Content: content, FilingReady: map[string]bool{"GB": true}, Authoritative: true})
	got := byCountry(live)
	if m := got["GB"]; m.Status != market.StatusActive || m.Reason != "" || strings.Join(m.Services, ",") != "DATA,VOICE" {
		t.Errorf("GB live: %+v", m)
	}
	if m := got["EE"]; m.Status != market.StatusPilot || m.Reason != market.ReasonFilingNotReady {
		t.Errorf("EE without a ready gate: %+v", m)
	}
	if live[0].Country != "EE" || live[1].Country != "GB" || live[2].Country != "MX" {
		t.Errorf("markets are not ordered by country: %+v", live)
	}
}
