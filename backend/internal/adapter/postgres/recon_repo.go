package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/reconciliation"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// recon_run, recon_item, recon_resolution — migration 000018
// ---------------------------------------------------------------------------

const (
	sqlReconRunInsert = `INSERT INTO ztax.recon_run
		(tenant_id, run_id, legal_entity_id, legal_period, ran_at, ran_by, first_break, unavailable)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	sqlReconItemInsert = `INSERT INTO ztax.recon_item
		(tenant_id, item_id, run_id, ordinal, stage, match_key, currency, expected, observed, variance, status, root_cause, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`
	sqlReconRunByID = `SELECT run_id, legal_entity_id, legal_period, ran_at, ran_by, first_break, unavailable
		FROM ztax.recon_run WHERE tenant_id = $1 AND run_id = $2`
	sqlReconItemColumns = `item_id, run_id, stage, match_key, currency, expected, observed, variance, status, root_cause, detail`
	sqlReconItems       = `SELECT ` + sqlReconItemColumns + ` FROM ztax.recon_item WHERE tenant_id = $1 AND run_id = $2 ORDER BY ordinal`
	sqlReconItemByID    = `SELECT ` + sqlReconItemColumns + ` FROM ztax.recon_item WHERE tenant_id = $1 AND item_id = $2`
	sqlReconResolutions = `SELECT r.item_id, r.actor, r.resolver, r.reason, r.action, r.root_cause, r.evidence, r.resolved_at
		FROM ztax.recon_resolution r JOIN ztax.recon_item i ON i.tenant_id = r.tenant_id AND i.item_id = r.item_id
		WHERE r.tenant_id = $1 AND i.run_id = $2`
	sqlReconResolve = `INSERT INTO ztax.recon_resolution
		(tenant_id, item_id, actor, resolver, reason, action, root_cause, evidence, resolved_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`
)

// ReconRepo implements port.ReconciliationRepository.
type ReconRepo struct{ s *Store }

// Reconciliations returns the reconciliation repository.
func (s *Store) Reconciliations() *ReconRepo { return &ReconRepo{s: s} }

var _ port.ReconciliationRepository = (*ReconRepo)(nil)

func optMoney(m *fiscal.Money) (*Numeric, error) {
	if m == nil {
		return nil, nil
	}
	v, err := MoneyValue(*m)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// CreateRun writes a run and its items in the caller's transaction.
func (r *ReconRepo) CreateRun(ctx context.Context, run reconciliation.Run, items []reconciliation.RunItem) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if run.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The run belongs to a tenant outside the caller's scope.")
	}
	var firstBreak *string
	if run.FirstBreak != nil {
		s := string(*run.FirstBreak)
		firstBreak = &s
	}
	unavailable := make([]string, len(run.Unavailable))
	for i, s := range run.Unavailable {
		unavailable[i] = string(s)
	}
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlReconRunInsert, tenant.UUID(), run.ID.UUID(), run.LegalEntity.UUID(), run.Period,
		run.RanAt.UTC(), optUser(run.RanBy), firstBreak, unavailable); err != nil {
		return mapError(err, "insert reconciliation run")
	}
	for i, it := range items {
		var currency *string
		for _, m := range []*fiscal.Money{it.Expected, it.Observed} {
			if m != nil {
				c := string(m.Currency())
				currency = &c
				break
			}
		}
		expected, err := optMoney(it.Expected)
		if err != nil {
			return err
		}
		observed, err := optMoney(it.Observed)
		if err != nil {
			return err
		}
		variance, err := optMoney(it.Variance)
		if err != nil {
			return err
		}
		if _, err := db.Exec(ctx, sqlReconItemInsert, tenant.UUID(), it.ID.UUID(), run.ID.UUID(), i, string(it.Stage),
			string(it.Key), currency, expected, observed, variance, string(it.Status), optString(string(it.Cause)),
			optString(it.Detail)); err != nil {
			return mapError(err, "insert reconciliation item")
		}
	}
	return nil
}

// Run returns a run, its items and their resolutions.
func (r *ReconRepo) Run(ctx context.Context, runID id.ReconciliationID) (reconciliation.Run, []reconciliation.RunItem, []port.ReconResolution, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return reconciliation.Run{}, nil, nil, err
	}
	db := r.s.db(ctx)
	var (
		run             = reconciliation.Run{TenantID: tenant}
		runUUID, leUUID uuid.UUID
		ranBy           *uuid.UUID
		firstBreak      *string
		unavailable     []string
	)
	if err := db.QueryRow(ctx, sqlReconRunByID, tenant.UUID(), runID.UUID()).Scan(&runUUID, &leUUID, &run.Period, &run.RanAt,
		&ranBy, &firstBreak, &unavailable); err != nil {
		return reconciliation.Run{}, nil, nil, mapError(err, "read reconciliation run")
	}
	run.ID, run.LegalEntity, run.RanAt = id.NewReconciliationID(runUUID), id.NewLegalEntityID(leUUID), run.RanAt.UTC()
	if ranBy != nil {
		run.RanBy = id.NewUserID(*ranBy)
	}
	if firstBreak != nil {
		s := reconciliation.Stage(*firstBreak)
		run.FirstBreak = &s
	}
	for _, s := range unavailable {
		run.Unavailable = append(run.Unavailable, reconciliation.Stage(s))
	}

	rows, err := db.Query(ctx, sqlReconItems, tenant.UUID(), runID.UUID())
	if err != nil {
		return reconciliation.Run{}, nil, nil, mapError(err, "read reconciliation items")
	}
	var items []reconciliation.RunItem
	for rows.Next() {
		it, err := scanReconItem(rows)
		if err != nil {
			rows.Close()
			return reconciliation.Run{}, nil, nil, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return reconciliation.Run{}, nil, nil, mapError(err, "read reconciliation items")
	}

	rows, err = db.Query(ctx, sqlReconResolutions, tenant.UUID(), runID.UUID())
	if err != nil {
		return reconciliation.Run{}, nil, nil, mapError(err, "read reconciliation resolutions")
	}
	defer rows.Close()
	var resolutions []port.ReconResolution
	for rows.Next() {
		var (
			res                  port.ReconResolution
			itemUUID             uuid.UUID
			actor, action, cause string
			resolver             *uuid.UUID
		)
		if err := rows.Scan(&itemUUID, &actor, &resolver, &res.Reason, &action, &cause, &res.Evidence, &res.At); err != nil {
			return reconciliation.Run{}, nil, nil, mapError(err, "scan reconciliation resolution")
		}
		res.Item, res.Actor, res.Action, res.Cause, res.At = id.NewReconItemID(itemUUID), reconciliation.Actor(actor),
			reconciliation.Action(action), reconciliation.RootCause(cause), res.At.UTC()
		if resolver != nil {
			res.Resolver = id.NewUserID(*resolver)
		}
		resolutions = append(resolutions, res)
	}
	return run, items, resolutions, mapError(rows.Err(), "read reconciliation resolutions")
}

// Item returns one item.
func (r *ReconRepo) Item(ctx context.Context, itemID id.ReconItemID) (reconciliation.RunItem, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return reconciliation.RunItem{}, err
	}
	return scanReconItem(r.s.db(ctx).QueryRow(ctx, sqlReconItemByID, tenant.UUID(), itemID.UUID()))
}

// Resolve records one item's resolution.
func (r *ReconRepo) Resolve(ctx context.Context, res port.ReconResolution) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlReconResolve, tenant.UUID(), res.Item.UUID(), string(res.Actor), optUser(res.Resolver),
		res.Reason, string(res.Action), string(res.Cause), res.Evidence, res.At.UTC())
	return conflictOnDuplicate(err, "insert reconciliation resolution", "The item has already been resolved.")
}

func scanReconItem(row scanner) (reconciliation.RunItem, error) {
	var (
		it                           reconciliation.RunItem
		itemUUID, runUUID            uuid.UUID
		stage, key, status           string
		currency, cause, detail      *string
		expected, observed, variance Numeric
	)
	if err := row.Scan(&itemUUID, &runUUID, &stage, &key, &currency, &expected, &observed, &variance, &status, &cause, &detail); err != nil {
		return reconciliation.RunItem{}, mapError(err, "scan reconciliation item")
	}
	it.ID, it.Run = id.NewReconItemID(itemUUID), id.NewReconciliationID(runUUID)
	it.Stage, it.Key, it.Status = reconciliation.Stage(stage), reconciliation.MatchKey(key), reconciliation.Status(status)
	it.Cause, it.Detail = reconciliation.RootCause(deref(cause)), deref(detail)
	cur := fiscal.Currency(deref(currency))
	for _, p := range []struct {
		src Numeric
		dst **fiscal.Money
	}{{expected, &it.Expected}, {observed, &it.Observed}, {variance, &it.Variance}} {
		if !p.src.Valid {
			continue
		}
		m, err := p.src.Money(cur)
		if err != nil {
			return reconciliation.RunItem{}, err
		}
		*p.dst = &m
	}
	return it, nil
}
