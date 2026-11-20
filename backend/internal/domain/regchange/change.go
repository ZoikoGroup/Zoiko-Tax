// Package regchange is regulatory change intelligence: the change feed, the
// impact assessment of a change and the review recommendations derived from
// it (Engineering Doc 03 §04 change_event and impact_assessment, §06 source
// watch, WF-05 intake and triage, WF-09 impact; Sidebar Navigation
// Architecture WF-02 specialist rule review; ZTAX-AIGOV-001 §4, §12, §14).
//
// Doc 03 asks for three things to stay apart, and this package keeps them so:
//
//   - A reported change is not a confirmed legal effect. A change is recorded
//     as NEW with its three clocks — published, effective and discovered —
//     never assumed equal (Z03-AT-10), and only a person's triage gives it a
//     priority. Every change ends with a disposition (not applicable, change
//     needed, monitor) and its rationale, or stays visibly ON_HOLD with the
//     hold's reason (Z03-AT-09).
//   - An impact assessment is a read. It is a pure function of the change,
//     the active content bundle's coverage and the tenant's own registrations
//     and obligations, with every impacted item's exact basis recorded and a
//     digest over inputs and output, so the same inputs give the same
//     assessment (WF-09 "deterministic replay"). It writes no tax decision,
//     ledger posting or filing (Z03-AT-17).
//   - A recommendation is not an approval. A specialist recommends, a
//     different person independently reviews, and an accepted
//     recommendation is a hand-off reference only: no release pack is
//     published, no rule or content changes and no tax result moves here
//     (WF-02 "No direct release-pack publication"). An AI-assisted
//     recommendation carries its provenance and never counts as one of the
//     two people (AIGOV-001 §12).
//
// Nothing is edited. A change's and a recommendation's state is the fold of
// appended events; an assessment is immutable once recorded.
package regchange

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/remittance"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The reason codes.
var (
	// ReasonDedupeConflict is a change whose source identity, version and
	// kind are already recorded with different particulars (Doc 03 §08
	// "409 conflicting change dedupe key").
	ReasonDedupeConflict = errs.Register("CHANGE_DEDUPE_CONFLICT", errs.CategoryConflict,
		"g-008",
		"This source edition's change of this kind is already recorded with different particulars. Re-read the recorded change; a corrected source is a new edition or document digest.")

	// ReasonAssessmentSuperseded is a review of a recommendation whose
	// assessment is no longer the change's current one.
	ReasonAssessmentSuperseded = errs.Register("ASSESSMENT_SUPERSEDED", errs.CategoryConflict,
		"g-008",
		"The impact assessment this recommendation was made on is no longer the change's current assessment. Nothing was reviewed. Make the recommendation again on the current assessment.")

	// ReasonVersionConflict is an act on a version that is no longer current.
	ReasonVersionConflict = registration.ReasonVersionConflict

	// ReasonMakerChecker is the estate's maker-checker code.
	ReasonMakerChecker = remittance.ReasonMakerChecker
)

// Bounds on free text.
const (
	MaxTitle    = 200
	MaxSummary  = 4000
	MaxText     = 200
	MaxURL      = 2048
	MaxReason   = 2000
	MaxScope    = 20
	MaxServices = 24
)

var (
	sha256Shape       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	jurisdictionShape = regexp.MustCompile(`^[A-Z]{2}(-[A-Z0-9]{1,3})?$`)
	codeShape         = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
)

// Kind is the class of a legal change (Doc 03 §06).
type Kind string

// The kinds, in Doc 03 §06's order.
const (
	KindNewLevy            Kind = "NEW_LEVY"
	KindRateChange         Kind = "RATE_CHANGE"
	KindRepealSunset       Kind = "REPEAL_SUNSET"
	KindBaseChange         Kind = "BASE_CHANGE"
	KindClassChange        Kind = "CLASS_CHANGE"
	KindJurisdictionChange Kind = "JURISDICTION_CHANGE"
	KindBackdatedRule      Kind = "BACKDATED_RULE"
	KindRightsWithdrawal   Kind = "RIGHTS_WITHDRAWAL"
	KindSourceDispute      Kind = "SOURCE_DISPUTE"
)

// Kinds is every kind.
var Kinds = []Kind{KindNewLevy, KindRateChange, KindRepealSunset, KindBaseChange, KindClassChange,
	KindJurisdictionChange, KindBackdatedRule, KindRightsWithdrawal, KindSourceDispute}

// Valid reports whether the kind is one of Doc 03 §06's.
func (k Kind) Valid() bool { return slices.Contains(Kinds, k) }

// Detector is who reported a change: a person's legal intake or a source
// watcher (Doc 03 §03 "official source watchers + human legal intake").
type Detector string

// The detectors. No watcher is connected to this cell; a MONITOR change is
// one a person records from a watcher's alert, naming the alert.
const (
	DetectedByHuman   Detector = "HUMAN"
	DetectedByMonitor Detector = "MONITOR"
)

// Valid reports whether the detector is one of these.
func (d Detector) Valid() bool { return d == DetectedByHuman || d == DetectedByMonitor }

// Priority is triage's urgency (Doc 03 §06 monitoring targets).
type Priority string

// The priorities.
const (
	PriorityUrgent   Priority = "URGENT"
	PriorityMaterial Priority = "MATERIAL"
	PriorityOrdinary Priority = "ORDINARY"
)

// Valid reports whether the priority is one of these.
func (p Priority) Valid() bool {
	return p == PriorityUrgent || p == PriorityMaterial || p == PriorityOrdinary
}

// Status is where a change stands.
type Status string

// The statuses. IN_IMPACT_ASSESSMENT is reached only by recording an impact
// assessment; CLOSED and NO_IMPACT are final.
const (
	StatusNew          Status = "NEW"
	StatusTriaged      Status = "TRIAGED"
	StatusInAssessment Status = "IN_IMPACT_ASSESSMENT"
	StatusOnHold       Status = "ON_HOLD"
	StatusClosed       Status = "CLOSED"
	StatusNoImpact     Status = "NO_IMPACT"
)

// Statuses is every status.
var Statuses = []Status{StatusNew, StatusTriaged, StatusInAssessment, StatusOnHold, StatusClosed, StatusNoImpact}

// Valid reports whether the status is one of these.
func (s Status) Valid() bool { return slices.Contains(Statuses, s) }

// Final reports whether nothing more happens to a change in this status.
func (s Status) Final() bool { return s == StatusClosed || s == StatusNoImpact }

// Disposition is the decision every change ends with (Doc 03 WF-05
// acceptance: "not applicable / change needed / monitor").
type Disposition string

// The dispositions.
const (
	DispositionNotApplicable Disposition = "NOT_APPLICABLE"
	DispositionChangeNeeded  Disposition = "CHANGE_NEEDED"
	DispositionMonitor       Disposition = "MONITOR"
)

// Valid reports whether the disposition is one of these.
func (d Disposition) Valid() bool {
	return d == DispositionNotApplicable || d == DispositionChangeNeeded || d == DispositionMonitor
}

// HoldReason is why a change is held rather than decided (Doc 03 WF-05
// states and exceptions).
type HoldReason string

// The hold reasons.
const (
	HoldSourceDisputed  HoldReason = "SOURCE_DISPUTED"
	HoldCounselQuestion HoldReason = "COUNSEL_QUESTION"
	HoldRightsHold      HoldReason = "RIGHTS_HOLD"
	HoldMissingFacts    HoldReason = "MISSING_FACTS"
	HoldMonitorFailure  HoldReason = "MONITOR_FAILURE"
)

// HoldReasons is every hold reason.
var HoldReasons = []HoldReason{HoldSourceDisputed, HoldCounselQuestion, HoldRightsHold, HoldMissingFacts, HoldMonitorFailure}

// Valid reports whether the hold reason is one of these.
func (h HoldReason) Valid() bool { return slices.Contains(HoldReasons, h) }

// EventKind is what happened to a change.
type EventKind string

// The events. RECORDED is always the first.
const (
	EventRecorded          EventKind = "RECORDED"
	EventTriaged           EventKind = "TRIAGED"
	EventAssessmentStarted EventKind = "ASSESSMENT_STARTED"
	EventHeld              EventKind = "HELD"
	EventResumed           EventKind = "RESUMED"
	EventClosed            EventKind = "CLOSED"
	EventNoImpact          EventKind = "NO_IMPACT"
)

// Source is the source edition a change is reported from (Doc 03 §04
// source_document): who published it, its citation, the edition and the
// SHA-256 of the captured document, hashed in the browser.
type Source struct {
	Authority      string
	Citation       string
	URL            string
	Edition        string
	DocumentSHA256 string
}

// Event is one appended act on a change.
type Event struct {
	Seq         int
	Kind        EventKind
	Status      Status
	Priority    Priority
	Disposition Disposition
	Hold        HoldReason
	Reason      string
	// Assessment is the assessment an ASSESSMENT_STARTED event was made by.
	Assessment id.ImpactAssessmentID
	RecordedAt time.Time
	RecordedBy id.UserID
}

// Change is one recorded regulatory change, folded with its events.
type Change struct {
	ID       id.RegulatoryChangeID
	TenantID id.TenantID

	Jurisdiction string // a country, or a country and one subdivision: US, US-NY
	Kind         Kind
	Title        string
	Summary      string
	Source       Source

	// The three clocks, never assumed equal (Doc 03 WF-05, Z03-AT-10).
	// PublishedOn and EffectiveFrom are the authority's civil dates, UTC
	// midnights; DiscoveredAt is the instant the change was seen.
	PublishedOn   time.Time
	EffectiveFrom time.Time
	DiscoveredAt  time.Time

	DetectedBy Detector
	// MonitorRef is the watcher alert a MONITOR change was recorded from.
	MonitorRef string

	// ServiceFamilies and TaxTypes narrow the change's scope; empty means
	// the whole jurisdiction.
	ServiceFamilies []string
	TaxTypes        []string

	// SourceKey is the digest of the source identity and version; DedupeKey
	// adds the kind. A multi-effect source is recorded as one change per
	// kind, linked by SourceKey (Doc 03 WF-05 "split or linked explicitly").
	SourceKey canonical.Digest
	DedupeKey canonical.Digest

	IntakeKey  string
	IntakeHash canonical.Digest

	CreatedAt time.Time
	CreatedBy id.UserID

	Status  Status
	History []Event
}

// Version is the number of events recorded. An act names it.
func (c Change) Version() int { return len(c.History) }

// Retroactive reports whether the change takes effect before it was
// published (Doc 03 §06 BACKDATED_RULE).
func (c Change) Retroactive() bool { return c.EffectiveFrom.Before(c.PublishedOn) }

// ValidJurisdiction reports whether j is a country, or a country and one
// subdivision.
func ValidJurisdiction(j string) bool { return jurisdictionShape.MatchString(j) }

// Day is the civil date of an instant, in UTC, as a midnight.
func Day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// NormalizeCodes upper-cases, trims, de-duplicates and sorts a scope list.
func NormalizeCodes(in []string) []string {
	out := []string{}
	for _, s := range in {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}

// Normalize trims what is compared and lowercases the digest.
func (s Source) Normalize() Source {
	s.Authority, s.Citation = strings.TrimSpace(s.Authority), strings.TrimSpace(s.Citation)
	s.URL, s.Edition = strings.TrimSpace(s.URL), strings.TrimSpace(s.Edition)
	s.DocumentSHA256 = strings.ToLower(strings.TrimSpace(s.DocumentSHA256))
	return s
}

// Validate refuses a change that cannot be recorded: no source or no
// effective date is refused before anything is recorded (Doc 03 §08 "422 no
// source/effective date").
func (c Change) Validate(now time.Time) error {
	if !ValidJurisdiction(c.Jurisdiction) {
		return errs.Invalid("jurisdiction", errs.ReasonInvalidValue, "A jurisdiction is a country, or a country and one subdivision: US, US-NY, GB.")
	}
	if !c.Kind.Valid() {
		return errs.Invalid("kind", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a change kind of Doc 03 §06.", c.Kind))
	}
	if err := text("title", c.Title, true, MaxTitle); err != nil {
		return err
	}
	if len(c.Summary) > MaxSummary {
		return errs.Invalid("summary", errs.ReasonInvalidValue, fmt.Sprintf("A summary is at most %d characters.", MaxSummary))
	}
	if err := text("source.authority", c.Source.Authority, true, MaxText); err != nil {
		return err
	}
	if err := text("source.citation", c.Source.Citation, true, MaxText); err != nil {
		return err
	}
	if err := text("source.edition", c.Source.Edition, true, MaxText); err != nil {
		return err
	}
	if c.Source.URL != "" && (len(c.Source.URL) > MaxURL || !strings.HasPrefix(c.Source.URL, "https://")) {
		return errs.Invalid("source.url", errs.ReasonInvalidValue, "A source URL is an https:// address of at most 2048 characters.")
	}
	if !sha256Shape.MatchString(c.Source.DocumentSHA256) {
		return errs.Invalid("source.documentSha256", errs.ReasonMissingField,
			"A change is recorded against the captured source document's SHA-256, in lowercase hex.")
	}
	if c.PublishedOn.IsZero() {
		return errs.Invalid("publishedOn", errs.ReasonMissingField, "A change states the date its source was published.")
	}
	if c.EffectiveFrom.IsZero() {
		return errs.Invalid("effectiveFrom", errs.ReasonMissingField, "A change states the date it takes legal effect. It is never assumed to be the publication date.")
	}
	if c.PublishedOn.After(Day(now)) {
		return errs.Invalid("publishedOn", errs.ReasonInvalidValue, "A source is not published in the future.")
	}
	if c.DiscoveredAt.IsZero() || c.DiscoveredAt.After(now) {
		return errs.Invalid("discoveredAt", errs.ReasonInvalidValue, "A change states when it was discovered, which is not in the future.")
	}
	if c.DiscoveredAt.Before(c.PublishedOn) {
		return errs.Invalid("discoveredAt", errs.ReasonInvalidValue, "A change is discovered on or after its source was published.")
	}
	if !c.DetectedBy.Valid() {
		return errs.Invalid("detectedBy", errs.ReasonInvalidValue, "A change is detected by HUMAN intake or a MONITOR.")
	}
	if c.DetectedBy == DetectedByMonitor {
		if err := text("monitorRef", c.MonitorRef, true, MaxText); err != nil {
			return errs.Invalid("monitorRef", errs.ReasonMissingField, "A monitor-detected change names the watcher alert it was recorded from.")
		}
	} else if c.MonitorRef != "" {
		return errs.Invalid("monitorRef", errs.ReasonInvalidValue, "Only a monitor-detected change names a watcher alert.")
	}
	if c.Kind == KindBackdatedRule && !c.Retroactive() {
		return errs.Invalid("effectiveFrom", errs.ReasonInvalidValue, "A BACKDATED_RULE takes effect before the date it was published.")
	}
	if err := codes("serviceFamilies", c.ServiceFamilies, MaxServices); err != nil {
		return err
	}
	return codes("taxTypes", c.TaxTypes, MaxScope)
}

func codes(field string, list []string, limit int) error {
	if len(list) > limit {
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("At most %d codes.", limit))
	}
	for _, s := range list {
		if !codeShape.MatchString(s) {
			return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("%q is not a code: upper-case letters, digits and underscores.", s))
		}
	}
	return nil
}

func stringsValue(list []string) canonical.Value {
	vs := make([]canonical.Value, len(list))
	for i, s := range list {
		vs[i] = canonical.String(s)
	}
	return canonical.Array(vs...)
}

// sourceIdentity is the source's identity and version: the authority, the
// jurisdiction, the citation, the edition and the document's digest.
func (c Change) sourceIdentity() []canonical.Field {
	return []canonical.Field{
		canonical.F("authority", canonical.String(strings.ToLower(c.Source.Authority))),
		canonical.F("jurisdiction", canonical.String(c.Jurisdiction)),
		canonical.F("citation", canonical.String(strings.ToLower(c.Source.Citation))),
		canonical.F("edition", canonical.String(strings.ToLower(c.Source.Edition))),
		canonical.F("documentSha256", canonical.String(c.Source.DocumentSHA256)),
	}
}

// Keys computes the source key and the dedupe key.
func (c Change) Keys() (source, dedupe canonical.Digest, err error) {
	if source, err = canonical.Sum(canonical.Object(c.sourceIdentity()...)); err != nil {
		return
	}
	dedupe, err = canonical.Sum(canonical.Object(append(c.sourceIdentity(), canonical.F("kind", canonical.String(string(c.Kind))))...))
	return
}

// Payload is the canonical document the intake hash covers: the change as
// reported, not who reported it or when it was recorded.
func (c Change) Payload() canonical.Value {
	return canonical.Object(
		canonical.F("jurisdiction", canonical.String(c.Jurisdiction)),
		canonical.F("kind", canonical.String(string(c.Kind))),
		canonical.F("title", canonical.String(c.Title)),
		canonical.F("summary", canonical.OptString(c.Summary)),
		canonical.F("source", canonical.Object(
			canonical.F("authority", canonical.String(c.Source.Authority)),
			canonical.F("citation", canonical.String(c.Source.Citation)),
			canonical.F("url", canonical.OptString(c.Source.URL)),
			canonical.F("edition", canonical.String(c.Source.Edition)),
			canonical.F("documentSha256", canonical.String(c.Source.DocumentSHA256)),
		)),
		canonical.F("publishedOn", canonical.String(c.PublishedOn.Format(time.DateOnly))),
		canonical.F("effectiveFrom", canonical.String(c.EffectiveFrom.Format(time.DateOnly))),
		canonical.F("detectedBy", canonical.String(string(c.DetectedBy))),
		canonical.F("monitorRef", canonical.OptString(c.MonitorRef)),
		canonical.F("serviceFamilies", stringsValue(c.ServiceFamilies)),
		canonical.F("taxTypes", stringsValue(c.TaxTypes)),
	)
}

// Open is a new change's first event: RECORDED, as NEW.
func Open(c Change, now time.Time) ([]Event, error) {
	if err := c.Validate(now); err != nil {
		return nil, err
	}
	return []Event{{Seq: 1, Kind: EventRecorded, Status: StatusNew, RecordedAt: now.UTC(), RecordedBy: c.CreatedBy}}, nil
}

// Fold applies a change's events in order.
func Fold(c Change, events []Event) (Change, error) {
	if len(events) == 0 || events[0].Kind != EventRecorded || events[0].Seq != 1 || events[0].Status != StatusNew {
		return Change{}, fmt.Errorf("regchange: a change's history starts with its RECORDED event")
	}
	c.Status = StatusNew
	for i, e := range events {
		if e.Seq != i+1 {
			return Change{}, fmt.Errorf("regchange: event %d is out of sequence", e.Seq)
		}
		if i > 0 && !folds(c, events[:i], e) {
			return Change{}, fmt.Errorf("regchange: %s from %s to %s", e.Kind, c.Status, e.Status)
		}
		c.Status = e.Status
	}
	c.History = slices.Clone(events)
	return c, nil
}

func folds(c Change, before []Event, e Event) bool {
	from := c.Status
	switch e.Kind {
	case EventTriaged:
		return from == StatusNew && e.Status == StatusTriaged && e.Priority.Valid()
	case EventAssessmentStarted:
		return from == StatusTriaged && e.Status == StatusInAssessment && !e.Assessment.IsZero()
	case EventHeld:
		return !from.Final() && from != StatusOnHold && e.Status == StatusOnHold && e.Hold.Valid()
	case EventResumed:
		return from == StatusOnHold && e.Status == heldFrom(before)
	case EventClosed:
		return (from == StatusInAssessment) && e.Status == StatusClosed &&
			(e.Disposition == DispositionChangeNeeded || e.Disposition == DispositionMonitor)
	case EventNoImpact:
		return (from == StatusNew || from == StatusTriaged || from == StatusInAssessment) && e.Status == StatusNoImpact &&
			e.Disposition == DispositionNotApplicable
	}
	return false
}

// heldFrom is the status a change was in when it was last held.
func heldFrom(events []Event) Status {
	for i := len(events) - 1; i > 0; i-- {
		if events[i].Kind == EventHeld {
			return events[i-1].Status
		}
	}
	return ""
}

// ResumesTo is the status a held change resumes to, and false when it is
// not held.
func (c Change) ResumesTo() (Status, bool) {
	if c.Status != StatusOnHold {
		return "", false
	}
	return heldFrom(c.History), true
}

func (c Change) next(kind EventKind, to Status, reason string, by id.UserID, at time.Time) Event {
	return Event{Seq: len(c.History) + 1, Kind: kind, Status: to, Reason: reason, RecordedAt: at.UTC(), RecordedBy: by}
}

// Priority is the priority triage gave, and false before triage.
func (c Change) Priority() (Priority, bool) {
	for i := len(c.History) - 1; i >= 0; i-- {
		if c.History[i].Kind == EventTriaged {
			return c.History[i].Priority, true
		}
	}
	return "", false
}

// Disposition is the change's final decision, and false while open.
func (c Change) Disposition() (Disposition, bool) {
	if !c.Status.Final() {
		return "", false
	}
	return c.History[len(c.History)-1].Disposition, true
}

// Hold is the reason a held change is held, and false when it is not.
func (c Change) Hold() (HoldReason, bool) {
	if c.Status != StatusOnHold {
		return "", false
	}
	return c.History[len(c.History)-1].Hold, true
}

// Act is what a person asks to be done to a change.
type Act struct {
	// To is the status asked for: TRIAGED, ON_HOLD, CLOSED, NO_IMPACT, or
	// the status a held change resumes to.
	To          Status
	Priority    Priority
	Disposition Disposition
	Hold        HoldReason
	Reason      string
	Version     int
}

// Closing is what closure needs to know beyond the change: whether an
// assessment and an accepted recommendation exist.
type Closing struct {
	HasAssessment             bool
	HasAcceptedRecommendation bool
}

// Transition records a person's act on the change, against the version they
// read, with its reason.
//
//   - TRIAGED from NEW, with a priority (WF-05 "clear priority").
//   - ON_HOLD from any open status, with a hold reason; back to the status
//     it was held from (Z03-AT-09 "tracked hold").
//   - NO_IMPACT from NEW, TRIAGED or IN_IMPACT_ASSESSMENT: disposition
//     NOT_APPLICABLE.
//   - CLOSED from IN_IMPACT_ASSESSMENT: disposition MONITOR, or
//     CHANGE_NEEDED once a recommendation on it has been independently
//     accepted — the change is handed off, not made, here.
//
// IN_IMPACT_ASSESSMENT is never a status a person names: recording an
// impact assessment makes it.
func (c Change) Transition(a Act, cl Closing, by id.UserID, at time.Time) (Event, error) {
	reason := strings.TrimSpace(a.Reason)
	if !a.To.Valid() {
		return Event{}, errs.Invalid("to", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a change status.", a.To))
	}
	if a.Version != c.Version() {
		return Event{}, errs.New(errs.CategoryConflict, ReasonVersionConflict,
			fmt.Sprintf("The change is at version %d; version %d was read. Re-read it and act on the current version.", c.Version(), a.Version))
	}
	if err := reasonText("reason", reason); err != nil {
		return Event{}, err
	}
	if c.Status.Final() {
		return Event{}, invalidMove("change", string(c.Status), string(a.To))
	}
	if back, held := c.ResumesTo(); held {
		if a.To != back {
			return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				fmt.Sprintf("A held change resumes to %s, the status it was held from, before anything else is decided.", back))
		}
		return c.next(EventResumed, back, reason, by, at), nil
	}
	switch a.To {
	case StatusTriaged:
		if c.Status != StatusNew {
			return Event{}, invalidMove("change", string(c.Status), string(a.To))
		}
		if !a.Priority.Valid() {
			return Event{}, errs.Invalid("priority", errs.ReasonMissingField, "Triage gives the change a priority: URGENT, MATERIAL or ORDINARY.")
		}
		e := c.next(EventTriaged, StatusTriaged, reason, by, at)
		e.Priority = a.Priority
		return e, nil
	case StatusOnHold:
		if !a.Hold.Valid() {
			return Event{}, errs.Invalid("holdReason", errs.ReasonMissingField, "A hold names its reason: SOURCE_DISPUTED, COUNSEL_QUESTION, RIGHTS_HOLD, MISSING_FACTS or MONITOR_FAILURE.")
		}
		e := c.next(EventHeld, StatusOnHold, reason, by, at)
		e.Hold = a.Hold
		return e, nil
	case StatusNoImpact:
		if a.Disposition != "" && a.Disposition != DispositionNotApplicable {
			return Event{}, errs.Invalid("disposition", errs.ReasonInvalidValue, "NO_IMPACT is the disposition NOT_APPLICABLE.")
		}
		e := c.next(EventNoImpact, StatusNoImpact, reason, by, at)
		e.Disposition = DispositionNotApplicable
		return e, nil
	case StatusClosed:
		if c.Status != StatusInAssessment || !cl.HasAssessment {
			return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				"A change is closed with a decision only after its impact is assessed. A change that does not apply is closed as NO_IMPACT.")
		}
		switch a.Disposition {
		case DispositionMonitor:
		case DispositionChangeNeeded:
			if !cl.HasAcceptedRecommendation {
				return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
					"CHANGE_NEEDED closes a change only once a recommendation on its current assessment has been independently accepted and handed off.")
			}
		default:
			return Event{}, errs.Invalid("disposition", errs.ReasonMissingField, "CLOSED takes the disposition CHANGE_NEEDED or MONITOR.")
		}
		e := c.next(EventClosed, StatusClosed, reason, by, at)
		e.Disposition = a.Disposition
		return e, nil
	case StatusInAssessment:
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"A change is IN_IMPACT_ASSESSMENT only by recording an impact assessment of it.")
	}
	return Event{}, invalidMove("change", string(c.Status), string(a.To))
}

// StartAssessment is the event an assessment records on a TRIAGED change,
// and false when the change is already being assessed. A NEW change is
// triaged first; a held or decided one is not assessed.
func (c Change) StartAssessment(assessment id.ImpactAssessmentID, by id.UserID, at time.Time) (Event, bool, error) {
	switch c.Status {
	case StatusInAssessment:
		return Event{}, false, nil
	case StatusTriaged:
		e := c.next(EventAssessmentStarted, StatusInAssessment, "", by, at)
		e.Assessment = assessment
		return e, true, nil
	case StatusNew:
		return Event{}, false, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"A change is triaged before its impact is assessed.")
	}
	return Event{}, false, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
		fmt.Sprintf("A %s change is not assessed.", c.Status))
}

// Filter narrows a listing. Empty fields do not filter.
type Filter struct {
	Status       Status
	Kind         Kind
	Jurisdiction string // a country matches its subdivisions
	Priority     Priority
}

// Match reports whether a change passes the filter.
func (f Filter) Match(c Change) bool {
	p, _ := c.Priority()
	return (f.Status == "" || c.Status == f.Status) && (f.Kind == "" || c.Kind == f.Kind) &&
		(f.Priority == "" || p == f.Priority) &&
		(f.Jurisdiction == "" || c.Jurisdiction == f.Jurisdiction || strings.HasPrefix(c.Jurisdiction, f.Jurisdiction+"-"))
}

func text(field, value string, required bool, limit int) error {
	switch {
	case value == "" && required:
		return errs.Invalid(field, errs.ReasonMissingField, fmt.Sprintf("%s is required.", field))
	case len(value) > limit:
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("%s is at most %d characters.", field, limit))
	case value != strings.TrimSpace(value):
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("%s carries no leading or trailing space.", field))
	}
	return nil
}

func reasonText(field, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errs.Invalid(field, errs.ReasonMissingField, "This act gives its rationale.")
	}
	if len(reason) > MaxReason {
		return errs.Invalid(field, errs.ReasonInvalidValue, fmt.Sprintf("A rationale is at most %d characters.", MaxReason))
	}
	return nil
}

func invalidMove(what, from, to string) error {
	return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
		fmt.Sprintf("A %s does not move from %s to %s.", what, from, to))
}

func makerChecker(detail string) error {
	return errs.New(errs.CategoryPolicy, ReasonMakerChecker, detail)
}
