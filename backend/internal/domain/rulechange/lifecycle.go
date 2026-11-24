package rulechange

import (
	"fmt"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// Status is a proposal's place in the queue.
//
//	PROPOSED ─recommend (reviewer ≠ proposer)─▶ RECOMMENDED ─approve (≠ proposer, ≠ recommender)─▶ APPROVED_FOR_RELEASE
//	   │  ▲                                        │
//	   │  └── test evidence recorded               ├─▶ CHANGES_REQUESTED ─(new proposal supersedes)─▶ SUPERSEDED
//	   ├──────────────────────────────────────────┼─▶ REJECTED
//	   └──────────────────────────────────────────┴─▶ WITHDRAWN (by the proposer)
//
// APPROVED_FOR_RELEASE is the end of this queue and the start of a content
// release; it is not ACTIVE, and no pack is published by reaching it
// (Doc 03 §07: LEGAL_REVIEW → APPROVED → TESTING / QA → SIGNED → STAGED →
// ACTIVE, each a separate gate).
type Status string

// The statuses.
const (
	StatusProposed           Status = "PROPOSED"
	StatusRecommended        Status = "RECOMMENDED"
	StatusApprovedForRelease Status = "APPROVED_FOR_RELEASE"
	StatusChangesRequested   Status = "CHANGES_REQUESTED"
	StatusRejected           Status = "REJECTED"
	StatusWithdrawn          Status = "WITHDRAWN"
	StatusSuperseded         Status = "SUPERSEDED"
)

// Statuses is every status, in queue order.
var Statuses = []Status{StatusProposed, StatusRecommended, StatusApprovedForRelease, StatusChangesRequested,
	StatusRejected, StatusWithdrawn, StatusSuperseded}

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	for _, k := range Statuses {
		if k == s {
			return true
		}
	}
	return false
}

// Pending reports whether the proposal is still in review.
func (s Status) Pending() bool { return s == StatusProposed || s == StatusRecommended }

// EventKind is what happened to a proposal.
type EventKind string

// The event kinds.
const (
	EventProposed         EventKind = "PROPOSED"
	EventTestEvidence     EventKind = "TEST_EVIDENCE_RECORDED"
	EventRecommended      EventKind = "RECOMMENDED"
	EventApproved         EventKind = "APPROVED"
	EventChangesRequested EventKind = "CHANGES_REQUESTED"
	EventRejected         EventKind = "REJECTED"
	EventWithdrawn        EventKind = "WITHDRAWN"
	EventSuperseded       EventKind = "SUPERSEDED"
)

// Valid reports whether k is a known kind.
func (k EventKind) Valid() bool {
	switch k {
	case EventProposed, EventTestEvidence, EventRecommended, EventApproved, EventChangesRequested, EventRejected,
		EventWithdrawn, EventSuperseded:
		return true
	}
	return false
}

// Event is one appended fact about a proposal.
type Event struct {
	Seq          int
	Kind         EventKind
	Status       Status
	Reason       string
	TestEvidence *TestEvidence
	RecordedAt   time.Time
	RecordedBy   id.UserID
}

// Revision is the proposal's event count: what an act names to prove it was
// decided on the record as it now stands.
func (p Proposal) Revision() int { return len(p.History) }

// Open is a new proposal's first event, and its test evidence when the
// proposer attached it at intake.
func Open(p Proposal, ev *TestEvidence) []Event {
	out := []Event{{Seq: 1, Kind: EventProposed, Status: StatusProposed, RecordedAt: p.CreatedAt, RecordedBy: p.CreatedBy}}
	if ev != nil {
		e := *ev
		out = append(out, Event{Seq: 2, Kind: EventTestEvidence, Status: StatusProposed, TestEvidence: &e,
			RecordedAt: p.CreatedAt, RecordedBy: p.CreatedBy})
	}
	return out
}

// Recommender is who recommended the proposal.
func (p Proposal) Recommender() (id.UserID, time.Time, bool) {
	for i := len(p.History) - 1; i >= 0; i-- {
		if p.History[i].Kind == EventRecommended {
			return p.History[i].RecordedBy, p.History[i].RecordedAt, true
		}
	}
	return id.UserID{}, time.Time{}, false
}

// Approver is who approved the proposal for release.
func (p Proposal) Approver() (id.UserID, time.Time, bool) {
	for _, e := range p.History {
		if e.Kind == EventApproved {
			return e.RecordedBy, e.RecordedAt, true
		}
	}
	return id.UserID{}, time.Time{}, false
}

func (p Proposal) checkRevision(revision int) error {
	if revision != p.Revision() {
		return errs.New(errs.CategoryConflict, ReasonVersionConflict,
			fmt.Sprintf("The proposal is at revision %d, not %d: something was recorded after you read it. Nothing was changed.", p.Revision(), revision))
	}
	return nil
}

// RecordTestEvidence is the event attaching test evidence to a PROPOSED
// change. Evidence is attached before review, so what a reviewer recommends
// and an approver approves is the change with that evidence; new evidence
// after a recommendation would be a different change to approve.
func (p Proposal) RecordTestEvidence(ev TestEvidence, revision int, by id.UserID, at time.Time) (Event, error) {
	if err := p.checkRevision(revision); err != nil {
		return Event{}, err
	}
	if p.Status != StatusProposed {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Test evidence is attached while a change is PROPOSED; this one is %s.", p.Status))
	}
	ev.Ref, ev.SHA256 = strings.TrimSpace(ev.Ref), strings.ToLower(strings.TrimSpace(ev.SHA256))
	if err := ev.Validate(); err != nil {
		return Event{}, err
	}
	return Event{Seq: p.Revision() + 1, Kind: EventTestEvidence, Status: StatusProposed, TestEvidence: &ev,
		RecordedAt: at, RecordedBy: by}, nil
}

// Transition is the event that moves the proposal to `to`, decided on
// revision by a person, or why it may not.
//
// Conflict of interest: the proposer neither recommends, asks for changes
// to, rejects nor approves their own change; the recommender does not also
// approve it. Only the proposer withdraws it.
func (p Proposal) Transition(to Status, reason string, revision int, by id.UserID, at time.Time) (Event, error) {
	if err := p.checkRevision(revision); err != nil {
		return Event{}, err
	}
	reason = strings.TrimSpace(reason)
	switch {
	case reason == "":
		return Event{}, errs.Invalid("reason", errs.ReasonMissingField, "Every act on a rule change gives its rationale.")
	case len(reason) > MaxReason:
		return Event{}, errs.Invalid("reason", errs.ReasonInvalidValue, fmt.Sprintf("A reason is at most %d characters.", MaxReason))
	}
	notProposer := func(act string) error {
		if by == p.CreatedBy {
			return errs.New(errs.CategoryPolicy, ReasonMakerChecker,
				fmt.Sprintf("You proposed this change, so a different person %s it (conflict of interest).", act))
		}
		return nil
	}
	var kind EventKind
	var err error
	switch {
	case p.Status == StatusProposed && to == StatusRecommended:
		kind = EventRecommended
		if err = notProposer("reviews"); err == nil && p.TestEvidence == nil {
			err = errs.New(errs.CategoryPolicy, ReasonTestEvidenceRequired,
				"Attach the test evidence before the change is recommended.")
		}
	case p.Status == StatusRecommended && to == StatusApprovedForRelease:
		kind = EventApproved
		if err = notProposer("approves"); err != nil {
			break
		}
		if rec, _, _ := p.Recommender(); rec == by {
			err = errs.New(errs.CategoryPolicy, ReasonMakerChecker,
				"You recommended this change, so an independent approver approves it.")
			break
		}
		switch {
		case p.TestEvidence == nil:
			err = errs.New(errs.CategoryPolicy, ReasonTestEvidenceRequired, "A change is approved only with its test evidence.")
		case !p.Citation.Authority.MayDirectlyGround():
			err = errs.New(errs.CategoryPolicy, ReasonSourceAuthorityInsufficient,
				fmt.Sprintf("The change cites a %s source. Only A0 or A1 authority grounds a production rule by itself.", p.Citation.Authority))
		}
	case p.Status.Pending() && to == StatusChangesRequested:
		kind = EventChangesRequested
		err = notProposer("reviews")
	case p.Status.Pending() && to == StatusRejected:
		kind = EventRejected
		err = notProposer("reviews")
	case (p.Status.Pending() || p.Status == StatusChangesRequested) && to == StatusWithdrawn:
		kind = EventWithdrawn
		if by != p.CreatedBy {
			err = errs.New(errs.CategoryPolicy, errs.ReasonForbidden,
				"Only the proposer withdraws a change; a reviewer asks for changes or rejects it.")
		}
	default:
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A rule change does not move from %s to %s.", p.Status, to))
	}
	if err != nil {
		return Event{}, err
	}
	return Event{Seq: p.Revision() + 1, Kind: kind, Status: to, Reason: reason, RecordedAt: at, RecordedBy: by}, nil
}

// Supersede is the event retiring a CHANGES_REQUESTED proposal when its
// proposer records the revised change. Written by the intake of the new
// proposal, never asked for on its own.
func (p Proposal) Supersede(by id.RuleChangeID, who id.UserID, at time.Time) (Event, error) {
	if p.Status != StatusChangesRequested {
		return Event{}, errs.Invalid("supersedes", errs.ReasonInvalidValue,
			fmt.Sprintf("A new proposal supersedes one sent back for changes; %s is %s.", p.ID, p.Status))
	}
	return Event{Seq: p.Revision() + 1, Kind: EventSuperseded, Status: StatusSuperseded,
		Reason: "Superseded by revised proposal " + by.String() + ".", RecordedAt: at, RecordedBy: who}, nil
}

// Fold replays a proposal's events onto it.
func Fold(p Proposal, events []Event) (Proposal, error) {
	p.History, p.Status, p.TestEvidence = nil, "", nil
	for i, e := range events {
		if e.Seq != i+1 || !e.Kind.Valid() || !e.Status.Valid() {
			return Proposal{}, fmt.Errorf("rulechange: proposal %s event %d is out of order or unknown", p.ID, i+1)
		}
		if (e.Kind == EventProposed) != (i == 0) {
			return Proposal{}, fmt.Errorf("rulechange: proposal %s does not start with PROPOSED", p.ID)
		}
		if e.Kind == EventTestEvidence {
			if e.TestEvidence == nil {
				return Proposal{}, fmt.Errorf("rulechange: proposal %s event %d records no evidence", p.ID, e.Seq)
			}
			ev := *e.TestEvidence
			p.TestEvidence = &ev
		}
		p.History = append(p.History, e)
		p.Status = e.Status
	}
	if len(p.History) == 0 {
		return Proposal{}, fmt.Errorf("rulechange: proposal %s has no events", p.ID)
	}
	return p, nil
}

// Filter selects proposals for the queue.
type Filter struct {
	Status       Status
	Pack         string
	Jurisdiction string
}

// Match reports whether p is selected.
func (f Filter) Match(p Proposal) bool {
	return (f.Status == "" || p.Status == f.Status) && (f.Pack == "" || string(p.Pack) == f.Pack) &&
		(f.Jurisdiction == "" || p.Jurisdiction == f.Jurisdiction)
}
