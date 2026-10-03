package content

import (
	"github.com/zoikogroup/zoikotax/backend/internal/domain/sourcing"
)

// IncidentResponse is what a source incident requires of one pack: the pack
// half of the "licence expiry/withdrawal" path in the Build Plan's risk
// register.
type IncidentResponse struct {
	Pack PackID
	// Affected is false when the pack does not depend on the source, in which
	// case nothing else here applies.
	Affected bool
	From, To PackStatus
	// NewOutcomesPermitted is whether the pack may keep authorising new
	// outcomes. It is always false for an affected pack: SUSPENDED content must
	// not silently continue (ZTAX-CONT-REQ-0042), and a degraded-mode policy
	// that says otherwise is an explicit, separate decision this function does
	// not make on anyone's behalf.
	NewOutcomesPermitted bool
	// ReplayPermitted is whether decisions already made under the pack may
	// still be reproduced from it. It follows the licence's surviving rights
	// for a lapse (ZTAX-SRC-REQ-0030) and the rights profile otherwise, and it
	// is never a reason to delete a bundle: historical bundles stay retrievable
	// for authorised replay either way (ZTAX-CONT-REQ-0024, -0043); this says
	// whether the replay is licensed.
	ReplayPermitted bool
	// ImpactAssessment is always required for an affected pack
	// (ZTAX-SRC-REQ-0032, ZTAX-CONT-REQ-0054): which customers, periods,
	// filings and private bundles relied on the source.
	ImpactAssessment bool
	Reason           sourcing.Reason
	Severity         sourcing.Severity
}

// RespondToSourceIncident is the defined pack path for a source leaving use.
//
// An affected pack in any live status moves to SUSPENDED — "a license
// termination can trigger suspension even if legal content remains correct"
// (CONT-001 §25) — with an impact assessment owed. It does not move straight to
// WITHDRAWN even when the source is withdrawn: withdrawing a pack is a
// governed decision about open obligations and unfiled returns
// (ZTAX-CONT-REQ-0082) that needs a person and a replacement analysis, and
// suspension is the state that stops new outcomes while that happens. A pack
// already SUSPENDED or WITHDRAWN stays where it is.
//
// record is the source's licence record as it stood before the incident; its
// surviving rights decide replay.
func RespondToSourceIncident(pack PackManifest, status PackStatus, inc sourcing.Incident,
	record sourcing.SourceLicenseRecord) (IncidentResponse, error) {
	if err := inc.Validate(); err != nil {
		return IncidentResponse{}, err
	}
	if record.SourceID != inc.Source {
		return IncidentResponse{}, errorf("incident is about %s and the record is %s", inc.Source, record.SourceID)
	}
	if !status.Valid() {
		return IncidentResponse{}, errorf("pack %s has status %q", pack.ID, status)
	}
	r := IncidentResponse{Pack: pack.ID, From: status, To: status, Reason: inc.Reason, Severity: inc.Severity}
	for _, s := range pack.Sources {
		if s.Source == inc.Source {
			r.Affected = true
		}
	}
	if !r.Affected {
		r.NewOutcomesPermitted = status.Releasable()
		r.ReplayPermitted = true
		return r, nil
	}
	r.ImpactAssessment = true
	r.ReplayPermitted = record.RightAfter(sourcing.RightHistoricalReplay, inc.Reason)
	if CanTransitionPack(status, PackSuspended) {
		r.To = PackSuspended
	}
	return r, nil
}
