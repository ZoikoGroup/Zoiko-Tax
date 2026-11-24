package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/regchange"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The regulatory change intelligence surface (Engineering Doc 03 WF-05,
// WF-09; Sidebar WF-02). Enumerations cross it as the strings the domain
// names them, so nothing here depends on the generated constant names.

func (rt *Router) regChangesUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.RegulatoryChanges != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"Regulatory change intelligence is not wired in this cell. The request was not applied."))
	return true
}

func toRegulatoryChange(v app.ChangeView) gen.RegulatoryChange {
	c := v.Change
	out := gen.RegulatoryChange{
		ID: c.ID.String(), Jurisdiction: c.Jurisdiction, Kind: gen.RegulatoryChangeKind(c.Kind), Title: c.Title,
		Summary: optStr(c.Summary),
		Source: gen.RegulatoryChangeSource{Authority: c.Source.Authority, Citation: c.Source.Citation, URL: optStr(c.Source.URL),
			Edition: c.Source.Edition, DocumentSha256: c.Source.DocumentSHA256},
		PublishedOn: c.PublishedOn.Format(time.DateOnly), EffectiveFrom: c.EffectiveFrom.Format(time.DateOnly),
		DiscoveredAt: canonical.FormatTime(c.DiscoveredAt), DetectedBy: gen.RegulatoryChangeDetector(c.DetectedBy),
		MonitorRef: optStr(c.MonitorRef), ServiceFamilies: []gen.ServiceFamily{}, TaxTypes: append([]string{}, c.TaxTypes...),
		Retroactive: c.Retroactive(), Status: gen.RegulatoryChangeStatus(c.Status), Version: saturate32(c.Version()),
		SourceKey: c.SourceKey.String(), RelatedChangeIds: []string{}, RecommendationCount: saturate32(v.Recommendations),
		AcceptedRecommendationCount: saturate32(v.Accepted), History: make([]gen.RegulatoryChangeEvent, len(c.History)),
		CreatedAt: canonical.FormatTime(c.CreatedAt), CreatedBy: c.CreatedBy.String(),
	}
	for _, s := range c.ServiceFamilies {
		out.ServiceFamilies = append(out.ServiceFamilies, gen.ServiceFamily(s))
	}
	for _, rid := range v.Related {
		out.RelatedChangeIds = append(out.RelatedChangeIds, rid.String())
	}
	if p, ok := c.Priority(); ok {
		gp := gen.RegulatoryChangePriority(p)
		out.Priority = &gp
	}
	if d, ok := c.Disposition(); ok {
		gd := gen.RegulatoryChangeDisposition(d)
		out.Disposition = &gd
	}
	if h, ok := c.Hold(); ok {
		gh := gen.RegulatoryChangeHoldReason(h)
		out.HoldReason = &gh
	}
	if back, ok := c.ResumesTo(); ok {
		gb := gen.RegulatoryChangeStatus(back)
		out.ResumesTo = &gb
	}
	if !v.CurrentAssessment.IsZero() {
		a, s := v.CurrentAssessment.String(), gen.ImpactSeverity(v.CurrentSeverity)
		out.CurrentAssessmentID, out.CurrentSeverity = &a, &s
	}
	for i, e := range c.History {
		ge := gen.RegulatoryChangeEvent{
			Seq: saturate32(e.Seq), Kind: gen.RegulatoryChangeEventKind(e.Kind), Status: gen.RegulatoryChangeStatus(e.Status),
			Reason: optStr(e.Reason), RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String(),
		}
		if e.Priority != "" {
			p := gen.RegulatoryChangePriority(e.Priority)
			ge.Priority = &p
		}
		if e.Disposition != "" {
			d := gen.RegulatoryChangeDisposition(e.Disposition)
			ge.Disposition = &d
		}
		if e.Hold != "" {
			h := gen.RegulatoryChangeHoldReason(e.Hold)
			ge.HoldReason = &h
		}
		if !e.Assessment.IsZero() {
			a := e.Assessment.String()
			ge.AssessmentID = &a
		}
		out.History[i] = ge
	}
	return out
}

func toImpactAssessment(v app.AssessmentView) gen.ImpactAssessment {
	a := v.Assessment
	out := gen.ImpactAssessment{
		ID: a.ID.String(), ChangeID: a.Change.String(), BundleID: a.BundleID, BundleDigest: a.BundleDigest,
		AsOf: a.AsOf.Format(time.DateOnly), Severity: gen.ImpactSeverity(a.Severity), Retroactive: a.Retroactive,
		Current: v.Current, Items: make([]gen.ImpactItem, len(a.Items)), InputsDigest: a.InputsDigest.String(),
		AssessmentDigest: a.AssessmentDigest.String(), ComponentsRead: saturate32(a.ComponentsRead),
		RegistrationsRead: saturate32(a.RegistrationsRead), ObligationsRead: saturate32(a.ObligationsRead),
		CreatedAt: canonical.FormatTime(a.CreatedAt), CreatedBy: a.CreatedBy.String(),
	}
	for i, it := range a.Items {
		out.Items[i] = gen.ImpactItem{Kind: gen.ImpactItemKind(it.Kind), Ref: it.Ref, Label: it.Label, Jurisdiction: it.Jurisdiction,
			Severity: gen.ImpactSeverity(it.Severity), Basis: it.Basis, Retrospective: it.Retrospective}
	}
	return out
}

func toTargets(ts []regchange.Target) []gen.RecommendationTarget {
	out := make([]gen.RecommendationTarget, len(ts))
	for i, t := range ts {
		out[i] = gen.RecommendationTarget{Kind: gen.ImpactItemKind(t.Kind), Ref: t.Ref}
	}
	return out
}

func fromTargets(ts []gen.RecommendationTarget) []regchange.Target {
	out := make([]regchange.Target, len(ts))
	for i, t := range ts {
		out[i] = regchange.Target{Kind: regchange.ItemKind(t.Kind), Ref: t.Ref}
	}
	return out
}

func toReviewRecommendation(v app.RecommendationView) gen.ReviewRecommendation {
	r := v.Recommendation
	cur, _ := r.Current()
	out := gen.ReviewRecommendation{
		ID: r.ID.String(), ChangeID: r.Change.String(), AssessmentID: r.Assessment.String(),
		AssessmentDigest: r.AssessmentDigest.String(), Stale: v.Stale(), Origin: gen.ReviewRecommendationOrigin(r.Origin),
		Status: gen.ReviewRecommendationStatus(r.Status), Version: saturate32(r.Version()),
		Action: gen.ReviewRecommendationAction(cur.Action), Rationale: cur.Rationale, Targets: toTargets(cur.Targets),
		History: make([]gen.ReviewRecommendationEvent, len(r.History)), CreatedAt: canonical.FormatTime(r.CreatedAt),
		CreatedBy: r.CreatedBy.String(),
	}
	if !v.CurrentAssessment.IsZero() {
		c := v.CurrentAssessment.String()
		out.CurrentAssessmentID = &c
	}
	if p := r.AI; p != nil {
		out.Ai = &gen.RecommendationAiProvenance{UseCase: p.UseCase, ModelProfile: p.ModelProfile, ProviderProfile: p.ProviderProfile,
			PromptProfile: p.PromptProfile, AiTrainVersion: p.AiTrainVersion,
			AuthorityOutcome: gen.RecommendationAuthorityOutcome(p.AuthorityOutcome), InvocationID: optStr(p.InvocationID), Draft: p.Draft}
	}
	if by, ok := r.Recommender(); ok {
		s := by.String()
		out.RecommendedBy = &s
	}
	if e, ok := r.Review(); ok {
		s := e.RecordedBy.String()
		out.ReviewedBy = &s
	}
	if ref, ok := r.HandoffRef(); ok {
		out.HandoffRef = &ref
	}
	for i, e := range r.History {
		ge := gen.ReviewRecommendationEvent{
			Seq: saturate32(e.Seq), Kind: gen.ReviewRecommendationEventKind(e.Kind), Status: gen.ReviewRecommendationStatus(e.Status),
			Reason: optStr(e.Reason), HandoffRef: optStr(e.HandoffRef), RecordedAt: canonical.FormatTime(e.RecordedAt),
			RecordedBy: e.RecordedBy.String(),
		}
		if c := e.Content; c != nil {
			a := gen.ReviewRecommendationAction(c.Action)
			ge.Action, ge.Rationale, ge.Targets = &a, optStr(c.Rationale), toTargets(c.Targets)
		}
		out.History[i] = ge
	}
	return out
}

func (rt *Router) regChangeID(w http.ResponseWriter, r *http.Request) (id.RegulatoryChangeID, bool) {
	cid, err := id.ParseRegulatoryChangeID(r.PathValue("changeId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("changeId", errs.ReasonInvalidValue, "That is not a valid regulatory change identifier."))
		return id.RegulatoryChangeID{}, false
	}
	return cid, true
}

func (rt *Router) recommendationID(w http.ResponseWriter, r *http.Request) (id.ReviewRecommendationID, bool) {
	rid, err := id.ParseReviewRecommendationID(r.PathValue("recommendationId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("recommendationId", errs.ReasonInvalidValue, "That is not a valid review recommendation identifier."))
		return id.ReviewRecommendationID{}, false
	}
	return rid, true
}

func queryChangeID(r *http.Request) (id.RegulatoryChangeID, error) {
	s := r.URL.Query().Get("changeId")
	if s == "" {
		return id.RegulatoryChangeID{}, nil
	}
	cid, err := id.ParseRegulatoryChangeID(s)
	if err != nil {
		return id.RegulatoryChangeID{}, errs.Invalid("changeId", errs.ReasonInvalidValue, "That is not a valid regulatory change identifier.")
	}
	return cid, nil
}

// ---------------------------------------------------------------------------
// changes
// ---------------------------------------------------------------------------

func (rt *Router) handleListRegulatoryChanges(w http.ResponseWriter, r *http.Request) {
	if rt.regChangesUnavailable(w, r) {
		return
	}
	q := r.URL.Query()
	f := regchange.Filter{Status: regchange.Status(q.Get("status")), Kind: regchange.Kind(q.Get("kind")),
		Jurisdiction: strings.ToUpper(q.Get("jurisdiction")), Priority: regchange.Priority(q.Get("priority"))}
	var err error
	switch {
	case f.Status != "" && !f.Status.Valid():
		err = errs.Invalid("status", errs.ReasonInvalidValue, "That is not a regulatory change status.")
	case f.Kind != "" && !f.Kind.Valid():
		err = errs.Invalid("kind", errs.ReasonInvalidValue, "That is not a regulatory change kind.")
	case f.Priority != "" && !f.Priority.Valid():
		err = errs.Invalid("priority", errs.ReasonInvalidValue, "That is not a triage priority.")
	case f.Jurisdiction != "" && !regchange.ValidJurisdiction(f.Jurisdiction):
		err = errs.Invalid("jurisdiction", errs.ReasonInvalidValue, "A jurisdiction is a country, or a country and one subdivision.")
	}
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	got, err := rt.RegulatoryChanges.Changes(r.Context(), f, limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.RegulatoryChangeList{Changes: make([]gen.RegulatoryChange, len(got))}
	for i, v := range got {
		out.Changes[i] = toRegulatoryChange(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleRecordRegulatoryChange(w http.ResponseWriter, r *http.Request) {
	if rt.regChangesUnavailable(w, r) {
		return
	}
	var req gen.RegulatoryChangeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.ChangeInput{
		IntakeKey: r.Header.Get("Idempotency-Key"), Jurisdiction: req.Jurisdiction, Kind: regchange.Kind(req.Kind),
		Title: req.Title, Summary: deref(req.Summary), DetectedBy: regchange.Detector(req.DetectedBy), MonitorRef: deref(req.MonitorRef),
		Source: regchange.Source{Authority: req.Source.Authority, Citation: req.Source.Citation, URL: deref(req.Source.URL),
			Edition: req.Source.Edition, DocumentSHA256: req.Source.DocumentSha256},
		TaxTypes: req.TaxTypes,
	}
	for _, s := range req.ServiceFamilies {
		in.ServiceFamilies = append(in.ServiceFamilies, string(s))
	}
	var err error
	if in.PublishedOn, err = parseCivilDate("publishedOn", req.PublishedOn); err == nil {
		in.EffectiveFrom, err = parseCivilDate("effectiveFrom", req.EffectiveFrom)
	}
	if err == nil && req.DiscoveredAt != nil {
		in.DiscoveredAt, err = parseTimestamp("discoveredAt", *req.DiscoveredAt)
	}
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, created, err := rt.RegulatoryChanges.RecordChange(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toRegulatoryChange(v))
}

func (rt *Router) handleGetRegulatoryChange(w http.ResponseWriter, r *http.Request) {
	if rt.regChangesUnavailable(w, r) {
		return
	}
	cid, ok := rt.regChangeID(w, r)
	if !ok {
		return
	}
	v, err := rt.RegulatoryChanges.Change(r.Context(), cid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toRegulatoryChange(v))
}

func (rt *Router) handleTransitionRegulatoryChange(w http.ResponseWriter, r *http.Request) {
	if rt.regChangesUnavailable(w, r) {
		return
	}
	cid, ok := rt.regChangeID(w, r)
	if !ok {
		return
	}
	var req gen.RegulatoryChangeTransitionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	a := regchange.Act{To: regchange.Status(req.To), Reason: req.Reason, Version: int(req.ExpectedVersion)}
	if req.Priority != nil {
		a.Priority = regchange.Priority(*req.Priority)
	}
	if req.Disposition != nil {
		a.Disposition = regchange.Disposition(*req.Disposition)
	}
	if req.HoldReason != nil {
		a.Hold = regchange.HoldReason(*req.HoldReason)
	}
	v, err := rt.RegulatoryChanges.TransitionChange(r.Context(), cid, a)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toRegulatoryChange(v))
}

func (rt *Router) handleAssessRegulatoryChange(w http.ResponseWriter, r *http.Request) {
	if rt.regChangesUnavailable(w, r) {
		return
	}
	cid, ok := rt.regChangeID(w, r)
	if !ok {
		return
	}
	a, created, err := rt.RegulatoryChanges.AssessChange(r.Context(), cid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toImpactAssessment(app.AssessmentView{Assessment: a, Current: true}))
}

// ---------------------------------------------------------------------------
// assessments
// ---------------------------------------------------------------------------

func (rt *Router) handleListImpactAssessments(w http.ResponseWriter, r *http.Request) {
	if rt.regChangesUnavailable(w, r) {
		return
	}
	cid, err := queryChangeID(r)
	sev := regchange.Severity(r.URL.Query().Get("minSeverity"))
	if err == nil && sev != "" && !sev.Valid() {
		err = errs.Invalid("minSeverity", errs.ReasonInvalidValue, "That is not an impact severity.")
	}
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	got, err := rt.RegulatoryChanges.Assessments(r.Context(), cid, sev, limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.ImpactAssessmentList{Assessments: make([]gen.ImpactAssessment, len(got))}
	for i, v := range got {
		out.Assessments[i] = toImpactAssessment(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleGetImpactAssessment(w http.ResponseWriter, r *http.Request) {
	if rt.regChangesUnavailable(w, r) {
		return
	}
	aid, err := id.ParseImpactAssessmentID(r.PathValue("assessmentId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("assessmentId", errs.ReasonInvalidValue, "That is not a valid impact assessment identifier."))
		return
	}
	v, err := rt.RegulatoryChanges.Assessment(r.Context(), aid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toImpactAssessment(v))
}

// ---------------------------------------------------------------------------
// recommendations
// ---------------------------------------------------------------------------

func (rt *Router) handleListReviewRecommendations(w http.ResponseWriter, r *http.Request) {
	if rt.regChangesUnavailable(w, r) {
		return
	}
	q := r.URL.Query()
	f := regchange.RecFilter{Status: regchange.RecStatus(q.Get("status")), Origin: regchange.Origin(q.Get("origin"))}
	var err error
	f.Change, err = queryChangeID(r)
	switch {
	case err != nil:
	case f.Status != "" && !f.Status.Valid():
		err = errs.Invalid("status", errs.ReasonInvalidValue, "That is not a recommendation status.")
	case f.Origin != "" && f.Origin != regchange.OriginHuman && f.Origin != regchange.OriginAIAssisted:
		err = errs.Invalid("origin", errs.ReasonInvalidValue, "A recommendation's origin is HUMAN or AI_ASSISTED.")
	}
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	got, err := rt.RegulatoryChanges.Recommendations(r.Context(), f, limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.ReviewRecommendationList{Recommendations: make([]gen.ReviewRecommendation, len(got))}
	for i, v := range got {
		out.Recommendations[i] = toReviewRecommendation(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleDraftReviewRecommendation(w http.ResponseWriter, r *http.Request) {
	if rt.regChangesUnavailable(w, r) {
		return
	}
	var req gen.ReviewRecommendationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	aid, err := id.ParseImpactAssessmentID(req.AssessmentID)
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("assessmentId", errs.ReasonInvalidValue, "That is not a valid impact assessment identifier."))
		return
	}
	in := app.RecommendationInput{
		IntakeKey: r.Header.Get("Idempotency-Key"), Assessment: aid, Origin: regchange.Origin(req.Origin),
		Content: regchange.Content{Action: regchange.Action(req.Action), Rationale: req.Rationale, Targets: fromTargets(req.Targets)},
	}
	if p := req.Ai; p != nil {
		in.AI = &regchange.AIProvenance{UseCase: p.UseCase, ModelProfile: p.ModelProfile, ProviderProfile: p.ProviderProfile,
			PromptProfile: p.PromptProfile, AiTrainVersion: p.AiTrainVersion, AuthorityOutcome: regchange.AuthorityOutcome(p.AuthorityOutcome),
			InvocationID: deref(p.InvocationID), Draft: p.Draft}
	}
	v, created, err := rt.RegulatoryChanges.DraftRecommendation(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toReviewRecommendation(v))
}

func (rt *Router) handleGetReviewRecommendation(w http.ResponseWriter, r *http.Request) {
	if rt.regChangesUnavailable(w, r) {
		return
	}
	rid, ok := rt.recommendationID(w, r)
	if !ok {
		return
	}
	v, err := rt.RegulatoryChanges.Recommendation(r.Context(), rid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toReviewRecommendation(v))
}

func (rt *Router) actRecommendation(w http.ResponseWriter, r *http.Request, req any,
	act func(context.Context, id.ReviewRecommendationID) (app.RecommendationView, error)) {
	if rt.regChangesUnavailable(w, r) {
		return
	}
	rid, ok := rt.recommendationID(w, r)
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
	writeJSON(w, r, rt.log, http.StatusOK, toReviewRecommendation(v))
}

func (rt *Router) handleReviseReviewRecommendation(w http.ResponseWriter, r *http.Request) {
	var req gen.ReviewRecommendationRevisionRequest
	rt.actRecommendation(w, r, &req, func(ctx context.Context, rid id.ReviewRecommendationID) (app.RecommendationView, error) {
		c := regchange.Content{Action: regchange.Action(req.Action), Rationale: req.Rationale, Targets: fromTargets(req.Targets)}
		return rt.RegulatoryChanges.ReviseRecommendation(ctx, rid, c, int(req.ExpectedVersion))
	})
}

func (rt *Router) handleRecommendReviewRecommendation(w http.ResponseWriter, r *http.Request) {
	var req gen.ReviewRecommendationActRequest
	rt.actRecommendation(w, r, &req, func(ctx context.Context, rid id.ReviewRecommendationID) (app.RecommendationView, error) {
		return rt.RegulatoryChanges.RecommendRecommendation(ctx, rid, req.Reason, int(req.ExpectedVersion))
	})
}

func (rt *Router) handleReviewReviewRecommendation(w http.ResponseWriter, r *http.Request) {
	var req gen.ReviewRecommendationReviewRequest
	rt.actRecommendation(w, r, &req, func(ctx context.Context, rid id.ReviewRecommendationID) (app.RecommendationView, error) {
		return rt.RegulatoryChanges.ReviewRecommendation(ctx, rid, regchange.Decision(req.Decision), req.Reason, int(req.ExpectedVersion))
	})
}

func (rt *Router) handleWithdrawReviewRecommendation(w http.ResponseWriter, r *http.Request) {
	var req gen.ReviewRecommendationActRequest
	rt.actRecommendation(w, r, &req, func(ctx context.Context, rid id.ReviewRecommendationID) (app.RecommendationView, error) {
		return rt.RegulatoryChanges.WithdrawRecommendation(ctx, rid, req.Reason, int(req.ExpectedVersion))
	})
}
