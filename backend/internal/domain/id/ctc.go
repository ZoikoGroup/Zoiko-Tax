package id

import "github.com/google/uuid"

// The e-invoicing / CTC layer's identifiers (Engineering Doc 21, G-030).
// They live in their own file so the layer adds its types without touching
// the shared list in id.go.

// CTCApplicabilityID identifies one country applicability record: whether a
// legal entity's documents of one type, to one kind of buyer, owe an
// electronic document duty in one country, and under which model and format.
type CTCApplicabilityID struct{ base }

// NewCTCApplicabilityID wraps a raw UUID as a CTCApplicabilityID.
func NewCTCApplicabilityID(u uuid.UUID) CTCApplicabilityID { return CTCApplicabilityID{base{u}} }

// ParseCTCApplicabilityID reads a CTCApplicabilityID from its canonical
// string form.
func ParseCTCApplicabilityID(s string) (CTCApplicabilityID, error) {
	u, err := parse("CTC applicability id", s)
	if err != nil {
		return CTCApplicabilityID{}, err
	}
	return NewCTCApplicabilityID(u), nil
}

// CTCValidationID identifies one immutable format validation report.
type CTCValidationID struct{ base }

// NewCTCValidationID wraps a raw UUID as a CTCValidationID.
func NewCTCValidationID(u uuid.UUID) CTCValidationID { return CTCValidationID{base{u}} }

// ParseCTCValidationID reads a CTCValidationID from its canonical string form.
func ParseCTCValidationID(s string) (CTCValidationID, error) {
	u, err := parse("CTC validation id", s)
	if err != nil {
		return CTCValidationID{}, err
	}
	return NewCTCValidationID(u), nil
}

// CTCSubmissionID identifies one authority submission of a fiscal document
// under one applicability record. It is also the reference every dispatch
// attempt carries, so a retry is never a distinct submission.
type CTCSubmissionID struct{ base }

// NewCTCSubmissionID wraps a raw UUID as a CTCSubmissionID.
func NewCTCSubmissionID(u uuid.UUID) CTCSubmissionID { return CTCSubmissionID{base{u}} }

// ParseCTCSubmissionID reads a CTCSubmissionID from its canonical string form.
func ParseCTCSubmissionID(s string) (CTCSubmissionID, error) {
	u, err := parse("CTC submission id", s)
	if err != nil {
		return CTCSubmissionID{}, err
	}
	return NewCTCSubmissionID(u), nil
}

// CTCAttemptID identifies one dispatch attempt of a submission.
type CTCAttemptID struct{ base }

// NewCTCAttemptID wraps a raw UUID as a CTCAttemptID.
func NewCTCAttemptID(u uuid.UUID) CTCAttemptID { return CTCAttemptID{base{u}} }

// ParseCTCAttemptID reads a CTCAttemptID from its canonical string form.
func ParseCTCAttemptID(s string) (CTCAttemptID, error) {
	u, err := parse("CTC dispatch attempt id", s)
	if err != nil {
		return CTCAttemptID{}, err
	}
	return NewCTCAttemptID(u), nil
}
