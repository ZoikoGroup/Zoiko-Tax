package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// obligation and obligation_contribution — migrations 000003, 000010, 000012
// ---------------------------------------------------------------------------

const (
	sqlObligationColumns = `o.tenant_id, o.obligation_id, o.business_key, o.recorded_at, o.supersedes_id,
		o.jurisdiction_id, o.obligation_type, o.period_start, o.period_end, o.due_date, o.status,
		o.currency, o.assessed_amount, o.legal_entity_id, o.authority, o.definition_id,
		o.definition_version, o.bundle_id, o.bundle_digest, o.duty, o.legal_timezone, o.reason_code,
		o.recorded_by`
	sqlObligationInsert = `INSERT INTO ztax.obligation
		(tenant_id, obligation_id, business_key, valid_from, valid_to, recorded_at, supersedes_id,
		 jurisdiction_id, obligation_type, period_start, period_end, due_date, status, currency,
		 assessed_amount, legal_entity_id, authority, definition_id, definition_version, bundle_id,
		 bundle_digest, duty, legal_timezone, reason_code, recorded_by)
		VALUES ($1, $2, $3, $4, NULL, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
		        $17, $18, $19, $20, $21, $22, $23)`
	// The current row of a chain is the one nothing supersedes. The chain
	// indexes of 000012 make that exactly one row per business key.
	sqlObligationCurrentPredicate = `NOT EXISTS (SELECT 1 FROM ztax.obligation s
		WHERE s.tenant_id = o.tenant_id AND s.supersedes_id = o.obligation_id)`
	sqlObligationCurrent = `SELECT ` + sqlObligationColumns + ` FROM ztax.obligation o
		WHERE o.tenant_id = $1 AND o.business_key = $2 AND ` + sqlObligationCurrentPredicate
	sqlObligationByID = `SELECT ` + sqlObligationColumns + ` FROM ztax.obligation o
		WHERE o.tenant_id = $1 AND o.obligation_id = $2`
	sqlObligationList = `SELECT ` + sqlObligationColumns + ` FROM ztax.obligation o
		WHERE o.tenant_id = $1 AND ($2 = '' OR o.status = $2) AND ` + sqlObligationCurrentPredicate + `
		ORDER BY o.due_date, o.business_key LIMIT $3`
	// A transaction-scoped advisory lock: an obligation that does not exist
	// yet has no row to lock, and its first two writers must still serialize.
	// A hash collision between two keys only makes them wait for each other.
	sqlObligationLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	sqlContributionAdd = `INSERT INTO ztax.obligation_contribution
		(tenant_id, business_key, decision_id, kind, amount, currency, recorded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant_id, business_key, decision_id, kind) DO NOTHING`
	sqlContributionGet = `SELECT amount, currency FROM ztax.obligation_contribution
		WHERE tenant_id = $1 AND business_key = $2 AND decision_id = $3 AND kind = $4`
	sqlContributionSum = `SELECT business_key, currency, SUM(amount) FROM ztax.obligation_contribution
		WHERE tenant_id = $1 AND business_key = ANY($2) GROUP BY business_key, currency`
)

// listCap bounds a listing whatever the caller asks for.
const obligationListCap = 500

// ObligationRepo implements port.ObligationRepository.
type ObligationRepo struct{ s *Store }

// Obligations returns the obligation repository.
func (s *Store) Obligations() *ObligationRepo { return &ObligationRepo{s: s} }

var _ port.ObligationRepository = (*ObligationRepo)(nil)

// Lock takes the business key's advisory lock until the transaction ends.
func (r *ObligationRepo) Lock(ctx context.Context, businessKey string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlObligationLock, tenant.String()+"/obligation/"+businessKey)
	return mapError(err, "lock obligation")
}

// Current returns the newest row of a business key.
func (r *ObligationRepo) Current(ctx context.Context, businessKey string) (obligation.Obligation, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return obligation.Obligation{}, err
	}
	return scanObligation(r.s.db(ctx).QueryRow(ctx, sqlObligationCurrent, tenant.UUID(), businessKey))
}

// ByID returns one row.
func (r *ObligationRepo) ByID(ctx context.Context, obligationID id.ObligationID) (obligation.Obligation, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return obligation.Obligation{}, err
	}
	return scanObligation(r.s.db(ctx).QueryRow(ctx, sqlObligationByID, tenant.UUID(), obligationID.UUID()))
}

// List returns the current row of each chain, by due date.
func (r *ObligationRepo) List(ctx context.Context, f port.ObligationFilter) ([]obligation.Obligation, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 || limit > obligationListCap {
		limit = obligationListCap
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlObligationList, tenant.UUID(), string(f.Status), limit)
	if err != nil {
		return nil, mapError(err, "list obligations")
	}
	defer rows.Close()
	var out []obligation.Obligation
	for rows.Next() {
		o, err := scanObligation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, mapError(rows.Err(), "list obligations")
}

// Append writes a row. A unique violation on the chain indexes means the
// writer's view of the chain is stale.
func (r *ObligationRepo) Append(ctx context.Context, o obligation.Obligation) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if o.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The obligation belongs to a tenant outside the caller's scope.")
	}
	if err := o.Validate(); err != nil {
		return obligationFault(err, "The obligation is not well-formed.")
	}
	if o.Assessed == nil {
		return obligationFault(nil, "An assessed obligation row carries its amount.")
	}
	amount, err := MoneyValue(*o.Assessed)
	if err != nil {
		return err
	}
	var recordedBy *uuid.UUID
	if !o.RecordedBy.IsZero() {
		u := o.RecordedBy.UUID()
		recordedBy = &u
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlObligationInsert,
		tenant.UUID(), o.ID.UUID(), o.BusinessKey, o.RecordedAt.UTC(), optUUID(o.Supersedes),
		o.JurisdictionID, o.Type, civilDate(o.Period.Start), civilDate(o.Period.End), civilDate(o.Period.Due),
		string(o.Status), string(o.Assessed.Currency()), amount, o.LegalEntity.UUID(), o.Authority,
		o.Definition.ID, o.Definition.Version, o.Content.BundleID, o.Content.BundleDigest, string(o.Duty),
		o.Timezone, optString(string(o.Reason)), recordedBy)
	if err != nil {
		mapped := mapError(err, "insert obligation")
		if errs.ReasonOf(mapped) == errs.ReasonAlreadyExists {
			return errs.Wrap(err, errs.CategoryConflict, errs.ReasonOptimisticConflict,
				"The obligation changed since it was read; read it again and retry.")
		}
		return mapped
	}
	return nil
}

// Contribute records a contribution once per decision, key and kind.
func (r *ObligationRepo) Contribute(ctx context.Context, c port.ObligationContribution) (bool, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	amount, err := MoneyValue(c.Amount)
	if err != nil {
		return false, err
	}
	tag, err := r.s.db(ctx).Exec(ctx, sqlContributionAdd, tenant.UUID(), c.BusinessKey, c.DecisionID.UUID(),
		string(c.Kind), amount, string(c.Amount.Currency()), c.RecordedAt.UTC())
	if err != nil {
		return false, mapError(err, "insert obligation contribution")
	}
	return tag.RowsAffected() == 1, nil
}

// Contribution returns one recorded contribution.
func (r *ObligationRepo) Contribution(ctx context.Context, businessKey string, decisionID id.DecisionID, kind port.ContributionKind) (fiscal.Money, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return fiscal.Money{}, err
	}
	var (
		amount   Numeric
		currency string
	)
	if err := r.s.db(ctx).QueryRow(ctx, sqlContributionGet, tenant.UUID(), businessKey, decisionID.UUID(), string(kind)).
		Scan(&amount, &currency); err != nil {
		return fiscal.Money{}, mapError(err, "read obligation contribution")
	}
	return amount.Money(fiscal.Currency(currency))
}

// Assessed sums each key's contributions.
func (r *ObligationRepo) Assessed(ctx context.Context, businessKeys []string) (map[string]fiscal.Money, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]fiscal.Money{}
	if len(businessKeys) == 0 {
		return out, nil
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlContributionSum, tenant.UUID(), businessKeys)
	if err != nil {
		return nil, mapError(err, "sum obligation contributions")
	}
	defer rows.Close()
	for rows.Next() {
		var (
			key, currency string
			sum           Numeric
		)
		if err := rows.Scan(&key, &currency, &sum); err != nil {
			return nil, mapError(err, "scan obligation assessment")
		}
		if _, dup := out[key]; dup {
			// One obligation is assessed in one currency; two means a writer
			// skipped the currency check, and no sum of them is a figure.
			return nil, obligationFault(nil, "An obligation's contributions are in more than one currency.")
		}
		m, err := sum.Money(fiscal.Currency(currency))
		if err != nil {
			return nil, err
		}
		out[key] = m
	}
	return out, mapError(rows.Err(), "sum obligation contributions")
}

func scanObligation(row pgx.Row) (obligation.Obligation, error) {
	var (
		tenantUUID, obligationUUID             uuid.UUID
		supersedes, legalEntity, recordedBy    *uuid.UUID
		start, end, due                        pgtype.Date
		status, currency                       string
		assessed                               Numeric
		authority, defID, defVersion, bundleID *string
		bundleDigest, duty, timezone, reason   *string
		o                                      obligation.Obligation
	)
	if err := row.Scan(&tenantUUID, &obligationUUID, &o.BusinessKey, &o.RecordedAt, &supersedes,
		&o.JurisdictionID, &o.Type, &start, &end, &due, &status, &currency, &assessed, &legalEntity,
		&authority, &defID, &defVersion, &bundleID, &bundleDigest, &duty, &timezone, &reason,
		&recordedBy); err != nil {
		return obligation.Obligation{}, mapError(err, "scan obligation")
	}
	o.TenantID = id.NewTenantID(tenantUUID)
	o.ID = id.NewObligationID(obligationUUID)
	o.RecordedAt = o.RecordedAt.UTC()
	o.Status = obligation.Status(status)
	if supersedes != nil {
		s := id.NewObligationID(*supersedes)
		o.Supersedes = &s
	}
	if legalEntity != nil {
		o.LegalEntity = id.NewLegalEntityID(*legalEntity)
	}
	if recordedBy != nil {
		o.RecordedBy = id.NewUserID(*recordedBy)
	}
	o.Authority = deref(authority)
	o.Definition = obligation.DefinitionRef{ID: deref(defID), Version: deref(defVersion)}
	o.Content = obligation.ContentRef{BundleID: deref(bundleID), BundleDigest: deref(bundleDigest)}
	o.Duty = obligation.DutyKind(deref(duty))
	o.Timezone = deref(timezone)
	o.Reason = errs.ReasonCode(deref(reason))
	if assessed.Valid {
		m, err := assessed.Money(fiscal.Currency(currency))
		if err != nil {
			return obligation.Obligation{}, err
		}
		o.Assessed = &m
	}

	// The dates are civil dates in the legal calendar; they come back as
	// midnight there, which is what they were computed as.
	loc := time.UTC
	if o.Timezone != "" {
		l, err := time.LoadLocation(o.Timezone)
		if err != nil {
			return obligation.Obligation{}, obligationFault(err, "A stored obligation names a timezone this build does not know.")
		}
		loc = l
	}
	o.Period = obligation.Period{Start: inLocation(start, loc), End: inLocation(end, loc), Due: inLocation(due, loc)}
	o.Due = obligation.DueDates{Legal: o.Period.Due}
	return o, nil
}

// civilDate is the civil date of t in its own location.
func civilDate(t time.Time) pgtype.Date {
	return pgtype.Date{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}

func inLocation(d pgtype.Date, loc *time.Location) time.Time {
	return time.Date(d.Time.Year(), d.Time.Month(), d.Time.Day(), 0, 0, 0, 0, loc)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// obligationFault is an internal error: a row this package was handed or read
// that no correct writer produces.
func obligationFault(err error, msg string) error {
	if err == nil {
		return errs.New(errs.CategoryInternal, errs.ReasonInternal, msg)
	}
	return errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, msg)
}
