package document

import (
	"fmt"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// A document is immutable once committed; what happens to it afterwards is a
// history of status events about it (FIN-001 §5). A void or a credit note
// does not edit the invoice it cancels — it is a new document, and the
// invoice gains an event saying it has been voided or credited, naming the
// document that did it.

// StatusEvent is one entry in a document's lifecycle. Seq 1 is COMMITTED.
type StatusEvent struct {
	Document id.FiscalDocumentID
	Seq      int
	Status   Status
	// Cause is the document whose commit moved this one, where one did.
	Cause      *id.FiscalDocumentID
	RecordedAt time.Time
	RecordedBy id.UserID
}

// Bills reports whether a document of type t charges for what its cited
// decisions decided, rather than cancelling or reducing an earlier charge.
func (t Type) Bills() bool {
	switch t {
	case TypeInvoice, TypeDebitNote, TypeAdjustment, TypeRebill:
		return true
	}
	return false
}

// Releases reports whether a document in status s no longer charges: its
// decisions may be billed again, by a rebill.
func (s Status) Releases() bool {
	return s == StatusVoided || s == StatusFullyCredited
}

// IsCorrectionReason reports whether a reason code is one of the registered
// CORRECTION_* reasons a correcting document may carry.
func IsCorrectionReason(r errs.ReasonCode) bool {
	return strings.HasPrefix(string(r), "CORRECTION_") && errs.Registered(r)
}

// CanCorrect reports whether a document in status s may be corrected by a
// document of type t. A void or a credit cancels a charge that still stands;
// a rebill replaces one that has been cancelled.
func CanCorrect(s Status, t Type) bool {
	switch t {
	case TypeVoid, TypeCreditNote:
		switch s {
		case StatusCommitted, StatusIssued, StatusDelivered, StatusAccepted, StatusPartiallyCredited:
			return true
		}
	case TypeRebill:
		return s.Releases()
	}
	return false
}

// After is the status a corrected document moves to when a correction of
// type t commits against it. fully reports whether, with this correction,
// every line of the original has been cancelled. A rebill does not move the
// document it replaces: that document was already cancelled.
func After(t Type, fully bool) (Status, bool) {
	switch t {
	case TypeVoid:
		return StatusVoided, true
	case TypeCreditNote:
		if fully {
			return StatusFullyCredited, true
		}
		return StatusPartiallyCredited, true
	}
	return "", false
}

// Negate builds credit-note lines cancelling the chosen lines of original:
// each net and tax negated exactly, naming the line it cancels, under a
// fresh line id. Nothing is recomputed — a credit of an invoice line returns
// precisely what the line charged.
func Negate(original Document, chosen []id.FiscalLineID, fresh []id.FiscalLineID) ([]Line, error) {
	if len(chosen) == 0 {
		return nil, fmt.Errorf("document: a credit note cancels at least one line")
	}
	if len(fresh) != len(chosen) {
		return nil, fmt.Errorf("document: %d lines to cancel need %d fresh ids, got %d", len(chosen), len(chosen), len(fresh))
	}
	byID := make(map[id.FiscalLineID]Line, len(original.Lines))
	for _, l := range original.Lines {
		byID[l.ID] = l
	}
	seen := map[id.FiscalLineID]bool{}
	out := make([]Line, len(chosen))
	for i, lid := range chosen {
		l, ok := byID[lid]
		if !ok {
			return nil, fmt.Errorf("document: %s is not a line of %s", lid, original.ID)
		}
		if seen[lid] {
			return nil, fmt.Errorf("document: line %s is cancelled twice", lid)
		}
		seen[lid] = true
		if fresh[i] == lid {
			return nil, fmt.Errorf("document: a credit reuses line id %s", lid)
		}
		pred := lid
		nl := Line{ID: fresh[i], SourceLineRef: l.SourceLineRef, ComponentInstance: l.ComponentInstance, Net: l.Net.Neg(), Predecessor: &pred}
		for _, t := range l.Taxes {
			nl.Taxes = append(nl.Taxes, TaxLine{Decision: t.Decision, Component: t.Component, Amount: t.Amount.Neg()})
		}
		out[i] = nl
	}
	return out, nil
}

// Cancelled is the tax a cancelling document's lines must carry: each
// cancelled line's taxes, negated, as decided-tax entries for Commit. It is
// derived from the original document, never from the request, so a credit
// note can cancel only what was charged.
func Cancelled(original Document, cancelling []Line) []DecisionTax {
	byID := make(map[id.FiscalLineID]Line, len(original.Lines))
	for _, l := range original.Lines {
		byID[l.ID] = l
	}
	var out []DecisionTax
	for _, c := range cancelling {
		if c.Predecessor == nil {
			continue
		}
		for _, t := range byID[*c.Predecessor].Taxes {
			out = append(out, DecisionTax{Decision: t.Decision, Component: t.Component, Amount: t.Amount.Neg()})
		}
	}
	return out
}

// Totals sums a document's lines: net, tax, and their total. A discount is
// presented, not subtracted again — a line's net is after its discount.
func Totals(d Document) (net, tax, gross fiscal.Money, err error) {
	if net, err = fiscal.ParseMoney("0", d.Currency); err != nil {
		return
	}
	tax = net
	for _, l := range d.Lines {
		if net, err = net.Add(l.Net); err != nil {
			return
		}
		for _, t := range l.Taxes {
			if tax, err = tax.Add(t.Amount); err != nil {
				return
			}
		}
	}
	gross, err = net.Add(tax)
	return
}
