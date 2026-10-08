package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/reconciliation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/settlement"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Reconciliation runs (W2 lane J; ZTAX-FIN-001 §17–§20). The comparisons are
// internal/domain/reconciliation's; this assembles what a legal period's
// stages compare and records every item.
//
// What is compared today:
//
//   - R1, calculated to document: each current decision whose event fell in
//     the period, its decided tax against the tax every document presents
//     for it, voids and credits included. A decision no document presents is
//     MISSING — calculated and never documented.
//   - R3, decision to subledger: the same decided tax against what its own
//     posting put on TAX_COLLECTED_LIABILITY, and each completed refund of it
//     against its REFUND journal.
//
// R2 (collection), R4 (return), R5 (remittance) and R6 (GL) compare records
// this cell does not hold yet. They are reported unavailable, by name, never
// left out — so a clean run says which stages it did not look at
// (ZTAX-FIN-REQ-0077). R7 is the trace: the first stage, in order, with an
// exception. There is no tolerance: none is configured, and a tolerance is
// never assumed (ZTAX-FIN-REQ-0080).

// ReconciliationService runs and resolves reconciliations.
type ReconciliationService struct {
	docs    *DocumentService
	refunds port.RefundRepository
	recon   port.ReconciliationRepository
}

// NewReconciliationService wires the service over the document service whose
// decided-tax rule R1 and R3 share.
func NewReconciliationService(docs *DocumentService, refunds port.RefundRepository, recon port.ReconciliationRepository) *ReconciliationService {
	return &ReconciliationService{docs: docs, refunds: refunds, recon: recon}
}

// RunItemView is one item and its resolution, if it has one.
type RunItemView struct {
	reconciliation.RunItem
	Resolution *port.ReconResolution
}

// RunView is a run as read back.
type RunView struct {
	Run   reconciliation.Run
	Items []RunItemView
}

// ResolutionInput is a person's resolution of one exception.
type ResolutionInput struct {
	Reason   string
	Action   reconciliation.Action
	Cause    reconciliation.RootCause
	Evidence []string
}

// Run reconciles a legal period of the tenant's default legal entity.
func (s *ReconciliationService) Run(ctx context.Context, period string) (RunView, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleOperator)
	if err != nil {
		return RunView{}, err
	}
	if err := s.docs.wired(); err != nil {
		return RunView{}, err
	}
	p, err := subledger.ParseLegalPeriod(period)
	if err != nil {
		return RunView{}, errs.Invalid("period", errs.ReasonInvalidValue, "A legal period is a month, YYYY-MM.")
	}
	start, _ := time.Parse("2006-01", p)
	le, err := s.docs.det.defaultLegalEntity(ctx)
	if err != nil {
		return RunView{}, err
	}
	det := s.docs.det

	decisions, err := det.decisions.CurrentInWindow(ctx, start, start.AddDate(0, 1, 0))
	if err != nil {
		return RunView{}, err
	}
	type scoped struct {
		id      id.DecisionID
		decided fiscal.Money
	}
	var inScope []scoped
	for _, d := range decisions {
		components, err := s.docs.decidedTax(ctx, d.DecisionID, "")
		if errs.IsCategory(err, errs.CategoryValidation) {
			// The decision's content declares no tax: nothing to trace.
			continue
		}
		if err != nil {
			return RunView{}, err
		}
		total, ok, err := sumComponents(components)
		if err != nil {
			return RunView{}, err
		}
		if ok {
			inScope = append(inScope, scoped{d.DecisionID, total})
		}
	}
	ids := make([]id.DecisionID, len(inScope))
	for i, d := range inScope {
		ids[i] = d.id
	}
	documented, err := s.docs.docs.TaxByDecision(ctx, ids)
	if err != nil {
		return RunView{}, err
	}

	var r1, r3 []reconciliation.Item
	none := reconciliation.Policy{}
	for _, d := range inScope {
		key := reconciliation.MatchKey("decision:" + d.id.String())
		decided := d.decided
		var observed *fiscal.Money
		if m, ok := documented[d.id]; ok {
			observed = &m
		}
		it, err := reconciliation.Compare(reconciliation.R1CalculatedToDocument, key, &decided, observed, none, false)
		if err != nil {
			return RunView{}, err
		}
		r1 = append(r1, it)

		posted, err := s.collected(ctx, SourceDecisionCommitted, d.id.String())
		if err != nil {
			return RunView{}, err
		}
		if it, err = reconciliation.Compare(reconciliation.R3DocumentToSubledger, key, &decided, posted, none, false); err != nil {
			return RunView{}, err
		}
		r3 = append(r3, it)

		refunds, err := s.refunds.ForDecision(ctx, d.id)
		if err != nil {
			return RunView{}, err
		}
		for _, rf := range refunds {
			if rf.Current.Status != settlement.RefundCompleted {
				continue
			}
			expected := rf.Refund.Amount.Neg()
			posted, err := s.collected(ctx, SourceRefundCompleted, rf.Refund.ID.String())
			if err != nil {
				return RunView{}, err
			}
			it, err := reconciliation.Compare(reconciliation.R3DocumentToSubledger,
				reconciliation.MatchKey("refund:"+rf.Refund.ID.String()), &expected, posted, none, false)
			if err != nil {
				return RunView{}, err
			}
			r3 = append(r3, it)
		}
	}
	trace := reconciliation.EndToEnd(map[reconciliation.Stage]reconciliation.StageResult{
		reconciliation.R1CalculatedToDocument: {Stage: reconciliation.R1CalculatedToDocument, Available: true, Items: r1},
		reconciliation.R3DocumentToSubledger:  {Stage: reconciliation.R3DocumentToSubledger, Available: true, Items: r3},
	})

	runID, err := idgen.ReconciliationID(det.ids)
	if err != nil {
		return RunView{}, internal(err, "The run could not be recorded.")
	}
	run := reconciliation.Run{
		ID: runID, TenantID: sc.Tenant(), LegalEntity: le.ID, Period: p,
		RanAt: det.clock.Now().UTC().Truncate(time.Microsecond), RanBy: sc.Subject(),
		FirstBreak: trace.FirstBreak, Unavailable: trace.Unavailable,
	}
	var items []reconciliation.RunItem
	for _, it := range append(r1, r3...) {
		itemID, err := idgen.ReconItemID(det.ids)
		if err != nil {
			return RunView{}, internal(err, "The run could not be recorded.")
		}
		items = append(items, reconciliation.RunItem{ID: itemID, Run: runID, Item: it})
	}
	if err := s.inTx(ctx, func(ctx context.Context) error { return s.recon.CreateRun(ctx, run, items) }); err != nil {
		return RunView{}, err
	}
	v := RunView{Run: run, Items: make([]RunItemView, len(items))}
	for i, it := range items {
		v.Items[i] = RunItemView{RunItem: it}
	}
	return v, nil
}

// sumComponents totals a decision's components; false when it has none.
func sumComponents(components map[string]fiscal.Money) (fiscal.Money, bool, error) {
	names := make([]string, 0, len(components))
	for n := range components {
		names = append(names, n)
	}
	sort.Strings(names)
	var (
		total fiscal.Money
		found bool
	)
	for _, n := range names {
		if !found {
			total, found = components[n], true
			continue
		}
		var err error
		if total, err = total.Add(components[n]); err != nil {
			return fiscal.Money{}, false, err
		}
	}
	return total, found, nil
}

// collected is the net movement one source event's journals put on
// TAX_COLLECTED_LIABILITY, credits positive; nil when it posted nothing.
func (s *ReconciliationService) collected(ctx context.Context, kind, sourceID string) (*fiscal.Money, error) {
	journals, err := s.docs.det.fiscal.Journals.BySource(ctx, kind, sourceID)
	if err != nil {
		return nil, err
	}
	var (
		total fiscal.Money
		found bool
	)
	for _, j := range journals {
		for _, l := range j.Lines {
			if l.Account != subledger.AccountTaxCollectedLiability {
				continue
			}
			amt := l.Amount
			if l.Side == subledger.Debit {
				amt = amt.Neg()
			}
			if !found {
				total, found = amt, true
				continue
			}
			if total, err = total.Add(amt); err != nil {
				return nil, err
			}
		}
	}
	if !found {
		return nil, nil
	}
	return &total, nil
}

// RunByID reads a run with its items and resolutions.
func (s *ReconciliationService) RunByID(ctx context.Context, runID id.ReconciliationID) (RunView, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return RunView{}, err
	}
	run, items, resolutions, err := s.recon.Run(ctx, runID)
	if err != nil {
		return RunView{}, err
	}
	byItem := map[id.ReconItemID]port.ReconResolution{}
	for _, r := range resolutions {
		byItem[r.Item] = r
	}
	v := RunView{Run: run, Items: make([]RunItemView, len(items))}
	for i, it := range items {
		v.Items[i] = RunItemView{RunItem: it}
		if r, ok := byItem[it.ID]; ok {
			r := r
			v.Items[i].Resolution = &r
		}
	}
	return v, nil
}

// Resolve records a person's resolution of one exception item
// (ZTAX-FIN-REQ-0084): resolver, reason, action, root cause and evidence, or
// nothing. A matched item is not an exception and is not resolved.
func (s *ReconciliationService) Resolve(ctx context.Context, runID id.ReconciliationID, itemID id.ReconItemID, in ResolutionInput) (RunItemView, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleOperator)
	if err != nil {
		return RunItemView{}, err
	}
	if sc.Subject().IsZero() {
		return RunItemView{}, errs.New(errs.CategoryPolicy, errs.ReasonForbidden, "An exception is resolved by a person.")
	}
	if len(in.Reason) > 1000 || len(in.Evidence) > 50 {
		return RunItemView{}, errs.Invalid("reason", errs.ReasonInvalidValue, "A reason is at most 1000 characters, with at most 50 evidence references.")
	}
	for _, e := range in.Evidence {
		if strings.TrimSpace(e) == "" || len(e) > 500 {
			return RunItemView{}, errs.Invalid("evidence", errs.ReasonInvalidValue, "Each evidence reference is between 1 and 500 characters.")
		}
	}
	it, err := s.recon.Item(ctx, itemID)
	if err != nil {
		return RunItemView{}, err
	}
	if it.Run != runID {
		return RunItemView{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such item in this run.")
	}
	res := reconciliation.Resolution{
		Actor: reconciliation.ActorHuman, Resolver: sc.Subject(), Reason: in.Reason, Action: in.Action, Cause: in.Cause,
		Evidence: in.Evidence, At: s.docs.det.clock.Now().UTC().Truncate(time.Microsecond),
	}
	resolved, err := reconciliation.Resolve(it.Item, res)
	if err != nil {
		if !it.Status.Exception() {
			return RunItemView{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				fmt.Sprintf("The item is %s; only an exception is resolved.", it.Status))
		}
		return RunItemView{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonMissingField,
			"A resolution records its reason, action, root cause and at least one piece of evidence (ZTAX-FIN-REQ-0084).")
	}
	stored := port.ReconResolution{Item: itemID, Resolution: res}
	if err := s.recon.Resolve(ctx, stored); err != nil {
		return RunItemView{}, err
	}
	it.Item = resolved
	return RunItemView{RunItem: it, Resolution: &stored}, nil
}

func (s *ReconciliationService) inTx(ctx context.Context, fn func(context.Context) error) error {
	tx, txCtx, err := s.docs.det.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(txCtx)
}
