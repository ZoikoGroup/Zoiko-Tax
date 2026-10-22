package migration_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/migration"
)

// Lane M's migration domain against ZTAX-MIG-001's register. Test names
// carry the requirement id each verifies, so docs/requirements.yaml can point
// at it.

var (
	creator  = id.NewUserID(uuid.MustParse("00000000-0000-7000-8000-000000000004"))
	approver = id.NewUserID(uuid.MustParse("00000000-0000-7000-8000-000000000005"))
	when     = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
)

func program() migration.Program {
	return migration.Program{
		ID: id.NewMigrationProgramID(uuid.New()), Name: "2025 history", Type: "HISTORICAL_DECISION_MIGRATION",
		LegalEntity: id.NewLegalEntityID(uuid.New()), PeriodFrom: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		PeriodTo: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Scale: 2, CreatedBy: creator,
	}
}

func snapshot(t *testing.T, declared string, records ...migration.SourceRecord) migration.Snapshot {
	t.Helper()
	s := migration.Snapshot{SourceSystem: "Incumbent", Method: "FILE", Records: records, ControlTotals: map[string]string{}}
	if declared != "" {
		s.ControlTotals["EUR"] = declared
	}
	if err := s.Seal(); err != nil {
		t.Fatal(err)
	}
	return s
}

func rec(sourceID, date, currency, total string, comps map[string]string) migration.SourceRecord {
	return migration.SourceRecord{SourceID: sourceID, DocumentDate: date, Currency: currency, Total: total, Components: comps}
}

var mapping = migration.Mapping{Version: 1, Codes: map[string]string{"VAT19": "TAX_VAT", "ECO": "TAX_ECO_LEVY"}}

func convert(t *testing.T, p migration.Program, s migration.Snapshot, m migration.Mapping) ([]migration.Converted, []migration.Exception, migration.Reconciliation) {
	t.Helper()
	c, e, r, err := migration.Convert(p, s, m,
		func() (id.MigratedRecordID, error) { return id.NewMigratedRecordID(uuid.New()), nil },
		func() (id.MigrationExceptionID, error) { return id.NewMigrationExceptionID(uuid.New()), nil })
	if err != nil {
		t.Fatal(err)
	}
	return c, e, r
}

func kinds(ex []migration.Exception) map[migration.ExceptionKind]int {
	out := map[migration.ExceptionKind]int{}
	for _, e := range ex {
		out[e.Kind]++
	}
	return out
}

func TestMIGREQ0006To0008ASnapshotIsHashedAndCountedAndOrderFree(t *testing.T) {
	a := rec("A", "2025-03-01", "EUR", "21.00", map[string]string{"VAT19": "21.00"})
	b := rec("B", "2025-04-01", "EUR", "10.50", map[string]string{"VAT19": "10.50"})
	s1, s2 := snapshot(t, "", a, b), snapshot(t, "", b, a)
	if s1.Hash.IsZero() || s1.Count != 2 {
		t.Fatalf("hash %v count %d", s1.Hash, s1.Count)
	}
	if s1.Hash != s2.Hash {
		t.Fatal("the same records in a different order hashed differently")
	}
	b.Total = "10.51"
	if snapshot(t, "", a, b).Hash == s1.Hash {
		t.Fatal("a changed amount did not change the hash")
	}
}

func TestMIGREQ0014To0016And0045TheCrosswalkKeepsTheSourceID(t *testing.T) {
	s := snapshot(t, "", rec("INV-1", "2025-03-01", "EUR", "21.04", map[string]string{"VAT19": "21.00", "ECO": "0.04"}))
	conv, ex, r := convert(t, program(), s, mapping)
	if len(ex) != 0 || len(conv) != 1 {
		t.Fatalf("converted %d, exceptions %v", len(conv), ex)
	}
	c := conv[0]
	if c.SourceID != "INV-1" || c.CanonicalID.IsZero() || c.CanonicalID.String() == "INV-1" || !r.CrosswalkComplete {
		t.Fatalf("crosswalk %+v", c)
	}
}

func TestMIGREQ0029And0026AnUnmappedCodeIsAnExceptionNeverADefault(t *testing.T) {
	s := snapshot(t, "", rec("INV-1", "2025-03-01", "EUR", "21.04", map[string]string{"VAT19": "21.00", "MYSTERY": "0.04"}))
	conv, ex, _ := convert(t, program(), s, mapping)
	if len(conv) != 0 || kinds(ex)[migration.ExUnmappedCode] != 1 {
		t.Fatalf("converted %d, exceptions %v", len(conv), kinds(ex))
	}
}

func TestMIGREQ0037AmountsAreCarriedAsRecorded(t *testing.T) {
	// The source charged 19.00 on 100.00; nothing here knows or applies a
	// rate, and the recorded figure is the converted one.
	s := snapshot(t, "", rec("INV-1", "2025-03-01", "EUR", "19.00", map[string]string{"VAT19": "19.00"}))
	conv, _, _ := convert(t, program(), s, mapping)
	if conv[0].Total.CanonicalString() != "19.00" || conv[0].Components["TAX_VAT"].CanonicalString() != "19.00" {
		t.Fatalf("history was recalculated: %+v", conv[0])
	}
}

func TestMIGREQ0060DatesCurrenciesPrecisionAndDuplicatesAreCaught(t *testing.T) {
	s := snapshot(t, "",
		rec("D", "2025-03-01", "EUR", "1.00", map[string]string{"VAT19": "1.00"}),
		rec("D", "2025-03-02", "EUR", "1.00", map[string]string{"VAT19": "1.00"}),
		rec("X", "2025-03-01", "euro", "1.00", map[string]string{"VAT19": "1.00"}),
		rec("P", "2025-03-01", "EUR", "1.005", map[string]string{"VAT19": "1.005"}),
		rec("T", "2024-12-31", "EUR", "1.00", map[string]string{"VAT19": "1.00"}),
		rec("S", "2025-03-01", "EUR", "2.00", map[string]string{"VAT19": "1.00"}),
	)
	conv, ex, r := convert(t, program(), s, mapping)
	k := kinds(ex)
	if k[migration.ExDuplicateSource] != 1 || k[migration.ExCurrencyConflict] != 1 || k[migration.ExPrecisionLoss] != 1 ||
		k[migration.ExDateConflict] != 1 || k[migration.ExComponentTotal] != 1 || len(conv) != 1 {
		t.Fatalf("converted %d, exceptions %v", len(conv), k)
	}
	if r.ConvertedCount+r.ExceptedCount != r.SourceCount {
		t.Fatalf("counts do not account for every record: %+v", r)
	}
}

func TestMIGREQ0049To0051And0056ReconciliationIsByCountValueAndCrosswalk(t *testing.T) {
	s := snapshot(t, "31.50",
		rec("A", "2025-03-01", "EUR", "21.00", map[string]string{"VAT19": "21.00"}),
		rec("B", "2025-04-01", "EUR", "10.50", map[string]string{"UNKNOWN": "10.50"}),
	)
	_, _, r := convert(t, program(), s, mapping)
	if r.Passed() {
		t.Fatal("a run that left 10.50 unconverted reconciled")
	}
	if len(r.Totals) != 1 || r.Totals[0].Source.CanonicalString() != "31.50" || r.Totals[0].Converted.CanonicalString() != "21.00" {
		t.Fatalf("totals %+v", r.Totals)
	}
	ok := snapshot(t, "21.00", rec("A", "2025-03-01", "EUR", "21.00", map[string]string{"VAT19": "21.00"}))
	if _, _, r := convert(t, program(), ok, mapping); !r.Passed() {
		t.Fatalf("a clean run did not reconcile: %+v", r)
	}
}

func TestMIGREQ0012ASourceThatDisagreesWithItsControlTotalIsSurfaced(t *testing.T) {
	s := snapshot(t, "99.00", rec("A", "2025-03-01", "EUR", "21.00", map[string]string{"VAT19": "21.00"}))
	_, ex, r := convert(t, program(), s, mapping)
	if kinds(ex)[migration.ExControlTotal] != 1 || r.Passed() {
		t.Fatalf("a control-total mismatch was hidden: %v", kinds(ex))
	}
}

func TestMIGREQ0046To0048ExceptionsAreDecidedOnceWithAReason(t *testing.T) {
	e := migration.Exception{Kind: migration.ExUnmappedCode, Severity: migration.SeverityMedium, Disposition: migration.DispositionOpen}
	if _, err := e.Decide(migration.DispositionResidual, "", approver, when); err == nil {
		t.Fatal("a residual was accepted without a reason")
	}
	d, err := e.Decide(migration.DispositionResidual, "levy not migrated", approver, when)
	if err != nil || d.Disposition != migration.DispositionResidual || d.DecidedBy != approver {
		t.Fatalf("%v %+v", err, d)
	}
	if _, err := d.Decide(migration.DispositionResolved, "changed my mind", approver, when); err == nil {
		t.Fatal("a decided exception was decided again")
	}
}

func TestMIGREQ0092And0093And0118TheSwitchNeedsGatesAndAnotherPerson(t *testing.T) {
	p := program()
	var history []migration.CutoverEvent
	step := func(to migration.CutoverState, r migration.Readiness, by id.UserID) error {
		e, err := migration.NextCutover(history, to, p, r, "go", by, when)
		if err == nil {
			history = append(history, e)
		}
		return err
	}
	ready := migration.Readiness{HasRun: true, Reconciled: true}
	for _, s := range []migration.CutoverState{migration.CutoverPrecheck, migration.CutoverFreeze, migration.CutoverFinalDelta, migration.CutoverReconciling} {
		if err := step(s, ready, creator); err != nil {
			t.Fatal(err)
		}
	}
	blocked := migration.Readiness{HasRun: true, Reconciled: true, OpenHigh: 1}
	if err := step(migration.CutoverReady, blocked, creator); errs.ReasonOf(err) != migration.ReasonCutoverBlocked {
		t.Fatalf("an open high-severity exception did not block: %v", err)
	}
	if err := step(migration.CutoverReady, ready, creator); err != nil {
		t.Fatal(err)
	}
	if err := step(migration.CutoverSwitching, ready, creator); errs.ReasonOf(err) != migration.ReasonCutoverBlocked {
		t.Fatalf("the program's creator switched: %v", err)
	}
	if err := step(migration.CutoverSwitching, ready, approver); err != nil {
		t.Fatal(err)
	}
	if migration.CanCutover(migration.CutoverSwitching, migration.CutoverAborted) {
		t.Fatal("abort after the switch is allowed; it is a rollback")
	}
}
