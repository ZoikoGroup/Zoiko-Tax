package sourcing

import (
	"time"
)

// OnboardingState is SRC-001 §6's source onboarding lifecycle.
//
// The names are the specification's, with two spellings forced by the wire:
// "REVIEW/RENEW" is REVIEW_RENEW, and the single "SUSPEND/WITHDRAW" row is two
// states, SUSPENDED and WITHDRAWN, because they have different exits — a
// suspended source can be remediated and re-reviewed, a withdrawn one is
// retired — and a state machine that merged them could not say which.
type OnboardingState string

// The onboarding states, in the order a source passes through them.
const (
	StateDiscover               OnboardingState = "DISCOVER"
	StateRightsScreen           OnboardingState = "RIGHTS_SCREEN"
	StateLegalReview            OnboardingState = "LEGAL_REVIEW"
	StateSecurityPrivacyReview  OnboardingState = "SECURITY_PRIVACY_REVIEW"
	StateCommercialApproval     OnboardingState = "COMMERCIAL_APPROVAL"
	StateTechnicalQualification OnboardingState = "TECHNICAL_QUALIFICATION"
	StateRightsProfileApproved  OnboardingState = "RIGHTS_PROFILE_APPROVED"
	StateIngestionEnabled       OnboardingState = "INGESTION_ENABLED"
	StateContentValidation      OnboardingState = "CONTENT_VALIDATION"
	StateProduction             OnboardingState = "PRODUCTION"
	StateReviewRenew            OnboardingState = "REVIEW_RENEW"
	StateSuspended              OnboardingState = "SUSPENDED"
	StateWithdrawn              OnboardingState = "WITHDRAWN"
)

// onboarding is the pre-production path, in order. It is strictly linear and
// no step may be skipped. SRC-001 qualifies two of them ("where required"), and
// the qualification is honoured by passing through the step with a recorded
// "not required" finding rather than by a transition that jumps it: a skipped
// step leaves no record that anybody decided it could be skipped.
var onboarding = []OnboardingState{
	StateDiscover, StateRightsScreen, StateLegalReview, StateSecurityPrivacyReview, StateCommercialApproval,
	StateTechnicalQualification, StateRightsProfileApproved, StateIngestionEnabled, StateContentValidation,
	StateProduction,
}

// Valid reports whether s is a known state.
func (s OnboardingState) Valid() bool {
	if s == StateReviewRenew || s == StateSuspended || s == StateWithdrawn {
		return true
	}
	return onboardingIndex(s) >= 0
}

func onboardingIndex(s OnboardingState) int {
	for i, k := range onboarding {
		if k == s {
			return i
		}
	}
	return -1
}

// FeedsProduction reports whether a source in this state may feed a pack
// build. PRODUCTION may, and so may REVIEW_RENEW: a periodic review is a source
// in use being looked at again, not a source taken out of use, and treating it
// as the latter would suspend every pack on every renewal cycle. Everything
// before PRODUCTION has not finished onboarding; SUSPENDED and WITHDRAWN have
// left it.
func (s OnboardingState) FeedsProduction() bool { return s == StateProduction || s == StateReviewRenew }

// Terminal reports whether no transition leaves s.
func (s OnboardingState) Terminal() bool { return s == StateWithdrawn }

// Reason is why a source was suspended or withdrawn (SRC-001 §6: "rights,
// quality, breach, expiry or legal issue disables future use").
type Reason string

// The reasons.
const (
	ReasonLicenceExpired    Reason = "LICENCE_EXPIRED"
	ReasonLicenceTerminated Reason = "LICENCE_TERMINATED"
	ReasonLicenceBreach     Reason = "LICENCE_BREACH"
	ReasonRightsRestricted  Reason = "RIGHTS_RESTRICTED"
	ReasonQualityFailure    Reason = "QUALITY_FAILURE"
	ReasonSourceUnavailable Reason = "SOURCE_UNAVAILABLE"
	ReasonLegalIssue        Reason = "LEGAL_ISSUE"
	// ReasonCandidateRejected is the reason a source still onboarding is
	// withdrawn: it never reached production, and nothing depends on it.
	ReasonCandidateRejected Reason = "CANDIDATE_REJECTED"
)

// Valid reports whether r is a known reason.
func (r Reason) Valid() bool {
	switch r {
	case ReasonLicenceExpired, ReasonLicenceTerminated, ReasonLicenceBreach, ReasonRightsRestricted,
		ReasonQualityFailure, ReasonSourceUnavailable, ReasonLegalIssue, ReasonCandidateRejected:
		return true
	}
	return false
}

// LapsesLicence reports whether the reason ends the licence itself, so that
// only the record's surviving rights continue (ZTAX-SRC-REQ-0030). A quality
// failure or an unavailable source suspends use with the licence intact.
func (r Reason) LapsesLicence() bool {
	return r == ReasonLicenceExpired || r == ReasonLicenceTerminated || r == ReasonLicenceBreach
}

// CanTransition reports whether from → to is a legal step.
//
// The legal steps, and only these:
//
//	onboarding state n      → n+1                 (no skipping; see onboarding)
//	LEGAL_REVIEW … CONTENT_VALIDATION → RIGHTS_SCREEN   (re-screen on a finding)
//	any onboarding state    → WITHDRAWN           (candidate rejected)
//	PRODUCTION              → REVIEW_RENEW | SUSPENDED | WITHDRAWN
//	REVIEW_RENEW            → PRODUCTION | SUSPENDED | WITHDRAWN
//	SUSPENDED               → REVIEW_RENEW | WITHDRAWN
//	WITHDRAWN               → nothing
//
// SUSPENDED goes back through REVIEW_RENEW rather than straight to PRODUCTION
// because a suspension is a finding that something changed, and returning to
// production without the review that establishes it changed back would make
// the suspension a pause button. A suspended source never re-enters onboarding
// either: its identity, history and dependent packs carry on, which is what
// makes an expiry an incident on a known source rather than a new source.
func CanTransition(from, to OnboardingState) bool {
	if !from.Valid() || !to.Valid() || from == to {
		return false
	}
	switch from {
	case StateProduction:
		return to == StateReviewRenew || to == StateSuspended || to == StateWithdrawn
	case StateReviewRenew:
		return to == StateProduction || to == StateSuspended || to == StateWithdrawn
	case StateSuspended:
		return to == StateReviewRenew || to == StateWithdrawn
	case StateWithdrawn:
		return false
	}
	i := onboardingIndex(from)
	switch {
	case to == StateWithdrawn:
		return true
	case onboardingIndex(to) == i+1:
		return true
	case to == StateRightsScreen && i > onboardingIndex(StateRightsScreen):
		return true
	}
	return false
}

// Transition is one recorded step of a source's lifecycle.
//
// A step into SUSPENDED or WITHDRAWN carries a Reason, and no other step does:
// leaving use is the event an auditor will ask "why" about (ZTAX-SRC-REQ-0093
// makes rights changes auditable), and a reason on an ordinary promotion would
// be noise that trains a reader to skip the field.
type Transition struct {
	From, To OnboardingState
	Reason   Reason
	At       time.Time
	// Actor is who made the step. A rights profile reaching
	// RIGHTS_PROFILE_APPROVED is a General Counsel decision (SRC-001 §28) and
	// the record has to say whose.
	Actor string
}

// Validate checks the step is legal and carries what it must.
func (t Transition) Validate() error {
	if !CanTransition(t.From, t.To) {
		return errorf("%s → %s is not a legal onboarding transition", t.From, t.To)
	}
	leaving := t.To == StateSuspended || t.To == StateWithdrawn
	switch {
	case leaving && !t.Reason.Valid():
		return errorf("%s → %s needs a reason", t.From, t.To)
	case !leaving && t.Reason != "":
		return errorf("%s → %s carries reason %s; only a suspension or withdrawal does", t.From, t.To, t.Reason)
	case t.At.IsZero():
		return errorf("%s → %s names no instant", t.From, t.To)
	case t.Actor == "":
		return errorf("%s → %s names no actor", t.From, t.To)
	}
	return nil
}

// Severity is SRC-001 §22's rights-incident severity.
type Severity string

// The severities.
const (
	SeverityR1 Severity = "R1" // attribution/notice defect, no unauthorised access
	SeverityR2 Severity = "R2" // use outside seat/volume/territory/environment
	SeverityR3 Severity = "R3" // unauthorised display/export or AI corpus inclusion
	SeverityR4 Severity = "R4" // unauthorised redistribution or material breach
	SeverityR5 Severity = "R5" // systemic breach, injunction risk, source termination
)

// Valid reports whether s is a known severity.
func (s Severity) Valid() bool {
	switch s {
	case SeverityR1, SeverityR2, SeverityR3, SeverityR4, SeverityR5:
		return true
	}
	return false
}

// Incident is a source leaving use: an expiry, a termination, a breach, or a
// quality or legal finding.
//
// The Build Plan's risk register treats licence expiry and withdrawal as an
// incident with a defined pack path rather than as a quiet change of a date,
// and this is that incident. The source half of the path is here (the record
// moves to SUSPENDED or WITHDRAWN, and only surviving rights continue); the
// pack half — every pack that depends on the source is suspended for new
// outcomes and keeps replaying history where replay survives — is
// internal/domain/content.RespondToSourceIncident, because it is a statement
// about packs.
type Incident struct {
	Source   SourceID
	Reason   Reason
	Severity Severity
	At       time.Time
	// Withdraw is true when the source is retired rather than suspended.
	Withdraw bool
}

// Validate checks the incident is complete.
func (i Incident) Validate() error {
	switch {
	case i.Source == "":
		return errorf("an incident names the source it is about")
	case !i.Reason.Valid() || i.Reason == ReasonCandidateRejected:
		return errorf("incident on %s has reason %q, which is not an in-use reason", i.Source, i.Reason)
	case !i.Severity.Valid():
		return errorf("incident on %s has severity %q; SRC-001 §22 defines R1 to R5", i.Source, i.Severity)
	case i.At.IsZero():
		return errorf("incident on %s names no instant", i.Source)
	}
	return nil
}
