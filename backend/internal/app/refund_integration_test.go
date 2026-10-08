//go:build integration

package app_test

import (
	"sync"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/settlement"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// Refunds against the real store. The worked pack posts a decision's VAT and
// eco levy to TAX_COLLECTED_LIABILITY: 100.00 net and 3 units collect 21.00
// VAT and 0.11 levy, so 21.11 EUR is refundable.

func (c *fiscalCell) refunds() *app.RefundService {
	return app.NewRefundService(c.store.Decisions(), c.store.Journals(), c.store.Refunds(), c.store.Outbox(),
		c.store, c.clock, idgen.V7{}, app.NewIdempotency(c.store.Idempotency(), c.store, c.clock))
}

// refund requests a refund and reports the settled response; a refused
// request renders its reason code as the body.
func (c *fiscalCell) refund(t *testing.T, svc *app.RefundService, key string, decision id.DecisionID, amount string) (app.Settled, error) {
	t.Helper()
	return svc.Request(c.ctx, app.RefundInput{
		IdempotencyKey: key, Decision: decision, Amount: fiscaltest.Money(t, amount, "EUR"), PaymentRef: "psp:ch_" + key,
		Render: func(v app.RefundView) ([]byte, error) { return []byte(v.Refund.ID.String()), nil },
		RenderFailure: func(err error) app.Response {
			return app.Response{Status: 400, Body: []byte(errs.ReasonOf(err))}
		},
	})
}

func (c *fiscalCell) mustRefund(t *testing.T, svc *app.RefundService, key string, decision id.DecisionID, amount string) id.RefundID {
	t.Helper()
	s, err := c.refund(t, svc, key, decision, amount)
	if err != nil || s.Status != 201 {
		t.Fatalf("refund %s: %v %d %s", key, err, s.Status, s.Body)
	}
	refundID, err := id.ParseRefundID(string(s.Body))
	if err != nil {
		t.Fatal(err)
	}
	return refundID
}

func TestIntegrationFINREQ0059RefundIsBoundedByCollectedTax(t *testing.T) {
	c := openFiscalCell(t)
	svc := c.refunds()
	d := c.mustCommit(t, "k-r1", "INV-R1/1", "100.00", "3", nil)

	first := c.mustRefund(t, svc, "rf-1", d, "21.00")

	// 0.11 is left. Asking for more is refused, and the refusal is settled
	// under its key like any deterministic failure.
	s, err := c.refund(t, svc, "rf-2", d, "0.12")
	if err != nil || s.Status != 400 || string(s.Body) != string(errs.ReasonInvalidValue) {
		t.Fatalf("an over-refund: %v %d %s", err, s.Status, s.Body)
	}
	// A retry of the first key returns the first refund, not a second one.
	again, err := c.refund(t, svc, "rf-1", d, "21.00")
	if err != nil || !again.Replayed || string(again.Body) != first.String() {
		t.Fatalf("a retried refund: %v replayed=%v %s", err, again.Replayed, again.Body)
	}

	// A timeout leaves the refund UNCERTAIN, and an UNCERTAIN refund still
	// holds its tax: it may have paid.
	v, err := svc.Report(c.ctx, first, app.ReportInput{Outcome: settlement.RefundTimedOut})
	if err != nil || v.Current().Status != settlement.RefundUncertain {
		t.Fatalf("timed out: %v %+v", err, v.Current())
	}
	if s, _ := c.refund(t, svc, "rf-3", d, "1.00"); s.Status != 400 {
		t.Fatalf("a refund over an UNCERTAIN one was admitted: %d %s", s.Status, s.Body)
	}

	// The provider declines it: FAILED releases the tax.
	if v, err = svc.Report(c.ctx, first, app.ReportInput{Outcome: settlement.RefundDeclined, ExternalRef: "psp:re_x"}); err != nil {
		t.Fatal(err)
	}
	if v.Current().Status != settlement.RefundFailed || len(v.History) != 3 {
		t.Fatalf("declined: %+v", v.History)
	}
	c.mustRefund(t, svc, "rf-4", d, "21.11")

	// Nothing leaves FAILED.
	if _, err := svc.Report(c.ctx, first, app.ReportInput{Outcome: settlement.RefundSucceeded}); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("a report on a failed refund: %v", err)
	}
	// Nothing was posted for any of it: no refund has completed.
	refunds, err := c.store.Journals().BySource(c.ctx, app.SourceRefundCompleted, first.String())
	if err != nil || len(refunds) != 0 {
		t.Fatalf("journals for a failed refund: %v %+v", err, refunds)
	}
}

func TestIntegrationFINREQ0015CompletedRefundPostsAndLeavesTheDecision(t *testing.T) {
	c := openFiscalCell(t)
	svc := c.refunds()
	d := c.mustCommit(t, "k-rp1", "INV-RP/1", "100.00", "3", nil)
	before, err := c.svc.Decision(c.ctx, d)
	if err != nil {
		t.Fatal(err)
	}

	rf := c.mustRefund(t, svc, "rp-1", d, "21.00")
	if _, err := svc.Report(c.ctx, rf, app.ReportInput{Outcome: settlement.RefundAccepted}); err != nil {
		t.Fatal(err)
	}
	done, err := svc.Report(c.ctx, rf, app.ReportInput{Outcome: settlement.RefundSucceeded, ExternalRef: "psp:re_1"})
	if err != nil || done.Current().Status != settlement.RefundCompleted || done.Current().Seq != 3 {
		t.Fatalf("succeeded: %v %+v", err, done.History)
	}
	// The same report arriving twice is one move.
	same, err := svc.Report(c.ctx, rf, app.ReportInput{Outcome: settlement.RefundSucceeded, ExternalRef: "psp:re_1"})
	if err != nil || len(same.History) != 3 {
		t.Fatalf("a repeated report: %v %d events", err, len(same.History))
	}

	journals, err := c.store.Journals().BySource(c.ctx, app.SourceRefundCompleted, rf.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(journals) != 1 || journals[0].Type != subledger.JournalRefund || len(journals[0].Lines) != 2 {
		t.Fatalf("refund journals %+v", journals)
	}
	for _, l := range journals[0].Lines {
		if l.Amount.CanonicalString() != "21.00" || l.Decision == nil || *l.Decision != d {
			t.Fatalf("refund line %+v", l)
		}
		switch {
		case l.Account == subledger.AccountTaxCollectedLiability && l.Side == subledger.Debit:
		case l.Account == subledger.AccountTaxCashClearing && l.Side == subledger.Credit:
		default:
			t.Fatalf("refund line on %s %s", l.Account, l.Side)
		}
	}

	// The decision is exactly what it was: a refund re-determines nothing.
	after, err := c.svc.Decision(c.ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Record.ResultDigest.Equal(before.Record.ResultDigest) {
		t.Fatal("refunding the decision changed its recorded result")
	}

	// Each state change wrote its event in its own transaction: the request,
	// then the two moves.
	var n int
	if err := ownerPool(t).QueryRow(c.ctx,
		`SELECT count(*) FROM ztax.outbox WHERE tenant_id = $1 AND aggregate_key = $2`,
		c.tenant.UUID(), "refund/"+rf.String()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("%d refund events, want 3 (requested, pending, completed)", n)
	}
}

func TestIntegrationRefundRefusesASupersededDecision(t *testing.T) {
	c := openFiscalCell(t)
	svc := c.refunds()
	original := c.mustCommit(t, "k-rs1", "INV-RS/1", "100.00", "3", nil)
	c.mustCommit(t, "k-rs2", "INV-RS/1", "50.00", "3", &original)

	s, err := c.refund(t, svc, "rs-1", original, "1.00")
	if err != nil || s.Status != 400 || string(s.Body) != string(errs.ReasonStateTransitionInvalid) {
		t.Fatalf("a refund of a superseded decision: %v %d %s", err, s.Status, s.Body)
	}
}

func TestIntegrationConcurrentRefundsCannotOverspend(t *testing.T) {
	c := openFiscalCell(t)
	svc := c.refunds()
	d := c.mustCommit(t, "k-rc1", "INV-RC/1", "100.00", "3", nil)

	// Eight refunds of 5.00 against 21.11: whatever the interleaving, at most
	// four can be admitted, because admission serializes on the decision.
	const n = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		admitted int
	)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := c.refund(t, svc, "rc-"+string(rune('a'+i)), d, "5.00")
			if err != nil {
				t.Error(err)
				return
			}
			if s.Status == 201 {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != 4 {
		t.Fatalf("%d refunds of 5.00 admitted against 21.11 collected, want 4", admitted)
	}
	states, err := c.store.Refunds().ForDecision(c.ctx, d)
	if err != nil || len(states) != 4 {
		t.Fatalf("stored refunds: %v %d", err, len(states))
	}
}
