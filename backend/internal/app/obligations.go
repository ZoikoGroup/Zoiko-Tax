package app

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Obligations assessed from committed decisions (W2 lane I).
//
// A commit under content that declares obligations assesses its tax into the
// obligation for the period its event falls in, creating that obligation the
// first time the period sees a decision. The assessed amount is the sum of the
// per-decision contributions, never a figure carried forward; an obligation
// row is written only when the obligation is created or its status changes,
// so the chain of rows is its lifecycle and nothing else (ADR-0003 §2.2).

// placed is one obligation declaration resolved for an event time.
type placed struct {
	decl   content.ObligationDeclaration
	key    string
	period obligation.Period
	loc    *time.Location
}

// placeObligation resolves a declaration for an event time: the period the
// event falls in, the due date, and the business key — definition, legal
// entity and period start, so a new content version extends the period's
// obligation rather than opening a second one.
func placeObligation(decl content.ObligationDeclaration, le id.LegalEntityID, eventTime time.Time) (placed, error) {
	loc, err := time.LoadLocation(decl.Period.Timezone)
	if err != nil {
		return placed{}, internal(err, fmt.Sprintf("The content names legal timezone %q, which this cell cannot load.", decl.Period.Timezone))
	}
	pr := obligation.PeriodRule{Kind: obligation.PeriodKind(decl.Period.Kind), Timezone: loc.String(), YearStartMonth: time.Month(decl.Period.YearStartMonth)}
	span, err := pr.PeriodFor(eventTime, loc)
	if err != nil {
		return placed{}, internal(err, "The content's obligation period cannot place this event.")
	}
	due := obligation.DueDateRule{
		Anchor: obligation.AnchorPeriodEnd, OffsetMonths: decl.Due.OffsetMonths, DayOfMonth: decl.Due.DayOfMonth,
		OffsetDays: decl.Due.OffsetDays, Adjustment: obligation.AdjustNone,
	}
	dates, err := due.Compute(span, loc, nil)
	if err != nil {
		return placed{}, internal(err, "The content's obligation due date cannot be computed.")
	}
	start := span.Start.In(loc)
	return placed{
		decl: decl, loc: loc,
		key:    decl.ID + "/" + le.String() + "/" + start.Format(time.DateOnly),
		period: obligation.Period{Start: start, End: span.End.In(loc).AddDate(0, 0, -1), Due: dates.Legal},
	}, nil
}

// assessedAmount is what a decision assesses into a declaration: the sum of
// the named slots it emitted, or false when it emitted none of them. A zero
// sum is an assessment — an exempt supply is still reported in the period's
// return.
func assessedAmount(decl content.ObligationDeclaration, result evidence.Result) (fiscal.Money, bool, error) {
	cur := fiscal.Currency(decl.Currency)
	var (
		sum   fiscal.Money
		found bool
	)
	for _, slot := range decl.Assesses {
		v, ok := result.Emitted[slot]
		if !ok || v.Type != rule.TypeMoney {
			continue
		}
		if v.Money.Currency() != cur {
			return fiscal.Money{}, false, errs.Invalid("currency", errs.ReasonCurrencyMismatch,
				fmt.Sprintf("Obligation %s is assessed in %s and this decision emits %s.", decl.ID, cur, v.Money.Currency()))
		}
		if !found {
			sum, found = v.Money, true
			continue
		}
		next, err := sum.Add(v.Money)
		if err != nil {
			return fiscal.Money{}, false, internal(err, "The assessed amounts could not be combined.")
		}
		sum = next
	}
	return sum, found, nil
}

// assessment is one business key's change from one commit.
type assessment struct {
	at            placed
	contributions []port.ObligationContribution
}

// assess records a decision's contributions to the obligations its content
// declares — and, for a correction, withdraws what the corrected decision
// assessed — then brings each touched obligation's status in line.
func (s *DeterminationService) assess(ctx context.Context, tenant id.TenantID, b *rule.Bundle, d evidence.Decision,
	prior *evidence.Record, priorEventTime time.Time) error {
	p := b.Fiscal()
	if p == nil || len(p.Obligations) == 0 {
		return nil
	}
	le, err := s.defaultLegalEntity(ctx)
	if err != nil {
		return err
	}
	byKey := map[string]*assessment{}
	add := func(at placed, c port.ObligationContribution) {
		a, ok := byKey[at.key]
		if !ok {
			a = &assessment{at: at}
			byKey[at.key] = a
		}
		c.BusinessKey = at.key
		a.contributions = append(a.contributions, c)
	}
	for _, decl := range p.Obligations {
		if prior != nil {
			was, err := placeObligation(decl, le.ID, priorEventTime)
			if err != nil {
				return err
			}
			x, err := s.fiscal.Obligations.Contribution(ctx, was.key, prior.DecisionID, port.ContributionAssess)
			switch {
			case errs.IsCategory(err, errs.CategoryNotFound):
				// The corrected decision assessed nothing here: it was
				// committed before this content declared the obligation.
			case err != nil:
				return err
			default:
				add(was, port.ObligationContribution{DecisionID: d.ID, Kind: port.ContributionWithdraw, Amount: x.Neg(), RecordedAt: d.Envelope.DecisionTime})
			}
		}
		y, ok, err := assessedAmount(decl, d.Result)
		if err != nil {
			return err
		}
		if ok {
			at, err := placeObligation(decl, le.ID, d.Envelope.EventTime)
			if err != nil {
				return err
			}
			add(at, port.ObligationContribution{DecisionID: d.ID, Kind: port.ContributionAssess, Amount: y, RecordedAt: d.Envelope.DecisionTime})
		}
	}

	// Business-key order, after the accumulator locks: every writer takes the
	// same locks in the same order.
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a := byKey[k]
		if err := s.fiscal.Obligations.Lock(ctx, k); err != nil {
			return err
		}
		for _, c := range a.contributions {
			inserted, err := s.fiscal.Obligations.Contribute(ctx, c)
			if err != nil {
				return err
			}
			if !inserted {
				return errs.New(errs.CategoryInternal, errs.ReasonInternal,
					fmt.Sprintf("Decision %s had already been assessed into %s.", d.ID, k))
			}
		}
		if err := s.settleObligation(ctx, tenant, le, b, a.at, d.Envelope.DecisionTime); err != nil {
			return err
		}
	}
	return nil
}

// settleObligation creates the obligation on its first assessment and moves
// it when an assessment changes figures somebody already acted on.
func (s *DeterminationService) settleObligation(ctx context.Context, tenant id.TenantID, le identity.LegalEntity,
	b *rule.Bundle, at placed, now time.Time) error {
	sums, err := s.fiscal.Obligations.Assessed(ctx, []string{at.key})
	if err != nil {
		return err
	}
	total, ok := sums[at.key]
	if !ok {
		return errs.New(errs.CategoryInternal, errs.ReasonInternal, "An obligation was assessed and has no contributions.")
	}
	cur, err := s.fiscal.Obligations.Current(ctx, at.key)
	if errs.IsCategory(err, errs.CategoryNotFound) {
		oid, err := idgen.ObligationID(s.ids)
		if err != nil {
			return internal(err, "The obligation could not be created.")
		}
		return s.appendObligation(ctx, obligation.Obligation{
			ID: oid, TenantID: tenant, BusinessKey: at.key, RecordedAt: now,
			JurisdictionID: at.decl.Jurisdiction, Type: at.decl.Type, Period: at.period,
			Status: obligation.StatusOpen, Assessed: &total,
			LegalEntity: le.ID, Authority: at.decl.Authority,
			Definition: obligation.DefinitionRef{ID: at.decl.ID, Version: at.decl.Version},
			Content:    obligation.ContentRef{BundleID: b.ID(), BundleDigest: b.Digest()},
			Duty:       obligation.DutyKind(at.decl.Duty), Due: obligation.DueDates{Legal: at.period.Due},
			Timezone: at.loc.String(),
		}, nil)
	}
	if err != nil {
		return err
	}
	var to obligation.Status
	switch cur.Status {
	case obligation.StatusOpen, obligation.StatusDataRequired, obligation.StatusSuspended,
		obligation.StatusAmendmentRequired, obligation.StatusRejected:
		// Still being prepared: the figure changes and the status does not.
		return nil
	case obligation.StatusReady:
		// Readiness was confirmed for other figures.
		to = obligation.StatusOpen
	case obligation.StatusAccepted, obligation.StatusPaymentDue, obligation.StatusPaid:
		to = obligation.StatusAmendmentRequired
	case obligation.StatusFiled, obligation.StatusUncertain:
		return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("The %s return for the period starting %s is %s with the authority; a decision in that period waits until the authority answers.",
				at.decl.Type, at.period.Start.Format(time.DateOnly), cur.Status))
	default:
		return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("The %s obligation for the period starting %s is %s; reopen it before committing into it.",
				at.decl.Type, at.period.Start.Format(time.DateOnly), cur.Status))
	}
	oid, err := idgen.ObligationID(s.ids)
	if err != nil {
		return internal(err, "The obligation could not be updated.")
	}
	next, err := cur.Transition(to, now, oid)
	if err != nil {
		return err
	}
	next.Assessed = &total
	next.RecordedBy = id.UserID{}
	return s.appendObligation(ctx, next, &cur)
}

// defaultLegalEntity is the entity a commit posts and assesses for.
func (s *DeterminationService) defaultLegalEntity(ctx context.Context) (identity.LegalEntity, error) {
	le, err := s.fiscal.LegalEntities.Default(ctx)
	if errs.IsCategory(err, errs.CategoryNotFound) {
		return identity.LegalEntity{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"The tenant has no default legal entity to post for.")
	}
	return le, err
}

// ---------------------------------------------------------------------------
// Obligations, read and moved
// ---------------------------------------------------------------------------

// ObligationView is an obligation's current row with its live assessment and
// its status as of now, OVERDUE derived.
type ObligationView struct {
	obligation.Obligation
	EffectiveStatus obligation.Status
	// SupersededBy is set on a row that is history. Its figures are the ones
	// it was written with; the live assessment belongs to the current row.
	SupersededBy *id.ObligationID
}

// manualTargets are the statuses a user moves an obligation to. The rest are
// the authority boundary's to set: a submission files, the authority accepts
// or rejects, a payment pays.
var manualTargets = map[obligation.Status]bool{
	obligation.StatusOpen: true, obligation.StatusDataRequired: true, obligation.StatusReady: true,
	obligation.StatusSuspended: true, obligation.StatusClosed: true,
}

func (s *DeterminationService) obligationsWired() error {
	if s.fiscal == nil || !s.fiscal.complete() {
		return errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable, "Obligations are not wired in this cell.")
	}
	return nil
}

// Obligations lists the tenant's current obligations, by due date.
func (s *DeterminationService) Obligations(ctx context.Context, status obligation.Status, limit int) ([]ObligationView, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return nil, err
	}
	if err := s.obligationsWired(); err != nil {
		return nil, err
	}
	// OVERDUE is derived, never stored: filter on it after deriving.
	stored := status
	if status == obligation.StatusOverdue {
		stored = ""
	}
	rows, err := s.fiscal.Obligations.List(ctx, port.ObligationFilter{Status: stored, Limit: limit})
	if err != nil {
		return nil, err
	}
	views, err := s.views(ctx, rows)
	if err != nil {
		return nil, err
	}
	if status != obligation.StatusOverdue {
		return views, nil
	}
	out := views[:0]
	for _, v := range views {
		if v.EffectiveStatus == obligation.StatusOverdue {
			out = append(out, v)
		}
	}
	return out, nil
}

// Obligation reads one obligation row, current or superseded.
func (s *DeterminationService) Obligation(ctx context.Context, obligationID id.ObligationID) (ObligationView, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return ObligationView{}, err
	}
	if err := s.obligationsWired(); err != nil {
		return ObligationView{}, err
	}
	o, err := s.fiscal.Obligations.ByID(ctx, obligationID)
	if err != nil {
		return ObligationView{}, err
	}
	cur, err := s.fiscal.Obligations.Current(ctx, o.BusinessKey)
	if err != nil {
		return ObligationView{}, err
	}
	if cur.ID != o.ID {
		// A superseded row is reported as written: no live figure, and the
		// status it had, not one derived for today.
		next := cur.ID
		return ObligationView{Obligation: o, EffectiveStatus: o.Status, SupersededBy: &next}, nil
	}
	views, err := s.views(ctx, []obligation.Obligation{o})
	if err != nil {
		return ObligationView{}, err
	}
	return views[0], nil
}

// TransitionObligation moves an obligation to a status a user may set. The
// identifier must be the obligation's current row: a move made against a row
// somebody has since superseded is refused, not applied to figures the user
// never saw.
func (s *DeterminationService) TransitionObligation(ctx context.Context, obligationID id.ObligationID, to obligation.Status) (ObligationView, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleOperator)
	if err != nil {
		return ObligationView{}, err
	}
	if err := s.obligationsWired(); err != nil {
		return ObligationView{}, err
	}
	if !manualTargets[to] {
		return ObligationView{}, errs.Invalid("to", errs.ReasonInvalidValue,
			fmt.Sprintf("%q is not a status an obligation is moved to by hand; OPEN, DATA_REQUIRED, READY, SUSPENDED or CLOSED.", to))
	}
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return ObligationView{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	o, err := s.fiscal.Obligations.ByID(txCtx, obligationID)
	if err != nil {
		return ObligationView{}, err
	}
	if err := s.fiscal.Obligations.Lock(txCtx, o.BusinessKey); err != nil {
		return ObligationView{}, err
	}
	cur, err := s.fiscal.Obligations.Current(txCtx, o.BusinessKey)
	if err != nil {
		return ObligationView{}, err
	}
	if cur.ID != o.ID {
		return ObligationView{}, errs.New(errs.CategoryConflict, errs.ReasonOptimisticConflict,
			fmt.Sprintf("Obligation %s has been superseded by %s; read it again and retry.", o.ID, cur.ID))
	}
	sums, err := s.fiscal.Obligations.Assessed(txCtx, []string{cur.BusinessKey})
	if err != nil {
		return ObligationView{}, err
	}
	oid, err := idgen.ObligationID(s.ids)
	if err != nil {
		return ObligationView{}, internal(err, "The obligation could not be moved.")
	}
	next, err := cur.Transition(to, s.clock.Now().UTC().Truncate(time.Microsecond), oid)
	if err != nil {
		return ObligationView{}, err
	}
	if total, ok := sums[cur.BusinessKey]; ok {
		next.Assessed = &total
	}
	next.RecordedBy = sc.Subject()
	if err := s.appendObligation(txCtx, next, &cur); err != nil {
		return ObligationView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ObligationView{}, err
	}
	views, err := s.views(ctx, []obligation.Obligation{next})
	if err != nil {
		return ObligationView{}, err
	}
	return views[0], nil
}

// views attaches each obligation's live assessment and its status as of
// today in its own legal calendar: a return due on the 20th is not overdue on
// the 20th.
func (s *DeterminationService) views(ctx context.Context, rows []obligation.Obligation) ([]ObligationView, error) {
	keys := make([]string, len(rows))
	for i, o := range rows {
		keys[i] = o.BusinessKey
	}
	sums, err := s.fiscal.Obligations.Assessed(ctx, keys)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	out := make([]ObligationView, len(rows))
	for i, o := range rows {
		if total, ok := sums[o.BusinessKey]; ok {
			o.Assessed = &total
		}
		today := now.In(o.Period.Due.Location())
		today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())
		out[i] = ObligationView{Obligation: o, EffectiveStatus: o.EffectiveStatus(today)}
	}
	return out, nil
}
