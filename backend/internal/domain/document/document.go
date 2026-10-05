// Package document is the fiscal document lifecycle of ZTAX-FIN-001 §3–§6:
// what a committed transaction becomes once it is invoiced, credited, voided,
// refunded, rebilled, amended or restated, and how each of those stays linked
// to the one before it.
//
// Two rules shape everything here. A committed document is never changed —
// every correction is a new document naming its predecessors and its root
// (ZTAX-FIN-REQ-0003, -0004) — and a document's tax is the pinned
// TaxDecision's tax, never a figure the document invented (ZTAX-FIN-REQ-0011).
// The first is enforced by there being no method that edits a Document; the
// second by Commit, which refuses a document whose tax lines disagree with the
// decisions they cite.
package document

import (
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// Type is FIN-001 §3's document type. QUOTE and COMMIT are deliberately not
// here: a quote is not a fiscal document and cannot become one by changing a
// field (ZTAX-FIN-REQ-0001, -0002), and a commit is the TaxDecision that
// documents cite, not a document of its own.
type Type string

// The document types.
const (
	TypeInvoice       Type = "INVOICE"
	TypeCreditNote    Type = "CREDIT_NOTE"
	TypeDebitNote     Type = "DEBIT_NOTE"
	TypePartialCredit Type = "PARTIAL_CREDIT"
	TypeVoid          Type = "VOID"
	// TypeRefund is a return of money or value. It is a separate type from
	// a credit (ZTAX-FIN-REQ-0015): a credit changes what is owed, a refund
	// moves what was paid, and either can happen without the other.
	TypeRefund      Type = "REFUND"
	TypeRebill      Type = "REBILL"
	TypeAdjustment  Type = "ADJUSTMENT"
	TypeAmendment   Type = "AMENDMENT"
	TypeRestatement Type = "RESTATEMENT"
)

// corrects reports whether a type corrects a predecessor and must name one.
func (t Type) corrects() bool {
	switch t {
	case TypeInvoice:
		return false
	case TypeDebitNote, TypeAdjustment:
		// May stand alone or follow a predecessor.
		return false
	}
	return true
}

func (t Type) valid() bool {
	switch t {
	case TypeInvoice, TypeCreditNote, TypeDebitNote, TypePartialCredit, TypeVoid, TypeRefund,
		TypeRebill, TypeAdjustment, TypeAmendment, TypeRestatement:
		return true
	}
	return false
}

// Status is FIN-001 §5's lifecycle state. A document is constructed as DRAFT,
// becomes authoritative at COMMITTED, and is immutable from then on: later
// states are new lifecycle records about the document, not edits to it.
type Status string

// The statuses.
const (
	StatusDraft             Status = "DRAFT"
	StatusValidated         Status = "VALIDATED"
	StatusCommitted         Status = "COMMITTED"
	StatusIssued            Status = "ISSUED"
	StatusDelivered         Status = "DELIVERED"
	StatusAccepted          Status = "ACCEPTED"
	StatusPartiallyCredited Status = "PARTIALLY_CREDITED"
	StatusFullyCredited     Status = "FULLY_CREDITED"
	StatusVoided            Status = "VOIDED"
	StatusRefunded          Status = "REFUNDED"
	StatusAmended           Status = "AMENDED"
	StatusDisputed          Status = "DISPUTED"
	StatusClosed            Status = "CLOSED"
	StatusSuspended         Status = "SUSPENDED"
)

// TaxLine is one tax on one line, as the document presents it.
type TaxLine struct {
	// Decision is the TaxDecision the amount comes from (ZTAX-FIN-REQ-0010).
	Decision  id.DecisionID
	Component string
	Amount    fiscal.Money
}

// Line is FIN-001 §6's fiscal line.
type Line struct {
	ID id.FiscalLineID
	// SourceLineRef is the BSS/ERP line it came from (ZTAX-FIN-REQ-0020).
	SourceLineRef string
	// ComponentInstance is the classified product component, where one
	// applies (ZTAX-FIN-REQ-0019).
	ComponentInstance string
	Net               fiscal.Money
	// Discount is the allocated discount, explicit and with the reference to
	// the allocation that produced it (ZTAX-FIN-REQ-0021).
	Discount      *fiscal.Money
	AllocationRef string
	Taxes         []TaxLine
	// RoundingAdjustment is explicit and bounded (ZTAX-FIN-REQ-0022).
	RoundingAdjustment *fiscal.Money
	// Predecessor is the line this one corrects, for credits, rebills and
	// amendments, with the amount of it affected (ZTAX-FIN-REQ-0013).
	Predecessor *id.FiscalLineID
}

// Document is one fiscal document.
type Document struct {
	ID          id.FiscalDocumentID
	TenantID    id.TenantID
	LegalEntity id.LegalEntityID
	Type        Type
	// Number is the legal, customer-visible document number. It is stored
	// apart from the canonical id and may be assigned by an authority
	// (ZTAX-FIN-REQ-0006).
	Number string
	// Root and Predecessors are the lineage (ZTAX-FIN-REQ-0007). An original
	// document is its own root.
	Root         id.FiscalDocumentID
	Predecessors []id.FiscalDocumentID
	// Reason is the correction's machine-readable reason (ZTAX-FIN-REQ-0008).
	Reason errs.ReasonCode
	// IssueDate and TaxPoint are separate facts (ZTAX-FIN-REQ-0009).
	IssueDate time.Time
	TaxPoint  time.Time
	Currency  fiscal.Currency
	Decisions []id.DecisionID
	Lines     []Line
	Status    Status
	// Restated marks a RESTATEMENT's figures as a recomputation, so they are
	// never read as the original historical position (ZTAX-FIN-REQ-0018).
	Restated bool
}

// Authoritative reports whether the document is committed fiscal history.
func (d Document) Authoritative() bool {
	return d.Status != StatusDraft && d.Status != StatusValidated
}

// Validate refuses a document that cannot be committed.
func (d Document) Validate() error {
	switch {
	case d.ID.IsZero() || d.TenantID.IsZero() || d.LegalEntity.IsZero():
		return fmt.Errorf("document: not scoped to a tenant and legal entity")
	case !d.Type.valid():
		return fmt.Errorf("document: type %q", d.Type)
	case d.Root.IsZero():
		return fmt.Errorf("document %s names no root", d.ID)
	case d.IssueDate.IsZero() || d.TaxPoint.IsZero():
		return fmt.Errorf("document %s has no issue date or no tax point", d.ID)
	case d.Currency == "":
		return fmt.Errorf("document %s has no currency", d.ID)
	}
	if d.Type.corrects() {
		if len(d.Predecessors) == 0 {
			return fmt.Errorf("document %s is a %s and names no predecessor", d.ID, d.Type)
		}
		if d.Reason == "" || !errs.Registered(d.Reason) {
			return fmt.Errorf("document %s is a %s and carries no registered reason", d.ID, d.Type)
		}
	} else if len(d.Predecessors) == 0 && d.Root != d.ID {
		return fmt.Errorf("document %s has no predecessor and is not its own root", d.ID)
	}
	if d.Type == TypeRestatement && !d.Restated {
		return fmt.Errorf("document %s is a restatement and is not marked restated", d.ID)
	}
	if d.Type != TypeRestatement && d.Restated {
		return fmt.Errorf("document %s is marked restated and is a %s", d.ID, d.Type)
	}
	lineIDs := map[id.FiscalLineID]bool{}
	for i, l := range d.Lines {
		if l.ID.IsZero() {
			return fmt.Errorf("document %s line %d has no id", d.ID, i)
		}
		if lineIDs[l.ID] {
			return fmt.Errorf("document %s repeats line %s", d.ID, l.ID)
		}
		lineIDs[l.ID] = true
		if l.Net.Currency() != d.Currency {
			return fmt.Errorf("document %s line %s is in %s", d.ID, l.ID, l.Net.Currency())
		}
		if l.Discount != nil && l.AllocationRef == "" {
			return fmt.Errorf("document %s line %s carries a discount with no allocation reference", d.ID, l.ID)
		}
		if d.Type.corrects() && d.Type != TypeRefund && l.Predecessor == nil {
			return fmt.Errorf("document %s line %s corrects nothing; a %s line names the line it corrects", d.ID, l.ID, d.Type)
		}
	}
	return nil
}

// RoundingBound is the largest rounding adjustment a line may carry, in the
// document currency, as content supplies it. A rounding adjustment above it is
// not a rounding adjustment (ZTAX-FIN-REQ-0022).
type RoundingBound = fiscal.Money

// DecisionTax is a pinned TaxDecision's tax for one component, the figure a
// document's tax line must agree with.
type DecisionTax struct {
	Decision  id.DecisionID
	Component string
	Amount    fiscal.Money
}

// Variance is a document-to-decision disagreement (FIN-001 §23
// DOCUMENT_DECISION_VARIANCE).
type Variance struct {
	Decision  id.DecisionID
	Component string
	Decided   *fiscal.Money
	Presented *fiscal.Money
}

// Commit makes a draft authoritative.
//
// It refuses a document whose required tax line names no decision
// (ZTAX-FIN-REQ-0097), and returns — rather than absorbs — every variance
// between the document's tax lines and the decisions they cite
// (ZTAX-FIN-REQ-0011, -0098): a document that disagrees with its decision is
// an explicit exception, and committing it anyway would make the document the
// truth and the decision a rumour. Rounding adjustments above bound are
// refused too.
func Commit(d Document, decided []DecisionTax, bound RoundingBound) (Document, []Variance, error) {
	if d.Status != StatusDraft && d.Status != StatusValidated {
		return Document{}, nil, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Document %s is %s; only a draft is committed.", d.ID, d.Status))
	}
	if err := d.Validate(); err != nil {
		return Document{}, nil, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, err.Error())
	}
	cited := map[id.DecisionID]bool{}
	for _, dec := range d.Decisions {
		cited[dec] = true
	}
	type key struct {
		dec  id.DecisionID
		comp string
	}
	want := map[key]fiscal.Money{}
	for _, t := range decided {
		want[key{t.Decision, t.Component}] = t.Amount
	}
	got := map[key]fiscal.Money{}
	for _, l := range d.Lines {
		if l.RoundingAdjustment != nil {
			mag := *l.RoundingAdjustment
			if mag.Sign() < 0 {
				mag = mag.Neg()
			}
			if over, err := mag.Cmp(bound); err != nil || over > 0 {
				return Document{}, nil, errs.Invalid("lines.roundingAdjustment", errs.ReasonInvalidValue,
					fmt.Sprintf("Line %s carries a rounding adjustment of %s, beyond the %s bound.", l.ID, l.RoundingAdjustment.CanonicalString(), bound.CanonicalString()))
			}
		}
		for _, t := range l.Taxes {
			if t.Decision.IsZero() || !cited[t.Decision] {
				return Document{}, nil, errs.Invalid("lines.taxes.decision", errs.ReasonMissingField,
					fmt.Sprintf("Line %s presents %s tax with no pinned TaxDecision; it cannot become authoritative.", l.ID, t.Component))
			}
			k := key{t.Decision, t.Component}
			sum, ok := got[k]
			if !ok {
				got[k] = t.Amount
				continue
			}
			var err error
			if got[k], err = sum.Add(t.Amount); err != nil {
				return Document{}, nil, err
			}
		}
	}
	var variances []Variance
	keys := map[key]bool{}
	for k := range want {
		keys[k] = true
	}
	for k := range got {
		keys[k] = true
	}
	ordered := make([]key, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].dec != ordered[j].dec {
			return ordered[i].dec.String() < ordered[j].dec.String()
		}
		return ordered[i].comp < ordered[j].comp
	})
	for _, k := range ordered {
		w, wok := want[k]
		g, gok := got[k]
		if wok && gok {
			if c, err := w.Cmp(g); err == nil && c == 0 {
				continue
			}
		}
		v := Variance{Decision: k.dec, Component: k.comp}
		if wok {
			w := w
			v.Decided = &w
		}
		if gok {
			g := g
			v.Presented = &g
		}
		variances = append(variances, v)
	}
	if len(variances) > 0 {
		return Document{}, variances, errs.New(errs.CategoryConflict, errs.ReasonConflicted,
			fmt.Sprintf("Document %s presents tax that differs from its pinned decisions in %d place(s).", d.ID, len(variances)))
	}
	out := d
	out.Status = StatusCommitted
	return out, nil, nil
}

// Correction is what a correcting document is built from.
type Correction struct {
	ID        id.FiscalDocumentID
	Type      Type
	Reason    errs.ReasonCode
	IssueDate time.Time
	TaxPoint  time.Time
	Number    string
	Decisions []id.DecisionID
	Lines     []Line
}

// Correct builds a correcting document as a successor of original. The
// original is passed by value and returned untouched: correction never
// mutates history (ZTAX-FIN-REQ-0003, -0014, -0016, -0017). The successor's
// root is the original's root, so a chain of corrections has one root however
// long it grows.
func Correct(original Document, c Correction) (Document, error) {
	if !original.Authoritative() {
		return Document{}, fmt.Errorf("document: %s is %s; only committed history is corrected", original.ID, original.Status)
	}
	if !c.Type.corrects() && c.Type != TypeDebitNote && c.Type != TypeAdjustment {
		return Document{}, fmt.Errorf("document: a %s does not correct a predecessor", c.Type)
	}
	if c.ID == original.ID {
		// FIN-REQ-0005: an id names one document, forever.
		return Document{}, fmt.Errorf("document: a correction reuses its original's id %s", c.ID)
	}
	predecessorLines := map[id.FiscalLineID]bool{}
	for _, l := range original.Lines {
		predecessorLines[l.ID] = true
	}
	for _, l := range c.Lines {
		if l.Predecessor != nil && !predecessorLines[*l.Predecessor] {
			return Document{}, fmt.Errorf("document: correction line %s names %s, which is not a line of %s", l.ID, *l.Predecessor, original.ID)
		}
	}
	d := Document{
		ID: c.ID, TenantID: original.TenantID, LegalEntity: original.LegalEntity, Type: c.Type,
		Number: c.Number, Root: original.Root, Predecessors: []id.FiscalDocumentID{original.ID},
		Reason: c.Reason, IssueDate: c.IssueDate, TaxPoint: c.TaxPoint, Currency: original.Currency,
		Decisions: c.Decisions, Lines: c.Lines, Status: StatusDraft, Restated: c.Type == TypeRestatement,
	}
	if c.Type == TypeVoid {
		// A void carries the original's lines negated and nothing else: it
		// cancels, it does not re-describe. The caller supplies one fresh line
		// id per original line, because a line id names one line forever.
		if len(c.Lines) != len(original.Lines) {
			return Document{}, fmt.Errorf("document: voiding %s needs %d fresh line ids, got %d", original.ID, len(original.Lines), len(c.Lines))
		}
		fresh := c.Lines
		d.Lines = nil
		for i, l := range original.Lines {
			if fresh[i].ID == l.ID {
				return Document{}, fmt.Errorf("document: a void reuses line id %s", l.ID)
			}
			nl := Line{ID: fresh[i].ID, SourceLineRef: l.SourceLineRef, ComponentInstance: l.ComponentInstance, Net: l.Net.Neg()}
			pred := l.ID
			nl.Predecessor = &pred
			for _, t := range l.Taxes {
				nl.Taxes = append(nl.Taxes, TaxLine{Decision: t.Decision, Component: t.Component, Amount: t.Amount.Neg()})
			}
			d.Lines = append(d.Lines, nl)
		}
		d.Decisions = original.Decisions
	}
	if err := d.Validate(); err != nil {
		return Document{}, err
	}
	return d, nil
}

// Chain is a document lineage, original first.
type Chain []Document

// Validate checks that every document after the first names a predecessor in
// the chain and shares the first's root.
func (c Chain) Validate() error {
	if len(c) == 0 {
		return nil
	}
	root := c[0].Root
	if root != c[0].ID {
		return fmt.Errorf("document: chain starts at %s, which is not its own root", c[0].ID)
	}
	seen := map[id.FiscalDocumentID]bool{c[0].ID: true}
	for _, d := range c[1:] {
		if d.Root != root {
			return fmt.Errorf("document: %s has root %s; the chain's root is %s", d.ID, d.Root, root)
		}
		if seen[d.ID] {
			return fmt.Errorf("document: %s appears twice in its chain", d.ID)
		}
		for _, p := range d.Predecessors {
			if !seen[p] {
				return fmt.Errorf("document: %s names predecessor %s, which does not precede it", d.ID, p)
			}
		}
		seen[d.ID] = true
	}
	return nil
}
