package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/legal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// The dashboard's arithmetic and ranking, over in-memory stores. The SQL is
// exercised against Postgres in dashboard_integration_test.go.

var dashboardEntity = id.NewLegalEntityID(uuid.MustParse("01920000-0000-7000-8000-0000000000ee"))

type fakeDashboardRepo struct {
	decisions map[string]port.DecisionCount
	postings  []port.Posting
	summary   port.ReconciliationSummary
	breaks    []port.ReconciliationException
	seals     port.SealCoverage
	refunds   []port.RefundAwaiting
	dead      int

	// what the service asked for
	fromPeriod, toPeriod string
}

func (f *fakeDashboardRepo) Decisions(context.Context, time.Time, time.Time) (map[string]port.DecisionCount, error) {
	return f.decisions, nil
}

func (f *fakeDashboardRepo) Postings(_ context.Context, _ id.LegalEntityID, _ subledger.Account, from, to string) ([]port.Posting, error) {
	f.fromPeriod, f.toPeriod = from, to
	return f.postings, nil
}

func (f *fakeDashboardRepo) Reconciliation(context.Context, id.LegalEntityID, int) (port.ReconciliationSummary, []port.ReconciliationException, error) {
	return f.summary, f.breaks, nil
}

func (f *fakeDashboardRepo) SealCoverage(context.Context) (port.SealCoverage, error) {
	return f.seals, nil
}

func (f *fakeDashboardRepo) RefundsAwaiting(context.Context, time.Time, int) ([]port.RefundAwaiting, error) {
	return f.refunds, nil
}

func (f *fakeDashboardRepo) DeadDeliveries(context.Context, time.Time) (int, error) {
	return f.dead, nil
}

// The fakes below implement only what the dashboard calls; the embedded
// interface panics on anything else, which would be a test failure worth
// seeing.
type fakeEntities struct{ port.LegalEntityRepository }

func (fakeEntities) Default(context.Context) (identity.LegalEntity, error) {
	return identity.LegalEntity{ID: dashboardEntity, Default: true}, nil
}

type fakeObligations struct {
	port.ObligationRepository
	rows []obligation.Obligation
	sums map[string]fiscal.Money
}

func (f fakeObligations) List(context.Context, port.ObligationFilter) ([]obligation.Obligation, error) {
	return append([]obligation.Obligation(nil), f.rows...), nil
}

func (f fakeObligations) Assessed(context.Context, []string) (map[string]fiscal.Money, error) {
	return f.sums, nil
}

type fakeAuths struct{ port.AuthorizationRepository }

func (fakeAuths) List(context.Context) ([]legal.Authorization, error) { return nil, nil }

func money(t *testing.T, amount, currency string) fiscal.Money {
	t.Helper()
	return fiscaltest.Money(t, amount, fiscal.Currency(currency))
}

func dueObligation(key string, status obligation.Status, due time.Time) obligation.Obligation {
	return obligation.Obligation{
		ID: id.NewObligationID(uuid.Must(uuid.NewV7())), BusinessKey: key, LegalEntity: dashboardEntity,
		JurisdictionID: "jurisdiction:xa", Type: "VAT_RETURN", Status: status,
		Period: obligation.Period{
			Start: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), Due: due,
		},
	}
}

func dashboardMatrix() legal.Matrix {
	rule := func(id, country, authority string, status legal.Status) legal.Rule {
		auth := legal.AuthNone
		if status == legal.StatusDirectWithAuth {
			auth = legal.AuthFilingMandate
		}
		return legal.Rule{ID: id, Version: 1, Country: country, Authority: authority, Service: legal.ServiceFile,
			Status: status, Provider: "test", AuthorizationType: auth, Funds: legal.FundsNoCustody, OpinionRef: "T-1",
			EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	}
	return legal.Matrix{Version: "test-1", Rules: []legal.Rule{
		rule("GB-HMRC-FILE", "GB", "GB-HMRC", legal.StatusCounselPending),
		rule("DE-ELSTER-FILE", "DE", "DE-ELSTER", legal.StatusDirectWithAuth),
		rule("FR-DGFIP-FILE", "FR", "FR-DGFIP", legal.StatusDirectAllowed),
	}}
}

func TestDashboardRefusesAnAdministrator(t *testing.T) {
	svc := app.NewDashboardService(&fakeDashboardRepo{}, fakeEntities{}, fakeObligations{}, fakeAuths{}, legal.Matrix{},
		&settableClock{t: time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)})
	_, err := svc.Dashboard(as(security.RoleAdmin), "")
	if errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an ADMIN-only subject read the dashboard: %v", err)
	}
}

func TestDashboardRefusesAMalformedPeriod(t *testing.T) {
	svc := app.NewDashboardService(&fakeDashboardRepo{}, fakeEntities{}, fakeObligations{}, fakeAuths{}, legal.Matrix{},
		&settableClock{t: time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)})
	for _, p := range []string{"2026-13", "2026-9", "September", "2026-09-01"} {
		if _, err := svc.Dashboard(as(security.RoleAnalyst), p); errs.ReasonOf(err) != errs.ReasonInvalidValue {
			t.Errorf("period %q: got %v, want INVALID_VALUE", p, err)
		}
	}
}

func TestDashboardEmptyCellReportsZeros(t *testing.T) {
	svc := app.NewDashboardService(&fakeDashboardRepo{}, fakeEntities{}, fakeObligations{}, fakeAuths{}, legal.Matrix{},
		&settableClock{t: time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)})
	d, err := svc.Dashboard(as(security.RoleAuditor), "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Period != "2026-10" || d.Determinations.PreviousPeriod != "2026-09" {
		t.Fatalf("periods %s / %s", d.Period, d.Determinations.PreviousPeriod)
	}
	if len(d.Trend.Points) != app.DashboardTrendPeriods || d.Trend.Points[0].Period != "2026-03" || d.Trend.Currency != "" {
		t.Fatalf("empty trend: %+v", d.Trend)
	}
	if d.Actions == nil || len(d.Actions) != 0 || len(d.Liability) != 0 || d.Jurisdictions.Authorities == nil {
		t.Fatalf("an empty cell has empty, not absent, lists: %+v", d)
	}
}

func TestDashboardAssemblesTheRecord(t *testing.T) {
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	repo := &fakeDashboardRepo{
		decisions: map[string]port.DecisionCount{
			"2026-09": {Recorded: 4, ReviewRequired: 2},
			"2026-08": {Recorded: 3},
		},
		postings: []port.Posting{
			{Period: "2026-08", Side: subledger.Credit, Amount: money(t, "50.00", "EUR"), Lines: 3},
			{Period: "2026-09", Side: subledger.Credit, Amount: money(t, "100.00", "EUR"), Lines: 4},
			{Period: "2026-09", Side: subledger.Debit, Amount: money(t, "21.11", "EUR"), Lines: 1},
			{Period: "2026-09", Side: subledger.Credit, Amount: money(t, "999.00", "USD"), Lines: 1},
		},
		summary: port.ReconciliationSummary{Runs: 1, Matched: 9, Breaks: 1},
		breaks: []port.ReconciliationException{{
			ItemID: "01920ab0-2b3c-7d4e-8f50-6a7b8c9d0e1f", Period: "2026-09", Stage: "R1_CALCULATED_TO_DOCUMENT",
			Status: "MISSING", MatchKey: "decision:x",
		}},
		seals: port.SealCoverage{Decisions: 7, Sealed: 3, Seals: 2},
		refunds: []port.RefundAwaiting{{RefundID: "01920a50-2a3b-7c4d-8e5f-6a7b8c9d0e1f", Status: "UNCERTAIN",
			Amount: money(t, "10.50", "EUR"), PaymentReference: "psp:1", RequestedAt: now.Add(-48 * time.Hour)}},
		dead: 3,
	}
	obligations := fakeObligations{
		rows: []obligation.Obligation{
			dueObligation("late", obligation.StatusOpen, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)),
			dueObligation("soon", obligation.StatusReady, time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)),
			dueObligation("later", obligation.StatusOpen, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)),
			dueObligation("filed", obligation.StatusFiled, time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)),
			dueObligation("outside", obligation.StatusOpen, time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC)),
		},
		sums: map[string]fiscal.Money{
			"late": money(t, "40.00", "EUR"), "soon": money(t, "2.50", "EUR"),
			"later": money(t, "7.00", "USD"), "filed": money(t, "1000.00", "EUR"),
		},
	}
	other := dueObligation("elsewhere", obligation.StatusOpen, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	other.LegalEntity = id.NewLegalEntityID(uuid.Must(uuid.NewV7()))
	obligations.rows = append(obligations.rows, other)

	svc := app.NewDashboardService(repo, fakeEntities{}, obligations, fakeAuths{}, dashboardMatrix(), &settableClock{t: now})
	d, err := svc.Dashboard(as(security.RoleOperator), "2026-09")
	if err != nil {
		t.Fatal(err)
	}

	if repo.fromPeriod != "2026-02" || repo.toPeriod != "2026-09" {
		t.Errorf("postings read for %s..%s, want 2026-02..2026-09", repo.fromPeriod, repo.toPeriod)
	}
	det := d.Determinations
	if det.Decisions != 4 || det.PreviousDecisions != 3 || det.ReviewRequired != 2 {
		t.Errorf("determinations %+v", det)
	}
	// One amount per currency, never one sum across them.
	if got := moneyStrings(det.Tax); got != "EUR 100.00, USD 999.00" {
		t.Errorf("tax %s", got)
	}
	if got := moneyStrings(det.PreviousTax); got != "EUR 50.00" {
		t.Errorf("previous tax %s", got)
	}
	if len(d.Liability) != 2 || d.Liability[0].Debits.CanonicalString() != "21.11" ||
		d.Liability[1].Debits.CanonicalString() != "0.00" || d.Liability[1].Credits.Currency() != "USD" {
		t.Errorf("liability %+v", d.Liability)
	}

	// The window: late (overdue), soon and later count; filed, outside and
	// another entity's do not.
	ob := d.Obligations
	if ob.Due != 3 || ob.Overdue != 1 || moneyStrings(ob.Amounts) != "EUR 42.50, USD 7.00" {
		t.Errorf("obligations due %d overdue %d amounts %s", ob.Due, ob.Overdue, moneyStrings(ob.Amounts))
	}

	// EUR has more lines than USD, so the trend is in EUR; September is the
	// largest point.
	tr := d.Trend
	if tr.Currency != "EUR" || len(tr.Points) != 8 || tr.Points[7].Period != "2026-09" {
		t.Fatalf("trend %+v", tr)
	}
	if tr.Points[7].Index != 100 || tr.Points[6].Index != 50 || tr.Points[5].Index != 0 || tr.Points[5].Liability != nil {
		t.Errorf("trend indexes %d %d %d", tr.Points[7].Index, tr.Points[6].Index, tr.Points[5].Index)
	}
	if tr.Points[7].Decisions != 4 {
		t.Errorf("trend decisions %d", tr.Points[7].Decisions)
	}

	j := d.Jurisdictions
	if j.Ready != 1 || j.AtRisk != 1 || j.Blocked != 1 || len(j.Authorities) != 3 {
		t.Fatalf("jurisdictions %+v", j)
	}
	if a := j.Authorities[0]; a.Country != "DE" || a.Readiness != app.ReadinessAtRisk || a.Reason != legal.ReasonAuthMissing {
		t.Errorf("DE %+v", a)
	}
	if a := j.Authorities[1]; a.Country != "FR" || a.Readiness != app.ReadinessReady || a.Reason != "" {
		t.Errorf("FR %+v", a)
	}
	if a := j.Authorities[2]; a.Country != "GB" || a.Readiness != app.ReadinessBlocked || a.Reason != legal.ReasonCounselPending {
		t.Errorf("GB %+v", a)
	}

	// Most urgent first: P0 by due date then undated, then P1, then P2.
	var kinds []app.ActionKind
	for _, a := range d.Actions {
		kinds = append(kinds, a.Kind)
	}
	want := []app.ActionKind{
		app.ActionObligationOverdue, app.ActionRefundUncertain,
		app.ActionObligationDue, app.ActionReconciliationBreak, app.ActionDecisionReview,
		app.ActionObligationDue, app.ActionWebhookDead,
	}
	if len(kinds) != len(want) {
		t.Fatalf("actions %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("actions %v, want %v", kinds, want)
		}
	}
	if a := d.Actions[0]; a.Priority != app.P0 || a.Due != "2026-10-01" || a.Exposure == nil || a.Exposure.CanonicalString() != "40.00" {
		t.Errorf("overdue action %+v", a)
	}
	if a := d.Actions[2]; a.Priority != app.P1 || a.Due != "2026-10-14" {
		t.Errorf("an obligation due within a week is P1: %+v", a)
	}
	if a := d.Actions[5]; a.Priority != app.P2 || a.Due != "2026-11-05" {
		t.Errorf("an obligation due within the window is P2: %+v", a)
	}
	if a := d.Actions[4]; a.Title != "2 decisions need review" {
		t.Errorf("review action %q", a.Title)
	}
}

func moneyStrings(ms []fiscal.Money) string {
	s := ""
	for i, m := range ms {
		if i > 0 {
			s += ", "
		}
		s += string(m.Currency()) + " " + m.CanonicalString()
	}
	return s
}
