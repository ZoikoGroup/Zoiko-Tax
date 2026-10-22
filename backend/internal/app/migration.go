package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/migration"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Migration and historical conversion (ZTAX-MIG-001, W4 lane M). The rules
// are internal/domain/migration's; this is where programs, snapshots,
// mappings and runs meet the store.
//
// A run converts AS_KNOWN into migration's own tables. It never writes a
// ZoikoTax decision, obligation or posting: converted history explains the
// past and is reconciled against its source, and that is all.

// MigrationService runs migration programs.
type MigrationService struct {
	store    port.MigrationRepository
	entities port.LegalEntityRepository
	tx       port.TxManager
	clock    clock.Clock
	ids      idgen.Generator
}

// NewMigrationService wires the service.
func NewMigrationService(store port.MigrationRepository, entities port.LegalEntityRepository, tx port.TxManager,
	clk clock.Clock, ids idgen.Generator) *MigrationService {
	return &MigrationService{store: store, entities: entities, tx: tx, clock: clk, ids: ids}
}

func (s *MigrationService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

var (
	migrationReaders   = []security.Role{security.RoleAdmin, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor}
	migrationOperators = []security.Role{security.RoleAdmin, security.RoleOperator}
	migrationDeciders  = []security.Role{security.RoleAdmin, security.RoleOperator, security.RoleAnalyst}
)

// maxSnapshotRecords bounds one snapshot's upload.
const maxSnapshotRecords = 5000

// ProgramInput is a program to create.
type ProgramInput struct {
	Name        string
	Type        migration.Type
	LegalEntity id.LegalEntityID
	PeriodFrom  time.Time
	PeriodTo    time.Time
	Scale       int
}

// ProgramView is a program with everything recorded against it and what
// its cutover may do next.
type ProgramView struct {
	migration.Program
	Snapshots    []migration.Snapshot
	Mappings     []migration.Mapping
	Runs         []migration.Run
	Cutover      []migration.CutoverEvent
	CutoverState migration.CutoverState
	Readiness    migration.Readiness
	Blockers     []string
}

// CreateProgram records a program for a legal entity; zero is the tenant's
// default.
func (s *MigrationService) CreateProgram(ctx context.Context, in ProgramInput) (migration.Program, error) {
	sc, err := requirePerson(ctx, migrationOperators...)
	if err != nil {
		return migration.Program{}, err
	}
	pid, err := idgen.MigrationProgramID(s.ids)
	if err != nil {
		return migration.Program{}, internal(err, "The program could not be created.")
	}
	p := migration.Program{ID: pid, TenantID: sc.Tenant(), Name: strings.TrimSpace(in.Name), Type: in.Type, LegalEntity: in.LegalEntity,
		PeriodFrom: in.PeriodFrom, PeriodTo: in.PeriodTo, Scale: in.Scale, CreatedAt: s.now(), CreatedBy: sc.Subject()}
	err = s.inTx(ctx, func(ctx context.Context) error {
		if p.LegalEntity.IsZero() {
			le, err := s.entities.Default(ctx)
			if err != nil {
				return err
			}
			p.LegalEntity = le.ID
		} else if _, err := s.entities.ByID(ctx, p.LegalEntity); err != nil {
			return err
		}
		if err := p.Validate(); err != nil {
			return errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, strings.TrimPrefix(err.Error(), "migration: ")+".")
		}
		return s.store.CreateProgram(ctx, p)
	})
	return p, err
}

// Programs lists the tenant's programs.
func (s *MigrationService) Programs(ctx context.Context) ([]migration.Program, error) {
	if _, err := requireRoleOrSystem(ctx, migrationReaders...); err != nil {
		return nil, err
	}
	return s.store.Programs(ctx)
}

// Program reads a program with its snapshots, mappings, runs and cutover.
func (s *MigrationService) Program(ctx context.Context, programID id.MigrationProgramID) (ProgramView, error) {
	if _, err := requireRoleOrSystem(ctx, migrationReaders...); err != nil {
		return ProgramView{}, err
	}
	return s.view(ctx, programID)
}

// SnapshotInput is a source extraction to register.
type SnapshotInput struct {
	SourceSystem  string
	SourceVersion string
	Method        string
	Watermark     string
	Records       []migration.SourceRecord
	ControlTotals map[string]string
}

// RegisterSnapshot records an immutable snapshot of a source's records.
func (s *MigrationService) RegisterSnapshot(ctx context.Context, programID id.MigrationProgramID, in SnapshotInput) (migration.Snapshot, error) {
	sc, err := requirePerson(ctx, migrationOperators...)
	if err != nil {
		return migration.Snapshot{}, err
	}
	if len(in.Records) > maxSnapshotRecords {
		return migration.Snapshot{}, errs.Invalid("records", errs.ReasonInvalidValue,
			fmt.Sprintf("A snapshot holds at most %d records; split the extraction.", maxSnapshotRecords))
	}
	sid, err := idgen.MigrationSnapshotID(s.ids)
	if err != nil {
		return migration.Snapshot{}, internal(err, "The snapshot could not be registered.")
	}
	snap := migration.Snapshot{ID: sid, TenantID: sc.Tenant(), Program: programID, SourceSystem: strings.TrimSpace(in.SourceSystem),
		SourceVersion: strings.TrimSpace(in.SourceVersion), Method: in.Method, Watermark: strings.TrimSpace(in.Watermark),
		Records: in.Records, ControlTotals: in.ControlTotals, CreatedAt: s.now(), CreatedBy: sc.Subject()}
	if err := snap.Seal(); err != nil {
		return migration.Snapshot{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, strings.TrimPrefix(err.Error(), "migration: ")+".")
	}
	err = s.inTx(ctx, func(ctx context.Context) error {
		if _, err := s.store.Program(ctx, programID); err != nil {
			return err
		}
		return s.store.CreateSnapshot(ctx, snap)
	})
	return snap, err
}

// CreateMapping records the next version of a program's code mapping.
func (s *MigrationService) CreateMapping(ctx context.Context, programID id.MigrationProgramID, codes map[string]string) (migration.Mapping, error) {
	sc, err := requirePerson(ctx, migrationOperators...)
	if err != nil {
		return migration.Mapping{}, err
	}
	mid, err := idgen.MigrationMappingID(s.ids)
	if err != nil {
		return migration.Mapping{}, internal(err, "The mapping could not be recorded.")
	}
	m := migration.Mapping{ID: mid, TenantID: sc.Tenant(), Program: programID, Codes: codes, CreatedAt: s.now(), CreatedBy: sc.Subject()}
	if err := m.Validate(); err != nil {
		return migration.Mapping{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, strings.TrimPrefix(err.Error(), "migration: ")+".")
	}
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "mapping/"+programID.String()); err != nil {
			return err
		}
		if _, err := s.store.Program(ctx, programID); err != nil {
			return err
		}
		prior, err := s.store.Mappings(ctx, programID)
		if err != nil {
			return err
		}
		m.Version = 1
		if len(prior) > 0 {
			m.Version = prior[0].Version + 1
		}
		return s.store.CreateMapping(ctx, m)
	})
	return m, err
}

// ExecuteRun converts a snapshot under a mapping version, AS_KNOWN, and
// records the converted history, the exceptions and the reconciliation.
func (s *MigrationService) ExecuteRun(ctx context.Context, programID id.MigrationProgramID,
	snapshotID id.MigrationSnapshotID, mappingID id.MigrationMappingID) (migration.Run, error) {
	sc, err := requirePerson(ctx, migrationOperators...)
	if err != nil {
		return migration.Run{}, err
	}
	var out migration.Run
	err = s.inTx(ctx, func(ctx context.Context) error {
		p, err := s.store.Program(ctx, programID)
		if err != nil {
			return err
		}
		snap, err := s.store.Snapshot(ctx, snapshotID)
		if err != nil {
			return err
		}
		m, err := s.store.Mapping(ctx, mappingID)
		if err != nil {
			return err
		}
		if snap.Program != p.ID || m.Program != p.ID {
			return errs.Invalid("snapshotId", errs.ReasonInvalidValue, "A run converts a snapshot and a mapping of its own program.")
		}
		rid, err := idgen.MigrationRunID(s.ids)
		if err != nil {
			return internal(err, "The run could not be recorded.")
		}
		records, exceptions, recon, err := migration.Convert(p, snap, m,
			func() (id.MigratedRecordID, error) { return idgen.MigratedRecordID(s.ids) },
			func() (id.MigrationExceptionID, error) { return idgen.MigrationExceptionID(s.ids) })
		if err != nil {
			return internal(err, "The conversion could not be computed.")
		}
		out = migration.Run{ID: rid, TenantID: sc.Tenant(), Program: p.ID, Snapshot: snap.ID, Mapping: m.ID, Mode: migration.ModeAsKnown,
			Records: records, Exceptions: exceptions, Recon: recon, CreatedAt: s.now(), CreatedBy: sc.Subject()}
		return s.store.CreateRun(ctx, out)
	})
	return out, err
}

// Run reads one run with its converted records and exceptions.
func (s *MigrationService) Run(ctx context.Context, runID id.MigrationRunID) (migration.Run, error) {
	if _, err := requireRoleOrSystem(ctx, migrationReaders...); err != nil {
		return migration.Run{}, err
	}
	return s.store.Run(ctx, runID)
}

// DecideException resolves an exception, or accepts it as a known residual.
func (s *MigrationService) DecideException(ctx context.Context, exceptionID id.MigrationExceptionID,
	d migration.Disposition, note string) (migration.Exception, error) {
	sc, err := requirePerson(ctx, migrationDeciders...)
	if err != nil {
		return migration.Exception{}, err
	}
	var out migration.Exception
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "exception/"+exceptionID.String()); err != nil {
			return err
		}
		e, _, err := s.store.Exception(ctx, exceptionID)
		if err != nil {
			return err
		}
		if out, err = e.Decide(d, strings.TrimSpace(note), sc.Subject(), s.now()); err != nil {
			return err
		}
		return s.store.DecideException(ctx, out)
	})
	return out, err
}

// MoveCutover records the next cutover state, under the readiness of the
// program's latest run.
func (s *MigrationService) MoveCutover(ctx context.Context, programID id.MigrationProgramID, to migration.CutoverState, note string) (ProgramView, error) {
	sc, err := requirePerson(ctx, migrationOperators...)
	if err != nil {
		return ProgramView{}, err
	}
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "cutover/"+programID.String()); err != nil {
			return err
		}
		v, err := s.view(ctx, programID)
		if err != nil {
			return err
		}
		e, err := migration.NextCutover(v.Cutover, to, v.Program, v.Readiness, strings.TrimSpace(note), sc.Subject(), s.now())
		if err != nil {
			return err
		}
		return s.store.AppendCutoverEvent(ctx, programID, e)
	})
	if err != nil {
		return ProgramView{}, err
	}
	return s.view(ctx, programID)
}

func (s *MigrationService) view(ctx context.Context, programID id.MigrationProgramID) (ProgramView, error) {
	p, err := s.store.Program(ctx, programID)
	if err != nil {
		return ProgramView{}, err
	}
	v := ProgramView{Program: p, CutoverState: migration.CutoverPlanned}
	if v.Snapshots, err = s.store.Snapshots(ctx, programID); err != nil {
		return ProgramView{}, err
	}
	if v.Mappings, err = s.store.Mappings(ctx, programID); err != nil {
		return ProgramView{}, err
	}
	if v.Runs, err = s.store.Runs(ctx, programID); err != nil {
		return ProgramView{}, err
	}
	if v.Cutover, err = s.store.CutoverEvents(ctx, programID); err != nil {
		return ProgramView{}, err
	}
	if len(v.Cutover) > 0 {
		v.CutoverState = v.Cutover[len(v.Cutover)-1].State
	}
	if len(v.Runs) > 0 {
		latest := v.Runs[0]
		v.Readiness = migration.Readiness{HasRun: true, Reconciled: latest.Recon.Passed()}
		for _, e := range latest.Exceptions {
			switch {
			case e.Disposition == migration.DispositionResidual:
				v.Readiness.Residuals++
			case e.Disposition != migration.DispositionOpen:
			case e.Severity == migration.SeverityHigh:
				v.Readiness.OpenHigh++
			default:
				v.Readiness.OpenOther++
			}
		}
	}
	v.Blockers = v.Readiness.Blockers()
	return v, nil
}

func (s *MigrationService) inTx(ctx context.Context, fn func(context.Context) error) error {
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(txCtx)
}
