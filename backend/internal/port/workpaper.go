package port

import (
	"context"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/contribution"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/workpaper"
)

// WorkpaperRepository stores regulatory workpapers, their builds and their
// event history (OBL-07). It is append-only: a header, a build and an event
// are never updated or deleted, and a workpaper's state is the fold of its
// events.
type WorkpaperRepository interface {
	// Lock serializes the tenant's writes to one key for the rest of the
	// transaction: a workpaper, or the filer, duty and period one is
	// created for.
	Lock(ctx context.Context, key string) error
	// Create writes a header and its CREATED event. A second workpaper for
	// one filer, duty and period is refused as already existing.
	Create(ctx context.Context, w workpaper.Workpaper, created workpaper.Event) error
	// ByKey returns the workpaper of a filer, duty and period, and false
	// when there is none.
	ByKey(ctx context.Context, legalEntity id.LegalEntityID, duty contribution.Regime, period string) (workpaper.Workpaper, bool, error)
	// Workpaper returns one workpaper, folded, with every build's summary
	// and the latest build's output checked against its hash.
	Workpaper(ctx context.Context, workpaperID id.WorkpaperID) (workpaper.Workpaper, error)
	// List returns the tenant's workpapers matching the filter, newest
	// period first, with their builds' summaries but no output.
	List(ctx context.Context, f workpaper.Filter) ([]workpaper.Workpaper, error)
	// AppendBuild writes a build and its output. A second build of one
	// version is refused as already existing.
	AppendBuild(ctx context.Context, workpaperID id.WorkpaperID, b workpaper.Build) error
	// AppendEvents writes the next events, in order.
	AppendEvents(ctx context.Context, workpaperID id.WorkpaperID, events []workpaper.Event) error
}

// WorkpaperContributionSource is what a workpaper build reads of the
// provider contribution ledger. ProviderContributionRepository provides it.
type WorkpaperContributionSource interface {
	InPeriod(ctx context.Context, period string) ([]contribution.Contribution, error)
	KnownSources(ctx context.Context, keys []contribution.SourceKey) (map[contribution.SourceKey]bool, error)
}

// WorkpaperSubledgerSource is what a workpaper build reads of the Tax
// Control Subledger: the filer's balances posted into the period.
// PeriodRepository provides it.
type WorkpaperSubledgerSource interface {
	Population(ctx context.Context, legalEntity id.LegalEntityID, period string) (PeriodPopulation, error)
}
