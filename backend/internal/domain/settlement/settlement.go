// Package settlement is what happens to tax after it is documented: it is
// collected, refunded, reported on a return and remitted (ZTAX-FIN-001
// §14–§16). Each of those is its own lifecycle with its own uncertainty, and
// the rule this package exists to hold is that none of them advances on a
// guess: a processor timeout is not a collection (ZTAX-FIN-REQ-0058), and an
// unconfirmed payment is not a remittance (ZTAX-FIN-REQ-0068).
//
// ZoikoTax does not hold the money. These are records of what the bank, the
// payment provider and the authority said (ZTAX-FIN-REQ-0067).
package settlement

import (
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// CollectionKind is a collection event's kind. Refunds and chargebacks are
// events of their own, not negative collections (ZTAX-FIN-REQ-0055, -0060).
type CollectionKind string

// The collection kinds.
const (
	KindCollection CollectionKind = "COLLECTION"
	KindRefund     CollectionKind = "REFUND"
	KindChargeback CollectionKind = "CHARGEBACK"
	KindReversal   CollectionKind = "REVERSAL"
)

// CollectionStatus is what the source system said.
type CollectionStatus string

// The statuses.
const (
	CollectionPending   CollectionStatus = "PENDING"
	CollectionSettled   CollectionStatus = "SETTLED"
	CollectionFailed    CollectionStatus = "FAILED"
	CollectionUncertain CollectionStatus = "UNCERTAIN"
)

// ProcessorOutcome is a payment provider's raw response class.
type ProcessorOutcome string

// The processor outcomes.
const (
	ProcessorSucceeded ProcessorOutcome = "SUCCEEDED"
	ProcessorDeclined  ProcessorOutcome = "DECLINED"
	ProcessorTimedOut  ProcessorOutcome = "TIMED_OUT"
	ProcessorUnknown   ProcessorOutcome = "UNKNOWN"
)

// StatusFrom maps a processor outcome to a collection status. A timeout and
// an unknown response are UNCERTAIN — never SETTLED — because the money may or
// may not have moved, and recording it as moved is how a tax position gets
// cleared that was never paid (ZTAX-FIN-REQ-0058).
func StatusFrom(o ProcessorOutcome) CollectionStatus {
	switch o {
	case ProcessorSucceeded:
		return CollectionSettled
	case ProcessorDeclined:
		return CollectionFailed
	}
	return CollectionUncertain
}

// Collection is one collection, refund or chargeback event
// (ZTAX-FIN-REQ-0056).
type Collection struct {
	ID         string
	Kind       CollectionKind
	PaymentRef string
	Source     string
	Amount     fiscal.Money
	Status     CollectionStatus
	Date       time.Time
}

// Validate refuses an event that does not identify its source and payment.
func (c Collection) Validate() error {
	switch {
	case c.ID == "" || c.PaymentRef == "" || c.Source == "":
		return fmt.Errorf("settlement: collection names no id, payment reference or source")
	case c.Date.IsZero():
		return fmt.Errorf("settlement: collection %s has no date", c.ID)
	case c.Amount.Sign() <= 0:
		return fmt.Errorf("settlement: collection %s amount %s is not positive; the kind says which way it moves", c.ID, c.Amount.CanonicalString())
	}
	switch c.Kind {
	case KindCollection, KindRefund, KindChargeback, KindReversal:
	default:
		return fmt.Errorf("settlement: collection %s has kind %q", c.ID, c.Kind)
	}
	switch c.Status {
	case CollectionPending, CollectionSettled, CollectionFailed, CollectionUncertain:
	default:
		return fmt.Errorf("settlement: collection %s has status %q", c.ID, c.Status)
	}
	return nil
}

// ReopensReconciliation reports whether the event reopens the document-to-
// collection reconciliation it touches (ZTAX-FIN-REQ-0060).
func (c Collection) ReopensReconciliation() bool {
	return c.Kind == KindChargeback || c.Kind == KindReversal
}

// AllocationMethod is how a partial collection spreads over the documents it
// pays (ZTAX-FIN-REQ-0057). There is no default; pro-rata is one choice among
// several and law or the customer's policy may require another.
type AllocationMethod string

// The allocation methods.
const (
	AllocateProRata     AllocationMethod = "PRO_RATA"
	AllocateOldestFirst AllocationMethod = "OLDEST_FIRST"
)

// Exposure is one document's open amount, for allocation.
type Exposure struct {
	Document id.FiscalDocumentID
	IssuedAt time.Time
	Open     fiscal.Money
}

// Allocation is one document's share.
type Allocation struct {
	Document id.FiscalDocumentID
	Amount   fiscal.Money
}

// Allocate spreads a collected amount over open exposures under an explicit
// method. Amounts beyond the total open exposure are refused rather than
// parked anywhere.
func Allocate(amount fiscal.Money, exposures []Exposure, method AllocationMethod, p fiscal.RoundingPolicy) ([]Allocation, error) {
	if len(exposures) == 0 {
		return nil, fmt.Errorf("settlement: nothing to allocate %s against", amount.CanonicalString())
	}
	total := amount.Zero()
	for _, e := range exposures {
		var err error
		if total, err = total.Add(e.Open); err != nil {
			return nil, err
		}
	}
	if c, err := amount.Cmp(total); err != nil {
		return nil, err
	} else if c > 0 {
		return nil, fmt.Errorf("settlement: %s exceeds the %s open across the documents", amount.CanonicalString(), total.CanonicalString())
	}
	switch method {
	case AllocateProRata:
		weights := make([]fiscal.Money, len(exposures))
		for i, e := range exposures {
			weights[i] = e.Open
		}
		parts, err := fiscal.Allocate(amount, weights, p)
		if err != nil {
			return nil, err
		}
		out := make([]Allocation, len(exposures))
		for i, e := range exposures {
			out[i] = Allocation{Document: e.Document, Amount: parts[i]}
		}
		return out, nil
	case AllocateOldestFirst:
		es := append([]Exposure(nil), exposures...)
		sort.SliceStable(es, func(i, j int) bool { return es[i].IssuedAt.Before(es[j].IssuedAt) })
		left := amount
		var out []Allocation
		for _, e := range es {
			if left.Sign() == 0 {
				break
			}
			take, err := left.Min(e.Open)
			if err != nil {
				return nil, err
			}
			out = append(out, Allocation{Document: e.Document, Amount: take})
			if left, err = left.Sub(take); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("settlement: allocation method %q is not declared", method)
}

// RefundStatus is a refund's own lifecycle, separate from the credit that
// entitles it (ZTAX-FIN-REQ-0059): a credit can be issued while the refund is
// still pending, and a refund can fail without the credit being undone.
type RefundStatus string

// The refund statuses.
const (
	RefundRequested RefundStatus = "REQUESTED"
	RefundPending   RefundStatus = "PENDING"
	RefundCompleted RefundStatus = "COMPLETED"
	RefundFailed    RefundStatus = "FAILED"
	RefundUncertain RefundStatus = "UNCERTAIN"
)

// ReturnStatus is a return version's state.
type ReturnStatus string

// The return statuses.
const (
	ReturnDraft    ReturnStatus = "DRAFT"
	ReturnFiled    ReturnStatus = "FILED"
	ReturnAccepted ReturnStatus = "ACCEPTED"
)

// ReturnVersion is one version of one return. A filed version is never
// edited; an amendment is a new version (ZTAX-FIN-REQ-0062, -0063). Its
// population is the immutable ids it reports on (ZTAX-FIN-REQ-0061), and the
// population hash is what a return-clearing journal cites
// (ZTAX-FIN-REQ-0065).
type ReturnVersion struct {
	ReturnID   string
	Version    int
	Authority  string
	Period     string
	Population []string
	Amount     fiscal.Money
	Status     ReturnStatus
	Amends     *int
}

// PopulationHash digests the population, order-independently.
func (r ReturnVersion) PopulationHash() (canonical.Digest, error) {
	ids := append([]string(nil), r.Population...)
	sort.Strings(ids)
	vs := make([]canonical.Value, len(ids))
	for i, s := range ids {
		vs[i] = canonical.String(s)
	}
	return canonical.Sum(canonical.Array(vs...))
}

// Amend creates the next version from a filed one. The filed version is
// returned untouched; the new one is a DRAFT naming what it amends. The
// amount on the new version is the return's own — a return figure is never
// written back into the TaxDecisions it reports on (ZTAX-FIN-REQ-0064).
func (r ReturnVersion) Amend(population []string, amount fiscal.Money) (ReturnVersion, error) {
	if r.Status == ReturnDraft {
		return ReturnVersion{}, fmt.Errorf("settlement: return %s v%d is a draft; edit it, do not amend it", r.ReturnID, r.Version)
	}
	prior := r.Version
	return ReturnVersion{
		ReturnID: r.ReturnID, Version: r.Version + 1, Authority: r.Authority, Period: r.Period,
		Population: append([]string(nil), population...), Amount: amount, Status: ReturnDraft, Amends: &prior,
	}, nil
}

// RemittanceStatus is a remittance's state. UNCERTAIN is explicit
// (ZTAX-FIN-REQ-0068) and prevents clearing.
type RemittanceStatus string

// The remittance statuses.
const (
	RemittanceInstructed RemittanceStatus = "INSTRUCTED"
	RemittancePending    RemittanceStatus = "PENDING"
	RemittanceConfirmed  RemittanceStatus = "CONFIRMED"
	RemittanceFailed     RemittanceStatus = "FAILED"
	RemittanceUncertain  RemittanceStatus = "UNCERTAIN"
)

// Offset is an authority credit applied against what is owed.
type Offset struct {
	Reference string
	Amount    fiscal.Money
}

// Evidence is remittance evidence (ZTAX-FIN-REQ-0070).
type Evidence struct {
	Authority string
	Amount    fiscal.Money
	Date      time.Time
	Reference string
	Channel   string
}

// Remittance is one remittance against a return.
type Remittance struct {
	Return   ReturnVersion
	Amount   fiscal.Money
	Offsets  []Offset
	Status   RemittanceStatus
	Evidence *Evidence
}

// Instruct derives a remittance instruction from an approved return
// (ZTAX-FIN-REQ-0066). The amount is the return's, less any offsets
// (ZTAX-FIN-REQ-0069); a draft return instructs nothing.
func Instruct(r ReturnVersion, offsets []Offset) (Remittance, error) {
	if r.Status != ReturnFiled && r.Status != ReturnAccepted {
		return Remittance{}, fmt.Errorf("settlement: return %s v%d is %s; remittance derives from a filed or accepted return", r.ReturnID, r.Version, r.Status)
	}
	due := r.Amount
	for _, o := range offsets {
		var err error
		if due, err = due.Sub(o.Amount); err != nil {
			return Remittance{}, err
		}
	}
	if due.Sign() < 0 {
		return Remittance{}, fmt.Errorf("settlement: offsets exceed the return's %s", r.Amount.CanonicalString())
	}
	return Remittance{Return: r, Amount: due, Offsets: append([]Offset(nil), offsets...), Status: RemittanceInstructed}, nil
}

// Confirm records the evidence that the payment landed. Evidence missing any
// of authority, amount, currency, date or reference does not confirm.
func (r Remittance) Confirm(e Evidence) (Remittance, error) {
	if e.Authority == "" || e.Reference == "" || e.Date.IsZero() || e.Amount.Currency() == "" {
		return Remittance{}, fmt.Errorf("settlement: remittance evidence names no authority, reference, date or currency")
	}
	if e.Authority != r.Return.Authority {
		return Remittance{}, fmt.Errorf("settlement: evidence is from %s; the return is owed to %s", e.Authority, r.Return.Authority)
	}
	out := r
	out.Status = RemittanceConfirmed
	out.Evidence = &e
	return out, nil
}

// Clears reports whether the remittance may clear the remittance control
// position. Only confirmed payment evidence does; UNCERTAIN, PENDING and the
// rest leave the position open.
func (r Remittance) Clears() bool { return r.Status == RemittanceConfirmed && r.Evidence != nil }
