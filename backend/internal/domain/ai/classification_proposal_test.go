package ai

import "testing"

func TestNewAiClassificationProposal_Valid(t *testing.T) {
	id, err := NewAiClassificationProposalID("33333333-3333-3333-3333-333333333333")
	if err != nil {
		t.Fatalf("NewAiClassificationProposalID: unexpected error: %v", err)
	}

	c, err := NewAiClassificationProposal(id, "doc-789", "DIGITAL_SERVICE", "0.87", validProvenance())
	if err != nil {
		t.Fatalf("NewAiClassificationProposal: unexpected error: %v", err)
	}

	if c.SubjectRef() != "doc-789" {
		t.Errorf("SubjectRef() = %q, want %q", c.SubjectRef(), "doc-789")
	}
	if c.ProposedCode() != "DIGITAL_SERVICE" {
		t.Errorf("ProposedCode() = %q, want %q", c.ProposedCode(), "DIGITAL_SERVICE")
	}
	if c.Confidence() != "0.87" {
		t.Errorf("Confidence() = %q, want %q", c.Confidence(), "0.87")
	}
	if c.Reviewed() {
		t.Error("Reviewed() = true, want false for a freshly constructed proposal")
	}
}

func TestNewAiClassificationProposal_EmptyConfidenceAllowed(t *testing.T) {
	// A model that does not report a confidence score is still a valid,
	// recordable proposal — only subjectRef and proposedCode are required.
	id, _ := NewAiClassificationProposalID("33333333-3333-3333-3333-333333333333")

	c, err := NewAiClassificationProposal(id, "doc-789", "DIGITAL_SERVICE", "", validProvenance())
	if err != nil {
		t.Fatalf("NewAiClassificationProposal: unexpected error with empty confidence: %v", err)
	}
	if c.Confidence() != "" {
		t.Errorf("Confidence() = %q, want empty string", c.Confidence())
	}
}

func TestNewAiClassificationProposal_EmptySubjectRef(t *testing.T) {
	id, _ := NewAiClassificationProposalID("33333333-3333-3333-3333-333333333333")

	_, err := NewAiClassificationProposal(id, "", "DIGITAL_SERVICE", "0.87", validProvenance())
	if err == nil {
		t.Fatal("NewAiClassificationProposal: expected error for empty subject reference, got nil")
	}
}

func TestNewAiClassificationProposal_EmptyProposedCode(t *testing.T) {
	id, _ := NewAiClassificationProposalID("33333333-3333-3333-3333-333333333333")

	_, err := NewAiClassificationProposal(id, "doc-789", "", "0.87", validProvenance())
	if err == nil {
		t.Fatal("NewAiClassificationProposal: expected error for empty proposed code, got nil")
	}
}

func TestNewAiClassificationProposal_InvalidProvenance(t *testing.T) {
	id, _ := NewAiClassificationProposalID("33333333-3333-3333-3333-333333333333")

	bad := validProvenance()
	bad.AiTrainVersion = ""

	_, err := NewAiClassificationProposal(id, "doc-789", "DIGITAL_SERVICE", "0.87", bad)
	if err == nil {
		t.Fatal("NewAiClassificationProposal: expected error for empty AI train version, got nil")
	}
}

func TestAiClassificationProposal_MarkReviewed(t *testing.T) {
	id, _ := NewAiClassificationProposalID("33333333-3333-3333-3333-333333333333")
	c, err := NewAiClassificationProposal(id, "doc-789", "DIGITAL_SERVICE", "0.87", validProvenance())
	if err != nil {
		t.Fatalf("NewAiClassificationProposal: unexpected error: %v", err)
	}

	reviewed, err := c.MarkReviewed("reviewer-9")
	if err != nil {
		t.Fatalf("MarkReviewed: unexpected error: %v", err)
	}
	if !reviewed.Reviewed() {
		t.Error("Reviewed() = false after MarkReviewed, want true")
	}
	if reviewed.ReviewerID() != "reviewer-9" {
		t.Errorf("ReviewerID() = %q, want %q", reviewed.ReviewerID(), "reviewer-9")
	}
	if c.Reviewed() {
		t.Error("original AiClassificationProposal was mutated by MarkReviewed, want it unchanged")
	}
}

func TestAiClassificationProposal_MarkReviewed_EmptyReviewerID(t *testing.T) {
	id, _ := NewAiClassificationProposalID("33333333-3333-3333-3333-333333333333")
	c, err := NewAiClassificationProposal(id, "doc-789", "DIGITAL_SERVICE", "0.87", validProvenance())
	if err != nil {
		t.Fatalf("NewAiClassificationProposal: unexpected error: %v", err)
	}

	_, err = c.MarkReviewed("")
	if err == nil {
		t.Fatal("MarkReviewed: expected error for empty reviewer id, got nil")
	}
}
