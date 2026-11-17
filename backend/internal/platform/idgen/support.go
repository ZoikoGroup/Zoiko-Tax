package idgen

import "github.com/zoikogroup/zoikotax/backend/internal/domain/id"

// SupportCaseID returns a fresh SupportCaseID.
func SupportCaseID(g Generator) (id.SupportCaseID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.SupportCaseID{}, err
	}
	return id.NewSupportCaseID(u), nil
}
