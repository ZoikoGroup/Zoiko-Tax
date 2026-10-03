package determination

import (
	"errors"
	"fmt"
	"sort"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Line is one line of a document as determination sees it.
type Line struct {
	// Key is the line's stable reference, carried into the result and trace.
	Key string
	// Amount is the line's stated amount: the net when no inclusive component
	// applies to the line, the gross when one does (DET-001 §7.1).
	Amount fiscal.Money
	// Quantity is required by a SPECIFIC component and ignored otherwise.
	Quantity *fiscal.Quantity
	// Applicable is stage 4's output for this line: the components that apply.
	// It is explicit — there is no "all components" default — because which
	// components apply is selection by content, and an empty list is a real
	// answer ("nothing applies") rather than a missing one.
	Applicable []ComponentID
}

// Document is what Evaluate determines.
type Document struct {
	Currency fiscal.Currency
	Lines    []Line
}

// ComponentResult is one component's outcome on one line.
type ComponentResult struct {
	Component    ComponentID
	Jurisdiction string
	TaxType      string
	Inclusive    bool
	// Base is the resolved base at full precision, or nil where the rate kind
	// does not read one (FLAT, SPECIFIC, and inclusive components, whose base
	// is defined through the multiplier rather than resolved).
	Base *fiscal.Money
	// Raw is the full-precision amount before the component's rounding point.
	Raw fiscal.Money
	// Amount is the rounded amount, the legal number (DET-001 §6.2).
	Amount fiscal.Money
}

// LineResult is one line's determination.
type LineResult struct {
	Key string
	// Net is the line's net: its stated amount, or the extracted net of an
	// inclusive line.
	Net fiscal.Money
	// Components are in evaluation order.
	Components []ComponentResult
}

// Tax returns the sum of the line's component amounts.
func (l LineResult) Tax(currency fiscal.Currency) (fiscal.Money, error) {
	total, err := fiscal.ParseMoney("0", currency)
	if err != nil {
		return fiscal.Money{}, err
	}
	for _, c := range l.Components {
		if total, err = total.Add(c.Amount); err != nil {
			return fiscal.Money{}, err
		}
	}
	return total, nil
}

// TraceStep is one step of the execution trace (DET-001 §11.1). Every field is
// a string so the trace is canonical without a schema per step kind.
type TraceStep struct {
	Line      string
	Component ComponentID
	Step      string
	Detail    string
}

// Determination is the result of Evaluate.
type Determination struct {
	Currency fiscal.Currency
	Lines    []LineResult
	Trace    []TraceStep
}

// The trace step kinds.
const (
	StepSubtractInclusive = "SUBTRACT_INCLUSIVE"
	StepMultiplier        = "MULTIPLIER"
	StepExtract           = "EXTRACT"
	StepAllocateExtracted = "ALLOCATE_EXTRACTED"
	StepBase              = "BASE"
	StepZeroSubstituted   = "ZERO_SUBSTITUTED"
	StepAdjust            = "ADJUST"
	StepFloor             = "FLOOR"
	StepRate              = "RATE"
	StepRound             = "ROUND"
	StepCoarseRound       = "COARSE_ROUND"
	StepAllocateBack      = "ALLOCATE_BACK"
)

// lineState is one line's working state during evaluation.
type lineState struct {
	line    Line
	applies map[ComponentID]bool
	net     fiscal.Money
	// gross is the stated gross: net plus every inclusive amount.
	gross   fiscal.Money
	amounts map[ComponentID]fiscal.Money
	results map[ComponentID]*ComponentResult
}

// Evaluate determines a document under the plan.
func (p *Plan) Evaluate(doc Document) (Determination, error) {
	if doc.Currency == "" {
		return Determination{}, errs.Invalid("currency", errs.ReasonMissingField, "A document currency is required.")
	}
	if len(doc.Lines) == 0 {
		return Determination{}, errs.Invalid("lines", errs.ReasonMissingField, "A document needs at least one line.")
	}
	ev := &evaluation{plan: p, doc: doc}
	states := make([]*lineState, len(doc.Lines))
	for i, l := range doc.Lines {
		st, err := ev.newLine(l)
		if err != nil {
			return Determination{}, err
		}
		states[i] = st
	}
	for _, st := range states {
		if err := ev.evaluateLine(st); err != nil {
			return Determination{}, err
		}
	}
	if err := ev.roundCoarse(states); err != nil {
		return Determination{}, err
	}

	out := Determination{Currency: doc.Currency, Lines: make([]LineResult, len(states)), Trace: ev.trace}
	for i, st := range states {
		lr := LineResult{Key: st.line.Key, Net: st.net}
		for _, idx := range p.order {
			id := p.components[idx].ID
			if r, ok := st.results[id]; ok {
				lr.Components = append(lr.Components, *r)
			}
		}
		out.Lines[i] = lr
	}
	return out, nil
}

type evaluation struct {
	plan  *Plan
	doc   Document
	trace []TraceStep
}

func (ev *evaluation) step(line string, c ComponentID, kind, format string, args ...any) {
	ev.trace = append(ev.trace, TraceStep{Line: line, Component: c, Step: kind, Detail: fmt.Sprintf(format, args...)})
}

func (ev *evaluation) newLine(l Line) (*lineState, error) {
	if l.Key == "" {
		return nil, errs.Invalid("lines.key", errs.ReasonMissingField, "Every line needs a key.")
	}
	if l.Amount.Currency() != ev.doc.Currency {
		return nil, errs.Invalid("lines.amount", errs.ReasonCurrencyMismatch,
			fmt.Sprintf("Line %s is in %s; the document is in %s.", l.Key, l.Amount.Currency(), ev.doc.Currency))
	}
	st := &lineState{
		line: l, applies: map[ComponentID]bool{},
		amounts: map[ComponentID]fiscal.Money{}, results: map[ComponentID]*ComponentResult{},
	}
	for _, id := range l.Applicable {
		if _, ok := ev.plan.index[id]; !ok {
			return nil, errs.Invalid("lines.applicable", errs.ReasonInvalidValue,
				fmt.Sprintf("Line %s selects component %s, which the schedule does not declare.", l.Key, id))
		}
		st.applies[id] = true
	}
	return st, nil
}

// evaluateLine runs DET-001 §7 then §6 for one line.
func (ev *evaluation) evaluateLine(st *lineState) error {
	p := ev.plan
	key := st.line.Key
	st.net = st.line.Amount
	st.gross = st.line.Amount

	if err := ev.extract(st); err != nil {
		return err
	}

	for _, idx := range p.order {
		c := p.components[idx]
		if c.Inclusive || !st.applies[c.ID] || c.Base.Kind == BaseDocumentNet {
			continue
		}
		base, err := ev.resolveBase(st, c)
		if err != nil {
			return err
		}
		raw, err := ev.applyRate(st, c, base)
		if err != nil {
			return err
		}
		res := &ComponentResult{
			Component: c.ID, Jurisdiction: c.JurisdictionID, TaxType: c.TaxType,
			Base: base, Raw: raw,
		}
		st.results[c.ID] = res
		if coarse(c.Policy) {
			// Rounded with its pool once every line is evaluated (§8.2).
			continue
		}
		amount, err := raw.Round(c.Policy)
		if err != nil {
			return err
		}
		res.Amount = amount
		st.amounts[c.ID] = amount
		ev.step(key, c.ID, StepRound, "%s -> %s under %s", raw.CanonicalString(), amount.CanonicalString(), c.Policy)
	}
	return nil
}

// extract is DET-001 §7.4 for one line. It does nothing for a line with no
// inclusive component applied.
func (ev *evaluation) extract(st *lineState) error {
	p := ev.plan
	key := st.line.Key
	var proportional []Component
	anyInclusive := false
	for _, idx := range p.order {
		c := p.components[idx]
		if c.Inclusive && st.applies[c.ID] {
			anyInclusive = true
			if c.Rate.Kind == RateAdValorem {
				proportional = append(proportional, c)
			}
		}
	}
	if !anyInclusive {
		return nil
	}

	// Step 1: inclusive FLAT and SPECIFIC amounts leave the gross first, in
	// the declared order (DET-REQ-0021).
	g := st.line.Amount
	for _, id := range p.inclusiveFlat {
		if !st.applies[id] {
			continue
		}
		c := p.components[p.index[id]]
		raw, err := ev.applyRate(st, c, nil)
		if err != nil {
			return err
		}
		amount, err := raw.Round(c.Policy)
		if err != nil {
			return err
		}
		if g, err = g.Sub(amount); err != nil {
			return err
		}
		st.amounts[id] = amount
		st.results[id] = &ComponentResult{
			Component: id, Jurisdiction: c.JurisdictionID, TaxType: c.TaxType, Inclusive: true, Raw: raw, Amount: amount,
		}
		ev.step(key, id, StepSubtractInclusive, "%s leaves the gross; %s remains", amount.CanonicalString(), g.CanonicalString())
	}

	if len(proportional) == 0 {
		st.net = g
		return nil
	}

	// Step 2: the multiplier on net, in topological order (§7.3.1). Never
	// rounded (DET-REQ-0018).
	m := map[ComponentID]fiscal.Factor{}
	sumM := fiscal.Factor{}
	for _, c := range proportional {
		r := fiscal.FactorOf(*c.Rate.Proportion)
		var mi fiscal.Factor
		var err error
		switch c.Base.Kind {
		case BaseLineNet:
			mi = r
		case BaseNetPlusComponents, BaseComponentSum:
			inner := fiscal.Factor{}
			if c.Base.Kind == BaseNetPlusComponents {
				inner = fiscal.FactorOne()
			}
			for _, ref := range c.Base.Components {
				mj, ok := m[ref]
				if !ok {
					ev.step(key, c.ID, StepZeroSubstituted, "%s did not apply; its multiplier is zero", ref)
					continue
				}
				if inner, err = inner.Add(mj); err != nil {
					return err
				}
			}
			if mi, err = r.Mul(inner); err != nil {
				return err
			}
		}
		m[c.ID] = mi
		if sumM, err = sumM.Add(mi); err != nil {
			return err
		}
		ev.step(key, c.ID, StepMultiplier, "m = %s", mi)
	}
	bigM, err := fiscal.FactorOne().Add(sumM)
	if err != nil {
		return err
	}

	// Step 3: the only rounding in extraction.
	n, err := g.DivFactor(bigM, *p.extraction)
	if err != nil {
		return err
	}
	// Step 4: tax by subtraction, never by rate (DET-REQ-0019).
	t, err := g.Sub(n)
	if err != nil {
		return err
	}
	ev.step(key, "", StepExtract, "G %s / M %s = N %s under %s; T = %s",
		g.CanonicalString(), bigM, n.CanonicalString(), *p.extraction, t.CanonicalString())

	// Step 5: largest remainder over m_i, ties on ascending ComponentID
	// (DET-REQ-0020). The order of weights is the tie-break, so sort by ID.
	ids := make([]ComponentID, 0, len(proportional))
	for _, c := range proportional {
		ids = append(ids, c.ID)
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	weights := make([]fiscal.Factor, len(ids))
	for i, id := range ids {
		weights[i] = m[id]
	}
	parts, err := fiscal.AllocateByFactors(t, weights, *p.extraction)
	if err != nil {
		if errors.Is(err, fiscal.ErrNoWeight) {
			return errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
				fmt.Sprintf("Line %s: the inclusive components' rates are all zero, so the extracted tax %s has nowhere to go.", key, t.CanonicalString()))
		}
		return err
	}
	for i, id := range ids {
		c := p.components[p.index[id]]
		st.amounts[id] = parts[i]
		st.results[id] = &ComponentResult{
			Component: id, Jurisdiction: c.JurisdictionID, TaxType: c.TaxType, Inclusive: true,
			Raw: parts[i], Amount: parts[i],
		}
		ev.step(key, id, StepAllocateExtracted, "%s by largest remainder on m", parts[i].CanonicalString())
	}
	st.net = n
	return nil
}

// resolveBase resolves a base declaration at full precision from the line's
// net and the rounded amounts of components already evaluated (DET-001 §6.2,
// DET-REQ-0015), then applies the declared adjustments in order.
func (ev *evaluation) resolveBase(st *lineState, c Component) (*fiscal.Money, error) {
	key := st.line.Key
	var base fiscal.Money
	var err error
	switch c.Base.Kind {
	case BaseQuantity:
		return nil, nil
	case BaseLineNet:
		base = st.net
	case BaseLineGross:
		base = st.net
		for _, idx := range ev.plan.order {
			ic := ev.plan.components[idx]
			if ic.Inclusive {
				if amt, ok := st.amounts[ic.ID]; ok {
					if base, err = base.Add(amt); err != nil {
						return nil, err
					}
				}
			}
		}
	case BaseNetPlusComponents, BaseComponentSum:
		if c.Base.Kind == BaseNetPlusComponents {
			base = st.net
		} else {
			base = st.net.Zero()
		}
		for _, ref := range c.Base.Components {
			amt, ok := st.amounts[ref]
			if !ok {
				// DET-REQ-0014: the one permitted zero substitution, and it is
				// recorded so the thin base is visible.
				ev.step(key, c.ID, StepZeroSubstituted, "%s did not apply; zero is used for it", ref)
				continue
			}
			if base, err = base.Add(amt); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("determination: base kind %s is not resolved per line", c.Base.Kind)
	}
	ev.step(key, c.ID, StepBase, "%s %s", c.Base.Kind, base.CanonicalString())
	return ev.adjust(key, c, base)
}

// adjust applies the declared adjustment sequence and the zero floor.
func (ev *evaluation) adjust(key string, c Component, base fiscal.Money) (*fiscal.Money, error) {
	if len(c.Base.Adjustments) == 0 {
		return &base, nil
	}
	startedNonNegative := base.Sign() >= 0
	var err error
	for _, a := range c.Base.Adjustments {
		switch a.Kind {
		case AdjustSubtractAmount:
			base, err = base.Sub(*a.Amount)
		case AdjustSubtractProportion:
			var cut fiscal.Money
			if cut, err = base.Times(*a.Proportion); err == nil {
				base, err = base.Sub(cut)
			}
		}
		if err != nil {
			return nil, err
		}
		ev.step(key, c.ID, StepAdjust, "%s %s -> %s", a.Kind, a.Name, base.CanonicalString())
	}
	// DET-REQ-0006: an adjustment may not take a base below zero unless
	// content says so. A base that was already negative — a return line — was
	// not taken there by an adjustment and is not floored.
	if startedNonNegative && base.Sign() < 0 && !c.Base.AllowNegative {
		ev.step(key, c.ID, StepFloor, "adjusted base %s floors at zero", base.CanonicalString())
		base = base.Zero()
	}
	return &base, nil
}

// applyRate is DET-001 §4 at full precision. Nothing here rounds.
func (ev *evaluation) applyRate(st *lineState, c Component, base *fiscal.Money) (fiscal.Money, error) {
	key := st.line.Key
	var raw fiscal.Money
	var err error
	switch c.Rate.Kind {
	case RateAdValorem:
		raw, err = base.Times(*c.Rate.Proportion)
	case RateSpecific:
		if st.line.Quantity == nil {
			return fiscal.Money{}, errs.Invalid("lines.quantity", errs.ReasonMissingField,
				fmt.Sprintf("Line %s selects per-unit component %s and carries no quantity.", key, c.ID))
		}
		if err := ev.sameCurrency(*c.Rate.PerUnit, c.ID); err != nil {
			return fiscal.Money{}, err
		}
		raw, err = c.Rate.PerUnit.TimesQuantity(*st.line.Quantity)
	case RateFlat:
		if err := ev.sameCurrency(*c.Rate.Flat, c.ID); err != nil {
			return fiscal.Money{}, err
		}
		raw = *c.Rate.Flat
	case RateBracket:
		raw, err = ev.bracket(c, *base)
	}
	if err != nil {
		return fiscal.Money{}, err
	}
	ev.step(key, c.ID, StepRate, "%s = %s", c.Rate.Kind, raw.CanonicalString())
	return raw, nil
}

func (ev *evaluation) sameCurrency(m fiscal.Money, id ComponentID) error {
	if m.Currency() != ev.doc.Currency {
		return errs.Invalid("currency", errs.ReasonCurrencyMismatch,
			fmt.Sprintf("Component %s is denominated in %s; the document is in %s.", id, m.Currency(), ev.doc.Currency))
	}
	return nil
}

// bracket applies a tiered schedule (DET-001 §4.4) at full precision.
func (ev *evaluation) bracket(c Component, base fiscal.Money) (fiscal.Money, error) {
	b := c.Rate.Bracket
	if err := ev.sameCurrency(b.Tiers[0].From, c.ID); err != nil {
		return fiscal.Money{}, err
	}
	if base.Sign() < 0 {
		return fiscal.Money{}, errs.Invalid("base", errs.ReasonInvalidValue,
			fmt.Sprintf("Component %s: a bracket schedule has no tier for a negative base %s.", c.ID, base.CanonicalString()))
	}
	switch b.Mode {
	case BracketCliff:
		rate := b.Tiers[0].Rate
		for _, t := range b.Tiers[1:] {
			reached, err := base.Cmp(t.From)
			if err != nil {
				return fiscal.Money{}, err
			}
			if reached < 0 {
				break
			}
			rate = t.Rate
		}
		return base.Times(rate)
	default: // BracketMarginal; Compile admits nothing else.
		total := base.Zero()
		for i, t := range b.Tiers {
			upper := base
			if i+1 < len(b.Tiers) {
				var err error
				if upper, err = base.Min(b.Tiers[i+1].From); err != nil {
					return fiscal.Money{}, err
				}
			}
			portion, err := upper.Sub(t.From)
			if err != nil {
				return fiscal.Money{}, err
			}
			if portion.Sign() <= 0 {
				break
			}
			part, err := portion.Times(t.Rate)
			if err != nil {
				return fiscal.Money{}, err
			}
			if total, err = total.Add(part); err != nil {
				return fiscal.Money{}, err
			}
		}
		return total, nil
	}
}

// pool is one coarse rounding point: a set of (line, component) raw amounts
// rounded as a total and allocated back (DET-001 §8.2).
type pool struct {
	key     string
	policy  fiscal.RoundingPolicy
	total   fiscal.Money
	members []poolMember
}

type poolMember struct {
	st     *lineState
	id     ComponentID
	weight fiscal.Money
}

// roundCoarse evaluates document-level components and rounds every pool
// coarser than the line, allocating each rounded total back to the lines that
// contributed so that the lines sum to it exactly (DET-REQ-0023).
func (ev *evaluation) roundCoarse(states []*lineState) error {
	p := ev.plan
	pools := map[string]*pool{}
	var keys []string
	get := func(c Component) *pool {
		k, _ := poolKey(c)
		pl, ok := pools[k]
		if !ok {
			zero, _ := fiscal.ParseMoney("0", ev.doc.Currency)
			pl = &pool{key: k, policy: c.Policy, total: zero}
			pools[k] = pl
			keys = append(keys, k)
		}
		return pl
	}

	for _, idx := range p.order {
		c := p.components[idx]
		if !coarse(c.Policy) {
			continue
		}
		if c.Base.Kind == BaseDocumentNet {
			if err := ev.documentComponent(states, c, get(c)); err != nil {
				return err
			}
			continue
		}
		for _, st := range states {
			res, ok := st.results[c.ID]
			if !ok {
				continue
			}
			pl := get(c)
			var err error
			if pl.total, err = pl.total.Add(res.Raw); err != nil {
				return err
			}
			pl.members = append(pl.members, poolMember{st: st, id: c.ID, weight: res.Raw})
		}
	}

	for _, k := range keys {
		pl := pools[k]
		if len(pl.members) == 0 {
			continue
		}
		rounded, err := pl.total.Round(pl.policy)
		if err != nil {
			return err
		}
		ev.step("", "", StepCoarseRound, "%s: %s -> %s under %s", pl.key, pl.total.CanonicalString(), rounded.CanonicalString(), pl.policy)
		weights, err := magnitudes(pl)
		if err != nil {
			return err
		}
		parts, err := fiscal.Allocate(pl.total, weights, pl.policy)
		if err != nil {
			return err
		}
		for i, m := range pl.members {
			res := m.st.results[m.id]
			res.Amount = parts[i]
			m.st.amounts[m.id] = parts[i]
			ev.step(m.st.line.Key, m.id, StepAllocateBack, "%s of %s", parts[i].CanonicalString(), pl.key)
		}
	}
	return nil
}

// documentComponent evaluates a DOCUMENT_NET component once on the document
// net and enrols the lines it applies to, weighted by their nets.
func (ev *evaluation) documentComponent(states []*lineState, c Component, pl *pool) error {
	base, err := fiscal.ParseMoney("0", ev.doc.Currency)
	if err != nil {
		return err
	}
	var members []*lineState
	for _, st := range states {
		if !st.applies[c.ID] {
			continue
		}
		if base, err = base.Add(st.net); err != nil {
			return err
		}
		members = append(members, st)
	}
	if len(members) == 0 {
		return nil
	}
	ev.step("", c.ID, StepBase, "%s %s", BaseDocumentNet, base.CanonicalString())
	adjusted, err := ev.adjust("", c, base)
	if err != nil {
		return err
	}
	docState := &lineState{line: Line{Key: ""}}
	raw, err := ev.applyRate(docState, c, adjusted)
	if err != nil {
		return err
	}
	pl.total = raw
	for _, st := range members {
		st.results[c.ID] = &ComponentResult{
			Component: c.ID, Jurisdiction: c.JurisdictionID, TaxType: c.TaxType, Base: adjusted, Raw: raw,
		}
		pl.members = append(pl.members, poolMember{st: st, id: c.ID, weight: st.net})
	}
	return nil
}

// magnitudes returns the pool's weights as non-negative amounts. A pool whose
// contributions carry both signs has no well-defined largest-remainder
// allocation — a remainder on a credit does not rank against one on a debit —
// so it is refused rather than allocated by a rule nobody declared.
func magnitudes(pl *pool) ([]fiscal.Money, error) {
	out := make([]fiscal.Money, len(pl.members))
	sign := 0
	for i, m := range pl.members {
		s := m.weight.Sign()
		if s != 0 {
			if sign != 0 && s != sign {
				return nil, errs.Invalid("lines", errs.ReasonInvalidValue,
					fmt.Sprintf("Rounding point %s mixes positive and negative amounts; a document rounded at that point must be all charges or all credits.", pl.key))
			}
			sign = s
		}
		if s < 0 {
			out[i] = m.weight.Neg()
		} else {
			out[i] = m.weight
		}
	}
	return out, nil
}

// Canonical renders a determination for the evidence record. Order is
// significant throughout and is the evaluation order (ADR-0011 P4).
func (d Determination) Canonical() canonical.Value {
	lines := make([]canonical.Value, 0, len(d.Lines))
	for _, l := range d.Lines {
		comps := make([]canonical.Value, 0, len(l.Components))
		for _, c := range l.Components {
			base := canonical.Absent()
			if c.Base != nil {
				base = canonical.Money(*c.Base)
			}
			comps = append(comps, canonical.Object(
				canonical.F("component", canonical.String(string(c.Component))),
				canonical.F("jurisdiction", canonical.String(c.Jurisdiction)),
				canonical.F("taxType", canonical.String(c.TaxType)),
				canonical.F("inclusive", canonical.Bool(c.Inclusive)),
				canonical.F("base", base),
				canonical.F("raw", canonical.Money(c.Raw)),
				canonical.F("amount", canonical.Money(c.Amount)),
			))
		}
		lines = append(lines, canonical.Object(
			canonical.F("key", canonical.String(l.Key)),
			canonical.F("net", canonical.Money(l.Net)),
			canonical.F("components", canonical.Array(comps...)),
		))
	}
	trace := make([]canonical.Value, 0, len(d.Trace))
	for _, s := range d.Trace {
		trace = append(trace, canonical.Object(
			canonical.F("line", canonical.OptString(s.Line)),
			canonical.F("component", canonical.OptString(string(s.Component))),
			canonical.F("step", canonical.String(s.Step)),
			canonical.F("detail", canonical.String(s.Detail)),
		))
	}
	return canonical.Object(
		canonical.F("currency", canonical.String(string(d.Currency))),
		canonical.F("lines", canonical.Array(lines...)),
		canonical.F("trace", canonical.Array(trace...)),
	)
}
