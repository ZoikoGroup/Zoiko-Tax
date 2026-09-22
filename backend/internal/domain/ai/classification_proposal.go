package ai

import "fmt"

// AiClassificationProposal is a proposed classification from the AI plane —
// the advisory counterpart to a fiscal ClassificationDecision. ADR-0006
// §2.6: nothing in this package converts one into the other.
type AiClassificationProposal struct {
	id AiClassificationProposalID

	subjectRef string

	// ProposedCode is the classification code the model proposed, in
	// whatever code system the caller's classification content defines.
	// This package does not validate it against that system — validation
	// against real classification content is classification-domain
	// territory, on the far side of the boundary this package exists to
	// hold.
	proposedCode string

	// Confidence is carried as a canonical decimal string in [0,1], not a
	// float64 — consistent with the project-wide avoidance of float64 for
	// any value that ends up in evidence or telemetry. This is a defensive
	// design choice for this package, not a literal ADR-0006 requirement;
	// confirm it against team convention before relying on it.
	confidence string

	provenance Provenance

	reviewed   bool
	reviewerID string
}

// NewAiClassificationProposal constructs an AiClassificationProposal.
// confidence must be a decimal string in [0,1] or empty if the model did
// not report one.
func NewAiClassificationProposal(
	id AiClassificationProposalID,
	subjectRef string,
	proposedCode string,
	confidence string,
	provenance Provenance,
) (AiClassificationProposal, error) {
	if subjectRef == "" {
		return AiClassificationProposal{}, fmt.Errorf("ai: empty subject reference")
	}
	if proposedCode == "" {
		return AiClassificationProposal{}, fmt.Errorf("ai: empty proposed code")
	}
	if err := provenance.validate(); err != nil {
		return AiClassificationProposal{}, err
	}
	return AiClassificationProposal{
		id:           id,
		subjectRef:   subjectRef,
		proposedCode: proposedCode,
		confidence:   confidence,
		provenance:   provenance,
	}, nil
}

// ID returns the proposal's identifier.
func (c AiClassificationProposal) ID() AiClassificationProposalID { return c.id }

// SubjectRef returns what this proposal is about, in the caller's own terms.
func (c AiClassificationProposal) SubjectRef() string { return c.subjectRef }

// ProposedCode returns the classification code the model proposed.
func (c AiClassificationProposal) ProposedCode() string { return c.proposedCode }

// Confidence returns the model's reported confidence as a canonical decimal
// string in [0,1], or empty if none was reported.
func (c AiClassificationProposal) Confidence() string { return c.confidence }

// Provenance returns the evidenced-transfer metadata for this proposal.
func (c AiClassificationProposal) Provenance() Provenance { return c.provenance }

// Reviewed reports whether a human has reviewed this proposal.
func (c AiClassificationProposal) Reviewed() bool { return c.reviewed }

// ReviewerID returns the identity of the reviewer, if reviewed.
func (c AiClassificationProposal) ReviewerID() string { return c.reviewerID }

// MarkReviewed records that a human reviewed this proposal. See
// AiSuggestion.MarkReviewed for why a rejection is recorded the same way as
// an acceptance.
func (c AiClassificationProposal) MarkReviewed(reviewerID string) (AiClassificationProposal, error) {
	if reviewerID == "" {
		return AiClassificationProposal{}, fmt.Errorf("ai: empty reviewer id")
	}
	c.reviewed = true
	c.reviewerID = reviewerID
	return c, nil
}
