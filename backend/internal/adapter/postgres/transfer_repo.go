package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// cross_cell_transfer — ADR-0009 §2.6, SEC-REQ-0042
// ---------------------------------------------------------------------------

const (
	sqlTransferColumns = `tenant_id, transfer_id, source_cell, destination_cell,
		profile_id, profile_version, mechanism, purpose, data_classes, content_digest,
		requested_by, approved_by, recorded_at`

	sqlTransferInsert = `
		INSERT INTO ztax.cross_cell_transfer (` + sqlTransferColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`

	sqlTransferByID = `SELECT ` + sqlTransferColumns + `
		FROM ztax.cross_cell_transfer WHERE tenant_id = $1 AND transfer_id = $2`

	sqlTransferList = `SELECT ` + sqlTransferColumns + `
		FROM ztax.cross_cell_transfer WHERE tenant_id = $1
		ORDER BY recorded_at DESC, transfer_id DESC LIMIT $2`
)

// TransferRepo implements port.TransferLog.
type TransferRepo struct{ s *Store }

// Transfers returns the cross-cell transfer log.
func (s *Store) Transfers() *TransferRepo { return &TransferRepo{s: s} }

var _ port.TransferLog = (*TransferRepo)(nil)

// Append records a transfer. The record is validated again here: the type can
// only be produced permitted by NewCrossCellTransfer, but its fields are
// exported for this boundary, and a literal assembled elsewhere must not reach
// the evidence half-formed. The table's CHECKs say the same things a third
// time, for any writer that is not this one.
func (r *TransferRepo) Append(ctx context.Context, t privacy.CrossCellTransfer) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if t.TenantID != tenant {
		return errs.New(errs.CategoryPolicy, errs.ReasonTenantMismatch,
			"The transfer belongs to a tenant outside the caller's scope.")
	}
	if err := t.Validate(); err != nil {
		return errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
			"The cross-cell transfer record is incomplete.")
	}
	classes := make([]string, len(t.DataClasses))
	for i, c := range t.DataClasses {
		classes[i] = string(c)
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlTransferInsert,
		tenant.UUID(), t.ID.UUID(), t.SourceCell, t.DestinationCell,
		t.ProfileID, t.ProfileVersion, string(t.Mechanism), string(t.Purpose), classes, t.ContentDigest.String(),
		t.RequestedBy.UUID(), t.ApprovedBy.UUID(), t.RecordedAt)
	return mapError(err, "insert cross-cell transfer")
}

// ByID reads one transfer.
func (r *TransferRepo) ByID(ctx context.Context, transferID id.TransferID) (privacy.CrossCellTransfer, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return privacy.CrossCellTransfer{}, err
	}
	return scanTransfer(r.s.db(ctx).QueryRow(ctx, sqlTransferByID, tenant.UUID(), transferID.UUID()))
}

// List returns the tenant's transfers, newest first.
func (r *TransferRepo) List(ctx context.Context, limit int) ([]privacy.CrossCellTransfer, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlTransferList, tenant.UUID(), capLimit(limit))
	if err != nil {
		return nil, mapError(err, "list cross-cell transfers")
	}
	defer rows.Close()
	var out []privacy.CrossCellTransfer
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, mapError(rows.Err(), "list cross-cell transfers")
}

func scanTransfer(row scanner) (privacy.CrossCellTransfer, error) {
	var (
		tenantUUID, transferUUID, requested, approved uuid.UUID
		mechanism, purpose, digest                    string
		classes                                       []string
		recordedAt                                    time.Time
		t                                             privacy.CrossCellTransfer
	)
	if err := row.Scan(&tenantUUID, &transferUUID, &t.SourceCell, &t.DestinationCell,
		&t.ProfileID, &t.ProfileVersion, &mechanism, &purpose, &classes, &digest,
		&requested, &approved, &recordedAt); err != nil {
		return privacy.CrossCellTransfer{}, mapError(err, "scan cross-cell transfer")
	}
	t.TenantID = id.NewTenantID(tenantUUID)
	t.ID = id.NewTransferID(transferUUID)
	t.RequestedBy = id.NewUserID(requested)
	t.ApprovedBy = id.NewUserID(approved)
	t.Mechanism = privacy.TransferMechanism(mechanism)
	t.Purpose = privacy.Purpose(purpose)
	t.RecordedAt = recordedAt.UTC()
	t.DataClasses = make([]privacy.Class, len(classes))
	for i, c := range classes {
		t.DataClasses[i] = privacy.Class(c)
	}
	d, err := parseStoredDigest(digest, "content_digest")
	if err != nil {
		return privacy.CrossCellTransfer{}, err
	}
	t.ContentDigest = d
	// A stored record that no longer validates is evidence that changed, or
	// was written by something that bypassed this adapter. Either way it is
	// reported, not returned.
	if err := t.Validate(); err != nil {
		return privacy.CrossCellTransfer{}, errs.Wrap(errors.Join(errors.New("postgres: stored cross-cell transfer"), err),
			errs.CategoryInternal, errs.ReasonEvidenceIntegrity,
			"A stored cross-cell transfer record is not well formed.")
	}
	return t, nil
}
