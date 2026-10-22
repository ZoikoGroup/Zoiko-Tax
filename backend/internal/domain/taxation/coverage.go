package taxation

import (
	"sort"
	"time"
)

// RegimeCoverage is one country's coverage at an instant.
type RegimeCoverage struct {
	ID               string
	Country          string
	Name             string
	Services         []string
	RequiresLevel    string
	InclusivePricing bool
	Components       []ComponentCoverage
}

// ComponentCoverage is one component in force at an instant.
type ComponentCoverage struct {
	ID           string
	Jurisdiction string
	Authority    string
	TaxType      string
	Name         string
	Services     []string
	Treatments   []string
	RateKind     string
	Rate         string
	Currency     string
	Unit         string
	LegalReason  string
	From         string
	To           string
	Source       string
	Citation     string
}

// Coverage reports what the profile determines by location at t: every
// regime, and every component with a version in force then. It is the
// truthful-coverage register of playbook §25 for the active content — a
// country it does not list is one a determination answers UNSUPPORTED.
func (e *Engine) Coverage(t time.Time) []RegimeCoverage {
	byCountry := map[string]*RegimeCoverage{}
	for _, country := range e.covered {
		rg := e.regimes[country]
		services := make([]string, 0, len(rg.services))
		for s := range rg.services {
			services = append(services, s)
		}
		sort.Strings(services)
		level := ""
		if rg.decl.RequiresLevel != "" {
			level = rg.decl.RequiresLevel
		}
		byCountry[country] = &RegimeCoverage{
			ID: rg.decl.ID, Country: country, Name: rg.decl.Name, Services: services,
			RequiresLevel: level, InclusivePricing: rg.decl.Extraction != nil,
		}
	}
	for _, c := range e.comps {
		v, ok := c.versionAt(t)
		if !ok {
			continue
		}
		seen := map[string]bool{}
		var treatments []string
		for _, a := range c.decl.Applies {
			if !seen[a.Treatment] {
				seen[a.Treatment] = true
				treatments = append(treatments, a.Treatment)
			}
		}
		cc := ComponentCoverage{
			ID: c.decl.ID, Jurisdiction: c.decl.Jurisdiction, Authority: c.decl.Authority,
			TaxType: c.decl.TaxType, Name: c.decl.Name, Services: append([]string(nil), c.decl.Services...),
			Treatments: treatments, RateKind: v.decl.Kind, Rate: rateValue(v), Unit: v.perUnits,
			LegalReason: v.reason, From: v.decl.From, To: v.decl.To, Source: v.decl.Source, Citation: v.decl.Citation,
		}
		if v.decl.Currency != "" {
			cc.Currency = v.decl.Currency
		}
		r := byCountry[c.regime.decl.Country]
		r.Components = append(r.Components, cc)
	}
	out := make([]RegimeCoverage, 0, len(e.covered))
	for _, country := range e.covered {
		out = append(out, *byCountry[country])
	}
	return out
}
