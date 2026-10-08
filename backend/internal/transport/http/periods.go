package http

import (
	"net/http"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The subledger's legal periods (ZTAX-FIN-001 §20–§22).

func toPeriod(v app.PeriodView) gen.SubledgerPeriod {
	out := gen.SubledgerPeriod{
		Period: v.Period, LegalEntityID: v.LegalEntity.String(), State: gen.PeriodState(v.State),
		History: make([]gen.PeriodEvent, len(v.History)), Intact: v.Intact,
	}
	for i, e := range v.History {
		ge := gen.PeriodEvent{Seq: saturate32(e.Seq), State: gen.PeriodState(e.State), RecordedAt: canonical.FormatTime(e.RecordedAt)}
		if e.Reason != "" {
			r := e.Reason
			ge.Reason = &r
		}
		if !e.RecordedBy.IsZero() {
			u := e.RecordedBy.String()
			ge.RecordedBy = &u
		}
		if !e.ApprovedBy.IsZero() {
			u := e.ApprovedBy.String()
			ge.ApprovedBy = &u
		}
		if e.Request != "" {
			r := e.Request
			ge.RequestID = &r
		}
		if !e.Manifest.IsZero() {
			m := e.Manifest.String()
			ge.ManifestDigest = &m
		}
		out.History[i] = ge
	}
	if !v.Manifest.IsZero() {
		out.Manifest = &gen.CloseManifestDocument{Digest: v.Manifest.String(), Body: v.ManifestBody}
	}
	return out
}

func (rt *Router) periodsUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Periods != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"The subledger is not wired in this cell. The request was not applied."))
	return true
}

func (rt *Router) handleGetPeriod(w http.ResponseWriter, r *http.Request) {
	if rt.periodsUnavailable(w, r) {
		return
	}
	v, err := rt.Periods.Period(r.Context(), r.PathValue("period"))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toPeriod(v))
}

func (rt *Router) handleTransitionPeriod(w http.ResponseWriter, r *http.Request) {
	if rt.periodsUnavailable(w, r) {
		return
	}
	var req gen.PeriodTransitionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	reason := ""
	if req.Reason != nil {
		reason = *req.Reason
	}
	v, err := rt.Periods.Transition(r.Context(), r.PathValue("period"), subledger.PeriodState(req.To), reason)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toPeriod(v))
}

func (rt *Router) handleRequestReopen(w http.ResponseWriter, r *http.Request) {
	if rt.periodsUnavailable(w, r) {
		return
	}
	var req gen.ReopenRequestBody
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	got, err := rt.Periods.RequestReopen(r.Context(), r.PathValue("period"), req.Reason)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, gen.PeriodReopenRequest{
		ID: got.ID.String(), Period: got.Period, Reason: got.Reason,
		RequestedAt: canonical.FormatTime(got.RequestedAt), RequestedBy: got.RequestedBy.String(),
	})
}

func (rt *Router) handleApproveReopen(w http.ResponseWriter, r *http.Request) {
	if rt.periodsUnavailable(w, r) {
		return
	}
	requestID, err := id.ParseReopenRequestID(r.PathValue("requestId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("requestId", errs.ReasonInvalidValue, "That is not a valid request identifier."))
		return
	}
	v, err := rt.Periods.ApproveReopen(r.Context(), r.PathValue("period"), requestID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toPeriod(v))
}
