package ctc_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ctc"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

func u(n int) uuid.UUID { return uuid.MustParse(fmt.Sprintf("01920000-0000-7000-8000-%012d", n)) }

var (
	tenant  = id.NewTenantID(u(1))
	entity  = id.NewLegalEntityID(u(2))
	other   = id.NewLegalEntityID(u(3))
	maker   = id.NewUserID(u(10))
	checker = id.NewUserID(u(11))
	third   = id.NewUserID(u(12))
	day     = time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
)

func money(t *testing.T, s, cur string) fiscal.Money {
	t.Helper()
	m, err := fiscal.ParseMoney(s, fiscal.Currency(cur))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func itTerms() ctc.Terms {
	return ctc.Terms{Duty: ctc.DutyRequired, Model: ctc.ModelClearance, Profile: ctc.ProfileITFatturaPA,
		FormatVersion: "FPR12 1.9", Authority: "Agenzia delle Entrate — SdI", LegalSource: "D.Lgs. 127/2015",
		SourceAsOf: day, EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func cell() ctc.Cell {
	return ctc.Cell{LegalEntity: entity, Country: "IT", DocumentType: document.TypeInvoice, BuyerKind: ctc.BuyerB2B}
}

func mustCode(t *testing.T, err error, code errs.ReasonCode) {
	t.Helper()
	if errs.ReasonOf(err) != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func newRecord(t *testing.T, terms ctc.Terms) ctc.Applicability {
	t.Helper()
	a, err := ctc.New(ctc.NewInput{ID: id.NewCTCApplicabilityID(u(100)), TenantID: tenant, Cell: cell(), Terms: terms,
		CreatedBy: maker, CreatedAt: day})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func apply(t *testing.T, a ctc.Applicability, e ctc.Event, err error) ctc.Applicability {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	a.History = append(a.History, e)
	got, err := ctc.Fold(a, a.Versions, a.History)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

var evidence = ctc.Attachment{SHA256: strings.Repeat("ab", 32), Filename: "opinion.pdf", MediaType: "application/pdf", Bytes: 100}

// active walks a record through evidence, review and every gate to ACTIVE.
func active(t *testing.T, terms ctc.Terms) ctc.Applicability {
	t.Helper()
	a := newRecord(t, terms)
	e, ok, err := a.AddEvidence(evidence, maker, day)
	if !ok {
		t.Fatal("evidence not added")
	}
	a = apply(t, a, e, err)
	e, err = a.Submit(maker, day)
	a = apply(t, a, e, err)
	for i, g := range ctc.RequiredGates(terms.Duty) {
		e, ok, err := a.SignGate(g, 1, fmt.Sprintf("CAB-%d", i), "", checker, day)
		if !ok {
			t.Fatalf("gate %s not signed", g)
		}
		a = apply(t, a, e, err)
	}
	e, err = a.Activate(1, third, day)
	return apply(t, a, e, err)
}

// Doc 21 A41, A58, A59: a record activates only on every required gate,
// signed on its current version by someone other than its authors, with
// evidence, by an activator who is neither.
func TestActivationNeedsEveryGateEvidenceAndAnotherPerson(t *testing.T) {
	a := newRecord(t, itTerms())
	e, err := a.Submit(maker, day)
	a = apply(t, a, e, err)

	_, _, err = a.SignGate(ctc.GateScope, 1, "CAB-1", "", maker, day)
	mustCode(t, err, ctc.ReasonMakerChecker)
	_, _, err = a.SignGate(ctc.GateScope, 2, "CAB-1", "", checker, day)
	mustCode(t, err, ctc.ReasonVersionConflict)

	e, _, err = a.SignGate(ctc.GateScope, 1, "CAB-1", "", checker, day)
	a = apply(t, a, e, err)
	if _, ok, err := a.SignGate(ctc.GateScope, 1, "CAB-1", "", checker, day); ok || err != nil {
		t.Fatalf("a second sign-off of the same gate is a no-op: %v %v", ok, err)
	}
	_, err = a.Activate(1, third, day)
	mustCode(t, err, ctc.ReasonGateMissing)
	if !strings.Contains(err.Error(), "G1_LEGAL_CONTENT_RIGHTS") {
		t.Fatalf("the missing gates are named: %v", err)
	}
	for _, g := range ctc.Gates[1:] {
		e, _, err := a.SignGate(g, 1, "CAB-x", "", checker, day)
		a = apply(t, a, e, err)
	}
	_, err = a.Activate(1, third, day)
	mustCode(t, err, ctc.ReasonEvidenceMissing)
	e, _, err = a.AddEvidence(evidence, maker, day)
	a = apply(t, a, e, err)
	_, err = a.Activate(1, maker, day)
	mustCode(t, err, ctc.ReasonMakerChecker)
	e, err = a.Activate(1, third, day)
	a = apply(t, a, e, err)
	if a.Status != ctc.StatusActive {
		t.Fatalf("status %s", a.Status)
	}
}

// A revision returns a record in review to draft, and sign-offs on the
// earlier version do not carry over.
func TestRevisionVoidsEarlierSignoffs(t *testing.T) {
	a := newRecord(t, itTerms())
	e, err := a.Submit(maker, day)
	a = apply(t, a, e, err)
	e, _, err = a.SignGate(ctc.GateScope, 1, "CAB-1", "", checker, day)
	a = apply(t, a, e, err)

	if _, _, changed, err := a.Revise(itTerms(), maker, day); changed || err != nil {
		t.Fatalf("identical terms are not a new version: %v %v", changed, err)
	}
	next := itTerms()
	next.FormatVersion = "FPR12 1.9.1"
	v, e, changed, err := a.Revise(next, maker, day)
	if !changed || err != nil {
		t.Fatalf("revise: %v %v", changed, err)
	}
	a.Versions = append(a.Versions, v)
	a = apply(t, a, e, nil)
	if a.Status != ctc.StatusDraft || a.Current().Number != 2 || len(a.MissingGates()) != 5 {
		t.Fatalf("status %s version %d missing %v", a.Status, a.Current().Number, a.MissingGates())
	}
}

// A PROHIBITED record needs only G0 and G1, and has no model or profile.
func TestProhibitedRecordsDispatchNothing(t *testing.T) {
	terms := itTerms()
	terms.Duty = ctc.DutyProhibited
	if _, err := ctc.New(ctc.NewInput{ID: id.NewCTCApplicabilityID(u(100)), TenantID: tenant, Cell: cell(), Terms: terms,
		CreatedBy: maker, CreatedAt: day}); err == nil {
		t.Fatal("a prohibited duty with a profile is refused")
	}
	terms.Model, terms.Profile, terms.FormatVersion = ctc.ModelNone, ctc.ProfileNone, ""
	a := active(t, terms)
	if len(ctc.RequiredGates(terms.Duty)) != 2 || a.Status != ctc.StatusActive {
		t.Fatalf("gates %v status %s", ctc.RequiredGates(terms.Duty), a.Status)
	}
	if _, err := ctc.Validate(ctc.ValidateInput{ID: id.NewCTCValidationID(u(200)), TenantID: tenant, Document: input(t, invoice(t)),
		Applicability: a, At: day, By: maker}); errs.ReasonOf(err) != ctc.ReasonSubmissionProhibited {
		t.Fatalf("nothing is validated for dispatch under a prohibited record: %v", err)
	}
}

// A national profile is used only in its own country.
func TestNationalProfileStaysInItsCountry(t *testing.T) {
	c := cell()
	c.Country = "FR"
	_, err := ctc.New(ctc.NewInput{ID: id.NewCTCApplicabilityID(u(100)), TenantID: tenant, Cell: c, Terms: itTerms(),
		CreatedBy: maker, CreatedAt: day})
	mustCode(t, err, ctc.ReasonProfileCountryMismatch)
}

// Doc 21 W01, A02, A03, A30: resolution is per cell and never assumes.
func TestResolutionIsReviewBlockedWithoutOneActiveRecord(t *testing.T) {
	if r := ctc.Resolve(nil, cell(), day); r.Duty != ctc.DutyReviewBlocked || r.Reason != ctc.ReasonNoApplicability {
		t.Fatalf("no record: %+v", r)
	}
	draft := newRecord(t, itTerms())
	if r := ctc.Resolve([]ctc.Applicability{draft}, cell(), day); r.Reason != ctc.ReasonApplicabilityNotActive {
		t.Fatalf("draft: %+v", r)
	}
	a := active(t, itTerms())
	if r := ctc.Resolve([]ctc.Applicability{a}, cell(), day); r.Duty != ctc.DutyRequired || r.Record == nil {
		t.Fatalf("active: %+v", r)
	}
	b2c := cell()
	b2c.BuyerKind = ctc.BuyerB2C
	if r := ctc.Resolve([]ctc.Applicability{a}, b2c, day); r.Duty != ctc.DutyReviewBlocked {
		t.Fatalf("a B2B record decides nothing for B2C: %+v", r)
	}
	if r := ctc.Resolve([]ctc.Applicability{a}, cell(), time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)); r.Duty != ctc.DutyReviewBlocked {
		t.Fatalf("before the effective date: %+v", r)
	}
	twin := a
	twin.ID = id.NewCTCApplicabilityID(u(101))
	if r := ctc.Resolve([]ctc.Applicability{a, twin}, cell(), day); r.Reason != ctc.ReasonApplicabilityConflict {
		t.Fatalf("two active: %+v", r)
	}
	if !a.Overlaps(twin) {
		t.Fatal("same cell, same dates overlap")
	}
	e, err := a.Suspend("Authority outage", checker, day)
	s := apply(t, a, e, err)
	if r := ctc.Resolve([]ctc.Applicability{s}, cell(), day); r.Reason != ctc.ReasonApplicabilityNotActive {
		t.Fatalf("suspended: %+v", r)
	}
	if _, err := s.Resume("Back", checker, day); errs.ReasonOf(err) != ctc.ReasonMakerChecker {
		t.Fatalf("the suspender does not resume: %v", err)
	}
}

func invoice(t *testing.T) document.Document {
	t.Helper()
	dec := id.NewDecisionID(u(300))
	tax := money(t, "22.00", "EUR")
	return document.Document{
		ID: id.NewFiscalDocumentID(u(400)), TenantID: tenant, LegalEntity: entity, Type: document.TypeInvoice,
		Number: "FT-2026-000184", Root: id.NewFiscalDocumentID(u(400)), IssueDate: day, TaxPoint: day, Currency: "EUR",
		Decisions: []id.DecisionID{dec},
		Lines: []document.Line{{ID: id.NewFiscalLineID(u(401)), SourceLineRef: "BSS-1", Net: money(t, "100.00", "EUR"),
			Taxes: []document.TaxLine{{Decision: dec, Component: "VAT", Amount: tax}}}},
		Status: document.StatusCommitted,
	}
}

func input(t *testing.T, d document.Document) ctc.DocumentInput {
	t.Helper()
	return ctc.DocumentInput{Document: d, Status: document.StatusCommitted, Net: money(t, "100.00", string(d.Currency)),
		Tax: money(t, "22.00", string(d.Currency)), Gross: money(t, "122.00", string(d.Currency))}
}

func result(v ctc.Validation, rule string) ctc.RuleResult {
	for _, r := range v.Results {
		if r.RuleID == rule {
			return r
		}
	}
	return ctc.RuleResult{}
}

// Doc 21 W06, A15, A16, A17: deterministic results; an exact-decimal diff;
// the schema stage is NOT_EVALUATED, never a pass.
func TestValidationIsDeterministicAndHonest(t *testing.T) {
	a := active(t, itTerms())
	in := input(t, invoice(t))
	run := func(in ctc.DocumentInput) ctc.Validation {
		v, err := ctc.Validate(ctc.ValidateInput{ID: id.NewCTCValidationID(u(200)), TenantID: tenant, Document: in,
			Applicability: a, At: day, By: maker})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	v1, v2 := run(in), run(in)
	if v1.Outcome != ctc.OutcomePassed || !v1.Digest.Equal(v2.Digest) || v1.Verify() != nil {
		t.Fatalf("outcome %s, digests %s %s", v1.Outcome, v1.Digest, v2.Digest)
	}
	if x := result(v1, "ZT-CTC-X01"); x.Outcome != ctc.RuleNotEvaluated || x.Reason != ctc.ReasonRuleSchemaNotEvaluated {
		t.Fatalf("schema stage %+v", x)
	}

	off := in
	off.Tax = money(t, "22.01", "EUR")
	off.Gross = money(t, "122.01", "EUR")
	v := run(off)
	tot := result(v, "ZT-CTC-S06")
	if v.Outcome != ctc.OutcomeFailed || tot.Outcome != ctc.RuleFail || !strings.Contains(tot.Detail, "-0.01") {
		t.Fatalf("a one-cent mismatch fails with its exact diff: %s %+v", v.Outcome, tot)
	}

	long := invoice(t)
	long.Number = "FT-2026-000184-ABCDEFGHIJ"
	if r := result(run(input(t, long)), "ZT-CTC-IT01"); r.Outcome != ctc.RuleFail || r.Reason != ctc.ReasonRuleNumberFormat {
		t.Fatalf("FatturaPA number length %+v", r)
	}
	foreign := invoice(t)
	foreign.LegalEntity = other
	if r := result(run(input(t, foreign)), "ZT-CTC-A01"); r.Outcome != ctc.RuleFail {
		t.Fatalf("another entity's document %+v", r)
	}
}

func prepared(t *testing.T) (ctc.Submission, ctc.Applicability, ctc.DocumentInput) {
	t.Helper()
	a := active(t, itTerms())
	in := input(t, invoice(t))
	v, err := ctc.Validate(ctc.ValidateInput{ID: id.NewCTCValidationID(u(200)), TenantID: tenant, Document: in,
		Applicability: a, At: day, By: maker})
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := ctc.Prepare(ctc.PrepareInput{ID: id.NewCTCSubmissionID(u(500)), TenantID: tenant, Document: in,
		Applicability: a, Validation: &v, At: day, By: maker})
	if err != nil {
		t.Fatal(err)
	}
	return s, a, in
}

func step(t *testing.T, s ctc.Submission, e ctc.SubmissionEvent, err error) ctc.Submission {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ctc.FoldSubmission(s, append(s.History, e))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Doc 21 W14, W15, A40–A45.
func TestSubmissionLifecycle(t *testing.T) {
	s, a, _ := prepared(t)
	attempt := id.NewCTCAttemptID(u(600))
	dispatch := func(s ctc.Submission, by id.UserID, at id.CTCAttemptID) (ctc.SubmissionEvent, bool, error) {
		return s.Dispatch(ctc.DispatchInput{ExpectedDigest: s.PayloadDigest.String(), CurrentDigest: s.PayloadDigest,
			Applicability: a, Attempt: at, By: by, At: day})
	}
	if _, _, err := dispatch(s, maker, attempt); errs.ReasonOf(err) != ctc.ReasonMakerChecker {
		t.Fatalf("the preparer does not dispatch: %v", err)
	}
	if _, _, err := s.Dispatch(ctc.DispatchInput{ExpectedDigest: "zt1:00", CurrentDigest: s.PayloadDigest, Applicability: a,
		Attempt: attempt, By: checker, At: day}); errs.ReasonOf(err) != ctc.ReasonPayloadDigestMismatch {
		t.Fatalf("digest named: %v", err)
	}
	e, _, err := dispatch(s, checker, attempt)
	s = step(t, s, e, err)
	if _, ok, err := dispatch(s, checker, id.NewCTCAttemptID(u(601))); ok || err != nil {
		t.Fatalf("a retry is no second attempt: %v %v", ok, err)
	}

	e, _, err = s.Respond(ctc.Response{Kind: ctc.SubEventTransportReceived, Attempt: attempt, TransportRef: "RCPT-1"}, maker, day)
	s = step(t, s, e, err)
	if s.Status != ctc.SubTransportReceived {
		t.Fatalf("a transport receipt is not acceptance: %s", s.Status)
	}
	e, _, err = s.Respond(ctc.Response{Kind: ctc.SubEventOutcomeUnknown, Attempt: attempt, Detail: "Timeout"}, maker, day)
	s = step(t, s, e, err)
	if _, _, err := dispatch(s, checker, id.NewCTCAttemptID(u(602))); errs.ReasonOf(err) != ctc.ReasonOutcomeUncertain {
		t.Fatalf("no resend while uncertain: %v", err)
	}
	if _, _, err := s.Respond(ctc.Response{Kind: ctc.SubEventAccepted, Attempt: attempt, AuthorityRef: "IdSdI 1"}, maker, day); err == nil {
		t.Fatal("an acceptance carries the receipt digest")
	}
	accept := ctc.Response{Kind: ctc.SubEventAccepted, Attempt: attempt, AuthorityRef: "IdSdI 1", EvidenceSHA256: strings.Repeat("cd", 32)}
	e, _, err = s.Respond(accept, maker, day)
	s = step(t, s, e, err)
	if _, ok, err := s.Respond(accept, maker, day); ok || err != nil {
		t.Fatalf("a replayed acceptance is one event: %v %v", ok, err)
	}
	_, _, err = s.Respond(ctc.Response{Kind: ctc.SubEventRejected, Attempt: attempt, AuthorityCode: "00404",
		EvidenceSHA256: strings.Repeat("ef", 32), Detail: "late"}, maker, day)
	mustCode(t, err, ctc.ReasonFinalOutcome)
}

// Doc 21 A46, A60: a rejection opens a correction closed by a new document
// naming the rejected one; a suspended record dispatches nothing new.
func TestRejectionOpensCorrectionAndSuspensionStopsDispatch(t *testing.T) {
	s, a, in := prepared(t)
	susp, err := a.Suspend("Emergency", checker, day)
	sa := apply(t, a, susp, err)
	if _, _, err := s.Dispatch(ctc.DispatchInput{ExpectedDigest: s.PayloadDigest.String(), CurrentDigest: s.PayloadDigest,
		Applicability: sa, Attempt: id.NewCTCAttemptID(u(600)), By: checker, At: day}); errs.ReasonOf(err) != ctc.ReasonApplicabilityNotActive {
		t.Fatalf("suspended: %v", err)
	}
	attempt := id.NewCTCAttemptID(u(600))
	e, _, err := s.Dispatch(ctc.DispatchInput{ExpectedDigest: s.PayloadDigest.String(), CurrentDigest: s.PayloadDigest,
		Applicability: a, Attempt: attempt, By: checker, At: day})
	s = step(t, s, e, err)
	e, _, err = s.Respond(ctc.Response{Kind: ctc.SubEventRejected, Attempt: attempt, AuthorityCode: "00404",
		EvidenceSHA256: strings.Repeat("ef", 32), Detail: "Duplicate"}, maker, day)
	s = step(t, s, e, err)
	task, ok := s.Correction([]document.Document{in.Document})
	if !ok || !task.Open() || task.AuthorityCode != "00404" {
		t.Fatalf("task %+v %v", task, ok)
	}
	credit := in.Document
	credit.ID, credit.Type, credit.Predecessors = id.NewFiscalDocumentID(u(402)), document.TypeCreditNote, []id.FiscalDocumentID{in.Document.ID}
	if task, _ := s.Correction([]document.Document{in.Document, credit}); task.Open() {
		t.Fatal("a credit naming the rejected document closes the task")
	}
}

// A validation made before the document's state changed is stale.
func TestStaleValidationIsNotReused(t *testing.T) {
	a := active(t, itTerms())
	in := input(t, invoice(t))
	v, err := ctc.Validate(ctc.ValidateInput{ID: id.NewCTCValidationID(u(200)), TenantID: tenant, Document: in,
		Applicability: a, At: day, By: maker})
	if err != nil {
		t.Fatal(err)
	}
	moved := in
	moved.Status = document.StatusVoided
	_, _, err = ctc.Prepare(ctc.PrepareInput{ID: id.NewCTCSubmissionID(u(500)), TenantID: tenant, Document: moved,
		Applicability: a, Validation: &v, At: day, By: maker})
	mustCode(t, err, ctc.ReasonValidationStale)
	_, _, err = ctc.Prepare(ctc.PrepareInput{ID: id.NewCTCSubmissionID(u(500)), TenantID: tenant, Document: in,
		Applicability: a, At: day, By: maker})
	mustCode(t, err, ctc.ReasonValidationRequired)
}
