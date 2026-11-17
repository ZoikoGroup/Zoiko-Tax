package http

import (
	"net/http"
	"strconv"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/support"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The support case surface (Engineering Doc 18, W16, G-052).

func (rt *Router) supportUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Support != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"Support cases are not wired in this cell. The request was not applied."))
	return true
}

func supportOptUser(u id.UserID) *string {
	if u.IsZero() {
		return nil
	}
	s := u.String()
	return &s
}

func supportOptID(zero bool, s string) *string {
	if zero {
		return nil
	}
	return &s
}

func toSupportCase(v app.SupportCaseView) gen.SupportCase {
	c := v.Case
	out := gen.SupportCase{
		ID: c.ID.String(), Type: gen.SupportCaseType(c.Type), OpenedType: gen.SupportCaseType(c.OpenedType),
		Severity: gen.SupportSeverity(c.Severity), Subject: c.Subject, Description: c.Description,
		Product: optStr(c.Product), LegalEntityID: supportOptID(c.LegalEntity.IsZero(), c.LegalEntity.String()),
		FiscalPeriod: optStr(c.Period), ContractRef: optStr(c.ContractRef), SellerRef: optStr(c.SellerRef),
		References: gen.SupportCaseReferences{
			DecisionID:   supportOptID(c.Refs.Decision.IsZero(), c.Refs.Decision.String()),
			DocumentID:   supportOptID(c.Refs.Document.IsZero(), c.Refs.Document.String()),
			ObligationID: supportOptID(c.Refs.Obligation.IsZero(), c.Refs.Obligation.String()),
			RemittanceID: supportOptID(c.Refs.Remittance.IsZero(), c.Refs.Remittance.String()),
		},
		Assignee: supportOptUser(c.Assignee), Evidence: append([]string{}, c.Evidence...), Status: gen.SupportCaseStatus(c.Status),
		MissingFacts: make([]gen.SupportFact, len(c.MissingFacts)),
		Clocks: gen.SupportCaseClocks{
			SLADueAt: canonical.FormatTime(v.Clocks.SLADueAt), SLA: gen.SupportClockState(v.Clocks.SLA),
			FirstResponseAt: optTime(v.Clocks.FirstResponseAt), StatutoryDueAt: optTime(v.Clocks.StatutoryDueAt),
			Statutory: gen.SupportClockState(v.Clocks.Statutory), StatutoryPrecedes: v.Clocks.StatutoryPrecedes,
			DueAt: canonical.FormatTime(v.Clocks.DueAt),
		},
		Actions:         make([]gen.SupportCaseAction, len(v.Actions)),
		SignOffRequired: v.SignOffRequired, SignedOff: v.SignedOff, Responded: v.Responded,
		OpenedAt: canonical.FormatTime(c.OpenedAt), OpenedBy: c.OpenedBy.String(),
		History: make([]gen.SupportCaseEvent, len(c.History)),
	}
	if c.EscalatedTo != "" {
		e := gen.SupportEscalationTarget(c.EscalatedTo)
		out.EscalatedTo = &e
	}
	for i, f := range c.MissingFacts {
		out.MissingFacts[i] = gen.SupportFact(f)
	}
	for i, a := range v.Actions {
		out.Actions[i] = gen.SupportCaseAction(a)
	}
	for i, e := range c.History {
		ev := gen.SupportCaseEvent{
			Seq: saturate32(e.Seq), Action: gen.SupportCaseAction(e.Action), Status: gen.SupportCaseStatus(e.Status),
			Note: optStr(e.Note), LegalEntityID: supportOptID(e.LegalEntity.IsZero(), e.LegalEntity.String()),
			FiscalPeriod: optStr(e.Period), ContractRef: optStr(e.ContractRef), SellerRef: optStr(e.SellerRef),
			StatutoryDueAt: optTime(e.StatutoryDueAt), SLADueAt: optTime(e.SLADueAt), Assignee: supportOptUser(e.Assignee),
			RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String(),
		}
		if e.Type != "" {
			t := gen.SupportCaseType(e.Type)
			ev.Type = &t
		}
		if e.Severity != "" {
			s := gen.SupportSeverity(e.Severity)
			ev.Severity = &s
		}
		if e.EscalateTo != "" {
			t := gen.SupportEscalationTarget(e.EscalateTo)
			ev.EscalateTo = &t
		}
		if len(e.Evidence) > 0 {
			ev.Evidence = append([]string{}, e.Evidence...)
		}
		for _, f := range e.MissingFacts {
			ev.MissingFacts = append(ev.MissingFacts, gen.SupportFact(f))
		}
		if e.Reopens > 0 {
			n := saturate32(e.Reopens)
			ev.Reopens = &n
		}
		out.History[i] = ev
	}
	return out
}

func (rt *Router) supportCaseID(w http.ResponseWriter, r *http.Request) (id.SupportCaseID, bool) {
	caseID, err := id.ParseSupportCaseID(r.PathValue("caseId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("caseId", errs.ReasonInvalidValue, "That is not a valid support case identifier."))
		return id.SupportCaseID{}, false
	}
	return caseID, true
}

// parseOptionalID reads an optional identifier field; a nil pointer is the
// zero value.
func supportParseID[T any](field string, s *string, parse func(string) (T, error)) (T, error) {
	var zero T
	if s == nil || *s == "" {
		return zero, nil
	}
	v, err := parse(*s)
	if err != nil {
		return zero, errs.Invalid(field, errs.ReasonInvalidValue, "That is not a valid identifier.")
	}
	return v, nil
}

func supportParseTime(field string, s *string) (time.Time, error) {
	if s == nil || *s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		return time.Time{}, errs.Invalid(field, errs.ReasonInvalidValue, "A time is RFC 3339, in UTC.")
	}
	return t, nil
}

func (rt *Router) handleListSupportCases(w http.ResponseWriter, r *http.Request) {
	if rt.supportUnavailable(w, r) {
		return
	}
	q := r.URL.Query()
	f := app.SupportCaseFilter{
		Type: support.Type(q.Get("type")), Severity: support.Severity(q.Get("severity")), Status: support.Status(q.Get("status")),
		Product: q.Get("product"), Period: q.Get("fiscalPeriod"), SLA: support.ClockState(q.Get("slaClock")),
		Statutory: support.ClockState(q.Get("statutoryClock")), Limit: limitOf(r),
	}
	invalid := func(field, detail string) {
		writeProblem(w, r, rt.log, errs.Invalid(field, errs.ReasonInvalidValue, detail))
	}
	switch {
	case f.Type != "" && !f.Type.Valid():
		invalid("type", "That is not a support case type.")
		return
	case f.Severity != "" && !f.Severity.Valid():
		invalid("severity", "Severity is S1, S2, S3 or S4.")
		return
	case f.Status != "" && !f.Status.Valid():
		invalid("status", "That is not a support case status.")
		return
	case f.Period != "" && !support.ValidPeriod(f.Period):
		invalid("fiscalPeriod", "A fiscal period is a month, YYYY-MM.")
		return
	case f.SLA != "" && !gen.SupportClockState(f.SLA).Valid():
		invalid("slaClock", "That is not a clock state.")
		return
	case f.Statutory != "" && !gen.SupportClockState(f.Statutory).Valid():
		invalid("statutoryClock", "That is not a clock state.")
		return
	}
	if s := q.Get("legalEntityId"); s != "" {
		le, err := id.ParseLegalEntityID(s)
		if err != nil {
			invalid("legalEntityId", "That is not a valid legal entity identifier.")
			return
		}
		f.LegalEntity = le
	}
	if s := q.Get("assignee"); s != "" {
		u, err := id.ParseUserID(s)
		if err != nil {
			invalid("assignee", "That is not a valid user identifier.")
			return
		}
		f.Assignee = u
	}
	if s := q.Get("unassigned"); s != "" {
		b, err := strconv.ParseBool(s)
		if err != nil {
			invalid("unassigned", "unassigned is true or false.")
			return
		}
		f.Unassigned = b
	}
	views, err := rt.Support.Cases(r.Context(), f)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.SupportCaseList{Cases: make([]gen.SupportCase, len(views))}
	for i, v := range views {
		out.Cases[i] = toSupportCase(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleOpenSupportCase(w http.ResponseWriter, r *http.Request) {
	if rt.supportUnavailable(w, r) {
		return
	}
	var req gen.SupportCaseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.SupportCaseInput{
		Type: support.Type(req.Type), Severity: support.Severity(req.Severity), Subject: req.Subject,
		Description: req.Description, Product: deref(req.Product), Period: deref(req.FiscalPeriod),
		ContractRef: deref(req.ContractRef), SellerRef: deref(req.SellerRef), Evidence: req.Evidence,
	}
	var err error
	if in.LegalEntity, err = supportParseID("legalEntityId", req.LegalEntityID, id.ParseLegalEntityID); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if in.Assignee, err = supportParseID("assignee", req.Assignee, id.ParseUserID); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if in.StatutoryDueAt, err = supportParseTime("statutoryDueAt", req.StatutoryDueAt); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if req.References != nil {
		if in.Refs, err = supportReferences(*req.References); err != nil {
			writeProblem(w, r, rt.log, err)
			return
		}
	}
	v, err := rt.Support.OpenCase(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toSupportCase(v))
}

func supportReferences(refs gen.SupportCaseReferences) (support.References, error) {
	var (
		out support.References
		err error
	)
	if out.Decision, err = supportParseID("references.decisionId", refs.DecisionID, id.ParseDecisionID); err != nil {
		return out, err
	}
	if out.Document, err = supportParseID("references.documentId", refs.DocumentID, id.ParseFiscalDocumentID); err != nil {
		return out, err
	}
	if out.Obligation, err = supportParseID("references.obligationId", refs.ObligationID, id.ParseObligationID); err != nil {
		return out, err
	}
	if out.Remittance, err = supportParseID("references.remittanceId", refs.RemittanceID, id.ParseRemittanceID); err != nil {
		return out, err
	}
	return out, nil
}

func (rt *Router) handleGetSupportCase(w http.ResponseWriter, r *http.Request) {
	if rt.supportUnavailable(w, r) {
		return
	}
	caseID, ok := rt.supportCaseID(w, r)
	if !ok {
		return
	}
	v, err := rt.Support.Case(r.Context(), caseID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toSupportCase(v))
}

func (rt *Router) handleActOnSupportCase(w http.ResponseWriter, r *http.Request) {
	if rt.supportUnavailable(w, r) {
		return
	}
	caseID, ok := rt.supportCaseID(w, r)
	if !ok {
		return
	}
	var req gen.SupportCaseActionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	a := support.Act{
		Action: support.Action(req.Action), Note: deref(req.Note), Period: deref(req.FiscalPeriod),
		ContractRef: deref(req.ContractRef), SellerRef: deref(req.SellerRef), Evidence: req.Evidence,
	}
	if req.Type != nil {
		a.Type = support.Type(*req.Type)
	}
	if req.Severity != nil {
		a.Severity = support.Severity(*req.Severity)
	}
	if req.EscalateTo != nil {
		a.EscalateTo = support.EscalationTarget(*req.EscalateTo)
	}
	var err error
	if a.LegalEntity, err = supportParseID("legalEntityId", req.LegalEntityID, id.ParseLegalEntityID); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if a.Assignee, err = supportParseID("assignee", req.Assignee, id.ParseUserID); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if a.StatutoryDueAt, err = supportParseTime("statutoryDueAt", req.StatutoryDueAt); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Support.ActOnCase(r.Context(), caseID, a)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toSupportCase(v))
}
