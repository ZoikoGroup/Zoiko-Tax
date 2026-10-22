package http

import (
	"net/http"
	"sort"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/shadow"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The shadow assurance surface (ZTAX-SHD-001 §30, W4 lane M).

func (rt *Router) shadowUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Shadow != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"Shadow assurance is not wired in this cell. The request was not applied."))
	return true
}

func toComparator(c shadow.Comparator) gen.Comparator {
	return gen.Comparator{
		ID: c.ID.String(), Name: c.Name, Type: gen.ComparatorType(c.Type), Provider: c.Provider, Version: c.Version,
		Environment: optStr(c.Environment), Limitations: optStr(c.Limitations), Claim: gen.ShadowClaim(c.Type.Claim()),
		CreatedAt: canonical.FormatTime(c.CreatedAt), CreatedBy: c.CreatedBy.String(),
	}
}

func decimalOf(m *fiscal.Money) *string {
	if m == nil {
		return nil
	}
	s := m.CanonicalString()
	return &s
}

func toDiscrepancy(d shadow.Discrepancy) gen.Discrepancy {
	out := gen.Discrepancy{ID: d.ID.String(), RunID: d.Run.String(), CaseSeq: saturate32(d.CaseSeq), State: gen.DiscrepancyState(d.State),
		Severity: gen.ShadowSeverity(d.Severity), RootCause: gen.ShadowRootCause(d.RootCause), History: make([]gen.DiscrepancyEvent, len(d.History))}
	for i, e := range d.History {
		ge := gen.DiscrepancyEvent{Seq: saturate32(e.Seq), State: gen.DiscrepancyState(e.State), Note: e.Note,
			RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String()}
		if e.Severity != "" {
			s := gen.ShadowSeverity(e.Severity)
			ge.Severity = &s
		}
		if e.RootCause != "" {
			c := gen.ShadowRootCause(e.RootCause)
			ge.RootCause = &c
		}
		out.History[i] = ge
	}
	return out
}

func toShadowRun(v app.ShadowRunView) gen.ShadowRun {
	run := v.Run
	m := run.Metrics
	out := gen.ShadowRun{
		ID: run.ID.String(), Comparator: toComparator(v.ComparatorProfile), Mode: gen.ShadowMode(run.Mode),
		WindowFrom: canonical.FormatTime(run.WindowFrom), WindowTo: canonical.FormatTime(run.WindowTo),
		Currency: string(run.Policy.Currency), Tolerance: run.Policy.Tolerance.CanonicalString(),
		Materiality: run.Policy.Materiality.CanonicalString(), InputHash: run.InputHash.String(),
		Metrics: gen.ShadowMetrics{Cases: saturate32(m.Cases), Paired: saturate32(m.Paired), Exact: saturate32(m.Exact),
			Equivalent: saturate32(m.Equivalent), Differing: saturate32(m.Monetary), Category: saturate32(m.Category),
			Population: saturate32(m.Population), Uncomparable: saturate32(m.Uncomparable), Material: saturate32(m.Material),
			ComparedValue: m.ComparedValue.CanonicalString(), AbsoluteVariance: m.AbsoluteVariance.CanonicalString()},
		CutoverBlockers: append([]string{}, v.Blockers...), Discrepancies: make([]gen.Discrepancy, len(v.Discrepancies)),
		CreatedAt: canonical.FormatTime(run.CreatedAt), CreatedBy: run.CreatedBy.String(),
	}
	codes := make([]string, 0, len(run.Policy.ComponentMap))
	for code := range run.Policy.ComponentMap {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		out.ComponentMap = append(out.ComponentMap, gen.ComponentMapping{ExternalCode: code, Slot: run.Policy.ComponentMap[code]})
	}
	for i, d := range v.Discrepancies {
		out.Discrepancies[i] = toDiscrepancy(d)
	}
	if run.Cases != nil {
		out.Cases = make([]gen.ShadowCase, len(run.Cases))
		for i, c := range run.Cases {
			gc := gen.ShadowCase{Seq: saturate32(c.Seq), CaseRef: c.CaseRef, Pairing: gen.ShadowPairing(c.Pairing),
				State: gen.ShadowVariance(c.State), ComparatorTotal: decimalOf(c.ComparatorTotal), ZoikoTotal: decimalOf(c.ZoikoTotal),
				Variance: decimalOf(c.Variance), Material: c.Material, Components: make([]gen.ShadowComponent, len(c.Components))}
			if !c.Decision.IsZero() {
				d := c.Decision.String()
				gc.DecisionID = &d
			}
			for j, cc := range c.Components {
				gc.Components[j] = gen.ShadowComponent{ExternalCode: optStr(cc.ExternalCode), Slot: cc.Slot,
					Comparator: decimalOf(cc.Comparator), Zoiko: decimalOf(cc.Zoiko), State: gen.ShadowComponentState(cc.State)}
			}
			out.Cases[i] = gc
		}
	}
	return out
}

func (rt *Router) handleListComparators(w http.ResponseWriter, r *http.Request) {
	if rt.shadowUnavailable(w, r) {
		return
	}
	got, err := rt.Shadow.Comparators(r.Context())
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.ComparatorList{Comparators: make([]gen.Comparator, len(got))}
	for i, c := range got {
		out.Comparators[i] = toComparator(c)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleCreateComparator(w http.ResponseWriter, r *http.Request) {
	if rt.shadowUnavailable(w, r) {
		return
	}
	var req gen.ComparatorRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if !req.Type.Valid() {
		writeProblem(w, r, rt.log, errs.Invalid("type", errs.ReasonInvalidValue, "That is not a comparator type this service knows."))
		return
	}
	c, err := rt.Shadow.CreateComparator(r.Context(), app.ComparatorInput{Name: req.Name, Type: shadow.ComparatorType(req.Type),
		Provider: req.Provider, Version: deref(req.Version), Environment: deref(req.Environment), Limitations: deref(req.Limitations)})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toComparator(c))
}

func (rt *Router) handleListShadowRuns(w http.ResponseWriter, r *http.Request) {
	if rt.shadowUnavailable(w, r) {
		return
	}
	views, err := rt.Shadow.Runs(r.Context(), limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.ShadowRunList{Runs: make([]gen.ShadowRun, len(views))}
	for i, v := range views {
		out.Runs[i] = toShadowRun(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleExecuteShadowRun(w http.ResponseWriter, r *http.Request) {
	if rt.shadowUnavailable(w, r) {
		return
	}
	var req gen.ShadowRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	comparatorID, err := id.ParseComparatorID(req.ComparatorID)
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("comparatorId", errs.ReasonInvalidValue, "That is not a valid comparator identifier."))
		return
	}
	from, err := parseTimestamp("windowFrom", req.WindowFrom)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	to, err := parseTimestamp("windowTo", req.WindowTo)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	cur := fiscal.Currency(req.Currency)
	money := func(field, s string) (fiscal.Money, error) {
		if err := parseDecimal(field, s); err != nil {
			return fiscal.Money{}, err
		}
		m, err := fiscal.ParseMoney(s, cur)
		if err != nil {
			return fiscal.Money{}, errs.Invalid(field, errs.ReasonInvalidValue, "That is not an amount in the run's currency.")
		}
		return m, nil
	}
	in := app.RunInput{Comparator: comparatorID, Mode: shadow.Mode(req.Mode), WindowFrom: from, WindowTo: to,
		Policy: shadow.Policy{Currency: cur, ComponentMap: map[string]string{}}}
	for _, m := range req.ComponentMap {
		if _, dup := in.Policy.ComponentMap[m.ExternalCode]; dup {
			writeProblem(w, r, rt.log, errs.Invalid("componentMap", errs.ReasonInvalidValue, "A comparator code is mapped once."))
			return
		}
		in.Policy.ComponentMap[m.ExternalCode] = m.Slot
	}
	if in.Policy.Tolerance, err = money("tolerance", req.Tolerance); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if in.Policy.Materiality, err = money("materiality", req.Materiality); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	for _, o := range req.Observations {
		obs := shadow.Observation{CaseRef: o.CaseRef, Components: map[string]fiscal.Money{}}
		if obs.Total, err = money("observations.total", o.Total); err != nil {
			writeProblem(w, r, rt.log, err)
			return
		}
		for _, c := range o.Components {
			if _, dup := obs.Components[c.Code]; dup {
				writeProblem(w, r, rt.log, errs.Invalid("observations.components", errs.ReasonInvalidValue,
					"A comparator result names each component code once."))
				return
			}
			m, err := money("observations.components.amount", c.Amount)
			if err != nil {
				writeProblem(w, r, rt.log, err)
				return
			}
			obs.Components[c.Code] = m
		}
		in.Observations = append(in.Observations, obs)
	}
	v, err := rt.Shadow.ExecuteRun(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toShadowRun(v))
}

func (rt *Router) handleGetShadowRun(w http.ResponseWriter, r *http.Request) {
	if rt.shadowUnavailable(w, r) {
		return
	}
	runID, err := id.ParseShadowRunID(r.PathValue("shadowRunId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("shadowRunId", errs.ReasonInvalidValue, "That is not a valid shadow run identifier."))
		return
	}
	v, err := rt.Shadow.Run(r.Context(), runID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toShadowRun(v))
}

func (rt *Router) handleMoveDiscrepancy(w http.ResponseWriter, r *http.Request) {
	if rt.shadowUnavailable(w, r) {
		return
	}
	discrepancyID, err := id.ParseDiscrepancyID(r.PathValue("discrepancyId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("discrepancyId", errs.ReasonInvalidValue, "That is not a valid discrepancy identifier."))
		return
	}
	var req gen.DiscrepancyTransitionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	var (
		severity shadow.Severity
		cause    shadow.RootCause
	)
	if req.Severity != nil {
		severity = shadow.Severity(*req.Severity)
	}
	if req.RootCause != nil {
		cause = shadow.RootCause(*req.RootCause)
	}
	d, err := rt.Shadow.MoveDiscrepancy(r.Context(), discrepancyID, shadow.State(req.To), severity, cause, req.Note)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toDiscrepancy(d))
}
