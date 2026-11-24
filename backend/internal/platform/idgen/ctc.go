package idgen

import "github.com/zoikogroup/zoikotax/backend/internal/domain/id"

// CTCApplicabilityID returns a fresh CTCApplicabilityID.
func CTCApplicabilityID(g Generator) (id.CTCApplicabilityID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.CTCApplicabilityID{}, err
	}
	return id.NewCTCApplicabilityID(u), nil
}

// CTCValidationID returns a fresh CTCValidationID.
func CTCValidationID(g Generator) (id.CTCValidationID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.CTCValidationID{}, err
	}
	return id.NewCTCValidationID(u), nil
}

// CTCSubmissionID returns a fresh CTCSubmissionID.
func CTCSubmissionID(g Generator) (id.CTCSubmissionID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.CTCSubmissionID{}, err
	}
	return id.NewCTCSubmissionID(u), nil
}

// CTCAttemptID returns a fresh CTCAttemptID.
func CTCAttemptID(g Generator) (id.CTCAttemptID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.CTCAttemptID{}, err
	}
	return id.NewCTCAttemptID(u), nil
}
