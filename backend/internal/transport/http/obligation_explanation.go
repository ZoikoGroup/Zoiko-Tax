package http

import (
	"net/http"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// handleExplainObligation serves POST /v1/obligations/{obligationId}/explanation.
// There is no body: the tenant comes from the session and the row from the
// store. It logs nothing about the answer.
func (rt *Router) handleExplainObligation(w http.ResponseWriter, r *http.Request) {
	if rt.ObligationExplanation == nil {
		writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
			"This cell is not configured for obligation explanations."))
		return
	}
	obligationID, ok := rt.obligationID(w, r)
	if !ok {
		return
	}
	o, err := rt.ObligationExplanation.Explain(r.Context(), obligationID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toObligationExplanation(o))
}

func toObligationExplanation(o app.ExplanationOutcome) gen.ObligationExplanation {
	out := gen.ObligationExplanation{
		ObligationID: o.ObligationID.String(),
		Status:       gen.ObligationExplanationStatus(o.Status),
		CitedIds:     make([]gen.ObligationID, 0, len(o.CitedIDs)),
		// Advisory by type: there is no value of this field the service can
		// produce other than false (ADR-0006 §2.6).
		Authoritative: false,
	}
	out.CitedIds = append(out.CitedIds, o.CitedIDs...)
	if o.Status != app.ExplanationAnswered {
		reason := string(o.ReviewReason)
		out.ReviewReason = &reason
	} else {
		text := o.Explanation
		out.Explanation = &text
	}
	if o.ValidatorVersion != "" {
		v := o.ValidatorVersion
		out.ValidatorVersion = &v
	}
	if o.HasProvenance {
		prov := o.Provenance
		out.Provenance = &gen.AiProvenance{
			UseCase: string(prov.UseCase), ModelProfile: prov.ModelProfile, ProviderProfile: prov.ProviderProfile,
			PromptProfile: prov.PromptProfile, AiTrainVersion: prov.AiTrainVersion,
		}
		if prov.InvocationID != "" {
			out.Provenance.InvocationID = &prov.InvocationID
		}
	}
	return out
}
