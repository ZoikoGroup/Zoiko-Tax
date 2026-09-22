// Package authority is the boundary between the estate and a tax authority.
//
// Everything here exists to handle one situation correctly: we sent a filing
// and we do not know whether it arrived.
//
// That is the Critical risk in the register, and ADR-0016 §2.2 states the rule
// it produces — a 5xx must never be the representation of "we may have filed",
// because a 5xx invites a retry and a retry on an uncertain submission files
// the same return twice. So an adapter here does not return an error for a
// timeout. It returns Outcome{State: Uncertain}, which is a state of the
// SubmissionAttempt record, and the record does not leave that state without a
// person establishing what actually happened.
//
// The corollary, which is easy to get wrong: an adapter must distinguish "the
// request never left" from "the request left and we did not hear back". The
// first is safely retryable; the second is not. That distinction is the whole
// job of the Outcome type below, and it is why the adapter interface cannot
// simply return (result, error).
package authority

import (
	"context"
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// AdapterID names one authority integration. Adapters ship on each authority's
// calendar rather than ours, which is why they are separate deployables on the
// ADAPTER train (ADR-0009 §2.2).
type AdapterID string

// State is where a submission attempt stands.
type State string

// The states.
const (
	// StatePrepared has been built and not yet sent.
	StatePrepared State = "PREPARED"
	// StateInFlight has been sent and no response has arrived.
	StateInFlight State = "IN_FLIGHT"
	// StateAccepted was acknowledged.
	StateAccepted State = "ACCEPTED"
	// StateRejected was refused, with a reason, and may be corrected.
	StateRejected State = "REJECTED"
	// StateUncertain is the one that matters. We may have filed. Nothing
	// automated resolves it.
	StateUncertain State = "UNCERTAIN_SUBMISSION"
)

// SafeToRetry reports whether re-sending this submission is safe.
//
// Only PREPARED is. IN_FLIGHT may still land, and UNCERTAIN may already have —
// retrying either is how one return gets filed twice. A caller that wants to
// make progress on an UNCERTAIN attempt uses Resolve, which asks the authority
// what it holds rather than sending anything.
func (s State) SafeToRetry() bool { return s == StatePrepared }

// RequiresHumanResolution reports whether leaving this state needs a person.
func (s State) RequiresHumanResolution() bool { return s == StateUncertain }

// Terminal reports whether the attempt is settled.
func (s State) Terminal() bool { return s == StateAccepted || s == StateRejected }

// Submission is what an adapter is asked to file.
type Submission struct {
	AttemptID    id.SubmissionAttemptID
	TenantID     id.TenantID
	ObligationID id.ObligationID
	AdapterID    AdapterID
	// RequestDigest is the canonical digest of the payload. It is what a
	// Resolve call matches against what the authority says it holds, so a
	// duplicate-safe resolution can tell "they have ours" from "they have a
	// different one".
	RequestDigest string
	// Payload is the authority's own format, already rendered. This package
	// does not interpret it.
	Payload []byte
}

// Outcome is what an adapter reports.
//
// It is a value rather than an (ok, error) pair because "we do not know" is a
// legitimate outcome that neither branch of a pair can carry honestly: as an
// error it invites a retry, and as a success it claims something untrue.
type Outcome struct {
	State State
	// AuthorityRef is the authority's own reference, verbatim and never parsed
	// (ADR-0012 §2.5). It may be absent for a rejection.
	AuthorityRef string
	// Reason explains a rejection or an uncertainty, from the registered
	// vocabulary.
	Reason errs.ReasonCode
	// Detail is the authority's message, for a human reading the failure. It
	// is not parsed and nothing branches on it.
	Detail    string
	SettledAt time.Time
}

// Validate refuses an incoherent outcome.
func (o Outcome) Validate() error {
	switch o.State {
	case StateAccepted:
		if o.AuthorityRef == "" {
			// An acceptance with no reference cannot be proven later, which is
			// the only reason to record an acceptance at all.
			return fmt.Errorf("authority: an accepted submission carries no authority reference")
		}
	case StateRejected:
		if o.Reason == "" {
			return fmt.Errorf("authority: a rejected submission names no reason")
		}
	case StateUncertain, StateInFlight, StatePrepared:
	default:
		return fmt.Errorf("authority: unknown state %q", o.State)
	}
	if o.Reason != "" && !errs.Registered(o.Reason) {
		return fmt.Errorf("authority: reason code %q is not registered", o.Reason)
	}
	return nil
}

// Adapter files with one authority.
//
// Note the signatures. Submit returns an Outcome and an error, and the two mean
// different things: the error is for a failure that definitely did not reach
// the authority — a malformed payload, a configuration fault — while anything
// that might have reached it comes back as an Outcome with a state. An adapter
// that returns an error for a timeout has misunderstood the contract, and that
// is the single most important thing to get right when writing one.
type Adapter interface {
	// ID names this adapter.
	ID() AdapterID

	// Submit files. A network timeout, a connection reset after the request was
	// written, or any response the adapter cannot interpret all produce
	// Outcome{State: StateUncertain} and a nil error.
	Submit(ctx context.Context, s Submission) (Outcome, error)

	// Resolve asks the authority what it holds for an attempt, without sending
	// anything. It is the duplicate-safe path out of StateUncertain: it is a
	// read, so calling it twice is harmless, and it is the only way an
	// uncertain attempt becomes accepted or rejected.
	//
	// An authority with no query facility returns an outcome still in
	// StateUncertain, which is honest — the resolution is then a human reading
	// a portal, recorded through the same path.
	Resolve(ctx context.Context, s Submission) (Outcome, error)
}

// Registry holds the adapters this cell can reach.
type Registry struct{ adapters map[AdapterID]Adapter }

// NewRegistry builds a registry.
func NewRegistry(adapters ...Adapter) *Registry {
	r := &Registry{adapters: make(map[AdapterID]Adapter, len(adapters))}
	for _, a := range adapters {
		r.adapters[a.ID()] = a
	}
	return r
}

// Lookup returns an adapter.
//
// A missing adapter is CategoryUnsupported rather than NotFound: it is a fact
// about our coverage, and ADR-0016 §2.1 says a customer is entitled to have
// that recorded rather than receiving an error that records nothing.
func (r *Registry) Lookup(adapterID AdapterID) (Adapter, error) {
	a, ok := r.adapters[adapterID]
	if !ok {
		return nil, errs.New(errs.CategoryUnsupported, errs.ReasonJurisdictionUnsupported,
			"No filing adapter is available for that authority in this cell.")
	}
	return a, nil
}

// IDs returns the registered adapter identifiers, for /v1/capabilities.
func (r *Registry) IDs() []AdapterID {
	out := make([]AdapterID, 0, len(r.adapters))
	for adapterID := range r.adapters {
		out = append(out, adapterID)
	}
	return out
}
