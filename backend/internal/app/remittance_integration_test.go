//go:build integration

package app_test

import (
	"context"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/compliance"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/remittance"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// Remittance against PostgreSQL: a return the authority accepted, paid to a
// verified payee, settled on evidence.

// acceptedObligation files the period's return through a compliance package
// and records the authority's acceptance.
func (c *fiscalCell) acceptedObligation(t *testing.T) app.ObligationView {
	t.Helper()
	svc, legalSvc := c.compliance(t)
	if _, err := legalSvc.Grant(c.as(security.RoleAdmin), mandateInput()); err != nil {
		t.Fatal(err)
	}
	v := c.newPackage(t, svc, c.readyObligation(t))
	v, err := svc.Prepare(c.ctx, v.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	reviewer := c.as(security.RoleAnalyst)
	if v, err = svc.Review(reviewer, v.ID, 1, compliance.ReviewAccept, ""); err != nil {
		t.Fatal(err)
	}
	if v, err = svc.Approve(reviewer, v.ID, 1, v.Preparation().PayloadHash, ""); err != nil {
		t.Fatal(err)
	}
	if v, err = svc.Submit(c.ctx, v.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Acknowledge(c.ctx, v.ID, app.AcknowledgeInput{Acknowledgement: compliance.AckAccepted,
		AuthorityRef: "ELS-1", Evidence: "accepted"}); err != nil {
		t.Fatal(err)
	}
	return c.onlyObligation(t)
}

func (c *fiscalCell) verifiedPayee(t *testing.T, svc *app.RemittanceService, maker, checker context.Context) remittance.Beneficiary {
	t.Helper()
	b, err := svc.CreateBeneficiary(maker, app.BeneficiaryInput{
		Country: "DE", Authority: "DE-ELSTER", LegalName: "Finanzamt Musterstadt", Rail: remittance.RailSEPA,
		AccountRef: "vault://payees/de/finanzamt", Currencies: []string{"eur"}, Source: "official instructions 2026",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyBeneficiary(maker, b.ID, "I entered them"); errs.ReasonOf(err) != remittance.ReasonMakerChecker {
		t.Fatalf("the author verified their own payee details: %v", err)
	}
	if b, err = svc.VerifyBeneficiary(checker, b.ID, "called the authority"); err != nil || b.State != remittance.BeneficiaryVerified {
		t.Fatalf("verify: %v %s", err, b.State)
	}
	return b
}

// ZTAX-REM-REQ-0005, -0007, -0015, -0024, -0026, -0031, -0037, -0043.
func TestIntegrationREMREQ0005AnAcceptedReturnIsPaidOnSettlementEvidence(t *testing.T) {
	c := openFiscalCell(t)
	svc := app.NewRemittanceService(c.store.Remittances(), c.store.Obligations(), c.svc, c.store, c.clock, idgen.V7{})
	o := c.acceptedObligation(t)
	if o.Status != obligation.StatusAccepted {
		t.Fatalf("obligation is %s", o.Status)
	}
	admin, analyst := c.as(security.RoleAdmin), c.as(security.RoleAnalyst)

	// An unverified payee is not paid (ZTAX-REM-REQ-0015).
	unverified, err := svc.CreateBeneficiary(admin, app.BeneficiaryInput{
		Country: "DE", Authority: "DE-ELSTER-2", LegalName: "Unverified", Rail: remittance.RailSEPA,
		AccountRef: "vault://payees/de/unverified", Currencies: []string{"EUR"}, Source: "email",
	})
	if err != nil {
		t.Fatal(err)
	}
	payee := c.verifiedPayee(t, svc, admin, analyst)

	in := app.RemittanceInput{Obligation: o.ID, Beneficiary: unverified.ID, FundingRef: "vault://funding/acme/eur",
		PaymentReference: "123/456/78901", ExecutionDate: c.clock.t, Mode: remittance.ModeCustomerExecutes}
	v, created, err := svc.CreateRemittance(c.ctx, in)
	if err != nil || !created || v.Amount.CanonicalString() != "21.00" || v.ObligationStatus != obligation.StatusPaymentDue {
		t.Fatalf("create: %v created=%v %+v obligation %s", err, created, v.Remittance, v.ObligationStatus)
	}
	if _, err := svc.ApproveRemittance(analyst, v.ID, v.InstructionHash, ""); errs.ReasonOf(err) != remittance.ReasonPaymentBlocked {
		t.Fatalf("approved a payment to an unverified payee: %v", err)
	}
	// Idempotent while live: asking again returns the same instruction
	// (ZTAX-REM-REQ-0007).
	in.Beneficiary = payee.ID
	again, created, err := svc.CreateRemittance(c.ctx, in)
	if err != nil || created || again.ID != v.ID {
		t.Fatalf("asking again: %v created=%v", err, created)
	}
	if _, err := svc.CancelRemittance(c.ctx, v.ID, "wrong payee"); err != nil {
		t.Fatal(err)
	}
	v, created, err = svc.CreateRemittance(c.ctx, in)
	if err != nil || !created || v.Supersedes != again.ID {
		t.Fatalf("replacement: %v created=%v supersedes %v", err, created, v.Supersedes)
	}

	// Maker-checker (ZTAX-REM-REQ-0024) and the payee's author
	// (ZTAX-REM-REQ-0031) cannot approve.
	if _, err := svc.ApproveRemittance(c.ctx, v.ID, v.InstructionHash, ""); errs.ReasonOf(err) != remittance.ReasonMakerChecker {
		t.Fatalf("the maker approved: %v", err)
	}
	independent := c.as(security.RoleAnalyst)
	if _, err := svc.ApproveRemittance(independent, v.ID, v.InstructionHash, ""); err != nil {
		t.Fatalf("an independent approver was refused: %v", err)
	}
	if v, err = svc.SubmitRemittance(c.ctx, v.ID, ""); err != nil || v.Status != remittance.StatusSubmitted {
		t.Fatalf("submit: %v %s", err, v.Status)
	}
	if v.ObligationStatus != obligation.StatusPaymentDue {
		t.Fatalf("submission moved the obligation to %s", v.ObligationStatus)
	}
	if _, err := svc.RecordStatus(c.ctx, v.ID, app.StatusInput{Status: remittance.StatusSettled,
		Evidence: remittance.EvidenceAccountDebit, ProviderRef: "D-1", Raw: "debited"}); errs.ReasonOf(err) != remittance.ReasonPaymentBlocked {
		t.Fatalf("a debit settled the payment: %v", err)
	}
	v, err = svc.RecordStatus(c.ctx, v.ID, app.StatusInput{Status: remittance.StatusSettled,
		Evidence: remittance.EvidenceBankStatement, ProviderRef: "STMT-1", Raw: "statement line"})
	if err != nil || v.Status != remittance.StatusSettled || v.ObligationStatus != obligation.StatusPaid {
		t.Fatalf("settle: %v payment %s obligation %s", err, v.Status, v.ObligationStatus)
	}
}
