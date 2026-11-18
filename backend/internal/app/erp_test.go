package app_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/erp"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	outboxevent "github.com/zoikogroup/zoikotax/backend/internal/domain/outbox"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// memERP is the ERP bridge store in memory: append-only, refusing a mapping
// version or a batch event sequence already taken, as migration 000034's
// keys do. Its posted journals are whatever the test puts in it.
type memERP struct {
	mu        sync.Mutex
	bridges   []erp.Bridge
	batches   []erp.Batch
	events    map[id.ERPExportID][]erp.Event
	journals  []subledger.Journal
	snapshots []erp.GLSnapshot
	recons    []erp.GLReconciliation
}

func (m *memERP) Lock(context.Context, string) error { return nil }

func (m *memERP) CreateBridge(_ context.Context, b erp.Bridge) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bridges = append(m.bridges, b)
	return nil
}

func (m *memERP) AppendMapping(_ context.Context, bridge id.ERPBridgeID, mp erp.Mapping) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.bridges {
		if m.bridges[i].ID == bridge {
			if len(m.bridges[i].Mappings)+1 != mp.Version {
				return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "version")
			}
			m.bridges[i].Mappings = append(m.bridges[i].Mappings, mp)
			return nil
		}
	}
	return errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
}

func (m *memERP) Bridge(_ context.Context, bridge id.ERPBridgeID) (erp.Bridge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.bridges {
		if b.ID == bridge {
			b.Mappings = slices.Clone(b.Mappings)
			return b, nil
		}
	}
	return erp.Bridge{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
}

func (m *memERP) Bridges(context.Context) ([]erp.Bridge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.bridges), nil
}

func (m *memERP) CreateBatch(_ context.Context, b erp.Batch, prepared erp.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.events == nil {
		m.events = map[id.ERPExportID][]erp.Event{}
	}
	m.batches = append(m.batches, b)
	m.events[b.ID] = []erp.Event{prepared}
	return nil
}

func (m *memERP) AppendBatchEvent(_ context.Context, batch id.ERPExportID, e erp.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events[batch])+1 != e.Seq {
		return errs.New(errs.CategoryConflict, errs.ReasonOptimisticConflict, "seq")
	}
	m.events[batch] = append(m.events[batch], e)
	return nil
}

func (m *memERP) folded(match func(erp.Batch) bool) []erp.Batch {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []erp.Batch
	for _, b := range m.batches {
		f, err := erp.Fold(b, m.events[b.ID])
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

func (m *memERP) Batch(_ context.Context, batch id.ERPExportID) (erp.Batch, error) {
	got := m.folded(func(b erp.Batch) bool { return b.ID == batch })
	if len(got) == 0 {
		return erp.Batch{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
	}
	return got[0], nil
}

func (m *memERP) Batches(_ context.Context, bridge id.ERPBridgeID, f erp.Filter) ([]erp.Batch, error) {
	return m.folded(func(b erp.Batch) bool {
		return b.Bridge == bridge && (f.Period == "" || b.Period == f.Period) && (f.Currency == "" || b.Currency == f.Currency) &&
			(f.Status == "" || b.Status == f.Status)
	}), nil
}

func (m *memERP) PeriodJournals(_ context.Context, le id.LegalEntityID, period string, cur fiscal.Currency) ([]subledger.Journal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []subledger.Journal
	for _, j := range m.journals {
		if j.LegalEntity == le && j.LegalPeriod == period && j.Currency == cur {
			out = append(out, j)
		}
	}
	return out, nil
}

func (m *memERP) RecordSnapshot(_ context.Context, s erp.GLSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshots = append(m.snapshots, s)
	return nil
}

func (m *memERP) Snapshot(_ context.Context, sid id.GLSnapshotID) (erp.GLSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.snapshots {
		if s.ID == sid {
			return s, nil
		}
	}
	return erp.GLSnapshot{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
}

func (m *memERP) Snapshots(_ context.Context, bridge id.ERPBridgeID, period string) ([]erp.GLSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []erp.GLSnapshot
	for _, s := range m.snapshots {
		if s.Bridge == bridge && (period == "" || s.Period == period) {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *memERP) RecordReconciliation(_ context.Context, r erp.GLReconciliation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recons = append(m.recons, r)
	return nil
}

func (m *memERP) Reconciliations(_ context.Context, bridge id.ERPBridgeID, period string) ([]erp.GLReconciliation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []erp.GLReconciliation
	for _, r := range m.recons {
		if r.Bridge == bridge && (period == "" || r.Period == period) {
			out = append(out, r)
		}
	}
	return out, nil
}

// memOutbox keeps what was appended.
type memOutbox struct {
	mu     sync.Mutex
	events []outboxevent.Event
}

func (o *memOutbox) Append(_ context.Context, e outboxevent.Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, e)
	return nil
}

func (o *memOutbox) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.events)
}

var (
	erpOperator  = ctbPerson("01920000-0000-7000-8000-0000000000d1", security.RoleOperator)
	erpOperator2 = ctbPerson("01920000-0000-7000-8000-0000000000d2", security.RoleOperator)
	erpAnalyst   = ctbPerson("01920000-0000-7000-8000-0000000000d3", security.RoleAnalyst)
	erpAuditor   = ctbPerson("01920000-0000-7000-8000-0000000000d4", security.RoleAuditor)
)

func erpJournal(t *testing.T, n int, tax string) subledger.Journal {
	t.Helper()
	m, err := fiscal.ParseMoney(tax, "USD")
	if err != nil {
		t.Fatal(err)
	}
	j := subledger.Journal{
		ID: id.NewJournalID(uuid.MustParse(fmt.Sprintf("01920000-0000-7000-8000-%012d", 500+n))), TenantID: tenantID,
		LegalEntity: ctbEntity.ID, Type: subledger.JournalInvoice, Source: subledger.SourceEvent{Kind: "DECISION_COMMITTED", ID: fmt.Sprint(n)},
		PostingDate: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), LegalPeriod: "2026-09", Currency: "USD",
		Profile: subledger.ProfileRef{ID: "p", Version: "1"},
		Lines: []subledger.JournalLine{
			{Account: subledger.AccountCustomerGLBridge, Side: subledger.Debit, Amount: m},
			{Account: subledger.AccountTaxCollectedLiability, Side: subledger.Credit, Amount: m},
		},
	}
	if err := j.Validate(); err != nil {
		t.Fatal(err)
	}
	return j
}

type erpHarness struct {
	svc    *app.ERPService
	store  *memERP
	outbox *memOutbox
	bridge erp.Bridge
}

var erpAccounts = []erp.AccountMap{
	{Account: subledger.AccountTaxCollectedLiability, GLAccount: "2310-00"},
	{Account: subledger.AccountCustomerGLBridge, GLAccount: "1990-00"},
}

func newERP(t *testing.T, journals ...subledger.Journal) erpHarness {
	t.Helper()
	store := &memERP{journals: journals}
	out := &memOutbox{}
	clk := &settableClock{t: time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)}
	svc := app.NewERPService(store, ctbEntities{def: ctbEntity}, out, noTx{}, clk, &idgen.Sequential{})
	b, err := svc.CreateBridge(erpOperator, app.ERPBridgeInput{Name: "Acme — SAP", Vendor: erp.VendorSAP, CompanyCode: "AC01"})
	if err != nil {
		t.Fatal(err)
	}
	if b.LegalEntity != ctbEntity.ID {
		t.Fatalf("bridge entity %s", b.LegalEntity)
	}
	b, created, err := svc.RecordMapping(erpOperator, b.ID, app.ERPMappingInput{ChartVersion: "COA-2026.2", Accounts: erpAccounts})
	if err != nil || !created {
		t.Fatalf("mapping: %v %v", created, err)
	}
	return erpHarness{svc: svc, store: store, outbox: out, bridge: b}
}

func (h erpHarness) prepare(t *testing.T) app.ERPBatchView {
	t.Helper()
	v, created, err := h.svc.Prepare(erpOperator, h.bridge.ID, app.ERPPrepareInput{Period: "2026-09", Currency: "usd", PostingDate: "2026-10-03"})
	if err != nil || !created {
		t.Fatalf("prepare: %v %v", created, err)
	}
	return v
}

func (h erpHarness) dispatch(t *testing.T, v app.ERPBatchView) app.ERPBatchView {
	t.Helper()
	got, dispatched, err := h.svc.Dispatch(erpAnalyst, h.bridge.ID, v.Batch.ID, v.Batch.Digest.String())
	if err != nil || !dispatched {
		t.Fatalf("dispatch: %v %v", dispatched, err)
	}
	return got
}

func lastAttempt(v app.ERPBatchView) id.ERPAttemptID {
	a := v.Batch.Attempts()
	return a[len(a)-1].Attempt
}

func (h erpHarness) snapshot(t *testing.T, bridge1990, liability2310 string) erp.GLSnapshot {
	t.Helper()
	s, err := h.svc.RecordSnapshot(erpOperator, h.bridge.ID, app.GLSnapshotInput{
		Period: "2026-09", Currency: "USD", AsOf: "2026-10-05T18:00:00Z", SourceRef: "FAGLB03 AC01 2026/09",
		Balances: []app.GLSnapshotBalance{
			{GLAccount: "2310-00", Debits: "0.00", Credits: liability2310},
			{GLAccount: "1990-00", Debits: bridge1990, Credits: "0.00"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The whole bridge: a mapping, a sealed batch, a four-eyed dispatch through
// the outbox, a retry that is no new attempt, a receipt kept apart from the
// posting, and a GL that ties.
func TestAnExportIsSealedDispatchedOnceAndReconciled(t *testing.T) {
	h := newERP(t, erpJournal(t, 1, "10.00"), erpJournal(t, 2, "7.50"))

	// An identical mapping records no new version.
	if b, created, err := h.svc.RecordMapping(erpOperator, h.bridge.ID, app.ERPMappingInput{ChartVersion: "COA-2026.2",
		Accounts: []erp.AccountMap{erpAccounts[1], erpAccounts[0]}}); err != nil || created || len(b.Mappings) != 1 {
		t.Fatalf("same mapping again: created %v, %d versions, %v", created, len(b.Mappings), err)
	}

	v := h.prepare(t)
	if len(v.Batch.Journals) != 2 || v.Batch.Status != erp.StatusPrepared || v.Batch.Currency != "USD" {
		t.Fatalf("batch %d journals, %s", len(v.Batch.Journals), v.Batch.Status)
	}
	// Preparing again finds nothing new and returns the same batch.
	again, created, err := h.svc.Prepare(erpOperator, h.bridge.ID, app.ERPPrepareInput{Period: "2026-09", Currency: "USD", PostingDate: "2026-10-03"})
	if err != nil || created || again.Batch.ID != v.Batch.ID {
		t.Fatalf("prepare again: created %v, %v", created, err)
	}

	if _, _, err := h.svc.Dispatch(erpOperator, h.bridge.ID, v.Batch.ID, v.Batch.Digest.String()); errs.ReasonOf(err) != erp.ReasonMakerChecker {
		t.Fatalf("the preparer dispatched: %v", err)
	}
	if _, _, err := h.svc.Dispatch(erpAuditor, h.bridge.ID, v.Batch.ID, v.Batch.Digest.String()); !errs.IsCategory(err, errs.CategoryPolicy) {
		t.Fatalf("an auditor dispatched: %v", err)
	}
	v = h.dispatch(t, v)
	if v.Batch.Status != erp.StatusTransmitted || h.outbox.count() != 1 {
		t.Fatalf("status %s, %d events", v.Batch.Status, h.outbox.count())
	}
	e := h.outbox.events[0]
	if e.Type != app.EventGLExportDispatched || e.AggregateKey != "erp-export/"+v.Batch.ID.String() {
		t.Fatalf("event %s %s", e.Type, e.AggregateKey)
	}
	// QA-LED-069: the same batch again is no new attempt and no new event.
	if got, dispatched, err := h.svc.Dispatch(erpOperator2, h.bridge.ID, v.Batch.ID, v.Batch.Digest.String()); err != nil || dispatched ||
		len(got.Batch.Attempts()) != 1 || h.outbox.count() != 1 {
		t.Fatalf("retry: dispatched %v, %d attempts, %d events, %v", dispatched, len(got.Batch.Attempts()), h.outbox.count(), err)
	}

	attempt := lastAttempt(v)
	v, recorded, err := h.svc.Respond(erpOperator, h.bridge.ID, v.Batch.ID, erp.Response{Kind: erp.EventAcknowledged, Attempt: attempt, RemoteCorrelation: " MSG-1 "})
	if err != nil || !recorded || v.Batch.Status != erp.StatusAcknowledged {
		t.Fatalf("receipt: %v %s %v", recorded, v.Batch.Status, err)
	}

	// A receipt is not a posting: the GL is still held.
	snap := h.snapshot(t, "17.50", "17.50")
	r, err := h.svc.Reconcile(erpOperator, h.bridge.ID, snap.ID)
	if err != nil || r.Outcome != erp.OutcomeHeld || r.Holds[0].Reason != erp.HoldAwaitingPosting {
		t.Fatalf("reconcile after receipt: %s %v %v", r.Outcome, r.Holds, err)
	}

	posted := erp.Response{Kind: erp.EventPosted, Attempt: attempt, ERPReference: "AC01/2026/1900004411"}
	if v, recorded, err = h.svc.Respond(erpAnalyst, h.bridge.ID, v.Batch.ID, posted); err != nil || !recorded || v.Batch.Status != erp.StatusERPPosted {
		t.Fatalf("posting: %v %s %v", recorded, v.Batch.Status, err)
	}
	if _, recorded, err = h.svc.Respond(erpAnalyst, h.bridge.ID, v.Batch.ID, posted); err != nil || recorded {
		t.Fatalf("the same posting again: %v %v", recorded, err)
	}
	r, err = h.svc.Reconcile(erpOperator, h.bridge.ID, snap.ID)
	if err != nil || r.Outcome != erp.OutcomeReconciled || r.PostedJournals != 2 || r.RanBy.IsZero() {
		t.Fatalf("reconcile after posting: %s %d %v", r.Outcome, r.PostedJournals, err)
	}

	// The auditor reads everything and changes nothing.
	if list, err := h.svc.Reconciliations(erpAuditor, h.bridge.ID, "2026-09"); err != nil || len(list) != 2 {
		t.Fatalf("auditor reads: %d %v", len(list), err)
	}
	if _, _, err := h.svc.Prepare(erpAuditor, h.bridge.ID, app.ERPPrepareInput{Period: "2026-09", Currency: "USD", PostingDate: "2026-10-03"}); !errs.IsCategory(err, errs.CategoryPolicy) {
		t.Fatalf("an auditor prepared: %v", err)
	}
	if _, err := h.svc.Bridges(ctbAdmin); !errs.IsCategory(err, errs.CategoryPolicy) {
		t.Fatalf("an admin read the bridges: %v", err)
	}
}

// QA-LED-068: a mapping change between preparation and dispatch is refused;
// the batch is withdrawn and prepared again under the new version.
func TestAMappingChangeBlocksTheDispatch(t *testing.T) {
	h := newERP(t, erpJournal(t, 1, "10.00"))
	v := h.prepare(t)
	b, created, err := h.svc.RecordMapping(erpOperator, h.bridge.ID, app.ERPMappingInput{ChartVersion: "COA-2026.3", Accounts: []erp.AccountMap{
		{Account: subledger.AccountTaxCollectedLiability, GLAccount: "2311-00"}, erpAccounts[1]}})
	if err != nil || !created || len(b.Mappings) != 2 {
		t.Fatalf("mapping v2: %v %v", created, err)
	}
	if _, _, err := h.svc.Dispatch(erpAnalyst, h.bridge.ID, v.Batch.ID, v.Batch.Digest.String()); errs.ReasonOf(err) != erp.ReasonMappingChanged {
		t.Fatalf("dispatch under a superseded mapping: %v", err)
	}
	if h.outbox.count() != 0 {
		t.Fatal("a refused dispatch left an event")
	}
	// The journals are held by the prepared batch until it is withdrawn.
	if _, _, err := h.svc.Prepare(erpOperator, h.bridge.ID, app.ERPPrepareInput{Period: "2026-09", Currency: "USD", PostingDate: "2026-10-04"}); errs.ReasonOf(err) != erp.ReasonNothingToExport {
		t.Fatalf("prepare while held: %v", err)
	}
	if _, err := h.svc.Withdraw(erpOperator, h.bridge.ID, v.Batch.ID, "mapping v2"); err != nil {
		t.Fatal(err)
	}
	v2 := h.prepare(t)
	if v2.Batch.MappingVersion != 2 || v2.Batch.Lines[1].GLAccount != "2311-00" {
		t.Fatalf("v2 batch: version %d lines %+v", v2.Batch.MappingVersion, v2.Batch.Lines)
	}
}

// LED-23 "ERP posting failure": a rejection opens a correction task and
// holds the reconciliation, until a new batch carries the journals into the
// GL.
func TestARejectionHoldsTheReconciliationUntilReposted(t *testing.T) {
	h := newERP(t, erpJournal(t, 1, "10.00"))
	v := h.dispatch(t, h.prepare(t))
	v, _, err := h.svc.Respond(erpOperator, h.bridge.ID, v.Batch.ID, erp.Response{Kind: erp.EventRejected, Attempt: lastAttempt(v),
		RemoteCorrelation: "MSG-9", Detail: "F5 702: account 2310-00 blocked"})
	if err != nil || v.Batch.Status != erp.StatusRejected || v.Correction == nil || !v.Correction.Open() {
		t.Fatalf("rejection: %s %+v %v", v.Batch.Status, v.Correction, err)
	}
	snap := h.snapshot(t, "0.00", "0.00")
	r, err := h.svc.Reconcile(erpOperator, h.bridge.ID, snap.ID)
	if err != nil || r.Outcome != erp.OutcomeHeld || r.Holds[0].Reason != erp.HoldRejected {
		t.Fatalf("reconcile after rejection: %s %v %v", r.Outcome, r.Holds, err)
	}
	if r.Exceptions() != 2 {
		t.Fatalf("%d exceptions, want the two unposted accounts", r.Exceptions())
	}

	// The correction: a new batch of the released journals, posted.
	v2 := h.dispatch(t, h.prepare(t))
	if _, _, err := h.svc.Respond(erpOperator, h.bridge.ID, v2.Batch.ID, erp.Response{Kind: erp.EventPosted, Attempt: lastAttempt(v2),
		ERPReference: "AC01/2026/2"}); err != nil {
		t.Fatal(err)
	}
	listed, err := h.svc.Batches(erpAuditor, h.bridge.ID, erp.Filter{Status: erp.StatusRejected})
	if err != nil || len(listed) != 1 || listed[0].Correction == nil || listed[0].Correction.Open() {
		t.Fatalf("rejected batches: %+v %v", listed, err)
	}
	r, err = h.svc.Reconcile(erpOperator, h.bridge.ID, h.snapshot(t, "10.00", "10.00").ID)
	if err != nil || r.Outcome != erp.OutcomeReconciled {
		t.Fatalf("reconcile after reposting: %s %v %v", r.Outcome, r.Holds, err)
	}
}

// LED-23 "External status unknown": a resend needs a status query first,
// and every attempt carries the batch's one external reference.
func TestAnUnknownOutcomeNeedsAStatusQuery(t *testing.T) {
	h := newERP(t, erpJournal(t, 1, "10.00"))
	v := h.dispatch(t, h.prepare(t))
	first := lastAttempt(v)
	if _, _, err := h.svc.Respond(erpOperator, h.bridge.ID, v.Batch.ID, erp.Response{Kind: erp.EventOutcomeUnknown, Attempt: first,
		Detail: "connector timed out"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.Dispatch(erpAnalyst, h.bridge.ID, v.Batch.ID, v.Batch.Digest.String()); errs.ReasonOf(err) != erp.ReasonOutcomeUnknown {
		t.Fatalf("blind resend: %v", err)
	}
	if _, _, err := h.svc.Respond(erpOperator, h.bridge.ID, v.Batch.ID, erp.Response{Kind: erp.EventStatusNotFound, Attempt: first,
		RemoteCorrelation: "QUERY-1"}); err != nil {
		t.Fatal(err)
	}
	v = h.dispatch(t, v)
	if len(v.Batch.Attempts()) != 2 || h.outbox.count() != 2 {
		t.Fatalf("%d attempts, %d events", len(v.Batch.Attempts()), h.outbox.count())
	}
	for _, e := range h.outbox.events {
		body, err := canonical.Encode(e.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), v.Batch.ExternalRef()) || !strings.Contains(string(body), v.Batch.Digest.String()) {
			t.Fatalf("an attempt's event does not carry the batch's external reference")
		}
	}
}

func TestASnapshotOnAnotherBridgeIsNotReconciled(t *testing.T) {
	h := newERP(t, erpJournal(t, 1, "10.00"))
	other, err := h.svc.CreateBridge(erpOperator, app.ERPBridgeInput{Name: "Other", Vendor: erp.VendorNetSuite, CompanyCode: "AC02"})
	if err != nil {
		t.Fatal(err)
	}
	snap := h.snapshot(t, "10.00", "10.00")
	if _, err := h.svc.Reconcile(erpOperator, other.ID, snap.ID); !errs.IsCategory(err, errs.CategoryNotFound) {
		t.Fatalf("cross-bridge snapshot: %v", err)
	}
	if _, err := h.svc.Reconcile(erpOperator, h.bridge.ID, snap.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.RecordSnapshot(erpOperator, h.bridge.ID, app.GLSnapshotInput{Period: "2026-09", Currency: "USD",
		AsOf: "2026-10-05T18:00:00Z", SourceRef: "R", Balances: []app.GLSnapshotBalance{{GLAccount: "1", Debits: "-1.00", Credits: "0"}}}); !errs.IsCategory(err, errs.CategoryValidation) {
		t.Fatalf("a negative reported amount: %v", err)
	}
}
