package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/migration"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// migration_* — migration 000025
// ---------------------------------------------------------------------------

const (
	sqlMigProgramColumns = `program_id, name, migration_type, legal_entity_id, period_from, period_to, amount_scale, created_at, created_by`
	sqlMigProgramInsert  = `INSERT INTO ztax.migration_program (tenant_id, ` + sqlMigProgramColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	sqlMigProgramSelect = `SELECT ` + sqlMigProgramColumns + ` FROM ztax.migration_program WHERE tenant_id = $1`

	sqlMigSnapshotInsert = `INSERT INTO ztax.migration_snapshot (tenant_id, snapshot_id, program_id, source_system,
		source_version, method, watermark, records, record_count, control_totals, snapshot_hash, created_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`
	sqlMigSnapshotSelect = `SELECT snapshot_id, program_id, source_system, source_version, method, watermark, %s,
		record_count, control_totals, snapshot_hash, created_at, created_by FROM ztax.migration_snapshot WHERE tenant_id = $1`

	sqlMigMappingInsert = `INSERT INTO ztax.migration_mapping (tenant_id, mapping_id, program_id, version, codes, created_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
	sqlMigMappingSelect = `SELECT mapping_id, program_id, version, codes, created_at, created_by FROM ztax.migration_mapping WHERE tenant_id = $1`

	sqlMigRunInsert = `INSERT INTO ztax.migration_run (tenant_id, run_id, program_id, snapshot_id, mapping_id, mode,
		source_count, converted_count, excepted_count, crosswalk_complete, totals, created_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`
	sqlMigRunSelect = `SELECT run_id, program_id, snapshot_id, mapping_id, mode, source_count, converted_count, excepted_count,
		crosswalk_complete, totals, created_at, created_by FROM ztax.migration_run WHERE tenant_id = $1`
	sqlMigRecordInsert = `INSERT INTO ztax.migration_record (tenant_id, run_id, seq, source_id, canonical_id, document_date,
		currency, total, components) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	sqlMigRecordSelect = `SELECT seq, source_id, canonical_id, document_date, currency, total, components
		FROM ztax.migration_record WHERE tenant_id = $1 AND run_id = $2 ORDER BY seq`
	sqlMigExceptionInsert = `INSERT INTO ztax.migration_exception (tenant_id, exception_id, run_id, seq, kind, severity, source_id, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	sqlMigExceptionSelect = `SELECT e.exception_id, e.run_id, e.seq, e.kind, e.severity, e.source_id, e.detail,
		d.disposition, d.note, d.decided_at, d.decided_by
		FROM ztax.migration_exception e LEFT JOIN ztax.migration_exception_decision d
		  ON d.tenant_id = e.tenant_id AND d.exception_id = e.exception_id
		WHERE e.tenant_id = $1`
	sqlMigDecisionInsert = `INSERT INTO ztax.migration_exception_decision (tenant_id, exception_id, disposition, note, decided_at, decided_by)
		VALUES ($1, $2, $3, $4, $5, $6)`
	sqlMigCutoverInsert = `INSERT INTO ztax.migration_cutover_event (tenant_id, program_id, seq, state, note, recorded_at, recorded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
	sqlMigCutoverSelect = `SELECT seq, state, note, recorded_at, recorded_by FROM ztax.migration_cutover_event
		WHERE tenant_id = $1 AND program_id = $2 ORDER BY seq`
	sqlMigLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
)

// MigrationRepo implements port.MigrationRepository.
type MigrationRepo struct{ s *Store }

// Migrations returns the migration repository.
func (s *Store) Migrations() *MigrationRepo { return &MigrationRepo{s: s} }

var _ port.MigrationRepository = (*MigrationRepo)(nil)

// Lock takes one of the tenant's migration locks for the transaction.
func (r *MigrationRepo) Lock(ctx context.Context, key string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlMigLock, tenant.String()+"/migration/"+key)
	return mapError(err, "lock migration")
}

// CreateProgram records a program.
func (r *MigrationRepo) CreateProgram(ctx context.Context, p migration.Program) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if p.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The program belongs to a tenant outside the caller's scope.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlMigProgramInsert, tenant.UUID(), p.ID.UUID(), p.Name, string(p.Type), p.LegalEntity.UUID(),
		civilDate(p.PeriodFrom), civilDate(p.PeriodTo), p.Scale, p.CreatedAt.UTC(), p.CreatedBy.UUID())
	return mapError(err, "insert migration program")
}

// Program returns one program.
func (r *MigrationRepo) Program(ctx context.Context, programID id.MigrationProgramID) (migration.Program, error) {
	got, err := r.programs(ctx, ` AND program_id = $2`, programID.UUID())
	if err != nil {
		return migration.Program{}, err
	}
	if len(got) == 0 {
		return migration.Program{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such migration program.")
	}
	return got[0], nil
}

// Programs returns the tenant's programs, newest first.
func (r *MigrationRepo) Programs(ctx context.Context) ([]migration.Program, error) {
	return r.programs(ctx, ``, nil)
}

func (r *MigrationRepo) programs(ctx context.Context, where string, arg any) ([]migration.Program, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	args := []any{tenant.UUID()}
	if arg != nil {
		args = append(args, arg)
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlMigProgramSelect+where+` ORDER BY created_at DESC, program_id DESC`, args...)
	if err != nil {
		return nil, mapError(err, "read migration programs")
	}
	defer rows.Close()
	var out []migration.Program
	for rows.Next() {
		var (
			p           = migration.Program{TenantID: tenant}
			pid, le, by uuid.UUID
			typ         string
			from, to    pgtype.Date
		)
		if err := rows.Scan(&pid, &p.Name, &typ, &le, &from, &to, &p.Scale, &p.CreatedAt, &by); err != nil {
			return nil, mapError(err, "scan migration program")
		}
		p.ID, p.Type, p.LegalEntity, p.CreatedBy = id.NewMigrationProgramID(pid), migration.Type(typ), id.NewLegalEntityID(le), id.NewUserID(by)
		p.PeriodFrom, p.PeriodTo, p.CreatedAt = inLocation(from, time.UTC), inLocation(to, time.UTC), p.CreatedAt.UTC()
		out = append(out, p)
	}
	return out, mapError(rows.Err(), "read migration programs")
}

// CreateSnapshot records an immutable snapshot.
func (r *MigrationRepo) CreateSnapshot(ctx context.Context, s migration.Snapshot) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	records, err := json.Marshal(s.Records)
	if err != nil {
		return obligationFault(err, "The snapshot's records could not be stored.")
	}
	totals := s.ControlTotals
	if totals == nil {
		totals = map[string]string{}
	}
	ct, err := json.Marshal(totals)
	if err != nil {
		return obligationFault(err, "The snapshot's control totals could not be stored.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlMigSnapshotInsert, tenant.UUID(), s.ID.UUID(), s.Program.UUID(), s.SourceSystem,
		optString(s.SourceVersion), s.Method, optString(s.Watermark), records, s.Count, ct, s.Hash.String(), s.CreatedAt.UTC(), s.CreatedBy.UUID())
	return mapError(err, "insert migration snapshot")
}

// Snapshot returns one snapshot with its records.
func (r *MigrationRepo) Snapshot(ctx context.Context, snapshotID id.MigrationSnapshotID) (migration.Snapshot, error) {
	got, err := r.snapshots(ctx, true, ` AND snapshot_id = $2`, snapshotID.UUID())
	if err != nil {
		return migration.Snapshot{}, err
	}
	if len(got) == 0 {
		return migration.Snapshot{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such snapshot.")
	}
	return got[0], nil
}

// Snapshots returns a program's snapshots without their records.
func (r *MigrationRepo) Snapshots(ctx context.Context, programID id.MigrationProgramID) ([]migration.Snapshot, error) {
	return r.snapshots(ctx, false, ` AND program_id = $2`, programID.UUID())
}

func (r *MigrationRepo) snapshots(ctx context.Context, withRecords bool, where string, arg any) ([]migration.Snapshot, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	col := `'[]'::jsonb`
	if withRecords {
		col = `records`
	}
	rows, err := r.s.db(ctx).Query(ctx, fmt.Sprintf(sqlMigSnapshotSelect, col)+where+` ORDER BY created_at DESC`, tenant.UUID(), arg)
	if err != nil {
		return nil, mapError(err, "read migration snapshots")
	}
	defer rows.Close()
	var out []migration.Snapshot
	for rows.Next() {
		var (
			s               = migration.Snapshot{TenantID: tenant}
			sid, pid, by    uuid.UUID
			version, mark   *string
			records, totals []byte
			hash            string
		)
		if err := rows.Scan(&sid, &pid, &s.SourceSystem, &version, &s.Method, &mark, &records, &s.Count, &totals, &hash,
			&s.CreatedAt, &by); err != nil {
			return nil, mapError(err, "scan migration snapshot")
		}
		s.ID, s.Program, s.CreatedBy, s.CreatedAt = id.NewMigrationSnapshotID(sid), id.NewMigrationProgramID(pid), id.NewUserID(by), s.CreatedAt.UTC()
		s.SourceVersion, s.Watermark = deref(version), deref(mark)
		if err := json.Unmarshal(records, &s.Records); err != nil {
			return nil, obligationFault(err, "A stored snapshot's records do not decode.")
		}
		if len(s.Records) == 0 {
			s.Records = nil
		}
		if err := json.Unmarshal(totals, &s.ControlTotals); err != nil {
			return nil, obligationFault(err, "A stored snapshot's control totals do not decode.")
		}
		if s.Hash, err = canonical.ParseDigest(hash); err != nil {
			return nil, obligationFault(err, "A stored snapshot's hash does not parse.")
		}
		out = append(out, s)
	}
	return out, mapError(rows.Err(), "read migration snapshots")
}

// CreateMapping records a mapping version.
func (r *MigrationRepo) CreateMapping(ctx context.Context, m migration.Mapping) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	codes, err := json.Marshal(m.Codes)
	if err != nil {
		return obligationFault(err, "The mapping could not be stored.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlMigMappingInsert, tenant.UUID(), m.ID.UUID(), m.Program.UUID(), m.Version, codes,
		m.CreatedAt.UTC(), m.CreatedBy.UUID())
	return mapError(err, "insert migration mapping")
}

// Mapping returns one mapping version.
func (r *MigrationRepo) Mapping(ctx context.Context, mappingID id.MigrationMappingID) (migration.Mapping, error) {
	got, err := r.mappings(ctx, ` AND mapping_id = $2`, mappingID.UUID())
	if err != nil {
		return migration.Mapping{}, err
	}
	if len(got) == 0 {
		return migration.Mapping{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such mapping.")
	}
	return got[0], nil
}

// Mappings returns a program's mapping versions, newest first.
func (r *MigrationRepo) Mappings(ctx context.Context, programID id.MigrationProgramID) ([]migration.Mapping, error) {
	return r.mappings(ctx, ` AND program_id = $2`, programID.UUID())
}

func (r *MigrationRepo) mappings(ctx context.Context, where string, arg any) ([]migration.Mapping, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlMigMappingSelect+where+` ORDER BY version DESC`, tenant.UUID(), arg)
	if err != nil {
		return nil, mapError(err, "read migration mappings")
	}
	defer rows.Close()
	var out []migration.Mapping
	for rows.Next() {
		var (
			m            = migration.Mapping{TenantID: tenant}
			mid, pid, by uuid.UUID
			codes        []byte
		)
		if err := rows.Scan(&mid, &pid, &m.Version, &codes, &m.CreatedAt, &by); err != nil {
			return nil, mapError(err, "scan migration mapping")
		}
		m.ID, m.Program, m.CreatedBy, m.CreatedAt = id.NewMigrationMappingID(mid), id.NewMigrationProgramID(pid), id.NewUserID(by), m.CreatedAt.UTC()
		if err := json.Unmarshal(codes, &m.Codes); err != nil {
			return nil, obligationFault(err, "A stored mapping does not decode.")
		}
		out = append(out, m)
	}
	return out, mapError(rows.Err(), "read migration mappings")
}

// totalWire is a reconciled currency total, stored as decimal strings.
type totalWire struct {
	Currency  string  `json:"currency"`
	Declared  *string `json:"declared,omitempty"`
	Source    string  `json:"source"`
	Converted string  `json:"converted"`
	Matches   bool    `json:"matches"`
}

// CreateRun records a run, its records and its exceptions.
func (r *MigrationRepo) CreateRun(ctx context.Context, run migration.Run) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	wire := make([]totalWire, len(run.Recon.Totals))
	for i, t := range run.Recon.Totals {
		wire[i] = totalWire{Currency: t.Currency, Declared: moneyString(t.Declared), Source: t.Source.CanonicalString(),
			Converted: t.Converted.CanonicalString(), Matches: t.Matches}
	}
	totals, err := json.Marshal(wire)
	if err != nil {
		return obligationFault(err, "The run's totals could not be stored.")
	}
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlMigRunInsert, tenant.UUID(), run.ID.UUID(), run.Program.UUID(), run.Snapshot.UUID(), run.Mapping.UUID(),
		run.Mode, run.Recon.SourceCount, run.Recon.ConvertedCount, run.Recon.ExceptedCount, run.Recon.CrosswalkComplete, totals,
		run.CreatedAt.UTC(), run.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert migration run")
	}
	for _, c := range run.Records {
		comps := map[string]string{}
		for slot, m := range c.Components {
			comps[slot] = m.CanonicalString()
		}
		body, err := json.Marshal(comps)
		if err != nil {
			return obligationFault(err, "A record's components could not be stored.")
		}
		total, err := MoneyValue(c.Total)
		if err != nil {
			return err
		}
		if _, err := db.Exec(ctx, sqlMigRecordInsert, tenant.UUID(), run.ID.UUID(), c.Seq, c.SourceID, c.CanonicalID.UUID(),
			civilDate(c.DocumentDate), string(c.Total.Currency()), total, body); err != nil {
			return mapError(err, "insert migration record")
		}
	}
	for _, e := range run.Exceptions {
		if _, err := db.Exec(ctx, sqlMigExceptionInsert, tenant.UUID(), e.ID.UUID(), run.ID.UUID(), e.Seq, string(e.Kind),
			string(e.Severity), optString(e.SourceID), e.Detail); err != nil {
			return mapError(err, "insert migration exception")
		}
	}
	return nil
}

// Run returns one run with its records and exceptions.
func (r *MigrationRepo) Run(ctx context.Context, runID id.MigrationRunID) (migration.Run, error) {
	got, err := r.runs(ctx, ` AND run_id = $2`, runID.UUID())
	if err != nil {
		return migration.Run{}, err
	}
	if len(got) == 0 {
		return migration.Run{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such migration run.")
	}
	run := got[0]
	tenant, _ := tenantOf(ctx)
	rows, err := r.s.db(ctx).Query(ctx, sqlMigRecordSelect, tenant.UUID(), runID.UUID())
	if err != nil {
		return migration.Run{}, mapError(err, "read migration records")
	}
	defer rows.Close()
	for rows.Next() {
		var (
			c        migration.Converted
			cid      uuid.UUID
			date     pgtype.Date
			currency string
			total    Numeric
			body     []byte
		)
		if err := rows.Scan(&c.Seq, &c.SourceID, &cid, &date, &currency, &total, &body); err != nil {
			return migration.Run{}, mapError(err, "scan migration record")
		}
		cur := fiscal.Currency(currency)
		c.CanonicalID, c.DocumentDate = id.NewMigratedRecordID(cid), inLocation(date, time.UTC)
		if c.Total, err = total.Money(cur); err != nil {
			return migration.Run{}, err
		}
		var comps map[string]string
		if err := json.Unmarshal(body, &comps); err != nil {
			return migration.Run{}, obligationFault(err, "A stored record's components do not decode.")
		}
		c.Components = map[string]fiscal.Money{}
		for slot, s := range comps {
			m, err := fiscal.ParseMoney(s, cur)
			if err != nil {
				return migration.Run{}, obligationFault(err, "A stored component amount does not parse.")
			}
			c.Components[slot] = m
		}
		run.Records = append(run.Records, c)
	}
	return run, mapError(rows.Err(), "read migration records")
}

// Runs returns a program's runs, newest first, without records.
func (r *MigrationRepo) Runs(ctx context.Context, programID id.MigrationProgramID) ([]migration.Run, error) {
	return r.runs(ctx, ` AND program_id = $2`, programID.UUID())
}

func (r *MigrationRepo) runs(ctx context.Context, where string, arg any) ([]migration.Run, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, sqlMigRunSelect+where+` ORDER BY created_at DESC, run_id DESC`, tenant.UUID(), arg)
	if err != nil {
		return nil, mapError(err, "read migration runs")
	}
	var out []migration.Run
	for rows.Next() {
		var (
			run                    = migration.Run{TenantID: tenant}
			rid, pid, sid, mid, by uuid.UUID
			totals                 []byte
		)
		if err := rows.Scan(&rid, &pid, &sid, &mid, &run.Mode, &run.Recon.SourceCount, &run.Recon.ConvertedCount,
			&run.Recon.ExceptedCount, &run.Recon.CrosswalkComplete, &totals, &run.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan migration run")
		}
		run.ID, run.Program, run.Snapshot, run.Mapping = id.NewMigrationRunID(rid), id.NewMigrationProgramID(pid),
			id.NewMigrationSnapshotID(sid), id.NewMigrationMappingID(mid)
		run.CreatedBy, run.CreatedAt = id.NewUserID(by), run.CreatedAt.UTC()
		var wire []totalWire
		if err := json.Unmarshal(totals, &wire); err != nil {
			rows.Close()
			return nil, obligationFault(err, "A stored run's totals do not decode.")
		}
		for _, w := range wire {
			cur := fiscal.Currency(w.Currency)
			t := migration.CurrencyTotal{Currency: w.Currency, Matches: w.Matches}
			if t.Source, err = fiscal.ParseMoney(w.Source, cur); err != nil {
				rows.Close()
				return nil, obligationFault(err, "A stored total does not parse.")
			}
			if t.Converted, err = fiscal.ParseMoney(w.Converted, cur); err != nil {
				rows.Close()
				return nil, obligationFault(err, "A stored total does not parse.")
			}
			if w.Declared != nil {
				d, err := fiscal.ParseMoney(*w.Declared, cur)
				if err != nil {
					rows.Close()
					return nil, obligationFault(err, "A stored total does not parse.")
				}
				t.Declared = &d
			}
			run.Recon.Totals = append(run.Recon.Totals, t)
		}
		out = append(out, run)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read migration runs")
	}
	for i := range out {
		exs, err := r.exceptions(ctx, ` AND e.run_id = $2 ORDER BY e.seq`, out[i].ID.UUID())
		if err != nil {
			return nil, err
		}
		for _, e := range exs {
			out[i].Exceptions = append(out[i].Exceptions, e.ex)
		}
	}
	return out, nil
}

type exceptionRow struct {
	ex  migration.Exception
	run id.MigrationRunID
}

func (r *MigrationRepo) exceptions(ctx context.Context, where string, arg any) ([]exceptionRow, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlMigExceptionSelect+where, tenant.UUID(), arg)
	if err != nil {
		return nil, mapError(err, "read migration exceptions")
	}
	defer rows.Close()
	var out []exceptionRow
	for rows.Next() {
		var (
			e                  migration.Exception
			eid, rid           uuid.UUID
			kind, severity     string
			source, disp, note *string
			at                 *time.Time
			by                 *uuid.UUID
		)
		if err := rows.Scan(&eid, &rid, &e.Seq, &kind, &severity, &source, &e.Detail, &disp, &note, &at, &by); err != nil {
			return nil, mapError(err, "scan migration exception")
		}
		e.ID, e.Kind, e.Severity, e.SourceID = id.NewMigrationExceptionID(eid), migration.ExceptionKind(kind), migration.Severity(severity), deref(source)
		e.Disposition = migration.DispositionOpen
		if disp != nil {
			e.Disposition, e.Note = migration.Disposition(*disp), deref(note)
		}
		if at != nil {
			e.DecidedAt = at.UTC()
		}
		if by != nil {
			e.DecidedBy = id.NewUserID(*by)
		}
		out = append(out, exceptionRow{ex: e, run: id.NewMigrationRunID(rid)})
	}
	return out, mapError(rows.Err(), "read migration exceptions")
}

// Exception returns one exception and its run.
func (r *MigrationRepo) Exception(ctx context.Context, exceptionID id.MigrationExceptionID) (migration.Exception, id.MigrationRunID, error) {
	got, err := r.exceptions(ctx, ` AND e.exception_id = $2`, exceptionID.UUID())
	if err != nil {
		return migration.Exception{}, id.MigrationRunID{}, err
	}
	if len(got) == 0 {
		return migration.Exception{}, id.MigrationRunID{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such migration exception.")
	}
	return got[0].ex, got[0].run, nil
}

// DecideException records an exception's decision.
func (r *MigrationRepo) DecideException(ctx context.Context, e migration.Exception) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlMigDecisionInsert, tenant.UUID(), e.ID.UUID(), string(e.Disposition), e.Note,
		e.DecidedAt.UTC(), e.DecidedBy.UUID())
	return mapError(err, "insert migration exception decision")
}

// CutoverEvents returns a program's cutover history.
func (r *MigrationRepo) CutoverEvents(ctx context.Context, programID id.MigrationProgramID) ([]migration.CutoverEvent, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlMigCutoverSelect, tenant.UUID(), programID.UUID())
	if err != nil {
		return nil, mapError(err, "read cutover events")
	}
	defer rows.Close()
	var out []migration.CutoverEvent
	for rows.Next() {
		var (
			e     migration.CutoverEvent
			state string
			by    uuid.UUID
		)
		if err := rows.Scan(&e.Seq, &state, &e.Note, &e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan cutover event")
		}
		e.State, e.RecordedAt, e.RecordedBy = migration.CutoverState(state), e.RecordedAt.UTC(), id.NewUserID(by)
		out = append(out, e)
	}
	return out, mapError(rows.Err(), "read cutover events")
}

// AppendCutoverEvent records one cutover move.
func (r *MigrationRepo) AppendCutoverEvent(ctx context.Context, programID id.MigrationProgramID, e migration.CutoverEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlMigCutoverInsert, tenant.UUID(), programID.UUID(), e.Seq, string(e.State), e.Note,
		e.RecordedAt.UTC(), e.RecordedBy.UUID())
	return mapError(err, "insert cutover event")
}
