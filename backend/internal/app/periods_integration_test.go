//go:build integration

package app_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// Subledger period close against PostgreSQL. The fiscal cell's events fall
// on 2026-09-24, so every commit posts into legal period 2026-09.

const septemberPeriod = "2026-09"

func (c *fiscalCell) periods(t *testing.T) *app.PeriodService {
	t.Helper()
	c.svc.WithFiscal(app.FiscalStores{
		Accumulators: c.store.Accumulators(), Journals: c.store.Journals(), LegalEntities: c.store.LegalEntities(),
		Outbox: c.store.Outbox(), Obligations: c.store.Obligations(), Periods: c.store.Periods(),
	})
	return app.NewPeriodService(c.store.Periods(), c.store.LegalEntities(), c.store, c.clock, idgen.V7{})
}

// as returns the cell's context acting as a different user holding roles.
func (c *fiscalCell) as(roles ...security.Role) context.Context {
	sc := security.New(c.tenant, id.NewUserID(uuid.Must(uuid.NewV7())), id.NewSessionID(uuid.Must(uuid.NewV7())), roles, time.Now())
	return security.Into(c.ctx, sc)
}

func (c *fiscalCell) move(t *testing.T, svc *app.PeriodService, to subledger.PeriodState) app.PeriodView {
	t.Helper()
	v, err := svc.Transition(c.ctx, septemberPeriod, to, "month end")
	if err != nil {
		t.Fatalf("to %s: %v", to, err)
	}
	return v
}

func manifestJournals(t *testing.T, v app.PeriodView) (journals []string, supersedes string) {
	t.Helper()
	var m struct {
		Journals   []string `json:"journals"`
		Supersedes string   `json:"supersedes"`
	}
	if err := json.Unmarshal(v.ManifestBody, &m); err != nil {
		t.Fatal(err)
	}
	return m.Journals, m.Supersedes
}

func TestIntegrationFINREQ0089To0091APeriodClosesSealsAmendsAndReopensOnApproval(t *testing.T) {
	c := openFiscalCell(t)
	svc := c.periods(t)
	a := c.mustCommit(t, "k-p1", "INV-P1/1", "100.00", "3", nil)

	v, err := svc.Period(c.ctx, septemberPeriod)
	if err != nil || v.State != subledger.PeriodOpen || v.Intact != nil {
		t.Fatalf("an untouched period: %v %+v", err, v)
	}

	// Hard close seals the population (ZTAX-FIN-REQ-0089).
	closed := c.move(t, svc, subledger.PeriodHardClose)
	first := closed.Manifest
	if js, _ := manifestJournals(t, closed); first.IsZero() || len(js) != 1 {
		t.Fatalf("the first manifest: %s %v", first, js)
	}
	if v, _ := svc.Period(c.ctx, septemberPeriod); v.Intact == nil || !*v.Intact {
		t.Fatal("a freshly closed period is not intact")
	}

	// Nothing posts into it by the normal path (ZTAX-FIN-REQ-0090, -0104):
	// neither a new commit nor a correction, whose reversal keeps the period.
	if s, err := c.commit(t, "k-p2", "INV-P2/1", "100.00", "3", nil); err != nil || string(s.Body) != string(errs.ReasonStateTransitionInvalid) {
		t.Fatalf("a commit into a closed period: %v %d %s", err, s.Status, s.Body)
	}
	if s, err := c.commit(t, "k-p1b", "INV-P1/1", "50.00", "3", &a); err != nil || string(s.Body) != string(errs.ReasonStateTransitionInvalid) {
		t.Fatalf("a correction into a closed period: %v %d %s", err, s.Status, s.Body)
	}

	// Inside an amendment window the correction posts, marked an amendment.
	c.move(t, svc, subledger.PeriodAmendmentActive)
	c.mustCommit(t, "k-p1c", "INV-P1/1", "50.00", "3", &a)
	reversals, err := c.store.Journals().BySource(c.ctx, app.SourceDecisionSuperseded, a.String())
	if err != nil || len(reversals) != 1 || !reversals[0].Amendment || reversals[0].LegalPeriod != septemberPeriod {
		t.Fatalf("the amendment's reversal: %v %+v", err, reversals)
	}

	// Closing again writes a second manifest naming the first; the first is kept.
	again := c.move(t, svc, subledger.PeriodHardClose)
	js, supersedes := manifestJournals(t, again)
	if again.Manifest.Equal(first) || supersedes != first.String() || len(js) != 3 {
		t.Fatalf("the second manifest supersedes %q over %d journals", supersedes, len(js))
	}

	// Reopening takes someone else's approval (ZTAX-FIN-REQ-0091).
	if _, err := svc.Transition(c.ctx, septemberPeriod, subledger.PeriodReopened, "x"); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Fatalf("a reopen by transition: %v", err)
	}
	both := c.as(security.RoleOperator, security.RoleAdmin)
	req, err := svc.RequestReopen(both, septemberPeriod, "a late supplier credit")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveReopen(both, septemberPeriod, req.ID); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("a self-approved reopen: %v", err)
	}
	if _, err := svc.ApproveReopen(c.ctx, septemberPeriod, req.ID); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an operator approved a reopen: %v", err)
	}
	approver := c.as(security.RoleAdmin)
	reopened, err := svc.ApproveReopen(approver, septemberPeriod, req.ID)
	if err != nil || reopened.State != subledger.PeriodReopened {
		t.Fatalf("an approved reopen: %v %+v", err, reopened.State)
	}
	last := reopened.History[len(reopened.History)-1]
	if last.Request != req.ID.String() || last.ApprovedBy.IsZero() || last.RecordedBy != req.RequestedBy || !reopened.Manifest.Equal(again.Manifest) {
		t.Fatalf("the reopening event %+v", last)
	}
	if _, err := svc.ApproveReopen(approver, septemberPeriod, req.ID); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("a request approved twice: %v", err)
	}
	c.mustCommit(t, "k-p3", "INV-P3/1", "100.00", "3", nil)

	// Sealed is final.
	c.move(t, svc, subledger.PeriodHardClose)
	c.move(t, svc, subledger.PeriodSealed)
	if _, err := svc.RequestReopen(both, septemberPeriod, "too late"); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("a reopen request on a sealed period: %v", err)
	}
}

// ZTAX-FIN-REQ-0089: a journal that reached a closed period around the guard
// is found.
func TestIntegrationFINREQ0089ATamperedClosedPeriodIsNotIntact(t *testing.T) {
	c := openFiscalCell(t)
	svc := c.periods(t)
	a := c.mustCommit(t, "k-pt1", "INV-PT1/1", "100.00", "3", nil)
	c.move(t, svc, subledger.PeriodHardClose)

	// Straight to the store, as nothing in the application can.
	posted, err := c.store.Journals().BySource(c.ctx, app.SourceDecisionCommitted, a.String())
	if err != nil || len(posted) != 1 {
		t.Fatal(err)
	}
	copied := posted[0]
	copied.ID = id.NewJournalID(uuid.Must(uuid.NewV7()))
	copied.Source.ID = uuid.Must(uuid.NewV7()).String()
	if err := c.store.Journals().Append(c.ctx, copied); err != nil {
		t.Fatal(err)
	}
	v, err := svc.Period(c.ctx, septemberPeriod)
	if err != nil || v.Intact == nil || *v.Intact {
		t.Fatalf("a closed period with a journal it did not seal reads intact: %v %+v", err, v.Intact)
	}
}
