package id

import "github.com/google/uuid"

// SupportCaseID identifies one customer support case (Engineering Doc 18,
// W16). It lives in its own file so the support surface stays self-contained.
type SupportCaseID struct{ base }

// NewSupportCaseID wraps a raw UUID as a SupportCaseID.
func NewSupportCaseID(u uuid.UUID) SupportCaseID { return SupportCaseID{base{u}} }

// ParseSupportCaseID reads a SupportCaseID from its canonical string form.
func ParseSupportCaseID(s string) (SupportCaseID, error) {
	u, err := parse("support case id", s)
	if err != nil {
		return SupportCaseID{}, err
	}
	return NewSupportCaseID(u), nil
}
