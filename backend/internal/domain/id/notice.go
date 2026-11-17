package id

import "github.com/google/uuid"

// NoticeID identifies one regulatory notice, assessment, audit, appeal or
// information request an authority sent (W17, G-052).
type NoticeID struct{ base }

// NewNoticeID wraps a raw UUID as a NoticeID.
func NewNoticeID(u uuid.UUID) NoticeID { return NoticeID{base{u}} }

// ParseNoticeID reads a NoticeID from its canonical string form.
func ParseNoticeID(s string) (NoticeID, error) {
	u, err := parse("notice id", s)
	if err != nil {
		return NoticeID{}, err
	}
	return NewNoticeID(u), nil
}
