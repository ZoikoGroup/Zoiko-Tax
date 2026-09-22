package ai

import "testing"

func TestNewAiExtraction_Valid(t *testing.T) {
	id, err := NewAiExtractionID("22222222-2222-2222-2222-222222222222")
	if err != nil {
		t.Fatalf("NewAiExtractionID: unexpected error: %v", err)
	}

	fields := map[string]string{
		"invoiceNumber": "INV-2026-0042",
		"totalAmount":   "150.00",
	}

	e, err := NewAiExtraction(id, "doc-456", fields, validProvenance())
	if err != nil {
		t.Fatalf("NewAiExtraction: unexpected error: %v", err)
	}

	if e.SourceRef() != "doc-456" {
		t.Errorf("SourceRef() = %q, want %q", e.SourceRef(), "doc-456")
	}
	if e.Reviewed() {
		t.Error("Reviewed() = true, want false for a freshly constructed extraction")
	}
}

func TestNewAiExtraction_EmptySourceRef(t *testing.T) {
	id, _ := NewAiExtractionID("22222222-2222-2222-2222-222222222222")

	_, err := NewAiExtraction(id, "", map[string]string{"a": "b"}, validProvenance())
	if err == nil {
		t.Fatal("NewAiExtraction: expected error for empty source reference, got nil")
	}
}

func TestNewAiExtraction_EmptyFieldsAllowed(t *testing.T) {
	// An extraction that found nothing is still a valid, recordable outcome —
	// only sourceRef and provenance are required.
	id, _ := NewAiExtractionID("22222222-2222-2222-2222-222222222222")

	e, err := NewAiExtraction(id, "doc-456", nil, validProvenance())
	if err != nil {
		t.Fatalf("NewAiExtraction: unexpected error with nil fields: %v", err)
	}
	if len(e.Fields()) != 0 {
		t.Errorf("Fields() length = %d, want 0", len(e.Fields()))
	}
}

func TestNewAiExtraction_InvalidProvenance(t *testing.T) {
	id, _ := NewAiExtractionID("22222222-2222-2222-2222-222222222222")

	bad := validProvenance()
	bad.AuthorityOutcome = "not-a-real-outcome"

	_, err := NewAiExtraction(id, "doc-456", nil, bad)
	if err == nil {
		t.Fatal("NewAiExtraction: expected error for invalid authority outcome, got nil")
	}
}

func TestAiExtraction_Field(t *testing.T) {
	id, _ := NewAiExtractionID("22222222-2222-2222-2222-222222222222")
	fields := map[string]string{"invoiceNumber": "INV-2026-0042"}

	e, err := NewAiExtraction(id, "doc-456", fields, validProvenance())
	if err != nil {
		t.Fatalf("NewAiExtraction: unexpected error: %v", err)
	}

	v, ok := e.Field("invoiceNumber")
	if !ok {
		t.Fatal("Field(\"invoiceNumber\") ok = false, want true")
	}
	if v != "INV-2026-0042" {
		t.Errorf("Field(\"invoiceNumber\") = %q, want %q", v, "INV-2026-0042")
	}

	_, ok = e.Field("doesNotExist")
	if ok {
		t.Error("Field(\"doesNotExist\") ok = true, want false")
	}
}

func TestAiExtraction_FieldsAreDefensivelyCopied(t *testing.T) {
	id, _ := NewAiExtractionID("22222222-2222-2222-2222-222222222222")
	input := map[string]string{"key": "original"}

	e, err := NewAiExtraction(id, "doc-456", input, validProvenance())
	if err != nil {
		t.Fatalf("NewAiExtraction: unexpected error: %v", err)
	}

	// Mutating the map passed into the constructor must not affect the
	// extraction — the constructor should have copied it.
	input["key"] = "mutated-after-construction"
	if v, _ := e.Field("key"); v != "original" {
		t.Errorf("Field(\"key\") = %q after external mutation, want %q (constructor must copy input)", v, "original")
	}

	// Mutating the map returned by Fields() must not affect the extraction
	// either — Fields() should return a copy, not the internal map.
	out := e.Fields()
	out["key"] = "mutated-via-getter"
	if v, _ := e.Field("key"); v != "original" {
		t.Errorf("Field(\"key\") = %q after mutating Fields() result, want %q (Fields() must return a copy)", v, "original")
	}
}

func TestAiExtraction_MarkReviewed(t *testing.T) {
	id, _ := NewAiExtractionID("22222222-2222-2222-2222-222222222222")
	e, err := NewAiExtraction(id, "doc-456", nil, validProvenance())
	if err != nil {
		t.Fatalf("NewAiExtraction: unexpected error: %v", err)
	}

	reviewed, err := e.MarkReviewed("reviewer-7")
	if err != nil {
		t.Fatalf("MarkReviewed: unexpected error: %v", err)
	}
	if !reviewed.Reviewed() {
		t.Error("Reviewed() = false after MarkReviewed, want true")
	}
	if reviewed.ReviewerID() != "reviewer-7" {
		t.Errorf("ReviewerID() = %q, want %q", reviewed.ReviewerID(), "reviewer-7")
	}
	if e.Reviewed() {
		t.Error("original AiExtraction was mutated by MarkReviewed, want it unchanged")
	}
}

func TestAiExtraction_MarkReviewed_EmptyReviewerID(t *testing.T) {
	id, _ := NewAiExtractionID("22222222-2222-2222-2222-222222222222")
	e, err := NewAiExtraction(id, "doc-456", nil, validProvenance())
	if err != nil {
		t.Fatalf("NewAiExtraction: unexpected error: %v", err)
	}

	_, err = e.MarkReviewed("")
	if err == nil {
		t.Fatal("MarkReviewed: expected error for empty reviewer id, got nil")
	}
}
