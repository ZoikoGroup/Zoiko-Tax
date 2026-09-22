// Package outbox is the only path by which an event leaves a cell.
//
// ADR-0014 §2.1: every externally visible state change writes an outbox row in
// the same transaction as the change. No handler publishes to a broker and no
// handler calls a webhook, so an event exists only if the transaction that
// produced it committed — and conversely, a committed change always has its
// event, because both are in the same transaction.
//
// Delivery is at-least-once and nothing here claims otherwise (ADR-0014 §2.3).
// Exactly-once across a process boundary is not available, and designs that
// claim it fail quietly. The event id is the deduplication key, it is stable
// across redeliveries, and it is documented as such for every consumer
// including customers.
package outbox

import (
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Event is one outbox row, and one CloudEvent.
type Event struct {
	// ID is both the row's key and the CloudEvents id, so a consumer
	// deduplicating on the event id is deduplicating on the same value the
	// database uniquely constrains (ADR-0014 §2.1).
	ID       id.OutboxID
	TenantID id.TenantID

	// AggregateKey is the partition key. Ordering is per aggregate and nothing
	// stronger is claimed (ADR-0014 §2.7): there is no global ordering, no
	// cross-aggregate ordering and no cross-cell ordering, and a consumer
	// needing causality across aggregates uses the data rather than the
	// arrival order.
	AggregateKey string
	Type         string
	// SchemaRef names the registered JSON Schema version the payload validates
	// against. An event that does not validate fails the transaction that
	// produced it, which fails the domain operation — emitting an invalid event
	// is not a degraded outcome to be tolerated (ADR-0014 §2.6).
	SchemaRef string
	Payload   canonical.Value

	// CreatedAt is the transaction's commit time, which is what the CloudEvents
	// time carries — not the publish time (ADR-0014 §2.5). A consumer reasoning
	// about when something happened must not be reading when the relay got
	// round to it.
	CreatedAt   time.Time
	PublishedAt *time.Time
}

// TypeNamespace is the registered namespace for event types.
const TypeNamespace = "com.zoikotax"

// Validate refuses an event that cannot be published coherently.
func (e Event) Validate() error {
	switch {
	case e.ID.IsZero():
		return fmt.Errorf("outbox: event has no id")
	case e.TenantID.IsZero():
		return fmt.Errorf("outbox: event has no tenant")
	case e.AggregateKey == "":
		// Without a partition key there is no ordering guarantee at all, and a
		// consumer would receive an aggregate's events interleaved.
		return fmt.Errorf("outbox: event %s has no aggregate key", e.ID)
	case e.Type == "":
		return fmt.Errorf("outbox: event %s has no type", e.ID)
	case e.SchemaRef == "":
		return fmt.Errorf("outbox: event %s names no schema", e.ID)
	case e.Payload.IsAbsent():
		return fmt.Errorf("outbox: event %s has no payload", e.ID)
	case e.CreatedAt.IsZero():
		return fmt.Errorf("outbox: event %s has no creation time", e.ID)
	}
	return nil
}

// CloudEvent renders the ADR-0014 §2.5 envelope: CloudEvents 1.0, structured
// JSON mode.
//
// The ztx_ extension attributes carry the tenant, the cell and the seven
// release-train versions, so a consumer can name the exact combination that
// produced the event (Build Plan §6). That is the same requirement the release
// evidence manifest serves for a decision, applied at the event boundary.
func (e Event) CloudEvent(cell, region string, trains map[string]string) canonical.Value {
	fields := []canonical.Field{
		canonical.F("specversion", canonical.String("1.0")),
		canonical.F("id", canonical.String(e.ID.String())),
		canonical.F("source", canonical.String(fmt.Sprintf("/cells/%s/%s", region, cell))),
		canonical.F("type", canonical.String(e.Type)),
		canonical.F("time", canonical.Time(e.CreatedAt)),
		canonical.F("dataschema", canonical.String(e.SchemaRef)),
		canonical.F("datacontenttype", canonical.String("application/json")),
		canonical.F("subject", canonical.String(e.AggregateKey)),
		canonical.F("ztxtenant", canonical.String(e.TenantID.String())),
		canonical.F("ztxcell", canonical.String(cell)),
		canonical.F("ztxregion", canonical.String(region)),
		canonical.F("data", e.Payload),
	}
	// Train versions as individual attributes rather than a nested object:
	// CloudEvents extension attributes are flat, and a consumer filtering on a
	// train version should not have to parse a nested document to do it.
	for _, name := range []string{"app", "content", "ai", "adapter", "infra", "schema", "migration"} {
		if v, ok := trains[name]; ok && v != "" {
			fields = append(fields, canonical.F("ztxtrain"+name, canonical.String(v)))
		}
	}
	return canonical.Object(fields...)
}

// Published returns a copy marked as delivered.
func (e Event) Published(at time.Time) Event {
	t := at.UTC()
	e.PublishedAt = &t
	return e
}

// Publisher delivers an event to the broker.
//
// It is an interface so that the relay can be tested without a Kafka cluster,
// and so that the broker choice stays behind one seam. A publisher that returns
// an error leaves the row unpublished; the relay retries it, which is what
// at-least-once means.
type Publisher interface {
	Publish(envelope canonical.Value, partitionKey string) error
}
