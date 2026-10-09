package app

import (
	"context"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/outbox"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// The events a cell emits (ADR-0014; W2 lane K's AsyncAPI definitions).
//
// Every event leaves through the outbox, in the transaction that made the
// change it reports (ADR-0014 §2.1). Each one is registered three times, and
// the three are checked against one another: here, as a type and a schema
// reference; in contracts/schemas/events, as the JSON Schema its payload
// validates against; and in contracts/asyncapi, as a message on the channel a
// consumer subscribes to. events_test.go fails when they disagree.
//
// Events are thin. They say what changed and where to read it, and carry no
// fiscal amount a consumer could act on without the authorization a read
// requires: a decision event names the decision and its outcome, and the
// figures are GET /v1/decisions/{id}. An event delivered to a webhook endpoint
// leaves the cell's control, and a figure in it is a classified value in a
// system nobody here operates (ZTAX-PRIV-001 §24). The exceptions carry their
// amount because the amount is the event: a threshold crossing, and a refund.

// EventSpec is one registered event.
type EventSpec struct {
	Type      string
	SchemaRef string
}

// The decision and obligation events.
const (
	EventDecisionCommitted           = outbox.TypeNamespace + ".decision.committed"
	SchemaDecisionCommittedRef       = "ztax:events/decision-committed/1.0.0"
	EventDecisionCorrected           = outbox.TypeNamespace + ".decision.corrected"
	SchemaDecisionCorrectedRef       = "ztax:events/decision-corrected/1.0.0"
	EventObligationStatusChanged     = outbox.TypeNamespace + ".obligation.status-changed"
	SchemaObligationStatusChangedRef = "ztax:events/obligation-status-changed/1.0.0"
)

// Events is every event this build emits.
func Events() []EventSpec {
	return []EventSpec{
		{EventDecisionCommitted, SchemaDecisionCommittedRef},
		{EventDecisionCorrected, SchemaDecisionCorrectedRef},
		{EventObligationStatusChanged, SchemaObligationStatusChangedRef},
		{EventThresholdCrossed, SchemaThresholdCrossedRef},
		{EventRefundRequested, SchemaRefundRequestedRef},
		{EventRefundStatusChanged, SchemaRefundStatusChangedRef},
		{EventDocumentCommitted, SchemaDocumentCommittedRef},
	}
}

// outboxWired reports whether the service can emit.
func (s *DeterminationService) outboxWired() bool {
	return s.fiscal != nil && s.fiscal.Outbox != nil
}

// emitDecision announces a recorded decision, in the caller's transaction. A
// decision that supersedes another is a correction, and says which.
func (s *DeterminationService) emitDecision(ctx context.Context, d evidence.Decision) error {
	if !s.outboxWired() {
		return nil
	}
	rec, err := evidence.NewRecord(d)
	if err != nil {
		return internal(err, "The decision event could not be published.")
	}
	eid, err := idgen.OutboxID(s.ids)
	if err != nil {
		return internal(err, "The decision event could not be published.")
	}
	fields := []canonical.Field{
		canonical.F("decisionId", canonical.String(d.ID.String())),
		canonical.F("businessKey", canonical.String(d.BusinessKey)),
		canonical.F("outcome", canonical.String(string(rec.Outcome))),
		canonical.F("reasonCode", canonical.String(string(rec.Reason))),
		canonical.F("authoritative", canonical.Bool(false)),
		canonical.F("eventTime", canonical.Time(d.Envelope.EventTime)),
		canonical.F("recordedAt", canonical.Time(d.Envelope.DecisionTime)),
		canonical.F("bundleId", canonical.String(d.Envelope.BundleID)),
		canonical.F("bundleDigest", canonical.String(d.Envelope.BundleDigest)),
		canonical.F("envelopeDigest", canonical.String(d.EnvelopeDigest.String())),
		canonical.F("resultDigest", canonical.String(d.ResultDigest.String())),
	}
	typ, schema := EventDecisionCommitted, SchemaDecisionCommittedRef
	if d.Supersedes != nil {
		typ, schema = EventDecisionCorrected, SchemaDecisionCorrectedRef
		fields = append(fields, canonical.F("supersedes", canonical.String(d.Supersedes.String())))
	}
	return s.fiscal.Outbox.Append(ctx, outbox.Event{
		ID: eid, TenantID: d.TenantID,
		// Partitioned by business key, so a decision and its corrections
		// arrive in the order they were recorded (ADR-0014 §2.7).
		AggregateKey: "decision/" + d.BusinessKey,
		Type:         typ, SchemaRef: schema, Payload: canonical.Object(fields...), CreatedAt: d.Envelope.DecisionTime,
	})
}

// appendObligation writes an obligation row and announces it, in the
// caller's transaction. previous is the row it supersedes, nil for the first.
func (s *DeterminationService) appendObligation(ctx context.Context, o obligation.Obligation, previous *obligation.Obligation) error {
	if err := s.fiscal.Obligations.Append(ctx, o); err != nil {
		return err
	}
	if !s.outboxWired() {
		return nil
	}
	eid, err := idgen.OutboxID(s.ids)
	if err != nil {
		return internal(err, "The obligation event could not be published.")
	}
	fields := []canonical.Field{
		canonical.F("obligationId", canonical.String(o.ID.String())),
		canonical.F("type", canonical.String(o.Type)),
		canonical.F("authority", canonical.String(o.Authority)),
		canonical.F("jurisdiction", canonical.String(o.JurisdictionID)),
		canonical.F("legalEntityId", canonical.String(o.LegalEntity.String())),
		canonical.F("periodStart", canonical.String(o.Period.Start.Format(time.DateOnly))),
		canonical.F("periodEnd", canonical.String(o.Period.End.Format(time.DateOnly))),
		canonical.F("dueDate", canonical.String(o.Period.Due.Format(time.DateOnly))),
		canonical.F("status", canonical.String(string(o.Status))),
		canonical.F("recordedAt", canonical.Time(o.RecordedAt)),
	}
	if previous != nil {
		fields = append(fields,
			canonical.F("supersedes", canonical.String(previous.ID.String())),
			canonical.F("previousStatus", canonical.String(string(previous.Status))))
	}
	return s.fiscal.Outbox.Append(ctx, outbox.Event{
		ID: eid, TenantID: o.TenantID, AggregateKey: "obligation/" + o.BusinessKey,
		Type: EventObligationStatusChanged, SchemaRef: SchemaObligationStatusChangedRef,
		Payload: canonical.Object(fields...), CreatedAt: o.RecordedAt,
	})
}
