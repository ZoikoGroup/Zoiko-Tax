// Package retention decides how long evidence is kept and whether anything
// stops it going when that time is up (ZTAX-EVID-001 §11–§13; ZTAX-PRIV-001
// §14; ZTAX-LEG-001's legal hold).
//
// Three rules shape it. There is no global retention period: a record is kept
// for what a versioned policy for its class and its legal entity's country
// says, and where no policy says, or two disagree, the answer is to keep it
// and escalate — never a guess (ZTAX-EVID-REQ-0022, -0092; ZTAX-PRIV-REQ-0052).
// A legal hold overrides disposition for what it scopes and nothing else: its
// scope is specific, never a tenant at large, so a hold cannot become a reason
// to keep unrelated personal data (ZTAX-EVID-REQ-0023; ZTAX-PRIV-REQ-0050,
// -0051). And nothing here rewrites a decision: retention and holds are
// records of their own, evaluated against the decision as it stands
// (ZTAX-EVID-REQ-0105).
package retention

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// RecordClass is a kind of retained record.
type RecordClass string

// The record classes a policy can govern.
const (
	ClassDecision RecordClass = "DECISION"
	ClassDocument RecordClass = "DOCUMENT"
	ClassJournal  RecordClass = "JOURNAL"
	ClassRefund   RecordClass = "REFUND"
)

// Valid reports whether c is a known class.
func (c RecordClass) Valid() bool {
	switch c {
	case ClassDecision, ClassDocument, ClassJournal, ClassRefund:
		return true
	}
	return false
}

// Trigger is the instant a retention period runs from.
type Trigger string

// The triggers.
const (
	// TriggerEventTime runs from when the taxable event happened.
	TriggerEventTime Trigger = "EVENT_TIME"
	// TriggerRecordedAt runs from when the record was made.
	TriggerRecordedAt Trigger = "RECORDED_AT"
	// TriggerYearEnd runs from the end of the calendar year of the event,
	// the common statutory form ("ten years from the end of the year").
	TriggerYearEnd Trigger = "EVENT_YEAR_END"
)

// Valid reports whether t is a known trigger.
func (t Trigger) Valid() bool {
	return t == TriggerEventTime || t == TriggerRecordedAt || t == TriggerYearEnd
}

// Policy is one version of a retention policy. Versions are append-only: a
// change is a new version, and the version a verdict used is named in it
// (ZTAX-EVID-REQ-0052, -0126).
type Policy struct {
	ID      string
	Version int
	Class   RecordClass
	// Country is the ISO 3166-1 alpha-2 country of the legal entity whose
	// records the policy governs.
	Country string
	Years   int
	Trigger Trigger
	// EffectiveFrom is when this version starts to govern.
	EffectiveFrom time.Time
	Citation      string
	RecordedAt    time.Time
	RecordedBy    id.UserID
}

// MaxYears bounds a policy. Nothing statutory runs longer; a figure above it
// is a typing error, not a policy.
const MaxYears = 100

// Validate refuses a policy that does not say what, where, how long and why.
func (p Policy) Validate() error {
	switch {
	case strings.TrimSpace(p.ID) == "" || len(p.ID) > 128:
		return fmt.Errorf("retention: a policy has an id of at most 128 characters")
	case p.Version < 1:
		return fmt.Errorf("retention: policy %s version %d", p.ID, p.Version)
	case !p.Class.Valid():
		return fmt.Errorf("retention: policy %s governs class %q", p.ID, p.Class)
	case len(p.Country) != 2 || strings.ToUpper(p.Country) != p.Country:
		return fmt.Errorf("retention: policy %s names country %q; a country is ISO 3166-1 alpha-2", p.ID, p.Country)
	case p.Years < 1 || p.Years > MaxYears:
		return fmt.Errorf("retention: policy %s keeps for %d years", p.ID, p.Years)
	case !p.Trigger.Valid():
		return fmt.Errorf("retention: policy %s runs from %q", p.ID, p.Trigger)
	case p.EffectiveFrom.IsZero():
		return fmt.Errorf("retention: policy %s has no effective date", p.ID)
	case strings.TrimSpace(p.Citation) == "":
		// A retention period is law or contract; one that cites nothing is
		// a number somebody chose.
		return fmt.Errorf("retention: policy %s cites no authority", p.ID)
	}
	return nil
}

// Record is what a verdict is about: one retained record's class and the
// facts its retention runs from.
type Record struct {
	Class       RecordClass
	Ref         string
	LegalEntity id.LegalEntityID
	// Country is the legal entity's country; empty when nobody recorded it.
	Country     string
	BusinessKey string
	Decision    id.DecisionID
	EventTime   time.Time
	RecordedAt  time.Time
}

// Outcome is what a verdict decided.
type Outcome string

// The outcomes.
const (
	// OutcomeRetain: inside its retention period.
	OutcomeRetain Outcome = "RETAIN"
	// OutcomeHeld: past its period, or not, and under an active hold.
	OutcomeHeld Outcome = "HELD"
	// OutcomeEligible: past its period, under no hold. Disposition may
	// proceed — after re-checking holds at the moment it acts
	// (ZTAX-EVID-REQ-0026).
	OutcomeEligible Outcome = "ELIGIBLE"
	// OutcomeNoPolicy: no policy governs it. Kept, and escalated.
	OutcomeNoPolicy Outcome = "NO_POLICY"
	// OutcomeConflicted: more than one policy governs it, disagreeing. Kept,
	// and escalated.
	OutcomeConflicted Outcome = "CONFLICTED"
)

// Disposable reports whether an outcome lets disposition proceed.
func (o Outcome) Disposable() bool { return o == OutcomeEligible }

// Verdict is the decision for one record at one instant, with what it rested
// on, so it can be reproduced (ZTAX-EVID-REQ-0126).
type Verdict struct {
	Outcome     Outcome
	Policy      *Policy
	Candidates  []Policy
	RetainUntil time.Time
	Holds       []id.LegalHoldID
	Detail      string
}

// governing returns, for each policy id, the latest version effective at at.
func governing(policies []Policy, at time.Time) []Policy {
	latest := map[string]Policy{}
	for _, p := range policies {
		if p.EffectiveFrom.After(at) {
			continue
		}
		if cur, ok := latest[p.ID]; !ok || p.Version > cur.Version {
			latest[p.ID] = p
		}
	}
	out := make([]Policy, 0, len(latest))
	for _, p := range latest {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RetainUntil is when a policy's period ends for a record.
func (p Policy) RetainUntil(r Record) time.Time {
	var from time.Time
	switch p.Trigger {
	case TriggerRecordedAt:
		from = r.RecordedAt
	case TriggerYearEnd:
		from = time.Date(r.EventTime.UTC().Year()+1, time.January, 1, 0, 0, 0, 0, time.UTC)
	default:
		from = r.EventTime
	}
	return from.UTC().AddDate(p.Years, 0, 0)
}

// Evaluate decides a record's retention at now, from the policies and holds
// as they stand. Policies are matched by class and country, each by its
// latest version in effect at now; holds by whether they are active and
// scope the record.
func Evaluate(r Record, policies []Policy, holds []Hold, now time.Time) Verdict {
	var v Verdict
	for _, h := range holds {
		if h.Active() && h.Scope.Covers(r) {
			v.Holds = append(v.Holds, h.ID)
		}
	}
	var matches []Policy
	for _, p := range governing(policies, now) {
		if p.Class == r.Class && p.Country == r.Country {
			matches = append(matches, p)
		}
	}
	v.Candidates = matches
	switch {
	case r.Country == "":
		v.Outcome, v.Detail = OutcomeNoPolicy, "the record's legal entity has no recorded country, so no policy can govern it"
	case len(matches) == 0:
		v.Outcome, v.Detail = OutcomeNoPolicy, fmt.Sprintf("no %s policy governs %s records", r.Country, r.Class)
	default:
		until := matches[0].RetainUntil(r)
		for _, m := range matches[1:] {
			if !m.RetainUntil(r).Equal(until) {
				v.Outcome = OutcomeConflicted
				v.Detail = fmt.Sprintf("%d policies govern %s %s records and disagree", len(matches), r.Country, r.Class)
				return withHolds(v)
			}
		}
		p := matches[0]
		v.Policy, v.RetainUntil = &p, until
		if now.Before(until) {
			v.Outcome = OutcomeRetain
		} else {
			v.Outcome = OutcomeEligible
		}
	}
	return withHolds(v)
}

// withHolds lets an active hold override whatever retention decided: a held
// record is never eligible, and a record with no policy is still reported as
// held when it is (ZTAX-EVID-REQ-0023).
func withHolds(v Verdict) Verdict {
	if len(v.Holds) > 0 && (v.Outcome == OutcomeEligible || v.Outcome == OutcomeRetain) {
		v.Outcome = OutcomeHeld
	}
	return v
}

// ---------------------------------------------------------------------------
// legal hold
// ---------------------------------------------------------------------------

// Scope is what a hold covers. It is specific by construction: named
// decisions or business keys, or a bounded window of events, optionally
// narrowed to one legal entity — never a whole tenant
// (ZTAX-PRIV-REQ-0051).
type Scope struct {
	LegalEntity  id.LegalEntityID
	BusinessKeys []string
	Decisions    []id.DecisionID
	EventFrom    time.Time
	EventTo      time.Time
}

// MaxScopeWindow bounds a window-only hold. A matter that needs longer names
// what it needs.
const MaxScopeWindow = 10 * 366 * 24 * time.Hour

// Validate refuses a scope that is not specific.
func (s Scope) Validate() error {
	named := len(s.BusinessKeys) + len(s.Decisions)
	windowed := !s.EventFrom.IsZero() || !s.EventTo.IsZero()
	switch {
	case named == 0 && !windowed:
		return fmt.Errorf("retention: a hold names decisions or business keys, or a bounded window of events")
	case windowed && (s.EventFrom.IsZero() || s.EventTo.IsZero() || !s.EventTo.After(s.EventFrom)):
		return fmt.Errorf("retention: a hold's window has a start and an end after it")
	case named == 0 && s.EventTo.Sub(s.EventFrom) > MaxScopeWindow:
		return fmt.Errorf("retention: a window-only hold covers at most ten years; name what the matter needs")
	case len(s.BusinessKeys) > 1000 || len(s.Decisions) > 1000:
		return fmt.Errorf("retention: a hold names at most 1000 decisions and 1000 business keys")
	}
	for _, k := range s.BusinessKeys {
		if strings.TrimSpace(k) == "" || len(k) > 255 {
			return fmt.Errorf("retention: a held business key is between 1 and 255 characters")
		}
	}
	return nil
}

// Covers reports whether the scope covers a record. Every criterion given
// must hold: a window narrows named keys, a legal entity narrows both.
func (s Scope) Covers(r Record) bool {
	if !s.LegalEntity.IsZero() && s.LegalEntity != r.LegalEntity {
		return false
	}
	named := len(s.BusinessKeys) + len(s.Decisions)
	if named > 0 && !slices.Contains(s.BusinessKeys, r.BusinessKey) && !slices.Contains(s.Decisions, r.Decision) {
		return false
	}
	if !s.EventFrom.IsZero() && (r.EventTime.Before(s.EventFrom) || !r.EventTime.Before(s.EventTo)) {
		return false
	}
	return true
}

// HoldStatus is a hold's state.
type HoldStatus string

// The hold statuses. RELEASED is final: a matter that revives places a new
// hold, so the record of what was released, when and why stands.
const (
	HoldActive   HoldStatus = "ACTIVE"
	HoldReleased HoldStatus = "RELEASED"
)

// EventKind is what a hold event did.
type EventKind string

// The hold event kinds.
const (
	EventPlaced       EventKind = "PLACED"
	EventScopeChanged EventKind = "SCOPE_CHANGED"
	EventReleased     EventKind = "RELEASED"
)

// HoldEvent is one entry in a hold's history: every placement, scope change
// and release, with who and why (ZTAX-EVID-REQ-0053).
type HoldEvent struct {
	Hold       id.LegalHoldID
	Seq        int
	Kind       EventKind
	Scope      Scope
	Reason     string
	RecordedAt time.Time
	RecordedBy id.UserID
}

// Hold is a legal hold as its history leaves it.
type Hold struct {
	ID       id.LegalHoldID
	TenantID id.TenantID
	// Matter is the claim, investigation or request the hold serves
	// (ZTAX-LEG-REQ-0091).
	Matter  string
	Status  HoldStatus
	Scope   Scope
	History []HoldEvent
}

// Active reports whether the hold is in force.
func (h Hold) Active() bool { return h.Status == HoldActive }

// Fold builds a hold from its events, oldest first.
func Fold(holdID id.LegalHoldID, tenant id.TenantID, matter string, events []HoldEvent) (Hold, error) {
	h := Hold{ID: holdID, TenantID: tenant, Matter: matter, History: events}
	for i, e := range events {
		if e.Seq != i+1 {
			return Hold{}, fmt.Errorf("retention: hold %s event %d has sequence %d", holdID, i+1, e.Seq)
		}
		switch e.Kind {
		case EventPlaced:
			if i != 0 {
				return Hold{}, fmt.Errorf("retention: hold %s is placed twice", holdID)
			}
			h.Status, h.Scope = HoldActive, e.Scope
		case EventScopeChanged:
			if h.Status != HoldActive {
				return Hold{}, fmt.Errorf("retention: hold %s changes scope while %s", holdID, h.Status)
			}
			h.Scope = e.Scope
		case EventReleased:
			if h.Status != HoldActive {
				return Hold{}, fmt.Errorf("retention: hold %s is released while %s", holdID, h.Status)
			}
			h.Status = HoldReleased
		default:
			return Hold{}, fmt.Errorf("retention: hold %s event kind %q", holdID, e.Kind)
		}
	}
	if len(events) == 0 {
		return Hold{}, fmt.Errorf("retention: hold %s has no history", holdID)
	}
	return h, nil
}
