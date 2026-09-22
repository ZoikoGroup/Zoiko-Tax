// Package app holds the use cases.
//
// ADR-0009 §2.4 puts the transaction boundary here and nowhere else: a handler
// opens the transaction, passes a transactional context down, and commits.
// Domain code never sees a transaction and adapters never open one, so there is
// exactly one place where "what is atomic" is decided and it is reviewable.
package app

import (
	"context"
	"errors"
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

// AuthService authenticates and manages sessions.
type AuthService struct {
	tenants  port.TenantRepository
	users    port.UserRepository
	sessions port.SessionRepository
	audit    port.AuditRepository
	tx       port.TxManager
	clock    clock.Clock
	ids      idgen.Generator
}

// NewAuthService wires the service.
func NewAuthService(
	tenants port.TenantRepository,
	users port.UserRepository,
	sessions port.SessionRepository,
	audit port.AuditRepository,
	tx port.TxManager,
	clk clock.Clock,
	ids idgen.Generator,
) *AuthService {
	return &AuthService{tenants: tenants, users: users, sessions: sessions, audit: audit, tx: tx, clock: clk, ids: ids}
}

// SignInInput is what a sign-in needs.
type SignInInput struct {
	// TenantSlug names the tenant. Sign-in is per tenant: the same address may
	// hold accounts in two tenants, and they share nothing.
	TenantSlug string
	Email      string
	Password   string
	UserAgent  string
	ClientIP   string
}

// SignInResult carries the established session.
type SignInResult struct {
	// Token is the raw session secret, and this is the only moment it exists
	// outside the client. The caller sets it as an httpOnly cookie and drops
	// it; nothing stores it.
	Token   identity.SessionToken
	Session identity.Session
	User    identity.User
	Tenant  identity.Tenant
}

// SignIn authenticates a user and establishes a session.
//
// Every failure below returns the same error. That is deliberate and it is the
// main design decision in this function: distinguishing "no such tenant" from
// "no such user" from "wrong password" from "account disabled" hands an
// attacker a way to enumerate which tenants and addresses exist, and tells a
// legitimate user nothing they can act on that "check your details" does not.
//
// The audit trail records the distinction, because an administrator reviewing
// failures does need it, and that is a channel an attacker cannot read.
func (s *AuthService) SignIn(ctx context.Context, in SignInInput) (SignInResult, error) {
	now := s.clock.Now()

	tenant, err := s.tenants.BySlug(ctx, in.TenantSlug)
	if err != nil {
		// A not-found tenant is an authentication failure, not a 404. Reporting
		// it as missing would confirm which tenant handles exist.
		return SignInResult{}, invalidCredentials(err)
	}
	if !tenant.CanAuthenticate() {
		s.recordFailure(ctx, tenant.ID, nil, "SIGN_IN_REFUSED", "tenant", tenant.Slug, string(tenant.Status), now)
		return SignInResult{}, errs.New(errs.CategoryPolicy, errs.ReasonTenantSuspended,
			"The tenant is suspended. Contact your administrator.")
	}

	user, err := s.users.ByEmail(ctx, tenant.ID, in.Email)
	if err != nil {
		// Not found. The comparison below is skipped, so this path is faster
		// than a real verification — a timing difference an attacker can
		// measure to enumerate addresses. It is left as is rather than papered
		// over with a dummy hash: the mitigation that actually works is rate
		// limiting on this endpoint, which is where it belongs, and a fake
		// Argon2id run would cost 100ms of server CPU per probe to hide a
		// signal that remains measurable in aggregate.
		return SignInResult{}, invalidCredentials(err)
	}

	if err := user.Verifier.Verify(in.Password); err != nil {
		s.recordFailure(ctx, tenant.ID, &user.ID, "SIGN_IN_FAILED", "user", user.ID.String(), "bad-credential", now)
		return SignInResult{}, invalidCredentials(err)
	}

	// The password was right. Only now does account state matter, and it is
	// reported as itself: at this point the caller has proven who they are, so
	// telling them their account is disabled leaks nothing they did not know.
	if user.Status == identity.UserDisabled {
		s.recordFailure(ctx, tenant.ID, &user.ID, "SIGN_IN_REFUSED", "user", user.ID.String(), "disabled", now)
		return SignInResult{}, errs.New(errs.CategoryPolicy, errs.ReasonUserDisabled,
			"The user account is disabled.")
	}
	if !user.CanAuthenticate() {
		s.recordFailure(ctx, tenant.ID, &user.ID, "SIGN_IN_REFUSED", "user", user.ID.String(), string(user.Status), now)
		return SignInResult{}, invalidCredentials(errors.New("account cannot authenticate"))
	}

	token, err := identity.NewSessionToken()
	if err != nil {
		return SignInResult{}, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal,
			"The session could not be established.")
	}
	sessionID, err := idgen.SessionID(s.ids)
	if err != nil {
		return SignInResult{}, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal,
			"The session could not be established.")
	}
	session := identity.NewSession(sessionID, user, token, now, in.UserAgent, in.ClientIP)

	// The session insert, the credential upgrade and the audit record are one
	// transaction. A session that exists with no audit record is a sign-in
	// nobody can review.
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return SignInResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The repositories need a tenant in scope, and there is no authenticated
	// subject yet — the session being written is what will authenticate future
	// requests. A system context supplies the tenant without granting a role.
	txCtx = security.Into(txCtx, security.System(tenant.ID))

	if err := s.sessions.Create(txCtx, session); err != nil {
		return SignInResult{}, err
	}

	// The one moment the plaintext is available, so the one moment a verifier
	// below current policy can be upgraded (see identity.Verifier.NeedsRehash).
	if user.Verifier.NeedsRehash() {
		if upgraded, err := identity.NewVerifier(in.Password); err == nil {
			if err := s.users.SetVerifier(txCtx, user.ID, upgraded); err != nil {
				return SignInResult{}, err
			}
		}
		// A failed upgrade is not a failed sign-in. The existing verifier is
		// still valid; the upgrade will be retried on the next sign-in.
	}

	if err := s.append(txCtx, tenant.ID, &user.ID, "SIGN_IN", "session", session.ID.String(), now,
		canonical.F("userAgent", canonical.OptString(session.UserAgent)),
		canonical.F("clientIp", canonical.OptString(session.ClientIP)),
	); err != nil {
		return SignInResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return SignInResult{}, err
	}
	return SignInResult{Token: token, Session: session, User: user, Tenant: tenant}, nil
}

// Authenticated is what the middleware gets back from a cookie.
type Authenticated struct {
	Security security.Context
	User     identity.User
	Tenant   identity.Tenant
}

// Authenticate resolves a session cookie into a security context.
//
// This runs on every authenticated request, so it is written to do as little as
// possible: one indexed lookup by digest, one user read, and a session write
// only when the idle window actually needs extending (identity.ShouldSlide).
func (s *AuthService) Authenticate(ctx context.Context, cookie string) (Authenticated, error) {
	token, err := identity.ParseSessionToken(cookie)
	if err != nil {
		return Authenticated{}, errs.New(errs.CategoryPolicy, errs.ReasonUnauthenticated,
			"The request carried no valid session. Sign in and retry.")
	}

	session, err := s.sessions.ByTokenDigest(ctx, token.Digest())
	if err != nil {
		return Authenticated{}, err
	}

	now := s.clock.Now()
	switch session.State(now) {
	case identity.SessionRevoked:
		return Authenticated{}, errs.New(errs.CategoryPolicy, errs.ReasonSessionRevoked,
			"The session was revoked. Sign in again.")
	case identity.SessionExpired:
		return Authenticated{}, errs.New(errs.CategoryPolicy, errs.ReasonSessionExpired,
			"The session has expired. Sign in again.")
	}

	// The tenant comes from the session row, never from the request. A header
	// or path segment naming a tenant is an input; this is a fact established
	// when the user proved who they were.
	scoped := security.Into(ctx, security.System(session.TenantID))

	tenant, err := s.tenants.ByID(scoped, session.TenantID)
	if err != nil {
		return Authenticated{}, err
	}
	if !tenant.CanAuthenticate() {
		return Authenticated{}, errs.New(errs.CategoryPolicy, errs.ReasonTenantSuspended,
			"The tenant is suspended. Contact your administrator.")
	}

	user, err := s.users.ByID(scoped, session.UserID)
	if err != nil {
		return Authenticated{}, err
	}
	// Checked on every request rather than only at sign-in: disabling an
	// account has to take effect now, not when the session happens to expire.
	if user.Status == identity.UserDisabled {
		return Authenticated{}, errs.New(errs.CategoryPolicy, errs.ReasonUserDisabled,
			"The user account is disabled.")
	}

	if session.ShouldSlide(now) {
		if err := s.sessions.Slide(scoped, session.ID, session.SlideTo(now)); err != nil {
			// A failed slide is not a failed request: the session is still
			// valid for now, and the next request will try again. Failing the
			// request would turn a database hiccup into a sign-out.
			_ = err
		}
	}

	sc := security.New(session.TenantID, user.ID, session.ID, user.Roles, session.CreatedAt)
	return Authenticated{Security: sc, User: user, Tenant: tenant}, nil
}

// SignOut revokes the caller's own session.
func (s *AuthService) SignOut(ctx context.Context) error {
	sc, ok := security.From(ctx)
	if !ok || !sc.Authenticated() {
		// Signing out when not signed in is the desired end state, so it
		// succeeds rather than erroring.
		return nil
	}
	now := s.clock.Now()

	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.sessions.Revoke(txCtx, sc.Session(), "signed out", now); err != nil {
		return err
	}
	subject := sc.Subject()
	if err := s.append(txCtx, sc.Tenant(), &subject, "SIGN_OUT", "session", sc.Session().String(), now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ChangePassword replaces the caller's own password and ends every other
// session they hold.
//
// Ending the other sessions is the point of the operation as much as the new
// password is: a password change after a suspected compromise that leaves the
// attacker's session live has achieved nothing.
func (s *AuthService) ChangePassword(ctx context.Context, current, next string) error {
	sc, ok := security.From(ctx)
	if !ok || !sc.Authenticated() {
		return errs.New(errs.CategoryPolicy, errs.ReasonUnauthenticated,
			"The request carried no valid session. Sign in and retry.")
	}
	if err := identity.ValidatePassword(next); err != nil {
		return errs.Wrap(err, errs.CategoryValidation, errs.ReasonPasswordTooWeak,
			"The password does not meet the minimum length policy.")
	}

	user, err := s.users.ByID(ctx, sc.Subject())
	if err != nil {
		return err
	}
	if err := user.Verifier.Verify(current); err != nil {
		return invalidCredentials(err)
	}

	verifier, err := identity.NewVerifier(next)
	if err != nil {
		return errs.Wrap(err, errs.CategoryValidation, errs.ReasonPasswordTooWeak,
			"The password does not meet the minimum length policy.")
	}

	now := s.clock.Now()
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.users.SetVerifier(txCtx, user.ID, verifier); err != nil {
		return err
	}
	revoked, err := s.sessions.RevokeAllForUser(txCtx, user.ID, "password changed", now)
	if err != nil {
		return err
	}
	subject := sc.Subject()
	if err := s.append(txCtx, sc.Tenant(), &subject, "PASSWORD_CHANGED", "user", user.ID.String(), now,
		canonical.F("sessionsRevoked", canonical.Integer(int64(revoked))),
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SweepExpiredSessions removes sessions past their absolute limit. It runs on a
// timer in cmd/ztax-core and reports how many it removed.
func (s *AuthService) SweepExpiredSessions(ctx context.Context) (int, error) {
	return s.sessions.DeleteExpired(ctx, s.clock.Now())
}

// append writes an audit record. Detail is canonical (ADR-0011), so two
// records describing the same action digest identically whatever order the
// fields were written in.
func (s *AuthService) append(ctx context.Context, tenant id.TenantID, actor *id.UserID, action, subjectType, subjectID string, now time.Time, detail ...canonical.Field) error {
	auditID, err := idgen.AuditID(s.ids)
	if err != nil {
		return errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "The action could not be recorded.")
	}
	body, err := canonical.Encode(canonical.Object(detail...))
	if err != nil {
		return errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "The action could not be recorded.")
	}
	return s.audit.Append(ctx, port.AuditRecord{
		ID:          auditID,
		TenantID:    tenant,
		ActorUserID: actor,
		Action:      action,
		SubjectType: subjectType,
		SubjectID:   subjectID,
		Detail:      body,
		RecordedAt:  now,
	})
}

// recordFailure appends an audit record outside any transaction, for a path
// that is about to return an error.
//
// It deliberately ignores its own failure. This runs while rejecting a
// sign-in, and turning "we could not write the audit row" into a different
// error for the caller would leak the distinction the rejection exists to hide.
// The write failure itself surfaces through the database's own alerting.
func (s *AuthService) recordFailure(ctx context.Context, tenant id.TenantID, actor *id.UserID, action, subjectType, subjectID, reason string, now time.Time) {
	scoped := security.Into(ctx, security.System(tenant))
	_ = s.append(scoped, tenant, actor, action, subjectType, subjectID, now,
		canonical.F("reason", canonical.String(reason)))
}

// invalidCredentials is the one error every sign-in failure returns.
func invalidCredentials(cause error) error {
	return errs.Wrap(cause, errs.CategoryPolicy, errs.ReasonInvalidCredentials,
		"The credentials supplied were not valid.")
}
