package app

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/notice"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Regulatory notice and appeal intake (Engineering Doc 18 W17, G-052). The
// rules are internal/domain/notice's; this is where they meet the people
// acting, the legal entities a notice is addressed to, and the store.
//
// Every write is a person's act, so the four-eye approval of a response is a
// comparison of people, and system work cannot answer a notice. A notice
// never writes a fiscal record: the links it carries are identifiers, and
// nothing here reads or changes a return, a ledger or a rule.
//
// Roles: OPERATOR and ANALYST register and work notices (Compliance Ops and
// Legal). AUDITOR reads. ADMIN reads too, because a notice is addressed to the
// tenant and an administrator has to be able to see that one arrived; ADMIN
// never acts on one, for the reason ADMIN holds no fiscal role.

// NoticeService registers and works regulatory notices.
type NoticeService struct {
	notices  port.NoticeRepository
	entities port.LegalEntityRepository
	tx       port.TxManager
	clock    clock.Clock
	ids      idgen.Generator
}

// NewNoticeService wires the service. entities, when not nil, checks that a
// named recipient is one of the tenant's legal entities.
func NewNoticeService(notices port.NoticeRepository, entities port.LegalEntityRepository, tx port.TxManager,
	clk clock.Clock, ids idgen.Generator) *NoticeService {
	return &NoticeService{notices: notices, entities: entities, tx: tx, clock: clk, ids: ids}
}

var (
	noticeReaders = []security.Role{security.RoleAdmin, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor}
	noticeActors  = []security.Role{security.RoleOperator, security.RoleAnalyst}
)

func (s *NoticeService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

// NoticeView is a notice as of now: what needs attention and how its two
// clocks stand.
type NoticeView struct {
	notice.Notice
	Flags  []notice.Flag
	Urgent bool
	AsOf   time.Time
}

func (s *NoticeService) view(n notice.Notice) NoticeView {
	now := s.now()
	v := NoticeView{Notice: n, Flags: n.FlagsAt(now), AsOf: now}
	v.Urgent = slices.Contains(v.Flags, notice.FlagUrgentLegalReview)
	return v
}

// NoticeIntake is a notice to register.
type NoticeIntake struct {
	Notice           notice.Notice
	Deadline         notice.Deadline
	DeadlineEvidence string
	Owner            id.UserID
	SLADue           time.Time
	Note             string
}

// Intake registers a notice with its deadline as received. A notice with no
// resolved recipient goes straight to legal review (SUP-08).
func (s *NoticeService) Intake(ctx context.Context, in NoticeIntake) (NoticeView, error) {
	sc, err := requirePerson(ctx, noticeActors...)
	if err != nil {
		return NoticeView{}, err
	}
	var out notice.Notice
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.checkEntity(ctx, in.Notice.LegalEntity); err != nil {
			return err
		}
		nid, err := idgen.NoticeID(s.ids)
		if err != nil {
			return internal(err, "The notice could not be registered.")
		}
		now := s.now()
		n := in.Notice
		n.ID, n.TenantID, n.CreatedAt, n.CreatedBy = nid, sc.Tenant(), now, sc.Subject()
		n.Authority, n.AuthorityRef = strings.TrimSpace(n.Authority), strings.TrimSpace(n.AuthorityRef)
		n.Jurisdiction, n.LinkedCase = strings.ToUpper(strings.TrimSpace(n.Jurisdiction)), strings.TrimSpace(n.LinkedCase)
		events, err := notice.Open(n, notice.Intake{Deadline: in.Deadline, DeadlineEvidence: in.DeadlineEvidence,
			Owner: in.Owner, SLADue: in.SLADue, Note: in.Note}, sc.Subject(), now)
		if err != nil {
			return err
		}
		if err := s.notices.Create(ctx, n, events); err != nil {
			return err
		}
		out, err = s.notices.Notice(ctx, nid)
		return err
	})
	if err != nil {
		return NoticeView{}, err
	}
	return s.view(out), nil
}

func (s *NoticeService) checkEntity(ctx context.Context, le id.LegalEntityID) error {
	if le.IsZero() || s.entities == nil {
		return nil
	}
	if _, err := s.entities.ByID(ctx, le); err != nil {
		if errs.IsCategory(err, errs.CategoryNotFound) {
			return errs.Invalid("legalEntityId", errs.ReasonInvalidValue, "That is not one of the tenant's legal entities.")
		}
		return err
	}
	return nil
}

// Notice reads one notice as of now.
func (s *NoticeService) Notice(ctx context.Context, noticeID id.NoticeID) (NoticeView, error) {
	if _, err := requireRoleOrSystem(ctx, noticeReaders...); err != nil {
		return NoticeView{}, err
	}
	n, err := s.notices.Notice(ctx, noticeID)
	if err != nil {
		return NoticeView{}, err
	}
	return s.view(n), nil
}

// NoticeFilter narrows the register. Zero fields match everything.
type NoticeFilter struct {
	Type         notice.Type
	Status       notice.Status
	Authority    string
	LegalEntity  id.LegalEntityID
	AuthorityRef string
	ReceivedFrom time.Time
	ReceivedTo   time.Time
	// Period matches a notice whose period contains this date.
	Period time.Time
	// AmountMin and AmountMax bound the stated amount; a notice in another
	// currency, or with no amount, does not match a bound.
	AmountMin     *fiscal.Money
	AmountMax     *fiscal.Money
	AttachmentSHA string
	Authenticity  notice.Authenticity
	UrgentOnly    bool
	// IncludeClosed lists CLOSED notices when no status is asked for.
	IncludeClosed bool
}

func (f NoticeFilter) match(v NoticeView) bool {
	n := v.Notice
	switch {
	case f.Type != "" && n.Type != f.Type,
		f.Status != "" && n.Status != f.Status,
		f.Status == "" && !f.IncludeClosed && n.Status == notice.StatusClosed,
		f.Authority != "" && !strings.EqualFold(n.Authority, f.Authority),
		f.AuthorityRef != "" && !strings.Contains(strings.ToLower(n.AuthorityRef), strings.ToLower(f.AuthorityRef)),
		!f.ReceivedFrom.IsZero() && n.ReceivedAt.Before(f.ReceivedFrom),
		!f.ReceivedTo.IsZero() && n.ReceivedAt.After(f.ReceivedTo),
		f.Authenticity != "" && n.Authenticity() != f.Authenticity,
		f.UrgentOnly && !v.Urgent:
		return false
	}
	if !f.LegalEntity.IsZero() {
		if le, _ := n.Entity(); le != f.LegalEntity {
			return false
		}
	}
	if !f.Period.IsZero() {
		day := f.Period.Format(time.DateOnly)
		if n.PeriodStart.IsZero() || day < n.PeriodStart.Format(time.DateOnly) || day > n.PeriodEnd.Format(time.DateOnly) {
			return false
		}
	}
	if f.AttachmentSHA != "" && !n.HasAttachment(strings.ToLower(f.AttachmentSHA)) {
		return false
	}
	for _, b := range []struct {
		bound *fiscal.Money
		want  func(int) bool
	}{{f.AmountMin, func(c int) bool { return c >= 0 }}, {f.AmountMax, func(c int) bool { return c <= 0 }}} {
		if b.bound == nil {
			continue
		}
		if n.Amount == nil || n.Amount.Currency() != b.bound.Currency() {
			return false
		}
		c, err := n.Amount.Cmp(*b.bound)
		if err != nil || !b.want(c) {
			return false
		}
	}
	return true
}

// Notices lists the register in working order: unknown deadlines first, then
// the soonest statutory deadline; closed notices last.
func (s *NoticeService) Notices(ctx context.Context, f NoticeFilter, limit int) ([]NoticeView, error) {
	if _, err := requireRoleOrSystem(ctx, noticeReaders...); err != nil {
		return nil, err
	}
	rows, err := s.notices.List(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]NoticeView, 0, len(rows))
	for _, n := range rows {
		if v := s.view(n); f.match(v) {
			out = append(out, v)
		}
	}
	rank := func(v NoticeView) (int, time.Time) {
		switch {
		case v.Status == notice.StatusClosed:
			return 3, time.Time{}
		case !v.Current().Known():
			return 0, time.Time{}
		case v.Status.Answered() && v.Status != notice.StatusAppeal:
			return 2, v.Current().DueAt()
		}
		return 1, v.Current().DueAt()
	}
	slices.SortStableFunc(out, func(a, b NoticeView) int {
		ra, ta := rank(a)
		rb, tb := rank(b)
		if ra != rb {
			return ra - rb
		}
		return ta.Compare(tb)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Transition moves a notice through review, or closes it.
func (s *NoticeService) Transition(ctx context.Context, noticeID id.NoticeID, to notice.Status, note string) (NoticeView, error) {
	return s.act(ctx, noticeID, func(_ context.Context, n notice.Notice, by id.UserID, at time.Time) ([]notice.Event, error) {
		return one(n.Transition(to, note, by, at))
	})
}

// RecordAuthenticity records what was established about the notice's origin.
func (s *NoticeService) RecordAuthenticity(ctx context.Context, noticeID id.NoticeID, a notice.Authenticity, evidence string) (NoticeView, error) {
	return s.act(ctx, noticeID, func(_ context.Context, n notice.Notice, by id.UserID, at time.Time) ([]notice.Event, error) {
		return n.RecordAuthenticity(a, evidence, by, at)
	})
}

// Escalate sends a notice to legal.
func (s *NoticeService) Escalate(ctx context.Context, noticeID id.NoticeID, reason notice.EscalationReason, note, evidence string) (NoticeView, error) {
	return s.act(ctx, noticeID, func(_ context.Context, n notice.Notice, by id.UserID, at time.Time) ([]notice.Event, error) {
		return one(n.Escalate(reason, note, evidence, by, at))
	})
}

// ResolveEntity records the legal entity a notice is addressed to.
func (s *NoticeService) ResolveEntity(ctx context.Context, noticeID id.NoticeID, le id.LegalEntityID, evidence string) (NoticeView, error) {
	return s.act(ctx, noticeID, func(ctx context.Context, n notice.Notice, by id.UserID, at time.Time) ([]notice.Event, error) {
		if err := s.checkEntity(ctx, le); err != nil {
			return nil, err
		}
		return one(n.ResolveEntity(le, evidence, by, at))
	})
}

// AssignOwner names the deadline owner and our SLA target.
func (s *NoticeService) AssignOwner(ctx context.Context, noticeID id.NoticeID, owner id.UserID, sla time.Time, note string) (NoticeView, error) {
	return s.act(ctx, noticeID, func(_ context.Context, n notice.Notice, by id.UserID, at time.Time) ([]notice.Event, error) {
		return one(n.AssignOwner(owner, sla, note, by, at))
	})
}

// RecordAttachment records a document's digest as evidence.
func (s *NoticeService) RecordAttachment(ctx context.Context, noticeID id.NoticeID, a notice.Attachment, note string) (NoticeView, error) {
	return s.act(ctx, noticeID, func(_ context.Context, n notice.Notice, by id.UserID, at time.Time) ([]notice.Event, error) {
		return one(n.RecordAttachment(a, note, by, at))
	})
}

// RecordDeadline confirms or extends the statutory deadline.
func (s *NoticeService) RecordDeadline(ctx context.Context, noticeID id.NoticeID, r notice.DeadlineRecord) (NoticeView, error) {
	return s.act(ctx, noticeID, func(_ context.Context, n notice.Notice, by id.UserID, at time.Time) ([]notice.Event, error) {
		return one(n.RecordDeadline(r, by, at))
	})
}

// PrepareResponse records a response package version.
func (s *NoticeService) PrepareResponse(ctx context.Context, noticeID id.NoticeID, summary, sha string) (NoticeView, error) {
	return s.act(ctx, noticeID, func(_ context.Context, n notice.Notice, by id.UserID, at time.Time) ([]notice.Event, error) {
		return one(n.PrepareResponse(summary, sha, by, at))
	})
}

// ApproveResponse is the four-eye approval of a response package version.
func (s *NoticeService) ApproveResponse(ctx context.Context, noticeID id.NoticeID, version int, note string) (NoticeView, error) {
	return s.act(ctx, noticeID, func(_ context.Context, n notice.Notice, by id.UserID, at time.Time) ([]notice.Event, error) {
		return one(n.ApproveResponse(version, note, by, at))
	})
}

// RecordSubmission records the approved response's receipt by the authority.
func (s *NoticeService) RecordSubmission(ctx context.Context, noticeID id.NoticeID, receipt, evidence, note string) (NoticeView, error) {
	return s.act(ctx, noticeID, func(_ context.Context, n notice.Notice, by id.UserID, at time.Time) ([]notice.Event, error) {
		return one(n.RecordSubmission(receipt, evidence, note, by, at))
	})
}

// RecordOutcome records what the authority decided.
func (s *NoticeService) RecordOutcome(ctx context.Context, noticeID id.NoticeID, r notice.OutcomeRecord) (NoticeView, error) {
	return s.act(ctx, noticeID, func(_ context.Context, n notice.Notice, by id.UserID, at time.Time) ([]notice.Event, error) {
		return one(n.RecordOutcome(r, by, at))
	})
}

// LodgeAppeal opens an appeal against the recorded outcome.
func (s *NoticeService) LodgeAppeal(ctx context.Context, noticeID id.NoticeID, r notice.AppealRecord) (NoticeView, error) {
	return s.act(ctx, noticeID, func(_ context.Context, n notice.Notice, by id.UserID, at time.Time) ([]notice.Event, error) {
		return one(n.LodgeAppeal(r, by, at))
	})
}

func one(e notice.Event, err error) ([]notice.Event, error) {
	if err != nil {
		return nil, err
	}
	return []notice.Event{e}, nil
}

// act applies one act to a notice under its lock, as a person, and returns
// the notice as of now.
func (s *NoticeService) act(ctx context.Context, noticeID id.NoticeID,
	fn func(context.Context, notice.Notice, id.UserID, time.Time) ([]notice.Event, error)) (NoticeView, error) {
	sc, err := requirePerson(ctx, noticeActors...)
	if err != nil {
		return NoticeView{}, err
	}
	var out notice.Notice
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.notices.Lock(ctx, noticeID); err != nil {
			return err
		}
		// Read under the lock: an act decided against a history somebody has
		// since extended is decided against the wrong one.
		n, err := s.notices.Notice(ctx, noticeID)
		if err != nil {
			return err
		}
		events, err := fn(ctx, n, sc.Subject(), s.now())
		if err != nil {
			return err
		}
		if err := s.notices.AppendEvents(ctx, noticeID, events); err != nil {
			return err
		}
		out, err = s.notices.Notice(ctx, noticeID)
		return err
	})
	if err != nil {
		return NoticeView{}, err
	}
	return s.view(out), nil
}

func (s *NoticeService) inTx(ctx context.Context, fn func(context.Context) error) error {
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(txCtx)
}
