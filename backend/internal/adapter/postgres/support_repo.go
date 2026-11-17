package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/support"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// support_case, support_case_event — migration 000029
// ---------------------------------------------------------------------------

const (
	sqlSupportCaseColumns = `case_id, case_type, severity, subject, description, product, legal_entity_id, fiscal_period,
		contract_ref, seller_ref, decision_id, document_id, obligation_id, remittance_id, statutory_due_at, sla_due_at,
		assignee, evidence, opened_at, opened_by`
	sqlSupportCaseInsert = `INSERT INTO ztax.support_case (tenant_id, ` + sqlSupportCaseColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)`
	sqlSupportCaseSelect = `SELECT ` + sqlSupportCaseColumns + ` FROM ztax.support_case WHERE tenant_id = $1`

	sqlSupportEventColumns = `case_id, seq, action, status_after, note, case_type, severity, legal_entity_id, fiscal_period,
		contract_ref, seller_ref, statutory_due_at, sla_due_at, assignee, escalate_to, evidence, missing_facts,
		reopens_seq, recorded_at, recorded_by`
	sqlSupportEventInsert = `INSERT INTO ztax.support_case_event (tenant_id, ` + sqlSupportEventColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)`
	sqlSupportEventSelect = `SELECT ` + sqlSupportEventColumns + ` FROM ztax.support_case_event WHERE tenant_id = $1`

	sqlSupportLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	supportListCap = 500
)

// SupportRepo implements port.SupportCaseRepository. It writes the two
// support tables and nothing else.
type SupportRepo struct{ s *Store }

// Support returns the support case repository.
func (s *Store) Support() *SupportRepo { return &SupportRepo{s: s} }

var _ port.SupportCaseRepository = (*SupportRepo)(nil)

// Lock takes the case's lock for the transaction.
func (r *SupportRepo) Lock(ctx context.Context, caseID id.SupportCaseID) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlSupportLock, tenant.String()+"/support/"+caseID.String())
	return mapError(err, "lock support case")
}

func optUUIDOf[T interface {
	uuider
	IsZero() bool
}](v T) *uuid.UUID {
	if v.IsZero() {
		return nil
	}
	u := v.UUID()
	return &u
}

// Create records a case and its OPENED event.
func (r *SupportRepo) Create(ctx context.Context, c support.Case, opened support.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if c.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The case belongs to a tenant outside the caller's scope.")
	}
	if err := c.Validate(); err != nil {
		return obligationFault(err, "The support case is not well-formed.")
	}
	if opened.Action != support.ActionOpened || opened.Seq != 1 {
		return obligationFault(nil, "A support case is created by its OPENED event.")
	}
	evidence := c.Evidence
	if evidence == nil {
		evidence = []string{}
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlSupportCaseInsert, tenant.UUID(), c.ID.UUID(), string(c.Type), string(c.Severity),
		c.Subject, c.Description, optString(c.Product), optUUIDOf(c.LegalEntity), optString(c.Period),
		optString(c.ContractRef), optString(c.SellerRef), optUUIDOf(c.Refs.Decision), optUUIDOf(c.Refs.Document),
		optUUIDOf(c.Refs.Obligation), optUUIDOf(c.Refs.Remittance), optTimestamp(c.StatutoryDueAt), c.SLADueAt.UTC(),
		optUUIDOf(c.Assignee), evidence, c.OpenedAt.UTC(), c.OpenedBy.UUID()); err != nil {
		return mapError(err, "insert support case")
	}
	return r.Append(ctx, c.ID, opened)
}

// Append records one event.
func (r *SupportRepo) Append(ctx context.Context, caseID id.SupportCaseID, e support.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	var (
		missing []string
		reopens *int
	)
	for _, f := range e.MissingFacts {
		missing = append(missing, string(f))
	}
	if e.Reopens > 0 {
		reopens = &e.Reopens
	}
	var evidence []string
	if len(e.Evidence) > 0 {
		evidence = e.Evidence
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlSupportEventInsert, tenant.UUID(), caseID.UUID(), e.Seq, string(e.Action),
		string(e.Status), optString(e.Note), optString(string(e.Type)), optString(string(e.Severity)),
		optUUIDOf(e.LegalEntity), optString(e.Period), optString(e.ContractRef), optString(e.SellerRef),
		optTimestamp(e.StatutoryDueAt), optTimestamp(e.SLADueAt), optUUIDOf(e.Assignee), optString(string(e.EscalateTo)),
		evidence, missing, reopens, e.RecordedAt.UTC(), e.RecordedBy.UUID())
	return mapError(err, "insert support case event")
}

// Case returns one case.
func (r *SupportRepo) Case(ctx context.Context, caseID id.SupportCaseID) (support.Case, error) {
	got, err := r.cases(ctx, ` AND case_id = $2`, caseID.UUID())
	if err != nil {
		return support.Case{}, err
	}
	if len(got) == 0 {
		return support.Case{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such support case.")
	}
	return got[0], nil
}

// List returns the tenant's most recently opened cases.
func (r *SupportRepo) List(ctx context.Context, limit int) ([]support.Case, error) {
	if limit <= 0 || limit > supportListCap {
		limit = supportListCap
	}
	return r.cases(ctx, ` AND case_id IN (SELECT case_id FROM ztax.support_case WHERE tenant_id = $1
		ORDER BY opened_at DESC, case_id DESC LIMIT $2)`, limit)
}

// cases loads the cases a fixed predicate from this file selects, newest
// first, and folds each with its events.
func (r *SupportRepo) cases(ctx context.Context, where string, arg any) ([]support.Case, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, sqlSupportCaseSelect+where+` ORDER BY opened_at DESC, case_id DESC`, tenant.UUID(), arg)
	if err != nil {
		return nil, mapError(err, "read support cases")
	}
	var out []support.Case
	for rows.Next() {
		var (
			c                                             = support.Case{TenantID: tenant}
			cid, by                                       uuid.UUID
			typ, severity                                 string
			product, period, contract, seller             *string
			entity, decision, document, obligation, remit *uuid.UUID
			assignee                                      *uuid.UUID
			statutory                                     *time.Time
		)
		if err := rows.Scan(&cid, &typ, &severity, &c.Subject, &c.Description, &product, &entity, &period, &contract,
			&seller, &decision, &document, &obligation, &remit, &statutory, &c.SLADueAt, &assignee, &c.Evidence,
			&c.OpenedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan support case")
		}
		c.ID, c.OpenedBy, c.Type, c.Severity = id.NewSupportCaseID(cid), id.NewUserID(by), support.Type(typ), support.Severity(severity)
		c.Product, c.Period, c.ContractRef, c.SellerRef = deref(product), deref(period), deref(contract), deref(seller)
		c.SLADueAt, c.OpenedAt = c.SLADueAt.UTC(), c.OpenedAt.UTC()
		if entity != nil {
			c.LegalEntity = id.NewLegalEntityID(*entity)
		}
		if decision != nil {
			c.Refs.Decision = id.NewDecisionID(*decision)
		}
		if document != nil {
			c.Refs.Document = id.NewFiscalDocumentID(*document)
		}
		if obligation != nil {
			c.Refs.Obligation = id.NewObligationID(*obligation)
		}
		if remit != nil {
			c.Refs.Remittance = id.NewRemittanceID(*remit)
		}
		if assignee != nil {
			c.Assignee = id.NewUserID(*assignee)
		}
		if statutory != nil {
			c.StatutoryDueAt = statutory.UTC()
		}
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read support cases")
	}
	if len(out) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, len(out))
	for i, c := range out {
		ids[i] = c.ID.UUID()
	}
	rows, err = db.Query(ctx, sqlSupportEventSelect+` AND case_id = ANY($2) ORDER BY case_id, seq`, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read support case events")
	}
	defer rows.Close()
	events := map[id.SupportCaseID][]support.Event{}
	for rows.Next() {
		var (
			e                                                 support.Event
			cid, by                                           uuid.UUID
			action, status                                    string
			note, typ, severity, period, contract, seller, to *string
			entity, assignee                                  *uuid.UUID
			statutory, sla                                    *time.Time
			missing                                           []string
			reopens                                           *int
		)
		if err := rows.Scan(&cid, &e.Seq, &action, &status, &note, &typ, &severity, &entity, &period, &contract, &seller,
			&statutory, &sla, &assignee, &to, &e.Evidence, &missing, &reopens, &e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan support case event")
		}
		e.Action, e.Status, e.Note = support.Action(action), support.Status(status), deref(note)
		e.Type, e.Severity, e.EscalateTo = support.Type(deref(typ)), support.Severity(deref(severity)), support.EscalationTarget(deref(to))
		e.Period, e.ContractRef, e.SellerRef = deref(period), deref(contract), deref(seller)
		e.RecordedAt, e.RecordedBy = e.RecordedAt.UTC(), id.NewUserID(by)
		if entity != nil {
			e.LegalEntity = id.NewLegalEntityID(*entity)
		}
		if assignee != nil {
			e.Assignee = id.NewUserID(*assignee)
		}
		if statutory != nil {
			e.StatutoryDueAt = statutory.UTC()
		}
		if sla != nil {
			e.SLADueAt = sla.UTC()
		}
		for _, f := range missing {
			e.MissingFacts = append(e.MissingFacts, support.Fact(f))
		}
		if reopens != nil {
			e.Reopens = *reopens
		}
		k := id.NewSupportCaseID(cid)
		events[k] = append(events[k], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read support case events")
	}
	for i := range out {
		folded, err := support.Fold(out[i], events[out[i].ID])
		if err != nil {
			return nil, obligationFault(err, "A stored support case's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}
