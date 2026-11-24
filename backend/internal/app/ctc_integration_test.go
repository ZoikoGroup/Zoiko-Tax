//go:build integration

package app_test

import (
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ctc"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// The e-invoicing / CTC layer against PostgreSQL (Doc 21, migration 000036):
// a committed invoice validated and submitted under a record activated on
// G0–G4 by other people, rejected by the authority, and corrected by a credit
// note through the fiscal corrections flow — every record read back from the
// store.
func TestIntegrationDoc21ApplicabilityValidationAndSubmission(t *testing.T) {
	c := openFiscalCell(t)
	docs := c.documents()
	d := c.mustCommit(t, "k-ctc-1", "INV-CTC-1/1", "100.00", "3", nil)
	inv := mustDoc(t)(c.issue(t, docs, "ctc-1", c.docLine(t, d, "100.00", "21.00", "0.11")))

	svc := app.NewCTCService(c.store.CTC(), c.store.Documents(), c.store.LegalEntities(), c.store, c.clock, idgen.V7{})
	operator, operator2 := c.as(security.RoleOperator), c.as(security.RoleOperator)
	analyst, analyst2 := c.as(security.RoleAnalyst), c.as(security.RoleAnalyst)

	terms := ctc.Terms{Duty: ctc.DutyRequired, Model: ctc.ModelClearance, Profile: ctc.ProfilePeppolBIS3, FormatVersion: "3.0.18",
		Authority: "Peppol network", LegalSource: "Doc 21 R02", SourceAsOf: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC),
		EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	a, err := svc.CreateApplicability(operator, app.CTCApplicabilityInput{Country: "DE", DocumentType: document.TypeInvoice,
		BuyerKind: ctc.BuyerB2B, Terms: terms})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.AddApplicabilityEvidence(operator, a.ID, ctc.Attachment{SHA256: strings.Repeat("ab", 32),
		Filename: "opinion.pdf", MediaType: "application/pdf", Bytes: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TransitionApplicability(operator, a.ID, app.CTCTransitionInput{Action: app.CTCSubmit}); err != nil {
		t.Fatal(err)
	}
	for _, g := range ctc.Gates {
		if _, _, err := svc.SignApplicabilityGate(analyst, a.ID, app.CTCGateInput{Gate: g, Version: 1, SignoffRef: "CAB-" + string(g)}); err != nil {
			t.Fatal(err)
		}
	}
	if a, err = svc.TransitionApplicability(analyst2, a.ID, app.CTCTransitionInput{Action: app.CTCActivate, Version: 1}); err != nil || a.Status != ctc.StatusActive {
		t.Fatalf("activate: %v", err)
	}
	if got, err := svc.Applicability(analyst, a.ID); err != nil || len(got.MissingGates()) != 0 || len(got.Evidence()) != 1 ||
		!got.Current().Digest.Equal(a.Current().Digest) {
		t.Fatalf("read back: %v", err)
	}

	v, created, err := svc.Validate(operator, inv, a.ID)
	if err != nil || !created || v.Outcome != ctc.OutcomePassed {
		t.Fatalf("validate: %v %v %+v", created, err, v.Results)
	}
	if again, created, err := svc.Validate(operator, inv, a.ID); err != nil || created || again.ID != v.ID || again.Verify() != nil {
		t.Fatalf("validate again: %v %v", created, err)
	}
	sv, created, err := svc.PrepareSubmission(operator, inv, a.ID)
	if err != nil || !created || sv.Submission.Gross.CanonicalString() != "121.11" {
		t.Fatalf("prepare: %v %v", created, err)
	}
	if again, created, err := svc.PrepareSubmission(operator2, inv, a.ID); err != nil || created || again.Submission.ID != sv.Submission.ID {
		t.Fatalf("prepare again: %v %v", created, err)
	}
	sid, digest := sv.Submission.ID, sv.Submission.PayloadDigest.String()
	if _, _, err := svc.Dispatch(operator, sid, digest); errs.ReasonOf(err) != ctc.ReasonMakerChecker {
		t.Fatalf("the preparer dispatched: %v", err)
	}
	if sv, _, err = svc.Dispatch(analyst, sid, digest); err != nil || sv.Submission.Status != ctc.SubSent {
		t.Fatalf("dispatch: %v", err)
	}
	attempt := sv.Submission.Attempts()[0].Attempt
	if sv, _, err = svc.Respond(operator, sid, ctc.Response{Kind: ctc.SubEventTransportReceived, Attempt: attempt, TransportRef: "AS4-1"}); err != nil ||
		sv.Submission.Status != ctc.SubTransportReceived {
		t.Fatalf("receipt: %v", err)
	}
	if sv, _, err = svc.Respond(operator, sid, ctc.Response{Kind: ctc.SubEventRejected, Attempt: attempt, AuthorityCode: "BR-CO-15",
		EvidenceSHA256: strings.Repeat("cd", 32), Detail: "Rejected"}); err != nil || sv.Correction == nil || !sv.Correction.Open() {
		t.Fatalf("rejection: %v", err)
	}
	if _, _, err := svc.Respond(operator, sid, ctc.Response{Kind: ctc.SubEventAccepted, Attempt: attempt, AuthorityRef: "X",
		EvidenceSHA256: strings.Repeat("ef", 32)}); errs.ReasonOf(err) != ctc.ReasonFinalOutcome {
		t.Fatalf("a later acceptance overwrote the rejection: %v", err)
	}

	mustDoc(t)(c.correct(t, docs, "ctc-credit", inv, document.TypeCreditNote, nil))
	list, err := svc.Submissions(analyst, ctc.SubmissionFilter{Status: ctc.SubRejected})
	if err != nil || len(list) != 1 || list[0].Correction == nil || list[0].Correction.Open() {
		t.Fatalf("the credit note closes the correction task: %+v %v", list, err)
	}
}
