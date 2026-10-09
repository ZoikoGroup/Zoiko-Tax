package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// fiscal documents — migration 000016
// ---------------------------------------------------------------------------

const (
	sqlDocColumns = `d.tenant_id, d.document_id, d.legal_entity_id, d.document_type, d.document_number, d.root_id,
		d.reason_code, d.issue_date, d.tax_point, d.currency, d.restated, d.ext_source_system, d.ext_namespace,
		d.ext_value, d.net_total, d.tax_total, d.gross_total, d.recorded_at, d.recorded_by`
	sqlDocInsert = `INSERT INTO ztax.fiscal_document
		(tenant_id, document_id, legal_entity_id, document_type, document_number, root_id, reason_code, issue_date,
		 tax_point, currency, restated, ext_source_system, ext_namespace, ext_value, net_total, tax_total,
		 gross_total, recorded_at, recorded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`
	sqlDocPredecessorInsert = `INSERT INTO ztax.fiscal_document_predecessor (tenant_id, document_id, predecessor_id)
		VALUES ($1, $2, $3)`
	sqlDocDecisionInsert = `INSERT INTO ztax.fiscal_document_decision (tenant_id, document_id, decision_id)
		VALUES ($1, $2, $3)`
	sqlLineInsert = `INSERT INTO ztax.fiscal_line
		(tenant_id, line_id, document_id, ordinal, source_line_ref, component_instance, net_amount,
		 discount_amount, allocation_ref, predecessor_line_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	sqlLineTaxInsert = `INSERT INTO ztax.fiscal_line_tax (tenant_id, line_id, ordinal, decision_id, component, amount)
		VALUES ($1, $2, $3, $4, $5, $6)`
	sqlDocStatusInsert = `INSERT INTO ztax.fiscal_document_status
		(tenant_id, document_id, seq, status, cause_id, recorded_at, recorded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	sqlDocByID      = `SELECT ` + sqlDocColumns + ` FROM ztax.fiscal_document d WHERE d.tenant_id = $1 AND d.document_id = $2`
	sqlDocByRoot    = `SELECT ` + sqlDocColumns + ` FROM ztax.fiscal_document d WHERE d.tenant_id = $1 AND d.root_id = $2 ORDER BY d.recorded_at, d.document_id`
	sqlDocPreds     = `SELECT predecessor_id FROM ztax.fiscal_document_predecessor WHERE tenant_id = $1 AND document_id = $2 ORDER BY predecessor_id`
	sqlDocDecisions = `SELECT decision_id FROM ztax.fiscal_document_decision WHERE tenant_id = $1 AND document_id = $2 ORDER BY decision_id`
	sqlDocLines     = `SELECT line_id, source_line_ref, component_instance, net_amount, discount_amount, allocation_ref, predecessor_line_id
		FROM ztax.fiscal_line WHERE tenant_id = $1 AND document_id = $2 ORDER BY ordinal`
	sqlDocLineTaxes = `SELECT decision_id, component, amount FROM ztax.fiscal_line_tax WHERE tenant_id = $1 AND line_id = $2 ORDER BY ordinal`
	sqlDocStatuses  = `SELECT seq, status, cause_id, recorded_at, recorded_by FROM ztax.fiscal_document_status
		WHERE tenant_id = $1 AND document_id = $2 ORDER BY seq`
	sqlDocCitedBy = `SELECT d.document_id, d.document_type, s.status
		FROM ztax.fiscal_document_decision x
		JOIN ztax.fiscal_document d ON d.tenant_id = x.tenant_id AND d.document_id = x.document_id
		JOIN ztax.fiscal_document_status s ON s.tenant_id = d.tenant_id AND s.document_id = d.document_id
		WHERE x.tenant_id = $1 AND x.decision_id = $2
		  AND s.seq = (SELECT max(y.seq) FROM ztax.fiscal_document_status y
		               WHERE y.tenant_id = d.tenant_id AND y.document_id = d.document_id)
		ORDER BY d.recorded_at, d.document_id`
	sqlDocCancelled = `SELECT c.predecessor_line_id
		FROM ztax.fiscal_line c
		JOIN ztax.fiscal_document cd ON cd.tenant_id = c.tenant_id AND cd.document_id = c.document_id
		JOIN ztax.fiscal_line o ON o.tenant_id = c.tenant_id AND o.line_id = c.predecessor_line_id
		WHERE c.tenant_id = $1 AND o.document_id = $2 AND cd.document_type IN ('VOID', 'CREDIT_NOTE')`
	sqlDocLock          = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
	sqlDocTaxByDecision = `SELECT t.decision_id, d.currency, SUM(t.amount)
		FROM ztax.fiscal_line_tax t
		JOIN ztax.fiscal_line l ON l.tenant_id = t.tenant_id AND l.line_id = t.line_id
		JOIN ztax.fiscal_document d ON d.tenant_id = l.tenant_id AND d.document_id = l.document_id
		WHERE t.tenant_id = $1 AND t.decision_id = ANY($2)
		GROUP BY t.decision_id, d.currency`
)

// DocumentRepo implements port.DocumentRepository.
type DocumentRepo struct{ s *Store }

// Documents returns the fiscal document repository.
func (s *Store) Documents() *DocumentRepo { return &DocumentRepo{s: s} }

var _ port.DocumentRepository = (*DocumentRepo)(nil)

// Lock takes a document chain's lock.
func (r *DocumentRepo) Lock(ctx context.Context, root id.FiscalDocumentID) error {
	return r.lock(ctx, "/document-chain/"+root.String())
}

// LockDecision takes a decision's billing lock.
func (r *DocumentRepo) LockDecision(ctx context.Context, decisionID id.DecisionID) error {
	return r.lock(ctx, "/document-decision/"+decisionID.String())
}

func (r *DocumentRepo) lock(ctx context.Context, key string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlDocLock, tenant.String()+key)
	return mapError(err, "lock fiscal document")
}

// Create writes a committed document whole, in the caller's transaction.
func (r *DocumentRepo) Create(ctx context.Context, rec port.DocumentRecord, first document.StatusEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	d := rec.Document
	if d.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The document belongs to a tenant outside the caller's scope.")
	}
	if !d.Authoritative() || first.Seq != 1 || first.Status != document.StatusCommitted || first.Document != d.ID {
		return documentFault(nil, "A document is stored committed, with its COMMITTED status first.")
	}
	if err := d.Validate(); err != nil {
		return documentFault(err, "The document is not well-formed.")
	}
	net, err := MoneyValue(rec.Net)
	if err != nil {
		return err
	}
	tax, err := MoneyValue(rec.Tax)
	if err != nil {
		return err
	}
	gross, err := MoneyValue(rec.Gross)
	if err != nil {
		return err
	}
	var extSystem, extNamespace, extValue *string
	if d.External != nil {
		extSystem, extNamespace, extValue = &d.External.SourceSystem, &d.External.Namespace, &d.External.Value
	}
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlDocInsert, tenant.UUID(), d.ID.UUID(), d.LegalEntity.UUID(), string(d.Type),
		optString(d.Number), d.Root.UUID(), optString(string(d.Reason)), civilDate(d.IssueDate), d.TaxPoint.UTC(),
		string(d.Currency), d.Restated, extSystem, extNamespace, extValue, net, tax, gross,
		rec.RecordedAt.UTC(), optUser(rec.RecordedBy)); err != nil {
		return mapError(err, "insert fiscal document")
	}
	for _, p := range d.Predecessors {
		if _, err := db.Exec(ctx, sqlDocPredecessorInsert, tenant.UUID(), d.ID.UUID(), p.UUID()); err != nil {
			return mapError(err, "insert fiscal document predecessor")
		}
	}
	for _, dec := range d.Decisions {
		if _, err := db.Exec(ctx, sqlDocDecisionInsert, tenant.UUID(), d.ID.UUID(), dec.UUID()); err != nil {
			return mapError(err, "insert fiscal document decision")
		}
	}
	for i, l := range d.Lines {
		lnet, err := MoneyValue(l.Net)
		if err != nil {
			return err
		}
		var discount *Numeric
		if l.Discount != nil {
			v, err := MoneyValue(*l.Discount)
			if err != nil {
				return err
			}
			discount = &v
		}
		if _, err := db.Exec(ctx, sqlLineInsert, tenant.UUID(), l.ID.UUID(), d.ID.UUID(), i, optString(l.SourceLineRef),
			optString(l.ComponentInstance), lnet, discount, optString(l.AllocationRef), optUUID(l.Predecessor)); err != nil {
			return mapError(err, "insert fiscal line")
		}
		for j, t := range l.Taxes {
			amt, err := MoneyValue(t.Amount)
			if err != nil {
				return err
			}
			if _, err := db.Exec(ctx, sqlLineTaxInsert, tenant.UUID(), l.ID.UUID(), j, t.Decision.UUID(), t.Component, amt); err != nil {
				return mapError(err, "insert fiscal line tax")
			}
		}
	}
	return r.AppendStatus(ctx, first)
}

// AppendStatus writes the next status event.
func (r *DocumentRepo) AppendStatus(ctx context.Context, e document.StatusEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if e.Seq < 1 || e.Status == "" || e.RecordedAt.IsZero() {
		return documentFault(nil, "The document status event is not well-formed.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlDocStatusInsert, tenant.UUID(), e.Document.UUID(), e.Seq, string(e.Status),
		optUUID(e.Cause), e.RecordedAt.UTC(), optUser(e.RecordedBy))
	return conflictOnDuplicate(err, "insert fiscal document status", "The document changed since it was read; read it again and retry.")
}

// ByID returns a document whole, with its status history.
func (r *DocumentRepo) ByID(ctx context.Context, documentID id.FiscalDocumentID) (port.DocumentRecord, []document.StatusEvent, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return port.DocumentRecord{}, nil, err
	}
	rec, err := r.scanDocument(ctx, tenant, sqlDocByID, documentID.UUID())
	if err != nil {
		return port.DocumentRecord{}, nil, err
	}
	history, err := r.statuses(ctx, tenant, documentID)
	if err != nil {
		return port.DocumentRecord{}, nil, err
	}
	return rec, history, nil
}

// Lineage returns a chain's documents in commit order.
func (r *DocumentRepo) Lineage(ctx context.Context, root id.FiscalDocumentID) ([]port.DocumentRecord, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlDocByRoot, tenant.UUID(), root.UUID())
	if err != nil {
		return nil, mapError(err, "read document lineage")
	}
	var headers []port.DocumentRecord
	for rows.Next() {
		h, err := scanDocumentHeader(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		headers = append(headers, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read document lineage")
	}
	for i := range headers {
		if err := r.fill(ctx, tenant, &headers[i]); err != nil {
			return nil, err
		}
	}
	return headers, nil
}

// CitedBy returns the documents pinning a decision, with current statuses.
func (r *DocumentRepo) CitedBy(ctx context.Context, decisionID id.DecisionID) ([]port.DocumentCitation, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlDocCitedBy, tenant.UUID(), decisionID.UUID())
	if err != nil {
		return nil, mapError(err, "read decision citations")
	}
	defer rows.Close()
	var out []port.DocumentCitation
	for rows.Next() {
		var (
			docUUID     uuid.UUID
			typ, status string
		)
		if err := rows.Scan(&docUUID, &typ, &status); err != nil {
			return nil, mapError(err, "scan decision citation")
		}
		out = append(out, port.DocumentCitation{Document: id.NewFiscalDocumentID(docUUID), Type: document.Type(typ), Status: document.Status(status)})
	}
	return out, mapError(rows.Err(), "read decision citations")
}

// CancelledLines reports the lines of a document already cancelled.
func (r *DocumentRepo) CancelledLines(ctx context.Context, documentID id.FiscalDocumentID) (map[id.FiscalLineID]bool, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlDocCancelled, tenant.UUID(), documentID.UUID())
	if err != nil {
		return nil, mapError(err, "read cancelled lines")
	}
	defer rows.Close()
	out := map[id.FiscalLineID]bool{}
	for rows.Next() {
		var u uuid.UUID
		if err := rows.Scan(&u); err != nil {
			return nil, mapError(err, "scan cancelled line")
		}
		out[id.NewFiscalLineID(u)] = true
	}
	return out, mapError(rows.Err(), "read cancelled lines")
}

// TaxByDecision sums the documented tax of each decision.
func (r *DocumentRepo) TaxByDecision(ctx context.Context, decisions []id.DecisionID) (map[id.DecisionID]fiscal.Money, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(decisions))
	for i, d := range decisions {
		ids[i] = d.UUID()
	}
	out := map[id.DecisionID]fiscal.Money{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlDocTaxByDecision, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "sum documented tax")
	}
	defer rows.Close()
	for rows.Next() {
		var (
			u        uuid.UUID
			currency string
			sum      Numeric
		)
		if err := rows.Scan(&u, &currency, &sum); err != nil {
			return nil, mapError(err, "scan documented tax")
		}
		dec := id.NewDecisionID(u)
		if _, dup := out[dec]; dup {
			return nil, documentFault(nil, "A decision is documented in more than one currency.")
		}
		m, err := sum.Money(fiscal.Currency(currency))
		if err != nil {
			return nil, err
		}
		out[dec] = m
	}
	return out, mapError(rows.Err(), "sum documented tax")
}

func (r *DocumentRepo) scanDocument(ctx context.Context, tenant id.TenantID, sql string, args ...any) (port.DocumentRecord, error) {
	rec, err := scanDocumentHeader(r.s.db(ctx).QueryRow(ctx, sql, append([]any{tenant.UUID()}, args...)...))
	if err != nil {
		return port.DocumentRecord{}, err
	}
	return rec, r.fill(ctx, tenant, &rec)
}

// fill reads a header's predecessors, decisions, lines and taxes.
func (r *DocumentRepo) fill(ctx context.Context, tenant id.TenantID, rec *port.DocumentRecord) error {
	db := r.s.db(ctx)
	d := &rec.Document
	ids, err := r.uuids(ctx, sqlDocPreds, tenant, d.ID)
	if err != nil {
		return err
	}
	for _, u := range ids {
		d.Predecessors = append(d.Predecessors, id.NewFiscalDocumentID(u))
	}
	if ids, err = r.uuids(ctx, sqlDocDecisions, tenant, d.ID); err != nil {
		return err
	}
	for _, u := range ids {
		d.Decisions = append(d.Decisions, id.NewDecisionID(u))
	}
	rows, err := db.Query(ctx, sqlDocLines, tenant.UUID(), d.ID.UUID())
	if err != nil {
		return mapError(err, "read fiscal lines")
	}
	for rows.Next() {
		var (
			lineUUID                       uuid.UUID
			sourceRef, component, allocRef *string
			net                            Numeric
			discount                       Numeric
			predecessor                    *uuid.UUID
		)
		if err := rows.Scan(&lineUUID, &sourceRef, &component, &net, &discount, &allocRef, &predecessor); err != nil {
			rows.Close()
			return mapError(err, "scan fiscal line")
		}
		l := document.Line{ID: id.NewFiscalLineID(lineUUID), SourceLineRef: deref(sourceRef), ComponentInstance: deref(component), AllocationRef: deref(allocRef)}
		if l.Net, err = net.Money(d.Currency); err != nil {
			rows.Close()
			return err
		}
		if discount.Valid {
			m, err := discount.Money(d.Currency)
			if err != nil {
				rows.Close()
				return err
			}
			l.Discount = &m
		}
		if predecessor != nil {
			p := id.NewFiscalLineID(*predecessor)
			l.Predecessor = &p
		}
		d.Lines = append(d.Lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return mapError(err, "read fiscal lines")
	}
	for i := range d.Lines {
		rows, err := db.Query(ctx, sqlDocLineTaxes, tenant.UUID(), d.Lines[i].ID.UUID())
		if err != nil {
			return mapError(err, "read fiscal line taxes")
		}
		for rows.Next() {
			var (
				decUUID   uuid.UUID
				component string
				amount    Numeric
			)
			if err := rows.Scan(&decUUID, &component, &amount); err != nil {
				rows.Close()
				return mapError(err, "scan fiscal line tax")
			}
			m, err := amount.Money(d.Currency)
			if err != nil {
				rows.Close()
				return err
			}
			d.Lines[i].Taxes = append(d.Lines[i].Taxes, document.TaxLine{Decision: id.NewDecisionID(decUUID), Component: component, Amount: m})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return mapError(err, "read fiscal line taxes")
		}
	}
	return nil
}

func (r *DocumentRepo) uuids(ctx context.Context, sql string, tenant id.TenantID, documentID id.FiscalDocumentID) ([]uuid.UUID, error) {
	rows, err := r.s.db(ctx).Query(ctx, sql, tenant.UUID(), documentID.UUID())
	if err != nil {
		return nil, mapError(err, "read fiscal document links")
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var u uuid.UUID
		if err := rows.Scan(&u); err != nil {
			return nil, mapError(err, "scan fiscal document link")
		}
		out = append(out, u)
	}
	return out, mapError(rows.Err(), "read fiscal document links")
}

func (r *DocumentRepo) statuses(ctx context.Context, tenant id.TenantID, documentID id.FiscalDocumentID) ([]document.StatusEvent, error) {
	rows, err := r.s.db(ctx).Query(ctx, sqlDocStatuses, tenant.UUID(), documentID.UUID())
	if err != nil {
		return nil, mapError(err, "read document statuses")
	}
	defer rows.Close()
	var out []document.StatusEvent
	for rows.Next() {
		var (
			e          = document.StatusEvent{Document: documentID}
			status     string
			cause      *uuid.UUID
			recordedBy *uuid.UUID
		)
		if err := rows.Scan(&e.Seq, &status, &cause, &e.RecordedAt, &recordedBy); err != nil {
			return nil, mapError(err, "scan document status")
		}
		e.Status = document.Status(status)
		e.RecordedAt = e.RecordedAt.UTC()
		if cause != nil {
			c := id.NewFiscalDocumentID(*cause)
			e.Cause = &c
		}
		if recordedBy != nil {
			e.RecordedBy = id.NewUserID(*recordedBy)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read document statuses")
	}
	if len(out) == 0 {
		return nil, documentFault(nil, "A stored document has no status.")
	}
	return out, nil
}

func scanDocumentHeader(row scanner) (port.DocumentRecord, error) {
	var (
		tenantUUID, docUUID, leUUID, rootUUID uuid.UUID
		typ, currency                         string
		number, reason                        *string
		issue                                 pgtype.Date
		extSystem, extNamespace, extValue     *string
		net, tax, gross                       Numeric
		recordedBy                            *uuid.UUID
		rec                                   port.DocumentRecord
	)
	d := &rec.Document
	if err := row.Scan(&tenantUUID, &docUUID, &leUUID, &typ, &number, &rootUUID, &reason, &issue, &d.TaxPoint,
		&currency, &d.Restated, &extSystem, &extNamespace, &extValue, &net, &tax, &gross, &rec.RecordedAt, &recordedBy); err != nil {
		return port.DocumentRecord{}, mapError(err, "scan fiscal document")
	}
	d.TenantID, d.ID, d.LegalEntity, d.Root = id.NewTenantID(tenantUUID), id.NewFiscalDocumentID(docUUID), id.NewLegalEntityID(leUUID), id.NewFiscalDocumentID(rootUUID)
	d.Type, d.Currency, d.Number, d.Reason = document.Type(typ), fiscal.Currency(currency), deref(number), errs.ReasonCode(deref(reason))
	d.IssueDate = inLocation(issue, time.UTC)
	d.TaxPoint = d.TaxPoint.UTC()
	d.Status = document.StatusCommitted
	if extValue != nil {
		d.External = &id.ExternalReference{SourceSystem: deref(extSystem), Namespace: deref(extNamespace), Value: *extValue}
	}
	var err error
	if rec.Net, err = net.Money(d.Currency); err != nil {
		return port.DocumentRecord{}, err
	}
	if rec.Tax, err = tax.Money(d.Currency); err != nil {
		return port.DocumentRecord{}, err
	}
	if rec.Gross, err = gross.Money(d.Currency); err != nil {
		return port.DocumentRecord{}, err
	}
	rec.RecordedAt = rec.RecordedAt.UTC()
	if recordedBy != nil {
		rec.RecordedBy = id.NewUserID(*recordedBy)
	}
	return rec, nil
}

// documentFault is an internal error: a document no correct writer produces.
func documentFault(err error, msg string) error { return obligationFault(err, msg) }
