package app

import (
	"context"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rulechange"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// The rule approval queue (CONT-001 §3 ContentChange, §8; Doc 03 §07; Sidebar
// WF-02). The rules are internal/domain/rulechange's.
//
// The service holds a rule-change repository and nothing else: no bundle
// signer, no pack activator, no rule or decision repository. Approval makes
// a change eligible for a content release and that is all it can do —
// "No direct release-pack publication" (WF-02).
//
// Roles. ANALYST and OPERATOR propose changes and attach test evidence.
// ANALYST reviews and approves, never their own proposal, and never a
// change they recommended. AUDITOR and ADMIN read. Every act is a signed-in
// person's: a system or AI principal never recommends or approves
// (ZTAX-CONT-REQ-0011, -0012).

// RuleChangeService runs the rule approval queue.
type RuleChangeService struct {
	store port.RuleChangeRepository
	tx    port.TxManager
	clock clock.Clock
	ids   idgen.Generator
}

// NewRuleChangeService wires the service.
func NewRuleChangeService(store port.RuleChangeRepository, tx port.TxManager, clk clock.Clock, ids idgen.Generator) *RuleChangeService {
	return &RuleChangeService{store: store, tx: tx, clock: clk, ids: ids}
}

var (
	ruleChangeReaders   = []security.Role{security.RoleOperator, security.RoleAnalyst, security.RoleAuditor, security.RoleAdmin}
	ruleChangeProposers = []security.Role{security.RoleOperator, security.RoleAnalyst}
	ruleChangeReviewers = []security.Role{security.RoleAnalyst}
)

const ruleChangeListCap = 500

func (s *RuleChangeService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

// RuleChangeView is a proposal as of now.
type RuleChangeView struct {
	rulechange.Proposal
	AsOf time.Time
}

// RuleChangeInput is a proposal to record.
type RuleChangeInput struct {
	IntakeKey     string
	Pack          string
	BaseVersion   string
	Rule          string
	Jurisdiction  string
	ChangeType    rulechange.ChangeType
	Summary       string
	Detail        string
	Impact        string
	Citation      rulechange.Citation
	PublishedOn   time.Time
	EffectiveFrom time.Time
	EffectiveTo   time.Time
	OriginRef     string
	Supersedes    id.RuleChangeID
	TestEvidence  *rulechange.TestEvidence
}

func (s *RuleChangeService) inTx(ctx context.Context, fn func(context.Context) error) error {
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

// ProposeRuleChange records a proposal as PROPOSED. A revision of one sent
// back for changes names it in Supersedes, and the old one is SUPERSEDED in
// the same transaction. created is false when the Idempotency-Key already
// recorded this request.
func (s *RuleChangeService) ProposeRuleChange(ctx context.Context, in RuleChangeInput) (RuleChangeView, bool, error) {
	sc, err := requirePerson(ctx, ruleChangeProposers...)
	if err != nil {
		return RuleChangeView{}, false, err
	}
	key := strings.TrimSpace(in.IntakeKey)
	if err := checkIntakeKey(key); err != nil {
		return RuleChangeView{}, false, err
	}
	base, err := content.ParseVersion(strings.TrimSpace(in.BaseVersion))
	if err != nil {
		return RuleChangeView{}, false, errs.Invalid("baseVersion", errs.ReasonInvalidValue, "A pack version is MAJOR.MINOR.PATCH.")
	}
	now := s.now()
	p := rulechange.Proposal{
		TenantID: sc.Tenant(), Pack: content.PackID(strings.TrimSpace(in.Pack)), BaseVersion: base,
		Rule: content.RuleSemanticID(strings.TrimSpace(in.Rule)), Jurisdiction: in.Jurisdiction, ChangeType: in.ChangeType,
		Summary: in.Summary, Detail: in.Detail, Impact: in.Impact, Citation: in.Citation, OriginRef: in.OriginRef,
		Supersedes: in.Supersedes, IntakeKey: key, CreatedAt: now, CreatedBy: sc.Subject(),
	}
	if !in.PublishedOn.IsZero() {
		p.PublishedOn = registration.Day(in.PublishedOn)
	}
	if !in.EffectiveFrom.IsZero() {
		p.EffectiveFrom = registration.Day(in.EffectiveFrom)
	}
	if !in.EffectiveTo.IsZero() {
		p.EffectiveTo = registration.Day(in.EffectiveTo)
	}
	p = p.Normalize()
	if err := p.Validate(); err != nil {
		return RuleChangeView{}, false, err
	}
	var ev *rulechange.TestEvidence
	if in.TestEvidence != nil {
		e := rulechange.TestEvidence{Ref: strings.TrimSpace(in.TestEvidence.Ref), SHA256: strings.ToLower(strings.TrimSpace(in.TestEvidence.SHA256))}
		if err := e.Validate(); err != nil {
			return RuleChangeView{}, false, err
		}
		ev = &e
	}
	payload := p.Payload()
	if ev != nil {
		payload = canonical.Object(canonical.F("proposal", payload), canonical.F("testEvidence", canonical.Object(
			canonical.F("ref", canonical.String(ev.Ref)), canonical.F("sha256", canonical.OptString(ev.SHA256)))))
	}
	if p.IntakeHash, err = canonical.Sum(payload); err != nil {
		return RuleChangeView{}, false, internal(err, "The proposal could not be hashed.")
	}
	var (
		out     rulechange.Proposal
		created bool
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "intake/"+key); err != nil {
			return err
		}
		if prior, found, err := s.store.RuleChangeByIntakeKey(ctx, key); err != nil {
			return err
		} else if found {
			if !prior.IntakeHash.Equal(p.IntakeHash) {
				return keyReuse()
			}
			out = prior
			return nil
		}
		cid, err := idgen.RuleChangeID(s.ids)
		if err != nil {
			return internal(err, "The proposal could not be recorded.")
		}
		p.ID = cid
		var supersede *rulechange.Event
		if !p.Supersedes.IsZero() {
			if err := s.store.Lock(ctx, "change/"+p.Supersedes.String()); err != nil {
				return err
			}
			old, err := s.store.RuleChange(ctx, p.Supersedes)
			if err != nil {
				if errs.IsCategory(err, errs.CategoryNotFound) {
					return errs.Invalid("supersedes", errs.ReasonInvalidValue, "That is not one of the tenant's rule changes.")
				}
				return err
			}
			if old.Pack != p.Pack {
				return errs.Invalid("supersedes", errs.ReasonInvalidValue, "A revised proposal is against the same rule pack.")
			}
			e, err := old.Supersede(cid, sc.Subject(), now)
			if err != nil {
				return err
			}
			supersede = &e
		}
		if err := s.store.CreateRuleChange(ctx, p, rulechange.Open(p, ev)); err != nil {
			return err
		}
		if supersede != nil {
			if err := s.store.AppendRuleChangeEvent(ctx, p.Supersedes, *supersede); err != nil {
				return err
			}
		}
		created = true
		out, err = s.store.RuleChange(ctx, cid)
		return err
	})
	if err != nil {
		return RuleChangeView{}, false, err
	}
	return RuleChangeView{Proposal: out, AsOf: s.now()}, created, nil
}

// RuleChanges lists the queue, newest first.
func (s *RuleChangeService) RuleChanges(ctx context.Context, f rulechange.Filter, limit int) ([]RuleChangeView, error) {
	if _, err := requireRoleOrSystem(ctx, ruleChangeReaders...); err != nil {
		return nil, err
	}
	all, err := s.store.RuleChanges(ctx, ruleChangeListCap)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > ruleChangeListCap {
		limit = ruleChangeListCap
	}
	now := s.now()
	out := []RuleChangeView{}
	for _, p := range all {
		if f.Match(p) && len(out) < limit {
			out = append(out, RuleChangeView{Proposal: p, AsOf: now})
		}
	}
	return out, nil
}

// RuleChange reads one proposal.
func (s *RuleChangeService) RuleChange(ctx context.Context, changeID id.RuleChangeID) (RuleChangeView, error) {
	if _, err := requireRoleOrSystem(ctx, ruleChangeReaders...); err != nil {
		return RuleChangeView{}, err
	}
	p, err := s.store.RuleChange(ctx, changeID)
	if err != nil {
		return RuleChangeView{}, err
	}
	return RuleChangeView{Proposal: p, AsOf: s.now()}, nil
}

// RecordRuleChangeTestEvidence attaches test evidence to a PROPOSED change.
func (s *RuleChangeService) RecordRuleChangeTestEvidence(ctx context.Context, changeID id.RuleChangeID,
	ev rulechange.TestEvidence, revision int) (RuleChangeView, error) {
	return s.act(ctx, changeID, ruleChangeProposers, func(p rulechange.Proposal, by id.UserID, at time.Time) (rulechange.Event, error) {
		return p.RecordTestEvidence(ev, revision, by, at)
	})
}

// TransitionRuleChange moves a proposal to the status a person names,
// against the revision they read. Withdrawal is the proposer's; review and
// approval are a reviewer's.
func (s *RuleChangeService) TransitionRuleChange(ctx context.Context, changeID id.RuleChangeID, to rulechange.Status,
	reason string, revision int) (RuleChangeView, error) {
	roles := ruleChangeReviewers
	if to == rulechange.StatusWithdrawn {
		roles = ruleChangeProposers
	}
	return s.act(ctx, changeID, roles, func(p rulechange.Proposal, by id.UserID, at time.Time) (rulechange.Event, error) {
		return p.Transition(to, reason, revision, by, at)
	})
}

func (s *RuleChangeService) act(ctx context.Context, changeID id.RuleChangeID, roles []security.Role,
	fn func(rulechange.Proposal, id.UserID, time.Time) (rulechange.Event, error)) (RuleChangeView, error) {
	sc, err := requirePerson(ctx, roles...)
	if err != nil {
		return RuleChangeView{}, err
	}
	var out rulechange.Proposal
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.Lock(ctx, "change/"+changeID.String()); err != nil {
			return err
		}
		p, err := s.store.RuleChange(ctx, changeID)
		if err != nil {
			return err
		}
		e, err := fn(p, sc.Subject(), s.now())
		if err != nil {
			return err
		}
		if err := s.store.AppendRuleChangeEvent(ctx, changeID, e); err != nil {
			return err
		}
		out, err = s.store.RuleChange(ctx, changeID)
		return err
	})
	if err != nil {
		return RuleChangeView{}, err
	}
	return RuleChangeView{Proposal: out, AsOf: s.now()}, nil
}
