package http

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/classification"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/crosswalk"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rulechange"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/sourcing"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The content workbench surface: vendor crosswalks (Doc 02 WF-06/WF-07) and
// the rule approval queue (Doc 03 §07; Sidebar WF-02). Enumerations cross it
// as the strings the domain names them.

func (rt *Router) contentWorkbenchUnavailable(w http.ResponseWriter, r *http.Request, svc bool) bool {
	if svc {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"The content workbench is not wired in this cell. The request was not applied."))
	return true
}

// ---------------------------------------------------------------------------
// crosswalks
// ---------------------------------------------------------------------------

func toCrosswalkEntry(e crosswalk.Entry) gen.CrosswalkEntry {
	return gen.CrosswalkEntry{
		ExternalCode: e.ExternalCode, Description: e.Description, Match: gen.CrosswalkMatch(e.Match),
		ServiceTarget: optStr(string(e.ServiceTarget)), LevyHint: optStr(e.LevyHint), Predicate: optStr(e.Predicate),
		Note: optStr(e.Note),
	}
}

func fromCrosswalkEntry(e gen.CrosswalkEntry) crosswalk.Entry {
	return crosswalk.Entry{
		ExternalCode: e.ExternalCode, Description: e.Description, Match: crosswalk.Match(e.Match),
		ServiceTarget: classification.OntologyID(deref(e.ServiceTarget)), LevyHint: deref(e.LevyHint),
		Predicate: deref(e.Predicate), Note: deref(e.Note),
	}
}

func toCrosswalk(v app.CrosswalkView) gen.Crosswalk {
	c := v.Crosswalk
	out := gen.Crosswalk{
		ID: c.ID.String(), Vendor: c.Vendor, Namespace: c.Namespace, VendorRelease: c.VendorRelease, Title: c.Title,
		AdvisoryOnly: true, Versions: make([]gen.CrosswalkVersion, len(c.Versions)), AsOf: canonical.FormatTime(v.AsOf),
		CreatedAt: canonical.FormatTime(c.CreatedAt), CreatedBy: c.CreatedBy.String(),
	}
	if cur, ok := c.Current(); ok {
		n := saturate32(cur.Number)
		out.CurrentVersion = &n
	}
	for i, ver := range c.Versions {
		gv := gen.CrosswalkVersion{
			Version: saturate32(ver.Number), Status: gen.CrosswalkStatus(ver.Status), Revision: saturate32(ver.Revision()),
			Rights: gen.CrosswalkRights{Basis: gen.CrosswalkRightsBasis(ver.Rights.Basis), Reference: ver.Rights.Reference,
				ExpiresOn: civilOut(ver.Rights.ExpiresOn), EvidenceSha256: optStr(ver.Rights.EvidenceSHA256)},
			EffectiveFrom: ver.EffectiveFrom.Format(time.DateOnly), EffectiveTo: civilOut(ver.EffectiveTo),
			ContentDigest: ver.Digest.String(), Entries: make([]gen.CrosswalkEntry, len(ver.Entries)),
			History: make([]gen.CrosswalkEvent, len(ver.History)), CreatedAt: canonical.FormatTime(ver.CreatedAt),
			CreatedBy: ver.CreatedBy.String(),
		}
		for j, e := range ver.Entries {
			gv.Entries[j] = toCrosswalkEntry(e)
		}
		for j, e := range ver.History {
			gv.History[j] = gen.CrosswalkEvent{Seq: saturate32(e.Seq), Kind: gen.CrosswalkEventKind(e.Kind),
				Status: gen.CrosswalkStatus(e.Status), Reason: optStr(e.Reason), RecordedAt: canonical.FormatTime(e.RecordedAt),
				RecordedBy: e.RecordedBy.String()}
		}
		if s, ok := ver.Submitter(); ok {
			by := s.String()
			gv.SubmittedBy = &by
		}
		if by, at, ok := ver.Approver(); ok {
			b, a := by.String(), canonical.FormatTime(at)
			gv.ApprovedBy, gv.ApprovedAt = &b, &a
		}
		out.Versions[i] = gv
	}
	return out
}

func (rt *Router) crosswalkID(w http.ResponseWriter, r *http.Request) (id.CrosswalkID, bool) {
	cid, err := id.ParseCrosswalkID(r.PathValue("crosswalkId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("crosswalkId", errs.ReasonInvalidValue, "That is not a valid crosswalk identifier."))
		return id.CrosswalkID{}, false
	}
	return cid, true
}

func positiveInt(field, s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, errs.Invalid(field, errs.ReasonInvalidValue, "A version is a positive whole number.")
	}
	return n, nil
}

func crosswalkVersionInput(r *http.Request, req gen.CrosswalkVersionRequest) (app.CrosswalkVersionInput, error) {
	in := app.CrosswalkVersionInput{
		IntakeKey: r.Header.Get("Idempotency-Key"), Submit: req.Submit != nil && *req.Submit,
		Rights: crosswalk.Rights{Basis: crosswalk.RightsBasis(req.Rights.Basis), Reference: req.Rights.Reference,
			EvidenceSHA256: deref(req.Rights.EvidenceSha256)},
	}
	var err error
	if req.Rights.ExpiresOn != nil {
		if in.Rights.ExpiresOn, err = parseCivilDate("rights.expiresOn", *req.Rights.ExpiresOn); err != nil {
			return in, err
		}
	}
	if in.EffectiveFrom, err = parseCivilDate("effectiveFrom", req.EffectiveFrom); err != nil {
		return in, err
	}
	if req.EffectiveTo != nil {
		if in.EffectiveTo, err = parseCivilDate("effectiveTo", *req.EffectiveTo); err != nil {
			return in, err
		}
	}
	if len(req.Entries) > crosswalk.MaxEntries {
		return in, errs.Invalid("entries", errs.ReasonInvalidValue, "A crosswalk version maps at most 2000 codes.")
	}
	in.Entries = make([]crosswalk.Entry, len(req.Entries))
	for i, e := range req.Entries {
		in.Entries[i] = fromCrosswalkEntry(e)
	}
	return in, nil
}

func (rt *Router) handleListCrosswalks(w http.ResponseWriter, r *http.Request) {
	if rt.contentWorkbenchUnavailable(w, r, rt.Crosswalks != nil) {
		return
	}
	status := crosswalk.Status(r.URL.Query().Get("status"))
	if status != "" && !status.Valid() {
		writeProblem(w, r, rt.log, errs.Invalid("status", errs.ReasonInvalidValue, "That is not a crosswalk status."))
		return
	}
	got, err := rt.Crosswalks.Crosswalks(r.Context(), r.URL.Query().Get("vendor"), status, limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.CrosswalkList{Crosswalks: make([]gen.Crosswalk, len(got))}
	for i, v := range got {
		out.Crosswalks[i] = toCrosswalk(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleCreateCrosswalk(w http.ResponseWriter, r *http.Request) {
	if rt.contentWorkbenchUnavailable(w, r, rt.Crosswalks != nil) {
		return
	}
	var req gen.CrosswalkRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	ver, err := crosswalkVersionInput(r, req.Version)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, created, err := rt.Crosswalks.CreateCrosswalk(r.Context(), app.CrosswalkInput{
		Vendor: req.Vendor, Namespace: req.Namespace, VendorRelease: req.VendorRelease, Title: req.Title, Version: ver,
	})
	rt.writeCrosswalk(w, r, v, created, err)
}

func (rt *Router) writeCrosswalk(w http.ResponseWriter, r *http.Request, v app.CrosswalkView, created bool, err error) {
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toCrosswalk(v))
}

func (rt *Router) handleGetCrosswalk(w http.ResponseWriter, r *http.Request) {
	if rt.contentWorkbenchUnavailable(w, r, rt.Crosswalks != nil) {
		return
	}
	cid, ok := rt.crosswalkID(w, r)
	if !ok {
		return
	}
	v, err := rt.Crosswalks.Crosswalk(r.Context(), cid)
	rt.writeCrosswalk(w, r, v, false, err)
}

func (rt *Router) handleAddCrosswalkVersion(w http.ResponseWriter, r *http.Request) {
	if rt.contentWorkbenchUnavailable(w, r, rt.Crosswalks != nil) {
		return
	}
	cid, ok := rt.crosswalkID(w, r)
	if !ok {
		return
	}
	var req gen.CrosswalkVersionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in, err := crosswalkVersionInput(r, req)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, created, err := rt.Crosswalks.AddCrosswalkVersion(r.Context(), cid, in)
	rt.writeCrosswalk(w, r, v, created, err)
}

func (rt *Router) handleTransitionCrosswalkVersion(w http.ResponseWriter, r *http.Request) {
	if rt.contentWorkbenchUnavailable(w, r, rt.Crosswalks != nil) {
		return
	}
	cid, ok := rt.crosswalkID(w, r)
	if !ok {
		return
	}
	n, err := positiveInt("version", r.PathValue("version"))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	var req gen.ContentTransitionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Crosswalks.TransitionCrosswalkVersion(r.Context(), cid, n, crosswalk.Status(req.To), deref(req.Reason),
		int(req.ExpectedRevision))
	rt.writeCrosswalk(w, r, v, false, err)
}

func (rt *Router) handleLookupCrosswalk(w http.ResponseWriter, r *http.Request) {
	if rt.contentWorkbenchUnavailable(w, r, rt.Crosswalks != nil) {
		return
	}
	cid, ok := rt.crosswalkID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var (
		day time.Time
		n   int
		err error
	)
	if s := q.Get("on"); s != "" {
		day, err = parseCivilDate("on", s)
	}
	if s := q.Get("atVersion"); err == nil && s != "" {
		n, err = positiveInt("atVersion", s)
	}
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	got, err := rt.Crosswalks.LookupCrosswalk(r.Context(), cid, q.Get("code"), day, n)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.CrosswalkLookup{
		CrosswalkID: got.Crosswalk.String(), Code: got.Code, On: got.On.Format(time.DateOnly),
		Resolution: gen.CrosswalkResolution(got.Resolution), AdvisoryOnly: true,
		Candidates: make([]gen.CrosswalkEntry, len(got.Candidates)), Detail: got.Detail, AsOf: canonical.FormatTime(got.AsOf),
	}
	if got.Version > 0 {
		v := saturate32(got.Version)
		out.Version = &v
	}
	for i, e := range got.Candidates {
		out.Candidates[i] = toCrosswalkEntry(e)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// rule changes
// ---------------------------------------------------------------------------

func toTestEvidence(t *rulechange.TestEvidence) *gen.RuleChangeTestEvidence {
	if t == nil {
		return nil
	}
	return &gen.RuleChangeTestEvidence{Ref: t.Ref, Sha256: optStr(t.SHA256)}
}

func toRuleChange(v app.RuleChangeView) gen.RuleChange {
	p := v.Proposal
	out := gen.RuleChange{
		ID: p.ID.String(), PackID: string(p.Pack), BaseVersion: p.BaseVersion.String(), RuleID: optStr(string(p.Rule)),
		Jurisdiction: p.Jurisdiction, ChangeType: gen.RuleChangeType(p.ChangeType), Summary: p.Summary, Detail: p.Detail,
		Impact: optStr(p.Impact),
		Citation: gen.RuleChangeCitation{SourceID: string(p.Citation.Source), Reference: p.Citation.Reference,
			Edition: p.Citation.Edition, AuthorityClass: gen.SourceAuthorityClass(p.Citation.Authority)},
		PublishedOn: civilOut(p.PublishedOn), EffectiveFrom: p.EffectiveFrom.Format(time.DateOnly),
		EffectiveTo: civilOut(p.EffectiveTo), OriginRef: optStr(p.OriginRef), Status: gen.RuleChangeStatus(p.Status),
		Revision: saturate32(p.Revision()), TestEvidence: toTestEvidence(p.TestEvidence),
		ReleaseEligible: p.Status == rulechange.StatusApprovedForRelease,
		History:         make([]gen.RuleChangeEvent, len(p.History)), AsOf: canonical.FormatTime(v.AsOf),
		CreatedAt: canonical.FormatTime(p.CreatedAt), CreatedBy: p.CreatedBy.String(),
	}
	if !p.Supersedes.IsZero() {
		s := p.Supersedes.String()
		out.Supersedes = &s
	}
	if by, _, ok := p.Recommender(); ok {
		s := by.String()
		out.RecommendedBy = &s
	}
	if by, at, ok := p.Approver(); ok {
		b, a := by.String(), canonical.FormatTime(at)
		out.ApprovedBy, out.ApprovedAt = &b, &a
	}
	for i, e := range p.History {
		out.History[i] = gen.RuleChangeEvent{Seq: saturate32(e.Seq), Kind: gen.RuleChangeEventKind(e.Kind),
			Status: gen.RuleChangeStatus(e.Status), Reason: optStr(e.Reason), TestEvidence: toTestEvidence(e.TestEvidence),
			RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String()}
	}
	return out
}

func (rt *Router) ruleChangeID(w http.ResponseWriter, r *http.Request) (id.RuleChangeID, bool) {
	cid, err := id.ParseRuleChangeID(r.PathValue("changeId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("changeId", errs.ReasonInvalidValue, "That is not a valid rule change identifier."))
		return id.RuleChangeID{}, false
	}
	return cid, true
}

func (rt *Router) writeRuleChange(w http.ResponseWriter, r *http.Request, v app.RuleChangeView, created bool, err error) {
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, r, rt.log, status, toRuleChange(v))
}

func (rt *Router) handleListRuleChanges(w http.ResponseWriter, r *http.Request) {
	if rt.contentWorkbenchUnavailable(w, r, rt.RuleChanges != nil) {
		return
	}
	q := r.URL.Query()
	f := rulechange.Filter{Status: rulechange.Status(q.Get("status")), Pack: q.Get("packId"),
		Jurisdiction: strings.ToUpper(q.Get("jurisdiction"))}
	switch {
	case f.Status != "" && !f.Status.Valid():
		writeProblem(w, r, rt.log, errs.Invalid("status", errs.ReasonInvalidValue, "That is not a rule change status."))
		return
	case f.Jurisdiction != "" && !rulechange.ValidJurisdiction(f.Jurisdiction):
		writeProblem(w, r, rt.log, errs.Invalid("jurisdiction", errs.ReasonInvalidValue, "A jurisdiction is a country, or a country and one subdivision."))
		return
	case f.Pack != "" && content.PackID(f.Pack).Validate() != nil:
		writeProblem(w, r, rt.log, errs.Invalid("packId", errs.ReasonInvalidValue, "That is not a rule pack id."))
		return
	}
	got, err := rt.RuleChanges.RuleChanges(r.Context(), f, limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.RuleChangeList{RuleChanges: make([]gen.RuleChange, len(got))}
	for i, v := range got {
		out.RuleChanges[i] = toRuleChange(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleProposeRuleChange(w http.ResponseWriter, r *http.Request) {
	if rt.contentWorkbenchUnavailable(w, r, rt.RuleChanges != nil) {
		return
	}
	var req gen.RuleChangeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.RuleChangeInput{
		IntakeKey: r.Header.Get("Idempotency-Key"), Pack: req.PackID, BaseVersion: req.BaseVersion, Rule: deref(req.RuleID),
		Jurisdiction: req.Jurisdiction, ChangeType: rulechange.ChangeType(req.ChangeType), Summary: req.Summary,
		Detail: req.Detail, Impact: deref(req.Impact), OriginRef: deref(req.OriginRef),
		Citation: rulechange.Citation{Source: sourcing.SourceID(req.Citation.SourceID), Reference: req.Citation.Reference,
			Edition: req.Citation.Edition, Authority: content.AuthorityClass(req.Citation.AuthorityClass)},
	}
	if req.TestEvidence != nil {
		in.TestEvidence = &rulechange.TestEvidence{Ref: req.TestEvidence.Ref, SHA256: deref(req.TestEvidence.Sha256)}
	}
	err := func() error {
		var err error
		if in.EffectiveFrom, err = parseCivilDate("effectiveFrom", req.EffectiveFrom); err != nil {
			return err
		}
		if req.EffectiveTo != nil {
			if in.EffectiveTo, err = parseCivilDate("effectiveTo", *req.EffectiveTo); err != nil {
				return err
			}
		}
		if req.PublishedOn != nil {
			if in.PublishedOn, err = parseCivilDate("publishedOn", *req.PublishedOn); err != nil {
				return err
			}
		}
		if req.Supersedes != nil {
			if in.Supersedes, err = id.ParseRuleChangeID(*req.Supersedes); err != nil {
				return errs.Invalid("supersedes", errs.ReasonInvalidValue, "That is not a valid rule change identifier.")
			}
		}
		return nil
	}()
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, created, err := rt.RuleChanges.ProposeRuleChange(r.Context(), in)
	rt.writeRuleChange(w, r, v, created, err)
}

func (rt *Router) handleGetRuleChange(w http.ResponseWriter, r *http.Request) {
	if rt.contentWorkbenchUnavailable(w, r, rt.RuleChanges != nil) {
		return
	}
	cid, ok := rt.ruleChangeID(w, r)
	if !ok {
		return
	}
	v, err := rt.RuleChanges.RuleChange(r.Context(), cid)
	rt.writeRuleChange(w, r, v, false, err)
}

func (rt *Router) actRuleChange(w http.ResponseWriter, r *http.Request, req any,
	act func(context.Context, id.RuleChangeID) (app.RuleChangeView, error)) {
	if rt.contentWorkbenchUnavailable(w, r, rt.RuleChanges != nil) {
		return
	}
	cid, ok := rt.ruleChangeID(w, r)
	if !ok {
		return
	}
	if err := decodeJSON(r, req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := act(r.Context(), cid)
	rt.writeRuleChange(w, r, v, false, err)
}

func (rt *Router) handleRecordRuleChangeTestEvidence(w http.ResponseWriter, r *http.Request) {
	var req gen.RuleChangeTestEvidenceRequest
	rt.actRuleChange(w, r, &req, func(ctx context.Context, cid id.RuleChangeID) (app.RuleChangeView, error) {
		ev := rulechange.TestEvidence{Ref: req.TestEvidence.Ref, SHA256: deref(req.TestEvidence.Sha256)}
		return rt.RuleChanges.RecordRuleChangeTestEvidence(ctx, cid, ev, int(req.ExpectedRevision))
	})
}

func (rt *Router) handleTransitionRuleChange(w http.ResponseWriter, r *http.Request) {
	var req gen.RuleChangeTransitionRequest
	rt.actRuleChange(w, r, &req, func(ctx context.Context, cid id.RuleChangeID) (app.RuleChangeView, error) {
		return rt.RuleChanges.TransitionRuleChange(ctx, cid, rulechange.Status(req.To), req.Reason, int(req.ExpectedRevision))
	})
}
