package app

import (
	"context"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// AdminService administers a tenant: its users, their roles and their sessions.
//
// Every method here requires RoleAdmin and writes an audit record in the same
// transaction as the change. The pairing is the design: an access change with
// no audit record is what an access review cannot reconstruct, and making them
// one transaction means the code cannot drift into doing one without the other.
type AdminService struct {
	tenants  port.TenantRepository
	users    port.UserRepository
	sessions port.SessionRepository
	audit    port.AuditRepository
	tx       port.TxManager
	clock    clock.Clock
	ids      idgen.Generator
	// region is the cell's own ZTAX_REGION. A tenant created here is resident
	// here, and nothing may create one claiming to live somewhere else
	// (ADR-0017 §2.10).
	region string
}

// NewAdminService wires the service.
func NewAdminService(
	tenants port.TenantRepository,
	users port.UserRepository,
	sessions port.SessionRepository,
	audit port.AuditRepository,
	tx port.TxManager,
	clk clock.Clock,
	ids idgen.Generator,
	region string,
) *AdminService {
	return &AdminService{tenants: tenants, users: users, sessions: sessions, audit: audit, tx: tx, clock: clk, ids: ids, region: region}
}

// requireAdmin is the authorization check every method below starts with.
func requireAdmin(ctx context.Context) (security.Context, error) {
	sc, ok := security.From(ctx)
	if !ok || !sc.Authenticated() {
		return security.Context{}, errs.New(errs.CategoryPolicy, errs.ReasonUnauthenticated,
			"The request carried no valid session. Sign in and retry.")
	}
	if !sc.HasRole(security.RoleAdmin) {
		return security.Context{}, errs.New(errs.CategoryPolicy, errs.ReasonForbidden,
			"The authenticated subject does not hold a role permitting this action.")
	}
	return sc, nil
}

// CreateUserInput describes a user to invite.
type CreateUserInput struct {
	Email       string
	DisplayName string
	Roles       []security.Role
	// Password may be empty, which creates an INVITED user who exists, holds
	// roles and appears in the admin list, but cannot sign in until a
	// credential is set.
	Password string
}

// CreateUser adds a user to the caller's tenant.
func (s *AdminService) CreateUser(ctx context.Context, in CreateUserInput) (identity.User, error) {
	sc, err := requireAdmin(ctx)
	if err != nil {
		return identity.User{}, err
	}
	if err := identity.ValidateEmail(in.Email); err != nil {
		return identity.User{}, errs.Invalid("email", errs.ReasonInvalidValue, "That is not a valid email address.")
	}
	if in.DisplayName == "" {
		return identity.User{}, errs.Invalid("displayName", errs.ReasonMissingField, "A display name is required.")
	}
	for _, r := range in.Roles {
		if !r.Valid() {
			return identity.User{}, errs.Invalid("roles", errs.ReasonInvalidValue, "That role is not one this service recognises.")
		}
	}

	user := identity.User{
		TenantID:    sc.Tenant(),
		Email:       identity.NormalizeEmail(in.Email),
		DisplayName: in.DisplayName,
		Status:      identity.UserInvited,
		CreatedAt:   s.clock.Now(),
		Roles:       in.Roles,
	}
	if in.Password != "" {
		if err := identity.ValidatePassword(in.Password); err != nil {
			return identity.User{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonPasswordTooWeak,
				"The password does not meet the minimum length policy.")
		}
		v, err := identity.NewVerifier(in.Password)
		if err != nil {
			return identity.User{}, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "The user could not be created.")
		}
		user.Verifier = v
		user.Status = identity.UserActive
	}

	userID, err := idgen.UserID(s.ids)
	if err != nil {
		return identity.User{}, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "The user could not be created.")
	}
	user.ID = userID

	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return identity.User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.users.Create(txCtx, user); err != nil {
		return identity.User{}, err
	}
	for _, r := range in.Roles {
		if err := s.users.GrantRole(txCtx, user.ID, r, sc.Subject(), user.CreatedAt); err != nil {
			return identity.User{}, err
		}
	}
	if err := s.append(txCtx, sc, "USER_CREATED", "user", user.ID.String(), user.CreatedAt,
		canonical.F("email", canonical.String(user.Email)),
		canonical.F("status", canonical.String(string(user.Status))),
		canonical.F("roles", rolesValue(in.Roles)),
	); err != nil {
		return identity.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return identity.User{}, err
	}
	return user, nil
}

// ListUsers returns the tenant's users.
//
// It permits ANALYST and AUDITOR as well as ADMIN: seeing who has access is a
// read an auditor is specifically there to perform.
func (s *AdminService) ListUsers(ctx context.Context, limit int) ([]identity.User, error) {
	sc, ok := security.From(ctx)
	if !ok || !sc.Authenticated() {
		return nil, errs.New(errs.CategoryPolicy, errs.ReasonUnauthenticated,
			"The request carried no valid session. Sign in and retry.")
	}
	if !sc.HasAny(security.RoleAdmin, security.RoleAnalyst, security.RoleAuditor) {
		return nil, errs.New(errs.CategoryPolicy, errs.ReasonForbidden,
			"The authenticated subject does not hold a role permitting this action.")
	}
	return s.users.List(ctx, limit)
}

// SetUserStatus enables or disables a user.
//
// Disabling revokes every live session the user holds, in the same transaction.
// Without that, a disabled account keeps working until its session expires,
// which is not what "disabled" means to the person who clicked it.
func (s *AdminService) SetUserStatus(ctx context.Context, userID id.UserID, status identity.UserStatus) error {
	sc, err := requireAdmin(ctx)
	if err != nil {
		return err
	}
	switch status {
	case identity.UserActive, identity.UserInvited, identity.UserDisabled:
	default:
		return errs.Invalid("status", errs.ReasonInvalidValue, "That status is not one this service recognises.")
	}
	// An administrator disabling their own account locks the tenant out if they
	// are the last one. Refusing self-disable is the cheap half of that
	// problem; the last-admin check below is the other half.
	if status == identity.UserDisabled && userID == sc.Subject() {
		return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"An administrator cannot disable their own account.")
	}
	if status == identity.UserDisabled {
		if err := s.refuseIfLastAdmin(ctx, userID); err != nil {
			return err
		}
	}

	now := s.clock.Now()
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.users.SetStatus(txCtx, userID, status); err != nil {
		return err
	}
	revoked := 0
	if status == identity.UserDisabled {
		if revoked, err = s.sessions.RevokeAllForUser(txCtx, userID, "user disabled", now); err != nil {
			return err
		}
	}
	if err := s.append(txCtx, sc, "USER_STATUS_CHANGED", "user", userID.String(), now,
		canonical.F("status", canonical.String(string(status))),
		canonical.F("sessionsRevoked", canonical.Integer(int64(revoked))),
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GrantRole adds a role to a user.
func (s *AdminService) GrantRole(ctx context.Context, userID id.UserID, role security.Role) error {
	sc, err := requireAdmin(ctx)
	if err != nil {
		return err
	}
	if !role.Valid() {
		return errs.Invalid("role", errs.ReasonInvalidValue, "That role is not one this service recognises.")
	}
	now := s.clock.Now()

	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.users.GrantRole(txCtx, userID, role, sc.Subject(), now); err != nil {
		return err
	}
	if err := s.append(txCtx, sc, "ROLE_GRANTED", "user", userID.String(), now,
		canonical.F("role", canonical.String(string(role))),
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RevokeRole removes a role from a user.
//
// Revoking a role does not revoke the user's sessions. A session's roles are
// read from the database on every request (AuthService.Authenticate), so the
// change takes effect on the next request without anyone being signed out.
func (s *AdminService) RevokeRole(ctx context.Context, userID id.UserID, role security.Role) error {
	sc, err := requireAdmin(ctx)
	if err != nil {
		return err
	}
	if role == security.RoleAdmin {
		if err := s.refuseIfLastAdmin(ctx, userID); err != nil {
			return err
		}
	}
	now := s.clock.Now()

	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.users.RevokeRole(txCtx, userID, role); err != nil {
		return err
	}
	if err := s.append(txCtx, sc, "ROLE_REVOKED", "user", userID.String(), now,
		canonical.F("role", canonical.String(string(role))),
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// refuseIfLastAdmin stops the tenant being locked out of its own administration.
//
// Without this, revoking the last ADMIN role leaves a tenant that can still use
// the product and can never again add a user — recoverable only by an operator
// with database access, which is exactly the kind of support path ADR-0009 §2.6
// says should not need to exist.
func (s *AdminService) refuseIfLastAdmin(ctx context.Context, userID id.UserID) error {
	users, err := s.users.List(ctx, 0)
	if err != nil {
		return err
	}
	others := 0
	for _, u := range users {
		if u.ID == userID || u.Status == identity.UserDisabled {
			continue
		}
		for _, r := range u.Roles {
			if r == security.RoleAdmin {
				others++
				break
			}
		}
	}
	if others == 0 {
		return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"This is the tenant's last administrator. Grant the role to another user first.")
	}
	return nil
}

// ListSessions returns the tenant's sessions, so an administrator can see who
// is signed in and end a session they do not recognise.
func (s *AdminService) ListSessions(ctx context.Context, limit int) ([]identity.Session, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	return s.sessions.ListForTenant(ctx, limit)
}

// RevokeSession ends one session.
func (s *AdminService) RevokeSession(ctx context.Context, sessionID id.SessionID) error {
	sc, err := requireAdmin(ctx)
	if err != nil {
		return err
	}
	now := s.clock.Now()

	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.sessions.Revoke(txCtx, sessionID, "revoked by administrator", now); err != nil {
		return err
	}
	if err := s.append(txCtx, sc, "SESSION_REVOKED", "session", sessionID.String(), now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListAudit returns the tenant's administrative audit trail.
func (s *AdminService) ListAudit(ctx context.Context, limit int) ([]port.AuditRecord, error) {
	sc, ok := security.From(ctx)
	if !ok || !sc.Authenticated() {
		return nil, errs.New(errs.CategoryPolicy, errs.ReasonUnauthenticated,
			"The request carried no valid session. Sign in and retry.")
	}
	// AUDITOR reads this by definition; ADMIN reads it to review their own
	// tenant. Nobody else, because it names who did what.
	if !sc.HasAny(security.RoleAdmin, security.RoleAuditor) {
		return nil, errs.New(errs.CategoryPolicy, errs.ReasonForbidden,
			"The authenticated subject does not hold a role permitting this action.")
	}
	return s.audit.List(ctx, limit)
}

// CurrentTenant returns the caller's tenant.
func (s *AdminService) CurrentTenant(ctx context.Context) (identity.Tenant, error) {
	sc, ok := security.From(ctx)
	if !ok || !sc.Authenticated() {
		return identity.Tenant{}, errs.New(errs.CategoryPolicy, errs.ReasonUnauthenticated,
			"The request carried no valid session. Sign in and retry.")
	}
	return s.tenants.ByID(ctx, sc.Tenant())
}

// ProvisionTenantInput describes a tenant and its first administrator.
type ProvisionTenantInput struct {
	Slug        string
	DisplayName string
	AdminEmail  string
	AdminName   string
	AdminPasswordProvider
}

// AdminPasswordProvider supplies the first administrator's password. It is an
// embedded interface rather than a string field so that a caller has to think
// about where the value came from — the bootstrap path reads it from the
// environment once at startup, and nothing else may call this at all.
type AdminPasswordProvider interface {
	AdminPassword() string
}

// StaticPassword is the bootstrap implementation.
type StaticPassword string

// AdminPassword returns the password.
func (p StaticPassword) AdminPassword() string { return string(p) }

// ProvisionTenant creates a tenant and its first administrator.
//
// This is the bootstrap path and it is deliberately not reachable over HTTP.
// Tenant creation is an operator act with a residency decision attached
// (ADR-0009 §2.6), and an endpoint that creates tenants is an endpoint that
// creates residency obligations from an unauthenticated request.
func (s *AdminService) ProvisionTenant(ctx context.Context, in ProvisionTenantInput) (identity.Tenant, identity.User, error) {
	if err := identity.ValidateSlug(in.Slug); err != nil {
		return identity.Tenant{}, identity.User{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
			"The tenant handle may hold only lowercase letters, digits and hyphens.")
	}
	if err := identity.ValidateEmail(in.AdminEmail); err != nil {
		return identity.Tenant{}, identity.User{}, errs.Invalid("adminEmail", errs.ReasonInvalidValue,
			"That is not a valid email address.")
	}
	password := in.AdminPassword()
	if err := identity.ValidatePassword(password); err != nil {
		return identity.Tenant{}, identity.User{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonPasswordTooWeak,
			"The password does not meet the minimum length policy.")
	}

	now := s.clock.Now()
	tenantID, err := idgen.TenantID(s.ids)
	if err != nil {
		return identity.Tenant{}, identity.User{}, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "The tenant could not be created.")
	}
	userID, err := idgen.UserID(s.ids)
	if err != nil {
		return identity.Tenant{}, identity.User{}, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "The tenant could not be created.")
	}
	verifier, err := identity.NewVerifier(password)
	if err != nil {
		return identity.Tenant{}, identity.User{}, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "The tenant could not be created.")
	}

	tenant := identity.Tenant{
		ID:              tenantID,
		Slug:            in.Slug,
		DisplayName:     in.DisplayName,
		ResidencyRegion: s.region,
		Status:          identity.TenantActive,
		CreatedAt:       now,
	}
	admin := identity.User{
		TenantID:    tenantID,
		ID:          userID,
		Email:       identity.NormalizeEmail(in.AdminEmail),
		DisplayName: in.AdminName,
		Verifier:    verifier,
		Status:      identity.UserActive,
		Roles:       []security.Role{security.RoleAdmin},
		CreatedAt:   now,
	}

	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return identity.Tenant{}, identity.User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txCtx = security.Into(txCtx, security.System(tenantID))

	if err := s.tenants.Create(txCtx, tenant); err != nil {
		return identity.Tenant{}, identity.User{}, err
	}
	if err := s.users.Create(txCtx, admin); err != nil {
		return identity.Tenant{}, identity.User{}, err
	}
	if err := s.users.GrantRole(txCtx, admin.ID, security.RoleAdmin, admin.ID, now); err != nil {
		return identity.Tenant{}, identity.User{}, err
	}

	auditID, err := idgen.AuditID(s.ids)
	if err != nil {
		return identity.Tenant{}, identity.User{}, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "The tenant could not be created.")
	}
	detail, err := canonical.Encode(canonical.Object(
		canonical.F("slug", canonical.String(tenant.Slug)),
		canonical.F("residencyRegion", canonical.String(tenant.ResidencyRegion)),
		canonical.F("firstAdmin", canonical.String(admin.ID.String())),
	))
	if err != nil {
		return identity.Tenant{}, identity.User{}, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "The tenant could not be created.")
	}
	// actor is nil: this was an operator act, not a user's. A reviewer should
	// see that distinction rather than a tenant appearing to have created
	// itself.
	if err := s.audit.Append(txCtx, port.AuditRecord{
		ID: auditID, TenantID: tenantID, Action: "TENANT_PROVISIONED",
		SubjectType: "tenant", SubjectID: tenant.Slug, Detail: detail, RecordedAt: now,
	}); err != nil {
		return identity.Tenant{}, identity.User{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return identity.Tenant{}, identity.User{}, err
	}
	return tenant, admin, nil
}

func (s *AdminService) append(ctx context.Context, sc security.Context, action, subjectType, subjectID string, now time.Time, detail ...canonical.Field) error {
	auditID, err := idgen.AuditID(s.ids)
	if err != nil {
		return errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "The action could not be recorded.")
	}
	body, err := canonical.Encode(canonical.Object(detail...))
	if err != nil {
		return errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "The action could not be recorded.")
	}
	actor := sc.Subject()
	return s.audit.Append(ctx, port.AuditRecord{
		ID:          auditID,
		TenantID:    sc.Tenant(),
		ActorUserID: &actor,
		Action:      action,
		SubjectType: subjectType,
		SubjectID:   subjectID,
		Detail:      body,
		RecordedAt:  now,
	})
}

func rolesValue(roles []security.Role) canonical.Value {
	if len(roles) == 0 {
		return canonical.Absent()
	}
	items := make([]canonical.Value, 0, len(roles))
	for _, r := range roles {
		items = append(items, canonical.String(string(r)))
	}
	return canonical.Array(items...)
}
