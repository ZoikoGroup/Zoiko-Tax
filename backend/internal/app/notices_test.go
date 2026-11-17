package app_test

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/notice"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// memNotices is an in-memory port.NoticeRepository: tenant-scoped and
// append-only, refusing an event that does not follow the history it has.
type memNotices struct {
	mu      sync.Mutex
	headers map[id.NoticeID]notice.Notice
	events  map[id.NoticeID][]notice.Event
}

var _ port.NoticeRepository = (*memNotices)(nil)

func newMemNotices() *memNotices {
	return &memNotices{headers: map[id.NoticeID]notice.Notice{}, events: map[id.NoticeID][]notice.Event{}}
}

func (m *memNotices) Lock(context.Context, id.NoticeID) error { return nil }

func (m *memNotices) Create(ctx context.Context, n notice.Notice, events []notice.Event) error {
	if t, ok := security.MustTenant(ctx); !ok || t != n.TenantID {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "tenant")
	}
	m.mu.Lock()
	m.headers[n.ID] = n
	m.mu.Unlock()
	return m.AppendEvents(ctx, n.ID, events)
}

func (m *memNotices) AppendEvents(_ context.Context, noticeID id.NoticeID, events []notice.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := append(append([]notice.Event(nil), m.events[noticeID]...), events...)
	if _, err := notice.Fold(m.headers[noticeID], next); err != nil {
		return errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "history does not fold")
	}
	m.events[noticeID] = next
	return nil
}

func (m *memNotices) Notice(ctx context.Context, noticeID id.NoticeID) (notice.Notice, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.headers[noticeID]
	if t, in := security.MustTenant(ctx); !ok || !in || t != h.TenantID {
		return notice.Notice{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "no such notice")
	}
	return notice.Fold(h, m.events[noticeID])
}

func (m *memNotices) List(ctx context.Context, _ int) ([]notice.Notice, error) {
	m.mu.Lock()
	ids := make([]id.NoticeID, 0, len(m.headers))
	for k := range m.headers {
		ids = append(ids, k)
	}
	m.mu.Unlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	var out []notice.Notice
	for _, k := range ids {
		n, err := m.Notice(ctx, k)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

var (
	noticePreparer = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000d1"))
	noticeApprover = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000d2"))
)

func asUser(u id.UserID, roles ...security.Role) context.Context {
	sc := security.New(tenantID, u, id.NewSessionID(uuid.MustParse("01920000-0000-7000-8000-0000000000cc")), roles, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	return security.Into(context.Background(), sc)
}

func noticeService(t *testing.T) (*app.NoticeService, *settableClock) {
	t.Helper()
	clk := &settableClock{t: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	return app.NewNoticeService(newMemNotices(), nil, noTx{}, clk, &idgen.Sequential{}), clk
}

func intake(t *testing.T, le id.LegalEntityID, due string) app.NoticeIntake {
	t.Helper()
	d, err := time.Parse(time.DateOnly, due)
	if err != nil {
		t.Fatal(err)
	}
	amount, err := fiscal.ParseMoney("1250.00", "EUR")
	if err != nil {
		t.Fatal(err)
	}
	return app.NoticeIntake{
		Notice: notice.Notice{Type: notice.TypeAssessment, Authority: "DE-BZST", Jurisdiction: "de", LegalEntity: le,
			AuthorityRef: "ST-2026-0042", ReceivedAt: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC), Amount: &amount},
		Deadline:         notice.Deadline{Date: d, Timezone: "Europe/Berlin", Source: notice.SourceStatedOnNotice, Verified: true},
		DeadlineEvidence: "Stated on page 1.",
	}
}

func TestNoticeServiceWorksANoticeThroughFourEyeApproval(t *testing.T) {
	svc, _ := noticeService(t)
	le := id.NewLegalEntityID(uuid.MustParse("01920000-0000-7000-8000-00000000e001"))
	op := asUser(noticePreparer, security.RoleOperator)
	an := asUser(noticeApprover, security.RoleAnalyst)

	v, err := svc.Intake(op, intake(t, le, "2026-10-31"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Jurisdiction != "DE" || v.Status != notice.StatusReceived {
		t.Fatalf("notice %+v", v.Notice)
	}
	steps := []func() (app.NoticeView, error){
		func() (app.NoticeView, error) { return svc.Transition(op, v.ID, notice.StatusAuthenticityReview, "") },
		func() (app.NoticeView, error) {
			return svc.RecordAuthenticity(op, v.ID, notice.AuthenticityVerified, "Phoned the authority.")
		},
		func() (app.NoticeView, error) { return svc.Transition(op, v.ID, notice.StatusLegalReview, "") },
		func() (app.NoticeView, error) { return svc.PrepareResponse(op, v.ID, "Objection", "") },
	}
	for _, step := range steps {
		if v, err = step(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.ApproveResponse(op, v.ID, 1, ""); errs.ReasonOf(err) != notice.ReasonSegregationOfDuties {
		t.Fatalf("self-approval: %v", err)
	}
	if v, err = svc.ApproveResponse(an, v.ID, 1, ""); err != nil {
		t.Fatal(err)
	}
	if v, err = svc.RecordSubmission(op, v.ID, "RCPT-1", "", ""); err != nil {
		t.Fatal(err)
	}
	if v.Status != notice.StatusSubmitted || v.Current().Date.Format(time.DateOnly) != "2026-10-31" {
		t.Fatalf("status %s deadline %v", v.Status, v.Current().Date)
	}
}

func TestNoticeServiceRoles(t *testing.T) {
	svc, _ := noticeService(t)
	le := id.NewLegalEntityID(uuid.MustParse("01920000-0000-7000-8000-00000000e001"))
	if _, err := svc.Intake(asUser(noticePreparer, security.RoleAuditor), intake(t, le, "2026-10-31")); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an auditor registered a notice: %v", err)
	}
	if _, err := svc.Intake(asUser(noticePreparer, security.RoleAdmin), intake(t, le, "2026-10-31")); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an administrator registered a notice: %v", err)
	}
	if _, err := svc.Intake(system(), intake(t, le, "2026-10-31")); err == nil {
		t.Fatal("system work registered a notice; every notice act is a person's")
	}
	v, err := svc.Intake(asUser(noticePreparer, security.RoleAnalyst), intake(t, le, "2026-10-31"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []security.Role{security.RoleAuditor, security.RoleAdmin} {
		if _, err := svc.Notice(asUser(noticeApprover, r), v.ID); err != nil {
			t.Fatalf("%s cannot read: %v", r, err)
		}
		if _, err := svc.Escalate(asUser(noticeApprover, r), v.ID, notice.EscalationLegal, "x", ""); errs.ReasonOf(err) != errs.ReasonForbidden {
			t.Fatalf("%s acted on a notice: %v", r, err)
		}
	}
}

func TestNoticeRegisterFiltersAndOrder(t *testing.T) {
	svc, clk := noticeService(t)
	op := asUser(noticePreparer, security.RoleOperator)
	le := id.NewLegalEntityID(uuid.MustParse("01920000-0000-7000-8000-00000000e001"))

	late, err := svc.Intake(op, intake(t, le, "2026-12-31"))
	if err != nil {
		t.Fatal(err)
	}
	soon, err := svc.Intake(op, intake(t, le, "2026-10-05"))
	if err != nil {
		t.Fatal(err)
	}
	unknown := intake(t, id.LegalEntityID{}, "2026-10-31")
	unknown.Deadline = notice.Deadline{Timezone: "Europe/Berlin", Source: notice.SourceUnknown}
	unknown.DeadlineEvidence = ""
	orphan, err := svc.Intake(op, unknown)
	if err != nil {
		t.Fatal(err)
	}
	if orphan.Status != notice.StatusLegalReview || !orphan.Urgent {
		t.Fatalf("SUP-08: status %s urgent %v", orphan.Status, orphan.Urgent)
	}

	all, err := svc.Notices(op, app.NoticeFilter{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ID != orphan.ID || all[1].ID != soon.ID || all[2].ID != late.ID {
		t.Fatalf("order: %v", []id.NoticeID{all[0].ID, all[1].ID, all[2].ID})
	}
	urgent, err := svc.Notices(op, app.NoticeFilter{UrgentOnly: true}, 0)
	if err != nil || len(urgent) != 1 || urgent[0].ID != orphan.ID {
		t.Fatalf("urgent: %v %v", len(urgent), err)
	}
	minimum, err := fiscal.ParseMoney("1000.00", "EUR")
	if err != nil {
		t.Fatal(err)
	}
	above, err := svc.Notices(op, app.NoticeFilter{AmountMin: &minimum, LegalEntity: le}, 0)
	if err != nil || len(above) != 2 {
		t.Fatalf("amount and entity: %v %v", len(above), err)
	}
	other, err := fiscal.ParseMoney("1000.00", "USD")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Notices(op, app.NoticeFilter{AmountMin: &other}, 0); len(got) != 0 {
		t.Fatal("an amount in another currency matched")
	}

	clk.Set(time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC))
	v, err := svc.Notice(op, soon.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range v.Flags {
		found = found || f == notice.FlagDeadlineSoon
	}
	if !found {
		t.Fatalf("flags %v", v.Flags)
	}
}
