package http

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/classification"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

func (rt *Router) decisionsUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.ClassificationDecisions != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"Classification decisions are not wired in this cell. The request was not applied."))
	return true
}

func toClassificationDecision(d classification.ReviewDecision) gen.ClassificationDecision {
	out := gen.ClassificationDecision{
		ProposalID: d.ProposalID.String(), SubjectRef: d.SubjectRef, ProposedCode: d.ProposedCode,
		Action: gen.ClassificationDecisionAction(d.Action), Reason: d.Reason,
		TenantID: d.TenantID.String(), ReviewerUserID: d.ReviewerID.String(), DecidedAt: canonical.FormatTime(d.DecidedAt),
	}
	if d.Note != "" {
		out.Note = &d.Note
	}
	if d.ModifiedCode != "" {
		out.ModifiedCode = &d.ModifiedCode
	}
	if d.EvidenceID != "" {
		out.EvidenceID = &d.EvidenceID
	}
	return out
}

// handleRecordClassificationDecision saves a reviewer's decision. The reviewer
// and the tenant come from the session; the body names neither, and a body that
// does is refused by decodeJSON.
func (rt *Router) handleRecordClassificationDecision(w http.ResponseWriter, r *http.Request) {
	if rt.decisionsUnavailable(w, r) {
		return
	}
	var req gen.ClassificationDecisionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	proposalID, err := uuid.Parse(req.ProposalID)
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("proposalId", errs.ReasonInvalidValue, "That is not a valid proposal identifier."))
		return
	}
	d, err := rt.ClassificationDecisions.Record(r.Context(), app.DecisionInput{
		ProposalID: proposalID, SubjectRef: req.SubjectRef, ProposedCode: req.ProposedCode,
		Action: classification.Action(req.Action), Reason: req.Reason, Note: deref(req.Note),
		ModifiedCode: deref(req.ModifiedCode), EvidenceID: deref(req.EvidenceID),
	})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toClassificationDecision(d))
}

func (rt *Router) handleGetClassificationDecision(w http.ResponseWriter, r *http.Request) {
	if rt.decisionsUnavailable(w, r) {
		return
	}
	proposalID, err := uuid.Parse(r.PathValue("proposalId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("proposalId", errs.ReasonInvalidValue, "That is not a valid proposal identifier."))
		return
	}
	d, err := rt.ClassificationDecisions.Decision(r.Context(), proposalID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toClassificationDecision(d))
}
