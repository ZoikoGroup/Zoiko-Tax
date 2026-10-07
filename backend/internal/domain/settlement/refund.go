package settlement

import (
	"fmt"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// A refund returns tax a committed decision charged. It is not a credit and
// not a correction (ZTAX-FIN-REQ-0015): the decision it refunds stays exactly
// as it was decided, nothing is re-determined, and the refund's own state —
// whether the money has actually gone back — is a lifecycle of its own
// (ZTAX-FIN-REQ-0059). A credit note can be issued while its refund is still
// pending, and a refund can fail without the credit being undone.
//
// Its history is a sequence of events rather than a mutable status column:
// the header is written once, and each thing the payment provider says is a
// new event. The current status is the last event's.

// Refund is the immutable header of one refund.
type Refund struct {
	ID          id.RefundID
	TenantID    id.TenantID
	LegalEntity id.LegalEntityID
	// Decision is the committed decision whose tax is returned. It must be
	// the current version of its business key: a superseded decision's
	// posting has already been reversed, and refunding it would return tax
	// the ledger no longer holds.
	Decision id.DecisionID
	// Amount is positive tax, in the currency the decision posted it in.
	Amount fiscal.Money
	// PaymentRef is the payment provider's reference for the payment being
	// refunded. ZoikoTax does not move the money (ZTAX-FIN-REQ-0067); this is
	// how a refund is matched to what the provider reports.
	PaymentRef  string
	Reason      string
	RequestedAt time.Time
	// RequestedBy is the user who asked for it; zero for system work.
	RequestedBy id.UserID
}

// MaxPaymentRefLength bounds the provider reference.
const MaxPaymentRefLength = 255

// MaxRefundReasonLength bounds the free-text reason.
const MaxRefundReasonLength = 1000

// Validate refuses a header that does not identify what it refunds.
func (r Refund) Validate() error {
	switch {
	case r.ID.IsZero() || r.TenantID.IsZero() || r.LegalEntity.IsZero():
		return fmt.Errorf("settlement: refund carries no id, tenant or legal entity")
	case r.Decision.IsZero():
		return fmt.Errorf("settlement: refund %s names no decision", r.ID)
	case strings.TrimSpace(r.PaymentRef) == "" || len(r.PaymentRef) > MaxPaymentRefLength:
		return fmt.Errorf("settlement: refund %s needs a payment reference of at most %d characters", r.ID, MaxPaymentRefLength)
	case len(r.Reason) > MaxRefundReasonLength:
		return fmt.Errorf("settlement: refund %s reason exceeds %d characters", r.ID, MaxRefundReasonLength)
	case r.RequestedAt.IsZero():
		return fmt.Errorf("settlement: refund %s has no request time", r.ID)
	case r.Amount.Sign() <= 0:
		// A refund's direction is its kind. A negative refund would be a
		// charge, and a charge is a decision.
		return fmt.Errorf("settlement: refund %s amount %s is not positive", r.ID, r.Amount.CanonicalString())
	}
	return nil
}

// RefundOutcome is what the payment provider reported about a refund.
type RefundOutcome string

// The refund outcomes.
const (
	// RefundAccepted is the provider taking the refund on: it will be paid,
	// not that it has been.
	RefundAccepted  RefundOutcome = "ACCEPTED"
	RefundSucceeded RefundOutcome = "SUCCEEDED"
	RefundDeclined  RefundOutcome = "DECLINED"
	RefundTimedOut  RefundOutcome = "TIMED_OUT"
	RefundUnknown   RefundOutcome = "UNKNOWN"
)

// Valid reports whether o is a known outcome.
func (o RefundOutcome) Valid() bool {
	switch o {
	case RefundAccepted, RefundSucceeded, RefundDeclined, RefundTimedOut, RefundUnknown:
		return true
	}
	return false
}

// RefundStatusFrom maps a provider outcome to a refund status. A timeout and
// an unknown response are UNCERTAIN, never COMPLETED and never FAILED: the
// money may or may not have gone back, and either guess moves the tax
// position on something nobody knows (ZTAX-FIN-REQ-0058 applied to refunds).
func RefundStatusFrom(o RefundOutcome) RefundStatus {
	switch o {
	case RefundAccepted:
		return RefundPending
	case RefundSucceeded:
		return RefundCompleted
	case RefundDeclined:
		return RefundFailed
	}
	return RefundUncertain
}

// Valid reports whether s is a known status.
func (s RefundStatus) Valid() bool {
	switch s {
	case RefundRequested, RefundPending, RefundCompleted, RefundFailed, RefundUncertain:
		return true
	}
	return false
}

// Terminal reports whether nothing further can happen to the refund.
func (s RefundStatus) Terminal() bool {
	return s == RefundCompleted || s == RefundFailed
}

// Reserves reports whether a refund in this status counts against the tax
// still refundable on its decision. Everything but FAILED does: an UNCERTAIN
// refund may have paid, and treating it as unpaid is how one charge gets
// refunded twice.
func (s RefundStatus) Reserves() bool {
	return s != RefundFailed
}

// CanMoveTo reports whether a refund in s may move to next.
//
// A provider can report a refund pending, then settled or declined; an
// uncertain refund is resolved by a later definite report. Nothing leaves
// COMPLETED or FAILED — a refund that the provider later reverses is a new
// event of its own kind, not a rewrite of this one. A repeated report of the
// same status is not a move.
func (s RefundStatus) CanMoveTo(next RefundStatus) bool {
	if s == next || s.Terminal() {
		return false
	}
	switch next {
	case RefundPending:
		return s == RefundRequested || s == RefundUncertain
	case RefundCompleted, RefundFailed:
		return true
	case RefundUncertain:
		return s == RefundRequested || s == RefundPending
	}
	return false
}

// RefundEvent is one entry in a refund's history.
type RefundEvent struct {
	Refund id.RefundID
	// Seq orders the history from 1, which is the REQUESTED event written
	// with the header.
	Seq    int
	Status RefundStatus
	// Outcome is the provider report that caused the event; empty on the
	// REQUESTED event.
	Outcome RefundOutcome
	// ExternalRef is the provider's reference for the report, where it gave
	// one.
	ExternalRef string
	RecordedAt  time.Time
	RecordedBy  id.UserID
}

// Requested is the first event of a refund.
func Requested(r Refund) RefundEvent {
	return RefundEvent{Refund: r.ID, Seq: 1, Status: RefundRequested, RecordedAt: r.RequestedAt, RecordedBy: r.RequestedBy}
}

// Report applies a provider outcome to the refund's current event and
// returns the next one. It refuses an outcome the lifecycle does not permit
// from where the refund is, rather than recording a move that did not
// happen.
func Report(current RefundEvent, o RefundOutcome, externalRef string, at time.Time, by id.UserID) (RefundEvent, error) {
	if !o.Valid() {
		return RefundEvent{}, fmt.Errorf("settlement: %q is not a refund outcome", o)
	}
	if len(externalRef) > MaxPaymentRefLength {
		return RefundEvent{}, fmt.Errorf("settlement: external reference exceeds %d characters", MaxPaymentRefLength)
	}
	next := RefundStatusFrom(o)
	if !current.Status.CanMoveTo(next) {
		return RefundEvent{}, fmt.Errorf("settlement: refund %s is %s and cannot become %s", current.Refund, current.Status, next)
	}
	return RefundEvent{
		Refund: current.Refund, Seq: current.Seq + 1, Status: next, Outcome: o,
		ExternalRef: externalRef, RecordedAt: at.UTC(), RecordedBy: by,
	}, nil
}
