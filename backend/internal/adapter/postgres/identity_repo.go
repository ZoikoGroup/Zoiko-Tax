package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// ---------------------------------------------------------------------------
// tenant
// ---------------------------------------------------------------------------

const (
	sqlTenantInsert = `
		INSERT INTO ztax.tenant (tenant_id, slug, display_name, residency_region, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`

	sqlTenantColumns = `tenant_id, slug, display_name, residency_region, status, created_at`

	sqlTenantByID   = `SELECT ` + sqlTenantColumns + ` FROM ztax.tenant WHERE tenant_id = $1`
	sqlTenantBySlug = `SELECT ` + sqlTenantColumns + ` FROM ztax.tenant WHERE slug = $1`
	sqlTenantList   = `SELECT ` + sqlTenantColumns + ` FROM ztax.tenant ORDER BY created_at DESC LIMIT $1`

	// The status change and its event are one statement pair inside one
	// transaction: the materialised column and the append-only history cannot
	// disagree, because nothing commits unless both land.
	sqlTenantSetStatus   = `UPDATE ztax.tenant SET status = $2 WHERE tenant_id = $1`
	sqlTenantStatusEvent = `
		INSERT INTO ztax.tenant_status_event (tenant_id, seq, status, reason, actor_user_id, recorded_at)
		VALUES ($1, (SELECT COALESCE(MAX(seq), 0) + 1 FROM ztax.tenant_status_event WHERE tenant_id = $1), $2, $3, $4, $5)`
)

// TenantRepo implements port.TenantRepository.
type TenantRepo struct{ s *Store }

// Tenants returns the tenant repository.
func (s *Store) Tenants() *TenantRepo { return &TenantRepo{s: s} }

var _ port.TenantRepository = (*TenantRepo)(nil)

// Create inserts a tenant.
func (r *TenantRepo) Create(ctx context.Context, t identity.Tenant) error {
	_, err := r.s.db(ctx).Exec(ctx, sqlTenantInsert,
		t.ID.UUID(), t.Slug, t.DisplayName, t.ResidencyRegion, string(t.Status), t.CreatedAt)
	return mapError(err, "insert tenant")
}

// ByID reads one tenant.
func (r *TenantRepo) ByID(ctx context.Context, tenantID id.TenantID) (identity.Tenant, error) {
	return scanTenant(r.s.db(ctx).QueryRow(ctx, sqlTenantByID, tenantID.UUID()))
}

// BySlug reads one tenant by its handle. This is the sign-in path's first
// lookup, before any tenant is in scope.
func (r *TenantRepo) BySlug(ctx context.Context, slug string) (identity.Tenant, error) {
	return scanTenant(r.s.db(ctx).QueryRow(ctx, sqlTenantBySlug, slug))
}

// List returns tenants in the cell, newest first.
func (r *TenantRepo) List(ctx context.Context, limit int) ([]identity.Tenant, error) {
	rows, err := r.s.db(ctx).Query(ctx, sqlTenantList, capLimit(limit))
	if err != nil {
		return nil, mapError(err, "list tenants")
	}
	defer rows.Close()

	var out []identity.Tenant
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, mapError(rows.Err(), "list tenants")
}

// SetStatus changes a tenant's status and records why.
func (r *TenantRepo) SetStatus(ctx context.Context, tenantID id.TenantID, status identity.TenantStatus, reason string, actor *id.UserID, at time.Time) error {
	db := r.s.db(ctx)
	if _, err := db.Exec(ctx, sqlTenantSetStatus, tenantID.UUID(), string(status)); err != nil {
		return mapError(err, "set tenant status")
	}
	var actorUUID *uuid.UUID
	if actor != nil {
		u := actor.UUID()
		actorUUID = &u
	}
	_, err := db.Exec(ctx, sqlTenantStatusEvent, tenantID.UUID(), string(status), reason, actorUUID, at)
	return mapError(err, "record tenant status event")
}

type scanner interface{ Scan(dest ...any) error }

func scanTenant(row scanner) (identity.Tenant, error) {
	var (
		tenantUUID uuid.UUID
		status     string
		t          identity.Tenant
	)
	err := row.Scan(&tenantUUID, &t.Slug, &t.DisplayName, &t.ResidencyRegion, &status, &t.CreatedAt)
	if err != nil {
		return identity.Tenant{}, mapError(err, "scan tenant")
	}
	t.ID = id.NewTenantID(tenantUUID)
	t.Status = identity.TenantStatus(status)
	return t, nil
}

// ---------------------------------------------------------------------------
// user
// ---------------------------------------------------------------------------

const (
	sqlUserInsert = `
		INSERT INTO ztax.app_user (tenant_id, user_id, email, display_name, password_verifier, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`

	sqlUserColumns = `tenant_id, user_id, email, display_name, password_verifier, status, created_at`

	sqlUserByID = `SELECT ` + sqlUserColumns + `
		FROM ztax.app_user WHERE tenant_id = $1 AND user_id = $2`

	// lower(email) matches the functional unique index, so this is an index
	// lookup rather than a scan, and it is case-insensitive in the same way the
	// uniqueness constraint is. Those two agreeing is not optional: if they
	// disagreed, two accounts could exist that one query treats as one.
	sqlUserByEmail = `SELECT ` + sqlUserColumns + `
		FROM ztax.app_user WHERE tenant_id = $1 AND lower(email) = lower($2)`

	sqlUserList = `SELECT ` + sqlUserColumns + `
		FROM ztax.app_user WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2`

	sqlUserSetStatus      = `UPDATE ztax.app_user SET status = $3 WHERE tenant_id = $1 AND user_id = $2`
	sqlUserSetVerifier    = `UPDATE ztax.app_user SET password_verifier = $3 WHERE tenant_id = $1 AND user_id = $2`
	sqlUserSetDisplayName = `UPDATE ztax.app_user SET display_name = $3 WHERE tenant_id = $1 AND user_id = $2`

	sqlRolesForUser = `SELECT role FROM ztax.user_role WHERE tenant_id = $1 AND user_id = $2 ORDER BY role`
	sqlRoleGrant    = `
		INSERT INTO ztax.user_role (tenant_id, user_id, role, granted_at, granted_by)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, user_id, role) DO NOTHING`
	sqlRoleRevoke = `DELETE FROM ztax.user_role WHERE tenant_id = $1 AND user_id = $2 AND role = $3`
)

// UserRepo implements port.UserRepository.
type UserRepo struct{ s *Store }

// Users returns the user repository.
func (s *Store) Users() *UserRepo { return &UserRepo{s: s} }

var _ port.UserRepository = (*UserRepo)(nil)

// Create inserts a user.
func (r *UserRepo) Create(ctx context.Context, u identity.User) error {
	var verifier *string
	if !u.Verifier.IsZero() {
		v := u.Verifier.String()
		verifier = &v
	}
	_, err := r.s.db(ctx).Exec(ctx, sqlUserInsert,
		u.TenantID.UUID(), u.ID.UUID(), identity.NormalizeEmail(u.Email),
		u.DisplayName, verifier, string(u.Status), u.CreatedAt)
	return mapError(err, "insert user")
}

// ByID reads one user in the caller's tenant, with roles.
func (r *UserRepo) ByID(ctx context.Context, userID id.UserID) (identity.User, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return identity.User{}, err
	}
	u, err := scanUser(r.s.db(ctx).QueryRow(ctx, sqlUserByID, tenant.UUID(), userID.UUID()))
	if err != nil {
		return identity.User{}, err
	}
	if u.Roles, err = r.Roles(ctx, u.ID); err != nil {
		return identity.User{}, err
	}
	return u, nil
}

// ByEmail reads one user for sign-in.
//
// The tenant is explicit because there is no security context yet: the caller
// resolved a tenant from the request and nobody is authenticated. That is the
// one place in this package where the tenant predicate is not taken from the
// context, and it is why the parameter exists on the interface at all.
func (r *UserRepo) ByEmail(ctx context.Context, tenantID id.TenantID, email string) (identity.User, error) {
	u, err := scanUser(r.s.db(ctx).QueryRow(ctx, sqlUserByEmail, tenantID.UUID(), identity.NormalizeEmail(email)))
	if err != nil {
		return identity.User{}, err
	}
	roles, err := r.rolesFor(ctx, tenantID, u.ID)
	if err != nil {
		return identity.User{}, err
	}
	u.Roles = roles
	return u, nil
}

// List returns the tenant's users, newest first, with roles.
func (r *UserRepo) List(ctx context.Context, limit int) ([]identity.User, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlUserList, tenant.UUID(), capLimit(limit))
	if err != nil {
		return nil, mapError(err, "list users")
	}
	defer rows.Close()

	var out []identity.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err, "list users")
	}
	// Roles are loaded after the cursor is closed rather than inside the loop:
	// querying on the same connection while a result set is open is how a
	// deadlock against your own pool happens.
	for i := range out {
		roles, err := r.rolesFor(ctx, tenant, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Roles = roles
	}
	return out, nil
}

// SetStatus enables or disables a user.
func (r *UserRepo) SetStatus(ctx context.Context, userID id.UserID, status identity.UserStatus) error {
	return r.update(ctx, sqlUserSetStatus, userID, string(status), "set user status")
}

// SetVerifier replaces a user's password verifier.
func (r *UserRepo) SetVerifier(ctx context.Context, userID id.UserID, v identity.Verifier) error {
	return r.update(ctx, sqlUserSetVerifier, userID, v.String(), "set user verifier")
}

// SetDisplayName renames a user.
func (r *UserRepo) SetDisplayName(ctx context.Context, userID id.UserID, name string) error {
	return r.update(ctx, sqlUserSetDisplayName, userID, name, "set user display name")
}

func (r *UserRepo) update(ctx context.Context, query string, userID id.UserID, value string, op string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	tag, err := r.s.db(ctx).Exec(ctx, query, tenant.UUID(), userID.UUID(), value)
	if err != nil {
		return mapError(err, op)
	}
	// Zero rows means the user is not in this tenant. Reporting not-found
	// rather than success matters: a silent no-op would let an administrator
	// believe they had disabled an account that is still live.
	if tag.RowsAffected() == 0 {
		return errs.New(errs.CategoryNotFound, errs.ReasonNotFound,
			"The referenced resource does not exist, or is outside the caller's tenant.")
	}
	return nil
}

// GrantRole adds a role. It is idempotent: granting a role twice is not an
// error, because an administrator clicking twice has not done anything wrong.
func (r *UserRepo) GrantRole(ctx context.Context, userID id.UserID, role security.Role, grantedBy id.UserID, at time.Time) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	if !role.Valid() {
		return errs.Invalid("role", errs.ReasonInvalidValue, "That role is not one this service recognises.")
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlRoleGrant,
		tenant.UUID(), userID.UUID(), string(role), at, grantedBy.UUID())
	return mapError(err, "grant role")
}

// RevokeRole removes a role. Also idempotent, for the same reason.
func (r *UserRepo) RevokeRole(ctx context.Context, userID id.UserID, role security.Role) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlRoleRevoke, tenant.UUID(), userID.UUID(), string(role))
	return mapError(err, "revoke role")
}

// Roles returns a user's roles.
func (r *UserRepo) Roles(ctx context.Context, userID id.UserID) ([]security.Role, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	return r.rolesFor(ctx, tenant, userID)
}

func (r *UserRepo) rolesFor(ctx context.Context, tenant id.TenantID, userID id.UserID) ([]security.Role, error) {
	rows, err := r.s.db(ctx).Query(ctx, sqlRolesForUser, tenant.UUID(), userID.UUID())
	if err != nil {
		return nil, mapError(err, "read roles")
	}
	defer rows.Close()

	var out []security.Role
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, mapError(err, "scan role")
		}
		// A role this build does not understand is dropped rather than carried.
		// It cannot be checked, and carrying it would let a future rename
		// silently grant access.
		if r := security.Role(role); r.Valid() {
			out = append(out, r)
		}
	}
	return out, mapError(rows.Err(), "read roles")
}

func scanUser(row scanner) (identity.User, error) {
	var (
		tenantUUID uuid.UUID
		userUUID   uuid.UUID
		verifier   *string
		status     string
		u          identity.User
	)
	err := row.Scan(&tenantUUID, &userUUID, &u.Email, &u.DisplayName, &verifier, &status, &u.CreatedAt)
	if err != nil {
		return identity.User{}, mapError(err, "scan user")
	}
	u.TenantID = id.NewTenantID(tenantUUID)
	u.ID = id.NewUserID(userUUID)
	u.Status = identity.UserStatus(status)
	if verifier != nil {
		// A stored verifier this build cannot parse is treated as no verifier:
		// the account cannot sign in with a password, which fails closed. It is
		// not an error that would take the whole sign-in path down.
		if v, err := identity.ParseVerifier(*verifier); err == nil {
			u.Verifier = v
		}
	}
	return u, nil
}

// ---------------------------------------------------------------------------
// session
// ---------------------------------------------------------------------------

const (
	sqlSessionInsert = `
		INSERT INTO ztax.session (
			tenant_id, session_id, user_id, token_digest,
			created_at, idle_expires_at, absolute_expires_at, user_agent, client_ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	sqlSessionColumns = `tenant_id, session_id, user_id, token_digest,
		created_at, idle_expires_at, absolute_expires_at, revoked_at, revoked_reason, user_agent, host(client_ip)`

	// No tenant predicate, and there cannot be one: the caller has a cookie and
	// nothing else. token_digest is unique across the cell, so this resolves to
	// exactly one session or to none, and the tenant is read from the row.
	sqlSessionByDigest = `SELECT ` + sqlSessionColumns + ` FROM ztax.session WHERE token_digest = $1`

	sqlSessionList = `SELECT ` + sqlSessionColumns + `
		FROM ztax.session WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2`

	sqlSessionSlide = `
		UPDATE ztax.session SET idle_expires_at = $3
		WHERE tenant_id = $1 AND session_id = $2 AND revoked_at IS NULL`

	sqlSessionRevoke = `
		UPDATE ztax.session SET revoked_at = $3, revoked_reason = $4
		WHERE tenant_id = $1 AND session_id = $2 AND revoked_at IS NULL`

	sqlSessionRevokeForUser = `
		UPDATE ztax.session SET revoked_at = $3, revoked_reason = $4
		WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL`

	// Expired sessions are deleted rather than kept. They are operational
	// state, not evidence: the audit trail of who signed in and when lives in
	// admin_audit, which is append-only and is where an access review looks.
	sqlSessionDeleteExpired = `DELETE FROM ztax.session WHERE absolute_expires_at < $1`
)

// SessionRepo implements port.SessionRepository.
type SessionRepo struct{ s *Store }

// Sessions returns the session repository.
func (s *Store) Sessions() *SessionRepo { return &SessionRepo{s: s} }

var _ port.SessionRepository = (*SessionRepo)(nil)

// Create inserts a session.
func (r *SessionRepo) Create(ctx context.Context, s identity.Session) error {
	var ip *string
	if s.ClientIP != "" {
		ip = &s.ClientIP
	}
	_, err := r.s.db(ctx).Exec(ctx, sqlSessionInsert,
		s.TenantID.UUID(), s.ID.UUID(), s.UserID.UUID(), s.TokenDigest,
		s.CreatedAt, s.IdleExpiresAt, s.AbsoluteExpiresAt, s.UserAgent, ip)
	return mapError(err, "insert session")
}

// ByTokenDigest resolves a cookie to a session.
func (r *SessionRepo) ByTokenDigest(ctx context.Context, digest []byte) (identity.Session, error) {
	return scanSession(r.s.db(ctx).QueryRow(ctx, sqlSessionByDigest, digest))
}

// ListForTenant returns the tenant's sessions, newest first.
func (r *SessionRepo) ListForTenant(ctx context.Context, limit int) ([]identity.Session, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlSessionList, tenant.UUID(), capLimit(limit))
	if err != nil {
		return nil, mapError(err, "list sessions")
	}
	defer rows.Close()

	var out []identity.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, mapError(rows.Err(), "list sessions")
}

// Slide extends the idle window.
func (r *SessionRepo) Slide(ctx context.Context, sessionID id.SessionID, idleExpiresAt time.Time) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlSessionSlide, tenant.UUID(), sessionID.UUID(), idleExpiresAt)
	return mapError(err, "slide session")
}

// Revoke ends one session.
func (r *SessionRepo) Revoke(ctx context.Context, sessionID id.SessionID, reason string, at time.Time) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.s.db(ctx).Exec(ctx, sqlSessionRevoke, tenant.UUID(), sessionID.UUID(), at, reason)
	return mapError(err, "revoke session")
}

// RevokeAllForUser ends every live session a user holds, and reports how many.
// This is what runs when an account is disabled or a password changes — a
// disabled account with a live session is not disabled.
func (r *SessionRepo) RevokeAllForUser(ctx context.Context, userID id.UserID, reason string, at time.Time) (int, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return 0, err
	}
	tag, err := r.s.db(ctx).Exec(ctx, sqlSessionRevokeForUser, tenant.UUID(), userID.UUID(), at, reason)
	if err != nil {
		return 0, mapError(err, "revoke user sessions")
	}
	return int(tag.RowsAffected()), nil
}

// DeleteExpired removes sessions past their absolute limit. It runs as a
// background sweep and is not tenant-scoped, because it is the platform
// reclaiming its own state rather than anyone reading anyone's data.
func (r *SessionRepo) DeleteExpired(ctx context.Context, before time.Time) (int, error) {
	tag, err := r.s.db(ctx).Exec(ctx, sqlSessionDeleteExpired, before)
	if err != nil {
		return 0, mapError(err, "delete expired sessions")
	}
	return int(tag.RowsAffected()), nil
}

func scanSession(row scanner) (identity.Session, error) {
	var (
		tenantUUID  uuid.UUID
		sessionUUID uuid.UUID
		userUUID    uuid.UUID
		revokedAt   *time.Time
		reason      *string
		clientIP    *string
		s           identity.Session
	)
	err := row.Scan(&tenantUUID, &sessionUUID, &userUUID, &s.TokenDigest,
		&s.CreatedAt, &s.IdleExpiresAt, &s.AbsoluteExpiresAt,
		&revokedAt, &reason, &s.UserAgent, &clientIP)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// A cookie that matches nothing is an authentication failure, not a
			// missing resource: the caller asked to be authenticated and was
			// not, and a 404 here would leak that a digest is unknown.
			return identity.Session{}, errs.Wrap(err, errs.CategoryPolicy, errs.ReasonUnauthenticated,
				"The request carried no valid session. Sign in and retry.")
		}
		return identity.Session{}, mapError(err, "scan session")
	}
	s.TenantID = id.NewTenantID(tenantUUID)
	s.ID = id.NewSessionID(sessionUUID)
	s.UserID = id.NewUserID(userUUID)
	s.RevokedAt = revokedAt
	if reason != nil {
		s.RevokedReason = *reason
	}
	if clientIP != nil {
		s.ClientIP = *clientIP
	}
	return s, nil
}

// ---------------------------------------------------------------------------
// audit
// ---------------------------------------------------------------------------

const (
	sqlAuditInsert = `
		INSERT INTO ztax.admin_audit (tenant_id, audit_id, actor_user_id, action, subject_type, subject_id, detail, recorded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	sqlAuditColumns = `tenant_id, audit_id, actor_user_id, action, subject_type, subject_id, detail, recorded_at`

	sqlAuditList = `SELECT ` + sqlAuditColumns + `
		FROM ztax.admin_audit WHERE tenant_id = $1 ORDER BY recorded_at DESC LIMIT $2`

	sqlAuditListForSubject = `SELECT ` + sqlAuditColumns + `
		FROM ztax.admin_audit
		WHERE tenant_id = $1 AND subject_type = $2 AND subject_id = $3
		ORDER BY recorded_at DESC LIMIT $4`
)

// AuditRepo implements port.AuditRepository.
type AuditRepo struct{ s *Store }

// Audit returns the audit repository.
func (s *Store) Audit() *AuditRepo { return &AuditRepo{s: s} }

var _ port.AuditRepository = (*AuditRepo)(nil)

// Append records an administrative action.
func (r *AuditRepo) Append(ctx context.Context, rec port.AuditRecord) error {
	var actor *uuid.UUID
	if rec.ActorUserID != nil {
		u := rec.ActorUserID.UUID()
		actor = &u
	}
	detail := rec.Detail
	if len(detail) == 0 {
		detail = []byte("{}")
	}
	_, err := r.s.db(ctx).Exec(ctx, sqlAuditInsert,
		rec.TenantID.UUID(), rec.ID.UUID(), actor, rec.Action,
		rec.SubjectType, rec.SubjectID, detail, rec.RecordedAt)
	return mapError(err, "append audit")
}

// List returns the tenant's audit trail, newest first.
func (r *AuditRepo) List(ctx context.Context, limit int) ([]port.AuditRecord, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlAuditList, tenant.UUID(), capLimit(limit))
	if err != nil {
		return nil, mapError(err, "list audit")
	}
	return collectAudit(rows)
}

// ListForSubject returns the audit trail for one subject.
func (r *AuditRepo) ListForSubject(ctx context.Context, subjectType, subjectID string, limit int) ([]port.AuditRecord, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.s.db(ctx).Query(ctx, sqlAuditListForSubject,
		tenant.UUID(), subjectType, subjectID, capLimit(limit))
	if err != nil {
		return nil, mapError(err, "list audit for subject")
	}
	return collectAudit(rows)
}

func collectAudit(rows pgx.Rows) ([]port.AuditRecord, error) {
	defer rows.Close()
	var out []port.AuditRecord
	for rows.Next() {
		var (
			tenantUUID uuid.UUID
			auditUUID  uuid.UUID
			actor      *uuid.UUID
			rec        port.AuditRecord
		)
		if err := rows.Scan(&tenantUUID, &auditUUID, &actor, &rec.Action,
			&rec.SubjectType, &rec.SubjectID, &rec.Detail, &rec.RecordedAt); err != nil {
			return nil, mapError(err, "scan audit")
		}
		rec.TenantID = id.NewTenantID(tenantUUID)
		rec.ID = id.NewAuditID(auditUUID)
		if actor != nil {
			a := id.NewUserID(*actor)
			rec.ActorUserID = &a
		}
		out = append(out, rec)
	}
	return out, mapError(rows.Err(), "list audit")
}

// capLimit bounds a page size. An unbounded LIMIT is a way to ask the cell to
// materialise a tenant's entire history into one response, so the ceiling is
// applied here rather than trusted from the transport layer.
func capLimit(n int) int {
	const maxPage = 500
	if n <= 0 || n > maxPage {
		return maxPage
	}
	return n
}
