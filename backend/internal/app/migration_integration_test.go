//go:build integration

package app_test

import (
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/migration"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// Migration against PostgreSQL: a program converts a snapshot AS_KNOWN,
// reconciles it, governs its exceptions and moves its cutover.

// ZTAX-MIG-REQ-0001, -0006, -0014, -0043, -0046, -0092, -0093.
func TestIntegrationMIGREQ0001AProgramConvertsReconcilesAndGovernsItsCutover(t *testing.T) {
	c := openFiscalCell(t)
	svc := app.NewMigrationService(c.store.Migrations(), c.store.LegalEntities(), c.store, c.clock, idgen.V7{})
	admin, other := c.as(security.RoleAdmin), c.as(security.RoleOperator)

	p, err := svc.CreateProgram(admin, app.ProgramInput{Name: "2025 history", Type: "HISTORICAL_DECISION_MIGRATION",
		PeriodFrom: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), PeriodTo: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Scale: 2})
	if err != nil || p.LegalEntity.IsZero() {
		t.Fatalf("program: %v", err)
	}
	snap, err := svc.RegisterSnapshot(admin, p.ID, app.SnapshotInput{SourceSystem: "Incumbent", Method: "FILE",
		ControlTotals: map[string]string{"EUR": "42.00"},
		Records: []migration.SourceRecord{
			{SourceID: "INV-1", DocumentDate: "2025-03-14", Currency: "EUR", Total: "21.00", Components: map[string]string{"VAT19": "21.00"}},
			{SourceID: "INV-2", DocumentDate: "2025-04-02", Currency: "EUR", Total: "21.00", Components: map[string]string{"VAT19": "20.96", "ECO": "0.04"}},
		}})
	if err != nil || snap.Hash.IsZero() {
		t.Fatalf("snapshot: %v", err)
	}
	m1, err := svc.CreateMapping(admin, p.ID, map[string]string{"VAT19": "TAX_VAT"})
	if err != nil {
		t.Fatal(err)
	}
	run1, err := svc.ExecuteRun(admin, p.ID, snap.ID, m1.ID)
	if err != nil || run1.Recon.Passed() || len(run1.Exceptions) != 1 || run1.Exceptions[0].Kind != migration.ExUnmappedCode {
		t.Fatalf("first run: %v %+v", err, run1.Recon)
	}

	// A second mapping version and a rerun make a new run; the first stays
	// (ZTAX-MIG-REQ-0043).
	m2, err := svc.CreateMapping(admin, p.ID, map[string]string{"VAT19": "TAX_VAT", "ECO": "TAX_ECO_LEVY"})
	if err != nil || m2.Version != 2 {
		t.Fatalf("mapping v2: %v %d", err, m2.Version)
	}
	run2, err := svc.ExecuteRun(admin, p.ID, snap.ID, m2.ID)
	if err != nil || !run2.Recon.Passed() || len(run2.Exceptions) != 0 {
		t.Fatalf("second run: %v %+v %v", err, run2.Recon, run2.Exceptions)
	}
	read, err := svc.Run(c.ctx, run2.ID)
	if err != nil || len(read.Records) != 2 || read.Records[0].SourceID != "INV-1" || read.Records[0].CanonicalID.IsZero() {
		t.Fatalf("read back: %v %+v", err, read.Records)
	}
	if _, err := svc.Run(c.ctx, run1.ID); err != nil {
		t.Fatal("the first run was lost")
	}

	// The first run's exception is accepted as a residual, once.
	if _, err := svc.DecideException(admin, run1.Exceptions[0].ID, migration.DispositionResidual, "superseded by v2"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DecideException(admin, run1.Exceptions[0].ID, migration.DispositionResolved, "again"); errs.CategoryOf(err) != errs.CategoryConflict {
		t.Fatalf("an exception was decided twice: %v", err)
	}

	for _, s := range []migration.CutoverState{migration.CutoverPrecheck, migration.CutoverFreeze, migration.CutoverFinalDelta,
		migration.CutoverReconciling, migration.CutoverReady} {
		if _, err := svc.MoveCutover(admin, p.ID, s, "step"); err != nil {
			t.Fatalf("to %s: %v", s, err)
		}
	}
	if _, err := svc.MoveCutover(admin, p.ID, migration.CutoverSwitching, "switch"); errs.ReasonOf(err) != migration.ReasonCutoverBlocked {
		t.Fatalf("the creator switched: %v", err)
	}
	v, err := svc.MoveCutover(other, p.ID, migration.CutoverSwitching, "switch")
	if err != nil || v.CutoverState != migration.CutoverSwitching || len(v.Runs) != 2 {
		t.Fatalf("switch: %v %s", err, v.CutoverState)
	}
}
