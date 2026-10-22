package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/remittance"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// remittance_beneficiary(_event), remittance, remittance_step — migration 000023
// ---------------------------------------------------------------------------

const (
	sqlBeneficiaryColumns = `beneficiary_id, supersedes, country_code, authority, legal_name, payment_rail, account_ref,
		reference_rule, currencies, source, created_at, created_by`
	sqlBeneficiaryInsert = `INSERT INTO ztax.remittance_beneficiary (tenant_id, ` + sqlBeneficiaryColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`
	sqlBeneficiarySelect      = `SELECT ` + sqlBeneficiaryColumns + ` FROM ztax.remittance_beneficiary WHERE tenant_id = $1`
	sqlBeneficiaryEventInsert = `INSERT INTO ztax.remittance_beneficiary_event
		(tenant_id, beneficiary_id, seq, kind, evidence, recorded_at, recorded_by) VALUES ($1, $2, $3, $4, $5, $6, $7)`
	sqlBeneficiaryEvents = `SELECT beneficiary_id, seq, kind, evidence, recorded_at, recorded_by
		FROM ztax.remittance_beneficiary_event WHERE tenant_id = $1`

	sqlRemittanceColumns = `remittance_id, obligation_id, obligation_key, legal_entity_id, authority, period_start,
		period_end, payment_due, legal_timezone, currency, amount, beneficiary_id, funding_ref, payment_reference,
		execution_date, operating_mode, supersedes, instruction_hash, created_at, created_by`
	sqlRemittanceInsert = `INSERT INTO ztax.remittance (tenant_id, ` + sqlRemittanceColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)`
	sqlRemittanceSelect = `SELECT ` + sqlRemittanceColumns + ` FROM ztax.remittance WHERE tenant_id = $1`

	sqlRemittanceStepColumns = `remittance_id, seq, kind, status_after, instruction_hash, submission_id, evidence_kind,
		resolution, provider_ref, raw, note, recorded_at, recorded_by`
	sqlRemittanceStepInsert = `INSERT INTO ztax.remittance_step (tenant_id, ` + sqlRemittanceStepColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`
	sqlRemittanceStepSelect = `SELECT ` + sqlRemittanceStepColumns + ` FROM ztax.remittance_step WHERE tenant_id = $1`

	sqlRemittanceLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	remittanceListCap = 500
)

// RemittanceRepo implements port.RemittanceRepository.
type RemittanceRepo struct{ s *Store }

// Remittances returns the remittance repository.
func (s *Store) Remittances() *RemittanceRepo { return &RemittanceRepo{s: s} }

var _ port.RemittanceRepository = (*RemittanceRepo)(nil)

// Lock takes one of the tenant's remittance locks for the transaction.
func (r *RemittanceRepo) Lock(ctx context.Context, key string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlRemittanceLock, tenant.String()+"/remittance/"+key)
	return mapError(err, "lock remittance")
}

// CreateBeneficiary records a payee version and its first event.
func (r *RemittanceRepo) CreateBeneficiary(ctx context.Context, b remittance.Beneficiary, created remittance.BeneficiaryEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if b.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The beneficiary belongs to a tenant outside the caller's scope.")
	}
	if err := b.Validate(); err != nil {
		return obligationFault(err, "The beneficiary is not well-formed.")
	}
	if created.Kind != remittance.BeneficiaryCreated || created.Seq != 1 {
		return obligationFault(nil, "A beneficiary version is created by its CREATED event.")
	}
	var supersedes *uuid.UUID
	if !b.Supersedes.IsZero() {
		u := b.Supersedes.UUID()
		supersedes = &u
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlBeneficiaryInsert, tenant.UUID(), b.ID.UUID(), supersedes, b.Country, b.Authority,
		b.LegalName, string(b.Rail), b.AccountRef, optString(b.ReferenceRule), b.Currencies, b.Source,
		b.CreatedAt.UTC(), b.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert beneficiary")
	}
	return r.AppendBeneficiaryEvent(ctx, b.ID, created)
}

// AppendBeneficiaryEvent records one event.
func (r *RemittanceRepo) AppendBeneficiaryEvent(ctx context.Context, beneficiaryID id.BeneficiaryID, e remittance.BeneficiaryEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlBeneficiaryEventInsert, tenant.UUID(), beneficiaryID.UUID(), e.Seq, string(e.Kind),
		optString(e.Evidence), e.RecordedAt.UTC(), e.RecordedBy.UUID())
	return mapError(err, "insert beneficiary event")
}

// Beneficiary returns one payee version.
func (r *RemittanceRepo) Beneficiary(ctx context.Context, beneficiaryID id.BeneficiaryID) (remittance.Beneficiary, error) {
	got, err := r.beneficiaries(ctx, ` AND beneficiary_id = $2`, beneficiaryID.UUID())
	if err != nil {
		return remittance.Beneficiary{}, err
	}
	if len(got) == 0 {
		return remittance.Beneficiary{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such beneficiary.")
	}
	return got[0], nil
}

// Beneficiaries returns every payee version of the tenant, newest first.
func (r *RemittanceRepo) Beneficiaries(ctx context.Context) ([]remittance.Beneficiary, error) {
	return r.beneficiaries(ctx, ``, nil)
}

func (r *RemittanceRepo) beneficiaries(ctx context.Context, where string, arg any) ([]remittance.Beneficiary, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	args := []any{tenant.UUID()}
	if arg != nil {
		args = append(args, arg)
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, sqlBeneficiarySelect+where+` ORDER BY created_at DESC, beneficiary_id DESC`, args...)
	if err != nil {
		return nil, mapError(err, "read beneficiaries")
	}
	var out []remittance.Beneficiary
	for rows.Next() {
		var (
			b             = remittance.Beneficiary{TenantID: tenant}
			bid, by       uuid.UUID
			supersedes    *uuid.UUID
			rail          string
			referenceRule *string
		)
		if err := rows.Scan(&bid, &supersedes, &b.Country, &b.Authority, &b.LegalName, &rail, &b.AccountRef, &referenceRule,
			&b.Currencies, &b.Source, &b.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan beneficiary")
		}
		b.ID, b.CreatedBy, b.Rail, b.ReferenceRule, b.CreatedAt = id.NewBeneficiaryID(bid), id.NewUserID(by),
			remittance.Rail(rail), deref(referenceRule), b.CreatedAt.UTC()
		if supersedes != nil {
			b.Supersedes = id.NewBeneficiaryID(*supersedes)
		}
		out = append(out, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read beneficiaries")
	}
	if len(out) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(out))
	for i, b := range out {
		ids[i] = b.ID.UUID()
	}
	rows, err = db.Query(ctx, sqlBeneficiaryEvents+` AND beneficiary_id = ANY($2) ORDER BY beneficiary_id, seq`, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read beneficiary events")
	}
	defer rows.Close()
	events := map[id.BeneficiaryID][]remittance.BeneficiaryEvent{}
	for rows.Next() {
		var (
			e        remittance.BeneficiaryEvent
			bid, by  uuid.UUID
			kind     string
			evidence *string
		)
		if err := rows.Scan(&bid, &e.Seq, &kind, &evidence, &e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan beneficiary event")
		}
		e.Kind, e.Evidence, e.RecordedAt, e.RecordedBy = remittance.BeneficiaryEventKind(kind), deref(evidence), e.RecordedAt.UTC(), id.NewUserID(by)
		k := id.NewBeneficiaryID(bid)
		events[k] = append(events[k], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read beneficiary events")
	}
	for i := range out {
		folded, err := remittance.FoldBeneficiary(out[i], events[out[i].ID])
		if err != nil {
			return nil, obligationFault(err, "A stored beneficiary's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}

// Create records an instruction and its CREATED step.
func (r *RemittanceRepo) Create(ctx context.Context, rem remittance.Remittance, created remittance.Step) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if rem.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The payment belongs to a tenant outside the caller's scope.")
	}
	if err := rem.Validate(); err != nil {
		return obligationFault(err, "The payment instruction is not well-formed.")
	}
	if rem.InstructionHash.IsZero() {
		return obligationFault(nil, "A payment instruction is stored with its instruction hash.")
	}
	if created.Kind != remittance.StepCreated || created.Seq != 1 {
		return obligationFault(nil, "A payment instruction is created by its CREATED step.")
	}
	amount, err := MoneyValue(rem.Amount)
	if err != nil {
		return err
	}
	var supersedes *uuid.UUID
	if !rem.Supersedes.IsZero() {
		u := rem.Supersedes.UUID()
		supersedes = &u
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlRemittanceInsert, tenant.UUID(), rem.ID.UUID(), rem.Obligation.UUID(), rem.ObligationKey,
		rem.LegalEntity.UUID(), rem.Authority, civilDate(rem.PeriodStart), civilDate(rem.PeriodEnd), rem.PaymentDue.UTC(),
		rem.Timezone, string(rem.Amount.Currency()), amount, rem.Beneficiary.UUID(), rem.FundingRef, rem.PaymentReference,
		civilDate(rem.ExecutionDate), string(rem.Mode), supersedes, rem.InstructionHash.String(),
		rem.CreatedAt.UTC(), rem.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert remittance")
	}
	return r.AppendStep(ctx, rem.ID, created)
}

// AppendStep records one step.
func (r *RemittanceRepo) AppendStep(ctx context.Context, remittanceID id.RemittanceID, st remittance.Step) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	var (
		hash    *string
		attempt *uuid.UUID
	)
	if !st.InstructionHash.IsZero() {
		h := st.InstructionHash.String()
		hash = &h
	}
	if !st.Submission.IsZero() {
		u := st.Submission.UUID()
		attempt = &u
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlRemittanceStepInsert, tenant.UUID(), remittanceID.UUID(), st.Seq, string(st.Kind),
		string(st.Status), hash, attempt, optString(string(st.Evidence)), optString(string(st.Resolution)),
		optString(st.ProviderRef), optString(st.Raw), optString(st.Note), st.RecordedAt.UTC(), st.RecordedBy.UUID())
	return mapError(err, "insert remittance step")
}

// Remittance returns one instruction.
func (r *RemittanceRepo) Remittance(ctx context.Context, remittanceID id.RemittanceID) (remittance.Remittance, error) {
	got, err := r.remittances(ctx, ` AND remittance_id = $2`, remittanceID.UUID())
	if err != nil {
		return remittance.Remittance{}, err
	}
	if len(got) == 0 {
		return remittance.Remittance{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such payment.")
	}
	return got[0], nil
}

// ByObligation returns an obligation's instructions, oldest first.
func (r *RemittanceRepo) ByObligation(ctx context.Context, obligationKey string) ([]remittance.Remittance, error) {
	got, err := r.remittances(ctx, ` AND obligation_key = $2`, obligationKey)
	if err != nil {
		return nil, err
	}
	// Read in due order; within one obligation that is creation order.
	for i := 1; i < len(got); i++ {
		for j := i; j > 0 && got[j].CreatedAt.Before(got[j-1].CreatedAt); j-- {
			got[j], got[j-1] = got[j-1], got[j]
		}
	}
	return got, nil
}

// List returns the tenant's instructions, by payment due date.
func (r *RemittanceRepo) List(ctx context.Context, limit int) ([]remittance.Remittance, error) {
	if limit <= 0 || limit > remittanceListCap {
		limit = remittanceListCap
	}
	return r.remittances(ctx, ` AND remittance_id IN (SELECT remittance_id FROM ztax.remittance WHERE tenant_id = $1
		ORDER BY payment_due, remittance_id LIMIT $2)`, limit)
}

// remittances loads the instructions a fixed predicate from this file selects.
func (r *RemittanceRepo) remittances(ctx context.Context, where string, arg any) ([]remittance.Remittance, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, sqlRemittanceSelect+where+` ORDER BY payment_due, created_at, remittance_id`, tenant.UUID(), arg)
	if err != nil {
		return nil, mapError(err, "read remittances")
	}
	var out []remittance.Remittance
	for rows.Next() {
		var (
			rem                   = remittance.Remittance{TenantID: tenant}
			rid, obl, le, ben, by uuid.UUID
			supersedes            *uuid.UUID
			start, end, execution pgtype.Date
			currency, mode, hash  string
			amount                Numeric
		)
		if err := rows.Scan(&rid, &obl, &rem.ObligationKey, &le, &rem.Authority, &start, &end, &rem.PaymentDue, &rem.Timezone,
			&currency, &amount, &ben, &rem.FundingRef, &rem.PaymentReference, &execution, &mode, &supersedes, &hash,
			&rem.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan remittance")
		}
		loc, err := time.LoadLocation(rem.Timezone)
		if err != nil {
			rows.Close()
			return nil, obligationFault(err, "A stored payment names a timezone this build does not know.")
		}
		m, err := amount.Money(fiscal.Currency(currency))
		if err != nil {
			rows.Close()
			return nil, err
		}
		d, err := canonical.ParseDigest(hash)
		if err != nil {
			rows.Close()
			return nil, obligationFault(err, "A stored payment's instruction hash does not parse.")
		}
		rem.ID, rem.Obligation, rem.LegalEntity, rem.Beneficiary, rem.CreatedBy = id.NewRemittanceID(rid), id.NewObligationID(obl),
			id.NewLegalEntityID(le), id.NewBeneficiaryID(ben), id.NewUserID(by)
		rem.PeriodStart, rem.PeriodEnd, rem.ExecutionDate = inLocation(start, loc), inLocation(end, loc), inLocation(execution, loc)
		rem.PaymentDue, rem.CreatedAt, rem.Amount, rem.Mode, rem.InstructionHash = rem.PaymentDue.In(loc), rem.CreatedAt.UTC(), m,
			remittance.Mode(mode), d
		if supersedes != nil {
			rem.Supersedes = id.NewRemittanceID(*supersedes)
		}
		out = append(out, rem)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read remittances")
	}
	if len(out) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, len(out))
	for i, rem := range out {
		ids[i] = rem.ID.UUID()
	}
	rows, err = db.Query(ctx, sqlRemittanceStepSelect+` AND remittance_id = ANY($2) ORDER BY remittance_id, seq`, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read remittance steps")
	}
	defer rows.Close()
	steps := map[id.RemittanceID][]remittance.Step{}
	for rows.Next() {
		var (
			st                                         remittance.Step
			rid, by                                    uuid.UUID
			kind, status                               string
			hash, evidence, resolution, ref, raw, note *string
			attempt                                    *uuid.UUID
		)
		if err := rows.Scan(&rid, &st.Seq, &kind, &status, &hash, &attempt, &evidence, &resolution, &ref, &raw, &note,
			&st.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan remittance step")
		}
		st.Kind, st.Status, st.RecordedAt, st.RecordedBy = remittance.StepKind(kind), remittance.Status(status), st.RecordedAt.UTC(), id.NewUserID(by)
		if hash != nil {
			d, err := canonical.ParseDigest(*hash)
			if err != nil {
				return nil, obligationFault(err, "A stored payment step carries a hash that does not parse.")
			}
			st.InstructionHash = d
		}
		if attempt != nil {
			st.Submission = id.NewSubmissionAttemptID(*attempt)
		}
		st.Evidence, st.Resolution = remittance.Evidence(deref(evidence)), remittance.Resolution(deref(resolution))
		st.ProviderRef, st.Raw, st.Note = deref(ref), deref(raw), deref(note)
		k := id.NewRemittanceID(rid)
		steps[k] = append(steps[k], st)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read remittance steps")
	}
	for i := range out {
		folded, err := remittance.Fold(out[i], steps[out[i].ID])
		if err != nil {
			return nil, obligationFault(err, "A stored payment's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}
