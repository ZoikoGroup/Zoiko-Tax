//go:build integration

package app_test

import (
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

// Fiscal documents against PostgreSQL. The worked pack posts TAX_VAT and
// TAX_ECO_LEVY; a line of 100.00 net and 3 units decides 21.00 and 0.11.

func (c *fiscalCell) documents() *app.DocumentService {
	return app.NewDocumentService(c.svc, c.store.Documents(), app.NewIdempotency(c.store.Idempotency(), c.store, c.clock))
}

func (c *fiscalCell) docLine(t *testing.T, d id.DecisionID, net, vat, levy string) app.LineInput {
	t.Helper()
	l := app.LineInput{SourceLineRef: "bss:line", Net: fiscaltest.Money(t, net, "EUR"),
		Taxes: []document.TaxLine{{Decision: d, Component: "TAX_VAT", Amount: fiscaltest.Money(t, vat, "EUR")}}}
	if levy != "" {
		l.Taxes = append(l.Taxes, document.TaxLine{Decision: d, Component: "TAX_ECO_LEVY", Amount: fiscaltest.Money(t, levy, "EUR")})
	}
	return l
}

func render(v app.DocumentView) ([]byte, error) { return []byte(v.Record.Document.ID.String()), nil }

func failure(err error) app.Response {
	return app.Response{Status: 400, Body: []byte(errs.ReasonOf(err))}
}

func (c *fiscalCell) issue(t *testing.T, svc *app.DocumentService, key string, lines ...app.LineInput) (app.Settled, error) {
	t.Helper()
	return svc.Issue(c.ctx, app.IssueInput{
		IdempotencyKey: key, Type: document.TypeInvoice, Number: "INV-" + key,
		IssueDate: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), TaxPoint: c.event, Currency: "EUR",
		External: &id.ExternalReference{SourceSystem: "bss", Namespace: "invoice", Value: key},
		Lines:    lines, Render: render, RenderFailure: failure,
	})
}

// mustDoc is curried so a two-valued call can be its argument:
// mustDoc(t)(c.issue(...)).
func mustDoc(t *testing.T) func(app.Settled, error) id.FiscalDocumentID {
	return func(s app.Settled, err error) id.FiscalDocumentID {
		t.Helper()
		if err != nil || s.Status != 201 {
			t.Fatalf("document: %v %d %s", err, s.Status, s.Body)
		}
		docID, err := id.ParseFiscalDocumentID(string(s.Body))
		if err != nil {
			t.Fatal(err)
		}
		return docID
	}
}

func refused(t *testing.T, s app.Settled, err error, want errs.ReasonCode) {
	t.Helper()
	if err != nil {
		if errs.ReasonOf(err) != want {
			t.Fatalf("refused with %v, want %s", err, want)
		}
		return
	}
	if s.Status < 400 || string(s.Body) != string(want) {
		t.Fatalf("got %d %s, want a refusal %s", s.Status, s.Body, want)
	}
}

func (c *fiscalCell) correct(t *testing.T, svc *app.DocumentService, key string, orig id.FiscalDocumentID, typ document.Type,
	cancel []id.FiscalLineID, lines ...app.LineInput) (app.Settled, error) {
	t.Helper()
	return svc.Correct(c.ctx, app.CorrectionInput{
		IdempotencyKey: key, Original: orig, Type: typ, Reason: errs.ReasonCorrectionCustomerCancellation,
		IssueDate: time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC), TaxPoint: c.event, CancelLines: cancel,
		Lines: lines, Render: render, RenderFailure: failure,
	})
}

func (c *fiscalCell) status(t *testing.T, svc *app.DocumentService, docID id.FiscalDocumentID) document.Status {
	t.Helper()
	v, err := svc.Document(c.ctx, docID)
	if err != nil {
		t.Fatal(err)
	}
	return v.Status()
}

// ZTAX-FIN-REQ-0010, -0011: a document's tax is its decisions' tax.
func TestIntegrationFINREQ0011ADocumentPresentsExactlyWhatWasDecided(t *testing.T) {
	c := openFiscalCell(t)
	svc := c.documents()
	d := c.mustCommit(t, "k-doc1", "INV-DOC1/1", "100.00", "3", nil)

	// Presenting a different amount, or leaving out a decided component, is
	// a variance and nothing is committed.
	s, err := c.issue(t, svc, "doc-bad", c.docLine(t, d, "100.00", "20.00", "0.11"))
	refused(t, s, err, errs.ReasonConflicted)
	s, err = c.issue(t, svc, "doc-missing", c.docLine(t, d, "100.00", "21.00", ""))
	refused(t, s, err, errs.ReasonConflicted)

	inv := mustDoc(t)(c.issue(t, svc, "doc-1", c.docLine(t, d, "100.00", "21.00", "0.11")))
	v, err := svc.Document(c.ctx, inv)
	if err != nil {
		t.Fatal(err)
	}
	r := v.Record
	if r.Net.CanonicalString() != "100.00" || r.Tax.CanonicalString() != "21.11" || r.Gross.CanonicalString() != "121.11" ||
		r.Document.Root != inv || v.Status() != document.StatusCommitted || r.Document.External == nil ||
		len(r.Document.Lines) != 1 || len(r.Document.Lines[0].Taxes) != 2 || r.Document.Decisions[0] != d {
		t.Fatalf("committed document %+v status %s", r, v.Status())
	}

	// The same decision is not billed twice while the first still stands.
	s, err = c.issue(t, svc, "doc-2", c.docLine(t, d, "100.00", "21.00", "0.11"))
	refused(t, s, err, errs.ReasonAlreadyExists)

	// A superseded decision is not billed at all.
	d2 := c.mustCommit(t, "k-doc2", "INV-DOC2/1", "100.00", "3", nil)
	c.mustCommit(t, "k-doc2b", "INV-DOC2/1", "40.00", "3", &d2)
	s, err = c.issue(t, svc, "doc-3", c.docLine(t, d2, "100.00", "21.00", "0.11"))
	refused(t, s, err, errs.ReasonStateTransitionInvalid)
}

// ZTAX-FIN-REQ-0003, -0004, -0012, -0013: corrections are linked successors
// that cancel exactly what was charged, and the original is never edited.
func TestIntegrationFINREQ0004CreditsVoidsAndRebillsAreLinkedSuccessors(t *testing.T) {
	c := openFiscalCell(t)
	svc := c.documents()
	d1 := c.mustCommit(t, "k-dc1", "INV-DC/1", "100.00", "3", nil)
	d2 := c.mustCommit(t, "k-dc2", "INV-DC/2", "100.00", "3", nil)
	inv := mustDoc(t)(c.issue(t, svc, "dc-inv", c.docLine(t, d1, "100.00", "21.00", "0.11"), c.docLine(t, d2, "100.00", "21.00", "0.11")))
	orig, err := svc.Document(c.ctx, inv)
	if err != nil {
		t.Fatal(err)
	}
	line1, line2 := orig.Record.Document.Lines[0].ID, orig.Record.Document.Lines[1].ID

	// A void of a partly credited document is refused; first, credit line 1.
	credit1 := mustDoc(t)(c.correct(t, svc, "dc-cn1", inv, document.TypeCreditNote, []id.FiscalLineID{line1}))
	if got := c.status(t, svc, inv); got != document.StatusPartiallyCredited {
		t.Fatalf("after crediting one line of two: %s", got)
	}
	cn, _ := svc.Document(c.ctx, credit1)
	if cn.Record.Tax.CanonicalString() != "-21.11" || cn.Record.Document.Root != inv ||
		cn.Record.Document.Predecessors[0] != inv || *cn.Record.Document.Lines[0].Predecessor != line1 {
		t.Fatalf("credit note %+v", cn.Record)
	}
	s, err := c.correct(t, svc, "dc-void", inv, document.TypeVoid, nil)
	refused(t, s, err, errs.ReasonStateTransitionInvalid)
	s, err = c.correct(t, svc, "dc-cn-again", inv, document.TypeCreditNote, []id.FiscalLineID{line1})
	refused(t, s, err, errs.ReasonStateTransitionInvalid)

	// Crediting the rest fully credits it.
	mustDoc(t)(c.correct(t, svc, "dc-cn2", inv, document.TypeCreditNote, []id.FiscalLineID{line2}))
	if got := c.status(t, svc, inv); got != document.StatusFullyCredited {
		t.Fatalf("after crediting every line: %s", got)
	}
	s, err = c.correct(t, svc, "dc-cn3", inv, document.TypeCreditNote, nil)
	refused(t, s, err, errs.ReasonStateTransitionInvalid)

	// A rebill replaces it, citing the same decisions again now they are released.
	rebill := mustDoc(t)(c.correct(t, svc, "dc-rebill", inv, document.TypeRebill, nil,
		withPredecessor(c.docLine(t, d1, "100.00", "21.00", "0.11"), line1)))

	// The original is exactly what was committed; the chain is four documents with one root.
	again, _ := svc.Document(c.ctx, inv)
	if again.Record.Tax.CanonicalString() != orig.Record.Tax.CanonicalString() || len(again.Record.Document.Lines) != 2 {
		t.Fatal("the original changed")
	}
	chain, err := svc.Lineage(c.ctx, rebill)
	if err != nil || len(chain) != 4 {
		t.Fatalf("lineage %v %d", err, len(chain))
	}
	ch := make(document.Chain, len(chain))
	for i, v := range chain {
		ch[i] = v.Record.Document
	}
	if err := ch.Validate(); err != nil || ch[0].ID != inv {
		t.Fatalf("the chain: %v", err)
	}
}

func TestIntegrationVoidCancelsEverythingAndReleasesTheDecision(t *testing.T) {
	c := openFiscalCell(t)
	svc := c.documents()
	d := c.mustCommit(t, "k-dv", "INV-DV/1", "100.00", "3", nil)
	inv := mustDoc(t)(c.issue(t, svc, "dv-inv", c.docLine(t, d, "100.00", "21.00", "0.11")))
	void := mustDoc(t)(c.correct(t, svc, "dv-void", inv, document.TypeVoid, nil))
	if got := c.status(t, svc, inv); got != document.StatusVoided {
		t.Fatalf("after a void: %s", got)
	}
	v, _ := svc.Document(c.ctx, void)
	if v.Record.Net.CanonicalString() != "-100.00" || v.Record.Tax.CanonicalString() != "-21.11" || v.Record.Document.Reason != errs.ReasonCorrectionCustomerCancellation {
		t.Fatalf("void %+v", v.Record)
	}
	s, err := c.correct(t, svc, "dv-void2", inv, document.TypeVoid, nil)
	refused(t, s, err, errs.ReasonStateTransitionInvalid)
	// A void is not itself correctable, and a reason outside the register is refused.
	s, err = c.correct(t, svc, "dv-void3", void, document.TypeVoid, nil)
	refused(t, s, err, errs.ReasonStateTransitionInvalid)
	if _, err := svc.Correct(c.ctx, app.CorrectionInput{IdempotencyKey: "dv-r", Original: inv, Type: document.TypeRebill,
		Reason: errs.ReasonInvalidValue, Render: render, RenderFailure: failure}); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("an unregistered correction reason: %v", err)
	}
	// Released: the decision may be billed by a fresh invoice.
	mustDoc(t)(c.issue(t, svc, "dv-inv2", c.docLine(t, d, "100.00", "21.00", "0.11")))
}

func withPredecessor(l app.LineInput, pred id.FiscalLineID) app.LineInput {
	l.Predecessor = &pred
	return l
}
