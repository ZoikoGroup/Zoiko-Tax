package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/retention"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Evidence retention and legal hold (ZTAX-EVID-001 §11–§13; ZTAX-PRIV-001
// §14). The rules are internal/domain/retention's; this is where they meet
// the store and the people who use them.
//
// Who may do what:
//   - Policies and holds are written by an administrator, signed in as
//     themselves: never system work, because a hold is a legal act somebody
//     answers for (ZTAX-LEG-REQ-0091).
//   - Holds are read by an administrator or an auditor, and every read is
//     written to the administrative audit trail as privileged evidence
//     access (ZTAX-EVID-REQ-0115).
//   - A hold grants nothing. It names what is kept; it does not open a
//     record to anyone the record's own access rules would not
//     (ZTAX-EVID-REQ-0024), and nothing here returns evidence content.
//   - A disposition verdict is read by an administrator, an auditor, or the
//     cell's own work — the disposition job it exists for.
//
// Nothing here deletes anything. The application's database role cannot;
// the disposition job that will is out of scope, and the verdict is the gate
// it is required to pass, taken under the hold lock so a hold placed
// concurrently is seen (ZTAX-EVID-REQ-0026).

// RetentionService administers retention policies and legal holds.
type RetentionService struct {
	store     port.RetentionRepository
	decisions port.DecisionRepository
	entities  port.LegalEntityRepository
	audit     port.AuditRepository
	tx        port.TxManager
	clock     clock.Clock
	ids       idgen.Generator
}

// NewRetentionService wires the service.
func NewRetentionService(store port.RetentionRepository, decisions port.DecisionRepository, entities port.LegalEntityRepository,
	audit port.AuditRepository, tx port.TxManager, clk clock.Clock, ids idgen.Generator) *RetentionService {
	return &RetentionService{store: store, decisions: decisions, entities: entities, audit: audit, tx: tx, clock: clk, ids: ids}
}

// requirePerson is an administrator acting as themselves.
func requirePerson(ctx context.Context, roles ...security.Role) (security.Context, error) {
	sc, ok := security.From(ctx)
	if !ok || !sc.Authenticated() {
		return security.Context{}, errs.New(errs.CategoryPolicy, errs.ReasonUnauthenticated,
			"The request carried no valid session. Sign in and retry.")
	}
	if !sc.HasAny(roles...) {
		return security.Context{}, errs.New(errs.CategoryPolicy, errs.ReasonForbidden,
			"The authenticated subject does not hold a role permitting this action.")
	}
	return sc, nil
}

func (s *RetentionService) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

// PolicyInput is a policy version to record.
type PolicyInput struct {
	ID            string
	Class         retention.RecordClass
	Country       string
	Years         int
	Trigger       retention.Trigger
	EffectiveFrom time.Time
	Citation      string
}

// RecordPolicy records the next version of a policy (ZTAX-EVID-REQ-0052).
func (s *RetentionService) RecordPolicy(ctx context.Context, in PolicyInput) (retention.Policy, error) {
	sc, err := requirePerson(ctx, security.RoleAdmin)
	if err != nil {
		return retention.Policy{}, err
	}
	var out retention.Policy
	err = s.inTx(ctx, func(ctx context.Context) error {
		// One writer of policies at a time per tenant, so the next version
		// number is the next.
		if err := s.store.LockHolds(ctx); err != nil {
			return err
		}
		all, err := s.store.Policies(ctx)
		if err != nil {
			return err
		}
		p := retention.Policy{ID: in.ID, Version: 1, Class: in.Class, Country: in.Country, Years: in.Years, Trigger: in.Trigger,
			EffectiveFrom: in.EffectiveFrom.UTC(), Citation: strings.TrimSpace(in.Citation), RecordedAt: s.now(), RecordedBy: sc.Subject()}
		for _, q := range all {
			if q.ID == p.ID && q.Version >= p.Version {
				p.Version = q.Version + 1
			}
		}
		if err := p.Validate(); err != nil {
			return errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, strings.TrimPrefix(err.Error(), "retention: "))
		}
		if err := s.store.AppendPolicy(ctx, p); err != nil {
			return err
		}
		out = p
		return s.record(ctx, sc, "RETENTION_POLICY_RECORDED", "retention_policy", fmt.Sprintf("%s/v%d", p.ID, p.Version),
			canonical.F("class", canonical.String(string(p.Class))),
			canonical.F("country", canonical.String(p.Country)),
			canonical.F("years", canonical.Integer(int64(p.Years))),
			canonical.F("trigger", canonical.String(string(p.Trigger))))
	})
	return out, err
}

// Policies returns every version of every policy.
func (s *RetentionService) Policies(ctx context.Context) ([]retention.Policy, error) {
	if _, err := requirePerson(ctx, security.RoleAdmin, security.RoleAuditor); err != nil {
		return nil, err
	}
	return s.store.Policies(ctx)
}

// PlaceHold places a legal hold for a matter.
func (s *RetentionService) PlaceHold(ctx context.Context, matter, reason string, scope retention.Scope) (retention.Hold, error) {
	sc, err := requirePerson(ctx, security.RoleAdmin)
	if err != nil {
		return retention.Hold{}, err
	}
	matter = strings.TrimSpace(matter)
	if matter == "" || len(matter) > 255 {
		return retention.Hold{}, errs.Invalid("matter", errs.ReasonMissingField,
			"A hold names the claim, investigation or request it serves, in at most 255 characters (ZTAX-LEG-REQ-0091).")
	}
	if err := checkHoldReason(reason); err != nil {
		return retention.Hold{}, err
	}
	if err := validScope(scope); err != nil {
		return retention.Hold{}, err
	}
	holdID, err := idgen.LegalHoldID(s.ids)
	if err != nil {
		return retention.Hold{}, internal(err, "The hold could not be recorded.")
	}
	h := retention.Hold{ID: holdID, TenantID: sc.Tenant(), Matter: matter}
	placed := retention.HoldEvent{Hold: holdID, Seq: 1, Kind: retention.EventPlaced, Scope: scope, Reason: reason,
		RecordedAt: s.now(), RecordedBy: sc.Subject()}
	var out retention.Hold
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.LockHolds(ctx); err != nil {
			return err
		}
		if err := s.store.CreateHold(ctx, h, placed); err != nil {
			return err
		}
		if out, err = s.store.Hold(ctx, holdID); err != nil {
			return err
		}
		return s.record(ctx, sc, "LEGAL_HOLD_PLACED", "legal_hold", holdID.String(), scopeFields(scope)...)
	})
	return out, err
}

// ChangeHoldScope replaces an active hold's scope, as a new event in its
// history (ZTAX-EVID-REQ-0053).
func (s *RetentionService) ChangeHoldScope(ctx context.Context, holdID id.LegalHoldID, reason string, scope retention.Scope) (retention.Hold, error) {
	if err := validScope(scope); err != nil {
		return retention.Hold{}, err
	}
	return s.advance(ctx, holdID, reason, retention.EventScopeChanged, scope)
}

// ReleaseHold releases a hold. What it held is then decided by retention
// alone again, at the next verdict (ZTAX-EVID-REQ-0054).
func (s *RetentionService) ReleaseHold(ctx context.Context, holdID id.LegalHoldID, reason string) (retention.Hold, error) {
	return s.advance(ctx, holdID, reason, retention.EventReleased, retention.Scope{})
}

func (s *RetentionService) advance(ctx context.Context, holdID id.LegalHoldID, reason string, kind retention.EventKind, scope retention.Scope) (retention.Hold, error) {
	sc, err := requirePerson(ctx, security.RoleAdmin)
	if err != nil {
		return retention.Hold{}, err
	}
	if err := checkHoldReason(reason); err != nil {
		return retention.Hold{}, err
	}
	var out retention.Hold
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.LockHolds(ctx); err != nil {
			return err
		}
		cur, err := s.store.Hold(ctx, holdID)
		if err != nil {
			return err
		}
		if !cur.Active() {
			return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				"The hold has been released. A release is final; a matter that revives places a new hold.")
		}
		e := retention.HoldEvent{Hold: holdID, Seq: len(cur.History) + 1, Kind: kind, Scope: scope, Reason: reason,
			RecordedAt: s.now(), RecordedBy: sc.Subject()}
		if kind == retention.EventReleased {
			// A release records the scope it ended, so the history reads
			// without folding.
			e.Scope = cur.Scope
		}
		if err := s.store.AppendHoldEvent(ctx, e); err != nil {
			return err
		}
		if out, err = s.store.Hold(ctx, holdID); err != nil {
			return err
		}
		action := "LEGAL_HOLD_SCOPE_CHANGED"
		if kind == retention.EventReleased {
			action = "LEGAL_HOLD_RELEASED"
		}
		return s.record(ctx, sc, action, "legal_hold", holdID.String(), scopeFields(e.Scope)...)
	})
	return out, err
}

// Hold reads one hold. The read is recorded as privileged evidence access.
func (s *RetentionService) Hold(ctx context.Context, holdID id.LegalHoldID) (retention.Hold, error) {
	sc, err := requirePerson(ctx, security.RoleAdmin, security.RoleAuditor)
	if err != nil {
		return retention.Hold{}, err
	}
	var out retention.Hold
	err = s.inTx(ctx, func(ctx context.Context) error {
		if out, err = s.store.Hold(ctx, holdID); err != nil {
			return err
		}
		return s.record(ctx, sc, "LEGAL_HOLD_READ", "legal_hold", holdID.String())
	})
	return out, err
}

// Holds lists the tenant's holds. The search is recorded as privileged
// evidence access (ZTAX-EVID-REQ-0115).
func (s *RetentionService) Holds(ctx context.Context) ([]retention.Hold, error) {
	sc, err := requirePerson(ctx, security.RoleAdmin, security.RoleAuditor)
	if err != nil {
		return nil, err
	}
	var out []retention.Hold
	err = s.inTx(ctx, func(ctx context.Context) error {
		if out, err = s.store.Holds(ctx); err != nil {
			return err
		}
		return s.record(ctx, sc, "LEGAL_HOLD_SEARCHED", "legal_hold", "*",
			canonical.F("returned", canonical.Integer(int64(len(out)))))
	})
	return out, err
}

// Verdict decides whether a decision's evidence may be disposed of now. It
// is the gate a disposition job passes immediately before it acts: it takes
// the hold lock, so a hold being placed is either seen or waits for it
// (ZTAX-EVID-REQ-0026).
// It returns the instant the verdict was taken at.
func (s *RetentionService) Verdict(ctx context.Context, decisionID id.DecisionID) (retention.Record, retention.Verdict, time.Time, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleAdmin, security.RoleAuditor); err != nil {
		return retention.Record{}, retention.Verdict{}, time.Time{}, err
	}
	var (
		rec retention.Record
		v   retention.Verdict
		at  time.Time
	)
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.store.LockHolds(ctx); err != nil {
			return err
		}
		d, err := s.decisions.ByID(ctx, decisionID)
		if err != nil {
			return err
		}
		// Decisions are recorded for the tenant's default legal entity; its
		// country is the jurisdiction whose retention law governs.
		le, err := s.entities.Default(ctx)
		if err != nil && !errs.IsCategory(err, errs.CategoryNotFound) {
			return err
		}
		rec = retention.Record{Class: retention.ClassDecision, Ref: decisionID.String(), LegalEntity: le.ID, Country: le.Country,
			BusinessKey: d.BusinessKey, Decision: decisionID, EventTime: d.EventTime.UTC(), RecordedAt: d.RecordedAt.UTC()}
		policies, err := s.store.Policies(ctx)
		if err != nil {
			return err
		}
		holds, err := s.store.Holds(ctx)
		if err != nil {
			return err
		}
		at = s.now()
		v = retention.Evaluate(rec, policies, holds, at)
		return nil
	})
	return rec, v, at, err
}

func checkHoldReason(reason string) error {
	if strings.TrimSpace(reason) == "" || len(reason) > 1000 {
		return errs.Invalid("reason", errs.ReasonMissingField, "Every change to a hold gives a reason of at most 1000 characters.")
	}
	return nil
}

func validScope(scope retention.Scope) error {
	if err := scope.Validate(); err != nil {
		return errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
			strings.TrimPrefix(err.Error(), "retention: ")+" (ZTAX-PRIV-REQ-0051).")
	}
	return nil
}

func scopeFields(scope retention.Scope) []canonical.Field {
	fs := []canonical.Field{
		canonical.F("businessKeys", canonical.Integer(int64(len(scope.BusinessKeys)))),
		canonical.F("decisions", canonical.Integer(int64(len(scope.Decisions)))),
	}
	if !scope.EventFrom.IsZero() {
		fs = append(fs, canonical.F("eventFrom", canonical.String(scope.EventFrom.UTC().Format(time.RFC3339))),
			canonical.F("eventTo", canonical.String(scope.EventTo.UTC().Format(time.RFC3339))))
	}
	return fs
}

// record appends to the administrative audit trail.
func (s *RetentionService) record(ctx context.Context, sc security.Context, action, subjectType, subjectID string, detail ...canonical.Field) error {
	auditID, err := idgen.AuditID(s.ids)
	if err != nil {
		return internal(err, "The action could not be recorded.")
	}
	body, err := canonical.Encode(canonical.Object(detail...))
	if err != nil {
		return internal(err, "The action could not be recorded.")
	}
	actor := sc.Subject()
	return s.audit.Append(ctx, port.AuditRecord{
		ID: auditID, TenantID: sc.Tenant(), ActorUserID: &actor, Action: action,
		SubjectType: subjectType, SubjectID: subjectID, Detail: body, RecordedAt: s.now(),
	})
}

func (s *RetentionService) inTx(ctx context.Context, fn func(context.Context) error) error {
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
