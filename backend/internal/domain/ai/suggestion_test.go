package ai

import (
	"testing"
	"time"
)

func validProvenance() Provenance {
	return Provenance{
		UseCase:          "classification-review",
		ModelProfile:     "gpt-x",
		ProviderProfile:  "provider-a",
		PromptProfile:    "prompt-v1",
		Region:           "eu-west-1",
		DataClass:        "internal",
		RiskTier:         RiskTierT1,
		AuthorityOutcome: AuthorityOutcomeA1,
		AiTrainVersion:   "0.0.0-dev",
	}
}

func TestNewAiSuggestion_Valid(t *testing.T) {
	id, err := NewAiSuggestionID("11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("NewAiSuggestionID: unexpected error: %v", err)
	}

	s, err := NewAiSuggestion(
		id,
		"doc-123",
		"Consider classifying this as a digital service.",
		"",
		validProvenance(),
		time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("NewAiSuggestion: unexpected error: %v", err)
	}

	if s.SubjectRef() != "doc-123" {
		t.Errorf("SubjectRef() = %q, want %q", s.SubjectRef(), "doc-123")
	}
	if s.Reviewed() {
		t.Errorf("Reviewed() = true, want false for a freshly constructed suggestion")
	}
}

func TestNewAiSuggestion_EmptySubjectRef(t *testing.T) {
	id, _ := NewAiSuggestionID("11111111-1111-1111-1111-111111111111")

	_, err := NewAiSuggestion(
		id,
		"", // empty subject ref — must be rejected
		"some text",
		"",
		validProvenance(),
		time.Now(),
	)
	if err == nil {
		t.Fatal("NewAiSuggestion: expected error for empty subject reference, got nil")
	}
}

func TestNewAiSuggestion_EmptyText(t *testing.T) {
	id, _ := NewAiSuggestionID("11111111-1111-1111-1111-111111111111")

	_, err := NewAiSuggestion(
		id,
		"doc-123",
		"", // empty text — must be rejected
		"",
		validProvenance(),
		time.Now(),
	)
	if err == nil {
		t.Fatal("NewAiSuggestion: expected error for empty text, got nil")
	}
}

func TestNewAiSuggestion_ZeroReceivedAt(t *testing.T) {
	id, _ := NewAiSuggestionID("11111111-1111-1111-1111-111111111111")

	_, err := NewAiSuggestion(
		id,
		"doc-123",
		"some text",
		"",
		validProvenance(),
		time.Time{}, // zero value — must be rejected
	)
	if err == nil {
		t.Fatal("NewAiSuggestion: expected error for zero receivedAt, got nil")
	}
}

func TestNewAiSuggestion_InvalidProvenance(t *testing.T) {
	id, _ := NewAiSuggestionID("11111111-1111-1111-1111-111111111111")

	bad := validProvenance()
	bad.RiskTier = "not-a-real-tier"

	_, err := NewAiSuggestion(id, "doc-123", "some text", "", bad, time.Now())
	if err == nil {
		t.Fatal("NewAiSuggestion: expected error for invalid risk tier, got nil")
	}
}

func TestAiSuggestion_MarkReviewed(t *testing.T) {
	id, _ := NewAiSuggestionID("11111111-1111-1111-1111-111111111111")
	s, err := NewAiSuggestion(id, "doc-123", "some text", "", validProvenance(), time.Now())
	if err != nil {
		t.Fatalf("NewAiSuggestion: unexpected error: %v", err)
	}

	reviewed, err := s.MarkReviewed("reviewer-42")
	if err != nil {
		t.Fatalf("MarkReviewed: unexpected error: %v", err)
	}
	if !reviewed.Reviewed() {
		t.Error("Reviewed() = false after MarkReviewed, want true")
	}
	if reviewed.ReviewerID() != "reviewer-42" {
		t.Errorf("ReviewerID() = %q, want %q", reviewed.ReviewerID(), "reviewer-42")
	}

	// The original value must be unaffected — MarkReviewed returns a new
	// AiSuggestion rather than mutating in place.
	if s.Reviewed() {
		t.Error("original AiSuggestion was mutated by MarkReviewed, want it unchanged")
	}
}

func TestAiSuggestion_MarkReviewed_EmptyReviewerID(t *testing.T) {
	id, _ := NewAiSuggestionID("11111111-1111-1111-1111-111111111111")
	s, err := NewAiSuggestion(id, "doc-123", "some text", "", validProvenance(), time.Now())
	if err != nil {
		t.Fatalf("NewAiSuggestion: unexpected error: %v", err)
	}

	_, err = s.MarkReviewed("")
	if err == nil {
		t.Fatal("MarkReviewed: expected error for empty reviewer id, got nil")
	}
}
