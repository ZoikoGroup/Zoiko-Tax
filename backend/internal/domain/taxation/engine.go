package taxation

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/determination"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/jurisdiction"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The states a line, and a document, end in. RESOLVED is the only one that
// carries amounts; every other is a recorded outcome with its evidence and a
// reason, and is never presented as a zero tax (playbook §12).
const (
	StateResolved       = "RESOLVED"
	StateReviewRequired = "REVIEW_REQUIRED"
	StateUnsupported    = "UNSUPPORTED"
	StateAmbiguous      = "AMBIGUOUS"
	StateConflicted     = "CONFLICTED"
)

// severity orders the states for the document's own: the document is as
// unresolved as its least resolved line.
var severity = map[string]int{
	StateResolved: 0, StateUnsupported: 1, StateReviewRequired: 2, StateAmbiguous: 3, StateConflicted: 4,
}

// item is one evaluated unit: a line, or one part of a bundle line.
type item struct {
	key      string
	line     string
	part     string
	sku      string
	service  string
	amount   fiscal.Money
	quantity *fiscal.Quantity
	evidence []jurisdiction.SitusEvidence

	res      ItemResult
	selected []selection
}

// selection is one component applied to one item.
type selection struct {
	comp      *component
	ver       version
	apply     ApplyDecl
	treatment string
	exemption *Exemption
}

type run struct {
	e        *Engine
	doc      Document
	at       time.Time
	trace    []Step
	sellerCC string
}

func (r *run) step(item, kind, format string, args ...any) {
	r.trace = append(r.trace, Step{Item: item, Step: kind, Detail: fmt.Sprintf(format, args...)})
}

// The selection trace step kinds. The evaluation's own steps are DET-001's
// (determination.Step*) and are carried through unchanged.
const (
	StepClassify  = "CLASSIFY"
	StepSitus     = "SITUS"
	StepRegime    = "REGIME"
	StepSelect    = "SELECT"
	StepSkip      = "SKIP"
	StepExemption = "EXEMPTION"
	StepState     = "STATE"
)

// Determine runs the location-aware pipeline over a document at a tax point.
//
// It returns an error only for a document whose shape is wrong — a missing
// currency, an allocation that does not sum — and for a defect in the engine.
// Everything that is a fact about coverage or evidence is a state on the
// result.
func (e *Engine) Determine(doc Document, taxPoint time.Time) (Result, error) {
	if taxPoint.IsZero() {
		return Result{}, errs.Invalid("eventTime", errs.ReasonMissingField, "A tax point is required.")
	}
	if err := doc.Validate(); err != nil {
		return Result{}, invalid(err)
	}
	r := &run{e: e, doc: doc, at: taxPoint}
	cc, err := r.sellerCountry()
	if err != nil {
		return Result{}, err
	}
	r.sellerCC = cc
	base, err := r.documentEvidence()
	if err != nil {
		return Result{}, err
	}

	items, err := r.items(base)
	if err != nil {
		return Result{}, err
	}
	for _, it := range items {
		r.resolveItem(it)
	}

	out := Result{
		State: StateResolved, Reason: string(errs.ReasonNotAuthoritative),
		Currency: doc.Currency, PriceMode: doc.PriceMode,
		ProfileVersion: e.version, GraphVersion: e.graph.Version(),
	}
	for _, it := range items {
		if severity[it.res.State] > severity[out.State] {
			out.State, out.Reason = it.res.State, it.res.Reason
		}
	}
	if out.State == StateResolved {
		if err := r.evaluate(items, &out); err != nil {
			return Result{}, err
		}
	}
	for _, it := range items {
		out.Items = append(out.Items, it.res)
	}
	out.Trace = r.trace
	return out, nil
}

// invalid maps a document shape error to the validation problem it is.
func invalid(err error) error {
	var fe *FieldError
	if errors.As(err, &fe) {
		return errs.Invalid(fe.Field, errs.ReasonInvalidValue, "The document's "+fe.Field+" "+fe.Detail+".")
	}
	return errs.Invalid("document", errs.ReasonInvalidValue, err.Error())
}

// sellerCountry is the country the seller is established in. It decides
// whether a supply crosses a border, so it must be known even where the
// seller's own country is one the profile does not cover.
func (r *run) sellerCountry() (string, error) {
	p := *r.doc.Seller.Establishment
	switch {
	case p.Country != "":
		return p.Country, nil
	case p.Subdivision != "":
		return p.Subdivision[:2], nil
	}
	n, ok := r.e.graph.Node(jurisdiction.ID(p.Jurisdiction))
	if !ok {
		return "", errs.Invalid("document.seller.establishment", errs.ReasonJurisdictionUnresolved,
			"The seller's establishment names a jurisdiction the active content does not declare. Name its country or subdivision instead.")
	}
	return n.Country, nil
}

// evidence converts a caller's place into a situs evidence item. A
// subdivision code the graph does not know becomes country evidence, and the
// trace says so: the country may still be covered at that level, and if it
// needs the subdivision the regime check will say so.
func (r *run) evidence(item string, t jurisdiction.EvidenceType, src jurisdiction.EvidenceSource, at time.Time, p Place) jurisdiction.SitusEvidence {
	if at.IsZero() {
		at = r.at
	}
	e := jurisdiction.SitusEvidence{Type: t, Source: src, CollectedAt: at.UTC().Truncate(time.Microsecond)}
	switch {
	case p.Jurisdiction != "":
		e.Jurisdiction = jurisdiction.ID(p.Jurisdiction)
	case p.Subdivision != "":
		if id, ok := r.e.byCode[p.Subdivision]; ok {
			e.Jurisdiction = id
		} else if r.e.excluded(p.Subdivision) {
			// Outside the country's tax territory: an identifier the graph
			// does not declare, so the evidence places the supply nowhere
			// the content covers and the line is UNSUPPORTED.
			e.Jurisdiction = jurisdiction.ID("jurisdiction:" + strings.ToLower(p.Subdivision))
			r.step(item, StepSitus, "%s subdivision %s is outside regime %s's territory",
				t, p.Subdivision, r.e.regimes[p.Subdivision[:2]].decl.ID)
		} else {
			e.Country = p.Subdivision[:2]
			r.step(item, StepSitus, "%s subdivision %s is not in graph %s; recorded as country %s evidence",
				t, p.Subdivision, r.e.graph.Version(), e.Country)
		}
	default:
		e.Country = p.Country
	}
	return e
}

func (r *run) documentEvidence() ([]jurisdiction.SitusEvidence, error) {
	d := r.doc
	out := []jurisdiction.SitusEvidence{
		r.evidence("", jurisdiction.EvidenceSellerEstablishment, jurisdiction.SourceSellerRecorded, time.Time{}, *d.Seller.Establishment),
	}
	if d.Customer.Establishment != nil {
		out = append(out, r.evidence("", jurisdiction.EvidenceCustomerEstablishment, jurisdiction.SourceSellerRecorded, time.Time{}, *d.Customer.Establishment))
	}
	if v := d.Customer.VAT; v != nil {
		out = append(out, jurisdiction.SitusEvidence{
			Type: jurisdiction.EvidenceVATIdentification, Source: jurisdiction.SourceCustomerAsserted,
			CollectedAt: r.at.UTC().Truncate(time.Microsecond), Country: v.Country,
			VATValidation: jurisdiction.VATValidation(v.Validation),
		})
	}
	for _, ev := range d.Evidence {
		out = append(out, r.evidence("", jurisdiction.EvidenceType(ev.Type), jurisdiction.EvidenceSource(ev.Source), ev.CollectedAt, ev.Place))
	}
	for _, e := range out {
		if err := e.Validate(); err != nil {
			return nil, errs.Invalid("document.evidence", errs.ReasonInvalidValue, err.Error())
		}
	}
	return out, nil
}

// items expands lines into evaluation units: a line, or each part of a
// bundle line under "<line>/<part>".
func (r *run) items(base []jurisdiction.SitusEvidence) ([]*item, error) {
	var out []*item
	for _, l := range r.doc.Lines {
		ev := append([]jurisdiction.SitusEvidence(nil), base...)
		for _, x := range l.Evidence {
			e := r.evidence(l.Key, jurisdiction.EvidenceType(x.Type), jurisdiction.EvidenceSource(x.Source), x.CollectedAt, x.Place)
			if err := e.Validate(); err != nil {
				return nil, errs.Invalid("document.lines.evidence", errs.ReasonInvalidValue, err.Error())
			}
			ev = append(ev, e)
		}
		if len(l.Parts) == 0 {
			out = append(out, &item{key: l.Key, line: l.Key, sku: l.SKU, service: l.Service, amount: l.Amount, quantity: l.Quantity, evidence: ev})
			continue
		}
		for _, p := range l.Parts {
			out = append(out, &item{
				key: l.Key + "/" + p.Key, line: l.Key, part: p.Key, sku: l.SKU, service: p.Service,
				amount: p.Amount, quantity: p.Quantity, evidence: ev,
			})
		}
	}
	return out, nil
}

// resolveItem runs stages 2 to 4 for one item and leaves it either RESOLVED
// with its selections, or in an explicit state.
func (r *run) resolveItem(it *item) {
	it.res = ItemResult{
		Key: it.key, Line: it.line, Part: it.part, SKU: it.sku, Service: it.service,
		Amount: it.amount, State: StateResolved,
	}
	settle := func(state string, reason errs.ReasonCode, format string, args ...any) {
		it.res.State, it.res.Reason = state, string(reason)
		r.step(it.key, StepState, "%s (%s): %s", state, reason, fmt.Sprintf(format, args...))
	}

	// Stage 3 comes first here because situs rules select by service.
	if it.service == "" {
		settle(StateReviewRequired, errs.ReasonClassificationRequired, "SKU %q has no ontology family", it.sku)
		return
	}
	r.step(it.key, StepClassify, "service %s (SKU %s)", it.service, orNone(it.sku))

	sr, err := r.e.resolver.Resolve(jurisdiction.Request{
		EventTime: r.at, Ontology: it.service, Evidence: it.evidence, Covered: r.e.covered,
	})
	if err != nil {
		// Malformed evidence was refused at validation; anything here is
		// evidence the graph cannot interpret, which is a review matter.
		settle(StateReviewRequired, errs.ReasonSitusEvidenceInsufficient, "situs evidence could not be resolved: %v", err)
		return
	}
	situs, err := canonical.Encode(sr.Canonical())
	if err != nil {
		settle(StateConflicted, errs.ReasonSitusDatasetDefect, "situs resolution could not be recorded: %v", err)
		return
	}
	it.res.Situs = situs
	it.res.SitusOutcome = string(sr.Outcome)
	it.res.SitusRule = sr.RuleID
	it.res.CustomerRole = string(sr.Supply.EffectiveRole)
	for _, j := range sr.Applicable {
		it.res.Applicable = append(it.res.Applicable, string(j.ID))
	}
	for _, t := range sr.NeededEvidence {
		it.res.NeededEvidence = append(it.res.NeededEvidence, string(t))
	}
	r.step(it.key, StepSitus, "%s under rule %s: %s", sr.Outcome, orNone(sr.RuleID), strings.Join(it.res.Applicable, ", "))

	if e, ok := r.outranksFromOutside(it, sr); ok {
		where := e.Country
		if where == "" {
			where = string(e.Jurisdiction)
		}
		settle(StateUnsupported, errs.ReasonJurisdictionUnsupported,
			"%s evidence places the supply in %s, which the active content does not declare", e.Type, where)
		return
	}

	switch sr.Outcome {
	case jurisdiction.OutcomeResolved:
	case jurisdiction.OutcomeInsufficientEvidence:
		settle(StateReviewRequired, errs.ReasonSitusEvidenceInsufficient, "needs one of %s", strings.Join(it.res.NeededEvidence, ", "))
		return
	case jurisdiction.OutcomeAmbiguous:
		settle(StateAmbiguous, errs.ReasonAmbiguous, "the evidence supports more than one place of supply")
		return
	case jurisdiction.OutcomeUnsupported:
		settle(StateUnsupported, errs.ReasonJurisdictionUnsupported, "no active content covers %s", strings.Join(it.res.Applicable, ", "))
		return
	default:
		settle(StateConflicted, errs.ReasonSitusDatasetDefect, "situs outcome %s", sr.Outcome)
		return
	}

	primary, _ := jurisdiction.Resolution{Applicable: sr.Applicable}.Primary()
	// The deepest general jurisdiction is the place of supply; a SPECIAL one
	// sorts last but is never the place of supply.
	for i := len(sr.Applicable) - 1; i >= 0; i-- {
		if sr.Applicable[i].Level != jurisdiction.LevelSpecial {
			primary = sr.Applicable[i]
			break
		}
	}
	it.res.PlaceOfSupply = string(primary.ID)
	cross := r.sellerCC != primary.Country
	it.res.Supply = SupplyDomestic
	if cross {
		it.res.Supply = SupplyCrossBorder
	}

	rg := r.e.regimes[primary.Country]
	it.res.Regime = rg.decl.ID
	r.step(it.key, StepRegime, "%s (%s); seller in %s, supply %s, customer %s",
		rg.decl.ID, primary.Country, r.sellerCC, it.res.Supply, it.res.CustomerRole)
	have, _ := primary.Level.Rank()
	need, _ := rg.level.Rank()
	if have < need {
		settle(StateReviewRequired, errs.ReasonJurisdictionLevelRequired,
			"%s resolved to %s level; regime %s needs %s", primary.ID, primary.Level, rg.decl.ID, rg.level)
		return
	}
	if !rg.services[it.service] {
		settle(StateUnsupported, errs.ReasonServiceNotCovered, "regime %s declares no content for %s", rg.decl.ID, it.service)
		return
	}

	applicable := map[string]bool{}
	for _, j := range sr.Applicable {
		applicable[string(j.ID)] = true
	}
	for _, c := range r.e.comps {
		if c.regime != rg {
			continue
		}
		if !applicable[c.decl.Jurisdiction] {
			continue
		}
		if len(c.services) > 0 && !c.services[it.service] {
			continue
		}
		v, ok := c.versionAt(r.at)
		if !ok {
			r.step(it.key, StepSkip, "%s has no version in force at %s", c.decl.ID, canonical.FormatTime(r.at))
			continue
		}
		a, ok := c.match(it.res.CustomerRole, it.res.Supply)
		if !ok {
			r.step(it.key, StepSkip, "%s does not apply to a %s customer on a %s supply", c.decl.ID, it.res.CustomerRole, it.res.Supply)
			continue
		}
		if v.rate.Kind == determination.RateSpecific {
			if it.quantity == nil || string(it.quantity.Unit()) != v.perUnits {
				settle(StateReviewRequired, errs.ReasonQuantityRequired,
					"%s is levied per %s and the line carries no quantity in that unit", c.decl.ID, v.perUnits)
				return
			}
		}
		sel := selection{comp: c, ver: v, apply: a, treatment: a.Treatment}
		if a.Treatment == TreatmentCustomerCharge && len(c.exemptBy) > 0 {
			ex, held := r.exemption(it, c)
			if held {
				settle(StateReviewRequired, errs.ReasonExemptionUnverified,
					"certificate %s would exempt %s and could not be verified", ex.Certificate, c.decl.ID)
				return
			}
			if ex != nil {
				sel.treatment, sel.exemption = TreatmentExempt, ex
			}
		}
		r.step(it.key, StepSelect, "%s version from %s, %s, %s", c.decl.ID, v.decl.From, sel.treatment, rateText(v))
		it.selected = append(it.selected, sel)
	}
	if len(it.selected) == 0 {
		settle(StateUnsupported, errs.ReasonNoApplicableContent,
			"no component of %s applies to %s for a %s customer on a %s supply", rg.decl.ID, it.service, it.res.CustomerRole, it.res.Supply)
	}
}

// outranksFromOutside finds evidence the graph cannot place that the rule
// would have preferred to whatever it resolved with.
//
// The resolver treats an item it cannot map as unusable and moves on, which
// is right for an item that is merely stale and wrong for one that is in a
// country the content does not declare: a service address abroad would be
// passed over for a billing address at home, and the supply taxed where it
// did not happen. Such an item is a fact about our coverage, so the line is
// UNSUPPORTED rather than resolved by the next item down.
func (r *run) outranksFromOutside(it *item, sr jurisdiction.SitusResolution) (jurisdiction.SitusEvidence, bool) {
	rule, ok := r.e.rules[sr.RuleID]
	if !ok {
		return jurisdiction.SitusEvidence{}, false
	}
	rank := map[string]int{"WEAK": 1, "SUPPORTING": 2, "STRONG": 3}
	position := map[string]int{}
	switch jurisdiction.Mode(rule.Mode) {
	case jurisdiction.ModePrecedence:
		for i, t := range rule.Order {
			if rank[rule.Reliability[t]] >= rank[rule.MinimumReliability] {
				position[t] = i
			}
		}
	case jurisdiction.ModeQuorum:
		// Every eligible item counts toward agreement, so one that cannot be
		// placed is one that may contradict the rest.
		for _, t := range rule.Eligible {
			position[t] = 0
		}
	}
	limit := len(rule.Order) + len(rule.Eligible)
	if jurisdiction.Mode(rule.Mode) == jurisdiction.ModePrecedence && len(sr.Winning) > 0 &&
		(sr.Outcome == jurisdiction.OutcomeResolved || sr.Outcome == jurisdiction.OutcomeAmbiguous) {
		limit = position[string(sr.Winning[0].Type)]
	}
	for _, e := range it.evidence {
		p, considered := position[string(e.Type)]
		if !considered || p >= limit && jurisdiction.Mode(rule.Mode) == jurisdiction.ModePrecedence {
			continue
		}
		outside := (e.Country != "" && !r.e.countries[e.Country])
		if e.Jurisdiction != "" {
			_, known := r.e.graph.Node(e.Jurisdiction)
			outside = !known
		}
		if outside {
			return e, true
		}
	}
	return jurisdiction.SitusEvidence{}, false
}

// excluded reports whether a subdivision code is outside its country's
// regime territory.
func (e *Engine) excluded(code string) bool {
	rg, ok := e.regimes[code[:2]]
	return ok && contains(rg.decl.ExcludedSubdivisions, code)
}

// match returns the one applicability entry that matches.
func (c *component) match(role, supply string) (ApplyDecl, bool) {
	for _, a := range c.decl.Applies {
		if a.Supply != SupplyAny && a.Supply != supply {
			continue
		}
		if len(a.Roles) > 0 && !contains(a.Roles, role) {
			continue
		}
		return a, true
	}
	return ApplyDecl{}, false
}

// exemption finds a certificate that exempts c on this item. It returns the
// exemption, or held when a certificate would apply and its verification was
// unavailable. Every certificate considered and refused is traced with why:
// an expired certificate, one held by someone else, or one scoped to another
// authority must be visibly refused, never silently ignored (playbook §29).
func (r *run) exemption(it *item, c *component) (*Exemption, bool) {
	for _, cert := range r.doc.Certificates {
		if !c.exemptBy[cert.Type] {
			continue
		}
		why := ""
		switch {
		case cert.Jurisdiction != c.decl.Jurisdiction:
			why = fmt.Sprintf("issued for %s, not %s", cert.Jurisdiction, c.decl.Jurisdiction)
		case cert.Holder != r.doc.Customer.ID:
			why = fmt.Sprintf("held by %s, not the customer %s", cert.Holder, r.doc.Customer.ID)
		case len(cert.TaxTypes) > 0 && !contains(cert.TaxTypes, c.decl.TaxType):
			why = fmt.Sprintf("scoped to %s, not %s", strings.Join(cert.TaxTypes, ", "), c.decl.TaxType)
		case len(cert.Components) > 0 && !contains(cert.Components, c.decl.ID):
			why = fmt.Sprintf("scoped to %s", strings.Join(cert.Components, ", "))
		default:
			from, _ := civilDate(cert.ValidFrom, c.loc)
			if r.at.Before(from) {
				why = "not yet valid at the tax point"
				break
			}
			if cert.ValidTo != "" {
				to, _ := civilDate(cert.ValidTo, c.loc)
				if !r.at.Before(to) {
					why = "expired before the tax point"
				}
			}
		}
		if why != "" {
			r.step(it.key, StepExemption, "certificate %s does not exempt %s: %s", cert.ID, c.decl.ID, why)
			continue
		}
		switch cert.Verification {
		case VerificationOK:
			r.step(it.key, StepExemption, "certificate %s (%s) exempts %s", cert.ID, cert.Type, c.decl.ID)
			return &Exemption{Certificate: cert.ID, Type: cert.Type}, false
		case VerificationDown:
			return &Exemption{Certificate: cert.ID, Type: cert.Type}, true
		default:
			r.step(it.key, StepExemption, "certificate %s is unverified and exempts nothing", cert.ID)
		}
	}
	return nil, false
}

// evaluate runs stages 6 to 8 over a fully resolved document.
func (r *run) evaluate(items []*item, out *Result) error {
	inclusive := r.doc.PriceMode == PriceInclusive
	var extraction *fiscal.RoundingPolicy
	if inclusive {
		for _, it := range items {
			rg := r.e.regimes[r.countryOf(it.res.PlaceOfSupply)]
			if rg.decl.Extraction == nil || (extraction != nil && extraction.String() != rg.decl.Extraction.String()) {
				out.State, out.Reason = StateUnsupported, string(errs.ReasonInclusivePricingUnsupported)
				r.step(it.key, StepState, "%s (%s): regime %s", StateUnsupported, errs.ReasonInclusivePricingUnsupported, rg.decl.ID)
				return nil
			}
			extraction = rg.decl.Extraction
		}
	}

	// Plan identifiers: the component id, or id#treatment where one
	// document applies a component under two treatments.
	treatments := map[string]map[string]bool{}
	for _, it := range items {
		for _, s := range it.selected {
			if treatments[s.comp.decl.ID] == nil {
				treatments[s.comp.decl.ID] = map[string]bool{}
			}
			treatments[s.comp.decl.ID][s.treatment] = true
		}
	}
	planID := func(id, treatment string) string {
		if len(treatments[id]) <= 1 {
			return id
		}
		return id + "#" + treatment
	}

	actual, err := r.plan(items, planID, inclusive, extraction, false)
	if err != nil {
		return err
	}
	var assessed *determination.Determination
	for _, it := range items {
		for _, s := range it.selected {
			if s.exemption != nil {
				a, err := r.plan(items, planID, inclusive, extraction, true)
				if err != nil {
					return err
				}
				assessed = &a
				break
			}
		}
		if assessed != nil {
			break
		}
	}

	for _, s := range actual.Trace {
		r.trace = append(r.trace, Step{Item: s.Line, Component: string(s.Component), Step: s.Step, Detail: s.Detail})
	}
	return r.collect(items, planID, actual, assessed, out)
}

func (r *run) countryOf(id string) string {
	n, _ := r.e.graph.Node(jurisdiction.ID(id))
	return n.Country
}

// plan builds and evaluates the DET-001 plan for the document. With
// asAssessed, exempted components are evaluated as charges, so the suppressed
// amount the trace retains is the amount the certificate actually removed.
func (r *run) plan(items []*item, planID func(string, string) string, inclusive bool, extraction *fiscal.RoundingPolicy, asAssessed bool) (determination.Determination, error) {
	byPlan := map[string]determination.Component{}
	variants := map[string][]string{}
	add := func(s selection) {
		treatment := s.treatment
		if asAssessed && treatment == TreatmentExempt {
			treatment = TreatmentCustomerCharge
		}
		id := planID(s.comp.decl.ID, s.treatment)
		if _, ok := byPlan[id]; ok {
			return
		}
		incl := inclusive && treatment == TreatmentCustomerCharge
		byPlan[id] = s.comp.planComponent(s.ver, id, incl)
		variants[s.comp.decl.ID] = append(variants[s.comp.decl.ID], id)
	}
	for _, it := range items {
		for _, s := range it.selected {
			add(s)
		}
	}
	// A compounding base names components by declared id. Every variant of a
	// named component present in the plan is named; one that is in no line's
	// selection is declared with its in-force version so that DET-REQ-0014's
	// recorded zero substitution, rather than a compile error, says it did
	// not apply.
	for changed := true; changed; {
		changed = false
		ids := sortedKeys(byPlan)
		for _, id := range ids {
			for _, ref := range byPlan[id].Base.Components {
				if _, ok := byPlan[string(ref)]; ok {
					continue
				}
				if vs := variants[string(ref)]; len(vs) > 0 {
					continue
				}
				dep := r.e.compIndex[string(ref)]
				v, ok := dep.versionAt(r.at)
				if !ok {
					v = dep.versions[len(dep.versions)-1]
				}
				byPlan[string(ref)] = dep.planComponent(v, string(ref), false)
				variants[string(ref)] = []string{string(ref)}
				changed = true
			}
		}
	}
	comps := make([]determination.Component, 0, len(byPlan))
	var subtraction []determination.ComponentID
	for _, id := range sortedKeys(byPlan) {
		c := byPlan[id]
		var refs []determination.ComponentID
		for _, ref := range c.Base.Components {
			for _, v := range variants[string(ref)] {
				refs = append(refs, determination.ComponentID(v))
			}
		}
		c.Base.Components = refs
		if c.Inclusive && (c.Rate.Kind == determination.RateFlat || c.Rate.Kind == determination.RateSpecific) {
			subtraction = append(subtraction, c.ID)
		}
		comps = append(comps, c)
	}
	sched := determination.Schedule{Components: comps, InclusiveSubtractionOrder: subtraction}
	if inclusive {
		sched.ExtractionPolicy = extraction
	}
	p, err := determination.Compile(sched)
	if err != nil {
		// The full graph compiled at load; a subset that does not is the
		// inclusive constraints of DET-001 §7 refusing this content's shape.
		return determination.Determination{}, errs.New(errs.CategoryUnsupported, errs.ReasonInclusivePricingUnsupported, err.Error())
	}

	doc := determination.Document{Currency: r.doc.Currency}
	for _, it := range items {
		l := determination.Line{Key: it.key, Amount: it.amount, Quantity: it.quantity}
		for _, s := range it.selected {
			if s.treatment == TreatmentExempt && !asAssessed {
				continue
			}
			l.Applicable = append(l.Applicable, determination.ComponentID(planID(s.comp.decl.ID, s.treatment)))
		}
		doc.Lines = append(doc.Lines, l)
	}
	return p.Evaluate(doc)
}

// collect renders the evaluation into item results, totals and emitted
// slots.
func (r *run) collect(items []*item, planID func(string, string) string, actual determination.Determination, assessed *determination.Determination, out *Result) error {
	// Zero at the scale of the amounts beside it: canon/v1 keeps scale, so
	// "0" next to "0.80" would be a different assertion about precision.
	zero := actual.Lines[0].Net.Zero()
	var err error
	totals := Totals{Net: zero, CustomerTax: zero, ProviderAccrual: zero, ReverseCharge: zero}
	emitted := map[string]fiscal.Money{SlotCustomerTax: zero, SlotProviderAccrual: zero, SlotReverseCharge: zero}
	add := func(slot string, m fiscal.Money) error {
		prior, ok := emitted[slot]
		if !ok {
			emitted[slot] = m
			return nil
		}
		sum, err := prior.Add(m)
		emitted[slot] = sum
		return err
	}

	assessedBy := map[string]map[string]fiscal.Money{}
	if assessed != nil {
		for _, l := range assessed.Lines {
			assessedBy[l.Key] = map[string]fiscal.Money{}
			for _, c := range l.Components {
				assessedBy[l.Key][string(c.Component)] = c.Amount
			}
		}
	}

	for i, it := range items {
		lr := actual.Lines[i]
		net := lr.Net
		it.res.Net = &net
		if totals.Net, err = totals.Net.Add(net); err != nil {
			return err
		}
		amounts := map[string]determination.ComponentResult{}
		for _, c := range lr.Components {
			amounts[string(c.Component)] = c
		}
		for _, s := range it.selected {
			pid := planID(s.comp.decl.ID, s.treatment)
			cr := ComponentResult{
				ID: s.comp.decl.ID, Version: s.ver.decl.From, Regime: s.comp.regime.decl.ID,
				Jurisdiction: s.comp.decl.Jurisdiction, Authority: s.comp.decl.Authority,
				TaxType: s.comp.decl.TaxType, Name: s.comp.decl.Name, Emit: s.comp.decl.Emit,
				Treatment: s.treatment, RateKind: string(s.ver.rate.Kind), Rate: rateValue(s.ver), Unit: s.ver.perUnits,
				LegalReason: s.ver.reason, Responsibility: r.e.resp[s.apply.Responsibility],
				Source: s.ver.decl.Source, Citation: s.ver.decl.Citation, Charged: zero,
			}
			if s.exemption != nil {
				ex := *s.exemption
				none := zero
				if a, ok := assessedBy[it.key][pid]; ok {
					ex.Assessed = &a
					none = a.Zero()
				}
				cr.Exemption = &ex
				cr.LegalReason = ReasonExempt
				cr.Raw, cr.Amount, cr.Charged = none, none, none
				it.res.Components = append(it.res.Components, cr)
				continue
			}
			res, ok := amounts[pid]
			if !ok {
				return fmt.Errorf("taxation: component %s was selected for %s and the evaluation has no result for it", pid, it.key)
			}
			cr.Inclusive, cr.Base, cr.Raw, cr.Amount = res.Inclusive, res.Base, res.Raw, res.Amount
			cr.Charged = res.Amount.Zero()
			switch s.treatment {
			case TreatmentCustomerCharge:
				cr.Charged = res.Amount
				if totals.CustomerTax, err = totals.CustomerTax.Add(res.Amount); err != nil {
					return err
				}
				if err := add(SlotCustomerTax, res.Amount); err != nil {
					return err
				}
				if err := add(s.comp.decl.Emit, res.Amount); err != nil {
					return err
				}
			case TreatmentProviderCost:
				if totals.ProviderAccrual, err = totals.ProviderAccrual.Add(res.Amount); err != nil {
					return err
				}
				if err := add(SlotProviderAccrual, res.Amount); err != nil {
					return err
				}
				if err := add(s.comp.decl.Emit, res.Amount); err != nil {
					return err
				}
			case TreatmentReverseCharge:
				// The customer accounts for it. It is the seller's to
				// disclose, never to charge or to remit.
				if totals.ReverseCharge, err = totals.ReverseCharge.Add(res.Amount); err != nil {
					return err
				}
				if err := add(SlotReverseCharge, res.Amount); err != nil {
					return err
				}
			}
			it.res.Components = append(it.res.Components, cr)
		}
	}
	if totals.CustomerTotal, err = totals.Net.Add(totals.CustomerTax); err != nil {
		return err
	}
	out.Totals = &totals
	out.Emitted = emitted
	return nil
}

func rateValue(v version) string {
	switch v.rate.Kind {
	case determination.RateAdValorem:
		return v.rate.Proportion.CanonicalString()
	case determination.RateSpecific:
		return v.rate.PerUnit.CanonicalString()
	default:
		return v.rate.Flat.CanonicalString()
	}
}

func rateText(v version) string {
	switch v.rate.Kind {
	case determination.RateAdValorem:
		return "rate " + v.rate.Proportion.CanonicalString()
	case determination.RateSpecific:
		return fmt.Sprintf("%s %s per %s", v.rate.PerUnit.CanonicalString(), v.rate.PerUnit.Currency(), v.perUnits)
	default:
		return fmt.Sprintf("flat %s %s", v.rate.Flat.CanonicalString(), v.rate.Flat.Currency())
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
