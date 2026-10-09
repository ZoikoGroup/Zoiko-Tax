package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// subledger period close — migration 000017
// ---------------------------------------------------------------------------

const (
	sqlPeriodLockShared    = `SELECT pg_advisory_xact_lock_shared(hashtextextended($1, 0))`
	sqlPeriodLockExclusive = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
	sqlPeriodHistory       = `SELECT seq, state, reason, recorded_at, recorded_by, request_id, approved_by, manifest_digest
		FROM ztax.tcsl_period_event WHERE tenant_id = $1 AND legal_entity_id = $2 AND legal_period = $3 ORDER BY seq`
	sqlPeriodEventInsert = `INSERT INTO ztax.tcsl_period_event
		(tenant_id, legal_entity_id, legal_period, seq, state, reason, recorded_at, recorded_by, request_id, approved_by, manifest_digest)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	sqlPeriodJournals = `SELECT journal_id FROM ztax.tcsl_journal
		WHERE tenant_id = $1 AND legal_entity_id = $2 AND legal_period = $3 ORDER BY journal_id`
	sqlPeriodBalances = `SELECT l.account, l.currency, l.side, SUM(l.amount)
		FROM ztax.tcsl_journal_line l
		JOIN ztax.tcsl_journal j ON j.tenant_id = l.tenant_id AND j.journal_id = l.journal_id
		WHERE l.tenant_id = $1 AND j.legal_entity_id = $2 AND j.legal_period = $3
		GROUP BY l.account, l.currency, l.side`
	// Documents whose tax point falls in the month, UTC, with each one's
	// current status.
	sqlPeriodDocuments = `SELECT d.document_id, d.document_type, s.status
		FROM ztax.fiscal_document d
		JOIN ztax.fiscal_document_status s ON s.tenant_id = d.tenant_id AND s.document_id = d.document_id
		WHERE d.tenant_id = $1 AND d.legal_entity_id = $2
		  AND d.tax_point >= $3 AND d.tax_point < $4
		  AND s.seq = (SELECT max(x.seq) FROM ztax.fiscal_document_status x
		               WHERE x.tenant_id = d.tenant_id AND x.document_id = d.document_id)`
	// Refunds requested in the month whose money is not confirmed either way.
	sqlPeriodOpenRefunds = `SELECT r.refund_id, e.status
		FROM ztax.refund r
		JOIN ztax.refund_event e ON e.tenant_id = r.tenant_id AND e.refund_id = r.refund_id
		WHERE r.tenant_id = $1 AND r.legal_entity_id = $2
		  AND r.requested_at >= $3 AND r.requested_at < $4
		  AND e.seq = (SELECT max(x.seq) FROM ztax.refund_event x WHERE x.tenant_id = r.tenant_id AND x.refund_id = r.refund_id)
		  AND e.status IN ('REQUESTED', 'PENDING', 'UNCERTAIN')`
	sqlManifestInsert = `INSERT INTO ztax.tcsl_close_manifest
		(tenant_id, manifest_digest, legal_entity_id, legal_period, body, created_at)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (tenant_id, manifest_digest) DO NOTHING`
	sqlManifestGet  = `SELECT body FROM ztax.tcsl_close_manifest WHERE tenant_id = $1 AND manifest_digest = $2`
	sqlReopenInsert = `INSERT INTO ztax.tcsl_reopen_request
		(tenant_id, request_id, legal_entity_id, legal_period, reason, requested_at, requested_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`
	sqlReopenGet = `SELECT request_id, legal_entity_id, legal_period, reason, requested_at, requested_by
		FROM ztax.tcsl_reopen_request WHERE tenant_id = $1 AND request_id = $2`
)

// PeriodRepo implements port.PeriodRepository.
type PeriodRepo struct{ s *Store }

// Periods returns the period repository.
func (s *Store) Periods() *PeriodRepo { return &PeriodRepo{s: s} }

var _ port.PeriodRepository = (*PeriodRepo)(nil)

func (r *PeriodRepo) lock(ctx context.Context, sql string, le id.LegalEntityID, period string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sql, tenant.String()+"/tcsl-period/"+le.String()+"/"+period)
	return mapError(err, "lock subledger period")
}

// LockForPosting takes the period's lock shared.
func (r *PeriodRepo) LockForPosting(ctx context.Context, le id.LegalEntityID, period string) error {
	return r.lock(ctx, sqlPeriodLockShared, le, period)
}

// LockForTransition takes the period's lock exclusive.
func (r *PeriodRepo) LockForTransition(ctx context.Context, le id.LegalEntityID, period string) error {
	return r.lock(ctx, sqlPeriodLockExclusive, le, period)
}

// History returns the period's events.
func (r *PeriodRepo) History(ctx context.Context, le id.LegalEntityID, period string) ([]subledger.PeriodEvent, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlPeriodHistory, tenant.UUID(), le.UUID(), period)
	if err != nil {
		return nil, mapError(err, "read period history")
	}
	defer rows.Close()
	var out []subledger.PeriodEvent
	for rows.Next() {
		var (
			e                                 = subledger.PeriodEvent{LegalEntity: le, Period: period}
			state                             string
			reason, manifest                  *string
			recordedBy, requestID, approvedBy *uuid.UUID
		)
		if err := rows.Scan(&e.Seq, &state, &reason, &e.RecordedAt, &recordedBy, &requestID, &approvedBy, &manifest); err != nil {
			return nil, mapError(err, "scan period event")
		}
		e.State, e.Reason, e.RecordedAt = subledger.PeriodState(state), deref(reason), e.RecordedAt.UTC()
		if recordedBy != nil {
			e.RecordedBy = id.NewUserID(*recordedBy)
		}
		if requestID != nil {
			e.Request = requestID.String()
		}
		if approvedBy != nil {
			e.ApprovedBy = id.NewUserID(*approvedBy)
		}
		if manifest != nil {
			if e.Manifest, err = parseStoredDigest(*manifest, "manifest_digest"); err != nil {
				return nil, err
			}
		}
		out = append(out, e)
	}
	return out, mapError(rows.Err(), "read period history")
}

// AppendEvent writes the next event.
func (r *PeriodRepo) AppendEvent(ctx context.Context, e subledger.PeriodEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if e.Seq < 1 || !e.State.Valid() || e.RecordedAt.IsZero() {
		return periodFault(nil, "The period event is not well-formed.")
	}
	var requestID *uuid.UUID
	if e.Request != "" {
		u, err := uuid.Parse(e.Request)
		if err != nil {
			return periodFault(err, "The period event names a malformed request.")
		}
		requestID = &u
	}
	var manifest *string
	if !e.Manifest.IsZero() {
		m := e.Manifest.String()
		manifest = &m
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlPeriodEventInsert, tenant.UUID(), e.LegalEntity.UUID(), e.Period, e.Seq, string(e.State),
		optString(e.Reason), e.RecordedAt.UTC(), optUser(e.RecordedBy), requestID, optUser(e.ApprovedBy), manifest)
	return conflictOnDuplicate(err, "insert period event", "The period moved since it was read; read it again and retry.")
}

// Population reads what a close of the period would seal.
func (r *PeriodRepo) Population(ctx context.Context, le id.LegalEntityID, period string) (port.PeriodPopulation, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return port.PeriodPopulation{}, err
	}
	start, err := time.Parse("2006-01", period)
	if err != nil {
		return port.PeriodPopulation{}, periodFault(err, "The period is not a month.")
	}
	end := start.AddDate(0, 1, 0)
	db := r.s.db(ctx)
	var pop port.PeriodPopulation

	rows, err := db.Query(ctx, sqlPeriodJournals, tenant.UUID(), le.UUID(), period)
	if err != nil {
		return port.PeriodPopulation{}, mapError(err, "read period journals")
	}
	for rows.Next() {
		var u uuid.UUID
		if err := rows.Scan(&u); err != nil {
			rows.Close()
			return port.PeriodPopulation{}, mapError(err, "scan period journal")
		}
		pop.Journals = append(pop.Journals, id.NewJournalID(u))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return port.PeriodPopulation{}, mapError(err, "read period journals")
	}

	rows, err = db.Query(ctx, sqlPeriodBalances, tenant.UUID(), le.UUID(), period)
	if err != nil {
		return port.PeriodPopulation{}, mapError(err, "sum period lines")
	}
	byKey := map[subledger.BalanceKey]*subledger.ManifestBalance{}
	var order []subledger.BalanceKey
	for rows.Next() {
		var (
			account, currency, side string
			sum                     Numeric
		)
		if err := rows.Scan(&account, &currency, &side, &sum); err != nil {
			rows.Close()
			return port.PeriodPopulation{}, mapError(err, "scan period balance")
		}
		m, err := sum.Money(fiscal.Currency(currency))
		if err != nil {
			rows.Close()
			return port.PeriodPopulation{}, err
		}
		k := subledger.BalanceKey{Account: subledger.Account(account), Currency: fiscal.Currency(currency)}
		b, ok := byKey[k]
		if !ok {
			b = &subledger.ManifestBalance{Account: k.Account, Currency: k.Currency, Debits: m.Zero(), Credits: m.Zero()}
			byKey[k] = b
			order = append(order, k)
		}
		if side == string(subledger.Debit) {
			b.Debits = m
		} else {
			b.Credits = m
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return port.PeriodPopulation{}, mapError(err, "sum period lines")
	}
	for _, k := range order {
		pop.Balances = append(pop.Balances, *byKey[k])
	}

	rows, err = db.Query(ctx, sqlPeriodDocuments, tenant.UUID(), le.UUID(), start, end)
	if err != nil {
		return port.PeriodPopulation{}, mapError(err, "read period documents")
	}
	for rows.Next() {
		var (
			u           uuid.UUID
			typ, status string
		)
		if err := rows.Scan(&u, &typ, &status); err != nil {
			rows.Close()
			return port.PeriodPopulation{}, mapError(err, "scan period document")
		}
		pop.Documents = append(pop.Documents, subledger.ManifestDocument{Document: id.NewFiscalDocumentID(u), Type: typ, Status: status})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return port.PeriodPopulation{}, mapError(err, "read period documents")
	}

	rows, err = db.Query(ctx, sqlPeriodOpenRefunds, tenant.UUID(), le.UUID(), start, end)
	if err != nil {
		return port.PeriodPopulation{}, mapError(err, "read open refunds")
	}
	defer rows.Close()
	for rows.Next() {
		var (
			u      uuid.UUID
			status string
		)
		if err := rows.Scan(&u, &status); err != nil {
			return port.PeriodPopulation{}, mapError(err, "scan open refund")
		}
		pop.Exceptions = append(pop.Exceptions, subledger.ManifestException{Kind: "REFUND", Ref: u.String(), Status: status})
	}
	return pop, mapError(rows.Err(), "read open refunds")
}

// PutManifest stores a manifest under its digest.
func (r *PeriodRepo) PutManifest(ctx context.Context, le id.LegalEntityID, period string, digest canonical.Digest, body []byte, at time.Time) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if !canonical.SumBytes(body).Equal(digest) {
		return periodFault(nil, "A close manifest's bytes do not hash to its digest.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlManifestInsert, tenant.UUID(), digest.String(), le.UUID(), period, body, at.UTC())
	return mapError(err, "insert close manifest")
}

// Manifest returns a manifest's bytes, verified against its digest.
func (r *PeriodRepo) Manifest(ctx context.Context, digest canonical.Digest) ([]byte, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var body []byte
	if err := r.s.db(ctx).QueryRow(ctx, sqlManifestGet, tenant.UUID(), digest.String()).Scan(&body); err != nil {
		return nil, mapError(err, "read close manifest")
	}
	if !canonical.SumBytes(body).Equal(digest) {
		return nil, errs.New(errs.CategoryInternal, errs.ReasonEvidenceIntegrity, "A stored close manifest no longer hashes to its digest.")
	}
	return body, nil
}

// CreateReopenRequest writes a reopen request.
func (r *PeriodRepo) CreateReopenRequest(ctx context.Context, req port.ReopenRequest) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if req.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The request belongs to a tenant outside the caller's scope.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlReopenInsert, tenant.UUID(), req.ID.UUID(), req.LegalEntity.UUID(), req.Period,
		req.Reason, req.RequestedAt.UTC(), req.RequestedBy.UUID())
	return mapError(err, "insert reopen request")
}

// ReopenRequest reads one request.
func (r *PeriodRepo) ReopenRequest(ctx context.Context, requestID id.ReopenRequestID) (port.ReopenRequest, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return port.ReopenRequest{}, err
	}
	var (
		req             = port.ReopenRequest{TenantID: tenant}
		reqUUID, le, by uuid.UUID
	)
	if err := r.s.db(ctx).QueryRow(ctx, sqlReopenGet, tenant.UUID(), requestID.UUID()).Scan(&reqUUID, &le, &req.Period,
		&req.Reason, &req.RequestedAt, &by); err != nil {
		return port.ReopenRequest{}, mapError(err, "read reopen request")
	}
	req.ID, req.LegalEntity, req.RequestedBy, req.RequestedAt = id.NewReopenRequestID(reqUUID), id.NewLegalEntityID(le), id.NewUserID(by), req.RequestedAt.UTC()
	return req, nil
}

// periodFault is an internal error: a period record no correct writer produces.
func periodFault(err error, msg string) error { return obligationFault(err, msg) }
