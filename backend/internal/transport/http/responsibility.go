package http

import (
	"net/http"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

func (rt *Router) handleDecisionResponsibility(w http.ResponseWriter, r *http.Request) {
	if rt.determinationUnavailable(w, r) {
		return
	}
	decisionID, ok := rt.decisionID(w, r)
	if !ok {
		return
	}
	levies, err := rt.Determination.Responsibility(r.Context(), decisionID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.LevyResponsibilityList{Levies: make([]gen.LevyResponsibility, len(levies))}
	for i, l := range levies {
		out.Levies[i] = toLevyResponsibility(l)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func toLevyResponsibility(l obligation.LevyResponsibility) gen.LevyResponsibility {
	d := l.Decision
	out := gen.LevyResponsibility{
		ID: d.ID.String(), Item: l.Item, Component: l.Component, ComponentVersion: l.ComponentVersion,
		TaxType: l.TaxType, Jurisdiction: l.Jurisdiction, Authority: l.Authority,
		Treatment: gen.LevyResponsibilityTreatment(l.Treatment), LegalEntityID: d.LegalEntity.String(),
		Rule:     gen.ResponsibilityRuleRef{ID: d.RuleID, Version: d.RuleVersion},
		BundleID: d.Provenance.BundleID, BundleDigest: d.Provenance.BundleDigest,
		EffectiveFrom: canonical.FormatTime(d.EffectiveFrom), Status: gen.ResponsibilityStatus(d.Status),
		Roles:      make([]gen.RoleAssignment, len(d.Assignments)),
		RecordedAt: canonical.FormatTime(l.RecordedAt),
	}
	for i, a := range d.Assignments {
		ra := gen.RoleAssignment{
			Role: gen.RoleAssignmentRole(a.Role), Status: gen.ResponsibilityStatus(a.Status),
			Statutory: toRoleHolder(a.Statutory), Agent: toRoleHolder(a.Agent),
		}
		if a.Detail != "" {
			detail := a.Detail
			ra.Detail = &detail
		}
		out.Roles[i] = ra
	}
	return out
}

func toRoleHolder(p *obligation.Party) *gen.RoleHolder {
	if p == nil {
		return nil
	}
	out := &gen.RoleHolder{Kind: gen.RoleHolderKind(p.Kind)}
	if !p.LegalEntity.IsZero() {
		le := p.LegalEntity.String()
		out.LegalEntityID = &le
	}
	if p.Ref != "" {
		ref := p.Ref
		out.Ref = &ref
	}
	return out
}
