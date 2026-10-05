package document_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

var (
	tenant   = id.NewTenantID(uuid.MustParse("00000000-0000-7000-8000-000000000001"))
	entity   = id.NewLegalEntityID(uuid.MustParse("00000000-0000-7000-8000-000000000002"))
	decision = id.NewDecisionID(uuid.MustParse("00000000-0000-7000-8000-0000000000e1"))
	invID    = id.NewFiscalDocumentID(uuid.MustParse("00000000-0000-7000-8000-0000000000f1"))
	issued   = time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)
	supplied = time.Date(2027, 3, 28, 0, 0, 0, 0, time.UTC)
)

func lineID(n int) id.FiscalLineID {
	return id.NewFiscalLineID(uuid.MustParse("00000000-0000-7000-8000-0000000001" + string(rune('0'+n/10)) + string(rune('0'+n%10))))
}

func docID(n int) id.FiscalDocumentID {
	return id.NewFiscalDocumentID(uuid.MustParse("00000000-0000-7000-8000-0000000002" + string(rune('0'+n/10)) + string(rune('0'+n%10))))
}

func eur(t *testing.T, s string) fiscal.Money { return fiscaltest.Money(t, s, "EUR") }

func invoice(t *testing.T, tax string) document.Document {
	return document.Document{
		ID: invID, TenantID: tenant, LegalEntity: entity, Type: document.TypeInvoice, Number: "INV-2027-0001",
		Root: invID, IssueDate: issued, TaxPoint: supplied, Currency: "EUR", Decisions: []id.DecisionID{decision},
		Status: document.StatusDraft,
		Lines: []document.Line{{
			ID: lineID(1), SourceLineRef: "bss:order-9/line-1", ComponentInstance: "component:voice/mobile",
			Net: eur(t, "100.00"), Taxes: []document.TaxLine{{Decision: decision, Component: "vat", Amount: eur(t, tax)}},
		}},
	}
}

func decided(t *testing.T) []document.DecisionTax {
	return []document.DecisionTax{{Decision: decision, Component: "vat", Amount: eur(t, "21.00")}}
}

func committed(t *testing.T) document.Document {
	d, v, err := document.Commit(invoice(t, "21.00"), decided(t), eur(t, "0.01"))
	if err != nil {
		t.Fatalf("commit: %v %v", err, v)
	}
	return d
}

func TestFINREQ0001And0002AQuoteIsNotAFiscalDocument(t *testing.T) {
	// There is no QUOTE document type: a quote cannot be committed, filed or
	// reconciled as a document because it cannot be one.
	d := invoice(t, "21.00")
	d.Type = "QUOTE"
	if err := d.Validate(); err == nil {
		t.Fatal("a QUOTE validated as a fiscal document")
	}
	if invoice(t, "21.00").Authoritative() {
		t.Fatal("a draft reports itself authoritative")
	}
	if !committed(t).Authoritative() {
		t.Fatal("a committed document is not authoritative")
	}
}

func TestFINREQ0011And0098DocumentTaxMustAgreeWithItsDecision(t *testing.T) {
	_, variances, err := document.Commit(invoice(t, "21.01"), decided(t), eur(t, "0.01"))
	if err == nil {
		t.Fatal("a document presenting 21.01 against a decided 21.00 committed")
	}
	if len(variances) != 1 || variances[0].Decided.String() != "21.00" || variances[0].Presented.String() != "21.01" {
		t.Fatalf("variances %+v", variances)
	}
}

func TestFINREQ0097MissingDecisionBlocksCommit(t *testing.T) {
	d := invoice(t, "21.00")
	d.Decisions = nil
	if _, _, err := document.Commit(d, decided(t), eur(t, "0.01")); err == nil {
		t.Fatal("a tax line with no pinned decision committed")
	}
}

func TestFINREQ0022RoundingAdjustmentIsExplicitAndBounded(t *testing.T) {
	d := invoice(t, "21.00")
	adj := eur(t, "0.05")
	d.Lines[0].RoundingAdjustment = &adj
	if _, _, err := document.Commit(d, decided(t), eur(t, "0.01")); err == nil {
		t.Fatal("a 0.05 rounding adjustment passed a 0.01 bound")
	}
}

func TestFINREQ0006And0009NumberTaxPointAndIssueDateAreSeparate(t *testing.T) {
	d := committed(t)
	if d.Number == d.ID.String() || d.IssueDate.Equal(d.TaxPoint) {
		t.Fatalf("number %q id %s issue %v tax point %v", d.Number, d.ID, d.IssueDate, d.TaxPoint)
	}
	d.TaxPoint = time.Time{}
	if err := d.Validate(); err == nil {
		t.Fatal("a document with no tax point validated")
	}
}

func TestFINREQ0003And0004CorrectionIsALinkedSuccessor(t *testing.T) {
	orig := committed(t)
	pred := lineID(1)
	credit, err := document.Correct(orig, document.Correction{
		ID: docID(2), Type: document.TypePartialCredit, Reason: errs.ReasonInvalidValue, IssueDate: issued, TaxPoint: supplied,
		Decisions: []id.DecisionID{decision},
		Lines: []document.Line{{ID: lineID(2), Net: eur(t, "-50.00"), Predecessor: &pred,
			Taxes: []document.TaxLine{{Decision: decision, Component: "vat", Amount: eur(t, "-10.50")}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if orig.Status != document.StatusCommitted || orig.Lines[0].Net.String() != "100.00" {
		t.Fatal("correcting changed the original")
	}
	if credit.Root != orig.ID || len(credit.Predecessors) != 1 || credit.Predecessors[0] != orig.ID {
		t.Fatalf("credit lineage root %s predecessors %v", credit.Root, credit.Predecessors)
	}
}

func TestFINREQ0005AndFINREQ0013CorrectionsUseFreshIDsAndNamePredecessorLines(t *testing.T) {
	orig := committed(t)
	if _, err := document.Correct(orig, document.Correction{ID: orig.ID, Type: document.TypeCreditNote, Reason: errs.ReasonInvalidValue,
		IssueDate: issued, TaxPoint: supplied}); err == nil {
		t.Fatal("a correction reused its original's id")
	}
	if _, err := document.Correct(orig, document.Correction{ID: docID(3), Type: document.TypePartialCredit, Reason: errs.ReasonInvalidValue,
		IssueDate: issued, TaxPoint: supplied, Lines: []document.Line{{ID: lineID(3), Net: eur(t, "-1")}}}); err == nil {
		t.Fatal("a partial credit line naming no predecessor line was accepted")
	}
}

func TestFINREQ0008CorrectionReasonIsMachineReadable(t *testing.T) {
	orig := committed(t)
	if _, err := document.Correct(orig, document.Correction{ID: docID(4), Type: document.TypeCreditNote, Reason: "customer was cross",
		IssueDate: issued, TaxPoint: supplied}); err == nil {
		t.Fatal("a free-text correction reason was accepted")
	}
}

func TestFINREQ0014VoidPreservesTheOriginal(t *testing.T) {
	orig := committed(t)
	void, err := document.Correct(orig, document.Correction{ID: docID(5), Type: document.TypeVoid, Reason: errs.ReasonInvalidValue,
		IssueDate: issued, TaxPoint: supplied, Lines: []document.Line{{ID: lineID(5)}}})
	if err != nil {
		t.Fatal(err)
	}
	if void.Lines[0].Net.String() != "-100.00" || void.Lines[0].Taxes[0].Amount.String() != "-21.00" || void.Lines[0].ID == orig.Lines[0].ID {
		t.Fatalf("void line %+v", void.Lines[0])
	}
	if orig.Lines[0].Net.String() != "100.00" {
		t.Fatal("voiding altered the original")
	}
}

func TestFINREQ0015RefundIsNotACredit(t *testing.T) {
	if document.TypeRefund == document.TypeCreditNote {
		t.Fatal("refund and credit share a type")
	}
	orig := committed(t)
	refund, err := document.Correct(orig, document.Correction{ID: docID(6), Type: document.TypeRefund, Reason: errs.ReasonInvalidValue,
		IssueDate: issued, TaxPoint: supplied})
	if err != nil || refund.Type != document.TypeRefund {
		t.Fatalf("refund: %v", err)
	}
}

func TestFINREQ0016And0007RebillPreservesTheChain(t *testing.T) {
	orig := committed(t)
	credit, _ := document.Correct(orig, document.Correction{ID: docID(7), Type: document.TypeCreditNote, Reason: errs.ReasonInvalidValue,
		IssueDate: issued, TaxPoint: supplied})
	credit.Status = document.StatusCommitted
	pred := lineID(1)
	if _, err := document.Correct(credit, document.Correction{ID: docID(8), Type: document.TypeRebill, Reason: errs.ReasonInvalidValue,
		IssueDate: issued, TaxPoint: supplied, Lines: []document.Line{{ID: lineID(8), Net: eur(t, "90.00"), Predecessor: &pred}}}); err == nil {
		// The rebill line names a line of the original, not of the credit it
		// directly follows; that must be refused.
		t.Fatal("a rebill line named a line of a document it does not follow")
	}
	rebill, err := document.Correct(credit, document.Correction{ID: docID(8), Type: document.TypeRebill, Reason: errs.ReasonInvalidValue,
		IssueDate: issued, TaxPoint: supplied})
	if err != nil {
		t.Fatal(err)
	}
	if err := (document.Chain{orig, credit, rebill}).Validate(); err != nil {
		t.Fatalf("chain: %v", err)
	}
	if rebill.Root != orig.ID {
		t.Fatalf("rebill root %s", rebill.Root)
	}
}

func TestFINREQ0018RestatementIsDistinguishedFromHistory(t *testing.T) {
	orig := committed(t)
	r, err := document.Correct(orig, document.Correction{ID: docID(9), Type: document.TypeRestatement, Reason: errs.ReasonInvalidValue,
		IssueDate: issued, TaxPoint: supplied})
	if err != nil || !r.Restated {
		t.Fatalf("restatement %v restated=%t", err, r.Restated)
	}
	orig.Restated = true
	if err := orig.Validate(); err == nil {
		t.Fatal("an invoice marked restated validated")
	}
}
