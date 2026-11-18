package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/contribution"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/workpaper"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// regulatory_workpaper(_build, _event) — migration 000032
// ---------------------------------------------------------------------------

const (
	sqlWorkpaperColumns = `workpaper_id, legal_entity_id, duty, period, authority, reporting_currency, materiality,
		obligation_id, content_release, created_at, created_by`
	sqlWorkpaperInsert = `INSERT INTO ztax.regulatory_workpaper (tenant_id, ` + sqlWorkpaperColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	sqlWorkpaperSelect = `SELECT ` + sqlWorkpaperColumns + ` FROM ztax.regulatory_workpaper w WHERE w.tenant_id = $1`
	sqlWorkpaperByID   = sqlWorkpaperSelect + ` AND w.workpaper_id = $2`
	sqlWorkpaperByKey  = sqlWorkpaperSelect + ` AND w.legal_entity_id = $2 AND w.duty = $3 AND w.period = $4`
	// Every filter is a parameter; an empty one matches everything. The
	// status filter reads the latest event, which is the folded status.
	sqlWorkpaperList = sqlWorkpaperSelect + `
		AND ($2 = '' OR w.period = $2)
		AND ($3 = '' OR w.duty = $3)
		AND ($4::uuid IS NULL OR w.legal_entity_id = $4)
		AND ($5 = '' OR (SELECT e.status_after FROM ztax.regulatory_workpaper_event e
			WHERE e.tenant_id = w.tenant_id AND e.workpaper_id = w.workpaper_id
			ORDER BY e.seq DESC LIMIT 1) = $5)
		ORDER BY w.period DESC, w.created_at DESC, w.workpaper_id DESC LIMIT $6`

	sqlWorkpaperBuildInsert = `INSERT INTO ztax.regulatory_workpaper_build (tenant_id, workpaper_id, version, inputs_hash,
		output_hash, basis_digest, formula_version, content_release, output_canonical, built_at, built_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	sqlWorkpaperBuilds = `SELECT workpaper_id, version, inputs_hash, output_hash, basis_digest, formula_version,
		content_release, built_at, built_by FROM ztax.regulatory_workpaper_build
		WHERE tenant_id = $1 AND workpaper_id = ANY($2) ORDER BY workpaper_id, version`
	sqlWorkpaperOutput = `SELECT output_canonical FROM ztax.regulatory_workpaper_build
		WHERE tenant_id = $1 AND workpaper_id = $2 AND version = $3`

	sqlWorkpaperEventColumns = `workpaper_id, seq, kind, status_after, note, build_version, content_release, fx_currency,
		fx_rate, fx_rate_source, fx_rate_date, fx_rounding_mode, fx_rounding_scale, fx_conversion_basis, adjustment_number,
		adjustment_kind, adjustment_category, adjustment_amount, decision, exception_key, exception_fingerprint,
		evidence_sha256, evidence_name, evidence_media, recorded_at, recorded_by`
	sqlWorkpaperEventInsert = `INSERT INTO ztax.regulatory_workpaper_event (tenant_id, ` + sqlWorkpaperEventColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22,
		$23, $24, $25, $26, $27)`
	sqlWorkpaperEvents = `SELECT ` + sqlWorkpaperEventColumns + ` FROM ztax.regulatory_workpaper_event
		WHERE tenant_id = $1 AND workpaper_id = ANY($2) ORDER BY workpaper_id, seq`

	sqlWorkpaperLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	workpaperListCap = 500
)

// WorkpaperRepo implements port.WorkpaperRepository.
type WorkpaperRepo struct{ s *Store }

// Workpapers returns the regulatory workpaper repository.
func (s *Store) Workpapers() *WorkpaperRepo { return &WorkpaperRepo{s: s} }

var _ port.WorkpaperRepository = (*WorkpaperRepo)(nil)

// Lock takes one of the tenant's workpaper locks for the transaction.
func (r *WorkpaperRepo) Lock(ctx context.Context, key string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlWorkpaperLock, tenant.String()+"/workpaper/"+key)
	return mapError(err, "lock workpaper")
}

// Create writes a header and its CREATED event.
func (r *WorkpaperRepo) Create(ctx context.Context, w workpaper.Workpaper, created workpaper.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if w.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The workpaper belongs to a tenant outside the caller's scope.")
	}
	if err := w.Validate(); err != nil {
		return obligationFault(err, "The workpaper is not well-formed.")
	}
	if created.Kind != workpaper.EventCreated || created.Seq != 1 {
		return obligationFault(nil, "A workpaper is created by its CREATED event.")
	}
	materiality, err := MoneyValue(w.Materiality)
	if err != nil {
		return err
	}
	var obligation *uuid.UUID
	if !w.Obligation.IsZero() {
		u := w.Obligation.UUID()
		obligation = &u
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlWorkpaperInsert, tenant.UUID(), w.ID.UUID(), w.LegalEntity.UUID(), string(w.Duty),
		w.Period, w.Authority, string(w.ReportingCurrency), materiality, obligation, w.InitialRelease, w.CreatedAt.UTC(),
		w.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert workpaper")
	}
	return r.AppendEvents(ctx, w.ID, []workpaper.Event{created})
}

// AppendBuild writes a build and its canonical output.
func (r *WorkpaperRepo) AppendBuild(ctx context.Context, workpaperID id.WorkpaperID, b workpaper.Build) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if b.Output == nil || b.InputsHash.IsZero() || b.OutputHash.IsZero() || b.BasisDigest.IsZero() {
		return obligationFault(nil, "A build is stored with its output and its hashes.")
	}
	body, err := workpaper.EncodeOutput(*b.Output)
	if err != nil {
		return obligationFault(err, "The build's output could not be encoded.")
	}
	if !canonical.SumBytes(body).Equal(b.OutputHash) {
		return obligationFault(nil, "The build's output does not hash to its output hash.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlWorkpaperBuildInsert, tenant.UUID(), workpaperID.UUID(), b.Version, b.InputsHash.String(),
		b.OutputHash.String(), b.BasisDigest.String(), b.FormulaVersion, b.ContentRelease, string(body), b.BuiltAt.UTC(),
		b.BuiltBy.UUID())
	return mapError(err, "insert workpaper build")
}

func wpOptInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func wpOptDate(e *workpaper.FXRate) pgtype.Date {
	if e == nil {
		return pgtype.Date{}
	}
	return civilDate(e.RateDate)
}

// AppendEvents writes the next events, in order.
func (r *WorkpaperRepo) AppendEvents(ctx context.Context, workpaperID id.WorkpaperID, events []workpaper.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	for _, e := range events {
		var (
			fxCurrency, fxSource, fxMode, fxBasis *string
			fxRate, adjAmount                     Numeric
			fxScale                               *int
			adjKind, decision, evName, evMedia    *string
			evSHA                                 *string
		)
		if e.FX != nil {
			cur, mode := string(e.FX.Currency), string(e.FX.RoundingMode)
			src, basis, scale := e.FX.Source, e.FX.Basis, int(e.FX.Scale)
			fxCurrency, fxSource, fxMode, fxBasis, fxScale = &cur, &src, &mode, &basis, &scale
			if fxRate, err = RateValue(e.FX.Rate); err != nil {
				return err
			}
		}
		if e.AdjustmentAmount != nil {
			if adjAmount, err = MoneyValue(*e.AdjustmentAmount); err != nil {
				return err
			}
		}
		if e.AdjustmentKind != "" {
			k := string(e.AdjustmentKind)
			adjKind = &k
		}
		if e.Decision != "" {
			d := string(e.Decision)
			decision = &d
		}
		if e.Evidence != nil {
			sha, name, media := e.Evidence.SHA256, e.Evidence.Filename, e.Evidence.MediaType
			evSHA, evName, evMedia = &sha, &name, &media
		}
		if _, err := r.s.db(ctx).Exec(ctx, sqlWorkpaperEventInsert, tenant.UUID(), workpaperID.UUID(), e.Seq, string(e.Kind),
			string(e.Status), optString(e.Note), wpOptInt(e.BuildVersion), optString(e.ContentRelease), fxCurrency, fxRate,
			fxSource, wpOptDate(e.FX), fxMode, fxScale, fxBasis, wpOptInt(e.AdjustmentNumber), adjKind,
			optString(e.AdjustmentCategory), adjAmount, decision, optString(e.ExceptionKey), optString(e.Fingerprint), evSHA,
			evName, evMedia, e.RecordedAt.UTC(), e.RecordedBy.UUID()); err != nil {
			return mapError(err, "insert workpaper event")
		}
	}
	return nil
}

// Workpaper returns one workpaper, with its latest build's output.
func (r *WorkpaperRepo) Workpaper(ctx context.Context, workpaperID id.WorkpaperID) (workpaper.Workpaper, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return workpaper.Workpaper{}, err
	}
	got, err := r.load(ctx, sqlWorkpaperByID, tenant.UUID(), workpaperID.UUID())
	if err != nil {
		return workpaper.Workpaper{}, err
	}
	if len(got) == 0 {
		return workpaper.Workpaper{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such workpaper.")
	}
	w := got[0]
	if b := w.Latest(); b != nil {
		var body string
		if err := r.s.db(ctx).QueryRow(ctx, sqlWorkpaperOutput, tenant.UUID(), w.ID.UUID(), b.Version).Scan(&body); err != nil {
			return workpaper.Workpaper{}, mapError(err, "read workpaper output")
		}
		out, err := workpaper.DecodeOutput([]byte(body), b.OutputHash)
		if err != nil {
			return workpaper.Workpaper{}, errs.Wrap(err, errs.CategoryInternal, errs.ReasonEvidenceIntegrity,
				"A stored workpaper build no longer matches its output hash.")
		}
		w.Builds[len(w.Builds)-1].Output = &out
	}
	return w, nil
}

// ByKey returns the workpaper of a filer, duty and period.
func (r *WorkpaperRepo) ByKey(ctx context.Context, le id.LegalEntityID, duty contribution.Regime, period string) (workpaper.Workpaper, bool, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return workpaper.Workpaper{}, false, err
	}
	got, err := r.load(ctx, sqlWorkpaperByKey, tenant.UUID(), le.UUID(), string(duty), period)
	if err != nil || len(got) == 0 {
		return workpaper.Workpaper{}, false, err
	}
	return got[0], true, nil
}

// List returns the workpapers a filter selects, newest period first.
func (r *WorkpaperRepo) List(ctx context.Context, f workpaper.Filter) ([]workpaper.Workpaper, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 || limit > workpaperListCap {
		limit = workpaperListCap
	}
	var entity *uuid.UUID
	if !f.LegalEntity.IsZero() {
		u := f.LegalEntity.UUID()
		entity = &u
	}
	return r.load(ctx, sqlWorkpaperList, tenant.UUID(), f.Period, string(f.Duty), entity, string(f.Status), limit)
}

// load reads the headers a fixed query from this file selects, and folds
// each with its events and its builds' summaries.
func (r *WorkpaperRepo) load(ctx context.Context, query string, args ...any) ([]workpaper.Workpaper, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, mapError(err, "read workpapers")
	}
	var out []workpaper.Workpaper
	for rows.Next() {
		var (
			w               = workpaper.Workpaper{TenantID: tenant}
			wid, entity, by uuid.UUID
			obligation      *uuid.UUID
			duty, currency  string
			materiality     Numeric
		)
		if err := rows.Scan(&wid, &entity, &duty, &w.Period, &w.Authority, &currency, &materiality, &obligation,
			&w.InitialRelease, &w.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan workpaper")
		}
		w.ReportingCurrency = fiscal.Currency(currency)
		if w.Materiality, err = materiality.Money(w.ReportingCurrency); err != nil {
			rows.Close()
			return nil, err
		}
		w.ID, w.LegalEntity, w.CreatedBy = id.NewWorkpaperID(wid), id.NewLegalEntityID(entity), id.NewUserID(by)
		w.Duty, w.CreatedAt = contribution.Regime(duty), w.CreatedAt.UTC()
		if obligation != nil {
			w.Obligation = id.NewObligationID(*obligation)
		}
		out = append(out, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read workpapers")
	}
	if len(out) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(out))
	for i, w := range out {
		ids[i] = w.ID.UUID()
	}
	builds, err := r.builds(ctx, tenant.UUID(), ids)
	if err != nil {
		return nil, err
	}
	events, err := r.events(ctx, tenant.UUID(), ids, out)
	if err != nil {
		return nil, err
	}
	for i := range out {
		folded, err := workpaper.Fold(out[i], events[out[i].ID], builds[out[i].ID])
		if err != nil {
			return nil, obligationFault(err, "A stored workpaper's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}

func (r *WorkpaperRepo) builds(ctx context.Context, tenant uuid.UUID, ids []uuid.UUID) (map[id.WorkpaperID][]workpaper.Build, error) {
	rows, err := r.s.db(ctx).Query(ctx, sqlWorkpaperBuilds, tenant, ids)
	if err != nil {
		return nil, mapError(err, "read workpaper builds")
	}
	defer rows.Close()
	out := map[id.WorkpaperID][]workpaper.Build{}
	for rows.Next() {
		var (
			b                    workpaper.Build
			wid, by              uuid.UUID
			inputs, output, base string
		)
		if err := rows.Scan(&wid, &b.Version, &inputs, &output, &base, &b.FormulaVersion, &b.ContentRelease, &b.BuiltAt,
			&by); err != nil {
			return nil, mapError(err, "scan workpaper build")
		}
		for _, d := range []struct {
			into *canonical.Digest
			s    string
		}{{&b.InputsHash, inputs}, {&b.OutputHash, output}, {&b.BasisDigest, base}} {
			if *d.into, err = canonical.ParseDigest(d.s); err != nil {
				return nil, obligationFault(err, "A stored workpaper build's digest does not parse.")
			}
		}
		b.BuiltAt, b.BuiltBy = b.BuiltAt.UTC(), id.NewUserID(by)
		k := id.NewWorkpaperID(wid)
		out[k] = append(out[k], b)
	}
	return out, mapError(rows.Err(), "read workpaper builds")
}

func (r *WorkpaperRepo) events(ctx context.Context, tenant uuid.UUID, ids []uuid.UUID, headers []workpaper.Workpaper) (map[id.WorkpaperID][]workpaper.Event, error) {
	currency := map[id.WorkpaperID]fiscal.Currency{}
	for _, w := range headers {
		currency[w.ID] = w.ReportingCurrency
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlWorkpaperEvents, tenant, ids)
	if err != nil {
		return nil, mapError(err, "read workpaper events")
	}
	defer rows.Close()
	out := map[id.WorkpaperID][]workpaper.Event{}
	for rows.Next() {
		var (
			e                                                  workpaper.Event
			wid, by                                            uuid.UUID
			kind, status                                       string
			note, release, fxCurrency, fxSource, fxMode, basis *string
			adjKind, adjCategory, decision, key, fingerprint   *string
			evSHA, evName, evMedia                             *string
			build, fxScale, adjNumber                          *int
			fxRate, adjAmount                                  Numeric
			fxDate                                             pgtype.Date
		)
		if err := rows.Scan(&wid, &e.Seq, &kind, &status, &note, &build, &release, &fxCurrency, &fxRate, &fxSource, &fxDate,
			&fxMode, &fxScale, &basis, &adjNumber, &adjKind, &adjCategory, &adjAmount, &decision, &key, &fingerprint, &evSHA,
			&evName, &evMedia, &e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan workpaper event")
		}
		k := id.NewWorkpaperID(wid)
		e.Kind, e.Status, e.Note, e.ContentRelease = workpaper.EventKind(kind), workpaper.Status(status), deref(note), deref(release)
		e.AdjustmentKind, e.AdjustmentCategory = workpaper.AdjustmentKind(deref(adjKind)), deref(adjCategory)
		e.Decision, e.ExceptionKey, e.Fingerprint = workpaper.Decision(deref(decision)), deref(key), deref(fingerprint)
		e.RecordedAt, e.RecordedBy = e.RecordedAt.UTC(), id.NewUserID(by)
		if build != nil {
			e.BuildVersion = *build
		}
		if adjNumber != nil {
			e.AdjustmentNumber = *adjNumber
		}
		if fxCurrency != nil {
			rate, err := fxRate.Rate(fiscal.RateBasisNet)
			if err != nil {
				return nil, err
			}
			fx := workpaper.FXRate{Currency: fiscal.Currency(*fxCurrency), Rate: rate, Source: deref(fxSource),
				RoundingMode: fiscal.RoundingMode(deref(fxMode)), Basis: deref(basis)}
			if fxDate.Valid {
				fx.RateDate = fxDate.Time
			}
			if fxScale != nil {
				fx.Scale = int32(*fxScale) //nolint:gosec // G115: the column is CHECKed to 0..12
			}
			e.FX = &fx
		}
		if adjAmount.Valid {
			m, err := adjAmount.Money(currency[k])
			if err != nil {
				return nil, err
			}
			e.AdjustmentAmount = &m
		}
		if evSHA != nil {
			e.Evidence = &workpaper.Evidence{SHA256: *evSHA, Filename: deref(evName), MediaType: deref(evMedia)}
		}
		out[k] = append(out[k], e)
	}
	return out, mapError(rows.Err(), "read workpaper events")
}
