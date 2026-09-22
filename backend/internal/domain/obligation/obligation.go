// Package obligation models what a tenant owes an authority, and when.
//
// The state machine here is the whole package, and one transition in it is the
// reason it is written carefully: UNCERTAIN.
//
// ADR-0016 §2.2 makes UNCERTAIN_SUBMISSION a state of the record, never a 5xx.
// A 5xx invites a retry, and a retry on an uncertain submission is the Critical
// risk in the register — it is how one return gets filed twice. So an obligation
// that reaches UNCERTAIN does not go back to READY on its own, and there is no
// transition that lets a timer or a retry loop move it. It leaves that state
// only through an explicit, duplicate-safe resolution a person initiates.
package obligation

import (
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Status is where an obligation is in its lifecycle.
type Status string

// The statuses.
const (
	// StatusOpen is accruing; the period has not closed.
	StatusOpen Status = "OPEN"
	// StatusReady has a computed assessment and may be filed.
	StatusReady Status = "READY"
	// StatusFiled has been submitted and the authority has not yet responded.
	StatusFiled Status = "FILED"
	// StatusAccepted was acknowledged by the authority.
	StatusAccepted Status = "ACCEPTED"
	// StatusRejected was refused by the authority, and can be corrected and
	// refiled.
	StatusRejected Status = "REJECTED"
	// StatusUncertain is the state ADR-0016 §2.2 exists for: we may have filed
	// and we do not know. It is not an error and not a success, and it renders
	// as itself (ADR-0019 C3) rather than as a spinner.
	StatusUncertain Status = "UNCERTAIN"
	// StatusClosed is settled.
	StatusClosed Status = "CLOSED"
)

// transitions is the state machine, as an explicit table.
//
// A table rather than a switch, because the question a reviewer asks is "can it
// go from X to Y" and a table answers it by inspection. Note the two entries
// that matter most:
//
//   - UNCERTAIN leads only to ACCEPTED, REJECTED or CLOSED, and every one of
//     those requires a human to establish what actually happened at the
//     authority. Nothing leads from UNCERTAIN back to READY or FILED, so no
//     retry path exists for a submission that may already have landed.
//   - FILED leads to UNCERTAIN, which is how a timed-out submission is
//     recorded. The alternative — going back to READY — is the duplicate-filing
//     bug written down.
var transitions = map[Status][]Status{
	StatusOpen:      {StatusReady, StatusClosed},
	StatusReady:     {StatusFiled, StatusOpen, StatusClosed},
	StatusFiled:     {StatusAccepted, StatusRejected, StatusUncertain},
	StatusRejected:  {StatusReady, StatusClosed},
	StatusAccepted:  {StatusClosed},
	StatusUncertain: {StatusAccepted, StatusRejected, StatusClosed},
	StatusClosed:    nil,
}

// CanTransition reports whether a move is permitted.
func CanTransition(from, to Status) bool {
	for _, allowed := range transitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// RequiresHumanResolution reports whether leaving this state needs a person.
//
// Only UNCERTAIN does. It is exposed as a method rather than left implicit so
// that a scheduler, a retry loop or a background worker can ask before acting,
// and so the answer is in one place rather than in each of them.
func (s Status) RequiresHumanResolution() bool { return s == StatusUncertain }

// Terminal reports whether an obligation in this state is finished.
func (s Status) Terminal() bool { return s == StatusClosed }

// Period is the span an obligation covers.
type Period struct {
	Start time.Time
	End   time.Time
	Due   time.Time
}

// Validate refuses an incoherent period.
func (p Period) Validate() error {
	switch {
	case p.Start.IsZero() || p.End.IsZero():
		return fmt.Errorf("obligation: period has no start or no end")
	case p.End.Before(p.Start):
		return fmt.Errorf("obligation: period ends before it starts")
	case p.Due.IsZero():
		return fmt.Errorf("obligation: period has no due date")
	case p.Due.Before(p.Start):
		// A due date before the period even begins is a content or calendar
		// defect, and filing against it would be filing early for a period that
		// has not happened.
		return fmt.Errorf("obligation: due date precedes the period it covers")
	}
	return nil
}

// Obligation is one filing duty.
type Obligation struct {
	ID       id.ObligationID
	TenantID id.TenantID

	BusinessKey string
	Supersedes  *id.ObligationID
	RecordedAt  time.Time

	JurisdictionID string
	Type           string
	Period         Period
	Status         Status

	// Assessed is the amount, once it is known. It is a pointer because an
	// OPEN obligation legitimately has no assessment yet, and a zero Money
	// would say "we assessed nothing owed", which is a different fact.
	Assessed *fiscal.Money
	Reason   errs.ReasonCode
}

// Transition returns a corrected copy in a new state.
//
// Append-only: a transition is a new row linked by Supersedes (ADR-0003 §2.2),
// so this returns a value and never mutates. Writing the returned value and
// keeping the original is what makes the history reconstructable.
func (o Obligation) Transition(to Status, at time.Time, newID id.ObligationID) (Obligation, error) {
	if !CanTransition(o.Status, to) {
		return Obligation{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("An obligation cannot move from %s to %s.", o.Status, to))
	}
	if to == StatusFiled && o.Assessed == nil {
		// Filing an obligation with no assessment would submit a return with no
		// figure in it.
		return Obligation{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"An obligation cannot be filed before it has been assessed.")
	}
	prior := o.ID
	next := o
	next.ID = newID
	next.Supersedes = &prior
	next.Status = to
	next.RecordedAt = at.UTC()
	return next, nil
}

// Overdue reports whether the obligation passed its due date unfiled.
//
// The instant is a parameter rather than a clock read: ADR-0003 §2.5 keeps wall
// time out of the domain, and a report run "as of" a past date has to get the
// answer for that date rather than for today.
func (o Obligation) Overdue(asOf time.Time) bool {
	if o.Status.Terminal() || o.Status == StatusAccepted || o.Status == StatusFiled {
		return false
	}
	return asOf.After(o.Period.Due)
}

// Validate refuses an incoherent obligation.
func (o Obligation) Validate() error {
	if o.JurisdictionID == "" {
		return fmt.Errorf("obligation: names no jurisdiction")
	}
	if o.Type == "" {
		return fmt.Errorf("obligation: names no type")
	}
	if _, known := transitions[o.Status]; !known {
		return fmt.Errorf("obligation: unknown status %q", o.Status)
	}
	if o.Reason != "" && !errs.Registered(o.Reason) {
		return fmt.Errorf("obligation: reason code %q is not registered", o.Reason)
	}
	return o.Period.Validate()
}

// Canonical renders an obligation for the evidence record.
func (o Obligation) Canonical() canonical.Value {
	assessed := canonical.Absent()
	currency := canonical.Absent()
	if o.Assessed != nil {
		assessed = canonical.Money(*o.Assessed)
		currency = canonical.String(string(o.Assessed.Currency()))
	}
	return canonical.Object(
		canonical.F("jurisdiction", canonical.String(o.JurisdictionID)),
		canonical.F("type", canonical.String(o.Type)),
		canonical.F("periodStart", canonical.Time(o.Period.Start)),
		canonical.F("periodEnd", canonical.Time(o.Period.End)),
		canonical.F("due", canonical.Time(o.Period.Due)),
		canonical.F("status", canonical.String(string(o.Status))),
		canonical.F("assessed", assessed),
		canonical.F("currency", currency),
		canonical.F("reason", canonical.OptString(string(o.Reason))),
	)
}
