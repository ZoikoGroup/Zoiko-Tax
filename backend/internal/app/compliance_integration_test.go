//go:build integration

package app_test

import (
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/compliance"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// Compliance packages against PostgreSQL: one obligation's filing from
// package to the authority's answer, under the development legal matrix.

func (c *fiscalCell) compliance(t *testing.T) (*app.ComplianceService, *app.LegalService) {
	t.Helper()
	legalSvc := c.legal(t, true)
	return app.NewComplianceService(c.store.Compliance(), c.store.Obligations(), c.svc, legalSvc, c.store, c.clock, idgen.V7{}), legalSvc
}

// readyObligation commits one line and marks the period's return READY.
func (c *fiscalCell) readyObligation(t *testing.T) app.ObligationView {
	t.Helper()
	c.mustCommit(t, "k-c1", "INV-C1/1", "100.00", "3", nil)
	o := c.onlyObligation(t)
	moved, err := c.svc.TransitionObligation(c.ctx, o.ID, obligation.StatusReady)
	if err != nil {
		t.Fatal(err)
	}
	return moved
}

func (c *fiscalCell) newPackage(t *testing.T, svc *app.ComplianceService, o app.ObligationView) app.PackageView {
	t.Helper()
	v, created, err := svc.CreatePackage(c.ctx, app.CreatePackageInput{
		Obligation: o.ID, Mode: compliance.ModeSoftwareOnly, Channel: compliance.ChannelManualPortal,
		FilingCountry: "DE", FilingAuthority: "DE-ELSTER",
	})
	if err != nil || !created {
		t.Fatalf("create: %v (created %v)", err, created)
	}
	return v
}

// ZTAX-COMP-REQ-0001, -0004, -0008, -0041, -0044, -0047, -0055, -0056,
// -0058, -0066.
func TestIntegrationCOMPREQ0001APackageFilesItsObligationOnEvidence(t *testing.T) {
	c := openFiscalCell(t)
	svc, legalSvc := c.compliance(t)
	o := c.readyObligation(t)

	v := c.newPackage(t, svc, o)
	if v.Obligation != o.ID || v.ObligationKey != o.BusinessKey || !v.StatutoryDue.After(o.Period.Due) {
		t.Fatalf("package does not reference its obligation and its legal due date: %+v", v.Package)
	}
	// Idempotent for the obligation (ZTAX-COMP-REQ-0004).
	again, created, err := svc.CreatePackage(c.ctx, app.CreatePackageInput{
		Obligation: o.ID, Mode: compliance.ModeManagedFile, Channel: compliance.ChannelDirectAPI,
		FilingCountry: "DE", FilingAuthority: "DE-ELSTER",
	})
	if err != nil || created || again.ID != v.ID || again.Mode != compliance.ModeSoftwareOnly {
		t.Fatalf("asking again: %v created=%v %+v", err, created, again.Package)
	}

	// The preparer cannot review; a second person can (ZTAX-COMP-REQ-0041).
	v, err = svc.Prepare(c.ctx, v.ID, "Q3 from the subledger")
	if err != nil {
		t.Fatal(err)
	}
	prep := v.Preparation()
	if prep == nil || prep.ControlTotal == nil || prep.ControlTotal.CanonicalString() != "21.00" {
		t.Fatalf("preparation %+v does not carry the assessment as its control total", prep)
	}
	if _, err := svc.Review(c.ctx, v.ID, 1, compliance.ReviewAccept, ""); errs.ReasonOf(err) != compliance.ReasonSegregationOfDuties {
		t.Fatalf("the preparer reviewed their own version: %v", err)
	}
	reviewer := c.as(security.RoleAnalyst)
	if v, err = svc.Review(reviewer, v.ID, 1, compliance.ReviewAccept, ""); err != nil {
		t.Fatal(err)
	}
	if v, err = svc.Approve(reviewer, v.ID, 1, prep.PayloadHash, ""); err != nil || v.Status != compliance.StatusApproved {
		t.Fatalf("approve: %v %s", err, v.Status)
	}

	// No mandate on record: the legal gate blocks the submission
	// (ZTAX-COMP-REQ-0047), and nothing is recorded.
	if _, err := svc.Submit(c.ctx, v.ID, ""); errs.ReasonOf(err) != compliance.ReasonFilingBlocked {
		t.Fatalf("submitted with no filing mandate: %v", err)
	}
	if _, err := legalSvc.Grant(c.as(security.RoleAdmin), mandateInput()); err != nil {
		t.Fatal(err)
	}
	if v, err = svc.Submit(c.ctx, v.ID, ""); err != nil || v.Status != compliance.StatusTransmitting {
		t.Fatalf("submit: %v %s", err, v.Status)
	}
	// TRANSMITTING is not FILED: the obligation has not moved
	// (ZTAX-COMP-REQ-0055, -0058).
	if v.ObligationStatus != obligation.StatusReady {
		t.Fatalf("starting a submission moved the obligation to %s", v.ObligationStatus)
	}

	// Acceptance needs the authority's reference (ZTAX-COMP-REQ-0056), and
	// moves the obligation through FILED to ACCEPTED.
	if _, err := svc.Acknowledge(c.ctx, v.ID, app.AcknowledgeInput{Acknowledgement: compliance.AckAccepted, Evidence: "accepted"}); errs.CategoryOf(err) != errs.CategoryValidation {
		t.Fatalf("an acceptance with no reference: %v", err)
	}
	v, err = svc.Acknowledge(c.ctx, v.ID, app.AcknowledgeInput{Acknowledgement: compliance.AckAccepted,
		AuthorityRef: "ELS-1", Evidence: "Portal receipt: accepted, ELS-1"})
	if err != nil || v.Status != compliance.StatusAccepted || v.ObligationStatus != obligation.StatusAccepted {
		t.Fatalf("acknowledge: %v package %s obligation %s", err, v.Status, v.ObligationStatus)
	}
	last := v.History[len(v.History)-1]
	if last.PayloadHash != prep.PayloadHash || last.Version != 1 {
		t.Fatal("the acceptance is not linked to the submitted version and payload (ZTAX-COMP-REQ-0066)")
	}
	if got := c.onlyObligation(t); got.Status != obligation.StatusAccepted {
		t.Fatalf("obligation is %s", got.Status)
	}
}

// ZTAX-COMP-REQ-0059 to -0061: an uncertain submission is not resubmitted
// until a person resolves it.
func TestIntegrationCOMPREQ0060UncertainSubmissionWaitsForAPerson(t *testing.T) {
	c := openFiscalCell(t)
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
	v, err = svc.Acknowledge(c.ctx, v.ID, app.AcknowledgeInput{Acknowledgement: compliance.AckUnknown, Evidence: "portal timed out after upload"})
	if err != nil || v.Status != compliance.StatusUncertain || v.ObligationStatus != obligation.StatusUncertain {
		t.Fatalf("unknown: %v package %s obligation %s", err, v.Status, v.ObligationStatus)
	}
	if _, err := svc.Submit(c.ctx, v.ID, ""); errs.CategoryOf(err) != errs.CategoryConflict {
		t.Fatalf("an uncertain submission was submitted again: %v", err)
	}
	if v, err = svc.Resolve(c.ctx, v.ID, compliance.ResolutionConfirmedNotFiled, "", "authority status service has no record"); err != nil ||
		v.Status != compliance.StatusApproved {
		t.Fatalf("resolve: %v %s", err, v.Status)
	}
	if v, err = svc.Submit(c.ctx, v.ID, ""); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	for _, s := range v.History {
		if s.Kind == compliance.StepSubmitted {
			attempts++
		}
	}
	if attempts != 2 {
		t.Fatalf("%d attempts on record, want the uncertain one and the next", attempts)
	}
}
