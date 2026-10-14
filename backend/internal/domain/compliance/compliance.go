// Package compliance models the operational work that turns an obligation
// into a filing (ZTAX-COMP-001, W4 lane M).
//
// A CompliancePackage is the executable form of one obligation: who prepares
// it, who reviews and approves exactly what, how it is submitted, and what the
// authority said back. It never decides the obligation itself — the period,
// authority and responsible party are the ObligationDecision's, and a package
// only references them (ZTAX-COMP-REQ-0001 to -0003).
//
// The package row is immutable. Its state is its step history, folded: every
// preparation, review, approval, submission attempt and acknowledgement is a
// new step, so the lineage of a filing is never rewritten
// (ZTAX-COMP-REQ-0129).
//
// The rule this package is written around is the same one obligation's state
// machine is: a submission whose outcome is unknown is UNCERTAIN_SUBMISSION,
// and nothing leaves that state except a person recording what the authority
// actually did (ZTAX-COMP-REQ-0059, -0060). TRANSMITTING is not FILED, and a
// transport success is not an acknowledgement (ZTAX-COMP-REQ-0055, -0058).
package compliance

import (
	"fmt"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// The package's own reason codes.
var (
	// ReasonSegregationOfDuties is a reviewer or approver who prepared the
	// version they are asked to pass (ZTAX-COMP-REQ-0040, -0041).
	ReasonSegregationOfDuties = errs.Register("SEGREGATION_OF_DUTIES", errs.CategoryPolicy,
		"lane-m",
		"The person who prepared this version cannot review or approve it. Another person must.")

	// ReasonStaleVersion is a review or approval naming a version that is no
	// longer the one prepared (ZTAX-COMP-REQ-0044, -0045).
	ReasonStaleVersion = errs.Register("PREPARATION_VERSION_STALE", errs.CategoryConflict,
		"lane-m",
		"The preparation version named is not the current one. Read the package again and act on what it holds now.")

	// ReasonFilingBlocked is a submission the legal gate or the obligation's
	// state does not permit (ZTAX-COMP-REQ-0047, -0071, -0073).
	ReasonFilingBlocked = errs.Register("FILING_BLOCKED", errs.CategoryPolicy,
		"lane-m",
		"This filing may not be submitted now. The detail says which precondition failed.")
)

// Mode is who does the work (ZTAX-COMP-001 §3). It is set per package, so one
// tenant mixes modes across legal entities and jurisdictions
// (ZTAX-COMP-REQ-0005, -0006).
type Mode string

// The operating modes.
const (
	ModeSoftwareOnly Mode = "SOFTWARE_ONLY"
	ModeAssisted     Mode = "ASSISTED"
	ModeManagedPrep  Mode = "MANAGED_PREP"
	ModeManagedFile  Mode = "MANAGED_FILE"
	ModeFullManaged  Mode = "FULL_MANAGED_COMPLIANCE"
	ModePartnerLed   Mode = "PARTNER_LED"
)

// Valid reports whether the mode is one of §3's.
func (m Mode) Valid() bool {
	switch m {
	case ModeSoftwareOnly, ModeAssisted, ModeManagedPrep, ModeManagedFile, ModeFullManaged, ModePartnerLed:
		return true
	}
	return false
}

// Managed reports whether ZoikoTax, not the customer, carries the work.
func (m Mode) Managed() bool {
	return m == ModeManagedPrep || m == ModeManagedFile || m == ModeFullManaged
}

// Channel is how a filing reaches the authority (ZTAX-COMP-001 §13,
// ZTAX-COMP-REQ-0052).
type Channel string

// The submission channels.
const (
	ChannelDirectAPI    Channel = "DIRECT_API"
	ChannelEfileNetwork Channel = "EFILE_NETWORK"
	ChannelFileTransfer Channel = "FILE_TRANSFER"
	ChannelPortalRPA    Channel = "WEB_PORTAL_RPA"
	ChannelManualPortal Channel = "MANUAL_PORTAL"
	ChannelLocalPartner Channel = "LOCAL_PARTNER"
	ChannelPaper        Channel = "PAPER"
)

// Valid reports whether the channel is one of §13's.
func (c Channel) Valid() bool {
	switch c {
	case ChannelDirectAPI, ChannelEfileNetwork, ChannelFileTransfer, ChannelPortalRPA, ChannelManualPortal,
		ChannelLocalPartner, ChannelPaper:
		return true
	}
	return false
}

// Status is where the filing is (ZTAX-COMP-001 §14).
type Status string

// The statuses a package is in. The payment states of §14 are REM-001's and
// are not represented here: a successful filing is never PAID
// (ZTAX-COMP-REQ-0116).
const (
	StatusReady        Status = "READY"
	StatusInReview     Status = "IN_REVIEW"
	StatusApproved     Status = "APPROVED"
	StatusTransmitting Status = "TRANSMITTING"
	StatusUncertain    Status = "UNCERTAIN_SUBMISSION"
	StatusFiled        Status = "FILED"
	StatusAccepted     Status = "ACCEPTED"
	StatusRejected     Status = "REJECTED"
	StatusCancelled    Status = "CANCELLED"
)

// Terminal reports whether the package's work is finished.
func (s Status) Terminal() bool { return s == StatusAccepted || s == StatusCancelled }

// Submitted reports whether an attempt has left, or may have left, for the
// authority.
func (s Status) Submitted() bool {
	switch s {
	case StatusTransmitting, StatusUncertain, StatusFiled, StatusAccepted, StatusRejected:
		return true
	}
	return false
}

// ReviewDecision is a reviewer's verdict on one preparation version
// (ZTAX-COMP-001 §4).
type ReviewDecision string

// The review decisions.
const (
	ReviewAccept          ReviewDecision = "ACCEPT"
	ReviewReject          ReviewDecision = "REJECT"
	ReviewReturnForChange ReviewDecision = "RETURN_FOR_CHANGE"
	ReviewEscalate        ReviewDecision = "ESCALATE"
)

// Valid reports whether the decision is one of §4's.
func (d ReviewDecision) Valid() bool {
	switch d {
	case ReviewAccept, ReviewReject, ReviewReturnForChange, ReviewEscalate:
		return true
	}
	return false
}

// Acknowledgement is what the authority, or the network, answered
// (ZTAX-COMP-001 §16).
type Acknowledgement string

// The acknowledgements.
const (
	AckReceived              Acknowledgement = "RECEIVED"
	AckAccepted              Acknowledgement = "ACCEPTED"
	AckAcceptedWithWarnings  Acknowledgement = "ACCEPTED_WITH_WARNINGS"
	AckRejectedValidation    Acknowledgement = "REJECTED_VALIDATION"
	AckRejectedAuthorization Acknowledgement = "REJECTED_AUTHORIZATION"
	AckRejectedDuplicate     Acknowledgement = "REJECTED_DUPLICATE"
	AckPendingAuthority      Acknowledgement = "PENDING_AUTHORITY"
	AckUnknown               Acknowledgement = "UNKNOWN"
)

// Outcome is the package status an acknowledgement moves to, or false for an
// acknowledgement that is not one of §16's.
//
// UNKNOWN is UNCERTAIN_SUBMISSION, never a failure: no reliable structured
// outcome means we may have filed. RECEIVED and PENDING_AUTHORITY are FILED —
// receipt, not acceptance (ZTAX-COMP-REQ-0056, -0057).
func (a Acknowledgement) Outcome() (Status, bool) {
	switch a {
	case AckReceived, AckPendingAuthority:
		return StatusFiled, true
	case AckAccepted, AckAcceptedWithWarnings:
		return StatusAccepted, true
	case AckRejectedValidation, AckRejectedAuthorization, AckRejectedDuplicate:
		return StatusRejected, true
	case AckUnknown:
		return StatusUncertain, true
	}
	return "", false
}

// NeedsReference reports whether the acknowledgement claims the authority has
// the filing, which only the authority's own reference evidences
// (ZTAX-COMP-REQ-0056).
func (a Acknowledgement) NeedsReference() bool {
	o, _ := a.Outcome()
	return o == StatusFiled || o == StatusAccepted
}

// Resolution is how an uncertain submission was settled (ZTAX-COMP-001 §15).
type Resolution string

// The resolutions.
const (
	ResolutionConfirmedFiled    Resolution = "CONFIRMED_FILED"
	ResolutionConfirmedNotFiled Resolution = "CONFIRMED_NOT_FILED"
	ResolutionDuplicateDetected Resolution = "DUPLICATE_DETECTED"
	ResolutionManual            Resolution = "MANUAL_INVESTIGATION"
)

// Outcome is the status a resolution moves an uncertain package to.
// MANUAL_INVESTIGATION records the work and leaves it uncertain; only
// CONFIRMED_NOT_FILED reopens the way to another attempt, and that attempt
// is linked to this one (ZTAX-COMP-REQ-0061).
func (r Resolution) Outcome() (Status, bool) {
	switch r {
	case ResolutionConfirmedFiled, ResolutionDuplicateDetected:
		return StatusFiled, true
	case ResolutionConfirmedNotFiled:
		return StatusApproved, true
	case ResolutionManual:
		return StatusUncertain, true
	}
	return "", false
}

// StepKind is what a step records.
type StepKind string

// The step kinds. CREATED is always the first and only the first.
const (
	StepCreated      StepKind = "CREATED"
	StepPrepared     StepKind = "PREPARED"
	StepReviewed     StepKind = "REVIEWED"
	StepApproved     StepKind = "APPROVED"
	StepSubmitted    StepKind = "SUBMISSION_STARTED"
	StepAcknowledged StepKind = "ACKNOWLEDGED"
	StepResolved     StepKind = "UNCERTAIN_RESOLVED"
	StepCancelled    StepKind = "CANCELLED"
)

// Step is one act on a package. The fields a kind does not use are zero.
type Step struct {
	Seq  int
	Kind StepKind
	// Status is the package's status after this step.
	Status Status

	// Version and PayloadHash identify the preparation the step acted on.
	Version     int
	PayloadHash canonical.Digest
	// ControlTotal is the figure a preparation carries: the obligation's
	// assessment when it was prepared. Nil for a non-monetary duty.
	ControlTotal *fiscal.Money

	Review          ReviewDecision
	Submission      id.SubmissionAttemptID
	Channel         Channel
	Acknowledgement Acknowledgement
	Resolution      Resolution
	// AuthorityRef is the authority's own receipt or reference.
	AuthorityRef string
	// Evidence is the raw authority response, or the operator's account of
	// it, kept as received (ZTAX-COMP-REQ-0063).
	Evidence string
	Note     string

	RecordedAt time.Time
	RecordedBy id.UserID
}

// Package is one obligation's compliance package, folded from its steps.
type Package struct {
	ID       id.CompliancePackageID
	TenantID id.TenantID

	// The obligation it executes, as it stood when the package was made, and
	// the business key that finds its current row.
	Obligation    id.ObligationID
	ObligationKey string
	// Responsibility is the ResponsibilityDecision the obligation names, zero
	// while the obligation names none.
	Responsibility id.ResponsibilityDecisionID
	LegalEntity    id.LegalEntityID
	ObligationType string
	Jurisdiction   string
	PeriodStart    time.Time
	PeriodEnd      time.Time

	Mode    Mode
	Channel Channel
	// FilingCountry and FilingAuthority name the authority in the legal
	// matrix the submission is gated against.
	FilingCountry   string
	FilingAuthority string

	// Dates (ZTAX-COMP-001 §6). StatutoryDue is the obligation's legal due
	// date and is never overwritten; the internal targets are ours and may
	// only be earlier (ZTAX-COMP-REQ-0008, -0011, -0096).
	StatutoryDue time.Time
	Timezone     string
	PrepareDue   time.Time
	ReviewDue    time.Time
	SubmitTarget time.Time

	CreatedAt time.Time
	CreatedBy id.UserID

	// Folded from the steps.
	Status  Status
	History []Step
}

// Validate refuses a package that cannot be created.
func (p Package) Validate() error {
	switch {
	case p.Obligation.IsZero() || p.ObligationKey == "":
		return fmt.Errorf("compliance: package references no obligation")
	case p.LegalEntity.IsZero():
		return fmt.Errorf("compliance: package names no legal entity")
	case !p.Mode.Valid():
		return fmt.Errorf("compliance: operating mode %q is not one this service knows", p.Mode)
	case !p.Channel.Valid():
		return fmt.Errorf("compliance: submission channel %q is not one this service knows", p.Channel)
	case len(p.FilingCountry) != 2 || strings.ToUpper(p.FilingCountry) != p.FilingCountry:
		return fmt.Errorf("compliance: filing country is an ISO 3166-1 alpha-2 code")
	case strings.TrimSpace(p.FilingAuthority) == "":
		return fmt.Errorf("compliance: package names no filing authority")
	case p.StatutoryDue.IsZero():
		return fmt.Errorf("compliance: package has no statutory due date")
	case p.Timezone == "":
		return fmt.Errorf("compliance: package names no authority timezone")
	}
	for _, t := range []struct {
		name string
		at   time.Time
	}{{"prepare", p.PrepareDue}, {"review", p.ReviewDue}, {"submit", p.SubmitTarget}} {
		if !t.at.IsZero() && t.at.After(p.StatutoryDue) {
			return fmt.Errorf("compliance: the internal %s target falls after the statutory due date", t.name)
		}
	}
	if !p.PrepareDue.IsZero() && !p.ReviewDue.IsZero() && p.ReviewDue.Before(p.PrepareDue) {
		return fmt.Errorf("compliance: the review target precedes the preparation target")
	}
	if !p.ReviewDue.IsZero() && !p.SubmitTarget.IsZero() && p.SubmitTarget.Before(p.ReviewDue) {
		return fmt.Errorf("compliance: the submission target precedes the review target")
	}
	return nil
}

// transitions is the package's state machine, as a table. The entries that
// matter: UNCERTAIN_SUBMISSION leads only where a recorded resolution takes
// it, and nothing leads from TRANSMITTING or UNCERTAIN_SUBMISSION back to a
// state a new attempt starts from, except CONFIRMED_NOT_FILED.
var transitions = map[Status][]Status{
	StatusReady:        {StatusInReview, StatusCancelled},
	StatusInReview:     {StatusInReview, StatusReady, StatusApproved, StatusCancelled},
	StatusApproved:     {StatusInReview, StatusTransmitting, StatusCancelled},
	StatusTransmitting: {StatusFiled, StatusAccepted, StatusRejected, StatusUncertain},
	StatusUncertain:    {StatusUncertain, StatusFiled, StatusApproved},
	StatusFiled:        {StatusFiled, StatusAccepted, StatusRejected},
	StatusRejected:     {StatusInReview},
	StatusAccepted:     nil,
	StatusCancelled:    nil,
}

// CanTransition reports whether a move is permitted.
func CanTransition(from, to Status) bool {
	for _, allowed := range transitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// Fold applies a package's steps in order and returns it with its status and
// history. It refuses a history no correct writer produces.
func Fold(p Package, steps []Step) (Package, error) {
	if len(steps) == 0 || steps[0].Kind != StepCreated || steps[0].Seq != 1 {
		return Package{}, fmt.Errorf("compliance: a package's history starts with its CREATED step")
	}
	p.Status = steps[0].Status
	for i, s := range steps[1:] {
		if s.Seq != i+2 || s.Kind == StepCreated {
			return Package{}, fmt.Errorf("compliance: step %d is out of sequence", s.Seq)
		}
		if !CanTransition(p.Status, s.Status) {
			return Package{}, fmt.Errorf("compliance: step %d moves %s to %s", s.Seq, p.Status, s.Status)
		}
		p.Status = s.Status
	}
	p.History = steps
	return p, nil
}

// next is the step that follows the history, with its sequence number and
// time stamped.
func (p Package) next(kind StepKind, to Status, by id.UserID, at time.Time) (Step, error) {
	if !CanTransition(p.Status, to) {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("A package that is %s cannot move to %s.", p.Status, to))
	}
	if by.IsZero() {
		return Step{}, errs.New(errs.CategoryPolicy, errs.ReasonForbidden, "Every compliance step is recorded against a person.")
	}
	return Step{Seq: len(p.History) + 1, Kind: kind, Status: to, RecordedAt: at.UTC(), RecordedBy: by}, nil
}

// Preparation is the current preparation version, or nil before the first.
func (p Package) Preparation() *Step {
	for i := len(p.History) - 1; i >= 0; i-- {
		if p.History[i].Kind == StepPrepared {
			return &p.History[i]
		}
	}
	return nil
}

// acceptedReview is the reviewer's ACCEPT of the current preparation, if the
// last review of it was one.
func (p Package) acceptedReview(version int) *Step {
	for i := len(p.History) - 1; i >= 0; i-- {
		s := p.History[i]
		if s.Kind == StepPrepared {
			return nil
		}
		if s.Kind == StepReviewed && s.Version == version {
			if s.Review == ReviewAccept {
				return &p.History[i]
			}
			return nil
		}
	}
	return nil
}

// Approval is the approval of the current preparation, or nil when the
// current version has none. A new preparation invalidates any earlier
// approval simply by being newer (ZTAX-COMP-REQ-0045).
func (p Package) Approval() *Step {
	for i := len(p.History) - 1; i >= 0; i-- {
		switch p.History[i].Kind {
		case StepPrepared:
			return nil
		case StepApproved:
			return &p.History[i]
		}
	}
	return nil
}

// Attempt is the latest submission attempt, or nil before the first.
func (p Package) Attempt() *Step {
	for i := len(p.History) - 1; i >= 0; i-- {
		if p.History[i].Kind == StepSubmitted {
			return &p.History[i]
		}
	}
	return nil
}

// Prepare records a new preparation version. From IN_REVIEW or APPROVED it
// supersedes the version under review, and with it any approval
// (ZTAX-COMP-REQ-0035, -0045); from REJECTED it is the remediation
// (ZTAX-COMP-REQ-0065).
func (p Package) Prepare(payloadHash canonical.Digest, controlTotal *fiscal.Money, note string, by id.UserID, at time.Time) (Step, error) {
	if payloadHash.IsZero() {
		return Step{}, errs.New(errs.CategoryInternal, errs.ReasonInternal, "A preparation is recorded with its payload hash.")
	}
	s, err := p.next(StepPrepared, StatusInReview, by, at)
	if err != nil {
		return Step{}, err
	}
	version := 1
	if prev := p.Preparation(); prev != nil {
		version = prev.Version + 1
	}
	s.Version, s.PayloadHash, s.ControlTotal, s.Note = version, payloadHash, controlTotal, note
	return s, nil
}

// Review records a reviewer's decision on the version they name. The
// reviewer is never the version's preparer (ZTAX-COMP-REQ-0040, -0041), and
// the version must be the current one.
func (p Package) Review(version int, decision ReviewDecision, note string, by id.UserID, at time.Time) (Step, error) {
	if !decision.Valid() {
		return Step{}, errs.Invalid("decision", errs.ReasonInvalidValue,
			fmt.Sprintf("%q is not a review decision; ACCEPT, REJECT, RETURN_FOR_CHANGE or ESCALATE.", decision))
	}
	if p.Status != StatusInReview {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only a package in review is reviewed; this one is %s.", p.Status))
	}
	prep, err := p.current(version)
	if err != nil {
		return Step{}, err
	}
	if prep.RecordedBy == by {
		return Step{}, errs.New(errs.CategoryPolicy, ReasonSegregationOfDuties,
			"The preparer of a version cannot review it; another person must (ZTAX-COMP-REQ-0041).")
	}
	if (decision == ReviewReject || decision == ReviewReturnForChange || decision == ReviewEscalate) && strings.TrimSpace(note) == "" {
		return Step{}, errs.Invalid("note", errs.ReasonMissingField, "A review that does not accept says why.")
	}
	to := StatusInReview
	if decision == ReviewReject || decision == ReviewReturnForChange {
		to = StatusReady
	}
	s, err := p.next(StepReviewed, to, by, at)
	if err != nil {
		return Step{}, err
	}
	s.Version, s.PayloadHash, s.Review, s.Note = prep.Version, prep.PayloadHash, decision, note
	return s, nil
}

// Approve pins the exact version and payload hash that may be submitted
// (ZTAX-COMP-REQ-0044). It needs an accepting review of that version, and an
// approver who did not prepare it.
func (p Package) Approve(version int, payloadHash canonical.Digest, note string, by id.UserID, at time.Time) (Step, error) {
	if p.Status != StatusInReview {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only a package in review is approved; this one is %s.", p.Status))
	}
	prep, err := p.current(version)
	if err != nil {
		return Step{}, err
	}
	if payloadHash != prep.PayloadHash {
		return Step{}, errs.New(errs.CategoryConflict, ReasonStaleVersion,
			"The payload hash approved is not the hash of the version prepared; approval is of exact content.")
	}
	if p.acceptedReview(prep.Version) == nil {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Version %d has no accepting review; it is reviewed before it is approved.", prep.Version))
	}
	if prep.RecordedBy == by {
		return Step{}, errs.New(errs.CategoryPolicy, ReasonSegregationOfDuties,
			"The preparer of a version cannot approve it; another person must (ZTAX-COMP-REQ-0041).")
	}
	s, err := p.next(StepApproved, StatusApproved, by, at)
	if err != nil {
		return Step{}, err
	}
	s.Version, s.PayloadHash, s.Note = prep.Version, prep.PayloadHash, note
	return s, nil
}

// Submit starts an attempt with the approved payload. Each attempt has its
// own identity and carries the approved hash (ZTAX-COMP-REQ-0050, -0051).
// A package that is UNCERTAIN_SUBMISSION is refused by the state machine
// itself: there is no edge from it to TRANSMITTING (ZTAX-COMP-REQ-0060).
func (p Package) Submit(attempt id.SubmissionAttemptID, note string, by id.UserID, at time.Time) (Step, error) {
	if p.Status != StatusApproved {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only an approved package is submitted; this one is %s.", p.Status))
	}
	appr := p.Approval()
	if appr == nil {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"The current version has no approval; it is approved before it is submitted.")
	}
	if attempt.IsZero() {
		return Step{}, errs.New(errs.CategoryInternal, errs.ReasonInternal, "A submission attempt is recorded with its identifier.")
	}
	s, err := p.next(StepSubmitted, StatusTransmitting, by, at)
	if err != nil {
		return Step{}, err
	}
	s.Version, s.PayloadHash, s.Submission, s.Channel, s.Note = appr.Version, appr.PayloadHash, attempt, p.Channel, note
	return s, nil
}

// Acknowledge records the authority's answer to the latest attempt. It never
// upgrades the state beyond the evidence: a claim of receipt or acceptance
// carries the authority's reference, and every answer carries the raw
// response (ZTAX-COMP-REQ-0056, -0063).
func (p Package) Acknowledge(ack Acknowledgement, authorityRef, evidence, note string, by id.UserID, at time.Time) (Step, error) {
	to, ok := ack.Outcome()
	if !ok {
		return Step{}, errs.Invalid("acknowledgement", errs.ReasonInvalidValue,
			fmt.Sprintf("%q is not an acknowledgement this service knows.", ack))
	}
	if p.Status != StatusTransmitting && p.Status != StatusFiled {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("An acknowledgement answers a transmitting or filed submission; this package is %s.", p.Status))
	}
	if ack.NeedsReference() && strings.TrimSpace(authorityRef) == "" {
		return Step{}, errs.Invalid("authorityReference", errs.ReasonMissingField,
			fmt.Sprintf("%s says the authority has the filing, which its own reference evidences (ZTAX-COMP-REQ-0056).", ack))
	}
	if strings.TrimSpace(evidence) == "" {
		return Step{}, errs.Invalid("evidence", errs.ReasonMissingField,
			"The authority's response is recorded as received (ZTAX-COMP-REQ-0063).")
	}
	if p.Status == StatusFiled && to == StatusUncertain {
		// A filed return has the authority's receipt; nothing afterwards makes
		// it uncertain whether it was filed.
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"A filed submission has the authority's receipt; it cannot become uncertain.")
	}
	attempt := p.Attempt()
	s, err := p.next(StepAcknowledged, to, by, at)
	if err != nil {
		return Step{}, err
	}
	s.Version, s.PayloadHash, s.Submission, s.Channel = attempt.Version, attempt.PayloadHash, attempt.Submission, attempt.Channel
	s.Acknowledgement, s.AuthorityRef, s.Evidence, s.Note = ack, strings.TrimSpace(authorityRef), evidence, note
	return s, nil
}

// Resolve settles an uncertain submission with what a person established at
// the authority (ZTAX-COMP-001 §15). The attempt it resolves stays in the
// history, and its payload identity with it (ZTAX-COMP-REQ-0061).
func (p Package) Resolve(res Resolution, authorityRef, evidence string, by id.UserID, at time.Time) (Step, error) {
	to, ok := res.Outcome()
	if !ok {
		return Step{}, errs.Invalid("resolution", errs.ReasonInvalidValue,
			fmt.Sprintf("%q is not a resolution; CONFIRMED_FILED, CONFIRMED_NOT_FILED, DUPLICATE_DETECTED or MANUAL_INVESTIGATION.", res))
	}
	if p.Status != StatusUncertain {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("Only an uncertain submission is resolved; this package is %s.", p.Status))
	}
	if strings.TrimSpace(evidence) == "" {
		return Step{}, errs.Invalid("evidence", errs.ReasonMissingField,
			"A resolution records what was established at the authority, and how.")
	}
	if (res == ResolutionConfirmedFiled || res == ResolutionDuplicateDetected) && strings.TrimSpace(authorityRef) == "" {
		return Step{}, errs.Invalid("authorityReference", errs.ReasonMissingField,
			"Confirming a filing names the authority's reference for it.")
	}
	attempt := p.Attempt()
	s, err := p.next(StepResolved, to, by, at)
	if err != nil {
		return Step{}, err
	}
	s.Version, s.PayloadHash, s.Submission, s.Channel = attempt.Version, attempt.PayloadHash, attempt.Submission, attempt.Channel
	s.Resolution, s.AuthorityRef, s.Evidence = res, strings.TrimSpace(authorityRef), evidence
	return s, nil
}

// Cancel closes a package that has not been submitted. The reason stays with
// it; nothing is deleted.
func (p Package) Cancel(reason string, by id.UserID, at time.Time) (Step, error) {
	if strings.TrimSpace(reason) == "" {
		return Step{}, errs.Invalid("reason", errs.ReasonMissingField, "A cancellation gives a reason.")
	}
	if p.Status.Submitted() {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"A submitted filing is not cancelled; it is amended through the authority.")
	}
	s, err := p.next(StepCancelled, StatusCancelled, by, at)
	if err != nil {
		return Step{}, err
	}
	s.Note = reason
	return s, nil
}

func (p Package) current(version int) (Step, error) {
	prep := p.Preparation()
	if prep == nil {
		return Step{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid, "The package has not been prepared.")
	}
	if version != prep.Version {
		return Step{}, errs.New(errs.CategoryConflict, ReasonStaleVersion,
			fmt.Sprintf("Version %d is not the current preparation; version %d is.", version, prep.Version))
	}
	return *prep, nil
}

// ---------------------------------------------------------------------------
// deadlines and priority
// ---------------------------------------------------------------------------

// Overdue is which deadlines a package has missed, as of an instant. The
// statutory and internal breaches are separate facts (ZTAX-COMP-REQ-0017,
// -0121): missing our own target is not missing the authority's.
type Overdue struct {
	Statutory bool
	Internal  bool
}

// OverdueAt reports the breaches as of asOf. A submitted package has met the
// deadline it was submitted against.
func (p Package) OverdueAt(asOf time.Time) Overdue {
	if p.Status.Submitted() || p.Status.Terminal() {
		return Overdue{}
	}
	var o Overdue
	o.Statutory = asOf.After(p.StatutoryDue)
	for _, t := range []time.Time{p.PrepareDue, p.ReviewDue, p.SubmitTarget} {
		if !t.IsZero() && asOf.After(t) {
			o.Internal = true
		}
	}
	return o
}

// Priority is the package's place in the queue (ZTAX-COMP-001 §22).
type Priority string

// The priorities.
const (
	PriorityP0 Priority = "P0" // legal deadline imminent
	PriorityP1 Priority = "P1" // material blocker
	PriorityP2 Priority = "P2" // required review
	PriorityP3 Priority = "P3" // normal execution
	PriorityP4 Priority = "P4" // follow-up
	PriorityP5 Priority = "P5" // done, or nothing owed
)

// imminent is how close the statutory deadline is before it outranks
// everything else.
const imminent = 72 * time.Hour

// PriorityAt ranks the package as of asOf. It combines the statutory time
// left with what the package is waiting on, not arrival order
// (ZTAX-COMP-REQ-0091). An uncertain submission near its deadline is P0: the
// escalation §15 requires, rather than a silent retry (ZTAX-COMP-REQ-0062).
func (p Package) PriorityAt(asOf time.Time) Priority {
	left := p.StatutoryDue.Sub(asOf)
	switch p.Status {
	case StatusAccepted, StatusCancelled:
		return PriorityP5
	case StatusUncertain:
		if left <= imminent {
			return PriorityP0
		}
		return PriorityP1
	case StatusRejected:
		if left <= imminent {
			return PriorityP0
		}
		return PriorityP1
	case StatusTransmitting, StatusFiled:
		return PriorityP4
	}
	if left <= imminent {
		return PriorityP0
	}
	if p.Status == StatusInReview || p.Status == StatusApproved {
		return PriorityP2
	}
	return PriorityP3
}

// Canonical renders what the package files for, for the payload a preparation
// hashes.
func (p Package) Canonical() canonical.Value {
	return canonical.Object(
		canonical.F("obligationId", canonical.String(p.Obligation.String())),
		canonical.F("obligationType", canonical.String(p.ObligationType)),
		canonical.F("jurisdiction", canonical.String(p.Jurisdiction)),
		canonical.F("legalEntityId", canonical.String(p.LegalEntity.String())),
		canonical.F("periodStart", canonical.String(p.PeriodStart.Format(time.DateOnly))),
		canonical.F("periodEnd", canonical.String(p.PeriodEnd.Format(time.DateOnly))),
		canonical.F("filingCountry", canonical.String(p.FilingCountry)),
		canonical.F("filingAuthority", canonical.String(p.FilingAuthority)),
	)
}
