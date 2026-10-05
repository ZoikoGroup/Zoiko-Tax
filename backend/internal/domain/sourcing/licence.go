package sourcing

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Term is a licence's validity window: [Start, End).
//
// Half-open, so that a licence ending at midnight and its renewal starting at
// midnight do not overlap and do not leave a gap. A zero End is a licence with
// no end date — a public licence, a public-domain memo, Zoiko's own authorship
// — and is still bounded by review (SourceLicenseRecord.ReviewDue), because
// "perpetual" describes the licence and not our confidence in our reading of it.
type Term struct {
	Start, End time.Time
	AutoRenew  bool
	// NoticeDays is the renewal or termination notice period, which is what
	// the 120/90/60/30-day renewal alerts of SRC-001 §21 count back from.
	NoticeDays int
}

// Covers reports whether at falls inside the term.
func (t Term) Covers(at time.Time) bool {
	if at.Before(t.Start) {
		return false
	}
	return t.End.IsZero() || at.Before(t.End)
}

// TerminationEffect is what survives a licence ending (SRC-001 §21).
//
// SRC-001 asks for each of "stop new ingestion; stop future calculation use;
// delete raw data; keep evidence; keep derived decisions; keep compiled rules;
// keep historical replay" to be decided separately. The machine-readable part
// of that decision is the set of rights that survive: everything not listed
// stops (ZTAX-SRC-REQ-0030 — an expired licence does not authorise new use
// beyond surviving rights), and historical_replay among them is what keeps
// decisions made while the licence was active reproducible.
type TerminationEffect struct {
	SurvivingRights []Right
	// DeleteRawWithinDays is the deletion obligation on raw source copies.
	// Zero means no deletion obligation; legal holds override it either way
	// (ZTAX-SRC-REQ-0031).
	DeleteRawWithinDays int
	Notes               string
}

// Survives reports whether r continues after the licence ends.
func (e TerminationEffect) Survives(r Right) bool {
	for _, s := range e.SurvivingRights {
		if s == r {
			return true
		}
	}
	return false
}

// CostModel is SRC-001 §5's cost_model.
type CostModel string

// The cost models.
const (
	CostNone        CostModel = "NONE"
	CostFixed       CostModel = "FIXED"
	CostPerSeat     CostModel = "PER_SEAT"
	CostPerQuery    CostModel = "PER_QUERY"
	CostPerCustomer CostModel = "PER_CUSTOMER"
	CostPerRecord   CostModel = "PER_RECORD"
	CostRevenue     CostModel = "REVENUE_SHARE"
)

// Valid reports whether c is a known model.
func (c CostModel) Valid() bool {
	switch c {
	case CostNone, CostFixed, CostPerSeat, CostPerQuery, CostPerCustomer, CostPerRecord, CostRevenue:
		return true
	}
	return false
}

// SourceLicenseRecord is SRC-001 §5: who owns a source, why we may use it, for
// which purposes, where it may run, and what happens when the licence ends.
//
// The field set is §5's. Free-text terms that no gate reads (derivative rules,
// residency terms, AI terms, audit rights) are carried as text because they are
// what a lawyer reviews, and modelling them as structure before anyone has
// decided what the structure is would be inventing law.
type SourceLicenseRecord struct {
	SourceID          SourceID
	ProviderLegalName string // the actual licensor, not a marketing name (ZTAX-SRC-REQ-0002)
	SourceName        string
	SourceVersion     string
	Class             SourceClass
	Acquisition       AcquisitionMethod
	// LicenceRef names the executed agreement, public licence (with its exact
	// version — ZTAX-SRC-REQ-0041), authority terms or public-domain memo.
	LicenceRef string
	Rights     RightsProfile
	// Territories bounds where the rights apply. Empty means the licence is
	// not territorially restricted.
	Territories []string
	// Environments are the deployment modes the licence permits. Empty is not
	// "anywhere": SRC-001 §5 makes the environment a required field, and an
	// unstated environment is an unestablished right.
	Environments       []DeploymentMode
	Constraints        string // user/volume constraints
	AttributionNotice  string
	DerivativeRules    string
	DataResidencyTerms string
	AITerms            string
	Term               Term
	Termination        TerminationEffect
	AuditRights        string
	Subprocessors      []string
	CostModel          CostModel
	LegalOwner         string
	TechnicalOwner     string
	// ReviewDue is when the licence must next be reviewed. Required: no
	// production source may be orphaned without one (ZTAX-SRC-REQ-0072).
	ReviewDue time.Time
	// LicenceTermsHash is the digest of the archived licence terms
	// (ZTAX-SRC-REQ-0039). Required for every class but S7, which has no
	// third-party terms to archive.
	LicenceTermsHash string
	State            OnboardingState
}

// Validate checks the record is complete enough to be evaluated at all. It
// says nothing about whether any use is permitted; that is Evaluate.
func (r SourceLicenseRecord) Validate() error {
	var missing []string
	need := func(ok bool, field string) {
		if !ok {
			missing = append(missing, field)
		}
	}
	need(r.SourceID != "", "sourceId")
	need(strings.TrimSpace(r.ProviderLegalName) != "", "providerLegalName")
	need(r.SourceName != "", "sourceName")
	need(r.LicenceRef != "", "licenceRef")
	need(r.LegalOwner != "", "legalOwner")
	need(r.TechnicalOwner != "", "technicalOwner")
	need(!r.Term.Start.IsZero(), "term.start")
	need(!r.ReviewDue.IsZero(), "reviewDue")
	need(len(r.Environments) > 0, "environments")
	need(r.Class == ClassZoikoAuthored || r.LicenceTermsHash != "", "licenceTermsHash")
	if len(missing) > 0 {
		return errorf("source %s: licence record is missing %s", r.SourceID, strings.Join(missing, ", "))
	}
	if !r.Class.Valid() {
		return errorf("source %s: class %q is not S0–S7", r.SourceID, r.Class)
	}
	if !r.Acquisition.Valid() {
		return errorf("source %s: acquisition method %q is not one SRC-001 §5 defines", r.SourceID, r.Acquisition)
	}
	if !r.CostModel.Valid() {
		return errorf("source %s: cost model %q is not one SRC-001 §5 defines", r.SourceID, r.CostModel)
	}
	if !r.State.Valid() {
		return errorf("source %s: onboarding state %q is not one SRC-001 §6 defines", r.SourceID, r.State)
	}
	if !r.Term.End.IsZero() && !r.Term.End.After(r.Term.Start) {
		return errorf("source %s: licence term ends before it starts", r.SourceID)
	}
	for _, e := range r.Environments {
		if !e.Valid() {
			return errorf("source %s: environment %q is not a deployment mode", r.SourceID, e)
		}
	}
	for _, s := range r.Termination.SurvivingRights {
		if !s.Valid() {
			return errorf("source %s: surviving right %q is not in the taxonomy", r.SourceID, s)
		}
	}
	if err := r.Rights.Validate(); err != nil {
		return fmt.Errorf("source %s: %w", r.SourceID, err)
	}
	return nil
}

// Current reports why the record cannot authorise new use at instant at, or
// nil if it can. Current means all of: onboarding has reached production and
// not left it, the licence term covers at, and the licence review is not
// overdue.
//
// Review overdue blocks. ZTAX-CONT-REQ-0071 lets pack policy decide whether a
// stale review blocks authoritative use; at build time there is no policy to
// consult, and a build is the last point at which refusing costs nothing.
func (r SourceLicenseRecord) Current(at time.Time) error {
	switch {
	case !r.State.FeedsProduction():
		return errorf("source %s is %s; only a PRODUCTION or REVIEW_RENEW source feeds a pack", r.SourceID, r.State)
	case !r.Term.Covers(at):
		return errorf("source %s: licence term %s does not cover %s", r.SourceID, r.termString(), at.UTC().Format(time.RFC3339))
	case !at.Before(r.ReviewDue):
		return errorf("source %s: licence review was due %s", r.SourceID, r.ReviewDue.UTC().Format(time.RFC3339))
	}
	return nil
}

func (r SourceLicenseRecord) termString() string {
	end := "open-ended"
	if !r.Term.End.IsZero() {
		end = r.Term.End.UTC().Format(time.RFC3339)
	}
	return "[" + r.Term.Start.UTC().Format(time.RFC3339) + ", " + end + ")"
}

// DetectLapse reports the incident a record in use is in at instant at, if
// any: a licence whose term has ended while the record still says PRODUCTION
// or REVIEW_RENEW. That is a source-health incident (ZTAX-CONT-001 §6), not a
// date that quietly passed, and ZTAX-SRC-REQ-0029's automated expiry alert is
// a caller of this.
func (r SourceLicenseRecord) DetectLapse(at time.Time) (Incident, bool) {
	if !r.State.FeedsProduction() || r.Term.End.IsZero() || at.Before(r.Term.End) {
		return Incident{}, false
	}
	return Incident{Source: r.SourceID, Reason: ReasonLicenceExpired, Severity: SeverityR5, At: r.Term.End}, true
}

// Apply moves the record into the state an incident requires and returns the
// transition that did it.
func (r SourceLicenseRecord) Apply(i Incident, actor string) (SourceLicenseRecord, Transition, error) {
	if err := i.Validate(); err != nil {
		return r, Transition{}, err
	}
	if i.Source != r.SourceID {
		return r, Transition{}, errorf("incident is about %s, the record is %s", i.Source, r.SourceID)
	}
	to := StateSuspended
	if i.Withdraw {
		to = StateWithdrawn
	}
	t := Transition{From: r.State, To: to, Reason: i.Reason, At: i.At, Actor: actor}
	if err := t.Validate(); err != nil {
		return r, Transition{}, err
	}
	r.State = to
	return r, t, nil
}

// RightAfter reports whether right r is still exercisable for history after
// the record has left use for reason. For a reason that lapses the licence,
// only surviving rights continue; for any other reason the licence is intact
// and the profile still decides, though new use is stopped by the state.
func (r SourceLicenseRecord) RightAfter(right Right, reason Reason) bool {
	if reason.LapsesLicence() {
		return r.Termination.Survives(right)
	}
	g, ok := r.Rights.Grants[right]
	return ok && g.State == StateAllow
}

// Evaluation is the build-time rights decision for one source and one use —
// the "bundle build-time rights evaluation" SRC-001 §23 lists as evidence.
type Evaluation struct {
	Source    SourceID
	Mode      DeploymentMode
	Decisions []Decision
	// Violations is every reason the use is refused. Empty means permitted.
	Violations []string
}

// Permitted reports whether the use may proceed.
func (e Evaluation) Permitted() bool { return len(e.Violations) == 0 }

// Evaluate decides a use against the record at instant at.
//
// It reports every violation rather than the first, because the person
// reading a refused build is negotiating a licence or fixing a register, and
// one round trip per missing right is a week per right.
func (r SourceLicenseRecord) Evaluate(u Use, at time.Time) Evaluation {
	e := Evaluation{Source: r.SourceID, Mode: u.Mode}
	if err := r.Validate(); err != nil {
		e.Violations = append(e.Violations, err.Error())
		return e
	}
	if !r.Class.MayGroundProduction() {
		e.Violations = append(e.Violations,
			fmt.Sprintf("source %s is class %s; discovery-web material does not ground production content (ZTAX-SRC-REQ-0037)", r.SourceID, r.Class))
	}
	if err := r.Current(at); err != nil {
		e.Violations = append(e.Violations, err.Error())
	}
	if !u.Mode.Valid() {
		e.Violations = append(e.Violations, fmt.Sprintf("deployment mode %q is not one SRC-001 §15 defines", u.Mode))
	} else if !containsMode(r.Environments, u.Mode) {
		e.Violations = append(e.Violations,
			fmt.Sprintf("source %s is licensed for %s, not %s", r.SourceID, joinModes(r.Environments), u.Mode))
	}
	if len(r.Territories) > 0 {
		if len(u.Territories) == 0 {
			e.Violations = append(e.Violations,
				fmt.Sprintf("source %s is licensed only in %s, and the pack is not territorially bounded", r.SourceID, strings.Join(r.Territories, ", ")))
		}
		for _, t := range u.Territories {
			if !contains(r.Territories, t) {
				e.Violations = append(e.Violations,
					fmt.Sprintf("source %s is licensed only in %s; the pack covers %s", r.SourceID, strings.Join(r.Territories, ", "), t))
			}
		}
	}

	rights := append([]Right(nil), u.Rights...)
	sortRights(rights)
	for _, right := range dedupe(rights) {
		if !right.Valid() {
			e.Violations = append(e.Violations, fmt.Sprintf("the use asks for %q, which is not in the SRC-001 §4 taxonomy", right))
			continue
		}
		d := r.Rights.Evaluate(right, u)
		e.Decisions = append(e.Decisions, d)
		if !d.Permitted {
			e.Violations = append(e.Violations, fmt.Sprintf("source %s: %s is %s: %s", r.SourceID, right, d.State, d.Reason))
		}
	}
	return e
}

// ErrRightsGate is wrapped by every refusal Gate returns, so a caller can
// distinguish "the licence does not permit this" from a malformed input.
var ErrRightsGate = errors.New("sourcing: rights gate refused the build")

// Gate evaluates every source a build depends on, for every deployment mode
// it targets, and refuses unless all of them are permitted
// (ZTAX-SRC-REQ-0050, ZTAX-SRC-REQ-0051, ZTAX-CONT-REQ-0021).
//
// extra maps a source to rights the pack needs beyond BuildRights — a pack
// that shows a quotation needs quote, for instance. A source with no record is
// a violation, not a skip: "every production external source MUST have a
// registered SourceLicenseRecord" (ZTAX-SRC-REQ-0001). A build that depends on
// no source at all is refused too, because content traceable to no source is
// what ZTAX-CONT-REQ-0001 forbids.
func Gate(sources []SourceID, register map[SourceID]SourceLicenseRecord, extra map[SourceID][]Right,
	modes []DeploymentMode, territories []string, at time.Time) ([]Evaluation, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("%w: the pack declares no sources; content must be traceable to at least one (ZTAX-CONT-REQ-0001)", ErrRightsGate)
	}
	if len(modes) == 0 {
		return nil, fmt.Errorf("%w: the pack declares no deployment mode, so no use can be evaluated", ErrRightsGate)
	}
	ordered := append([]SourceID(nil), sources...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	ms := append([]DeploymentMode(nil), modes...)
	sort.Slice(ms, func(i, j int) bool { return ms[i] < ms[j] })

	var (
		out        []Evaluation
		violations []string
	)
	for _, id := range ordered {
		rec, ok := register[id]
		if !ok {
			violations = append(violations, fmt.Sprintf("source %s has no SourceLicenseRecord in the register (ZTAX-SRC-REQ-0001)", id))
			continue
		}
		if rec.SourceID != id {
			violations = append(violations, fmt.Sprintf("register entry %s describes source %s", id, rec.SourceID))
			continue
		}
		for _, m := range ms {
			rights := append(BuildRights(m), extra[id]...)
			e := rec.Evaluate(Use{Rights: rights, Mode: m, Territories: territories}, at)
			out = append(out, e)
			violations = append(violations, e.Violations...)
		}
	}
	if len(violations) > 0 {
		return out, fmt.Errorf("%w:\n  - %s", ErrRightsGate, strings.Join(violations, "\n  - "))
	}
	return out, nil
}

func dedupe(rs []Right) []Right {
	out := rs[:0]
	for i, r := range rs {
		if i == 0 || r != rs[i-1] {
			out = append(out, r)
		}
	}
	return out
}

func containsMode(set []DeploymentMode, m DeploymentMode) bool {
	for _, s := range set {
		if s == m {
			return true
		}
	}
	return false
}

func joinModes(ms []DeploymentMode) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = string(m)
	}
	return strings.Join(parts, ", ")
}
