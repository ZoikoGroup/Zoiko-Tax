// Package notice models regulatory notice and appeal intake (Engineering Doc
// 18 W17, G-052; NAV-013).
//
// A Notice is something an authority sent a legal entity: a notice, an
// assessment, an audit, an appeal decision or an information request. It is
// registered with the deadline the authority set, the source of that
// deadline and whether anyone has verified it — never with a generic support
// due date.
//
// Three rules shape the package:
//
//   - The statutory clock is a ledger. The deadline recorded at intake stays
//     visible for ever, and the date changes only through a recorded
//     extension that carries the authority's evidence (T18-17). Nothing
//     reopens or resets it: there is no edge out of CLOSED, and moving a
//     response back into legal review touches no date. Our own SLA clock is a
//     separate field and never moves the statutory one.
//   - Unknowns escalate; they are never assumed. An unknown or unverified
//     deadline raises an urgent legal-review flag and admits no extension
//     (W17). Authenticity that is not verified escalates to legal validation
//     and blocks any approved answer (SUP-05). A recipient legal entity that
//     is absent or ambiguous goes to legal review at intake, with no
//     automatic answer (SUP-08).
//   - A notice references fiscal records and never changes them. Links to
//     returns, receipts, payments, ledger entries, rule versions and
//     correspondence are identifiers kept as given.
//
// The notice row is immutable. Its state is its event history, folded, the
// same shape compliance packages have.
package notice

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/compliance"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// The package's own reason codes. Four-eye approval reuses compliance's
// SEGREGATION_OF_DUTIES: it is the same rule, compared the same way.
var (
	// ReasonLegalValidationRequired is an answer the notice may not have yet:
	// its authenticity is not verified (SUP-05) or the legal entity it is
	// addressed to is not resolved (SUP-08).
	ReasonLegalValidationRequired = errs.Register("NOTICE_LEGAL_VALIDATION_REQUIRED", errs.CategoryPolicy,
		"w17",
		"The notice needs legal validation first: its authenticity is not verified or its recipient legal entity is not resolved.")

	// ReasonDeadlineUnverified is an extension asked of a deadline nobody has
	// verified (W17): there is nothing yet to extend.
	ReasonDeadlineUnverified = errs.Register("NOTICE_DEADLINE_UNVERIFIED", errs.CategoryConflict,
		"w17",
		"The statutory deadline is unknown or unverified. Record its verification before any extension.")

	// ReasonSettlementUnproven is a notice claimed settled on a payment that
	// has not settled (T18-20).
	ReasonSettlementUnproven = errs.Register("NOTICE_SETTLEMENT_UNPROVEN", errs.CategoryPolicy,
		"w17",
		"A notice is recorded as settled only on the authority's confirmation or a settled payment, never on a pending instruction.")

	// ReasonClosureBlocked is a closure with neither the authority's outcome
	// and receipt nor a documented legal escalation behind it.
	ReasonClosureBlocked = errs.Register("NOTICE_CLOSURE_BLOCKED", errs.CategoryPolicy,
		"w17",
		"A notice closes only on the authority's recorded outcome or a documented legal escalation.")
)

// ReasonSegregationOfDuties is the four-eye refusal, shared with compliance.
var ReasonSegregationOfDuties = compliance.ReasonSegregationOfDuties

// Type is what the authority sent.
type Type string

// The notice types.
const (
	TypeNotice             Type = "NOTICE"
	TypeAssessment         Type = "ASSESSMENT"
	TypeAudit              Type = "AUDIT"
	TypeAppeal             Type = "APPEAL"
	TypeInformationRequest Type = "INFORMATION_REQUEST"
)

// Valid reports whether the type is one this service knows.
func (t Type) Valid() bool {
	switch t {
	case TypeNotice, TypeAssessment, TypeAudit, TypeAppeal, TypeInformationRequest:
		return true
	}
	return false
}

// Status is where the matter stands.
type Status string

// The statuses. APPEAL is the branch after an outcome; CLOSED is terminal and
// nothing leaves it.
const (
	StatusReceived           Status = "RECEIVED"
	StatusAuthenticityReview Status = "AUTHENTICITY_REVIEW"
	StatusLegalReview        Status = "LEGAL_REVIEW"
	StatusResponseApproved   Status = "RESPONSE_APPROVED"
	StatusSubmitted          Status = "SUBMITTED"
	StatusOutcomeRecorded    Status = "OUTCOME_RECORDED"
	StatusAppeal             Status = "APPEAL"
	StatusClosed             Status = "CLOSED"
)

// Valid reports whether the status is one this service knows.
func (s Status) Valid() bool {
	_, ok := transitions[s]
	return ok
}

// Answered reports whether the response the statutory clock runs against has
// left for the authority, or the authority has decided.
func (s Status) Answered() bool {
	return s == StatusSubmitted || s == StatusOutcomeRecorded || s == StatusClosed
}

// transitions is the state machine. A step that keeps the status (an owner,
// an attachment, a deadline confirmation) is always permitted while the
// notice is open, and is not listed.
var transitions = map[Status][]Status{
	StatusReceived:           {StatusAuthenticityReview, StatusLegalReview},
	StatusAuthenticityReview: {StatusLegalReview},
	StatusLegalReview:        {StatusResponseApproved, StatusClosed},
	StatusResponseApproved:   {StatusLegalReview, StatusSubmitted},
	StatusSubmitted:          {StatusOutcomeRecorded},
	StatusOutcomeRecorded:    {StatusAppeal, StatusClosed},
	StatusAppeal:             {StatusLegalReview, StatusOutcomeRecorded},
	StatusClosed:             nil,
}

// CanTransition reports whether a step may move a notice from one status to
// another. Staying put is permitted from every status but CLOSED.
func CanTransition(from, to Status) bool {
	if from == StatusClosed {
		return false
	}
	if from == to {
		return true
	}
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Authenticity is what has been established about whether the authority
// really sent the notice.
type Authenticity string

// The authenticity statuses. Only VERIFIED admits an approved answer.
const (
	AuthenticityUnknown      Authenticity = "UNKNOWN"
	AuthenticityVerified     Authenticity = "VERIFIED"
	AuthenticityUnverifiable Authenticity = "UNVERIFIABLE"
	AuthenticityNotAuthentic Authenticity = "NOT_AUTHENTIC"
)

// Valid reports whether the status is one this service knows.
func (a Authenticity) Valid() bool {
	switch a {
	case AuthenticityUnknown, AuthenticityVerified, AuthenticityUnverifiable, AuthenticityNotAuthentic:
		return true
	}
	return false
}

// DeadlineSource is where a statutory deadline came from.
type DeadlineSource string

// The deadline sources. ESTIMATED and UNKNOWN are never verified.
const (
	SourceStatedOnNotice        DeadlineSource = "STATED_ON_NOTICE"
	SourceStatute               DeadlineSource = "STATUTE"
	SourceAuthorityConfirmation DeadlineSource = "AUTHORITY_CONFIRMATION"
	SourceAuthorityPortal       DeadlineSource = "AUTHORITY_PORTAL"
	SourceEstimated             DeadlineSource = "ESTIMATED"
	SourceUnknown               DeadlineSource = "UNKNOWN"
)

// Valid reports whether the source is one this service knows.
func (d DeadlineSource) Valid() bool {
	switch d {
	case SourceStatedOnNotice, SourceStatute, SourceAuthorityConfirmation, SourceAuthorityPortal, SourceEstimated, SourceUnknown:
		return true
	}
	return false
}

// verifiable reports whether a deadline from this source can be verified.
func (d DeadlineSource) verifiable() bool { return d != SourceEstimated && d != SourceUnknown }

// DeadlineKind is what a deadline ledger entry records.
type DeadlineKind string

// The deadline kinds. ORIGINAL is the intake's; CONFIRMATION verifies or
// establishes the date without moving it; EXTENSION moves it later on the
// authority's evidence; APPEAL is the next deadline an appeal opens.
const (
	DeadlineOriginal     DeadlineKind = "ORIGINAL"
	DeadlineConfirmation DeadlineKind = "CONFIRMATION"
	DeadlineExtension    DeadlineKind = "EXTENSION"
	DeadlineAppeal       DeadlineKind = "APPEAL"
)

// Outcome is what the authority decided.
type Outcome string

// The outcomes.
const (
	OutcomeAccepted  Outcome = "ACCEPTED"
	OutcomeAdjusted  Outcome = "ADJUSTED"
	OutcomeRejected  Outcome = "REJECTED"
	OutcomeWithdrawn Outcome = "WITHDRAWN"
)

// Valid reports whether the outcome is one this service knows.
func (o Outcome) Valid() bool {
	switch o {
	case OutcomeAccepted, OutcomeAdjusted, OutcomeRejected, OutcomeWithdrawn:
		return true
	}
	return false
}

// Settlement is the evidence a claim of settlement rests on (T18-20).
type Settlement string

// The settlement evidence kinds. A pending payment instruction is named so
// that it can be refused by name.
const (
	SettlementAuthorityConfirmed Settlement = "AUTHORITY_CONFIRMED"
	SettlementPaymentSettled     Settlement = "PAYMENT_SETTLED"
	SettlementPaymentPending     Settlement = "PAYMENT_INSTRUCTION_PENDING"
)

// Valid reports whether the settlement evidence is one this service knows.
func (s Settlement) Valid() bool {
	switch s {
	case SettlementAuthorityConfirmed, SettlementPaymentSettled, SettlementPaymentPending:
		return true
	}
	return false
}

// EscalationReason is why a notice went to legal.
type EscalationReason string

// The escalation reasons.
const (
	EscalationDeadline     EscalationReason = "DEADLINE_UNVERIFIED"
	EscalationAuthenticity EscalationReason = "AUTHENTICITY_UNRESOLVED"
	EscalationEntity       EscalationReason = "ENTITY_UNRESOLVED"
	EscalationLegal        EscalationReason = "LEGAL_JUDGEMENT"
)

// Valid reports whether the reason is one this service knows.
func (r EscalationReason) Valid() bool {
	switch r {
	case EscalationDeadline, EscalationAuthenticity, EscalationEntity, EscalationLegal:
		return true
	}
	return false
}

// LinkKind is the kind of record a link references.
type LinkKind string

// The link kinds. Every one is a read-only reference.
const (
	LinkReturn            LinkKind = "RETURN"
	LinkSubmissionReceipt LinkKind = "SUBMISSION_RECEIPT"
	LinkPayment           LinkKind = "PAYMENT"
	LinkLedgerEntry       LinkKind = "LEDGER_ENTRY"
	LinkRuleVersion       LinkKind = "RULE_VERSION"
	LinkCorrespondence    LinkKind = "CORRESPONDENCE"
	LinkObligation        LinkKind = "OBLIGATION"
	LinkDecision          LinkKind = "DECISION"
)

// Valid reports whether the kind is one this service knows.
func (k LinkKind) Valid() bool {
	switch k {
	case LinkReturn, LinkSubmissionReceipt, LinkPayment, LinkLedgerEntry, LinkRuleVersion, LinkCorrespondence,
		LinkObligation, LinkDecision:
		return true
	}
	return false
}

// Link references a record elsewhere, by its identifier.
type Link struct {
	Kind      LinkKind
	Reference string
}

// EventKind is what an event records.
type EventKind string

// The event kinds. RECEIVED is the first and only the first.
const (
	EventReceived         EventKind = "RECEIVED"
	EventTransitioned     EventKind = "TRANSITIONED"
	EventAuthenticity     EventKind = "AUTHENTICITY_RECORDED"
	EventEscalated        EventKind = "LEGAL_ESCALATED"
	EventEntityResolved   EventKind = "ENTITY_RESOLVED"
	EventOwnerAssigned    EventKind = "OWNER_ASSIGNED"
	EventAttachment       EventKind = "ATTACHMENT_RECORDED"
	EventDeadline         EventKind = "DEADLINE_RECORDED"
	EventResponsePrepared EventKind = "RESPONSE_PREPARED"
	EventResponseApproved EventKind = "RESPONSE_APPROVED"
	EventSubmitted        EventKind = "SUBMITTED"
	EventOutcome          EventKind = "OUTCOME_RECORDED"
	EventAppeal           EventKind = "APPEAL_LODGED"
	EventClosed           EventKind = "CLOSED"
)

// Deadline is one statutory deadline as recorded: a civil date in the
// authority's calendar, the instant it ends there, where it came from and
// whether it was verified. A zero Date is a deadline nobody knows yet.
type Deadline struct {
	Kind     DeadlineKind
	Date     time.Time
	Timezone string
	Source   DeadlineSource
	Verified bool
}

// Known reports whether the deadline has a date.
func (d Deadline) Known() bool { return !d.Date.IsZero() }

// DueAt is the last instant of the due date in the authority's timezone, or
// zero for an unknown deadline.
func (d Deadline) DueAt() time.Time {
	if !d.Known() {
		return time.Time{}
	}
	loc, err := time.LoadLocation(d.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return time.Date(d.Date.Year(), d.Date.Month(), d.Date.Day(), 23, 59, 59, 999999000, loc)
}

// Attachment is evidence of a document: its SHA-256, name and media type.
// The document itself is not stored here.
type Attachment struct {
	SHA256    string
	Filename  string
	MediaType string
}

// Event is one act on a notice. The fields a kind does not use are zero.
type Event struct {
	Seq  int
	Kind EventKind
	// Status is the notice's status after this event.
	Status Status

	Note         string
	Evidence     string
	AuthorityRef string

	Deadline     *Deadline
	Authenticity Authenticity
	Escalation   EscalationReason
	LegalEntity  id.LegalEntityID
	Owner        id.UserID
	// SLADue is our internal target. It is never a statutory date.
	SLADue     time.Time
	Attachment *Attachment

	ResponseVersion int
	// ResponseSHA256 is the digest of the response package's document, when
	// one was attached.
	ResponseSHA256 string

	Outcome       Outcome
	OutcomeAmount *fiscal.Money
	Settled       bool
	Settlement    Settlement
	HearingDate   time.Time

	RecordedAt time.Time
	RecordedBy id.UserID
}

// Notice is one registered notice, folded from its events.
type Notice struct {
	ID       id.NoticeID
	TenantID id.TenantID

	Type         Type
	Authority    string
	Jurisdiction string
	// LegalEntity is the recipient as registered; zero when the notice names
	// none we could resolve.
	LegalEntity     id.LegalEntityID
	EntityAmbiguous bool
	// AuthorityRef is the authority's own reference for the matter.
	AuthorityRef string
	ReceivedAt   time.Time
	PeriodStart  time.Time
	PeriodEnd    time.Time
	// Amount is what the notice states, as stated. It is a reference, not a
	// liability this service computes or posts.
	Amount     *fiscal.Money
	LinkedCase string
	Links      []Link

	CreatedAt time.Time
	CreatedBy id.UserID

	Status  Status
	History []Event
}

var (
	jurisdictionShape = regexp.MustCompile(`^[A-Z]{2}(-[A-Z0-9]{1,3})?$`)
	sha256Shape       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	mediaTypeShape    = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]{0,63}/[a-z0-9][a-z0-9!#$&^_.+-]{0,126}$`)
)

// validTimezone reports whether tz names an IANA timezone.
func validTimezone(tz string) bool {
	if tz == "" || strings.EqualFold(tz, "local") {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

// Validate refuses a notice header that cannot be registered.
func (n Notice) Validate() error {
	switch {
	case !n.Type.Valid():
		return errs.Invalid("type", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a notice type this service knows.", n.Type))
	case strings.TrimSpace(n.Authority) == "" || len(n.Authority) > 128:
		return errs.Invalid("authority", errs.ReasonMissingField, "A notice names the authority that sent it.")
	case !jurisdictionShape.MatchString(n.Jurisdiction):
		return errs.Invalid("jurisdiction", errs.ReasonInvalidValue, "The jurisdiction is an ISO 3166 country or subdivision code, such as DE or US-CA.")
	case strings.TrimSpace(n.AuthorityRef) == "" || len(n.AuthorityRef) > 200:
		return errs.Invalid("authorityReference", errs.ReasonMissingField, "A notice carries the authority's own reference for the matter.")
	case n.ReceivedAt.IsZero():
		return errs.Invalid("receivedAt", errs.ReasonMissingField, "A notice records when it was received.")
	case n.PeriodStart.IsZero() != n.PeriodEnd.IsZero():
		return errs.Invalid("periodEnd", errs.ReasonInvalidValue, "A period has both a start and an end, or neither.")
	case !n.PeriodEnd.IsZero() && n.PeriodEnd.Before(n.PeriodStart):
		return errs.Invalid("periodEnd", errs.ReasonInvalidValue, "The period ends before it starts.")
	case len(n.LinkedCase) > 200:
		return errs.Invalid("linkedCaseId", errs.ReasonInvalidValue, "A linked case identifier is at most 200 characters.")
	case len(n.Links) > 50:
		return errs.Invalid("links", errs.ReasonInvalidValue, "A notice carries at most 50 links.")
	}
	for i, l := range n.Links {
		if !l.Kind.Valid() || strings.TrimSpace(l.Reference) == "" || len(l.Reference) > 200 {
			return errs.Invalid(fmt.Sprintf("links[%d]", i), errs.ReasonInvalidValue,
				"A link names a known kind and the referenced record's identifier.")
		}
	}
	return nil
}

// validateDeadline refuses a deadline that claims more than it has.
func validateDeadline(field string, d Deadline, evidence string) error {
	if !d.Source.Valid() {
		return errs.Invalid(field+".source", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a deadline source this service knows.", d.Source))
	}
	if !validTimezone(d.Timezone) {
		return errs.Invalid(field+".timezone", errs.ReasonInvalidValue, "The deadline names the authority's IANA timezone, such as Europe/Berlin.")
	}
	if !d.Known() {
		if d.Source != SourceUnknown || d.Verified {
			return errs.Invalid(field+".dueDate", errs.ReasonMissingField,
				"A deadline with no date is UNKNOWN and unverified; a source or verification needs the date it vouches for.")
		}
		return nil
	}
	if d.Source == SourceUnknown {
		return errs.Invalid(field+".source", errs.ReasonInvalidValue, "A dated deadline names where its date came from.")
	}
	if d.Verified && !d.Source.verifiable() {
		return errs.Invalid(field+".verified", errs.ReasonInvalidValue, fmt.Sprintf("A %s deadline is not verified; it is checked against the authority first.", d.Source))
	}
	if d.Verified && strings.TrimSpace(evidence) == "" {
		return errs.Invalid(field+".evidence", errs.ReasonMissingField, "A verified deadline says how it was verified.")
	}
	return nil
}

// Intake is what the person registering a notice states besides the header.
type Intake struct {
	Deadline Deadline
	// DeadlineEvidence is how the deadline was established.
	DeadlineEvidence string
	Owner            id.UserID
	SLADue           time.Time
	Note             string
}

// Open validates a new notice and returns its first events. A notice whose
// recipient legal entity is absent or ambiguous goes straight to legal review
// (SUP-08); nothing answers it automatically.
func Open(n Notice, in Intake, by id.UserID, at time.Time) ([]Event, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}
	if by.IsZero() {
		return nil, errs.New(errs.CategoryPolicy, errs.ReasonForbidden, "A notice is registered by a person.")
	}
	in.Deadline.Kind = DeadlineOriginal
	if err := validateDeadline("deadline", in.Deadline, in.DeadlineEvidence); err != nil {
		return nil, err
	}
	d := in.Deadline
	first := Event{Seq: 1, Kind: EventReceived, Status: StatusReceived, Deadline: &d, Authenticity: AuthenticityUnknown,
		Owner: in.Owner, SLADue: in.SLADue, Evidence: strings.TrimSpace(in.DeadlineEvidence), Note: strings.TrimSpace(in.Note),
		RecordedAt: at.UTC(), RecordedBy: by}
	out := []Event{first}
	if n.LegalEntity.IsZero() || n.EntityAmbiguous {
		out = append(out, Event{Seq: 2, Kind: EventEscalated, Status: StatusLegalReview, Escalation: EscalationEntity,
			Note:       "The recipient legal entity is absent or ambiguous; legal review decides who the notice is for (SUP-08).",
			RecordedAt: at.UTC(), RecordedBy: by})
	}
	return out, nil
}

// Fold applies a notice's events in order. It refuses a history no correct
// writer produces.
func Fold(n Notice, events []Event) (Notice, error) {
	if len(events) == 0 || events[0].Kind != EventReceived || events[0].Seq != 1 {
		return Notice{}, fmt.Errorf("notice: a notice's history starts with its RECEIVED event")
	}
	n.Status = events[0].Status
	for i, e := range events[1:] {
		if e.Seq != i+2 || e.Kind == EventReceived {
			return Notice{}, fmt.Errorf("notice: event %d is out of sequence", e.Seq)
		}
		if !CanTransition(n.Status, e.Status) {
			return Notice{}, fmt.Errorf("notice: event %d moves %s to %s", e.Seq, n.Status, e.Status)
		}
		n.Status = e.Status
	}
	n.History = events
	return n, nil
}

// ---------------------------------------------------------------------------
// what the history says
// ---------------------------------------------------------------------------

// DeadlineEntry is one row of the deadline ledger, with who recorded it and
// on what evidence.
type DeadlineEntry struct {
	Deadline
	Seq          int
	Evidence     string
	AuthorityRef string
	Attachment   string
	RecordedAt   time.Time
	RecordedBy   id.UserID
}

// Deadlines is the deadline ledger, oldest first. The ORIGINAL entry is
// always first and is never rewritten.
func (n Notice) Deadlines() []DeadlineEntry {
	var out []DeadlineEntry
	for _, e := range n.History {
		if e.Deadline == nil {
			continue
		}
		entry := DeadlineEntry{Deadline: *e.Deadline, Seq: e.Seq, Evidence: e.Evidence, AuthorityRef: e.AuthorityRef,
			RecordedAt: e.RecordedAt, RecordedBy: e.RecordedBy}
		if e.Attachment != nil {
			entry.Attachment = e.Attachment.SHA256
		}
		out = append(out, entry)
	}
	return out
}

// Original is the deadline as registered at intake.
func (n Notice) Original() Deadline {
	if len(n.History) > 0 && n.History[0].Deadline != nil {
		return *n.History[0].Deadline
	}
	return Deadline{Kind: DeadlineOriginal, Source: SourceUnknown}
}

// Current is the statutory deadline in force: the latest ledger entry. A
// confirmation never moves a known date, so the latest entry is either the
// same date verified, an evidenced extension, or an appeal's next deadline.
func (n Notice) Current() Deadline {
	cur := n.Original()
	for _, e := range n.History[min(1, len(n.History)):] {
		if e.Deadline != nil {
			cur = *e.Deadline
		}
	}
	return cur
}

// Authenticity is the latest authenticity recorded, UNKNOWN until one is.
func (n Notice) Authenticity() Authenticity {
	out := AuthenticityUnknown
	for _, e := range n.History {
		if e.Kind == EventAuthenticity {
			out = e.Authenticity
		}
	}
	return out
}

// Entity is the recipient legal entity as resolved, and whether it is.
func (n Notice) Entity() (id.LegalEntityID, bool) {
	le, ok := n.LegalEntity, !n.LegalEntity.IsZero() && !n.EntityAmbiguous
	for _, e := range n.History {
		if e.Kind == EventEntityResolved {
			le, ok = e.LegalEntity, true
		}
	}
	return le, ok
}

// Owner is the deadline owner and our SLA target, as last assigned.
func (n Notice) Owner() (id.UserID, time.Time) {
	var (
		owner id.UserID
		sla   time.Time
	)
	for _, e := range n.History {
		if e.Kind == EventReceived || e.Kind == EventOwnerAssigned {
			owner, sla = e.Owner, e.SLADue
		}
	}
	return owner, sla
}

// Escalated reports whether the notice has gone to legal.
func (n Notice) Escalated() bool {
	for _, e := range n.History {
		if e.Kind == EventEscalated {
			return true
		}
	}
	return false
}

// documentedEscalation reports whether a legal escalation carries evidence:
// counsel's reference, a decision, a document.
func (n Notice) documentedEscalation() bool {
	for _, e := range n.History {
		if e.Kind == EventEscalated && strings.TrimSpace(e.Evidence) != "" {
			return true
		}
	}
	return false
}

// Attachments are the documents recorded as evidence, in order.
func (n Notice) Attachments() []Event {
	var out []Event
	for _, e := range n.History {
		if e.Attachment != nil && e.Kind == EventAttachment {
			out = append(out, e)
		}
	}
	return out
}

// HasAttachment reports whether a document with this digest is recorded.
func (n Notice) HasAttachment(sha string) bool {
	for _, e := range n.History {
		if e.Attachment != nil && e.Attachment.SHA256 == sha {
			return true
		}
	}
	return false
}

// Response is the current response package, or nil before the first.
func (n Notice) Response() *Event {
	for i := len(n.History) - 1; i >= 0; i-- {
		if n.History[i].Kind == EventResponsePrepared {
			return &n.History[i]
		}
	}
	return nil
}

// Approval is the approval of the current response package, or nil. A newer
// package supersedes any approval simply by being newer.
func (n Notice) Approval() *Event {
	for i := len(n.History) - 1; i >= 0; i-- {
		switch n.History[i].Kind {
		case EventResponsePrepared:
			return nil
		case EventResponseApproved:
			return &n.History[i]
		}
	}
	return nil
}

// LastOutcome is the authority's latest recorded outcome, or nil.
func (n Notice) LastOutcome() *Event {
	for i := len(n.History) - 1; i >= 0; i-- {
		if n.History[i].Kind == EventOutcome {
			return &n.History[i]
		}
	}
	return nil
}

// Hearing is the hearing date of the appeal in progress, zero when there is
// none.
func (n Notice) Hearing() time.Time {
	if n.Status != StatusAppeal && n.Status != StatusLegalReview && n.Status != StatusResponseApproved && n.Status != StatusSubmitted {
		return time.Time{}
	}
	for i := len(n.History) - 1; i >= 0; i-- {
		switch n.History[i].Kind {
		case EventOutcome:
			return time.Time{}
		case EventAppeal:
			return n.History[i].HearingDate
		}
	}
	return time.Time{}
}

// ---------------------------------------------------------------------------
// acts
// ---------------------------------------------------------------------------

func (n Notice) next(kind EventKind, to Status, by id.UserID, at time.Time) (Event, error) {
	if n.Status == StatusClosed {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"The notice is closed. Nothing reopens it, and its clock is not reset; a new matter is a new notice.")
	}
	if !CanTransition(n.Status, to) {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A notice that is %s cannot move to %s.", n.Status, to))
	}
	if by.IsZero() {
		return Event{}, errs.New(errs.CategoryPolicy, errs.ReasonForbidden, "Every notice event is recorded against a person.")
	}
	return Event{Seq: len(n.History) + 1, Kind: kind, Status: to, RecordedAt: at.UTC(), RecordedBy: by}, nil
}

// chain appends events computed one after another, each against the history
// the previous one leaves.
func (n Notice) chain(steps ...func(Notice) (Event, error)) ([]Event, error) {
	var out []Event
	cur := n
	for _, step := range steps {
		e, err := step(cur)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		cur.History = append(append([]Event(nil), cur.History...), e)
		cur.Status = e.Status
	}
	return out, nil
}

func required(field, v, why string) error {
	if strings.TrimSpace(v) == "" {
		return errs.Invalid(field, errs.ReasonMissingField, why)
	}
	return nil
}

func tooLong(field, v string, limit int) error {
	if len(v) > limit {
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("At most %d characters.", limit))
	}
	return nil
}

// Transition moves the notice through the review stages and to closure. The
// other moves are their own acts, each with its own evidence:
// approval, submission, outcome and appeal.
func (n Notice) Transition(to Status, note string, by id.UserID, at time.Time) (Event, error) {
	if err := tooLong("note", note, 2000); err != nil {
		return Event{}, err
	}
	switch to {
	case StatusAuthenticityReview:
		if n.Status != StatusReceived {
			return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				fmt.Sprintf("Authenticity review starts from RECEIVED; this notice is %s.", n.Status))
		}
	case StatusLegalReview:
		switch n.Status {
		case StatusAuthenticityReview:
			if n.Authenticity() != AuthenticityVerified {
				return Event{}, errs.New(errs.CategoryPolicy, ReasonLegalValidationRequired,
					"Authenticity is not verified, so the notice is not accepted into review as genuine. Record its authenticity, or escalate it to legal validation (SUP-05).")
			}
		case StatusAppeal:
		case StatusResponseApproved:
			if err := required("note", note, "Withdrawing an approved response for more work says why."); err != nil {
				return Event{}, err
			}
		default:
			return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				fmt.Sprintf("A notice that is %s does not move to LEGAL_REVIEW this way; escalate it instead.", n.Status))
		}
	case StatusClosed:
		return n.Close(note, by, at)
	default:
		return Event{}, errs.Invalid("to", errs.ReasonInvalidValue,
			fmt.Sprintf("%s is reached through its own action — approval, submission, outcome or appeal — with the evidence it needs.", to))
	}
	e, err := n.next(EventTransitioned, to, by, at)
	if err != nil {
		return Event{}, err
	}
	e.Note = strings.TrimSpace(note)
	return e, nil
}

// Close records the disposition. It needs the authority's outcome with its
// receipt, or a documented legal escalation; an appeal still running keeps
// the matter open (SUP-16).
func (n Notice) Close(note string, by id.UserID, at time.Time) (Event, error) {
	if err := required("note", note, "A closure records its disposition."); err != nil {
		return Event{}, err
	}
	if err := tooLong("note", note, 2000); err != nil {
		return Event{}, err
	}
	switch n.Status {
	case StatusOutcomeRecorded:
		if o := n.LastOutcome(); o == nil || strings.TrimSpace(o.AuthorityRef) == "" {
			return Event{}, errs.New(errs.CategoryPolicy, ReasonClosureBlocked, "The outcome carries no authority receipt.")
		}
	case StatusLegalReview:
		if !n.documentedEscalation() {
			return Event{}, errs.New(errs.CategoryPolicy, ReasonClosureBlocked,
				"Nothing the authority decided is recorded, and no legal escalation documents a disposition. Record the outcome, or escalate with counsel's documentation.")
		}
	case StatusAppeal:
		return Event{}, errs.New(errs.CategoryPolicy, ReasonClosureBlocked,
			"An appeal is open; the matter stays open until its outcome is recorded (SUP-16).")
	default:
		if n.Status != StatusClosed {
			return Event{}, errs.New(errs.CategoryPolicy, ReasonClosureBlocked,
				fmt.Sprintf("A notice that is %s has neither an outcome nor a documented escalation to close on.", n.Status))
		}
	}
	e, err := n.next(EventClosed, StatusClosed, by, at)
	if err != nil {
		return Event{}, err
	}
	e.Note = strings.TrimSpace(note)
	return e, nil
}

// RecordAuthenticity records what was established about the notice's origin.
// Anything but VERIFIED escalates to legal validation in the same act, and
// voids an approved answer: nothing is accepted as genuine that is not
// (SUP-05).
func (n Notice) RecordAuthenticity(a Authenticity, evidence string, by id.UserID, at time.Time) ([]Event, error) {
	if !a.Valid() || a == AuthenticityUnknown {
		return nil, errs.Invalid("authenticity", errs.ReasonInvalidValue,
			"Record VERIFIED, UNVERIFIABLE or NOT_AUTHENTIC; UNKNOWN is where every notice starts.")
	}
	if err := required("evidence", evidence, "Authenticity is recorded with how it was established."); err != nil {
		return nil, err
	}
	if err := tooLong("evidence", evidence, 20000); err != nil {
		return nil, err
	}
	if n.Status.Answered() {
		return nil, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("The notice is %s; its authenticity was settled before it was answered.", n.Status))
	}
	steps := []func(Notice) (Event, error){func(cur Notice) (Event, error) {
		e, err := cur.next(EventAuthenticity, cur.Status, by, at)
		e.Authenticity, e.Evidence = a, strings.TrimSpace(evidence)
		return e, err
	}}
	if a != AuthenticityVerified {
		steps = append(steps, func(cur Notice) (Event, error) {
			to := cur.Status
			if to == StatusReceived || to == StatusAuthenticityReview || to == StatusResponseApproved {
				to = StatusLegalReview
			}
			e, err := cur.next(EventEscalated, to, by, at)
			e.Escalation, e.Evidence = EscalationAuthenticity, strings.TrimSpace(evidence)
			e.Note = fmt.Sprintf("Authenticity is %s; escalated to legal validation (SUP-05).", a)
			return e, err
		})
	}
	return n.chain(steps...)
}

// Escalate sends the notice to legal. Evidence — counsel's reference, a
// memo — makes it a documented escalation, which is what a disposition
// without an authority outcome rests on.
func (n Notice) Escalate(reason EscalationReason, note, evidence string, by id.UserID, at time.Time) (Event, error) {
	if !reason.Valid() {
		return Event{}, errs.Invalid("reason", errs.ReasonInvalidValue, fmt.Sprintf("%q is not an escalation reason.", reason))
	}
	if err := required("note", note, "An escalation says what legal is asked to decide."); err != nil {
		return Event{}, err
	}
	if err := tooLong("note", note, 2000); err != nil {
		return Event{}, err
	}
	if err := tooLong("evidence", evidence, 20000); err != nil {
		return Event{}, err
	}
	to := n.Status
	if to == StatusReceived || to == StatusAuthenticityReview {
		to = StatusLegalReview
	}
	e, err := n.next(EventEscalated, to, by, at)
	if err != nil {
		return Event{}, err
	}
	e.Escalation, e.Note, e.Evidence = reason, strings.TrimSpace(note), strings.TrimSpace(evidence)
	return e, nil
}

// ResolveEntity records which legal entity the notice is addressed to, on
// legal's evidence (SUP-08).
func (n Notice) ResolveEntity(le id.LegalEntityID, evidence string, by id.UserID, at time.Time) (Event, error) {
	if le.IsZero() {
		return Event{}, errs.Invalid("legalEntityId", errs.ReasonMissingField, "Name the legal entity the notice is addressed to.")
	}
	if err := required("evidence", evidence, "Resolving the recipient says how it was established."); err != nil {
		return Event{}, err
	}
	if err := tooLong("evidence", evidence, 20000); err != nil {
		return Event{}, err
	}
	if n.Status.Answered() {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("The notice is %s; its recipient was settled before it was answered.", n.Status))
	}
	e, err := n.next(EventEntityResolved, n.Status, by, at)
	if err != nil {
		return Event{}, err
	}
	e.LegalEntity, e.Evidence = le, strings.TrimSpace(evidence)
	return e, nil
}

// AssignOwner names the person who owns the deadline, and our internal SLA
// target. The SLA is ours; it never moves the statutory date.
func (n Notice) AssignOwner(owner id.UserID, sla time.Time, note string, by id.UserID, at time.Time) (Event, error) {
	if owner.IsZero() {
		return Event{}, errs.Invalid("ownerId", errs.ReasonMissingField, "Name the person who owns the deadline.")
	}
	if err := tooLong("note", note, 2000); err != nil {
		return Event{}, err
	}
	e, err := n.next(EventOwnerAssigned, n.Status, by, at)
	if err != nil {
		return Event{}, err
	}
	e.Owner, e.SLADue, e.Note = owner, sla, strings.TrimSpace(note)
	return e, nil
}

// RecordAttachment records a document as evidence by its digest, name and
// media type. Nothing is uploaded or stored here.
func (n Notice) RecordAttachment(a Attachment, note string, by id.UserID, at time.Time) (Event, error) {
	a.SHA256, a.Filename, a.MediaType = strings.ToLower(strings.TrimSpace(a.SHA256)), strings.TrimSpace(a.Filename), strings.ToLower(strings.TrimSpace(a.MediaType))
	switch {
	case !sha256Shape.MatchString(a.SHA256):
		return Event{}, errs.Invalid("sha256", errs.ReasonInvalidValue, "The digest is the document's SHA-256 in lowercase hex.")
	case a.Filename == "" || len(a.Filename) > 255:
		return Event{}, errs.Invalid("filename", errs.ReasonInvalidValue, "The file name is 1 to 255 characters.")
	case !mediaTypeShape.MatchString(a.MediaType):
		return Event{}, errs.Invalid("mediaType", errs.ReasonInvalidValue, "The media type is type/subtype, such as application/pdf.")
	case n.HasAttachment(a.SHA256):
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "A document with this digest is already recorded on the notice.")
	}
	if err := tooLong("note", note, 2000); err != nil {
		return Event{}, err
	}
	e, err := n.next(EventAttachment, n.Status, by, at)
	if err != nil {
		return Event{}, err
	}
	e.Attachment, e.Note = &a, strings.TrimSpace(note)
	return e, nil
}

// DeadlineRecord is a confirmation or extension of the statutory deadline.
type DeadlineRecord struct {
	Deadline
	Evidence     string
	AuthorityRef string
	// Attachment is the digest of a recorded document that evidences it.
	Attachment string
}

// RecordDeadline adds to the deadline ledger. A CONFIRMATION verifies the
// date in force, or establishes an unknown one; it never moves a known date.
// An EXTENSION moves the date later and needs a verified date to extend and
// the authority's evidence of the grant — never a fabricated one (W17,
// T18-17). The ORIGINAL entry stays in the ledger either way.
func (n Notice) RecordDeadline(r DeadlineRecord, by id.UserID, at time.Time) (Event, error) {
	if err := required("evidence", r.Evidence, "A deadline is recorded with the evidence it rests on."); err != nil {
		return Event{}, err
	}
	if err := tooLong("evidence", r.Evidence, 20000); err != nil {
		return Event{}, err
	}
	if err := tooLong("authorityReference", r.AuthorityRef, 200); err != nil {
		return Event{}, err
	}
	if r.Attachment != "" && !n.HasAttachment(r.Attachment) {
		return Event{}, errs.Invalid("attachmentSha256", errs.ReasonInvalidValue, "Record the document as an attachment before citing it.")
	}
	if !r.Known() {
		return Event{}, errs.Invalid("dueDate", errs.ReasonMissingField, "A confirmation or extension names the date.")
	}
	if err := validateDeadline("deadline", r.Deadline, r.Evidence); err != nil {
		return Event{}, err
	}
	cur := n.Current()
	switch r.Kind {
	case DeadlineConfirmation:
		if !r.Verified {
			return Event{}, errs.Invalid("verified", errs.ReasonInvalidValue, "A confirmation is a verification; an unverified date stays the one in force.")
		}
		if cur.Known() && !sameDay(cur, r.Deadline) {
			return Event{}, errs.New(errs.CategoryConflict, ReasonDeadlineUnverified,
				fmt.Sprintf("The deadline in force is %s; a confirmation cannot move it. A later date is an extension with the authority's evidence (T18-17).",
					cur.Date.Format(time.DateOnly)))
		}
	case DeadlineExtension:
		if !cur.Known() || !cur.Verified {
			return Event{}, errs.New(errs.CategoryConflict, ReasonDeadlineUnverified,
				"The deadline in force is unknown or unverified; there is nothing verified to extend (W17).")
		}
		if !r.Verified {
			return Event{}, errs.Invalid("verified", errs.ReasonInvalidValue, "An extension is the authority's grant, verified; an unverified one is not recorded.")
		}
		if strings.TrimSpace(r.AuthorityRef) == "" && r.Attachment == "" {
			return Event{}, errs.Invalid("authorityReference", errs.ReasonMissingField,
				"An extension carries the authority's reference for the grant or the recorded document that grants it.")
		}
		if !r.DueAt().After(cur.DueAt()) {
			return Event{}, errs.Invalid("dueDate", errs.ReasonInvalidValue, "An extension moves the deadline later than the one in force.")
		}
	default:
		return Event{}, errs.Invalid("kind", errs.ReasonInvalidValue, "A deadline is recorded as a CONFIRMATION or an EXTENSION; an appeal's deadline is lodged with the appeal.")
	}
	if n.Status.Answered() {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("The notice is %s; its response is no longer running against this deadline.", n.Status))
	}
	e, err := n.next(EventDeadline, n.Status, by, at)
	if err != nil {
		return Event{}, err
	}
	d := r.Deadline
	e.Deadline, e.Evidence, e.AuthorityRef = &d, strings.TrimSpace(r.Evidence), strings.TrimSpace(r.AuthorityRef)
	if r.Attachment != "" {
		for _, a := range n.Attachments() {
			if a.Attachment.SHA256 == r.Attachment {
				att := *a.Attachment
				e.Attachment = &att
			}
		}
	}
	return e, nil
}

func sameDay(a, b Deadline) bool {
	return a.Date.Format(time.DateOnly) == b.Date.Format(time.DateOnly) && a.Timezone == b.Timezone
}

// PrepareResponse records a new response package version. From
// RESPONSE_APPROVED it supersedes the approved one and returns the notice to
// legal review; the deadline is untouched.
func (n Notice) PrepareResponse(summary, sha string, by id.UserID, at time.Time) (Event, error) {
	if err := required("summary", summary, "A response package says what it answers."); err != nil {
		return Event{}, err
	}
	if err := tooLong("summary", summary, 2000); err != nil {
		return Event{}, err
	}
	sha = strings.ToLower(strings.TrimSpace(sha))
	if sha != "" && !sha256Shape.MatchString(sha) {
		return Event{}, errs.Invalid("sha256", errs.ReasonInvalidValue, "The digest is the document's SHA-256 in lowercase hex.")
	}
	if n.Status != StatusLegalReview && n.Status != StatusResponseApproved {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A response is prepared in legal review; this notice is %s.", n.Status))
	}
	e, err := n.next(EventResponsePrepared, StatusLegalReview, by, at)
	if err != nil {
		return Event{}, err
	}
	version := 1
	if prev := n.Response(); prev != nil {
		version = prev.ResponseVersion + 1
	}
	e.ResponseVersion, e.ResponseSHA256, e.Note = version, sha, strings.TrimSpace(summary)
	return e, nil
}

// ApproveResponse is the four-eye approval of the response package version
// named. The approver did not prepare it; the notice's authenticity is
// verified and its recipient resolved (SUP-05, SUP-08).
func (n Notice) ApproveResponse(version int, note string, by id.UserID, at time.Time) (Event, error) {
	if err := tooLong("note", note, 2000); err != nil {
		return Event{}, err
	}
	if n.Status != StatusLegalReview {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A response is approved in legal review; this notice is %s.", n.Status))
	}
	prep := n.Response()
	if prep == nil {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid, "No response package has been prepared.")
	}
	if version != prep.ResponseVersion {
		return Event{}, errs.New(errs.CategoryConflict, compliance.ReasonStaleVersion,
			fmt.Sprintf("Version %d is not the current response package; version %d is.", version, prep.ResponseVersion))
	}
	if prep.RecordedBy == by {
		return Event{}, errs.New(errs.CategoryPolicy, ReasonSegregationOfDuties,
			"The person who prepared this response package cannot approve it; another person must.")
	}
	if a := n.Authenticity(); a != AuthenticityVerified {
		return Event{}, errs.New(errs.CategoryPolicy, ReasonLegalValidationRequired,
			fmt.Sprintf("The notice's authenticity is %s. No answer is approved until it is verified (SUP-05).", a))
	}
	if _, ok := n.Entity(); !ok {
		return Event{}, errs.New(errs.CategoryPolicy, ReasonLegalValidationRequired,
			"The recipient legal entity is not resolved. No answer is approved until legal resolves it (SUP-08).")
	}
	e, err := n.next(EventResponseApproved, StatusResponseApproved, by, at)
	if err != nil {
		return Event{}, err
	}
	e.ResponseVersion, e.ResponseSHA256, e.Note = prep.ResponseVersion, prep.ResponseSHA256, strings.TrimSpace(note)
	return e, nil
}

// RecordSubmission records that the approved response reached the
// authority. It needs the authority's submission receipt.
func (n Notice) RecordSubmission(receipt, evidence, note string, by id.UserID, at time.Time) (Event, error) {
	if n.Status != StatusResponseApproved {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only an approved response is submitted; this notice is %s.", n.Status))
	}
	if err := required("authorityReceiptReference", receipt, "A submission is recorded with the authority's receipt for it."); err != nil {
		return Event{}, err
	}
	for _, c := range []struct {
		f, v string
		n    int
	}{{"authorityReceiptReference", receipt, 200}, {"evidence", evidence, 20000}, {"note", note, 2000}} {
		if err := tooLong(c.f, c.v, c.n); err != nil {
			return Event{}, err
		}
	}
	appr := n.Approval()
	e, err := n.next(EventSubmitted, StatusSubmitted, by, at)
	if err != nil {
		return Event{}, err
	}
	e.AuthorityRef, e.Evidence, e.Note = strings.TrimSpace(receipt), strings.TrimSpace(evidence), strings.TrimSpace(note)
	if appr != nil {
		e.ResponseVersion, e.ResponseSHA256 = appr.ResponseVersion, appr.ResponseSHA256
	}
	return e, nil
}

// OutcomeRecord is what the authority decided.
type OutcomeRecord struct {
	Outcome      Outcome
	AuthorityRef string
	Evidence     string
	Note         string
	// Amount is the figure the authority decided, as stated.
	Amount *fiscal.Money
	// Settled claims the matter is paid or otherwise discharged, on the
	// evidence Settlement names.
	Settled    bool
	Settlement Settlement
}

// RecordOutcome records the authority's decision on a submitted response or
// an appeal. It needs the authority's receipt or decision reference, and a
// claim of settlement needs settlement evidence — a pending payment
// instruction is not one (T18-20).
func (n Notice) RecordOutcome(r OutcomeRecord, by id.UserID, at time.Time) (Event, error) {
	if !r.Outcome.Valid() {
		return Event{}, errs.Invalid("outcome", errs.ReasonInvalidValue,
			fmt.Sprintf("%q is not an outcome; ACCEPTED, ADJUSTED, REJECTED or WITHDRAWN.", r.Outcome))
	}
	if n.Status != StatusSubmitted && n.Status != StatusAppeal {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("An outcome answers a submitted response or an appeal; this notice is %s.", n.Status))
	}
	if err := required("authorityReceiptReference", r.AuthorityRef, "An outcome is recorded with the authority's receipt or decision reference."); err != nil {
		return Event{}, err
	}
	if err := required("evidence", r.Evidence, "The authority's decision is recorded as received."); err != nil {
		return Event{}, err
	}
	for _, c := range []struct {
		f, v string
		n    int
	}{{"authorityReceiptReference", r.AuthorityRef, 200}, {"evidence", r.Evidence, 20000}, {"note", r.Note, 2000}} {
		if err := tooLong(c.f, c.v, c.n); err != nil {
			return Event{}, err
		}
	}
	if r.Settlement != "" && !r.Settlement.Valid() {
		return Event{}, errs.Invalid("settlement", errs.ReasonInvalidValue, fmt.Sprintf("%q is not settlement evidence this service knows.", r.Settlement))
	}
	if r.Settled && r.Settlement != SettlementAuthorityConfirmed && r.Settlement != SettlementPaymentSettled {
		return Event{}, errs.New(errs.CategoryPolicy, ReasonSettlementUnproven,
			"A notice is recorded as settled on the authority's confirmation or a settled payment; a pending payment instruction settles nothing (T18-20).")
	}
	e, err := n.next(EventOutcome, StatusOutcomeRecorded, by, at)
	if err != nil {
		return Event{}, err
	}
	e.Outcome, e.AuthorityRef, e.Evidence, e.Note = r.Outcome, strings.TrimSpace(r.AuthorityRef), strings.TrimSpace(r.Evidence), strings.TrimSpace(r.Note)
	e.OutcomeAmount, e.Settled, e.Settlement = r.Amount, r.Settled, r.Settlement
	return e, nil
}

// AppealRecord is an appeal against a recorded outcome.
type AppealRecord struct {
	// Next is the deadline the appeal runs to; Kind is set to APPEAL.
	Next         Deadline
	Hearing      time.Time
	Evidence     string
	AuthorityRef string
	Note         string
}

// LodgeAppeal opens the appeal branch after an outcome. An appeal with a
// hearing still to come keeps the matter open with a next deadline (SUP-16).
// The appeal's deadline is a new ledger entry; the original and every
// extension stay where they were.
func (n Notice) LodgeAppeal(r AppealRecord, by id.UserID, at time.Time) (Event, error) {
	if n.Status != StatusOutcomeRecorded {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("An appeal is lodged against a recorded outcome; this notice is %s.", n.Status))
	}
	if err := required("evidence", r.Evidence, "An appeal is recorded with the evidence it was lodged on."); err != nil {
		return Event{}, err
	}
	for _, c := range []struct {
		f, v string
		n    int
	}{{"authorityReference", r.AuthorityRef, 200}, {"evidence", r.Evidence, 20000}, {"note", r.Note, 2000}} {
		if err := tooLong(c.f, c.v, c.n); err != nil {
			return Event{}, err
		}
	}
	if !r.Hearing.IsZero() && r.Hearing.After(at) && !r.Next.Known() {
		return Event{}, errs.Invalid("nextDeadline.dueDate", errs.ReasonMissingField,
			"An appeal with a hearing to come keeps the matter open with its next deadline (SUP-16).")
	}
	r.Next.Kind = DeadlineAppeal
	if err := validateDeadline("nextDeadline", r.Next, r.Evidence); err != nil {
		return Event{}, err
	}
	e, err := n.next(EventAppeal, StatusAppeal, by, at)
	if err != nil {
		return Event{}, err
	}
	d := r.Next
	e.Deadline, e.HearingDate = &d, r.Hearing
	e.Evidence, e.AuthorityRef, e.Note = strings.TrimSpace(r.Evidence), strings.TrimSpace(r.AuthorityRef), strings.TrimSpace(r.Note)
	return e, nil
}

// ---------------------------------------------------------------------------
// flags
// ---------------------------------------------------------------------------

// Flag is something about a notice that needs attention now.
type Flag string

// The flags.
const (
	FlagDeadlineUnknown      Flag = "DEADLINE_UNKNOWN"
	FlagDeadlineUnverified   Flag = "DEADLINE_UNVERIFIED"
	FlagAuthenticityUnknown  Flag = "AUTHENTICITY_UNVERIFIED"
	FlagAuthenticityDisputed Flag = "AUTHENTICITY_DISPUTED"
	FlagEntityUnresolved     Flag = "ENTITY_UNRESOLVED"
	FlagDeadlineSoon         Flag = "DEADLINE_SOON"
	FlagStatutoryOverdue     Flag = "STATUTORY_OVERDUE"
	FlagSLABreached          Flag = "SLA_BREACHED"
	FlagLegalEscalated       Flag = "LEGAL_ESCALATED"
	FlagUrgentLegalReview    Flag = "URGENT_LEGAL_REVIEW"
	FlagHearingScheduled     Flag = "HEARING_SCHEDULED"
)

// soon is how close a statutory deadline is before it is flagged.
const soon = 7 * 24 * time.Hour

// FlagsAt reports what needs attention as of asOf, in a fixed order. The
// statutory and SLA breaches are separate facts: missing our own target is not
// missing the authority's. An unknown or unverified deadline, unresolved
// authenticity or an unresolved recipient is an urgent legal review (W17,
// SUP-05, SUP-08).
func (n Notice) FlagsAt(asOf time.Time) []Flag {
	if n.Status == StatusClosed {
		return nil
	}
	var out []Flag
	urgent := false
	cur := n.Current()
	pending := !n.Status.Answered() || n.Status == StatusAppeal
	if pending {
		switch {
		case !cur.Known():
			out, urgent = append(out, FlagDeadlineUnknown), true
		case !cur.Verified:
			out, urgent = append(out, FlagDeadlineUnverified), true
		}
	}
	switch n.Authenticity() {
	case AuthenticityUnknown:
		out = append(out, FlagAuthenticityUnknown)
		urgent = urgent || n.Status != StatusReceived && n.Status != StatusAuthenticityReview
	case AuthenticityUnverifiable, AuthenticityNotAuthentic:
		out, urgent = append(out, FlagAuthenticityDisputed), true
	}
	if _, ok := n.Entity(); !ok {
		out, urgent = append(out, FlagEntityUnresolved), true
	}
	if pending && cur.Known() {
		due := cur.DueAt()
		switch {
		case asOf.After(due):
			out = append(out, FlagStatutoryOverdue)
		case due.Sub(asOf) <= soon:
			out = append(out, FlagDeadlineSoon)
		}
	}
	if _, sla := n.Owner(); !sla.IsZero() && asOf.After(sla) && !n.Status.Answered() {
		out = append(out, FlagSLABreached)
	}
	if n.Escalated() {
		out = append(out, FlagLegalEscalated)
	}
	if h := n.Hearing(); !h.IsZero() && h.After(asOf) {
		out = append(out, FlagHearingScheduled)
	}
	if urgent {
		out = append(out, FlagUrgentLegalReview)
	}
	return out
}

// ---------------------------------------------------------------------------
// actions
// ---------------------------------------------------------------------------

// Action is something a person may do to a notice in its current status. The
// list is the state machine's answer only; each act still applies its own
// guards — four-eye, authenticity, recipient, evidence — when it is taken.
type Action string

// The actions.
const (
	ActionStartAuthenticityReview Action = "START_AUTHENTICITY_REVIEW"
	ActionMoveToLegalReview       Action = "MOVE_TO_LEGAL_REVIEW"
	ActionRecordAuthenticity      Action = "RECORD_AUTHENTICITY"
	ActionEscalate                Action = "ESCALATE"
	ActionResolveEntity           Action = "RESOLVE_ENTITY"
	ActionAssignOwner             Action = "ASSIGN_OWNER"
	ActionRecordAttachment        Action = "RECORD_ATTACHMENT"
	ActionRecordDeadline          Action = "RECORD_DEADLINE"
	ActionPrepareResponse         Action = "PREPARE_RESPONSE"
	ActionApproveResponse         Action = "APPROVE_RESPONSE"
	ActionRecordSubmission        Action = "RECORD_SUBMISSION"
	ActionRecordOutcome           Action = "RECORD_OUTCOME"
	ActionLodgeAppeal             Action = "LODGE_APPEAL"
	ActionClose                   Action = "CLOSE"
)

// AllowedActions is what the notice's status admits, in a fixed order.
func (n Notice) AllowedActions() []Action {
	if n.Status == StatusClosed {
		return nil
	}
	var out []Action
	switch n.Status {
	case StatusReceived:
		out = append(out, ActionStartAuthenticityReview)
	case StatusAuthenticityReview, StatusAppeal:
		out = append(out, ActionMoveToLegalReview)
	case StatusLegalReview:
		out = append(out, ActionPrepareResponse)
		if n.Response() != nil {
			out = append(out, ActionApproveResponse)
		}
	case StatusResponseApproved:
		out = append(out, ActionRecordSubmission, ActionPrepareResponse, ActionMoveToLegalReview)
	}
	if n.Status == StatusSubmitted || n.Status == StatusAppeal {
		out = append(out, ActionRecordOutcome)
	}
	if n.Status == StatusOutcomeRecorded {
		out = append(out, ActionLodgeAppeal)
	}
	if !n.Status.Answered() {
		out = append(out, ActionRecordAuthenticity, ActionResolveEntity, ActionRecordDeadline)
	}
	out = append(out, ActionEscalate, ActionAssignOwner, ActionRecordAttachment)
	if n.Status == StatusLegalReview || n.Status == StatusOutcomeRecorded {
		out = append(out, ActionClose)
	}
	return out
}
