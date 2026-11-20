//go:build integration

package app_test

import (
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/erp"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// The ERP journal bridge against PostgreSQL (LED-23, R6): a commit's posted
// journals sealed into a batch, dispatched once through the outbox, posted
// by the ERP as a person recorded it, and reconciled to the GL balances the
// ERP reported — every figure read back from the store.
func TestIntegrationLED23AnExportIsSealedDispatchedPostedAndReconciled(t *testing.T) {
	c := openFiscalCell(t)
	c.mustCommit(t, "k-erp-1", "INV-ERP-1/1", "100.00", "3", nil)
	c.mustCommit(t, "k-erp-2", "INV-ERP-2/1", "250.00", "2", nil)
	svc := app.NewERPService(c.store.ERP(), c.store.LegalEntities(), c.store.Outbox(), c.store, c.clock, idgen.V7{})
	preparer, checker := c.as(security.RoleOperator), c.as(security.RoleAnalyst)

	b, err := svc.CreateBridge(preparer, app.ERPBridgeInput{Name: "Integration SAP", Vendor: erp.VendorSAP, CompanyCode: "IT01"})
	if err != nil {
		t.Fatal(err)
	}
	accounts := make([]erp.AccountMap, 0, 12)
	for _, a := range []subledger.Account{
		subledger.AccountTaxCollectedLiability, subledger.AccountTaxAccruedLiability, subledger.AccountTaxRecoverable,
		subledger.AccountTaxReceivableControl, subledger.AccountTaxCashClearing, subledger.AccountTaxReturnClearing,
		subledger.AccountTaxRemittanceClearing, subledger.AccountTaxAdjustmentControl, subledger.AccountFXControl,
		subledger.AccountRoundingControl, subledger.AccountSuspenseException, subledger.AccountCustomerGLBridge,
	} {
		accounts = append(accounts, erp.AccountMap{Account: a, GLAccount: "GL-" + string(a)})
	}
	if b, _, err = svc.RecordMapping(preparer, b.ID, app.ERPMappingInput{ChartVersion: "COA-IT", Accounts: accounts}); err != nil {
		t.Fatal(err)
	}
	// The mapping reads back in the order it was sealed, so the same mapping
	// again is no new version.
	if again, created, err := svc.RecordMapping(preparer, b.ID, app.ERPMappingInput{ChartVersion: "COA-IT", Accounts: accounts}); err != nil || created || len(again.Mappings) != 1 {
		t.Fatalf("the same mapping again: created %v, %v", created, err)
	}

	v, created, err := svc.Prepare(preparer, b.ID, app.ERPPrepareInput{Period: septemberPeriod, Currency: "EUR", PostingDate: "2026-10-03"})
	if err != nil || !created || len(v.Batch.Journals) == 0 {
		t.Fatalf("prepare: created %v, %v", created, err)
	}
	// What the store holds still verifies against the sealed digest.
	stored, err := svc.Batch(checker, b.ID, v.Batch.ID)
	if err != nil || stored.Batch.Verify() != nil || stored.Batch.Digest.String() != v.Batch.Digest.String() {
		t.Fatalf("read back: %v / %v", err, stored.Batch.Verify())
	}
	if _, _, err := svc.Dispatch(preparer, b.ID, v.Batch.ID, v.Batch.Digest.String()); errs.ReasonOf(err) != erp.ReasonMakerChecker {
		t.Fatalf("the preparer dispatched: %v", err)
	}
	v, dispatched, err := svc.Dispatch(checker, b.ID, v.Batch.ID, v.Batch.Digest.String())
	if err != nil || !dispatched || v.Batch.Status != erp.StatusTransmitted {
		t.Fatalf("dispatch: %v %v", dispatched, err)
	}
	if _, dispatched, err := svc.Dispatch(checker, b.ID, v.Batch.ID, v.Batch.Digest.String()); err != nil || dispatched {
		t.Fatalf("a retried dispatch made an attempt: %v %v", dispatched, err)
	}
	var events int
	if err := ownerPool(t).QueryRow(c.ctx, `SELECT count(*) FROM ztax.outbox WHERE tenant_id = $1 AND event_type = $2`,
		c.tenant.UUID(), app.EventGLExportDispatched).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("%d dispatch events, want 1", events)
	}
	attempt := v.Batch.Attempts()[0].Attempt
	if v, _, err = svc.Respond(checker, b.ID, v.Batch.ID, erp.Response{Kind: erp.EventAcknowledged, Attempt: attempt, RemoteCorrelation: "MSG-1"}); err != nil {
		t.Fatal(err)
	}
	if v, _, err = svc.Respond(checker, b.ID, v.Batch.ID, erp.Response{Kind: erp.EventPosted, Attempt: attempt, ERPReference: "IT01/2026/1"}); err != nil || v.Batch.Status != erp.StatusERPPosted {
		t.Fatalf("posting: %v", err)
	}

	// The ERP reports exactly what the subledger posted.
	journals, err := c.store.ERP().PeriodJournals(c.ctx, b.LegalEntity, septemberPeriod, "EUR")
	if err != nil {
		t.Fatal(err)
	}
	balances, err := subledger.Balances(journals)
	if err != nil {
		t.Fatal(err)
	}
	in := app.GLSnapshotInput{Period: septemberPeriod, Currency: "EUR", AsOf: "2026-10-05T18:00:00Z", SourceRef: "IT report"}
	for k, bal := range balances {
		in.Balances = append(in.Balances, app.GLSnapshotBalance{GLAccount: "GL-" + string(k.Account),
			Debits: bal.Debits.CanonicalString(), Credits: bal.Credits.CanonicalString()})
	}
	snap, err := svc.RecordSnapshot(preparer, b.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	r, err := svc.Reconcile(preparer, b.ID, snap.ID)
	if err != nil || r.Outcome != erp.OutcomeReconciled || r.PostedJournals != len(journals) {
		t.Fatalf("reconcile: %s %v %+v %v", r.Outcome, r.Holds, r.Items, err)
	}
	listed, err := svc.Reconciliations(checker, b.ID, septemberPeriod)
	if err != nil || len(listed) != 1 || listed[0].Outcome != erp.OutcomeReconciled || len(listed[0].Items) != len(r.Items) {
		t.Fatalf("read back: %+v %v", listed, err)
	}
	for i, it := range listed[0].Items {
		if it.Variance == nil || it.Variance.CanonicalString() != r.Items[i].Variance.CanonicalString() {
			t.Fatalf("item %d does not read back exactly", i)
		}
	}
}
