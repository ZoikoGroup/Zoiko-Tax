// Package support models the customer support triage queue (Engineering
// Doc 18, W16, G-052).
//
// The outcome it protects is narrow: requests are routed and prioritised
// without giving support any tax-rule or financial authority. Three things
// follow from that and are kept apart here:
//
//   - The case is not a fiscal record. A case may name a decision, a
//     document, an obligation or a remittance, but only as a read-only
//     reference: nothing here issues a refund, files a return, pays, or edits
//     a rule (SUP-03, SUP-04, SUP-11). There is no code path from a case to a
//     fiscal write.
//   - Two clocks, never one. The support SLA is an internal promise derived
//     from severity; a statutory deadline is the law's. They are kept as
//     separate facts, and when the statutory one falls earlier it takes
//     precedence and is flagged (SUP-06). Neither is reset by reopening or
//     by editing the case (SUP-07).
//   - One audited history. A case is a header written once and an
//     append-only list of events; its current classification, assignee,
//     evidence and status are the fold of those events. A revision is an
//     event, never an edit.
//
// The state machine is NEW → VERIFIED → TRIAGED → IN_REVIEW → RESPONDED →
// RESOLVED → CLOSED, with TRIAGE_INCOMPLETE for a case that lacks its
// contract, period or seller facts (SUP-02), and a linked reopening from
// RESOLVED or CLOSED back to IN_REVIEW (SUP-07). Closure needs a recorded
// customer response (SUP-14) and, for LEGAL and TAX_SPECIALIST cases, a
// sign-off by someone other than the person who opened the case (T18-18).
package support

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/compliance"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// ReasonSignOffRequired is a LEGAL or TAX_SPECIALIST case asked to close
// without an independent sign-off (T18-18).
var ReasonSignOffRequired = errs.Register("SIGN_OFF_REQUIRED", errs.CategoryConflict,
	"support",
	"This case closes only after a sign-off by someone other than the person who opened it.")

// ReasonSegregationOfDuties is the four-eye refusal shared with compliance
// packages: the opener of a case does not sign it off.
var ReasonSegregationOfDuties = compliance.ReasonSegregationOfDuties

// ---------------------------------------------------------------------------
// vocabularies
// ---------------------------------------------------------------------------

// Type is what a case is about. It routes the case; it grants nothing.
type Type string

// The case types (Doc 18 §W16).
const (
	TypeCoverageEntitlement Type = "COVERAGE_ENTITLEMENT"
	TypeInvoiceExplanation  Type = "INVOICE_EXPLANATION"
	TypeTaxSpecialist       Type = "TAX_SPECIALIST"
	TypeFiling              Type = "FILING"
	TypeRemittance          Type = "REMITTANCE"
	TypeSecurity            Type = "SECURITY"
	TypeLegal               Type = "LEGAL"
)

// Types are every case type, in the order the contract lists them.
var Types = []Type{TypeCoverageEntitlement, TypeInvoiceExplanation, TypeTaxSpecialist, TypeFiling,
	TypeRemittance, TypeSecurity, TypeLegal}

// Valid reports whether the type is one of the seven.
func (t Type) Valid() bool { return slices.Contains(Types, t) }

// NeedsSignOff reports whether a case of this type closes only after an
// independent sign-off (T18-18).
func (t Type) NeedsSignOff() bool { return t == TypeLegal || t == TypeTaxSpecialist }

// Severity is how badly the customer is affected. It sets the support SLA.
type Severity string

// The severities, S1 the most severe.
const (
	SeverityS1 Severity = "S1"
	SeverityS2 Severity = "S2"
	SeverityS3 Severity = "S3"
	SeverityS4 Severity = "S4"
)

// Valid reports whether the severity is S1 to S4.
func (s Severity) Valid() bool {
	switch s {
	case SeverityS1, SeverityS2, SeverityS3, SeverityS4:
		return true
	}
	return false
}

// SLATarget is the internal time to first customer response for a severity:
// S1 four hours, S2 one day, S3 three days, S4 seven days, all in elapsed
// time from opening. It is a support promise, not a legal deadline, and is
// never shown as one.
func (s Severity) SLATarget() time.Duration {
	switch s {
	case SeverityS1:
		return 4 * time.Hour
	case SeverityS2:
		return 24 * time.Hour
	case SeverityS3:
		return 72 * time.Hour
	default:
		return 7 * 24 * time.Hour
	}
}

// Status is where a case stands.
type Status string

// The statuses.
const (
	StatusNew              Status = "NEW"
	StatusVerified         Status = "VERIFIED"
	StatusTriageIncomplete Status = "TRIAGE_INCOMPLETE"
	StatusTriaged          Status = "TRIAGED"
	StatusInReview         Status = "IN_REVIEW"
	StatusResponded        Status = "RESPONDED"
	StatusResolved         Status = "RESOLVED"
	StatusClosed           Status = "CLOSED"
)

// Valid reports whether the status is one of the eight.
func (s Status) Valid() bool { _, ok := transitions[s]; return ok }

// transitions are the moves a status may make. A move to the same status is
// an act that changes no status (an assignment, a note) and is not listed.
var transitions = map[Status][]Status{
	StatusNew:              {StatusVerified},
	StatusVerified:         {StatusTriaged, StatusTriageIncomplete},
	StatusTriageIncomplete: {StatusTriaged},
	StatusTriaged:          {StatusInReview},
	StatusInReview:         {StatusResponded},
	StatusResponded:        {StatusInReview, StatusResolved},
	StatusResolved:         {StatusClosed, StatusInReview},
	StatusClosed:           {StatusInReview},
}

// CanTransition reports whether a case may move between two statuses.
func CanTransition(from, to Status) bool {
	return from == to || slices.Contains(transitions[from], to)
}

// Action is one thing a person does to a case. Every action is an event.
type Action string

// The actions. OPENED is only ever the first event.
const (
	ActionOpened           Action = "OPENED"
	ActionVerify           Action = "VERIFY"
	ActionTriage           Action = "TRIAGE"
	ActionAssign           Action = "ASSIGN"
	ActionRequestEvidence  Action = "REQUEST_EVIDENCE"
	ActionAddEvidence      Action = "ADD_EVIDENCE"
	ActionStartReview      Action = "START_REVIEW"
	ActionEscalate         Action = "ESCALATE"
	ActionRespond          Action = "RESPOND"
	ActionResolve          Action = "RESOLVE"
	ActionSignOff          Action = "SIGN_OFF"
	ActionClose            Action = "CLOSE"
	ActionReopen           Action = "REOPEN"
	ActionNote             Action = "NOTE"
	ActionRequestLegalHold Action = "REQUEST_LEGAL_HOLD"
)

// from is the statuses each action may be taken in. An action absent from
// a status's list is refused there, whoever asks.
var from = map[Action][]Status{
	ActionVerify:           {StatusNew},
	ActionTriage:           {StatusVerified, StatusTriageIncomplete},
	ActionStartReview:      {StatusTriaged, StatusResponded},
	ActionEscalate:         {StatusTriaged, StatusInReview, StatusResponded},
	ActionRespond:          {StatusInReview},
	ActionResolve:          {StatusResponded},
	ActionSignOff:          {StatusInReview, StatusResponded, StatusResolved},
	ActionClose:            {StatusResolved},
	ActionReopen:           {StatusResolved, StatusClosed},
	ActionAssign:           openStatuses,
	ActionRequestEvidence:  openStatuses,
	ActionAddEvidence:      openStatuses,
	ActionRequestLegalHold: openStatuses,
	ActionNote:             append(slices.Clone(openStatuses), StatusClosed),
}

var openStatuses = []Status{StatusNew, StatusVerified, StatusTriageIncomplete, StatusTriaged, StatusInReview,
	StatusResponded, StatusResolved}

// Actions are the actions a person may take, in the contract's order.
var Actions = []Action{ActionVerify, ActionTriage, ActionAssign, ActionRequestEvidence, ActionAddEvidence,
	ActionStartReview, ActionEscalate, ActionRespond, ActionResolve, ActionSignOff, ActionClose, ActionReopen,
	ActionNote, ActionRequestLegalHold}

// Valid reports whether the action is one a person may take.
func (a Action) Valid() bool { return slices.Contains(Actions, a) }

// leadsTo reports whether an action taken in one status ends in another.
func (a Action) leadsTo(from, to Status) bool {
	switch a {
	case ActionVerify:
		return to == StatusVerified
	case ActionTriage:
		return to == StatusTriaged || to == StatusTriageIncomplete
	case ActionStartReview, ActionEscalate, ActionReopen:
		return to == StatusInReview
	case ActionRespond:
		return to == StatusResponded
	case ActionResolve:
		return to == StatusResolved
	case ActionClose:
		return to == StatusClosed
	}
	return to == from
}

// AllowedIn reports whether the action may be taken in a status.
func (a Action) AllowedIn(s Status) bool { return slices.Contains(from[a], s) }

// EscalationTarget is who an escalated case is routed to. Routing it there
// hands it to people with authority; it gives support none.
type EscalationTarget string

// The escalation targets.
const (
	EscalateTaxSpecialist     EscalationTarget = "TAX_SPECIALIST"
	EscalateLegal             EscalationTarget = "LEGAL"
	EscalateSecurity          EscalationTarget = "SECURITY"
	EscalateFinanceOperations EscalationTarget = "FINANCE_OPERATIONS"
	EscalateEngineering       EscalationTarget = "ENGINEERING"
)

// Valid reports whether the target is one of the five.
func (e EscalationTarget) Valid() bool {
	switch e {
	case EscalateTaxSpecialist, EscalateLegal, EscalateSecurity, EscalateFinanceOperations, EscalateEngineering:
		return true
	}
	return false
}

// Fact is a fact triage needs before a case can be worked (SUP-02).
type Fact string

// The facts triage needs.
const (
	FactContract     Fact = "CONTRACT"
	FactFiscalPeriod Fact = "FISCAL_PERIOD"
	FactSeller       Fact = "SELLER"
)

// ---------------------------------------------------------------------------
// the case
// ---------------------------------------------------------------------------

// References are what a case is about, by identifier only. Support reads
// them; nothing here writes them (SUP-03, SUP-04, SUP-11).
type References struct {
	Decision   id.DecisionID
	Document   id.FiscalDocumentID
	Obligation id.ObligationID
	Remittance id.RemittanceID
}

// Case is a support case, folded from its events.
type Case struct {
	ID       id.SupportCaseID
	TenantID id.TenantID

	Type     Type
	Severity Severity
	// OpenedType is the type the case was opened as. A reclassification
	// changes Type, never this.
	OpenedType  Type
	Subject     string
	Description string
	// Product is the product line the request concerns, free text.
	Product string
	// LegalEntity is the customer legal entity, when known.
	LegalEntity id.LegalEntityID
	// Period is the impacted fiscal period, YYYY-MM, when known.
	Period      string
	ContractRef string
	SellerRef   string
	Refs        References

	// StatutoryDueAt is the law's deadline the case is exposed to, zero if
	// none. Recorded once; support never moves it.
	StatutoryDueAt time.Time
	// SLADueAt is the support SLA's due time: opening plus the severity's
	// target. A raised severity can only bring it earlier, measured from
	// opening; nothing moves it later or restarts it.
	SLADueAt time.Time

	Assignee id.UserID
	// Evidence are references to evidence — hashes or document ids — never
	// the evidence itself.
	Evidence []string

	OpenedAt time.Time
	OpenedBy id.UserID

	Status Status
	// EscalatedTo is the latest escalation's target, empty if never
	// escalated.
	EscalatedTo EscalationTarget
	// MissingFacts are what the latest incomplete triage asked for.
	MissingFacts []Fact
	History      []Event
}

// Event is one act on a case. Each carries only what it changed.
type Event struct {
	Seq    int
	Action Action
	// Status is the case's status after the event.
	Status Status
	Note   string

	// Triage's classification, each only when supplied.
	Type           Type
	Severity       Severity
	LegalEntity    id.LegalEntityID
	Period         string
	ContractRef    string
	SellerRef      string
	StatutoryDueAt time.Time
	// SLADueAt is set when a raised severity brought the SLA due time
	// earlier.
	SLADueAt time.Time

	Assignee     id.UserID
	EscalateTo   EscalationTarget
	Evidence     []string
	MissingFacts []Fact
	// Reopens is the sequence number of the RESOLVE or CLOSE event a
	// reopening reverses: the link SUP-07 asks for.
	Reopens int

	RecordedAt time.Time
	RecordedBy id.UserID
}

// Limits on what a case holds.
const (
	MaxSubject     = 200
	MaxDescription = 5000
	MaxNote        = 2000
	MaxRef         = 200
	MaxEvidence    = 50
)

var periodShape = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

// ValidPeriod reports whether s is a fiscal period, YYYY-MM.
func ValidPeriod(s string) bool { return periodShape.MatchString(s) }

func tooLong(s string, n int) bool { return len([]rune(s)) > n }

// Validate refuses a case that cannot be opened.
func (c Case) Validate() error {
	switch {
	case !c.Type.Valid():
		return errs.Invalid("type", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a support case type.", c.Type))
	case !c.Severity.Valid():
		return errs.Invalid("severity", errs.ReasonInvalidValue, "Severity is S1, S2, S3 or S4.")
	case strings.TrimSpace(c.Subject) == "":
		return errs.Invalid("subject", errs.ReasonMissingField, "A case has a subject.")
	case tooLong(c.Subject, MaxSubject):
		return errs.Invalid("subject", errs.ReasonInvalidValue, fmt.Sprintf("A subject is at most %d characters.", MaxSubject))
	case strings.TrimSpace(c.Description) == "":
		return errs.Invalid("description", errs.ReasonMissingField, "A case describes the request.")
	case tooLong(c.Description, MaxDescription):
		return errs.Invalid("description", errs.ReasonInvalidValue, fmt.Sprintf("A description is at most %d characters.", MaxDescription))
	case tooLong(c.Product, MaxRef):
		return errs.Invalid("product", errs.ReasonInvalidValue, fmt.Sprintf("A product is at most %d characters.", MaxRef))
	case c.OpenedAt.IsZero() || c.OpenedBy.IsZero():
		return errs.New(errs.CategoryPolicy, errs.ReasonForbidden, "A case is opened by a person, at a time.")
	case c.SLADueAt.IsZero():
		return errs.New(errs.CategoryInternal, errs.ReasonInternal, "A case is opened with its support SLA due time.")
	}
	return validateFacts(c.Period, c.ContractRef, c.SellerRef, c.Evidence)
}

func validateFacts(period, contract, seller string, evidence []string) error {
	switch {
	case period != "" && !ValidPeriod(period):
		return errs.Invalid("fiscalPeriod", errs.ReasonInvalidValue, "A fiscal period is a month, YYYY-MM.")
	case tooLong(contract, MaxRef):
		return errs.Invalid("contractRef", errs.ReasonInvalidValue, fmt.Sprintf("A contract reference is at most %d characters.", MaxRef))
	case tooLong(seller, MaxRef):
		return errs.Invalid("sellerRef", errs.ReasonInvalidValue, fmt.Sprintf("A seller reference is at most %d characters.", MaxRef))
	case len(evidence) > MaxEvidence:
		return errs.Invalid("evidence", errs.ReasonInvalidValue, fmt.Sprintf("A case holds at most %d evidence references.", MaxEvidence))
	}
	for _, e := range evidence {
		if strings.TrimSpace(e) == "" || tooLong(e, MaxRef) {
			return errs.Invalid("evidence", errs.ReasonInvalidValue,
				fmt.Sprintf("An evidence reference is a hash or identifier of 1 to %d characters.", MaxRef))
		}
	}
	return nil
}

// CleanEvidence trims and de-duplicates evidence references, keeping their
// order.
func CleanEvidence(in []string) []string {
	out := make([]string, 0, len(in))
	for _, e := range in {
		e = strings.TrimSpace(e)
		if e != "" && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// Missing are the facts triage still needs: the contract, the impacted
// period, and the seller — a customer legal entity or a seller reference
// (SUP-02).
func (c Case) Missing() []Fact {
	var out []Fact
	if strings.TrimSpace(c.ContractRef) == "" {
		out = append(out, FactContract)
	}
	if c.Period == "" {
		out = append(out, FactFiscalPeriod)
	}
	if c.LegalEntity.IsZero() && strings.TrimSpace(c.SellerRef) == "" {
		out = append(out, FactSeller)
	}
	return out
}

// Open is the first event of a case.
func (c Case) Open() Event {
	return Event{Seq: 1, Action: ActionOpened, Status: StatusNew, RecordedAt: c.OpenedAt.UTC(), RecordedBy: c.OpenedBy}
}

// Fold applies a case's events in order. It refuses a history no correct
// writer produces.
func Fold(c Case, events []Event) (Case, error) {
	if len(events) == 0 || events[0].Seq != 1 || events[0].Action != ActionOpened || events[0].Status != StatusNew {
		return Case{}, fmt.Errorf("support: a case's history starts with its OPENED event")
	}
	c.Status = StatusNew
	if c.OpenedType == "" {
		c.OpenedType = c.Type
	}
	c.History = events[:1]
	for i, e := range events[1:] {
		if e.Seq != i+2 || e.Action == ActionOpened {
			return Case{}, fmt.Errorf("support: event %d is out of sequence", e.Seq)
		}
		if !e.Action.AllowedIn(c.Status) || !CanTransition(c.Status, e.Status) || !e.Action.leadsTo(c.Status, e.Status) {
			return Case{}, fmt.Errorf("support: event %d takes %s on a %s case to %s", e.Seq, e.Action, c.Status, e.Status)
		}
		if e.Action == ActionClose {
			if err := c.closable(); err != nil {
				return Case{}, fmt.Errorf("support: event %d closes a case that could not close: %w", e.Seq, err)
			}
		}
		c = c.apply(e)
		c.History = events[:i+2]
	}
	return c, nil
}

// apply folds one event into the case.
func (c Case) apply(e Event) Case {
	c.Status = e.Status
	if e.Type != "" {
		c.Type = e.Type
	}
	if e.Severity != "" {
		c.Severity = e.Severity
	}
	if !e.LegalEntity.IsZero() {
		c.LegalEntity = e.LegalEntity
	}
	if e.Period != "" {
		c.Period = e.Period
	}
	if e.ContractRef != "" {
		c.ContractRef = e.ContractRef
	}
	if e.SellerRef != "" {
		c.SellerRef = e.SellerRef
	}
	if c.StatutoryDueAt.IsZero() && !e.StatutoryDueAt.IsZero() {
		c.StatutoryDueAt = e.StatutoryDueAt
	}
	if !e.SLADueAt.IsZero() && e.SLADueAt.Before(c.SLADueAt) {
		c.SLADueAt = e.SLADueAt
	}
	if !e.Assignee.IsZero() {
		c.Assignee = e.Assignee
	}
	if e.EscalateTo != "" {
		c.EscalatedTo = e.EscalateTo
	}
	if len(e.Evidence) > 0 {
		c.Evidence = CleanEvidence(append(slices.Clone(c.Evidence), e.Evidence...))
	}
	if e.Action == ActionTriage {
		c.MissingFacts = slices.Clone(e.MissingFacts)
	}
	return c
}

// sinceReopen is the history after the latest reopening: what counts toward
// the current closure.
func (c Case) sinceReopen() []Event {
	for i := len(c.History) - 1; i >= 0; i-- {
		if c.History[i].Action == ActionReopen {
			return c.History[i+1:]
		}
	}
	return c.History
}

// FirstResponse is the first customer response ever recorded, nil if none.
// It is what the support SLA is measured against, and a reopening does not
// unset it (SUP-07).
func (c Case) FirstResponse() *Event {
	for i := range c.History {
		if c.History[i].Action == ActionRespond {
			return &c.History[i]
		}
	}
	return nil
}

// Responded reports whether a customer response has been recorded since the
// latest reopening (SUP-14).
func (c Case) Responded() bool {
	return slices.ContainsFunc(c.sinceReopen(), func(e Event) bool { return e.Action == ActionRespond })
}

// NeedsSignOff reports whether the case closes only after an independent
// sign-off: it is, or was ever classified as, LEGAL or TAX_SPECIALIST. A
// reclassification does not shed the requirement (T18-18).
func (c Case) NeedsSignOff() bool {
	if c.Type.NeedsSignOff() || c.OpenedType.NeedsSignOff() {
		return true
	}
	return slices.ContainsFunc(c.History, func(e Event) bool { return e.Type.NeedsSignOff() })
}

// SignOff is the latest sign-off since the latest reopening by someone other
// than the opener, nil if none.
func (c Case) SignOff() *Event {
	since := c.sinceReopen()
	for i := len(since) - 1; i >= 0; i-- {
		if since[i].Action == ActionSignOff && since[i].RecordedBy != c.OpenedBy {
			return &since[i]
		}
	}
	return nil
}

// closable is the closure guard: a response was recorded (SUP-14) and, where
// the type needs one, an independent sign-off was too (T18-18).
func (c Case) closable() error {
	if !c.Responded() {
		return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"A case closes only after a response to the customer is recorded (SUP-14).")
	}
	if c.NeedsSignOff() && c.SignOff() == nil {
		return errs.New(errs.CategoryConflict, ReasonSignOffRequired,
			"A LEGAL or TAX_SPECIALIST case closes only after a sign-off by someone other than the person who opened it (T18-18).")
	}
	return nil
}

// Closable reports whether the case may close now, and why not.
func (c Case) Closable() error {
	if c.Status != StatusResolved {
		return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only a resolved case is closed; this one is %s.", c.Status))
	}
	return c.closable()
}

// ---------------------------------------------------------------------------
// acting on a case
// ---------------------------------------------------------------------------

// Act is what a person asks to do to a case.
type Act struct {
	Action Action
	Note   string

	// Triage's classification; only what is supplied changes.
	Type           Type
	Severity       Severity
	LegalEntity    id.LegalEntityID
	Period         string
	ContractRef    string
	SellerRef      string
	StatutoryDueAt time.Time

	Assignee   id.UserID
	EscalateTo EscalationTarget
	Evidence   []string
}

// Decide checks an act against the case and returns the event that records
// it. It never changes anything outside the case.
func (c Case) Decide(a Act, by id.UserID, at time.Time) (Event, error) {
	if !a.Action.Valid() {
		return Event{}, errs.Invalid("action", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a support action.", a.Action))
	}
	if by.IsZero() {
		return Event{}, errs.New(errs.CategoryPolicy, errs.ReasonForbidden, "Every act on a case is recorded against a person.")
	}
	if !a.Action.AllowedIn(c.Status) {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("%s is not taken on a %s case.", a.Action, c.Status))
	}
	note := strings.TrimSpace(a.Note)
	if tooLong(note, MaxNote) {
		return Event{}, errs.Invalid("note", errs.ReasonInvalidValue, fmt.Sprintf("A note is at most %d characters.", MaxNote))
	}
	e := Event{Seq: len(c.History) + 1, Action: a.Action, Status: c.Status, Note: note, RecordedAt: at.UTC(), RecordedBy: by}
	needNote := func(why string) error {
		if note == "" {
			return errs.Invalid("note", errs.ReasonMissingField, why)
		}
		return nil
	}

	switch a.Action {
	case ActionVerify:
		e.Status = StatusVerified
	case ActionTriage:
		return c.triage(a, e)
	case ActionAssign:
		if a.Assignee.IsZero() {
			return Event{}, errs.Invalid("assignee", errs.ReasonMissingField, "An assignment names the person who will own the case.")
		}
		e.Assignee = a.Assignee
	case ActionRequestEvidence:
		if err := needNote("An evidence request says what is needed from the customer."); err != nil {
			return Event{}, err
		}
	case ActionAddEvidence:
		ev := CleanEvidence(a.Evidence)
		if len(ev) == 0 {
			return Event{}, errs.Invalid("evidence", errs.ReasonMissingField, "Adding evidence names at least one evidence reference.")
		}
		if err := validateFacts("", "", "", append(slices.Clone(c.Evidence), ev...)); err != nil {
			return Event{}, err
		}
		e.Evidence = ev
	case ActionStartReview:
		e.Status = StatusInReview
	case ActionEscalate:
		if !a.EscalateTo.Valid() {
			return Event{}, errs.Invalid("escalateTo", errs.ReasonInvalidValue,
				"An escalation names its target: TAX_SPECIALIST, LEGAL, SECURITY, FINANCE_OPERATIONS or ENGINEERING.")
		}
		if err := needNote("An escalation says why."); err != nil {
			return Event{}, err
		}
		e.Status, e.EscalateTo = StatusInReview, a.EscalateTo
	case ActionRespond:
		if err := needNote("A response records what the customer was told."); err != nil {
			return Event{}, err
		}
		e.Status = StatusResponded
	case ActionResolve:
		e.Status = StatusResolved
	case ActionSignOff:
		if by == c.OpenedBy {
			return Event{}, errs.New(errs.CategoryPolicy, ReasonSegregationOfDuties,
				"The person who opened this case cannot sign it off. Another person must (T18-18).")
		}
		if err := needNote("A sign-off records what was checked."); err != nil {
			return Event{}, err
		}
	case ActionClose:
		if err := c.closable(); err != nil {
			return Event{}, err
		}
		e.Status = StatusClosed
	case ActionReopen:
		if err := needNote("A reopening says why."); err != nil {
			return Event{}, err
		}
		for i := len(c.History) - 1; i >= 0; i-- {
			if k := c.History[i].Action; k == ActionClose || k == ActionResolve {
				e.Reopens = c.History[i].Seq
				break
			}
		}
		e.Status = StatusInReview
	case ActionNote:
		if err := needNote("A note has text."); err != nil {
			return Event{}, err
		}
	case ActionRequestLegalHold:
		// A request, recorded for an administrator to act on. Support
		// places no hold itself.
		if err := needNote("A legal hold request says what should be held, and why."); err != nil {
			return Event{}, err
		}
	}
	return e, nil
}

// triage classifies the case and checks it has the facts to be worked. A
// case lacking its contract, period or seller stays TRIAGE_INCOMPLETE and
// the event records the facts requested (SUP-02).
func (c Case) triage(a Act, e Event) (Event, error) {
	if a.Type != "" && !a.Type.Valid() {
		return Event{}, errs.Invalid("type", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a support case type.", a.Type))
	}
	if a.Severity != "" && !a.Severity.Valid() {
		return Event{}, errs.Invalid("severity", errs.ReasonInvalidValue, "Severity is S1, S2, S3 or S4.")
	}
	period, contract, seller := strings.TrimSpace(a.Period), strings.TrimSpace(a.ContractRef), strings.TrimSpace(a.SellerRef)
	if err := validateFacts(period, contract, seller, nil); err != nil {
		return Event{}, err
	}
	if !a.StatutoryDueAt.IsZero() && !c.StatutoryDueAt.IsZero() && !a.StatutoryDueAt.Equal(c.StatutoryDueAt) {
		return Event{}, errs.Invalid("statutoryDueAt", errs.ReasonInvalidValue,
			"A statutory deadline, once recorded on a case, is not changed by support (SUP-07).")
	}
	if a.Type != c.Type {
		e.Type = a.Type
	}
	if a.Severity != c.Severity {
		e.Severity = a.Severity
		// A raised severity brings the SLA due time earlier, measured from
		// opening; a lowered one leaves it where it was.
		if e.Severity != "" {
			if due := c.OpenedAt.Add(e.Severity.SLATarget()).UTC(); due.Before(c.SLADueAt) {
				e.SLADueAt = due
			}
		}
	}
	if a.LegalEntity != c.LegalEntity {
		e.LegalEntity = a.LegalEntity
	}
	if period != c.Period {
		e.Period = period
	}
	if contract != c.ContractRef {
		e.ContractRef = contract
	}
	if seller != c.SellerRef {
		e.SellerRef = seller
	}
	if c.StatutoryDueAt.IsZero() && !a.StatutoryDueAt.IsZero() {
		e.StatutoryDueAt = a.StatutoryDueAt.UTC()
	}
	next := c.apply(e)
	if missing := next.Missing(); len(missing) > 0 {
		e.Status, e.MissingFacts = StatusTriageIncomplete, missing
	} else {
		e.Status = StatusTriaged
	}
	return e, nil
}

// ---------------------------------------------------------------------------
// the clocks
// ---------------------------------------------------------------------------

// ClockState is where one clock stands now.
type ClockState string

// The clock states. MET is the support SLA answered in time; PASSED is a
// statutory deadline gone by; NONE is no statutory deadline.
const (
	ClockNone     ClockState = "NONE"
	ClockOnTrack  ClockState = "ON_TRACK"
	ClockAtRisk   ClockState = "AT_RISK"
	ClockBreached ClockState = "BREACHED"
	ClockMet      ClockState = "MET"
	ClockPassed   ClockState = "PASSED"
)

// StatutoryWarning is how close a statutory deadline is before it is shown as
// at risk.
const StatutoryWarning = 72 * time.Hour

// Clocks are the two clocks of a case, kept apart (SUP-06).
type Clocks struct {
	SLADueAt        time.Time
	SLA             ClockState
	FirstResponseAt time.Time

	StatutoryDueAt time.Time
	Statutory      ClockState
	// StatutoryPrecedes is a statutory deadline earlier than the SLA's due
	// time: it, not the SLA, is the time the case must beat.
	StatutoryPrecedes bool
	// DueAt is the earlier of the two.
	DueAt time.Time
}

// Clocks reads both clocks as of now. Reading them changes nothing, and
// nothing a case records resets either (SUP-07).
func (c Case) Clocks(now time.Time) Clocks {
	k := Clocks{SLADueAt: c.SLADueAt, StatutoryDueAt: c.StatutoryDueAt, DueAt: c.SLADueAt, Statutory: ClockNone}
	window := c.SLADueAt.Sub(c.OpenedAt)
	switch first := c.FirstResponse(); {
	case first != nil:
		k.FirstResponseAt = first.RecordedAt
		k.SLA = ClockMet
		if first.RecordedAt.After(c.SLADueAt) {
			k.SLA = ClockBreached
		}
	case now.After(c.SLADueAt):
		k.SLA = ClockBreached
	case c.SLADueAt.Sub(now) <= window/4:
		k.SLA = ClockAtRisk
	default:
		k.SLA = ClockOnTrack
	}
	if !c.StatutoryDueAt.IsZero() {
		switch {
		case now.After(c.StatutoryDueAt):
			k.Statutory = ClockPassed
		case c.StatutoryDueAt.Sub(now) <= StatutoryWarning:
			k.Statutory = ClockAtRisk
		default:
			k.Statutory = ClockOnTrack
		}
		if c.StatutoryDueAt.Before(c.SLADueAt) {
			k.StatutoryPrecedes, k.DueAt = true, c.StatutoryDueAt
		}
	}
	return k
}

// Actionable are the actions the case's status allows now. The service still
// checks every guard when one is taken.
func (c Case) Actionable() []Action {
	var out []Action
	for _, a := range Actions {
		if !a.AllowedIn(c.Status) {
			continue
		}
		if a == ActionClose && c.closable() != nil {
			continue
		}
		out = append(out, a)
	}
	return out
}
