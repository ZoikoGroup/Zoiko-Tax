package http

import (
	"net/http"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/migration"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The migration surface (ZTAX-MIG-001 §33, W4 lane M).

func (rt *Router) migrationUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Migration != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"Migration is not wired in this cell. The request was not applied."))
	return true
}

func toMigrationProgram(p migration.Program) gen.MigrationProgram {
	return gen.MigrationProgram{ID: p.ID.String(), Name: p.Name, Type: gen.MigrationType(p.Type), LegalEntityID: p.LegalEntity.String(),
		PeriodFrom: p.PeriodFrom.Format(time.DateOnly), PeriodTo: p.PeriodTo.Format(time.DateOnly), Scale: saturate32(p.Scale),
		CreatedAt: canonical.FormatTime(p.CreatedAt), CreatedBy: p.CreatedBy.String()}
}

func toMigrationSnapshot(s migration.Snapshot) gen.MigrationSnapshot {
	out := gen.MigrationSnapshot{ID: s.ID.String(), ProgramID: s.Program.String(), SourceSystem: s.SourceSystem,
		SourceVersion: optStr(s.SourceVersion), Method: gen.ExtractionMethod(s.Method), Watermark: optStr(s.Watermark),
		RecordCount: saturate32(s.Count), Hash: s.Hash.String(), ControlTotals: []gen.ControlTotal{},
		CreatedAt: canonical.FormatTime(s.CreatedAt), CreatedBy: s.CreatedBy.String()}
	curs := make([]string, 0, len(s.ControlTotals))
	for c := range s.ControlTotals {
		curs = append(curs, c)
	}
	sort.Strings(curs)
	for _, c := range curs {
		out.ControlTotals = append(out.ControlTotals, gen.ControlTotal{Currency: c, Total: s.ControlTotals[c]})
	}
	return out
}

func toMigrationMapping(m migration.Mapping) gen.MigrationMapping {
	out := gen.MigrationMapping{ID: m.ID.String(), ProgramID: m.Program.String(), Version: saturate32(m.Version),
		Codes: []gen.CodeMapping{}, CreatedAt: canonical.FormatTime(m.CreatedAt), CreatedBy: m.CreatedBy.String()}
	codes := make([]string, 0, len(m.Codes))
	for c := range m.Codes {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, c := range codes {
		out.Codes = append(out.Codes, gen.CodeMapping{LegacyCode: c, Slot: m.Codes[c]})
	}
	return out
}

func toMigrationException(e migration.Exception) gen.MigrationException {
	out := gen.MigrationException{ID: e.ID.String(), Seq: saturate32(e.Seq), Kind: gen.MigrationExceptionKind(e.Kind),
		Severity: gen.MigrationSeverity(e.Severity), SourceID: optStr(e.SourceID), Detail: e.Detail,
		Disposition: gen.MigrationDisposition(e.Disposition), Note: optStr(e.Note)}
	if !e.DecidedAt.IsZero() {
		at, by := canonical.FormatTime(e.DecidedAt), e.DecidedBy.String()
		out.DecidedAt, out.DecidedBy = &at, &by
	}
	return out
}

func toMigrationRun(r migration.Run) gen.MigrationRun {
	out := gen.MigrationRun{ID: r.ID.String(), ProgramID: r.Program.String(), SnapshotID: r.Snapshot.String(), MappingID: r.Mapping.String(),
		Mode: gen.MigrationRunMode(r.Mode), Exceptions: make([]gen.MigrationException, len(r.Exceptions)),
		Reconciliation: gen.MigrationReconciliation{SourceCount: saturate32(r.Recon.SourceCount), ConvertedCount: saturate32(r.Recon.ConvertedCount),
			ExceptedCount: saturate32(r.Recon.ExceptedCount), CrosswalkComplete: r.Recon.CrosswalkComplete, Passed: r.Recon.Passed(),
			Totals: make([]gen.MigrationCurrencyTotal, len(r.Recon.Totals))},
		CreatedAt: canonical.FormatTime(r.CreatedAt), CreatedBy: r.CreatedBy.String()}
	for i, t := range r.Recon.Totals {
		out.Reconciliation.Totals[i] = gen.MigrationCurrencyTotal{Currency: t.Currency, Declared: decimalOf(t.Declared),
			Source: t.Source.CanonicalString(), Converted: t.Converted.CanonicalString(), Matches: t.Matches}
	}
	for i, e := range r.Exceptions {
		out.Exceptions[i] = toMigrationException(e)
	}
	if r.Records != nil {
		out.Records = make([]gen.MigratedRecord, len(r.Records))
		for i, c := range r.Records {
			rec := gen.MigratedRecord{Seq: saturate32(c.Seq), SourceID: c.SourceID, CanonicalID: c.CanonicalID.String(),
				DocumentDate: c.DocumentDate.Format(time.DateOnly), Currency: string(c.Total.Currency()), Total: c.Total.CanonicalString(),
				Components: []gen.MigratedComponent{}}
			slots := make([]string, 0, len(c.Components))
			for s := range c.Components {
				slots = append(slots, s)
			}
			sort.Strings(slots)
			for _, s := range slots {
				rec.Components = append(rec.Components, gen.MigratedComponent{Slot: s, Amount: c.Components[s].CanonicalString()})
			}
			out.Records[i] = rec
		}
	}
	return out
}

func toProgramDetail(v app.ProgramView) gen.MigrationProgramDetail {
	out := gen.MigrationProgramDetail{Program: toMigrationProgram(v.Program), CutoverState: gen.CutoverState(v.CutoverState),
		Snapshots: make([]gen.MigrationSnapshot, len(v.Snapshots)), Mappings: make([]gen.MigrationMapping, len(v.Mappings)),
		Runs: make([]gen.MigrationRun, len(v.Runs)), Cutover: make([]gen.CutoverEvent, len(v.Cutover)),
		Readiness: gen.MigrationReadiness{HasRun: v.Readiness.HasRun, Reconciled: v.Readiness.Reconciled,
			OpenHigh: saturate32(v.Readiness.OpenHigh), OpenOther: saturate32(v.Readiness.OpenOther),
			Residuals: saturate32(v.Readiness.Residuals), Blockers: append([]string{}, v.Blockers...)}}
	for i, s := range v.Snapshots {
		out.Snapshots[i] = toMigrationSnapshot(s)
	}
	for i, m := range v.Mappings {
		out.Mappings[i] = toMigrationMapping(m)
	}
	for i, r := range v.Runs {
		out.Runs[i] = toMigrationRun(r)
	}
	for i, e := range v.Cutover {
		out.Cutover[i] = gen.CutoverEvent{Seq: saturate32(e.Seq), State: gen.CutoverState(e.State), Note: e.Note,
			RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String()}
	}
	return out
}

func (rt *Router) migrationProgramID(w http.ResponseWriter, r *http.Request) (id.MigrationProgramID, bool) {
	pid, err := id.ParseMigrationProgramID(r.PathValue("migrationProgramId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("migrationProgramId", errs.ReasonInvalidValue, "That is not a valid migration program identifier."))
		return id.MigrationProgramID{}, false
	}
	return pid, true
}

func (rt *Router) handleListMigrationPrograms(w http.ResponseWriter, r *http.Request) {
	if rt.migrationUnavailable(w, r) {
		return
	}
	got, err := rt.Migration.Programs(r.Context())
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.MigrationProgramList{Programs: make([]gen.MigrationProgram, len(got))}
	for i, p := range got {
		out.Programs[i] = toMigrationProgram(p)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleCreateMigrationProgram(w http.ResponseWriter, r *http.Request) {
	if rt.migrationUnavailable(w, r) {
		return
	}
	var req gen.MigrationProgramRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	from, err1 := time.Parse(time.DateOnly, req.PeriodFrom)
	to, err2 := time.Parse(time.DateOnly, req.PeriodTo)
	if err1 != nil || err2 != nil {
		writeProblem(w, r, rt.log, errs.Invalid("periodFrom", errs.ReasonInvalidValue, "The period is two calendar dates, YYYY-MM-DD."))
		return
	}
	in := app.ProgramInput{Name: req.Name, Type: migration.Type(req.Type), PeriodFrom: from, PeriodTo: to, Scale: int(req.Scale)}
	if req.LegalEntityID != nil {
		le, err := id.ParseLegalEntityID(*req.LegalEntityID)
		if err != nil {
			writeProblem(w, r, rt.log, errs.Invalid("legalEntityId", errs.ReasonInvalidValue, "That is not a valid legal entity identifier."))
			return
		}
		in.LegalEntity = le
	}
	p, err := rt.Migration.CreateProgram(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toMigrationProgram(p))
}

func (rt *Router) handleGetMigrationProgram(w http.ResponseWriter, r *http.Request) {
	if rt.migrationUnavailable(w, r) {
		return
	}
	pid, ok := rt.migrationProgramID(w, r)
	if !ok {
		return
	}
	v, err := rt.Migration.Program(r.Context(), pid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toProgramDetail(v))
}

func (rt *Router) handleRegisterMigrationSnapshot(w http.ResponseWriter, r *http.Request) {
	if rt.migrationUnavailable(w, r) {
		return
	}
	pid, ok := rt.migrationProgramID(w, r)
	if !ok {
		return
	}
	var req gen.MigrationSnapshotRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.SnapshotInput{SourceSystem: req.SourceSystem, SourceVersion: deref(req.SourceVersion), Method: string(req.Method),
		Watermark: deref(req.Watermark), ControlTotals: map[string]string{}}
	for _, t := range req.ControlTotals {
		if _, dup := in.ControlTotals[t.Currency]; dup {
			writeProblem(w, r, rt.log, errs.Invalid("controlTotals", errs.ReasonInvalidValue, "A control total is declared once per currency."))
			return
		}
		in.ControlTotals[t.Currency] = t.Total
	}
	for _, rec := range req.Records {
		sr := migration.SourceRecord{SourceID: rec.SourceID, DocumentDate: rec.DocumentDate, Currency: rec.Currency, Total: rec.Total,
			Components: map[string]string{}}
		for _, c := range rec.Components {
			if _, dup := sr.Components[c.Code]; dup {
				writeProblem(w, r, rt.log, errs.Invalid("records.components", errs.ReasonInvalidValue, "A source record names each code once."))
				return
			}
			sr.Components[c.Code] = c.Amount
		}
		in.Records = append(in.Records, sr)
	}
	s, err := rt.Migration.RegisterSnapshot(r.Context(), pid, in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toMigrationSnapshot(s))
}

func (rt *Router) handleCreateMigrationMapping(w http.ResponseWriter, r *http.Request) {
	if rt.migrationUnavailable(w, r) {
		return
	}
	pid, ok := rt.migrationProgramID(w, r)
	if !ok {
		return
	}
	var req gen.MigrationMappingRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	codes := map[string]string{}
	for _, c := range req.Codes {
		if _, dup := codes[c.LegacyCode]; dup {
			writeProblem(w, r, rt.log, errs.Invalid("codes", errs.ReasonInvalidValue, "A legacy code is mapped once."))
			return
		}
		codes[c.LegacyCode] = c.Slot
	}
	m, err := rt.Migration.CreateMapping(r.Context(), pid, codes)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toMigrationMapping(m))
}

func (rt *Router) handleExecuteMigrationRun(w http.ResponseWriter, r *http.Request) {
	if rt.migrationUnavailable(w, r) {
		return
	}
	pid, ok := rt.migrationProgramID(w, r)
	if !ok {
		return
	}
	var req gen.MigrationRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	sid, err1 := id.ParseMigrationSnapshotID(req.SnapshotID)
	mid, err2 := id.ParseMigrationMappingID(req.MappingID)
	if err1 != nil || err2 != nil {
		writeProblem(w, r, rt.log, errs.Invalid("snapshotId", errs.ReasonInvalidValue, "A run names a valid snapshot and mapping."))
		return
	}
	run, err := rt.Migration.ExecuteRun(r.Context(), pid, sid, mid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toMigrationRun(run))
}

func (rt *Router) handleMoveMigrationCutover(w http.ResponseWriter, r *http.Request) {
	if rt.migrationUnavailable(w, r) {
		return
	}
	pid, ok := rt.migrationProgramID(w, r)
	if !ok {
		return
	}
	var req gen.CutoverRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Migration.MoveCutover(r.Context(), pid, migration.CutoverState(req.To), req.Note)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toProgramDetail(v))
}

func (rt *Router) handleGetMigrationRun(w http.ResponseWriter, r *http.Request) {
	if rt.migrationUnavailable(w, r) {
		return
	}
	rid, err := id.ParseMigrationRunID(r.PathValue("migrationRunId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("migrationRunId", errs.ReasonInvalidValue, "That is not a valid migration run identifier."))
		return
	}
	run, err := rt.Migration.Run(r.Context(), rid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if run.Records == nil {
		run.Records = []migration.Converted{}
	}
	writeJSON(w, r, rt.log, http.StatusOK, toMigrationRun(run))
}

func (rt *Router) handleDecideMigrationException(w http.ResponseWriter, r *http.Request) {
	if rt.migrationUnavailable(w, r) {
		return
	}
	eid, err := id.ParseMigrationExceptionID(r.PathValue("migrationExceptionId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("migrationExceptionId", errs.ReasonInvalidValue, "That is not a valid migration exception identifier."))
		return
	}
	var req gen.ExceptionDecisionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	e, err := rt.Migration.DecideException(r.Context(), eid, migration.Disposition(req.Disposition), req.Note)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toMigrationException(e))
}
