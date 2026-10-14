package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/shadow"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// shadow_comparator, shadow_run, shadow_case, shadow_discrepancy(_event) —
// migration 000024
// ---------------------------------------------------------------------------

const (
	sqlComparatorColumns = `comparator_id, name, comparator_type, provider, version, environment, limitations, created_at, created_by`
	sqlComparatorInsert  = `INSERT INTO ztax.shadow_comparator (tenant_id, ` + sqlComparatorColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	sqlComparatorSelect = `SELECT ` + sqlComparatorColumns + ` FROM ztax.shadow_comparator WHERE tenant_id = $1`

	sqlRunColumns = `run_id, comparator_id, mode, window_from, window_to, currency, component_map, tolerance, materiality,
		input_hash, cases, paired, exact_matches, equivalent_matches, monetary_variances, category_variances,
		population_variances, uncomparable, material, compared_value, absolute_variance, created_at, created_by`
	sqlRunInsert = `INSERT INTO ztax.shadow_run (tenant_id, ` + sqlRunColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)`
	sqlRunSelect = `SELECT ` + sqlRunColumns + ` FROM ztax.shadow_run WHERE tenant_id = $1`

	sqlCaseColumns = `seq, case_ref, decision_id, pairing, state, comparator_total, zoiko_total, variance, material, components`
	sqlCaseInsert  = `INSERT INTO ztax.shadow_case (tenant_id, run_id, ` + sqlCaseColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	sqlCaseSelect = `SELECT ` + sqlCaseColumns + ` FROM ztax.shadow_case WHERE tenant_id = $1 AND run_id = $2 ORDER BY seq`

	sqlDiscrepancyInsert = `INSERT INTO ztax.shadow_discrepancy (tenant_id, discrepancy_id, run_id, case_seq) VALUES ($1, $2, $3, $4)`
	sqlDiscrepancySelect = `SELECT discrepancy_id, run_id, case_seq FROM ztax.shadow_discrepancy WHERE tenant_id = $1`
	sqlDiscEventInsert   = `INSERT INTO ztax.shadow_discrepancy_event
		(tenant_id, discrepancy_id, seq, state, severity, root_cause, note, recorded_at, recorded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
	sqlDiscEventSelect = `SELECT discrepancy_id, seq, state, severity, root_cause, note, recorded_at, recorded_by
		FROM ztax.shadow_discrepancy_event WHERE tenant_id = $1 AND discrepancy_id = ANY($2) ORDER BY discrepancy_id, seq`

	sqlShadowLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	shadowRunListCap = 200
)

// ShadowRepo implements port.ShadowRepository.
type ShadowRepo struct{ s *Store }

// Shadow returns the shadow assurance repository.
func (s *Store) Shadow() *ShadowRepo { return &ShadowRepo{s: s} }

var _ port.ShadowRepository = (*ShadowRepo)(nil)

// Lock takes one of the tenant's shadow locks for the transaction.
func (r *ShadowRepo) Lock(ctx context.Context, key string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlShadowLock, tenant.String()+"/shadow/"+key)
	return mapError(err, "lock shadow")
}

// CreateComparator records a comparator profile.
func (r *ShadowRepo) CreateComparator(ctx context.Context, c shadow.Comparator) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if c.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The comparator belongs to a tenant outside the caller's scope.")
	}
	if err := c.Validate(); err != nil {
		return obligationFault(err, "The comparator is not well-formed.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlComparatorInsert, tenant.UUID(), c.ID.UUID(), c.Name, string(c.Type), c.Provider, c.Version,
		optString(c.Environment), optString(c.Limitations), c.CreatedAt.UTC(), c.CreatedBy.UUID())
	return mapError(err, "insert comparator")
}

// Comparator returns one profile.
func (r *ShadowRepo) Comparator(ctx context.Context, comparatorID id.ComparatorID) (shadow.Comparator, error) {
	got, err := r.comparators(ctx, ` AND comparator_id = $2`, comparatorID.UUID())
	if err != nil {
		return shadow.Comparator{}, err
	}
	if len(got) == 0 {
		return shadow.Comparator{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such comparator.")
	}
	return got[0], nil
}

// Comparators returns every profile of the tenant, newest first.
func (r *ShadowRepo) Comparators(ctx context.Context) ([]shadow.Comparator, error) {
	return r.comparators(ctx, ``, nil)
}

func (r *ShadowRepo) comparators(ctx context.Context, where string, arg any) ([]shadow.Comparator, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	args := []any{tenant.UUID()}
	if arg != nil {
		args = append(args, arg)
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlComparatorSelect+where+` ORDER BY created_at DESC, comparator_id DESC`, args...)
	if err != nil {
		return nil, mapError(err, "read comparators")
	}
	defer rows.Close()
	var out []shadow.Comparator
	for rows.Next() {
		var (
			c                  = shadow.Comparator{TenantID: tenant}
			cid, by            uuid.UUID
			typ                string
			environment, limit *string
		)
		if err := rows.Scan(&cid, &c.Name, &typ, &c.Provider, &c.Version, &environment, &limit, &c.CreatedAt, &by); err != nil {
			return nil, mapError(err, "scan comparator")
		}
		c.ID, c.Type, c.CreatedBy, c.CreatedAt = id.NewComparatorID(cid), shadow.ComparatorType(typ), id.NewUserID(by), c.CreatedAt.UTC()
		c.Environment, c.Limitations = deref(environment), deref(limit)
		out = append(out, c)
	}
	return out, mapError(rows.Err(), "read comparators")
}

// componentWire is how a component comparison is stored: decimal strings,
// never numbers.
type componentWire struct {
	ExternalCode string  `json:"externalCode,omitempty"`
	Slot         string  `json:"slot"`
	Comparator   *string `json:"comparator,omitempty"`
	Zoiko        *string `json:"zoiko,omitempty"`
	State        string  `json:"state"`
}

func moneyString(m *fiscal.Money) *string {
	if m == nil {
		return nil
	}
	s := m.CanonicalString()
	return &s
}

// CreateRun records a run, its cases and its discrepancies.
func (r *ShadowRepo) CreateRun(ctx context.Context, run shadow.Run, discrepancies []shadow.Discrepancy) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if run.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The run belongs to a tenant outside the caller's scope.")
	}
	mapping, err := json.Marshal(run.Policy.ComponentMap)
	if err != nil {
		return obligationFault(err, "The run's component map could not be stored.")
	}
	tol, err := MoneyValue(run.Policy.Tolerance)
	if err != nil {
		return err
	}
	mat, err := MoneyValue(run.Policy.Materiality)
	if err != nil {
		return err
	}
	compared, err := MoneyValue(run.Metrics.ComparedValue)
	if err != nil {
		return err
	}
	variance, err := MoneyValue(run.Metrics.AbsoluteVariance)
	if err != nil {
		return err
	}
	m := run.Metrics
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlRunInsert, tenant.UUID(), run.ID.UUID(), run.Comparator.UUID(), string(run.Mode),
		run.WindowFrom.UTC(), run.WindowTo.UTC(), string(run.Policy.Currency), mapping, tol, mat, run.InputHash.String(),
		m.Cases, m.Paired, m.Exact, m.Equivalent, m.Monetary, m.Category, m.Population, m.Uncomparable, m.Material,
		compared, variance, run.CreatedAt.UTC(), run.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert shadow run")
	}
	for _, c := range run.Cases {
		components := make([]componentWire, len(c.Components))
		for i, cc := range c.Components {
			components[i] = componentWire{ExternalCode: cc.ExternalCode, Slot: cc.Slot, Comparator: moneyString(cc.Comparator),
				Zoiko: moneyString(cc.Zoiko), State: string(cc.State)}
		}
		body, err := json.Marshal(components)
		if err != nil {
			return obligationFault(err, "A case's components could not be stored.")
		}
		var decision *uuid.UUID
		if !c.Decision.IsZero() {
			u := c.Decision.UUID()
			decision = &u
		}
		ct, err := optMoney(c.ComparatorTotal)
		if err != nil {
			return err
		}
		zt, err := optMoney(c.ZoikoTotal)
		if err != nil {
			return err
		}
		vr, err := optMoney(c.Variance)
		if err != nil {
			return err
		}
		if _, err := db.Exec(ctx, sqlCaseInsert, tenant.UUID(), run.ID.UUID(), c.Seq, c.CaseRef, decision, string(c.Pairing),
			string(c.State), ct, zt, vr, c.Material, body); err != nil {
			return mapError(err, "insert shadow case")
		}
	}
	for _, d := range discrepancies {
		if _, err := db.Exec(ctx, sqlDiscrepancyInsert, tenant.UUID(), d.ID.UUID(), run.ID.UUID(), d.CaseSeq); err != nil {
			return mapError(err, "insert discrepancy")
		}
		for _, e := range d.History {
			if err := r.AppendDiscrepancyEvent(ctx, d.ID, e); err != nil {
				return err
			}
		}
	}
	return nil
}

// Run returns one run with its cases.
func (r *ShadowRepo) Run(ctx context.Context, runID id.ShadowRunID) (shadow.Run, error) {
	got, err := r.runs(ctx, ` AND run_id = $2`, runID.UUID())
	if err != nil {
		return shadow.Run{}, err
	}
	if len(got) == 0 {
		return shadow.Run{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such shadow run.")
	}
	run := got[0]
	tenant, _ := tenantOf(ctx)
	rows, err := r.s.db(ctx).Query(ctx, sqlCaseSelect, tenant.UUID(), runID.UUID())
	if err != nil {
		return shadow.Run{}, mapError(err, "read shadow cases")
	}
	defer rows.Close()
	cur := run.Policy.Currency
	for rows.Next() {
		var (
			c              shadow.Case
			decision       *uuid.UUID
			pairing, state string
			ct, zt, vr     Numeric
			body           []byte
		)
		if err := rows.Scan(&c.Seq, &c.CaseRef, &decision, &pairing, &state, &ct, &zt, &vr, &c.Material, &body); err != nil {
			return shadow.Run{}, mapError(err, "scan shadow case")
		}
		c.Pairing, c.State = shadow.Pairing(pairing), shadow.Variance(state)
		if decision != nil {
			c.Decision = id.NewDecisionID(*decision)
		}
		for _, x := range []struct {
			n   Numeric
			dst **fiscal.Money
		}{{ct, &c.ComparatorTotal}, {zt, &c.ZoikoTotal}, {vr, &c.Variance}} {
			if !x.n.Valid {
				continue
			}
			m, err := x.n.Money(cur)
			if err != nil {
				return shadow.Run{}, err
			}
			*x.dst = &m
		}
		var components []componentWire
		if err := json.Unmarshal(body, &components); err != nil {
			return shadow.Run{}, obligationFault(err, "A stored case's components do not decode.")
		}
		for _, w := range components {
			cc := shadow.ComponentComparison{ExternalCode: w.ExternalCode, Slot: w.Slot, State: shadow.Variance(w.State)}
			for _, x := range []struct {
				s   *string
				dst **fiscal.Money
			}{{w.Comparator, &cc.Comparator}, {w.Zoiko, &cc.Zoiko}} {
				if x.s == nil {
					continue
				}
				m, err := fiscal.ParseMoney(*x.s, cur)
				if err != nil {
					return shadow.Run{}, obligationFault(err, "A stored component amount does not parse.")
				}
				*x.dst = &m
			}
			c.Components = append(c.Components, cc)
		}
		run.Cases = append(run.Cases, c)
	}
	return run, mapError(rows.Err(), "read shadow cases")
}

// Runs returns the tenant's runs, newest first, without their cases.
func (r *ShadowRepo) Runs(ctx context.Context, limit int) ([]shadow.Run, error) {
	if limit <= 0 || limit > shadowRunListCap {
		limit = shadowRunListCap
	}
	return r.runs(ctx, ` AND run_id IN (SELECT run_id FROM ztax.shadow_run WHERE tenant_id = $1
		ORDER BY created_at DESC, run_id DESC LIMIT $2)`, limit)
}

func (r *ShadowRepo) runs(ctx context.Context, where string, arg any) ([]shadow.Run, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlRunSelect+where+` ORDER BY created_at DESC, run_id DESC`, tenant.UUID(), arg)
	if err != nil {
		return nil, mapError(err, "read shadow runs")
	}
	defer rows.Close()
	var out []shadow.Run
	for rows.Next() {
		var (
			run                          = shadow.Run{TenantID: tenant}
			rid, cid, by                 uuid.UUID
			mode, currency, hash         string
			mapping                      []byte
			tol, mat, compared, variance Numeric
			m                            shadow.Metrics
		)
		if err := rows.Scan(&rid, &cid, &mode, &run.WindowFrom, &run.WindowTo, &currency, &mapping, &tol, &mat, &hash,
			&m.Cases, &m.Paired, &m.Exact, &m.Equivalent, &m.Monetary, &m.Category, &m.Population, &m.Uncomparable, &m.Material,
			&compared, &variance, &run.CreatedAt, &by); err != nil {
			return nil, mapError(err, "scan shadow run")
		}
		cur := fiscal.Currency(currency)
		run.ID, run.Comparator, run.Mode, run.CreatedBy = id.NewShadowRunID(rid), id.NewComparatorID(cid), shadow.Mode(mode), id.NewUserID(by)
		run.WindowFrom, run.WindowTo, run.CreatedAt = run.WindowFrom.UTC(), run.WindowTo.UTC(), run.CreatedAt.UTC()
		run.Policy.Currency = cur
		if err := json.Unmarshal(mapping, &run.Policy.ComponentMap); err != nil {
			return nil, obligationFault(err, "A stored run's component map does not decode.")
		}
		for _, x := range []struct {
			n   Numeric
			dst *fiscal.Money
		}{{tol, &run.Policy.Tolerance}, {mat, &run.Policy.Materiality}, {compared, &m.ComparedValue}, {variance, &m.AbsoluteVariance}} {
			v, err := x.n.Money(cur)
			if err != nil {
				return nil, err
			}
			*x.dst = v
		}
		if run.InputHash, err = canonical.ParseDigest(hash); err != nil {
			return nil, obligationFault(err, "A stored run's input hash does not parse.")
		}
		run.Metrics = m
		out = append(out, run)
	}
	return out, mapError(rows.Err(), "read shadow runs")
}

// Discrepancies returns a run's discrepancies, or every one of the tenant.
func (r *ShadowRepo) Discrepancies(ctx context.Context, runID id.ShadowRunID) ([]shadow.Discrepancy, error) {
	if runID.IsZero() {
		return r.discrepancies(ctx, ``, nil)
	}
	return r.discrepancies(ctx, ` AND run_id = $2`, runID.UUID())
}

// Discrepancy returns one.
func (r *ShadowRepo) Discrepancy(ctx context.Context, discrepancyID id.DiscrepancyID) (shadow.Discrepancy, error) {
	got, err := r.discrepancies(ctx, ` AND discrepancy_id = $2`, discrepancyID.UUID())
	if err != nil {
		return shadow.Discrepancy{}, err
	}
	if len(got) == 0 {
		return shadow.Discrepancy{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such discrepancy.")
	}
	return got[0], nil
}

// AppendDiscrepancyEvent records one event.
func (r *ShadowRepo) AppendDiscrepancyEvent(ctx context.Context, discrepancyID id.DiscrepancyID, e shadow.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlDiscEventInsert, tenant.UUID(), discrepancyID.UUID(), e.Seq, string(e.State),
		optString(string(e.Severity)), optString(string(e.RootCause)), e.Note, e.RecordedAt.UTC(), e.RecordedBy.UUID())
	return mapError(err, "insert discrepancy event")
}

func (r *ShadowRepo) discrepancies(ctx context.Context, where string, arg any) ([]shadow.Discrepancy, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	args := []any{tenant.UUID()}
	if arg != nil {
		args = append(args, arg)
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, sqlDiscrepancySelect+where+` ORDER BY run_id, case_seq`, args...)
	if err != nil {
		return nil, mapError(err, "read discrepancies")
	}
	var out []shadow.Discrepancy
	for rows.Next() {
		var (
			d        = shadow.Discrepancy{TenantID: tenant}
			did, rid uuid.UUID
		)
		if err := rows.Scan(&did, &rid, &d.CaseSeq); err != nil {
			rows.Close()
			return nil, mapError(err, "scan discrepancy")
		}
		d.ID, d.Run = id.NewDiscrepancyID(did), id.NewShadowRunID(rid)
		out = append(out, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read discrepancies")
	}
	if len(out) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(out))
	for i, d := range out {
		ids[i] = d.ID.UUID()
	}
	rows, err = db.Query(ctx, sqlDiscEventSelect, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read discrepancy events")
	}
	defer rows.Close()
	events := map[id.DiscrepancyID][]shadow.Event{}
	for rows.Next() {
		var (
			e               shadow.Event
			did, by         uuid.UUID
			state           string
			severity, cause *string
			at              time.Time
		)
		if err := rows.Scan(&did, &e.Seq, &state, &severity, &cause, &e.Note, &at, &by); err != nil {
			return nil, mapError(err, "scan discrepancy event")
		}
		e.State, e.Severity, e.RootCause = shadow.State(state), shadow.Severity(deref(severity)), shadow.RootCause(deref(cause))
		e.RecordedAt, e.RecordedBy = at.UTC(), id.NewUserID(by)
		k := id.NewDiscrepancyID(did)
		events[k] = append(events[k], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read discrepancy events")
	}
	for i := range out {
		folded, err := shadow.FoldDiscrepancy(out[i], events[out[i].ID])
		if err != nil {
			return nil, obligationFault(err, "A stored discrepancy's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}
