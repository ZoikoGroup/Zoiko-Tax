package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Registrations and exemption evidence (Engineering Doc 17, G-048). The
// rules are internal/domain/registration's; this is where a record meets the
// people acting, the tenant's legal entities and the store.
//
// The service holds a registration repository and the legal entities and
// nothing else: no decision, rule, obligation or fiscal repository. So no
// act here — an approval, an activation, an expiry — can change a tax
// result, already committed or still to come (Doc 17 §01; NAV-008).
//
// Roles. OPERATOR and ANALYST record registrations and receive certificates
// (Entity Admin, Tax Admin). ANALYST is the specialist: it records
// authority checks, screens, recommends, confirms, activates and suspends.
// AUDITOR reads. ADMIN reads none of it: Doc 17 §04 gives a group-wide
// administrator no automatic access to a tenant's customer evidence.
//
// Every write is a person's act, so the four-eye rules are comparisons of
// people: a submitter does not verify or activate their own registration,
// a recommender does not confirm their own recommendation, and an uploader
// does not approve their own document.
//
// Intake is idempotent on the Idempotency-Key: the same key and request
// returns the record already made; the same key with a different request
// is IDEMPOTENCY_KEY_REUSE (Doc 17 ER-027, XC-07).

// RegistrationService runs the registration ledger and the certificate
// evidence register.
type RegistrationService struct {
	store    port.RegistrationRepository
	entities port.LegalEntityRepository
	tx       port.TxManager
	clock    clock.Clock
	ids      idgen.Generator
}

// NewRegistrationService wires the service.
func NewRegistrationService(store port.RegistrationRepository, entities port.LegalEntityRepository, tx port.TxManager,
	clk clock.Clock, ids idgen.Generator) *RegistrationService {
	return &RegistrationService{store: store, entities: entities, tx: tx, clock: clk, ids: ids}
}

var (
	registrationReaders     = []security.Role{security.RoleOperator, security.RoleAnalyst, security.RoleAuditor}
	registrationRecorders   = []security.Role{security.RoleOperator, security.RoleAnalyst}
	registrationSpecialists = []security.Role{security.RoleAnalyst}
)

// registrationListCap bounds what a listing reads before filtering.
const registrationListCap = 500

func (s *RegistrationService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

// RegistrationView is a registration as of now.
type RegistrationView struct {
	registration.Registration
	Flags   []registration.Flag
	InForce bool
	AsOf    time.Time
	// NumberRevealed is whether the caller sees the whole registration
	// number. A listing never reveals it; a detail read reveals it to the
	// people who record and review registrations, not to an auditor.
	NumberRevealed bool
}

func (s *RegistrationService) registrationView(r registration.Registration, reveal bool) RegistrationView {
	now := s.now()
	day := registration.Day(now)
	return RegistrationView{Registration: r, Flags: r.FlagsAt(day), InForce: r.InForce(day), AsOf: now, NumberRevealed: reveal}
}

// CertificateView is a certificate as of now.
type CertificateView struct {
	registration.Certificate
	Flags   []registration.Flag
	InForce bool
	AsOf    time.Time
}

func (s *RegistrationService) certificateView(c registration.Certificate) CertificateView {
	now := s.now()
	day := registration.Day(now)
	return CertificateView{Certificate: c, Flags: c.FlagsAt(day), InForce: c.InForce(day), AsOf: now}
}

func (s *RegistrationService) entity(ctx context.Context, le id.LegalEntityID) (id.LegalEntityID, error) {
	if le.IsZero() {
		d, err := s.entities.Default(ctx)
		if err != nil {
			return id.LegalEntityID{}, err
		}
		return d.ID, nil
	}
	got, err := s.entities.ByID(ctx, le)
	if err != nil {
		if errs.IsCategory(err, errs.CategoryNotFound) {
			return id.LegalEntityID{}, errs.Invalid("legalEntityId", errs.ReasonInvalidValue, "That is not one of the tenant's legal entities.")
		}
		return id.LegalEntityID{}, err
	}
	return got.ID, nil
}

func checkIntakeKey(key string) error {
	if key == "" {
		return errs.New(errs.CategoryValidation, errs.ReasonIdempotencyKeyRequired,
			"This endpoint requires an Idempotency-Key header. The request was not applied.")
	}
	if len(key) > 255 {
		return errs.Invalid("Idempotency-Key", errs.ReasonInvalidValue, "An Idempotency-Key is at most 255 characters.")
	}
	return nil
}

func keyReuse() error {
	return errs.New(errs.CategoryConflict, errs.ReasonIdempotencyKeyReuse,
		"This Idempotency-Key was already used for a different request. Nothing was recorded.")
}

// ---------------------------------------------------------------------------
// registrations
// ---------------------------------------------------------------------------

// RegistrationInput is a registration to record.
type RegistrationInput struct {
	IntakeKey     string
	LegalEntity   id.LegalEntityID // zero for the tenant's default entity
	Scheme        registration.Scheme
	Jurisdiction  string
	Authority     string
	Number        string
	EffectiveFrom time.Time
	EffectiveTo   time.Time
	Supersedes    id.RegistrationID
	Attachments   []registration.Attachment
	// Submit submits the registration for review as it is recorded;
	// otherwise it is kept as a DRAFT.
	Submit bool
}

// CreateRegistration records a registration as a DRAFT, or SUBMITTED for
// review — never VERIFIED or ACTIVE (Doc 17 ER-014). created is false when
// the Idempotency-Key already recorded this request.
func (s *RegistrationService) CreateRegistration(ctx context.Context, in RegistrationInput) (RegistrationView, bool, error) {
	sc, err := requirePerson(ctx, registrationRecorders...)
	if err != nil {
		return RegistrationView{}, false, err
	}
	key := strings.TrimSpace(in.IntakeKey)
	if err := checkIntakeKey(key); err != nil {
		return RegistrationView{}, false, err
	}
	var (
		out     registration.Registration
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		le, err := s.entity(ctx, in.LegalEntity)
		if err != nil {
			return err
		}
		r := registration.Registration{
			TenantID: sc.Tenant(), LegalEntity: le, Scheme: in.Scheme,
			Jurisdiction: strings.ToUpper(strings.TrimSpace(in.Jurisdiction)), Authority: strings.TrimSpace(in.Authority),
			Number: registration.NormalizeNumber(in.Scheme, in.Number), EffectiveFrom: registration.Day(in.EffectiveFrom),
			Supersedes: in.Supersedes, IntakeKey: key, CreatedBy: sc.Subject(),
		}
		if in.EffectiveFrom.IsZero() {
			r.EffectiveFrom = time.Time{}
		}
		if !in.EffectiveTo.IsZero() {
			r.EffectiveTo = registration.Day(in.EffectiveTo)
		}
		docs := make([]registration.Attachment, len(in.Attachments))
		for i, a := range in.Attachments {
			docs[i] = a.Normalize()
		}
		if err := r.Validate(); err != nil {
			return err
		}
		if r.IntakeHash, err = canonical.Sum(r.Payload(in.Submit, docs)); err != nil {
			return internal(err, "The registration could not be hashed.")
		}
		if err := s.store.Lock(ctx, "intake/"+key); err != nil {
			return err
		}
		if prior, found, err := s.store.RegistrationByIntakeKey(ctx, key); err != nil {
			return err
		} else if found {
			if !prior.IntakeHash.Equal(r.IntakeHash) {
				return keyReuse()
			}
			out = prior
			return nil
		}
		if err := s.store.Lock(ctx, "number/"+string(r.Scheme)+"/"+r.Number); err != nil {
			return err
		}
		same, err := s.store.RegistrationsByNumber(ctx, r.Scheme, r.Number)
		if err != nil {
			return err
		}
		for _, o := range same {
			if o.Status.Closed() {
				continue
			}
			if o.LegalEntity != r.LegalEntity {
				return errs.New(errs.CategoryConflict, registration.ReasonProvenanceConflict,
					"This registration number is already recorded for another of the tenant's legal entities. One entity's registration is never reused for another.")
			}
			if o.ID != r.Supersedes {
				return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists,
					fmt.Sprintf("Registration %s already records this number. A renewal or correction is a new registration that supersedes it.", o.ID))
			}
		}
		if !r.Supersedes.IsZero() {
			old, err := s.store.Registration(ctx, r.Supersedes)
			if err != nil {
				if errs.IsCategory(err, errs.CategoryNotFound) {
					return errs.Invalid("supersedes", errs.ReasonInvalidValue, "That is not one of the tenant's registrations.")
				}
				return err
			}
			if old.LegalEntity != r.LegalEntity || old.Scheme != r.Scheme {
				return errs.Invalid("supersedes", errs.ReasonInvalidValue, "A registration supersedes one of the same legal entity and scheme.")
			}
		}
		rid, err := idgen.RegistrationID(s.ids)
		if err != nil {
			return internal(err, "The registration could not be recorded.")
		}
		now := s.now()
		r.ID, r.CreatedAt = rid, now
		events, err := registration.Open(r, docs, in.Submit, sc.Subject(), now)
		if err != nil {
			return err
		}
		if err := s.store.CreateRegistration(ctx, r, events); err != nil {
			return err
		}
		created = true
		out, err = s.store.Registration(ctx, rid)
		return err
	})
	if err != nil {
		return RegistrationView{}, false, err
	}
	return s.registrationView(out, true), created, nil
}

// Registrations lists registrations as of now, newest first, the number
// masked.
func (s *RegistrationService) Registrations(ctx context.Context, f registration.Filter, limit int) ([]RegistrationView, error) {
	if _, err := requireRoleOrSystem(ctx, registrationReaders...); err != nil {
		return nil, err
	}
	all, err := s.store.Registrations(ctx, registrationListCap)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > registrationListCap {
		limit = registrationListCap
	}
	day := registration.Day(s.now())
	out := []RegistrationView{}
	for _, r := range all {
		if f.Match(r, day) && len(out) < limit {
			out = append(out, s.registrationView(r, false))
		}
	}
	return out, nil
}

// Registration reads one registration as of now.
func (s *RegistrationService) Registration(ctx context.Context, registrationID id.RegistrationID) (RegistrationView, error) {
	sc, err := requireRoleOrSystem(ctx, registrationReaders...)
	if err != nil {
		return RegistrationView{}, err
	}
	r, err := s.store.Registration(ctx, registrationID)
	if err != nil {
		return RegistrationView{}, err
	}
	return s.registrationView(r, sc.HasAny(registrationRecorders...)), nil
}

// TransitionRegistration moves a registration to the status a person
// names, against the version they read. Submission and withdrawal are the
// recorders'; everything else is the specialist's.
func (s *RegistrationService) TransitionRegistration(ctx context.Context, registrationID id.RegistrationID,
	to registration.Status, reason string, version int) (RegistrationView, error) {
	roles := registrationSpecialists
	if to == registration.StatusSubmitted || to == registration.StatusWithdrawn {
		roles = registrationRecorders
	}
	return s.actRegistration(ctx, registrationID, roles, func(r registration.Registration, by id.UserID, at time.Time) (registration.Event, error) {
		return r.Transition(to, reason, version, by, at)
	})
}

// RecordRegistrationCheck records an authority check a specialist made.
func (s *RegistrationService) RecordRegistrationCheck(ctx context.Context, registrationID id.RegistrationID,
	c registration.Check, note string) (RegistrationView, error) {
	return s.actRegistration(ctx, registrationID, registrationSpecialists, func(r registration.Registration, by id.UserID, at time.Time) (registration.Event, error) {
		return r.RecordCheck(c, note, by, at)
	})
}

// RecordRegistrationAttachment records a supporting document by digest.
func (s *RegistrationService) RecordRegistrationAttachment(ctx context.Context, registrationID id.RegistrationID,
	a registration.Attachment) (RegistrationView, error) {
	return s.actRegistration(ctx, registrationID, registrationRecorders, func(r registration.Registration, by id.UserID, at time.Time) (registration.Event, error) {
		return r.RecordAttachment(a, by, at)
	})
}

func (s *RegistrationService) actRegistration(ctx context.Context, registrationID id.RegistrationID, roles []security.Role,
	fn func(registration.Registration, id.UserID, time.Time) (registration.Event, error)) (RegistrationView, error) {
	sc, err := requirePerson(ctx, roles...)
	if err != nil {
		return RegistrationView{}, err
	}
	var out registration.Registration
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "registration/"+registrationID.String()); err != nil {
			return err
		}
		// Read under the lock: a version compared against a history somebody
		// has since extended is compared against the wrong one.
		r, err := s.store.Registration(ctx, registrationID)
		if err != nil {
			return err
		}
		e, err := fn(r, sc.Subject(), s.now())
		if err != nil {
			return err
		}
		if err := s.store.AppendRegistrationEvents(ctx, registrationID, []registration.Event{e}); err != nil {
			return err
		}
		out, err = s.store.Registration(ctx, registrationID)
		return err
	})
	if err != nil {
		return RegistrationView{}, err
	}
	return s.registrationView(out, true), nil
}

// ---------------------------------------------------------------------------
// exemption certificates
// ---------------------------------------------------------------------------

// CertificateInput is a certificate to receive, with its document.
type CertificateInput struct {
	IntakeKey         string
	LegalEntity       id.LegalEntityID // the seller; zero for the default entity
	BuyerRef          string
	BuyerName         string
	Type              registration.CertificateType
	ExemptionReason   string
	Jurisdiction      string
	CertificateNumber string
	ProductScope      []string
	IssuedOn          time.Time
	ExpiresOn         time.Time
	Supersedes        id.ExemptionCertificateID
	Document          registration.Attachment
}

// checkProvenance refuses a document already held for a different buyer or
// seller (Doc 17 EER-10 "certificate copied from another entity").
func (s *RegistrationService) checkProvenance(ctx context.Context, sha string, self id.ExemptionCertificateID,
	le id.LegalEntityID, buyer string) error {
	if err := s.store.Lock(ctx, "document/"+sha); err != nil {
		return err
	}
	holders, err := s.store.CertificatesWithDocument(ctx, sha)
	if err != nil {
		return err
	}
	for _, h := range holders {
		if h.ID != self && (h.LegalEntity != le || h.BuyerRef != buyer) {
			return errs.New(errs.CategoryConflict, registration.ReasonProvenanceConflict,
				fmt.Sprintf("This document is already recorded as certificate %s for a different buyer or seller entity. It is not evidence for this one.", h.ID))
		}
	}
	return nil
}

// ReceiveCertificate receives a certificate as SUBMITTED, evidence version
// 1. Receipt is not approval and changes no tax result. created is false
// when the Idempotency-Key already received this request.
func (s *RegistrationService) ReceiveCertificate(ctx context.Context, in CertificateInput) (CertificateView, bool, error) {
	sc, err := requirePerson(ctx, registrationRecorders...)
	if err != nil {
		return CertificateView{}, false, err
	}
	key := strings.TrimSpace(in.IntakeKey)
	if err := checkIntakeKey(key); err != nil {
		return CertificateView{}, false, err
	}
	var (
		out     registration.Certificate
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		le, err := s.entity(ctx, in.LegalEntity)
		if err != nil {
			return err
		}
		scope := make([]string, 0, len(in.ProductScope))
		for _, p := range in.ProductScope {
			if p = strings.TrimSpace(p); p != "" && !slices.Contains(scope, p) {
				scope = append(scope, p)
			}
		}
		c := registration.Certificate{
			TenantID: sc.Tenant(), LegalEntity: le, BuyerRef: strings.TrimSpace(in.BuyerRef),
			BuyerName: strings.TrimSpace(in.BuyerName), Type: in.Type, ExemptionReason: strings.TrimSpace(in.ExemptionReason),
			Jurisdiction: strings.ToUpper(strings.TrimSpace(in.Jurisdiction)), CertificateNumber: strings.TrimSpace(in.CertificateNumber),
			ProductScope: scope, Supersedes: in.Supersedes, IntakeKey: key, CreatedBy: sc.Subject(),
		}
		if !in.IssuedOn.IsZero() {
			c.IssuedOn = registration.Day(in.IssuedOn)
		}
		if !in.ExpiresOn.IsZero() {
			c.ExpiresOn = registration.Day(in.ExpiresOn)
		}
		doc := in.Document.Normalize()
		if err := c.Validate(); err != nil {
			return err
		}
		if err := doc.Validate(); err != nil {
			return err
		}
		if c.IntakeHash, err = canonical.Sum(c.Payload(doc)); err != nil {
			return internal(err, "The certificate could not be hashed.")
		}
		if err := s.store.Lock(ctx, "intake/"+key); err != nil {
			return err
		}
		if prior, found, err := s.store.CertificateByIntakeKey(ctx, key); err != nil {
			return err
		} else if found {
			if !prior.IntakeHash.Equal(c.IntakeHash) {
				return keyReuse()
			}
			out = prior
			return nil
		}
		if err := s.checkProvenance(ctx, doc.SHA256, id.ExemptionCertificateID{}, c.LegalEntity, c.BuyerRef); err != nil {
			return err
		}
		if !c.Supersedes.IsZero() {
			old, err := s.store.Certificate(ctx, c.Supersedes)
			if err != nil {
				if errs.IsCategory(err, errs.CategoryNotFound) {
					return errs.Invalid("supersedes", errs.ReasonInvalidValue, "That is not one of the tenant's certificates.")
				}
				return err
			}
			if old.LegalEntity != c.LegalEntity || old.BuyerRef != c.BuyerRef || old.Jurisdiction != c.Jurisdiction {
				return errs.Invalid("supersedes", errs.ReasonInvalidValue,
					"A certificate renews one of the same seller entity, buyer and jurisdiction.")
			}
		}
		cid, err := idgen.ExemptionCertificateID(s.ids)
		if err != nil {
			return internal(err, "The certificate could not be received.")
		}
		now := s.now()
		c.ID, c.CreatedAt = cid, now
		received, err := registration.Receive(c, doc, sc.Subject(), now)
		if err != nil {
			return err
		}
		if err := s.store.CreateCertificate(ctx, c, received); err != nil {
			return err
		}
		created = true
		out, err = s.store.Certificate(ctx, cid)
		return err
	})
	if err != nil {
		return CertificateView{}, false, err
	}
	return s.certificateView(out), created, nil
}

// Certificates lists certificates as of now, newest first.
func (s *RegistrationService) Certificates(ctx context.Context, f registration.CertificateFilter, limit int) ([]CertificateView, error) {
	if _, err := requireRoleOrSystem(ctx, registrationReaders...); err != nil {
		return nil, err
	}
	all, err := s.store.Certificates(ctx, registrationListCap)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > registrationListCap {
		limit = registrationListCap
	}
	day := registration.Day(s.now())
	out := []CertificateView{}
	for _, c := range all {
		if f.Match(c, day) && len(out) < limit {
			out = append(out, s.certificateView(c))
		}
	}
	return out, nil
}

// Certificate reads one certificate as of now.
func (s *RegistrationService) Certificate(ctx context.Context, certificateID id.ExemptionCertificateID) (CertificateView, error) {
	if _, err := requireRoleOrSystem(ctx, registrationReaders...); err != nil {
		return CertificateView{}, err
	}
	c, err := s.store.Certificate(ctx, certificateID)
	if err != nil {
		return CertificateView{}, err
	}
	return s.certificateView(c), nil
}

// AddCertificateVersion records a corrected document as the next evidence
// version.
func (s *RegistrationService) AddCertificateVersion(ctx context.Context, certificateID id.ExemptionCertificateID,
	doc registration.Attachment, note string) (CertificateView, error) {
	return s.actCertificate(ctx, certificateID, registrationRecorders,
		func(ctx context.Context, c registration.Certificate, by id.UserID, at time.Time) (registration.CertificateEvent, error) {
			e, err := c.AddVersion(doc, note, by, at)
			if err != nil {
				return e, err
			}
			return e, s.checkProvenance(ctx, e.Document.SHA256, c.ID, c.LegalEntity, c.BuyerRef)
		})
}

// ScreenCertificate records the completeness checklist.
func (s *RegistrationService) ScreenCertificate(ctx context.Context, certificateID id.ExemptionCertificateID,
	sc registration.Screening, note string) (CertificateView, error) {
	return s.actCertificate(ctx, certificateID, registrationSpecialists,
		func(_ context.Context, c registration.Certificate, by id.UserID, at time.Time) (registration.CertificateEvent, error) {
			return c.Screen(sc, note, by, at)
		})
}

// RecommendCertificate records the maker's recommendation.
func (s *RegistrationService) RecommendCertificate(ctx context.Context, certificateID id.ExemptionCertificateID,
	r registration.Recommendation, rationale string) (CertificateView, error) {
	if !r.EffectiveFrom.IsZero() {
		r.EffectiveFrom = registration.Day(r.EffectiveFrom)
	}
	if !r.EffectiveTo.IsZero() {
		r.EffectiveTo = registration.Day(r.EffectiveTo)
	}
	return s.actCertificate(ctx, certificateID, registrationSpecialists,
		func(_ context.Context, c registration.Certificate, by id.UserID, at time.Time) (registration.CertificateEvent, error) {
			return c.Recommend(r, rationale, by, at)
		})
}

// DecideCertificate is the checker confirming or returning the pending
// recommendation on the evidence version they reviewed. An approval of a
// renewal supersedes the certificate it renews, in the same transaction.
func (s *RegistrationService) DecideCertificate(ctx context.Context, certificateID id.ExemptionCertificateID,
	d registration.Decision, version int, note string) (CertificateView, error) {
	return s.actCertificate(ctx, certificateID, registrationSpecialists,
		func(ctx context.Context, c registration.Certificate, by id.UserID, at time.Time) (registration.CertificateEvent, error) {
			e, err := c.Decide(d, version, note, by, at)
			if err != nil || e.Status != registration.CertApproved || c.Supersedes.IsZero() {
				return e, err
			}
			if err := s.store.Lock(ctx, "certificate/"+c.Supersedes.String()); err != nil {
				return e, err
			}
			old, err := s.store.Certificate(ctx, c.Supersedes)
			if err != nil {
				return e, err
			}
			if old.Status != registration.CertApproved && old.Status != registration.CertSuspended {
				return e, nil
			}
			sup, err := old.Supersede(c.ID, by, at)
			if err != nil {
				return e, err
			}
			return e, s.store.AppendCertificateEvent(ctx, old.ID, sup)
		})
}

// TransitionCertificate suspends, reinstates, expires or revokes an
// approved certificate.
func (s *RegistrationService) TransitionCertificate(ctx context.Context, certificateID id.ExemptionCertificateID,
	to registration.CertificateStatus, reason string) (CertificateView, error) {
	return s.actCertificate(ctx, certificateID, registrationSpecialists,
		func(_ context.Context, c registration.Certificate, by id.UserID, at time.Time) (registration.CertificateEvent, error) {
			return c.Transition(to, reason, by, at)
		})
}

func (s *RegistrationService) actCertificate(ctx context.Context, certificateID id.ExemptionCertificateID, roles []security.Role,
	fn func(context.Context, registration.Certificate, id.UserID, time.Time) (registration.CertificateEvent, error)) (CertificateView, error) {
	sc, err := requirePerson(ctx, roles...)
	if err != nil {
		return CertificateView{}, err
	}
	var out registration.Certificate
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "certificate/"+certificateID.String()); err != nil {
			return err
		}
		c, err := s.store.Certificate(ctx, certificateID)
		if err != nil {
			return err
		}
		e, err := fn(ctx, c, sc.Subject(), s.now())
		if err != nil {
			return err
		}
		if err := s.store.AppendCertificateEvent(ctx, certificateID, e); err != nil {
			return err
		}
		out, err = s.store.Certificate(ctx, certificateID)
		return err
	})
	if err != nil {
		return CertificateView{}, err
	}
	return s.certificateView(out), nil
}

func (s *RegistrationService) inTx(ctx context.Context, fn func(context.Context) error) error {
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(txCtx)
}
