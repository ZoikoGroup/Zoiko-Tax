package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/contribution"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// provider_contribution(_event) — migration 000031
// ---------------------------------------------------------------------------

const (
	sqlProviderContributionColumns = `contribution_id, source_event_id, source_version, source_type, source_checksum, regime, period,
		legal_entity_id, obligation_id, classification_ref, situs_ref, source_ref, component_ref, rule_version, currency,
		eligible_base, excluded_base, exclusion_reason, quantity, quantity_unit, reverses_source_event_id,
		reverses_source_version, amendment, immutable_hash, recorded_at, recorded_by`
	sqlProviderContributionInsert = `INSERT INTO ztax.provider_contribution (tenant_id, ` + sqlProviderContributionColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22,
		$23, $24, $25, $26, $27)`
	sqlProviderContributionSelect = `SELECT ` + sqlProviderContributionColumns + ` FROM ztax.provider_contribution c WHERE c.tenant_id = $1`

	sqlProviderContributionByID     = sqlProviderContributionSelect + ` AND c.contribution_id = $2`
	sqlProviderContributionBySource = sqlProviderContributionSelect + ` AND c.source_event_id = $2 AND c.source_version = $3`
	sqlProviderContributionReversal = sqlProviderContributionSelect + ` AND c.reverses_source_event_id = $2 AND c.reverses_source_version = $3`
	sqlProviderContributionInPeriod = sqlProviderContributionSelect + ` AND c.period = $2 ORDER BY c.recorded_at, c.contribution_id`
	// Every filter is a parameter; an empty one matches everything. The
	// status filter reads the latest status event, which is the folded
	// status, so the limit applies after it rather than before.
	sqlProviderContributionList = sqlProviderContributionSelect + `
		AND ($2 = '' OR c.period = $2)
		AND ($3 = '' OR c.regime = $3)
		AND ($4 = '' OR c.source_type = $4)
		AND ($5::uuid IS NULL OR c.legal_entity_id = $5)
		AND ($6 = '' OR (SELECT e.status_after FROM ztax.provider_contribution_event e
			WHERE e.tenant_id = c.tenant_id AND e.contribution_id = c.contribution_id
			ORDER BY e.seq DESC LIMIT 1) = $6)
		ORDER BY c.recorded_at DESC, c.contribution_id DESC LIMIT $7`
	sqlProviderContributionKnown = `SELECT source_event_id, source_version FROM ztax.provider_contribution
		WHERE tenant_id = $1 AND (source_event_id, source_version) IN (SELECT * FROM unnest($2::text[], $3::integer[]))`

	sqlProviderContributionEventInsert = `INSERT INTO ztax.provider_contribution_event
		(tenant_id, contribution_id, seq, kind, status_after, reason, recorded_at, recorded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	sqlProviderContributionEvents = `SELECT contribution_id, seq, kind, status_after, reason, recorded_at, recorded_by
		FROM ztax.provider_contribution_event WHERE tenant_id = $1 AND contribution_id = ANY($2) ORDER BY contribution_id, seq`

	sqlProviderContributionLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	contributionListCap = 500
)

// ContributionRepo implements port.ProviderContributionRepository.
type ContributionRepo struct{ s *Store }

// ProviderContributions returns the provider contribution repository.
func (s *Store) ProviderContributions() *ContributionRepo { return &ContributionRepo{s: s} }

var _ port.ProviderContributionRepository = (*ContributionRepo)(nil)

// Lock takes one of the tenant's contribution locks for the transaction.
func (r *ContributionRepo) Lock(ctx context.Context, key string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlProviderContributionLock, tenant.String()+"/contribution/"+key)
	return mapError(err, "lock contribution")
}

// Record writes a contribution and its RECORDED event.
func (r *ContributionRepo) Record(ctx context.Context, c contribution.Contribution, recorded contribution.StatusEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if c.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The contribution belongs to a tenant outside the caller's scope.")
	}
	if err := c.Validate(); err != nil {
		return obligationFault(err, "The contribution is not well-formed.")
	}
	if c.Hash.IsZero() {
		return obligationFault(nil, "A contribution is stored with its immutable hash.")
	}
	if recorded.Kind != contribution.EventRecorded || recorded.Seq != 1 {
		return obligationFault(nil, "A contribution is created by its RECORDED event.")
	}
	eligible, err := MoneyValue(c.EligibleBase)
	if err != nil {
		return err
	}
	excluded, err := MoneyValue(c.ExcludedBase)
	if err != nil {
		return err
	}
	var (
		obligation  *uuid.UUID
		quantity    Numeric
		unit        *string
		revID       *string
		revVersion  *int
		sourceCheck = optString(c.SourceChecksum)
	)
	if !c.Obligation.IsZero() {
		u := c.Obligation.UUID()
		obligation = &u
	}
	if c.Quantity != nil {
		if quantity, err = QuantityValue(*c.Quantity); err != nil {
			return err
		}
		u := string(c.Quantity.Unit())
		unit = &u
	}
	if c.Reverses != nil {
		eid, v := c.Reverses.EventID, c.Reverses.Version
		revID, revVersion = &eid, &v
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlProviderContributionInsert, tenant.UUID(), c.ID.UUID(), c.Source.EventID, c.Source.Version,
		string(c.SourceType), sourceCheck, string(c.Regime), c.Period, c.LegalEntity.UUID(), obligation, c.ClassificationRef,
		c.SitusRef, c.SourceRef, optString(c.ComponentRef), c.RuleVersion, string(c.Currency()), eligible, excluded,
		optString(c.ExclusionReason), quantity, unit, revID, revVersion, c.Amendment, c.Hash.String(),
		c.RecordedAt.UTC(), c.RecordedBy.UUID()); err != nil {
		return mapError(err, "insert provider contribution")
	}
	return r.AppendEvent(ctx, c.ID, recorded)
}

// AppendEvent writes one status event.
func (r *ContributionRepo) AppendEvent(ctx context.Context, contributionID id.ContributionID, e contribution.StatusEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlProviderContributionEventInsert, tenant.UUID(), contributionID.UUID(), e.Seq, string(e.Kind),
		string(e.Status), optString(e.Reason), e.RecordedAt.UTC(), e.RecordedBy.UUID())
	return mapError(err, "insert provider contribution event")
}

// Contribution returns one contribution.
func (r *ContributionRepo) Contribution(ctx context.Context, contributionID id.ContributionID) (contribution.Contribution, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return contribution.Contribution{}, err
	}
	got, err := r.load(ctx, sqlProviderContributionByID, tenant.UUID(), contributionID.UUID())
	if err != nil {
		return contribution.Contribution{}, err
	}
	if len(got) == 0 {
		return contribution.Contribution{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such contribution.")
	}
	return got[0], nil
}

// BySource returns the contribution recorded for a source key.
func (r *ContributionRepo) BySource(ctx context.Context, key contribution.SourceKey) (contribution.Contribution, bool, error) {
	return r.one(ctx, sqlProviderContributionBySource, key)
}

// ReversalOf returns the contribution reversing a source key.
func (r *ContributionRepo) ReversalOf(ctx context.Context, key contribution.SourceKey) (contribution.Contribution, bool, error) {
	return r.one(ctx, sqlProviderContributionReversal, key)
}

func (r *ContributionRepo) one(ctx context.Context, query string, key contribution.SourceKey) (contribution.Contribution, bool, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return contribution.Contribution{}, false, err
	}
	got, err := r.load(ctx, query, tenant.UUID(), key.EventID, key.Version)
	if err != nil || len(got) == 0 {
		return contribution.Contribution{}, false, err
	}
	return got[0], true, nil
}

// List returns the contributions a filter selects, newest first.
func (r *ContributionRepo) List(ctx context.Context, f contribution.Filter) ([]contribution.Contribution, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 || limit > contributionListCap {
		limit = contributionListCap
	}
	var entity *uuid.UUID
	if !f.LegalEntity.IsZero() {
		u := f.LegalEntity.UUID()
		entity = &u
	}
	return r.load(ctx, sqlProviderContributionList, tenant.UUID(), f.Period, string(f.Regime), string(f.SourceType), entity,
		string(f.Status), limit)
}

// InPeriod returns every contribution of a period.
func (r *ContributionRepo) InPeriod(ctx context.Context, period string) ([]contribution.Contribution, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return r.load(ctx, sqlProviderContributionInPeriod, tenant.UUID(), period)
}

// KnownSources reports which keys the ledger holds.
func (r *ContributionRepo) KnownSources(ctx context.Context, keys []contribution.SourceKey) (map[contribution.SourceKey]bool, error) {
	out := map[contribution.SourceKey]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	ids, versions := make([]string, len(keys)), make([]int32, len(keys))
	for i, k := range keys {
		ids[i], versions[i] = k.EventID, saturateInt32(k.Version)
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlProviderContributionKnown, tenant.UUID(), ids, versions)
	if err != nil {
		return nil, mapError(err, "read known contribution sources")
	}
	defer rows.Close()
	for rows.Next() {
		var k contribution.SourceKey
		if err := rows.Scan(&k.EventID, &k.Version); err != nil {
			return nil, mapError(err, "scan known contribution source")
		}
		out[k] = true
	}
	return out, mapError(rows.Err(), "read known contribution sources")
}

func saturateInt32(n int) int32 {
	switch {
	case n > 1<<31-1:
		return 1<<31 - 1
	case n < 0:
		return 0
	default:
		return int32(n)
	}
}

// load reads the contributions a fixed query from this file selects, and
// folds each with its status events.
func (r *ContributionRepo) load(ctx context.Context, query string, args ...any) ([]contribution.Contribution, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, mapError(err, "read provider contributions")
	}
	var out []contribution.Contribution
	for rows.Next() {
		var (
			c                                        = contribution.Contribution{TenantID: tenant}
			cid, entity, by                          uuid.UUID
			obligation                               *uuid.UUID
			sourceType, regime, currency, hash       string
			checksum, component, reason, unit, revID *string
			revVersion                               *int
			eligible, excluded, quantity             Numeric
		)
		if err := rows.Scan(&cid, &c.Source.EventID, &c.Source.Version, &sourceType, &checksum, &regime, &c.Period, &entity,
			&obligation, &c.ClassificationRef, &c.SitusRef, &c.SourceRef, &component, &c.RuleVersion, &currency, &eligible,
			&excluded, &reason, &quantity, &unit, &revID, &revVersion, &c.Amendment, &hash, &c.RecordedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan provider contribution")
		}
		cur := fiscal.Currency(currency)
		if c.EligibleBase, err = eligible.Money(cur); err != nil {
			rows.Close()
			return nil, err
		}
		if c.ExcludedBase, err = excluded.Money(cur); err != nil {
			rows.Close()
			return nil, err
		}
		if quantity.Valid {
			q, err := quantity.Quantity(fiscal.Unit(deref(unit)))
			if err != nil {
				rows.Close()
				return nil, err
			}
			c.Quantity = &q
		}
		if c.Hash, err = canonical.ParseDigest(hash); err != nil {
			rows.Close()
			return nil, obligationFault(err, "A stored contribution's immutable hash does not parse.")
		}
		c.ID, c.LegalEntity, c.RecordedBy = id.NewContributionID(cid), id.NewLegalEntityID(entity), id.NewUserID(by)
		c.SourceType, c.Regime, c.RecordedAt = contribution.SourceType(sourceType), contribution.Regime(regime), c.RecordedAt.UTC()
		c.SourceChecksum, c.ComponentRef, c.ExclusionReason = deref(checksum), deref(component), deref(reason)
		if obligation != nil {
			c.Obligation = id.NewObligationID(*obligation)
		}
		if revID != nil && revVersion != nil {
			c.Reverses = &contribution.SourceKey{EventID: *revID, Version: *revVersion}
		}
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read provider contributions")
	}
	if len(out) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, len(out))
	for i, c := range out {
		ids[i] = c.ID.UUID()
	}
	rows, err = db.Query(ctx, sqlProviderContributionEvents, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read provider contribution events")
	}
	defer rows.Close()
	events := map[id.ContributionID][]contribution.StatusEvent{}
	for rows.Next() {
		var (
			e            contribution.StatusEvent
			cid, by      uuid.UUID
			kind, status string
			reason       *string
		)
		if err := rows.Scan(&cid, &e.Seq, &kind, &status, &reason, &e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan provider contribution event")
		}
		e.Kind, e.Status, e.Reason = contribution.EventKind(kind), contribution.Status(status), deref(reason)
		e.RecordedAt, e.RecordedBy = e.RecordedAt.UTC(), id.NewUserID(by)
		k := id.NewContributionID(cid)
		events[k] = append(events[k], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read provider contribution events")
	}
	for i := range out {
		folded, err := contribution.Fold(out[i], events[out[i].ID])
		if err != nil {
			return nil, obligationFault(err, "A stored contribution's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}
