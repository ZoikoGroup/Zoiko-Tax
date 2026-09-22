// Package jurisdiction resolves a location to the set of taxing authorities
// that apply to it.
//
// The semantics this package cannot supply are the ones ZTAX-JUR-001 was going
// to specify, and that document was never produced to the specification
// pipeline (Build Plan §2). What is here is therefore the structure — the
// types, the precedence model, the resolution interface and the evidence a
// resolution has to carry — with the substantive rules coming from content, as
// ADR-0001 §2.5 requires of every other fiscal rule.
//
// The one thing this package settles on its own is that a resolution is a
// record, not a lookup. A determination that cannot say which jurisdictions
// applied and why cannot be explained (UX-05) or replayed, so Resolution
// carries its inputs and its precedence decision rather than just an answer.
package jurisdiction

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Level is the tier of a jurisdiction in the hierarchy.
//
// The order of these constants is the precedence order, and Level.Rank depends
// on it. It is declared once here so that "which jurisdiction wins" is answered
// by one comparison rather than by a rule repeated per country pack.
type Level string

// The levels, outermost first.
const (
	LevelCountry  Level = "COUNTRY"
	LevelState    Level = "STATE"
	LevelCounty   Level = "COUNTY"
	LevelCity     Level = "CITY"
	LevelDistrict Level = "DISTRICT"
	// LevelSpecial is a special-purpose district — a transit authority, a
	// stadium district — that does not sit cleanly in the geographic
	// hierarchy. It ranks last so it never displaces a general jurisdiction.
	LevelSpecial Level = "SPECIAL"
)

var levelRank = map[Level]int{
	LevelCountry: 0, LevelState: 1, LevelCounty: 2,
	LevelCity: 3, LevelDistrict: 4, LevelSpecial: 5,
}

// Rank returns the precedence position, and whether the level is known.
func (l Level) Rank() (int, bool) {
	r, ok := levelRank[l]
	return r, ok
}

// Valid reports whether l is a known level.
func (l Level) Valid() bool { _, ok := levelRank[l]; return ok }

// ID is a content identifier for a jurisdiction: a stable, human-meaningful
// string under a registered grammar, not a UUID (ADR-0012 §2.4).
//
//	jurisdiction:us
//	jurisdiction:us-ca
//	jurisdiction:us-ca/alameda/berkeley
//
// A content reviewer, an auditor and a golden vector all need to name the same
// jurisdiction across versions, which a surrogate key cannot do.
type ID string

// Validate applies the grammar.
func (i ID) Validate() error {
	s := string(i)
	if !strings.HasPrefix(s, "jurisdiction:") {
		return fmt.Errorf("jurisdiction: %q does not carry the jurisdiction: prefix", i)
	}
	rest := strings.TrimPrefix(s, "jurisdiction:")
	if rest == "" {
		return fmt.Errorf("jurisdiction: %q names nothing", i)
	}
	for _, r := range rest {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '/':
		default:
			return fmt.Errorf("jurisdiction: %q holds %q, which the grammar does not permit", i, r)
		}
	}
	return nil
}

// Jurisdiction is one taxing authority's area.
type Jurisdiction struct {
	ID     ID
	Parent ID
	Level  Level
	Name   string
	// Country is the ISO 3166-1 alpha-2 code.
	Country string
	// Timezone is the civil timezone, kept beside the boundary because
	// ADR-0003 §2.6 resolves a legal effective date against it — an instrument
	// saying "from 1 April" means midnight in this zone, under the DST rules as
	// they stood.
	Timezone string
}

// Location is what a caller supplies to be resolved.
//
// Both an address and coordinates may be present. Neither is preferred here:
// which one wins is a content decision per country, because the answer differs
// — a US rooftop resolution prefers coordinates, and a jurisdiction defined by
// postal code prefers the address.
type Location struct {
	Country    string
	Region     string
	Locality   string
	PostalCode string
	Line1      string
	// Latitude and Longitude are canonical decimal strings, never floats
	// (ADR-0001 C1 applies to any decimal that reaches a fiscal path, and a
	// coordinate that picks the wrong side of a boundary picks the wrong tax).
	Latitude  string
	Longitude string
}

// HasCoordinates reports whether a point resolution is possible.
func (l Location) HasCoordinates() bool { return l.Latitude != "" && l.Longitude != "" }

// Canonical renders a location for the canonical input (ADR-0011).
func (l Location) Canonical() canonical.Value {
	return canonical.Object(
		canonical.F("country", canonical.String(l.Country)),
		canonical.F("region", canonical.OptString(l.Region)),
		canonical.F("locality", canonical.OptString(l.Locality)),
		canonical.F("postalCode", canonical.OptString(l.PostalCode)),
		canonical.F("line1", canonical.OptString(l.Line1)),
		canonical.F("latitude", canonical.OptString(l.Latitude)),
		canonical.F("longitude", canonical.OptString(l.Longitude)),
	)
}

// Method is how a resolution was reached. It is recorded because the methods
// differ in reliability, and a decision reached by postal-code lookup should be
// distinguishable from one reached by rooftop geocoding when somebody disputes
// it.
type Method string

// The resolution methods.
const (
	// MethodPoint is a spatial containment test against a boundary.
	MethodPoint Method = "POINT_IN_POLYGON"
	// MethodAdministrative is a match on named administrative divisions.
	MethodAdministrative Method = "ADMINISTRATIVE"
	// MethodPostalCode is a postal-code lookup, which is approximate wherever
	// postal areas cross jurisdiction boundaries — which is most places.
	MethodPostalCode Method = "POSTAL_CODE"
	// MethodDeclared is a jurisdiction the caller asserted. It is recorded as
	// such so that a determination made on the customer's own assertion is
	// never mistaken for one we resolved.
	MethodDeclared Method = "DECLARED"
)

// Resolution is the record of one jurisdiction resolution.
type Resolution struct {
	// Applicable is every jurisdiction that applies, in precedence order.
	Applicable []Jurisdiction
	Method     Method
	// Input is the location as supplied, so the resolution can be re-checked
	// against what was actually asked rather than against what was meant.
	Input Location
	// Unresolved is set when no jurisdiction was found. It is not an error:
	// ADR-0016 §2.1 makes "we do not cover this" a recorded outcome, and a
	// customer is entitled to have it recorded against their transaction.
	Unresolved bool
}

// Primary returns the most specific applicable jurisdiction.
//
// "Most specific" is the deepest level present, which is the convention every
// country pack is authored against. Where two jurisdictions share a level the
// first in precedence order wins, and Sort has already made that deterministic.
func (r Resolution) Primary() (Jurisdiction, bool) {
	if len(r.Applicable) == 0 {
		return Jurisdiction{}, false
	}
	return r.Applicable[len(r.Applicable)-1], true
}

// Sort orders the applicable set outermost-first and deterministically.
//
// The tie-break on ID is what makes this deterministic rather than merely
// ordered: two jurisdictions at one level would otherwise come back in whatever
// order the database returned them, and a determination whose evidence lists
// them differently on two replicas is a determination that does not replay.
func (r *Resolution) Sort() {
	sort.SliceStable(r.Applicable, func(i, j int) bool {
		ri, iok := r.Applicable[i].Level.Rank()
		rj, jok := r.Applicable[j].Level.Rank()
		switch {
		case iok && jok && ri != rj:
			return ri < rj
		case iok != jok:
			// An unknown level sorts last rather than being dropped: it is a
			// data-quality finding, and hiding it would make it harder to find.
			return iok
		}
		return r.Applicable[i].ID < r.Applicable[j].ID
	})
}

// Canonical renders a resolution for the evidence record.
func (r Resolution) Canonical() canonical.Value {
	items := make([]canonical.Value, 0, len(r.Applicable))
	for _, j := range r.Applicable {
		items = append(items, canonical.Object(
			canonical.F("id", canonical.String(string(j.ID))),
			canonical.F("level", canonical.String(string(j.Level))),
			canonical.F("name", canonical.String(j.Name)),
			canonical.F("country", canonical.String(j.Country)),
		))
	}
	return canonical.Object(
		canonical.F("method", canonical.String(string(r.Method))),
		canonical.F("input", r.Input.Canonical()),
		// Order is precedence and is significant (ADR-0011 P4).
		canonical.F("applicable", canonical.Array(items...)),
		canonical.F("unresolved", canonical.Bool(r.Unresolved)),
	)
}
