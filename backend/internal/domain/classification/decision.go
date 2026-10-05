package classification

import (
	"fmt"
	"math/big"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// The dual decisions of ZTAX-CLS-001. A component is classified twice, for two
// different questions, by two different decision types: how it is taxed in a
// jurisdiction (TaxabilityDecision), and whether its revenue counts toward a
// regulatory regime's contribution base (RegulatoryRevenueDecision). Neither
// is inferred from the other (ZTAX-CLS-REQ-0004, -0005; ZTAX-OBL-REQ-0105),
// each has its own effective dates (ZTAX-CLS-REQ-0006), and neither carries a
// money amount — classification decides what a thing is for tax purposes, and
// determination and the obligation engine decide what is owed
// (ZTAX-CLS-REQ-0071, -0072).

// Status is a classification resolution status (ZTAX-CLS-REQ-0073 to -0077).
type Status string

// The statuses.
const (
	StatusResolved       Status = "RESOLVED"
	StatusAmbiguous      Status = "AMBIGUOUS"
	StatusConflicted     Status = "CONFLICTED"
	StatusUnsupported    Status = "UNSUPPORTED"
	StatusReviewRequired Status = "REVIEW_REQUIRED"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case StatusResolved, StatusAmbiguous, StatusConflicted, StatusUnsupported, StatusReviewRequired:
		return true
	}
	return false
}

// FeedsAuthoritative reports whether a decision in this status may reach an
// authoritative downstream path on its own. Only RESOLVED may
// (ZTAX-CLS-REQ-0078); a downstream specification that accepts another state
// says so where it consumes the decision, not here.
func (s Status) FeedsAuthoritative() bool { return s == StatusResolved }

// ContentRef pins the rule content a decision was made under
// (ZTAX-CLS-REQ-0065, -0070).
type ContentRef struct {
	BundleID     string
	BundleDigest string
	RuleID       string
}

func (c ContentRef) validate() error {
	if c.BundleID == "" || c.BundleDigest == "" || c.RuleID == "" {
		return fmt.Errorf("classification: decision pins no bundle, digest or rule")
	}
	return nil
}

// Effective is a decision's own effective period.
type Effective struct {
	From time.Time
	// To is exclusive; nil is open-ended.
	To *time.Time
}

func (e Effective) validate() error {
	if e.From.IsZero() {
		return fmt.Errorf("classification: decision is not effective-dated")
	}
	if e.To != nil && !e.To.After(e.From) {
		return fmt.Errorf("classification: decision ends before it starts")
	}
	return nil
}

// TaxabilityDecision is the taxability classification of one component
// instance in one jurisdiction.
type TaxabilityDecision struct {
	ID       id.ClassificationID
	TenantID id.TenantID
	// ComponentInstance and Ontology pin the canonical component and the
	// ontology release it was read from (ZTAX-CLS-REQ-0063, -0064).
	ComponentInstance string
	Node              OntologyID
	OntologyVersion   string
	Jurisdiction      string
	// Category is the jurisdiction's tax category for the component. Set
	// only when RESOLVED.
	Category   string
	Status     Status
	Provenance ContentRef
	Effective  Effective
	// Candidates are the conflicting categories kept for review
	// (ZTAX-CLS-REQ-0081).
	Candidates []string
}

// Validate refuses an incoherent taxability decision.
func (d TaxabilityDecision) Validate() error {
	switch {
	case d.ComponentInstance == "":
		return fmt.Errorf("classification: taxability decision names no component instance")
	case d.OntologyVersion == "":
		return fmt.Errorf("classification: taxability decision pins no ontology version")
	case d.Jurisdiction == "":
		return fmt.Errorf("classification: taxability decision names no jurisdiction")
	case !d.Status.Valid():
		return fmt.Errorf("classification: taxability decision has status %q", d.Status)
	}
	if err := d.Node.Validate(); err != nil {
		return err
	}
	if err := d.Provenance.validate(); err != nil {
		return err
	}
	if err := d.Effective.validate(); err != nil {
		return err
	}
	return checkResolution(d.Status, d.Category, d.Candidates)
}

// RevenueTreatment is a regulatory-revenue classification's outcome
// (ZTAX-CLS-REQ-0068).
type RevenueTreatment string

// The treatments.
const (
	RevenueIncluded    RevenueTreatment = "INCLUDED"
	RevenueExcluded    RevenueTreatment = "EXCLUDED"
	RevenuePartial     RevenueTreatment = "PARTIAL"
	RevenueConditional RevenueTreatment = "CONDITIONAL"
	RevenueAmbiguous   RevenueTreatment = "AMBIGUOUS"
	RevenueUnsupported RevenueTreatment = "UNSUPPORTED"
)

// RegulatoryRevenueDecision classifies one component's revenue for one
// regulatory regime.
type RegulatoryRevenueDecision struct {
	ID                id.ClassificationID
	TenantID          id.TenantID
	ComponentInstance string
	Node              OntologyID
	OntologyVersion   string
	// Regime is the regulatory regime (ZTAX-CLS-REQ-0067).
	Regime    string
	Treatment RevenueTreatment
	// AllocationBasis says how a PARTIAL inclusion is split
	// (ZTAX-CLS-REQ-0069). It names a basis; it computes nothing.
	AllocationBasis string
	Condition       string
	Status          Status
	Provenance      ContentRef
	Effective       Effective
}

// Validate refuses an incoherent regulatory-revenue decision.
func (d RegulatoryRevenueDecision) Validate() error {
	switch {
	case d.ComponentInstance == "" || d.OntologyVersion == "":
		return fmt.Errorf("classification: regulatory-revenue decision names no component or ontology version")
	case d.Regime == "":
		return fmt.Errorf("classification: regulatory-revenue decision names no regime")
	case !d.Status.Valid():
		return fmt.Errorf("classification: regulatory-revenue decision has status %q", d.Status)
	}
	if err := d.Node.Validate(); err != nil {
		return err
	}
	if err := d.Provenance.validate(); err != nil {
		return err
	}
	if err := d.Effective.validate(); err != nil {
		return err
	}
	switch d.Treatment {
	case RevenueIncluded, RevenueExcluded:
	case RevenuePartial:
		if d.AllocationBasis == "" {
			return fmt.Errorf("classification: a PARTIAL inclusion names no allocation basis")
		}
	case RevenueConditional:
		if d.Condition == "" {
			return fmt.Errorf("classification: a CONDITIONAL inclusion names no condition")
		}
	case RevenueAmbiguous, RevenueUnsupported:
		if d.Status == StatusResolved {
			return fmt.Errorf("classification: a %s treatment cannot be RESOLVED", d.Treatment)
		}
	default:
		return fmt.Errorf("classification: regulatory-revenue treatment %q", d.Treatment)
	}
	return nil
}

func checkResolution(s Status, category string, candidates []string) error {
	switch s {
	case StatusResolved:
		if category == "" {
			return fmt.Errorf("classification: a RESOLVED decision names no category")
		}
	case StatusConflicted:
		if len(candidates) < 2 {
			return fmt.Errorf("classification: a CONFLICTED decision keeps %d candidates; it keeps every conflicting one", len(candidates))
		}
		fallthrough
	default:
		if category != "" {
			// ZTAX-CLS-REQ-0079: an unresolved decision does not quietly
			// carry the highest-confidence candidate as its answer.
			return fmt.Errorf("classification: a %s decision carries category %q", s, category)
		}
	}
	return nil
}

// A3Policy is an approved bounded use case for constrained auto-mapping
// (ZTAX-CLS-REQ-0083, -0084).
type A3Policy struct {
	UseCase  string
	Approved bool
	// Threshold is the calibrated confidence threshold, a canonical decimal.
	Threshold string
}

// Proposal is an AI mapping proposal to an existing ontology node.
type Proposal struct {
	Target     OntologyID
	Confidence string
	// OutOfDistribution and Conflict are the guardrail signals on which the
	// mapping abstains (ZTAX-CLS-REQ-0085).
	OutOfDistribution bool
	Conflict          bool
	// Manifest is the AIReleaseManifest reference (ZTAX-CLS-REQ-0089).
	Manifest string
}

// AutoMapOutcome is what a proposal under a policy may do.
type AutoMapOutcome string

// The outcomes.
const (
	AutoMapAccept  AutoMapOutcome = "ACCEPT"
	AutoMapAbstain AutoMapOutcome = "ABSTAIN"
	AutoMapReview  AutoMapOutcome = "REVIEW"
)

// AutoMap decides whether an AI proposal may be accepted without review.
//
// It accepts only under an approved policy, only at or above the calibrated
// threshold, only with a manifest, and only to a node the ontology already
// declares — exists is the caller's ontology lookup, so the AI cannot create a
// node, a taxability category or a revenue category by proposing one
// (ZTAX-CLS-REQ-0086 to -0088). Out-of-distribution or conflicting input
// abstains whatever the confidence.
func AutoMap(p Proposal, policy A3Policy, exists func(OntologyID) bool) (AutoMapOutcome, error) {
	if p.Manifest == "" {
		return "", fmt.Errorf("classification: AI proposal carries no release manifest")
	}
	if p.OutOfDistribution || p.Conflict {
		return AutoMapAbstain, nil
	}
	if !policy.Approved || policy.UseCase == "" {
		return AutoMapReview, nil
	}
	if !exists(p.Target) {
		return AutoMapReview, nil
	}
	conf, ok := new(big.Rat).SetString(p.Confidence)
	if !ok {
		return "", fmt.Errorf("classification: confidence %q is not a decimal", p.Confidence)
	}
	threshold, ok := new(big.Rat).SetString(policy.Threshold)
	if !ok {
		return "", fmt.Errorf("classification: policy threshold %q is not a decimal", policy.Threshold)
	}
	if conf.Cmp(threshold) < 0 {
		return AutoMapReview, nil
	}
	return AutoMapAccept, nil
}
