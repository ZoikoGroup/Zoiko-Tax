package app_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// memRegistrations is the registration and certificate ledgers in memory:
// append-only, refusing a second record under an intake key as the unique
// indexes of migration 000033 do.
type memRegistrations struct {
	mu         sync.Mutex
	regs       []registration.Registration
	regEvents  map[id.RegistrationID][]registration.Event
	certs      []registration.Certificate
	certEvents map[id.ExemptionCertificateID][]registration.CertificateEvent
}

func (m *memRegistrations) Lock(context.Context, string) error { return nil }

func (m *memRegistrations) CreateRegistration(_ context.Context, r registration.Registration, events []registration.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.regs {
		if o.IntakeKey == r.IntakeKey {
			return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "intake key")
		}
	}
	if m.regEvents == nil {
		m.regEvents = map[id.RegistrationID][]registration.Event{}
	}
	m.regs = append(m.regs, r)
	m.regEvents[r.ID] = slices.Clone(events)
	return nil
}

func (m *memRegistrations) AppendRegistrationEvents(_ context.Context, rid id.RegistrationID, events []registration.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range events {
		if len(m.regEvents[rid])+1 != e.Seq {
			return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "seq")
		}
		m.regEvents[rid] = append(m.regEvents[rid], e)
	}
	return nil
}

func (m *memRegistrations) foldedRegs(match func(registration.Registration) bool) []registration.Registration {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []registration.Registration
	for _, r := range m.regs {
		if !match(r) {
			continue
		}
		f, err := registration.Fold(r, m.regEvents[r.ID])
		if err != nil {
			panic(err)
		}
		out = append(out, f)
	}
	slices.Reverse(out)
	return out
}

func (m *memRegistrations) Registration(_ context.Context, rid id.RegistrationID) (registration.Registration, error) {
	got := m.foldedRegs(func(r registration.Registration) bool { return r.ID == rid })
	if len(got) == 0 {
		return registration.Registration{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
	}
	return got[0], nil
}

func (m *memRegistrations) RegistrationByIntakeKey(_ context.Context, key string) (registration.Registration, bool, error) {
	got := m.foldedRegs(func(r registration.Registration) bool { return r.IntakeKey == key })
	if len(got) == 0 {
		return registration.Registration{}, false, nil
	}
	return got[0], true, nil
}

func (m *memRegistrations) RegistrationsByNumber(_ context.Context, s registration.Scheme, n string) ([]registration.Registration, error) {
	return m.foldedRegs(func(r registration.Registration) bool { return r.Scheme == s && r.Number == n }), nil
}

func (m *memRegistrations) Registrations(_ context.Context, limit int) ([]registration.Registration, error) {
	got := m.foldedRegs(func(registration.Registration) bool { return true })
	if len(got) > limit {
		got = got[:limit]
	}
	return got, nil
}

func (m *memRegistrations) CreateCertificate(_ context.Context, c registration.Certificate, e registration.CertificateEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.certs {
		if o.IntakeKey == c.IntakeKey {
			return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "intake key")
		}
	}
	if m.certEvents == nil {
		m.certEvents = map[id.ExemptionCertificateID][]registration.CertificateEvent{}
	}
	m.certs = append(m.certs, c)
	m.certEvents[c.ID] = []registration.CertificateEvent{e}
	return nil
}

func (m *memRegistrations) AppendCertificateEvent(_ context.Context, cid id.ExemptionCertificateID, e registration.CertificateEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.certEvents[cid])+1 != e.Seq {
		return errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "seq")
	}
	m.certEvents[cid] = append(m.certEvents[cid], e)
	return nil
}

func (m *memRegistrations) foldedCerts(match func(registration.Certificate, []registration.CertificateEvent) bool) []registration.Certificate {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []registration.Certificate
	for _, c := range m.certs {
		if !match(c, m.certEvents[c.ID]) {
			continue
		}
		f, err := registration.FoldCertificate(c, m.certEvents[c.ID])
		if err != nil {
			panic(err)
		}
		out = append(out, f)
	}
	slices.Reverse(out)
	return out
}

func (m *memRegistrations) Certificate(_ context.Context, cid id.ExemptionCertificateID) (registration.Certificate, error) {
	got := m.foldedCerts(func(c registration.Certificate, _ []registration.CertificateEvent) bool { return c.ID == cid })
	if len(got) == 0 {
		return registration.Certificate{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
	}
	return got[0], nil
}

func (m *memRegistrations) CertificateByIntakeKey(_ context.Context, key string) (registration.Certificate, bool, error) {
	got := m.foldedCerts(func(c registration.Certificate, _ []registration.CertificateEvent) bool { return c.IntakeKey == key })
	if len(got) == 0 {
		return registration.Certificate{}, false, nil
	}
	return got[0], true, nil
}

func (m *memRegistrations) CertificatesWithDocument(_ context.Context, sha string) ([]registration.Certificate, error) {
	return m.foldedCerts(func(_ registration.Certificate, es []registration.CertificateEvent) bool {
		return slices.ContainsFunc(es, func(e registration.CertificateEvent) bool { return e.Document != nil && e.Document.SHA256 == sha })
	}), nil
}

func (m *memRegistrations) Certificates(_ context.Context, limit int) ([]registration.Certificate, error) {
	got := m.foldedCerts(func(registration.Certificate, []registration.CertificateEvent) bool { return true })
	if len(got) > limit {
		got = got[:limit]
	}
	return got, nil
}

var (
	regOperator = ctbPerson("01920000-0000-7000-8000-0000000000e1", security.RoleOperator)
	regAnalyst  = ctbPerson("01920000-0000-7000-8000-0000000000e2", security.RoleAnalyst)
	regAnalyst2 = ctbPerson("01920000-0000-7000-8000-0000000000e3", security.RoleAnalyst)
	regAuditor  = ctbPerson("01920000-0000-7000-8000-0000000000e4", security.RoleAuditor)
	regAdmin    = ctbPerson("01920000-0000-7000-8000-0000000000e5", security.RoleAdmin)
)

func newRegistrations(t *testing.T) (*app.RegistrationService, *memRegistrations, *settableClock) {
	t.Helper()
	store := &memRegistrations{}
	clk := &settableClock{t: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	return app.NewRegistrationService(store, ctbEntities{def: ctbEntity}, noTx{}, clk, &idgen.Sequential{}), store, clk
}

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func pdf(c byte) registration.Attachment {
	return registration.Attachment{SHA256: strings.Repeat(string(c), 64), Filename: "evidence.pdf", MediaType: "application/pdf", Bytes: 2048}
}

func frn(key string) app.RegistrationInput {
	return app.RegistrationInput{IntakeKey: key, Scheme: registration.SchemeUSFCCFRN, Jurisdiction: "us", Authority: "FCC",
		Number: "0012-345-678", EffectiveFrom: day("2026-01-01"), Attachments: []registration.Attachment{pdf('a')}, Submit: true}
}

func resale(key string, doc registration.Attachment) app.CertificateInput {
	return app.CertificateInput{IntakeKey: key, BuyerRef: "CUST-0042", BuyerName: "Hudson Wireless LLC",
		Type: registration.TypeResale, ExemptionReason: "Resale of telecommunication services", Jurisdiction: "US-NY",
		CertificateNumber: "ST-120-0042", ProductScope: []string{"voice", "voice", " data "}, IssuedOn: day("2026-09-15"),
		ExpiresOn: day("2027-09-14"), Document: doc}
}

// ER-013, ER-027, XC-07: a registration is recorded SUBMITTED on the
// default entity, a retry under its key returns it, and the same key with a
// different request is refused.
func TestARegistrationIsRecordedOnceAndSubmittedNotActive(t *testing.T) {
	svc, store, _ := newRegistrations(t)
	v, created, err := svc.CreateRegistration(regOperator, frn("k-1"))
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	if v.Status != registration.StatusSubmitted || v.InForce || v.Number != "0012345678" || v.Jurisdiction != "US" ||
		v.LegalEntity != ctbEntity.ID || !v.NumberRevealed {
		t.Fatalf("%+v", v.Registration)
	}
	again, created, err := svc.CreateRegistration(regOperator, frn("k-1"))
	if err != nil || created || again.ID != v.ID {
		t.Fatalf("replay: %v %v", created, err)
	}
	changed := frn("k-1")
	changed.Authority = "FCC CORES"
	if _, _, err := svc.CreateRegistration(regOperator, changed); errs.ReasonOf(err) != errs.ReasonIdempotencyKeyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	if _, _, err := svc.CreateRegistration(regOperator, frn("")); errs.ReasonOf(err) != errs.ReasonIdempotencyKeyRequired {
		t.Fatalf("no key: %v", err)
	}
	if _, _, err := svc.CreateRegistration(regOperator, frn("k-2")); errs.ReasonOf(err) != errs.ReasonAlreadyExists {
		t.Fatalf("the same number twice: %v", err)
	}
	wrong := frn("k-3")
	wrong.Scheme = registration.SchemeUSUSAC499
	if _, _, err := svc.CreateRegistration(regOperator, wrong); errs.ReasonOf(err) != registration.ReasonSchemeMismatch {
		t.Fatalf("an FRN as a 499 Filer ID: %v", err)
	}
	if len(store.regs) != 1 {
		t.Fatalf("%d registrations", len(store.regs))
	}
}

// Doc 17 §07 end to end: an authority check verifies, a second person
// activates the exact version, and a listing masks the number.
func TestRegistrationVerificationAndActivation(t *testing.T) {
	svc, _, _ := newRegistrations(t)
	v, _, err := svc.CreateRegistration(regOperator, frn("k-1"))
	if err != nil {
		t.Fatal(err)
	}
	check := registration.Check{Source: registration.SourceFCC, Method: registration.MethodManualLookup,
		Outcome: registration.OutcomeUnavailable, CheckedAt: day("2026-11-18")}
	v, err = svc.RecordRegistrationCheck(regAnalyst, v.ID, check, "CORES was down")
	if err != nil || v.Status != registration.StatusSubmitted || !slices.Contains(v.Flags, registration.FlagPendingValidation) {
		t.Fatalf("unavailable: %s %v", v.Status, err)
	}
	if _, err := svc.RecordRegistrationCheck(regOperator, v.ID, check, ""); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an operator recorded a check: %v", err)
	}
	check.Outcome, check.Reference = registration.OutcomeVerified, "CORES-0012345678-20261118"
	v, err = svc.RecordRegistrationCheck(regAnalyst, v.ID, check, "")
	if err != nil || v.Status != registration.StatusVerified {
		t.Fatalf("verified: %s %v", v.Status, err)
	}
	if _, err := svc.TransitionRegistration(regAnalyst, v.ID, registration.StatusActive, "ok", v.Version()-1); errs.ReasonOf(err) != registration.ReasonVersionConflict {
		t.Fatalf("stale activation: %v", err)
	}
	v, err = svc.TransitionRegistration(regAnalyst, v.ID, registration.StatusActive, "Legal officer approval REF-9", v.Version())
	if err != nil || !v.InForce {
		t.Fatalf("activate: %v", err)
	}
	list, err := svc.Registrations(regAuditor, registration.Filter{Status: registration.StatusActive}, 0)
	if err != nil || len(list) != 1 || list[0].NumberRevealed {
		t.Fatalf("list: %d %v", len(list), err)
	}
	if got, err := svc.Registration(regAuditor, v.ID); err != nil || got.NumberRevealed {
		t.Fatalf("an auditor saw the whole number: %v", err)
	}
	if _, err := svc.Registrations(regAdmin, registration.Filter{}, 0); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("admin read registrations: %v", err)
	}
}

// OBL-02: one subsidiary's registration number is not reused for another.
func TestARegistrationNumberBelongsToOneEntity(t *testing.T) {
	svc, store, _ := newRegistrations(t)
	if _, _, err := svc.CreateRegistration(regOperator, frn("k-1")); err != nil {
		t.Fatal(err)
	}
	store.regs[0].LegalEntity = id.NewLegalEntityID([16]byte{1}) // the number now belongs to another entity
	if _, _, err := svc.CreateRegistration(regOperator, frn("k-2")); errs.ReasonOf(err) != registration.ReasonProvenanceConflict {
		t.Fatalf("a number reused across entities: %v", err)
	}
}

// The certificate lifecycle with dual control (ER-040..042), and an
// approval that changes nothing but the certificate.
func TestCertificateDualControl(t *testing.T) {
	svc, store, _ := newRegistrations(t)
	c, created, err := svc.ReceiveCertificate(regOperator, resale("c-1", pdf('b')))
	if err != nil || !created || c.Status != registration.CertSubmitted || len(c.ProductScope) != 2 {
		t.Fatalf("receive: %v %v %v", created, err, c.ProductScope)
	}
	if again, created, err := svc.ReceiveCertificate(regOperator, resale("c-1", pdf('b'))); err != nil || created || again.ID != c.ID {
		t.Fatalf("replay: %v %v", created, err)
	}
	if _, _, err := svc.ReceiveCertificate(regOperator, resale("c-1", pdf('c'))); errs.ReasonOf(err) != errs.ReasonIdempotencyKeyReuse {
		t.Fatalf("same key, different bytes: %v", err)
	}
	p := registration.CheckPass
	screen := registration.Screening{SellerMatches: p, BuyerMatches: p, JurisdictionMatches: p, FormAccepted: p,
		SignaturePresent: p, DatesValid: p, NumberPresent: p, Result: registration.ScreenDocumentComplete}
	if c, err = svc.ScreenCertificate(regAnalyst, c.ID, screen, ""); err != nil || c.Status != registration.CertReviewRequired {
		t.Fatalf("screen: %v", err)
	}
	rec := registration.Recommendation{Approve: true, EffectiveFrom: day("2026-09-15"), EffectiveTo: day("2027-09-14")}
	if c, err = svc.RecommendCertificate(regAnalyst, c.ID, rec, "ST-120 complete; buyer and seller match"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DecideCertificate(regAnalyst, c.ID, registration.DecisionConfirm, 1, ""); errs.ReasonOf(err) != registration.ReasonMakerChecker {
		t.Fatalf("the recommender confirmed: %v", err)
	}
	if c, err = svc.DecideCertificate(regAnalyst2, c.ID, registration.DecisionConfirm, 1, "Checked"); err != nil || !c.InForce {
		t.Fatalf("confirm: %v", err)
	}
	if len(store.certs) != 1 || len(store.regs) != 0 {
		t.Fatal("an approval wrote something other than its certificate")
	}

	// A renewal supersedes the approved certificate on its own approval; the
	// old one stays approved until then (ER-038).
	in := resale("c-2", pdf('d'))
	in.Supersedes, in.IssuedOn, in.ExpiresOn = c.ID, day("2026-11-01"), day("2028-10-31")
	n, _, err := svc.ReceiveCertificate(regOperator, in)
	if err != nil {
		t.Fatal(err)
	}
	if old, _ := svc.Certificate(regAuditor, c.ID); old.Status != registration.CertApproved {
		t.Fatalf("a renewal upload changed the old certificate: %s", old.Status)
	}
	n, _ = svc.ScreenCertificate(regAnalyst, n.ID, screen, "")
	rec = registration.Recommendation{Approve: true, EffectiveFrom: day("2026-11-01"), EffectiveTo: day("2028-10-31")}
	n, _ = svc.RecommendCertificate(regAnalyst, n.ID, rec, "renewal")
	if _, err := svc.DecideCertificate(regAnalyst2, n.ID, registration.DecisionConfirm, 1, ""); err != nil {
		t.Fatal(err)
	}
	if old, _ := svc.Certificate(regAuditor, c.ID); old.Status != registration.CertSuperseded {
		t.Fatalf("the renewed certificate is %s", old.Status)
	}
}

// EER-10, ER-023: a document held for one buyer is not evidence for another.
func TestACertificateDocumentBelongsToOneBuyer(t *testing.T) {
	svc, _, _ := newRegistrations(t)
	if _, _, err := svc.ReceiveCertificate(regOperator, resale("c-1", pdf('e'))); err != nil {
		t.Fatal(err)
	}
	copied := resale("c-2", pdf('e'))
	copied.BuyerRef, copied.BuyerName = "CUST-0099", "Hudson Wireless LLC"
	if _, _, err := svc.ReceiveCertificate(regOperator, copied); errs.ReasonOf(err) != registration.ReasonProvenanceConflict {
		t.Fatalf("a document carried to another buyer: %v", err)
	}
	if _, _, err := svc.ReceiveCertificate(regAuditor, resale("c-3", pdf('f'))); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an auditor received a certificate: %v", err)
	}
}

// ER-037, ER-039: an expiring certificate is in the renewal queue, and
// expiry blocks only from its end date on.
func TestCertificateExpiryQueue(t *testing.T) {
	svc, _, clk := newRegistrations(t)
	in := resale("c-1", pdf('9'))
	in.ExpiresOn = day("2026-11-19")
	c, _, err := svc.ReceiveCertificate(regOperator, in)
	if err != nil {
		t.Fatal(err)
	}
	p := registration.CheckPass
	c, _ = svc.ScreenCertificate(regAnalyst, c.ID, registration.Screening{SellerMatches: p, BuyerMatches: p,
		JurisdictionMatches: p, FormAccepted: p, SignaturePresent: p, DatesValid: p, NumberPresent: p,
		Result: registration.ScreenDocumentComplete}, "")
	c, _ = svc.RecommendCertificate(regAnalyst, c.ID, registration.Recommendation{Approve: true,
		EffectiveFrom: day("2026-09-15"), EffectiveTo: day("2026-11-19")}, "ok")
	if _, err := svc.DecideCertificate(regAnalyst2, c.ID, registration.DecisionConfirm, 1, ""); err != nil {
		t.Fatal(err)
	}
	queue, err := svc.Certificates(regAuditor, registration.CertificateFilter{Flag: registration.FlagExpiringSoon}, 0)
	if err != nil || len(queue) != 1 {
		t.Fatalf("renewal queue: %d %v", len(queue), err)
	}
	if _, err := svc.TransitionCertificate(regAnalyst, c.ID, registration.CertExpired, ""); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("expired early: %v", err)
	}
	clk.Set(time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC))
	got, err := svc.TransitionCertificate(regAnalyst, c.ID, registration.CertExpired, "")
	if err != nil || got.Status != registration.CertExpired || got.InForce {
		t.Fatalf("expire: %v", err)
	}
}
