//go:build integration

package app_test

import (
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/shadow"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// Shadow assurance against PostgreSQL: an incumbent's results compared with
// the decisions the fiscal cell commits.

// ZTAX-SHD-REQ-0002, -0014, -0018, -0023, -0030.
func TestIntegrationSHDREQ0002AShadowRunComparesWithoutTouchingDecisions(t *testing.T) {
	c := openFiscalCell(t)
	svc := app.NewShadowService(c.store.Shadow(), c.store.Decisions(), c.files, c.store, c.clock, idgen.V7{})
	analyst := c.as(security.RoleAnalyst)

	c.mustCommit(t, "k-s1", "INV-S1/1", "100.00", "3", nil)
	c.mustCommit(t, "k-s2", "INV-S2/1", "100.00", "3", nil)
	c.mustCommit(t, "k-s3", "INV-S3/1", "100.00", "3", nil)
	before, err := c.store.Decisions().CurrentInWindow(c.ctx, c.event.Add(-time.Hour), c.event.Add(time.Hour))
	if err != nil || len(before) != 3 {
		t.Fatalf("decisions before: %v %d", err, len(before))
	}
	one := c.emittedMoney(t, "INV-S1/1")

	cmp, err := svc.CreateComparator(analyst, app.ComparatorInput{Name: "Incumbent", Type: shadow.ComparatorCommercialEngine, Provider: "Vendor"})
	if err != nil || cmp.Version != "UNKNOWN" {
		t.Fatalf("comparator: %v %s", err, cmp.Version)
	}
	eur := func(s string) fiscal.Money { return fiscaltest.Money(t, s, "EUR") }
	componentMap := map[string]string{}
	for slot := range one {
		componentMap["X-"+slot] = slot
	}
	agree := map[string]fiscal.Money{}
	total := eur("0.00")
	for slot, m := range one {
		agree["X-"+slot] = m
		total, _ = total.Add(m)
	}
	off := map[string]fiscal.Money{}
	for code, m := range agree {
		off[code] = m
	}
	off["X-TAX_VAT"] = eur("99.00")
	offTotal, _ := total.Sub(one["TAX_VAT"])
	offTotal, _ = offTotal.Add(eur("99.00"))

	v, err := svc.ExecuteRun(analyst, app.RunInput{
		Comparator: cmp.ID, Mode: shadow.ModeDualDetermination, WindowFrom: c.event.Add(-time.Hour), WindowTo: c.event.Add(time.Hour),
		Policy: shadow.Policy{Currency: "EUR", ComponentMap: componentMap, Tolerance: eur("0.00"), Materiality: eur("0.01")},
		Observations: []shadow.Observation{
			{CaseRef: "INV-S1/1", Components: agree, Total: total},
			{CaseRef: "INV-S2/1", Components: off, Total: offTotal},
			{CaseRef: "INV-UNKNOWN/1", Components: agree, Total: total},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := v.Metrics
	// Three results, plus INV-S3/1 committed in the window with no result.
	if m.Cases != 4 || m.Paired != 2 || m.Exact != 1 || m.Monetary != 1 || m.Population != 2 || m.Material != 3 {
		t.Fatalf("metrics %+v", m)
	}
	if len(v.Discrepancies) != 3 || v.Claim != shadow.ClaimMatchesComparator || len(v.Blockers) == 0 || v.InputHash.IsZero() {
		t.Fatalf("run %d discrepancies, claim %s, blockers %v", len(v.Discrepancies), v.Claim, v.Blockers)
	}
	read, err := svc.Run(c.ctx, v.ID)
	if err != nil || len(read.Cases) != 4 {
		t.Fatalf("read back: %v %d cases", err, len(read.Cases))
	}

	// Non-impact: the decisions are exactly as they were.
	after, err := c.store.Decisions().CurrentInWindow(c.ctx, c.event.Add(-time.Hour), c.event.Add(time.Hour))
	if err != nil || len(after) != len(before) {
		t.Fatalf("decisions after: %v %d", err, len(after))
	}
	for i := range before {
		if before[i].DecisionID != after[i].DecisionID || before[i].ResultDigest != after[i].ResultDigest {
			t.Fatal("a shadow run changed a committed decision")
		}
	}

	d, err := svc.MoveDiscrepancy(analyst, v.Discrepancies[0].ID, shadow.StateTriaged, shadow.SeverityTwo, "RATE_RULE", "rate differs")
	if err != nil || d.State != shadow.StateTriaged {
		t.Fatalf("triage: %v %s", err, d.State)
	}
}

// emittedMoney reads the money a business key's current decision emitted.
func (c *fiscalCell) emittedMoney(t *testing.T, businessKey string) map[string]fiscal.Money {
	t.Helper()
	history, err := c.store.Decisions().History(c.ctx, businessKey)
	if err != nil || len(history) == 0 {
		t.Fatalf("history of %s: %v", businessKey, err)
	}
	data, err := c.files.Get(c.ctx, history[len(history)-1].ResultDigest)
	if err != nil {
		t.Fatal(err)
	}
	result, err := evidence.DecodeResult(data)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]fiscal.Money{}
	for slot, v := range result.Emitted {
		if v.Type == rule.TypeMoney {
			out[slot] = v.Money
		}
	}
	return out
}
