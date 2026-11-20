package http

import (
	"net/http"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ctc"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The e-invoicing / CTC surface (Engineering Doc 21, G-030): country
// applicability, format validation and authority submissions. Amounts cross
// it as the canonical decimal strings the fiscal documents committed;
// nothing here does arithmetic on one.

func (rt *Router) ctcUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.CTC != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"E-invoicing / CTC is not wired in this cell. The request was not applied."))
	return true
}

func civil(t time.Time) string { return t.UTC().Format(time.DateOnly) }

func optCivil(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := civil(t)
	return &s
}

func optInt32(n int) *int32 {
	if n == 0 {
		return nil
	}
	v := saturate32(n)
	return &v
}

func toCTCTerms(t ctc.Terms) gen.CTCTerms {
	return gen.CTCTerms{
		Duty: gen.CTCDuty(t.Duty), Model: gen.CTCModel(t.Model), Profile: gen.CTCProfile(t.Profile),
		FormatVersion: optStr(t.FormatVersion), Authority: t.Authority, LegalSource: t.LegalSource,
		SourceAsOf: civil(t.SourceAsOf), EffectiveFrom: civil(t.EffectiveFrom), EffectiveTo: optCivil(t.EffectiveTo),
		Conditions: optStr(t.Conditions),
	}
}

func parseCivil(field, value string, required bool) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" && !required {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.DateOnly, value)
	if err != nil {
		return time.Time{}, errs.Invalid(field, errs.ReasonInvalidValue, "A date is a calendar date, YYYY-MM-DD.")
	}
	return t, nil
}

func fromCTCTerms(t gen.CTCTerms) (ctc.Terms, error) {
	out := ctc.Terms{Duty: ctc.Duty(t.Duty), Model: ctc.Model(t.Model), Profile: ctc.Profile(t.Profile),
		Authority: t.Authority, LegalSource: t.LegalSource}
	if t.FormatVersion != nil {
		out.FormatVersion = *t.FormatVersion
	}
	if t.Conditions != nil {
		out.Conditions = *t.Conditions
	}
	var err error
	if out.SourceAsOf, err = parseCivil("terms.sourceAsOf", t.SourceAsOf, true); err != nil {
		return ctc.Terms{}, err
	}
	if out.EffectiveFrom, err = parseCivil("terms.effectiveFrom", t.EffectiveFrom, true); err != nil {
		return ctc.Terms{}, err
	}
	if t.EffectiveTo != nil {
		if out.EffectiveTo, err = parseCivil("terms.effectiveTo", *t.EffectiveTo, false); err != nil {
			return ctc.Terms{}, err
		}
	}
	return out, nil
}

func toCTCApplicability(a ctc.Applicability) gen.CTCApplicability {
	cur := a.Current()
	out := gen.CTCApplicability{
		ID: a.ID.String(), LegalEntityID: a.LegalEntity.String(), Country: a.Country,
		DocumentType: gen.CTCDocumentType(a.DocumentType), BuyerKind: gen.CTCBuyerKind(a.BuyerKind),
		Status: gen.CTCApplicabilityStatus(a.Status), CurrentVersion: saturate32(cur.Number), Terms: toCTCTerms(cur.Terms),
		Versions: make([]gen.CTCTermsVersion, len(a.Versions)), RequiredGates: []gen.CTCGate{}, MissingGates: []gen.CTCGate{},
		Signoffs: []gen.CTCGateSignoff{}, Evidence: []gen.EvidenceDocument{}, History: make([]gen.CTCApplicabilityEvent, len(a.History)),
		CreatedAt: canonical.FormatTime(a.CreatedAt), CreatedBy: a.CreatedBy.String(),
	}
	for i, v := range a.Versions {
		out.Versions[i] = gen.CTCTermsVersion{Version: saturate32(v.Number), Terms: toCTCTerms(v.Terms), Digest: v.Digest.String(),
			RecordedAt: canonical.FormatTime(v.RecordedAt), RecordedBy: v.RecordedBy.String()}
	}
	for _, g := range ctc.RequiredGates(cur.Terms.Duty) {
		out.RequiredGates = append(out.RequiredGates, gen.CTCGate(g))
	}
	for _, g := range a.MissingGates() {
		out.MissingGates = append(out.MissingGates, gen.CTCGate(g))
	}
	signed := a.Signoffs(cur.Number)
	for _, g := range ctc.Gates {
		if e, ok := signed[g]; ok {
			out.Signoffs = append(out.Signoffs, gen.CTCGateSignoff{Gate: gen.CTCGate(g), Version: saturate32(e.Version),
				SignoffRef: e.SignoffRef, Note: optStr(e.Reason), SignedAt: canonical.FormatTime(e.RecordedAt), SignedBy: e.RecordedBy.String()})
		}
	}
	for _, e := range a.Evidence() {
		out.Evidence = append(out.Evidence, toEvidenceDocument(*e.Evidence))
	}
	for i, e := range a.History {
		ge := gen.CTCApplicabilityEvent{Seq: saturate32(e.Seq), Kind: gen.CTCApplicabilityEventKind(e.Kind),
			Status: gen.CTCApplicabilityStatus(e.Status), Version: optInt32(e.Version), SignoffRef: optStr(e.SignoffRef),
			Reason: optStr(e.Reason), RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String()}
		if e.Gate != "" {
			g := gen.CTCGate(e.Gate)
			ge.Gate = &g
		}
		if e.Evidence != nil {
			d := toEvidenceDocument(*e.Evidence)
			ge.Evidence = &d
		}
		out.History[i] = ge
	}
	return out
}

func toCTCValidation(v ctc.Validation) gen.CTCValidation {
	out := gen.CTCValidation{
		ID: v.ID.String(), DocumentID: v.Document.String(), ApplicabilityID: v.Applicability.String(),
		ApplicabilityVersion: saturate32(v.ApplicabilityVersion), Profile: gen.CTCProfile(v.Profile),
		FormatVersion: optStr(v.FormatVersion), Ruleset: v.Ruleset, InputDigest: v.InputDigest.String(),
		Outcome: gen.CTCValidationOutcome(v.Outcome), FatalFailures: saturate32(v.Failures()), SchemaValidated: false,
		Results: make([]gen.CTCRuleResult, len(v.Results)), Digest: v.Digest.String(),
		ValidatedAt: canonical.FormatTime(v.ValidatedAt), ValidatedBy: v.ValidatedBy.String(),
	}
	for i, res := range v.Results {
		gr := gen.CTCRuleResult{RuleID: res.RuleID, Stage: gen.CTCRuleStage(res.Stage), Severity: gen.CTCRuleSeverity(res.Severity),
			Outcome: gen.CTCRuleOutcome(res.Outcome), BusinessTerm: optStr(res.BusinessTerm), Detail: optStr(res.Detail)}
		if res.Reason != "" {
			code := string(res.Reason)
			gr.ReasonCode = &code
		}
		out.Results[i] = gr
	}
	return out
}

func toCTCSubmission(v app.CTCSubmissionView) gen.CTCSubmission {
	s := v.Submission
	out := gen.CTCSubmission{
		ID: s.ID.String(), DocumentID: s.Document.String(), DocumentNumber: optStr(s.DocumentNumber),
		DocumentType: gen.CTCDocumentType(s.DocumentType), LegalEntityID: s.LegalEntity.String(), Country: s.Country,
		BuyerKind: gen.CTCBuyerKind(s.BuyerKind), ApplicabilityID: s.Applicability.String(),
		ApplicabilityVersion: saturate32(s.ApplicabilityVersion), Model: gen.CTCModel(s.Model), Profile: gen.CTCProfile(s.Profile),
		FormatVersion: optStr(s.FormatVersion), ValidationID: s.Validation.String(), Currency: string(s.Currency),
		Gross: s.Gross.CanonicalString(), PayloadDigest: s.PayloadDigest.String(), ExternalRef: s.ExternalRef(),
		PreparedAt: canonical.FormatTime(s.PreparedAt), PreparedBy: s.PreparedBy.String(),
		Status: gen.CTCSubmissionStatus(s.Status), History: make([]gen.CTCSubmissionEvent, len(s.History)),
	}
	for i, e := range s.History {
		ge := gen.CTCSubmissionEvent{Seq: saturate32(e.Seq), Kind: gen.CTCSubmissionEventKind(e.Kind),
			Status: gen.CTCSubmissionStatus(e.Status), TransportRef: optStr(e.TransportRef), AuthorityRef: optStr(e.AuthorityRef),
			AuthorityCode: optStr(e.AuthorityCode), EvidenceSha256: optStr(e.EvidenceSHA256), Detail: optStr(e.Detail),
			RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String()}
		if !e.Attempt.IsZero() {
			a := e.Attempt.String()
			ge.AttemptID = &a
		}
		out.History[i] = ge
	}
	if t := v.Correction; t != nil {
		c := gen.CTCCorrectionTask{DocumentID: t.Document.String(), Open: t.Open(), OpenedAt: canonical.FormatTime(t.OpenedAt),
			AuthorityCode: optStr(t.AuthorityCode), Reason: optStr(t.Reason), CorrectedBy: make([]string, len(t.CorrectedBy))}
		for i, d := range t.CorrectedBy {
			c.CorrectedBy[i] = d.String()
		}
		out.Correction = &c
	}
	return out
}

func created(ok bool) int {
	if ok {
		return http.StatusCreated
	}
	return http.StatusOK
}

func (rt *Router) ctcApplicabilityID(w http.ResponseWriter, r *http.Request) (id.CTCApplicabilityID, bool) {
	a, err := id.ParseCTCApplicabilityID(r.PathValue("applicabilityId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("applicabilityId", errs.ReasonInvalidValue, "That is not a valid applicability record identifier."))
		return id.CTCApplicabilityID{}, false
	}
	return a, true
}

func (rt *Router) ctcSubmissionID(w http.ResponseWriter, r *http.Request) (id.CTCSubmissionID, bool) {
	s, err := id.ParseCTCSubmissionID(r.PathValue("submissionId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("submissionId", errs.ReasonInvalidValue, "That is not a valid submission identifier."))
		return id.CTCSubmissionID{}, false
	}
	return s, true
}

// optionalID parses an optional identifier query or body value.
func optionalID[T any](field, value string, parse func(string) (T, error)) (T, error) {
	var zero T
	if strings.TrimSpace(value) == "" {
		return zero, nil
	}
	v, err := parse(value)
	if err != nil {
		return zero, errs.Invalid(field, errs.ReasonInvalidValue, "That is not a valid identifier.")
	}
	return v, nil
}

func (rt *Router) handleListCTCApplicability(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	q := r.URL.Query()
	le, err := optionalID("legalEntityId", q.Get("legalEntityId"), id.ParseLegalEntityID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	f := ctc.Filter{LegalEntity: le, Country: q.Get("country"), Status: ctc.Status(q.Get("status"))}
	if f.Status != "" && !f.Status.Valid() {
		writeProblem(w, r, rt.log, errs.Invalid("status", errs.ReasonInvalidValue, "That is not an applicability status."))
		return
	}
	got, err := rt.CTC.Applicabilities(r.Context(), f)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.CTCApplicabilityList{Records: make([]gen.CTCApplicability, len(got))}
	for i, a := range got {
		out.Records[i] = toCTCApplicability(a)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleCreateCTCApplicability(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	var req gen.CTCApplicabilityRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	terms, err := fromCTCTerms(req.Terms)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.CTCApplicabilityInput{Country: req.Country, DocumentType: document.Type(req.DocumentType),
		BuyerKind: ctc.BuyerKind(req.BuyerKind), Terms: terms}
	if req.LegalEntityID != nil {
		if in.LegalEntity, err = optionalID("legalEntityId", *req.LegalEntityID, id.ParseLegalEntityID); err != nil {
			writeProblem(w, r, rt.log, err)
			return
		}
	}
	a, err := rt.CTC.CreateApplicability(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toCTCApplicability(a))
}

func (rt *Router) handleGetCTCApplicability(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	aid, ok := rt.ctcApplicabilityID(w, r)
	if !ok {
		return
	}
	a, err := rt.CTC.Applicability(r.Context(), aid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toCTCApplicability(a))
}

func (rt *Router) handleReviseCTCApplicability(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	aid, ok := rt.ctcApplicabilityID(w, r)
	if !ok {
		return
	}
	var req gen.CTCTerms
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	terms, err := fromCTCTerms(req)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	a, ok, err := rt.CTC.ReviseApplicability(r.Context(), aid, terms)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, created(ok), toCTCApplicability(a))
}

func (rt *Router) handleAddCTCApplicabilityEvidence(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	aid, ok := rt.ctcApplicabilityID(w, r)
	if !ok {
		return
	}
	var req gen.EvidenceDocument
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	a, ok, err := rt.CTC.AddApplicabilityEvidence(r.Context(), aid,
		ctc.Attachment{SHA256: req.Sha256, Filename: req.Filename, MediaType: req.MediaType, Bytes: req.Bytes})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, created(ok), toCTCApplicability(a))
}

func (rt *Router) handleSignCTCApplicabilityGate(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	aid, ok := rt.ctcApplicabilityID(w, r)
	if !ok {
		return
	}
	var req gen.CTCGateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.CTCGateInput{Gate: ctc.Gate(req.Gate), Version: int(req.Version), SignoffRef: req.SignoffRef}
	if req.Note != nil {
		in.Note = *req.Note
	}
	a, ok, err := rt.CTC.SignApplicabilityGate(r.Context(), aid, in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, created(ok), toCTCApplicability(a))
}

func (rt *Router) handleTransitionCTCApplicability(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	aid, ok := rt.ctcApplicabilityID(w, r)
	if !ok {
		return
	}
	var req gen.CTCTransitionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.CTCTransitionInput{Action: app.CTCAction(req.Action)}
	if req.Version != nil {
		in.Version = int(*req.Version)
	}
	if req.Reason != nil {
		in.Reason = *req.Reason
	}
	a, err := rt.CTC.TransitionApplicability(r.Context(), aid, in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toCTCApplicability(a))
}

func (rt *Router) handleResolveCTCApplicability(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	q := r.URL.Query()
	le, err := optionalID("legalEntityId", q.Get("legalEntityId"), id.ParseLegalEntityID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	date, err := parseCivil("date", q.Get("date"), true)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	res, err := rt.CTC.Resolve(r.Context(), ctc.Cell{LegalEntity: le, Country: q.Get("country"),
		DocumentType: document.Type(q.Get("documentType")), BuyerKind: ctc.BuyerKind(q.Get("buyerKind"))}, date)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.CTCResolution{
		LegalEntityID: res.Cell.LegalEntity.String(), Country: res.Cell.Country, DocumentType: gen.CTCDocumentType(res.Cell.DocumentType),
		BuyerKind: gen.CTCBuyerKind(res.Cell.BuyerKind), Date: civil(res.Date), Duty: gen.CTCResolvedDuty(res.Duty),
		DispatchPermitted: res.Duty.Dispatches(), Detail: res.Detail,
	}
	if res.Reason != "" {
		code := string(res.Reason)
		out.ReasonCode = &code
	}
	if a := res.Record; a != nil {
		aid, status, t := a.ID.String(), gen.CTCApplicabilityStatus(a.Status), toCTCTerms(a.Current().Terms)
		out.ApplicabilityID, out.ApplicabilityStatus, out.Terms = &aid, &status, &t
		out.ApplicabilityVersion = optInt32(a.Current().Number)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleListCTCValidations(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	q := r.URL.Query()
	doc, err := optionalID("documentId", q.Get("documentId"), id.ParseFiscalDocumentID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	aid, err := optionalID("applicabilityId", q.Get("applicabilityId"), id.ParseCTCApplicabilityID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	got, err := rt.CTC.Validations(r.Context(), ctc.ValidationFilter{Document: doc, Applicability: aid})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.CTCValidationList{Validations: make([]gen.CTCValidation, len(got))}
	for i, v := range got {
		out.Validations[i] = toCTCValidation(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) documentAndRecord(w http.ResponseWriter, r *http.Request, documentID, applicabilityID string) (id.FiscalDocumentID, id.CTCApplicabilityID, bool) {
	doc, err := id.ParseFiscalDocumentID(documentID)
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("documentId", errs.ReasonInvalidValue, "That is not a valid fiscal document identifier."))
		return id.FiscalDocumentID{}, id.CTCApplicabilityID{}, false
	}
	aid, err := id.ParseCTCApplicabilityID(applicabilityID)
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("applicabilityId", errs.ReasonInvalidValue, "That is not a valid applicability record identifier."))
		return id.FiscalDocumentID{}, id.CTCApplicabilityID{}, false
	}
	return doc, aid, true
}

func (rt *Router) handleValidateCTCDocument(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	var req gen.CTCValidationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	doc, aid, ok := rt.documentAndRecord(w, r, req.DocumentID, req.ApplicabilityID)
	if !ok {
		return
	}
	v, ok, err := rt.CTC.Validate(r.Context(), doc, aid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, created(ok), toCTCValidation(v))
}

func (rt *Router) handleGetCTCValidation(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	vid, err := id.ParseCTCValidationID(r.PathValue("validationId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("validationId", errs.ReasonInvalidValue, "That is not a valid validation report identifier."))
		return
	}
	v, err := rt.CTC.Validation(r.Context(), vid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toCTCValidation(v))
}

func (rt *Router) handleListCTCSubmissions(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	q := r.URL.Query()
	doc, err := optionalID("documentId", q.Get("documentId"), id.ParseFiscalDocumentID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	aid, err := optionalID("applicabilityId", q.Get("applicabilityId"), id.ParseCTCApplicabilityID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	got, err := rt.CTC.Submissions(r.Context(), ctc.SubmissionFilter{Document: doc, Applicability: aid,
		Status: ctc.SubmissionStatus(q.Get("status"))})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.CTCSubmissionList{Submissions: make([]gen.CTCSubmission, len(got))}
	for i, v := range got {
		out.Submissions[i] = toCTCSubmission(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handlePrepareCTCSubmission(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	var req gen.CTCSubmissionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	doc, aid, ok := rt.documentAndRecord(w, r, req.DocumentID, req.ApplicabilityID)
	if !ok {
		return
	}
	v, ok, err := rt.CTC.PrepareSubmission(r.Context(), doc, aid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, created(ok), toCTCSubmission(v))
}

func (rt *Router) handleGetCTCSubmission(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	sid, ok := rt.ctcSubmissionID(w, r)
	if !ok {
		return
	}
	v, err := rt.CTC.Submission(r.Context(), sid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toCTCSubmission(v))
}

func (rt *Router) handleDispatchCTCSubmission(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	sid, ok := rt.ctcSubmissionID(w, r)
	if !ok {
		return
	}
	var req gen.CTCDispatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, ok, err := rt.CTC.Dispatch(r.Context(), sid, req.PayloadDigest)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, created(ok), toCTCSubmission(v))
}

func (rt *Router) handleRecordCTCResponse(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	sid, ok := rt.ctcSubmissionID(w, r)
	if !ok {
		return
	}
	var req gen.CTCResponseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	attempt, err := id.ParseCTCAttemptID(req.AttemptID)
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("attemptId", errs.ReasonInvalidValue, "That is not a valid dispatch attempt identifier."))
		return
	}
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	v, ok, err := rt.CTC.Respond(r.Context(), sid, ctc.Response{
		Kind: ctc.SubmissionEventKind(req.Kind), Attempt: attempt, TransportRef: deref(req.TransportRef),
		AuthorityRef: deref(req.AuthorityRef), AuthorityCode: deref(req.AuthorityCode), EvidenceSHA256: deref(req.EvidenceSha256),
		Detail: deref(req.Detail),
	})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, created(ok), toCTCSubmission(v))
}

func (rt *Router) handleWithdrawCTCSubmission(w http.ResponseWriter, r *http.Request) {
	if rt.ctcUnavailable(w, r) {
		return
	}
	sid, ok := rt.ctcSubmissionID(w, r)
	if !ok {
		return
	}
	var req gen.CTCWithdrawalRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.CTC.Withdraw(r.Context(), sid, req.Reason)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toCTCSubmission(v))
}
