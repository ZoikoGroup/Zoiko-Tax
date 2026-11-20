package crosswalk

import (
	"fmt"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/classification"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// Status is a crosswalk version's state (Doc 02 WF-06/WF-07 "Draft, In
// review, Released, Suspended"; Doc 12 FED-04 SUPERSEDED).
//
//	DRAFT ─submit─▶ IN_REVIEW ─approve (another person)─▶ APPROVED ─suspend─▶ SUSPENDED
//	  │                │                                    ▲   │              │
//	  └──withdraw──────┤──request changes─▶ CHANGES_REQUESTED   │              │
//	                   └──reject─▶ REJECTED        reinstate────┼──────────────┘
//	                                                            └─a later version approved─▶ SUPERSEDED
//
// APPROVED is Doc 02's "Released" for the mapping: approved for use as a
// translation, which is all a crosswalk is ever used for.
type Status string

// The statuses.
const (
	StatusDraft            Status = "DRAFT"
	StatusInReview         Status = "IN_REVIEW"
	StatusApproved         Status = "APPROVED"
	StatusChangesRequested Status = "CHANGES_REQUESTED"
	StatusRejected         Status = "REJECTED"
	StatusWithdrawn        Status = "WITHDRAWN"
	StatusSuspended        Status = "SUSPENDED"
	StatusSuperseded       Status = "SUPERSEDED"
)

// Statuses is every status, in lifecycle order.
var Statuses = []Status{StatusDraft, StatusInReview, StatusApproved, StatusChangesRequested, StatusRejected,
	StatusWithdrawn, StatusSuspended, StatusSuperseded}

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	for _, k := range Statuses {
		if k == s {
			return true
		}
	}
	return false
}

// Closed reports whether the version will never serve again.
func (s Status) Closed() bool {
	return s == StatusChangesRequested || s == StatusRejected || s == StatusWithdrawn || s == StatusSuperseded
}

// EventKind is what happened to a version.
type EventKind string

// The event kinds.
const (
	EventCreated          EventKind = "CREATED"
	EventSubmitted        EventKind = "SUBMITTED"
	EventApproved         EventKind = "APPROVED"
	EventChangesRequested EventKind = "CHANGES_REQUESTED"
	EventRejected         EventKind = "REJECTED"
	EventWithdrawn        EventKind = "WITHDRAWN"
	EventSuspended        EventKind = "SUSPENDED"
	EventReinstated       EventKind = "REINSTATED"
	EventSuperseded       EventKind = "SUPERSEDED"
)

// Valid reports whether k is a known kind.
func (k EventKind) Valid() bool {
	switch k {
	case EventCreated, EventSubmitted, EventApproved, EventChangesRequested, EventRejected, EventWithdrawn,
		EventSuspended, EventReinstated, EventSuperseded:
		return true
	}
	return false
}

// Event is one appended fact about a version.
type Event struct {
	Seq        int
	Kind       EventKind
	Status     Status
	Reason     string
	RecordedAt time.Time
	RecordedBy id.UserID
}

// Open is a new version's first events: CREATED as a DRAFT and, when the
// author submits it as they write it, SUBMITTED for review.
func Open(by id.UserID, at time.Time, submit bool) []Event {
	out := []Event{{Seq: 1, Kind: EventCreated, Status: StatusDraft, RecordedAt: at, RecordedBy: by}}
	if submit {
		out = append(out, Event{Seq: 2, Kind: EventSubmitted, Status: StatusInReview, RecordedAt: at, RecordedBy: by})
	}
	return out
}

// Submitter is who submitted the version for review.
func (v Version) Submitter() (id.UserID, bool) {
	for i := len(v.History) - 1; i >= 0; i-- {
		if v.History[i].Kind == EventSubmitted {
			return v.History[i].RecordedBy, true
		}
	}
	return id.UserID{}, false
}

// Approver is who approved the version, and when.
func (v Version) Approver() (id.UserID, time.Time, bool) {
	for _, e := range v.History {
		if e.Kind == EventApproved {
			return e.RecordedBy, e.RecordedAt, true
		}
	}
	return id.UserID{}, time.Time{}, false
}

// Transition is the event that moves the version to `to`, decided on
// revision by a person on day today, or why it may not.
//
// The four-eyes rule: neither the version's author nor its submitter
// approves, requests changes to or rejects it. The rights rule: a version
// whose grant has lapsed is not approved or reinstated.
func (v Version) Transition(to Status, reason string, revision int, by id.UserID, at, today time.Time) (Event, error) {
	if revision != v.Revision() {
		return Event{}, errs.New(errs.CategoryConflict, ReasonVersionConflict,
			fmt.Sprintf("The crosswalk version is at revision %d, not %d: something was recorded after you read it. Nothing was changed.", v.Revision(), revision))
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > MaxReason {
		return Event{}, errs.Invalid("reason", errs.ReasonInvalidValue, fmt.Sprintf("A reason is at most %d characters.", MaxReason))
	}
	needReason := func() error {
		if reason == "" {
			return errs.Invalid("reason", errs.ReasonMissingField, "This act gives its reason.")
		}
		return nil
	}
	independent := func(act string) error {
		sub, _ := v.Submitter()
		if by == v.CreatedBy || by == sub {
			return errs.New(errs.CategoryPolicy, ReasonMakerChecker,
				fmt.Sprintf("You wrote or submitted this crosswalk version, so a different person %s it.", act))
		}
		return nil
	}
	rights := func() error {
		if v.Rights.LapsedOn(today) {
			return errs.New(errs.CategoryPolicy, ReasonRightsLapsed,
				fmt.Sprintf("The %s grant %s ended on %s. Record a version with a current grant.", v.Rights.Basis, v.Rights.Reference, v.Rights.ExpiresOn.Format(time.DateOnly)))
		}
		return nil
	}
	var kind EventKind
	var err error
	switch {
	case v.Status == StatusDraft && to == StatusInReview:
		kind = EventSubmitted
	case (v.Status == StatusDraft || v.Status == StatusInReview) && to == StatusWithdrawn:
		kind = EventWithdrawn
		if err = needReason(); err == nil && by != v.CreatedBy {
			err = errs.New(errs.CategoryPolicy, errs.ReasonForbidden, "Only the version's author withdraws it; a reviewer requests changes or rejects.")
		}
	case v.Status == StatusInReview && to == StatusApproved:
		kind = EventApproved
		if err = needReason(); err == nil {
			if err = independent("approves"); err == nil {
				err = rights()
			}
		}
	case v.Status == StatusInReview && to == StatusChangesRequested:
		kind = EventChangesRequested
		if err = needReason(); err == nil {
			err = independent("reviews")
		}
	case v.Status == StatusInReview && to == StatusRejected:
		kind = EventRejected
		if err = needReason(); err == nil {
			err = independent("reviews")
		}
	case v.Status == StatusApproved && to == StatusSuspended:
		kind = EventSuspended
		err = needReason()
	case v.Status == StatusSuspended && to == StatusApproved:
		kind = EventReinstated
		if err = needReason(); err == nil {
			err = rights()
		}
	default:
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A crosswalk version does not move from %s to %s.", v.Status, to))
	}
	if err != nil {
		return Event{}, err
	}
	return Event{Seq: v.Revision() + 1, Kind: kind, Status: to, Reason: reason, RecordedAt: at, RecordedBy: by}, nil
}

// Supersede is the event that retires an APPROVED or SUSPENDED version when
// a later version of the same crosswalk is approved. It is written by the
// approval, never asked for on its own.
func (v Version) Supersede(by int, who id.UserID, at time.Time) (Event, error) {
	if v.Status != StatusApproved && v.Status != StatusSuspended {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only an approved or suspended version is superseded; version %d is %s.", v.Number, v.Status))
	}
	return Event{Seq: v.Revision() + 1, Kind: EventSuperseded, Status: StatusSuperseded,
		Reason: fmt.Sprintf("Superseded by version %d.", by), RecordedAt: at, RecordedBy: who}, nil
}

// Fold replays a version's events onto it, checking each one could have
// happened.
func Fold(v Version, events []Event) (Version, error) {
	v.History = nil
	v.Status = ""
	for i, e := range events {
		if e.Seq != i+1 || !e.Kind.Valid() || !e.Status.Valid() {
			return Version{}, fmt.Errorf("crosswalk: version %d event %d is out of order or unknown", v.Number, i+1)
		}
		if (e.Kind == EventCreated) != (i == 0) {
			return Version{}, fmt.Errorf("crosswalk: version %d does not start with CREATED", v.Number)
		}
		v.History = append(v.History, e)
		v.Status = e.Status
	}
	if len(v.History) == 0 {
		return Version{}, fmt.Errorf("crosswalk: version %d has no events", v.Number)
	}
	return v, nil
}

// Resolution is how a lookup came out.
type Resolution string

// The resolutions (Doc 12 FED-04 UNMAPPED / REVIEW_REQUIRED; Doc 02 WF-06
// "Vendor-code unknown, dual mapping, expired license -> ... quarantined; no
// generic substitution").
const (
	// ResolutionMapped: one unconditional EXACT mapping.
	ResolutionMapped Resolution = "MAPPED"
	// ResolutionReviewRequired: the code maps only under a predicate, or to
	// more than one candidate. The caller has to establish the facts; the
	// crosswalk does not choose.
	ResolutionReviewRequired Resolution = "REVIEW_REQUIRED"
	// ResolutionNoEquivalent: reviewed, and it corresponds to no canonical
	// service.
	ResolutionNoEquivalent Resolution = "NO_EQUIVALENT"
	// ResolutionUnmapped: the code is not in the version.
	ResolutionUnmapped Resolution = "UNMAPPED"
	// ResolutionNotInForce: the version's window does not cover the date.
	ResolutionNotInForce Resolution = "NOT_IN_FORCE"
	// ResolutionQuarantined: the version is suspended or its grant has
	// lapsed, so it serves nothing.
	ResolutionQuarantined Resolution = "QUARANTINED"
)

// Lookup is one external code resolved against one version on one day.
// It is advisory by construction: Candidates are canonical service codes a
// person may classify with, never a tax treatment.
type Lookup struct {
	Resolution Resolution
	Candidates []Entry
	Detail     string
}

// Resolve looks code up exactly in v for a transaction on day, as of today.
// Only an APPROVED, SUSPENDED or SUPERSEDED version is resolved against; a
// SUPERSEDED one only when the caller names it, to reproduce a historical
// translation.
func (v Version) Resolve(code string, day, today time.Time) Lookup {
	switch {
	case v.Status == StatusSuspended:
		return Lookup{Resolution: ResolutionQuarantined, Detail: "The version is suspended; nothing is translated and nothing falls back."}
	case v.Rights.LapsedOn(today):
		return Lookup{Resolution: ResolutionQuarantined, Detail: "The grant the vendor data is held under has lapsed."}
	case !v.InForceOn(day):
		return Lookup{Resolution: ResolutionNotInForce, Detail: "The version's effective window does not cover that date."}
	}
	var hits []Entry
	for _, e := range v.Entries {
		if e.ExternalCode == code {
			hits = append(hits, e)
		}
	}
	switch {
	case len(hits) == 0:
		return Lookup{Resolution: ResolutionUnmapped, Candidates: []Entry{},
			Detail: "The code is not in this version. There is no default and no nearest match."}
	case len(hits) == 1 && hits[0].Match == MatchNoEquivalent:
		return Lookup{Resolution: ResolutionNoEquivalent, Candidates: hits,
			Detail: "Reviewed: the code corresponds to no canonical service."}
	case len(hits) == 1 && hits[0].Match == MatchExact && hits[0].Predicate == "":
		return Lookup{Resolution: ResolutionMapped, Candidates: hits,
			Detail: "One exact mapping. It names a canonical service, not a tax treatment."}
	}
	return Lookup{Resolution: ResolutionReviewRequired, Candidates: hits,
		Detail: "The code maps only under a condition, or to more than one candidate. Establish the facts; the crosswalk does not choose."}
}

// Targets lists the distinct service targets of a version, for display.
func (v Version) Targets() []classification.OntologyID {
	seen := map[classification.OntologyID]bool{}
	var out []classification.OntologyID
	for _, e := range v.Entries {
		if e.ServiceTarget != "" && !seen[e.ServiceTarget] {
			seen[e.ServiceTarget] = true
			out = append(out, e.ServiceTarget)
		}
	}
	return out
}
