package http

import (
	"net/http"
	"strconv"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/notice"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The regulatory notice surface (Engineering Doc 18 W17, G-052; NAV-013).

func (rt *Router) noticesUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Notices != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"Regulatory notices are not wired in this cell. The request was not applied."))
	return true
}

func civilOut(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.Format(time.DateOnly)
	return &s
}

func noticeMoneyOut(m *fiscal.Money) *gen.MoneyValue {
	if m == nil {
		return nil
	}
	return &gen.MoneyValue{Amount: m.CanonicalString(), Currency: string(m.Currency())}
}

func toNoticeDeadline(d notice.Deadline) gen.NoticeDeadline {
	return gen.NoticeDeadline{Kind: gen.NoticeDeadlineKind(d.Kind), DueDate: civilOut(d.Date), DueAt: optTimestamp(d.DueAt()),
		Timezone: d.Timezone, Source: gen.NoticeDeadlineSource(d.Source), Verified: d.Verified}
}

func toNoticeDeadlineEntry(e notice.DeadlineEntry) gen.NoticeDeadlineEntry {
	return gen.NoticeDeadlineEntry{Seq: saturate32(e.Seq), Kind: gen.NoticeDeadlineKind(e.Kind), DueDate: civilOut(e.Date),
		DueAt: optTimestamp(e.DueAt()), Timezone: e.Timezone, Source: gen.NoticeDeadlineSource(e.Source), Verified: e.Verified,
		Evidence: optStr(e.Evidence), AuthorityReference: optStr(e.AuthorityRef), AttachmentSha256: optStr(e.Attachment),
		RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String()}
}

func optUserOut(u id.UserID) *string {
	if u.IsZero() {
		return nil
	}
	return optStr(u.String())
}

func optEntityOut(le id.LegalEntityID) *string {
	if le.IsZero() {
		return nil
	}
	return optStr(le.String())
}

func toNoticeEvent(e notice.Event) gen.NoticeEvent {
	out := gen.NoticeEvent{
		Seq: saturate32(e.Seq), Kind: gen.NoticeEventKind(e.Kind), Status: gen.NoticeStatus(e.Status),
		Note: optStr(e.Note), Evidence: optStr(e.Evidence), AuthorityReference: optStr(e.AuthorityRef),
		LegalEntityID: optEntityOut(e.LegalEntity), OwnerID: optUserOut(e.Owner), SLADue: optTimestamp(e.SLADue),
		ResponseSha256: optStr(e.ResponseSHA256), OutcomeAmount: noticeMoneyOut(e.OutcomeAmount), HearingDate: civilOut(e.HearingDate),
		RecordedAt: canonical.FormatTime(e.RecordedAt), RecordedBy: e.RecordedBy.String(),
	}
	if e.Deadline != nil {
		d := toNoticeDeadline(*e.Deadline)
		out.Deadline = &d
	}
	if e.Authenticity != "" {
		a := gen.NoticeAuthenticity(e.Authenticity)
		out.Authenticity = &a
	}
	if e.Escalation != "" {
		r := gen.NoticeEscalationReason(e.Escalation)
		out.EscalationReason = &r
	}
	if a := e.Attachment; a != nil {
		out.Attachment = &gen.NoticeAttachment{Sha256: a.SHA256, Filename: a.Filename, MediaType: a.MediaType}
	}
	if e.ResponseVersion > 0 {
		v := saturate32(e.ResponseVersion)
		out.ResponseVersion = &v
	}
	if e.Outcome != "" {
		o := gen.NoticeOutcome(e.Outcome)
		settled := e.Settled
		out.Outcome, out.Settled = &o, &settled
	}
	if e.Settlement != "" {
		s := gen.NoticeSettlement(e.Settlement)
		out.Settlement = &s
	}
	return out
}

func toNotice(v app.NoticeView) gen.Notice {
	n := v.Notice
	resolved, ok := n.Entity()
	owner, sla := n.Owner()
	out := gen.Notice{
		ID: n.ID.String(), Type: gen.NoticeType(n.Type), Authority: n.Authority, Jurisdiction: n.Jurisdiction,
		LegalEntityID: optEntityOut(n.LegalEntity), EntityAmbiguous: n.EntityAmbiguous, EntityResolved: ok,
		AuthorityReference: n.AuthorityRef, ReceivedAt: canonical.FormatTime(n.ReceivedAt),
		PeriodStart: civilOut(n.PeriodStart), PeriodEnd: civilOut(n.PeriodEnd), Amount: noticeMoneyOut(n.Amount),
		LinkedCaseID: optStr(n.LinkedCase), Links: make([]gen.NoticeLink, len(n.Links)),
		Status: gen.NoticeStatus(n.Status), Authenticity: gen.NoticeAuthenticity(n.Authenticity()), Escalated: n.Escalated(),
		Flags: make([]gen.NoticeFlag, len(v.Flags)), UrgentLegalReview: v.Urgent,
		StatutoryDeadline: gen.Notice_StatutoryDeadline{Original: toNoticeDeadline(n.Original()), Current: toNoticeDeadline(n.Current())},
		SLA: gen.Notice_SLA{OwnerID: optUserOut(owner), DueAt: optTimestamp(sla),
			Breached: !sla.IsZero() && v.AsOf.After(sla) && !n.Status.Answered() && n.Status != notice.StatusClosed},
		Deadlines: []gen.NoticeDeadlineEntry{}, Attachments: []gen.NoticeAttachment{}, AllowedActions: []gen.NoticeAction{},
		AsOf: canonical.FormatTime(v.AsOf), CreatedAt: canonical.FormatTime(n.CreatedAt), CreatedBy: n.CreatedBy.String(),
		History: make([]gen.NoticeEvent, len(n.History)),
	}
	if ok {
		out.ResolvedLegalEntityID = optEntityOut(resolved)
	}
	for i, l := range n.Links {
		out.Links[i] = gen.NoticeLink{Kind: gen.NoticeLinkKind(l.Kind), Reference: l.Reference}
	}
	for i, f := range v.Flags {
		out.Flags[i] = gen.NoticeFlag(f)
	}
	for _, e := range n.Deadlines() {
		out.Deadlines = append(out.Deadlines, toNoticeDeadlineEntry(e))
	}
	for _, e := range n.Attachments() {
		at := canonical.FormatTime(e.RecordedAt)
		by := e.RecordedBy.String()
		out.Attachments = append(out.Attachments, gen.NoticeAttachment{Sha256: e.Attachment.SHA256, Filename: e.Attachment.Filename,
			MediaType: e.Attachment.MediaType, Note: optStr(e.Note), RecordedAt: &at, RecordedBy: &by})
	}
	for _, a := range n.AllowedActions() {
		out.AllowedActions = append(out.AllowedActions, gen.NoticeAction(a))
	}
	for i, e := range n.History {
		out.History[i] = toNoticeEvent(e)
	}
	return out
}

func (rt *Router) noticeID(w http.ResponseWriter, r *http.Request) (id.NoticeID, bool) {
	noticeID, err := id.ParseNoticeID(r.PathValue("noticeId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("noticeId", errs.ReasonInvalidValue, "That is not a valid notice identifier."))
		return id.NoticeID{}, false
	}
	return noticeID, true
}

func (rt *Router) writeNotice(w http.ResponseWriter, r *http.Request, status int, v app.NoticeView, err error) {
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, status, toNotice(v))
}

func optCivilIn(field string, s *string) (time.Time, error) {
	if s == nil {
		return time.Time{}, nil
	}
	return parseCivilDate(field, *s)
}

func optUserIn(field string, s *string) (id.UserID, error) {
	if s == nil {
		return id.UserID{}, nil
	}
	u, err := id.ParseUserID(*s)
	if err != nil {
		return id.UserID{}, errs.Invalid(field, errs.ReasonInvalidValue, "That is not a valid user identifier.")
	}
	return u, nil
}

func optEntityIn(field string, s *string) (id.LegalEntityID, error) {
	if s == nil {
		return id.LegalEntityID{}, nil
	}
	le, err := id.ParseLegalEntityID(*s)
	if err != nil {
		return id.LegalEntityID{}, errs.Invalid(field, errs.ReasonInvalidValue, "That is not a valid legal entity identifier.")
	}
	return le, nil
}

func optMoneyIn(field string, v *gen.MoneyValue) (*fiscal.Money, error) {
	if v == nil {
		return nil, nil
	}
	m, err := parseMoney(field, *v)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func deadlineIn(field string, d gen.NoticeDeadlineInput) (notice.Deadline, error) {
	if !d.Source.Valid() {
		return notice.Deadline{}, errs.Invalid(field+".source", errs.ReasonInvalidValue, "That is not a deadline source.")
	}
	due, err := optCivilIn(field+".dueDate", d.DueDate)
	if err != nil {
		return notice.Deadline{}, err
	}
	return notice.Deadline{Date: due, Timezone: d.Timezone, Source: notice.DeadlineSource(d.Source), Verified: d.Verified}, nil
}

func (rt *Router) handleListNotices(w http.ResponseWriter, r *http.Request) {
	if rt.noticesUnavailable(w, r) {
		return
	}
	q := r.URL.Query()
	f := app.NoticeFilter{
		Type: notice.Type(q.Get("type")), Status: notice.Status(q.Get("status")), Authority: q.Get("authority"),
		AuthorityRef: q.Get("authorityReference"), Authenticity: notice.Authenticity(q.Get("authenticity")),
		AttachmentSHA: q.Get("attachmentSha256"),
	}
	fail := func(err error) { writeProblem(w, r, rt.log, err) }
	switch {
	case f.Type != "" && !f.Type.Valid():
		fail(errs.Invalid("type", errs.ReasonInvalidValue, "That is not a notice type."))
		return
	case f.Status != "" && !f.Status.Valid():
		fail(errs.Invalid("status", errs.ReasonInvalidValue, "That is not a notice status."))
		return
	case f.Authenticity != "" && !f.Authenticity.Valid():
		fail(errs.Invalid("authenticity", errs.ReasonInvalidValue, "That is not an authenticity status."))
		return
	}
	var err error
	for _, b := range []struct {
		name string
		dst  *bool
	}{{"urgent", &f.UrgentOnly}, {"includeClosed", &f.IncludeClosed}} {
		if s := q.Get(b.name); s != "" {
			if *b.dst, err = strconv.ParseBool(s); err != nil {
				fail(errs.Invalid(b.name, errs.ReasonInvalidValue, "Write true or false."))
				return
			}
		}
	}
	if s := q.Get("legalEntityId"); s != "" {
		if f.LegalEntity, err = optEntityIn("legalEntityId", &s); err != nil {
			fail(err)
			return
		}
	}
	for _, t := range []struct {
		name string
		dst  *time.Time
	}{{"receivedFrom", &f.ReceivedFrom}, {"receivedTo", &f.ReceivedTo}} {
		if s := q.Get(t.name); s != "" {
			if *t.dst, err = parseTimestamp(t.name, s); err != nil {
				fail(err)
				return
			}
		}
	}
	if s := q.Get("period"); s != "" {
		if f.Period, err = parseCivilDate("period", s); err != nil {
			fail(err)
			return
		}
	}
	currency := q.Get("currency")
	for _, b := range []struct {
		name string
		dst  **fiscal.Money
	}{{"amountMin", &f.AmountMin}, {"amountMax", &f.AmountMax}} {
		s := q.Get(b.name)
		if s == "" {
			continue
		}
		if currency == "" {
			fail(errs.Invalid("currency", errs.ReasonMissingField, "An amount bound is compared in the currency named."))
			return
		}
		m, err := parseMoney(b.name, gen.MoneyValue{Amount: s, Currency: currency})
		if err != nil {
			fail(err)
			return
		}
		*b.dst = &m
	}
	views, err := rt.Notices.Notices(r.Context(), f, limitOf(r))
	if err != nil {
		fail(err)
		return
	}
	out := gen.NoticeList{Notices: make([]gen.Notice, len(views))}
	for i, v := range views {
		out.Notices[i] = toNotice(v)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleCreateNotice(w http.ResponseWriter, r *http.Request) {
	if rt.noticesUnavailable(w, r) {
		return
	}
	var req gen.CreateNoticeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in, err := noticeIntakeIn(req)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := rt.Notices.Intake(r.Context(), in)
	rt.writeNotice(w, r, http.StatusCreated, v, err)
}

func noticeIntakeIn(req gen.CreateNoticeRequest) (app.NoticeIntake, error) {
	if !req.Type.Valid() {
		return app.NoticeIntake{}, errs.Invalid("type", errs.ReasonInvalidValue, "That is not a notice type.")
	}
	n := notice.Notice{Type: notice.Type(req.Type), Authority: req.Authority, Jurisdiction: req.Jurisdiction,
		AuthorityRef: req.AuthorityReference, LinkedCase: deref(req.LinkedCaseID)}
	var err error
	if n.LegalEntity, err = optEntityIn("legalEntityId", req.LegalEntityID); err != nil {
		return app.NoticeIntake{}, err
	}
	if req.EntityAmbiguous != nil {
		n.EntityAmbiguous = *req.EntityAmbiguous
	}
	if n.ReceivedAt, err = parseTimestamp("receivedAt", req.ReceivedAt); err != nil {
		return app.NoticeIntake{}, err
	}
	if n.PeriodStart, err = optCivilIn("periodStart", req.PeriodStart); err != nil {
		return app.NoticeIntake{}, err
	}
	if n.PeriodEnd, err = optCivilIn("periodEnd", req.PeriodEnd); err != nil {
		return app.NoticeIntake{}, err
	}
	if n.Amount, err = optMoneyIn("amount", req.Amount); err != nil {
		return app.NoticeIntake{}, err
	}
	for _, l := range req.Links {
		n.Links = append(n.Links, notice.Link{Kind: notice.LinkKind(l.Kind), Reference: l.Reference})
	}
	in := app.NoticeIntake{Notice: n, DeadlineEvidence: deref(req.DeadlineEvidence), Note: deref(req.Note)}
	if in.Deadline, err = deadlineIn("deadline", req.Deadline); err != nil {
		return app.NoticeIntake{}, err
	}
	if in.Owner, err = optUserIn("ownerId", req.OwnerID); err != nil {
		return app.NoticeIntake{}, err
	}
	if in.SLADue, err = optTimestampIn("slaDue", req.SLADue); err != nil {
		return app.NoticeIntake{}, err
	}
	return in, nil
}

func (rt *Router) handleGetNotice(w http.ResponseWriter, r *http.Request) {
	if rt.noticesUnavailable(w, r) {
		return
	}
	noticeID, ok := rt.noticeID(w, r)
	if !ok {
		return
	}
	v, err := rt.Notices.Notice(r.Context(), noticeID)
	rt.writeNotice(w, r, http.StatusOK, v, err)
}

func (rt *Router) handleGetNoticeDeadlines(w http.ResponseWriter, r *http.Request) {
	if rt.noticesUnavailable(w, r) {
		return
	}
	noticeID, ok := rt.noticeID(w, r)
	if !ok {
		return
	}
	v, err := rt.Notices.Notice(r.Context(), noticeID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	cur := v.Current()
	out := gen.NoticeDeadlineLedger{NoticeID: v.ID.String(), Original: toNoticeDeadline(v.Original()),
		Current: toNoticeDeadline(cur), Entries: []gen.NoticeDeadlineEntry{},
		Overdue: cur.Known() && v.AsOf.After(cur.DueAt()) && (!v.Status.Answered() || v.Status == notice.StatusAppeal)}
	for _, e := range v.Deadlines() {
		out.Entries = append(out.Entries, toNoticeDeadlineEntry(e))
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

// noticeAction decodes a request body and applies one act to the notice in
// the path.
func noticeAction[T any](rt *Router, w http.ResponseWriter, r *http.Request,
	fn func(id.NoticeID, T) (app.NoticeView, error)) {
	if rt.noticesUnavailable(w, r) {
		return
	}
	noticeID, ok := rt.noticeID(w, r)
	if !ok {
		return
	}
	var req T
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	v, err := fn(noticeID, req)
	rt.writeNotice(w, r, http.StatusOK, v, err)
}

func (rt *Router) handleRecordNoticeDeadline(w http.ResponseWriter, r *http.Request) {
	noticeAction(rt, w, r, func(nid id.NoticeID, req gen.NoticeDeadlineRequest) (app.NoticeView, error) {
		d, err := deadlineIn("deadline", gen.NoticeDeadlineInput{DueDate: &req.DueDate, Timezone: req.Timezone,
			Source: req.Source, Verified: req.Verified})
		if err != nil {
			return app.NoticeView{}, err
		}
		d.Kind = notice.DeadlineKind(req.Kind)
		return rt.Notices.RecordDeadline(r.Context(), nid, notice.DeadlineRecord{Deadline: d, Evidence: req.Evidence,
			AuthorityRef: deref(req.AuthorityReference), Attachment: deref(req.AttachmentSha256)})
	})
}

func (rt *Router) handleTransitionNotice(w http.ResponseWriter, r *http.Request) {
	noticeAction(rt, w, r, func(nid id.NoticeID, req gen.NoticeTransitionRequest) (app.NoticeView, error) {
		if !req.To.Valid() {
			return app.NoticeView{}, errs.Invalid("to", errs.ReasonInvalidValue, "That is not a notice status.")
		}
		return rt.Notices.Transition(r.Context(), nid, notice.Status(req.To), deref(req.Note))
	})
}

func (rt *Router) handleRecordNoticeAuthenticity(w http.ResponseWriter, r *http.Request) {
	noticeAction(rt, w, r, func(nid id.NoticeID, req gen.NoticeAuthenticityRequest) (app.NoticeView, error) {
		return rt.Notices.RecordAuthenticity(r.Context(), nid, notice.Authenticity(req.Authenticity), req.Evidence)
	})
}

func (rt *Router) handleEscalateNotice(w http.ResponseWriter, r *http.Request) {
	noticeAction(rt, w, r, func(nid id.NoticeID, req gen.NoticeEscalationRequest) (app.NoticeView, error) {
		return rt.Notices.Escalate(r.Context(), nid, notice.EscalationReason(req.Reason), req.Note, deref(req.Evidence))
	})
}

func (rt *Router) handleResolveNoticeEntity(w http.ResponseWriter, r *http.Request) {
	noticeAction(rt, w, r, func(nid id.NoticeID, req gen.NoticeEntityRequest) (app.NoticeView, error) {
		le, err := optEntityIn("legalEntityId", &req.LegalEntityID)
		if err != nil {
			return app.NoticeView{}, err
		}
		return rt.Notices.ResolveEntity(r.Context(), nid, le, req.Evidence)
	})
}

func (rt *Router) handleAssignNoticeOwner(w http.ResponseWriter, r *http.Request) {
	noticeAction(rt, w, r, func(nid id.NoticeID, req gen.NoticeOwnerRequest) (app.NoticeView, error) {
		owner, err := optUserIn("ownerId", &req.OwnerID)
		if err != nil {
			return app.NoticeView{}, err
		}
		sla, err := optTimestampIn("slaDue", req.SLADue)
		if err != nil {
			return app.NoticeView{}, err
		}
		return rt.Notices.AssignOwner(r.Context(), nid, owner, sla, deref(req.Note))
	})
}

func (rt *Router) handleRecordNoticeAttachment(w http.ResponseWriter, r *http.Request) {
	noticeAction(rt, w, r, func(nid id.NoticeID, req gen.NoticeAttachmentRequest) (app.NoticeView, error) {
		return rt.Notices.RecordAttachment(r.Context(), nid, notice.Attachment{SHA256: req.Sha256, Filename: req.Filename,
			MediaType: req.MediaType}, deref(req.Note))
	})
}

func (rt *Router) handlePrepareNoticeResponse(w http.ResponseWriter, r *http.Request) {
	noticeAction(rt, w, r, func(nid id.NoticeID, req gen.NoticeResponsePackageRequest) (app.NoticeView, error) {
		return rt.Notices.PrepareResponse(r.Context(), nid, req.Summary, deref(req.Sha256))
	})
}

func (rt *Router) handleApproveNoticeResponse(w http.ResponseWriter, r *http.Request) {
	noticeAction(rt, w, r, func(nid id.NoticeID, req gen.NoticeApprovalRequest) (app.NoticeView, error) {
		return rt.Notices.ApproveResponse(r.Context(), nid, int(req.Version), deref(req.Note))
	})
}

func (rt *Router) handleRecordNoticeSubmission(w http.ResponseWriter, r *http.Request) {
	noticeAction(rt, w, r, func(nid id.NoticeID, req gen.NoticeSubmissionRequest) (app.NoticeView, error) {
		return rt.Notices.RecordSubmission(r.Context(), nid, req.AuthorityReceiptReference, deref(req.Evidence), deref(req.Note))
	})
}

func (rt *Router) handleRecordNoticeOutcome(w http.ResponseWriter, r *http.Request) {
	noticeAction(rt, w, r, func(nid id.NoticeID, req gen.NoticeOutcomeRequest) (app.NoticeView, error) {
		amount, err := optMoneyIn("amount", req.Amount)
		if err != nil {
			return app.NoticeView{}, err
		}
		rec := notice.OutcomeRecord{Outcome: notice.Outcome(req.Outcome), AuthorityRef: req.AuthorityReceiptReference,
			Evidence: req.Evidence, Note: deref(req.Note), Amount: amount}
		if req.Settled != nil {
			rec.Settled = *req.Settled
		}
		if req.Settlement != nil {
			rec.Settlement = notice.Settlement(*req.Settlement)
		}
		return rt.Notices.RecordOutcome(r.Context(), nid, rec)
	})
}

func (rt *Router) handleLodgeNoticeAppeal(w http.ResponseWriter, r *http.Request) {
	noticeAction(rt, w, r, func(nid id.NoticeID, req gen.NoticeAppealRequest) (app.NoticeView, error) {
		next, err := deadlineIn("nextDeadline", req.NextDeadline)
		if err != nil {
			return app.NoticeView{}, err
		}
		hearing, err := optCivilIn("hearingDate", req.HearingDate)
		if err != nil {
			return app.NoticeView{}, err
		}
		return rt.Notices.LodgeAppeal(r.Context(), nid, notice.AppealRecord{Next: next, Hearing: hearing, Evidence: req.Evidence,
			AuthorityRef: deref(req.AuthorityReference), Note: deref(req.Note)})
	})
}
