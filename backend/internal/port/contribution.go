package port

import (
	"context"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/contribution"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// ProviderContributionRepository stores provider contribution events and
// their status events (OBL-06). It is append-only: a contribution row is
// never updated or deleted, and its status is the fold of its events.
type ProviderContributionRepository interface {
	// Lock serializes the tenant's writes to one key for the rest of the
	// transaction: a source event, or the original a reversal names.
	Lock(ctx context.Context, key string) error

	// Record writes a contribution and its RECORDED event. A second record
	// of the same source key is refused as already existing.
	Record(ctx context.Context, c contribution.Contribution, recorded contribution.StatusEvent) error
	// AppendEvent writes the next status event of a contribution.
	AppendEvent(ctx context.Context, contributionID id.ContributionID, e contribution.StatusEvent) error

	// Contribution returns one contribution, folded.
	Contribution(ctx context.Context, contributionID id.ContributionID) (contribution.Contribution, error)
	// BySource returns the contribution recorded for a source key, and false
	// when there is none.
	BySource(ctx context.Context, key contribution.SourceKey) (contribution.Contribution, bool, error)
	// ReversalOf returns the contribution that reverses a source key, and
	// false when there is none.
	ReversalOf(ctx context.Context, key contribution.SourceKey) (contribution.Contribution, bool, error)
	// List returns the tenant's contributions matching the filter, newest
	// first.
	List(ctx context.Context, f contribution.Filter) ([]contribution.Contribution, error)
	// InPeriod returns every contribution of a period, in any status.
	InPeriod(ctx context.Context, period string) ([]contribution.Contribution, error)
	// KnownSources reports which of the keys the ledger holds, in any period.
	KnownSources(ctx context.Context, keys []contribution.SourceKey) (map[contribution.SourceKey]bool, error)
}
