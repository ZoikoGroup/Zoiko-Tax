package port

import (
	"context"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/notice"
)

// NoticeRepository stores regulatory notices and their event history (W17).
// A notice is immutable; its state, its deadline ledger included, is its
// events.
type NoticeRepository interface {
	// Lock serializes writes to one notice for the rest of the transaction.
	Lock(ctx context.Context, noticeID id.NoticeID) error
	// Create records a notice and its first events, RECEIVED first.
	Create(ctx context.Context, n notice.Notice, events []notice.Event) error
	// AppendEvents records the next events of a notice, in order.
	AppendEvents(ctx context.Context, noticeID id.NoticeID, events []notice.Event) error
	// Notice returns one, folded from its events.
	Notice(ctx context.Context, noticeID id.NoticeID) (notice.Notice, error)
	// List returns the tenant's notices, most recently received first.
	List(ctx context.Context, limit int) ([]notice.Notice, error)
}
