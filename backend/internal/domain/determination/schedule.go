// Package determination is the component-level arithmetic of ZTAX-DET-001:
// what tax is due on a document, on what base, at what rate, rounded where.
//
// It sits above the rule IR's node DAG (DET-001 §15 item 5). The IR answers
// "which components apply and with what parameters"; this package answers
// "given those components, what are the amounts", and it is where the four
// pieces of DET-001 that are easy to get subtly wrong live:
//
//   - the component DAG and its deterministic order (§5),
//   - the exclusive calculation that rounds exactly once (§6),
//   - inclusive extraction that is exact to the minor unit (§7),
//   - rounding points coarser than the line, allocated back (§8).
//
// No tax logic is Go (DET-001 §0.2). Every rate, base, ordering and policy
// here arrives as a Schedule built from a signed content bundle; the package
// holds no jurisdiction, no rate and no default. It is also pure: no clock, no
// I/O, no randomness (DET-REQ-0035), so the same Schedule and Document give a
// byte-identical Determination on every replay.
package determination

import (
	"fmt"
	"sort"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
)

// MaxComponentIDLength bounds a component identifier.
const MaxComponentIDLength = 128

// ComponentID is a component's stable content identifier. It is the
// deterministic tie-break of DET-001 §5.4 and §7.6, so it is compared as a
// byte string and never normalised.
type ComponentID string

// Validate applies the grammar: non-empty printable ASCII with no whitespace.
func (c ComponentID) Validate() error {
	if c == "" {
		return fmt.Errorf("determination: empty component id")
	}
	if len(c) > MaxComponentIDLength {
		return fmt.Errorf("determination: component id %q is longer than %d", c, MaxComponentIDLength)
	}
	for _, r := range c {
		if r <= ' ' || r > '~' {
			return fmt.Errorf("determination: component id %q holds %q", c, r)
		}
	}
	return nil
}

// BaseKind is what a taxable base is a base of (DET-001 §3.2). Closed set.
type BaseKind string

// The base kinds.
const (
	// BaseLineNet is the line's net amount before any tax.
	BaseLineNet BaseKind = "LINE_NET"
	// BaseLineGross is the line's net plus its inclusive taxes: the stated
	// price of an inclusive line, and the net of a line with none. It never
	// means "net plus whatever happened to be evaluated first", which is the
	// order dependence DET-001 §3.3 forbids.
	BaseLineGross BaseKind = "LINE_GROSS"
	// BaseComponentSum is the sum of named components — tax on tax.
	BaseComponentSum BaseKind = "COMPONENT_SUM"
	// BaseNetPlusComponents is the line net plus named components.
	BaseNetPlusComponents BaseKind = "NET_PLUS_COMPONENTS"
	// BaseQuantity is a quantity, for SPECIFIC per-unit charges.
	BaseQuantity BaseKind = "QUANTITY"
	// BaseDocumentNet is the document's net total, for a tax assessed once per
	// document and allocated back to its lines.
	BaseDocumentNet BaseKind = "DOCUMENT_NET"
)

func (k BaseKind) valid() bool {
	switch k {
	case BaseLineNet, BaseLineGross, BaseComponentSum, BaseNetPlusComponents, BaseQuantity, BaseDocumentNet:
		return true
	}
	return false
}

// compounding reports whether the base names other components.
func (k BaseKind) compounding() bool {
	return k == BaseComponentSum || k == BaseNetPlusComponents
}

// BaseAdjustmentKind is one step of a declared base-adjustment sequence.
type BaseAdjustmentKind string

// The adjustment kinds. Exemptions, deductions, allowances and discounts all
// reduce to one of these two; what distinguishes them legally is their name
// and their position in the sequence, both of which content declares.
const (
	// AdjustSubtractAmount subtracts a fixed amount from the base.
	AdjustSubtractAmount BaseAdjustmentKind = "SUBTRACT_AMOUNT"
	// AdjustSubtractProportion subtracts a proportion of the base as it stands
	// at that step.
	AdjustSubtractProportion BaseAdjustmentKind = "SUBTRACT_PROPORTION"
)

// BaseAdjustment is one step in the order DET-001 §3.4 requires content to
// declare. The order is the slice order; there is no default order because
// the steps do not commute.
type BaseAdjustment struct {
	Kind       BaseAdjustmentKind
	Name       string
	Amount     *fiscal.Money
	Proportion *fiscal.Rate
}

// Base is a taxable base declaration (DET-001 §3).
type Base struct {
	Kind BaseKind
	// Components names the components a compounding base includes, by
	// identifier, explicitly (DET-REQ-0004).
	Components []ComponentID
	// Adjustments apply in slice order (DET-REQ-0005).
	Adjustments []BaseAdjustment
	// AllowNegative permits adjustments to take the base below zero. Without
	// it the base floors at zero and the trace says so (DET-REQ-0006).
	AllowNegative bool
}

// RateKind is how a component's rate applies (DET-001 §4.1). Closed set.
type RateKind string

// The rate kinds.
const (
	RateAdValorem RateKind = "AD_VALOREM"
	RateSpecific  RateKind = "SPECIFIC"
	RateFlat      RateKind = "FLAT"
	RateBracket   RateKind = "BRACKET"
)

// BracketMode is how a tiered schedule applies (DET-001 §4.4). It has no
// default, and a schedule without one does not compile (DET-REQ-0009).
type BracketMode string

// The bracket modes.
const (
	// BracketMarginal applies each tier's rate to the portion within the tier.
	BracketMarginal BracketMode = "MARGINAL"
	// BracketCliff applies the highest reached tier's rate to the whole base.
	BracketCliff BracketMode = "CLIFF"
)

// Tier is one step of a bracket schedule: from From upward, Rate applies.
// Tiers are lower bounds, so contiguity is structural; what Compile checks is
// that they start at zero and strictly ascend (DET-REQ-0010).
type Tier struct {
	From fiscal.Money
	Rate fiscal.Rate
}

// Bracket is a tiered rate schedule.
type Bracket struct {
	Mode  BracketMode
	Tiers []Tier
}

// Rate is a component's rate declaration. Exactly the field its Kind names is
// set. An AD_VALOREM proportion is a fiscal.Rate, which cannot exist without a
// basis, so DET-REQ-0007 holds by construction.
type Rate struct {
	Kind       RateKind
	Proportion *fiscal.Rate
	PerUnit    *fiscal.Money
	Flat       *fiscal.Money
	Bracket    *Bracket
}

// Component is DET-001 §5.1's TaxComponent: one tax, in one jurisdiction, at
// one rate, on one base.
type Component struct {
	ID             ComponentID
	JurisdictionID string
	TaxType        string
	Base           Base
	Rate           Rate
	// Policy is required; a zero RoundingPolicy has no mode and refuses
	// compilation (DET-REQ-0008).
	Policy    fiscal.RoundingPolicy
	Inclusive bool
}

// Schedule is the set of components a bundle declares for one transaction
// shape, plus the two declarations inclusive extraction needs.
type Schedule struct {
	Components []Component
	// ExtractionPolicy is the rounding of N := G / M (DET-001 §7.4 step 3).
	// Required when any component is inclusive.
	ExtractionPolicy *fiscal.RoundingPolicy
	// InclusiveSubtractionOrder is the order in which inclusive FLAT and
	// SPECIFIC amounts leave the gross before extraction (DET-001 §7.3.4).
	// It names every such component and nothing else.
	InclusiveSubtractionOrder []ComponentID
}

// Plan is a compiled Schedule: validated, with the evaluation order fixed.
// It is immutable after Compile, so one Plan may evaluate any number of
// documents concurrently.
type Plan struct {
	components []Component
	index      map[ComponentID]int
	// order is the topological order, ties on ComponentID (DET-REQ-0012).
	order            []int
	extraction       *fiscal.RoundingPolicy
	inclusiveFlat    []ComponentID
	referencedByBase map[ComponentID]bool
}

// Order returns the component identifiers in evaluation order.
func (p *Plan) Order() []ComponentID {
	out := make([]ComponentID, len(p.order))
	for i, idx := range p.order {
		out[i] = p.components[idx].ID
	}
	return out
}

// Compile validates a Schedule and fixes its evaluation order. Every check
// DET-001 places "at bundle build" is here, so a Plan that exists is a Plan
// whose content is coherent and the evaluator never has to ask.
func Compile(s Schedule) (*Plan, error) {
	if len(s.Components) == 0 {
		return nil, fmt.Errorf("determination: schedule declares no components")
	}
	p := &Plan{
		components:       append([]Component(nil), s.Components...),
		index:            make(map[ComponentID]int, len(s.Components)),
		extraction:       s.ExtractionPolicy,
		referencedByBase: map[ComponentID]bool{},
	}
	for i, c := range p.components {
		if err := c.ID.Validate(); err != nil {
			return nil, err
		}
		if _, dup := p.index[c.ID]; dup {
			return nil, fmt.Errorf("determination: component %s is declared twice", c.ID)
		}
		p.index[c.ID] = i
	}
	for _, c := range p.components {
		if err := validateComponent(c); err != nil {
			return nil, err
		}
		for _, ref := range c.Base.Components {
			if _, ok := p.index[ref]; !ok {
				return nil, fmt.Errorf("determination: component %s's base names %s, which the schedule does not declare", c.ID, ref)
			}
			if ref == c.ID {
				return nil, fmt.Errorf("determination: component %s's base names itself", c.ID)
			}
			p.referencedByBase[ref] = true
		}
	}
	if err := p.validateCoarseReferences(); err != nil {
		return nil, err
	}
	if err := p.validatePoolPolicies(); err != nil {
		return nil, err
	}
	order, err := p.topologicalOrder()
	if err != nil {
		return nil, err
	}
	p.order = order
	if err := p.validateInclusive(s.InclusiveSubtractionOrder); err != nil {
		return nil, err
	}
	return p, nil
}

func validateComponent(c Component) error {
	if c.JurisdictionID == "" {
		return fmt.Errorf("determination: component %s names no jurisdiction", c.ID)
	}
	if c.TaxType == "" {
		return fmt.Errorf("determination: component %s names no tax type", c.ID)
	}
	if c.Policy.Mode() == "" {
		return fmt.Errorf("determination: component %s carries no rounding policy", c.ID)
	}
	if !c.Base.Kind.valid() {
		return fmt.Errorf("determination: component %s has base kind %q", c.ID, c.Base.Kind)
	}
	if c.Base.Kind.compounding() {
		if len(c.Base.Components) == 0 {
			return fmt.Errorf("determination: component %s's %s base names no components", c.ID, c.Base.Kind)
		}
		seen := map[ComponentID]bool{}
		for _, ref := range c.Base.Components {
			if seen[ref] {
				return fmt.Errorf("determination: component %s's base names %s twice", c.ID, ref)
			}
			seen[ref] = true
		}
	} else if len(c.Base.Components) > 0 {
		return fmt.Errorf("determination: component %s's %s base names components; only a compounding base may", c.ID, c.Base.Kind)
	}
	for i, a := range c.Base.Adjustments {
		switch a.Kind {
		case AdjustSubtractAmount:
			if a.Amount == nil || a.Proportion != nil {
				return fmt.Errorf("determination: component %s adjustment %d must carry exactly an amount", c.ID, i)
			}
		case AdjustSubtractProportion:
			if a.Proportion == nil || a.Amount != nil {
				return fmt.Errorf("determination: component %s adjustment %d must carry exactly a proportion", c.ID, i)
			}
		default:
			return fmt.Errorf("determination: component %s adjustment %d has kind %q", c.ID, i, a.Kind)
		}
	}
	if c.Base.Kind == BaseDocumentNet {
		if c.Inclusive {
			return fmt.Errorf("determination: component %s is inclusive on a document base; extraction is per line", c.ID)
		}
		if c.Policy.Basis() != fiscal.BasisTaxComponent {
			return fmt.Errorf("determination: component %s on a document base must round at %s", c.ID, fiscal.BasisTaxComponent)
		}
	}
	return validateRate(c)
}

func validateRate(c Component) error {
	r := c.Rate
	set := 0
	for _, present := range []bool{r.Proportion != nil, r.PerUnit != nil, r.Flat != nil, r.Bracket != nil} {
		if present {
			set++
		}
	}
	if set != 1 {
		return fmt.Errorf("determination: component %s's rate must carry exactly one value, has %d", c.ID, set)
	}
	switch r.Kind {
	case RateAdValorem:
		if r.Proportion == nil {
			return fmt.Errorf("determination: component %s is AD_VALOREM with no proportion", c.ID)
		}
		if c.Base.Kind == BaseQuantity {
			return fmt.Errorf("determination: component %s applies a proportion to a quantity", c.ID)
		}
	case RateSpecific:
		if r.PerUnit == nil {
			return fmt.Errorf("determination: component %s is SPECIFIC with no per-unit amount", c.ID)
		}
		if c.Base.Kind != BaseQuantity {
			return fmt.Errorf("determination: component %s is SPECIFIC on a %s base; it needs QUANTITY", c.ID, c.Base.Kind)
		}
	case RateFlat:
		if r.Flat == nil {
			return fmt.Errorf("determination: component %s is FLAT with no amount", c.ID)
		}
	case RateBracket:
		if r.Bracket == nil {
			return fmt.Errorf("determination: component %s is BRACKET with no schedule", c.ID)
		}
		return validateBracket(c.ID, *r.Bracket)
	default:
		return fmt.Errorf("determination: component %s has rate kind %q", c.ID, r.Kind)
	}
	return nil
}

func validateBracket(id ComponentID, b Bracket) error {
	if b.Mode != BracketMarginal && b.Mode != BracketCliff {
		// DET-REQ-0009: the two modes differ materially and neither is a
		// default. A schedule that does not say which is not a schedule.
		return fmt.Errorf("determination: component %s's bracket schedule declares mode %q; it must be MARGINAL or CLIFF", id, b.Mode)
	}
	if len(b.Tiers) == 0 {
		return fmt.Errorf("determination: component %s's bracket schedule has no tiers", id)
	}
	if b.Tiers[0].From.Sign() != 0 {
		return fmt.Errorf("determination: component %s's first tier starts at %s, not zero; the base below it would have no rate", id, b.Tiers[0].From)
	}
	for i := 1; i < len(b.Tiers); i++ {
		c, err := b.Tiers[i].From.Cmp(b.Tiers[i-1].From)
		if err != nil {
			return fmt.Errorf("determination: component %s tier %d: %w", id, i, err)
		}
		if c <= 0 {
			// DET-REQ-0010: an equal or descending bound is an overlap.
			return fmt.Errorf("determination: component %s's tiers are not strictly ascending at tier %d", id, i)
		}
	}
	return nil
}

// coarse reports whether a policy rounds above the line.
func coarse(p fiscal.RoundingPolicy) bool { return p.Basis() != fiscal.BasisLine }

// validateCoarseReferences refuses a compounding base that names a component
// rounded above the line. DET-001 §6.2 compounds on the rounded amount, and a
// component rounded at the document has no rounded line amount until every
// line has been evaluated — so compounding on it would have to use a raw
// value, which §6.2 forbids, or wait, which would make line evaluation depend
// on other lines.
func (p *Plan) validateCoarseReferences() error {
	for _, c := range p.components {
		for _, ref := range c.Base.Components {
			if coarse(p.components[p.index[ref]].Policy) {
				return fmt.Errorf("determination: component %s compounds on %s, which rounds at %s; compounding needs a line-rounded amount",
					c.ID, ref, p.components[p.index[ref]].Policy.Basis())
			}
		}
	}
	return nil
}

// validatePoolPolicies requires one policy per rounding pool. A jurisdiction's
// total rounded twice, at two scales, is not a total.
func (p *Plan) validatePoolPolicies() error {
	pools := map[string]fiscal.RoundingPolicy{}
	for _, c := range p.components {
		key, ok := poolKey(c)
		if !ok || c.Policy.Basis() == fiscal.BasisTaxComponent {
			continue
		}
		if prior, seen := pools[key]; seen && prior.String() != c.Policy.String() {
			return fmt.Errorf("determination: rounding pool %s has two policies, %s and %s", key, prior, c.Policy)
		}
		pools[key] = c.Policy
	}
	return nil
}

// poolKey names the rounding pool a coarse component belongs to.
func poolKey(c Component) (string, bool) {
	switch c.Policy.Basis() {
	case fiscal.BasisTaxComponent:
		return "component:" + string(c.ID), true
	case fiscal.BasisJurisdictionTotal:
		return "jurisdiction:" + c.JurisdictionID, true
	case fiscal.BasisDocument:
		return "document", true
	}
	return "", false
}

// topologicalOrder is Kahn's algorithm with the ready set kept sorted by
// ComponentID, so independent components come out in one order on every
// replica (DET-REQ-0012) and nothing about jurisdiction level enters into it
// (DET-REQ-0013). A component left over is on a cycle (DET-REQ-0011).
func (p *Plan) topologicalOrder() ([]int, error) {
	n := len(p.components)
	indegree := make([]int, n)
	dependents := make([][]int, n)
	for i, c := range p.components {
		for _, ref := range c.Base.Components {
			j := p.index[ref]
			dependents[j] = append(dependents[j], i)
			indegree[i]++
		}
	}
	var ready []int
	for i := range p.components {
		if indegree[i] == 0 {
			ready = append(ready, i)
		}
	}
	byID := func(s []int) {
		sort.Slice(s, func(a, b int) bool { return p.components[s[a]].ID < p.components[s[b]].ID })
	}
	byID(ready)
	order := make([]int, 0, n)
	for len(ready) > 0 {
		next := ready[0]
		ready = ready[1:]
		order = append(order, next)
		for _, d := range dependents[next] {
			indegree[d]--
			if indegree[d] == 0 {
				ready = append(ready, d)
			}
		}
		byID(ready)
	}
	if len(order) != n {
		var cyclic []string
		for i := range p.components {
			if indegree[i] > 0 {
				cyclic = append(cyclic, string(p.components[i].ID))
			}
		}
		sort.Strings(cyclic)
		return nil, fmt.Errorf("determination: the component graph has a cycle through %v; a compounding cycle is a fixed point, not a calculation", cyclic)
	}
	return order, nil
}

// validateInclusive applies DET-001 §7's build-time rules.
func (p *Plan) validateInclusive(subtraction []ComponentID) error {
	var flats []ComponentID
	anyInclusive := false
	for _, c := range p.components {
		if !c.Inclusive {
			continue
		}
		anyInclusive = true
		if c.Policy.Basis() != fiscal.BasisLine {
			return fmt.Errorf("determination: inclusive component %s rounds at %s; extraction allocates at the line", c.ID, c.Policy.Basis())
		}
		switch c.Rate.Kind {
		case RateFlat, RateSpecific:
			flats = append(flats, c.ID)
			continue
		case RateBracket:
			return fmt.Errorf("determination: inclusive component %s is BRACKET; only a proportional rate has a multiplier on net", c.ID)
		}
		if len(c.Base.Adjustments) > 0 {
			return fmt.Errorf("determination: inclusive component %s adjusts its base; the multiplier on net is only defined for an unadjusted base", c.ID)
		}
		switch c.Base.Kind {
		case BaseLineNet, BaseNetPlusComponents, BaseComponentSum:
		default:
			return fmt.Errorf("determination: inclusive component %s has a %s base; extraction needs LINE_NET or a compounding base", c.ID, c.Base.Kind)
		}
		for _, ref := range c.Base.Components {
			dep := p.components[p.index[ref]]
			if !dep.Inclusive || dep.Rate.Kind != RateAdValorem {
				return fmt.Errorf("determination: inclusive component %s compounds on %s, which is not an inclusive proportional component", c.ID, ref)
			}
		}
	}
	if anyInclusive && p.extraction == nil {
		return fmt.Errorf("determination: the schedule has inclusive components and no extraction policy")
	}
	if p.extraction != nil && p.extraction.Mode() == "" {
		return fmt.Errorf("determination: the extraction policy carries no mode")
	}
	// §7.3.4: every inclusive flat or specific amount is named in the declared
	// subtraction order, exactly once, and nothing else is.
	declared := map[ComponentID]bool{}
	for _, id := range subtraction {
		if declared[id] {
			return fmt.Errorf("determination: %s appears twice in the inclusive subtraction order", id)
		}
		declared[id] = true
	}
	for _, id := range flats {
		if !declared[id] {
			return fmt.Errorf("determination: inclusive %s component %s is not in the declared subtraction order",
				p.components[p.index[id]].Rate.Kind, id)
		}
		delete(declared, id)
	}
	for id := range declared {
		return fmt.Errorf("determination: the inclusive subtraction order names %s, which is not an inclusive FLAT or SPECIFIC component", id)
	}
	p.inclusiveFlat = append([]ComponentID(nil), subtraction...)
	return nil
}
