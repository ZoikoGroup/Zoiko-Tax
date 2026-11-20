package id

import "github.com/google/uuid"

// The ERP journal bridge's identifiers (LED-23). They live in their own file
// so the bridge adds its types without touching the shared list in id.go.

// ERPBridgeID identifies one ERP journal bridge: the connection of a member
// legal entity's Tax Control Subledger to one customer general ledger.
type ERPBridgeID struct{ base }

// NewERPBridgeID wraps a raw UUID as an ERPBridgeID.
func NewERPBridgeID(u uuid.UUID) ERPBridgeID { return ERPBridgeID{base{u}} }

// ParseERPBridgeID reads an ERPBridgeID from its canonical string form.
func ParseERPBridgeID(s string) (ERPBridgeID, error) {
	u, err := parse("ERP bridge id", s)
	if err != nil {
		return ERPBridgeID{}, err
	}
	return NewERPBridgeID(u), nil
}

// ERPExportID identifies one sealed ERP export batch. It is also the
// external reference the ERP deduplicates on, so it never changes across
// dispatch attempts.
type ERPExportID struct{ base }

// NewERPExportID wraps a raw UUID as an ERPExportID.
func NewERPExportID(u uuid.UUID) ERPExportID { return ERPExportID{base{u}} }

// ParseERPExportID reads an ERPExportID from its canonical string form.
func ParseERPExportID(s string) (ERPExportID, error) {
	u, err := parse("ERP export batch id", s)
	if err != nil {
		return ERPExportID{}, err
	}
	return NewERPExportID(u), nil
}

// ERPAttemptID identifies one dispatch attempt of an export batch.
type ERPAttemptID struct{ base }

// NewERPAttemptID wraps a raw UUID as an ERPAttemptID.
func NewERPAttemptID(u uuid.UUID) ERPAttemptID { return ERPAttemptID{base{u}} }

// ParseERPAttemptID reads an ERPAttemptID from its canonical string form.
func ParseERPAttemptID(s string) (ERPAttemptID, error) {
	u, err := parse("ERP dispatch attempt id", s)
	if err != nil {
		return ERPAttemptID{}, err
	}
	return NewERPAttemptID(u), nil
}

// GLSnapshotID identifies one set of general-ledger balances an ERP reported.
type GLSnapshotID struct{ base }

// NewGLSnapshotID wraps a raw UUID as a GLSnapshotID.
func NewGLSnapshotID(u uuid.UUID) GLSnapshotID { return GLSnapshotID{base{u}} }

// ParseGLSnapshotID reads a GLSnapshotID from its canonical string form.
func ParseGLSnapshotID(s string) (GLSnapshotID, error) {
	u, err := parse("GL balance snapshot id", s)
	if err != nil {
		return GLSnapshotID{}, err
	}
	return NewGLSnapshotID(u), nil
}

// GLReconciliationID identifies one subledger-to-GL reconciliation (R6).
type GLReconciliationID struct{ base }

// NewGLReconciliationID wraps a raw UUID as a GLReconciliationID.
func NewGLReconciliationID(u uuid.UUID) GLReconciliationID { return GLReconciliationID{base{u}} }

// ParseGLReconciliationID reads a GLReconciliationID from its canonical
// string form.
func ParseGLReconciliationID(s string) (GLReconciliationID, error) {
	u, err := parse("GL reconciliation id", s)
	if err != nil {
		return GLReconciliationID{}, err
	}
	return NewGLReconciliationID(u), nil
}
