package idgen

import "github.com/zoikogroup/zoikotax/backend/internal/domain/id"

// WorkpaperID returns a fresh WorkpaperID.
func WorkpaperID(g Generator) (id.WorkpaperID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.WorkpaperID{}, err
	}
	return id.NewWorkpaperID(u), nil
}
