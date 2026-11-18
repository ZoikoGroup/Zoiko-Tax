package id

import "github.com/google/uuid"

// WorkpaperID identifies one regulatory workpaper: the assembled schedules
// and reconciliation for one legal filer, duty and period (OBL-07). It is
// kept in its own file so the workpaper service adds a type without touching
// the shared list in id.go.
type WorkpaperID struct{ base }

// NewWorkpaperID wraps a raw UUID as a WorkpaperID.
func NewWorkpaperID(u uuid.UUID) WorkpaperID { return WorkpaperID{base{u}} }

// ParseWorkpaperID reads a WorkpaperID from its canonical string form.
func ParseWorkpaperID(s string) (WorkpaperID, error) {
	u, err := parse("workpaper id", s)
	if err != nil {
		return WorkpaperID{}, err
	}
	return NewWorkpaperID(u), nil
}
