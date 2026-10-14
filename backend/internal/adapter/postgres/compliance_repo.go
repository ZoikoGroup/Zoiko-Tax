package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/compliance"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// compliance_package, compliance_step — migration 000022
// ---------------------------------------------------------------------------

const (
	sqlPackageColumns = `package_id, obligation_id, obligation_key, responsibility_id, legal_entity_id, obligation_type,
		jurisdiction_id, period_start, period_end, operating_mode, channel, filing_country, filing_authority,
		statutory_due, legal_timezone, prepare_due, review_due, submit_target, created_at, created_by`
	sqlPackageInsert = `INSERT INTO ztax.compliance_package (tenant_id, ` + sqlPackageColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)`
	sqlPackageSelect = `SELECT ` + sqlPackageColumns + ` FROM ztax.compliance_package WHERE tenant_id = $1`

	sqlStepColumns = `package_id, seq, kind, status_after, version, payload_hash, control_total, currency,
		review_decision, submission_id, channel, acknowledgement, resolution, authority_ref, evidence, note,
		recorded_at, recorded_by`
	sqlStepInsert = `INSERT INTO ztax.compliance_step (tenant_id, ` + sqlStepColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`
	sqlStepSelect = `SELECT ` + sqlStepColumns + ` FROM ztax.compliance_step WHERE tenant_id = $1`

	sqlPackageLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	// packageListCap bounds a list read.
	packageListCap = 500
)

// ComplianceRepo implements port.ComplianceRepository.
type ComplianceRepo struct{ s *Store }

// Compliance returns the compliance package repository.
func (s *Store) Compliance() *ComplianceRepo { return &ComplianceRepo{s: s} }

var _ port.ComplianceRepository = (*ComplianceRepo)(nil)

// Lock takes one obligation's package lock for the transaction.
func (r *ComplianceRepo) Lock(ctx context.Context, obligationKey string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlPackageLock, tenant.String()+"/compliance/"+obligationKey)
	return mapError(err, "lock compliance package")
}

// Create records a package and its CREATED step.
func (r *ComplianceRepo) Create(ctx context.Context, p compliance.Package, created compliance.Step) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if p.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The package belongs to a tenant outside the caller's scope.")
	}
	if err := p.Validate(); err != nil {
		return obligationFault(err, "The compliance package is not well-formed.")
	}
	if created.Kind != compliance.StepCreated || created.Seq != 1 {
		return obligationFault(nil, "A compliance package is created by its CREATED step.")
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlPackageInsert, tenant.UUID(), p.ID.UUID(), p.Obligation.UUID(), p.ObligationKey,
		optResponsibility(p.Responsibility), p.LegalEntity.UUID(), p.ObligationType, p.Jurisdiction, civilDate(p.PeriodStart),
		civilDate(p.PeriodEnd), string(p.Mode), string(p.Channel), p.FilingCountry, p.FilingAuthority, p.StatutoryDue.UTC(),
		p.Timezone, optTimestamp(p.PrepareDue), optTimestamp(p.ReviewDue), optTimestamp(p.SubmitTarget),
		p.CreatedAt.UTC(), p.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert compliance package")
	}
	return r.AppendStep(ctx, p.ID, created)
}

// AppendStep records one step.
func (r *ComplianceRepo) AppendStep(ctx context.Context, packageID id.CompliancePackageID, s compliance.Step) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	var (
		total    any
		currency *string
		version  *int
		hash     *string
		attempt  *uuid.UUID
	)
	if s.ControlTotal != nil {
		n, err := MoneyValue(*s.ControlTotal)
		if err != nil {
			return err
		}
		total = n
		c := string(s.ControlTotal.Currency())
		currency = &c
	}
	if s.Version > 0 {
		v := s.Version
		version = &v
	}
	if !s.PayloadHash.IsZero() {
		h := s.PayloadHash.String()
		hash = &h
	}
	if !s.Submission.IsZero() {
		u := s.Submission.UUID()
		attempt = &u
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlStepInsert, tenant.UUID(), packageID.UUID(), s.Seq, string(s.Kind), string(s.Status),
		version, hash, total, currency, optString(string(s.Review)), attempt, optString(string(s.Channel)),
		optString(string(s.Acknowledgement)), optString(string(s.Resolution)), optString(s.AuthorityRef),
		optString(s.Evidence), optString(s.Note), s.RecordedAt.UTC(), s.RecordedBy.UUID())
	return mapError(err, "insert compliance step")
}

// Package returns one package.
func (r *ComplianceRepo) Package(ctx context.Context, packageID id.CompliancePackageID) (compliance.Package, error) {
	got, err := r.read(ctx, ` AND package_id = $2`, packageID.UUID())
	if err != nil {
		return compliance.Package{}, err
	}
	if len(got) == 0 {
		return compliance.Package{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such compliance package.")
	}
	return got[0], nil
}

// ByObligation returns the package for an obligation's business key.
func (r *ComplianceRepo) ByObligation(ctx context.Context, obligationKey string) (compliance.Package, error) {
	got, err := r.read(ctx, ` AND package_id = (SELECT package_id FROM ztax.compliance_package
		WHERE tenant_id = $1 AND obligation_key = $2)`, obligationKey)
	if err != nil {
		return compliance.Package{}, err
	}
	if len(got) == 0 {
		return compliance.Package{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "The obligation has no compliance package.")
	}
	return got[0], nil
}

// List returns the tenant's packages, soonest statutory deadline first.
func (r *ComplianceRepo) List(ctx context.Context, limit int) ([]compliance.Package, error) {
	if limit <= 0 || limit > packageListCap {
		limit = packageListCap
	}
	return r.read(ctx, ` AND package_id IN (SELECT package_id FROM ztax.compliance_package WHERE tenant_id = $1
		ORDER BY statutory_due, package_id LIMIT $2)`, limit)
}

// read loads the packages a predicate selects and folds each one's steps. The
// predicate is a fixed string from this file, never caller text.
func (r *ComplianceRepo) read(ctx context.Context, where string, arg any) ([]compliance.Package, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)

	rows, err := db.Query(ctx, sqlPackageSelect+where+` ORDER BY statutory_due, package_id`, tenant.UUID(), arg)
	if err != nil {
		return nil, mapError(err, "read compliance packages")
	}
	var out []compliance.Package
	for rows.Next() {
		var (
			p                                   = compliance.Package{TenantID: tenant}
			pkg, obl, le, by                    uuid.UUID
			resp                                *uuid.UUID
			start, end                          pgtype.Date
			mode, channel                       string
			prepareDue, reviewDue, submitTarget *time.Time
		)
		if err := rows.Scan(&pkg, &obl, &p.ObligationKey, &resp, &le, &p.ObligationType, &p.Jurisdiction, &start, &end,
			&mode, &channel, &p.FilingCountry, &p.FilingAuthority, &p.StatutoryDue, &p.Timezone, &prepareDue, &reviewDue,
			&submitTarget, &p.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan compliance package")
		}
		loc, err := time.LoadLocation(p.Timezone)
		if err != nil {
			rows.Close()
			return nil, obligationFault(err, "A stored compliance package names a timezone this build does not know.")
		}
		p.ID, p.Obligation = id.NewCompliancePackageID(pkg), id.NewObligationID(obl)
		if resp != nil {
			p.Responsibility = id.NewResponsibilityDecisionID(*resp)
		}
		p.LegalEntity, p.CreatedBy = id.NewLegalEntityID(le), id.NewUserID(by)
		p.PeriodStart, p.PeriodEnd = inLocation(start, loc), inLocation(end, loc)
		p.Mode, p.Channel = compliance.Mode(mode), compliance.Channel(channel)
		p.StatutoryDue, p.CreatedAt = p.StatutoryDue.In(loc), p.CreatedAt.UTC()
		p.PrepareDue, p.ReviewDue, p.SubmitTarget = derefTime(prepareDue, loc), derefTime(reviewDue, loc), derefTime(submitTarget, loc)
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read compliance packages")
	}
	if len(out) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, len(out))
	for i, p := range out {
		ids[i] = p.ID.UUID()
	}
	rows, err = db.Query(ctx, sqlStepSelect+` AND package_id = ANY($2) ORDER BY package_id, seq`, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read compliance steps")
	}
	defer rows.Close()
	steps := map[id.CompliancePackageID][]compliance.Step{}
	for rows.Next() {
		var (
			s                                                        compliance.Step
			pkg, by                                                  uuid.UUID
			kind, status                                             string
			version                                                  *int
			hash, currency, review, channel, ack, res, ref, ev, note *string
			attempt                                                  *uuid.UUID
			total                                                    Numeric
		)
		if err := rows.Scan(&pkg, &s.Seq, &kind, &status, &version, &hash, &total, &currency, &review, &attempt, &channel,
			&ack, &res, &ref, &ev, &note, &s.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan compliance step")
		}
		s.Kind, s.Status, s.RecordedAt, s.RecordedBy = compliance.StepKind(kind), compliance.Status(status), s.RecordedAt.UTC(), id.NewUserID(by)
		if version != nil {
			s.Version = *version
		}
		if hash != nil {
			d, err := canonical.ParseDigest(*hash)
			if err != nil {
				return nil, obligationFault(err, "A stored compliance step carries a payload hash that does not parse.")
			}
			s.PayloadHash = d
		}
		if total.Valid {
			m, err := total.Money(fiscal.Currency(deref(currency)))
			if err != nil {
				return nil, err
			}
			s.ControlTotal = &m
		}
		if attempt != nil {
			s.Submission = id.NewSubmissionAttemptID(*attempt)
		}
		s.Review, s.Channel = compliance.ReviewDecision(deref(review)), compliance.Channel(deref(channel))
		s.Acknowledgement, s.Resolution = compliance.Acknowledgement(deref(ack)), compliance.Resolution(deref(res))
		s.AuthorityRef, s.Evidence, s.Note = deref(ref), deref(ev), deref(note)
		k := id.NewCompliancePackageID(pkg)
		steps[k] = append(steps[k], s)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read compliance steps")
	}
	for i := range out {
		folded, err := compliance.Fold(out[i], steps[out[i].ID])
		if err != nil {
			return nil, obligationFault(err, "A stored compliance package's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}

func optTimestamp(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func derefTime(t *time.Time, loc *time.Location) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.In(loc)
}

func optResponsibility(r id.ResponsibilityDecisionID) *uuid.UUID {
	if r.IsZero() {
		return nil
	}
	u := r.UUID()
	return &u
}
