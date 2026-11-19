// Package workpaper models regulatory workpapers: the deterministic
// assembly of a legal filer's tax or regulatory base for one duty and period,
// with its schedules, its reconciliation and the controls around them
// (OBL-07 Workpaper Assembly & Tax/Regulatory Base Ledger; LED-19 tie-out;
// OPS-15 exception triage).
//
// A workpaper is a header — duty, period, authority, legal filer, reporting
// currency, content release — and an append-only history. Four rules shape
// the package:
//
//   - A build is a pure function. Assemble reads the provider contribution
//     ledger and the filer's subledger balances, applies the documented FX
//     rates and the approved adjustments, and records its inputs hash, its
//     output and the output's hash. The same inputs build the same document,
//     so building again changes nothing; different inputs are a new build
//     version, never an edit of an old one.
//   - Nothing is assumed. A period with no source event, a held import, a
//     source currency with no documented rate and a total that does not tie
//     are exceptions, and an open exception keeps the workpaper out of
//     READY_FOR_REVIEW (OBL-07 "Source missing", "Currency mismatch").
//   - Two people. A material adjustment, the resolution of an exception and
//     the approval of a frozen build are each decided by someone other than
//     the person who proposed, resolved or froze it. Every adjustment and
//     resolution names its evidence by digest.
//   - Approval is of exact content. Freezing locks the source snapshot: the
//     build the reviewer sees is the build that is approved. A changed basis
//     or a rebuild with changed inputs revokes the approval (OBL-07 "Formula
//     changed"); the approval stays in the history.
//
// Every amount the package computes is a fiscal.Money. A build's stored
// document carries the canonical decimal strings those amounts render to.
package workpaper

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/compliance"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/contribution"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The package's own reason codes. Four-eye refusals reuse compliance's
// SEGREGATION_OF_DUTIES: it is the same rule, compared the same way.
var (
	// ReasonWorkpaperExists is a second workpaper for one filer, duty and
	// period with a different header.
	ReasonWorkpaperExists = errs.Register("WORKPAPER_EXISTS", errs.CategoryConflict,
		"obl-07",
		"A workpaper for this legal filer, duty and period already exists with a different header. There is one workpaper per filer, duty and period.")
	// ReasonSnapshotLocked is a change to a frozen workpaper's inputs.
	ReasonSnapshotLocked = errs.Register("WORKPAPER_SNAPSHOT_LOCKED", errs.CategoryConflict,
		"obl-07",
		"The workpaper is frozen for review and its source snapshot is locked. Return it to draft before rebuilding or changing its inputs.")
	// ReasonNotReady is a freeze the workpaper does not admit yet.
	ReasonNotReady = errs.Register("WORKPAPER_NOT_READY", errs.CategoryPolicy,
		"obl-07",
		"The workpaper cannot reach READY_FOR_REVIEW: a build is missing or out of date, an exception is unresolved, or an adjustment or resolution awaits its second person.")
	// ReasonFormulaChanged is an approval of a build assembled under a
	// formula this service no longer runs.
	ReasonFormulaChanged = errs.Register("WORKPAPER_FORMULA_CHANGED", errs.CategoryConflict,
		"obl-07",
		"The build was assembled under a formula this service no longer runs. Rebuild it; the approval of the old build is not carried over.")
	// ReasonStale is an act decided against a history somebody has since
	// extended.
	ReasonStale = errs.Register("WORKPAPER_STALE", errs.CategoryConflict,
		"obl-07",
		"The workpaper changed since it was read. Reload it and decide again.")
)

// ReasonSegregationOfDuties is the four-eye refusal, shared with compliance.
var ReasonSegregationOfDuties = compliance.ReasonSegregationOfDuties

// Status is where a workpaper stands (Document 08 §04).
type Status string

// The statuses.
const (
	StatusDraft          Status = "DRAFT"
	StatusDataIncomplete Status = "DATA_INCOMPLETE"
	StatusReadyForReview Status = "READY_FOR_REVIEW"
	StatusApproved       Status = "APPROVED"
	StatusRevoked        Status = "REVOKED"
)

// Valid reports whether the status is one of the five.
func (s Status) Valid() bool {
	switch s {
	case StatusDraft, StatusDataIncomplete, StatusReadyForReview, StatusApproved, StatusRevoked:
		return true
	}
	return false
}

// Open reports whether the workpaper's inputs may change: it is neither
// frozen for review nor approved.
func (s Status) Open() bool {
	return s == StatusDraft || s == StatusDataIncomplete || s == StatusRevoked
}

// EventKind is what an event records.
type EventKind string

// The event kinds. CREATED is the first and only the first.
const (
	EventCreated            EventKind = "CREATED"
	EventBuilt              EventKind = "BUILT"
	EventApprovalRevoked    EventKind = "APPROVAL_REVOKED"
	EventBasisChanged       EventKind = "BASIS_CHANGED"
	EventFXDocumented       EventKind = "FX_DOCUMENTED"
	EventAdjustmentProposed EventKind = "ADJUSTMENT_PROPOSED"
	EventAdjustmentDecided  EventKind = "ADJUSTMENT_DECIDED"
	EventResolutionProposed EventKind = "RESOLUTION_PROPOSED"
	EventResolutionDecided  EventKind = "RESOLUTION_DECIDED"
	EventFrozen             EventKind = "FROZEN"
	EventReturned           EventKind = "RETURNED"
	EventApproved           EventKind = "APPROVED"
)

// AdjustmentKind is what an adjustment is.
type AdjustmentKind string

// The kinds. An allowance is a positive deduction from the base the rule
// permits; an audited adjustment is a signed correction with its evidence.
const (
	KindAllowance  AdjustmentKind = "PERMITTED_ALLOWANCE"
	KindAdjustment AdjustmentKind = "AUDITED_ADJUSTMENT"
)

// Valid reports whether the kind is one of the two.
func (k AdjustmentKind) Valid() bool { return k == KindAllowance || k == KindAdjustment }

// Decision is a second person's answer to a proposal.
type Decision string

// The decisions.
const (
	DecisionApproved Decision = "APPROVED"
	DecisionRejected Decision = "REJECTED"
)

// Valid reports whether the decision is one of the two.
func (d Decision) Valid() bool { return d == DecisionApproved || d == DecisionRejected }

// AdjustmentState is where an adjustment stands.
type AdjustmentState string

// The states. A non-material adjustment is APPROVED when it is proposed; a
// material one waits for a second person.
const (
	AdjustmentProposed AdjustmentState = "PROPOSED"
	AdjustmentApproved AdjustmentState = "APPROVED"
	AdjustmentRejected AdjustmentState = "REJECTED"
)

// ResolutionState is where an exception resolution stands.
type ResolutionState string

// The states.
const (
	ResolutionProposed  ResolutionState = "PROPOSED"
	ResolutionConfirmed ResolutionState = "CONFIRMED"
	ResolutionRejected  ResolutionState = "REJECTED"
)

// Evidence is a document named by its SHA-256, computed where the document
// is. The document itself is not uploaded or stored here.
type Evidence struct {
	SHA256    string
	Filename  string
	MediaType string
}

// FXRate is a documented conversion of one source currency into the
// reporting currency: the rate, where it came from, the date it is for, and
// the conversion basis — how converted amounts are rounded and why.
type FXRate struct {
	Currency     fiscal.Currency
	Rate         fiscal.Rate
	Source       string
	RateDate     time.Time
	RoundingMode fiscal.RoundingMode
	Scale        int32
	Basis        string
	RecordedAt   time.Time
	RecordedBy   id.UserID
}

// Policy is the rounding the documented conversion basis names. Each
// schedule line is converted once and rounded once.
func (r FXRate) Policy() (fiscal.RoundingPolicy, error) {
	body, err := json.Marshal(struct {
		Mode  fiscal.RoundingMode  `json:"mode"`
		Scale int32                `json:"scale"`
		Basis fiscal.RoundingBasis `json:"basis"`
	}{r.RoundingMode, r.Scale, fiscal.BasisLine})
	if err != nil {
		return fiscal.RoundingPolicy{}, err
	}
	return fiscal.DecodeRoundingPolicy(body)
}

// Adjustment is a permitted allowance or an audited adjustment, folded from
// its proposal and decision.
type Adjustment struct {
	Number   int
	Kind     AdjustmentKind
	Category string
	// Amount is in the reporting currency. An allowance is positive and
	// deducted; an adjustment is signed and added.
	Amount   fiscal.Money
	Reason   string
	Evidence Evidence
	Material bool
	State    AdjustmentState

	ProposedAt   time.Time
	ProposedBy   id.UserID
	DecidedAt    time.Time
	DecidedBy    id.UserID
	DecisionNote string
}

// Resolution is an evidence-linked explanation of one exception, confirmed
// or rejected by a second person.
type Resolution struct {
	ExceptionKey string
	Fingerprint  string
	Explanation  string
	Evidence     Evidence
	State        ResolutionState

	ProposedAt   time.Time
	ProposedBy   id.UserID
	DecidedAt    time.Time
	DecidedBy    id.UserID
	DecisionNote string
}

// Event is one act on a workpaper. The fields a kind does not use are zero.
type Event struct {
	Seq  int
	Kind EventKind
	// Status is the workpaper's status after this event.
	Status Status
	Note   string

	BuildVersion   int
	ContentRelease string
	FX             *FXRate

	AdjustmentNumber   int
	AdjustmentKind     AdjustmentKind
	AdjustmentCategory string
	AdjustmentAmount   *fiscal.Money

	Decision     Decision
	ExceptionKey string
	Fingerprint  string
	Evidence     *Evidence

	RecordedAt time.Time
	RecordedBy id.UserID
}

// Build is one build version: what it read, what it produced, and who ran
// it. Output is nil when only the build's summary was loaded.
type Build struct {
	Version        int
	InputsHash     canonical.Digest
	OutputHash     canonical.Digest
	BasisDigest    canonical.Digest
	FormulaVersion string
	ContentRelease string
	BuiltAt        time.Time
	BuiltBy        id.UserID
	Output         *Output
}

// Workpaper is one workpaper, folded from its events.
type Workpaper struct {
	ID       id.WorkpaperID
	TenantID id.TenantID

	// The header (OBL-07 01): the legal filer, the duty and the period it is
	// for, the authority it is reported to, the reporting currency, and the
	// content release it was opened under.
	LegalEntity       id.LegalEntityID
	Duty              contribution.Regime
	Period            string
	Authority         string
	ReportingCurrency fiscal.Currency
	// Materiality is the threshold, in the reporting currency, at or above
	// which an adjustment needs a second person.
	Materiality    fiscal.Money
	Obligation     id.ObligationID
	InitialRelease string

	CreatedAt time.Time
	CreatedBy id.UserID

	Status  Status
	History []Event
	// Builds are oldest first. Only the latest carries its Output.
	Builds []Build

	// Folded from the history.
	ContentRelease string
	FX             []FXRate
	Adjustments    []Adjustment
	Resolutions    []Resolution
	FrozenBuild    int
}

// Bounds on the free-text fields.
const (
	MaxRef  = 200
	MaxText = 2000
)

var (
	periodShape    = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)
	currencyShape  = regexp.MustCompile(`^[A-Z]{3}$`)
	sha256Shape    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	mediaTypeShape = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]{0,63}/[a-z0-9][a-z0-9!#$&^_.+-]{0,126}$`)
	decimalShape   = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)
)

// ValidPeriod reports whether p is a period, YYYY-MM.
func ValidPeriod(p string) bool { return periodShape.MatchString(p) }

// ValidCurrency reports whether c is an ISO 4217 code.
func ValidCurrency(c string) bool { return currencyShape.MatchString(c) }

// ParseAmount reads an amount: a canonical decimal string, never a number.
func ParseAmount(field, amount string, currency fiscal.Currency) (fiscal.Money, error) {
	if !decimalShape.MatchString(amount) {
		return fiscal.Money{}, errs.Invalid(field, errs.ReasonInvalidValue, "An amount is a canonical decimal string, such as \"1250.00\".")
	}
	m, err := fiscal.ParseMoney(amount, currency)
	if err != nil {
		return fiscal.Money{}, errs.Invalid(field, errs.ReasonInvalidValue, "The amount is not representable exactly.")
	}
	return m, nil
}

func text(field, value string, required bool, limit int) error {
	switch {
	case strings.TrimSpace(value) == "" && required:
		return errs.Invalid(field, errs.ReasonMissingField, fmt.Sprintf("%s is required.", field))
	case len(value) > limit:
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("%s is at most %d characters.", field, limit))
	case value != strings.TrimSpace(value):
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("%s carries no leading or trailing space.", field))
	}
	return nil
}

// Validate refuses a header that cannot be recorded.
func (w Workpaper) Validate() error {
	switch {
	case w.LegalEntity.IsZero():
		return errs.Invalid("legalEntityId", errs.ReasonMissingField, "A workpaper belongs to a legal filer.")
	case !w.Duty.Valid():
		return errs.Invalid("duty", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a duty this service assembles.", w.Duty))
	case !ValidPeriod(w.Period):
		return errs.Invalid("period", errs.ReasonInvalidValue, "The period is a month, YYYY-MM.")
	case !ValidCurrency(string(w.ReportingCurrency)):
		return errs.Invalid("reportingCurrency", errs.ReasonInvalidValue, "The reporting currency is an ISO 4217 code, such as USD.")
	case w.Materiality.Currency() != w.ReportingCurrency:
		return errs.Invalid("materialityThreshold", errs.ReasonInvalidValue, "The materiality threshold is in the reporting currency.")
	case w.Materiality.Sign() < 0:
		return errs.Invalid("materialityThreshold", errs.ReasonInvalidValue, "The materiality threshold is zero or more.")
	}
	if err := text("authority", w.Authority, true, MaxRef); err != nil {
		return err
	}
	return text("contentRelease", w.InitialRelease, true, MaxRef)
}

// SameHeader reports whether two workpapers of one filer, duty and period
// were opened with the same header, so a repeated create returns the one
// already made.
func (w Workpaper) SameHeader(other Workpaper) bool {
	cmp, err := w.Materiality.Cmp(other.Materiality)
	return w.LegalEntity == other.LegalEntity && w.Duty == other.Duty && w.Period == other.Period &&
		w.Authority == other.Authority && w.ReportingCurrency == other.ReportingCurrency && err == nil && cmp == 0 &&
		w.Obligation == other.Obligation && w.InitialRelease == other.InitialRelease
}

// Created is the first event of a workpaper.
func Created(by id.UserID, at time.Time) Event {
	return Event{Seq: 1, Kind: EventCreated, Status: StatusDraft, RecordedAt: at.UTC(), RecordedBy: by}
}

// Fold applies a workpaper's events in order. builds are its builds, oldest
// first.
func Fold(w Workpaper, events []Event, builds []Build) (Workpaper, error) {
	if len(events) == 0 || events[0].Kind != EventCreated || events[0].Seq != 1 {
		return Workpaper{}, fmt.Errorf("workpaper: a workpaper's history starts with its CREATED event")
	}
	w.ContentRelease, w.Builds = w.InitialRelease, slices.Clone(builds)
	w.FX, w.Adjustments, w.Resolutions, w.FrozenBuild = nil, nil, nil, 0
	for i, e := range events {
		if e.Seq != i+1 {
			return Workpaper{}, fmt.Errorf("workpaper: event %d is out of sequence", e.Seq)
		}
		if !e.Status.Valid() {
			return Workpaper{}, fmt.Errorf("workpaper: event %d leaves status %q", e.Seq, e.Status)
		}
		switch e.Kind {
		case EventCreated:
			if i != 0 {
				return Workpaper{}, fmt.Errorf("workpaper: a second CREATED event")
			}
		case EventBuilt:
			if e.BuildVersion < 1 || e.BuildVersion > len(builds) {
				return Workpaper{}, fmt.Errorf("workpaper: event %d names build %d, which is not stored", e.Seq, e.BuildVersion)
			}
		case EventBasisChanged:
			w.ContentRelease = e.ContentRelease
		case EventFXDocumented:
			if e.FX == nil {
				return Workpaper{}, fmt.Errorf("workpaper: event %d documents no rate", e.Seq)
			}
			fx := *e.FX
			fx.RecordedAt, fx.RecordedBy = e.RecordedAt, e.RecordedBy
			w.FX = append(w.FX, fx)
		case EventAdjustmentProposed:
			if e.AdjustmentAmount == nil || e.Evidence == nil {
				return Workpaper{}, fmt.Errorf("workpaper: event %d proposes an incomplete adjustment", e.Seq)
			}
			a := Adjustment{Number: e.AdjustmentNumber, Kind: e.AdjustmentKind, Category: e.AdjustmentCategory,
				Amount: *e.AdjustmentAmount, Reason: e.Note, Evidence: *e.Evidence, ProposedAt: e.RecordedAt, ProposedBy: e.RecordedBy}
			a.Material = w.material(a.Amount)
			a.State = AdjustmentProposed
			if !a.Material {
				a.State = AdjustmentApproved
			}
			w.Adjustments = append(w.Adjustments, a)
		case EventAdjustmentDecided:
			n := e.AdjustmentNumber - 1
			if n < 0 || n >= len(w.Adjustments) {
				return Workpaper{}, fmt.Errorf("workpaper: event %d decides adjustment %d, which was not proposed", e.Seq, e.AdjustmentNumber)
			}
			a := &w.Adjustments[n]
			a.State = AdjustmentApproved
			if e.Decision == DecisionRejected {
				a.State = AdjustmentRejected
			}
			a.DecidedAt, a.DecidedBy, a.DecisionNote = e.RecordedAt, e.RecordedBy, e.Note
		case EventResolutionProposed:
			if e.Evidence == nil {
				return Workpaper{}, fmt.Errorf("workpaper: event %d proposes a resolution with no evidence", e.Seq)
			}
			w.Resolutions = append(w.Resolutions, Resolution{ExceptionKey: e.ExceptionKey, Fingerprint: e.Fingerprint,
				Explanation: e.Note, Evidence: *e.Evidence, State: ResolutionProposed, ProposedAt: e.RecordedAt, ProposedBy: e.RecordedBy})
		case EventResolutionDecided:
			r := w.pendingResolution(e.ExceptionKey)
			if r == nil {
				return Workpaper{}, fmt.Errorf("workpaper: event %d decides no pending resolution of %s", e.Seq, e.ExceptionKey)
			}
			r.State = ResolutionConfirmed
			if e.Decision == DecisionRejected {
				r.State = ResolutionRejected
			}
			r.DecidedAt, r.DecidedBy, r.DecisionNote = e.RecordedAt, e.RecordedBy, e.Note
		case EventFrozen:
			w.FrozenBuild = e.BuildVersion
		case EventReturned:
			w.FrozenBuild = 0
		case EventApprovalRevoked, EventApproved:
		default:
			return Workpaper{}, fmt.Errorf("workpaper: event %d has kind %q", e.Seq, e.Kind)
		}
		if !e.Status.lockedFrozen() {
			w.FrozenBuild = 0
		}
		w.Status = e.Status
	}
	w.History = slices.Clone(events)
	return w, nil
}

func (s Status) lockedFrozen() bool { return s == StatusReadyForReview || s == StatusApproved }

func (w Workpaper) material(amount fiscal.Money) bool {
	abs := amount
	if amount.Sign() < 0 {
		abs = amount.Neg()
	}
	c, err := abs.Cmp(w.Materiality)
	return err != nil || c >= 0
}

func (w *Workpaper) pendingResolution(key string) *Resolution {
	for i := len(w.Resolutions) - 1; i >= 0; i-- {
		if w.Resolutions[i].ExceptionKey == key && w.Resolutions[i].State == ResolutionProposed {
			return &w.Resolutions[i]
		}
	}
	return nil
}

// Latest returns the latest build, or nil before the first.
func (w Workpaper) Latest() *Build {
	if len(w.Builds) == 0 {
		return nil
	}
	return &w.Builds[len(w.Builds)-1]
}

// CurrentFX is the documented rate in force for each source currency — the
// latest documented — sorted by currency.
func (w Workpaper) CurrentFX() []FXRate {
	by := map[fiscal.Currency]FXRate{}
	for _, r := range w.FX {
		by[r.Currency] = r
	}
	out := make([]FXRate, 0, len(by))
	for _, r := range by {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b FXRate) int { return strings.Compare(string(a.Currency), string(b.Currency)) })
	return out
}

// BasisDigest is the digest of the part of a build's inputs the preparer
// controls: the content release, the documented rates in force and the
// approved adjustments. A freeze compares it with the latest build's, so a
// change since that build means rebuild first.
func (w Workpaper) BasisDigest() (canonical.Digest, error) {
	fx := w.CurrentFX()
	rates := make([]canonical.Value, len(fx))
	for i, r := range fx {
		rates[i] = canonical.Object(
			canonical.F("currency", canonical.String(string(r.Currency))),
			canonical.F("rate", canonical.Rate(r.Rate)),
			canonical.F("source", canonical.String(r.Source)),
			canonical.F("rateDate", canonical.String(r.RateDate.Format(time.DateOnly))),
			canonical.F("roundingMode", canonical.String(string(r.RoundingMode))),
			canonical.F("scale", canonical.Integer(int64(r.Scale))),
			canonical.F("basis", canonical.String(r.Basis)),
		)
	}
	var adjustments []canonical.Value
	for _, a := range w.Adjustments {
		if a.State != AdjustmentApproved {
			continue
		}
		adjustments = append(adjustments, canonical.Object(
			canonical.F("number", canonical.Integer(int64(a.Number))),
			canonical.F("kind", canonical.String(string(a.Kind))),
			canonical.F("category", canonical.String(a.Category)),
			canonical.F("amount", canonical.Money(a.Amount)),
			canonical.F("evidenceSha256", canonical.String(a.Evidence.SHA256)),
		))
	}
	return canonical.Sum(canonical.Object(
		canonical.F("contentRelease", canonical.String(w.ContentRelease)),
		canonical.F("fx", canonical.Array(rates...)),
		canonical.F("adjustments", canonical.Array(adjustments...)),
	))
}

// ExceptionState is one exception of the latest build and where its
// resolution stands.
type ExceptionState struct {
	Exception
	// Resolution is the latest resolution of this exception with the same
	// fingerprint, nil when there is none.
	Resolution *Resolution
}

// Resolved reports whether the exception has a confirmed resolution of this
// very difference.
func (e ExceptionState) Resolved() bool {
	return e.Resolution != nil && e.Resolution.State == ResolutionConfirmed
}

// Pending reports whether a resolution awaits its second person.
func (e ExceptionState) Pending() bool {
	return e.Resolution != nil && e.Resolution.State == ResolutionProposed
}

// Exceptions are the latest build's exceptions with their resolutions.
func (w Workpaper) Exceptions() []ExceptionState {
	b := w.Latest()
	if b == nil || b.Output == nil {
		return nil
	}
	out := make([]ExceptionState, len(b.Output.Exceptions))
	for i, e := range b.Output.Exceptions {
		out[i] = ExceptionState{Exception: e}
		for j := len(w.Resolutions) - 1; j >= 0; j-- {
			r := w.Resolutions[j]
			if r.ExceptionKey == e.Key && r.Fingerprint == e.Fingerprint {
				out[i].Resolution = &r
				break
			}
		}
	}
	return out
}

// Blocker is a reason a workpaper cannot be frozen for review yet.
type Blocker string

// The blockers.
const (
	BlockerNoBuild           Blocker = "NO_BUILD"
	BlockerBasisChanged      Blocker = "BASIS_CHANGED_SINCE_BUILD"
	BlockerFormulaChanged    Blocker = "FORMULA_CHANGED"
	BlockerExceptionOpen     Blocker = "EXCEPTION_UNRESOLVED"
	BlockerAdjustmentPending Blocker = "ADJUSTMENT_PENDING"
	BlockerResolutionPending Blocker = "RESOLUTION_PENDING"
)

// Blockers are the reasons the workpaper cannot reach READY_FOR_REVIEW now,
// in a fixed order. Empty for a workpaper that is not open.
func (w Workpaper) Blockers() []Blocker {
	if !w.Status.Open() {
		return nil
	}
	var out []Blocker
	b := w.Latest()
	if b == nil {
		return []Blocker{BlockerNoBuild}
	}
	if d, err := w.BasisDigest(); err != nil || !d.Equal(b.BasisDigest) {
		out = append(out, BlockerBasisChanged)
	}
	if b.FormulaVersion != FormulaVersion {
		out = append(out, BlockerFormulaChanged)
	}
	open, pending := false, false
	for _, e := range w.Exceptions() {
		if !e.Resolved() {
			open = true
		}
		if e.Pending() {
			pending = true
		}
	}
	if open {
		out = append(out, BlockerExceptionOpen)
	}
	if slices.ContainsFunc(w.Adjustments, func(a Adjustment) bool { return a.State == AdjustmentProposed }) {
		out = append(out, BlockerAdjustmentPending)
	}
	if pending {
		out = append(out, BlockerResolutionPending)
	}
	return out
}

// settled is the status an open workpaper has given its latest build and
// resolutions: DATA_INCOMPLETE while an exception of the latest build is
// unresolved, DRAFT otherwise.
func (w Workpaper) settled() Status {
	for _, e := range w.Exceptions() {
		if !e.Resolved() {
			return StatusDataIncomplete
		}
	}
	return StatusDraft
}

// ---------------------------------------------------------------------------
// acts
// ---------------------------------------------------------------------------

func (w Workpaper) next(kind EventKind, to Status, by id.UserID, at time.Time) (Event, error) {
	if by.IsZero() {
		return Event{}, errs.New(errs.CategoryPolicy, errs.ReasonForbidden, "Every workpaper event is recorded against a person.")
	}
	return Event{Seq: len(w.History) + 1, Kind: kind, Status: to, RecordedAt: at.UTC(), RecordedBy: by}, nil
}

func (w Workpaper) open(what string) error {
	if w.Status.Open() {
		return nil
	}
	return errs.New(errs.CategoryConflict, ReasonSnapshotLocked,
		fmt.Sprintf("The workpaper is %s; %s waits until it is returned to draft or its approval is revoked.", w.Status, what))
}

func (w Workpaper) with(events ...Event) Workpaper {
	cur := w
	cur.History = append(slices.Clone(w.History), events...)
	if len(events) > 0 {
		cur.Status = events[len(events)-1].Status
	}
	return cur
}

func note(field, v string, required bool) (string, error) {
	v = strings.TrimSpace(v)
	if err := text(field, v, required, MaxText); err != nil {
		return "", err
	}
	return v, nil
}

// CheckEvidence normalises and checks a document's digest, name and type.
func CheckEvidence(e *Evidence) (Evidence, error) {
	if e == nil {
		return Evidence{}, errs.Invalid("evidence", errs.ReasonMissingField,
			"An adjustment or a resolution names its evidence: the document's SHA-256, file name and media type.")
	}
	out := Evidence{SHA256: strings.ToLower(strings.TrimSpace(e.SHA256)), Filename: strings.TrimSpace(e.Filename),
		MediaType: strings.ToLower(strings.TrimSpace(e.MediaType))}
	switch {
	case !sha256Shape.MatchString(out.SHA256):
		return Evidence{}, errs.Invalid("evidence.sha256", errs.ReasonInvalidValue, "The digest is the document's SHA-256 in lowercase hex.")
	case out.Filename == "" || len(out.Filename) > 255:
		return Evidence{}, errs.Invalid("evidence.filename", errs.ReasonInvalidValue, "The file name is 1 to 255 characters.")
	case !mediaTypeShape.MatchString(out.MediaType):
		return Evidence{}, errs.Invalid("evidence.mediaType", errs.ReasonInvalidValue, "The media type is type/subtype, such as application/pdf.")
	}
	return out, nil
}

// RecordBuild records a new build. The caller has already found that its
// inputs differ from the latest build's: building again on the same inputs
// records nothing. A rebuild of an approved workpaper revokes the approval
// first; a workpaper frozen for review is not rebuilt.
func (w Workpaper) RecordBuild(b Build, by id.UserID, at time.Time) ([]Event, error) {
	if w.Status == StatusReadyForReview {
		return nil, errs.New(errs.CategoryConflict, ReasonSnapshotLocked,
			"The workpaper is frozen for review: its source snapshot is locked. Return it to draft to rebuild.")
	}
	if b.Version != len(w.Builds)+1 {
		return nil, fmt.Errorf("workpaper: build %d recorded after %d builds", b.Version, len(w.Builds))
	}
	var events []Event
	cur := w
	if w.Status == StatusApproved {
		e, err := w.next(EventApprovalRevoked, StatusRevoked, by, at)
		if err != nil {
			return nil, err
		}
		e.BuildVersion = w.FrozenBuild
		e.Note = fmt.Sprintf("Rebuilt with changed inputs; the approval of build %d no longer stands.", w.FrozenBuild)
		events = append(events, e)
		cur = w.with(e)
	}
	cur.Builds = append(slices.Clone(w.Builds), b)
	e, err := cur.next(EventBuilt, cur.settled(), by, at)
	if err != nil {
		return nil, err
	}
	e.BuildVersion = b.Version
	return append(events, e), nil
}

// Freeze locks the latest build for review: the source snapshot it read is
// the one the reviewer approves. Only a draft with no blocker is frozen.
func (w Workpaper) Freeze(msg string, by id.UserID, at time.Time) (Event, error) {
	n, err := note("note", msg, false)
	if err != nil {
		return Event{}, err
	}
	if !w.Status.Open() {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only a draft is frozen for review; this workpaper is %s.", w.Status))
	}
	parts := []string{}
	for _, b := range w.Blockers() {
		parts = append(parts, string(b))
	}
	if len(parts) == 0 && w.Status != StatusDraft {
		parts = append(parts, string(w.Status))
	}
	if len(parts) > 0 {
		return Event{}, errs.New(errs.CategoryPolicy, ReasonNotReady,
			fmt.Sprintf("The workpaper cannot reach READY_FOR_REVIEW: %s.", strings.Join(parts, ", ")))
	}
	e, err := w.next(EventFrozen, StatusReadyForReview, by, at)
	if err != nil {
		return Event{}, err
	}
	e.BuildVersion, e.Note = w.Latest().Version, n
	return e, nil
}

// ReturnToDraft unlocks a workpaper frozen for review, with the reason.
func (w Workpaper) ReturnToDraft(msg string, by id.UserID, at time.Time) (Event, error) {
	n, err := note("note", msg, true)
	if err != nil {
		return Event{}, err
	}
	if w.Status != StatusReadyForReview {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only a workpaper frozen for review is returned to draft; this one is %s.", w.Status))
	}
	e, err := w.next(EventReturned, w.settled(), by, at)
	if err != nil {
		return Event{}, err
	}
	e.BuildVersion, e.Note = w.FrozenBuild, n
	return e, nil
}

// Approve is the reviewer's approval of the frozen build named. The reviewer
// neither froze it nor ran it, and the build was assembled under the
// formula this service runs.
func (w Workpaper) Approve(version int, msg string, by id.UserID, at time.Time) (Event, error) {
	n, err := note("note", msg, false)
	if err != nil {
		return Event{}, err
	}
	if w.Status != StatusReadyForReview {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only a workpaper frozen for review is approved; this one is %s.", w.Status))
	}
	if version != w.FrozenBuild {
		return Event{}, errs.New(errs.CategoryConflict, ReasonStale,
			fmt.Sprintf("Build %d is not the frozen build; build %d is.", version, w.FrozenBuild))
	}
	b := w.Builds[w.FrozenBuild-1]
	if b.FormulaVersion != FormulaVersion {
		return Event{}, errs.New(errs.CategoryConflict, ReasonFormulaChanged,
			fmt.Sprintf("Build %d was assembled under %s; this service runs %s.", b.Version, b.FormulaVersion, FormulaVersion))
	}
	for i := len(w.History) - 1; i >= 0; i-- {
		if e := w.History[i]; e.Kind == EventFrozen {
			if e.RecordedBy == by {
				return Event{}, errs.New(errs.CategoryPolicy, ReasonSegregationOfDuties,
					"The person who froze this workpaper cannot approve it; another person must.")
			}
			break
		}
	}
	if b.BuiltBy == by {
		return Event{}, errs.New(errs.CategoryPolicy, ReasonSegregationOfDuties,
			"The person who ran the frozen build cannot approve it; another person must.")
	}
	e, err := w.next(EventApproved, StatusApproved, by, at)
	if err != nil {
		return Event{}, err
	}
	e.BuildVersion, e.Note = version, n
	return e, nil
}

// ChangeBasis records a new content release — the formula or reporting
// basis the next build applies. It invalidates an approval (REVOKED) and
// unlocks a workpaper frozen for review; the next freeze needs a rebuild.
func (w Workpaper) ChangeBasis(release, msg string, by id.UserID, at time.Time) (Event, error) {
	release = strings.TrimSpace(release)
	if err := text("contentRelease", release, true, MaxRef); err != nil {
		return Event{}, err
	}
	n, err := note("note", msg, true)
	if err != nil {
		return Event{}, err
	}
	if release == w.ContentRelease {
		return Event{}, errs.Invalid("contentRelease", errs.ReasonInvalidValue, "That is the content release already in force.")
	}
	to := w.Status
	switch w.Status {
	case StatusApproved:
		to = StatusRevoked
	case StatusReadyForReview:
		to = w.settled()
	}
	e, err := w.next(EventBasisChanged, to, by, at)
	if err != nil {
		return Event{}, err
	}
	e.ContentRelease, e.Note, e.BuildVersion = release, n, w.FrozenBuild
	return e, nil
}

// DocumentFX documents the rate and conversion basis for one source
// currency. The latest documented rate for a currency is the one in force;
// the earlier ones stay in the history.
func (w Workpaper) DocumentFX(r FXRate, by id.UserID, at time.Time) (Event, error) {
	if err := w.open("documenting an FX rate"); err != nil {
		return Event{}, err
	}
	r.Source, r.Basis = strings.TrimSpace(r.Source), strings.TrimSpace(r.Basis)
	switch {
	case !ValidCurrency(string(r.Currency)):
		return Event{}, errs.Invalid("fx.currency", errs.ReasonInvalidValue, "The source currency is an ISO 4217 code.")
	case r.Currency == w.ReportingCurrency:
		return Event{}, errs.Invalid("fx.currency", errs.ReasonInvalidValue, "The reporting currency needs no conversion.")
	case fiscal.FactorOf(r.Rate).Sign() <= 0:
		return Event{}, errs.Invalid("fx.rate", errs.ReasonInvalidValue, "A rate is a positive decimal.")
	case r.RateDate.IsZero():
		return Event{}, errs.Invalid("fx.rateDate", errs.ReasonMissingField, "A documented rate names the date it is for.")
	case r.Scale < 0 || r.Scale > fiscal.MaxScale:
		return Event{}, errs.Invalid("fx.scale", errs.ReasonInvalidValue, fmt.Sprintf("The rounding scale is 0 to %d.", fiscal.MaxScale))
	}
	if err := text("fx.rateSource", r.Source, true, MaxRef); err != nil {
		return Event{}, err
	}
	if err := text("fx.conversionBasis", r.Basis, true, MaxText); err != nil {
		return Event{}, err
	}
	if _, err := r.Policy(); err != nil {
		return Event{}, errs.Invalid("fx.roundingMode", errs.ReasonInvalidValue, "The rounding mode is one of HALF_UP, HALF_EVEN, HALF_DOWN, DOWN, UP, CEILING or FLOOR.")
	}
	e, err := w.next(EventFXDocumented, w.Status, by, at)
	if err != nil {
		return Event{}, err
	}
	r.RecordedAt, r.RecordedBy = time.Time{}, id.UserID{}
	e.FX = &r
	return e, nil
}

// AdjustmentProposal is an allowance or an adjustment to propose.
type AdjustmentProposal struct {
	Kind     AdjustmentKind
	Category string
	Amount   fiscal.Money
	Reason   string
	Evidence *Evidence
}

// ProposeAdjustment records an evidence-linked allowance or adjustment. One
// below the materiality threshold is approved as proposed; a material one
// waits for a second person.
func (w Workpaper) ProposeAdjustment(p AdjustmentProposal, by id.UserID, at time.Time) (Event, error) {
	if err := w.open("an adjustment"); err != nil {
		return Event{}, err
	}
	if !p.Kind.Valid() {
		return Event{}, errs.Invalid("adjustment.kind", errs.ReasonInvalidValue, "An adjustment is a PERMITTED_ALLOWANCE or an AUDITED_ADJUSTMENT.")
	}
	category := strings.TrimSpace(p.Category)
	if err := text("adjustment.category", category, true, MaxRef); err != nil {
		return Event{}, err
	}
	reason, err := note("adjustment.reason", p.Reason, true)
	if err != nil {
		return Event{}, err
	}
	ev, err := CheckEvidence(p.Evidence)
	if err != nil {
		return Event{}, err
	}
	switch {
	case p.Amount.Currency() != w.ReportingCurrency:
		return Event{}, errs.Invalid("adjustment.amount", errs.ReasonInvalidValue,
			fmt.Sprintf("An adjustment is in the reporting currency, %s.", w.ReportingCurrency))
	case p.Amount.IsZero():
		return Event{}, errs.Invalid("adjustment.amount", errs.ReasonInvalidValue, "An adjustment of zero adjusts nothing.")
	case p.Kind == KindAllowance && p.Amount.Sign() < 0:
		return Event{}, errs.Invalid("adjustment.amount", errs.ReasonInvalidValue, "An allowance is a positive amount deducted from the base.")
	}
	e, err := w.next(EventAdjustmentProposed, w.Status, by, at)
	if err != nil {
		return Event{}, err
	}
	amount := p.Amount
	e.AdjustmentNumber, e.AdjustmentKind, e.AdjustmentCategory, e.AdjustmentAmount = len(w.Adjustments)+1, p.Kind, category, &amount
	e.Note, e.Evidence = reason, &ev
	return e, nil
}

// DecideAdjustment is the second person's decision on a material
// adjustment. The proposer does not decide it.
func (w Workpaper) DecideAdjustment(number int, d Decision, msg string, by id.UserID, at time.Time) (Event, error) {
	if err := w.open("deciding an adjustment"); err != nil {
		return Event{}, err
	}
	if !d.Valid() {
		return Event{}, errs.Invalid("decision", errs.ReasonInvalidValue, "A decision is APPROVED or REJECTED.")
	}
	n, err := note("note", msg, d == DecisionRejected)
	if err != nil {
		return Event{}, err
	}
	if number < 1 || number > len(w.Adjustments) {
		return Event{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, fmt.Sprintf("No adjustment %d on this workpaper.", number))
	}
	a := w.Adjustments[number-1]
	if a.State != AdjustmentProposed {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Adjustment %d is %s; only a proposed material adjustment is decided.", number, a.State))
	}
	if a.ProposedBy == by {
		return Event{}, errs.New(errs.CategoryPolicy, ReasonSegregationOfDuties,
			"The person who proposed this adjustment cannot decide it; another person must.")
	}
	e, err := w.next(EventAdjustmentDecided, w.Status, by, at)
	if err != nil {
		return Event{}, err
	}
	e.AdjustmentNumber, e.Decision, e.Note = number, d, n
	return e, nil
}

// ProposeResolution explains one resolvable exception of the latest build,
// with its evidence. It names the exception's fingerprint, so it resolves
// that very difference and no other.
func (w Workpaper) ProposeResolution(key, fingerprint, explanation string, evidence *Evidence, by id.UserID, at time.Time) (Event, error) {
	if err := w.open("a resolution"); err != nil {
		return Event{}, err
	}
	n, err := note("explanation", explanation, true)
	if err != nil {
		return Event{}, err
	}
	ev, err := CheckEvidence(evidence)
	if err != nil {
		return Event{}, err
	}
	var found *ExceptionState
	for _, e := range w.Exceptions() {
		if e.Key == key {
			found = &e
			break
		}
	}
	switch {
	case found == nil:
		return Event{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, fmt.Sprintf("The latest build has no exception %q.", key))
	case fingerprint != "" && fingerprint != found.Fingerprint:
		return Event{}, errs.New(errs.CategoryConflict, ReasonStale,
			"That exception changed in the latest build. Read it again and resolve what it says now.")
	case !found.Resolvable:
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("%s is repaired at its source, or by documenting a rate, and rebuilt; it is not resolved by explanation.", found.Code))
	case found.Resolved():
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "That exception is already resolved.")
	case found.Pending():
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonAlreadyExists, "A resolution of that exception already awaits its second person.")
	}
	e, err := w.next(EventResolutionProposed, w.Status, by, at)
	if err != nil {
		return Event{}, err
	}
	e.ExceptionKey, e.Fingerprint, e.Note, e.Evidence = key, found.Fingerprint, n, &ev
	return e, nil
}

// DecideResolution is the second person's confirmation or rejection of a
// proposed resolution. A confirmation that resolves the last open exception
// moves the workpaper out of DATA_INCOMPLETE.
func (w Workpaper) DecideResolution(key string, d Decision, msg string, by id.UserID, at time.Time) (Event, error) {
	if err := w.open("deciding a resolution"); err != nil {
		return Event{}, err
	}
	if !d.Valid() {
		return Event{}, errs.Invalid("decision", errs.ReasonInvalidValue, "A decision is APPROVED or REJECTED.")
	}
	n, err := note("note", msg, d == DecisionRejected)
	if err != nil {
		return Event{}, err
	}
	r := w.pendingResolution(key)
	if r == nil {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("No resolution of %q awaits a decision.", key))
	}
	if r.ProposedBy == by {
		return Event{}, errs.New(errs.CategoryPolicy, ReasonSegregationOfDuties,
			"The person who proposed this resolution cannot decide it; another person must.")
	}
	e, err := w.next(EventResolutionDecided, w.Status, by, at)
	if err != nil {
		return Event{}, err
	}
	e.ExceptionKey, e.Fingerprint, e.Decision, e.Note = key, r.Fingerprint, d, n
	if w.Status != StatusRevoked {
		// Fold the decision in to see whether anything is still open.
		after, err := Fold(w, append(slices.Clone(w.History), e), w.Builds)
		if err != nil {
			return Event{}, err
		}
		e.Status = after.settled()
	}
	return e, nil
}

// Action is something the workpaper's state admits.
type Action string

// The actions.
const (
	ActionBuild             Action = "BUILD"
	ActionFreeze            Action = "FREEZE"
	ActionReturnToDraft     Action = "RETURN_TO_DRAFT"
	ActionApprove           Action = "APPROVE"
	ActionChangeBasis       Action = "CHANGE_BASIS"
	ActionDocumentFX        Action = "DOCUMENT_FX_RATE"
	ActionProposeAdjustment Action = "PROPOSE_ADJUSTMENT"
	ActionDecideAdjustment  Action = "DECIDE_ADJUSTMENT"
	ActionProposeResolution Action = "PROPOSE_RESOLUTION"
	ActionDecideResolution  Action = "DECIDE_RESOLUTION"
)

// Valid reports whether the action is one of these.
func (a Action) Valid() bool {
	switch a {
	case ActionBuild, ActionFreeze, ActionReturnToDraft, ActionApprove, ActionChangeBasis, ActionDocumentFX,
		ActionProposeAdjustment, ActionDecideAdjustment, ActionProposeResolution, ActionDecideResolution:
		return true
	}
	return false
}

// AllowedActions is what the state machine admits now, in a fixed order.
// Each act still applies its own guards — four eyes, evidence, blockers —
// when it is taken.
func (w Workpaper) AllowedActions() []Action {
	switch w.Status {
	case StatusReadyForReview:
		return []Action{ActionApprove, ActionReturnToDraft, ActionChangeBasis}
	case StatusApproved:
		return []Action{ActionBuild, ActionChangeBasis}
	}
	out := []Action{ActionBuild}
	if w.Status == StatusDraft && len(w.Blockers()) == 0 {
		out = append(out, ActionFreeze)
	}
	out = append(out, ActionChangeBasis, ActionDocumentFX, ActionProposeAdjustment)
	if slices.ContainsFunc(w.Adjustments, func(a Adjustment) bool { return a.State == AdjustmentProposed }) {
		out = append(out, ActionDecideAdjustment)
	}
	propose, decide := false, false
	for _, e := range w.Exceptions() {
		if e.Resolvable && !e.Resolved() && !e.Pending() {
			propose = true
		}
		if e.Pending() {
			decide = true
		}
	}
	if propose {
		out = append(out, ActionProposeResolution)
	}
	if decide {
		out = append(out, ActionDecideResolution)
	}
	return out
}

// Filter narrows a listing. Empty fields do not filter.
type Filter struct {
	Period      string
	Duty        contribution.Regime
	Status      Status
	LegalEntity id.LegalEntityID
	Limit       int
}
