package content

import (
	"sort"
	"strings"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/sourcing"
)

// Level is a package type from CONT-001 §10: how general the content in a pack
// is, from the shared ontology every pack builds on down to one customer's
// approved elections.
//
// The levels are totally ordered, most general first, in the specification's
// own table order, and a pack may depend only on packs at its own level or a
// more general one (CanDependOn). That is ZTAX-CONT-REQ-0027 made structural:
// a NATIONAL pack that could depend on a CUSTOMER-OVERLAY would be national law
// that changes per customer, and an overlay is the thing that "cannot override
// immutable legal constraints".
//
// The wire spellings are the specification's, hyphens included. "SECTOR/
// OBLIGATION" is spelled SECTOR: a slash in an enumerated value is a path
// separator waiting to happen, and the row's own text calls it sector content.
type Level string

// The levels, most general first.
const (
	LevelGlobal           Level = "GLOBAL"
	LevelRegional         Level = "REGIONAL"
	LevelNational         Level = "NATIONAL"
	LevelSubnational      Level = "SUBNATIONAL"
	LevelSector           Level = "SECTOR"
	LevelAuthorityAdapter Level = "AUTHORITY-ADAPTER"
	LevelCustomerOverlay  Level = "CUSTOMER-OVERLAY"
)

// Levels is every level, most general first.
var Levels = []Level{
	LevelGlobal, LevelRegional, LevelNational, LevelSubnational, LevelSector, LevelAuthorityAdapter, LevelCustomerOverlay,
}

// Rank is the level's position in Levels, or -1 for an unknown level.
//
// SECTOR sits below SUBNATIONAL because that is the specification's order and
// because telecom contribution and levy content routinely depends on state and
// municipal jurisdiction content, while the reverse — a municipal rate
// depending on a sector programme — would be the general depending on the
// specific. AUTHORITY-ADAPTER sits below all tax content because a filing
// schema maps tax results that already exist.
func (l Level) Rank() int {
	for i, k := range Levels {
		if k == l {
			return i
		}
	}
	return -1
}

// Valid reports whether l is a known level.
func (l Level) Valid() bool { return l.Rank() >= 0 }

// CanDependOn reports whether a pack at level l may depend on one at level dep:
// only at the same level or a more general one.
func (l Level) CanDependOn(dep Level) bool {
	a, b := l.Rank(), dep.Rank()
	return a >= 0 && b >= 0 && b <= a
}

// PackID is a stable package identity (CONT-001 §11 pack_id).
type PackID string

// Validate checks the identity's shape: upper-case letters, digits and hyphens,
// starting with a letter. It is printed in release notes, evidence and support
// matrices, and a pack identity that renders differently in each is several
// identities.
func (id PackID) Validate() error {
	s := string(id)
	if s == "" {
		return errorf("a pack has no identity")
	}
	for i, c := range s {
		ok := c >= 'A' && c <= 'Z' || c == '-' && i > 0 || c >= '0' && c <= '9' && i > 0
		if !ok {
			return errorf("pack id %q may contain only upper-case letters, digits and hyphens, starting with a letter", s)
		}
	}
	if strings.HasSuffix(s, "-") {
		return errorf("pack id %q ends with a hyphen", s)
	}
	return nil
}

// Capability is what a pack may be claimed to support (CONT-001 Appendix A).
// Support claims are capability-specific (ZTAX-CONT-REQ-0049): a pack that
// determines tax is not thereby a pack that files it.
type Capability string

// The capabilities.
const (
	CapabilityDetermine     Capability = "DETERMINE"
	CapabilityClassify      Capability = "CLASSIFY"
	CapabilityJurisdiction  Capability = "JURISDICTION"
	CapabilityObligations   Capability = "OBLIGATIONS"
	CapabilityRegistrations Capability = "REGISTRATIONS"
	CapabilityFile          Capability = "FILE"
	CapabilityRemit         Capability = "REMIT"
	CapabilityCTC           Capability = "CTC"
	CapabilityManaged       Capability = "MANAGED"
	CapabilityShadow        Capability = "SHADOW"
)

// Valid reports whether c is a known capability.
func (c Capability) Valid() bool {
	switch c {
	case CapabilityDetermine, CapabilityClassify, CapabilityJurisdiction, CapabilityObligations,
		CapabilityRegistrations, CapabilityFile, CapabilityRemit, CapabilityCTC, CapabilityManaged, CapabilityShadow:
		return true
	}
	return false
}

// PackStatus is CONT-001 §11's pack status.
type PackStatus string

// The pack states.
const (
	PackResearch   PackStatus = "RESEARCH"
	PackValidation PackStatus = "VALIDATION"
	PackPilot      PackStatus = "PILOT"
	PackProduction PackStatus = "PRODUCTION"
	PackManaged    PackStatus = "MANAGED"
	PackSuspended  PackStatus = "SUSPENDED"
	PackWithdrawn  PackStatus = "WITHDRAWN"
)

// Valid reports whether s is a known status.
func (s PackStatus) Valid() bool {
	switch s {
	case PackResearch, PackValidation, PackPilot, PackProduction, PackManaged, PackSuspended, PackWithdrawn:
		return true
	}
	return false
}

// Releasable reports whether a bundle may be built and signed for a pack in
// this status. SUSPENDED and WITHDRAWN may not: a suspended pack "cannot
// authorize new production outcomes" (CONT-001 §25, ZTAX-CONT-REQ-0042) and a
// new bundle is nothing but new outcomes; a withdrawn one has retired new use.
func (s PackStatus) Releasable() bool { return s.Valid() && s != PackSuspended && s != PackWithdrawn }

// Governed reports whether the status carries production obligations — an
// owner and a withdrawal plan (ZTAX-CONT-REQ-0040, -0041, -0069).
func (s PackStatus) Governed() bool { return s == PackPilot || s == PackProduction || s == PackManaged }

// CanTransitionPack reports whether from → to is a legal pack status change.
//
//	RESEARCH → VALIDATION → PILOT → PRODUCTION ⇄ MANAGED
//	any live status → SUSPENDED
//	SUSPENDED → VALIDATION              (re-certify before returning to use)
//	any status but WITHDRAWN → WITHDRAWN
//
// SUSPENDED returns through VALIDATION because a suspension is a finding that
// the pack's authority, licensing or integrity was uncertain (CONT-001 §2.11),
// and coming back without re-certification would make it a pause. Promotion
// never skips a step: PRODUCTION requires the golden suites PILOT ran
// (ZTAX-CONT-REQ-0017).
func CanTransitionPack(from, to PackStatus) bool {
	if !from.Valid() || !to.Valid() || from == to || from == PackWithdrawn {
		return false
	}
	switch to {
	case PackWithdrawn:
		return true
	case PackSuspended:
		return from != PackSuspended
	}
	switch from {
	case PackResearch:
		return to == PackValidation
	case PackValidation:
		return to == PackPilot
	case PackPilot:
		return to == PackProduction
	case PackProduction:
		return to == PackManaged
	case PackManaged:
		return to == PackProduction
	case PackSuspended:
		return to == PackValidation
	}
	return false
}

// Dependency is one declared dependency: a pack and the versions of it this
// pack was certified against (ZTAX-CONT-REQ-0007).
type Dependency struct {
	Pack       PackID     `json:"packId"`
	Constraint Constraint `json:"version"`
}

// Conflict is one declared incompatibility: a pack this one must not be
// resolved alongside, at the versions given (ZTAX-CONT-REQ-0007).
type Conflict struct {
	Pack       PackID     `json:"packId"`
	Constraint Constraint `json:"version"`
}

// SourceDependency is one source a pack was derived from, as recorded in the
// signed manifest (ZTAX-SRC-REQ-0049, ZTAX-CONT-REQ-0020).
//
// RecordDigest is the canonical digest of the SourceLicenseRecord the rights
// gate evaluated, which is what makes "which rights profile was effective at
// the time of this build" answerable later (ZTAX-SRC-REQ-0083) without the
// register having to be immutable — the register changes, the digest in a
// signed manifest does not.
type SourceDependency struct {
	Source       sourcing.SourceID `json:"sourceId"`
	LicenceRef   string            `json:"licenceRef"`
	RecordDigest string            `json:"recordDigest"`
	// Uses are rights the pack needs beyond sourcing.BuildRights.
	Uses []sourcing.Right `json:"uses,omitempty"`
}

// PackManifest is the pack section of a bundle manifest (CONT-001 §3
// "PackManifest", §11's field table): what the bundle is a release of, at what
// level, depending on what, derived from which sources, for which deployments.
//
// It is signed with the rest of the manifest, because every field here is a
// claim somebody acts on — a cell, a support matrix, a licence audit — and an
// unsigned claim next to signed rules is an invitation to edit the claim.
//
// Fields from §11 that are not here yet, and why: legal_effective_window,
// golden_suite_ref and expiry/freshness need the certification machinery of
// W3 to mean anything, and precedence needs more than one pack overlapping.
// Each is an additive field when it lands.
type PackManifest struct {
	ID      PackID     `json:"packId"`
	Version Version    `json:"version"`
	Level   Level      `json:"level"`
	Status  PackStatus `json:"status"`
	// Capabilities is the support scope (ZTAX-CONT-REQ-0006).
	Capabilities []Capability `json:"capabilities"`
	// Territories are the canonical jurisdiction identifiers the pack covers.
	// Empty means the pack is not territorially bounded.
	Territories []string `json:"territories,omitempty"`
	// DeploymentModes are where the bundle may be distributed, each of which
	// the rights gate proved every source permits (ZTAX-CONT-REQ-0021).
	DeploymentModes []sourcing.DeploymentMode `json:"deploymentModes"`
	Dependencies    []Dependency              `json:"dependencies,omitempty"`
	Conflicts       []Conflict                `json:"conflicts,omitempty"`
	Sources         []SourceDependency        `json:"sources"`
	// Owner is the accountable pack owner (ZTAX-CONT-REQ-0069).
	Owner string `json:"owner,omitempty"`
	// WithdrawalPlanRef names the suspension and withdrawal procedure
	// (CONT-001 §11 withdrawal_plan; ZTAX-CONT-REQ-0040, -0041).
	WithdrawalPlanRef string `json:"withdrawalPlanRef,omitempty"`
}

// Validate checks the manifest is well-formed. It does not resolve
// dependencies — that needs the other packs, and is Resolve's job — and it
// does not evaluate rights, which needs the register and is the build gate's.
func (m PackManifest) Validate() error {
	if err := m.ID.Validate(); err != nil {
		return err
	}
	if m.Version.IsZero() {
		return errorf("pack %s has no version, or version 0.0.0", m.ID)
	}
	if !m.Level.Valid() {
		return errorf("pack %s has level %q; CONT-001 §10 defines %s", m.ID, m.Level, levelList())
	}
	if !m.Status.Valid() {
		return errorf("pack %s has status %q", m.ID, m.Status)
	}
	if len(m.Capabilities) == 0 {
		return errorf("pack %s declares no capability (ZTAX-CONT-REQ-0006)", m.ID)
	}
	for _, c := range m.Capabilities {
		if !c.Valid() {
			return errorf("pack %s declares capability %q, which is not in CONT-001 Appendix A", m.ID, c)
		}
	}
	if len(m.DeploymentModes) == 0 {
		return errorf("pack %s declares no deployment mode", m.ID)
	}
	for _, d := range m.DeploymentModes {
		if !d.Valid() {
			return errorf("pack %s declares deployment mode %q", m.ID, d)
		}
	}
	seen := map[PackID]bool{}
	for _, d := range m.Dependencies {
		if err := d.Pack.Validate(); err != nil {
			return errorf("pack %s: dependency: %v", m.ID, err)
		}
		if d.Constraint.IsZero() {
			return errorf("pack %s depends on %s with no version constraint", m.ID, d.Pack)
		}
		if d.Pack == m.ID {
			return errorf("pack %s depends on itself", m.ID)
		}
		if seen[d.Pack] {
			return errorf("pack %s declares its dependency on %s twice", m.ID, d.Pack)
		}
		seen[d.Pack] = true
	}
	for _, c := range m.Conflicts {
		if err := c.Pack.Validate(); err != nil {
			return errorf("pack %s: conflict: %v", m.ID, err)
		}
		if c.Constraint.IsZero() {
			return errorf("pack %s conflicts with %s with no version constraint", m.ID, c.Pack)
		}
		if seen[c.Pack] {
			// Depending on a pack and conflicting with it at once is
			// satisfiable only if the two ranges are disjoint, which is a
			// pinned dependency spelled confusingly. Refuse it.
			return errorf("pack %s both depends on and conflicts with %s", m.ID, c.Pack)
		}
	}
	if len(m.Sources) == 0 {
		return errorf("pack %s declares no source (ZTAX-CONT-REQ-0001, ZTAX-SRC-REQ-0049)", m.ID)
	}
	for _, s := range m.Sources {
		if s.Source == "" || s.LicenceRef == "" || s.RecordDigest == "" {
			return errorf("pack %s: a source dependency names its source, licence and record digest", m.ID)
		}
		for _, u := range s.Uses {
			if !u.Valid() {
				return errorf("pack %s: source %s: use %q is not in the SRC-001 §4 taxonomy", m.ID, s.Source, u)
			}
		}
	}
	if m.Status.Governed() && (m.Owner == "" || m.WithdrawalPlanRef == "") {
		return errorf("pack %s is %s and must name its owner and withdrawal plan (ZTAX-CONT-REQ-0040, -0041, -0069)", m.ID, m.Status)
	}
	return nil
}

// Node is the part of the manifest dependency resolution reads.
func (m PackManifest) Node() PackNode {
	return PackNode{ID: m.ID, Version: m.Version, Level: m.Level, Dependencies: m.Dependencies, Conflicts: m.Conflicts}
}

// Normalize orders every collection by its key, which is the order the
// canonical encoding requires (ADR-0011 P4: the ordering key is a property of
// the schema). None of these collections carries meaning in its order.
func (m PackManifest) Normalize() PackManifest {
	m.Capabilities = append([]Capability(nil), m.Capabilities...)
	sort.Slice(m.Capabilities, func(i, j int) bool { return m.Capabilities[i] < m.Capabilities[j] })
	m.Territories = append([]string(nil), m.Territories...)
	sort.Strings(m.Territories)
	m.DeploymentModes = append([]sourcing.DeploymentMode(nil), m.DeploymentModes...)
	sort.Slice(m.DeploymentModes, func(i, j int) bool { return m.DeploymentModes[i] < m.DeploymentModes[j] })
	m.Dependencies = append([]Dependency(nil), m.Dependencies...)
	sort.Slice(m.Dependencies, func(i, j int) bool { return m.Dependencies[i].Pack < m.Dependencies[j].Pack })
	m.Conflicts = append([]Conflict(nil), m.Conflicts...)
	sort.Slice(m.Conflicts, func(i, j int) bool { return m.Conflicts[i].Pack < m.Conflicts[j].Pack })
	srcs := make([]SourceDependency, len(m.Sources))
	for i, s := range m.Sources {
		s.Uses = append([]sourcing.Right(nil), s.Uses...)
		sort.Slice(s.Uses, func(a, b int) bool { return s.Uses[a] < s.Uses[b] })
		srcs[i] = s
	}
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].Source < srcs[j].Source })
	m.Sources = srcs
	return m
}

func levelList() string {
	parts := make([]string, len(Levels))
	for i, l := range Levels {
		parts[i] = string(l)
	}
	return strings.Join(parts, ", ")
}
