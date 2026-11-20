package port

import (
	"context"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/erp"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/subledger"
)

// ERPRepository stores ERP journal bridges, their mapping versions, export
// batches with their delivery events, the GL balances ERPs reported and the
// GL reconciliations run over them (LED-23). It is append-only: no row is
// updated or deleted, a batch's status is the fold of its events, and a
// bridge's current mapping is its newest version.
type ERPRepository interface {
	// Lock serializes the tenant's writes to one key for the rest of the
	// transaction: a bridge, or a batch.
	Lock(ctx context.Context, key string) error

	// CreateBridge writes a bridge with no mapping yet.
	CreateBridge(ctx context.Context, b erp.Bridge) error
	// AppendMapping writes a bridge's next mapping version. A version
	// already taken is refused as already existing.
	AppendMapping(ctx context.Context, bridge id.ERPBridgeID, m erp.Mapping) error
	// Bridge returns one bridge with its mapping versions, oldest first.
	Bridge(ctx context.Context, bridge id.ERPBridgeID) (erp.Bridge, error)
	// Bridges returns the tenant's bridges, oldest first.
	Bridges(ctx context.Context) ([]erp.Bridge, error)

	// CreateBatch writes a sealed batch, its lines and its PREPARED event.
	CreateBatch(ctx context.Context, b erp.Batch, prepared erp.Event) error
	// AppendBatchEvent writes a batch's next event. A sequence already
	// taken is an optimistic conflict.
	AppendBatchEvent(ctx context.Context, batch id.ERPExportID, e erp.Event) error
	// Batch returns one batch, folded, with its lines.
	Batch(ctx context.Context, batch id.ERPExportID) (erp.Batch, error)
	// Batches returns a bridge's batches matching the filter, folded, with
	// their lines, newest first.
	Batches(ctx context.Context, bridge id.ERPBridgeID, f erp.Filter) ([]erp.Batch, error)

	// PeriodJournals returns the posted TCSL journals of a legal entity,
	// legal period and currency, with their lines.
	PeriodJournals(ctx context.Context, legalEntity id.LegalEntityID, period string, currency fiscal.Currency) ([]subledger.Journal, error)

	// RecordSnapshot writes a GL balance snapshot and its lines.
	RecordSnapshot(ctx context.Context, s erp.GLSnapshot) error
	// Snapshot returns one snapshot.
	Snapshot(ctx context.Context, snapshot id.GLSnapshotID) (erp.GLSnapshot, error)
	// Snapshots returns a bridge's snapshots, newest first; an empty period
	// does not filter.
	Snapshots(ctx context.Context, bridge id.ERPBridgeID, period string) ([]erp.GLSnapshot, error)

	// RecordReconciliation writes a GL reconciliation and its items.
	RecordReconciliation(ctx context.Context, r erp.GLReconciliation) error
	// Reconciliations returns a bridge's GL reconciliations, newest first;
	// an empty period does not filter.
	Reconciliations(ctx context.Context, bridge id.ERPBridgeID, period string) ([]erp.GLReconciliation, error)
}
