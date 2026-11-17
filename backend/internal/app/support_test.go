package app_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/support"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// memSupport is an in-memory support case store: tenant-scoped and
// append-only, folding on read as the real adapter does.
type memSupport struct {
	mu     sync.Mutex
	heads  map[id.SupportCaseID]support.Case
	events map[id.SupportCaseID][]support.Event
	order  []id.SupportCaseID
}

func newMemSupport() *memSupport {
	return &memSupport{heads: map[id.SupportCaseID]support.Case{}, events: map[id.SupportCaseID][]support.Event{}}
}

func (m *memSupport) Lock(context.Context, id.SupportCaseID) error { return nil }

func (m *memSupport) Create(ctx context.Context, c support.Case, opened support.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sc, _ := security.From(ctx); sc.Tenant() != c.TenantID {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "tenant")
	}
	m.heads[c.ID] = c
	m.events[c.ID] = []support.Event{opened}
	m.order = append(m.order, c.ID)
	return nil
}

func (m *memSupport) Append(_ context.Context, caseID id.SupportCaseID, e support.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.Seq != len(m.events[caseID])+1 {
		return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "seq")
	}
	m.events[caseID] = append(m.events[caseID], e)
	return nil
}

func (m *memSupport) Case(ctx context.Context, caseID id.SupportCaseID) (support.Case, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fold(ctx, caseID)
}

func (m *memSupport) fold(ctx context.Context, caseID id.SupportCaseID) (support.Case, error) {
	h, ok := m.heads[caseID]
	if sc, _ := security.From(ctx); !ok || sc.Tenant() != h.TenantID {
		return support.Case{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such support case.")
	}
	return support.Fold(h, slices.Clone(m.events[caseID]))
}

func (m *memSupport) List(ctx context.Context, limit int) ([]support.Case, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = len(m.order)
	}
	var out []support.Case
	for i := len(m.order) - 1; i >= 0 && len(out) < limit; i-- {
		c, err := m.fold(ctx, m.order[i])
		if err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

type memAudit struct {
	mu   sync.Mutex
	recs []port.AuditRecord
}

func (a *memAudit) Append(_ context.Context, r port.AuditRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recs = append(a.recs, r)
	return nil
}

func (a *memAudit) List(context.Context, int) ([]port.AuditRecord, error) { return a.recs, nil }

func (a *memAudit) ListForSubject(context.Context, string, string, int) ([]port.AuditRecord, error) {
	return a.recs, nil
}

var (
	supportOpener = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000d1"))
	supportAgent  = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000d2"))
	supportLegal  = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000d3"))
)

func person(user id.UserID, roles ...security.Role) context.Context {
	sc := security.New(tenantID, user, id.NewSessionID(uuid.MustParse("01920000-0000-7000-8000-0000000000cc")), roles, time.Now())
	return security.Into(context.Background(), sc)
}

type supportHarness struct {
	svc   *app.SupportService
	store *memSupport
	audit *memAudit
	clock *settableClock
}

func newSupportHarness() *supportHarness {
	h := &supportHarness{store: newMemSupport(), audit: &memAudit{}, clock: &settableClock{t: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}}
	h.svc = app.NewSupportService(h.store, h.audit, noTx{}, h.clock, &idgen.Sequential{})
	return h
}

func (h *supportHarness) act(ctx context.Context, t *testing.T, caseID id.SupportCaseID, a support.Act) app.SupportCaseView {
	t.Helper()
	v, err := h.svc.ActOnCase(ctx, caseID, a)
	if err != nil {
		t.Fatalf("%s: %v", a.Action, err)
	}
	return v
}

func TestSupportCaseLifecycleThroughTheService(t *testing.T) {
	h := newSupportHarness()
	opener := person(supportOpener, security.RoleOperator)
	agent := person(supportAgent, security.RoleAnalyst)
	counsel := person(supportLegal, security.RoleAdmin)

	statutory := h.clock.Now().Add(2 * time.Hour)
	v, err := h.svc.OpenCase(opener, app.SupportCaseInput{
		Type: support.TypeLegal, Severity: support.SeverityS2, Subject: "Authority letter", Description: "The customer received a letter.",
		StatutoryDueAt: statutory, Refs: support.References{Decision: id.NewDecisionID(uuid.MustParse("01920000-0000-7000-8000-0000000000e1"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := v.Case
	if c.Status != support.StatusNew || !v.Clocks.SLADueAt.Equal(h.clock.Now().Add(24*time.Hour)) {
		t.Fatalf("opened: %s, SLA due %s", c.Status, v.Clocks.SLADueAt)
	}
	if !v.Clocks.StatutoryPrecedes || !v.Clocks.DueAt.Equal(statutory) {
		t.Fatalf("SUP-06: the earlier statutory deadline does not take precedence: %+v", v.Clocks)
	}

	h.act(agent, t, c.ID, support.Act{Action: support.ActionVerify})
	v = h.act(agent, t, c.ID, support.Act{Action: support.ActionTriage, Period: "2026-09"})
	if v.Case.Status != support.StatusTriageIncomplete {
		t.Fatalf("SUP-02: %s", v.Case.Status)
	}
	h.act(agent, t, c.ID, support.Act{Action: support.ActionTriage, ContractRef: "MSA-7", SellerRef: "seller:acme"})
	h.act(agent, t, c.ID, support.Act{Action: support.ActionAssign, Assignee: supportAgent})
	h.act(agent, t, c.ID, support.Act{Action: support.ActionEscalate, EscalateTo: support.EscalateLegal, Note: "Counsel to review."})
	h.act(agent, t, c.ID, support.Act{Action: support.ActionRespond, Note: "We are reviewing the letter."})
	v = h.act(agent, t, c.ID, support.Act{Action: support.ActionResolve})
	if !v.SignOffRequired || v.SignedOff || slices.Contains(v.Actions, support.ActionClose) {
		t.Fatalf("T18-18: a LEGAL case offers closure before sign-off: %+v", v.Actions)
	}

	if _, err := h.svc.ActOnCase(agent, c.ID, support.Act{Action: support.ActionClose}); errs.ReasonOf(err) != support.ReasonSignOffRequired {
		t.Fatalf("closed without sign-off: %v", err)
	}
	if _, err := h.svc.ActOnCase(opener, c.ID, support.Act{Action: support.ActionSignOff, Note: "ok"}); errs.ReasonOf(err) != support.ReasonSegregationOfDuties {
		t.Fatalf("the opener signed off: %v", err)
	}
	h.act(counsel, t, c.ID, support.Act{Action: support.ActionSignOff, Note: "Counsel approves the response."})
	h.clock.Advance(48 * time.Hour)
	v = h.act(agent, t, c.ID, support.Act{Action: support.ActionClose})
	if v.Case.Status != support.StatusClosed {
		t.Fatalf("status %s", v.Case.Status)
	}
	before := v.Clocks

	v = h.act(agent, t, c.ID, support.Act{Action: support.ActionReopen, Note: "The customer replied."})
	if v.Case.Status != support.StatusInReview || !v.Clocks.SLADueAt.Equal(before.SLADueAt) || !v.Clocks.StatutoryDueAt.Equal(before.StatutoryDueAt) {
		t.Fatalf("SUP-07: reopening moved a clock or did not reopen: %+v", v)
	}
	if v.SignedOff || v.Responded {
		t.Fatalf("a reopened case kept its earlier sign-off or response toward the next closure: %+v", v)
	}
	if v.Case.Refs.Decision.IsZero() {
		t.Fatal("the decision reference was lost")
	}

	// Every act reached the audit trail, without the case's text.
	if got, want := len(h.audit.recs), len(v.Case.History); got != want {
		t.Fatalf("%d audit records for %d events", got, want)
	}
	for _, r := range h.audit.recs {
		if r.SubjectType != "support_case" || r.SubjectID != c.ID.String() {
			t.Fatalf("audit subject %s/%s", r.SubjectType, r.SubjectID)
		}
	}
}

func TestSupportRoles(t *testing.T) {
	h := newSupportHarness()
	in := app.SupportCaseInput{Type: support.TypeFiling, Severity: support.SeverityS3, Subject: "s", Description: "d"}
	if _, err := h.svc.OpenCase(person(supportOpener, security.RoleAuditor), in); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an auditor opened a case: %v", err)
	}
	for _, r := range []security.Role{security.RoleAdmin, security.RoleOperator, security.RoleAnalyst} {
		if _, err := h.svc.OpenCase(person(supportOpener, r), in); err != nil {
			t.Fatalf("%s could not open a case: %v", r, err)
		}
	}
	auditor := person(supportAgent, security.RoleAuditor)
	got, err := h.svc.Cases(auditor, app.SupportCaseFilter{})
	if err != nil || len(got) != 3 {
		t.Fatalf("an auditor reads the queue: %d %v", len(got), err)
	}
	if _, err := h.svc.ActOnCase(auditor, got[0].Case.ID, support.Act{Action: support.ActionVerify}); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an auditor acted on a case: %v", err)
	}
	if _, err := h.svc.OpenCase(system(), in); errs.ReasonOf(err) != errs.ReasonUnauthenticated {
		t.Fatalf("the system opened a case with no person behind it: %v", err)
	}
}

func TestSupportQueueFiltersAndOrder(t *testing.T) {
	h := newSupportHarness()
	op := person(supportOpener, security.RoleOperator)
	open := func(typ support.Type, sev support.Severity, statutory time.Duration) app.SupportCaseView {
		t.Helper()
		in := app.SupportCaseInput{Type: typ, Severity: sev, Subject: "s", Description: "d", Product: "Telecom"}
		if statutory > 0 {
			in.StatutoryDueAt = h.clock.Now().Add(statutory)
		}
		v, err := h.svc.OpenCase(op, in)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	slow := open(support.TypeInvoiceExplanation, support.SeverityS4, 0)  // due +7d
	fast := open(support.TypeFiling, support.SeverityS1, 0)              // due +4h
	legal := open(support.TypeLegal, support.SeverityS4, 90*time.Minute) // statutory +90m precedes
	h.act(op, t, slow.Case.ID, support.Act{Action: support.ActionAssign, Assignee: supportAgent})

	all, err := h.svc.Cases(op, app.SupportCaseFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Case.ID != legal.Case.ID || all[1].Case.ID != fast.Case.ID || all[2].Case.ID != slow.Case.ID {
		t.Fatal("the queue is not soonest-due first, with the statutory deadline taking precedence")
	}
	for name, tc := range map[string]struct {
		f    app.SupportCaseFilter
		want int
	}{
		"type":       {app.SupportCaseFilter{Type: support.TypeFiling}, 1},
		"severity":   {app.SupportCaseFilter{Severity: support.SeverityS4}, 2},
		"assignee":   {app.SupportCaseFilter{Assignee: supportAgent}, 1},
		"unassigned": {app.SupportCaseFilter{Unassigned: true}, 2},
		"statutory":  {app.SupportCaseFilter{Statutory: support.ClockAtRisk}, 1},
		"no clock":   {app.SupportCaseFilter{Statutory: support.ClockNone}, 2},
		"product":    {app.SupportCaseFilter{Product: "telecom"}, 3},
		"status":     {app.SupportCaseFilter{Status: support.StatusVerified}, 0},
	} {
		got, err := h.svc.Cases(op, tc.f)
		if err != nil || len(got) != tc.want {
			t.Errorf("%s: %d cases, want %d (%v)", name, len(got), tc.want, err)
		}
	}

	h.clock.Advance(5 * time.Hour)
	breached, err := h.svc.Cases(op, app.SupportCaseFilter{SLA: support.ClockBreached})
	if err != nil || len(breached) != 1 || breached[0].Case.ID != fast.Case.ID {
		t.Fatalf("the S1 case past its four hours is the one breached: %d %v", len(breached), err)
	}
}
