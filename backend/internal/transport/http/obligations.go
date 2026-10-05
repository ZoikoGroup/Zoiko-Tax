package http

import (
	"net/http"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The obligation surface (W2 lane I).

func toObligation(v app.ObligationView) gen.Obligation {
	o := v.Obligation
	out := gen.Obligation{
		ID: o.ID.String(), Type: o.Type, Duty: gen.ObligationDuty(o.Duty),
		Jurisdiction: o.JurisdictionID, Authority: o.Authority, LegalEntityID: o.LegalEntity.String(),
		Definition: gen.Obligation_Definition{ID: o.Definition.ID, Version: o.Definition.Version},
		Content:    gen.Obligation_Content{BundleID: o.Content.BundleID, BundleDigest: o.Content.BundleDigest},
		// The dates are civil dates in the legal calendar, held as midnight
		// there; formatting reads them in that location, never in UTC.
		PeriodStart: o.Period.Start.Format(time.DateOnly),
		PeriodEnd:   o.Period.End.Format(time.DateOnly),
		DueDate:     o.Period.Due.Format(time.DateOnly),
		Timezone:    o.Timezone, Status: gen.ObligationStatus(o.Status),
		EffectiveStatus: gen.EffectiveObligationStatus(v.EffectiveStatus),
		RecordedAt:      canonical.FormatTime(o.RecordedAt),
	}
	if o.Assessed != nil {
		amount, currency := o.Assessed.CanonicalString(), string(o.Assessed.Currency())
		out.AssessedAmount, out.Currency = &amount, &currency
	}
	if o.Supersedes != nil {
		s := o.Supersedes.String()
		out.Supersedes = &s
	}
	if v.SupersededBy != nil {
		s := v.SupersededBy.String()
		out.SupersededBy = &s
	}
	if !o.RecordedBy.IsZero() {
		u := o.RecordedBy.String()
		out.RecordedBy = &u
	}
	return out
}

func (rt *Router) obligationID(w http.ResponseWriter, r *http.Request) (id.ObligationID, bool) {
	obligationID, err := id.ParseObligationID(r.PathValue("obligationId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("obligationId", errs.ReasonInvalidValue,
			"That is not a valid obligation identifier."))
		return id.ObligationID{}, false
	}
	return obligationID, true
}

func (rt *Router) handleListObligations(w http.ResponseWriter, r *http.Request) {
	if rt.determinationUnavailable(w, r) {
		return
	}
	status := obligation.Status(r.URL.Query().Get("status"))
	if status != "" && !gen.EffectiveObligationStatus(status).Valid() {
		writeProblem(w, r, rt.log, errs.Invalid("status", errs.ReasonInvalidValue,
			"That is not an obligation status."))
		return
	}
	views, err := rt.Determination.Obligations(r.Context(), status, limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.ObligationList{Obligations: make([]gen.Obligation, len(views))}
	for i, v := range views {
		out.Obligations[i] = toObligation(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleGetObligation(w http.ResponseWriter, r *http.Request) {
	if rt.determinationUnavailable(w, r) {
		return
	}
	obligationID, ok := rt.obligationID(w, r)
	if !ok {
		return
	}
	v, err := rt.Determination.Obligation(r.Context(), obligationID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toObligation(v))
}

func (rt *Router) handleTransitionObligation(w http.ResponseWriter, r *http.Request) {
	if rt.determinationUnavailable(w, r) {
		return
	}
	obligationID, ok := rt.obligationID(w, r)
	if !ok {
		return
	}
	var req gen.ObligationTransitionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Determination.TransitionObligation(r.Context(), obligationID, obligation.Status(req.To))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toObligation(v))
}
