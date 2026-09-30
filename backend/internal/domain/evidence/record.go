package evidence

import (
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Record is the decision's row in the cell database: the index that names the
// evidence objects, plus the columns ADR-0003's as-of reads and the seal's leaf
// query need.
//
// The evidence objects are authoritative and the row is not. Every digest here
// is checked against the object it names on replay, so a row that disagrees
// with its evidence is detected rather than believed.
type Record struct {
	DecisionID id.DecisionID
	TenantID   id.TenantID

	// ADR-0003 §2.2's temporal columns.
	BusinessKey string
	Supersedes  *id.DecisionID
	// ValidFrom is the event time. A decision is valid for the event it was
	// made about; it has no end until something supersedes it, and even then
	// the row is not closed — history is reconstructed by ordering, not by
	// mutation (ADR-0003 §2.3).
	ValidFrom time.Time
	// RecordedAt is decision time, from the envelope, never the database's
	// now() (ADR-0003 §2.5).
	RecordedAt time.Time
	EventTime  time.Time

	Outcome Outcome
	Reason  errs.ReasonCode

	BundleID     string
	BundleDigest string
	IRVersion    int
	CanonProfile string
	Trains       Trains

	InputDigest    canonical.Digest
	EnvelopeDigest canonical.Digest
	ResultDigest   canonical.Digest

	// InputCanonical and TraceCanonical are query copies for the jsonb
	// columns, so an analyst can search decisions by input without fetching
	// objects. They are not evidence: jsonb normalizes what it stores, and
	// the replay reads the objects, never these.
	InputCanonical []byte
	TraceCanonical []byte
}

// NewRecord indexes a decision.
func NewRecord(d Decision) (Record, error) {
	if d.ID.IsZero() || d.TenantID.IsZero() {
		return Record{}, fmt.Errorf("evidence: decision has no id or tenant")
	}
	if d.BusinessKey == "" {
		return Record{}, fmt.Errorf("evidence: decision %s has no business key", d.ID)
	}
	if d.EnvelopeDigest.IsZero() || d.ResultDigest.IsZero() {
		return Record{}, fmt.Errorf("evidence: decision %s names no evidence", d.ID)
	}
	inputDigest, err := d.Envelope.InputDigest()
	if err != nil {
		return Record{}, err
	}
	input, err := canonical.Encode(d.Envelope.Input.Canonical())
	if err != nil {
		return Record{}, err
	}
	trace, err := canonical.Encode(CanonicalTrace(d.Result.Trace))
	if err != nil {
		return Record{}, err
	}
	return Record{
		DecisionID:     d.ID,
		TenantID:       d.TenantID,
		BusinessKey:    d.BusinessKey,
		Supersedes:     d.Supersedes,
		ValidFrom:      d.Envelope.EventTime,
		RecordedAt:     d.Envelope.DecisionTime,
		EventTime:      d.Envelope.EventTime,
		Outcome:        d.Result.Outcome,
		Reason:         d.Result.Reason,
		BundleID:       d.Envelope.BundleID,
		BundleDigest:   d.Envelope.BundleDigest,
		IRVersion:      d.Envelope.IRVersion,
		CanonProfile:   d.Envelope.CanonProfile,
		Trains:         d.Envelope.Trains,
		InputDigest:    inputDigest,
		EnvelopeDigest: d.EnvelopeDigest,
		ResultDigest:   d.ResultDigest,
		InputCanonical: input,
		TraceCanonical: trace,
	}, nil
}

// Leaf is the record's contribution to a period seal.
func (r Record) Leaf() SealLeaf {
	return SealLeaf{DecisionID: r.DecisionID, RecordedAt: r.RecordedAt, ResultDigest: r.ResultDigest}
}
