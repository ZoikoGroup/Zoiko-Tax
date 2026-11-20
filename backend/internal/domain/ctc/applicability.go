package ctc

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/document"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Country applicability (Doc 21 W01 Global CTC Coverage & Applicability,
// W20 Country Pack Certification & Activation, §11 release gates).
//
// A record is one cell — legal entity × country × document type × buyer
// kind — and the versioned terms that say what that cell owes: the duty, the
// control model, the format profile and version, the authority, the legal
// source and its as-of date, the effective dates and the eligibility
// conditions. Terms are never edited: a change is the next version, and a
// sign-off is bound to the version it was given on.

// Status is where a record stands. DRAFT and IN_REVIEW are Doc 21's
// NOT_ACTIVATED; only ACTIVE decides a duty.
type Status string

// The statuses.
const (
	StatusDraft     Status = "DRAFT"
	StatusInReview  Status = "IN_REVIEW"
	StatusActive    Status = "ACTIVE"
	StatusSuspended Status = "SUSPENDED"
	StatusRetired   Status = "RETIRED"
)

// Statuses is every status.
var Statuses = []Status{StatusDraft, StatusInReview, StatusActive, StatusSuspended, StatusRetired}

// Valid reports whether the status is one of these.
func (s Status) Valid() bool { return slices.Contains(Statuses, s) }

// Held reports whether a record in this status holds its cell: an active or
// suspended record, which no other record may overlap.
func (s Status) Held() bool { return s == StatusActive || s == StatusSuspended }

// Gate is one of Doc 21 §11's release gates.
type Gate string

// The gates.
const (
	GateScope      Gate = "G0_SCOPE_AUTHORITY"
	GateLegal      Gate = "G1_LEGAL_CONTENT_RIGHTS"
	GateTechnical  Gate = "G2_TECHNICAL_QA"
	GateSecurity   Gate = "G3_SECURITY_OPERATIONS"
	GateProduction Gate = "G4_CONTROLLED_PRODUCTION"
)

// Gates is every gate, in order.
var Gates = []Gate{GateScope, GateLegal, GateTechnical, GateSecurity, GateProduction}

// Valid reports whether the gate is one of these.
func (g Gate) Valid() bool { return slices.Contains(Gates, g) }

// RequiredGates are the gates a record with duty d needs signed before it
// is activated. Every record needs its scope (G0) and its legal
// interpretation and source rights (G1). A record under which documents are
// dispatched also needs the adapter's QA (G2), security and operations (G3)
// and controlled production (G4); a PROHIBITED record dispatches nothing.
func RequiredGates(d Duty) []Gate {
	if d.Dispatches() {
		return slices.Clone(Gates)
	}
	return []Gate{GateScope, GateLegal}
}

// Terms is one version of what a cell owes.
type Terms struct {
	Duty          Duty
	Model         Model
	Profile       Profile
	FormatVersion string
	// Authority is the tax authority or rail that receives the documents.
	Authority string
	// LegalSource is the legal source and reference the duty rests on, and
	// SourceAsOf the date it was reviewed (Doc 21 W01 "Legal source + as-of").
	LegalSource string
	SourceAsOf  time.Time
	// EffectiveFrom is the first date the terms apply; EffectiveTo, when not
	// zero, the last.
	EffectiveFrom time.Time
	EffectiveTo   time.Time
	// Conditions are the eligibility conditions the duty depends on — issuer
	// size, thresholds, waivers — as the reviewer stated them.
	Conditions string
}

// Normalize trims the text and reduces the dates to days.
func (t Terms) Normalize() Terms {
	t.FormatVersion, t.Authority = strings.TrimSpace(t.FormatVersion), strings.TrimSpace(t.Authority)
	t.LegalSource, t.Conditions = strings.TrimSpace(t.LegalSource), strings.TrimSpace(t.Conditions)
	if !t.SourceAsOf.IsZero() {
		t.SourceAsOf = Day(t.SourceAsOf)
	}
	if !t.EffectiveFrom.IsZero() {
		t.EffectiveFrom = Day(t.EffectiveFrom)
	}
	if !t.EffectiveTo.IsZero() {
		t.EffectiveTo = Day(t.EffectiveTo)
	}
	return t
}

// Validate refuses terms that cannot describe the cell. A PROHIBITED duty has
// no model and no profile; a dispatching duty has both, and a national
// profile is used only in its own country.
func (t Terms) Validate(country string) error {
	switch {
	case !t.Duty.Valid():
		return errs.Invalid("duty", errs.ReasonInvalidValue, "The duty is REQUIRED, OPTIONAL or PROHIBITED.")
	case !t.Model.Valid():
		return errs.Invalid("model", errs.ReasonInvalidValue, "That is not a control model.")
	case !t.Profile.Valid():
		return errs.Invalid("profile", errs.ReasonInvalidValue, "That is not a format profile.")
	}
	if t.Duty.Dispatches() {
		if t.Model == ModelNone {
			return errs.Invalid("model", errs.ReasonInvalidValue, "A REQUIRED or OPTIONAL duty names its control model.")
		}
		if t.Profile == ProfileNone {
			return errs.Invalid("profile", errs.ReasonInvalidValue, "A REQUIRED or OPTIONAL duty names its format profile.")
		}
	} else if t.Model != ModelNone || t.Profile != ProfileNone {
		return errs.Invalid("duty", errs.ReasonInvalidValue, "A PROHIBITED duty has model NONE and profile NONE.")
	}
	if c := t.Profile.Country(); c != "" && c != country {
		return errs.Invalid("profile", ReasonProfileCountryMismatch,
			fmt.Sprintf("%s is %s's profile; this record is for %s.", t.Profile, c, country))
	}
	for _, f := range []struct {
		name, value string
		required    bool
		limit       int
	}{
		{"formatVersion", t.FormatVersion, t.Duty.Dispatches(), MaxText},
		{"authority", t.Authority, true, MaxText},
		{"legalSource", t.LegalSource, true, MaxReason},
		{"conditions", t.Conditions, false, MaxReason},
	} {
		if err := text(f.name, f.value, f.required, f.limit); err != nil {
			return err
		}
	}
	switch {
	case t.SourceAsOf.IsZero():
		return errs.Invalid("sourceAsOf", errs.ReasonMissingField, "The date the legal source was reviewed is required.")
	case t.EffectiveFrom.IsZero():
		return errs.Invalid("effectiveFrom", errs.ReasonMissingField, "The first date the terms apply is required.")
	case !t.EffectiveTo.IsZero() && t.EffectiveTo.Before(t.EffectiveFrom):
		return errs.Invalid("effectiveTo", errs.ReasonInvalidValue, "The last date is on or after the first.")
	}
	return nil
}

// InEffect reports whether the terms apply on day.
func (t Terms) InEffect(day time.Time) bool {
	d := Day(day)
	return !d.Before(t.EffectiveFrom) && (t.EffectiveTo.IsZero() || !d.After(t.EffectiveTo))
}

// overlaps reports whether two terms' date ranges share a day.
func (t Terms) overlaps(o Terms) bool {
	endsBefore := func(a, b Terms) bool { return !a.EffectiveTo.IsZero() && a.EffectiveTo.Before(b.EffectiveFrom) }
	return !endsBefore(t, o) && !endsBefore(o, t)
}

func optDate(t time.Time) canonical.Value {
	if t.IsZero() {
		return canonical.Absent()
	}
	return canonical.String(t.Format(time.DateOnly))
}

// Version is one recorded version of a record's terms.
type Version struct {
	Number     int
	Terms      Terms
	Digest     canonical.Digest
	RecordedAt time.Time
	RecordedBy id.UserID
}

// Cell is what a record is about.
type Cell struct {
	LegalEntity  id.LegalEntityID
	Country      string
	DocumentType document.Type
	BuyerKind    BuyerKind
}

// Validate refuses a cell that is not one.
func (c Cell) Validate() error {
	switch {
	case c.LegalEntity.IsZero():
		return errs.Invalid("legalEntityId", errs.ReasonMissingField, "A record names its legal entity.")
	case !ValidCountry(c.Country):
		return errs.Invalid("country", errs.ReasonInvalidValue, "The country is an ISO 3166-1 alpha-2 code, such as FR.")
	case !ValidDocumentType(c.DocumentType):
		return errs.Invalid("documentType", errs.ReasonInvalidValue,
			"The document type is INVOICE, CREDIT_NOTE, DEBIT_NOTE, REBILL or ADJUSTMENT.")
	case !c.BuyerKind.Valid():
		return errs.Invalid("buyerKind", errs.ReasonInvalidValue, "The buyer kind is B2B, B2G or B2C.")
	}
	return nil
}

// Seal digests a version's terms with the cell they are for.
func Seal(record id.CTCApplicabilityID, c Cell, number int, t Terms) (canonical.Digest, error) {
	return canonical.Sum(canonical.Object(
		canonical.F("artifact", canonical.String("ctc-applicability-terms")),
		canonical.F("applicabilityId", canonical.String(record.String())),
		canonical.F("version", canonical.Integer(int64(number))),
		canonical.F("legalEntityId", canonical.String(c.LegalEntity.String())),
		canonical.F("country", canonical.String(c.Country)),
		canonical.F("documentType", canonical.String(string(c.DocumentType))),
		canonical.F("buyerKind", canonical.String(string(c.BuyerKind))),
		canonical.F("duty", canonical.String(string(t.Duty))),
		canonical.F("model", canonical.String(string(t.Model))),
		canonical.F("profile", canonical.String(string(t.Profile))),
		canonical.F("formatVersion", canonical.OptString(t.FormatVersion)),
		canonical.F("authority", canonical.String(t.Authority)),
		canonical.F("legalSource", canonical.String(t.LegalSource)),
		canonical.F("sourceAsOf", optDate(t.SourceAsOf)),
		canonical.F("effectiveFrom", optDate(t.EffectiveFrom)),
		canonical.F("effectiveTo", optDate(t.EffectiveTo)),
		canonical.F("conditions", canonical.OptString(t.Conditions)),
	))
}

// EventKind is what happened to a record.
type EventKind string

// The events. CREATED is always the first.
const (
	EventCreated       EventKind = "CREATED"
	EventRevised       EventKind = "REVISED"
	EventEvidenceAdded EventKind = "EVIDENCE_ADDED"
	EventSubmitted     EventKind = "SUBMITTED"
	EventReturned      EventKind = "RETURNED"
	EventGateSigned    EventKind = "GATE_SIGNED"
	EventActivated     EventKind = "ACTIVATED"
	EventSuspended     EventKind = "SUSPENDED"
	EventResumed       EventKind = "RESUMED"
	EventRetired       EventKind = "RETIRED"
)

// Event is one appended act on a record.
type Event struct {
	Seq    int
	Kind   EventKind
	Status Status
	// Version is the terms version the act was on: the version created or
	// revised, signed, or activated. Zero where the act is about no version.
	Version int
	// Gate and SignoffRef are a GATE_SIGNED event's: the gate, and the
	// sign-off's own reference (an approval ticket, a minute, an ADR).
	Gate       Gate
	SignoffRef string
	Reason     string
	// Evidence is an EVIDENCE_ADDED event's document.
	Evidence   *Attachment
	RecordedAt time.Time
	RecordedBy id.UserID
}

// Applicability is one record, folded.
type Applicability struct {
	ID       id.CTCApplicabilityID
	TenantID id.TenantID
	Cell
	CreatedAt time.Time
	CreatedBy id.UserID
	// Versions are the terms versions, oldest first.
	Versions []Version
	Status   Status
	History  []Event
}

// Current is the newest terms version.
func (a Applicability) Current() Version { return a.Versions[len(a.Versions)-1] }

// Evidence is every evidence document attached, oldest first.
func (a Applicability) Evidence() []Event {
	var out []Event
	for _, e := range a.History {
		if e.Kind == EventEvidenceAdded {
			out = append(out, e)
		}
	}
	return out
}

// Signoffs are the gate sign-offs given on a version.
func (a Applicability) Signoffs(version int) map[Gate]Event {
	out := map[Gate]Event{}
	for _, e := range a.History {
		if e.Kind == EventGateSigned && e.Version == version {
			if _, ok := out[e.Gate]; !ok {
				out[e.Gate] = e
			}
		}
	}
	return out
}

// MissingGates are the gates the current version still needs signed.
func (a Applicability) MissingGates() []Gate {
	cur := a.Current()
	signed := a.Signoffs(cur.Number)
	var out []Gate
	for _, g := range RequiredGates(cur.Terms.Duty) {
		if _, ok := signed[g]; !ok {
			out = append(out, g)
		}
	}
	return out
}

// Overlaps reports whether two records claim the same cell for a shared day.
func (a Applicability) Overlaps(b Applicability) bool {
	return a.ID != b.ID && a.Cell == b.Cell && a.Current().Terms.overlaps(b.Current().Terms)
}

func (a Applicability) last() Event { return a.History[len(a.History)-1] }

func (a Applicability) next(kind EventKind, to Status, by id.UserID, at time.Time) Event {
	return Event{Seq: len(a.History) + 1, Kind: kind, Status: to, RecordedAt: at.UTC(), RecordedBy: by}
}

// NewInput is a record to create.
type NewInput struct {
	ID        id.CTCApplicabilityID
	TenantID  id.TenantID
	Cell      Cell
	Terms     Terms
	CreatedBy id.UserID
	CreatedAt time.Time
}

// New builds a record in DRAFT with its first version and CREATED event.
func New(in NewInput) (Applicability, error) {
	if err := in.Cell.Validate(); err != nil {
		return Applicability{}, err
	}
	t := in.Terms.Normalize()
	if err := t.Validate(in.Cell.Country); err != nil {
		return Applicability{}, err
	}
	d, err := Seal(in.ID, in.Cell, 1, t)
	if err != nil {
		return Applicability{}, err
	}
	at := in.CreatedAt.UTC()
	return Applicability{
		ID: in.ID, TenantID: in.TenantID, Cell: in.Cell, CreatedAt: at, CreatedBy: in.CreatedBy,
		Versions: []Version{{Number: 1, Terms: t, Digest: d, RecordedAt: at, RecordedBy: in.CreatedBy}},
		Status:   StatusDraft,
		History:  []Event{{Seq: 1, Kind: EventCreated, Status: StatusDraft, Version: 1, RecordedAt: at, RecordedBy: in.CreatedBy}},
	}, nil
}

// step is the state machine: the status an event moves a record to from s.
func step(s Status, k EventKind) (Status, bool) {
	switch k {
	case EventRevised:
		if s == StatusDraft || s == StatusInReview {
			return StatusDraft, true
		}
	case EventEvidenceAdded:
		if s != StatusRetired {
			return s, true
		}
	case EventSubmitted:
		if s == StatusDraft {
			return StatusInReview, true
		}
	case EventReturned:
		if s == StatusInReview {
			return StatusDraft, true
		}
	case EventGateSigned:
		if s == StatusInReview {
			return StatusInReview, true
		}
	case EventActivated:
		if s == StatusInReview {
			return StatusActive, true
		}
	case EventSuspended:
		if s == StatusActive {
			return StatusSuspended, true
		}
	case EventResumed:
		if s == StatusSuspended {
			return StatusActive, true
		}
	case EventRetired:
		if s != StatusRetired {
			return StatusRetired, true
		}
	}
	return "", false
}

// Fold rebuilds a record from its identity, its versions and its events.
func Fold(a Applicability, versions []Version, events []Event) (Applicability, error) {
	if len(versions) == 0 || len(events) == 0 || events[0].Kind != EventCreated || events[0].Seq != 1 {
		return Applicability{}, fmt.Errorf("ctc: record %s's history starts with its CREATED event and first version", a.ID)
	}
	for i, v := range versions {
		if v.Number != i+1 {
			return Applicability{}, fmt.Errorf("ctc: record %s's versions are out of sequence", a.ID)
		}
	}
	status, seen := StatusDraft, 1
	for i, e := range events[1:] {
		if e.Seq != i+2 {
			return Applicability{}, fmt.Errorf("ctc: record %s's event %d is out of sequence", a.ID, e.Seq)
		}
		to, ok := step(status, e.Kind)
		if !ok || to != e.Status {
			return Applicability{}, fmt.Errorf("ctc: record %s: %s from %s to %s", a.ID, e.Kind, status, e.Status)
		}
		if e.Kind == EventRevised {
			seen++
			if e.Version != seen {
				return Applicability{}, fmt.Errorf("ctc: record %s's revision %d names version %d", a.ID, seen, e.Version)
			}
		}
		status = to
	}
	if seen != len(versions) {
		return Applicability{}, fmt.Errorf("ctc: record %s has %d versions and %d version events", a.ID, len(versions), seen)
	}
	a.Versions, a.History, a.Status = slices.Clone(versions), slices.Clone(events), status
	return a, nil
}

// Revise records the next version of the terms, returning the version and
// its REVISED event. Terms identical to the current version's record nothing
// and return false. A record in review returns to DRAFT: sign-offs given on
// an earlier version do not carry over.
func (a Applicability) Revise(t Terms, by id.UserID, at time.Time) (Version, Event, bool, error) {
	if a.Status != StatusDraft && a.Status != StatusInReview {
		return Version{}, Event{}, false, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A %s record's terms are not revised. Retire it and record a new one.", a.Status))
	}
	t = t.Normalize()
	if err := t.Validate(a.Country); err != nil {
		return Version{}, Event{}, false, err
	}
	n := len(a.Versions) + 1
	d, err := Seal(a.ID, a.Cell, n, t)
	if err != nil {
		return Version{}, Event{}, false, err
	}
	cur := a.Current()
	if same, err := Seal(a.ID, a.Cell, cur.Number, t); err == nil && same.Equal(cur.Digest) {
		return Version{}, Event{}, false, nil
	}
	v := Version{Number: n, Terms: t, Digest: d, RecordedAt: at.UTC(), RecordedBy: by}
	e := a.next(EventRevised, StatusDraft, by, at)
	e.Version = n
	return v, e, true, nil
}

// AddEvidence attaches an evidence document. The same document again records
// nothing and returns false.
func (a Applicability) AddEvidence(doc Attachment, by id.UserID, at time.Time) (Event, bool, error) {
	if a.Status == StatusRetired {
		return Event{}, false, invalidMove("record", string(a.Status), "evidence")
	}
	doc = doc.Normalize()
	if err := doc.Validate(); err != nil {
		return Event{}, false, err
	}
	for _, e := range a.Evidence() {
		if e.Evidence.SHA256 == doc.SHA256 {
			return Event{}, false, nil
		}
	}
	e := a.next(EventEvidenceAdded, a.Status, by, at)
	e.Evidence = &doc
	return e, true, nil
}

// Submit sends a draft for review and sign-off.
func (a Applicability) Submit(by id.UserID, at time.Time) (Event, error) {
	if a.Status != StatusDraft {
		return Event{}, invalidMove("record", string(a.Status), string(StatusInReview))
	}
	e := a.next(EventSubmitted, StatusInReview, by, at)
	e.Version = a.Current().Number
	return e, nil
}

// Return sends a record in review back to draft, with the reason.
func (a Applicability) Return(reason string, by id.UserID, at time.Time) (Event, error) {
	if a.Status != StatusInReview {
		return Event{}, invalidMove("record", string(a.Status), string(StatusDraft))
	}
	if err := reasonText("reason", reason, true); err != nil {
		return Event{}, err
	}
	e := a.next(EventReturned, StatusDraft, by, at)
	e.Reason = reason
	return e, nil
}

// independent refuses the record's creator and the current version's author:
// a sign-off and an activation are another person's (Doc 21 §04 "Self-approve
// own rule").
func (a Applicability) independent(by id.UserID, act string) error {
	if by == a.CreatedBy || by == a.Current().RecordedBy {
		return makerChecker(fmt.Sprintf("The person who recorded this record or its current terms cannot also %s. Another person must.", act))
	}
	return nil
}

// SignGate records a gate's sign-off on the named version. The version must
// be the current one; a gate already signed on it records nothing and
// returns false. The signer is not the record's creator or the current
// version's author.
func (a Applicability) SignGate(g Gate, version int, signoffRef, note string, by id.UserID, at time.Time) (Event, bool, error) {
	if !g.Valid() {
		return Event{}, false, errs.Invalid("gate", errs.ReasonInvalidValue, "That is not a G0–G4 release gate.")
	}
	if err := ref("signoffRef", signoffRef, true); err != nil {
		return Event{}, false, err
	}
	if err := reasonText("note", note, false); err != nil {
		return Event{}, false, err
	}
	if a.Status != StatusInReview {
		return Event{}, false, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Gates are signed on a record in review; this one is %s.", a.Status))
	}
	if cur := a.Current(); version != cur.Number {
		return Event{}, false, errs.New(errs.CategoryConflict, ReasonVersionConflict,
			fmt.Sprintf("The sign-off names version %d; the record's current version is %d.", version, cur.Number))
	}
	if !slices.Contains(RequiredGates(a.Current().Terms.Duty), g) {
		return Event{}, false, errs.Invalid("gate", errs.ReasonInvalidValue,
			fmt.Sprintf("%s is not required for a %s duty.", g, a.Current().Terms.Duty))
	}
	if err := a.independent(by, "sign its release gates"); err != nil {
		return Event{}, false, err
	}
	if _, ok := a.Signoffs(version)[g]; ok {
		return Event{}, false, nil
	}
	e := a.next(EventGateSigned, StatusInReview, by, at)
	e.Version, e.Gate, e.SignoffRef, e.Reason = version, g, signoffRef, strings.TrimSpace(note)
	return e, true, nil
}

// Activate puts the named version in force. It must be the current version,
// with every required gate signed on it, evidence attached, an effective
// window that has not already ended, and an activator other than the
// record's creator and the current version's author. Whether another record
// holds the cell is the caller's to check, across records.
func (a Applicability) Activate(version int, by id.UserID, at time.Time) (Event, error) {
	if a.Status != StatusInReview {
		return Event{}, invalidMove("record", string(a.Status), string(StatusActive))
	}
	cur := a.Current()
	if version != cur.Number {
		return Event{}, errs.New(errs.CategoryConflict, ReasonVersionConflict,
			fmt.Sprintf("The activation names version %d; the record's current version is %d.", version, cur.Number))
	}
	if err := a.independent(by, "activate it"); err != nil {
		return Event{}, err
	}
	if missing := a.MissingGates(); len(missing) > 0 {
		names := make([]string, len(missing))
		for i, g := range missing {
			names[i] = string(g)
		}
		return Event{}, errs.New(errs.CategoryConflict, ReasonGateMissing,
			fmt.Sprintf("Version %d is not signed for %s. It stays NOT_ACTIVATED.", cur.Number, strings.Join(names, ", ")))
	}
	if len(a.Evidence()) == 0 {
		return Event{}, errs.New(errs.CategoryConflict, ReasonEvidenceMissing, "Attach the legal source evidence before activation.")
	}
	if t := cur.Terms; !t.EffectiveTo.IsZero() && Day(at).After(t.EffectiveTo) {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Version %d's terms ended on %s; an ended duty is not activated.", cur.Number, t.EffectiveTo.Format(time.DateOnly)))
	}
	e := a.next(EventActivated, StatusActive, by, at)
	e.Version = cur.Number
	return e, nil
}

// Suspend stops new validation and dispatch under an active record at once,
// with the reason (Doc 21 A60). What was already submitted stays as
// recorded.
func (a Applicability) Suspend(reason string, by id.UserID, at time.Time) (Event, error) {
	if a.Status != StatusActive {
		return Event{}, invalidMove("record", string(a.Status), string(StatusSuspended))
	}
	if err := reasonText("reason", reason, true); err != nil {
		return Event{}, err
	}
	e := a.next(EventSuspended, StatusSuspended, by, at)
	e.Reason = reason
	return e, nil
}

// Resume puts a suspended record back in force. The person who suspended it
// does not also resume it.
func (a Applicability) Resume(reason string, by id.UserID, at time.Time) (Event, error) {
	if a.Status != StatusSuspended {
		return Event{}, invalidMove("record", string(a.Status), string(StatusActive))
	}
	if err := reasonText("reason", reason, true); err != nil {
		return Event{}, err
	}
	if a.last().Kind == EventSuspended && a.last().RecordedBy == by {
		return Event{}, makerChecker("The person who suspended this record cannot also resume it. Another person must.")
	}
	e := a.next(EventResumed, StatusActive, by, at)
	e.Reason = reason
	return e, nil
}

// Retire closes a record for good, with the reason. It stays visible as
// recorded, and what was submitted under it stays as it was.
func (a Applicability) Retire(reason string, by id.UserID, at time.Time) (Event, error) {
	if a.Status == StatusRetired {
		return Event{}, invalidMove("record", string(a.Status), string(StatusRetired))
	}
	if err := reasonText("reason", reason, true); err != nil {
		return Event{}, err
	}
	e := a.next(EventRetired, StatusRetired, by, at)
	e.Reason = reason
	return e, nil
}

// Resolution is what a cell owes on a date (Doc 21 W01): REQUIRED, OPTIONAL
// or PROHIBITED from the one active record in effect, or REVIEW_BLOCKED with
// the reason. Resolving has no side effect.
type Resolution struct {
	Cell   Cell
	Date   time.Time
	Duty   Duty
	Reason errs.ReasonCode
	Detail string
	// Record is the record that decided, or the one that blocks; nil when
	// none covers the cell.
	Record *Applicability
}

// Resolve answers what a cell owes on a date from the tenant's records for
// it.
func Resolve(records []Applicability, c Cell, date time.Time) Resolution {
	r := Resolution{Cell: c, Date: Day(date), Duty: DutyReviewBlocked}
	var active, suspended, pending []Applicability
	for _, a := range records {
		if a.Cell != c {
			continue
		}
		switch {
		case a.Status == StatusRetired:
		case a.Status == StatusActive && a.Current().Terms.InEffect(r.Date):
			active = append(active, a)
		case a.Status == StatusSuspended && a.Current().Terms.InEffect(r.Date):
			suspended = append(suspended, a)
		case (a.Status == StatusDraft || a.Status == StatusInReview) && a.Current().Terms.InEffect(r.Date):
			pending = append(pending, a)
		}
	}
	switch {
	case len(active) == 1:
		a := active[0]
		r.Duty, r.Record = a.Current().Terms.Duty, &a
		r.Detail = fmt.Sprintf("Decided by version %d of record %s, in effect from %s.", a.Current().Number, a.ID,
			a.Current().Terms.EffectiveFrom.Format(time.DateOnly))
	case len(active) > 1:
		a := active[0]
		r.Reason, r.Record = ReasonApplicabilityConflict, &a
		r.Detail = fmt.Sprintf("%d active records claim this cell on %s.", len(active), r.Date.Format(time.DateOnly))
	case len(suspended) > 0:
		a := suspended[0]
		r.Reason, r.Record = ReasonApplicabilityNotActive, &a
		r.Detail = fmt.Sprintf("Record %s covers this cell and is SUSPENDED.", a.ID)
	case len(pending) > 0:
		a := pending[0]
		r.Reason, r.Record = ReasonApplicabilityNotActive, &a
		r.Detail = fmt.Sprintf("Record %s covers this cell and is %s, not yet activated.", a.ID, a.Status)
	default:
		r.Reason = ReasonNoApplicability
		r.Detail = "No record covers this cell on this date."
	}
	return r
}

// Filter narrows a listing of records. Empty fields do not filter.
type Filter struct {
	LegalEntity id.LegalEntityID
	Country     string
	Status      Status
}

// Match reports whether a record passes the filter.
func (f Filter) Match(a Applicability) bool {
	return (f.LegalEntity.IsZero() || a.LegalEntity == f.LegalEntity) && (f.Country == "" || a.Country == f.Country) &&
		(f.Status == "" || a.Status == f.Status)
}
