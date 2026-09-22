package ai

import (
	"errors"
	"fmt"
	"time"
)

// AiSuggestion is an advisory output from the Model Gateway: something the
// AI plane proposes, never something it decides. ADR-0006 §2.6.
//
// This type has no method or function anywhere that produces a fiscal type.
// The only legal path from an AiSuggestion to an authoritative record is a
// human reviewer reading this suggestion and constructing the fiscal record
// themselves, recording their own identity as the author of that decision.
//
// rather than shortened to Suggestion, per project-owner decision (2026-09-22).
//
//nolint:revive // ADR-0006 §2.6 names this type explicitly. Kept verbatim
type AiSuggestion struct {
	id AiSuggestionID

	// SubjectRef names what this suggestion is about, in the caller's own
	// terms (e.g. a fiscal document id as an opaque string, never a
	// fiscal.FiscalDocumentID — that type must never appear in this
	// package's import graph).
	subjectRef string

	// Text is the human-readable suggestion. It is content produced by a
	// model, not content this package interprets, so it stays a string.
	text string

	// Payload is the canonical-form structured output backing Text, if any
	// (ADR-0011 canonical form). Optional — a suggestion may be text-only.
	payload string

	provenance Provenance

	// ReceivedAt is decision time for this suggestion, taken from the
	// request/callback envelope. It is never set from time.Now() inside
	// this package (ADR-0003 §2.5 applies the same discipline here).
	receivedAt time.Time

	// Reviewed and ReviewerID record whether, and by whom, this suggestion
	// was looked at — including a rejection, which is itself auditable
	// (ADR-0006 §2.7).
	reviewed   bool
	reviewerID string
}

// ErrSuggestionNotReviewed is returned by operations that require a
// suggestion to have been reviewed before proceeding.
var ErrSuggestionNotReviewed = errors.New("ai: suggestion not reviewed")

// NewAiSuggestion constructs an AiSuggestion. subjectRef and text are
// required; payload is optional (empty string if not applicable).
func NewAiSuggestion(
	id AiSuggestionID,
	subjectRef string,
	text string,
	payload string,
	provenance Provenance,
	receivedAt time.Time,
) (AiSuggestion, error) {
	if subjectRef == "" {
		return AiSuggestion{}, fmt.Errorf("ai: empty subject reference")
	}
	if text == "" {
		return AiSuggestion{}, fmt.Errorf("ai: empty suggestion text")
	}
	if err := provenance.validate(); err != nil {
		return AiSuggestion{}, err
	}
	if receivedAt.IsZero() {
		return AiSuggestion{}, fmt.Errorf("ai: zero receivedAt")
	}
	return AiSuggestion{
		id:         id,
		subjectRef: subjectRef,
		text:       text,
		payload:    payload,
		provenance: provenance,
		receivedAt: receivedAt,
	}, nil
}

// ID returns the suggestion's identifier.
func (s AiSuggestion) ID() AiSuggestionID { return s.id }

// SubjectRef returns what this suggestion is about, in the caller's own terms.
func (s AiSuggestion) SubjectRef() string { return s.subjectRef }

// Text returns the human-readable suggestion.
func (s AiSuggestion) Text() string { return s.text }

// Payload returns the canonical-form structured output backing Text, if any.
func (s AiSuggestion) Payload() string { return s.payload }

// Provenance returns the evidenced-transfer metadata for this suggestion.
func (s AiSuggestion) Provenance() Provenance { return s.provenance }

// ReceivedAt returns decision time for this suggestion.
func (s AiSuggestion) ReceivedAt() time.Time { return s.receivedAt }

// Reviewed reports whether a human has reviewed this suggestion.
func (s AiSuggestion) Reviewed() bool { return s.reviewed }

// ReviewerID returns the identity of the reviewer, if reviewed.
func (s AiSuggestion) ReviewerID() string { return s.reviewerID }

// MarkReviewed records that a human reviewed this suggestion, whether or not
// they acted on it. A rejected suggestion is still marked reviewed — "we
// considered and rejected" is itself auditable (ADR-0006 §2.7). Returns a
// new AiSuggestion; this type has no in-place mutation.
func (s AiSuggestion) MarkReviewed(reviewerID string) (AiSuggestion, error) {
	if reviewerID == "" {
		return AiSuggestion{}, fmt.Errorf("ai: empty reviewer id")
	}
	s.reviewed = true
	s.reviewerID = reviewerID
	return s, nil
}
