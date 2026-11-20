// Package subscription is a tenant's subscription plan and the usage the
// cell meters against it (Engineering Doc 18 §05 MeterEvent, W04, W08, W09;
// G-049).
//
// Two things are kept apart:
//
//   - A plan is a commercial record: a plan code, a term, the contract it
//     comes from and an included quota per metered capability. It is
//     proposed by one administrator and in force only once a second approves
//     it (Doc 18 §07 "SKU DRAFT until delegated Finance approval"). It names
//     no price: Doc 18 authorises no commercial pricing, and none is invented
//     here. A plan never grants an entitlement and never changes a tax result
//     (T18-04).
//   - Usage is not entered: it is counted from the cell's own records — the
//     decisions it recorded, the fiscal documents it issued, the filing
//     submissions it started, the shadow runs it executed — over a UTC month,
//     as exact integers. Each count carries the rule it was counted by and a
//     SHA-256 over the sorted identifiers of the rows it counted, so anyone can
//     list the same rows and get the same number and digest (W09 "trace every
//     chargeable event"; MTR-07 "full-population reconciliation"). A retried
//     request that returned the original record wrote no second row, so it is
//     counted once (MTR-01).
//
// Nothing is edited. A plan's state is the fold of its appended events; a
// changed plan is a new revision whose approval makes it the plan in force.
package subscription

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/registration"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Bounds.
const (
	MaxName        = 128
	MaxContractRef = 128
	MaxReason      = 2000
	// MaxIncluded bounds an included quantity so a sum or a difference of
	// two never overflows an int64.
	MaxIncluded int64 = 1_000_000_000_000
)

var (
	planCodeShape = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_.-]{0,63}$`)
	periodShape   = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)
)

// ---------------------------------------------------------------------------
// meters
// ---------------------------------------------------------------------------

// Meter is a metered capability.
type Meter string

// The meters. Each counts one kind of row the cell itself writes.
const (
	MeterCommittedDecisions Meter = "COMMITTED_DECISIONS"
	MeterFiscalDocuments    Meter = "FISCAL_DOCUMENTS"
	MeterFilingSubmissions  Meter = "FILING_SUBMISSIONS"
	MeterShadowRuns         Meter = "SHADOW_RUNS"
)

// DefinitionsVersion names this edition of the meter definitions. A count
// is reproducible against the version it was counted under.
const DefinitionsVersion = "ztax-meters-2026.11"

// Definition says what a meter counts and how.
type Definition struct {
	Meter Meter
	// Unit is what one counted row is.
	Unit string
	// Source is the table the rows are read from.
	Source string
	// Rule is the counting rule in words: which rows, which instant places
	// a row in a period, and which identifier is digested.
	Rule string
}

// Definitions is every meter, in the order a report lists them.
var Definitions = []Definition{
	{MeterCommittedDecisions, "decision", "ztax.tax_decision",
		"One per recorded tax decision whose recorded_at falls in the period: every commit, adjustment and refund decision the cell recorded. A quote records nothing and is not counted; a retried commit returns the original decision and is counted once. Digest over decision_id."},
	{MeterFiscalDocuments, "document", "ztax.fiscal_document",
		"One per fiscal document whose recorded_at falls in the period: invoices and every credit, debit, void or correction document, each a document of its own. Digest over document_id."},
	{MeterFilingSubmissions, "submission", "ztax.compliance_step",
		"One per compliance-package submission attempt started (step SUBMISSION_STARTED) whose recorded_at falls in the period. An attempt is started once; its acknowledgement is not counted again. Digest over submission_id."},
	{MeterShadowRuns, "run", "ztax.shadow_run",
		"One per shadow comparison run whose created_at falls in the period. Shadow runs are metered only here, never as native decisions (MTR-05). Digest over run_id."},
}

// Meters is every meter, in definition order.
func Meters() []Meter {
	out := make([]Meter, len(Definitions))
	for i, d := range Definitions {
		out[i] = d.Meter
	}
	return out
}

// Valid reports whether the meter is one of these.
func (m Meter) Valid() bool { return slices.Contains(Meters(), m) }

// DefinitionOf returns a meter's definition.
func DefinitionOf(m Meter) (Definition, bool) {
	for _, d := range Definitions {
		if d.Meter == m {
			return d, true
		}
	}
	return Definition{}, false
}

// Window is a period's half-open UTC interval [From, To).
type Window struct {
	Period   string
	From, To time.Time
}

// PeriodWindow parses a YYYY-MM period into its UTC month.
func PeriodWindow(period string) (Window, error) {
	if !periodShape.MatchString(period) {
		return Window{}, errs.Invalid("period", errs.ReasonInvalidValue, "A usage period is a month, YYYY-MM.")
	}
	from, err := time.Parse("2006-01", period)
	if err != nil {
		return Window{}, errs.Invalid("period", errs.ReasonInvalidValue, "A usage period is a month, YYYY-MM.")
	}
	return Window{Period: period, From: from.UTC(), To: from.UTC().AddDate(0, 1, 0)}, nil
}

// PeriodOf is the YYYY-MM period an instant falls in, in UTC.
func PeriodOf(t time.Time) string { return t.UTC().Format("2006-01") }

// BasisDigest is the digest a meter's count carries: SHA-256, lowercase
// hex, over the counted identifiers in ascending order, each followed by a
// newline except the last. No rows is the digest of the empty string.
func BasisDigest(ids []string) string {
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:])
}

// Count is one meter's count over a window, as read from the store.
type Count struct {
	Meter  Meter
	Count  int64
	Digest string
}

// ---------------------------------------------------------------------------
// plans
// ---------------------------------------------------------------------------

// OveragePolicy is what the contract says happens past the included quota.
type OveragePolicy string

// The policies. The cell reports against them and enforces neither: a
// HARD_LIMIT is a contract term the report flags, not a throttle this cell
// applies.
const (
	OverageAllowed   OveragePolicy = "ALLOW_OVERAGE"
	OverageHardLimit OveragePolicy = "HARD_LIMIT"
)

// Valid reports whether the policy is one of these.
func (o OveragePolicy) Valid() bool { return o == OverageAllowed || o == OverageHardLimit }

// Quota is one metered capability's included quantity per monthly period.
type Quota struct {
	Meter    Meter
	Included int64
	Overage  OveragePolicy
}

// Status is where a plan revision stands.
type Status string

// The statuses.
const (
	StatusProposed  Status = "PROPOSED"
	StatusApproved  Status = "APPROVED"
	StatusRejected  Status = "REJECTED"
	StatusWithdrawn Status = "WITHDRAWN"
)

// Valid reports whether the status is one of these.
func (s Status) Valid() bool {
	return s == StatusProposed || s == StatusApproved || s == StatusRejected || s == StatusWithdrawn
}

// Action is what a person does to a plan revision.
type Action string

// The actions.
const (
	ActApprove  Action = "APPROVE"
	ActReject   Action = "REJECT"
	ActWithdraw Action = "WITHDRAW"
)

// Valid reports whether the action is one of these.
func (a Action) Valid() bool { return a == ActApprove || a == ActReject || a == ActWithdraw }

// EventKind is what happened to a plan revision.
type EventKind string

// The events. PROPOSED is always the first.
const (
	EventProposed  EventKind = "PROPOSED"
	EventApproved  EventKind = "APPROVED"
	EventRejected  EventKind = "REJECTED"
	EventWithdrawn EventKind = "WITHDRAWN"
)

var actionEvents = map[Action]struct {
	kind EventKind
	to   Status
}{
	ActApprove: {EventApproved, StatusApproved}, ActReject: {EventRejected, StatusRejected},
	ActWithdraw: {EventWithdrawn, StatusWithdrawn},
}

// Event is one appended act on a plan revision.
type Event struct {
	Seq        int
	Kind       EventKind
	Status     Status
	Reason     string
	RecordedAt time.Time
	RecordedBy id.UserID
}

// Plan is one subscription plan revision, folded with its events.
type Plan struct {
	ID       id.SubscriptionPlanID
	TenantID id.TenantID

	PlanCode    string
	PlanName    string
	ContractRef string
	// TermStart and TermEnd are civil UTC dates; TermEnd zero is
	// open-ended, and the plan is in force on it.
	TermStart time.Time
	TermEnd   time.Time
	Quotas    []Quota

	IntakeKey  string
	IntakeHash canonical.Digest
	CreatedAt  time.Time
	CreatedBy  id.UserID

	Status  Status
	History []Event
}

// Version is the number of events recorded. An approval names it.
func (p Plan) Version() int { return len(p.History) }

// Quota returns the plan's quota for a meter, and false when the plan does
// not contract that meter.
func (p Plan) Quota(m Meter) (Quota, bool) {
	for _, q := range p.Quotas {
		if q.Meter == m {
			return q, true
		}
	}
	return Quota{}, false
}

// Validate refuses a plan that cannot be recorded.
func (p Plan) Validate() error {
	switch {
	case !planCodeShape.MatchString(p.PlanCode):
		return errs.Invalid("planCode", errs.ReasonInvalidValue, "A plan code is 1 to 64 capital letters, digits, dots, underscores or hyphens.")
	case strings.TrimSpace(p.PlanName) == "" || len(p.PlanName) > MaxName || p.PlanName != strings.TrimSpace(p.PlanName):
		return errs.Invalid("planName", errs.ReasonInvalidValue, fmt.Sprintf("A plan name is 1 to %d characters with no leading or trailing space.", MaxName))
	case strings.TrimSpace(p.ContractRef) == "" || len(p.ContractRef) > MaxContractRef || p.ContractRef != strings.TrimSpace(p.ContractRef):
		return errs.Invalid("contractRef", errs.ReasonInvalidValue, "A plan names the signed contract it comes from, in 1 to 128 characters.")
	case p.TermStart.IsZero():
		return errs.Invalid("termStart", errs.ReasonMissingField, "A plan states the date its term starts.")
	case !p.TermEnd.IsZero() && p.TermEnd.Before(p.TermStart):
		return errs.Invalid("termEnd", errs.ReasonInvalidValue, "A plan's term ends on or after it starts.")
	case len(p.Quotas) == 0:
		return errs.Invalid("quotas", errs.ReasonMissingField, "A plan contracts at least one metered capability.")
	}
	seen := map[Meter]bool{}
	for _, q := range p.Quotas {
		switch {
		case !q.Meter.Valid():
			return errs.Invalid("quotas.meter", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a meter.", q.Meter))
		case seen[q.Meter]:
			return errs.Invalid("quotas.meter", errs.ReasonInvalidValue, fmt.Sprintf("%s is contracted twice.", q.Meter))
		case q.Included < 0 || q.Included > MaxIncluded:
			return errs.Invalid("quotas.included", errs.ReasonInvalidValue, "An included quantity is a whole number from 0 to 1,000,000,000,000.")
		case !q.Overage.Valid():
			return errs.Invalid("quotas.overage", errs.ReasonInvalidValue, fmt.Sprintf("%q is not an overage policy.", q.Overage))
		}
		seen[q.Meter] = true
	}
	return nil
}

// Payload is the canonical document the intake hash covers.
func (p Plan) Payload() canonical.Value {
	end := canonical.Absent()
	if !p.TermEnd.IsZero() {
		end = canonical.String(p.TermEnd.Format(time.DateOnly))
	}
	quotas := make([]canonical.Value, len(p.Quotas))
	for i, q := range p.Quotas {
		quotas[i] = canonical.Object(
			canonical.F("meter", canonical.String(string(q.Meter))),
			canonical.F("included", canonical.Integer(q.Included)),
			canonical.F("overage", canonical.String(string(q.Overage))),
		)
	}
	return canonical.Object(
		canonical.F("planCode", canonical.String(p.PlanCode)),
		canonical.F("planName", canonical.String(p.PlanName)),
		canonical.F("contractRef", canonical.String(p.ContractRef)),
		canonical.F("termStart", canonical.String(p.TermStart.Format(time.DateOnly))),
		canonical.F("termEnd", end),
		canonical.F("quotas", canonical.Array(quotas...)),
	)
}

// Open is a new plan revision's first event: PROPOSED. Its term never
// starts before the day it is proposed, so approving it never re-reads a
// period already metered against another plan (COM-04).
func Open(p Plan, by id.UserID, at time.Time) ([]Event, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.TermStart.Before(registration.Day(at)) {
		return nil, errs.Invalid("termStart", errs.ReasonInvalidValue,
			fmt.Sprintf("A plan's term starts on or after today, %s. A plan never reaches back over usage already metered.", registration.Day(at).Format(time.DateOnly)))
	}
	return []Event{{Seq: 1, Kind: EventProposed, Status: StatusProposed, RecordedAt: at.UTC(), RecordedBy: by}}, nil
}

// Fold applies a plan's events in order.
func Fold(p Plan, events []Event) (Plan, error) {
	if len(events) == 0 || events[0].Kind != EventProposed || events[0].Seq != 1 || events[0].Status != StatusProposed {
		return Plan{}, fmt.Errorf("subscription: a plan's history starts with its PROPOSED event")
	}
	p.Status = StatusProposed
	for i, e := range events {
		if e.Seq != i+1 {
			return Plan{}, fmt.Errorf("subscription: event %d is out of sequence", e.Seq)
		}
		if i > 0 && (p.Status != StatusProposed || e.Kind == EventProposed) {
			return Plan{}, fmt.Errorf("subscription: %s after %s", e.Kind, p.Status)
		}
		p.Status = e.Status
	}
	p.History = slices.Clone(events)
	return p, nil
}

// Approval is the approving event, and false while there is none.
func (p Plan) Approval() (Event, bool) {
	for _, e := range p.History {
		if e.Kind == EventApproved {
			return e, true
		}
	}
	return Event{}, false
}

// Apply returns the event a person's act appends, or why it is refused.
// APPROVE is by someone other than the proposer, on the version read.
// REJECT and WITHDRAW give their reason. Only a PROPOSED revision moves.
func (p Plan) Apply(act Action, expectedVersion int, reason string, by id.UserID, at time.Time) (Event, error) {
	reason = strings.TrimSpace(reason)
	if !act.Valid() {
		return Event{}, errs.Invalid("action", errs.ReasonInvalidValue, fmt.Sprintf("%q is not a plan action.", act))
	}
	if expectedVersion != p.Version() {
		return Event{}, errs.New(errs.CategoryConflict, registration.ReasonVersionConflict,
			fmt.Sprintf("The plan is at version %d; version %d was read. Re-read it and act on the current version.", p.Version(), expectedVersion))
	}
	if p.Status != StatusProposed {
		return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A %s plan revision does not move. A change is a new revision.", p.Status))
	}
	if len(reason) > MaxReason {
		return Event{}, errs.Invalid("reason", errs.ReasonInvalidValue, fmt.Sprintf("A reason is at most %d characters.", MaxReason))
	}
	switch act {
	case ActApprove:
		if p.CreatedBy == by {
			return Event{}, errs.New(errs.CategoryPolicy, registration.ReasonMakerChecker,
				"The person who proposed this plan cannot also approve it. A second administrator must.")
		}
		if !p.TermEnd.IsZero() && registration.Day(at).After(p.TermEnd) {
			return Event{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid, "This plan's term has already ended.")
		}
	default:
		if reason == "" {
			return Event{}, errs.Invalid("reason", errs.ReasonMissingField, "This act gives its reason.")
		}
	}
	m := actionEvents[act]
	return Event{Seq: p.Version() + 1, Kind: m.kind, Status: m.to, Reason: reason, RecordedAt: at.UTC(), RecordedBy: by}, nil
}

// Covers reports whether an approved plan's term includes day.
func (p Plan) Covers(day time.Time) bool {
	return p.Status == StatusApproved && !p.TermStart.After(day) && (p.TermEnd.IsZero() || !day.After(p.TermEnd))
}

// InForce returns the plan in force on day: of the approved revisions whose
// term includes it, the one approved last. A later approval supersedes an
// earlier plan from the later plan's term start; nothing before it is
// re-read. False when no plan is in force.
func InForce(plans []Plan, day time.Time) (Plan, bool) {
	var (
		best   Plan
		bestAt time.Time
		found  bool
	)
	for _, p := range plans {
		if !p.Covers(day) {
			continue
		}
		a, _ := p.Approval()
		if !found || a.RecordedAt.After(bestAt) || (a.RecordedAt.Equal(bestAt) && p.ID.String() > best.ID.String()) {
			best, bestAt, found = p, a.RecordedAt, true
		}
	}
	return best, found
}

// ---------------------------------------------------------------------------
// usage
// ---------------------------------------------------------------------------

// UsageState is one meter's standing against the plan.
type UsageState string

// The states.
const (
	// UsageNoPlan: no plan is in force for the period.
	UsageNoPlan UsageState = "NO_PLAN"
	// UsageNotContracted: the plan does not contract this meter. Whether
	// such usage is chargeable is for the signed contract, never assumed
	// (MTR-04).
	UsageNotContracted UsageState = "NOT_CONTRACTED"
	UsageWithinQuota   UsageState = "WITHIN_QUOTA"
	UsageAtQuota       UsageState = "AT_QUOTA"
	UsageOverQuota     UsageState = "OVER_QUOTA"
)

// Line is one meter's usage over a period, against the plan in force.
type Line struct {
	Definition Definition
	Count      Count
	State      UsageState
	// Quota is the plan's quota; nil when the meter is not contracted or
	// there is no plan.
	Quota *Quota
	// Remaining is what is left of the included quantity, and Overage what
	// the count exceeds it by. Both zero without a quota.
	Remaining int64
	Overage   int64
}

// Assess lines up counts against the plan in force, in definition order.
// A meter with no count is reported as zero rows.
func Assess(counts []Count, plan *Plan) []Line {
	byMeter := map[Meter]Count{}
	for _, c := range counts {
		byMeter[c.Meter] = c
	}
	out := make([]Line, 0, len(Definitions))
	for _, d := range Definitions {
		c, ok := byMeter[d.Meter]
		if !ok {
			c = Count{Meter: d.Meter, Digest: BasisDigest(nil)}
		}
		l := Line{Definition: d, Count: c}
		switch q, contracted := quotaOf(plan, d.Meter); {
		case plan == nil:
			l.State = UsageNoPlan
		case !contracted:
			l.State = UsageNotContracted
		default:
			l.Quota = &q
			switch {
			case c.Count < q.Included:
				l.State, l.Remaining = UsageWithinQuota, q.Included-c.Count
			case c.Count == q.Included:
				l.State = UsageAtQuota
			default:
				l.State, l.Overage = UsageOverQuota, c.Count-q.Included
			}
		}
		out = append(out, l)
	}
	return out
}

func quotaOf(plan *Plan, m Meter) (Quota, bool) {
	if plan == nil {
		return Quota{}, false
	}
	return plan.Quota(m)
}
