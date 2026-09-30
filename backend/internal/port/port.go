// Package port holds the interfaces the app layer depends on.
//
// ADR-0009 §2.3: modules communicate in-process through interfaces, never
// through the network and never through shared mutable state. Keeping the seam
// real is the point — extracting a module later means implementing the same
// interface over a transport, rather than going looking for the boundary after
// the fact.
//
// Every method takes a context.Context carrying the security context, and every
// implementation derives its tenant predicate from that rather than from an
// argument (ADR-0012 §2.7). A repository method with a TenantID parameter would
// be a method a caller can get wrong.
package port

import (
	"context"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/idempotency"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// Tx is a transaction handle.
//
// ADR-0009 §2.4 puts the transaction boundary in internal/app and nowhere else:
// use-case handlers open the transaction, pass it down, and commit. Domain code
// never sees one and adapters never open one, so there is exactly one place
// where "what is atomic" is decided and it is reviewable.
type Tx interface {
	Commit(ctx context.Context) error
	// Rollback is safe to call after a commit, so a deferred rollback is the
	// correct idiom rather than something to guard with a flag.
	Rollback(ctx context.Context) error
}

// TxManager opens transactions.
type TxManager interface {
	Begin(ctx context.Context) (Tx, context.Context, error)
}

// TenantRepository reads and writes tenants.
//
// Tenants are the one entity not scoped to a tenant, so these methods take
// explicit arguments. Everything else in this file derives its scope from the
// context.
type TenantRepository interface {
	Create(ctx context.Context, t identity.Tenant) error
	ByID(ctx context.Context, tenantID id.TenantID) (identity.Tenant, error)
	BySlug(ctx context.Context, slug string) (identity.Tenant, error)
	List(ctx context.Context, limit int) ([]identity.Tenant, error)
	SetStatus(ctx context.Context, tenantID id.TenantID, status identity.TenantStatus, reason string, actor *id.UserID, at time.Time) error
}

// UserRepository reads and writes users within the context's tenant.
type UserRepository interface {
	Create(ctx context.Context, u identity.User) error
	ByID(ctx context.Context, userID id.UserID) (identity.User, error)
	// ByEmail is the sign-in lookup. It takes an explicit tenant because the
	// caller has resolved a tenant from the request but has no security context
	// yet — there is nobody authenticated at that point.
	ByEmail(ctx context.Context, tenantID id.TenantID, email string) (identity.User, error)
	List(ctx context.Context, limit int) ([]identity.User, error)
	SetStatus(ctx context.Context, userID id.UserID, status identity.UserStatus) error
	SetVerifier(ctx context.Context, userID id.UserID, v identity.Verifier) error
	SetDisplayName(ctx context.Context, userID id.UserID, name string) error
	GrantRole(ctx context.Context, userID id.UserID, role security.Role, grantedBy id.UserID, at time.Time) error
	RevokeRole(ctx context.Context, userID id.UserID, role security.Role) error
	Roles(ctx context.Context, userID id.UserID) ([]security.Role, error)
}

// SessionRepository reads and writes sessions.
type SessionRepository interface {
	Create(ctx context.Context, s identity.Session) error
	// ByTokenDigest resolves a cookie to a session. It takes no tenant, and
	// cannot: the caller has a cookie and nothing else. The tenant comes from
	// the row, which is what makes the lookup safe — a digest resolves to
	// exactly one session or to none.
	ByTokenDigest(ctx context.Context, digest []byte) (identity.Session, error)
	ListForTenant(ctx context.Context, limit int) ([]identity.Session, error)
	Slide(ctx context.Context, sessionID id.SessionID, idleExpiresAt time.Time) error
	Revoke(ctx context.Context, sessionID id.SessionID, reason string, at time.Time) error
	RevokeAllForUser(ctx context.Context, userID id.UserID, reason string, at time.Time) (int, error)
	DeleteExpired(ctx context.Context, before time.Time) (int, error)
}

// AuditRecord is one administrative action.
type AuditRecord struct {
	ID          id.AuditID
	TenantID    id.TenantID
	ActorUserID *id.UserID
	Action      string
	SubjectType string
	SubjectID   string
	// Detail is canonical JSON bytes (ADR-0011), holding no credential material
	// and no fiscal amount.
	Detail     []byte
	RecordedAt time.Time
}

// AuditRepository appends administrative audit records.
//
// There is no update and no delete, and that is the whole point of the
// interface: the database grant does not permit them either, so the constraint
// is expressed twice and neither expression is load-bearing alone.
type AuditRepository interface {
	Append(ctx context.Context, r AuditRecord) error
	List(ctx context.Context, limit int) ([]AuditRecord, error)
	ListForSubject(ctx context.Context, subjectType, subjectID string, limit int) ([]AuditRecord, error)
}

// ---------------------------------------------------------------------------
// evidence
// ---------------------------------------------------------------------------

// EvidenceStore is the regional immutable evidence object store (Build Plan W1
// lane D; ADR-0015 §2.4's "immutable regional object store").
//
// It is content-addressed and write-once. The key is the digest of the bytes,
// computed by the store, so there is no call that can put different bytes
// under an existing key — a second Put of the same bytes is a no-op, and there
// is no Delete. Objects are scoped to the tenant in the context, so a tenant's
// evidence can be retained, held and eventually disposed of as that tenant's.
//
// Get verifies what it returns: bytes that no longer hash to their key are an
// integrity failure, reported as one, never returned.
type EvidenceStore interface {
	Put(ctx context.Context, data []byte) (canonical.Digest, error)
	Get(ctx context.Context, digest canonical.Digest) ([]byte, error)
}

// DecisionRepository indexes recorded decisions (ADR-0003).
//
// Append-only by interface as well as by grant: there is no update and no
// delete, and a correction is a new decision whose Supersedes names the old.
type DecisionRepository interface {
	Append(ctx context.Context, r evidence.Record) error
	ByID(ctx context.Context, decisionID id.DecisionID) (evidence.Record, error)
	// AsOf is ADR-0003 §2.3: the version of a business key that was current
	// at decisionTime, for an event at eventTime. History is reconstructed by
	// ordering, never by a closed range.
	AsOf(ctx context.Context, businessKey string, decisionTime, eventTime time.Time) (evidence.Record, error)
	// History is every version of a business key, oldest first.
	History(ctx context.Context, businessKey string) ([]evidence.Record, error)
	// SealLeaves returns the leaves of every decision recorded in
	// [from, to), in evidence.LeafOrder.
	SealLeaves(ctx context.Context, from, to time.Time) ([]evidence.SealLeaf, error)
}

// ---------------------------------------------------------------------------
// idempotency
// ---------------------------------------------------------------------------

// IdempotencyRepository reads and writes idempotency records (ADR-0013).
//
// The record's key carries a tenant because the domain type does; every
// implementation refuses a key whose tenant is not the one in the context,
// rather than trusting it.
type IdempotencyRepository interface {
	// Insert writes a PENDING record. It reports false, and no error, when a
	// record for the key already exists: the primary key refusing the insert is
	// the concurrency control of ADR-0013 §2.5, not a failure.
	Insert(ctx context.Context, r idempotency.Record) (bool, error)
	// Get reads the record for a key, verifying a settled body against its
	// digest. A missing record is CategoryNotFound.
	Get(ctx context.Context, key idempotency.Key) (idempotency.Record, error)
	// Complete settles a PENDING record. It runs in the transaction that
	// applies the domain effect (ADR-0013 §2.6), and refuses a record that is
	// no longer PENDING.
	Complete(ctx context.Context, r idempotency.Record) error
	// Release deletes a PENDING record after a transient failure, so a retry is
	// a genuine new attempt (ADR-0013 §2.7). A settled record is never deleted
	// by this.
	Release(ctx context.Context, key idempotency.Key) error
	// Expire deletes the record for a key if it expired at or before now, and
	// reports whether it did. An expired key behaves as a new key (§2.9).
	Expire(ctx context.Context, key idempotency.Key, now time.Time) (bool, error)
}

// SealRepository indexes period seals.
type SealRepository interface {
	// LockSealing serialises sealing for the tenant in scope until the
	// enclosing transaction ends, so two sealers cannot both find a period
	// unsealed and both seal it.
	LockSealing(ctx context.Context) error
	// Overlapping reports seals whose period intersects [from, to).
	Overlapping(ctx context.Context, from, to time.Time) ([]evidence.SealRecord, error)
	Append(ctx context.Context, r evidence.SealRecord) error
	ByID(ctx context.Context, sealID id.SealID) (evidence.SealRecord, error)
}
