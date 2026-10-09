package app

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/idempotency"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/outbox"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Fiscal documents (W2 lane J; ZTAX-FIN-001 §3–§6).
//
// A document presents committed decisions to a customer. It invents no tax:
// every tax line names a pinned TaxDecision, and the tax a document presents
// for each decision and component must equal what that decision decided
// (ZTAX-FIN-REQ-0010, -0011). A document that disagrees is refused with its
// variances, never committed with them (ZTAX-FIN-REQ-0098). Which of a
// decision's emitted amounts are tax is the content's to say — the slots the
// bundle that made the decision posts to the subledger — so a document is
// checked against the same content its decisions were.
//
// Once committed, nothing about a document changes. A void or a credit note
// is a new document cancelling exactly what a predecessor charged; a rebill
// charges again once the original is cancelled; the predecessor gains a
// status event naming the document that moved it (ZTAX-FIN-REQ-0003, -0004).
//
// A decision is billed at most once at a time: a second billing document
// citing a decision whose first billing still stands is refused. Supported
// here: INVOICE, DEBIT_NOTE and ADJUSTMENT, and their VOID, CREDIT_NOTE and
// REBILL. A PARTIAL_CREDIT credits an amount rather than lines and needs the
// partial-credit decision of DET-001 §10.5 to cite; AMENDMENT and
// RESTATEMENT recompute history. Those are refused as UNSUPPORTED rather than
// approximated.

// The endpoints document writes are scoped to (ADR-0013 §2.2).
const (
	DocumentEndpoint   = "POST /v1/documents"
	CorrectionEndpoint = "POST /v1/documents/{documentId}/corrections"
)

// The document event.
const (
	EventDocumentCommitted     = outbox.TypeNamespace + ".document.committed"
	SchemaDocumentCommittedRef = "ztax:events/document-committed/1.0.0"
)

// DocumentService commits fiscal documents and their corrections.
type DocumentService struct {
	det         *DeterminationService
	docs        port.DocumentRepository
	idempotency *Idempotency
}

// NewDocumentService wires the service over the determination service whose
// decisions documents pin, and whose content says which amounts are tax.
func NewDocumentService(det *DeterminationService, docs port.DocumentRepository, g *Idempotency) *DocumentService {
	return &DocumentService{det: det, docs: docs, idempotency: g}
}

// DocumentView is a document as read back, with its status history.
type DocumentView struct {
	Record  port.DocumentRecord
	History []document.StatusEvent
}

// Status is the document's current status.
func (v DocumentView) Status() document.Status { return v.History[len(v.History)-1].Status }

// LineInput is one line a caller presents.
type LineInput struct {
	SourceLineRef     string
	ComponentInstance string
	Net               fiscal.Money
	Discount          *fiscal.Money
	AllocationRef     string
	Taxes             []document.TaxLine
	// Predecessor is the line a rebill line replaces.
	Predecessor *id.FiscalLineID
}

// IssueInput is one document a caller presents.
type IssueInput struct {
	IdempotencyKey string
	Type           document.Type
	Number         string
	IssueDate      time.Time
	TaxPoint       time.Time
	Currency       fiscal.Currency
	External       *id.ExternalReference
	Lines          []LineInput
	Render         func(DocumentView) ([]byte, error)
	RenderFailure  func(error) Response
}

// CorrectionInput is one correction of a committed document.
type CorrectionInput struct {
	IdempotencyKey string
	Original       id.FiscalDocumentID
	Type           document.Type
	Reason         errs.ReasonCode
	Number         string
	IssueDate      time.Time
	TaxPoint       time.Time
	// CancelLines are the lines a credit note cancels; empty is every line
	// not already cancelled. A void cancels them all.
	CancelLines []id.FiscalLineID
	// Lines are a rebill's.
	Lines         []LineInput
	Render        func(DocumentView) ([]byte, error)
	RenderFailure func(error) Response
}

func (s *DocumentService) wired() error {
	if s.det == nil || s.det.fiscal == nil || !s.det.fiscal.complete() || s.docs == nil || s.idempotency == nil {
		return errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable, "Fiscal documents are not wired in this cell.")
	}
	return nil
}

// Issue commits a billing document at most once per idempotency key.
func (s *DocumentService) Issue(ctx context.Context, in IssueInput) (Settled, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleOperator)
	if err != nil {
		return Settled{}, err
	}
	if err := s.wired(); err != nil {
		return Settled{}, err
	}
	switch in.Type {
	case document.TypeInvoice, document.TypeDebitNote, document.TypeAdjustment:
	case document.TypePartialCredit, document.TypeAmendment, document.TypeRestatement:
		return Settled{}, errs.New(errs.CategoryUnsupported, errs.ReasonUnsupportedTransaction,
			fmt.Sprintf("A %s is not supported yet.", in.Type))
	default:
		return Settled{}, errs.Invalid("type", errs.ReasonInvalidValue,
			"A document is issued as INVOICE, DEBIT_NOTE or ADJUSTMENT; corrections are made against the document they correct.")
	}
	digest, err := canonical.Sum(canonical.Object(
		canonical.F("type", canonical.String(string(in.Type))),
		canonical.F("number", canonical.String(in.Number)),
		canonical.F("issueDate", canonical.String(in.IssueDate.Format(time.DateOnly))),
		canonical.F("taxPoint", canonical.Time(in.TaxPoint)),
		canonical.F("currency", canonical.String(string(in.Currency))),
		canonical.F("external", externalCanonical(in.External)),
		canonical.F("lines", linesCanonical(in.Lines)),
	))
	if err != nil {
		return Settled{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, "The document cannot be put in canonical form.")
	}
	return s.idempotency.Do(ctx, Call{
		Key:       idempotency.Key{TenantID: sc.Tenant(), Endpoint: DocumentEndpoint, Value: in.IdempotencyKey},
		Digest:    digest,
		Retention: CommitRetention,
		Execute: func(ctx context.Context) (Response, error) {
			v, err := s.issue(ctx, sc, in)
			if err != nil {
				return Response{}, err
			}
			body, err := in.Render(v)
			if err != nil {
				return Response{}, internal(err, "The document could not be rendered.")
			}
			return Response{Status: 201, Body: body}, nil
		},
		RenderFailure: in.RenderFailure,
	})
}

func (s *DocumentService) issue(ctx context.Context, sc security.Context, in IssueInput) (DocumentView, error) {
	le, err := s.det.defaultLegalEntity(ctx)
	if err != nil {
		return DocumentView{}, err
	}
	docID, err := idgen.FiscalDocumentID(s.det.ids)
	if err != nil {
		return DocumentView{}, internal(err, "The document could not be recorded.")
	}
	lines, decisions, err := s.buildLines(in.Lines)
	if err != nil {
		return DocumentView{}, err
	}
	d := document.Document{
		ID: docID, TenantID: sc.Tenant(), LegalEntity: le.ID, Type: in.Type, Number: in.Number, Root: docID,
		IssueDate: in.IssueDate, TaxPoint: in.TaxPoint, Currency: in.Currency, Decisions: decisions, Lines: lines,
		Status: document.StatusDraft, External: in.External,
	}
	decided, err := s.billable(ctx, d)
	if err != nil {
		return DocumentView{}, err
	}
	return s.commit(ctx, sc, d, decided, nil)
}

// Correct commits a void, credit note or rebill of a committed document at
// most once per idempotency key.
func (s *DocumentService) Correct(ctx context.Context, in CorrectionInput) (Settled, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleOperator)
	if err != nil {
		return Settled{}, err
	}
	if err := s.wired(); err != nil {
		return Settled{}, err
	}
	switch in.Type {
	case document.TypeVoid, document.TypeCreditNote, document.TypeRebill:
	case document.TypePartialCredit, document.TypeAmendment, document.TypeRestatement, document.TypeRefund:
		return Settled{}, errs.New(errs.CategoryUnsupported, errs.ReasonUnsupportedTransaction,
			fmt.Sprintf("A %s is not supported yet.", in.Type))
	default:
		return Settled{}, errs.Invalid("type", errs.ReasonInvalidValue, "A correction is a VOID, CREDIT_NOTE or REBILL.")
	}
	if !document.IsCorrectionReason(in.Reason) {
		return Settled{}, errs.Invalid("reason", errs.ReasonInvalidValue,
			"A correction carries one of the registered CORRECTION_* reasons (ZTAX-FIN-REQ-0008).")
	}
	cancel := make([]canonical.Value, len(in.CancelLines))
	for i, l := range in.CancelLines {
		cancel[i] = canonical.String(l.String())
	}
	digest, err := canonical.Sum(canonical.Object(
		canonical.F("original", canonical.String(in.Original.String())),
		canonical.F("type", canonical.String(string(in.Type))),
		canonical.F("reason", canonical.String(string(in.Reason))),
		canonical.F("number", canonical.String(in.Number)),
		canonical.F("issueDate", canonical.String(in.IssueDate.Format(time.DateOnly))),
		canonical.F("taxPoint", canonical.Time(in.TaxPoint)),
		canonical.F("cancelLines", canonical.Array(cancel...)),
		canonical.F("lines", linesCanonical(in.Lines)),
	))
	if err != nil {
		return Settled{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, "The correction cannot be put in canonical form.")
	}
	return s.idempotency.Do(ctx, Call{
		Key:       idempotency.Key{TenantID: sc.Tenant(), Endpoint: CorrectionEndpoint, Value: in.IdempotencyKey},
		Digest:    digest,
		Retention: CommitRetention,
		Execute: func(ctx context.Context) (Response, error) {
			v, err := s.correct(ctx, sc, in)
			if err != nil {
				return Response{}, err
			}
			body, err := in.Render(v)
			if err != nil {
				return Response{}, internal(err, "The correction could not be rendered.")
			}
			return Response{Status: 201, Body: body}, nil
		},
		RenderFailure: in.RenderFailure,
	})
}

func (s *DocumentService) correct(ctx context.Context, sc security.Context, in CorrectionInput) (DocumentView, error) {
	head, _, err := s.docs.ByID(ctx, in.Original)
	if err != nil {
		return DocumentView{}, err
	}
	if err := s.docs.Lock(ctx, head.Document.Root); err != nil {
		return DocumentView{}, err
	}
	// Read again under the chain's lock: the correction is decided against
	// the original as it now stands.
	orig, history, err := s.docs.ByID(ctx, in.Original)
	if err != nil {
		return DocumentView{}, err
	}
	status := history[len(history)-1].Status
	if !orig.Document.Type.Bills() {
		return DocumentView{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Document %s is a %s; only a billing document is corrected.", in.Original, orig.Document.Type))
	}
	if !document.CanCorrect(status, in.Type) {
		return DocumentView{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Document %s is %s; it cannot take a %s.", in.Original, status, in.Type))
	}
	cancelled, err := s.docs.CancelledLines(ctx, in.Original)
	if err != nil {
		return DocumentView{}, err
	}

	docID, err := idgen.FiscalDocumentID(s.det.ids)
	if err != nil {
		return DocumentView{}, internal(err, "The correction could not be recorded.")
	}
	c := document.Correction{ID: docID, Type: in.Type, Reason: in.Reason, IssueDate: in.IssueDate, TaxPoint: in.TaxPoint, Number: in.Number}
	switch in.Type {
	case document.TypeVoid:
		if len(cancelled) > 0 {
			return DocumentView{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				fmt.Sprintf("Document %s has been partly credited; credit its remaining lines rather than voiding it.", in.Original))
		}
		for range orig.Document.Lines {
			lid, err := idgen.FiscalLineID(s.det.ids)
			if err != nil {
				return DocumentView{}, internal(err, "The correction could not be recorded.")
			}
			c.Lines = append(c.Lines, document.Line{ID: lid})
		}
	case document.TypeCreditNote:
		chosen := in.CancelLines
		if len(chosen) == 0 {
			for _, l := range orig.Document.Lines {
				if !cancelled[l.ID] {
					chosen = append(chosen, l.ID)
				}
			}
		}
		fresh := make([]id.FiscalLineID, len(chosen))
		for i, lid := range chosen {
			if cancelled[lid] {
				return DocumentView{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
					fmt.Sprintf("Line %s has already been cancelled.", lid))
			}
			if fresh[i], err = idgen.FiscalLineID(s.det.ids); err != nil {
				return DocumentView{}, internal(err, "The correction could not be recorded.")
			}
		}
		if c.Lines, err = document.Negate(orig.Document, chosen, fresh); err != nil {
			return DocumentView{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, err.Error())
		}
		c.Decisions = decisionsOf(c.Lines)
	case document.TypeRebill:
		lines, decisions, err := s.buildLines(in.Lines)
		if err != nil {
			return DocumentView{}, err
		}
		c.Lines, c.Decisions = lines, decisions
	}

	original := orig.Document
	original.Status = status
	d, err := document.Correct(original, c)
	if err != nil {
		return DocumentView{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, err.Error())
	}
	var decided []document.DecisionTax
	if in.Type == document.TypeRebill {
		if decided, err = s.billable(ctx, d); err != nil {
			return DocumentView{}, err
		}
	} else {
		decided = document.Cancelled(orig.Document, d.Lines)
	}

	var move *document.StatusEvent
	if next, moves := document.After(in.Type, fullyCancelled(orig.Document, cancelled, d.Lines)); moves {
		cause := docID
		move = &document.StatusEvent{
			Document: in.Original, Seq: len(history) + 1, Status: next, Cause: &cause,
			RecordedAt: s.det.clock.Now().UTC().Truncate(time.Microsecond), RecordedBy: sc.Subject(),
		}
	}
	return s.commit(ctx, sc, d, decided, move)
}

// commit checks a draft against what its decisions decided, records it, moves
// its predecessor, and announces it.
func (s *DocumentService) commit(ctx context.Context, sc security.Context, d document.Document,
	decided []document.DecisionTax, move *document.StatusEvent) (DocumentView, error) {
	zero, err := fiscal.ParseMoney("0", d.Currency)
	if err != nil {
		return DocumentView{}, errs.Invalid("currency", errs.ReasonInvalidValue, "That is not a currency.")
	}
	// No rounding adjustment is admitted until content supplies a bound for
	// one (ZTAX-FIN-REQ-0022); lines carry none here.
	committed, variances, err := document.Commit(d, decided, zero)
	if len(variances) > 0 {
		parts := make([]string, len(variances))
		for i, v := range variances {
			// Which, never how much: a Problem's detail reaches logs, and the
			// amounts are the customer's.
			parts[i] = fmt.Sprintf("%s/%s", v.Decision, v.Component)
		}
		return DocumentView{}, errs.New(errs.CategoryConflict, errs.ReasonConflicted,
			"The document presents tax that differs from its pinned decisions at "+strings.Join(parts, ", ")+
				". A document presents what was decided; correct the decision, not the document (ZTAX-FIN-REQ-0011).")
	}
	if err != nil {
		return DocumentView{}, err
	}
	net, tax, gross, err := document.Totals(committed)
	if err != nil {
		return DocumentView{}, internal(err, "The document's totals could not be computed.")
	}
	now := s.det.clock.Now().UTC().Truncate(time.Microsecond)
	rec := port.DocumentRecord{Document: committed, Net: net, Tax: tax, Gross: gross, RecordedAt: now, RecordedBy: sc.Subject()}
	first := document.StatusEvent{Document: committed.ID, Seq: 1, Status: document.StatusCommitted, RecordedAt: now, RecordedBy: sc.Subject()}
	if err := s.docs.Create(ctx, rec, first); err != nil {
		return DocumentView{}, err
	}
	if move != nil {
		if err := s.docs.AppendStatus(ctx, *move); err != nil {
			return DocumentView{}, err
		}
	}
	if err := s.emit(ctx, rec); err != nil {
		return DocumentView{}, err
	}
	return DocumentView{Record: rec, History: []document.StatusEvent{first}}, nil
}

// buildLines turns presented lines into document lines with fresh ids, and
// lists the decisions their taxes pin.
func (s *DocumentService) buildLines(in []LineInput) ([]document.Line, []id.DecisionID, error) {
	if len(in) == 0 {
		return nil, nil, errs.Invalid("lines", errs.ReasonMissingField, "A document has at least one line.")
	}
	lines := make([]document.Line, len(in))
	for i, l := range in {
		lid, err := idgen.FiscalLineID(s.det.ids)
		if err != nil {
			return nil, nil, internal(err, "The document could not be recorded.")
		}
		lines[i] = document.Line{
			ID: lid, SourceLineRef: l.SourceLineRef, ComponentInstance: l.ComponentInstance, Net: l.Net,
			Discount: l.Discount, AllocationRef: l.AllocationRef, Taxes: l.Taxes, Predecessor: l.Predecessor,
		}
	}
	return lines, decisionsOf(lines), nil
}

func decisionsOf(lines []document.Line) []id.DecisionID {
	var out []id.DecisionID
	for _, l := range lines {
		for _, t := range l.Taxes {
			if !slices.Contains(out, t.Decision) {
				out = append(out, t.Decision)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// billable is what a billing document's pinned decisions decided, after
// checking each one may be billed: it is the current version of its
// business key, and no billing document still standing already bills it. A
// rebill passes the second check for its original's decisions because the
// original is voided or fully credited before it may be rebilled.
func (s *DocumentService) billable(ctx context.Context, d document.Document) ([]document.DecisionTax, error) {
	presented := map[string]bool{}
	for _, l := range d.Lines {
		for _, t := range l.Taxes {
			presented[t.Decision.String()+"/"+t.Component] = true
		}
	}
	var out []document.DecisionTax
	for _, decisionID := range d.Decisions {
		if err := s.docs.LockDecision(ctx, decisionID); err != nil {
			return nil, err
		}
		citations, err := s.docs.CitedBy(ctx, decisionID)
		if err != nil {
			return nil, err
		}
		for _, c := range citations {
			if c.Type.Bills() && !c.Status.Releases() {
				return nil, errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists,
					fmt.Sprintf("Decision %s is already billed by document %s, which still stands. Void or credit it first.", decisionID, c.Document))
			}
		}
		components, err := s.decidedTax(ctx, decisionID, d.Currency)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(components))
		for name := range components {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			amount := components[name]
			// A component the decision decided as zero need not be presented;
			// one presented is checked like any other.
			if amount.IsZero() && !presented[decisionID.String()+"/"+name] {
				continue
			}
			out = append(out, document.DecisionTax{Decision: decisionID, Component: name, Amount: amount})
		}
	}
	return out, nil
}

// decidedTax is one decision's tax by component: the emitted amounts its
// bundle posts to the subledger, which must be in cur — or, with cur empty,
// in whatever currency the decision decided them. The decision must be the current version
// of its business key — a superseded decision's posting has been reversed,
// and billing it would present tax the ledger no longer holds.
func (s *DocumentService) decidedTax(ctx context.Context, decisionID id.DecisionID, cur fiscal.Currency) (map[string]fiscal.Money, error) {
	d, err := s.det.Decision(ctx, decisionID)
	if err != nil {
		return nil, err
	}
	history, err := s.det.decisions.History(ctx, d.Record.BusinessKey)
	if err != nil {
		return nil, err
	}
	for _, h := range history {
		if h.Supersedes != nil && *h.Supersedes == decisionID {
			return nil, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				fmt.Sprintf("Decision %s has been superseded by %s; bill the current version.", decisionID, h.DecisionID))
		}
	}
	b, ok := s.det.library.ByDigest(d.Record.BundleDigest)
	if !ok {
		return nil, errs.New(errs.CategoryUnavailable, errs.ReasonNoContentBundle,
			"The content bundle that made the decision is not loaded in this cell, so which of its amounts are tax cannot be read. The request was not applied.")
	}
	p := b.Fiscal()
	if p == nil || p.Posting == nil {
		return nil, errs.Invalid("lines.taxes.decision", errs.ReasonInvalidValue,
			fmt.Sprintf("The content that made decision %s declares no tax components to present.", decisionID))
	}
	out := map[string]fiscal.Money{}
	for _, l := range p.Posting.Lines {
		v, ok := d.Result.Emitted[l.Emitted]
		if !ok || v.Type != rule.TypeMoney {
			continue
		}
		if cur != "" && v.Money.Currency() != cur {
			return nil, errs.Invalid("currency", errs.ReasonCurrencyMismatch,
				fmt.Sprintf("Decision %s decided %s in %s; the document is in %s.", decisionID, l.Emitted, v.Money.Currency(), cur))
		}
		out[l.Emitted] = v.Money
	}
	return out, nil
}

// fullyCancelled reports whether, with cancelling, every line of original
// has been cancelled.
func fullyCancelled(original document.Document, before map[id.FiscalLineID]bool, cancelling []document.Line) bool {
	now := map[id.FiscalLineID]bool{}
	for k := range before {
		now[k] = true
	}
	for _, l := range cancelling {
		if l.Predecessor != nil {
			now[*l.Predecessor] = true
		}
	}
	for _, l := range original.Lines {
		if !now[l.ID] {
			return false
		}
	}
	return true
}

// emit announces a committed document, thinly, as decisions are announced.
func (s *DocumentService) emit(ctx context.Context, rec port.DocumentRecord) error {
	d := rec.Document
	eid, err := idgen.OutboxID(s.det.ids)
	if err != nil {
		return internal(err, "The document event could not be published.")
	}
	preds := make([]canonical.Value, len(d.Predecessors))
	for i, p := range d.Predecessors {
		preds[i] = canonical.String(p.String())
	}
	decisions := make([]canonical.Value, len(d.Decisions))
	for i, dec := range d.Decisions {
		decisions[i] = canonical.String(dec.String())
	}
	fields := []canonical.Field{
		canonical.F("documentId", canonical.String(d.ID.String())),
		canonical.F("type", canonical.String(string(d.Type))),
		canonical.F("rootId", canonical.String(d.Root.String())),
		canonical.F("predecessors", canonical.Array(preds...)),
		canonical.F("legalEntityId", canonical.String(d.LegalEntity.String())),
		canonical.F("issueDate", canonical.String(d.IssueDate.Format(time.DateOnly))),
		canonical.F("taxPoint", canonical.Time(d.TaxPoint)),
		canonical.F("currency", canonical.String(string(d.Currency))),
		canonical.F("decisionIds", canonical.Array(decisions...)),
		canonical.F("recordedAt", canonical.Time(rec.RecordedAt)),
	}
	if d.Reason != "" {
		fields = append(fields, canonical.F("reasonCode", canonical.String(string(d.Reason))))
	}
	return s.det.fiscal.Outbox.Append(ctx, outbox.Event{
		ID: eid, TenantID: d.TenantID, AggregateKey: "document/" + d.Root.String(),
		Type: EventDocumentCommitted, SchemaRef: SchemaDocumentCommittedRef,
		Payload: canonical.Object(fields...), CreatedAt: rec.RecordedAt,
	})
}

// Document reads one document and its status history.
func (s *DocumentService) Document(ctx context.Context, documentID id.FiscalDocumentID) (DocumentView, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return DocumentView{}, err
	}
	if err := s.wired(); err != nil {
		return DocumentView{}, err
	}
	rec, history, err := s.docs.ByID(ctx, documentID)
	if err != nil {
		return DocumentView{}, err
	}
	return DocumentView{Record: rec, History: history}, nil
}

// Lineage reads every document in a document's chain, original first.
func (s *DocumentService) Lineage(ctx context.Context, documentID id.FiscalDocumentID) ([]DocumentView, error) {
	v, err := s.Document(ctx, documentID)
	if err != nil {
		return nil, err
	}
	recs, err := s.docs.Lineage(ctx, v.Record.Document.Root)
	if err != nil {
		return nil, err
	}
	out := make([]DocumentView, len(recs))
	for i, r := range recs {
		_, history, err := s.docs.ByID(ctx, r.Document.ID)
		if err != nil {
			return nil, err
		}
		out[i] = DocumentView{Record: r, History: history}
	}
	return out, nil
}

func externalCanonical(e *id.ExternalReference) canonical.Value {
	if e == nil {
		return canonical.Absent()
	}
	return canonical.Object(
		canonical.F("sourceSystem", canonical.String(e.SourceSystem)),
		canonical.F("namespace", canonical.String(e.Namespace)),
		canonical.F("value", canonical.String(e.Value)),
	)
}

func linesCanonical(lines []LineInput) canonical.Value {
	vs := make([]canonical.Value, len(lines))
	for i, l := range lines {
		taxes := make([]canonical.Value, len(l.Taxes))
		for j, t := range l.Taxes {
			taxes[j] = canonical.Object(
				canonical.F("decisionId", canonical.String(t.Decision.String())),
				canonical.F("component", canonical.String(t.Component)),
				canonical.F("amount", canonical.Money(t.Amount)),
				canonical.F("currency", canonical.String(string(t.Amount.Currency()))),
			)
		}
		discount, predecessor := canonical.Absent(), canonical.Absent()
		if l.Discount != nil {
			discount = canonical.Money(*l.Discount)
		}
		if l.Predecessor != nil {
			predecessor = canonical.String(l.Predecessor.String())
		}
		vs[i] = canonical.Object(
			canonical.F("sourceLineRef", canonical.String(l.SourceLineRef)),
			canonical.F("componentInstance", canonical.String(l.ComponentInstance)),
			canonical.F("net", canonical.Money(l.Net)),
			canonical.F("discount", discount),
			canonical.F("allocationRef", canonical.String(l.AllocationRef)),
			canonical.F("predecessorLineId", predecessor),
			canonical.F("taxes", canonical.Array(taxes...)),
		)
	}
	return canonical.Array(vs...)
}
