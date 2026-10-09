//go:build integration

package app_test

import (
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/legal"
)

// The dashboard's queries against the real store, over what two commits of
// the worked pack recorded: an original and its correction.
func TestIntegrationDashboard(t *testing.T) {
	c := openFiscalCell(t)
	original := c.mustCommit(t, "k-d1", "INV-D/1", "100.00", "3", nil)
	c.mustCommit(t, "k-d2", "INV-D/1", "50.00", "3", &original)

	svc := app.NewDashboardService(c.store.Dashboard(), c.store.LegalEntities(), c.store.Obligations(),
		c.store.Authorizations(), legal.Matrix{}, c.clock)
	d, err := svc.Dashboard(c.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Period != "2026-09" {
		t.Fatalf("period %s, want the clock's month", d.Period)
	}
	if d.Determinations.Decisions != 2 || d.Determinations.PreviousDecisions != 0 {
		t.Errorf("decisions %+v", d.Determinations)
	}
	// The original credited VAT 21.00 + levy 0.11; its correction reversed
	// that (a debit) and credited 10.50 + 0.11. Credits and debits apart.
	if got := moneyStrings(d.Determinations.Tax); got != "EUR 31.72" {
		t.Errorf("tax credited %s, want EUR 31.72", got)
	}
	if len(d.Liability) != 1 || d.Liability[0].Credits.CanonicalString() != "31.72" ||
		d.Liability[0].Debits.CanonicalString() != "21.11" {
		t.Errorf("liability %+v", d.Liability)
	}
	tr := d.Trend
	if tr.Currency != "EUR" || len(tr.Points) != app.DashboardTrendPeriods {
		t.Fatalf("trend %+v", tr)
	}
	if last := tr.Points[len(tr.Points)-1]; last.Period != "2026-09" || last.Decisions != 2 || last.Index != 100 {
		t.Errorf("last trend point %+v", last)
	}
	// The quarter's VAT return is due 2026-10-20, inside the window from
	// 2026-09-25 and not overdue.
	if ob := d.Obligations; ob.Due != 1 || ob.Overdue != 0 || len(ob.Amounts) != 1 || ob.Amounts[0].Currency() != "EUR" {
		t.Errorf("obligations %+v", ob)
	}
	if e := d.Evidence; e.Decisions != 2 || e.Sealed != 0 || e.Seals != 0 {
		t.Errorf("evidence %+v", e)
	}
	if r := d.Reconciliation; r.Runs != 0 || r.Matched != 0 || r.Breaks != 0 || r.Open != 0 {
		t.Errorf("reconciliation %+v", r)
	}
	if len(d.Jurisdictions.Authorities) != 0 {
		t.Errorf("no matrix, yet authorities %+v", d.Jurisdictions.Authorities)
	}
	for _, a := range d.Actions {
		if a.Kind != app.ActionObligationDue {
			t.Errorf("unexpected action %+v", a)
		}
	}
}
