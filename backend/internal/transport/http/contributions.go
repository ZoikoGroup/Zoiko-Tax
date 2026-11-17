package http

import (
	"context"
	"net/http"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/contribution"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The provider contribution surface (OBL-06, OBL-10). Amounts cross it as
// the canonical decimal strings the service computed; nothing here does
// arithmetic on one.

func (rt *Router) contributionsUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Contributions != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"Provider contributions are not wired in this cell. The request was not applied."))
	return true
}

func toContribution(c contribution.Contribution) gen.ProviderContribution {
	out := gen.ProviderContribution{
		ID: c.ID.String(), SourceEventID: c.Source.EventID, SourceVersion: saturate32(c.Source.Version),
		SourceType: gen.ContributionSourceType(c.SourceType), SourceChecksum: optStr(c.SourceChecksum),
		Regime: gen.ContributionRegime(c.Regime), Period: c.Period, LegalEntityID: c.LegalEntity.String(),
		ClassificationRef: c.ClassificationRef, SitusRef: c.SitusRef, SourceRef: c.SourceRef,
		ComponentRef: optStr(c.ComponentRef), RuleVersion: c.RuleVersion, Currency: string(c.Currency()),
		EligibleBase: c.EligibleBase.CanonicalString(), ExcludedBase: c.ExcludedBase.CanonicalString(),
		ExclusionReason: optStr(c.ExclusionReason), Amendment: c.Amendment, ImmutableHash: c.Hash.String(),
		Status: gen.ContributionStatus(c.Status), RecordedAt: canonical.FormatTime(c.RecordedAt),
		RecordedBy: c.RecordedBy.String(), History: make([]gen.ContributionStatusEvent, len(c.History)),
	}
	if !c.Obligation.IsZero() {
		o := c.Obligation.String()
		out.ObligationID = &o
	}
	if c.Quantity != nil {
		q, u := c.Quantity.CanonicalString(), string(c.Quantity.Unit())
		out.Quantity, out.QuantityUnit = &q, &u
	}
	if c.Reverses != nil {
		out.Reverses = &gen.ContributionSourceKey{SourceEventID: c.Reverses.EventID, SourceVersion: saturate32(c.Reverses.Version)}
	}
	for i, e := range c.History {
		out.History[i] = gen.ContributionStatusEvent{
			Seq: saturate32(e.Seq), Kind: gen.ContributionEventKind(e.Kind), Status: gen.ContributionStatus(e.Status),
			Reason: optStr(e.Reason), RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String(),
		}
	}
	return out
}

func toContributionSummary(s app.PeriodSummary) gen.ContributionSummary {
	out := gen.ContributionSummary{Period: s.Period, DataIncomplete: s.DataIncomplete(),
		Groups: make([]gen.ContributionPeriodTotals, len(s.Groups))}
	for i, g := range s.Groups {
		status := gen.COMPLETE
		if g.DataIncomplete() {
			status = gen.DATAINCOMPLETE
		}
		t := gen.ContributionPeriodTotals{
			LegalEntityID: g.LegalEntity.String(), Regime: gen.ContributionRegime(g.Regime), Currency: string(g.Currency),
			Status: status, IncompleteReasons: make([]gen.ContributionIncompleteReason, len(g.Incomplete)),
			AcceptedCount: saturate32(g.Accepted), QuarantinedCount: saturate32(g.Quarantined),
			ReversalCount: saturate32(g.Reversals), AmendmentCount: saturate32(g.Amendments),
			EligibleTotal: g.Eligible.CanonicalString(), ExcludedTotal: g.Excluded.CanonicalString(),
			ReversedTotal: g.Reversed.CanonicalString(), PeriodDigest: g.Digest.String(),
			SourceChecksums: append([]string{}, g.SourceChecksums...),
		}
		for j, reason := range g.Incomplete {
			t.IncompleteReasons[j] = gen.ContributionIncompleteReason(reason)
		}
		out.Groups[i] = t
	}
	return out
}

func (rt *Router) contributionID(w http.ResponseWriter, r *http.Request) (id.ContributionID, bool) {
	contributionID, err := id.ParseContributionID(r.PathValue("contributionId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("contributionId", errs.ReasonInvalidValue, "That is not a valid contribution identifier."))
		return id.ContributionID{}, false
	}
	return contributionID, true
}

func (rt *Router) handleListContributions(w http.ResponseWriter, r *http.Request) {
	if rt.contributionsUnavailable(w, r) {
		return
	}
	q := r.URL.Query()
	f := contribution.Filter{Period: q.Get("period"), Regime: contribution.Regime(q.Get("regime")),
		Status: contribution.Status(q.Get("status")), SourceType: contribution.SourceType(q.Get("sourceType")), Limit: limitOf(r)}
	switch {
	case f.Regime != "" && !f.Regime.Valid():
		writeProblem(w, r, rt.log, errs.Invalid("regime", errs.ReasonInvalidValue, "That is not a contribution regime."))
		return
	case f.Status != "" && !f.Status.Valid():
		writeProblem(w, r, rt.log, errs.Invalid("status", errs.ReasonInvalidValue, "That is not a contribution status."))
		return
	case f.SourceType != "" && !f.SourceType.Valid():
		writeProblem(w, r, rt.log, errs.Invalid("sourceType", errs.ReasonInvalidValue, "That is not a contribution source type."))
		return
	}
	if v := q.Get("legalEntityId"); v != "" {
		le, err := id.ParseLegalEntityID(v)
		if err != nil {
			writeProblem(w, r, rt.log, errs.Invalid("legalEntityId", errs.ReasonInvalidValue, "That is not a valid legal entity identifier."))
			return
		}
		f.LegalEntity = le
	}
	got, err := rt.Contributions.Contributions(r.Context(), f)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.ProviderContributionList{Contributions: make([]gen.ProviderContribution, len(got))}
	for i, c := range got {
		out.Contributions[i] = toContribution(c)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleRecordContribution(w http.ResponseWriter, r *http.Request) {
	if rt.contributionsUnavailable(w, r) {
		return
	}
	var req gen.ContributionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	switch {
	case !req.SourceType.Valid():
		writeProblem(w, r, rt.log, errs.Invalid("sourceType", errs.ReasonInvalidValue, "That is not a contribution source type."))
		return
	case !req.Regime.Valid():
		writeProblem(w, r, rt.log, errs.Invalid("regime", errs.ReasonInvalidValue, "That is not a contribution regime."))
		return
	}
	in := app.ContributionInput{
		SourceEventID: req.SourceEventID, SourceVersion: int(req.SourceVersion), SourceType: contribution.SourceType(req.SourceType),
		SourceChecksum: deref(req.SourceChecksum), Regime: contribution.Regime(req.Regime), Period: req.Period,
		ClassificationRef: req.ClassificationRef, SitusRef: req.SitusRef, SourceRef: req.SourceRef,
		ComponentRef: deref(req.ComponentRef), RuleVersion: req.RuleVersion, Currency: req.Currency,
		EligibleBase: req.EligibleBase, ExcludedBase: deref(req.ExcludedBase), ExclusionReason: deref(req.ExclusionReason),
		Quantity: deref(req.Quantity), QuantityUnit: deref(req.QuantityUnit),
	}
	if req.LegalEntityID != nil {
		le, err := id.ParseLegalEntityID(*req.LegalEntityID)
		if err != nil {
			writeProblem(w, r, rt.log, errs.Invalid("legalEntityId", errs.ReasonInvalidValue, "That is not a valid legal entity identifier."))
			return
		}
		in.LegalEntity = le
	}
	if req.ObligationID != nil {
		o, err := id.ParseObligationID(*req.ObligationID)
		if err != nil {
			writeProblem(w, r, rt.log, errs.Invalid("obligationId", errs.ReasonInvalidValue, "That is not a valid obligation identifier."))
			return
		}
		in.Obligation = o
	}
	if req.Reverses != nil {
		in.Reverses = &contribution.SourceKey{EventID: req.Reverses.SourceEventID, Version: int(req.Reverses.SourceVersion)}
	}
	c, created, err := rt.Contributions.Record(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toContribution(c))
}

func (rt *Router) handleContributionSummary(w http.ResponseWriter, r *http.Request) {
	if rt.contributionsUnavailable(w, r) {
		return
	}
	s, err := rt.Contributions.Summary(r.Context(), r.URL.Query().Get("period"))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toContributionSummary(s))
}

func (rt *Router) handleContributionTrace(w http.ResponseWriter, r *http.Request) {
	if rt.contributionsUnavailable(w, r) {
		return
	}
	contributionID, ok := rt.contributionID(w, r)
	if !ok {
		return
	}
	t, err := rt.Contributions.Trace(r.Context(), contributionID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.ContributionTrace{Contribution: toContribution(t.Contribution),
		OriginalMissing: t.Contribution.IsReversal() && t.Original == nil}
	if t.Original != nil {
		o := toContribution(*t.Original)
		out.Original = &o
	}
	if t.Reversal != nil {
		rv := toContribution(*t.Reversal)
		out.Reversal = &rv
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleQuarantineContribution(w http.ResponseWriter, r *http.Request) {
	rt.moveContribution(w, r, rt.Contributions.Quarantine)
}

func (rt *Router) handleReleaseContribution(w http.ResponseWriter, r *http.Request) {
	rt.moveContribution(w, r, rt.Contributions.Release)
}

func (rt *Router) moveContribution(w http.ResponseWriter, r *http.Request,
	move func(ctx context.Context, contributionID id.ContributionID, reason string) (contribution.Contribution, error)) {
	if rt.contributionsUnavailable(w, r) {
		return
	}
	contributionID, ok := rt.contributionID(w, r)
	if !ok {
		return
	}
	var req gen.ContributionTransitionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	c, err := move(r.Context(), contributionID, req.Reason)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toContribution(c))
}
