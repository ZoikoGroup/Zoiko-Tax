// Package idgen generates identifiers.
//
// ADR-0012 §2.3: this is the only package permitted to call uuid.NewV7, and its
// interface is injected so that tests substitute a deterministic generator and
// golden vectors stay stable.
//
// UUIDv7 (RFC 9562) rather than v4 because the leading 48 bits are a Unix
// millisecond timestamp, which makes generated keys roughly time-ordered. Under
// ADR-0003's append-only regime every table grows monotonically and is read
// most often by recency, so index locality is worth having for free.
package idgen

import (
	"fmt"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// Generator produces the raw values the id constructors wrap. It is an
// interface so that a test can make identifier allocation reproducible; nothing
// else about it is interesting.
type Generator interface {
	// NewUUID returns a fresh UUIDv7. It returns an error rather than panicking
	// because it reads the system entropy source, which can fail, and a process
	// that cannot generate an identifier should refuse the request rather than
	// die with a stack trace in a fiscal path.
	NewUUID() (uuid.UUID, error)
}

// V7 is the production generator.
type V7 struct{}

// NewUUID returns a fresh UUIDv7.
func (V7) NewUUID() (uuid.UUID, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("idgen: new uuidv7: %w", err)
	}
	return u, nil
}

// Sequential is a deterministic generator for tests and golden vectors. It
// emits 00000000-0000-7000-8000-0000000000NN, which is a structurally valid v7
// with a counter where the timestamp would be.
type Sequential struct{ n atomic.Uint64 }

// NewUUID returns the next identifier in the sequence.
func (s *Sequential) NewUUID() (uuid.UUID, error) {
	n := s.n.Add(1)
	var u uuid.UUID
	u[6] = 0x70 // version 7
	u[8] = 0x80 // variant RFC 4122
	for i := 0; i < 8; i++ {
		// Masked explicitly rather than relying on the conversion's truncation,
		// so the intent is in the code rather than in Go's spec.
		u[15-i] = byte((n >> (8 * i)) & 0xff)
	}
	return u, nil
}

// The typed constructors. Each is a one-liner, and that is deliberate: a caller
// that wants a DecisionID asks for a DecisionID, so no call site ever holds a
// bare uuid.UUID long enough to put it in the wrong field.

// TenantID returns a fresh TenantID.
func TenantID(g Generator) (id.TenantID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.TenantID{}, err
	}
	return id.NewTenantID(u), nil
}

// UserID returns a fresh UserID.
func UserID(g Generator) (id.UserID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.UserID{}, err
	}
	return id.NewUserID(u), nil
}

// SessionID returns a fresh SessionID.
func SessionID(g Generator) (id.SessionID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.SessionID{}, err
	}
	return id.NewSessionID(u), nil
}

// DecisionID returns a fresh DecisionID.
func DecisionID(g Generator) (id.DecisionID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.DecisionID{}, err
	}
	return id.NewDecisionID(u), nil
}

// FiscalDocumentID returns a fresh FiscalDocumentID.
func FiscalDocumentID(g Generator) (id.FiscalDocumentID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.FiscalDocumentID{}, err
	}
	return id.NewFiscalDocumentID(u), nil
}

// FiscalLineID returns a fresh FiscalLineID.
func FiscalLineID(g Generator) (id.FiscalLineID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.FiscalLineID{}, err
	}
	return id.NewFiscalLineID(u), nil
}

// ClassificationID returns a fresh ClassificationID.
func ClassificationID(g Generator) (id.ClassificationID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.ClassificationID{}, err
	}
	return id.NewClassificationID(u), nil
}

// ObligationID returns a fresh ObligationID.
func ObligationID(g Generator) (id.ObligationID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.ObligationID{}, err
	}
	return id.NewObligationID(u), nil
}

// LedgerEntryID returns a fresh LedgerEntryID.
func LedgerEntryID(g Generator) (id.LedgerEntryID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.LedgerEntryID{}, err
	}
	return id.NewLedgerEntryID(u), nil
}

// OutboxID returns a fresh OutboxID, which is also the CloudEvents id.
func OutboxID(g Generator) (id.OutboxID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.OutboxID{}, err
	}
	return id.NewOutboxID(u), nil
}

// RequestID returns a fresh RequestID.
func RequestID(g Generator) (id.RequestID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.RequestID{}, err
	}
	return id.NewRequestID(u), nil
}

// AuditID returns a fresh AuditID.
func AuditID(g Generator) (id.AuditID, error) {
	u, err := g.NewUUID()
	if err != nil {
		return id.AuditID{}, err
	}
	return id.NewAuditID(u), nil
}
