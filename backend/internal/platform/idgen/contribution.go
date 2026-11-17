package idgen

import "github.com/zoikogroup/zoikotax/backend/internal/domain/id"

// ContributionID returns a fresh ContributionID.
func ContributionID(g Generator) (id.ContributionID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.ContributionID{}, err
	}
	return id.NewContributionID(u), nil
}
