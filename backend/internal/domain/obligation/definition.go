package obligation

import (
	"fmt"
	"sort"
)

// DutyKind is what an obligation definition obliges (ZTAX-OBL-REQ-0031 to
// -0036). Several are non-monetary, and a non-monetary duty is represented as
// itself rather than as a zero-amount tax line (ZTAX-OBL-REQ-0141).
type DutyKind string

// The duty kinds.
const (
	DutyTransactionMonetary  DutyKind = "TRANSACTION_MONETARY"
	DutyPeriodicContribution DutyKind = "PERIODIC_CONTRIBUTION"
	DutyRegistration         DutyKind = "REGISTRATION"
	DutyInformationReturn    DutyKind = "INFORMATION_RETURN"
	DutyRecordkeeping        DutyKind = "RECORDKEEPING"
	DutyNoticeResponse       DutyKind = "NOTICE_RESPONSE"
)

// Monetary reports whether the duty carries an amount.
func (k DutyKind) Monetary() bool {
	return k == DutyTransactionMonetary || k == DutyPeriodicContribution
}

func (k DutyKind) valid() bool {
	switch k {
	case DutyTransactionMonetary, DutyPeriodicContribution, DutyRegistration,
		DutyInformationReturn, DutyRecordkeeping, DutyNoticeResponse:
		return true
	}
	return false
}

// ClassificationBasis is which classification an obligation's basis reads.
// Taxability and regulatory revenue are separate classifications, and a
// regulatory contribution reads the latter; taxability is never its proxy
// (ZTAX-OBL-REQ-0104, -0105).
type ClassificationBasis string

// The bases.
const (
	ClassifiedByTaxability        ClassificationBasis = "TAXABILITY"
	ClassifiedByRegulatoryRevenue ClassificationBasis = "REGULATORY_REVENUE"
	ClassifiedByNone              ClassificationBasis = "NONE"
)

// Basis is what an obligation is measured on, and in what unit
// (ZTAX-OBL-REQ-0025).
type Basis struct {
	// Kind names the measure: "TAX_COLLECTED", "ASSESSABLE_REVENUE", "COUNT".
	// It is content vocabulary.
	Kind string
	// Unit is a currency code for a monetary basis, or a unit name otherwise.
	Unit           string
	Classification ClassificationBasis
}

// TriggerKind is the closed set of trigger types (ZTAX-OBL-REQ-0050).
type TriggerKind string

// The trigger kinds, and the three composites.
const (
	TriggerEvent           TriggerKind = "EVENT"
	TriggerActivity        TriggerKind = "ACTIVITY"
	TriggerThreshold       TriggerKind = "THRESHOLD"
	TriggerPeriod          TriggerKind = "PERIOD"
	TriggerRegistration    TriggerKind = "REGISTRATION"
	TriggerAuthorityDemand TriggerKind = "AUTHORITY_DEMAND"
	TriggerDependency      TriggerKind = "DEPENDENCY"
	TriggerChange          TriggerKind = "CHANGE"
	TriggerAll             TriggerKind = "ALL"
	TriggerAny             TriggerKind = "ANY"
	TriggerNot             TriggerKind = "NOT"
	// TriggerRef names another trigger in the definition set, so composites
	// can share sub-triggers. Refs are what make a cycle possible, and
	// ValidateTriggers is what makes one impossible.
	TriggerRef TriggerKind = "REF"
)

// Trigger is a trigger expression. Leaf kinds read one fact by name; the
// composites combine children; REF names a trigger declared in the set.
type Trigger struct {
	Kind     TriggerKind
	Fact     string
	Children []Trigger
	Ref      string
}

// Truth is three-valued: a trigger whose fact is unknown is not false
// (ZTAX-OBL-REQ-0052).
type Truth string

// The truth values.
const (
	True    Truth = "TRUE"
	False   Truth = "FALSE"
	Unknown Truth = "UNKNOWN"
)

// Facts are the authoritative business facts a trigger reads. A fact absent
// from the map is unknown, which is different from a fact recorded as false.
type Facts map[string]bool

// Evaluate evaluates a trigger deterministically (ZTAX-OBL-REQ-0049) under
// Kleene logic: ALL is false if any child is false, ANY true if any child is
// true, and otherwise an unknown child keeps the result unknown.
func (t Trigger) Evaluate(facts Facts, set map[string]Trigger) Truth {
	switch t.Kind {
	case TriggerAll:
		out := True
		for _, c := range t.Children {
			switch c.Evaluate(facts, set) {
			case False:
				return False
			case Unknown:
				out = Unknown
			}
		}
		return out
	case TriggerAny:
		out := False
		for _, c := range t.Children {
			switch c.Evaluate(facts, set) {
			case True:
				return True
			case Unknown:
				out = Unknown
			}
		}
		return out
	case TriggerNot:
		switch t.Children[0].Evaluate(facts, set) {
		case True:
			return False
		case False:
			return True
		}
		return Unknown
	case TriggerRef:
		return set[t.Ref].Evaluate(facts, set)
	}
	v, ok := facts[t.Fact]
	if !ok {
		return Unknown
	}
	if v {
		return True
	}
	return False
}

func (t Trigger) validate(set map[string]Trigger) error {
	switch t.Kind {
	case TriggerAll, TriggerAny:
		if len(t.Children) == 0 {
			return fmt.Errorf("obligation: %s trigger has no children", t.Kind)
		}
	case TriggerNot:
		if len(t.Children) != 1 {
			return fmt.Errorf("obligation: NOT trigger has %d children; it takes one", len(t.Children))
		}
	case TriggerRef:
		if _, ok := set[t.Ref]; !ok {
			return fmt.Errorf("obligation: trigger references %q, which the set does not declare", t.Ref)
		}
		return nil
	case TriggerEvent, TriggerActivity, TriggerThreshold, TriggerPeriod, TriggerRegistration,
		TriggerAuthorityDemand, TriggerDependency, TriggerChange:
		if t.Fact == "" {
			return fmt.Errorf("obligation: %s trigger reads no fact", t.Kind)
		}
		return nil
	default:
		return fmt.Errorf("obligation: trigger kind %q is not in the closed set", t.Kind)
	}
	for _, c := range t.Children {
		if err := c.validate(set); err != nil {
			return err
		}
	}
	return nil
}

func (t Trigger) refs(into map[string]bool) {
	if t.Kind == TriggerRef {
		into[t.Ref] = true
	}
	for _, c := range t.Children {
		c.refs(into)
	}
}

// ValidateTriggers schema-validates a named trigger set and refuses a cycle
// through REF (ZTAX-OBL-REQ-0051). Validation is at build: an evaluation
// never meets a trigger it could loop on.
func ValidateTriggers(set map[string]Trigger) error {
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := set[n].validate(set); err != nil {
			return fmt.Errorf("obligation: trigger %s: %w", n, err)
		}
	}
	const (
		white = 0
		grey  = 1
		black = 2
	)
	colour := map[string]int{}
	var visit func(string) error
	visit = func(n string) error {
		switch colour[n] {
		case grey:
			return fmt.Errorf("obligation: trigger %s is part of a reference cycle", n)
		case black:
			return nil
		}
		colour[n] = grey
		deps := map[string]bool{}
		set[n].refs(deps)
		keys := make([]string, 0, len(deps))
		for k := range deps {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, d := range keys {
			if err := visit(d); err != nil {
				return err
			}
		}
		colour[n] = black
		return nil
	}
	for _, n := range names {
		if err := visit(n); err != nil {
			return err
		}
	}
	return nil
}

// AmendmentTreatment is how a late change to a closed period is handled
// (ZTAX-OBL-REQ-0095 to -0097).
type AmendmentTreatment string

// The treatments.
const (
	AmendCurrentPeriod AmendmentTreatment = "CURRENT_PERIOD_ADJUSTMENT"
	AmendPriorPeriod   AmendmentTreatment = "PRIOR_PERIOD_AMENDMENT"
	AmendLaterTrueUp   AmendmentTreatment = "LATER_PERIOD_TRUE_UP"
)

// Provenance is a definition's source and legal provenance
// (ZTAX-OBL-REQ-0030).
type Provenance struct {
	Content  ContentRef
	Source   string
	Citation string
}

// DependencyKind types an edge between definitions (ZTAX-OBL-REQ-0100).
type DependencyKind string

// The dependency kinds.
const (
	// DependsActivatedBy is a filing or payment duty that exists only once a
	// registration is active.
	DependsActivatedBy DependencyKind = "ACTIVATED_BY"
	// DependsRequiresFiled cannot be met until the prerequisite is filed.
	DependsRequiresFiled DependencyKind = "REQUIRES_FILED"
)

// Dependency is a typed edge to a prerequisite definition.
type Dependency struct {
	Kind DependencyKind
	On   string
}

// Definition is ObligationDefinition: versioned content, never a hard-coded
// jurisdiction branch (ZTAX-OBL-REQ-0021).
type Definition struct {
	ID      string
	Version string
	// Authority and Jurisdiction identify who the duty is owed to and where
	// (ZTAX-OBL-REQ-0022).
	Authority    string
	Jurisdiction string
	Duty         DutyKind
	// Trigger names a trigger in the definition set (ZTAX-OBL-REQ-0023).
	Trigger string
	// ResponsibilityRule names the rule that decides the five roles
	// (ZTAX-OBL-REQ-0024).
	ResponsibilityRule string
	Basis              Basis
	Period             PeriodRule
	Due                DueDateRule
	// Threshold is set when the duty has a threshold or de-minimis rule
	// (ZTAX-OBL-REQ-0028).
	Threshold *Threshold
	// Amendment lists the permitted treatments in preference order
	// (ZTAX-OBL-REQ-0029). Empty means no treatment is approved, and a late
	// change routes to review.
	Amendment    []AmendmentTreatment
	Dependencies []Dependency
	Provenance   Provenance
}

// Validate applies the field requirements of ZTAX-OBL-REQ-0022 to -0030.
func (d Definition) Validate() error {
	switch {
	case d.ID == "" || d.Version == "":
		return fmt.Errorf("obligation: definition has no id or no version")
	case d.Authority == "" || d.Jurisdiction == "":
		return fmt.Errorf("obligation: definition %s names no authority or no jurisdiction", d.ID)
	case !d.Duty.valid():
		return fmt.Errorf("obligation: definition %s has duty %q", d.ID, d.Duty)
	case d.Trigger == "":
		return fmt.Errorf("obligation: definition %s names no trigger rule", d.ID)
	case d.ResponsibilityRule == "":
		return fmt.Errorf("obligation: definition %s names no responsibility rule", d.ID)
	case d.Provenance.Source == "" || d.Provenance.Citation == "":
		return fmt.Errorf("obligation: definition %s carries no source or citation", d.ID)
	}
	if err := d.Provenance.Content.Validate(); err != nil {
		return fmt.Errorf("obligation: definition %s: %w", d.ID, err)
	}
	if d.Duty.Monetary() {
		if d.Basis.Kind == "" || d.Basis.Unit == "" {
			return fmt.Errorf("obligation: monetary definition %s names no basis or no unit", d.ID)
		}
	}
	switch d.Basis.Classification {
	case ClassifiedByTaxability, ClassifiedByRegulatoryRevenue, ClassifiedByNone:
	default:
		return fmt.Errorf("obligation: definition %s's basis reads classification %q", d.ID, d.Basis.Classification)
	}
	if d.Duty == DutyPeriodicContribution && d.Basis.Classification != ClassifiedByRegulatoryRevenue {
		return fmt.Errorf("obligation: contribution %s's basis reads %s; a regulatory contribution reads regulatory-revenue classification",
			d.ID, d.Basis.Classification)
	}
	if err := d.Period.Validate(); err != nil {
		return fmt.Errorf("obligation: definition %s: %w", d.ID, err)
	}
	if err := d.Due.Validate(); err != nil {
		return fmt.Errorf("obligation: definition %s: %w", d.ID, err)
	}
	if d.Threshold != nil {
		if err := d.Threshold.Validate(); err != nil {
			return fmt.Errorf("obligation: definition %s: %w", d.ID, err)
		}
	}
	for _, a := range d.Amendment {
		switch a {
		case AmendCurrentPeriod, AmendPriorPeriod, AmendLaterTrueUp:
		default:
			return fmt.Errorf("obligation: definition %s permits amendment treatment %q", d.ID, a)
		}
	}
	return nil
}

// DefinitionSet is a pack's definitions and triggers, validated together.
type DefinitionSet struct {
	defs     map[string]Definition
	triggers map[string]Trigger
}

// NewDefinitionSet validates each definition, the trigger set, and the
// dependency graph, which must be acyclic (ZTAX-OBL-REQ-0099).
func NewDefinitionSet(defs []Definition, triggers map[string]Trigger) (*DefinitionSet, error) {
	if err := ValidateTriggers(triggers); err != nil {
		return nil, err
	}
	s := &DefinitionSet{defs: map[string]Definition{}, triggers: triggers}
	for _, d := range defs {
		if err := d.Validate(); err != nil {
			return nil, err
		}
		if _, dup := s.defs[d.ID]; dup {
			return nil, fmt.Errorf("obligation: definition %s is declared twice", d.ID)
		}
		if _, ok := triggers[d.Trigger]; !ok {
			return nil, fmt.Errorf("obligation: definition %s names trigger %s, which the set does not declare", d.ID, d.Trigger)
		}
		s.defs[d.ID] = d
	}
	for _, d := range s.defs {
		for _, dep := range d.Dependencies {
			on, ok := s.defs[dep.On]
			if !ok {
				return nil, fmt.Errorf("obligation: %s depends on %s, which the set does not declare", d.ID, dep.On)
			}
			if dep.Kind == DependsActivatedBy && on.Duty != DutyRegistration {
				return nil, fmt.Errorf("obligation: %s is activated by %s, which is not a registration", d.ID, dep.On)
			}
			if dep.Kind != DependsActivatedBy && dep.Kind != DependsRequiresFiled {
				return nil, fmt.Errorf("obligation: %s has dependency kind %q", d.ID, dep.Kind)
			}
		}
	}
	if err := s.acyclic(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *DefinitionSet) acyclic() error {
	ids := make([]string, 0, len(s.defs))
	for id := range s.defs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	state := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("obligation: definition %s is on a dependency cycle", id)
		case 2:
			return nil
		}
		state[id] = 1
		for _, dep := range s.defs[id].Dependencies {
			if err := visit(dep.On); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for _, id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

// Definition returns one definition.
func (s *DefinitionSet) Definition(id string) (Definition, bool) {
	d, ok := s.defs[id]
	return d, ok
}

// Triggered evaluates a definition's trigger. UNKNOWN is returned as itself
// and becomes DATA_REQUIRED on the obligation, never a quiet "not triggered".
func (s *DefinitionSet) Triggered(defID string, facts Facts) (Truth, error) {
	d, ok := s.defs[defID]
	if !ok {
		return Unknown, fmt.Errorf("obligation: no definition %s", defID)
	}
	return s.triggers[d.Trigger].Evaluate(facts, s.triggers), nil
}

// Activated returns the definitions a registration activates, sorted, so an
// activated registration can seed its recurring obligations
// (ZTAX-OBL-REQ-0076).
func (s *DefinitionSet) Activated(registrationDef string) []Definition {
	var out []Definition
	for _, d := range s.defs {
		for _, dep := range d.Dependencies {
			if dep.Kind == DependsActivatedBy && dep.On == registrationDef {
				out = append(out, d)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// PrerequisitesMet checks a dependent obligation against its prerequisites'
// recorded state, not the order events arrived in (ZTAX-OBL-REQ-0101).
// registrations maps a registration definition to its current state;
// statuses maps any other prerequisite definition to its obligation status.
func (s *DefinitionSet) PrerequisitesMet(defID string, registrations map[string]RegistrationState, statuses map[string]Status) (bool, []string) {
	d, ok := s.defs[defID]
	if !ok {
		return false, []string{"no definition " + defID}
	}
	var missing []string
	for _, dep := range d.Dependencies {
		switch dep.Kind {
		case DependsActivatedBy:
			if registrations[dep.On] != RegistrationRegistered {
				missing = append(missing, fmt.Sprintf("%s is %s, not REGISTERED", dep.On, orUnknown(string(registrations[dep.On]))))
			}
		case DependsRequiresFiled:
			switch statuses[dep.On] {
			case StatusFiled, StatusAccepted, StatusPaymentDue, StatusPaid, StatusClosed:
			default:
				missing = append(missing, fmt.Sprintf("%s is %s, not filed", dep.On, orUnknown(string(statuses[dep.On]))))
			}
		}
	}
	return len(missing) == 0, missing
}

func orUnknown(s string) string {
	if s == "" {
		return "UNKNOWN"
	}
	return s
}
