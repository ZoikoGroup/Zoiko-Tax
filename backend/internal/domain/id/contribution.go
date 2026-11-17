package id

import "github.com/google/uuid"

// ContributionID identifies one provider contribution event: an operator's
// contribution base taken from one source event (OBL-06). It is kept in its
// own file so the contribution ledger adds a type without touching the
// shared list in id.go.
type ContributionID struct{ base }

// NewContributionID wraps a raw UUID as a ContributionID.
func NewContributionID(u uuid.UUID) ContributionID { return ContributionID{base{u}} }

// ParseContributionID reads a ContributionID from its canonical string form.
func ParseContributionID(s string) (ContributionID, error) {
	u, err := parse("contribution id", s)
	if err != nil {
		return ContributionID{}, err
	}
	return NewContributionID(u), nil
}
