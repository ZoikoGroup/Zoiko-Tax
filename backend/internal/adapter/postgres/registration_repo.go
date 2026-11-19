package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// entity_registration(_event), exemption_certificate(_event) — migration 000033
// ---------------------------------------------------------------------------

const (
	sqlRegistrationColumns = `registration_id, legal_entity_id, scheme, jurisdiction, authority, registration_number,
		effective_from, effective_to, supersedes, intake_key, intake_hash, created_at, created_by`
	sqlRegistrationInsert = `INSERT INTO ztax.entity_registration (tenant_id, ` + sqlRegistrationColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`
	sqlRegistrationSelect = `SELECT ` + sqlRegistrationColumns + ` FROM ztax.entity_registration WHERE tenant_id = $1`

	sqlRegistrationEventColumns = `registration_id, seq, kind, status_after, reason, check_source, check_method,
		check_outcome, check_reference, checked_at, receipt_sha256, attachment_sha256, attachment_name, attachment_media,
		attachment_bytes, recorded_at, recorded_by`
	sqlRegistrationEventInsert = `INSERT INTO ztax.entity_registration_event (tenant_id, ` + sqlRegistrationEventColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`
	sqlRegistrationEvents = `SELECT ` + sqlRegistrationEventColumns + ` FROM ztax.entity_registration_event
		WHERE tenant_id = $1 AND registration_id = ANY($2) ORDER BY registration_id, seq`

	sqlCertificateColumns = `certificate_id, legal_entity_id, buyer_ref, buyer_name, certificate_type, exemption_reason,
		jurisdiction, certificate_number, product_scope, issued_on, expires_on, supersedes, intake_key, intake_hash,
		created_at, created_by`
	sqlCertificateInsert = `INSERT INTO ztax.exemption_certificate (tenant_id, ` + sqlCertificateColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`
	sqlCertificateSelect = `SELECT ` + sqlCertificateColumns + ` FROM ztax.exemption_certificate WHERE tenant_id = $1`

	sqlCertificateEventColumns = `certificate_id, seq, kind, status_after, evidence_version, note, document_sha256,
		document_name, document_media, document_bytes, screening, screening_result, recommendation, effective_from,
		effective_to, decision, recorded_at, recorded_by`
	sqlCertificateEventInsert = `INSERT INTO ztax.exemption_certificate_event (tenant_id, ` + sqlCertificateEventColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`
	sqlCertificateEvents = `SELECT ` + sqlCertificateEventColumns + ` FROM ztax.exemption_certificate_event
		WHERE tenant_id = $1 AND certificate_id = ANY($2) ORDER BY certificate_id, seq`

	sqlRegistrationLock = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	registrationListCap = 500
)

// The predicates a read takes. Fixed strings from this file, never caller
// text.
const (
	whereRegistrationID     = ` AND registration_id = $2`
	whereRegistrationIntake = ` AND intake_key = $2`
	whereRegistrationNumber = ` AND scheme = $2 AND registration_number = $3`
	whereRegistrationRecent = ` ORDER BY created_at DESC, registration_id DESC LIMIT $2`
	whereCertificateID      = ` AND certificate_id = $2`
	whereCertificateIntake  = ` AND intake_key = $2`
	whereCertificateDoc     = ` AND certificate_id IN (SELECT certificate_id FROM ztax.exemption_certificate_event
		WHERE tenant_id = $1 AND document_sha256 = $2)`
	whereCertificateRecent = ` ORDER BY created_at DESC, certificate_id DESC LIMIT $2`
)

// RegistrationRepo implements port.RegistrationRepository.
type RegistrationRepo struct{ s *Store }

// Registrations returns the registration and exemption evidence repository.
func (s *Store) Registrations() *RegistrationRepo { return &RegistrationRepo{s: s} }

var _ port.RegistrationRepository = (*RegistrationRepo)(nil)

// screeningRow is the stored shape of a screening checklist.
type screeningRow struct {
	SellerMatches       string `json:"sellerMatches"`
	BuyerMatches        string `json:"buyerMatches"`
	JurisdictionMatches string `json:"jurisdictionMatches"`
	FormAccepted        string `json:"formAccepted"`
	SignaturePresent    string `json:"signaturePresent"`
	DatesValid          string `json:"datesValid"`
	NumberPresent       string `json:"numberPresent"`
}

// Lock takes one of the tenant's registration locks for the transaction.
func (r *RegistrationRepo) Lock(ctx context.Context, key string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlRegistrationLock, tenant.String()+"/registration/"+key)
	return mapError(err, "lock registration")
}

func regUUID(u uuid.UUID, zero bool) *uuid.UUID {
	if zero {
		return nil
	}
	return &u
}

// CreateRegistration writes a registration and its first events.
func (r *RegistrationRepo) CreateRegistration(ctx context.Context, reg registration.Registration, events []registration.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if reg.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The registration belongs to a tenant outside the caller's scope.")
	}
	if err := reg.Validate(); err != nil {
		return obligationFault(err, "The registration is not well-formed.")
	}
	if reg.IntakeHash.IsZero() || len(events) == 0 || events[0].Kind != registration.EventCreated || events[0].Seq != 1 {
		return obligationFault(nil, "A registration is created by its CREATED event, with its intake hash.")
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlRegistrationInsert, tenant.UUID(), reg.ID.UUID(), reg.LegalEntity.UUID(),
		string(reg.Scheme), reg.Jurisdiction, reg.Authority, reg.Number, civilDate(reg.EffectiveFrom), optDate(reg.EffectiveTo),
		regUUID(reg.Supersedes.UUID(), reg.Supersedes.IsZero()), reg.IntakeKey, reg.IntakeHash.String(), reg.CreatedAt.UTC(),
		reg.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert registration")
	}
	return r.AppendRegistrationEvents(ctx, reg.ID, events)
}

// AppendRegistrationEvents writes events in order.
func (r *RegistrationRepo) AppendRegistrationEvents(ctx context.Context, registrationID id.RegistrationID, events []registration.Event) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	for _, e := range events {
		var (
			source, method, outcome, ref, receipt *string
			checkedAt                             *time.Time
			sha, name, media                      *string
			bytes                                 *int64
		)
		if c := e.Check; c != nil {
			s, m, o, at := string(c.Source), string(c.Method), string(c.Outcome), c.CheckedAt.UTC()
			source, method, outcome, checkedAt = &s, &m, &o, &at
			ref, receipt = optString(c.Reference), optString(c.ReceiptSHA256)
		}
		if a := e.Attachment; a != nil {
			s, n, m, b := a.SHA256, a.Filename, a.MediaType, a.Bytes
			sha, name, media, bytes = &s, &n, &m, &b
		}
		if _, err := r.s.db(ctx).Exec(ctx, sqlRegistrationEventInsert, tenant.UUID(), registrationID.UUID(), e.Seq, string(e.Kind),
			string(e.Status), optString(e.Reason), source, method, outcome, ref, checkedAt, receipt, sha, name, media, bytes,
			e.RecordedAt.UTC(), e.RecordedBy.UUID()); err != nil {
			return mapError(err, "insert registration event")
		}
	}
	return nil
}

// Registration returns one registration.
func (r *RegistrationRepo) Registration(ctx context.Context, registrationID id.RegistrationID) (registration.Registration, error) {
	got, err := r.readRegistrations(ctx, whereRegistrationID, registrationID.UUID())
	if err != nil {
		return registration.Registration{}, err
	}
	if len(got) == 0 {
		return registration.Registration{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such registration.")
	}
	return got[0], nil
}

// RegistrationByIntakeKey returns the registration recorded under a key.
func (r *RegistrationRepo) RegistrationByIntakeKey(ctx context.Context, key string) (registration.Registration, bool, error) {
	got, err := r.readRegistrations(ctx, whereRegistrationIntake, key)
	if err != nil || len(got) == 0 {
		return registration.Registration{}, false, err
	}
	return got[0], true, nil
}

// RegistrationsByNumber returns the registrations of a scheme and number.
func (r *RegistrationRepo) RegistrationsByNumber(ctx context.Context, scheme registration.Scheme, number string) ([]registration.Registration, error) {
	return r.readRegistrations(ctx, whereRegistrationNumber, string(scheme), number)
}

// Registrations returns the most recent registrations.
func (r *RegistrationRepo) Registrations(ctx context.Context, limit int) ([]registration.Registration, error) {
	if limit <= 0 || limit > registrationListCap {
		limit = registrationListCap
	}
	return r.readRegistrations(ctx, whereRegistrationRecent, limit)
}

func (r *RegistrationRepo) readRegistrations(ctx context.Context, where string, args ...any) ([]registration.Registration, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, sqlRegistrationSelect+where, append([]any{tenant.UUID()}, args...)...)
	if err != nil {
		return nil, mapError(err, "read registrations")
	}
	var out []registration.Registration
	for rows.Next() {
		var (
			reg          = registration.Registration{TenantID: tenant}
			rid, le, by  uuid.UUID
			supersedes   *uuid.UUID
			scheme, hash string
			from, to     pgtype.Date
		)
		if err := rows.Scan(&rid, &le, &scheme, &reg.Jurisdiction, &reg.Authority, &reg.Number, &from, &to, &supersedes,
			&reg.IntakeKey, &hash, &reg.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan registration")
		}
		reg.ID, reg.LegalEntity, reg.CreatedBy = id.NewRegistrationID(rid), id.NewLegalEntityID(le), id.NewUserID(by)
		reg.Scheme, reg.CreatedAt, reg.EffectiveFrom = registration.Scheme(scheme), reg.CreatedAt.UTC(), inLocation(from, time.UTC)
		if to.Valid {
			reg.EffectiveTo = inLocation(to, time.UTC)
		}
		if supersedes != nil {
			reg.Supersedes = id.NewRegistrationID(*supersedes)
		}
		if reg.IntakeHash, err = canonical.ParseDigest(hash); err != nil {
			rows.Close()
			return nil, obligationFault(err, "A stored registration's intake hash does not parse.")
		}
		out = append(out, reg)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read registrations")
	}
	if len(out) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, len(out))
	for i, reg := range out {
		ids[i] = reg.ID.UUID()
	}
	rows, err = db.Query(ctx, sqlRegistrationEvents, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read registration events")
	}
	defer rows.Close()
	events := map[id.RegistrationID][]registration.Event{}
	for rows.Next() {
		var (
			e                                    registration.Event
			rid, by                              uuid.UUID
			kind, status                         string
			reason, source, method, outcome, ref *string
			receipt, sha, name, media            *string
			checkedAt                            *time.Time
			bytes                                *int64
		)
		if err := rows.Scan(&rid, &e.Seq, &kind, &status, &reason, &source, &method, &outcome, &ref, &checkedAt, &receipt,
			&sha, &name, &media, &bytes, &e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan registration event")
		}
		e.Kind, e.Status, e.Reason = registration.EventKind(kind), registration.Status(status), deref(reason)
		e.RecordedAt, e.RecordedBy = e.RecordedAt.UTC(), id.NewUserID(by)
		if outcome != nil {
			c := registration.Check{Source: registration.CheckSource(deref(source)), Method: registration.CheckMethod(deref(method)),
				Outcome: registration.CheckOutcome(*outcome), Reference: deref(ref), ReceiptSHA256: deref(receipt)}
			if checkedAt != nil {
				c.CheckedAt = checkedAt.UTC()
			}
			e.Check = &c
		}
		if sha != nil {
			a := registration.Attachment{SHA256: *sha, Filename: deref(name), MediaType: deref(media)}
			if bytes != nil {
				a.Bytes = *bytes
			}
			e.Attachment = &a
		}
		k := id.NewRegistrationID(rid)
		events[k] = append(events[k], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read registration events")
	}
	for i := range out {
		folded, err := registration.Fold(out[i], events[out[i].ID])
		if err != nil {
			return nil, obligationFault(err, "A stored registration's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}

// CreateCertificate writes a certificate and its RECEIVED event.
func (r *RegistrationRepo) CreateCertificate(ctx context.Context, c registration.Certificate, received registration.CertificateEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if c.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch, "The certificate belongs to a tenant outside the caller's scope.")
	}
	if err := c.Validate(); err != nil {
		return obligationFault(err, "The certificate is not well-formed.")
	}
	if c.IntakeHash.IsZero() || received.Kind != registration.CertEventReceived || received.Seq != 1 || received.Document == nil {
		return obligationFault(nil, "A certificate is created by its RECEIVED event and document, with its intake hash.")
	}
	scope := c.ProductScope
	if scope == nil {
		scope = []string{}
	}
	if _, err := r.s.db(ctx).Exec(ctx, sqlCertificateInsert, tenant.UUID(), c.ID.UUID(), c.LegalEntity.UUID(), c.BuyerRef,
		c.BuyerName, string(c.Type), c.ExemptionReason, c.Jurisdiction, optString(c.CertificateNumber), scope,
		civilDate(c.IssuedOn), optDate(c.ExpiresOn), regUUID(c.Supersedes.UUID(), c.Supersedes.IsZero()), c.IntakeKey,
		c.IntakeHash.String(), c.CreatedAt.UTC(), c.CreatedBy.UUID()); err != nil {
		return mapError(err, "insert exemption certificate")
	}
	return r.AppendCertificateEvent(ctx, c.ID, received)
}

// AppendCertificateEvent writes one certificate event.
func (r *RegistrationRepo) AppendCertificateEvent(ctx context.Context, certificateID id.ExemptionCertificateID, e registration.CertificateEvent) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	var (
		sha, name, media, result, recommendation, decision *string
		bytes                                              *int64
		screening                                          []byte
		from, to                                           pgtype.Date
	)
	if d := e.Document; d != nil {
		s, n, m, b := d.SHA256, d.Filename, d.MediaType, d.Bytes
		sha, name, media, bytes = &s, &n, &m, &b
	}
	if s := e.Screening; s != nil {
		row := screeningRow{
			SellerMatches: string(s.SellerMatches), BuyerMatches: string(s.BuyerMatches),
			JurisdictionMatches: string(s.JurisdictionMatches), FormAccepted: string(s.FormAccepted),
			SignaturePresent: string(s.SignaturePresent), DatesValid: string(s.DatesValid), NumberPresent: string(s.NumberPresent),
		}
		if screening, err = json.Marshal(row); err != nil {
			return obligationFault(err, "The screening could not be encoded.")
		}
		res := string(s.Result)
		result = &res
	}
	if rec := e.Recommendation; rec != nil {
		v := "REJECT"
		if rec.Approve {
			v = "APPROVE"
		}
		recommendation = &v
		from, to = optDate(rec.EffectiveFrom), optDate(rec.EffectiveTo)
	}
	if e.Decision != "" {
		d := string(e.Decision)
		decision = &d
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlCertificateEventInsert, tenant.UUID(), certificateID.UUID(), e.Seq, string(e.Kind),
		string(e.Status), e.EvidenceVersion, optString(e.Note), sha, name, media, bytes, screening, result, recommendation,
		from, to, decision, e.RecordedAt.UTC(), e.RecordedBy.UUID())
	return mapError(err, "insert exemption certificate event")
}

// Certificate returns one certificate.
func (r *RegistrationRepo) Certificate(ctx context.Context, certificateID id.ExemptionCertificateID) (registration.Certificate, error) {
	got, err := r.readCertificates(ctx, whereCertificateID, certificateID.UUID())
	if err != nil {
		return registration.Certificate{}, err
	}
	if len(got) == 0 {
		return registration.Certificate{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such exemption certificate.")
	}
	return got[0], nil
}

// CertificateByIntakeKey returns the certificate received under a key.
func (r *RegistrationRepo) CertificateByIntakeKey(ctx context.Context, key string) (registration.Certificate, bool, error) {
	got, err := r.readCertificates(ctx, whereCertificateIntake, key)
	if err != nil || len(got) == 0 {
		return registration.Certificate{}, false, err
	}
	return got[0], true, nil
}

// CertificatesWithDocument returns the certificates holding a document.
func (r *RegistrationRepo) CertificatesWithDocument(ctx context.Context, sha256 string) ([]registration.Certificate, error) {
	return r.readCertificates(ctx, whereCertificateDoc, sha256)
}

// Certificates returns the most recent certificates.
func (r *RegistrationRepo) Certificates(ctx context.Context, limit int) ([]registration.Certificate, error) {
	if limit <= 0 || limit > registrationListCap {
		limit = registrationListCap
	}
	return r.readCertificates(ctx, whereCertificateRecent, limit)
}

func (r *RegistrationRepo) readCertificates(ctx context.Context, where string, args ...any) ([]registration.Certificate, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	db := r.s.db(ctx)
	rows, err := db.Query(ctx, sqlCertificateSelect+where, append([]any{tenant.UUID()}, args...)...)
	if err != nil {
		return nil, mapError(err, "read exemption certificates")
	}
	var out []registration.Certificate
	for rows.Next() {
		var (
			c              = registration.Certificate{TenantID: tenant}
			cid, le, by    uuid.UUID
			supersedes     *uuid.UUID
			kind, hash     string
			number         *string
			issued, expiry pgtype.Date
		)
		if err := rows.Scan(&cid, &le, &c.BuyerRef, &c.BuyerName, &kind, &c.ExemptionReason, &c.Jurisdiction, &number,
			&c.ProductScope, &issued, &expiry, &supersedes, &c.IntakeKey, &hash, &c.CreatedAt, &by); err != nil {
			rows.Close()
			return nil, mapError(err, "scan exemption certificate")
		}
		c.ID, c.LegalEntity, c.CreatedBy = id.NewExemptionCertificateID(cid), id.NewLegalEntityID(le), id.NewUserID(by)
		c.Type, c.CertificateNumber, c.CreatedAt = registration.CertificateType(kind), deref(number), c.CreatedAt.UTC()
		c.IssuedOn = inLocation(issued, time.UTC)
		if expiry.Valid {
			c.ExpiresOn = inLocation(expiry, time.UTC)
		}
		if supersedes != nil {
			c.Supersedes = id.NewExemptionCertificateID(*supersedes)
		}
		if c.IntakeHash, err = canonical.ParseDigest(hash); err != nil {
			rows.Close()
			return nil, obligationFault(err, "A stored certificate's intake hash does not parse.")
		}
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read exemption certificates")
	}
	if len(out) == 0 {
		return nil, nil
	}

	ids := make([]uuid.UUID, len(out))
	for i, c := range out {
		ids[i] = c.ID.UUID()
	}
	rows, err = db.Query(ctx, sqlCertificateEvents, tenant.UUID(), ids)
	if err != nil {
		return nil, mapError(err, "read exemption certificate events")
	}
	defer rows.Close()
	events := map[id.ExemptionCertificateID][]registration.CertificateEvent{}
	for rows.Next() {
		var (
			e                                       registration.CertificateEvent
			cid, by                                 uuid.UUID
			kind, status                            string
			note, sha, name, media, result, rec, dc *string
			bytes                                   *int64
			screening                               []byte
			from, to                                pgtype.Date
		)
		if err := rows.Scan(&cid, &e.Seq, &kind, &status, &e.EvidenceVersion, &note, &sha, &name, &media, &bytes, &screening,
			&result, &rec, &from, &to, &dc, &e.RecordedAt, &by); err != nil {
			return nil, mapError(err, "scan exemption certificate event")
		}
		e.Kind, e.Status, e.Note = registration.CertificateEventKind(kind), registration.CertificateStatus(status), deref(note)
		e.Decision, e.RecordedAt, e.RecordedBy = registration.Decision(deref(dc)), e.RecordedAt.UTC(), id.NewUserID(by)
		if sha != nil {
			d := registration.Attachment{SHA256: *sha, Filename: deref(name), MediaType: deref(media)}
			if bytes != nil {
				d.Bytes = *bytes
			}
			e.Document = &d
		}
		if screening != nil {
			var row screeningRow
			if err := json.Unmarshal(screening, &row); err != nil {
				return nil, obligationFault(err, "A stored screening does not decode.")
			}
			e.Screening = &registration.Screening{
				SellerMatches: registration.CheckResult(row.SellerMatches), BuyerMatches: registration.CheckResult(row.BuyerMatches),
				JurisdictionMatches: registration.CheckResult(row.JurisdictionMatches), FormAccepted: registration.CheckResult(row.FormAccepted),
				SignaturePresent: registration.CheckResult(row.SignaturePresent), DatesValid: registration.CheckResult(row.DatesValid),
				NumberPresent: registration.CheckResult(row.NumberPresent), Result: registration.ScreeningResult(deref(result)),
			}
		}
		if rec != nil {
			r := registration.Recommendation{Approve: *rec == "APPROVE"}
			if from.Valid {
				r.EffectiveFrom = inLocation(from, time.UTC)
			}
			if to.Valid {
				r.EffectiveTo = inLocation(to, time.UTC)
			}
			e.Recommendation = &r
		}
		k := id.NewExemptionCertificateID(cid)
		events[k] = append(events[k], e)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "read exemption certificate events")
	}
	for i := range out {
		folded, err := registration.FoldCertificate(out[i], events[out[i].ID])
		if err != nil {
			return nil, obligationFault(err, "A stored certificate's history does not fold.")
		}
		out[i] = folded
	}
	return out, nil
}
