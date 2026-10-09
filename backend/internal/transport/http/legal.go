package http

import (
	"net/http"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/legal"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The LegalAuthorization matrix, customer authorizations and the gate
// (ZTAX-LEG-001 §3, §4, §9).

func (rt *Router) legalUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Legal != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"Legal authorization is not wired in this cell. The request was not applied."))
	return true
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := canonical.FormatTime(t)
	return &s
}

func toLegalMatrix(m legal.Matrix) gen.LegalMatrix {
	out := gen.LegalMatrix{Version: m.Version, Draft: m.Draft, Rules: make([]gen.LegalRule, len(m.Rules))}
	if !m.Digest.IsZero() {
		d := m.Digest.String()
		out.Digest = &d
	}
	for i, r := range m.Rules {
		out.Rules[i] = gen.LegalRule{
			ID: r.ID, Version: saturate32(r.Version), Country: r.Country, Authority: r.Authority,
			Service: gen.ServiceState(r.Service), Status: gen.LegalStatus(r.Status), Provider: r.Provider,
			AuthorizationType: gen.AuthorizationType(r.AuthorizationType), RequiresPeriods: r.RequiresPeriods,
			RequiresMatters: r.RequiresMatters, Qualification: optStr(r.Qualification), Credential: optStr(r.Credential),
			Funds: gen.FundsPosture(r.Funds), OpinionRef: r.OpinionRef, EffectiveFrom: canonical.FormatTime(r.EffectiveFrom),
			EffectiveTo: optTime(r.EffectiveTo),
		}
	}
	return out
}

func toAuthorization(a legal.Authorization, now time.Time) gen.CustomerAuthorization {
	out := gen.CustomerAuthorization{
		ID: a.ID.String(), LegalEntityID: a.LegalEntity.String(), Country: a.Country, Authority: a.Authority,
		Type: gen.AuthorizationType(a.Type), Matters: a.Matters, PeriodFrom: optStr(a.PeriodFrom), PeriodTo: optStr(a.PeriodTo),
		Representative: optStr(a.Representative), EffectiveFrom: canonical.FormatTime(a.EffectiveFrom), ExpiresAt: optTime(a.ExpiresAt),
		Evidence: a.Evidence, CredentialRef: optStr(a.CredentialRef), Status: gen.CustomerAuthorizationStatus(a.Status),
		InForce: a.InForce(now), RecordedAt: canonical.FormatTime(a.RecordedAt), RecordedBy: a.RecordedBy.String(),
		History: make([]gen.AuthorizationEvent, len(a.History)),
	}
	if out.Matters == nil {
		out.Matters = []string{}
	}
	for _, p := range a.Permissions {
		out.Permissions = append(out.Permissions, gen.AuthorizationPermission(p))
	}
	if !a.Supersedes.IsZero() {
		s := a.Supersedes.String()
		out.Supersedes = &s
	}
	for i, e := range a.History {
		ge := gen.AuthorizationEvent{Seq: saturate32(e.Seq), Kind: gen.AuthorizationEventKind(e.Kind), Reason: optStr(e.Reason),
			RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String()}
		if !e.By.IsZero() {
			b := e.By.String()
			ge.SupersededBy = &b
		}
		out.History[i] = ge
	}
	return out
}

func (rt *Router) handleGetLegalMatrix(w http.ResponseWriter, r *http.Request) {
	if rt.legalUnavailable(w, r) {
		return
	}
	m, err := rt.Legal.Matrix(r.Context())
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toLegalMatrix(m))
}

func (rt *Router) handleListAuthorizations(w http.ResponseWriter, r *http.Request) {
	if rt.legalUnavailable(w, r) {
		return
	}
	got, err := rt.Legal.Authorizations(r.Context())
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	at := rt.Legal.Now()
	out := gen.AuthorizationList{Authorizations: make([]gen.CustomerAuthorization, len(got))}
	for i, a := range got {
		out.Authorizations[i] = toAuthorization(a, at)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleGrantAuthorization(w http.ResponseWriter, r *http.Request) {
	if rt.legalUnavailable(w, r) {
		return
	}
	var req gen.AuthorizationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.GrantInput{Country: req.Country, Authority: req.Authority, Type: legal.AuthorizationType(req.Type),
		Matters: req.Matters, Evidence: req.Evidence}
	for _, p := range req.Permissions {
		in.Permissions = append(in.Permissions, legal.Permission(p))
	}
	for _, p := range []struct {
		src *string
		dst *string
	}{{req.PeriodFrom, &in.PeriodFrom}, {req.PeriodTo, &in.PeriodTo}, {req.Representative, &in.Representative}, {req.CredentialRef, &in.CredentialRef}} {
		if p.src != nil {
			*p.dst = *p.src
		}
	}
	var err error
	if in.EffectiveFrom, err = parseTimestamp("effectiveFrom", req.EffectiveFrom); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if req.ExpiresAt != nil {
		if in.ExpiresAt, err = parseTimestamp("expiresAt", *req.ExpiresAt); err != nil {
			writeProblem(w, r, rt.log, err)
			return
		}
	}
	if req.LegalEntityID != nil {
		if in.LegalEntity, err = id.ParseLegalEntityID(*req.LegalEntityID); err != nil {
			writeProblem(w, r, rt.log, errs.Invalid("legalEntityId", errs.ReasonInvalidValue, "That is not a valid legal entity identifier."))
			return
		}
	}
	if req.Supersedes != nil {
		if in.Supersedes, err = id.ParseAuthorizationID(*req.Supersedes); err != nil {
			writeProblem(w, r, rt.log, errs.Invalid("supersedes", errs.ReasonInvalidValue, "That is not a valid authorization identifier."))
			return
		}
	}
	a, err := rt.Legal.Grant(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toAuthorization(a, rt.Legal.Now()))
}

func authorizationIDFrom(r *http.Request) (id.AuthorizationID, error) {
	a, err := id.ParseAuthorizationID(r.PathValue("authorizationId"))
	if err != nil {
		return id.AuthorizationID{}, errs.Invalid("authorizationId", errs.ReasonInvalidValue, "That is not a valid authorization identifier.")
	}
	return a, nil
}

func (rt *Router) handleGetAuthorization(w http.ResponseWriter, r *http.Request) {
	if rt.legalUnavailable(w, r) {
		return
	}
	authID, err := authorizationIDFrom(r)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	a, err := rt.Legal.Authorization(r.Context(), authID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toAuthorization(a, rt.Legal.Now()))
}

func (rt *Router) handleRevokeAuthorization(w http.ResponseWriter, r *http.Request) {
	if rt.legalUnavailable(w, r) {
		return
	}
	authID, err := authorizationIDFrom(r)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	var req gen.RevocationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	a, err := rt.Legal.Revoke(r.Context(), authID, req.Reason)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toAuthorization(a, rt.Legal.Now()))
}

func (rt *Router) handleCheckAuthorization(w http.ResponseWriter, r *http.Request) {
	if rt.legalUnavailable(w, r) {
		return
	}
	var req gen.AuthorizationCheckRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	act := legal.Action{Country: req.Country, Authority: req.Authority, Service: legal.ServiceState(req.Service)}
	if req.Period != nil {
		act.Period = *req.Period
	}
	if req.Matter != nil {
		act.Matter = *req.Matter
	}
	if req.LegalEntityID != nil {
		le, err := id.ParseLegalEntityID(*req.LegalEntityID)
		if err != nil {
			writeProblem(w, r, rt.log, errs.Invalid("legalEntityId", errs.ReasonInvalidValue, "That is not a valid legal entity identifier."))
			return
		}
		act.LegalEntity = le
	}
	res, at, err := rt.Legal.Gate(r.Context(), act)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.AuthorizationCheck{Allowed: res.Allowed, Status: gen.LegalStatus(res.Status), CheckedAt: canonical.FormatTime(at),
		MatrixVersion: optStr(res.MatrixVersion), Detail: optStr(res.Detail)}
	if res.Reason != "" {
		reason := gen.AuthorizationCheckReason(res.Reason)
		out.Reason = &reason
	}
	if res.Rule != nil {
		out.Rule = &gen.RuleRef{ID: res.Rule.ID, Version: saturate32(res.Rule.Version)}
	}
	if !res.Authorization.IsZero() {
		a := res.Authorization.String()
		out.AuthorizationID = &a
	}
	if !res.MatrixDigest.IsZero() {
		d := res.MatrixDigest.String()
		out.MatrixDigest = &d
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}
