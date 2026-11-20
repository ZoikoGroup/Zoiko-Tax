// Package entitlement is the tenant entitlement register (Engineering Doc 18
// §01, §02, §05 TenantGrant, §07; G-004; W06, W07).
//
// An entitlement is an approved, time-bounded contract right for one of the
// tenant's legal entities to invoke one operation, in one operating mode, for
// one service family in one jurisdiction. Doc 18 keeps three things apart and
// so does this package:
//
//   - A grant is not its approval. A grant is recorded as a DRAFT, submitted
//     for review, and APPROVED only by a second administrator — never the
//     person who recorded or submitted it (W07 "same user submitting and
//     approving → 403"). A partial approval has no effect (T18-07).
//   - An approval is not coverage. "A commercial SKU alone never confers legal
//     or technical permission": an ACTIVE grant is ALLOW only when the cell's
//     verified market coverage proves the operation — content in force for the
//     service, an authoritative cell to commit, a ready filing gate to file
//     (Doc 18 §02 ALLOW = contract ∧ product ∧ legal pack ∧ runtime …). An
//     active grant over unproven coverage is RESTRICTED and says why; it
//     never makes a research profile live (T18-02, COV-01).
//   - Coverage is not edited here. Nothing in this package changes content,
//     a decision or a rule; it reads the coverage the cell already reports.
//
// Nothing is edited. A grant's lifecycle is the fold of its appended events;
// a changed scope is a new grant, and a revoked or expired one stays visible
// as recorded. Suspension trumps activation and is lifted only by a resume a
// second person approves (Doc 18 §07).
package entitlement

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ontology"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The Doc 18 reason codes this package raises.
var (
	// ReasonOverlap is an approval that would leave two approved grants for
	// the same scope in force at once (T18-06, COV-10).
	ReasonOverlap = errs.Register("ENTITLEMENT_OVERLAP", errs.CategoryConflict,
		"g-004",
		"Another approved grant already covers this legal entity, jurisdiction, service, mode and operation for part of these dates. Overlapping grants never widen scope: revoke or let the other one end, or record a grant for the dates it does not cover.")

	// ReasonVersionConflict is the estate's stale-version code.
	ReasonVersionConflict = registration.ReasonVersionConflict
	// ReasonMakerChecker is the estate's maker-checker code.
	ReasonMakerChecker = registration.ReasonMakerChecker
)

// Bounds on the free-text fields.
const (
	MaxContractRef = 128
	MaxReason      = 2000
)

var jurisdictionShape = regexp.MustCompile(`^[A-Z]{2}(-[A-Z0-9]{1,3})?$`)

// Mode is the operating mode a grant is for. Doc 18 §02: five modes, each a
// separate entitlement; none implies another.
type Mode string

// The modes.
const (
	ModeNative            Mode = "NATIVE"
	ModeFederated         Mode = "FEDERATED"
	ModeShadowAssurance   Mode = "SHADOW_ASSURANCE"
	ModeOEMEmbedded       Mode = "OEM_EMBEDDED"
	ModeManagedCompliance Mode = "MANAGED_COMPLIANCE"
)

// Modes is every mode, in the order a form offers them.
var Modes = []Mode{ModeNative, ModeFederated, ModeShadowAssurance, ModeOEMEmbedded, ModeManagedCompliance}

// Valid reports whether the mode is one of these.
func (m Mode) Valid() bool { return slices.Contains(Modes, m) }

// Operation is what a grant permits. Each is a separate right: a quote right
// is not a commit right, and a calculation right is not a filing right
// (COV-05, T18-05).
type Operation string

// The operations.
const (
	OpQuote         Operation = "QUOTE"
	OpCommit        Operation = "COMMIT"
	OpFile          Operation = "FILE"
	OpRemit         Operation = "REMIT"
	OpEInvoice      Operation = "E_INVOICE"
	OpShadowCompare Operation = "SHADOW_COMPARE"
)

// Operations is every operation, in the order a form offers them.
var Operations = []Operation{OpQuote, OpCommit, OpFile, OpRemit, OpEInvoice, OpShadowCompare}

// Valid reports whether the operation is one of these.
func (o Operation) Valid() bool { return slices.Contains(Operations, o) }

// modeOperations are the operations a mode can carry at all (Doc 18 §02
// "never implied"): shadow assurance is copy-only comparison and never
// affects an invoice, a return or a payment (COM-02); managed compliance is
// the human-assisted filing and payment workflow; federated and OEM rights
// are calculation rights.
var modeOperations = map[Mode][]Operation{
	ModeNative:            {OpQuote, OpCommit, OpFile, OpRemit, OpEInvoice},
	ModeFederated:         {OpQuote, OpCommit},
	ModeShadowAssurance:   {OpShadowCompare},
	ModeOEMEmbedded:       {OpQuote, OpCommit},
	ModeManagedCompliance: {OpFile, OpRemit},
}

// OperationsFor returns the operations a mode can carry.
func OperationsFor(m Mode) []Operation { return slices.Clone(modeOperations[m]) }

// Lifecycle is the stored state of a grant (Doc 18 §07: DRAFT → IN_REVIEW →
// APPROVED → … → SUSPENDED/EXPIRED). SCHEDULED, ACTIVE and EXPIRED are not
// stored: they are an APPROVED grant read against its dates (see Status).
type Lifecycle string

// The lifecycle states.
const (
	LifecycleDraft     Lifecycle = "DRAFT"
	LifecycleInReview  Lifecycle = "IN_REVIEW"
	LifecycleApproved  Lifecycle = "APPROVED"
	LifecycleRejected  Lifecycle = "REJECTED"
	LifecycleWithdrawn Lifecycle = "WITHDRAWN"
	LifecycleSuspended Lifecycle = "SUSPENDED"
	LifecycleRevoked   Lifecycle = "REVOKED"
)

// Closed reports whether nothing more happens to a grant in this state.
func (l Lifecycle) Closed() bool {
	return l == LifecycleRejected || l == LifecycleWithdrawn || l == LifecycleRevoked
}

// Status is a grant read as of a date.
type Status string

// The statuses a grant reads as.
const (
	StatusDraft     Status = "DRAFT"
	StatusInReview  Status = "IN_REVIEW"
	StatusScheduled Status = "SCHEDULED"
	StatusActive    Status = "ACTIVE"
	StatusSuspended Status = "SUSPENDED"
	StatusExpired   Status = "EXPIRED"
	StatusRejected  Status = "REJECTED"
	StatusWithdrawn Status = "WITHDRAWN"
	StatusRevoked   Status = "REVOKED"
)

// Statuses is every status.
var Statuses = []Status{StatusDraft, StatusInReview, StatusScheduled, StatusActive, StatusSuspended, StatusExpired,
	StatusRejected, StatusWithdrawn, StatusRevoked}

// Valid reports whether the status is one of these.
func (s Status) Valid() bool { return slices.Contains(Statuses, s) }

// Action is what a person does to a grant.
type Action string

// The actions.
const (
	ActSubmit        Action = "SUBMIT"
	ActApprove       Action = "APPROVE"
	ActReject        Action = "REJECT"
	ActWithdraw      Action = "WITHDRAW"
	ActSuspend       Action = "SUSPEND"
	ActRequestResume Action = "REQUEST_RESUME"
	ActResume        Action = "RESUME"
	ActRevoke        Action = "REVOKE"
)

// Actions is every action.
var Actions = []Action{ActSubmit, ActApprove, ActReject, ActWithdraw, ActSuspend, ActRequestResume, ActResume, ActRevoke}

// Valid reports whether the action is one of these.
func (a Action) Valid() bool { return slices.Contains(Actions, a) }

// EventKind is what happened to a grant.
type EventKind string

// The events. CREATED is always the first.
const (
	EventCreated         EventKind = "CREATED"
	EventSubmitted       EventKind = "SUBMITTED"
	EventApproved        EventKind = "APPROVED"
	EventRejected        EventKind = "REJECTED"
	EventWithdrawn       EventKind = "WITHDRAWN"
	EventSuspended       EventKind = "SUSPENDED"
	EventResumeRequested EventKind = "RESUME_REQUESTED"
	EventResumed         EventKind = "RESUMED"
	EventRevoked         EventKind = "REVOKED"
)

// Event is one appended act on a grant.
type Event struct {
	Seq        int
	Kind       EventKind
	Lifecycle  Lifecycle
	Reason     string
	RecordedAt time.Time
	RecordedBy id.UserID
}

// Grant is one entitlement grant, folded with its events.
type Grant struct {
	ID          id.EntitlementID
	TenantID    id.TenantID
	LegalEntity id.LegalEntityID

	// Jurisdiction is a country, or a country and one subdivision: GB,
	// US-NY. Country is its first two letters.
	Jurisdiction string
	Service      ontology.Family
	Mode         Mode
	Operation    Operation

	// EffectiveFrom and EffectiveTo are the contract dates, civil UTC dates.
	// EffectiveTo is zero when open-ended; the grant is in force on it.
	EffectiveFrom time.Time
	EffectiveTo   time.Time

	// ContractRef and ContractRevision name the signed contract schedule
	// the right comes from, as the customer's contract states it.
	ContractRef      string
	ContractRevision string

	IntakeKey  string
	IntakeHash canonical.Digest

	CreatedAt time.Time
	CreatedBy id.UserID

	Lifecycle Lifecycle
	History   []Event
}

// Country is the grant's country.
func (g Grant) Country() string {
	c, _, _ := strings.Cut(g.Jurisdiction, "-")
	return c
}

// Version is the number of events recorded. An approval names it.
func (g Grant) Version() int { return len(g.History) }

// Day is the civil UTC date of an instant, as a midnight.
func Day(t time.Time) time.Time { return registration.Day(t) }

// Validate refuses a grant that cannot be recorded. It normalizes nothing;
// the caller has.
func (g Grant) Validate() error {
	switch {
	case g.LegalEntity.IsZero():
		return errs.Invalid("legalEntityId", errs.ReasonMissingField, "A grant belongs to one of the tenant's legal entities.")
	case !jurisdictionShape.MatchString(g.Jurisdiction):
		return errs.Invalid("jurisdiction", errs.ReasonInvalidValue,
			"A jurisdiction is a country, or a country and one subdivision: GB, US, US-NY. There is no all-countries grant.")
	case !g.Service.Valid():
		return errs.Invalid("service", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a service family.", g.Service))
	case !g.Mode.Valid():
		return errs.Invalid("mode", errs.ReasonInvalidValue, fmt.Sprintf("%q is not an operating mode.", g.Mode))
	case !g.Operation.Valid():
		return errs.Invalid("operation", errs.ReasonInvalidValue, fmt.Sprintf("%q is not an operation.", g.Operation))
	case !slices.Contains(modeOperations[g.Mode], g.Operation):
		return errs.Invalid("operation", errs.ReasonInvalidValue,
			fmt.Sprintf("A %s grant does not carry %s. Each mode is a separate right and none implies another (Doc 18 §02).", g.Mode, g.Operation))
	case g.EffectiveFrom.IsZero():
		return errs.Invalid("effectiveFrom", errs.ReasonMissingField, "A grant states the date it is effective from.")
	case !g.EffectiveTo.IsZero() && g.EffectiveTo.Before(g.EffectiveFrom):
		return errs.Invalid("effectiveTo", errs.ReasonInvalidValue, "A grant ends on or after the date it starts.")
	case strings.TrimSpace(g.ContractRef) == "":
		return errs.Invalid("contractRef", errs.ReasonMissingField,
			"A grant names the signed contract it comes from. A feature flag or a SKU alone is not a contract right.")
	case len(g.ContractRef) > MaxContractRef || g.ContractRef != strings.TrimSpace(g.ContractRef):
		return errs.Invalid("contractRef", errs.ReasonInvalidValue, fmt.Sprintf("A contract reference is 1 to %d characters with no leading or trailing space.", MaxContractRef))
	case len(g.ContractRevision) > MaxContractRef || g.ContractRevision != strings.TrimSpace(g.ContractRevision):
		return errs.Invalid("contractRevision", errs.ReasonInvalidValue, fmt.Sprintf("A contract revision is at most %d characters.", MaxContractRef))
	}
	return nil
}

// Payload is the canonical document the intake hash covers: what was asked
// for, not who asked or when.
func (g Grant) Payload(submit bool) canonical.Value {
	to := canonical.Absent()
	if !g.EffectiveTo.IsZero() {
		to = canonical.String(g.EffectiveTo.Format(time.DateOnly))
	}
	rev := canonical.Absent()
	if g.ContractRevision != "" {
		rev = canonical.String(g.ContractRevision)
	}
	return canonical.Object(
		canonical.F("legalEntityId", canonical.String(g.LegalEntity.String())),
		canonical.F("jurisdiction", canonical.String(g.Jurisdiction)),
		canonical.F("service", canonical.String(string(g.Service))),
		canonical.F("mode", canonical.String(string(g.Mode))),
		canonical.F("operation", canonical.String(string(g.Operation))),
		canonical.F("effectiveFrom", canonical.String(g.EffectiveFrom.Format(time.DateOnly))),
		canonical.F("effectiveTo", to),
		canonical.F("contractRef", canonical.String(g.ContractRef)),
		canonical.F("contractRevision", rev),
		canonical.F("submit", canonical.Bool(submit)),
	)
}

// Open is a new grant's first events: CREATED as a DRAFT, and SUBMITTED
// when asked. A grant is never created APPROVED. Its start is never before
// the day it is recorded: an entitlement is not backdated (Doc 18 §07
// "Temporal integrity").
func Open(g Grant, submit bool, by id.UserID, at time.Time) ([]Event, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	if g.EffectiveFrom.Before(Day(at)) {
		return nil, errs.Invalid("effectiveFrom", errs.ReasonInvalidValue,
			fmt.Sprintf("A grant is never backdated: it starts on or after today, %s. A contract that reaches back is a reassessment, not an entitlement.", Day(at).Format(time.DateOnly)))
	}
	if !g.EffectiveTo.IsZero() && g.EffectiveTo.Before(Day(at)) {
		return nil, errs.Invalid("effectiveTo", errs.ReasonInvalidValue, "This grant would already have ended.")
	}
	events := []Event{{Seq: 1, Kind: EventCreated, Lifecycle: LifecycleDraft, RecordedAt: at.UTC(), RecordedBy: by}}
	if submit {
		events = append(events, Event{Seq: 2, Kind: EventSubmitted, Lifecycle: LifecycleInReview, RecordedAt: at.UTC(), RecordedBy: by})
	}
	return events, nil
}

// Fold applies a grant's events in order.
func Fold(g Grant, events []Event) (Grant, error) {
	if len(events) == 0 || events[0].Kind != EventCreated || events[0].Seq != 1 || events[0].Lifecycle != LifecycleDraft {
		return Grant{}, fmt.Errorf("entitlement: a grant's history starts with its CREATED event")
	}
	g.Lifecycle = LifecycleDraft
	for i, e := range events {
		if e.Seq != i+1 {
			return Grant{}, fmt.Errorf("entitlement: event %d is out of sequence", e.Seq)
		}
		if i > 0 {
			want, ok := moves[move{g.Lifecycle, e.Kind}]
			if !ok || want != e.Lifecycle {
				return Grant{}, fmt.Errorf("entitlement: %s from %s to %s", e.Kind, g.Lifecycle, e.Lifecycle)
			}
		}
		g.Lifecycle = e.Lifecycle
	}
	g.History = slices.Clone(events)
	return g, nil
}

type move struct {
	from Lifecycle
	kind EventKind
}

// moves is the lifecycle: where each event may happen and where it leaves
// the grant.
var moves = map[move]Lifecycle{
	{LifecycleDraft, EventSubmitted}:           LifecycleInReview,
	{LifecycleDraft, EventWithdrawn}:           LifecycleWithdrawn,
	{LifecycleInReview, EventApproved}:         LifecycleApproved,
	{LifecycleInReview, EventRejected}:         LifecycleRejected,
	{LifecycleInReview, EventWithdrawn}:        LifecycleWithdrawn,
	{LifecycleApproved, EventSuspended}:        LifecycleSuspended,
	{LifecycleApproved, EventRevoked}:          LifecycleRevoked,
	{LifecycleSuspended, EventResumeRequested}: LifecycleSuspended,
	{LifecycleSuspended, EventResumed}:         LifecycleApproved,
	{LifecycleSuspended, EventRevoked}:         LifecycleRevoked,
}

var actionKinds = map[Action]EventKind{
	ActSubmit: EventSubmitted, ActApprove: EventApproved, ActReject: EventRejected, ActWithdraw: EventWithdrawn,
	ActSuspend: EventSuspended, ActRequestResume: EventResumeRequested, ActResume: EventResumed, ActRevoke: EventRevoked,
}

// last returns the most recent event of one of the kinds, and false when
// there is none.
func (g Grant) last(kinds ...EventKind) (Event, bool) {
	for i := len(g.History) - 1; i >= 0; i-- {
		if slices.Contains(kinds, g.History[i].Kind) {
			return g.History[i], true
		}
	}
	return Event{}, false
}

// Submitter is the person who submitted the grant for review.
func (g Grant) Submitter() (id.UserID, bool) {
	e, ok := g.last(EventSubmitted)
	return e.RecordedBy, ok
}

// Approval is the approving event, and false while the grant has none.
func (g Grant) Approval() (Event, bool) { return g.last(EventApproved) }

// ResumePending is the outstanding resume request of a suspended grant,
// and false when there is none.
func (g Grant) ResumePending() (Event, bool) {
	if g.Lifecycle != LifecycleSuspended {
		return Event{}, false
	}
	e, ok := g.last(EventResumeRequested, EventSuspended)
	return e, ok && e.Kind == EventResumeRequested
}

// ActiveFrom is the first day the approved grant gives its right: its
// effective date, or the day it was approved when that is later. An approval
// never reaches back (Doc 18 W07 "never backdate"). Zero before approval.
func (g Grant) ActiveFrom() time.Time {
	a, ok := g.Approval()
	if !ok {
		return time.Time{}
	}
	if d := Day(a.RecordedAt); d.After(g.EffectiveFrom) {
		return d
	}
	return g.EffectiveFrom
}

// StatusOn is the grant read on a day. Suspension trumps activation, and a
// grant past its end date is EXPIRED whatever else is true of it.
func (g Grant) StatusOn(day time.Time) Status {
	ended := !g.EffectiveTo.IsZero() && day.After(g.EffectiveTo)
	switch g.Lifecycle {
	case LifecycleDraft:
		return StatusDraft
	case LifecycleInReview:
		return StatusInReview
	case LifecycleRejected:
		return StatusRejected
	case LifecycleWithdrawn:
		return StatusWithdrawn
	case LifecycleRevoked:
		return StatusRevoked
	case LifecycleSuspended:
		if ended {
			return StatusExpired
		}
		return StatusSuspended
	}
	switch {
	case ended:
		return StatusExpired
	case g.ActiveFrom().After(day):
		return StatusScheduled
	}
	return StatusActive
}

// Window is the span of days a grant could be in force: [from, to], to
// zero for open-ended. Used to refuse overlapping approvals.
func (g Grant) window() (time.Time, time.Time) { return g.EffectiveFrom, g.EffectiveTo }

// SameScope reports whether two grants are for the same legal entity,
// jurisdiction, service, mode and operation.
func (g Grant) SameScope(o Grant) bool {
	return g.LegalEntity == o.LegalEntity && g.Jurisdiction == o.Jurisdiction && g.Service == o.Service &&
		g.Mode == o.Mode && g.Operation == o.Operation
}

// Overlaps reports whether two grants' date windows share a day.
func (g Grant) Overlaps(o Grant) bool {
	af, at := g.window()
	bf, bt := o.window()
	return (at.IsZero() || !bf.After(at)) && (bt.IsZero() || !af.After(bt))
}

// Act is what a person asks of a grant.
type Act struct {
	Action          Action
	ExpectedVersion int
	Reason          string
}

// Apply returns the event a person's act appends, or why it is refused.
// others are the tenant's other grants, for the overlap rule.
//
//   - SUBMIT from DRAFT; WITHDRAW from DRAFT or IN_REVIEW.
//   - APPROVE from IN_REVIEW: by someone other than the person who recorded
//     or submitted it (403 MAKER_CHECKER), on the version read (409
//     VERSION_CONFLICT), not after its end date, and not overlapping another
//     approved grant of the same scope (409 ENTITLEMENT_OVERLAP).
//   - REJECT from IN_REVIEW.
//   - SUSPEND from APPROVED — an emergency stop any administrator may make.
//   - REQUEST_RESUME from SUSPENDED; RESUME only on a pending request, by
//     someone other than the requester and the suspender.
//   - REVOKE from APPROVED or SUSPENDED: the grant ends for good.
//
// Every act but SUBMIT gives its reason.
func (g Grant) Apply(a Act, others []Grant, by id.UserID, at time.Time) (Event, error) {
	reason := strings.TrimSpace(a.Reason)
	if !a.Action.Valid() {
		return Event{}, errs.Invalid("action", errs.ReasonInvalidValue, fmt.Sprintf("%q is not an entitlement action.", a.Action))
	}
	if a.ExpectedVersion != g.Version() {
		return Event{}, errs.New(errs.CategoryConflict, ReasonVersionConflict,
			fmt.Sprintf("The grant is at version %d; version %d was read. Re-read it and act on the current version.", g.Version(), a.ExpectedVersion))
	}
	kind := actionKinds[a.Action]
	to, ok := moves[move{g.Lifecycle, kind}]
	if !ok {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A %s grant cannot be %s.", g.Lifecycle, strings.ToLower(strings.ReplaceAll(string(a.Action), "_", " "))))
	}
	if a.Action != ActSubmit {
		if reason == "" {
			return Event{}, errs.Invalid("reason", errs.ReasonMissingField, "This act gives its reason.")
		}
	}
	if len(reason) > MaxReason {
		return Event{}, errs.Invalid("reason", errs.ReasonInvalidValue, fmt.Sprintf("A reason is at most %d characters.", MaxReason))
	}
	day := Day(at)
	switch a.Action {
	case ActApprove:
		if g.CreatedBy == by {
			return Event{}, makerChecker("The person who recorded this grant cannot also approve it. A second administrator must.")
		}
		if s, ok := g.Submitter(); ok && s == by {
			return Event{}, makerChecker("The person who submitted this grant cannot also approve it. A second administrator must.")
		}
		if !g.EffectiveTo.IsZero() && day.After(g.EffectiveTo) {
			return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				fmt.Sprintf("The grant ended on %s. Record a new grant for the dates the contract now covers.", g.EffectiveTo.Format(time.DateOnly)))
		}
		for _, o := range others {
			if o.ID == g.ID || !g.SameScope(o) {
				continue
			}
			if o.Lifecycle != LifecycleApproved && o.Lifecycle != LifecycleSuspended {
				continue
			}
			if o.StatusOn(day) == StatusExpired {
				continue
			}
			if g.Overlaps(o) {
				return Event{}, errs.New(errs.CategoryConflict, ReasonOverlap,
					fmt.Sprintf("Grant %s already covers this scope for part of these dates.", o.ID))
			}
		}
	case ActResume:
		req, ok := g.ResumePending()
		if !ok {
			return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				"A suspended grant is resumed only on a recorded resume request, approved by a second person.")
		}
		if req.RecordedBy == by {
			return Event{}, makerChecker("The person who requested the resume cannot also approve it. A second administrator must.")
		}
		if s, ok := g.last(EventSuspended); ok && s.RecordedBy == by {
			return Event{}, makerChecker("The person who suspended this grant cannot also resume it. A second administrator must.")
		}
	case ActRequestResume:
		if _, ok := g.ResumePending(); ok {
			return Event{}, errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "A resume is already requested and awaits a second administrator.")
		}
		if !g.EffectiveTo.IsZero() && day.After(g.EffectiveTo) {
			return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid, "The grant has ended; it is not resumed.")
		}
	}
	return Event{Seq: g.Version() + 1, Kind: kind, Lifecycle: to, Reason: reason, RecordedAt: at.UTC(), RecordedBy: by}, nil
}

// Offers is what a person may ask of a grant now, as a screen offers it.
// The server decides; this only keeps the screen from offering what the
// lifecycle refuses.
func (g Grant) Offers() []Action {
	var out []Action
	for _, a := range Actions {
		if _, ok := moves[move{g.Lifecycle, actionKinds[a]}]; !ok {
			continue
		}
		switch a {
		case ActResume:
			if _, ok := g.ResumePending(); !ok {
				continue
			}
		case ActRequestResume:
			if _, ok := g.ResumePending(); ok {
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

// Filter narrows a listing. Empty fields do not filter.
type Filter struct {
	Status      Status
	LegalEntity id.LegalEntityID
	Country     string
	Mode        Mode
	Operation   Operation
}

// Match reports whether a grant passes the filter on day.
func (f Filter) Match(g Grant, day time.Time) bool {
	return (f.Status == "" || g.StatusOn(day) == f.Status) && (f.LegalEntity.IsZero() || g.LegalEntity == f.LegalEntity) &&
		(f.Country == "" || g.Country() == f.Country) && (f.Mode == "" || g.Mode == f.Mode) &&
		(f.Operation == "" || g.Operation == f.Operation)
}

func makerChecker(detail string) error {
	return errs.New(errs.CategoryPolicy, ReasonMakerChecker, detail)
}
