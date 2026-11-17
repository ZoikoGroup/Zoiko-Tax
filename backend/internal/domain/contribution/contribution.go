// Package contribution models provider contributions: the operator's own
// obligations to a universal-service or similar fund, assessed on the
// provider's eligible revenue (OBL-06 Contribution Accumulator and Source
// Event Explorer; OBL-10 USAC; the ContributionEvent model).
//
// Three things are kept apart here because confusing them is how a
// contribution filing goes wrong:
//
//   - A contribution is not a customer charge. What a provider recovers from
//     its end users through a surcharge line is a receivable of its own; it
//     is not the contribution payable and is never added to it
//     (UAT-OBL-032, -033). A contribution can exist with no surcharge line at
//     all, so nothing here reads a tax decision.
//   - A source event is recorded once. The natural key is the source event
//     and its version: the same payload again returns the record already
//     made, and a different payload under the same key is refused, so a
//     redelivered import never counts twice (UAT-OBL-026).
//   - Nothing is edited. A reversal is a negative contribution of its own,
//     linked to the event it reverses (UAT-OBL-027); a quarantine holds an
//     accepted event out of the control totals without deleting it, and a
//     release puts it back. Both are appended status events.
//
// Every amount is a fiscal.Money, added exactly (UAT-OBL-034); nothing here
// divides, so no rounding policy is needed.
package contribution

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/remittance"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// ReasonSourceEventConflict is a source event and version recorded again
// with a different payload (UAT-OBL-026). The first record stands; a change
// is a new version or a reversal.
var ReasonSourceEventConflict = errs.Register("SOURCE_EVENT_CONFLICT", errs.CategoryConflict,
	"obl-06",
	"This source event and version were already recorded with a different payload. A change is a new version or a reversal, never an edit.")

// ReasonMakerChecker is the estate's maker-checker code: the person who
// quarantined an event does not also release it.
var ReasonMakerChecker = remittance.ReasonMakerChecker

// Regime is the fund a contribution is owed to.
type Regime string

// The regimes.
const (
	RegimeUSUSF     Regime = "US_USF"      // US Universal Service Fund, through USAC (OBL-10)
	RegimeCACRTCNCF Regime = "CA_CRTC_NCF" // Canada CRTC National Contribution Fund
	RegimeOther     Regime = "OTHER"
)

// Valid reports whether the regime is one this service knows.
func (r Regime) Valid() bool {
	switch r {
	case RegimeUSUSF, RegimeCACRTCNCF, RegimeOther:
		return true
	}
	return false
}

// SourceType is what kind of record the source event is.
type SourceType string

// The source types OBL-06 names.
const (
	SourceBSSInvoice         SourceType = "BSS_INVOICE"
	SourceRatedUsage         SourceType = "RATED_USAGE"
	SourceRevenueRecognition SourceType = "REVENUE_RECOGNITION_ADJUSTMENT"
	SourceLicenceActivity    SourceType = "LICENCE_ACTIVITY"
	SourceManualSchedule     SourceType = "MANUAL_CERTIFIED_SCHEDULE"
)

// Valid reports whether the source type is one OBL-06 names.
func (s SourceType) Valid() bool {
	switch s {
	case SourceBSSInvoice, SourceRatedUsage, SourceRevenueRecognition, SourceLicenceActivity, SourceManualSchedule:
		return true
	}
	return false
}

// Status is where a contribution stands. Only ACCEPTED counts.
type Status string

// The statuses.
const (
	StatusAccepted    Status = "ACCEPTED"
	StatusQuarantined Status = "QUARANTINED"
)

// Valid reports whether the status is one of the two.
func (s Status) Valid() bool { return s == StatusAccepted || s == StatusQuarantined }

// EventKind is what happened to a contribution.
type EventKind string

// The status events. RECORDED is always the first.
const (
	EventRecorded    EventKind = "RECORDED"
	EventQuarantined EventKind = "QUARANTINED"
	EventReleased    EventKind = "RELEASED"
)

// StatusEvent is one appended act on a contribution.
type StatusEvent struct {
	Seq        int
	Kind       EventKind
	Status     Status
	Reason     string
	RecordedAt time.Time
	RecordedBy id.UserID
}

// SourceKey is the natural key of a source event: its identifier in the
// source system and the version of it this is.
type SourceKey struct {
	EventID string
	Version int
}

// String renders the key for a message or a lock.
func (k SourceKey) String() string { return fmt.Sprintf("%s#%d", k.EventID, k.Version) }

// Bounds on the free-text fields.
const (
	MaxRef    = 200
	MaxReason = 2000
)

var (
	periodShape   = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)
	currencyShape = regexp.MustCompile(`^[A-Z]{3}$`)
	decimalShape  = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)
	checksumShape = regexp.MustCompile(`^[A-Za-z0-9:_+/=.-]{1,200}$`)
)

// ValidPeriod reports whether p is a period, YYYY-MM.
func ValidPeriod(p string) bool { return periodShape.MatchString(p) }

// ParseAmount reads a contribution base: a canonical decimal string, never
// a number, in the event's one currency.
func ParseAmount(field, amount, currency string) (fiscal.Money, error) {
	if !currencyShape.MatchString(currency) {
		return fiscal.Money{}, errs.Invalid("currency", errs.ReasonInvalidValue, "The currency is an ISO 4217 code, such as USD.")
	}
	if !decimalShape.MatchString(amount) {
		return fiscal.Money{}, errs.Invalid(field, errs.ReasonInvalidValue,
			"An amount is a canonical decimal string, such as \"1250.00\".")
	}
	m, err := fiscal.ParseMoney(amount, fiscal.Currency(currency))
	if err != nil {
		return fiscal.Money{}, errs.Invalid(field, errs.ReasonInvalidValue, "The amount is not representable exactly.")
	}
	return m, nil
}

// Contribution is one ContributionEvent: the provider's contribution base
// taken from one source event, folded with its status events.
type Contribution struct {
	ID       id.ContributionID
	TenantID id.TenantID

	Source         SourceKey
	SourceType     SourceType
	SourceChecksum string // the import file's checksum, when the event came in a file

	Regime      Regime
	Period      string // YYYY-MM
	LegalEntity id.LegalEntityID
	Obligation  id.ObligationID // zero when no obligation is named

	ClassificationRef string
	SitusRef          string // the jurisdiction the revenue is sited in
	SourceRef         string // the invoice, usage or GL reference
	ComponentRef      string // the classified component, when there is one
	RuleVersion       string // the rule or obligation version that decided eligibility

	EligibleBase    fiscal.Money
	ExcludedBase    fiscal.Money
	ExclusionReason string
	Quantity        *fiscal.Quantity

	// Reverses is the source event this contribution reverses, nil when it
	// is not a reversal. It names the source rather than a contribution so
	// that a reversal arriving before its original is still recorded; the
	// period summary reports it as incomplete until the original is known.
	Reverses *SourceKey

	// Amendment is true when the event was recorded after its period had
	// ended: a late event, counted in its own period and flagged.
	Amendment bool

	// Hash is the immutable hash: the canonical digest of every source field
	// above. Idempotency compares it, and the period digest is built from it.
	Hash canonical.Digest

	RecordedAt time.Time
	RecordedBy id.UserID

	Status  Status
	History []StatusEvent
}

// Currency is the event's one currency.
func (c Contribution) Currency() fiscal.Currency { return c.EligibleBase.Currency() }

// IsReversal reports whether this contribution reverses another.
func (c Contribution) IsReversal() bool { return c.Reverses != nil }

// Payload is the canonical document the immutable hash covers. Identity,
// recording time and status are not part of it: they say when and by whom
// the event was recorded, not what the source said.
func (c Contribution) Payload() canonical.Value {
	opt := func(s string) canonical.Value { return canonical.OptString(s) }
	obligation := canonical.Absent()
	if !c.Obligation.IsZero() {
		obligation = canonical.String(c.Obligation.String())
	}
	quantity, unit := canonical.Absent(), canonical.Absent()
	if c.Quantity != nil {
		quantity, unit = canonical.Quantity(*c.Quantity), canonical.String(string(c.Quantity.Unit()))
	}
	reverses := canonical.Absent()
	if c.Reverses != nil {
		reverses = canonical.Object(
			canonical.F("sourceEventId", canonical.String(c.Reverses.EventID)),
			canonical.F("sourceVersion", canonical.Integer(int64(c.Reverses.Version))),
		)
	}
	return canonical.Object(
		canonical.F("sourceEventId", canonical.String(c.Source.EventID)),
		canonical.F("sourceVersion", canonical.Integer(int64(c.Source.Version))),
		canonical.F("sourceType", canonical.String(string(c.SourceType))),
		canonical.F("sourceChecksum", opt(c.SourceChecksum)),
		canonical.F("regime", canonical.String(string(c.Regime))),
		canonical.F("period", canonical.String(c.Period)),
		canonical.F("legalEntityId", canonical.String(c.LegalEntity.String())),
		canonical.F("obligationId", obligation),
		canonical.F("classificationRef", canonical.String(c.ClassificationRef)),
		canonical.F("situsRef", canonical.String(c.SitusRef)),
		canonical.F("sourceRef", canonical.String(c.SourceRef)),
		canonical.F("componentRef", opt(c.ComponentRef)),
		canonical.F("ruleVersion", canonical.String(c.RuleVersion)),
		canonical.F("currency", canonical.String(string(c.Currency()))),
		canonical.F("eligibleBase", canonical.Money(c.EligibleBase)),
		canonical.F("excludedBase", canonical.Money(c.ExcludedBase)),
		canonical.F("exclusionReason", opt(c.ExclusionReason)),
		canonical.F("quantity", quantity),
		canonical.F("quantityUnit", unit),
		canonical.F("reverses", reverses),
	)
}

// Seal computes the immutable hash.
func (c *Contribution) Seal() error {
	d, err := canonical.Sum(c.Payload())
	if err != nil {
		return err
	}
	c.Hash = d
	return nil
}

// SamePayload reports whether two records of one source event say the same
// thing, by their immutable hashes.
func (c Contribution) SamePayload(other Contribution) bool { return c.Hash.Equal(other.Hash) }

func text(field, value string, required bool, max int) error {
	switch {
	case value == "" && required:
		return errs.Invalid(field, errs.ReasonMissingField, fmt.Sprintf("A contribution names its %s.", field))
	case len(value) > max:
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("%s is at most %d characters.", field, max))
	case value != strings.TrimSpace(value):
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("%s carries no leading or trailing space.", field))
	}
	return nil
}

// Validate refuses a contribution that cannot be recorded.
func (c Contribution) Validate() error {
	if err := text("sourceEventId", c.Source.EventID, true, MaxRef); err != nil {
		return err
	}
	if c.Source.Version < 1 {
		return errs.Invalid("sourceVersion", errs.ReasonInvalidValue, "A source event's version is 1 or more.")
	}
	if !c.SourceType.Valid() {
		return errs.Invalid("sourceType", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a source type OBL-06 names.", c.SourceType))
	}
	if c.SourceChecksum != "" && !checksumShape.MatchString(c.SourceChecksum) {
		return errs.Invalid("sourceChecksum", errs.ReasonInvalidValue, "A source checksum is up to 200 characters of a digest's alphabet.")
	}
	if !c.Regime.Valid() {
		return errs.Invalid("regime", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a contribution regime this service knows.", c.Regime))
	}
	if !ValidPeriod(c.Period) {
		return errs.Invalid("period", errs.ReasonInvalidValue, "The period is a month, YYYY-MM.")
	}
	if c.LegalEntity.IsZero() {
		return errs.Invalid("legalEntityId", errs.ReasonMissingField, "A contribution belongs to a member legal entity.")
	}
	for _, f := range []struct {
		name, value string
		required    bool
	}{
		{"classificationRef", c.ClassificationRef, true},
		{"situsRef", c.SitusRef, true},
		{"sourceRef", c.SourceRef, true},
		{"componentRef", c.ComponentRef, false},
		{"ruleVersion", c.RuleVersion, true},
	} {
		if err := text(f.name, f.value, f.required, MaxRef); err != nil {
			return err
		}
	}
	if err := text("exclusionReason", c.ExclusionReason, false, MaxReason); err != nil {
		return err
	}
	cur := c.EligibleBase.Currency()
	if !currencyShape.MatchString(string(cur)) || c.ExcludedBase.Currency() != cur {
		return errs.Invalid("currency", errs.ReasonInvalidValue, "A contribution carries both bases in its one currency.")
	}
	if !c.ExcludedBase.IsZero() && c.ExclusionReason == "" {
		return errs.Invalid("exclusionReason", errs.ReasonMissingField,
			"An excluded base says why it is excluded.")
	}
	if c.Quantity != nil && strings.TrimSpace(string(c.Quantity.Unit())) == "" {
		return errs.Invalid("quantityUnit", errs.ReasonMissingField, "A quantity names its unit.")
	}
	el, ex := c.EligibleBase.Sign(), c.ExcludedBase.Sign()
	if c.Reverses == nil {
		if el < 0 || ex < 0 {
			return errs.Invalid("eligibleBase", errs.ReasonInvalidValue,
				"A contribution base is not negative. A negative contribution is a reversal, and names what it reverses.")
		}
		return nil
	}
	if err := text("reverses.sourceEventId", c.Reverses.EventID, true, MaxRef); err != nil {
		return err
	}
	if c.Reverses.Version < 1 {
		return errs.Invalid("reverses.sourceVersion", errs.ReasonInvalidValue, "A source event's version is 1 or more.")
	}
	if *c.Reverses == c.Source {
		return errs.Invalid("reverses", errs.ReasonInvalidValue, "A source event does not reverse itself.")
	}
	if el > 0 || ex > 0 || (el == 0 && ex == 0) {
		return errs.Invalid("eligibleBase", errs.ReasonInvalidValue,
			"A reversal is a negative contribution: its bases are zero or negative, and not both zero.")
	}
	return nil
}

// CheckReversal refuses a reversal its original does not admit: a reversal
// is of a contribution that is not itself a reversal, for the same member
// entity, regime and currency, and for no more than the original was.
func (c Contribution) CheckReversal(original Contribution) error {
	switch {
	case original.IsReversal():
		return errs.Invalid("reverses", errs.ReasonInvalidValue, "A reversal is not itself reversed; record a new contribution instead.")
	case original.LegalEntity != c.LegalEntity:
		return errs.Invalid("reverses", errs.ReasonInvalidValue, "A reversal is for the same member legal entity as its original.")
	case original.Regime != c.Regime:
		return errs.Invalid("reverses", errs.ReasonInvalidValue, "A reversal is in the same regime as its original.")
	case original.Currency() != c.Currency():
		return errs.Invalid("reverses", errs.ReasonInvalidValue, "A reversal is in the same currency as its original.")
	}
	for _, pair := range []struct {
		field     string
		rev, orig fiscal.Money
	}{{"eligibleBase", c.EligibleBase, original.EligibleBase}, {"excludedBase", c.ExcludedBase, original.ExcludedBase}} {
		cmp, err := pair.rev.Neg().Cmp(pair.orig)
		if err != nil {
			return errs.Invalid(pair.field, errs.ReasonInvalidValue, "A reversal is in the same currency as its original.")
		}
		if cmp > 0 {
			return errs.Invalid(pair.field, errs.ReasonInvalidValue, "A reversal reverses no more than its original contributed.")
		}
	}
	return nil
}

// AmendmentAt reports whether an event for period recorded at `at` is late:
// recorded once the period's month has ended, in UTC.
func AmendmentAt(period string, at time.Time) bool {
	start, err := time.Parse("2006-01", period)
	if err != nil {
		return false
	}
	return !at.UTC().Before(start.AddDate(0, 1, 0))
}

// Fold applies a contribution's status events in order.
func Fold(c Contribution, events []StatusEvent) (Contribution, error) {
	if len(events) == 0 || events[0].Kind != EventRecorded || events[0].Seq != 1 || events[0].Status != StatusAccepted {
		return Contribution{}, fmt.Errorf("contribution: a contribution's history starts with its RECORDED event")
	}
	c.Status = StatusAccepted
	for i, e := range events[1:] {
		if e.Seq != i+2 {
			return Contribution{}, fmt.Errorf("contribution: status event %d is out of sequence", e.Seq)
		}
		switch {
		case e.Kind == EventQuarantined && c.Status == StatusAccepted && e.Status == StatusQuarantined:
		case e.Kind == EventReleased && c.Status == StatusQuarantined && e.Status == StatusAccepted:
		default:
			return Contribution{}, fmt.Errorf("contribution: %s from %s to %s", e.Kind, c.Status, e.Status)
		}
		c.Status = e.Status
	}
	c.History = slices.Clone(events)
	return c, nil
}

// Recorded is the first status event of a contribution.
func Recorded(by id.UserID, at time.Time) StatusEvent {
	return StatusEvent{Seq: 1, Kind: EventRecorded, Status: StatusAccepted, RecordedAt: at.UTC(), RecordedBy: by}
}

// Quarantine holds an accepted event out of the control totals, with the
// reason. The event and its history stay exactly as they were.
func (c Contribution) Quarantine(reason string, by id.UserID, at time.Time) (StatusEvent, error) {
	if c.Status != StatusAccepted {
		return StatusEvent{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only an accepted contribution is quarantined; this one is %s.", c.Status))
	}
	if err := reasonText(reason); err != nil {
		return StatusEvent{}, err
	}
	return StatusEvent{Seq: len(c.History) + 1, Kind: EventQuarantined, Status: StatusQuarantined, Reason: reason,
		RecordedAt: at.UTC(), RecordedBy: by}, nil
}

// Release returns a quarantined event to the control totals. Four eyes: the
// person who quarantined it does not release it.
func (c Contribution) Release(reason string, by id.UserID, at time.Time) (StatusEvent, error) {
	if c.Status != StatusQuarantined {
		return StatusEvent{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only a quarantined contribution is released; this one is %s.", c.Status))
	}
	if q := c.History[len(c.History)-1]; q.RecordedBy == by {
		return StatusEvent{}, errs.New(errs.CategoryPolicy, ReasonMakerChecker,
			"The person who quarantined this contribution cannot also release it. Another person must.")
	}
	if err := reasonText(reason); err != nil {
		return StatusEvent{}, err
	}
	return StatusEvent{Seq: len(c.History) + 1, Kind: EventReleased, Status: StatusAccepted, Reason: reason,
		RecordedAt: at.UTC(), RecordedBy: by}, nil
}

func reasonText(reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errs.Invalid("reason", errs.ReasonMissingField, "A quarantine or a release gives its reason.")
	}
	if len(reason) > MaxReason {
		return errs.Invalid("reason", errs.ReasonInvalidValue, fmt.Sprintf("A reason is at most %d characters.", MaxReason))
	}
	return nil
}

// Filter narrows a listing. Empty fields do not filter.
type Filter struct {
	Period      string
	Regime      Regime
	Status      Status
	LegalEntity id.LegalEntityID
	SourceType  SourceType
	Limit       int
}
