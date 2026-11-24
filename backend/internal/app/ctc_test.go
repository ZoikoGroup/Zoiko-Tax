package app_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ctc"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// memCTC is the CTC store in memory: append-only, refusing an event
// sequence already taken, as migration 000036's keys do.
type memCTC struct {
	mu          sync.Mutex
	records     []ctc.Applicability
	versions    map[id.CTCApplicabilityID][]ctc.Version
	events      map[id.CTCApplicabilityID][]ctc.Event
	validations []ctc.Validation
	subs        []ctc.Submission
	subEvents   map[id.CTCSubmissionID][]ctc.SubmissionEvent
}

func newMemCTC() *memCTC {
	return &memCTC{versions: map[id.CTCApplicabilityID][]ctc.Version{}, events: map[id.CTCApplicabilityID][]ctc.Event{},
		subEvents: map[id.CTCSubmissionID][]ctc.SubmissionEvent{}}
}

func (m *memCTC) Lock(context.Context, string) error { return nil }

func (m *memCTC) CreateApplicability(_ context.Context, a ctc.Applicability) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, a)
	m.versions[a.ID] = slices.Clone(a.Versions)
	m.events[a.ID] = slices.Clone(a.History)
	return nil
}

func (m *memCTC) AppendApplicabilityVersion(_ context.Context, r id.CTCApplicabilityID, v ctc.Version) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.versions[r])+1 != v.Number {
		return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "version")
	}
	m.versions[r] = append(m.versions[r], v)
	return nil
}

func (m *memCTC) AppendApplicabilityEvent(_ context.Context, r id.CTCApplicabilityID, e ctc.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events[r])+1 != e.Seq {
		return errs.New(errs.CategoryConflict, errs.ReasonOptimisticConflict, "seq")
	}
	m.events[r] = append(m.events[r], e)
	return nil
}

func (m *memCTC) folded(match func(ctc.Applicability) bool) []ctc.Applicability {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ctc.Applicability
	for _, a := range m.records {
		f, err := ctc.Fold(a, m.versions[a.ID], m.events[a.ID])
		if err != nil {
			panic(err)
		}
		if match(f) {
			out = append(out, f)
		}
	}
	return out
}

func (m *memCTC) Applicability(_ context.Context, r id.CTCApplicabilityID) (ctc.Applicability, error) {
	got := m.folded(func(a ctc.Applicability) bool { return a.ID == r })
	if len(got) == 0 {
		return ctc.Applicability{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
	}
	return got[0], nil
}

func (m *memCTC) Applicabilities(_ context.Context, f ctc.Filter) ([]ctc.Applicability, error) {
	return m.folded(f.Match), nil
}

func (m *memCTC) RecordValidation(_ context.Context, v ctc.Validation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.validations = append(m.validations, v)
	return nil
}

func (m *memCTC) Validation(_ context.Context, vid id.CTCValidationID) (ctc.Validation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.validations {
		if v.ID == vid {
			return v, nil
		}
	}
	return ctc.Validation{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
}

func (m *memCTC) Validations(_ context.Context, f ctc.ValidationFilter) ([]ctc.Validation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ctc.Validation
	for _, v := range m.validations {
		if (f.Document.IsZero() || v.Document == f.Document) && (f.Applicability.IsZero() || v.Applicability == f.Applicability) {
			out = append(out, v)
		}
	}
	slices.Reverse(out)
	return out, nil
}

func (m *memCTC) CreateSubmission(_ context.Context, s ctc.Submission) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs = append(m.subs, s)
	m.subEvents[s.ID] = slices.Clone(s.History)
	return nil
}

func (m *memCTC) AppendSubmissionEvent(_ context.Context, sid id.CTCSubmissionID, e ctc.SubmissionEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.subEvents[sid])+1 != e.Seq {
		return errs.New(errs.CategoryConflict, errs.ReasonOptimisticConflict, "seq")
	}
	m.subEvents[sid] = append(m.subEvents[sid], e)
	return nil
}

func (m *memCTC) foldedSubs(match func(ctc.Submission) bool) []ctc.Submission {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ctc.Submission
	for _, s := range m.subs {
		f, err := ctc.FoldSubmission(s, m.subEvents[s.ID])
		if err != nil {
			panic(err)
		}
		if match(f) {
			out = append(out, f)
		}
	}
	slices.Reverse(out)
	return out
}

func (m *memCTC) Submission(_ context.Context, sid id.CTCSubmissionID) (ctc.Submission, error) {
	got := m.foldedSubs(func(s ctc.Submission) bool { return s.ID == sid })
	if len(got) == 0 {
		return ctc.Submission{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
	}
	return got[0], nil
}

func (m *memCTC) Submissions(_ context.Context, f ctc.SubmissionFilter) ([]ctc.Submission, error) {
	return m.foldedSubs(func(s ctc.Submission) bool {
		return (f.Document.IsZero() || s.Document == f.Document) && (f.Applicability.IsZero() || s.Applicability == f.Applicability) &&
			(f.Status == "" || s.Status == f.Status)
	}), nil
}

// memDocs is the committed fiscal documents the CTC layer reads.
type memDocs struct {
	recs    []port.DocumentRecord
	history map[id.FiscalDocumentID][]document.StatusEvent
}

func (d *memDocs) ByID(_ context.Context, doc id.FiscalDocumentID) (port.DocumentRecord, []document.StatusEvent, error) {
	for _, r := range d.recs {
		if r.Document.ID == doc {
			return r, d.history[doc], nil
		}
	}
	return port.DocumentRecord{}, nil, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
}

func (d *memDocs) Lineage(_ context.Context, root id.FiscalDocumentID) ([]port.DocumentRecord, error) {
	var out []port.DocumentRecord
	for _, r := range d.recs {
		if r.Document.Root == root {
			out = append(out, r)
		}
	}
	return out, nil
}

var (
	ctcOperator  = ctbPerson("01920000-0000-7000-8000-0000000000e1", security.RoleOperator)
	ctcOperator2 = ctbPerson("01920000-0000-7000-8000-0000000000e2", security.RoleOperator)
	ctcAnalyst   = ctbPerson("01920000-0000-7000-8000-0000000000e3", security.RoleAnalyst)
	ctcAnalyst2  = ctbPerson("01920000-0000-7000-8000-0000000000e4", security.RoleAnalyst)
	ctcAuditor   = ctbPerson("01920000-0000-7000-8000-0000000000e5", security.RoleAuditor)
)

func ctcMoney(t *testing.T, s string) fiscal.Money {
	t.Helper()
	m, err := fiscal.ParseMoney(s, "EUR")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func ctcInvoice(t *testing.T) port.DocumentRecord {
	t.Helper()
	day := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	doc := id.NewFiscalDocumentID(uuid.MustParse("01920000-0000-7000-8000-000000000f01"))
	dec := id.NewDecisionID(uuid.MustParse("01920000-0000-7000-8000-000000000f02"))
	return port.DocumentRecord{
		Document: document.Document{ID: doc, TenantID: tenantID, LegalEntity: ctbEntity.ID, Type: document.TypeInvoice,
			Number: "FT-2026-000184", Root: doc, IssueDate: day, TaxPoint: day, Currency: "EUR", Decisions: []id.DecisionID{dec},
			Lines: []document.Line{{ID: id.NewFiscalLineID(uuid.MustParse("01920000-0000-7000-8000-000000000f03")), SourceLineRef: "BSS-1",
				Net: ctcMoney(t, "100.00"), Taxes: []document.TaxLine{{Decision: dec, Component: "VAT", Amount: ctcMoney(t, "22.00")}}}},
			Status: document.StatusCommitted},
		Net: ctcMoney(t, "100.00"), Tax: ctcMoney(t, "22.00"), Gross: ctcMoney(t, "122.00"),
	}
}

type ctcHarness struct {
	svc   *app.CTCService
	store *memCTC
	docs  *memDocs
	inv   port.DocumentRecord
}

func newCTC(t *testing.T) ctcHarness {
	t.Helper()
	inv := ctcInvoice(t)
	docs := &memDocs{recs: []port.DocumentRecord{inv}, history: map[id.FiscalDocumentID][]document.StatusEvent{
		inv.Document.ID: {{Document: inv.Document.ID, Seq: 1, Status: document.StatusCommitted}}}}
	store := newMemCTC()
	clk := &settableClock{t: time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)}
	return ctcHarness{svc: app.NewCTCService(store, docs, ctbEntities{def: ctbEntity}, noTx{}, clk, &idgen.Sequential{}),
		store: store, docs: docs, inv: inv}
}

func ctcTerms() ctc.Terms {
	return ctc.Terms{Duty: ctc.DutyRequired, Model: ctc.ModelClearance, Profile: ctc.ProfileITFatturaPA, FormatVersion: "FPR12 1.9",
		Authority: "Agenzia delle Entrate — SdI", LegalSource: "D.Lgs. 127/2015", SourceAsOf: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC),
		EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (h ctcHarness) record(t *testing.T) ctc.Applicability {
	t.Helper()
	a, err := h.svc.CreateApplicability(ctcOperator, app.CTCApplicabilityInput{Country: "it", DocumentType: document.TypeInvoice,
		BuyerKind: ctc.BuyerB2B, Terms: ctcTerms()})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (h ctcHarness) activate(t *testing.T, a ctc.Applicability) ctc.Applicability {
	t.Helper()
	if _, _, err := h.svc.AddApplicabilityEvidence(ctcOperator, a.ID, ctc.Attachment{SHA256: strings.Repeat("ab", 32),
		Filename: "opinion.pdf", MediaType: "application/pdf", Bytes: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.TransitionApplicability(ctcOperator, a.ID, app.CTCTransitionInput{Action: app.CTCSubmit}); err != nil {
		t.Fatal(err)
	}
	for _, g := range ctc.Gates {
		if _, ok, err := h.svc.SignApplicabilityGate(ctcAnalyst, a.ID, app.CTCGateInput{Gate: g, Version: 1, SignoffRef: "CAB-" + string(g)}); err != nil || !ok {
			t.Fatalf("gate %s: %v %v", g, ok, err)
		}
	}
	got, err := h.svc.TransitionApplicability(ctcAnalyst2, a.ID, app.CTCTransitionInput{Action: app.CTCActivate, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// The layer end to end: a record activated on G0–G4 by other people, a
// deterministic validation, an idempotent sealed submission, a four-eyed
// dispatch, and a rejection routed to the fiscal corrections flow.
func TestCTCEndToEnd(t *testing.T) {
	h := newCTC(t)
	a := h.record(t)
	if a.Country != "IT" || a.LegalEntity != ctbEntity.ID || a.Status != ctc.StatusDraft {
		t.Fatalf("record %+v", a.Cell)
	}
	if _, err := h.svc.CreateApplicability(ctcAuditor, app.CTCApplicabilityInput{}); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an auditor records nothing: %v", err)
	}
	doc := h.inv.Document.ID
	if _, _, err := h.svc.Validate(ctcOperator, doc, a.ID); errs.ReasonOf(err) != ctc.ReasonApplicabilityNotActive {
		t.Fatalf("nothing is validated under a draft: %v", err)
	}
	if _, err := h.svc.TransitionApplicability(ctcOperator, a.ID, app.CTCTransitionInput{Action: app.CTCActivate, Version: 1}); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an operator does not activate: %v", err)
	}
	a = h.activate(t, a)
	if a.Status != ctc.StatusActive {
		t.Fatalf("status %s", a.Status)
	}
	res, err := h.svc.Resolve(ctcAuditor, ctc.Cell{Country: "IT", DocumentType: document.TypeInvoice, BuyerKind: ctc.BuyerB2B},
		time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	if err != nil || res.Duty != ctc.DutyRequired {
		t.Fatalf("resolve %+v %v", res, err)
	}

	// A second record for the same cell does not activate over the first.
	b := h.record(t)
	if _, _, err := h.svc.AddApplicabilityEvidence(ctcOperator, b.ID, ctc.Attachment{SHA256: strings.Repeat("ab", 32),
		Filename: "opinion.pdf", MediaType: "application/pdf", Bytes: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.TransitionApplicability(ctcOperator, b.ID, app.CTCTransitionInput{Action: app.CTCSubmit}); err != nil {
		t.Fatal(err)
	}
	for _, g := range ctc.Gates {
		if _, _, err := h.svc.SignApplicabilityGate(ctcAnalyst, b.ID, app.CTCGateInput{Gate: g, Version: 1, SignoffRef: "CAB"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.svc.TransitionApplicability(ctcAnalyst2, b.ID, app.CTCTransitionInput{Action: app.CTCActivate, Version: 1}); errs.ReasonOf(err) != ctc.ReasonApplicabilityConflict {
		t.Fatalf("overlap: %v", err)
	}

	if _, _, err := h.svc.PrepareSubmission(ctcOperator, doc, a.ID); errs.ReasonOf(err) != ctc.ReasonValidationRequired {
		t.Fatalf("no submission without validation: %v", err)
	}
	v, created, err := h.svc.Validate(ctcOperator, doc, a.ID)
	if err != nil || !created || v.Outcome != ctc.OutcomePassed {
		t.Fatalf("validate %v %v %s", created, err, v.Outcome)
	}
	if again, created, err := h.svc.Validate(ctcAnalyst, doc, a.ID); err != nil || created || again.ID != v.ID {
		t.Fatalf("validating again returns the same report: %v %v", created, err)
	}

	sv, created, err := h.svc.PrepareSubmission(ctcOperator, doc, a.ID)
	if err != nil || !created {
		t.Fatalf("prepare %v %v", created, err)
	}
	if again, created, err := h.svc.PrepareSubmission(ctcOperator2, doc, a.ID); err != nil || created || again.Submission.ID != sv.Submission.ID {
		t.Fatalf("preparing again returns the live submission: %v %v", created, err)
	}
	sid, digest := sv.Submission.ID, sv.Submission.PayloadDigest.String()
	if _, _, err := h.svc.Dispatch(ctcOperator, sid, digest); errs.ReasonOf(err) != ctc.ReasonMakerChecker {
		t.Fatalf("the preparer does not dispatch: %v", err)
	}
	sv, ok, err := h.svc.Dispatch(ctcAnalyst, sid, digest)
	if err != nil || !ok || sv.Submission.Status != ctc.SubSent {
		t.Fatalf("dispatch %v %v", ok, err)
	}
	if _, ok, err := h.svc.Dispatch(ctcOperator2, sid, digest); err != nil || ok {
		t.Fatalf("a second dispatch is a no-op: %v %v", ok, err)
	}
	attempt := sv.Submission.Attempts()[0].Attempt
	sv, _, err = h.svc.Respond(ctcOperator, sid, ctc.Response{Kind: ctc.SubEventRejected, Attempt: attempt, AuthorityCode: "00404",
		EvidenceSHA256: strings.Repeat("CD", 32), Detail: "Duplicate"})
	if err != nil || sv.Correction == nil || !sv.Correction.Open() {
		t.Fatalf("reject %+v %v", sv.Correction, err)
	}

	// The corrections flow commits a credit note naming the rejected invoice.
	credit := h.inv
	credit.Document.ID = id.NewFiscalDocumentID(uuid.MustParse("01920000-0000-7000-8000-000000000f09"))
	credit.Document.Type, credit.Document.Predecessors = document.TypeCreditNote, []id.FiscalDocumentID{doc}
	h.docs.recs = append(h.docs.recs, credit)
	sv, err = h.svc.Submission(ctcAuditor, sid)
	if err != nil || sv.Correction == nil || sv.Correction.Open() {
		t.Fatalf("the credit closes the task: %+v %v", sv.Correction, err)
	}

	// A suspended record stops new validation.
	if _, err := h.svc.TransitionApplicability(ctcOperator, a.ID, app.CTCTransitionInput{Action: app.CTCSuspend, Reason: "Outage"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.Validate(ctcOperator, doc, a.ID); errs.ReasonOf(err) != ctc.ReasonApplicabilityNotActive {
		t.Fatalf("suspended: %v", err)
	}
}
