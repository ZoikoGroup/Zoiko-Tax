package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/entitlement"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ontology"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subscription"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// entitlement_grant(_event), subscription_plan(_quota, _event) — migration
// 000039; and the usage meters' reads over 000003, 000016, 000022 and 000024
// ---------------------------------------------------------------------------

const (
	sqlGrantColumns = `entitlement_id, legal_entity_id, jurisdiction, service, mode, operation, effective_from,
		effective_to, contract_ref, contract_revision, intake_key, intake_hash, created_at, created_by`
	sqlGrantInsert = `INSERT INTO ztax.entitlement_grant (tenant_id, ` + sqlGrantColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`
	sqlGrantSelect = `SELECT ` + sqlGrantColumns + ` FROM ztax.entitlement_grant WHERE tenant_id = $1`

	sqlGrantEventColumns = `entitlement_id, seq, kind, lifecycle_after, reason, recorded_at, recorded_by`
	sqlGrantEventInsert  = `INSERT INTO ztax.entitlement_grant_event (tenant_id, ` + sqlGrantEventColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	sqlGrantEvents = `SELECT ` + sqlGrantEventColumns + ` FROM ztax.entitlement_grant_event
		WHERE tenant_id = $1 AND entitlement_id = ANY($2) ORDER BY entitlement_id, seq`

	sqlPlanColumns = `plan_id, plan_code, plan_name, contract_ref, term_start, term_end, intake_key, intake_hash,
		created_at, created_by`
	sqlPlanInsert = `INSERT INTO ztax.subscription_plan (tenant_id, ` + sqlPlanColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	sqlPlanSelect = `SELECT ` + sqlPlanColumns + ` FROM ztax.subscription_plan WHERE tenant_id = $1`

	sqlQuotaInsert = `INSERT INTO ztax.subscription_plan_quota (tenant_id, plan_id, meter, included, overage_policy)
		VALUES ($1, $2, $3, $4, $5)`
	sqlQuotas = `SELECT plan_id, meter, included, overage_policy FROM ztax.subscription_plan_quota
		WHERE tenant_id = $1 AND plan_id = ANY($2) ORDER BY plan_id, meter`

	sqlPlanEventColumns = `plan_id, seq, kind, status_after, reason, recorded_at, recorded_by`
	sqlPlanEventInsert  = `INSERT INTO ztax.subscription_plan_event (tenant_id, ` + sqlPlanEventColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	sqlPlanEvents = `SELECT ` + sqlPlanEventColumns + ` FROM ztax.subscription_plan_event
		WHERE tenant_id = $1 AND plan_id = ANY($2) ORDER BY plan_id, seq`

	sqlEntitlementLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	entitlementListCap = 500
)

// The predicates a read takes. Fixed strings from this file, never caller
// text.
const (
	whereGrantID     = ` AND entitlement_id = $2`
	whereGrantIntake = ` AND intake_key = $2`
	whereGrantRecent = ` ORDER BY created_at DESC, entitlement_id DESC LIMIT $2`
	wherePlanID      = ` AND plan_id = $2`
	wherePlanIntake  = ` AND intake_key = $2`
	wherePlanRecent  = ` ORDER BY created_at DESC, plan_id DESC LIMIT $2`
)

// EntitlementRepo implements port.EntitlementRepository.
type EntitlementRepo struct{ s *Store }

// Entitlements returns the entitlement and subscription plan repository.
func (s *Store) Entitlements() *EntitlementRepo { return &EntitlementRepo{s: s} }

var _ port.EntitlementRepository = (*EntitlementRepo)(nil)

// Lock takes one of the tenant's entitlement locks for the transaction.
func (r *EntitlementRepo) Lock(ctx context.Context, key string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlEntitlementLock, tenant.String()+"/entitlement/"+key)
	return mapError(err, "lock entitlement")
}

// CreateGrant writes a grant and its first events.
func (r *EntitlementRepo) CreateGrant(ctx context.Context, g entitlement.Grant, events []entitlement.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if g.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The grant belongs to a tenant outside the caller's scope.")
	}
	if err := g.Validate(); err != nil {
		return obligationFault(err, "The grant is not well-formed.")
	}
	if g.IntakeHash.IsZero() || len(events) == 0 || events[0].Kind != entitlement.EventCreated || events[0].Seq != 1 {
		return obligationFault(nil, "A grant is created by its CREATED event, with its intake hash.")
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlGrantInsert, tenant.UUID(), g.ID.UUID(), g.LegalEntity.UUID(), g.Jurisdiction,
		string(g.Service), string(g.Mode), string(g.Operation), civilDate(g.EffectiveFrom), optDate(g.EffectiveTo),
		g.ContractRef, optString(g.ContractRevision), g.IntakeKey, g.IntakeHash.String(), g.CreatedAt.UTC(),
		g.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert entitlement grant")
	}
	return r.AppendGrantEvents(ctx, g.ID, events)
}

// AppendGrantEvents writes events in order.
func (r *EntitlementRepo) AppendGrantEvents(ctx context.Context, grantID id.EntitlementID, events []entitlement.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	for _, e := range events {
		if _, err := r.s.db(ctx).Exec(ctx, sqlGrantEventInsert, tenant.UUID(), grantID.UUID(), e.Seq, string(e.Kind),
			string(e.Lifecycle), optString(e.Reason), e.RecordedAt.UTC(), e.RecordedBy.UUID()); err != nil {
			return mapError(err, "insert entitlement grant event")
		}
	}
	return nil
}

// Grant returns one grant.
func (r *EntitlementRepo) Grant(ctx context.Context, grantID id.EntitlementID) (entitlement.Grant, error) {
	got, err := r.readGrants(ctx, whereGrantID, grantID.UUID())
	if err != nil {
		return entitlement.Grant{}, err
	}
	if len(got) == 0 {
		return entitlement.Grant{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such entitlement.")
	}
	return got[0], nil
}

// GrantByIntakeKey returns the grant recorded under a key.
func (r *EntitlementRepo) GrantByIntakeKey(ctx context.Context, key string) (entitlement.Grant, bool, error) {
	got, err := r.readGrants(ctx, whereGrantIntake, key)
	if err != nil || len(got) == 0 {
		return entitlement.Grant{}, false, err
	}
	return got[0], true, nil
}

// Grants returns the most recent grants.
func (r *EntitlementRepo) Grants(ctx context.Context, limit int) ([]entitlement.Grant, error) {
	if limit <= 0 || limit > entitlementListCap {
		limit = entitlementListCap
	}
	return r.readGrants(ctx, whereGrantRecent, limit)
}

func (r *EntitlementRepo) readGrants(ctx context.Context, where string, args ...any) ([]entitlement.Grant, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, sqlGrantSelect+where, append([]any{tenant.UUID()}, args...)...)
	if err != nil {
		return nil, mapError(err, "read entitlement grants")
	}
	var out []entitlement.Grant
	for rows.Next() {
		var (
			g                              = entitlement.Grant{TenantID: tenant}
			gid, le, by                    uuid.UUID
			service, mode, operation, hash string
			from, to                       pgtype.Date
			revision                       *string
		)
		if err := rows.Scan(&gid, &le, &g.Jurisdiction, &service, &mode, &operation, &from, &to, &g.ContractRef, &revision,
			&g.IntakeKey, &hash, &g.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan entitlement grant")
		}
		g.ID, g.LegalEntity, g.CreatedBy = id.NewEntitlementID(gid), id.NewLegalEntityID(le), id.NewUserID(by)
		g.Service, g.Mode, g.Operation = ontology.Family(service), entitlement.Mode(mode), entitlement.Operation(operation)
		g.EffectiveFrom, g.ContractRevision, g.CreatedAt = inLocation(from, time.UTC), deref(revision), g.CreatedAt.UTC()
		if to.Valid {
			g.EffectiveTo = inLocation(to, time.UTC)
		}
		if g.IntakeHash, err = canonical.ParseDigest(hash); err != nil {
			rows.Close()
			return nil, obligationFault(err, "A stored grant's intake hash does not parse.")
		}
		out = append(out, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read entitlement grants")
	}
	if len(out) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, len(out))
	for i, g := range out {
		ids[i] = g.ID.UUID()
	}
	rows, err = db.Query(ctx, sqlGrantEvents, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read entitlement grant events")
	}
	defer rows.Close()
	events := map[id.EntitlementID][]entitlement.Event{}
	for rows.Next() {
		var (
			e               entitlement.Event
			gid, by         uuid.UUID
			kind, lifecycle string
			reason          *string
		)
		if err := rows.Scan(&gid, &e.Seq, &kind, &lifecycle, &reason, &e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan entitlement grant event")
		}
		e.Kind, e.Lifecycle, e.Reason = entitlement.EventKind(kind), entitlement.Lifecycle(lifecycle), deref(reason)
		e.RecordedAt, e.RecordedBy = e.RecordedAt.UTC(), id.NewUserID(by)
		k := id.NewEntitlementID(gid)
		events[k] = append(events[k], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read entitlement grant events")
	}
	for i := range out {
		folded, err := entitlement.Fold(out[i], events[out[i].ID])
		if err != nil {
			return nil, obligationFault(err, "A stored grant's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}

// CreatePlan writes a plan revision, its quotas and its first event.
func (r *EntitlementRepo) CreatePlan(ctx context.Context, p subscription.Plan, events []subscription.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if p.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The plan belongs to a tenant outside the caller's scope.")
	}
	if err := p.Validate(); err != nil {
		return obligationFault(err, "The plan is not well-formed.")
	}
	if p.IntakeHash.IsZero() || len(events) != 1 || events[0].Kind != subscription.EventProposed || events[0].Seq != 1 {
		return obligationFault(nil, "A plan is created by its PROPOSED event, with its intake hash.")
	}
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlPlanInsert, tenant.UUID(), p.ID.UUID(), p.PlanCode, p.PlanName, p.ContractRef,
		civilDate(p.TermStart), optDate(p.TermEnd), p.IntakeKey, p.IntakeHash.String(), p.CreatedAt.UTC(),
		p.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert subscription plan")
	}
	for _, q := range p.Quotas {
		if _, err := db.Exec(ctx, sqlQuotaInsert, tenant.UUID(), p.ID.UUID(), string(q.Meter), q.Included, string(q.Overage)); err != nil {
			return mapError(err, "insert subscription quota")
		}
	}
	return r.AppendPlanEvent(ctx, p.ID, events[0])
}

// AppendPlanEvent writes one plan event.
func (r *EntitlementRepo) AppendPlanEvent(ctx context.Context, planID id.SubscriptionPlanID, e subscription.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlPlanEventInsert, tenant.UUID(), planID.UUID(), e.Seq, string(e.Kind), string(e.Status),
		optString(e.Reason), e.RecordedAt.UTC(), e.RecordedBy.UUID())
	return mapError(err, "insert subscription plan event")
}

// Plan returns one plan revision.
func (r *EntitlementRepo) Plan(ctx context.Context, planID id.SubscriptionPlanID) (subscription.Plan, error) {
	got, err := r.readPlans(ctx, wherePlanID, planID.UUID())
	if err != nil {
		return subscription.Plan{}, err
	}
	if len(got) == 0 {
		return subscription.Plan{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such subscription plan.")
	}
	return got[0], nil
}

// PlanByIntakeKey returns the plan revision proposed under a key.
func (r *EntitlementRepo) PlanByIntakeKey(ctx context.Context, key string) (subscription.Plan, bool, error) {
	got, err := r.readPlans(ctx, wherePlanIntake, key)
	if err != nil || len(got) == 0 {
		return subscription.Plan{}, false, err
	}
	return got[0], true, nil
}

// Plans returns the most recent plan revisions.
func (r *EntitlementRepo) Plans(ctx context.Context, limit int) ([]subscription.Plan, error) {
	if limit <= 0 || limit > entitlementListCap {
		limit = entitlementListCap
	}
	return r.readPlans(ctx, wherePlanRecent, limit)
}

func (r *EntitlementRepo) readPlans(ctx context.Context, where string, args ...any) ([]subscription.Plan, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, sqlPlanSelect+where, append([]any{tenant.UUID()}, args...)...)
	if err != nil {
		return nil, mapError(err, "read subscription plans")
	}
	var out []subscription.Plan
	for rows.Next() {
		var (
			p        = subscription.Plan{TenantID: tenant}
			pid, by  uuid.UUID
			hash     string
			from, to pgtype.Date
		)
		if err := rows.Scan(&pid, &p.PlanCode, &p.PlanName, &p.ContractRef, &from, &to, &p.IntakeKey, &hash,
			&p.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan subscription plan")
		}
		p.ID, p.CreatedBy, p.CreatedAt, p.TermStart = id.NewSubscriptionPlanID(pid), id.NewUserID(by), p.CreatedAt.UTC(), inLocation(from, time.UTC)
		if to.Valid {
			p.TermEnd = inLocation(to, time.UTC)
		}
		if p.IntakeHash, err = canonical.ParseDigest(hash); err != nil {
			rows.Close()
			return nil, obligationFault(err, "A stored plan's intake hash does not parse.")
		}
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read subscription plans")
	}
	if len(out) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, len(out))
	index := map[id.SubscriptionPlanID]int{}
	for i, p := range out {
		ids[i], index[p.ID] = p.ID.UUID(), i
	}
	if err := r.readQuotas(ctx, tenant.UUID(), ids, out, index); err != nil {
		return nil, err
	}
	rows, err = db.Query(ctx, sqlPlanEvents, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read subscription plan events")
	}
	defer rows.Close()
	events := map[id.SubscriptionPlanID][]subscription.Event{}
	for rows.Next() {
		var (
			e            subscription.Event
			pid, by      uuid.UUID
			kind, status string
			reason       *string
		)
		if err := rows.Scan(&pid, &e.Seq, &kind, &status, &reason, &e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan subscription plan event")
		}
		e.Kind, e.Status, e.Reason = subscription.EventKind(kind), subscription.Status(status), deref(reason)
		e.RecordedAt, e.RecordedBy = e.RecordedAt.UTC(), id.NewUserID(by)
		k := id.NewSubscriptionPlanID(pid)
		events[k] = append(events[k], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read subscription plan events")
	}
	for i := range out {
		folded, err := subscription.Fold(out[i], events[out[i].ID])
		if err != nil {
			return nil, obligationFault(err, "A stored plan's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}

func (r *EntitlementRepo) readQuotas(ctx context.Context, tenant uuid.UUID, ids []uuid.UUID, out []subscription.Plan,
	index map[id.SubscriptionPlanID]int) error {
	rows, err := r.s.db(ctx).Query(ctx, sqlQuotas, tenant, ids)
	if err != nil {
		return mapError(err, "read subscription quotas")
	}
	defer rows.Close()
	for rows.Next() {
		var (
			pid            uuid.UUID
			meter, overage string
			included       int64
		)
		if err := rows.Scan(&pid, &meter, &included, &overage); err != nil {
			return mapError(err, "scan subscription quota")
		}
		i := index[id.NewSubscriptionPlanID(pid)]
		out[i].Quotas = append(out[i].Quotas, subscription.Quota{Meter: subscription.Meter(meter), Included: included,
			Overage: subscription.OveragePolicy(overage)})
	}
	return mapError(rows.Err(), "read subscription quotas")
}

// ---------------------------------------------------------------------------
// usage
// ---------------------------------------------------------------------------

// meterSources are the rows each meter counts: an identifier and the
// instant that places a row in a period, from the tenant's own table. The
// identifiers are ordered bytewise (COLLATE "C"), which is the order
// subscription.BasisDigest sorts them in, so the digest computed here is the
// one a client computes from the listed rows.
var meterSources = map[subscription.Meter]string{
	subscription.MeterCommittedDecisions: `SELECT decision_id::text AS id, recorded_at AS at FROM ztax.tax_decision
		WHERE tenant_id = $1 AND recorded_at >= $2 AND recorded_at < $3`,
	subscription.MeterFiscalDocuments: `SELECT document_id::text AS id, recorded_at AS at FROM ztax.fiscal_document
		WHERE tenant_id = $1 AND recorded_at >= $2 AND recorded_at < $3`,
	subscription.MeterFilingSubmissions: `SELECT submission_id::text AS id, recorded_at AS at FROM ztax.compliance_step
		WHERE tenant_id = $1 AND kind = 'SUBMISSION_STARTED' AND recorded_at >= $2 AND recorded_at < $3`,
	subscription.MeterShadowRuns: `SELECT run_id::text AS id, created_at AS at FROM ztax.shadow_run
		WHERE tenant_id = $1 AND created_at >= $2 AND created_at < $3`,
}

// sqlMeterBasis is one statement, so the count, the digest and the listed
// rows are one snapshot. It always returns a row: with nothing counted, the
// count is zero, the digest is the empty string's and the row's id is null.
func meterBasisSQL(source string) string {
	return `WITH r AS (` + source + `),
agg AS (
  SELECT count(*) AS n,
    encode(sha256(convert_to(coalesce(string_agg(r.id, E'\n' ORDER BY r.id COLLATE "C"), ''), 'UTF8')), 'hex') AS d
  FROM r
)
SELECT agg.n, agg.d, p.id, p.at FROM agg
LEFT JOIN LATERAL (SELECT r.id, r.at FROM r ORDER BY r.id COLLATE "C" LIMIT $4) p ON true`
}

// UsageRepo implements port.UsageRepository.
type UsageRepo struct{ s *Store }

// Usage returns the usage meters' read repository.
func (s *Store) Usage() *UsageRepo { return &UsageRepo{s: s} }

var _ port.UsageRepository = (*UsageRepo)(nil)

// Counts counts every meter over [from, to).
func (r *UsageRepo) Counts(ctx context.Context, from, to time.Time) ([]subscription.Count, error) {
	out := make([]subscription.Count, 0, len(subscription.Definitions))
	for _, d := range subscription.Definitions {
		c, _, err := r.Basis(ctx, d.Meter, from, to, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Basis counts one meter and lists its first rows.
func (r *UsageRepo) Basis(ctx context.Context, m subscription.Meter, from, to time.Time, limit int) (subscription.Count, []port.UsageBasisRow, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return subscription.Count{}, nil, err
	}
	source, ok := meterSources[m]
	if !ok {
		return subscription.Count{}, nil, errs.Invalid("meter", errs.ReasonInvalidValue, "That is not a meter.")
	}
	if limit < 0 {
		limit = 0
	}
	rows, err := r.s.db(ctx).Query(ctx, meterBasisSQL(source), tenant.UUID(), from.UTC(), to.UTC(), limit)
	if err != nil {
		return subscription.Count{}, nil, mapError(err, "count usage")
	}
	defer rows.Close()
	out := subscription.Count{Meter: m}
	list := []port.UsageBasisRow{}
	for rows.Next() {
		var (
			rowID *string
			at    *time.Time
		)
		if err := rows.Scan(&out.Count, &out.Digest, &rowID, &at); err != nil {
			return subscription.Count{}, nil, mapError(err, "scan usage")
		}
		if rowID != nil && at != nil {
			list = append(list, port.UsageBasisRow{ID: *rowID, At: at.UTC()})
		}
	}
	return out, list, mapError(rows.Err(), "count usage")
}
