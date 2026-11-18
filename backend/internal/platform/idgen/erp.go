package idgen

import "github.com/zoikogroup/zoikotax/backend/internal/domain/id"

// ERPBridgeID returns a fresh ERPBridgeID.
func ERPBridgeID(g Generator) (id.ERPBridgeID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.ERPBridgeID{}, err
	}
	return id.NewERPBridgeID(u), nil
}

// ERPExportID returns a fresh ERPExportID.
func ERPExportID(g Generator) (id.ERPExportID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.ERPExportID{}, err
	}
	return id.NewERPExportID(u), nil
}

// ERPAttemptID returns a fresh ERPAttemptID.
func ERPAttemptID(g Generator) (id.ERPAttemptID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.ERPAttemptID{}, err
	}
	return id.NewERPAttemptID(u), nil
}

// GLSnapshotID returns a fresh GLSnapshotID.
func GLSnapshotID(g Generator) (id.GLSnapshotID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.GLSnapshotID{}, err
	}
	return id.NewGLSnapshotID(u), nil
}

// GLReconciliationID returns a fresh GLReconciliationID.
func GLReconciliationID(g Generator) (id.GLReconciliationID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.GLReconciliationID{}, err
	}
	return id.NewGLReconciliationID(u), nil
}
