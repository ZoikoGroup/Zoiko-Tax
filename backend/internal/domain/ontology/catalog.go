package ontology

import (
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// ExternalCode is an identifier from outside ZoikoTax: a customer SKU, a
// vendor template code, a TM Forum id, an incumbent tax engine's product
// code. It always names its source system and version (ZTAX-CLS-REQ-0103),
// and it is only ever mapped to the ontology — never used as an ontology key
// (ZTAX-CLS-REQ-0010 to -0012).
type ExternalCode struct {
	System        string
	SystemVersion string
	Code          string
}

func (e ExternalCode) validate() error {
	if e.System == "" || e.SystemVersion == "" || e.Code == "" {
		return fmt.Errorf("ontology: external code %+v names no system, version or code", e)
	}
	return nil
}

// MappingSource is where a mapping came from. The order of the constants is
// not the precedence; Precedence is.
type MappingSource string

// The mapping sources.
const (
	SourceTenant         MappingSource = "TENANT"
	SourceVendorTemplate MappingSource = "VENDOR_TEMPLATE"
	SourceTMF620         MappingSource = "TMF620"
	// SourceIncumbent is an imported legacy tax-engine code. It seeds a
	// proposal and never resolves on its own (ZTAX-CLS-REQ-0061).
	SourceIncumbent MappingSource = "INCUMBENT"
	// SourceAIProposal is an AI-proposed mapping to an existing node
	// (ZTAX-CLS-REQ-0082). It never resolves on its own either.
	SourceAIProposal MappingSource = "AI_PROPOSAL"
)

// precedence is the deterministic order mappings are consulted in
// (ZTAX-CLS-REQ-0059): the tenant's own word first, a promoted vendor
// template next, then a TMF620 catalog mapping. Proposals rank last and only
// ever produce REVIEW_REQUIRED.
var precedence = map[MappingSource]int{
	SourceTenant: 0, SourceVendorTemplate: 1, SourceTMF620: 2, SourceIncumbent: 3, SourceAIProposal: 3,
}

// proposal reports whether a source only proposes.
func (s MappingSource) proposal() bool { return s == SourceIncumbent || s == SourceAIProposal }

// MappingStatus is a mapping's lifecycle. A STAGED mapping is never used to
// classify (ZTAX-CLS-REQ-0106).
type MappingStatus string

// The mapping statuses.
const (
	MappingStaged  MappingStatus = "STAGED"
	MappingActive  MappingStatus = "ACTIVE"
	MappingRetired MappingStatus = "RETIRED"
)

// CatalogMapping is one mapping from an external code to a node.
type CatalogMapping struct {
	ID      string
	Version int
	// Tenant scopes the mapping (ZTAX-CLS-REQ-0053). A zero tenant is a
	// reusable template, and it is usable only once Promoted.
	Tenant   id.TenantID
	Promoted bool
	External ExternalCode
	Target   ID
	Charge   ChargeType
	Source   MappingSource
	Status   MappingStatus
	// EffectiveFrom and EffectiveTo date the mapping (ZTAX-CLS-REQ-0054).
	EffectiveFrom time.Time
	EffectiveTo   *time.Time
	// Evidence is the proposal evidence an AI or import mapping carries — an
	// AIReleaseManifest reference for AI (ZTAX-CLS-REQ-0089) — and a human
	// correction keeps it (ZTAX-CLS-REQ-0090).
	Evidence string
	// Supersedes names the mapping a correction replaces, which survives.
	Supersedes *string
}

func (m CatalogMapping) effectiveAt(t time.Time) bool {
	return !t.Before(m.EffectiveFrom) && (m.EffectiveTo == nil || t.Before(*m.EffectiveTo))
}

// Validate refuses a mapping that could not be used or audited.
func (m CatalogMapping) Validate(o *Ontology) error {
	if m.ID == "" || m.Version < 1 {
		return fmt.Errorf("ontology: mapping has no id or no version")
	}
	if err := m.External.validate(); err != nil {
		return err
	}
	if _, ok := precedence[m.Source]; !ok {
		return fmt.Errorf("ontology: mapping %s has source %q", m.ID, m.Source)
	}
	if m.EffectiveFrom.IsZero() {
		return fmt.Errorf("ontology: mapping %s is not effective-dated", m.ID)
	}
	if !o.Has(m.Target) {
		// ZTAX-CLS-REQ-0086: a mapping points at an existing node. Nothing
		// here — an AI proposal least of all — creates one.
		return fmt.Errorf("ontology: mapping %s targets %s, which the ontology does not declare", m.ID, m.Target)
	}
	if m.Source == SourceAIProposal && m.Evidence == "" {
		return fmt.Errorf("ontology: AI mapping %s carries no release-manifest evidence", m.ID)
	}
	if m.Tenant.IsZero() && m.Source == SourceTenant {
		return fmt.Errorf("ontology: tenant mapping %s names no tenant", m.ID)
	}
	return nil
}

// Resolution is a mapping lookup's outcome.
type Resolution struct {
	Status ResolutionStatus
	// Chosen is set when Status is RESOLVED.
	Chosen *CatalogMapping
	// Candidates are retained for review whenever the lookup did not resolve
	// cleanly — every conflicting mapping, every proposal (ZTAX-CLS-REQ-0081).
	Candidates []CatalogMapping
}

// ResolutionStatus is a mapping lookup's status, in classification's
// vocabulary.
type ResolutionStatus string

// The mapping resolution statuses.
const (
	StatusResolved       ResolutionStatus = "RESOLVED"
	StatusConflicted     ResolutionStatus = "CONFLICTED"
	StatusUnsupported    ResolutionStatus = "UNSUPPORTED"
	StatusReviewRequired ResolutionStatus = "REVIEW_REQUIRED"
)

// Resolve finds the mapping for an external code, for a tenant, at an instant.
//
// Only the tenant's own mappings and promoted templates are visible — another
// tenant's mapping never is (ZTAX-CLS-REQ-0057). Only ACTIVE mappings
// effective at the instant are used. The highest-precedence tier that has any
// mapping decides: one target is RESOLVED; two different targets in one tier
// is CONFLICTED, never a pick (ZTAX-CLS-REQ-0062); a tier of proposals only is
// REVIEW_REQUIRED. Nothing at all is UNSUPPORTED — not OTHER_NON_TELECOM.
func Resolve(tenant id.TenantID, code ExternalCode, at time.Time, mappings []CatalogMapping) Resolution {
	tiers := map[int][]CatalogMapping{}
	for _, m := range mappings {
		if m.External != code || m.Status != MappingActive || !m.effectiveAt(at) {
			continue
		}
		visible := m.Tenant == tenant || (m.Tenant.IsZero() && m.Promoted)
		if !visible {
			continue
		}
		p := precedence[m.Source]
		tiers[p] = append(tiers[p], m)
	}
	levels := make([]int, 0, len(tiers))
	for p := range tiers {
		levels = append(levels, p)
	}
	sort.Ints(levels)
	if len(levels) == 0 {
		return Resolution{Status: StatusUnsupported}
	}
	top := tiers[levels[0]]
	sort.Slice(top, func(i, j int) bool { return top[i].ID < top[j].ID })
	if top[0].Source.proposal() {
		return Resolution{Status: StatusReviewRequired, Candidates: top}
	}
	targets := map[ID]bool{}
	for _, m := range top {
		targets[m.Target] = true
	}
	if len(targets) > 1 {
		return Resolution{Status: StatusConflicted, Candidates: top}
	}
	chosen := top[0]
	return Resolution{Status: StatusResolved, Chosen: &chosen}
}

// Change is one external code whose resolution an import would change.
type Change struct {
	Code   ExternalCode
	Before ID
	After  ID
}

// Import is a staged bulk import.
type Import struct {
	Mappings []CatalogMapping
}

// Activate validates every staged mapping and returns the activated set with
// its impact on the current set (ZTAX-CLS-REQ-0108). One invalid mapping
// activates none of them: a partial import that activated what it could would
// leave a catalog half old and half new (ZTAX-CLS-REQ-0107).
func (im Import) Activate(o *Ontology, current []CatalogMapping, tenant id.TenantID, at time.Time) ([]CatalogMapping, []Change, error) {
	if len(im.Mappings) == 0 {
		return nil, nil, fmt.Errorf("ontology: empty import")
	}
	var bad []string
	for _, m := range im.Mappings {
		if m.Status != MappingStaged {
			bad = append(bad, fmt.Sprintf("%s is %s, not STAGED", m.ID, m.Status))
			continue
		}
		if err := m.Validate(o); err != nil {
			bad = append(bad, err.Error())
		}
	}
	if len(bad) > 0 {
		return nil, nil, fmt.Errorf("ontology: import refused, nothing activated: %v", bad)
	}
	activated := make([]CatalogMapping, len(im.Mappings))
	for i, m := range im.Mappings {
		m.Status = MappingActive
		activated[i] = m
	}
	after := append(append([]CatalogMapping(nil), current...), activated...)
	seen := map[ExternalCode]bool{}
	var changes []Change
	for _, m := range activated {
		if seen[m.External] {
			continue
		}
		seen[m.External] = true
		before := Resolve(tenant, m.External, at, current)
		now := Resolve(tenant, m.External, at, after)
		var b, a ID
		if before.Chosen != nil {
			b = before.Chosen.Target
		}
		if now.Chosen != nil {
			a = now.Chosen.Target
		}
		if a != b {
			changes = append(changes, Change{Code: m.External, Before: b, After: a})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Code.Code < changes[j].Code.Code })
	return activated, changes, nil
}

// TMF620 is the slice of a TM Forum TMF620 catalog entry the adapter reads.
type TMF620 struct {
	CatalogVersion       string
	ProductSpecification string
	ProductOffering      string
	ProductOfferingPrice string
}

// TMF620Codes turns a catalog entry into external codes. Every TM Forum id
// stays an external code under its own system name — specification, offering
// and price each — and none becomes a canonical id (ZTAX-CLS-REQ-0011,
// -0104).
func TMF620Codes(e TMF620) ([]ExternalCode, error) {
	if e.CatalogVersion == "" || e.ProductSpecification == "" {
		return nil, fmt.Errorf("ontology: TMF620 entry has no catalog version or no product specification")
	}
	out := []ExternalCode{{System: "tmf620:productSpecification", SystemVersion: e.CatalogVersion, Code: e.ProductSpecification}}
	if e.ProductOffering != "" {
		out = append(out, ExternalCode{System: "tmf620:productOffering", SystemVersion: e.CatalogVersion, Code: e.ProductOffering})
	}
	if e.ProductOfferingPrice != "" {
		out = append(out, ExternalCode{System: "tmf620:productOfferingPrice", SystemVersion: e.CatalogVersion, Code: e.ProductOfferingPrice})
	}
	return out, nil
}

// MappingSources returns every mapping source, sorted.
func MappingSources() []MappingSource {
	return sortedKeys(map[MappingSource]bool{
		SourceTenant: true, SourceVendorTemplate: true, SourceTMF620: true, SourceIncumbent: true, SourceAIProposal: true,
	})
}
