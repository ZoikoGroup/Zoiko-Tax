package http

import (
	"net/http"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/entitlement"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ontology"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subscription"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The commercial entitlement and subscription surface (Engineering Doc 18,
// G-004, G-049). Enumerations cross it as the strings the domain names
// them, so nothing here depends on the generated constant names.

func (rt *Router) entitlementsUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Entitlements != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"Entitlements and subscriptions are not wired in this cell. The request was not applied."))
	return true
}

func toEntitlement(v app.GrantView) gen.Entitlement {
	g, ev := v.Grant, v.Evaluation
	out := gen.Entitlement{
		ID: g.ID.String(), LegalEntityID: g.LegalEntity.String(), Jurisdiction: g.Jurisdiction, Country: g.Country(),
		Service: gen.ServiceFamily(g.Service), Mode: gen.EntitlementMode(g.Mode), Operation: gen.EntitlementOperation(g.Operation),
		EffectiveFrom: g.EffectiveFrom.Format(time.DateOnly), EffectiveTo: civilOut(g.EffectiveTo), ActiveFrom: civilOut(v.ActiveFrom),
		ContractRef: g.ContractRef, ContractRevision: optStr(g.ContractRevision),
		Lifecycle: gen.EntitlementLifecycle(g.Lifecycle), Status: gen.EntitlementStatus(ev.Status),
		Effective: gen.EntitlementEffective(ev.Effective), Decision: gen.EntitlementDecision(ev.Decision), Mismatch: ev.Mismatch,
		Findings: toFindings(ev.Findings), Coverage: toEntitlementCoverage(ev.Coverage), Version: saturate32(g.Version()),
		Actions: make([]gen.EntitlementAction, len(v.Offers)), History: make([]gen.EntitlementEvent, len(g.History)),
		AsOf: canonical.FormatTime(v.AsOf), CreatedAt: canonical.FormatTime(g.CreatedAt), CreatedBy: g.CreatedBy.String(),
	}
	for i, a := range v.Offers {
		out.Actions[i] = gen.EntitlementAction(a)
	}
	if s, ok := g.Submitter(); ok {
		by := s.String()
		out.SubmittedBy = &by
	}
	if a, ok := g.Approval(); ok {
		by, at := a.RecordedBy.String(), canonical.FormatTime(a.RecordedAt)
		out.ApprovedBy, out.ApprovedAt = &by, &at
	}
	for i, e := range g.History {
		out.History[i] = gen.EntitlementEvent{
			Seq: saturate32(e.Seq), Kind: gen.EntitlementEventKind(e.Kind), Lifecycle: gen.EntitlementLifecycle(e.Lifecycle),
			Reason: optStr(e.Reason), RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String(),
		}
	}
	return out
}

func toFindings(fs []entitlement.Finding) []gen.EntitlementFinding {
	out := make([]gen.EntitlementFinding, len(fs))
	for i, f := range fs {
		out[i] = gen.EntitlementFinding{Reason: gen.EntitlementReason(f.Reason), Detail: f.Detail}
	}
	return out
}

func toEntitlementCoverage(c entitlement.Coverage) gen.EntitlementCoverage {
	out := gen.EntitlementCoverage{
		State: gen.EntitlementCoverageState(c.State), MarketStatus: gen.EntitlementMarketStatus(c.MarketStatus),
		ServicesInForce: make([]gen.ServiceFamily, len(c.ServicesInForce)), Findings: toFindings(c.Findings),
	}
	for i, s := range c.ServicesInForce {
		out.ServicesInForce[i] = gen.ServiceFamily(s)
	}
	if c.MarketReason != "" {
		r := gen.DashboardMarketReason(c.MarketReason)
		out.MarketReason = &r
	}
	if c.Research != "" {
		r := gen.DashboardResearch(c.Research)
		out.Research = &r
	}
	if f := c.Filing; f != nil {
		out.Filing = &gen.EntitlementFilingGate{Ready: f.Ready, Authority: optStr(f.Authority), Reason: optStr(f.Reason)}
	}
	return out
}

func (rt *Router) entitlementID(w http.ResponseWriter, r *http.Request) (id.EntitlementID, bool) {
	gid, err := id.ParseEntitlementID(r.PathValue("entitlementId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("entitlementId", errs.ReasonInvalidValue, "That is not a valid entitlement identifier."))
		return id.EntitlementID{}, false
	}
	return gid, true
}

func (rt *Router) planID(w http.ResponseWriter, r *http.Request) (id.SubscriptionPlanID, bool) {
	pid, err := id.ParseSubscriptionPlanID(r.PathValue("planId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("planId", errs.ReasonInvalidValue, "That is not a valid subscription plan identifier."))
		return id.SubscriptionPlanID{}, false
	}
	return pid, true
}

// ---------------------------------------------------------------------------
// entitlements
// ---------------------------------------------------------------------------

func entitlementFilter(r *http.Request) (app.EntitlementFilter, error) {
	q := r.URL.Query()
	f := app.EntitlementFilter{Filter: entitlement.Filter{
		Status: entitlement.Status(q.Get("status")), Country: strings.ToUpper(q.Get("country")),
		Mode: entitlement.Mode(q.Get("mode")), Operation: entitlement.Operation(q.Get("operation")),
	}, Effective: entitlement.Effective(q.Get("effective"))}
	switch {
	case f.Status != "" && !f.Status.Valid():
		return f, errs.Invalid("status", errs.ReasonInvalidValue, "That is not an entitlement status.")
	case f.Effective != "" && !f.Effective.Valid():
		return f, errs.Invalid("effective", errs.ReasonInvalidValue, "That is not an effective state.")
	case f.Mode != "" && !f.Mode.Valid():
		return f, errs.Invalid("mode", errs.ReasonInvalidValue, "That is not an operating mode.")
	case f.Operation != "" && !f.Operation.Valid():
		return f, errs.Invalid("operation", errs.ReasonInvalidValue, "That is not an operation.")
	case f.Country != "" && (len(f.Country) != 2 || strings.ToUpper(f.Country) != f.Country || strings.ContainsAny(f.Country, "0123456789-")):
		return f, errs.Invalid("country", errs.ReasonInvalidValue, "A country is an ISO 3166-1 alpha-2 code.")
	}
	switch q.Get("mismatch") {
	case "", "false":
	case "true":
		f.Mismatch = true
	default:
		return f, errs.Invalid("mismatch", errs.ReasonInvalidValue, "mismatch is true or false.")
	}
	if v := q.Get("legalEntityId"); v != "" {
		le, err := optLegalEntityID(&v)
		if err != nil {
			return f, err
		}
		f.LegalEntity = le
	}
	return f, nil
}

func (rt *Router) handleListEntitlements(w http.ResponseWriter, r *http.Request) {
	if rt.entitlementsUnavailable(w, r) {
		return
	}
	f, err := entitlementFilter(r)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	got, err := rt.Entitlements.Entitlements(r.Context(), f, limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.EntitlementList{
		Entitlements: make([]gen.Entitlement, len(got.Grants)), Catalogue: optStr(got.Catalogue),
		Authoritative: got.Authoritative, AsOf: canonical.FormatTime(got.AsOf),
		Markets: toDashboardCoverage(app.DashboardCoverage{Markets: got.Markets}).Markets,
	}
	for i, v := range got.Grants {
		out.Entitlements[i] = toEntitlement(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleCreateEntitlement(w http.ResponseWriter, r *http.Request) {
	if rt.entitlementsUnavailable(w, r) {
		return
	}
	var req gen.EntitlementRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.EntitlementInput{
		IntakeKey: r.Header.Get("Idempotency-Key"), Jurisdiction: req.Jurisdiction, Service: ontology.Family(req.Service),
		Mode: entitlement.Mode(req.Mode), Operation: entitlement.Operation(req.Operation), ContractRef: req.ContractRef,
		Submit: req.Submit != nil && *req.Submit,
	}
	if req.ContractRevision != nil {
		in.ContractRevision = *req.ContractRevision
	}
	var err error
	if in.LegalEntity, err = optLegalEntityID(req.LegalEntityID); err == nil {
		in.EffectiveFrom, err = parseCivilDate("effectiveFrom", req.EffectiveFrom)
	}
	if err == nil && req.EffectiveTo != nil {
		in.EffectiveTo, err = parseCivilDate("effectiveTo", *req.EffectiveTo)
	}
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, created, err := rt.Entitlements.CreateEntitlement(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toEntitlement(v))
}

func (rt *Router) handleGetEntitlement(w http.ResponseWriter, r *http.Request) {
	if rt.entitlementsUnavailable(w, r) {
		return
	}
	gid, ok := rt.entitlementID(w, r)
	if !ok {
		return
	}
	v, err := rt.Entitlements.Entitlement(r.Context(), gid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toEntitlement(v))
}

func (rt *Router) handleActOnEntitlement(w http.ResponseWriter, r *http.Request) {
	if rt.entitlementsUnavailable(w, r) {
		return
	}
	gid, ok := rt.entitlementID(w, r)
	if !ok {
		return
	}
	var req gen.EntitlementActionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	act := entitlement.Act{Action: entitlement.Action(req.Action), ExpectedVersion: int(req.ExpectedVersion)}
	if req.Reason != nil {
		act.Reason = *req.Reason
	}
	v, err := rt.Entitlements.ActOnEntitlement(r.Context(), gid, act)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toEntitlement(v))
}

// ---------------------------------------------------------------------------
// subscription plans
// ---------------------------------------------------------------------------

func toSubscriptionPlan(v app.PlanView) gen.SubscriptionPlan {
	p := v.Plan
	out := gen.SubscriptionPlan{
		ID: p.ID.String(), PlanCode: p.PlanCode, PlanName: p.PlanName, ContractRef: p.ContractRef,
		TermStart: p.TermStart.Format(time.DateOnly), TermEnd: civilOut(p.TermEnd),
		Quotas: make([]gen.SubscriptionQuota, len(p.Quotas)), Status: gen.SubscriptionPlanStatus(p.Status), InForce: v.InForce,
		Version: saturate32(p.Version()), History: make([]gen.SubscriptionPlanEvent, len(p.History)),
		AsOf: canonical.FormatTime(v.AsOf), CreatedAt: canonical.FormatTime(p.CreatedAt), CreatedBy: p.CreatedBy.String(),
	}
	for i, q := range p.Quotas {
		out.Quotas[i] = gen.SubscriptionQuota{Meter: gen.UsageMeter(q.Meter), Included: q.Included, Overage: gen.UsageOveragePolicy(q.Overage)}
	}
	if a, ok := p.Approval(); ok {
		by, at := a.RecordedBy.String(), canonical.FormatTime(a.RecordedAt)
		out.ApprovedBy, out.ApprovedAt = &by, &at
	}
	for i, e := range p.History {
		out.History[i] = gen.SubscriptionPlanEvent{
			Seq: saturate32(e.Seq), Kind: gen.SubscriptionPlanEventKind(e.Kind), Status: gen.SubscriptionPlanStatus(e.Status),
			Reason: optStr(e.Reason), RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String(),
		}
	}
	return out
}

func toMeterDefinition(d subscription.Definition) gen.UsageMeterDefinition {
	return gen.UsageMeterDefinition{Meter: gen.UsageMeter(d.Meter), Unit: d.Unit, Source: d.Source, Rule: d.Rule}
}

func (rt *Router) handleListSubscriptionPlans(w http.ResponseWriter, r *http.Request) {
	if rt.entitlementsUnavailable(w, r) {
		return
	}
	got, err := rt.Entitlements.Plans(r.Context())
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.SubscriptionPlanList{
		Plans: make([]gen.SubscriptionPlan, len(got.Plans)), Meters: make([]gen.UsageMeterDefinition, len(subscription.Definitions)),
		DefinitionsVersion: subscription.DefinitionsVersion, AsOf: canonical.FormatTime(got.AsOf),
	}
	for i, v := range got.Plans {
		out.Plans[i] = toSubscriptionPlan(v)
	}
	for i, d := range subscription.Definitions {
		out.Meters[i] = toMeterDefinition(d)
	}
	if got.InForce != nil {
		s := got.InForce.ID.String()
		out.InForcePlanID = &s
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleProposeSubscriptionPlan(w http.ResponseWriter, r *http.Request) {
	if rt.entitlementsUnavailable(w, r) {
		return
	}
	var req gen.SubscriptionPlanRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.PlanInput{IntakeKey: r.Header.Get("Idempotency-Key"), PlanCode: req.PlanCode, PlanName: req.PlanName, ContractRef: req.ContractRef}
	if len(req.Quotas) > len(subscription.Definitions) {
		writeProblem(w, r, rt.log, errs.Invalid("quotas", errs.ReasonInvalidValue, "A plan contracts each meter at most once."))
		return
	}
	for _, q := range req.Quotas {
		in.Quotas = append(in.Quotas, subscription.Quota{Meter: subscription.Meter(q.Meter), Included: q.Included,
			Overage: subscription.OveragePolicy(q.Overage)})
	}
	var err error
	if in.TermStart, err = parseCivilDate("termStart", req.TermStart); err == nil && req.TermEnd != nil {
		in.TermEnd, err = parseCivilDate("termEnd", *req.TermEnd)
	}
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, created, err := rt.Entitlements.ProposePlan(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toSubscriptionPlan(v))
}

func (rt *Router) handleGetSubscriptionPlan(w http.ResponseWriter, r *http.Request) {
	if rt.entitlementsUnavailable(w, r) {
		return
	}
	pid, ok := rt.planID(w, r)
	if !ok {
		return
	}
	v, err := rt.Entitlements.Plan(r.Context(), pid)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toSubscriptionPlan(v))
}

func (rt *Router) handleActOnSubscriptionPlan(w http.ResponseWriter, r *http.Request) {
	if rt.entitlementsUnavailable(w, r) {
		return
	}
	pid, ok := rt.planID(w, r)
	if !ok {
		return
	}
	var req gen.SubscriptionPlanActionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	reason := ""
	if req.Reason != nil {
		reason = *req.Reason
	}
	v, err := rt.Entitlements.ActOnPlan(r.Context(), pid, subscription.Action(req.Action), int(req.ExpectedVersion), reason)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toSubscriptionPlan(v))
}

// ---------------------------------------------------------------------------
// usage
// ---------------------------------------------------------------------------

func (rt *Router) handleGetUsage(w http.ResponseWriter, r *http.Request) {
	if rt.entitlementsUnavailable(w, r) {
		return
	}
	rep, err := rt.Entitlements.Usage(r.Context(), r.URL.Query().Get("period"))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.UsageReport{
		Period: rep.Window.Period, From: canonical.FormatTime(rep.Window.From), To: canonical.FormatTime(rep.Window.To),
		Complete: rep.Complete, Lines: make([]gen.UsageLine, len(rep.Lines)), DefinitionsVersion: rep.DefinitionsVersion,
		AsOf: canonical.FormatTime(rep.AsOf),
	}
	if p := rep.Plan; p != nil {
		out.Plan = &gen.UsagePlanRef{ID: p.ID.String(), PlanCode: p.PlanCode, PlanName: p.PlanName}
	}
	for i, l := range rep.Lines {
		gl := gen.UsageLine{
			Meter: gen.UsageMeter(l.Definition.Meter), Unit: l.Definition.Unit, Source: l.Definition.Source, Rule: l.Definition.Rule,
			Count: l.Count.Count, BasisDigest: l.Count.Digest, State: gen.UsageState(l.State), Remaining: l.Remaining,
			OverageCount: l.Overage,
		}
		if q := l.Quota; q != nil {
			inc, pol := q.Included, gen.UsageOveragePolicy(q.Overage)
			gl.Included, gl.Overage = &inc, &pol
		}
		out.Lines[i] = gl
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleGetUsageBasis(w http.ResponseWriter, r *http.Request) {
	if rt.entitlementsUnavailable(w, r) {
		return
	}
	q := r.URL.Query()
	m := subscription.Meter(q.Get("meter"))
	if m == "" {
		writeProblem(w, r, rt.log, errs.Invalid("meter", errs.ReasonMissingField, "Name the meter whose basis to list."))
		return
	}
	b, err := rt.Entitlements.Basis(r.Context(), q.Get("period"), m, limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.UsageBasis{
		Period: b.Window.Period, From: canonical.FormatTime(b.Window.From), To: canonical.FormatTime(b.Window.To),
		Meter: gen.UsageMeter(b.Definition.Meter), Unit: b.Definition.Unit, Source: b.Definition.Source, Rule: b.Definition.Rule,
		Count: b.Count.Count, BasisDigest: b.Count.Digest, Rows: make([]gen.UsageBasisRow, len(b.Rows)), Truncated: b.Truncated,
		AsOf: canonical.FormatTime(b.AsOf),
	}
	for i, row := range b.Rows {
		out.Rows[i] = gen.UsageBasisRow{ID: row.ID, At: canonical.FormatTime(row.At)}
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}
