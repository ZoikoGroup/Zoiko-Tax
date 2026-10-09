package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/settlement"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// refund and refund_event — migration 000013
// ---------------------------------------------------------------------------

const (
	sqlRefundColumns = `r.tenant_id, r.refund_id, r.legal_entity_id, r.decision_id, r.amount, r.currency,
		r.payment_reference, r.reason, r.requested_at, r.requested_by`
	sqlRefundEventColumns = `e.refund_id, e.seq, e.status, e.outcome, e.external_reference, e.recorded_at, e.recorded_by`

	sqlRefundInsert = `INSERT INTO ztax.refund
		(tenant_id, refund_id, legal_entity_id, decision_id, amount, currency, payment_reference, reason,
		 requested_at, requested_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	sqlRefundEventInsert = `INSERT INTO ztax.refund_event
		(tenant_id, refund_id, seq, status, outcome, external_reference, recorded_at, recorded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	sqlRefundByID = `SELECT ` + sqlRefundColumns + ` FROM ztax.refund r
		WHERE r.tenant_id = $1 AND r.refund_id = $2`
	sqlRefundEvents = `SELECT ` + sqlRefundEventColumns + ` FROM ztax.refund_event e
		WHERE e.tenant_id = $1 AND e.refund_id = $2 ORDER BY e.seq`
	// Each refund of the decision with its latest event, oldest refund first.
	sqlRefundsForDecision = `SELECT ` + sqlRefundColumns + `, ` + sqlRefundEventColumns + `
		FROM ztax.refund r
		JOIN ztax.refund_event e ON e.tenant_id = r.tenant_id AND e.refund_id = r.refund_id
		WHERE r.tenant_id = $1 AND r.decision_id = $2
		  AND e.seq = (SELECT max(x.seq) FROM ztax.refund_event x
		               WHERE x.tenant_id = r.tenant_id AND x.refund_id = r.refund_id)
		ORDER BY r.requested_at, r.refund_id`
	// As for obligations: the first refund of a decision has no row to lock.
	sqlRefundDecisionLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
)

// RefundRepo implements port.RefundRepository.
type RefundRepo struct{ s *Store }

// Refunds returns the refund repository.
func (s *Store) Refunds() *RefundRepo { return &RefundRepo{s: s} }

var _ port.RefundRepository = (*RefundRepo)(nil)

// LockDecision takes the decision's refund lock until the transaction ends.
func (r *RefundRepo) LockDecision(ctx context.Context, decisionID id.DecisionID) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlRefundDecisionLock, tenant.String()+"/refund/"+decisionID.String())
	return mapError(err, "lock decision refunds")
}

// Create writes the header and its REQUESTED event, in the caller's
// transaction.
func (r *RefundRepo) Create(ctx context.Context, rf settlement.Refund) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if rf.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The refund belongs to a tenant outside the caller's scope.")
	}
	if err := rf.Validate(); err != nil {
		return refundFault(err, "The refund is not well-formed.")
	}
	amount, err := MoneyValue(rf.Amount)
	if err != nil {
		return err
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlRefundInsert,
		tenant.UUID(), rf.ID.UUID(), rf.LegalEntity.UUID(), rf.Decision.UUID(), amount, string(rf.Amount.Currency()),
		rf.PaymentRef, optString(rf.Reason), rf.RequestedAt.UTC(), optUser(rf.RequestedBy)); err != nil {
		return mapError(err, "insert refund")
	}
	return r.Append(ctx, settlement.Requested(rf))
}

// Append writes one event. A sequence already taken is an optimistic
// conflict: the writer decided its move against a history that has since
// grown.
func (r *RefundRepo) Append(ctx context.Context, e settlement.RefundEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if e.Refund.IsZero() || e.Seq < 1 || !e.Status.Valid() || e.RecordedAt.IsZero() {
		return refundFault(nil, "The refund event is not well-formed.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlRefundEventInsert,
		tenant.UUID(), e.Refund.UUID(), e.Seq, string(e.Status), optString(string(e.Outcome)),
		optString(e.ExternalRef), e.RecordedAt.UTC(), optUser(e.RecordedBy))
	if err != nil {
		mapped := mapError(err, "insert refund event")
		if errs.ReasonOf(mapped) == errs.ReasonAlreadyExists {
			return errs.Wrap(err, errs.CategoryConflict, errs.ReasonOptimisticConflict,
				"The refund changed since it was read; read it again and retry.")
		}
		return mapped
	}
	return nil
}

// ByID returns a refund and its whole history.
func (r *RefundRepo) ByID(ctx context.Context, refundID id.RefundID) (settlement.Refund, []settlement.RefundEvent, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return settlement.Refund{}, nil, err
	}
	rf, err := scanRefund(r.s.db(ctx).QueryRow(ctx, sqlRefundByID, tenant.UUID(), refundID.UUID()))
	if err != nil {
		return settlement.Refund{}, nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlRefundEvents, tenant.UUID(), refundID.UUID())
	if err != nil {
		return settlement.Refund{}, nil, mapError(err, "read refund events")
	}
	defer rows.Close()
	var events []settlement.RefundEvent
	for rows.Next() {
		e, err := scanRefundEvent(rows)
		if err != nil {
			return settlement.Refund{}, nil, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return settlement.Refund{}, nil, mapError(err, "read refund events")
	}
	if len(events) == 0 {
		// Create writes the header and its first event in one transaction.
		return settlement.Refund{}, nil, refundFault(nil, "A stored refund has no history.")
	}
	return rf, events, nil
}

// ForDecision returns a decision's refunds with their current events.
func (r *RefundRepo) ForDecision(ctx context.Context, decisionID id.DecisionID) ([]port.RefundState, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlRefundsForDecision, tenant.UUID(), decisionID.UUID())
	if err != nil {
		return nil, mapError(err, "list decision refunds")
	}
	defer rows.Close()
	var out []port.RefundState
	for rows.Next() {
		var (
			rr refundRow
			er refundEventRow
		)
		if err := rows.Scan(append(rr.targets(), er.targets()...)...); err != nil {
			return nil, mapError(err, "scan decision refund")
		}
		rf, err := rr.refund()
		if err != nil {
			return nil, err
		}
		out = append(out, port.RefundState{Refund: rf, Current: er.event()})
	}
	return out, mapError(rows.Err(), "list decision refunds")
}

// refundRow is the scan target for a refund header.
type refundRow struct {
	tenantUUID, refundUUID, leUUID, decisionUUID uuid.UUID
	amount                                       Numeric
	currency, paymentRef                         string
	reason                                       *string
	requestedAt                                  time.Time
	requestedBy                                  *uuid.UUID
}

func (r *refundRow) targets() []any {
	return []any{&r.tenantUUID, &r.refundUUID, &r.leUUID, &r.decisionUUID, &r.amount, &r.currency,
		&r.paymentRef, &r.reason, &r.requestedAt, &r.requestedBy}
}

func (r *refundRow) refund() (settlement.Refund, error) {
	m, err := r.amount.Money(fiscal.Currency(r.currency))
	if err != nil {
		return settlement.Refund{}, err
	}
	out := settlement.Refund{
		ID: id.NewRefundID(r.refundUUID), TenantID: id.NewTenantID(r.tenantUUID),
		LegalEntity: id.NewLegalEntityID(r.leUUID), Decision: id.NewDecisionID(r.decisionUUID),
		Amount: m, PaymentRef: r.paymentRef, Reason: deref(r.reason), RequestedAt: r.requestedAt.UTC(),
	}
	if r.requestedBy != nil {
		out.RequestedBy = id.NewUserID(*r.requestedBy)
	}
	return out, nil
}

// refundEventRow is the scan target for a refund event.
type refundEventRow struct {
	refundUUID           uuid.UUID
	seq                  int
	status               string
	outcome, externalRef *string
	recordedAt           time.Time
	recordedBy           *uuid.UUID
}

func (e *refundEventRow) targets() []any {
	return []any{&e.refundUUID, &e.seq, &e.status, &e.outcome, &e.externalRef, &e.recordedAt, &e.recordedBy}
}

func (e *refundEventRow) event() settlement.RefundEvent {
	out := settlement.RefundEvent{
		Refund: id.NewRefundID(e.refundUUID), Seq: e.seq, Status: settlement.RefundStatus(e.status),
		Outcome: settlement.RefundOutcome(deref(e.outcome)), ExternalRef: deref(e.externalRef),
		RecordedAt: e.recordedAt.UTC(),
	}
	if e.recordedBy != nil {
		out.RecordedBy = id.NewUserID(*e.recordedBy)
	}
	return out
}

func scanRefund(row pgx.Row) (settlement.Refund, error) {
	var rr refundRow
	if err := row.Scan(rr.targets()...); err != nil {
		return settlement.Refund{}, mapError(err, "scan refund")
	}
	return rr.refund()
}

func scanRefundEvent(row pgx.Row) (settlement.RefundEvent, error) {
	var er refundEventRow
	if err := row.Scan(er.targets()...); err != nil {
		return settlement.RefundEvent{}, mapError(err, "scan refund event")
	}
	return er.event(), nil
}

// optUser is a nullable user column: zero is system work.
func optUser(u id.UserID) *uuid.UUID {
	if u.IsZero() {
		return nil
	}
	v := u.UUID()
	return &v
}

// refundFault is an internal error: a refund this package was handed or read
// that no correct writer produces.
func refundFault(err error, msg string) error { return obligationFault(err, msg) }
