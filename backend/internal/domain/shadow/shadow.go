// Package shadow compares ZoikoTax's decisions with another engine's results
// (ZTAX-SHD-001, W4 lane M).
//
// Two rules shape everything here:
//
//   - Shadow is non-impact. It reads committed decisions and writes only its
//     own runs, cases and discrepancies; nothing it finds changes a fiscal,
//     obligation, filing or payment record (ZTAX-SHD-REQ-0001).
//   - Agreement is not correctness. A comparator's profile decides what a
//     match may claim: matching a commercial engine is "matches comparator",
//     never "verified correct" (ZTAX-SHD-001 §1, the assurance claim rule).
//
// Comparison is component by component, through an explicit mapping from
// the comparator's codes to ZoikoTax's emitted slots. A code with no mapping
// stays UNMAPPED and is a category variance; it is never forced onto the
// nearest slot, and a matching total does not hide a component difference
// (ZTAX-SHD-001 §10, §12).
package shadow

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// ---------------------------------------------------------------------------
// comparators
// ---------------------------------------------------------------------------

// ComparatorType is what kind of engine or evidence a comparator is
// (ZTAX-SHD-001 §3).
type ComparatorType string

// The comparator types.
const (
	ComparatorCommercialEngine ComparatorType = "COMMERCIAL_TAX_ENGINE"
	ComparatorCustomerLegacy   ComparatorType = "CUSTOMER_LEGACY_ENGINE"
	ComparatorHistoricalBilled ComparatorType = "HISTORICAL_BILLED_RESULT"
	ComparatorAuthority        ComparatorType = "AUTHORITY_CALCULATOR"
	ComparatorReference        ComparatorType = "QUALIFIED_REFERENCE_IMPLEMENTATION"
	ComparatorGoldenVector     ComparatorType = "GOLDEN_VECTOR"
	ComparatorFilingResult     ComparatorType = "FILING_RESULT"
	ComparatorManualReview     ComparatorType = "MANUAL_TAX_REVIEW"
	ComparatorPriorRelease     ComparatorType = "PRIOR_ZOIKO_RELEASE"
)

// Valid reports whether the type is one of §3's.
func (t ComparatorType) Valid() bool {
	switch t {
	case ComparatorCommercialEngine, ComparatorCustomerLegacy, ComparatorHistoricalBilled, ComparatorAuthority,
		ComparatorReference, ComparatorGoldenVector, ComparatorFilingResult, ComparatorManualReview, ComparatorPriorRelease:
		return true
	}
	return false
}

// Claim is what agreement with a comparator may be presented as.
type Claim string

// The claims.
const (
	ClaimMatchesComparator Claim = "MATCHES_COMPARATOR"
	ClaimVerifiedCorrect   Claim = "VERIFIED_CORRECT"
	ClaimRegression        Claim = "NO_REGRESSION"
)

// Claim is the strongest statement a match against this comparator
// supports. Only an independent oracle — an authority's calculator, a
// qualified reference implementation, an approved golden vector or a
// qualified manual review — verifies correctness. A prior ZoikoTax release
// proves only that nothing changed.
func (t ComparatorType) Claim() Claim {
	switch t {
	case ComparatorAuthority, ComparatorReference, ComparatorGoldenVector, ComparatorManualReview:
		return ClaimVerifiedCorrect
	case ComparatorPriorRelease:
		return ClaimRegression
	}
	return ClaimMatchesComparator
}

// Comparator is a comparator profile (ZTAX-SHD-001 §4). Its version is
// UNKNOWN when the engine does not expose it, and that opacity is reported,
// not hidden.
type Comparator struct {
	ID       id.ComparatorID
	TenantID id.TenantID

	Name        string
	Type        ComparatorType
	Provider    string
	Version     string
	Environment string
	Limitations string

	CreatedAt time.Time
	CreatedBy id.UserID
}

// Validate refuses a profile that cannot be recorded.
func (c Comparator) Validate() error {
	switch {
	case strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Provider) == "":
		return fmt.Errorf("shadow: a comparator names itself and its provider")
	case !c.Type.Valid():
		return fmt.Errorf("shadow: comparator type %q is not one this service knows", c.Type)
	case strings.TrimSpace(c.Version) == "":
		return fmt.Errorf("shadow: a comparator states its version, or UNKNOWN")
	}
	return nil
}

// ---------------------------------------------------------------------------
// runs and cases
// ---------------------------------------------------------------------------

// Mode is what a run compares (ZTAX-SHD-001 §2).
type Mode string

// The modes this service runs.
const (
	ModeDualDetermination Mode = "DUAL_DETERMINATION"
	ModeHistoricalReplay  Mode = "HISTORICAL_REPLAY"
	ModeLiveShadow        Mode = "LIVE_SHADOW"
	ModeReleaseDiff       Mode = "RELEASE_DIFFERENTIAL"
)

// Valid reports whether the mode is one this service runs.
func (m Mode) Valid() bool {
	switch m {
	case ModeDualDetermination, ModeHistoricalReplay, ModeLiveShadow, ModeReleaseDiff:
		return true
	}
	return false
}

// Variance is a case's or component's comparison state (ZTAX-SHD-001 §11).
type Variance string

// The variance states.
const (
	VarianceExact        Variance = "EXACT_MATCH"
	VarianceEquivalent   Variance = "EQUIVALENT_MATCH"
	VarianceMonetary     Variance = "MONETARY_VARIANCE"
	VarianceCategory     Variance = "CATEGORY_VARIANCE"
	VariancePopulation   Variance = "POPULATION_VARIANCE"
	VarianceUncomparable Variance = "UNCOMPARABLE"
)

// Matched reports whether the state counts as agreement.
func (v Variance) Matched() bool { return v == VarianceExact || v == VarianceEquivalent }

// Pairing is how a case was paired (ZTAX-SHD-001 §8).
type Pairing string

// The pairings this service makes. Fuzzy pairing is not attempted: a
// guessed pair would count in matched accuracy (ZTAX-SHD-001 §8).
const (
	PairingExactID            Pairing = "EXACT_ID"
	PairingUnpairedComparator Pairing = "UNPAIRED_COMPARATOR"
	PairingUnpairedZoiko      Pairing = "UNPAIRED_ZOIKO"
)

// Unmapped names a comparator code with no mapping to a ZoikoTax slot.
const Unmapped = "UNMAPPED_EXTERNAL_CODE"

// Observation is one comparator result, as imported.
type Observation struct {
	// CaseRef is the business key the comparator result is for.
	CaseRef    string
	Components map[string]fiscal.Money
	Total      fiscal.Money
}

// ComponentComparison is one component compared.
type ComponentComparison struct {
	// ExternalCode is the comparator's code; empty for a ZoikoTax slot the
	// comparator has no counterpart for.
	ExternalCode string
	// Slot is the ZoikoTax slot it maps to, or Unmapped.
	Slot       string
	Comparator *fiscal.Money
	Zoiko      *fiscal.Money
	State      Variance
}

// Case is one compared transaction.
type Case struct {
	Seq      int
	CaseRef  string
	Decision id.DecisionID
	Pairing  Pairing
	State    Variance

	ComparatorTotal *fiscal.Money
	ZoikoTotal      *fiscal.Money
	// Variance is ZoikoTax's total minus the comparator's, when both exist.
	Variance   *fiscal.Money
	Components []ComponentComparison
	Material   bool
}

// Policy is how a run compares (ZTAX-SHD-001 §12). Tolerance classifies a
// difference as equivalent; it never changes either amount.
type Policy struct {
	Currency     fiscal.Currency
	ComponentMap map[string]string
	Tolerance    fiscal.Money
	Materiality  fiscal.Money
}

// Validate refuses a policy that cannot compare.
func (p Policy) Validate() error {
	switch {
	case p.Tolerance.Currency() != p.Currency || p.Materiality.Currency() != p.Currency:
		return fmt.Errorf("shadow: tolerance and materiality are in the run's currency")
	case p.Tolerance.Sign() < 0 || p.Materiality.Sign() < 0:
		return fmt.Errorf("shadow: tolerance and materiality are not negative")
	case len(p.ComponentMap) == 0:
		return fmt.Errorf("shadow: a run maps at least one comparator code to a ZoikoTax slot")
	}
	for code, slot := range p.ComponentMap {
		if strings.TrimSpace(code) == "" || strings.TrimSpace(slot) == "" {
			return fmt.Errorf("shadow: a component mapping names both codes")
		}
	}
	return nil
}

func abs(m fiscal.Money) fiscal.Money {
	if m.Sign() < 0 {
		return m.Neg()
	}
	return m
}

// within reports whether |a-b| is at most tol, and the difference a-b.
func within(a, b, tol fiscal.Money) (bool, fiscal.Money, error) {
	d, err := a.Sub(b)
	if err != nil {
		return false, fiscal.Money{}, err
	}
	c, err := abs(d).Cmp(tol)
	if err != nil {
		return false, fiscal.Money{}, err
	}
	return c <= 0, d, nil
}

// Compare compares one comparator observation with the ZoikoTax result
// paired to it. zoiko is the decision's emitted money by slot. The result is
// a case; nothing is netted: one component's excess never cancels another's
// shortfall (ZTAX-SHD-001 §12).
func Compare(p Policy, obs Observation, decision id.DecisionID, zoiko map[string]fiscal.Money) (Case, error) {
	c := Case{CaseRef: obs.CaseRef, Decision: decision, Pairing: PairingExactID}
	total := obs.Total
	c.ComparatorTotal = &total
	if obs.Total.Currency() != p.Currency {
		c.State = VarianceUncomparable
		return c, nil
	}
	for _, m := range zoiko {
		if m.Currency() != p.Currency {
			c.State = VarianceUncomparable
			return c, nil
		}
	}

	covered := map[string]bool{}
	codes := make([]string, 0, len(obs.Components))
	for code := range obs.Components {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	worst := VarianceExact
	rank := map[Variance]int{VarianceExact: 0, VarianceEquivalent: 1, VarianceMonetary: 2, VarianceCategory: 3}
	note := func(v Variance) {
		if rank[v] > rank[worst] {
			worst = v
		}
	}
	for _, code := range codes {
		amount := obs.Components[code]
		cc := ComponentComparison{ExternalCode: code, Comparator: &amount}
		slot, mapped := p.ComponentMap[code]
		if !mapped {
			cc.Slot, cc.State = Unmapped, VarianceCategory
			note(cc.State)
			c.Components = append(c.Components, cc)
			continue
		}
		cc.Slot = slot
		covered[slot] = true
		z, ok := zoiko[slot]
		if !ok {
			cc.State = VarianceCategory
			note(cc.State)
			c.Components = append(c.Components, cc)
			continue
		}
		zz := z
		cc.Zoiko = &zz
		eq, diff, err := within(z, amount, p.Tolerance)
		if err != nil {
			return Case{}, err
		}
		switch {
		case diff.IsZero():
			cc.State = VarianceExact
		case eq:
			cc.State = VarianceEquivalent
		default:
			cc.State = VarianceMonetary
		}
		note(cc.State)
		c.Components = append(c.Components, cc)
	}
	// A ZoikoTax component the comparator has no counterpart for is a
	// difference in composition, even when the totals agree.
	slots := make([]string, 0, len(zoiko))
	for slot := range zoiko {
		slots = append(slots, slot)
	}
	sort.Strings(slots)
	for _, slot := range slots {
		if covered[slot] || !slices.Contains(mappedSlots(p), slot) && zoiko[slot].IsZero() {
			continue
		}
		z := zoiko[slot]
		c.Components = append(c.Components, ComponentComparison{Slot: slot, Zoiko: &z, State: VarianceCategory})
		note(VarianceCategory)
	}

	var zt fiscal.Money
	first := true
	for _, slot := range slots {
		if first {
			zt, first = zoiko[slot], false
			continue
		}
		next, err := zt.Add(zoiko[slot])
		if err != nil {
			return Case{}, err
		}
		zt = next
	}
	if first {
		zt = p.Tolerance.Zero()
	}
	c.ZoikoTotal = &zt
	eq, diff, err := within(zt, obs.Total, p.Tolerance)
	if err != nil {
		return Case{}, err
	}
	c.Variance = &diff
	if !eq {
		note(VarianceMonetary)
	} else if !diff.IsZero() {
		note(VarianceEquivalent)
	}
	c.State = worst
	material, err := c.material(p)
	if err != nil {
		return Case{}, err
	}
	c.Material = material
	return c, nil
}

func mappedSlots(p Policy) []string {
	out := make([]string, 0, len(p.ComponentMap))
	for _, s := range p.ComponentMap {
		out = append(out, s)
	}
	return out
}

// Run is one executed comparison (ZTAX-SHD-001 §6): its comparator, mode,
// window and policy, the hash of the comparator input it was given, and what
// it found. It is written once.
type Run struct {
	ID         id.ShadowRunID
	TenantID   id.TenantID
	Comparator id.ComparatorID
	Mode       Mode
	WindowFrom time.Time
	WindowTo   time.Time
	Policy     Policy
	InputHash  canonical.Digest
	Metrics    Metrics

	CreatedAt time.Time
	CreatedBy id.UserID

	// Cases is nil on a run read as a summary.
	Cases []Case
}

// Unpaired is a case with no counterpart on one side. Unpaired records are
// population variances, reported apart from amount differences
// (ZTAX-SHD-001 §7).
func Unpaired(caseRef string, pairing Pairing, decision id.DecisionID, comparator, zoiko *fiscal.Money) Case {
	c := Case{CaseRef: caseRef, Decision: decision, Pairing: pairing, State: VariancePopulation,
		ComparatorTotal: comparator, ZoikoTotal: zoiko, Material: true}
	return c
}

// material reports whether a case needs investigation: any category
// difference, or a total difference at or above the materiality threshold.
func (c Case) material(p Policy) (bool, error) {
	switch c.State {
	case VarianceExact, VarianceEquivalent, VarianceUncomparable:
		return false, nil
	case VarianceCategory, VariancePopulation:
		return true, nil
	}
	if c.Variance == nil {
		return true, nil
	}
	cmp, err := abs(*c.Variance).Cmp(p.Materiality)
	if err != nil {
		return false, err
	}
	if cmp >= 0 {
		return true, nil
	}
	// Below the threshold on the total, but a component may still differ
	// by more: component quality is not netted.
	for _, cc := range c.Components {
		if cc.State != VarianceMonetary || cc.Comparator == nil || cc.Zoiko == nil {
			continue
		}
		d, err := cc.Zoiko.Sub(*cc.Comparator)
		if err != nil {
			return false, err
		}
		if cmp, err := abs(d).Cmp(p.Materiality); err != nil {
			return false, err
		} else if cmp >= 0 {
			return true, nil
		}
	}
	return false, nil
}

// Metrics summarise a run (ZTAX-SHD-001 §24). Value-weighted figures sit
// beside counts so that many small matches cannot hide one large error.
type Metrics struct {
	Cases            int
	Paired           int
	Exact            int
	Equivalent       int
	Monetary         int
	Category         int
	Population       int
	Uncomparable     int
	Material         int
	ComparedValue    fiscal.Money
	AbsoluteVariance fiscal.Money
}

// Summarise computes a run's metrics from its cases.
func Summarise(p Policy, cases []Case) (Metrics, error) {
	m := Metrics{Cases: len(cases), ComparedValue: p.Tolerance.Zero(), AbsoluteVariance: p.Tolerance.Zero()}
	for _, c := range cases {
		if c.Pairing == PairingExactID {
			m.Paired++
		}
		switch c.State {
		case VarianceExact:
			m.Exact++
		case VarianceEquivalent:
			m.Equivalent++
		case VarianceMonetary:
			m.Monetary++
		case VarianceCategory:
			m.Category++
		case VariancePopulation:
			m.Population++
		case VarianceUncomparable:
			m.Uncomparable++
		}
		if c.Material {
			m.Material++
		}
		if c.State == VarianceUncomparable || c.Pairing != PairingExactID {
			continue
		}
		if c.ComparatorTotal != nil {
			v, err := m.ComparedValue.Add(abs(*c.ComparatorTotal))
			if err != nil {
				return Metrics{}, err
			}
			m.ComparedValue = v
		}
		if c.Variance != nil {
			v, err := m.AbsoluteVariance.Add(abs(*c.Variance))
			if err != nil {
				return Metrics{}, err
			}
			m.AbsoluteVariance = v
		}
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// discrepancies
// ---------------------------------------------------------------------------

// Severity is a discrepancy's materiality (ZTAX-SHD-001 §16).
type Severity string

// The severities.
const (
	SeverityOne   Severity = "SHD-SEV-1"
	SeverityTwo   Severity = "SHD-SEV-2"
	SeverityThree Severity = "SHD-SEV-3"
	SeverityFour  Severity = "SHD-SEV-4"
)

// Valid reports whether the severity is one of §16's.
func (s Severity) Valid() bool {
	return s == SeverityOne || s == SeverityTwo || s == SeverityThree || s == SeverityFour
}

// RootCause is why a discrepancy happened (ZTAX-SHD-001 §14).
type RootCause string

// RootCauses are the root causes, in the order §14 lists them.
var RootCauses = []RootCause{
	"SOURCE_DATA", "CATALOG_MAPPING", "ONTOLOGY", "JURISDICTION_SITUS", "TAXABILITY", "REGULATORY_REVENUE",
	"EXEMPTION", "RESPONSIBILITY", "RATE_RULE", "EFFECTIVE_DATE", "ROUNDING_PRECISION", "BUNDLE_ALLOCATION",
	"CUSTOMER_CONFIGURATION", "COMPARATOR_LIMITATION", "ZOIKO_DEFECT", "CONTENT_COVERAGE_GAP", "LEGAL_AMBIGUITY",
	"TIMING_STATE", "MIGRATION_TRANSFORM", "UNKNOWN",
}

// Valid reports whether the cause is one of §14's.
func (r RootCause) Valid() bool { return slices.Contains(RootCauses, r) }

// State is where a discrepancy case is (ZTAX-SHD-001 §15).
type State string

// The discrepancy states.
const (
	StateNew                State = "NEW"
	StateTriaged            State = "TRIAGED"
	StateDataRequired       State = "DATA_REQUIRED"
	StateAnalysis           State = "ANALYSIS"
	StateSMEReview          State = "SME_REVIEW"
	StateComparatorQuery    State = "COMPARATOR_QUERY"
	StateZoikoDefect        State = "ZOIKO_DEFECT_CONFIRMED"
	StateComparatorIssue    State = "COMPARATOR_ISSUE_CONFIRMED"
	StateExpectedDifference State = "EXPECTED_DIFFERENCE_APPROVED"
	StateLegalAmbiguity     State = "LEGAL_AMBIGUITY"
	StateFixPending         State = "FIX_PENDING"
	StateRetestRequired     State = "RETEST_REQUIRED"
	StateResolved           State = "RESOLVED"
	StateAcceptedRisk       State = "ACCEPTED_RISK"
	StateClosed             State = "CLOSED"
)

var investigating = []State{StateTriaged, StateDataRequired, StateAnalysis, StateSMEReview, StateComparatorQuery}

// transitions is the discrepancy lifecycle. Triage comes first; a finding
// is reached from investigation; closure needs a resolution or a governed
// risk acceptance, never a straight jump from NEW.
var transitions = map[State][]State{
	StateNew:                {StateTriaged},
	StateTriaged:            append(slices.Clone(investigating[1:]), StateZoikoDefect, StateComparatorIssue, StateExpectedDifference, StateLegalAmbiguity),
	StateDataRequired:       {StateTriaged, StateAnalysis},
	StateAnalysis:           {StateDataRequired, StateSMEReview, StateComparatorQuery, StateZoikoDefect, StateComparatorIssue, StateExpectedDifference, StateLegalAmbiguity},
	StateSMEReview:          {StateAnalysis, StateZoikoDefect, StateComparatorIssue, StateExpectedDifference, StateLegalAmbiguity},
	StateComparatorQuery:    {StateAnalysis, StateComparatorIssue, StateZoikoDefect},
	StateZoikoDefect:        {StateFixPending},
	StateComparatorIssue:    {StateResolved, StateAcceptedRisk},
	StateExpectedDifference: {StateResolved},
	StateLegalAmbiguity:     {StateSMEReview, StateAcceptedRisk, StateResolved},
	StateFixPending:         {StateRetestRequired},
	StateRetestRequired:     {StateResolved, StateAnalysis},
	StateResolved:           {StateClosed},
	StateAcceptedRisk:       {StateClosed, StateAnalysis},
	StateClosed:             nil,
}

// CanTransition reports whether a move is permitted.
func CanTransition(from, to State) bool { return slices.Contains(transitions[from], to) }

// Open reports whether the discrepancy still blocks: anything before a
// resolution. ACCEPTED_RISK is not a match and still counts against cutover
// for SHD-SEV-1 (ZTAX-SHD-001 §15).
func (s State) Open() bool { return s != StateResolved && s != StateClosed }

// Event is one act on a discrepancy.
type Event struct {
	Seq        int
	State      State
	Severity   Severity
	RootCause  RootCause
	Note       string
	RecordedAt time.Time
	RecordedBy id.UserID
}

// Discrepancy is the investigation of one material case, folded from its
// events.
type Discrepancy struct {
	ID       id.DiscrepancyID
	TenantID id.TenantID
	Run      id.ShadowRunID
	CaseSeq  int

	State     State
	Severity  Severity
	RootCause RootCause
	History   []Event
}

// FoldDiscrepancy applies a discrepancy's events.
func FoldDiscrepancy(d Discrepancy, events []Event) (Discrepancy, error) {
	if len(events) == 0 || events[0].State != StateNew || events[0].Seq != 1 {
		return Discrepancy{}, fmt.Errorf("shadow: a discrepancy's history starts NEW")
	}
	d.State, d.Severity, d.RootCause = StateNew, events[0].Severity, events[0].RootCause
	for i, e := range events[1:] {
		if e.Seq != i+2 || !CanTransition(d.State, e.State) {
			return Discrepancy{}, fmt.Errorf("shadow: discrepancy event %d moves %s to %s", e.Seq, d.State, e.State)
		}
		d.State = e.State
		if e.Severity != "" {
			d.Severity = e.Severity
		}
		if e.RootCause != "" {
			d.RootCause = e.RootCause
		}
	}
	d.History = events
	return d, nil
}

// Move records the next step of an investigation. Every move says why;
// triage names a severity and a root-cause candidate; a finding of
// expected difference or accepted risk carries the evidence for it
// (ZTAX-SHD-001 §13, §15).
func (d Discrepancy) Move(to State, severity Severity, cause RootCause, note string, by id.UserID, at time.Time) (Event, error) {
	if !CanTransition(d.State, to) {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A discrepancy that is %s cannot move to %s.", d.State, to))
	}
	if strings.TrimSpace(note) == "" {
		return Event{}, errs.Invalid("note", errs.ReasonMissingField, "Every move of a discrepancy says why.")
	}
	if severity != "" && !severity.Valid() {
		return Event{}, errs.Invalid("severity", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a severity.", severity))
	}
	if cause != "" && !cause.Valid() {
		return Event{}, errs.Invalid("rootCause", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a root cause.", cause))
	}
	if to == StateTriaged && (severity == "" || cause == "") {
		return Event{}, errs.Invalid("severity", errs.ReasonMissingField, "Triage assigns a severity and a root-cause candidate.")
	}
	if by.IsZero() {
		return Event{}, errs.New(errs.CategoryPolicy, errs.ReasonForbidden, "Every discrepancy move is recorded against a person.")
	}
	return Event{Seq: len(d.History) + 1, State: to, Severity: severity, RootCause: cause, Note: note, RecordedAt: at.UTC(), RecordedBy: by}, nil
}

// Blockers is why a run's evidence does not support cutover
// (ZTAX-SHD-001 §25). An empty list is not an approval: cutover is a
// governed decision made elsewhere.
func Blockers(comparator Comparator, m Metrics, open []Discrepancy) []string {
	var out []string
	if m.Cases == 0 {
		out = append(out, "The run compared nothing.")
	}
	if m.Population > 0 {
		out = append(out, fmt.Sprintf("%d case(s) are unpaired; pairing coverage is incomplete.", m.Population))
	}
	if m.Uncomparable > 0 {
		out = append(out, fmt.Sprintf("%d case(s) could not be compared.", m.Uncomparable))
	}
	sev1 := 0
	unresolved := 0
	for _, d := range open {
		if !d.State.Open() {
			continue
		}
		unresolved++
		if d.Severity == SeverityOne {
			sev1++
		}
	}
	if sev1 > 0 {
		out = append(out, fmt.Sprintf("%d SHD-SEV-1 discrepancy(ies) unresolved.", sev1))
	}
	if unresolved > 0 {
		out = append(out, fmt.Sprintf("%d material discrepancy(ies) not yet resolved.", unresolved))
	}
	if comparator.Type.Claim() != ClaimVerifiedCorrect {
		out = append(out, "The comparator is not an independent oracle; matches are not verified correctness.")
	}
	if strings.EqualFold(comparator.Version, "UNKNOWN") {
		out = append(out, "The comparator's version is unknown.")
	}
	return out
}
