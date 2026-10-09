//go:build integration

package app_test

import (
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/reconciliation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/settlement"
)

// Reconciliation of September against PostgreSQL. Each decision of 100.00 net
// and 3 units decides 21.11 of tax: 21.00 VAT and 0.11 levy.

func TestIntegrationFINREQ0077ReconciliationTracesEachStageAndRecordsResolutions(t *testing.T) {
	c := openFiscalCell(t)
	docs := c.documents()
	refunds := c.refunds()
	svc := app.NewReconciliationService(docs, c.store.Refunds(), c.store.Reconciliations())

	invoiced := c.mustCommit(t, "k-r-1", "INV-RC1/1", "100.00", "3", nil)
	undocumented := c.mustCommit(t, "k-r-2", "INV-RC2/1", "100.00", "3", nil)
	voided := c.mustCommit(t, "k-r-3", "INV-RC3/1", "100.00", "3", nil)
	mustDoc(t)(c.issue(t, docs, "r-inv1", c.docLine(t, invoiced, "100.00", "21.00", "0.11")))
	inv3 := mustDoc(t)(c.issue(t, docs, "r-inv3", c.docLine(t, voided, "100.00", "21.00", "0.11")))
	mustDoc(t)(c.correct(t, docs, "r-void3", inv3, document.TypeVoid, nil))
	rf := c.mustRefund(t, refunds, "r-rf1", invoiced, "21.00")
	if _, err := refunds.Report(c.ctx, rf, app.ReportInput{Outcome: settlement.RefundSucceeded}); err != nil {
		t.Fatal(err)
	}

	v, err := svc.Run(c.ctx, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]reconciliation.RunItem{}
	for _, it := range v.Items {
		byKey[string(it.Stage)+"|"+string(it.Key)] = it.RunItem
	}
	expect := func(stage reconciliation.Stage, key string, want reconciliation.Status, variance string) reconciliation.RunItem {
		t.Helper()
		it, ok := byKey[string(stage)+"|"+key]
		if !ok {
			t.Fatalf("no %s item for %s", stage, key)
		}
		got := ""
		if it.Variance != nil {
			got = it.Variance.CanonicalString()
		}
		if it.Status != want || got != variance {
			t.Fatalf("%s %s: %s variance %q, want %s %q", stage, key, it.Status, got, want, variance)
		}
		return it
	}
	expect(reconciliation.R1CalculatedToDocument, "decision:"+invoiced.String(), reconciliation.StatusMatched, "0.00")
	missing := expect(reconciliation.R1CalculatedToDocument, "decision:"+undocumented.String(), reconciliation.StatusMissing, "")
	expect(reconciliation.R1CalculatedToDocument, "decision:"+voided.String(), reconciliation.StatusUnmatched, "-21.11")
	for _, d := range []string{invoiced.String(), undocumented.String(), voided.String()} {
		expect(reconciliation.R3DocumentToSubledger, "decision:"+d, reconciliation.StatusMatched, "0.00")
	}
	matched := expect(reconciliation.R3DocumentToSubledger, "refund:"+rf.String(), reconciliation.StatusMatched, "0.00")
	if len(v.Items) != 7 {
		t.Fatalf("%d items, want three R1, three R3 decisions and one R3 refund", len(v.Items))
	}

	// R7: the first break in stage order, and every stage not compared
	// named (ZTAX-FIN-REQ-0077).
	if v.Run.FirstBreak == nil || *v.Run.FirstBreak != reconciliation.R1CalculatedToDocument {
		t.Fatalf("first break %v", v.Run.FirstBreak)
	}
	want := []reconciliation.Stage{reconciliation.R2DocumentToCollection, reconciliation.R4SubledgerToReturn,
		reconciliation.R5ReturnToRemittance, reconciliation.R6SubledgerToGL}
	if len(v.Run.Unavailable) != len(want) {
		t.Fatalf("unavailable %v", v.Run.Unavailable)
	}
	for i := range want {
		if v.Run.Unavailable[i] != want[i] {
			t.Fatalf("unavailable %v", v.Run.Unavailable)
		}
	}

	// ZTAX-FIN-REQ-0084: resolved only with reason, action, cause and
	// evidence; once; and only an exception.
	if _, err := svc.Resolve(c.ctx, v.Run.ID, missing.ID, app.ResolutionInput{Reason: "billed in October", Action: reconciliation.ActionWait,
		Cause: reconciliation.CauseTiming}); errs.ReasonOf(err) != errs.ReasonMissingField {
		t.Fatalf("a resolution without evidence: %v", err)
	}
	resolved, err := svc.Resolve(c.ctx, v.Run.ID, missing.ID, app.ResolutionInput{Reason: "billed in October", Action: reconciliation.ActionWait,
		Cause: reconciliation.CauseTiming, Evidence: []string{"bss:invoice-run-2026-10-01"}})
	if err != nil || resolved.Status != reconciliation.StatusResolved || resolved.Resolution.Resolver.IsZero() {
		t.Fatalf("a resolution: %v %+v", err, resolved)
	}
	if _, err := svc.Resolve(c.ctx, v.Run.ID, missing.ID, app.ResolutionInput{Reason: "again", Action: reconciliation.ActionWait,
		Cause: reconciliation.CauseTiming, Evidence: []string{"x"}}); errs.ReasonOf(err) != errs.ReasonOptimisticConflict {
		t.Fatalf("a second resolution: %v", err)
	}
	if _, err := svc.Resolve(c.ctx, v.Run.ID, matched.ID, app.ResolutionInput{Reason: "x", Action: reconciliation.ActionWait,
		Cause: reconciliation.CauseTiming, Evidence: []string{"x"}}); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("resolving a matched item: %v", err)
	}

	read, err := svc.RunByID(c.ctx, v.Run.ID)
	if err != nil || len(read.Items) != 7 {
		t.Fatalf("read back: %v", err)
	}
	found := false
	for _, it := range read.Items {
		if it.ID == missing.ID {
			found = it.Resolution != nil && it.Resolution.Evidence[0] == "bss:invoice-run-2026-10-01"
		}
	}
	if !found {
		t.Fatal("the resolution was not read back with its item")
	}
}
