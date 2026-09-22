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

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
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
