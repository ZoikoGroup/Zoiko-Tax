package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ctc"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// ctc_* — migration 000036
// ---------------------------------------------------------------------------

const (
	sqlCTCAppColumns = `applicability_id, legal_entity_id, country, document_type, buyer_kind, created_at, created_by`
	sqlCTCAppInsert  = `INSERT INTO ztax.ctc_applicability (tenant_id, ` + sqlCTCAppColumns + `) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	sqlCTCAppSelect  = `SELECT ` + sqlCTCAppColumns + ` FROM ztax.ctc_applicability WHERE tenant_id = $1`
	sqlCTCAppByID    = sqlCTCAppSelect + ` AND applicability_id = $2`
	// Every filter is a parameter; an empty one matches everything. The
	// status filter is applied after folding.
	sqlCTCAppList = sqlCTCAppSelect + ` AND ($2::uuid IS NULL OR legal_entity_id = $2) AND ($3 = '' OR country = $3)
		ORDER BY created_at, applicability_id LIMIT $4`

	sqlCTCVersionColumns = `applicability_id, version, duty, model, profile, format_version, authority, legal_source, source_as_of,
		effective_from, effective_to, conditions, digest, recorded_at, recorded_by`
	sqlCTCVersionInsert = `INSERT INTO ztax.ctc_applicability_version (tenant_id, ` + sqlCTCVersionColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`
	sqlCTCVersions = `SELECT ` + sqlCTCVersionColumns + ` FROM ztax.ctc_applicability_version
		WHERE tenant_id = $1 AND applicability_id = ANY($2) ORDER BY applicability_id, version`

	sqlCTCAppEventColumns = `applicability_id, seq, kind, status_after, version, gate, signoff_ref, reason, evidence_sha256,
		evidence_name, evidence_media, evidence_bytes, recorded_at, recorded_by`
	sqlCTCAppEventInsert = `INSERT INTO ztax.ctc_applicability_event (tenant_id, ` + sqlCTCAppEventColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`
	sqlCTCAppEvents = `SELECT ` + sqlCTCAppEventColumns + ` FROM ztax.ctc_applicability_event
		WHERE tenant_id = $1 AND applicability_id = ANY($2) ORDER BY applicability_id, seq`

	sqlCTCValColumns = `validation_id, document_id, applicability_id, applicability_version, profile, format_version, ruleset,
		input_digest, outcome, report_digest, validated_at, validated_by`
	sqlCTCValInsert = `INSERT INTO ztax.ctc_validation (tenant_id, ` + sqlCTCValColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`
	sqlCTCValSelect = `SELECT ` + sqlCTCValColumns + ` FROM ztax.ctc_validation WHERE tenant_id = $1`
	sqlCTCValByID   = sqlCTCValSelect + ` AND validation_id = $2`
	sqlCTCValList   = sqlCTCValSelect + ` AND ($2::uuid IS NULL OR document_id = $2) AND ($3::uuid IS NULL OR applicability_id = $3)
		ORDER BY validated_at DESC, validation_id DESC LIMIT $4`
	sqlCTCResultInsert = `INSERT INTO ztax.ctc_validation_result (tenant_id, validation_id, ordinal, rule_id, stage, severity, outcome,
		reason_code, business_term, detail) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	sqlCTCResults = `SELECT validation_id, rule_id, stage, severity, outcome, reason_code, business_term, detail
		FROM ztax.ctc_validation_result WHERE tenant_id = $1 AND validation_id = ANY($2) ORDER BY validation_id, ordinal`

	sqlCTCSubColumns = `submission_id, document_id, document_number, document_type, legal_entity_id, country, buyer_kind,
		applicability_id, applicability_version, model, profile, format_version, validation_id, currency, gross_total,
		payload_digest, prepared_at, prepared_by`
	sqlCTCSubInsert = `INSERT INTO ztax.ctc_submission (tenant_id, ` + sqlCTCSubColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`
	sqlCTCSubSelect = `SELECT ` + sqlCTCSubColumns + ` FROM ztax.ctc_submission WHERE tenant_id = $1`
	sqlCTCSubByID   = sqlCTCSubSelect + ` AND submission_id = $2`
	// The status filter reads the latest event, which is the folded status.
	sqlCTCSubList = sqlCTCSubSelect + ` AND ($2::uuid IS NULL OR document_id = $2) AND ($3::uuid IS NULL OR applicability_id = $3)
		AND ($4 = '' OR (SELECT e.status_after FROM ztax.ctc_submission_event e
			WHERE e.tenant_id = ctc_submission.tenant_id AND e.submission_id = ctc_submission.submission_id
			ORDER BY e.seq DESC LIMIT 1) = $4)
		ORDER BY prepared_at DESC, submission_id DESC LIMIT $5`
	sqlCTCSubEventColumns = `submission_id, seq, kind, status_after, attempt_id, transport_ref, authority_ref, authority_code,
		evidence_sha256, detail, recorded_at, recorded_by`
	sqlCTCSubEventInsert = `INSERT INTO ztax.ctc_submission_event (tenant_id, ` + sqlCTCSubEventColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`
	sqlCTCSubEvents = `SELECT ` + sqlCTCSubEventColumns + ` FROM ztax.ctc_submission_event
		WHERE tenant_id = $1 AND submission_id = ANY($2) ORDER BY submission_id, seq`

	sqlCTCLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	ctcListCap = 500
)

// CTCRepo implements port.CTCRepository.
type CTCRepo struct{ s *Store }

// CTC returns the e-invoicing / CTC repository.
func (s *Store) CTC() *CTCRepo { return &CTCRepo{s: s} }

var _ port.CTCRepository = (*CTCRepo)(nil)

func ctcFault(err error, msg string) error { return obligationFault(err, msg) }

func optDay(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	d := ctc.Day(t)
	return &d
}

func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func nilUUID(u uuid.UUID, zero bool) *uuid.UUID {
	if zero {
		return nil
	}
	return &u
}

// Lock takes one of the tenant's CTC locks for the transaction.
func (r *CTCRepo) Lock(ctx context.Context, key string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlCTCLock, tenant.String()+"/ctc/"+key)
	return mapError(err, "lock ctc")
}

// CreateApplicability writes a record, its first version and its CREATED
// event.
func (r *CTCRepo) CreateApplicability(ctx context.Context, a ctc.Applicability) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if a.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The record belongs to a tenant outside the caller's scope.")
	}
	if len(a.Versions) != 1 || len(a.History) != 1 || a.History[0].Kind != ctc.EventCreated {
		return ctcFault(nil, "A record is created with its first version and its CREATED event.")
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlCTCAppInsert, tenant.UUID(), a.ID.UUID(), a.LegalEntity.UUID(), a.Country,
		string(a.DocumentType), string(a.BuyerKind), a.CreatedAt.UTC(), a.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert ctc applicability")
	}
	if err := r.AppendApplicabilityVersion(ctx, a.ID, a.Versions[0]); err != nil {
		return err
	}
	return r.AppendApplicabilityEvent(ctx, a.ID, a.History[0])
}

// AppendApplicabilityVersion writes a terms version.
func (r *CTCRepo) AppendApplicabilityVersion(ctx context.Context, record id.CTCApplicabilityID, v ctc.Version) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if v.Digest.IsZero() {
		return ctcFault(nil, "A terms version is stored with its digest.")
	}
	t := v.Terms
	_, err = r.s.db(ctx).Exec(ctx, sqlCTCVersionInsert, tenant.UUID(), record.UUID(), v.Number, string(t.Duty), string(t.Model),
		string(t.Profile), optString(t.FormatVersion), t.Authority, t.LegalSource, ctc.Day(t.SourceAsOf), ctc.Day(t.EffectiveFrom),
		optDay(t.EffectiveTo), optString(t.Conditions), v.Digest.String(), v.RecordedAt.UTC(), v.RecordedBy.UUID())
	return mapError(err, "insert ctc applicability version")
}

// AppendApplicabilityEvent writes one record event.
func (r *CTCRepo) AppendApplicabilityEvent(ctx context.Context, record id.CTCApplicabilityID, e ctc.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	var (
		version          *int
		sha, name, media *string
		size             *int64
	)
	if e.Version > 0 {
		v := e.Version
		version = &v
	}
	if e.Evidence != nil {
		sha, name, media = &e.Evidence.SHA256, &e.Evidence.Filename, &e.Evidence.MediaType
		b := e.Evidence.Bytes
		size = &b
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlCTCAppEventInsert, tenant.UUID(), record.UUID(), e.Seq, string(e.Kind), string(e.Status),
		version, optString(string(e.Gate)), optString(e.SignoffRef), optString(e.Reason), sha, name, media, size,
		e.RecordedAt.UTC(), e.RecordedBy.UUID())
	return mapError(err, "insert ctc applicability event")
}

// Applicability returns one record.
func (r *CTCRepo) Applicability(ctx context.Context, record id.CTCApplicabilityID) (ctc.Applicability, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return ctc.Applicability{}, err
	}
	got, err := r.applicabilities(ctx, sqlCTCAppByID, tenant.UUID(), record.UUID())
	if err != nil {
		return ctc.Applicability{}, err
	}
	if len(got) == 0 {
		return ctc.Applicability{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such applicability record.")
	}
	return got[0], nil
}

// Applicabilities returns the tenant's records matching the filter.
func (r *CTCRepo) Applicabilities(ctx context.Context, f ctc.Filter) ([]ctc.Applicability, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	got, err := r.applicabilities(ctx, sqlCTCAppList, tenant.UUID(), nilUUID(f.LegalEntity.UUID(), f.LegalEntity.IsZero()),
		f.Country, ctcListCap)
	if err != nil {
		return nil, err
	}
	out := got[:0]
	for _, a := range got {
		if f.Match(a) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (r *CTCRepo) applicabilities(ctx context.Context, query string, args ...any) ([]ctc.Applicability, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, mapError(err, "read ctc applicability")
	}
	var out []ctc.Applicability
	for rows.Next() {
		var (
			a               = ctc.Applicability{TenantID: tenant}
			aid, entity, by uuid.UUID
			docType, buyer  string
		)
		if err := rows.Scan(&aid, &entity, &a.Country, &docType, &buyer, &a.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan ctc applicability")
		}
		a.ID, a.LegalEntity, a.CreatedBy = id.NewCTCApplicabilityID(aid), id.NewLegalEntityID(entity), id.NewUserID(by)
		a.DocumentType, a.BuyerKind, a.CreatedAt = document.Type(docType), ctc.BuyerKind(buyer), a.CreatedAt.UTC()
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read ctc applicability")
	}
	if len(out) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(out))
	for i, a := range out {
		ids[i] = a.ID.UUID()
	}

	versions := map[uuid.UUID][]ctc.Version{}
	rows, err = db.Query(ctx, sqlCTCVersions, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read ctc applicability versions")
	}
	for rows.Next() {
		var (
			v                    ctc.Version
			aid, by              uuid.UUID
			duty, model, profile string
			format, conditions   *string
			effectiveTo          *time.Time
			digest               string
		)
		if err := rows.Scan(&aid, &v.Number, &duty, &model, &profile, &format, &v.Terms.Authority, &v.Terms.LegalSource,
			&v.Terms.SourceAsOf, &v.Terms.EffectiveFrom, &effectiveTo, &conditions, &digest, &v.RecordedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan ctc applicability version")
		}
		if v.Digest, err = canonical.ParseDigest(digest); err != nil {
			rows.Close()
			return nil, ctcFault(err, "A stored terms version's digest does not parse.")
		}
		v.Terms.Duty, v.Terms.Model, v.Terms.Profile = ctc.Duty(duty), ctc.Model(model), ctc.Profile(profile)
		v.Terms.FormatVersion, v.Terms.Conditions = deref(format), deref(conditions)
		v.Terms.SourceAsOf, v.Terms.EffectiveFrom = dayOf(v.Terms.SourceAsOf), dayOf(v.Terms.EffectiveFrom)
		if effectiveTo != nil {
			v.Terms.EffectiveTo = dayOf(*effectiveTo)
		}
		v.RecordedAt, v.RecordedBy = v.RecordedAt.UTC(), id.NewUserID(by)
		versions[aid] = append(versions[aid], v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read ctc applicability versions")
	}

	rows, err = db.Query(ctx, sqlCTCAppEvents, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read ctc applicability events")
	}
	defer rows.Close()
	events := map[uuid.UUID][]ctc.Event{}
	for rows.Next() {
		var (
			e                                       ctc.Event
			aid, by                                 uuid.UUID
			kind, status                            string
			version                                 *int
			gate, signoff, reason, sha, name, media *string
			size                                    *int64
		)
		if err := rows.Scan(&aid, &e.Seq, &kind, &status, &version, &gate, &signoff, &reason, &sha, &name, &media, &size,
			&e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan ctc applicability event")
		}
		e.Kind, e.Status, e.RecordedAt, e.RecordedBy = ctc.EventKind(kind), ctc.Status(status), e.RecordedAt.UTC(), id.NewUserID(by)
		if version != nil {
			e.Version = *version
		}
		e.Gate, e.SignoffRef, e.Reason = ctc.Gate(deref(gate)), deref(signoff), deref(reason)
		if sha != nil {
			e.Evidence = &ctc.Attachment{SHA256: *sha, Filename: deref(name), MediaType: deref(media)}
			if size != nil {
				e.Evidence.Bytes = *size
			}
		}
		events[aid] = append(events[aid], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read ctc applicability events")
	}
	for i := range out {
		folded, err := ctc.Fold(out[i], versions[out[i].ID.UUID()], events[out[i].ID.UUID()])
		if err != nil {
			return nil, ctcFault(err, "A stored applicability record's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}

// RecordValidation writes a sealed report and its results.
func (r *CTCRepo) RecordValidation(ctx context.Context, v ctc.Validation) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if v.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The report belongs to a tenant outside the caller's scope.")
	}
	if err := v.Verify(); err != nil {
		return ctcFault(err, "A report is stored with the digest of its own contents.")
	}
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlCTCValInsert, tenant.UUID(), v.ID.UUID(), v.Document.UUID(), v.Applicability.UUID(),
		v.ApplicabilityVersion, string(v.Profile), optString(v.FormatVersion), v.Ruleset, v.InputDigest.String(),
		string(v.Outcome), v.Digest.String(), v.ValidatedAt.UTC(), v.ValidatedBy.UUID()); err != nil {
		return mapError(err, "insert ctc validation")
	}
	for i, res := range v.Results {
		if _, err := db.Exec(ctx, sqlCTCResultInsert, tenant.UUID(), v.ID.UUID(), i+1, res.RuleID, string(res.Stage),
			string(res.Severity), string(res.Outcome), optString(string(res.Reason)), optString(res.BusinessTerm),
			optString(res.Detail)); err != nil {
			return mapError(err, "insert ctc validation result")
		}
	}
	return nil
}

// Validation returns one report.
func (r *CTCRepo) Validation(ctx context.Context, validation id.CTCValidationID) (ctc.Validation, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return ctc.Validation{}, err
	}
	got, err := r.validations(ctx, sqlCTCValByID, tenant.UUID(), validation.UUID())
	if err != nil {
		return ctc.Validation{}, err
	}
	if len(got) == 0 {
		return ctc.Validation{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such validation report.")
	}
	return got[0], nil
}

// Validations returns the reports matching the filter, newest first.
func (r *CTCRepo) Validations(ctx context.Context, f ctc.ValidationFilter) ([]ctc.Validation, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return r.validations(ctx, sqlCTCValList, tenant.UUID(), nilUUID(f.Document.UUID(), f.Document.IsZero()),
		nilUUID(f.Applicability.UUID(), f.Applicability.IsZero()), ctcListCap)
}

func (r *CTCRepo) validations(ctx context.Context, query string, args ...any) ([]ctc.Validation, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, mapError(err, "read ctc validations")
	}
	var out []ctc.Validation
	for rows.Next() {
		var (
			v                               = ctc.Validation{TenantID: tenant}
			vid, doc, app, by               uuid.UUID
			profile, outcome, input, report string
			format                          *string
		)
		if err := rows.Scan(&vid, &doc, &app, &v.ApplicabilityVersion, &profile, &format, &v.Ruleset, &input, &outcome,
			&report, &v.ValidatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan ctc validation")
		}
		if v.InputDigest, err = canonical.ParseDigest(input); err == nil {
			v.Digest, err = canonical.ParseDigest(report)
		}
		if err != nil {
			rows.Close()
			return nil, ctcFault(err, "A stored validation report's digest does not parse.")
		}
		v.ID, v.Document, v.Applicability = id.NewCTCValidationID(vid), id.NewFiscalDocumentID(doc), id.NewCTCApplicabilityID(app)
		v.Profile, v.FormatVersion, v.Outcome = ctc.Profile(profile), deref(format), ctc.Outcome(outcome)
		v.ValidatedAt, v.ValidatedBy = v.ValidatedAt.UTC(), id.NewUserID(by)
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read ctc validations")
	}
	if len(out) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(out))
	index := map[uuid.UUID]int{}
	for i, v := range out {
		ids[i], index[v.ID.UUID()] = v.ID.UUID(), i
	}
	rows, err = db.Query(ctx, sqlCTCResults, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read ctc validation results")
	}
	defer rows.Close()
	for rows.Next() {
		var (
			res                      ctc.RuleResult
			vid                      uuid.UUID
			stage, severity, outcome string
			reason, term, detail     *string
		)
		if err := rows.Scan(&vid, &res.RuleID, &stage, &severity, &outcome, &reason, &term, &detail); err != nil {
			return nil, mapError(err, "scan ctc validation result")
		}
		res.Stage, res.Severity, res.Outcome = ctc.Stage(stage), ctc.Severity(severity), ctc.RuleOutcome(outcome)
		res.Reason, res.BusinessTerm, res.Detail = errs.ReasonCode(deref(reason)), deref(term), deref(detail)
		i := index[vid]
		out[i].Results = append(out[i].Results, res)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read ctc validation results")
	}
	for _, v := range out {
		if err := v.Verify(); err != nil {
			return nil, ctcFault(err, "A stored validation report no longer matches its digest.")
		}
	}
	return out, nil
}

// CreateSubmission writes a sealed submission and its PREPARED event.
func (r *CTCRepo) CreateSubmission(ctx context.Context, s ctc.Submission) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if s.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The submission belongs to a tenant outside the caller's scope.")
	}
	if len(s.History) != 1 || s.History[0].Kind != ctc.SubEventPrepared || s.PayloadDigest.IsZero() {
		return ctcFault(nil, "A submission is created sealed, by its PREPARED event.")
	}
	gross, err := MoneyValue(s.Gross)
	if err != nil {
		return err
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlCTCSubInsert, tenant.UUID(), s.ID.UUID(), s.Document.UUID(), optString(s.DocumentNumber),
		string(s.DocumentType), s.LegalEntity.UUID(), s.Country, string(s.BuyerKind), s.Applicability.UUID(),
		s.ApplicabilityVersion, string(s.Model), string(s.Profile), optString(s.FormatVersion), s.Validation.UUID(),
		string(s.Currency), gross, s.PayloadDigest.String(), s.PreparedAt.UTC(), s.PreparedBy.UUID()); err != nil {
		return mapError(err, "insert ctc submission")
	}
	return r.AppendSubmissionEvent(ctx, s.ID, s.History[0])
}

// AppendSubmissionEvent writes one submission event.
func (r *CTCRepo) AppendSubmissionEvent(ctx context.Context, submission id.CTCSubmissionID, e ctc.SubmissionEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlCTCSubEventInsert, tenant.UUID(), submission.UUID(), e.Seq, string(e.Kind), string(e.Status),
		nilUUID(e.Attempt.UUID(), e.Attempt.IsZero()), optString(e.TransportRef), optString(e.AuthorityRef),
		optString(e.AuthorityCode), optString(e.EvidenceSHA256), optString(e.Detail), e.RecordedAt.UTC(), e.RecordedBy.UUID())
	return mapError(err, "insert ctc submission event")
}

// Submission returns one submission.
func (r *CTCRepo) Submission(ctx context.Context, submission id.CTCSubmissionID) (ctc.Submission, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return ctc.Submission{}, err
	}
	got, err := r.submissions(ctx, sqlCTCSubByID, tenant.UUID(), submission.UUID())
	if err != nil {
		return ctc.Submission{}, err
	}
	if len(got) == 0 {
		return ctc.Submission{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such submission.")
	}
	return got[0], nil
}

// Submissions returns the submissions matching the filter, newest first.
func (r *CTCRepo) Submissions(ctx context.Context, f ctc.SubmissionFilter) ([]ctc.Submission, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return r.submissions(ctx, sqlCTCSubList, tenant.UUID(), nilUUID(f.Document.UUID(), f.Document.IsZero()),
		nilUUID(f.Applicability.UUID(), f.Applicability.IsZero()), string(f.Status), ctcListCap)
}

func (r *CTCRepo) submissions(ctx context.Context, query string, args ...any) ([]ctc.Submission, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, mapError(err, "read ctc submissions")
	}
	var out []ctc.Submission
	for rows.Next() {
		var (
			s                                                = ctc.Submission{TenantID: tenant}
			sid, doc, entity, app, val, by                   uuid.UUID
			number, format                                   *string
			docType, buyer, model, profile, currency, digest string
			gross                                            Numeric
		)
		if err := rows.Scan(&sid, &doc, &number, &docType, &entity, &s.Country, &buyer, &app, &s.ApplicabilityVersion, &model,
			&profile, &format, &val, &currency, &gross, &digest, &s.PreparedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan ctc submission")
		}
		s.Currency = fiscal.Currency(currency)
		if s.Gross, err = gross.Money(s.Currency); err == nil {
			s.PayloadDigest, err = canonical.ParseDigest(digest)
		}
		if err != nil {
			rows.Close()
			return nil, ctcFault(err, "A stored submission does not read back.")
		}
		s.ID, s.Document, s.LegalEntity = id.NewCTCSubmissionID(sid), id.NewFiscalDocumentID(doc), id.NewLegalEntityID(entity)
		s.Applicability, s.Validation, s.PreparedBy = id.NewCTCApplicabilityID(app), id.NewCTCValidationID(val), id.NewUserID(by)
		s.DocumentNumber, s.DocumentType, s.BuyerKind = deref(number), document.Type(docType), ctc.BuyerKind(buyer)
		s.Model, s.Profile, s.FormatVersion, s.PreparedAt = ctc.Model(model), ctc.Profile(profile), deref(format), s.PreparedAt.UTC()
		out = append(out, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read ctc submissions")
	}
	if len(out) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(out))
	for i, s := range out {
		ids[i] = s.ID.UUID()
	}
	rows, err = db.Query(ctx, sqlCTCSubEvents, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read ctc submission events")
	}
	defer rows.Close()
	events := map[uuid.UUID][]ctc.SubmissionEvent{}
	for rows.Next() {
		var (
			e                                       ctc.SubmissionEvent
			sid, by                                 uuid.UUID
			attempt                                 *uuid.UUID
			kind, status                            string
			transport, authority, code, sha, detail *string
		)
		if err := rows.Scan(&sid, &e.Seq, &kind, &status, &attempt, &transport, &authority, &code, &sha, &detail,
			&e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan ctc submission event")
		}
		e.Kind, e.Status = ctc.SubmissionEventKind(kind), ctc.SubmissionStatus(status)
		e.RecordedAt, e.RecordedBy = e.RecordedAt.UTC(), id.NewUserID(by)
		if attempt != nil {
			e.Attempt = id.NewCTCAttemptID(*attempt)
		}
		e.TransportRef, e.AuthorityRef, e.AuthorityCode = deref(transport), deref(authority), deref(code)
		e.EvidenceSHA256, e.Detail = deref(sha), deref(detail)
		events[sid] = append(events[sid], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read ctc submission events")
	}
	for i := range out {
		folded, err := ctc.FoldSubmission(out[i], events[out[i].ID.UUID()])
		if err != nil {
			return nil, ctcFault(err, "A stored submission's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}
