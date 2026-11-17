package app

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/support"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// The customer support triage queue (Engineering Doc 18, W16, G-052). The
// rules are internal/domain/support's; this is where a case meets the store
// and the audit trail.
//
// The service is built with a support case repository and the audit trail and
// nothing else: it holds no decision, document, obligation, refund, filing,
// remittance or rule repository, so no act on a case can change a fiscal
// record (SUP-03, SUP-04, SUP-11). The references a case carries are read
// back as identifiers.
//
// Support cases are not fiscal, so ADMIN works them alongside OPERATOR and
// ANALYST; AUDITOR reads them. Every write is a person's act, and four-eye
// sign-off is a comparison of people (T18-18).

// SupportService runs support cases.
type SupportService struct {
	store port.SupportCaseRepository
	audit port.AuditRepository
	tx    port.TxManager
	clock clock.Clock
	ids   idgen.Generator
}

// NewSupportService wires the service.
func NewSupportService(store port.SupportCaseRepository, audit port.AuditRepository, tx port.TxManager,
	clk clock.Clock, ids idgen.Generator) *SupportService {
	return &SupportService{store: store, audit: audit, tx: tx, clock: clk, ids: ids}
}

var (
	supportReaders = []security.Role{security.RoleAdmin, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor}
	supportAgents  = []security.Role{security.RoleAdmin, security.RoleOperator, security.RoleAnalyst}
)

func (s *SupportService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

// SupportCaseView is a case with what its state means now.
type SupportCaseView struct {
	Case   support.Case
	Clocks support.Clocks
	// Actions are what the case's state allows now.
	Actions []support.Action
	// SignOffRequired, SignedOff and Responded are the closure guard's
	// inputs (SUP-14, T18-18).
	SignOffRequired bool
	SignedOff       bool
	Responded       bool
}

func (s *SupportService) view(c support.Case, now time.Time) SupportCaseView {
	return SupportCaseView{
		Case: c, Clocks: c.Clocks(now), Actions: c.Actionable(),
		SignOffRequired: c.NeedsSignOff(), SignedOff: c.SignOff() != nil, Responded: c.Responded(),
	}
}

// SupportCaseInput is a case to open.
type SupportCaseInput struct {
	Type           support.Type
	Severity       support.Severity
	Subject        string
	Description    string
	Product        string
	LegalEntity    id.LegalEntityID
	Period         string
	ContractRef    string
	SellerRef      string
	Refs           support.References
	StatutoryDueAt time.Time
	Assignee       id.UserID
	Evidence       []string
}

// OpenCase opens a case, NEW, with its support SLA running from now.
func (s *SupportService) OpenCase(ctx context.Context, in SupportCaseInput) (SupportCaseView, error) {
	sc, err := requirePerson(ctx, supportAgents...)
	if err != nil {
		return SupportCaseView{}, err
	}
	cid, err := idgen.SupportCaseID(s.ids)
	if err != nil {
		return SupportCaseView{}, internal(err, "The case could not be opened.")
	}
	now := s.now()
	c := support.Case{
		ID: cid, TenantID: sc.Tenant(), Type: in.Type, Severity: in.Severity,
		Subject: strings.TrimSpace(in.Subject), Description: strings.TrimSpace(in.Description),
		Product: strings.TrimSpace(in.Product), LegalEntity: in.LegalEntity, Period: strings.TrimSpace(in.Period),
		ContractRef: strings.TrimSpace(in.ContractRef), SellerRef: strings.TrimSpace(in.SellerRef), Refs: in.Refs,
		Assignee: in.Assignee, Evidence: support.CleanEvidence(in.Evidence),
		OpenedAt: now, OpenedBy: sc.Subject(),
	}
	if in.Severity.Valid() {
		c.SLADueAt = now.Add(in.Severity.SLATarget())
	}
	if !in.StatutoryDueAt.IsZero() {
		c.StatutoryDueAt = in.StatutoryDueAt.UTC().Truncate(time.Microsecond)
	}
	if err := c.Validate(); err != nil {
		return SupportCaseView{}, err
	}
	var out support.Case
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Create(ctx, c, c.Open()); err != nil {
			return err
		}
		if err := s.record(ctx, sc, c.ID, c.Open()); err != nil {
			return err
		}
		out, err = s.store.Case(ctx, c.ID)
		return err
	})
	if err != nil {
		return SupportCaseView{}, err
	}
	return s.view(out, now), nil
}

// Case reads one case as of now.
func (s *SupportService) Case(ctx context.Context, caseID id.SupportCaseID) (SupportCaseView, error) {
	if _, err := requireRoleOrSystem(ctx, supportReaders...); err != nil {
		return SupportCaseView{}, err
	}
	c, err := s.store.Case(ctx, caseID)
	if err != nil {
		return SupportCaseView{}, err
	}
	return s.view(c, s.now()), nil
}

// SupportCaseFilter narrows the queue. Zero fields do not filter.
type SupportCaseFilter struct {
	Type        support.Type
	Severity    support.Severity
	Status      support.Status
	Product     string
	LegalEntity id.LegalEntityID
	Period      string
	Assignee    id.UserID
	// Unassigned keeps only cases nobody owns.
	Unassigned bool
	SLA        support.ClockState
	Statutory  support.ClockState
	Limit      int
}

// Cases is the triage queue: open cases first, soonest due first — the
// earlier of the SLA and any statutory deadline (SUP-06) — then closed cases,
// most recently opened first.
func (s *SupportService) Cases(ctx context.Context, f SupportCaseFilter) ([]SupportCaseView, error) {
	if _, err := requireRoleOrSystem(ctx, supportReaders...); err != nil {
		return nil, err
	}
	rows, err := s.store.List(ctx, f.Limit)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := []SupportCaseView{}
	for _, c := range rows {
		v := s.view(c, now)
		if f.keeps(v) {
			out = append(out, v)
		}
	}
	slices.SortStableFunc(out, func(a, b SupportCaseView) int {
		ac, bc := a.Case.Status == support.StatusClosed, b.Case.Status == support.StatusClosed
		switch {
		case ac != bc:
			if ac {
				return 1
			}
			return -1
		case ac:
			return b.Case.OpenedAt.Compare(a.Case.OpenedAt)
		}
		return a.Clocks.DueAt.Compare(b.Clocks.DueAt)
	})
	return out, nil
}

func (f SupportCaseFilter) keeps(v SupportCaseView) bool {
	c := v.Case
	switch {
	case f.Type != "" && c.Type != f.Type,
		f.Severity != "" && c.Severity != f.Severity,
		f.Status != "" && c.Status != f.Status,
		f.Product != "" && !strings.EqualFold(c.Product, f.Product),
		!f.LegalEntity.IsZero() && c.LegalEntity != f.LegalEntity,
		f.Period != "" && c.Period != f.Period,
		!f.Assignee.IsZero() && c.Assignee != f.Assignee,
		f.Unassigned && !c.Assignee.IsZero(),
		f.SLA != "" && v.Clocks.SLA != f.SLA,
		f.Statutory != "" && v.Clocks.Statutory != f.Statutory:
		return false
	}
	return true
}

// ActOnCase takes one action on a case and records it as the next event of
// its history. Nothing outside the case changes.
func (s *SupportService) ActOnCase(ctx context.Context, caseID id.SupportCaseID, a support.Act) (SupportCaseView, error) {
	sc, err := requirePerson(ctx, supportAgents...)
	if err != nil {
		return SupportCaseView{}, err
	}
	if !a.StatutoryDueAt.IsZero() {
		a.StatutoryDueAt = a.StatutoryDueAt.UTC().Truncate(time.Microsecond)
	}
	var (
		out support.Case
		now = s.now()
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, caseID); err != nil {
			return err
		}
		c, err := s.store.Case(ctx, caseID)
		if err != nil {
			return err
		}
		e, err := c.Decide(a, sc.Subject(), now)
		if err != nil {
			return err
		}
		if err := s.store.Append(ctx, c.ID, e); err != nil {
			return err
		}
		if err := s.record(ctx, sc, c.ID, e); err != nil {
			return err
		}
		out, err = s.store.Case(ctx, caseID)
		return err
	})
	if err != nil {
		return SupportCaseView{}, err
	}
	return s.view(out, now), nil
}

// record writes the act to the administrative audit trail: who did what to
// which case, never the case's free text.
func (s *SupportService) record(ctx context.Context, sc security.Context, caseID id.SupportCaseID, e support.Event) error {
	auditID, err := idgen.AuditID(s.ids)
	if err != nil {
		return internal(err, "The action could not be recorded.")
	}
	body, err := canonical.Encode(canonical.Object(
		canonical.F("action", canonical.String(string(e.Action))),
		canonical.F("seq", canonical.Integer(int64(e.Seq))),
		canonical.F("status", canonical.String(string(e.Status))),
	))
	if err != nil {
		return internal(err, "The action could not be recorded.")
	}
	actor := sc.Subject()
	return s.audit.Append(ctx, port.AuditRecord{
		ID: auditID, TenantID: sc.Tenant(), ActorUserID: &actor, Action: "support.case." + strings.ToLower(string(e.Action)),
		SubjectType: "support_case", SubjectID: caseID.String(), Detail: body, RecordedAt: e.RecordedAt,
	})
}

func (s *SupportService) inTx(ctx context.Context, fn func(context.Context) error) error {
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
