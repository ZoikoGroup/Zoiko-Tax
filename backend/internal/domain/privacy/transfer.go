package privacy

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// CrossCellTransfer is the evidence of one copy of a tenant's data from one
// cell to another.
//
// ADR-0009 §2.6: "Cross-cell communication is an explicit evidenced transfer,
// never an ordinary call." SEC-001 §11 says what makes it explicit — "purpose,
// destination, allowed data classes and evidence" — and SEC-REQ-0042 that it
// is authorized and logged. This record is all four: it names the profile that
// authorized it, the purpose and classes it moved, where it went, and the
// digest of exactly what left, so an auditor can ask "what of this tenant's
// data is in that cell, and on whose approval" and get an answer from records
// rather than from reconstruction.
//
// It is recorded in the source cell, append-only, before the data moves: the
// exporter is the accountable party, and a copy whose record failed to commit
// is a copy that must not happen. The destination recording its own receipt is
// the destination's business and a later piece of work.
//
// A CrossCellTransfer can only be built by NewCrossCellTransfer, which refuses
// one its TransferProfile does not permit. The fields are exported for the
// persistence boundary, which reads them back; Validate re-checks the shape on
// the way in so that a record assembled any other way still cannot be written
// half-formed.
type CrossCellTransfer struct {
	ID              id.TransferID
	TenantID        id.TenantID
	SourceCell      string
	DestinationCell string
	// The approval. Profile id and version rather than a copy of the profile:
	// profiles are themselves versioned records, and the version is what makes
	// "the profile as it stood when this was approved" retrievable.
	ProfileID      string
	ProfileVersion int
	Mechanism      TransferMechanism
	Purpose        Purpose
	// DataClasses are the classes in what moved, sorted and unique.
	DataClasses []Class
	// ContentDigest is the ADR-0011 digest of the canonical manifest of what
	// moved — not of the data's bytes in transit, which differ by encoding,
	// but of the statement of which records left.
	ContentDigest canonical.Digest
	// RequestedBy asked for the transfer and ApprovedBy authorized it. They
	// may not be the same person (SEC-REQ-0013): a transfer one person can
	// both request and approve is a transfer one person can make.
	RequestedBy id.UserID
	ApprovedBy  id.UserID
	RecordedAt  time.Time
}

// TransferRequest is what a caller asks to move.
type TransferRequest struct {
	ID              id.TransferID
	TenantID        id.TenantID
	SourceCell      string
	DestinationCell string
	Purpose         Purpose
	DataClasses     []Class
	ContentDigest   canonical.Digest
	RequestedBy     id.UserID
	ApprovedBy      id.UserID
	// At is when the transfer is recorded, supplied by the caller's clock:
	// the domain does not read one (ADR-0007 §2.5).
	At time.Time
}

// ErrInvalidTransfer is a transfer record that is not well formed, as distinct
// from one that is well formed and not permitted.
var ErrInvalidTransfer = errors.New("privacy: invalid cross-cell transfer")

// NewCrossCellTransfer builds the record for a transfer, or refuses it.
//
// The profile is checked first and completely: if it does not permit every
// class, the destination, the purpose and the date, the result wraps
// ErrTransferNotPermitted and there is no record to write. The request is then
// checked for shape, which wraps ErrInvalidTransfer.
func NewCrossCellTransfer(req TransferRequest, profile TransferProfile) (CrossCellTransfer, error) {
	classes := slices.Clone(req.DataClasses)
	slices.Sort(classes)
	classes = slices.Compact(classes)

	if err := profile.Permits(req.SourceCell, req.DestinationCell, req.Purpose, classes, req.At); err != nil {
		return CrossCellTransfer{}, err
	}
	t := CrossCellTransfer{
		ID:              req.ID,
		TenantID:        req.TenantID,
		SourceCell:      req.SourceCell,
		DestinationCell: req.DestinationCell,
		ProfileID:       profile.ID,
		ProfileVersion:  profile.Version,
		Mechanism:       profile.Mechanism,
		Purpose:         req.Purpose,
		DataClasses:     classes,
		ContentDigest:   req.ContentDigest,
		RequestedBy:     req.RequestedBy,
		ApprovedBy:      req.ApprovedBy,
		RecordedAt:      req.At.UTC(),
	}
	if err := t.Validate(); err != nil {
		return CrossCellTransfer{}, err
	}
	return t, nil
}

// Validate checks the record's shape: every field present, the cells real and
// different, the classes known and never P7, requester and approver distinct.
// It does not and cannot re-check the profile, which the record names but does
// not carry; NewCrossCellTransfer is where that happens.
func (t CrossCellTransfer) Validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalidTransfer}, args...)...)
	}
	switch {
	case t.ID.IsZero():
		return bad("no transfer id")
	case t.TenantID.IsZero():
		return bad("no tenant")
	case t.ProfileID == "" || t.ProfileVersion <= 0:
		return bad("no authorizing transfer profile")
	case !slices.Contains(TransferMechanisms, t.Mechanism):
		return bad("mechanism %q is not a transfer mechanism", t.Mechanism)
	case !slices.Contains(Purposes, t.Purpose):
		return bad("purpose %q is not an approved purpose", t.Purpose)
	case len(t.DataClasses) == 0:
		return bad("no data classes")
	case t.ContentDigest.IsZero():
		return bad("no digest of what moved")
	case t.RequestedBy.IsZero() || t.ApprovedBy.IsZero():
		return bad("no requester or no approver")
	case t.RequestedBy == t.ApprovedBy:
		return bad("the requester approved their own transfer")
	case t.RecordedAt.IsZero():
		return bad("no recording time")
	}
	if err := security.ValidateCellID(t.SourceCell); err != nil {
		return bad("source cell: %v", err)
	}
	if err := security.ValidateCellID(t.DestinationCell); err != nil {
		return bad("destination cell: %v", err)
	}
	if t.SourceCell == t.DestinationCell {
		return bad("source and destination are the same cell")
	}
	for _, c := range t.DataClasses {
		if !c.Valid() {
			return bad("%q is not a PRIV-001 class", c)
		}
		if c == P7 {
			return bad("P7 secrets and credentials are never transferred")
		}
	}
	return nil
}
