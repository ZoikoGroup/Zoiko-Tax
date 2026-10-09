package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// the Tax Command Center — reads only, over the tables of 000003, 000005,
// 000011, 000013, 000014 and 000018
// ---------------------------------------------------------------------------

const (
	// Months are UTC months of recorded_at: the dashboard reports when the
	// cell recorded a decision, which is the instant it vouches for.
	sqlDashboardDecisions = `SELECT to_char(recorded_at AT TIME ZONE 'UTC', 'YYYY-MM'), count(*),
  count(*) FILTER (WHERE outcome IN ('REVIEW_REQUIRED', 'AMBIGUOUS', 'CONFLICTED', 'UNSUPPORTED'))
FROM ztax.tax_decision
WHERE tenant_id = $1 AND recorded_at >= $2 AND recorded_at < $3
GROUP BY 1`

	// legal_period is YYYY-MM, so a text range is a period range.
	sqlDashboardPostings = `SELECT j.legal_period, l.side, l.currency, SUM(l.amount), count(*)
FROM ztax.tcsl_journal_line l
JOIN ztax.tcsl_journal j ON j.tenant_id = l.tenant_id AND j.journal_id = l.journal_id
WHERE l.tenant_id = $1 AND j.legal_entity_id = $2 AND l.account = $3
  AND j.legal_period >= $4 AND j.legal_period <= $5
GROUP BY j.legal_period, l.side, l.currency
ORDER BY j.legal_period, l.side, l.currency`

	// The latest run of each period: an earlier run of the same period is
	// history, and counting its items again would count one break twice.
	sqlDashboardLatestRuns = `WITH latest AS (
  SELECT DISTINCT ON (legal_period) run_id, legal_period
  FROM ztax.recon_run
  WHERE tenant_id = $1 AND legal_entity_id = $2
  ORDER BY legal_period, ran_at DESC, run_id DESC
)`

	sqlDashboardReconSummary = sqlDashboardLatestRuns + `
SELECT (SELECT count(*) FROM latest),
  count(i.item_id) FILTER (WHERE i.status IN ('MATCHED', 'TOLERANCE_MATCH', 'EXPLAINED', 'RESOLVED') OR r.item_id IS NOT NULL),
  count(i.item_id) FILTER (WHERE i.status NOT IN ('MATCHED', 'TOLERANCE_MATCH', 'EXPLAINED', 'RESOLVED', 'PENDING') AND r.item_id IS NULL),
  count(i.item_id) FILTER (WHERE i.status = 'PENDING' AND r.item_id IS NULL)
FROM latest
JOIN ztax.recon_item i ON i.tenant_id = $1 AND i.run_id = latest.run_id
LEFT JOIN ztax.recon_resolution r ON r.tenant_id = i.tenant_id AND r.item_id = i.item_id`

	sqlDashboardReconBreaks = sqlDashboardLatestRuns + `
SELECT i.item_id, latest.legal_period, i.stage, i.status, i.match_key, i.currency, i.variance
FROM latest
JOIN ztax.recon_item i ON i.tenant_id = $1 AND i.run_id = latest.run_id
LEFT JOIN ztax.recon_resolution r ON r.tenant_id = i.tenant_id AND r.item_id = i.item_id
WHERE i.status NOT IN ('MATCHED', 'TOLERANCE_MATCH', 'EXPLAINED', 'RESOLVED', 'PENDING') AND r.item_id IS NULL
ORDER BY latest.legal_period, i.ordinal
LIMIT $3`

	// A seal covers [period_start, period_end) of recorded_at (ADR-0011 §2.4).
	sqlDashboardSealCoverage = `SELECT
  (SELECT count(*) FROM ztax.tax_decision WHERE tenant_id = $1),
  (SELECT count(*) FROM ztax.tax_decision d WHERE d.tenant_id = $1 AND EXISTS (
     SELECT 1 FROM ztax.evidence_period_seal s
     WHERE s.tenant_id = d.tenant_id AND d.recorded_at >= s.period_start AND d.recorded_at < s.period_end)),
  (SELECT count(*) FROM ztax.evidence_period_seal WHERE tenant_id = $1)`

	sqlDashboardRefunds = `SELECT r.refund_id, e.status, r.amount, r.currency, r.payment_reference, r.requested_at
FROM ztax.refund r
JOIN LATERAL (
  SELECT status, recorded_at FROM ztax.refund_event
  WHERE tenant_id = r.tenant_id AND refund_id = r.refund_id
  ORDER BY seq DESC LIMIT 1
) e ON true
WHERE r.tenant_id = $1 AND (e.status = 'UNCERTAIN' OR (e.status = 'FAILED' AND e.recorded_at >= $2))
ORDER BY r.requested_at, r.refund_id
LIMIT $3`

	sqlDashboardDeadDeliveries = `SELECT count(*) FROM ztax.webhook_delivery
WHERE tenant_id = $1 AND status = 'DEAD' AND created_at >= $2`
)

// DashboardRepo implements port.DashboardRepository.
type DashboardRepo struct{ s *Store }

// Dashboard returns the Tax Command Center's read repository.
func (s *Store) Dashboard() *DashboardRepo { return &DashboardRepo{s: s} }

var _ port.DashboardRepository = (*DashboardRepo)(nil)

// Decisions counts decisions per UTC month of their recording.
func (r *DashboardRepo) Decisions(ctx context.Context, from, to time.Time) (map[string]port.DecisionCount, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlDashboardDecisions, tenant.UUID(), from.UTC(), to.UTC())
	if err != nil {
		return nil, mapError(err, "count decisions")
	}
	defer rows.Close()
	out := map[string]port.DecisionCount{}
	for rows.Next() {
		var (
			month            string
			recorded, review int
		)
		if err := rows.Scan(&month, &recorded, &review); err != nil {
			return nil, mapError(err, "scan decision count")
		}
		out[month] = port.DecisionCount{Recorded: recorded, ReviewRequired: review}
	}
	return out, mapError(rows.Err(), "count decisions")
}

// Postings sums one account's lines per period, side and currency.
func (r *DashboardRepo) Postings(ctx context.Context, legalEntityID id.LegalEntityID, account subledger.Account,
	fromPeriod, toPeriod string) ([]port.Posting, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlDashboardPostings, tenant.UUID(), legalEntityID.UUID(), string(account), fromPeriod, toPeriod)
	if err != nil {
		return nil, mapError(err, "sum postings")
	}
	defer rows.Close()
	var out []port.Posting
	for rows.Next() {
		var (
			period, side, currency string
			sum                    Numeric
			lines                  int
		)
		if err := rows.Scan(&period, &side, &currency, &sum, &lines); err != nil {
			return nil, mapError(err, "scan posting")
		}
		m, err := fiscal.MoneyFromNumeric(sum.Parts, fiscal.Currency(currency))
		if err != nil {
			return nil, err
		}
		out = append(out, port.Posting{Period: period, Side: subledger.Side(side), Amount: m, Lines: lines})
	}
	return out, mapError(rows.Err(), "sum postings")
}

// Reconciliation counts the latest runs' items and lists their breaks.
func (r *DashboardRepo) Reconciliation(ctx context.Context, legalEntityID id.LegalEntityID, limit int) (
	port.ReconciliationSummary, []port.ReconciliationException, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return port.ReconciliationSummary{}, nil, err
	}
	var sum port.ReconciliationSummary
	if err := r.s.db(ctx).QueryRow(ctx, sqlDashboardReconSummary, tenant.UUID(), legalEntityID.UUID()).
		Scan(&sum.Runs, &sum.Matched, &sum.Breaks, &sum.Open); err != nil {
		return port.ReconciliationSummary{}, nil, mapError(err, "count reconciliation items")
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlDashboardReconBreaks, tenant.UUID(), legalEntityID.UUID(), limit)
	if err != nil {
		return port.ReconciliationSummary{}, nil, mapError(err, "list reconciliation breaks")
	}
	defer rows.Close()
	var out []port.ReconciliationException
	for rows.Next() {
		var (
			itemID                          uuid.UUID
			period, stage, status, matchKey string
			currency                        *string
			variance                        Numeric
		)
		if err := rows.Scan(&itemID, &period, &stage, &status, &matchKey, &currency, &variance); err != nil {
			return port.ReconciliationSummary{}, nil, mapError(err, "scan reconciliation break")
		}
		e := port.ReconciliationException{ItemID: itemID.String(), Period: period, Stage: stage, Status: status, MatchKey: matchKey}
		if currency != nil && variance.Valid {
			m, err := fiscal.MoneyFromNumeric(variance.Parts, fiscal.Currency(*currency))
			if err != nil {
				return port.ReconciliationSummary{}, nil, err
			}
			e.Variance = &m
		}
		out = append(out, e)
	}
	return sum, out, mapError(rows.Err(), "list reconciliation breaks")
}

// SealCoverage counts decisions, the sealed among them, and seals.
func (r *DashboardRepo) SealCoverage(ctx context.Context) (port.SealCoverage, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return port.SealCoverage{}, err
	}
	var c port.SealCoverage
	if err := r.s.db(ctx).QueryRow(ctx, sqlDashboardSealCoverage, tenant.UUID()).Scan(&c.Decisions, &c.Sealed, &c.Seals); err != nil {
		return port.SealCoverage{}, mapError(err, "count seal coverage")
	}
	return c, nil
}

// RefundsAwaiting lists refunds whose latest status needs a person.
func (r *DashboardRepo) RefundsAwaiting(ctx context.Context, since time.Time, limit int) ([]port.RefundAwaiting, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlDashboardRefunds, tenant.UUID(), since.UTC(), limit)
	if err != nil {
		return nil, mapError(err, "list refunds awaiting")
	}
	defer rows.Close()
	var out []port.RefundAwaiting
	for rows.Next() {
		var (
			refundID                    uuid.UUID
			status, currency, reference string
			amount                      Numeric
			requestedAt                 time.Time
		)
		if err := rows.Scan(&refundID, &status, &amount, &currency, &reference, &requestedAt); err != nil {
			return nil, mapError(err, "scan refund awaiting")
		}
		m, err := fiscal.MoneyFromNumeric(amount.Parts, fiscal.Currency(currency))
		if err != nil {
			return nil, err
		}
		out = append(out, port.RefundAwaiting{RefundID: refundID.String(), Status: status, Amount: m,
			PaymentReference: reference, RequestedAt: requestedAt.UTC()})
	}
	return out, mapError(rows.Err(), "list refunds awaiting")
}

// DeadDeliveries counts dead-lettered webhook deliveries.
func (r *DashboardRepo) DeadDeliveries(ctx context.Context, since time.Time) (int, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return 0, err
	}
	var n int
	if err := r.s.db(ctx).QueryRow(ctx, sqlDashboardDeadDeliveries, tenant.UUID(), since.UTC()).Scan(&n); err != nil {
		return 0, mapError(err, "count dead deliveries")
	}
	return n, nil
}
