package ai

import "fmt"

// AiExtraction is structured data the AI plane extracted from a source
// document (e.g. a filed return, an invoice, a customer-supplied file).
// It is advisory, per ADR-0006 §2.6 — nothing in this package turns an
// AiExtraction into a fiscal record.
type AiExtraction struct {
	id AiExtractionID

	// SourceRef names the document this was extracted from, as an opaque
	// caller-supplied reference — not a fiscal or evidence type.
	sourceRef string

	// Fields holds the extracted values as canonical strings (ADR-0011).
	// Values are never float64: an extracted amount is exactly as
	// untrustworthy as any other AI output until a human confirms it, and
	// carrying it as anything but a string invites silent precision loss
	// before it has even been reviewed.
	fields map[string]string

	provenance Provenance

	reviewed   bool
	reviewerID string
}

// NewAiExtraction constructs an AiExtraction. fields may be empty (an
// extraction that found nothing is still a recorded, auditable outcome).
func NewAiExtraction(
	id AiExtractionID,
	sourceRef string,
	fields map[string]string,
	provenance Provenance,
) (AiExtraction, error) {
	if sourceRef == "" {
		return AiExtraction{}, fmt.Errorf("ai: empty source reference")
	}
	if err := provenance.validate(); err != nil {
		return AiExtraction{}, err
	}
	// Defensive copy: a caller must not be able to mutate an AiExtraction's
	// fields after construction by holding a reference to the map they
	// passed in.
	copied := make(map[string]string, len(fields))
	for k, v := range fields {
		copied[k] = v
	}
	return AiExtraction{
		id:         id,
		sourceRef:  sourceRef,
		fields:     copied,
		provenance: provenance,
	}, nil
}

// ID returns the extraction's identifier.
func (e AiExtraction) ID() AiExtractionID { return e.id }

// SourceRef returns the document this was extracted from.
func (e AiExtraction) SourceRef() string { return e.sourceRef }

// Provenance returns the evidenced-transfer metadata for this extraction.
func (e AiExtraction) Provenance() Provenance { return e.provenance }

// Reviewed reports whether a human has reviewed this extraction.
func (e AiExtraction) Reviewed() bool { return e.reviewed }

// ReviewerID returns the identity of the reviewer, if reviewed.
func (e AiExtraction) ReviewerID() string { return e.reviewerID }

// Field returns one extracted value by name and whether it was present.
func (e AiExtraction) Field(name string) (string, bool) {
	v, ok := e.fields[name]
	return v, ok
}

// Fields returns a copy of every extracted field. Copied on the way out for
// the same reason it is copied on the way in.
func (e AiExtraction) Fields() map[string]string {
	out := make(map[string]string, len(e.fields))
	for k, v := range e.fields {
		out[k] = v
	}
	return out
}

// MarkReviewed records that a human reviewed this extraction. See
// AiSuggestion.MarkReviewed for why a rejection is recorded the same way as
// an acceptance.
func (e AiExtraction) MarkReviewed(reviewerID string) (AiExtraction, error) {
	if reviewerID == "" {
		return AiExtraction{}, fmt.Errorf("ai: empty reviewer id")
	}
	e.reviewed = true
	e.reviewerID = reviewerID
	return e, nil
}
