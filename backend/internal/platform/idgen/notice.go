package idgen

import "github.com/zoikogroup/zoikotax/backend/internal/domain/id"

// NoticeID returns a fresh NoticeID.
func NoticeID(g Generator) (id.NoticeID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.NoticeID{}, err
	}
	return id.NewNoticeID(u), nil
}
