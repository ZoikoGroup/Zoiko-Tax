package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/idempotency"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/outbox"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/settlement"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Refunds (ZTAX-FIN-001 §14; W2 lane K's POST /v1/transactions:refund).
//
// A refund returns tax a committed decision charged. It is not a decision:
// it re-determines nothing, and the decision it refunds is never rewritten
// (ZTAX-FIN-REQ-0015). It has a lifecycle of its own, driven by what the
// payment provider reports (ZTAX-FIN-REQ-0059), and ZoikoTax moves no money
// in any of it (ZTAX-FIN-REQ-0067).
//
// What it may return is bounded by the ledger, not by the request: the tax
// the decision posted to TAX_COLLECTED_LIABILITY, less every refund of it that
// has not failed. An UNCERTAIN refund still counts — it may have paid — so a
// timeout cannot be turned into a second refund of the same tax by asking
// again. Admission serializes on the decision, so two concurrent refunds
// cannot both find the same tax still available.
//
// Only a COMPLETED refund posts. Until the provider confirms the money went
// back, the liability is still owed to the authority, and the ledger says so.

// RefundEndpoint scopes refund's idempotency keys, apart from commit's and
// adjust's (ADR-0013 §2.2).
const RefundEndpoint = "POST /v1/transactions:refund"

// SourceRefundCompleted is the source event a completed refund posts under.
const SourceRefundCompleted = "REFUND_COMPLETED"

// The refund events (contracts/schemas/events/refund-*).
const (
	EventRefundRequested         = outbox.TypeNamespace + ".refund.requested"
	SchemaRefundRequestedRef     = "ztax:events/refund-requested/1.0.0"
	EventRefundStatusChanged     = outbox.TypeNamespace + ".refund.status-changed"
	SchemaRefundStatusChangedRef = "ztax:events/refund-status-changed/1.0.0"
)

// refundPosting is the Tax Control Subledger profile a completed refund posts
// under: the collected liability falls by the tax returned, against the cash
// clearing account the money left through.
//
// It is the cell's rather than the content's. Which tax a decision charged,
// at what rate and to which authority, is jurisdictional law and comes from
// the signed bundle that made the decision; that a returned amount reduces
// the liability it was posted to is the subledger's own double entry
// (ZTAX-FIN-001 §9), the same in every jurisdiction, and versioned here.
var refundPosting = subledger.PostingProfileEntry{
	Type: subledger.JournalRefund,
	Lines: []subledger.PostingRule{
		{Account: subledger.AccountTaxCollectedLiability, Side: subledger.Debit, Amount: "TAX"},
		{Account: subledger.AccountTaxCashClearing, Side: subledger.Credit, Amount: "TAX"},
	},
}

var refundPostingRef = subledger.ProfileRef{ID: "tcsl-refund", Version: "1"}

// RefundService records refunds and what providers report about them.
type RefundService struct {
	decisions   port.DecisionRepository
	journals    port.JournalRepository
	refunds     port.RefundRepository
	outbox      port.OutboxWriter
	tx          port.TxManager
	clock       clock.Clock
	ids         idgen.Generator
	idempotency *Idempotency
}

// NewRefundService wires the service.
func NewRefundService(decisions port.DecisionRepository, journals port.JournalRepository, refunds port.RefundRepository,
	ob port.OutboxWriter, tx port.TxManager, clk clock.Clock, ids idgen.Generator, g *Idempotency) *RefundService {
	return &RefundService{
		decisions: decisions, journals: journals, refunds: refunds, outbox: ob,
		tx: tx, clock: clk, ids: ids, idempotency: g,
	}
}

// RefundView is a refund with its history, oldest event first.
type RefundView struct {
	Refund  settlement.Refund
	History []settlement.RefundEvent
}

// Current is the refund's latest event.
func (v RefundView) Current() settlement.RefundEvent { return v.History[len(v.History)-1] }

// RefundInput is one refund request.
type RefundInput struct {
	IdempotencyKey string
	Decision       id.DecisionID
	Amount         fiscal.Money
	PaymentRef     string
	Reason         string
	// Render produces the response a successful request returns; its bytes
	// are what every retry of the key receives.
	Render        func(RefundView) ([]byte, error)
	RenderFailure func(error) Response
}

// Request admits a refund at most once per idempotency key.
func (s *RefundService) Request(ctx context.Context, in RefundInput) (Settled, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleOperator)
	if err != nil {
		return Settled{}, err
	}
	if s.idempotency == nil {
		return Settled{}, errs.New(errs.CategoryInternal, errs.ReasonInternal,
			"The refund service was wired without an idempotency guard.")
	}
	if err := validateRefundInput(in); err != nil {
		return Settled{}, err
	}
	digest, err := refundDigest(in)
	if err != nil {
		return Settled{}, err
	}
	return s.idempotency.Do(ctx, Call{
		Key:    idempotency.Key{TenantID: sc.Tenant(), Endpoint: RefundEndpoint, Value: in.IdempotencyKey},
		Digest: digest,
		// A refund key lives as long as a commit key, for the same reason: a
		// late retry of an expired key would request the refund again.
		Retention: CommitRetention,
		Execute: func(ctx context.Context) (Response, error) {
			v, err := s.admit(ctx, sc, in)
			if err != nil {
				return Response{}, err
			}
			body, err := in.Render(v)
			if err != nil {
				return Response{}, internal(err, "The refund could not be rendered.")
			}
			return Response{Status: 201, Body: body}, nil
		},
		RenderFailure: in.RenderFailure,
	})
}

func validateRefundInput(in RefundInput) error {
	switch {
	case in.Decision.IsZero():
		return errs.Invalid("decisionId", errs.ReasonMissingField, "A refund names the decision whose tax it returns.")
	case in.Amount.Sign() <= 0:
		return errs.Invalid("amount", errs.ReasonInvalidValue, "A refund is a positive amount of tax.")
	case strings.TrimSpace(in.PaymentRef) == "" || len(in.PaymentRef) > settlement.MaxPaymentRefLength:
		return errs.Invalid("paymentReference", errs.ReasonMissingField,
			fmt.Sprintf("A payment reference of at most %d characters is required.", settlement.MaxPaymentRefLength))
	case len(in.Reason) > settlement.MaxRefundReasonLength:
		return errs.Invalid("reason", errs.ReasonInvalidValue,
			fmt.Sprintf("A reason is at most %d characters.", settlement.MaxRefundReasonLength))
	}
	return nil
}

// refundDigest is the ADR-0013 §2.3 request digest.
func refundDigest(in RefundInput) (canonical.Digest, error) {
	d, err := canonical.Sum(canonical.Object(
		canonical.F("decisionId", canonical.String(in.Decision.String())),
		canonical.F("amount", canonical.String(in.Amount.CanonicalString())),
		canonical.F("currency", canonical.String(string(in.Amount.Currency()))),
		canonical.F("paymentReference", canonical.String(in.PaymentRef)),
		canonical.F("reason", canonical.String(in.Reason)),
	))
	if err != nil {
		return canonical.Digest{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
			"The request cannot be put in canonical form.")
	}
	return d, nil
}

// admit records a refund inside the idempotency transaction.
func (s *RefundService) admit(ctx context.Context, sc security.Context, in RefundInput) (RefundView, error) {
	if err := s.refunds.LockDecision(ctx, in.Decision); err != nil {
		return RefundView{}, err
	}
	rec, err := s.decisions.ByID(ctx, in.Decision)
	if err != nil {
		return RefundView{}, err
	}
	history, err := s.decisions.History(ctx, rec.BusinessKey)
	if err != nil {
		return RefundView{}, err
	}
	for _, h := range history {
		if h.Supersedes != nil && *h.Supersedes == in.Decision {
			return RefundView{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				"That decision has been superseded and its posting reversed. Refund against the current version.")
		}
	}

	collected, le, err := s.collected(ctx, in.Decision, in.Amount.Currency())
	if err != nil {
		return RefundView{}, err
	}
	prior, err := s.refunds.ForDecision(ctx, in.Decision)
	if err != nil {
		return RefundView{}, err
	}
	available := collected
	for _, p := range prior {
		if !p.Current.Status.Reserves() || p.Refund.Amount.Currency() != in.Amount.Currency() {
			continue
		}
		if available, err = available.Sub(p.Refund.Amount); err != nil {
			return RefundView{}, internal(err, "The refunds already made could not be totalled.")
		}
	}
	if over, err := in.Amount.Cmp(available); err != nil {
		return RefundView{}, internal(err, "The refund could not be compared with the tax still refundable.")
	} else if over > 0 {
		return RefundView{}, errs.Invalid("amount", errs.ReasonInvalidValue,
			fmt.Sprintf("The decision has %s %s of collected tax still refundable; %s was requested.",
				available.CanonicalString(), available.Currency(), in.Amount.CanonicalString()))
	}

	refundID, err := idgen.RefundID(s.ids)
	if err != nil {
		return RefundView{}, internal(err, "The refund could not be recorded.")
	}
	rf := settlement.Refund{
		ID: refundID, TenantID: sc.Tenant(), LegalEntity: le, Decision: in.Decision, Amount: in.Amount,
		PaymentRef: in.PaymentRef, Reason: in.Reason,
		RequestedAt: s.clock.Now().UTC().Truncate(time.Microsecond), RequestedBy: sc.Subject(),
	}
	if err := s.refunds.Create(ctx, rf); err != nil {
		return RefundView{}, err
	}
	first := settlement.Requested(rf)
	if err := s.emit(ctx, rf, first, nil); err != nil {
		return RefundView{}, err
	}
	return RefundView{Refund: rf, History: []settlement.RefundEvent{first}}, nil
}

// collected is the tax a decision's own posting put on the collected
// liability in one currency, and the legal entity it was posted for. A
// decision that posted none has nothing to refund in that currency.
func (s *RefundService) collected(ctx context.Context, decisionID id.DecisionID, cur fiscal.Currency) (fiscal.Money, id.LegalEntityID, error) {
	journals, err := s.journals.BySource(ctx, SourceDecisionCommitted, decisionID.String())
	if err != nil {
		return fiscal.Money{}, id.LegalEntityID{}, err
	}
	var (
		total fiscal.Money
		le    id.LegalEntityID
		found bool
	)
	for _, j := range journals {
		if j.Currency != cur {
			continue
		}
		if !found {
			le, found = j.LegalEntity, true
			if total, err = fiscal.ParseMoney("0", cur); err != nil {
				return fiscal.Money{}, id.LegalEntityID{}, internal(err, "The collected tax could not be totalled.")
			}
		}
		for _, l := range j.Lines {
			if l.Account != subledger.AccountTaxCollectedLiability {
				continue
			}
			amt := l.Amount
			if l.Side == subledger.Debit {
				amt = amt.Neg()
			}
			if total, err = total.Add(amt); err != nil {
				return fiscal.Money{}, id.LegalEntityID{}, internal(err, "The collected tax could not be totalled.")
			}
		}
	}
	if !found || total.Sign() <= 0 {
		return fiscal.Money{}, id.LegalEntityID{}, errs.Invalid("amount", errs.ReasonInvalidValue,
			fmt.Sprintf("The decision posted no collected tax in %s, so there is none to refund.", cur))
	}
	return total, le, nil
}

// ReportInput is one provider report on a refund.
type ReportInput struct {
	Outcome     settlement.RefundOutcome
	ExternalRef string
}

// Report records what the payment provider said about a refund.
//
// It is safe to retry without an idempotency key: a report that would leave
// the refund exactly where its latest event already put it, with the same
// outcome and reference, is the same report arriving twice and returns the
// refund unchanged. Any other report the lifecycle does not permit is refused
// (STATE_TRANSITION_INVALID) rather than recorded.
func (s *RefundService) Report(ctx context.Context, refundID id.RefundID, in ReportInput) (RefundView, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleOperator)
	if err != nil {
		return RefundView{}, err
	}
	if !in.Outcome.Valid() {
		return RefundView{}, errs.Invalid("outcome", errs.ReasonInvalidValue, "That is not a refund outcome.")
	}
	if len(in.ExternalRef) > settlement.MaxPaymentRefLength {
		return RefundView{}, errs.Invalid("externalReference", errs.ReasonInvalidValue,
			fmt.Sprintf("An external reference is at most %d characters.", settlement.MaxPaymentRefLength))
	}
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return RefundView{}, err
	}
	defer func() { _ = tx.Rollback(txCtx) }()

	// The header names the decision whose lock to take; the history is read
	// again under it.
	header, _, err := s.refunds.ByID(txCtx, refundID)
	if err != nil {
		return RefundView{}, err
	}
	// The decision's lock, which admission takes too: a report that fails a
	// refund releases its tax, and must not interleave with an admission
	// counting what is still reserved.
	if err := s.refunds.LockDecision(txCtx, header.Decision); err != nil {
		return RefundView{}, err
	}
	// Re-read under the lock, so the move is decided against the history as
	// it now stands.
	rf, history, err := s.refunds.ByID(txCtx, refundID)
	if err != nil {
		return RefundView{}, err
	}
	current := history[len(history)-1]
	if current.Outcome == in.Outcome && current.ExternalRef == in.ExternalRef && current.Status == settlement.RefundStatusFrom(in.Outcome) {
		return RefundView{Refund: rf, History: history}, nil
	}
	next, err := settlement.Report(current, in.Outcome, in.ExternalRef, s.clock.Now().UTC().Truncate(time.Microsecond), sc.Subject())
	if err != nil {
		return RefundView{}, errs.Wrap(err, errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("The refund is %s; a provider report of %s cannot move it.", current.Status, in.Outcome))
	}
	if err := s.refunds.Append(txCtx, next); err != nil {
		return RefundView{}, err
	}
	if next.Status == settlement.RefundCompleted {
		if err := s.post(txCtx, rf, next); err != nil {
			return RefundView{}, err
		}
	}
	if err := s.emit(txCtx, rf, next, &current); err != nil {
		return RefundView{}, err
	}
	if err := tx.Commit(txCtx); err != nil {
		return RefundView{}, err
	}
	return RefundView{Refund: rf, History: append(history, next)}, nil
}

// post writes the completed refund's journal.
func (s *RefundService) post(ctx context.Context, rf settlement.Refund, completed settlement.RefundEvent) error {
	jid, err := idgen.JournalID(s.ids)
	if err != nil {
		return internal(err, "The refund could not be posted.")
	}
	profile := subledger.PostingProfile{
		Ref: refundPostingRef, TenantID: rf.TenantID, LegalEntity: rf.LegalEntity,
		Approval: "platform:ZTAX-FIN-001 §14",
		Rules:    map[string]subledger.PostingProfileEntry{SourceRefundCompleted: refundPosting},
	}
	decision := rf.Decision
	j, err := profile.Post(subledger.PostingEvent{
		Source:    subledger.SourceEvent{Kind: SourceRefundCompleted, ID: rf.ID.String()},
		EventTime: completed.RecordedAt,
		// The period the money went back in, as a commit's journal names the
		// period its event fell in.
		LegalPeriod: completed.RecordedAt.UTC().Format("2006-01"),
		Currency:    rf.Amount.Currency(),
		Amounts:     map[string]fiscal.Money{"TAX": rf.Amount},
		Decision:    &decision,
	}, jid, completed.RecordedAt)
	if err != nil {
		return internal(err, "The refund's posting does not post.")
	}
	return s.journals.Append(ctx, j)
}

// emit writes the refund's event to the outbox, in the caller's transaction.
func (s *RefundService) emit(ctx context.Context, rf settlement.Refund, e settlement.RefundEvent, previous *settlement.RefundEvent) error {
	eid, err := idgen.OutboxID(s.ids)
	if err != nil {
		return internal(err, "The refund event could not be published.")
	}
	fields := []canonical.Field{
		canonical.F("refundId", canonical.String(rf.ID.String())),
		canonical.F("decisionId", canonical.String(rf.Decision.String())),
		canonical.F("legalEntityId", canonical.String(rf.LegalEntity.String())),
		canonical.F("amount", canonical.String(rf.Amount.CanonicalString())),
		canonical.F("currency", canonical.String(string(rf.Amount.Currency()))),
		canonical.F("seq", canonical.Integer(int64(e.Seq))),
		canonical.F("status", canonical.String(string(e.Status))),
		canonical.F("recordedAt", canonical.Time(e.RecordedAt)),
	}
	typ, schema := EventRefundRequested, SchemaRefundRequestedRef
	if previous != nil {
		typ, schema = EventRefundStatusChanged, SchemaRefundStatusChangedRef
		fields = append(fields,
			canonical.F("previousStatus", canonical.String(string(previous.Status))),
			canonical.F("outcome", canonical.String(string(e.Outcome))))
	}
	return s.outbox.Append(ctx, outbox.Event{
		ID: eid, TenantID: rf.TenantID, AggregateKey: "refund/" + rf.ID.String(),
		Type: typ, SchemaRef: schema, Payload: canonical.Object(fields...), CreatedAt: e.RecordedAt,
	})
}

// Refund reads one refund and its history.
func (s *RefundService) Refund(ctx context.Context, refundID id.RefundID) (RefundView, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return RefundView{}, err
	}
	rf, history, err := s.refunds.ByID(ctx, refundID)
	if err != nil {
		return RefundView{}, err
	}
	return RefundView{Refund: rf, History: history}, nil
}
