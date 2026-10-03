package jurisdiction

import (
	"fmt"
	"sort"
)

// Mode is a situs rule's resolution mode (JUR-001 §4). A jurisdiction whose
// law fits none of the three is a finding against the specification, not a
// licence to improvise a fourth here.
type Mode string

// The modes.
const (
	// ModePrecedence is an ordered list, first usable item wins (§4.2).
	ModePrecedence Mode = "PRECEDENCE"
	// ModeQuorum needs N non-contradictory items (§4.3).
	ModeQuorum Mode = "QUORUM"
	// ModeSpatial is point in polygon against a pinned dataset (§4.4).
	ModeSpatial Mode = "SPATIAL"
)

// OutsidePolicy is what a SPATIAL rule does with a point outside every
// boundary in its dataset.
type OutsidePolicy string

// The policies.
const (
	OutsideInsufficient     OutsidePolicy = "INSUFFICIENT_EVIDENCE"
	OutsideFallBackToParent OutsidePolicy = "FALL_BACK_TO_PARENT"
)

// RoamingSourcing is where a roaming transaction is sourced (JUR-001 §6.2).
// Declared per pack; the runtime has no default (JUR-REQ-0018).
type RoamingSourcing string

// The roaming sourcing declarations.
const (
	RoamingHome    RoamingSourcing = "HOME"
	RoamingVisited RoamingSourcing = "VISITED"
	RoamingBoth    RoamingSourcing = "BOTH"
)

// CustomerRole is evidence-driven and has three values, not two
// (JUR-001 §7.2, JUR-REQ-0020).
type CustomerRole string

// The roles.
const (
	RoleB2B          CustomerRole = "B2B"
	RoleB2C          CustomerRole = "B2C"
	RoleUndetermined CustomerRole = "UNDETERMINED"
)

// Valid reports whether r is a known role.
func (r CustomerRole) Valid() bool { return r == RoleB2B || r == RoleB2C || r == RoleUndetermined }

// SitusRule is content: one mode and that mode's parameters.
type SitusRule struct {
	ID   string
	Mode Mode
	// Reliability assigns a class to every evidence type the rule reads
	// (JUR-REQ-0007). A type the rule reads and does not classify is refused
	// when the rule set is built.
	Reliability map[EvidenceType]ReliabilityClass

	// PRECEDENCE.
	Order              []EvidenceType
	MinimumReliability ReliabilityClass

	// QUORUM.
	Required       int
	Eligible       []EvidenceType
	AgreementLevel Level

	// SPATIAL.
	CoordinateSource []EvidenceType
	Dataset          string // id@version, pinned (JUR-REQ-0003)
	OnOutside        OutsidePolicy
	Parent           ID

	// Roaming is this rule's roaming sourcing, or empty if the rule's content
	// does not cover roaming at all.
	Roaming RoamingSourcing
}

// reads returns the evidence types the rule reads.
func (r SitusRule) reads() []EvidenceType {
	switch r.Mode {
	case ModePrecedence:
		return r.Order
	case ModeQuorum:
		return r.Eligible
	case ModeSpatial:
		return r.CoordinateSource
	}
	return nil
}

func (r SitusRule) validate() error {
	if r.ID == "" {
		return fmt.Errorf("jurisdiction: situs rule has no id")
	}
	types := r.reads()
	if len(types) == 0 {
		return fmt.Errorf("jurisdiction: situs rule %s (%s) reads no evidence", r.ID, r.Mode)
	}
	seen := map[EvidenceType]bool{}
	for _, t := range types {
		if !t.Valid() {
			// JUR-REQ-0006: an unknown type refuses the bundle.
			return fmt.Errorf("jurisdiction: situs rule %s reads evidence type %q, which is not in the closed vocabulary", r.ID, t)
		}
		if seen[t] {
			return fmt.Errorf("jurisdiction: situs rule %s names %s twice", r.ID, t)
		}
		seen[t] = true
		if c, ok := r.Reliability[t]; !ok || !c.Valid() {
			return fmt.Errorf("jurisdiction: situs rule %s reads %s and assigns it no reliability", r.ID, t)
		}
	}
	switch r.Mode {
	case ModePrecedence:
		if !r.MinimumReliability.Valid() {
			return fmt.Errorf("jurisdiction: precedence rule %s has minimum reliability %q", r.ID, r.MinimumReliability)
		}
	case ModeQuorum:
		if r.Required < 1 {
			return fmt.Errorf("jurisdiction: quorum rule %s requires %d items", r.ID, r.Required)
		}
		if !r.AgreementLevel.Valid() {
			// JUR-REQ-0012: "non-contradictory" means nothing until the level
			// at which agreement is judged is named.
			return fmt.Errorf("jurisdiction: quorum rule %s declares no agreement level", r.ID)
		}
	case ModeSpatial:
		for _, t := range types {
			if t != EvidenceDeviceCoordinates && t != EvidenceNetworkLocation {
				return fmt.Errorf("jurisdiction: spatial rule %s reads %s, which carries no coordinates", r.ID, t)
			}
		}
		if r.Dataset == "" {
			return fmt.Errorf("jurisdiction: spatial rule %s pins no boundary dataset", r.ID)
		}
		switch r.OnOutside {
		case OutsideInsufficient:
		case OutsideFallBackToParent:
			if err := r.Parent.Validate(); err != nil {
				return fmt.Errorf("jurisdiction: spatial rule %s falls back to a parent it does not name: %w", r.ID, err)
			}
		default:
			return fmt.Errorf("jurisdiction: spatial rule %s has outside policy %q", r.ID, r.OnOutside)
		}
	default:
		return fmt.Errorf("jurisdiction: situs rule %s has mode %q", r.ID, r.Mode)
	}
	switch r.Roaming {
	case "", RoamingHome, RoamingVisited, RoamingBoth:
	default:
		return fmt.Errorf("jurisdiction: situs rule %s has roaming sourcing %q", r.ID, r.Roaming)
	}
	return nil
}

// Binding selects a rule by ontology class and customer role. An empty field
// matches anything; specificity is the number of fields set.
type Binding struct {
	Ontology string
	Role     CustomerRole
	Rule     SitusRule
}

func (b Binding) specificity() int {
	n := 0
	if b.Ontology != "" {
		n++
	}
	if b.Role != "" {
		n++
	}
	return n
}

func (b Binding) matches(ontology string, role CustomerRole) bool {
	return (b.Ontology == "" || b.Ontology == ontology) && (b.Role == "" || b.Role == role)
}

// overlaps reports whether some selection matches both bindings.
func (b Binding) overlaps(o Binding) bool {
	return (b.Ontology == "" || o.Ontology == "" || b.Ontology == o.Ontology) &&
		(b.Role == "" || o.Role == "" || b.Role == o.Role)
}

// RuleSet is a pack's situs rules and its declared treatment of an
// undetermined customer.
type RuleSet struct {
	bindings       []Binding
	undeterminedAs CustomerRole
}

// NewRuleSet validates a pack's situs rules.
//
// Two bindings that could both match one selection at the same specificity
// refuse the set (JUR-001 §4.5.2, JUR-REQ-0014). Runtime never breaks the tie.
//
// undeterminedAs is how the pack treats a customer whose role the evidence
// does not settle. It is required: "treat as B2C" is the common answer, and
// JUR-001 §7.2 requires content to state it rather than the runtime to assume
// it (JUR-REQ-0020). RoleUndetermined is a valid answer — the rules then
// select on UNDETERMINED as a role of its own.
func NewRuleSet(bindings []Binding, undeterminedAs CustomerRole) (*RuleSet, error) {
	if !undeterminedAs.Valid() {
		return nil, fmt.Errorf("jurisdiction: the rule set does not state how an UNDETERMINED customer is treated")
	}
	ids := map[string]bool{}
	for i, b := range bindings {
		if b.Role != "" && !b.Role.Valid() {
			return nil, fmt.Errorf("jurisdiction: binding %d selects role %q", i, b.Role)
		}
		if err := b.Rule.validate(); err != nil {
			return nil, err
		}
		if ids[b.Rule.ID] {
			return nil, fmt.Errorf("jurisdiction: situs rule %s is declared twice", b.Rule.ID)
		}
		ids[b.Rule.ID] = true
		for j := 0; j < i; j++ {
			o := bindings[j]
			if b.specificity() == o.specificity() && b.overlaps(o) {
				return nil, fmt.Errorf("jurisdiction: situs rules %s and %s select at equal specificity for the same transactions; the bundle is refused rather than the tie broken at runtime",
					o.Rule.ID, b.Rule.ID)
			}
		}
	}
	bs := append([]Binding(nil), bindings...)
	sort.SliceStable(bs, func(i, j int) bool { return bs[i].specificity() > bs[j].specificity() })
	return &RuleSet{bindings: bs, undeterminedAs: undeterminedAs}, nil
}

// Select returns the most specific rule matching the selection. NewRuleSet
// has already guaranteed that at most one binding matches at that
// specificity.
func (rs *RuleSet) Select(ontology string, role CustomerRole) (SitusRule, bool) {
	for _, b := range rs.bindings {
		if b.matches(ontology, role) {
			return b.Rule, true
		}
	}
	return SitusRule{}, false
}

// UndeterminedAs returns the pack's declared treatment of an undetermined
// customer.
func (rs *RuleSet) UndeterminedAs() CustomerRole { return rs.undeterminedAs }
