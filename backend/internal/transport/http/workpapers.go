package http

import (
	"net/http"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/contribution"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/workpaper"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The regulatory workpaper surface (OBL-07, LED-19, OPS-15). Amounts cross
// it as the canonical decimal strings the service computed; nothing here
// does arithmetic on one.

func (rt *Router) workpapersUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Workpapers != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"Regulatory workpapers are not wired in this cell. The request was not applied."))
	return true
}

func wpCount(n int) *int32 {
	if n == 0 {
		return nil
	}
	v := saturate32(n)
	return &v
}

func wpUser(u id.UserID) *string {
	if u.IsZero() {
		return nil
	}
	s := u.String()
	return &s
}

func wpTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := canonical.FormatTime(t)
	return &s
}

func toWorkpaperEvidence(e workpaper.Evidence) gen.WorkpaperEvidence {
	return gen.WorkpaperEvidence{Sha256: e.SHA256, Filename: e.Filename, MediaType: e.MediaType}
}

func toWorkpaperFX(r workpaper.FXRate) gen.WorkpaperFXRequest {
	return gen.WorkpaperFXRequest{Currency: string(r.Currency), Rate: r.Rate.CanonicalString(), RateSource: r.Source,
		RateDate: r.RateDate.Format(time.DateOnly), RoundingMode: gen.WorkpaperRoundingMode(r.RoundingMode), Scale: r.Scale,
		ConversionBasis: r.Basis}
}

func toWorkpaperResolution(r workpaper.Resolution) gen.WorkpaperResolution {
	return gen.WorkpaperResolution{ExceptionKey: r.ExceptionKey, Fingerprint: r.Fingerprint, Explanation: r.Explanation,
		Evidence: toWorkpaperEvidence(r.Evidence), State: gen.WorkpaperResolutionState(r.State),
		ProposedAt: canonical.FormatTime(r.ProposedAt), ProposedBy: r.ProposedBy.String(), DecidedAt: wpTime(r.DecidedAt),
		DecidedBy: wpUser(r.DecidedBy), DecisionNote: optStr(r.DecisionNote)}
}

func toWorkpaperRefs(refs []workpaper.SourceRef) []gen.WorkpaperSourceRef {
	out := make([]gen.WorkpaperSourceRef, len(refs))
	for i, s := range refs {
		out[i] = gen.WorkpaperSourceRef{ContributionID: s.ContributionID, SourceEventID: s.SourceEventID,
			SourceVersion: saturate32(s.SourceVersion), SourceRef: s.SourceRef, RuleVersion: s.RuleVersion, Amount: s.Amount,
			Reverses: optStr(s.Reverses)}
	}
	return out
}

func toWorkpaperAdjustmentLines(lines []workpaper.AdjustmentLine) []gen.WorkpaperAdjustmentLine {
	out := make([]gen.WorkpaperAdjustmentLine, len(lines))
	for i, l := range lines {
		out[i] = gen.WorkpaperAdjustmentLine{Number: saturate32(l.Number), Category: l.Category, Amount: l.Amount, Reason: l.Reason,
			EvidenceSha256: l.EvidenceSHA256, Material: l.Material}
	}
	return out
}

func toWorkpaperOutput(o workpaper.Output) gen.WorkpaperOutput {
	out := gen.WorkpaperOutput{
		FormulaVersion: o.FormulaVersion, ContentRelease: o.ContentRelease, ReportingCurrency: o.ReportingCurrency,
		Revenue: make([]gen.WorkpaperRevenueLine, len(o.Revenue)), Excluded: make([]gen.WorkpaperExclusionLine, len(o.Excluded)),
		Volumes: gen.WorkpaperVolumes{Accepted: saturate32(o.Volumes.Accepted), Quarantined: saturate32(o.Volumes.Quarantined),
			Reversals: saturate32(o.Volumes.Reversals), Amendments: saturate32(o.Volumes.Amendments),
			Quantities: make([]gen.WorkpaperQuantityLine, len(o.Volumes.Quantities)),
			BySource:   make([]gen.WorkpaperSourceTypeLine, len(o.Volumes.BySource))},
		Allowances: toWorkpaperAdjustmentLines(o.Allowances), Adjustments: toWorkpaperAdjustmentLines(o.Adjustments),
		ControlTotals:  make([]gen.WorkpaperControlTotal, len(o.ControlTotals)),
		Reconciliation: make([]gen.WorkpaperReconLine, len(o.Reconciliation)),
		Subledger:      make([]gen.WorkpaperBalanceLine, len(o.Subledger)),
		Conversions:    make([]gen.WorkpaperConversion, len(o.Conversions)),
	}
	for i, l := range o.Revenue {
		out.Revenue[i] = gen.WorkpaperRevenueLine{Category: l.Category, Currency: l.Currency, Count: saturate32(l.Count),
			Amount: l.Amount, Converted: optStr(l.Converted), Sources: toWorkpaperRefs(l.Sources)}
	}
	for i, l := range o.Excluded {
		out.Excluded[i] = gen.WorkpaperExclusionLine{Reason: l.Reason, Currency: l.Currency, Count: saturate32(l.Count),
			Amount: l.Amount, Converted: optStr(l.Converted), Sources: toWorkpaperRefs(l.Sources)}
	}
	for i, q := range o.Volumes.Quantities {
		out.Volumes.Quantities[i] = gen.WorkpaperQuantityLine{Unit: q.Unit, Total: q.Total}
	}
	for i, s := range o.Volumes.BySource {
		out.Volumes.BySource[i] = gen.WorkpaperSourceTypeLine{SourceType: gen.ContributionSourceType(s.SourceType),
			Currency: s.Currency, Count: saturate32(s.Count), Eligible: s.Eligible}
	}
	if t := o.Totals; t != nil {
		out.Totals = &gen.WorkpaperTotals{Currency: t.Currency, GrossEligible: t.GrossEligible, Excluded: t.Excluded,
			Allowances: t.Allowances, Adjustments: t.Adjustments, ReportableBase: t.ReportableBase}
	}
	for i, c := range o.ControlTotals {
		ct := gen.WorkpaperControlTotal{Currency: c.Currency, Accepted: saturate32(c.Accepted),
			Quarantined: saturate32(c.Quarantined), Reversals: saturate32(c.Reversals), Eligible: c.Eligible, Excluded: c.Excluded,
			PeriodDigest: c.PeriodDigest, Incomplete: make([]gen.ContributionIncompleteReason, len(c.Incomplete)),
			SourceChecksums: append([]string{}, c.SourceChecksums...)}
		for j, r := range c.Incomplete {
			ct.Incomplete[j] = gen.ContributionIncompleteReason(r)
		}
		out.ControlTotals[i] = ct
	}
	for i, l := range o.Reconciliation {
		out.Reconciliation[i] = gen.WorkpaperReconLine{Code: l.Code, Currency: l.Currency, Expected: l.Expected, Actual: l.Actual,
			Difference: l.Difference, Status: gen.WorkpaperReconStatus(l.Status)}
	}
	for i, l := range o.Subledger {
		out.Subledger[i] = gen.WorkpaperBalanceLine{Account: l.Account, Currency: l.Currency, Debits: l.Debits, Credits: l.Credits}
	}
	for i, c := range o.Conversions {
		out.Conversions[i] = gen.WorkpaperConversion{From: c.From, To: c.To, Rate: c.Rate, RateSource: c.RateSource,
			RateDate: c.RateDate, RoundingMode: gen.WorkpaperRoundingMode(c.RoundingMode), Scale: saturate32(c.Scale), Basis: c.Basis}
	}
	return out
}

func toWorkpaperBuild(b workpaper.Build) gen.WorkpaperBuild {
	out := gen.WorkpaperBuild{Version: saturate32(b.Version), InputsHash: b.InputsHash.String(), OutputHash: b.OutputHash.String(),
		BasisDigest: b.BasisDigest.String(), FormulaVersion: b.FormulaVersion, ContentRelease: b.ContentRelease,
		BuiltAt: canonical.FormatTime(b.BuiltAt), BuiltBy: b.BuiltBy.String()}
	if b.Output != nil {
		o := toWorkpaperOutput(*b.Output)
		out.Output = &o
	}
	return out
}

func toWorkpaperEvent(e workpaper.Event) gen.WorkpaperEvent {
	out := gen.WorkpaperEvent{Seq: saturate32(e.Seq), Kind: gen.WorkpaperEventKind(e.Kind), Status: gen.WorkpaperStatus(e.Status),
		Note: optStr(e.Note), BuildVersion: wpCount(e.BuildVersion), ContentRelease: optStr(e.ContentRelease),
		AdjustmentNumber: wpCount(e.AdjustmentNumber), AdjustmentCategory: optStr(e.AdjustmentCategory),
		ExceptionKey: optStr(e.ExceptionKey), Fingerprint: optStr(e.Fingerprint),
		RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String()}
	if e.FX != nil {
		fx := toWorkpaperFX(*e.FX)
		out.Fx = &fx
	}
	if e.AdjustmentKind != "" {
		k := gen.WorkpaperAdjustmentKind(e.AdjustmentKind)
		out.AdjustmentKind = &k
	}
	if e.AdjustmentAmount != nil {
		a := e.AdjustmentAmount.CanonicalString()
		out.AdjustmentAmount = &a
	}
	if e.Decision != "" {
		d := gen.WorkpaperDecision(e.Decision)
		out.Decision = &d
	}
	if e.Evidence != nil {
		ev := toWorkpaperEvidence(*e.Evidence)
		out.Evidence = &ev
	}
	return out
}

func toWorkpaper(v app.WorkpaperView) gen.Workpaper {
	w := v.Workpaper
	out := gen.Workpaper{
		ID: w.ID.String(), LegalEntityID: w.LegalEntity.String(), Duty: gen.ContributionRegime(w.Duty), Period: w.Period,
		Authority: w.Authority, ReportingCurrency: string(w.ReportingCurrency), MaterialityThreshold: w.Materiality.CanonicalString(),
		InitialContentRelease: w.InitialRelease, ContentRelease: w.ContentRelease, FormulaVersion: workpaper.FormulaVersion,
		Status: gen.WorkpaperStatus(w.Status), EventCount: saturate32(len(w.History)), FrozenBuildVersion: wpCount(w.FrozenBuild),
		CreatedAt: canonical.FormatTime(w.CreatedAt), CreatedBy: w.CreatedBy.String(),
		Builds: make([]gen.WorkpaperBuild, len(w.Builds)), Exceptions: make([]gen.WorkpaperException, len(v.Exceptions)),
		FxRates: []gen.WorkpaperFXRate{}, Adjustments: make([]gen.WorkpaperAdjustment, len(w.Adjustments)),
		Resolutions: make([]gen.WorkpaperResolution, len(w.Resolutions)), History: make([]gen.WorkpaperEvent, len(w.History)),
		AllowedActions: make([]gen.WorkpaperAction, len(v.Actions)), Blockers: make([]gen.WorkpaperBlocker, len(v.Blockers)),
	}
	if !w.Obligation.IsZero() {
		o := w.Obligation.String()
		out.ObligationID = &o
	}
	for i, b := range w.Builds {
		summary := b
		summary.Output = nil
		out.Builds[i] = toWorkpaperBuild(summary)
	}
	if b := w.Latest(); b != nil && b.Output != nil {
		latest := toWorkpaperBuild(*b)
		out.LatestBuild = &latest
	}
	for i, e := range v.Exceptions {
		state := "OPEN"
		switch {
		case e.Resolved():
			state = "RESOLVED"
		case e.Pending():
			state = "PENDING"
		}
		x := gen.WorkpaperException{Key: e.Key, Code: gen.WorkpaperExceptionCode(e.Code), Currency: optStr(e.Currency),
			Difference: optStr(e.Difference), Detail: e.Detail, Resolvable: e.Resolvable, Fingerprint: e.Fingerprint,
			State: gen.WorkpaperExceptionState(state)}
		if e.Resolution != nil {
			r := toWorkpaperResolution(*e.Resolution)
			x.Resolution = &r
		}
		out.Exceptions[i] = x
	}
	for _, r := range w.CurrentFX() {
		fx := toWorkpaperFX(r)
		out.FxRates = append(out.FxRates, gen.WorkpaperFXRate{Currency: fx.Currency, Rate: fx.Rate, RateSource: fx.RateSource,
			RateDate: fx.RateDate, RoundingMode: fx.RoundingMode, Scale: fx.Scale, ConversionBasis: fx.ConversionBasis,
			RecordedAt: canonical.FormatTime(r.RecordedAt), RecordedBy: r.RecordedBy.String()})
	}
	for i, a := range w.Adjustments {
		out.Adjustments[i] = gen.WorkpaperAdjustment{Number: saturate32(a.Number), Kind: gen.WorkpaperAdjustmentKind(a.Kind),
			Category: a.Category, Amount: a.Amount.CanonicalString(), Currency: string(a.Amount.Currency()), Reason: a.Reason,
			Evidence: toWorkpaperEvidence(a.Evidence), Material: a.Material, State: gen.WorkpaperAdjustmentState(a.State),
			ProposedAt: canonical.FormatTime(a.ProposedAt), ProposedBy: a.ProposedBy.String(), DecidedAt: wpTime(a.DecidedAt),
			DecidedBy: wpUser(a.DecidedBy), DecisionNote: optStr(a.DecisionNote)}
	}
	for i, r := range w.Resolutions {
		out.Resolutions[i] = toWorkpaperResolution(r)
	}
	for i, e := range w.History {
		out.History[i] = toWorkpaperEvent(e)
	}
	for i, a := range v.Actions {
		out.AllowedActions[i] = gen.WorkpaperAction(a)
	}
	for i, b := range v.Blockers {
		out.Blockers[i] = gen.WorkpaperBlocker(b)
	}
	return out
}

func (rt *Router) workpaperID(w http.ResponseWriter, r *http.Request) (id.WorkpaperID, bool) {
	workpaperID, err := id.ParseWorkpaperID(r.PathValue("workpaperId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("workpaperId", errs.ReasonInvalidValue, "That is not a valid workpaper identifier."))
		return id.WorkpaperID{}, false
	}
	return workpaperID, true
}

func (rt *Router) handleListWorkpapers(w http.ResponseWriter, r *http.Request) {
	if rt.workpapersUnavailable(w, r) {
		return
	}
	q := r.URL.Query()
	f := workpaper.Filter{Period: q.Get("period"), Duty: contribution.Regime(q.Get("duty")),
		Status: workpaper.Status(q.Get("status")), Limit: limitOf(r)}
	switch {
	case f.Duty != "" && !f.Duty.Valid():
		writeProblem(w, r, rt.log, errs.Invalid("duty", errs.ReasonInvalidValue, "That is not a duty."))
		return
	case f.Status != "" && !f.Status.Valid():
		writeProblem(w, r, rt.log, errs.Invalid("status", errs.ReasonInvalidValue, "That is not a workpaper status."))
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
	got, err := rt.Workpapers.Workpapers(r.Context(), f)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.WorkpaperList{Workpapers: make([]gen.Workpaper, len(got))}
	for i, v := range got {
		out.Workpapers[i] = toWorkpaper(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleCreateWorkpaper(w http.ResponseWriter, r *http.Request) {
	if rt.workpapersUnavailable(w, r) {
		return
	}
	var req gen.WorkpaperRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if !req.Duty.Valid() {
		writeProblem(w, r, rt.log, errs.Invalid("duty", errs.ReasonInvalidValue, "That is not a duty."))
		return
	}
	in := app.WorkpaperInput{Duty: contribution.Regime(req.Duty), Period: req.Period, Authority: req.Authority,
		ReportingCurrency: req.ReportingCurrency, Materiality: deref(req.MaterialityThreshold), ContentRelease: req.ContentRelease}
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
	v, created, err := rt.Workpapers.Create(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toWorkpaper(v))
}

func (rt *Router) handleGetWorkpaper(w http.ResponseWriter, r *http.Request) {
	if rt.workpapersUnavailable(w, r) {
		return
	}
	workpaperID, ok := rt.workpaperID(w, r)
	if !ok {
		return
	}
	v, err := rt.Workpapers.Workpaper(r.Context(), workpaperID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toWorkpaper(v))
}

func (rt *Router) handleBuildWorkpaper(w http.ResponseWriter, r *http.Request) {
	if rt.workpapersUnavailable(w, r) {
		return
	}
	workpaperID, ok := rt.workpaperID(w, r)
	if !ok {
		return
	}
	var req gen.WorkpaperBuildRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	expected := 0
	if req.ExpectedSeq != nil {
		expected = int(*req.ExpectedSeq)
	}
	v, created, err := rt.Workpapers.Build(r.Context(), workpaperID, expected)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toWorkpaper(v))
}

func (rt *Router) handleCheckWorkpaperSources(w http.ResponseWriter, r *http.Request) {
	if rt.workpapersUnavailable(w, r) {
		return
	}
	workpaperID, ok := rt.workpaperID(w, r)
	if !ok {
		return
	}
	c, err := rt.Workpapers.SourceCheck(r.Context(), workpaperID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.WorkpaperSourceCheck{WorkpaperID: workpaperID.String(), CurrentInputsHash: c.CurrentInputsHash.String(),
		CurrentOutputHash: c.CurrentOutput.String(), Matches: c.Matches, CheckedAt: canonical.FormatTime(c.CheckedAt),
		LatestBuildVersion: wpCount(c.LatestVersion), FrozenBuildVersion: wpCount(c.FrozenVersion)}
	if c.LatestVersion > 0 {
		in, o := c.LatestInputsHash.String(), c.LatestOutputHash.String()
		out.LatestInputsHash, out.LatestOutputHash = &in, &o
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleActOnWorkpaper(w http.ResponseWriter, r *http.Request) {
	if rt.workpapersUnavailable(w, r) {
		return
	}
	workpaperID, ok := rt.workpaperID(w, r)
	if !ok {
		return
	}
	var req gen.WorkpaperActionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if !req.Action.Valid() {
		writeProblem(w, r, rt.log, errs.Invalid("action", errs.ReasonInvalidValue, "That is not a workpaper action."))
		return
	}
	a := app.WorkpaperAct{Action: workpaper.Action(req.Action), Note: deref(req.Note), ContentRelease: deref(req.ContentRelease),
		ExceptionKey: deref(req.ExceptionKey), Fingerprint: deref(req.Fingerprint)}
	for _, n := range []struct {
		into *int
		from *int32
	}{{&a.ExpectedSeq, req.ExpectedSeq}, {&a.BuildVersion, req.BuildVersion}, {&a.AdjustmentNumber, req.AdjustmentNumber}} {
		if n.from != nil {
			*n.into = int(*n.from)
		}
	}
	if req.Decision != nil {
		a.Decision = workpaper.Decision(*req.Decision)
	}
	if req.Fx != nil {
		a.FX = &app.FXInput{Currency: req.Fx.Currency, Rate: req.Fx.Rate, RateSource: req.Fx.RateSource, RateDate: req.Fx.RateDate,
			RoundingMode: string(req.Fx.RoundingMode), Scale: int(req.Fx.Scale), ConversionBasis: req.Fx.ConversionBasis}
	}
	if req.Adjustment != nil {
		a.Adjustment = &app.AdjustmentInput{Kind: workpaper.AdjustmentKind(req.Adjustment.Kind), Category: req.Adjustment.Category,
			Amount: req.Adjustment.Amount, Reason: req.Adjustment.Reason}
	}
	if req.Evidence != nil {
		a.Evidence = &workpaper.Evidence{SHA256: req.Evidence.Sha256, Filename: req.Evidence.Filename, MediaType: req.Evidence.MediaType}
	}
	v, err := rt.Workpapers.Act(r.Context(), workpaperID, a)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toWorkpaper(v))
}
