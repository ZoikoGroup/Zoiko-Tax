// Package id holds the estate's identifier types.
//
// ADR-0012 §2.2: every identifier is a distinct Go type, not an alias and not a
// defined type over uuid.UUID. Passing a FiscalDocumentID where a DecisionID is
// expected is a compile error, which is the entire point — these values are
// indistinguishable at runtime and mixing them is the kind of defect that is
// found in an audit rather than in a test.
//
// The types share an unexported embedded base so that String, UUID and IsZero
// are written once. Embedding does not make them assignable to one another, so
// the safety property survives the deduplication. It also keeps construction
// closed: the field is unexported, so no other package can write a populated
// literal, and the zero value is inert and reports IsZero.
//
// Generation lives in internal/platform/idgen, which is the only package
// permitted to call uuid.NewV7 (ADR-0012 §2.3).
package id

import (
	"fmt"

	"github.com/google/uuid"
)

// base carries the value. It is unexported so that a populated identifier
// cannot be constructed outside this package.
type base struct{ u uuid.UUID }

// UUID returns the underlying value, for the persistence boundary.
func (b base) UUID() uuid.UUID { return b.u }

// String renders the lowercase canonical form (ADR-0012 §2.1).
func (b base) String() string { return b.u.String() }

// IsZero reports whether this is the zero identifier, which never names
// anything. A zero identifier reaching a query is a defect, and callers check
// rather than pass it on.
func (b base) IsZero() bool { return b.u == uuid.Nil }

// The identifier types. Each names exactly one kind of thing.
type (
	// TenantID identifies a tenant. ADR-0012 §2.7 puts it on every row, in
	// every security context, and in the prefix of every read index.
	TenantID struct{ base }

	// UserID identifies a human or service principal within a tenant.
	UserID struct{ base }

	// SessionID identifies an authenticated session. It is not the session
	// cookie: the cookie carries a separate high-entropy secret that is never
	// stored, and this names the row.
	SessionID struct{ base }

	// DecisionID identifies one TaxDecision.
	DecisionID struct{ base }

	// FiscalDocumentID identifies one FiscalDocument.
	FiscalDocumentID struct{ base }

	// FiscalLineID identifies one line within a FiscalDocument.
	FiscalLineID struct{ base }

	// ClassificationID identifies one classification outcome.
	ClassificationID struct{ base }

	// ObligationID identifies one obligation instance.
	ObligationID struct{ base }

	// LedgerEntryID identifies one fiscal subledger entry.
	LedgerEntryID struct{ base }

	// OutboxID identifies one outbox row, and is also the CloudEvents id
	// (ADR-0014 §2.1).
	OutboxID struct{ base }

	// RequestID identifies one inbound request, for correlation.
	RequestID struct{ base }

	// AuditID identifies one administrative audit record.
	AuditID struct{ base }

	// SubmissionAttemptID identifies one attempt to file with an authority.
	SubmissionAttemptID struct{ base }
)

// The constructors. Each takes a uuid.UUID that idgen produced, or that the
// persistence layer read back. They are deliberately unexciting: the safety in
// ADR-0012 §2.2 comes from the types being distinct, not from the constructors
// being clever.

// NewTenantID wraps a raw UUID as a TenantID.
func NewTenantID(u uuid.UUID) TenantID { return TenantID{base{u}} }

// NewUserID wraps a raw UUID as a UserID.
func NewUserID(u uuid.UUID) UserID { return UserID{base{u}} }

// NewSessionID wraps a raw UUID as a SessionID.
func NewSessionID(u uuid.UUID) SessionID { return SessionID{base{u}} }

// NewDecisionID wraps a raw UUID as a DecisionID.
func NewDecisionID(u uuid.UUID) DecisionID { return DecisionID{base{u}} }

// NewFiscalDocumentID wraps a raw UUID as a FiscalDocumentID.
func NewFiscalDocumentID(u uuid.UUID) FiscalDocumentID { return FiscalDocumentID{base{u}} }

// NewFiscalLineID wraps a raw UUID as a FiscalLineID.
func NewFiscalLineID(u uuid.UUID) FiscalLineID { return FiscalLineID{base{u}} }

// NewClassificationID wraps a raw UUID as a ClassificationID.
func NewClassificationID(u uuid.UUID) ClassificationID { return ClassificationID{base{u}} }

// NewObligationID wraps a raw UUID as a ObligationID.
func NewObligationID(u uuid.UUID) ObligationID { return ObligationID{base{u}} }

// NewLedgerEntryID wraps a raw UUID as a LedgerEntryID.
func NewLedgerEntryID(u uuid.UUID) LedgerEntryID { return LedgerEntryID{base{u}} }

// NewOutboxID wraps a raw UUID as a OutboxID.
func NewOutboxID(u uuid.UUID) OutboxID { return OutboxID{base{u}} }

// NewRequestID wraps a raw UUID as a RequestID.
func NewRequestID(u uuid.UUID) RequestID { return RequestID{base{u}} }

// NewAuditID wraps a raw UUID as a AuditID.
func NewAuditID(u uuid.UUID) AuditID { return AuditID{base{u}} }

// NewSubmissionAttemptID wraps a raw UUID as a SubmissionAttemptID.
func NewSubmissionAttemptID(u uuid.UUID) SubmissionAttemptID { return SubmissionAttemptID{base{u}} }

// parse is the shared text ingress. Identifiers arrive from a URL path, a
// cookie lookup or a database column, and all three can carry something that is
// not a UUID.
func parse(kind, s string) (uuid.UUID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("id: parse %s %q: %w", kind, s, err)
	}
	return u, nil
}

// ParseTenantID reads a TenantID from its canonical string form.
func ParseTenantID(s string) (TenantID, error) {
	u, err := parse("tenant id", s)
	if err != nil {
		return TenantID{}, err
	}
	return NewTenantID(u), nil
}

// ParseUserID reads a UserID from its canonical string form.
func ParseUserID(s string) (UserID, error) {
	u, err := parse("user id", s)
	if err != nil {
		return UserID{}, err
	}
	return NewUserID(u), nil
}

// ParseDecisionID reads a DecisionID from its canonical string form.
func ParseDecisionID(s string) (DecisionID, error) {
	u, err := parse("decision id", s)
	if err != nil {
		return DecisionID{}, err
	}
	return NewDecisionID(u), nil
}

// ParseSessionID reads a SessionID from its canonical string form.
func ParseSessionID(s string) (SessionID, error) {
	u, err := parse("session id", s)
	if err != nil {
		return SessionID{}, err
	}
	return NewSessionID(u), nil
}

// ParseFiscalDocumentID reads a FiscalDocumentID from its canonical string form.
func ParseFiscalDocumentID(s string) (FiscalDocumentID, error) {
	u, err := parse("fiscal document id", s)
	if err != nil {
		return FiscalDocumentID{}, err
	}
	return NewFiscalDocumentID(u), nil
}

// ParseObligationID reads an ObligationID from its canonical string form.
func ParseObligationID(s string) (ObligationID, error) {
	u, err := parse("obligation id", s)
	if err != nil {
		return ObligationID{}, err
	}
	return NewObligationID(u), nil
}
