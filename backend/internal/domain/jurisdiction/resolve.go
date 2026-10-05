package jurisdiction

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Outcome is a situs resolution's single outcome (JUR-001 §9). Every one is a
// recorded decision; none is an error (JUR-REQ-0024).
type Outcome string

// The outcomes.
const (
	OutcomeResolved             Outcome = "RESOLVED"
	OutcomeInsufficientEvidence Outcome = "INSUFFICIENT_EVIDENCE"
	OutcomeAmbiguous            Outcome = "AMBIGUOUS"
	OutcomeUnsupported          Outcome = "UNSUPPORTED"
	// OutcomeDatasetDefect is overlapping boundaries at one level. It is the
	// one outcome that says our data is wrong rather than the input thin, and
	// the resolution raises an incident for it (JUR-001 §9.4).
	OutcomeDatasetDefect Outcome = "DATASET_DEFECT"
)

// Request is one resolution request: the canonical input's situs part.
type Request struct {
	// EventTime is when the taxable thing happened. Nodes, PPU periods and
	// ancestors are all resolved at it, never at decision time
	// (JUR-REQ-0015).
	EventTime time.Time
	// Ontology is the classification the rule is selected by.
	Ontology string
	Evidence []SitusEvidence
	// Covered lists the ISO countries an active pack covers. Resolution
	// outside them is UNSUPPORTED (JUR-001 §9.1). It is input rather than a
	// lookup because resolution reads nothing (JUR-REQ-0022).
	Covered []string
}

// Roaming records the derived roaming fact (JUR-001 §6.1, JUR-REQ-0017).
type Roaming struct {
	Home     string
	Visited  string
	Roaming  bool
	Sourcing RoamingSourcing
}

// Supply is the place-of-supply record of JUR-001 §7.1.
type Supply struct {
	SupplierJurisdiction ID
	CustomerJurisdiction ID
	PlaceOfSupply        ID
	// CustomerRole is what the evidence established; EffectiveRole is the
	// role after the pack's declared treatment of UNDETERMINED.
	CustomerRole  CustomerRole
	EffectiveRole CustomerRole
	// VATValidation is recorded whenever VAT identification evidence was
	// supplied, including when validation was unavailable (JUR-REQ-0021).
	VATValidation VATValidation
	CrossBorder   bool
}

// Step is one step of the resolution trace (JUR-001 §8.5, JUR-REQ-0027).
type Step struct {
	Step   string
	Detail string
}

// SitusResolution is the record of one resolution. It carries everything the
// replay envelope needs and everything a dispute asks about: the winning
// evidence, the evidence that was known and not used (JUR-REQ-0008), the
// dataset version (JUR-REQ-0003) and the trace.
type SitusResolution struct {
	Outcome      Outcome
	GraphVersion string
	RuleID       string
	Mode         Mode
	// Applicable is outermost-first with ties on id (JUR-REQ-0023).
	Applicable []Jurisdiction
	Winning    []SitusEvidence
	Unused     []SitusEvidence
	Dataset    *DatasetRef
	// NeededEvidence names, on INSUFFICIENT_EVIDENCE, the evidence types that
	// would have satisfied the rule (JUR-REQ-0025).
	NeededEvidence []EvidenceType
	Roaming        *Roaming
	Supply         Supply
	// Incident is set for DATASET_DEFECT.
	Incident bool
	Trace    []Step
}

// Resolver resolves situs. Its fields are the content bundle's graph and rule
// set and the boundary datasets the bundle pins; Resolve reads nothing else
// (JUR-REQ-0022).
type Resolver struct {
	Graph    *Graph
	Rules    *RuleSet
	Datasets map[string]*BoundaryDataset
}

// The trace step names.
const (
	StepRole       = "CUSTOMER_ROLE"
	StepSelect     = "SELECT_RULE"
	StepConsider   = "CONSIDER"
	StepWin        = "WIN"
	StepAgree      = "AGREE"
	StepSpatial    = "SPATIAL"
	StepHierarchy  = "HIERARCHY"
	StepSpecial    = "SPECIAL"
	StepRoaming    = "ROAMING"
	StepCoverage   = "COVERAGE"
	StepOutcome    = "OUTCOME"
	StepDiscarded  = "UNUSED"
	StepPlaceOfSup = "PLACE_OF_SUPPLY"
)

type resolution struct {
	r     *Resolver
	req   Request
	out   SitusResolution
	used  map[int]bool
	items []SitusEvidence
}

func (rs *resolution) step(kind, format string, args ...any) {
	rs.out.Trace = append(rs.out.Trace, Step{Step: kind, Detail: fmt.Sprintf(format, args...)})
}

// Resolve runs JUR-001 §8.3. It returns an error only for malformed input —
// evidence outside the vocabulary, an unpinned dataset — and every legitimate
// failure to resolve as an outcome.
func (r *Resolver) Resolve(req Request) (SitusResolution, error) {
	if r.Graph == nil || r.Rules == nil {
		return SitusResolution{}, fmt.Errorf("jurisdiction: resolver has no graph or no rule set")
	}
	if req.EventTime.IsZero() {
		return SitusResolution{}, fmt.Errorf("jurisdiction: resolution needs an event time")
	}
	items, err := canonicalise(req.Evidence)
	if err != nil {
		return SitusResolution{}, err
	}
	rs := &resolution{r: r, req: req, items: items, used: map[int]bool{}}
	rs.out.GraphVersion = r.Graph.Version()

	rs.customerRole()
	rule, ok := r.Rules.Select(req.Ontology, rs.out.Supply.EffectiveRole)
	if !ok {
		rs.step(StepSelect, "no situs rule selects ontology %q for role %s", req.Ontology, rs.out.Supply.EffectiveRole)
		rs.finish(OutcomeUnsupported)
		return rs.out, nil
	}
	rs.out.RuleID, rs.out.Mode = rule.ID, rule.Mode
	rs.step(StepSelect, "rule %s (%s) for ontology %q, role %s", rule.ID, rule.Mode, req.Ontology, rs.out.Supply.EffectiveRole)

	var primary ID
	var point *Point
	var outcome Outcome
	switch rule.Mode {
	case ModePrecedence:
		primary, outcome = rs.precedence(rule)
	case ModeQuorum:
		primary, outcome = rs.quorum(rule)
	case ModeSpatial:
		primary, point, outcome, err = rs.spatial(rule)
		if err != nil {
			return SitusResolution{}, err
		}
	}
	if outcome != OutcomeResolved {
		rs.finish(outcome)
		return rs.out, nil
	}

	chain, err := r.Graph.Chain(primary, req.EventTime)
	if err != nil {
		return SitusResolution{}, err
	}
	applicable := nodesToJurisdictions(chain)
	rs.step(StepHierarchy, "%s and %d ancestor(s) valid at %s", primary, len(chain)-1, req.EventTime.UTC().Format(time.RFC3339))

	if roam, roamOutcome := rs.roaming(rule, chain); roam != nil {
		rs.out.Roaming = roam
		if roamOutcome != OutcomeResolved {
			rs.finish(roamOutcome)
			return rs.out, nil
		}
		if applicable, err = rs.applyRoaming(roam, applicable); err != nil {
			return SitusResolution{}, err
		}
	}

	specials, err := rs.specials(applicable, point, rule)
	if err != nil {
		return SitusResolution{}, err
	}
	applicable = append(applicable, specials...)
	res := Resolution{Applicable: applicable}
	res.Sort()
	rs.out.Applicable = res.Applicable

	rs.placeOfSupply(primary)
	rs.finish(rs.coverage())
	return rs.out, nil
}

// customerRole settles the customer's role from VAT identification evidence
// and applies the pack's declared treatment of UNDETERMINED.
func (rs *resolution) customerRole() {
	role := RoleUndetermined
	status := VATValidation("")
	for _, e := range rs.items {
		if e.Type != EvidenceVATIdentification {
			continue
		}
		switch {
		case e.VATValidation == VATValidated:
			status, role = VATValidated, RoleB2B
		case e.VATValidation == VATValidationUnavailable && status != VATValidated:
			status = VATValidationUnavailable
		case status == "":
			status = VATUnvalidated
		}
	}
	rs.out.Supply.CustomerRole = role
	rs.out.Supply.VATValidation = status
	rs.out.Supply.EffectiveRole = role
	if role == RoleUndetermined {
		rs.out.Supply.EffectiveRole = rs.r.Rules.UndeterminedAs()
	}
	rs.step(StepRole, "evidence establishes %s (VAT %s); treated as %s",
		role, orNone(string(status)), rs.out.Supply.EffectiveRole)
}

// node maps an item to a graph node valid at event time. Country evidence maps
// to the country node; coordinates are only mapped by a spatial rule.
func (rs *resolution) node(e SitusEvidence) (Node, bool) {
	switch {
	case e.Jurisdiction != "":
		return rs.r.Graph.At(e.Jurisdiction, rs.req.EventTime)
	case e.Country != "":
		return rs.r.Graph.CountryAt(e.Country, rs.req.EventTime)
	}
	return Node{}, false
}

// precedence is §4.2: the first usable item in rule order wins, and evaluation
// stops there (JUR-REQ-0010). It never looks for agreement.
func (rs *resolution) precedence(rule SitusRule) (ID, Outcome) {
	for _, t := range rule.Order {
		if !rule.Reliability[t].AtLeast(rule.MinimumReliability) {
			rs.step(StepConsider, "%s is %s, below the rule's %s; not usable", t, rule.Reliability[t], rule.MinimumReliability)
			continue
		}
		var candidates []int
		for i, e := range rs.items {
			if e.Type != t {
				continue
			}
			if _, ok := rs.node(e); ok {
				candidates = append(candidates, i)
			} else {
				rs.step(StepConsider, "%s item does not map to a jurisdiction valid at event time", t)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		// Two usable items of the winning type that disagree are evidence
		// for two answers at one rank — AMBIGUOUS, not a pick.
		first, _ := rs.node(rs.items[candidates[0]])
		for _, i := range candidates[1:] {
			if n, _ := rs.node(rs.items[i]); n.ID != first.ID {
				for _, j := range candidates {
					rs.used[j] = true
				}
				rs.step(StepConsider, "%s items resolve to %s and %s", t, first.ID, n.ID)
				return "", OutcomeAmbiguous
			}
		}
		for _, i := range candidates {
			rs.used[i] = true
		}
		rs.step(StepWin, "%s -> %s; evaluation stops at the first usable item", t, first.ID)
		return first.ID, OutcomeResolved
	}
	rs.out.NeededEvidence = append([]EvidenceType(nil), rule.Order...)
	rs.step(StepOutcome, "no usable item of %s at or above %s", joinTypes(rule.Order), rule.MinimumReliability)
	return "", OutcomeInsufficientEvidence
}

// quorum is §4.3: at least Required items agreeing at the agreement level.
// Fewer is INSUFFICIENT_EVIDENCE; two answers each reaching the quorum is
// AMBIGUOUS. They do not collapse (JUR-REQ-0011).
func (rs *resolution) quorum(rule SitusRule) (ID, Outcome) {
	eligible := map[EvidenceType]bool{}
	for _, t := range rule.Eligible {
		eligible[t] = true
	}
	groups := map[ID][]int{}
	for i, e := range rs.items {
		if !eligible[e.Type] {
			continue
		}
		n, ok := rs.node(e)
		if !ok {
			rs.step(StepConsider, "%s item does not map to a jurisdiction valid at event time", e.Type)
			continue
		}
		at, ok := rs.r.Graph.AncestorAt(n.ID, rule.AgreementLevel, rs.req.EventTime)
		if !ok {
			rs.step(StepConsider, "%s item resolves to %s, which says nothing at %s level", e.Type, n.ID, rule.AgreementLevel)
			continue
		}
		groups[at.ID] = append(groups[at.ID], i)
		rs.step(StepAgree, "%s supports %s at %s level", e.Type, at.ID, rule.AgreementLevel)
	}
	var reached []ID
	for id, members := range groups {
		if len(members) >= rule.Required {
			reached = append(reached, id)
		}
	}
	sort.Slice(reached, func(i, j int) bool { return reached[i] < reached[j] })
	switch len(reached) {
	case 0:
		rs.out.NeededEvidence = append([]EvidenceType(nil), rule.Eligible...)
		rs.step(StepOutcome, "no jurisdiction has %d agreeing items of %s", rule.Required, joinTypes(rule.Eligible))
		return "", OutcomeInsufficientEvidence
	case 1:
		for _, i := range groups[reached[0]] {
			rs.used[i] = true
		}
		rs.step(StepWin, "%d items agree on %s", len(groups[reached[0]]), reached[0])
		return reached[0], OutcomeResolved
	default:
		for _, id := range reached {
			for _, i := range groups[id] {
				rs.used[i] = true
			}
		}
		rs.step(StepOutcome, "%s each reach the quorum of %d", joinIDs(reached), rule.Required)
		return "", OutcomeAmbiguous
	}
}

// spatial is §4.4 against the pinned dataset version.
func (rs *resolution) spatial(rule SitusRule) (ID, *Point, Outcome, error) {
	ds, ok := rs.r.Datasets[rule.Dataset]
	if !ok {
		// A pinned dataset the bundle does not carry is a broken bundle, not
		// thin input: there is no outcome that would be honest here.
		return "", nil, "", fmt.Errorf("jurisdiction: rule %s pins boundary dataset %s, which the resolver does not hold", rule.ID, rule.Dataset)
	}
	ref := ds.Ref()
	rs.out.Dataset = &ref
	var at = -1
	for _, t := range rule.CoordinateSource {
		for i, e := range rs.items {
			if e.Type == t && e.Point != nil {
				at = i
				break
			}
		}
		if at >= 0 {
			break
		}
	}
	if at < 0 {
		rs.out.NeededEvidence = append([]EvidenceType(nil), rule.CoordinateSource...)
		rs.step(StepOutcome, "no coordinates among %s", joinTypes(rule.CoordinateSource))
		return "", nil, OutcomeInsufficientEvidence, nil
	}
	rs.used[at] = true
	p := *rs.items[at].Point
	containing, err := ds.Containing(p)
	if err != nil {
		return "", nil, "", err
	}
	byLevel := map[Level][]Boundary{}
	for _, b := range containing {
		if b.Level != LevelSpecial {
			byLevel[b.Level] = append(byLevel[b.Level], b)
		}
	}
	// Levels in rank order, so which defect the trace names first does not
	// depend on map iteration.
	levels := make([]Level, 0, len(byLevel))
	for level := range byLevel {
		levels = append(levels, level)
	}
	sort.Slice(levels, func(i, j int) bool {
		ri, _ := levels[i].Rank()
		rj, _ := levels[j].Rank()
		return ri < rj
	})
	var deepest *Boundary
	deepestRank := -1
	for _, level := range levels {
		bs := byLevel[level]
		if len(bs) > 1 {
			// JUR-REQ-0013: two boundaries at one level contain the point.
			// That is a defect in the dataset, flagged, never a silent pick.
			rs.out.Incident = true
			rs.step(StepSpatial, "%s lies in %d %s boundaries in %s: %s",
				pointString(p), len(bs), level, ref, joinBoundaryIDs(bs))
			return "", &p, OutcomeDatasetDefect, nil
		}
		if rank, _ := level.Rank(); rank > deepestRank {
			deepestRank = rank
			b := bs[0]
			deepest = &b
		}
	}
	if deepest == nil {
		if rule.OnOutside == OutsideFallBackToParent {
			rs.step(StepSpatial, "%s is outside every boundary in %s; falling back to %s", pointString(p), ref, rule.Parent)
			return rule.Parent, &p, OutcomeResolved, nil
		}
		rs.out.NeededEvidence = append([]EvidenceType(nil), rule.CoordinateSource...)
		rs.step(StepSpatial, "%s is outside every boundary in %s", pointString(p), ref)
		return "", &p, OutcomeInsufficientEvidence, nil
	}
	rs.step(StepSpatial, "%s lies in %s (%s) in %s", pointString(p), deepest.Jurisdiction, deepest.Level, ref)
	return deepest.Jurisdiction, &p, OutcomeResolved, nil
}

// roaming derives the roaming fact (§6). It returns nil when the evidence
// cannot say whether the transaction roams.
func (rs *resolution) roaming(rule SitusRule, chain []Node) (*Roaming, Outcome) {
	var home, visited string
	for _, e := range rs.items {
		switch e.Type {
		case EvidenceSIMCountry:
			if home == "" {
				home = e.Country
			}
		case EvidenceNetworkLocation:
			if visited == "" {
				if n, ok := rs.node(e); ok {
					visited = n.Country
				} else if e.Point != nil && len(chain) > 0 {
					visited = chain[0].Country
				}
			}
		}
	}
	if home == "" || visited == "" {
		return nil, OutcomeResolved
	}
	r := &Roaming{Home: home, Visited: visited, Roaming: home != visited, Sourcing: rule.Roaming}
	if !r.Roaming {
		rs.step(StepRoaming, "home %s, visited %s: not roaming", home, visited)
		return r, OutcomeResolved
	}
	if rule.Roaming == "" {
		// JUR-REQ-0018: the runtime has no roaming default, so content that
		// does not declare one does not cover roaming.
		rs.step(StepRoaming, "home %s, visited %s: roaming, and rule %s declares no roaming sourcing", home, visited, rule.ID)
		return r, OutcomeUnsupported
	}
	rs.step(StepRoaming, "home %s, visited %s: roaming, sourced %s", home, visited, rule.Roaming)
	return r, OutcomeResolved
}

// applyRoaming builds the applicable set under the declared sourcing. Under
// BOTH, both claims are returned whole; nothing is apportioned here
// (JUR-REQ-0019) — that is determination's, with its own rounding and trace.
func (rs *resolution) applyRoaming(r *Roaming, resolved []Jurisdiction) ([]Jurisdiction, error) {
	home, ok := rs.r.Graph.CountryAt(r.Home, rs.req.EventTime)
	if !ok {
		return nil, fmt.Errorf("jurisdiction: home country %s is not in graph %s at event time", r.Home, rs.r.Graph.Version())
	}
	homeChain := []Jurisdiction{home.Jurisdiction}
	switch r.Sourcing {
	case RoamingHome:
		return homeChain, nil
	case RoamingVisited:
		return resolved, nil
	default: // RoamingBoth
		seen := map[ID]bool{}
		var out []Jurisdiction
		for _, j := range append(resolved, homeChain...) {
			if !seen[j.ID] {
				seen[j.ID] = true
				out = append(out, j)
			}
		}
		return out, nil
	}
}

// specials collects SPECIAL jurisdictions by membership and, for a spatial
// resolution, by boundary (§8.3 step 6). A point may fall in any number.
func (rs *resolution) specials(applicable []Jurisdiction, point *Point, rule SitusRule) ([]Jurisdiction, error) {
	ids := make([]ID, len(applicable))
	for i, j := range applicable {
		ids[i] = j.ID
	}
	found := map[ID]Jurisdiction{}
	for _, n := range rs.r.Graph.SpecialsFor(ids, rs.req.EventTime) {
		found[n.ID] = n.Jurisdiction
	}
	if point != nil && rule.Mode == ModeSpatial {
		containing, err := rs.r.Datasets[rule.Dataset].Containing(*point)
		if err != nil {
			return nil, err
		}
		for _, b := range containing {
			if b.Level != LevelSpecial {
				continue
			}
			if n, ok := rs.r.Graph.At(b.Jurisdiction, rs.req.EventTime); ok {
				found[n.ID] = n.Jurisdiction
			}
		}
	}
	out := make([]Jurisdiction, 0, len(found))
	for _, j := range found {
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID < out[k].ID })
	for _, j := range out {
		rs.step(StepSpecial, "%s applies", j.ID)
	}
	return out, nil
}

// placeOfSupply records §7.1. Reverse charge is not decided here (§7.4).
func (rs *resolution) placeOfSupply(primary ID) {
	s := &rs.out.Supply
	s.PlaceOfSupply = primary
	for _, e := range rs.items {
		var target *ID
		switch e.Type {
		case EvidenceSellerEstablishment:
			target = &s.SupplierJurisdiction
		case EvidenceCustomerEstablishment:
			target = &s.CustomerJurisdiction
		default:
			continue
		}
		if *target != "" {
			continue
		}
		if n, ok := rs.node(e); ok {
			*target = n.ID
		}
	}
	if s.SupplierJurisdiction != "" {
		sup, _ := rs.r.Graph.Node(s.SupplierJurisdiction)
		pos, _ := rs.r.Graph.Node(primary)
		s.CrossBorder = sup.Country != pos.Country
	}
	rs.step(StepPlaceOfSup, "supplier %s, customer %s, place of supply %s, cross-border %t",
		orNone(string(s.SupplierJurisdiction)), orNone(string(s.CustomerJurisdiction)), primary, s.CrossBorder)
}

// coverage downgrades a resolution outside every active pack to UNSUPPORTED.
// The applicable set is kept: "resolved, and we do not cover it" is a
// different fact from "could not resolve".
func (rs *resolution) coverage() Outcome {
	covered := map[string]bool{}
	for _, c := range rs.req.Covered {
		covered[c] = true
	}
	for _, j := range rs.out.Applicable {
		if j.Level == LevelCountry && !covered[j.Country] {
			rs.step(StepCoverage, "no active pack covers %s", j.Country)
			return OutcomeUnsupported
		}
	}
	return OutcomeResolved
}

// finish records the outcome and the unused evidence (JUR-REQ-0008).
func (rs *resolution) finish(o Outcome) {
	rs.out.Outcome = o
	rs.out.Winning, rs.out.Unused = nil, nil
	for i, e := range rs.items {
		if rs.used[i] {
			rs.out.Winning = append(rs.out.Winning, e)
		} else {
			rs.out.Unused = append(rs.out.Unused, e)
			rs.step(StepDiscarded, "%s (%s) recorded, not used", e.Type, e.Source)
		}
	}
	rs.step(StepOutcome, "%s", o)
}

// Canonical renders a resolution for the replay envelope and evidence. The
// trace and both evidence lists are in their deterministic order, so equal
// inputs give equal bytes (JUR-001 §10.1).
func (s SitusResolution) Canonical() canonical.Value {
	applicable := make([]canonical.Value, len(s.Applicable))
	for i, j := range s.Applicable {
		applicable[i] = canonical.Object(
			canonical.F("id", canonical.String(string(j.ID))),
			canonical.F("level", canonical.String(string(j.Level))),
			canonical.F("country", canonical.String(j.Country)),
		)
	}
	ev := func(items []SitusEvidence) canonical.Value {
		vs := make([]canonical.Value, len(items))
		for i, e := range items {
			vs[i] = e.Canonical()
		}
		return canonical.Array(vs...)
	}
	dataset := canonical.Absent()
	if s.Dataset != nil {
		dataset = canonical.Object(
			canonical.F("id", canonical.String(s.Dataset.ID)),
			canonical.F("version", canonical.String(s.Dataset.Version)),
			canonical.F("digest", canonical.String(s.Dataset.Digest.String())),
		)
	}
	needed := make([]canonical.Value, len(s.NeededEvidence))
	for i, t := range s.NeededEvidence {
		needed[i] = canonical.String(string(t))
	}
	roaming := canonical.Absent()
	if s.Roaming != nil {
		roaming = canonical.Object(
			canonical.F("home", canonical.String(s.Roaming.Home)),
			canonical.F("visited", canonical.String(s.Roaming.Visited)),
			canonical.F("roaming", canonical.Bool(s.Roaming.Roaming)),
			canonical.F("sourcing", canonical.OptString(string(s.Roaming.Sourcing))),
		)
	}
	trace := make([]canonical.Value, len(s.Trace))
	for i, t := range s.Trace {
		trace[i] = canonical.Object(canonical.F("step", canonical.String(t.Step)), canonical.F("detail", canonical.String(t.Detail)))
	}
	return canonical.Object(
		canonical.F("outcome", canonical.String(string(s.Outcome))),
		canonical.F("graphVersion", canonical.String(s.GraphVersion)),
		canonical.F("rule", canonical.OptString(s.RuleID)),
		canonical.F("mode", canonical.OptString(string(s.Mode))),
		canonical.F("applicable", canonical.Array(applicable...)),
		canonical.F("winning", ev(s.Winning)),
		canonical.F("unused", ev(s.Unused)),
		canonical.F("dataset", dataset),
		canonical.F("neededEvidence", canonical.Array(needed...)),
		canonical.F("roaming", roaming),
		canonical.F("supply", canonical.Object(
			canonical.F("supplier", canonical.OptString(string(s.Supply.SupplierJurisdiction))),
			canonical.F("customer", canonical.OptString(string(s.Supply.CustomerJurisdiction))),
			canonical.F("placeOfSupply", canonical.OptString(string(s.Supply.PlaceOfSupply))),
			canonical.F("customerRole", canonical.String(string(s.Supply.CustomerRole))),
			canonical.F("effectiveRole", canonical.String(string(s.Supply.EffectiveRole))),
			canonical.F("vatValidation", canonical.OptString(string(s.Supply.VATValidation))),
			canonical.F("crossBorder", canonical.Bool(s.Supply.CrossBorder)),
		)),
		canonical.F("incident", canonical.Bool(s.Incident)),
		canonical.F("trace", canonical.Array(trace...)),
	)
}

func nodesToJurisdictions(ns []Node) []Jurisdiction {
	out := make([]Jurisdiction, len(ns))
	for i, n := range ns {
		out[i] = n.Jurisdiction
	}
	return out
}

func joinTypes(ts []EvidenceType) string {
	s := make([]string, len(ts))
	for i, t := range ts {
		s[i] = string(t)
	}
	return strings.Join(s, ", ")
}

func joinIDs(ids []ID) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = string(id)
	}
	return strings.Join(s, ", ")
}

func joinBoundaryIDs(bs []Boundary) string {
	ids := make([]ID, len(bs))
	for i, b := range bs {
		ids[i] = b.Jurisdiction
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return joinIDs(ids)
}

func pointString(p Point) string { return "(" + p.Latitude + ", " + p.Longitude + ")" }

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
