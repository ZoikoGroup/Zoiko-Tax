package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The registration and exemption evidence surface (Engineering Doc 17,
// G-048). Enumerations cross it as the strings the domain names them, so
// nothing here depends on the generated constant names.

func (rt *Router) registrationsUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Registrations != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"Registrations and exemption evidence are not wired in this cell. The request was not applied."))
	return true
}

func toEvidenceDocument(a registration.Attachment) gen.EvidenceDocument {
	return gen.EvidenceDocument{Sha256: a.SHA256, Filename: a.Filename, MediaType: a.MediaType, Bytes: a.Bytes}
}

func fromEvidenceDocument(d gen.EvidenceDocument) registration.Attachment {
	return registration.Attachment{SHA256: d.Sha256, Filename: d.Filename, MediaType: d.MediaType, Bytes: d.Bytes}
}

func toAuthorityCheck(c registration.Check) gen.AuthorityCheck {
	return gen.AuthorityCheck{
		Source: gen.AuthorityCheckSource(c.Source), Method: gen.AuthorityCheckMethod(c.Method),
		Outcome: gen.AuthorityCheckOutcome(c.Outcome), Reference: optStr(c.Reference),
		CheckedAt: canonical.FormatTime(c.CheckedAt), ReceiptSha256: optStr(c.ReceiptSHA256),
	}
}

func toEvidenceFlags(flags []registration.Flag) []gen.EvidenceFlag {
	out := make([]gen.EvidenceFlag, len(flags))
	for i, f := range flags {
		out[i] = gen.EvidenceFlag(f)
	}
	return out
}

func toRegistration(v app.RegistrationView) gen.Registration {
	r := v.Registration
	number := r.Number
	if !v.NumberRevealed {
		number = registration.MaskNumber(r.Number)
	}
	out := gen.Registration{
		ID: r.ID.String(), LegalEntityID: r.LegalEntity.String(), Scheme: gen.RegistrationScheme(r.Scheme),
		Jurisdiction: r.Jurisdiction, Authority: r.Authority, RegistrationNumber: number, NumberMasked: !v.NumberRevealed,
		EffectiveFrom: r.EffectiveFrom.Format(time.DateOnly), EffectiveTo: civilOut(r.EffectiveTo),
		Status: gen.RegistrationStatus(r.Status), Version: saturate32(r.Version()), InForce: v.InForce,
		Flags: toEvidenceFlags(v.Flags), Attachments: []gen.EvidenceDocument{},
		History: make([]gen.RegistrationEvent, len(r.History)), AsOf: canonical.FormatTime(v.AsOf),
		CreatedAt: canonical.FormatTime(r.CreatedAt), CreatedBy: r.CreatedBy.String(),
	}
	if !r.Supersedes.IsZero() {
		s := r.Supersedes.String()
		out.Supersedes = &s
	}
	if c, ok := r.LastCheck(); ok {
		gc := toAuthorityCheck(c)
		out.LastCheck = &gc
	}
	if s, ok := r.Submitter(); ok {
		by := s.String()
		out.SubmittedBy = &by
	}
	for i, e := range r.History {
		ge := gen.RegistrationEvent{
			Seq: saturate32(e.Seq), Kind: gen.RegistrationEventKind(e.Kind), Status: gen.RegistrationStatus(e.Status),
			Reason: optStr(e.Reason), RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String(),
		}
		if e.Check != nil {
			c := toAuthorityCheck(*e.Check)
			ge.Check = &c
		}
		if e.Attachment != nil {
			d := toEvidenceDocument(*e.Attachment)
			ge.Attachment = &d
			out.Attachments = append(out.Attachments, d)
		}
		out.History[i] = ge
	}
	return out
}

func toScreening(s registration.Screening) gen.ExemptionScreening {
	return gen.ExemptionScreening{
		SellerMatches: gen.ScreeningCheckResult(s.SellerMatches), BuyerMatches: gen.ScreeningCheckResult(s.BuyerMatches),
		JurisdictionMatches: gen.ScreeningCheckResult(s.JurisdictionMatches), FormAccepted: gen.ScreeningCheckResult(s.FormAccepted),
		SignaturePresent: gen.ScreeningCheckResult(s.SignaturePresent), DatesValid: gen.ScreeningCheckResult(s.DatesValid),
		NumberPresent: gen.ScreeningCheckResult(s.NumberPresent), Result: gen.ScreeningResult(s.Result),
	}
}

func fromScreening(s gen.ExemptionScreening) registration.Screening {
	return registration.Screening{
		SellerMatches: registration.CheckResult(s.SellerMatches), BuyerMatches: registration.CheckResult(s.BuyerMatches),
		JurisdictionMatches: registration.CheckResult(s.JurisdictionMatches), FormAccepted: registration.CheckResult(s.FormAccepted),
		SignaturePresent: registration.CheckResult(s.SignaturePresent), DatesValid: registration.CheckResult(s.DatesValid),
		NumberPresent: registration.CheckResult(s.NumberPresent), Result: registration.ScreeningResult(s.Result),
	}
}

func recommendationOf(r registration.Recommendation) gen.ExemptionRecommendationOutcome {
	if r.Approve {
		return gen.ExemptionRecommendationOutcome("APPROVE")
	}
	return gen.ExemptionRecommendationOutcome("REJECT")
}

func toExemptionEvidence(v app.CertificateView) gen.ExemptionEvidence {
	c := v.Certificate
	doc, _ := c.Document()
	out := gen.ExemptionEvidence{
		ID: c.ID.String(), LegalEntityID: c.LegalEntity.String(), BuyerRef: c.BuyerRef, BuyerName: c.BuyerName,
		CertificateType: gen.ExemptionEvidenceType(c.Type), ExemptionReason: c.ExemptionReason, Jurisdiction: c.Jurisdiction,
		CertificateNumber: optStr(c.CertificateNumber), ProductScope: append([]string{}, c.ProductScope...),
		IssuedOn: c.IssuedOn.Format(time.DateOnly), ExpiresOn: civilOut(c.ExpiresOn), Status: gen.ExemptionEvidenceStatus(c.Status),
		EvidenceVersion: saturate32(c.EvidenceVersion), InForce: v.InForce, Flags: toEvidenceFlags(v.Flags),
		Document: toEvidenceDocument(doc), History: make([]gen.ExemptionEvidenceEvent, len(c.History)),
		AsOf: canonical.FormatTime(v.AsOf), CreatedAt: canonical.FormatTime(c.CreatedAt), CreatedBy: c.CreatedBy.String(),
	}
	if !c.Supersedes.IsZero() {
		s := c.Supersedes.String()
		out.Supersedes = &s
	}
	if s, ok := c.LastScreening(); ok {
		gs := toScreening(s)
		out.Screening = &gs
	}
	if p, ok := c.Pending(); ok && p.Recommendation != nil {
		out.PendingRecommendation = &gen.ExemptionRecommendation{
			Recommendation: recommendationOf(*p.Recommendation), EffectiveFrom: civilOut(p.Recommendation.EffectiveFrom),
			EffectiveTo: civilOut(p.Recommendation.EffectiveTo), EvidenceVersion: saturate32(p.EvidenceVersion),
			Rationale: p.Note, RecordedAt: canonical.FormatTime(p.RecordedAt), RecordedBy: p.RecordedBy.String(),
		}
	}
	if rec, maker, checker, at, ok := c.Approval(); ok {
		out.Approval = &gen.ExemptionApproval{
			EffectiveFrom: rec.EffectiveFrom.Format(time.DateOnly), EffectiveTo: civilOut(rec.EffectiveTo),
			RecommendedBy: maker.String(), ConfirmedBy: checker.String(), ConfirmedAt: canonical.FormatTime(at),
		}
	}
	for i, e := range c.History {
		ge := gen.ExemptionEvidenceEvent{
			Seq: saturate32(e.Seq), Kind: gen.ExemptionEvidenceEventKind(e.Kind), Status: gen.ExemptionEvidenceStatus(e.Status),
			EvidenceVersion: saturate32(e.EvidenceVersion), Note: optStr(e.Note),
			RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String(),
		}
		if e.Document != nil {
			d := toEvidenceDocument(*e.Document)
			ge.Document = &d
		}
		if e.Screening != nil {
			s := toScreening(*e.Screening)
			ge.Screening = &s
		}
		if e.Recommendation != nil {
			rec := recommendationOf(*e.Recommendation)
			ge.Recommendation = &rec
			ge.EffectiveFrom, ge.EffectiveTo = civilOut(e.Recommendation.EffectiveFrom), civilOut(e.Recommendation.EffectiveTo)
		}
		if e.Decision != "" {
			d := gen.ExemptionDecision(e.Decision)
			ge.Decision = &d
		}
		out.History[i] = ge
	}
	return out
}

func (rt *Router) registrationID(w http.ResponseWriter, r *http.Request) (id.RegistrationID, bool) {
	rid, err := id.ParseRegistrationID(r.PathValue("registrationId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("registrationId", errs.ReasonInvalidValue, "That is not a valid registration identifier."))
		return id.RegistrationID{}, false
	}
	return rid, true
}

func (rt *Router) certificateID(w http.ResponseWriter, r *http.Request) (id.ExemptionCertificateID, bool) {
	cid, err := id.ParseExemptionCertificateID(r.PathValue("certificateId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("certificateId", errs.ReasonInvalidValue, "That is not a valid exemption certificate identifier."))
		return id.ExemptionCertificateID{}, false
	}
	return cid, true
}

func optLegalEntityID(s *string) (id.LegalEntityID, error) {
	if s == nil {
		return id.LegalEntityID{}, nil
	}
	le, err := id.ParseLegalEntityID(*s)
	if err != nil {
		return id.LegalEntityID{}, errs.Invalid("legalEntityId", errs.ReasonInvalidValue, "That is not a valid legal entity identifier.")
	}
	return le, nil
}

func queryFlag(r *http.Request) (registration.Flag, error) {
	f := registration.Flag(r.URL.Query().Get("flag"))
	if f != "" && !f.Valid() {
		return "", errs.Invalid("flag", errs.ReasonInvalidValue, "That is not an evidence flag.")
	}
	return f, nil
}

func queryJurisdiction(r *http.Request) (string, error) {
	j := strings.ToUpper(r.URL.Query().Get("jurisdiction"))
	if j != "" && !registration.ValidJurisdiction(j) {
		return "", errs.Invalid("jurisdiction", errs.ReasonInvalidValue, "A jurisdiction is a country, or a country and one subdivision.")
	}
	return j, nil
}

// ---------------------------------------------------------------------------
// registrations
// ---------------------------------------------------------------------------

func (rt *Router) handleListRegistrations(w http.ResponseWriter, r *http.Request) {
	if rt.registrationsUnavailable(w, r) {
		return
	}
	q := r.URL.Query()
	f := registration.Filter{Status: registration.Status(q.Get("status")), Scheme: registration.Scheme(q.Get("scheme"))}
	var err error
	switch {
	case f.Status != "" && !f.Status.Valid():
		err = errs.Invalid("status", errs.ReasonInvalidValue, "That is not a registration status.")
	case f.Scheme != "" && !f.Scheme.Valid():
		err = errs.Invalid("scheme", errs.ReasonInvalidValue, "That is not a registration scheme.")
	}
	if err == nil {
		f.Flag, err = queryFlag(r)
	}
	if err == nil {
		f.Jurisdiction, err = queryJurisdiction(r)
	}
	if err == nil && q.Get("legalEntityId") != "" {
		v := q.Get("legalEntityId")
		f.LegalEntity, err = optLegalEntityID(&v)
	}
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	got, err := rt.Registrations.Registrations(r.Context(), f, limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.RegistrationList{Registrations: make([]gen.Registration, len(got))}
	for i, v := range got {
		out.Registrations[i] = toRegistration(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleCreateRegistration(w http.ResponseWriter, r *http.Request) {
	if rt.registrationsUnavailable(w, r) {
		return
	}
	var req gen.RegistrationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in, err := registrationInput(r, req)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, created, err := rt.Registrations.CreateRegistration(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toRegistration(v))
}

func registrationInput(r *http.Request, req gen.RegistrationRequest) (app.RegistrationInput, error) {
	in := app.RegistrationInput{
		IntakeKey: r.Header.Get("Idempotency-Key"), Scheme: registration.Scheme(req.Scheme), Jurisdiction: req.Jurisdiction,
		Authority: req.Authority, Number: req.RegistrationNumber, Submit: req.Submit != nil && *req.Submit,
	}
	var err error
	if in.LegalEntity, err = optLegalEntityID(req.LegalEntityID); err != nil {
		return in, err
	}
	if in.EffectiveFrom, err = parseCivilDate("effectiveFrom", req.EffectiveFrom); err != nil {
		return in, err
	}
	if req.EffectiveTo != nil {
		if in.EffectiveTo, err = parseCivilDate("effectiveTo", *req.EffectiveTo); err != nil {
			return in, err
		}
	}
	if req.Supersedes != nil {
		if in.Supersedes, err = id.ParseRegistrationID(*req.Supersedes); err != nil {
			return in, errs.Invalid("supersedes", errs.ReasonInvalidValue, "That is not a valid registration identifier.")
		}
	}
	if len(req.Attachments) > 20 {
		return in, errs.Invalid("attachments", errs.ReasonInvalidValue, "A registration is recorded with at most 20 documents.")
	}
	for _, d := range req.Attachments {
		in.Attachments = append(in.Attachments, fromEvidenceDocument(d))
	}
	return in, nil
}

func (rt *Router) handleGetRegistration(w http.ResponseWriter, r *http.Request) {
	if rt.registrationsUnavailable(w, r) {
		return
	}
	rid, ok := rt.registrationID(w, r)
	if !ok {
		return
	}
	v, err := rt.Registrations.Registration(r.Context(), rid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toRegistration(v))
}

func (rt *Router) handleTransitionRegistration(w http.ResponseWriter, r *http.Request) {
	var req gen.RegistrationTransitionRequest
	rt.actRegistration(w, r, &req, func(ctx context.Context, rid id.RegistrationID) (app.RegistrationView, error) {
		return rt.Registrations.TransitionRegistration(ctx, rid, registration.Status(req.To), deref(req.Reason), int(req.ExpectedVersion))
	})
}

func (rt *Router) handleRecordRegistrationCheck(w http.ResponseWriter, r *http.Request) {
	var req gen.RegistrationCheckRequest
	rt.actRegistration(w, r, &req, func(ctx context.Context, rid id.RegistrationID) (app.RegistrationView, error) {
		at, err := parseTimestamp("checkedAt", req.CheckedAt)
		if err != nil {
			return app.RegistrationView{}, err
		}
		c := registration.Check{
			Source: registration.CheckSource(req.Source), Method: registration.CheckMethod(req.Method),
			Outcome: registration.CheckOutcome(req.Outcome), Reference: deref(req.Reference), CheckedAt: at,
			ReceiptSHA256: deref(req.ReceiptSha256),
		}
		return rt.Registrations.RecordRegistrationCheck(ctx, rid, c, deref(req.Note))
	})
}

func (rt *Router) handleRecordRegistrationAttachment(w http.ResponseWriter, r *http.Request) {
	var req gen.EvidenceDocument
	rt.actRegistration(w, r, &req, func(ctx context.Context, rid id.RegistrationID) (app.RegistrationView, error) {
		return rt.Registrations.RecordRegistrationAttachment(ctx, rid, fromEvidenceDocument(req))
	})
}

func (rt *Router) actRegistration(w http.ResponseWriter, r *http.Request, req any,
	act func(context.Context, id.RegistrationID) (app.RegistrationView, error)) {
	if rt.registrationsUnavailable(w, r) {
		return
	}
	rid, ok := rt.registrationID(w, r)
	if !ok {
		return
	}
	if err := decodeJSON(r, req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := act(r.Context(), rid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toRegistration(v))
}

// ---------------------------------------------------------------------------
// exemption certificates
// ---------------------------------------------------------------------------

func (rt *Router) handleListExemptionCertificates(w http.ResponseWriter, r *http.Request) {
	if rt.registrationsUnavailable(w, r) {
		return
	}
	q := r.URL.Query()
	f := registration.CertificateFilter{Status: registration.CertificateStatus(q.Get("status")), BuyerRef: q.Get("buyerRef")}
	var err error
	if f.Status != "" && !f.Status.Valid() {
		err = errs.Invalid("status", errs.ReasonInvalidValue, "That is not an exemption certificate status.")
	}
	if err == nil {
		f.Flag, err = queryFlag(r)
	}
	if err == nil {
		f.Jurisdiction, err = queryJurisdiction(r)
	}
	if err == nil && q.Get("legalEntityId") != "" {
		v := q.Get("legalEntityId")
		f.LegalEntity, err = optLegalEntityID(&v)
	}
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	got, err := rt.Registrations.Certificates(r.Context(), f, limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.ExemptionEvidenceList{Certificates: make([]gen.ExemptionEvidence, len(got))}
	for i, v := range got {
		out.Certificates[i] = toExemptionEvidence(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleReceiveExemptionCertificate(w http.ResponseWriter, r *http.Request) {
	if rt.registrationsUnavailable(w, r) {
		return
	}
	var req gen.ExemptionEvidenceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in, err := certificateInput(r, req)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, created, err := rt.Registrations.ReceiveCertificate(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toExemptionEvidence(v))
}

func certificateInput(r *http.Request, req gen.ExemptionEvidenceRequest) (app.CertificateInput, error) {
	in := app.CertificateInput{
		IntakeKey: r.Header.Get("Idempotency-Key"), BuyerRef: req.BuyerRef, BuyerName: req.BuyerName,
		Type: registration.CertificateType(req.CertificateType), ExemptionReason: req.ExemptionReason,
		Jurisdiction: req.Jurisdiction, CertificateNumber: deref(req.CertificateNumber),
		ProductScope: append([]string{}, req.ProductScope...), Document: fromEvidenceDocument(req.Document),
	}
	var err error
	if in.LegalEntity, err = optLegalEntityID(req.LegalEntityID); err != nil {
		return in, err
	}
	if in.IssuedOn, err = parseCivilDate("issuedOn", req.IssuedOn); err != nil {
		return in, err
	}
	if req.ExpiresOn != nil {
		if in.ExpiresOn, err = parseCivilDate("expiresOn", *req.ExpiresOn); err != nil {
			return in, err
		}
	}
	if req.Supersedes != nil {
		if in.Supersedes, err = id.ParseExemptionCertificateID(*req.Supersedes); err != nil {
			return in, errs.Invalid("supersedes", errs.ReasonInvalidValue, "That is not a valid exemption certificate identifier.")
		}
	}
	return in, nil
}

func (rt *Router) handleGetExemptionCertificate(w http.ResponseWriter, r *http.Request) {
	if rt.registrationsUnavailable(w, r) {
		return
	}
	cid, ok := rt.certificateID(w, r)
	if !ok {
		return
	}
	v, err := rt.Registrations.Certificate(r.Context(), cid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toExemptionEvidence(v))
}

func (rt *Router) handleAddExemptionCertificateVersion(w http.ResponseWriter, r *http.Request) {
	var req gen.ExemptionVersionRequest
	rt.actCertificate(w, r, &req, func(ctx context.Context, cid id.ExemptionCertificateID) (app.CertificateView, error) {
		return rt.Registrations.AddCertificateVersion(ctx, cid, fromEvidenceDocument(req.Document), deref(req.Note))
	})
}

func (rt *Router) handleScreenExemptionCertificate(w http.ResponseWriter, r *http.Request) {
	var req gen.ExemptionScreeningRequest
	rt.actCertificate(w, r, &req, func(ctx context.Context, cid id.ExemptionCertificateID) (app.CertificateView, error) {
		return rt.Registrations.ScreenCertificate(ctx, cid, fromScreening(req.Screening), deref(req.Note))
	})
}

func (rt *Router) handleRecommendExemptionCertificate(w http.ResponseWriter, r *http.Request) {
	var req gen.ExemptionRecommendationRequest
	rt.actCertificate(w, r, &req, func(ctx context.Context, cid id.ExemptionCertificateID) (app.CertificateView, error) {
		var rec registration.Recommendation
		switch string(req.Recommendation) {
		case "APPROVE":
			rec.Approve = true
		case "REJECT":
		default:
			return app.CertificateView{}, errs.Invalid("recommendation", errs.ReasonInvalidValue, "A recommendation is APPROVE or REJECT.")
		}
		var err error
		if req.EffectiveFrom != nil {
			if rec.EffectiveFrom, err = parseCivilDate("effectiveFrom", *req.EffectiveFrom); err != nil {
				return app.CertificateView{}, err
			}
		}
		if req.EffectiveTo != nil {
			if rec.EffectiveTo, err = parseCivilDate("effectiveTo", *req.EffectiveTo); err != nil {
				return app.CertificateView{}, err
			}
		}
		return rt.Registrations.RecommendCertificate(ctx, cid, rec, req.Rationale)
	})
}

func (rt *Router) handleDecideExemptionCertificate(w http.ResponseWriter, r *http.Request) {
	var req gen.ExemptionDecisionRequest
	rt.actCertificate(w, r, &req, func(ctx context.Context, cid id.ExemptionCertificateID) (app.CertificateView, error) {
		return rt.Registrations.DecideCertificate(ctx, cid, registration.Decision(req.Decision), int(req.ExpectedEvidenceVersion), deref(req.Note))
	})
}

func (rt *Router) handleTransitionExemptionCertificate(w http.ResponseWriter, r *http.Request) {
	var req gen.ExemptionTransitionRequest
	rt.actCertificate(w, r, &req, func(ctx context.Context, cid id.ExemptionCertificateID) (app.CertificateView, error) {
		return rt.Registrations.TransitionCertificate(ctx, cid, registration.CertificateStatus(req.To), deref(req.Reason))
	})
}

func (rt *Router) actCertificate(w http.ResponseWriter, r *http.Request, req any,
	act func(context.Context, id.ExemptionCertificateID) (app.CertificateView, error)) {
	if rt.registrationsUnavailable(w, r) {
		return
	}
	cid, ok := rt.certificateID(w, r)
	if !ok {
		return
	}
	if err := decodeJSON(r, req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := act(r.Context(), cid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toExemptionEvidence(v))
}
