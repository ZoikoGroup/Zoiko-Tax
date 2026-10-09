package document_test

import (
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

func twoLineInvoice(t *testing.T) document.Document {
	d := committed(t)
	d.Lines = append(d.Lines, document.Line{
		ID: lineID(2), SourceLineRef: "bss:order-9/line-2", Net: eur(t, "50.00"),
		Taxes: []document.TaxLine{{Decision: decision, Component: "vat", Amount: eur(t, "10.50")}},
	})
	return d
}

// ZTAX-FIN-REQ-0013: a credit note cancels exactly what a line charged — net
// and every tax negated, nothing recomputed — and names the line it cancels.
func TestFINREQ0013ACreditNamesAndNegatesExactlyTheLinesItCancels(t *testing.T) {
	orig := twoLineInvoice(t)
	lines, err := document.Negate(orig, []id.FiscalLineID{lineID(2)}, []id.FiscalLineID{lineID(12)})
	if err != nil {
		t.Fatal(err)
	}
	l := lines[0]
	if l.Net.CanonicalString() != "-50.00" || l.Taxes[0].Amount.CanonicalString() != "-10.50" ||
		l.Predecessor == nil || *l.Predecessor != lineID(2) || l.ID != lineID(12) {
		t.Fatalf("credit line %+v", l)
	}
	want := document.Cancelled(orig, lines)
	if len(want) != 1 || want[0].Amount.CanonicalString() != "-10.50" || want[0].Decision != decision {
		t.Fatalf("cancelled tax %+v", want)
	}

	// The credit note commits against what it cancels, and only that.
	credit, err := document.Correct(orig, document.Correction{
		ID: docID(2), Type: document.TypeCreditNote, Reason: errs.ReasonCorrectionCustomerCancellation,
		IssueDate: issued, TaxPoint: supplied, Decisions: []id.DecisionID{decision}, Lines: lines,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, v, err := document.Commit(credit, want, eur(t, "0.00")); err != nil {
		t.Fatalf("commit: %v %v", err, v)
	}

	for name, call := range map[string]func() error{
		"an unknown line": func() error {
			_, err := document.Negate(orig, []id.FiscalLineID{lineID(9)}, []id.FiscalLineID{lineID(19)})
			return err
		},
		"a line twice": func() error {
			_, err := document.Negate(orig, []id.FiscalLineID{lineID(1), lineID(1)}, []id.FiscalLineID{lineID(11), lineID(12)})
			return err
		},
		"a reused line id": func() error {
			_, err := document.Negate(orig, []id.FiscalLineID{lineID(1)}, []id.FiscalLineID{lineID(1)})
			return err
		},
		"nothing": func() error { _, err := document.Negate(orig, nil, nil); return err },
	} {
		if call() == nil {
			t.Errorf("%s was negated", name)
		}
	}
}

func TestLifecycleMovesThePredecessorAndNeverItself(t *testing.T) {
	for _, c := range []struct {
		t     document.Type
		fully bool
		want  document.Status
		moves bool
	}{
		{document.TypeVoid, false, document.StatusVoided, true},
		{document.TypeCreditNote, true, document.StatusFullyCredited, true},
		{document.TypeCreditNote, false, document.StatusPartiallyCredited, true},
		{document.TypeRebill, false, "", false},
	} {
		got, moves := document.After(c.t, c.fully)
		if got != c.want || moves != c.moves {
			t.Errorf("%s (fully=%v): %s %v", c.t, c.fully, got, moves)
		}
	}
	// A charge that stands can be cancelled; a cancelled one can be rebilled;
	// nothing else.
	if !document.CanCorrect(document.StatusCommitted, document.TypeVoid) ||
		!document.CanCorrect(document.StatusPartiallyCredited, document.TypeCreditNote) ||
		document.CanCorrect(document.StatusVoided, document.TypeCreditNote) ||
		document.CanCorrect(document.StatusCommitted, document.TypeRebill) ||
		!document.CanCorrect(document.StatusFullyCredited, document.TypeRebill) {
		t.Fatal("correction admissibility")
	}
	if !document.TypeRebill.Bills() || document.TypeCreditNote.Bills() || !document.StatusVoided.Releases() {
		t.Fatal("billing and release")
	}
}

func TestFINREQ0008CorrectionReasonsAreRegisteredAndNamed(t *testing.T) {
	if !document.IsCorrectionReason(errs.ReasonCorrectionPricingError) {
		t.Fatal("a registered correction reason was refused")
	}
	for _, r := range []errs.ReasonCode{errs.ReasonInvalidValue, "CORRECTION_UNREGISTERED", ""} {
		if document.IsCorrectionReason(r) {
			t.Errorf("%q was accepted as a correction reason", r)
		}
	}
}

func TestTotalsAndExternalReference(t *testing.T) {
	d := twoLineInvoice(t)
	net, tax, gross, err := document.Totals(d)
	if err != nil || net.CanonicalString() != "150.00" || tax.CanonicalString() != "31.50" || gross.CanonicalString() != "181.50" {
		t.Fatalf("totals %s %s %s %v", net.CanonicalString(), tax.CanonicalString(), gross.CanonicalString(), err)
	}
	d.External = &id.ExternalReference{SourceSystem: "bss", Namespace: "invoice", Value: ""}
	if d.Validate() == nil {
		t.Fatal("an external reference with no value validated")
	}
	d.External.Value = "INV-77"
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}
