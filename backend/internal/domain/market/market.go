// Package market reports where ZoikoTax can serve, and how far: a market's
// coverage state as the customer console shows it (Doc 16 §04, Doc 18 "no
// coverage inflation").
//
// Two records meet here. The research catalogue is the global taxonomy's
// country profiles — what has been researched, and from which sources. It is
// never coverage: a profile is a research row, and counting profiles never
// counts live markets. Coverage is what the cell can actually do now — the
// content in force, whether the cell may emit authoritative output, and
// whether the legal gate would let a return be filed.
//
// Assess fails towards the weaker state. A market is ACTIVE only when content
// is in force for it, the cell is authoritative and the filing gate is ready;
// content alone is a PILOT, and a profile without content is RESEARCH_ONLY.
// A country neither record names is absent, which a client shows as not
// available.
package market

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Research is how far the research behind a profile has got.
type Research string

// The research states the catalogue records.
const (
	ResearchOnly            Research = "RESEARCH_ONLY"
	EvidenceReviewedPartial Research = "EVIDENCE_REVIEWED_PARTIAL"
	EvidenceReviewed        Research = "EVIDENCE_REVIEWED"
)

// Valid reports whether r is a known research state.
func (r Research) Valid() bool {
	switch r {
	case ResearchOnly, EvidenceReviewedPartial, EvidenceReviewed:
		return true
	}
	return false
}

// Profile is one country or territory the research catalogue names.
type Profile struct {
	Country  string   `json:"country"`
	Name     string   `json:"name"`
	Region   string   `json:"region"`
	Research Research `json:"research"`
	// Sources are the evidence source ids the research rests on.
	Sources []string `json:"sources"`
	// AsOf is the civil date the research was current at, YYYY-MM-DD.
	AsOf string `json:"asOf"`
}

// Catalogue is the research catalogue: versioned content, like the legal
// matrix, and never a statement that a market is live.
type Catalogue struct {
	Version string    `json:"version"`
	Issued  string    `json:"issued"`
	Source  string    `json:"source"`
	Note    string    `json:"note"`
	Markets []Profile `json:"markets"`
}

var (
	countryShape = regexp.MustCompile(`^[A-Z]{2}$`)
	dateShape    = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

// ParseCatalogue reads and validates a catalogue document. Profiles come
// back ordered by country.
func ParseCatalogue(b []byte) (Catalogue, error) {
	var c Catalogue
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Catalogue{}, fmt.Errorf("market: the catalogue does not parse: %w", err)
	}
	if strings.TrimSpace(c.Version) == "" {
		return Catalogue{}, fmt.Errorf("market: the catalogue names no version")
	}
	seen := map[string]bool{}
	for _, p := range c.Markets {
		switch {
		case !countryShape.MatchString(p.Country):
			return Catalogue{}, fmt.Errorf("market: %q is not an ISO 3166-1 alpha-2 code", p.Country)
		case seen[p.Country]:
			return Catalogue{}, fmt.Errorf("market: %s appears twice", p.Country)
		case strings.TrimSpace(p.Name) == "":
			return Catalogue{}, fmt.Errorf("market: %s has no name", p.Country)
		case !p.Research.Valid():
			return Catalogue{}, fmt.Errorf("market: %s has unknown research state %q", p.Country, p.Research)
		case !dateShape.MatchString(p.AsOf):
			return Catalogue{}, fmt.Errorf("market: %s has no as-of date", p.Country)
		}
		seen[p.Country] = true
	}
	slices.SortFunc(c.Markets, func(a, b Profile) int { return strings.Compare(a.Country, b.Country) })
	return c, nil
}

// Status is a market's coverage state.
type Status string

// The states Assess reports. A country in neither record is not reported.
const (
	// StatusActive: content in force, an authoritative cell and a filing
	// gate that would allow a return.
	StatusActive Status = "ACTIVE"
	// StatusPilot: content in force, and something short of ACTIVE.
	StatusPilot Status = "PILOT"
	// StatusResearchOnly: researched, not available for calculation.
	StatusResearchOnly Status = "RESEARCH_ONLY"
)

// Reason says what keeps a market from the next state.
type Reason string

// The reasons.
const (
	// ReasonAdvisoryCell: the cell emits advisory output only (before A4).
	ReasonAdvisoryCell Reason = "ADVISORY_CELL"
	// ReasonFilingNotReady: no authority there would accept a return now.
	ReasonFilingNotReady Reason = "FILING_NOT_READY"
	// ReasonNoContent: no content is in force for the country.
	ReasonNoContent Reason = "NO_CONTENT_IN_FORCE"
)

// Market is one country's coverage.
type Market struct {
	Country string
	// Name and Region come from the catalogue; empty for a country only
	// content names.
	Name, Region string
	Status       Status
	// Reason is empty for an ACTIVE market.
	Reason Reason
	// Research is empty for a country the catalogue does not name.
	Research Research
	// Services are the service families content in force covers there.
	Services []string
}

// Facts are what Assess decides from.
type Facts struct {
	Catalogue Catalogue
	// Content maps a country to the service families content in force covers
	// there. A country absent from it has no content.
	Content map[string][]string
	// FilingReady is the countries where some authority's filing gate is
	// ready now.
	FilingReady map[string]bool
	// Authoritative is whether the cell may emit authoritative output.
	Authoritative bool
}

// Assess reports every country either record names, ordered by country.
func Assess(f Facts) []Market {
	byCountry := map[string]*Market{}
	for _, p := range f.Catalogue.Markets {
		byCountry[p.Country] = &Market{Country: p.Country, Name: p.Name, Region: p.Region, Research: p.Research}
	}
	for country := range f.Content {
		if _, ok := byCountry[country]; !ok {
			byCountry[country] = &Market{Country: country}
		}
	}
	out := make([]Market, 0, len(byCountry))
	for country, m := range byCountry {
		services, ok := f.Content[country]
		switch {
		case !ok:
			m.Status, m.Reason = StatusResearchOnly, ReasonNoContent
		case !f.Authoritative:
			m.Status, m.Reason = StatusPilot, ReasonAdvisoryCell
		case !f.FilingReady[country]:
			m.Status, m.Reason = StatusPilot, ReasonFilingNotReady
		default:
			m.Status = StatusActive
		}
		m.Services = slices.Sorted(slices.Values(services))
		if m.Services == nil {
			m.Services = []string{}
		}
		out = append(out, *m)
	}
	slices.SortFunc(out, func(a, b Market) int { return strings.Compare(a.Country, b.Country) })
	return out
}
