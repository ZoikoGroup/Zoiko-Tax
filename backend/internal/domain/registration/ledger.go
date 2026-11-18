package registration

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The registration ledger (Doc 17 EER-04, EER-05, EER-06; OBL-02; OPS-14).
//
// This is the record of a registration the tenant holds with an authority —
// its scheme, number, effective dates and evidence — and not the
// obligation engine's registration *duty* (obligation.RegistrationState,
// NOT_REQUIRED … DEREGISTERED), which says whether a registration is
// required. The two are linked by the obligation engine reading this
// ledger, never by one writing the other: Doc 17 W17-07 keeps "no
// obligation inferred without content rule + required facts".

// Scheme is the kind of registration. Each scheme has its own identifier,
// and identifiers are never converted from one scheme to another (Doc 17
// §02 "Regulatory identity").
type Scheme string

// The schemes.
const (
	SchemeUSFCCFRN        Scheme = "US_FCC_FRN"
	SchemeUSUSAC499       Scheme = "US_USAC_499_FILER_ID"
	SchemeUSUSAC498       Scheme = "US_USAC_498_ID"
	SchemeUSSalesTax      Scheme = "US_STATE_SALES_TAX_PERMIT"
	SchemeUSStateTelecom  Scheme = "US_STATE_TELECOM_REGISTRATION"
	SchemeUKVAT           Scheme = "UK_VAT"
	SchemeEUVAT           Scheme = "EU_VAT"
	SchemeCAGSTHST        Scheme = "CA_GST_HST"
	SchemeOtherRegistered Scheme = "OTHER_REGISTRATION"
)

// Schemes is every scheme, in the order a form offers them.
var Schemes = []Scheme{SchemeUSFCCFRN, SchemeUSUSAC499, SchemeUSUSAC498, SchemeUSSalesTax, SchemeUSStateTelecom,
	SchemeUKVAT, SchemeEUVAT, SchemeCAGSTHST, SchemeOtherRegistered}

// Valid reports whether the scheme is one this ledger knows.
func (s Scheme) Valid() bool { return slices.Contains(Schemes, s) }

// structured are the schemes with a fixed identifier shape. A number that
// fails its own scheme's shape but fits another's is a scheme mismatch.
var structured = []struct {
	scheme Scheme
	shape  *regexp.Regexp
	label  string
}{
	{SchemeUSFCCFRN, regexp.MustCompile(`^[0-9]{10}$`), "an FCC Registration Number (10 digits)"},
	{SchemeUSUSAC499, regexp.MustCompile(`^[0-9]{6}$`), "a USAC 499 Filer ID (6 digits)"},
	{SchemeUSUSAC498, regexp.MustCompile(`^143[0-9]{6}$`), "a USAC 498 ID / SPIN (9 digits starting 143)"},
	{SchemeUKVAT, regexp.MustCompile(`^GB([0-9]{9}|[0-9]{12})$`), "a UK VAT number (GB and 9 or 12 digits)"},
	{SchemeEUVAT, regexp.MustCompile(`^(AT|BE|BG|CY|CZ|DE|DK|EE|EL|ES|FI|FR|HR|HU|IE|IT|LT|LU|LV|MT|NL|PL|PT|RO|SE|SI|SK|XI)[0-9A-Z]{2,12}$`),
		"an EU VAT number (member-state or XI prefix)"},
	{SchemeCAGSTHST, regexp.MustCompile(`^[0-9]{9}RT[0-9]{4}$`), "a Canadian GST/HST account (9 digits, RT, 4 digits)"},
}

var freeNumber = regexp.MustCompile(`^[A-Z0-9][A-Z0-9 ./-]{0,63}$`)

// euCountry is the country an EU VAT prefix belongs to. EL is Greece's VAT
// prefix; XI is Northern Ireland under the Windsor Framework, which VIES
// handles while GB numbers are HMRC's (Doc 17 ER-018).
func euCountry(prefix string) string {
	switch prefix {
	case "EL":
		return "GR"
	case "XI":
		return "GB"
	}
	return prefix
}

// NormalizeNumber is how a number is stored: upper case, and for a
// structured scheme without the spaces and hyphens people type.
func NormalizeNumber(s Scheme, number string) string {
	n := strings.ToUpper(strings.TrimSpace(number))
	for _, st := range structured {
		if st.scheme == s {
			return strings.NewReplacer(" ", "", "-", "", ".", "").Replace(n)
		}
	}
	return n
}

// CheckNumber refuses a number its scheme does not admit. A number that is
// the shape of another scheme is REGISTRATION_SCHEME_MISMATCH and is never
// converted (Doc 17 ER-011).
func CheckNumber(s Scheme, number string) error {
	for _, st := range structured {
		if st.scheme != s {
			continue
		}
		if st.shape.MatchString(number) {
			return nil
		}
		for _, other := range structured {
			if other.scheme != s && other.shape.MatchString(number) {
				return errs.Invalid("registrationNumber", ReasonSchemeMismatch,
					fmt.Sprintf("This number has the shape of %s, not %s. Enter it under its own scheme; it is never converted.", other.label, st.label))
			}
		}
		return errs.Invalid("registrationNumber", errs.ReasonInvalidValue, fmt.Sprintf("This scheme's number is %s.", st.label))
	}
	if !freeNumber.MatchString(number) {
		return errs.Invalid("registrationNumber", errs.ReasonInvalidValue,
			"A registration number is 1 to 64 letters, digits, spaces, dots, slashes or hyphens.")
	}
	return nil
}

// checkJurisdiction refuses a jurisdiction the scheme does not belong to.
func checkJurisdiction(s Scheme, jurisdiction, number string) error {
	if !ValidJurisdiction(jurisdiction) {
		return errs.Invalid("jurisdiction", errs.ReasonInvalidValue, "A jurisdiction is a country, or a country and one subdivision: US, US-NY, GB.")
	}
	country, sub, _ := strings.Cut(jurisdiction, "-")
	want := ""
	switch s {
	case SchemeUSFCCFRN, SchemeUSUSAC499, SchemeUSUSAC498:
		if jurisdiction != "US" {
			return errs.Invalid("jurisdiction", errs.ReasonInvalidValue, "An FCC or USAC registration is federal: its jurisdiction is US.")
		}
	case SchemeUSSalesTax, SchemeUSStateTelecom:
		if country != "US" || sub == "" {
			return errs.Invalid("jurisdiction", errs.ReasonInvalidValue, "A state registration names its state: US-NY, US-CA.")
		}
	case SchemeUKVAT:
		want = "GB"
	case SchemeEUVAT:
		if len(number) >= 2 {
			want = euCountry(number[:2])
		}
	case SchemeCAGSTHST:
		want = "CA"
	}
	if want != "" && country != want {
		return errs.Invalid("jurisdiction", errs.ReasonInvalidValue, fmt.Sprintf("This registration's jurisdiction is in %s.", want))
	}
	return nil
}

// Status is where a registration stands (Doc 17 §07).
type Status string

// The statuses. VERIFIED is not ACTIVE, and neither is authority to file.
const (
	StatusDraft     Status = "DRAFT"
	StatusSubmitted Status = "SUBMITTED"
	StatusVerified  Status = "VERIFIED"
	StatusActive    Status = "ACTIVE"
	StatusSuspended Status = "SUSPENDED"
	StatusExpired   Status = "EXPIRED"
	StatusRevoked   Status = "REVOKED"
	StatusWithdrawn Status = "WITHDRAWN"
)

// Statuses is every registration status.
var Statuses = []Status{StatusDraft, StatusSubmitted, StatusVerified, StatusActive, StatusSuspended, StatusExpired,
	StatusRevoked, StatusWithdrawn}

// Valid reports whether the status is one of these.
func (s Status) Valid() bool { return slices.Contains(Statuses, s) }

// Closed reports whether nothing more happens to a registration in this
// status. It stays visible as recorded (Doc 17 ER-012).
func (s Status) Closed() bool {
	return s == StatusExpired || s == StatusRevoked || s == StatusWithdrawn
}

// transitions are the moves a person asks for by naming the target. The move
// to VERIFIED is not here: only an authority check makes it.
var transitions = map[Status][]Status{
	StatusDraft:     {StatusSubmitted, StatusWithdrawn},
	StatusSubmitted: {StatusDraft, StatusWithdrawn},
	StatusVerified:  {StatusActive, StatusDraft},
	StatusActive:    {StatusSuspended, StatusExpired, StatusRevoked},
	StatusSuspended: {StatusActive, StatusExpired, StatusRevoked},
}

// CanTransition reports whether a person may move a registration.
func CanTransition(from, to Status) bool { return slices.Contains(transitions[from], to) }

// EventKind is what happened to a registration.
type EventKind string

// The events. CREATED is always the first.
const (
	EventCreated    EventKind = "CREATED"
	EventSubmitted  EventKind = "SUBMITTED"
	EventWithdrawn  EventKind = "WITHDRAWN"
	EventReturned   EventKind = "RETURNED"
	EventChecked    EventKind = "CHECK_RECORDED"
	EventActivated  EventKind = "ACTIVATED"
	EventSuspended  EventKind = "SUSPENDED"
	EventReinstated EventKind = "REINSTATED"
	EventExpired    EventKind = "EXPIRED"
	EventRevoked    EventKind = "REVOKED"
	EventAttachment EventKind = "ATTACHMENT_RECORDED"
)

// CheckSource is the authority, or authority service, a check was made
// against.
type CheckSource string

// The sources.
const (
	SourceFCC        CheckSource = "FCC"
	SourceUSAC       CheckSource = "USAC"
	SourceHMRC       CheckSource = "HMRC"
	SourceVIES       CheckSource = "VIES"
	SourceCRA        CheckSource = "CRA"
	SourceStateTax   CheckSource = "STATE_REVENUE_AUTHORITY"
	SourceOtherAuthy CheckSource = "OTHER_AUTHORITY"
)

// CheckSources is every source.
var CheckSources = []CheckSource{SourceFCC, SourceUSAC, SourceHMRC, SourceVIES, SourceCRA, SourceStateTax, SourceOtherAuthy}

// Valid reports whether the source is one of these.
func (c CheckSource) Valid() bool { return slices.Contains(CheckSources, c) }

// sources are the authorities that can confirm a scheme's number. VIES does
// not validate a GB number, and HMRC does not validate an EU one (Doc 17
// ER-018, XC-03).
var sources = map[Scheme][]CheckSource{
	SchemeUSFCCFRN:        {SourceFCC},
	SchemeUSUSAC499:       {SourceUSAC},
	SchemeUSUSAC498:       {SourceUSAC},
	SchemeUSSalesTax:      {SourceStateTax},
	SchemeUSStateTelecom:  {SourceStateTax, SourceOtherAuthy},
	SchemeUKVAT:           {SourceHMRC},
	SchemeEUVAT:           {SourceVIES},
	SchemeCAGSTHST:        {SourceCRA},
	SchemeOtherRegistered: CheckSources,
}

// SourcesFor returns the sources that can check a scheme.
func SourcesFor(s Scheme) []CheckSource { return slices.Clone(sources[s]) }

// CheckOutcome is what an authority check returned (Doc 17 §07).
type CheckOutcome string

// The outcomes. Only VERIFIED is positive; the other three never coerce to
// it (Doc 17 ER-015, XC-06).
const (
	OutcomeVerified     CheckOutcome = "VERIFIED"
	OutcomeNegative     CheckOutcome = "NEGATIVE"
	OutcomeInconclusive CheckOutcome = "INCONCLUSIVE"
	OutcomeUnavailable  CheckOutcome = "UNAVAILABLE"
)

// Valid reports whether the outcome is one of these.
func (o CheckOutcome) Valid() bool {
	switch o {
	case OutcomeVerified, OutcomeNegative, OutcomeInconclusive, OutcomeUnavailable:
		return true
	}
	return false
}

// CheckMethod is how a check was made. No authority adapter is connected,
// so the only method is a person's lookup, attested as theirs.
type CheckMethod string

// MethodManualLookup is a person looking the number up with the authority
// and recording what it returned.
const MethodManualLookup CheckMethod = "MANUAL_LOOKUP"

// Check is one authority check of a registration's number (Doc 17 EER-06).
type Check struct {
	Source    CheckSource
	Method    CheckMethod
	Outcome   CheckOutcome
	Reference string    // the authority's own reference or receipt, as returned
	CheckedAt time.Time // when the authority was asked
	// ReceiptSHA256 is the digest of a saved copy of the authority's
	// response, when one was kept.
	ReceiptSHA256 string
}

// Event is one appended act on a registration.
type Event struct {
	Seq        int
	Kind       EventKind
	Status     Status
	Reason     string
	Check      *Check
	Attachment *Attachment
	RecordedAt time.Time
	RecordedBy id.UserID
}

// Registration is one registration the tenant holds, or is applying for,
// with an authority, folded with its events.
type Registration struct {
	ID          id.RegistrationID
	TenantID    id.TenantID
	LegalEntity id.LegalEntityID

	Scheme       Scheme
	Jurisdiction string
	Authority    string
	Number       string

	// EffectiveFrom and EffectiveTo are the legal dates the registration is
	// effective, as the authority states them. EffectiveTo is zero when
	// open-ended. They are civil dates, UTC midnights.
	EffectiveFrom time.Time
	EffectiveTo   time.Time

	// Supersedes is the registration this one replaces — a renewal or a
	// corrected record — zero for none. The old one is not edited.
	Supersedes id.RegistrationID

	// IntakeKey is the Idempotency-Key it was created under, and IntakeHash
	// the digest of what was asked, so a retry returns this record and a
	// different request under the same key is refused.
	IntakeKey  string
	IntakeHash canonical.Digest

	CreatedAt time.Time
	CreatedBy id.UserID

	Status  Status
	History []Event
}

// Version is the registration's version: the number of its events. An
// activation names it and is refused when anything has been recorded since
// (Doc 17 §06).
func (r Registration) Version() int { return len(r.History) }

// Validate refuses a registration that cannot be recorded. It normalizes
// nothing; the caller has.
func (r Registration) Validate() error {
	if r.LegalEntity.IsZero() {
		return errs.Invalid("legalEntityId", errs.ReasonMissingField, "A registration belongs to one of the tenant's legal entities.")
	}
	if !r.Scheme.Valid() {
		return errs.Invalid("scheme", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a registration scheme this ledger knows.", r.Scheme))
	}
	if err := text("authority", r.Authority, true, 128); err != nil {
		return err
	}
	if r.Number == "" {
		return errs.Invalid("registrationNumber", errs.ReasonMissingField, "A registration records the authority's number for it.")
	}
	if err := CheckNumber(r.Scheme, r.Number); err != nil {
		return err
	}
	if err := checkJurisdiction(r.Scheme, r.Jurisdiction, r.Number); err != nil {
		return err
	}
	if r.EffectiveFrom.IsZero() {
		return errs.Invalid("effectiveFrom", errs.ReasonMissingField, "A registration states the date it is effective from.")
	}
	if !r.EffectiveTo.IsZero() && r.EffectiveTo.Before(r.EffectiveFrom) {
		return errs.Invalid("effectiveTo", errs.ReasonInvalidValue, "A registration ends on or after the date it starts.")
	}
	if !r.Supersedes.IsZero() && r.Supersedes == r.ID {
		return errs.Invalid("supersedes", errs.ReasonInvalidValue, "A registration does not supersede itself.")
	}
	return nil
}

// Payload is the canonical document the intake hash covers: what was asked
// for, not who asked or when.
func (r Registration) Payload(submit bool, attachments []Attachment) canonical.Value {
	supersedes := canonical.Absent()
	if !r.Supersedes.IsZero() {
		supersedes = canonical.String(r.Supersedes.String())
	}
	to := canonical.Absent()
	if !r.EffectiveTo.IsZero() {
		to = canonical.String(r.EffectiveTo.Format(time.DateOnly))
	}
	docs := make([]canonical.Value, len(attachments))
	for i, a := range attachments {
		docs[i] = attachmentValue(a)
	}
	return canonical.Object(
		canonical.F("legalEntityId", canonical.String(r.LegalEntity.String())),
		canonical.F("scheme", canonical.String(string(r.Scheme))),
		canonical.F("jurisdiction", canonical.String(r.Jurisdiction)),
		canonical.F("authority", canonical.String(r.Authority)),
		canonical.F("registrationNumber", canonical.String(r.Number)),
		canonical.F("effectiveFrom", canonical.String(r.EffectiveFrom.Format(time.DateOnly))),
		canonical.F("effectiveTo", to),
		canonical.F("supersedes", supersedes),
		canonical.F("submit", canonical.Bool(submit)),
		canonical.F("attachments", canonical.Array(docs...)),
	)
}

func attachmentValue(a Attachment) canonical.Value {
	return canonical.Object(
		canonical.F("sha256", canonical.String(a.SHA256)),
		canonical.F("filename", canonical.String(a.Filename)),
		canonical.F("mediaType", canonical.String(a.MediaType)),
		canonical.F("bytes", canonical.Integer(a.Bytes)),
	)
}

// Open is a new registration's first events: CREATED as a DRAFT, an
// ATTACHMENT_RECORDED per document, and SUBMITTED when asked. A submitted
// registration is SUBMITTED, never ACTIVE (Doc 17 ER-014).
func Open(r Registration, attachments []Attachment, submit bool, by id.UserID, at time.Time) ([]Event, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	r.Status, r.History = StatusDraft, []Event{{Seq: 1, Kind: EventCreated, Status: StatusDraft, RecordedAt: at, RecordedBy: by}}
	for _, a := range attachments {
		e, err := r.RecordAttachment(a, by, at)
		if err != nil {
			return nil, err
		}
		r.History = append(r.History, e)
	}
	if submit {
		e, err := r.Transition(StatusSubmitted, "", r.Version(), by, at)
		if err != nil {
			return nil, err
		}
		r.History = append(r.History, e)
	}
	return r.History, nil
}

// Fold applies a registration's events in order.
func Fold(r Registration, events []Event) (Registration, error) {
	if len(events) == 0 || events[0].Kind != EventCreated || events[0].Seq != 1 || events[0].Status != StatusDraft {
		return Registration{}, fmt.Errorf("registration: a registration's history starts with its CREATED event")
	}
	r.Status = StatusDraft
	for i, e := range events {
		if e.Seq != i+1 {
			return Registration{}, fmt.Errorf("registration: event %d is out of sequence", e.Seq)
		}
		if i > 0 && !folds(r.Status, e) {
			return Registration{}, fmt.Errorf("registration: %s from %s to %s", e.Kind, r.Status, e.Status)
		}
		r.Status = e.Status
	}
	r.History = slices.Clone(events)
	return r, nil
}

// folds reports whether a stored event is a move the ledger allows.
func folds(from Status, e Event) bool {
	switch e.Kind {
	case EventAttachment:
		return e.Status == from && !from.Closed()
	case EventChecked:
		if e.Check == nil {
			return false
		}
		return e.Status == checkedStatus(from, e.Check.Outcome)
	case EventSubmitted:
		return from == StatusDraft && e.Status == StatusSubmitted
	case EventWithdrawn:
		return (from == StatusDraft || from == StatusSubmitted) && e.Status == StatusWithdrawn
	case EventReturned:
		return (from == StatusSubmitted || from == StatusVerified) && e.Status == StatusDraft
	case EventActivated:
		return from == StatusVerified && e.Status == StatusActive
	case EventReinstated:
		return from == StatusSuspended && e.Status == StatusActive
	case EventSuspended, EventExpired, EventRevoked:
		return CanTransition(from, e.Status) && e.Status == kindStatus[e.Kind]
	}
	return false
}

var kindStatus = map[EventKind]Status{EventSuspended: StatusSuspended, EventExpired: StatusExpired, EventRevoked: StatusRevoked}

// checkedStatus is where a check leaves a registration: a VERIFIED outcome
// verifies a SUBMITTED one, a NEGATIVE outcome takes a VERIFIED one back to
// SUBMITTED, and nothing else moves. A NEGATIVE check on an ACTIVE
// registration does not suspend it — a person reviews and suspends (Doc 17
// §03 C; EER-13 "no auto-revocation").
func checkedStatus(from Status, o CheckOutcome) Status {
	switch {
	case from == StatusSubmitted && o == OutcomeVerified:
		return StatusVerified
	case from == StatusVerified && o == OutcomeNegative:
		return StatusSubmitted
	}
	return from
}

func (r Registration) next(kind EventKind, to Status, reason string, by id.UserID, at time.Time) Event {
	return Event{Seq: len(r.History) + 1, Kind: kind, Status: to, Reason: reason, RecordedAt: at.UTC(), RecordedBy: by}
}

// last returns the most recent event of a kind, and false when there is
// none.
func (r Registration) last(kinds ...EventKind) (Event, bool) {
	for i := len(r.History) - 1; i >= 0; i-- {
		if slices.Contains(kinds, r.History[i].Kind) {
			return r.History[i], true
		}
	}
	return Event{}, false
}

// Submitter is the person who last submitted the registration.
func (r Registration) Submitter() (id.UserID, bool) {
	e, ok := r.last(EventSubmitted)
	return e.RecordedBy, ok
}

// LastCheck is the most recent authority check, and false when there is
// none.
func (r Registration) LastCheck() (Check, bool) {
	e, ok := r.last(EventChecked)
	if !ok || e.Check == nil {
		return Check{}, false
	}
	return *e.Check, true
}

// HasAttachment reports whether a document with this digest is recorded.
func (r Registration) HasAttachment(sha string) bool {
	for _, e := range r.History {
		if e.Attachment != nil && e.Attachment.SHA256 == sha {
			return true
		}
	}
	return false
}

// InForce reports whether the registration is ACTIVE and within its
// effective dates on day. It is evidence of status, not a tax treatment.
func (r Registration) InForce(day time.Time) bool {
	return r.Status == StatusActive && !r.EffectiveFrom.After(day) && (r.EffectiveTo.IsZero() || !day.After(r.EffectiveTo))
}

// FlagsAt is what needs attention on day.
func (r Registration) FlagsAt(day time.Time) []Flag {
	out := endFlags(r.Status == StatusActive || r.Status == StatusSuspended || r.Status == StatusVerified,
		r.EffectiveFrom, r.EffectiveTo, day)
	if r.Status == StatusVerified {
		out = append(out, FlagAwaitingActivation)
	}
	if c, ok := r.LastCheck(); ok && !r.Status.Closed() {
		switch c.Outcome {
		case OutcomeInconclusive, OutcomeUnavailable:
			if r.Status == StatusSubmitted {
				out = append(out, FlagPendingValidation)
			}
		case OutcomeNegative:
			out = append(out, FlagAuthorityNegative)
		}
	}
	return out
}

// Transition moves a registration to a status a person names, with the
// reason, against the version they read.
//
//   - SUBMITTED from DRAFT; WITHDRAWN from DRAFT or SUBMITTED, before review.
//   - DRAFT from SUBMITTED or VERIFIED: returned for evidence, with reason.
//   - ACTIVE from VERIFIED: activation, by someone other than its
//     submitter, on the exact version, once its effective date has come.
//   - ACTIVE from SUSPENDED: reinstatement, by someone other than the
//     person who suspended it, while still within its dates.
//   - SUSPENDED, EXPIRED or REVOKED from ACTIVE or SUSPENDED, with reason;
//     EXPIRED only once its end date has passed.
func (r Registration) Transition(to Status, reason string, version int, by id.UserID, at time.Time) (Event, error) {
	reason = strings.TrimSpace(reason)
	if !to.Valid() {
		return Event{}, errs.Invalid("to", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a registration status.", to))
	}
	if !CanTransition(r.Status, to) {
		if to == StatusVerified {
			return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				"A registration is VERIFIED only by an authority check that returned VERIFIED.")
		}
		return Event{}, invalidMove("registration", string(r.Status), string(to))
	}
	if version != r.Version() {
		return Event{}, errs.New(errs.CategoryConflict, ReasonVersionConflict,
			fmt.Sprintf("The registration is at version %d; version %d was read. Re-read it and act on the current version.", r.Version(), version))
	}
	if reason != "" && len(reason) > MaxReason {
		return Event{}, errs.Invalid("reason", errs.ReasonInvalidValue, fmt.Sprintf("A reason is at most %d characters.", MaxReason))
	}
	day := Day(at)
	kind := EventKind("")
	switch to {
	case StatusSubmitted:
		kind = EventSubmitted
	case StatusWithdrawn:
		kind = EventWithdrawn
	case StatusDraft:
		kind = EventReturned
	case StatusActive:
		if r.Status == StatusSuspended {
			kind = EventReinstated
			if s, ok := r.last(EventSuspended); ok && s.RecordedBy == by {
				return Event{}, makerChecker("The person who suspended this registration cannot also reinstate it. Another person must.")
			}
		} else {
			kind = EventActivated
			if s, ok := r.Submitter(); ok && s == by {
				return Event{}, makerChecker("The person who submitted this registration cannot also activate it. Another person must.")
			}
			if r.EffectiveFrom.After(day) {
				return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
					fmt.Sprintf("The registration is effective from %s. It is activated on or after that date, not before.", r.EffectiveFrom.Format(time.DateOnly)))
			}
		}
		if !r.EffectiveTo.IsZero() && day.After(r.EffectiveTo) {
			return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				fmt.Sprintf("The registration ended on %s. Record it as expired, or record its renewal as a new registration.", r.EffectiveTo.Format(time.DateOnly)))
		}
	case StatusSuspended:
		kind = EventSuspended
	case StatusRevoked:
		kind = EventRevoked
	case StatusExpired:
		kind = EventExpired
		if r.EffectiveTo.IsZero() || !day.After(r.EffectiveTo) {
			return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				"A registration expires only once its effective end date has passed. To end it otherwise, revoke or suspend it with the reason.")
		}
	}
	if to != StatusSubmitted && to != StatusExpired {
		if err := reasonText("reason", reason); err != nil {
			return Event{}, err
		}
	}
	return r.next(kind, to, reason, by, at), nil
}

// RecordCheck records an authority check a person made. The source must be
// one that can check the scheme; a VERIFIED or NEGATIVE outcome carries the
// authority's own reference; nothing is checked in the future. The person
// who submitted the registration does not record the check that verifies
// it.
func (r Registration) RecordCheck(c Check, note string, by id.UserID, at time.Time) (Event, error) {
	c.Reference, c.ReceiptSHA256 = strings.TrimSpace(c.Reference), strings.ToLower(strings.TrimSpace(c.ReceiptSHA256))
	note = strings.TrimSpace(note)
	switch r.Status {
	case StatusSubmitted, StatusVerified, StatusActive, StatusSuspended:
	default:
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A %s registration is not checked with the authority; submit it first.", r.Status))
	}
	switch {
	case !c.Source.Valid():
		return Event{}, errs.Invalid("source", errs.ReasonInvalidValue, fmt.Sprintf("%q is not an authority source.", c.Source))
	case !slices.Contains(sources[r.Scheme], c.Source):
		return Event{}, errs.Invalid("source", errs.ReasonInvalidValue,
			fmt.Sprintf("%s does not validate a %s number. A check against the wrong register proves nothing.", c.Source, r.Scheme))
	case c.Method != MethodManualLookup:
		return Event{}, errs.Invalid("method", errs.ReasonInvalidValue, "No authority adapter is connected: a check is a person's MANUAL_LOOKUP.")
	case !c.Outcome.Valid():
		return Event{}, errs.Invalid("outcome", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a check outcome.", c.Outcome))
	case c.CheckedAt.IsZero() || c.CheckedAt.After(at):
		return Event{}, errs.Invalid("checkedAt", errs.ReasonInvalidValue, "A check states when the authority was asked, which is not in the future.")
	case (c.Outcome == OutcomeVerified || c.Outcome == OutcomeNegative) && c.Reference == "":
		return Event{}, errs.Invalid("reference", errs.ReasonMissingField,
			"A VERIFIED or NEGATIVE outcome records the authority's own reference or receipt for it.")
	case len(c.Reference) > MaxText:
		return Event{}, errs.Invalid("reference", errs.ReasonInvalidValue, fmt.Sprintf("A reference is at most %d characters.", MaxText))
	case c.ReceiptSHA256 != "" && !sha256Shape.MatchString(c.ReceiptSHA256):
		return Event{}, errs.Invalid("receiptSha256", errs.ReasonInvalidValue, "The receipt digest is a SHA-256 in lowercase hex.")
	case len(note) > MaxReason:
		return Event{}, errs.Invalid("note", errs.ReasonInvalidValue, fmt.Sprintf("A note is at most %d characters.", MaxReason))
	}
	to := checkedStatus(r.Status, c.Outcome)
	if to == StatusVerified {
		if s, ok := r.Submitter(); ok && s == by {
			return Event{}, makerChecker("The person who submitted this registration cannot also record the check that verifies it. Another person must.")
		}
	}
	c.CheckedAt = c.CheckedAt.UTC()
	e := r.next(EventChecked, to, note, by, at)
	e.Check = &c
	return e, nil
}

// RecordAttachment records a supporting document by digest. A document
// already recorded is refused rather than recorded twice.
func (r Registration) RecordAttachment(a Attachment, by id.UserID, at time.Time) (Event, error) {
	a = a.Normalize()
	if r.Status.Closed() {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A %s registration takes no more evidence; record a new registration.", r.Status))
	}
	if err := a.Validate(); err != nil {
		return Event{}, err
	}
	if r.HasAttachment(a.SHA256) {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "This document is already recorded on the registration.")
	}
	e := r.next(EventAttachment, r.Status, "", by, at)
	e.Attachment = &a
	return e, nil
}

// MaskNumber is a number as a list shows it: its last four characters.
// Doc 17 asks for scheme-specific redaction; until a scheme needs more,
// four is the most any scheme here shows.
func MaskNumber(n string) string {
	if len(n) <= 4 {
		return "****"
	}
	return "****" + n[len(n)-4:]
}

// Filter narrows a listing. Empty fields do not filter.
type Filter struct {
	Status       Status
	Scheme       Scheme
	LegalEntity  id.LegalEntityID
	Jurisdiction string // a country matches its subdivisions
	Flag         Flag
}

// Match reports whether a registration passes the filter on day.
func (f Filter) Match(r Registration, day time.Time) bool {
	return (f.Status == "" || r.Status == f.Status) && (f.Scheme == "" || r.Scheme == f.Scheme) &&
		(f.LegalEntity.IsZero() || r.LegalEntity == f.LegalEntity) && jurisdictionMatch(f.Jurisdiction, r.Jurisdiction) &&
		(f.Flag == "" || slices.Contains(r.FlagsAt(day), f.Flag))
}

func jurisdictionMatch(filter, j string) bool {
	return filter == "" || j == filter || strings.HasPrefix(j, filter+"-")
}
