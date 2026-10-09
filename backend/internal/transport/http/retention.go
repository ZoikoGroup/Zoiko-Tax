package http

import (
	"net/http"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/retention"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// Retention policies, legal holds and the disposition verdict
// (ZTAX-EVID-001 §11–§13).

func (rt *Router) retentionUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Retention != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"Retention is not wired in this cell. The request was not applied."))
	return true
}

func toRetentionPolicy(p retention.Policy) gen.RetentionPolicy {
	return gen.RetentionPolicy{
		ID: p.ID, Version: saturate32(p.Version), RecordClass: gen.RetentionRecordClass(p.Class), Country: p.Country,
		Years: saturate32(p.Years), Trigger: gen.RetentionTrigger(p.Trigger), EffectiveFrom: canonical.FormatTime(p.EffectiveFrom),
		Citation: p.Citation, RecordedAt: canonical.FormatTime(p.RecordedAt), RecordedBy: p.RecordedBy.String(),
	}
}

func toHoldScope(s retention.Scope) gen.LegalHoldScope {
	out := gen.LegalHoldScope{BusinessKeys: s.BusinessKeys}
	for _, d := range s.Decisions {
		out.DecisionIds = append(out.DecisionIds, d.String())
	}
	if !s.LegalEntity.IsZero() {
		le := s.LegalEntity.String()
		out.LegalEntityID = &le
	}
	if !s.EventFrom.IsZero() {
		from, to := canonical.FormatTime(s.EventFrom), canonical.FormatTime(s.EventTo)
		out.EventFrom, out.EventTo = &from, &to
	}
	return out
}

func toLegalHold(h retention.Hold) gen.LegalHold {
	out := gen.LegalHold{ID: h.ID.String(), Matter: h.Matter, Status: gen.LegalHoldStatus(h.Status), Scope: toHoldScope(h.Scope),
		History: make([]gen.LegalHoldEvent, len(h.History))}
	for i, e := range h.History {
		out.History[i] = gen.LegalHoldEvent{Seq: saturate32(e.Seq), Kind: gen.LegalHoldEventKind(e.Kind), Scope: toHoldScope(e.Scope),
			Reason: e.Reason, RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String()}
	}
	return out
}

// fromHoldScope reads a scope off the wire.
func fromHoldScope(s gen.LegalHoldScope) (retention.Scope, error) {
	out := retention.Scope{BusinessKeys: s.BusinessKeys}
	for _, raw := range s.DecisionIds {
		d, err := id.ParseDecisionID(raw)
		if err != nil {
			return retention.Scope{}, errs.Invalid("scope.decisionIds", errs.ReasonInvalidValue, "That is not a valid decision identifier.")
		}
		out.Decisions = append(out.Decisions, d)
	}
	if s.LegalEntityID != nil {
		le, err := id.ParseLegalEntityID(*s.LegalEntityID)
		if err != nil {
			return retention.Scope{}, errs.Invalid("scope.legalEntityId", errs.ReasonInvalidValue, "That is not a valid legal entity identifier.")
		}
		out.LegalEntity = le
	}
	for _, p := range []struct {
		field string
		src   *string
		dst   *time.Time
	}{{"scope.eventFrom", s.EventFrom, &out.EventFrom}, {"scope.eventTo", s.EventTo, &out.EventTo}} {
		if p.src == nil {
			continue
		}
		t, err := parseTimestamp(p.field, *p.src)
		if err != nil {
			return retention.Scope{}, err
		}
		*p.dst = t
	}
	return out, nil
}

func (rt *Router) handleListRetentionPolicies(w http.ResponseWriter, r *http.Request) {
	if rt.retentionUnavailable(w, r) {
		return
	}
	got, err := rt.Retention.Policies(r.Context())
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.RetentionPolicyList{Policies: make([]gen.RetentionPolicy, len(got))}
	for i, p := range got {
		out.Policies[i] = toRetentionPolicy(p)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleRecordRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	if rt.retentionUnavailable(w, r) {
		return
	}
	var req gen.RetentionPolicyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	from, err := parseTimestamp("effectiveFrom", req.EffectiveFrom)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	p, err := rt.Retention.RecordPolicy(r.Context(), app.PolicyInput{
		ID: req.ID, Class: retention.RecordClass(req.RecordClass), Country: req.Country, Years: int(req.Years),
		Trigger: retention.Trigger(req.Trigger), EffectiveFrom: from, Citation: req.Citation,
	})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toRetentionPolicy(p))
}

func (rt *Router) handleListLegalHolds(w http.ResponseWriter, r *http.Request) {
	if rt.retentionUnavailable(w, r) {
		return
	}
	got, err := rt.Retention.Holds(r.Context())
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.LegalHoldList{Holds: make([]gen.LegalHold, len(got))}
	for i, h := range got {
		out.Holds[i] = toLegalHold(h)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handlePlaceLegalHold(w http.ResponseWriter, r *http.Request) {
	if rt.retentionUnavailable(w, r) {
		return
	}
	var req gen.LegalHoldRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	scope, err := fromHoldScope(req.Scope)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	h, err := rt.Retention.PlaceHold(r.Context(), req.Matter, req.Reason, scope)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toLegalHold(h))
}

func holdIDFrom(r *http.Request) (id.LegalHoldID, error) {
	h, err := id.ParseLegalHoldID(r.PathValue("holdId"))
	if err != nil {
		return id.LegalHoldID{}, errs.Invalid("holdId", errs.ReasonInvalidValue, "That is not a valid legal hold identifier.")
	}
	return h, nil
}

func (rt *Router) handleGetLegalHold(w http.ResponseWriter, r *http.Request) {
	if rt.retentionUnavailable(w, r) {
		return
	}
	holdID, err := holdIDFrom(r)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	h, err := rt.Retention.Hold(r.Context(), holdID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toLegalHold(h))
}

func (rt *Router) handleChangeLegalHoldScope(w http.ResponseWriter, r *http.Request) {
	if rt.retentionUnavailable(w, r) {
		return
	}
	holdID, err := holdIDFrom(r)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	var req gen.LegalHoldScopeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	scope, err := fromHoldScope(req.Scope)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	h, err := rt.Retention.ChangeHoldScope(r.Context(), holdID, req.Reason, scope)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toLegalHold(h))
}

func (rt *Router) handleReleaseLegalHold(w http.ResponseWriter, r *http.Request) {
	if rt.retentionUnavailable(w, r) {
		return
	}
	holdID, err := holdIDFrom(r)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	var req gen.LegalHoldReleaseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	h, err := rt.Retention.ReleaseHold(r.Context(), holdID, req.Reason)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toLegalHold(h))
}

func (rt *Router) handleGetDecisionRetention(w http.ResponseWriter, r *http.Request) {
	if rt.retentionUnavailable(w, r) {
		return
	}
	decisionID, err := id.ParseDecisionID(r.PathValue("decisionId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("decisionId", errs.ReasonInvalidValue, "That is not a valid decision identifier."))
		return
	}
	rec, v, at, err := rt.Retention.Verdict(r.Context(), decisionID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.RetentionVerdict{DecisionID: decisionID.String(), RecordClass: gen.RetentionRecordClass(rec.Class),
		Outcome: gen.RetentionVerdictOutcome(v.Outcome), EvaluatedAt: canonical.FormatTime(at)}
	if rec.Country != "" {
		c := rec.Country
		out.Country = &c
	}
	if v.Policy != nil {
		out.Policy = &gen.PolicyVersionRef{ID: v.Policy.ID, Version: saturate32(v.Policy.Version)}
		until := canonical.FormatTime(v.RetainUntil)
		out.RetainUntil = &until
	}
	if v.Outcome == retention.OutcomeConflicted {
		for _, c := range v.Candidates {
			out.Candidates = append(out.Candidates, gen.PolicyVersionRef{ID: c.ID, Version: saturate32(c.Version)})
		}
	}
	for _, h := range v.Holds {
		out.Holds = append(out.Holds, h.String())
	}
	if v.Detail != "" {
		d := v.Detail
		out.Detail = &d
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}
