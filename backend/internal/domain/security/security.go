// Package security holds the canonical security context.
//
// ADR-0012 §2.7 requires tenant_id in every security context and in the prefix
// of every read index; ADR-0008 control 4 makes a query without a tenant_id
// predicate a build failure. This package is where that requirement becomes a
// type: a Context cannot be constructed without a tenant, and the repository
// layer takes a Context rather than a bare TenantID so that no call site can
// reach the database having forgotten which tenant it is acting for.
//
// The zero Context is unauthenticated and holds no tenant. Every accessor on it
// reports that honestly rather than returning a plausible zero identifier, so a
// missing authentication step fails closed.
package security

import (
	"context"
	"slices"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// Role is a named capability grant within a tenant.
//
// Roles are coarse on purpose at this stage. A fine-grained permission model
// belongs with the entitlement work in W3 lane N, and inventing one now would
// mean guessing at the shape of screens that do not exist. What is settled is
// that roles are per tenant and are checked structurally, never by string
// comparison at a call site.
type Role string

// The roles. Each is scoped to one tenant; there is no cross-tenant role, and
// no global superuser reachable through the API — a support path that can read
// any tenant's fiscal data is a residency violation waiting to be audited
// (ADR-0009 §2.6).
const (
	// RoleAdmin administers the tenant: users, roles, sessions.
	RoleAdmin Role = "ADMIN"
	// RoleOperator runs fiscal operations: commit, adjust, refund.
	RoleOperator Role = "OPERATOR"
	// RoleAnalyst reads decisions, obligations and evidence.
	RoleAnalyst Role = "ANALYST"
	// RoleAuditor reads evidence and audit records, and nothing else. It is
	// deliberately not a subset of ADMIN: an auditor who can change what they
	// audit is not an auditor.
	RoleAuditor Role = "AUDITOR"
)

// AllRoles is the closed set, for validation and for the admin surface.
var AllRoles = []Role{RoleAdmin, RoleOperator, RoleAnalyst, RoleAuditor}

// Valid reports whether r is a known role. An unknown role in a database row is
// a data-quality finding and is dropped on load rather than honoured.
func (r Role) Valid() bool { return slices.Contains(AllRoles, r) }

// Context is the canonical security context for one request.
//
// It is a value, not a pointer, and its fields are unexported: it is built once
// by the authentication middleware and read everywhere after, and nothing
// downstream may widen its own authority by mutating it.
type Context struct {
	tenant  id.TenantID
	subject id.UserID
	session id.SessionID
	roles   []Role
	// authenticatedAt is when the session was established, not when this
	// request arrived. Re-authentication for a consequential action (ADR-0019
	// C5) compares against it.
	authenticatedAt time.Time
}

// New builds a security context. It is called by the authentication middleware
// and by nothing else.
func New(tenant id.TenantID, subject id.UserID, session id.SessionID, roles []Role, authenticatedAt time.Time) Context {
	kept := make([]Role, 0, len(roles))
	for _, r := range roles {
		// An unknown role is dropped rather than carried. A role this build
		// does not understand cannot be checked, and carrying it would let a
		// future rename silently grant access.
		if r.Valid() && !slices.Contains(kept, r) {
			kept = append(kept, r)
		}
	}
	slices.Sort(kept)
	return Context{
		tenant:          tenant,
		subject:         subject,
		session:         session,
		roles:           kept,
		authenticatedAt: authenticatedAt.UTC(),
	}
}

// Tenant returns the tenant this request acts for.
func (c Context) Tenant() id.TenantID { return c.tenant }

// Subject returns the authenticated principal.
func (c Context) Subject() id.UserID { return c.subject }

// Session returns the session backing this request.
func (c Context) Session() id.SessionID { return c.session }

// Roles returns the subject's roles, sorted, as a copy.
func (c Context) Roles() []Role { return slices.Clone(c.roles) }

// AuthenticatedAt returns when the session was established.
func (c Context) AuthenticatedAt() time.Time { return c.authenticatedAt }

// Authenticated reports whether this context names a subject. The zero Context
// does not, which is what makes forgetting the middleware fail closed.
func (c Context) Authenticated() bool {
	return !c.tenant.IsZero() && !c.subject.IsZero()
}

// HasRole reports whether the subject holds a role.
func (c Context) HasRole(r Role) bool { return slices.Contains(c.roles, r) }

// HasAny reports whether the subject holds at least one of the given roles.
func (c Context) HasAny(roles ...Role) bool {
	for _, r := range roles {
		if c.HasRole(r) {
			return true
		}
	}
	return false
}

// ctxKey is the unexported key type for stashing a Context in a
// context.Context. Unexported so no other package can write the value.
type ctxKey struct{}

// Into returns a context carrying the security context.
func Into(ctx context.Context, sc Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, sc)
}

// From returns the security context carried by ctx. The second result is false
// when there is none, and the first is then the zero Context — which is
// unauthenticated, so a caller that ignores the boolean still fails closed.
func From(ctx context.Context) (Context, bool) {
	sc, ok := ctx.Value(ctxKey{}).(Context)
	return sc, ok
}

// MustTenant returns the tenant from ctx, and false if the context carries
// none. It is the accessor the repository layer uses, so that the tenant
// predicate in every query comes from the request's own security context rather
// than from an argument a caller could supply wrongly.
//
// It tests for a tenant rather than for a subject, because background work has
// a tenant and no subject (see System) and must still be able to read. The zero
// Context has neither, so an unauthenticated request still fails closed.
func MustTenant(ctx context.Context) (id.TenantID, bool) {
	sc, ok := From(ctx)
	if !ok || sc.tenant.IsZero() {
		return id.TenantID{}, false
	}
	return sc.Tenant(), true
}

// System returns a context for internal work that has no human subject — the
// outbox relay, a background sweep, a migration verification step. It carries a
// tenant, because even background work is scoped to one, and no roles, so it
// cannot pass an authorization check meant for a user.
//
// It is deliberately not reachable from the transport layer: there is no header
// or credential that produces it, only a caller inside the process constructing
// one explicitly.
func System(tenant id.TenantID) Context {
	return Context{tenant: tenant}
}
