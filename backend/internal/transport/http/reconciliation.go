package http

import (
	"net/http"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/reconciliation"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// Reconciliation runs (ZTAX-FIN-001 §17–§20).

func moneyOut(m *fiscal.Money) *gen.MoneyValue {
	if m == nil {
		return nil
	}
	return &gen.MoneyValue{Amount: m.CanonicalString(), Currency: string(m.Currency())}
}

func toReconItem(v app.RunItemView) gen.ReconciliationItem {
	out := gen.ReconciliationItem{
		ID: v.ID.String(), Stage: gen.ReconStage(v.Stage), MatchKey: string(v.Key),
		Expected: moneyOut(v.Expected), Observed: moneyOut(v.Observed), Variance: moneyOut(v.Variance),
		Status: gen.ReconciliationItemStatus(v.Status),
	}
	if v.Resolution != nil {
		// The item as stored keeps the status the comparison gave it; its
		// resolution is what makes it RESOLVED.
		out.Status = gen.ReconciliationItemStatus(reconciliation.StatusResolved)
	}
	if v.Cause != "" {
		c := gen.RootCause(v.Cause)
		out.RootCause = &c
	}
	if v.Detail != "" {
		d := v.Detail
		out.Detail = &d
	}
	if r := v.Resolution; r != nil {
		res := gen.ReconResolution{
			Actor: gen.ReconResolutionActor(r.Actor), Reason: r.Reason, Action: gen.ReconResolutionAction(r.Action),
			RootCause: gen.RootCause(r.Cause), Evidence: r.Evidence, ResolvedAt: canonical.FormatTime(r.At),
		}
		if !r.Resolver.IsZero() {
			u := r.Resolver.String()
			res.Resolver = &u
		}
		out.Resolution = &res
	}
	return out
}

func toReconRun(v app.RunView) gen.ReconciliationRun {
	out := gen.ReconciliationRun{
		ID: v.Run.ID.String(), Period: v.Run.Period, LegalEntityID: v.Run.LegalEntity.String(),
		RanAt: canonical.FormatTime(v.Run.RanAt), Unavailable: make([]gen.ReconStage, len(v.Run.Unavailable)),
		Items: make([]gen.ReconciliationItem, len(v.Items)),
	}
	if !v.Run.RanBy.IsZero() {
		u := v.Run.RanBy.String()
		out.RanBy = &u
	}
	if v.Run.FirstBreak != nil {
		s := gen.ReconStage(*v.Run.FirstBreak)
		out.FirstBreak = &s
	}
	for i, s := range v.Run.Unavailable {
		out.Unavailable[i] = gen.ReconStage(s)
	}
	for i, it := range v.Items {
		out.Items[i] = toReconItem(it)
	}
	return out
}

func (rt *Router) reconUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Reconciliations != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"Reconciliation is not wired in this cell. The request was not applied."))
	return true
}

func (rt *Router) runID(w http.ResponseWriter, r *http.Request) (id.ReconciliationID, bool) {
	runID, err := id.ParseReconciliationID(r.PathValue("runId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("runId", errs.ReasonInvalidValue, "That is not a valid run identifier."))
		return id.ReconciliationID{}, false
	}
	return runID, true
}

func (rt *Router) handleRunReconciliation(w http.ResponseWriter, r *http.Request) {
	if rt.reconUnavailable(w, r) {
		return
	}
	var req gen.ReconciliationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Reconciliations.Run(r.Context(), req.Period)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toReconRun(v))
}

func (rt *Router) handleGetReconciliation(w http.ResponseWriter, r *http.Request) {
	if rt.reconUnavailable(w, r) {
		return
	}
	runID, ok := rt.runID(w, r)
	if !ok {
		return
	}
	v, err := rt.Reconciliations.RunByID(r.Context(), runID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toReconRun(v))
}

func (rt *Router) handleResolveReconItem(w http.ResponseWriter, r *http.Request) {
	if rt.reconUnavailable(w, r) {
		return
	}
	runID, ok := rt.runID(w, r)
	if !ok {
		return
	}
	itemID, err := id.ParseReconItemID(r.PathValue("itemId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("itemId", errs.ReasonInvalidValue, "That is not a valid item identifier."))
		return
	}
	var req gen.ResolutionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Reconciliations.Resolve(r.Context(), runID, itemID, app.ResolutionInput{
		Reason: req.Reason, Action: reconciliation.Action(req.Action), Cause: reconciliation.RootCause(req.RootCause),
		Evidence: req.Evidence,
	})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toReconItem(v))
}
