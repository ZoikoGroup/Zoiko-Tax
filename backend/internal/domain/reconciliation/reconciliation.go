// Package reconciliation is the seven-way trace of ZTAX-FIN-001 §17–§20:
// calculated, documented, collected, posted, reported, remitted and exported
// tax compared stage by stage, so that a mismatch at one stage is never hidden
// by agreement at another.
//
// Two rules matter most. A tolerance match is not a match: it is its own
// status and keeps its variance (ZTAX-FIN-REQ-0078, -0081). And no exception
// is resolved without a recorded resolver, reason, action and evidence
// (ZTAX-FIN-REQ-0084) — which is also why an AI ranking of candidate causes
// never resolves anything by itself (ZTAX-FIN-REQ-0086).
package reconciliation

import (
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// Stage is one of FIN-001 §17's seven stages.
type Stage string

// The stages.
const (
	// R1CalculatedToDocument: TaxDecision amounts against document tax lines.
	R1CalculatedToDocument Stage = "R1_CALCULATED_TO_DOCUMENT"
	// R2DocumentToCollection: invoiced/credited/refunded tax against
	// collection state.
	R2DocumentToCollection Stage = "R2_DOCUMENT_TO_COLLECTION"
	// R3DocumentToSubledger: fiscal events against TCSL journals.
	R3DocumentToSubledger Stage = "R3_DOCUMENT_TO_SUBLEDGER"
	// R4SubledgerToReturn: the control population against return amounts.
	R4SubledgerToReturn Stage = "R4_SUBLEDGER_TO_RETURN"
	// R5ReturnToRemittance: reported liabilities against remittance evidence.
	R5ReturnToRemittance Stage = "R5_RETURN_TO_REMITTANCE"
	// R6SubledgerToGL: TCSL export against the customer's GL acknowledgement.
	R6SubledgerToGL Stage = "R6_SUBLEDGER_TO_GL"
	// R7EndToEnd: the whole lineage.
	R7EndToEnd Stage = "R7_END_TO_END"
)

// Stages is every stage, in trace order.
var Stages = []Stage{
	R1CalculatedToDocument, R2DocumentToCollection, R3DocumentToSubledger,
	R4SubledgerToReturn, R5ReturnToRemittance, R6SubledgerToGL, R7EndToEnd,
}

// Status is FIN-001 §18's ReconStatus.
type Status string

// The statuses.
const (
	StatusMatched        Status = "MATCHED"
	StatusToleranceMatch Status = "TOLERANCE_MATCH"
	StatusUnmatched      Status = "UNMATCHED"
	StatusPartial        Status = "PARTIAL"
	StatusDuplicate      Status = "DUPLICATE"
	StatusMissing        Status = "MISSING"
	StatusConflicted     Status = "CONFLICTED"
	StatusPending        Status = "PENDING"
	StatusExplained      Status = "EXPLAINED"
	StatusResolved       Status = "RESOLVED"
)

// Exception reports whether a status needs work.
func (s Status) Exception() bool {
	switch s {
	case StatusMatched, StatusToleranceMatch, StatusResolved, StatusExplained:
		return false
	}
	return true
}

// RootCause is FIN-001 §18's root-cause taxonomy (ZTAX-FIN-REQ-0083).
type RootCause string

// The root causes.
const (
	CauseClassification RootCause = "CLASSIFICATION"
	CauseJurisdiction   RootCause = "JURISDICTION"
	CauseTaxRule        RootCause = "TAX_RULE"
	CauseRounding       RootCause = "ROUNDING"
	CauseFX             RootCause = "FX"
	CauseDocument       RootCause = "DOCUMENT"
	CauseCollection     RootCause = "COLLECTION"
	CausePosting        RootCause = "POSTING"
	CauseReturn         RootCause = "RETURN"
	CauseRemittance     RootCause = "REMITTANCE"
	CauseGL             RootCause = "GL"
	CauseTiming         RootCause = "TIMING"
	CauseData           RootCause = "DATA"
	CauseOther          RootCause = "OTHER"
)

var causes = map[RootCause]bool{
	CauseClassification: true, CauseJurisdiction: true, CauseTaxRule: true, CauseRounding: true, CauseFX: true,
	CauseDocument: true, CauseCollection: true, CausePosting: true, CauseReturn: true, CauseRemittance: true,
	CauseGL: true, CauseTiming: true, CauseData: true, CauseOther: true,
}

// Valid reports whether c is in the taxonomy.
func (c RootCause) Valid() bool { return causes[c] }

// Tolerance is an explicit tolerance for one stage and currency
// (ZTAX-FIN-REQ-0080). There is no global penny tolerance: a stage and
// currency with no Tolerance has none.
type Tolerance struct {
	Stage    Stage
	Currency fiscal.Currency
	Absolute fiscal.Money
}

// Policy is a run's tolerance set.
type Policy struct {
	Tolerances []Tolerance
}

func (p Policy) tolerance(s Stage, c fiscal.Currency) (fiscal.Money, bool) {
	for _, t := range p.Tolerances {
		if t.Stage == s && t.Currency == c {
			return t.Absolute, true
		}
	}
	return fiscal.Money{}, false
}

// MatchKey is the deterministic key an item is compared on: canonical ids
// first, source ids and document numbers with legal entity, period and
// authority where those are what the stage has.
type MatchKey string

// Item is one comparison.
type Item struct {
	Stage    Stage
	Key      MatchKey
	Expected *fiscal.Money
	Observed *fiscal.Money
	// Variance is Observed - Expected, exact, whatever the status. A tolerance
	// match keeps it; materiality never rewrites it (ZTAX-FIN-REQ-0081).
	Variance  *fiscal.Money
	Status    Status
	Cause     RootCause
	Statutory bool
	Detail    string
}

// Compare compares one expected amount with one observed amount at a stage.
//
// Amounts are compared in their native currencies first (ZTAX-FIN-REQ-0031):
// two currencies cannot be matched here, and the item is CONFLICTED with an
// FX cause, classified separately from every other variance
// (ZTAX-FIN-REQ-0032). statutory marks a comparison the law requires to be
// exact, and it overrides any customer tolerance (ZTAX-FIN-REQ-0082).
func Compare(stage Stage, key MatchKey, expected, observed *fiscal.Money, policy Policy, statutory bool) (Item, error) {
	it := Item{Stage: stage, Key: key, Expected: expected, Observed: observed, Statutory: statutory}
	switch {
	case expected == nil && observed == nil:
		return Item{}, fmt.Errorf("reconciliation: %s %s compares nothing with nothing", stage, key)
	case expected == nil:
		it.Status, it.Detail = StatusUnmatched, "observed with nothing expected"
		return it, nil
	case observed == nil:
		it.Status, it.Detail = StatusMissing, "expected and not observed"
		return it, nil
	}
	if expected.Currency() != observed.Currency() {
		it.Status, it.Cause = StatusConflicted, CauseFX
		it.Detail = fmt.Sprintf("expected in %s, observed in %s; compared natively, not converted", expected.Currency(), observed.Currency())
		return it, nil
	}
	v, err := observed.Sub(*expected)
	if err != nil {
		return Item{}, err
	}
	it.Variance = &v
	if v.IsZero() {
		it.Status = StatusMatched
		return it, nil
	}
	if !statutory {
		if tol, ok := policy.tolerance(stage, expected.Currency()); ok {
			mag := v
			if mag.Sign() < 0 {
				mag = mag.Neg()
			}
			if c, err := mag.Cmp(tol); err == nil && c <= 0 {
				it.Status = StatusToleranceMatch
				it.Detail = fmt.Sprintf("within the %s %s tolerance", stage, tol.CanonicalString())
				return it, nil
			}
		}
	}
	// Same direction and smaller than expected: part of it arrived.
	if observed.Sign() == expected.Sign() && observed.Sign() != 0 {
		if c, err := absCmp(*observed, *expected); err == nil && c < 0 {
			it.Status = StatusPartial
			return it, nil
		}
	}
	it.Status = StatusUnmatched
	return it, nil
}

func absCmp(a, b fiscal.Money) (int, error) {
	if a.Sign() < 0 {
		a = a.Neg()
	}
	if b.Sign() < 0 {
		b = b.Neg()
	}
	return a.Cmp(b)
}

// Observation is one observed record for duplicate detection.
type Observation struct {
	Key    MatchKey
	Source string
	Amount fiscal.Money
}

// Duplicates returns the keys observed more than once, sorted. A duplicate is
// flagged rather than collapsed, so the duplicate source evidence survives
// while the double posting, reporting or remittance it would cause is
// prevented (ZTAX-FIN-REQ-0087).
func Duplicates(obs []Observation) []MatchKey {
	count := map[MatchKey]int{}
	for _, o := range obs {
		count[o.Key]++
	}
	var out []MatchKey
	for k, n := range count {
		if n > 1 {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Actor is who resolved an exception.
type Actor string

// The actors.
const (
	ActorHuman Actor = "HUMAN"
	// ActorAI is an A3 constrained-automation resolution. It is accepted only
	// for an immaterial exception under an approved auto-resolution policy.
	ActorAI Actor = "AI_A3"
)

// Action is the resolution action. A source-system correction and a ZoikoTax
// adjustment are distinct actions.
type Action string

// The actions.
const (
	ActionAdjust             Action = "ADJUST"
	ActionReclassify         Action = "RECLASSIFY"
	ActionAmend              Action = "AMEND"
	ActionWait               Action = "WAIT"
	ActionWaiveWithApproval  Action = "WAIVE_WITH_APPROVAL"
	ActionExternalCorrection Action = "EXTERNAL_CORRECTION"
)

// Resolution is the record every resolved exception carries.
type Resolution struct {
	Actor    Actor
	Resolver id.UserID
	Reason   string
	Action   Action
	Cause    RootCause
	Evidence []string
	At       time.Time
	// Material is the exception's materiality under the run's policy, and
	// AutoPolicy names the approved A3 policy an AI resolution relies on.
	Material   bool
	AutoPolicy string
}

// Resolve resolves an exception item, returning the resolved copy.
func Resolve(it Item, r Resolution) (Item, error) {
	if !it.Status.Exception() {
		return Item{}, fmt.Errorf("reconciliation: %s %s is %s, not an exception", it.Stage, it.Key, it.Status)
	}
	if r.Reason == "" || r.Action == "" || len(r.Evidence) == 0 || r.At.IsZero() || !r.Cause.Valid() {
		return Item{}, fmt.Errorf("reconciliation: a resolution records its reason, action, root cause, evidence and time")
	}
	switch r.Actor {
	case ActorHuman:
		if r.Resolver.IsZero() {
			return Item{}, fmt.Errorf("reconciliation: a human resolution names its resolver")
		}
	case ActorAI:
		// ZTAX-FIN-REQ-0086: AI may rank; it resolves only an immaterial
		// exception under an approved policy, never a material one.
		if r.Material || r.AutoPolicy == "" {
			return Item{}, fmt.Errorf("reconciliation: an AI may not resolve a material exception or act without an approved A3 policy")
		}
	default:
		return Item{}, fmt.Errorf("reconciliation: resolution actor %q", r.Actor)
	}
	out := it
	out.Status, out.Cause = StatusResolved, r.Cause
	return out, nil
}

// StageResult is one stage's outcome for a lineage.
type StageResult struct {
	Stage     Stage
	Available bool
	Items     []Item
}

// Trace is R7: every stage of one lineage, in order, each with its own
// verdict. It reports the stages that are not yet available as such rather
// than omitting them (ZTAX-FIN-REQ-0077), and its verdict is the first
// exception in stage order — a clean R5 cannot hide a broken R1.
type Trace struct {
	Stages      []StageResult
	FirstBreak  *Stage
	Unavailable []Stage
}

// EndToEnd assembles the R7 trace from the six stage results.
func EndToEnd(results map[Stage]StageResult) Trace {
	var t Trace
	for _, s := range Stages[:6] {
		r, ok := results[s]
		if !ok || !r.Available {
			t.Unavailable = append(t.Unavailable, s)
			t.Stages = append(t.Stages, StageResult{Stage: s})
			continue
		}
		t.Stages = append(t.Stages, r)
		if t.FirstBreak == nil {
			for _, it := range r.Items {
				if it.Status.Exception() {
					s := s
					t.FirstBreak = &s
					break
				}
			}
		}
	}
	return t
}

// Run is one reconciliation of a legal period: what was compared, when, by
// whom, and what the end-to-end trace found. A run is a record, never
// recomputed in place — a later run of the same period is a run of its own.
type Run struct {
	ID          id.ReconciliationID
	TenantID    id.TenantID
	LegalEntity id.LegalEntityID
	Period      string
	RanAt       time.Time
	RanBy       id.UserID
	FirstBreak  *Stage
	Unavailable []Stage
}

// RunItem is one compared item of a run, with its own identity so it can be
// resolved.
type RunItem struct {
	ID  id.ReconItemID
	Run id.ReconciliationID
	Item
}
