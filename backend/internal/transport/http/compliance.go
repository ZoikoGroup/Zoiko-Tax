package http

import (
	"net/http"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/compliance"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The compliance package surface (ZTAX-COMP-001 §32, W4 lane M).

func (rt *Router) complianceUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Compliance != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"Compliance operations are not wired in this cell. The request was not applied."))
	return true
}

func optTimestamp(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := canonical.FormatTime(t)
	return &s
}

func toCompliancePackage(v app.PackageView) gen.CompliancePackage {
	p := v.Package
	out := gen.CompliancePackage{
		ID: p.ID.String(), ObligationID: p.Obligation.String(), ObligationStatus: gen.ObligationStatus(v.ObligationStatus),
		LegalEntityID: p.LegalEntity.String(), ObligationType: p.ObligationType, Jurisdiction: p.Jurisdiction,
		// Civil dates in the legal calendar; formatted there, never in UTC.
		PeriodStart: p.PeriodStart.Format(time.DateOnly), PeriodEnd: p.PeriodEnd.Format(time.DateOnly),
		Mode: gen.ComplianceMode(p.Mode), Channel: gen.ComplianceChannel(p.Channel),
		FilingCountry: p.FilingCountry, FilingAuthority: p.FilingAuthority,
		StatutoryDue: canonical.FormatTime(p.StatutoryDue), Timezone: p.Timezone,
		PrepareDue: optTimestamp(p.PrepareDue), ReviewDue: optTimestamp(p.ReviewDue), SubmitTarget: optTimestamp(p.SubmitTarget),
		Status: gen.ComplianceStatus(p.Status), Priority: gen.CompliancePriority(v.Priority),
		Overdue: gen.CompliancePackage_Overdue{Statutory: v.Overdue.Statutory, Internal: v.Overdue.Internal},
		Ready:   v.Ready, Readiness: make([]gen.ComplianceReadiness, len(v.Readiness)),
		Gate:      gen.ComplianceGate{Allowed: v.Gate.Allowed},
		CreatedAt: canonical.FormatTime(p.CreatedAt), CreatedBy: p.CreatedBy.String(),
		History: make([]gen.ComplianceStep, len(p.History)),
	}
	if !p.Responsibility.IsZero() {
		r := p.Responsibility.String()
		out.ResponsibilityDecisionID = &r
	}
	if !v.Gate.Allowed {
		out.Gate.Reason, out.Gate.Detail = optStr(string(v.Gate.Reason)), optStr(v.Gate.Detail)
	}
	for i, r := range v.Readiness {
		out.Readiness[i] = gen.ComplianceReadiness{Dimension: gen.ComplianceReadinessDimension(r.Dimension), Ready: r.Ready, Detail: r.Detail}
	}
	for i, s := range p.History {
		out.History[i] = toComplianceStep(s)
	}
	return out
}

func toComplianceStep(s compliance.Step) gen.ComplianceStep {
	out := gen.ComplianceStep{
		Seq: saturate32(s.Seq), Kind: gen.ComplianceStepKind(s.Kind), Status: gen.ComplianceStatus(s.Status),
		AuthorityReference: optStr(s.AuthorityRef), Evidence: optStr(s.Evidence), Note: optStr(s.Note),
		RecordedAt: canonical.FormatTime(s.RecordedAt), RecordedBy: s.RecordedBy.String(),
	}
	if s.Version > 0 {
		v := saturate32(s.Version)
		out.Version = &v
	}
	if !s.PayloadHash.IsZero() {
		h := s.PayloadHash.String()
		out.PayloadHash = &h
	}
	if s.ControlTotal != nil {
		amount, currency := s.ControlTotal.CanonicalString(), string(s.ControlTotal.Currency())
		out.ControlTotal, out.Currency = &amount, &currency
	}
	if s.Review != "" {
		d := gen.ComplianceReviewDecision(s.Review)
		out.Review = &d
	}
	if !s.Submission.IsZero() {
		a := s.Submission.String()
		out.SubmissionID = &a
	}
	if s.Channel != "" {
		c := gen.ComplianceChannel(s.Channel)
		out.Channel = &c
	}
	if s.Acknowledgement != "" {
		a := gen.ComplianceAcknowledgement(s.Acknowledgement)
		out.Acknowledgement = &a
	}
	if s.Resolution != "" {
		r := gen.ComplianceResolution(s.Resolution)
		out.Resolution = &r
	}
	return out
}

func (rt *Router) packageID(w http.ResponseWriter, r *http.Request) (id.CompliancePackageID, bool) {
	packageID, err := id.ParseCompliancePackageID(r.PathValue("packageId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("packageId", errs.ReasonInvalidValue,
			"That is not a valid compliance package identifier."))
		return id.CompliancePackageID{}, false
	}
	return packageID, true
}

// optTimestampIn parses an optional timestamp field.
func optTimestampIn(field string, s *string) (time.Time, error) {
	if s == nil {
		return time.Time{}, nil
	}
	return parseTimestamp(field, *s)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (rt *Router) handleListCompliancePackages(w http.ResponseWriter, r *http.Request) {
	if rt.complianceUnavailable(w, r) {
		return
	}
	status := compliance.Status(r.URL.Query().Get("status"))
	if status != "" && !gen.ComplianceStatus(status).Valid() {
		writeProblem(w, r, rt.log, errs.Invalid("status", errs.ReasonInvalidValue, "That is not a compliance status."))
		return
	}
	views, err := rt.Compliance.Packages(r.Context(), status, limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.CompliancePackageList{Packages: make([]gen.CompliancePackage, len(views))}
	for i, v := range views {
		out.Packages[i] = toCompliancePackage(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleCreateCompliancePackage(w http.ResponseWriter, r *http.Request) {
	if rt.complianceUnavailable(w, r) {
		return
	}
	var req gen.CreateCompliancePackageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	obligationID, err := id.ParseObligationID(req.ObligationID)
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("obligationId", errs.ReasonInvalidValue, "That is not a valid obligation identifier."))
		return
	}
	if !req.Mode.Valid() || !req.Channel.Valid() {
		writeProblem(w, r, rt.log, errs.Invalid("mode", errs.ReasonInvalidValue, "That is not an operating mode and a submission channel this service knows."))
		return
	}
	in := app.CreatePackageInput{Obligation: obligationID, Mode: compliance.Mode(req.Mode), Channel: compliance.Channel(req.Channel),
		FilingCountry: req.FilingCountry, FilingAuthority: req.FilingAuthority}
	for _, t := range []struct {
		field string
		src   *string
		dst   *time.Time
	}{{"prepareDue", req.PrepareDue, &in.PrepareDue}, {"reviewDue", req.ReviewDue, &in.ReviewDue}, {"submitTarget", req.SubmitTarget, &in.SubmitTarget}} {
		if *t.dst, err = optTimestampIn(t.field, t.src); err != nil {
			writeProblem(w, r, rt.log, err)
			return
		}
	}
	v, created, err := rt.Compliance.CreatePackage(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toCompliancePackage(v))
}

func (rt *Router) handleGetCompliancePackage(w http.ResponseWriter, r *http.Request) {
	if rt.complianceUnavailable(w, r) {
		return
	}
	packageID, ok := rt.packageID(w, r)
	if !ok {
		return
	}
	v, err := rt.Compliance.Package(r.Context(), packageID)
	rt.writePackage(w, r, v, err)
}

func (rt *Router) handlePrepareCompliancePackage(w http.ResponseWriter, r *http.Request) {
	if rt.complianceUnavailable(w, r) {
		return
	}
	packageID, ok := rt.packageID(w, r)
	if !ok {
		return
	}
	var req gen.PreparationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Compliance.Prepare(r.Context(), packageID, deref(req.Note))
	rt.writePackage(w, r, v, err)
}

func (rt *Router) handleReviewCompliancePackage(w http.ResponseWriter, r *http.Request) {
	if rt.complianceUnavailable(w, r) {
		return
	}
	packageID, ok := rt.packageID(w, r)
	if !ok {
		return
	}
	var req gen.ComplianceReviewRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Compliance.Review(r.Context(), packageID, int(req.Version), compliance.ReviewDecision(req.Decision), deref(req.Note))
	rt.writePackage(w, r, v, err)
}

func (rt *Router) handleApproveCompliancePackage(w http.ResponseWriter, r *http.Request) {
	if rt.complianceUnavailable(w, r) {
		return
	}
	packageID, ok := rt.packageID(w, r)
	if !ok {
		return
	}
	var req gen.ComplianceApprovalRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	hash, err := canonical.ParseDigest(req.PayloadHash)
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("payloadHash", errs.ReasonInvalidValue, "That is not a canonical digest."))
		return
	}
	v, err := rt.Compliance.Approve(r.Context(), packageID, int(req.Version), hash, deref(req.Note))
	rt.writePackage(w, r, v, err)
}

func (rt *Router) handleSubmitCompliancePackage(w http.ResponseWriter, r *http.Request) {
	if rt.complianceUnavailable(w, r) {
		return
	}
	packageID, ok := rt.packageID(w, r)
	if !ok {
		return
	}
	var req gen.ComplianceSubmissionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Compliance.Submit(r.Context(), packageID, deref(req.Note))
	rt.writePackage(w, r, v, err)
}

func (rt *Router) handleAcknowledgeCompliancePackage(w http.ResponseWriter, r *http.Request) {
	if rt.complianceUnavailable(w, r) {
		return
	}
	packageID, ok := rt.packageID(w, r)
	if !ok {
		return
	}
	var req gen.ComplianceAcknowledgementRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Compliance.Acknowledge(r.Context(), packageID, app.AcknowledgeInput{
		Acknowledgement: compliance.Acknowledgement(req.Acknowledgement), AuthorityRef: deref(req.AuthorityReference),
		Evidence: req.Evidence, Note: deref(req.Note),
	})
	rt.writePackage(w, r, v, err)
}

func (rt *Router) handleResolveCompliancePackage(w http.ResponseWriter, r *http.Request) {
	if rt.complianceUnavailable(w, r) {
		return
	}
	packageID, ok := rt.packageID(w, r)
	if !ok {
		return
	}
	var req gen.ComplianceResolutionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Compliance.Resolve(r.Context(), packageID, compliance.Resolution(req.Resolution), deref(req.AuthorityReference), req.Evidence)
	rt.writePackage(w, r, v, err)
}

func (rt *Router) handleCancelCompliancePackage(w http.ResponseWriter, r *http.Request) {
	if rt.complianceUnavailable(w, r) {
		return
	}
	packageID, ok := rt.packageID(w, r)
	if !ok {
		return
	}
	var req gen.ComplianceCancellationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Compliance.Cancel(r.Context(), packageID, req.Reason)
	rt.writePackage(w, r, v, err)
}

func (rt *Router) writePackage(w http.ResponseWriter, r *http.Request, v app.PackageView, err error) {
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toCompliancePackage(v))
}
