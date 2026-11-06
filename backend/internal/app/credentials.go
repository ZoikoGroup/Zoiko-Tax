package app

import (
	"context"
	"fmt"
	"net/url"
	"strings"
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

// CredentialService issues and redeems the one-time links that set a
// password: an invitation for a user who has never had one, a reset for a
// user who has forgotten theirs.
//
// A link is a credential, so it is treated like a session: 256 random bits,
// only the digest stored, one use, a short life, and every earlier link for
// the same purpose spent the moment a new one is issued.
type CredentialService struct {
	tenants  port.TenantRepository
	users    port.UserRepository
	sessions port.SessionRepository
	tokens   port.CredentialTokenRepository
	audit    port.AuditRepository
	tx       port.TxManager
	clock    clock.Clock
	ids      idgen.Generator
	mailer   port.Mailer
	// publicURL is where people open the app; every link starts there.
	publicURL string
}

// NewCredentialService wires the service. mailer may not be nil: a cell that
// sends no mail has no CredentialService, and its routes answer 503.
func NewCredentialService(
	tenants port.TenantRepository,
	users port.UserRepository,
	sessions port.SessionRepository,
	tokens port.CredentialTokenRepository,
	audit port.AuditRepository,
	tx port.TxManager,
	clk clock.Clock,
	ids idgen.Generator,
	mailer port.Mailer,
	publicURL string,
) *CredentialService {
	return &CredentialService{
		tenants: tenants, users: users, sessions: sessions, tokens: tokens, audit: audit,
		tx: tx, clock: clk, ids: ids, mailer: mailer, publicURL: strings.TrimRight(publicURL, "/"),
	}
}

// Invite emails a set-password link to a user who has no password yet. It is
// also how an administrator re-sends one: the new link spends the old.
//
// The send is synchronous, unlike a reset: the administrator asked for this
// message by name and needs to know whether it went.
func (s *CredentialService) Invite(ctx context.Context, userID id.UserID) error {
	sc, err := requireAdmin(ctx)
	if err != nil {
		return err
	}
	user, err := s.users.ByID(ctx, userID)
	if err != nil {
		return err
	}
	if user.Status != identity.UserInvited {
		return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			"Only an invited user who has not yet set a password can be sent an invitation.")
	}
	tenant, err := s.tenants.ByID(ctx, sc.Tenant())
	if err != nil {
		return err
	}
	actor := sc.Subject()
	msg, err := s.issue(ctx, tenant, user, identity.TokenInvite, &actor)
	if err != nil {
		return err
	}
	if err := s.mailer.Send(ctx, msg); err != nil {
		return errs.Wrap(err, errs.CategoryUnavailable, errs.ReasonUnavailable,
			"The invitation could not be sent. Retry, or check the cell's mail configuration.")
	}
	return nil
}

// RequestReset prepares a reset link for the account at tenantSlug and email,
// and returns the message that carries it — or nil when there is nothing to
// send.
//
// Whether the account exists is not the caller's to learn, so every outcome
// looks the same from outside: no error, and the message delivered off the
// request path, so its round trip to the mail server does not time the answer
// either. An invited user who never set a password is sent a fresh invitation
// instead; a disabled user, or one in a suspended tenant, is sent nothing.
func (s *CredentialService) RequestReset(ctx context.Context, tenantSlug, email string) (*port.Message, error) {
	tenant, err := s.tenants.BySlug(ctx, tenantSlug)
	if err != nil || !tenant.CanAuthenticate() {
		// Not finding the tenant is an outcome here, not an error.
		return nil, nil
	}
	user, err := s.users.ByEmail(ctx, tenant.ID, email)
	if err != nil {
		return nil, nil
	}
	purpose := identity.TokenReset
	switch user.Status {
	case identity.UserActive:
	case identity.UserInvited:
		purpose = identity.TokenInvite
	default:
		s.recordRefusal(ctx, tenant.ID, user.ID, string(user.Status))
		return nil, nil
	}
	ctx = security.Into(ctx, security.System(tenant.ID))
	msg, err := s.issue(ctx, tenant, user, purpose, nil)
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

// Deliver sends a message RequestReset prepared.
func (s *CredentialService) Deliver(ctx context.Context, m port.Message) error {
	return s.mailer.Send(ctx, m)
}

// SetPasswordResult says where the new password signs in.
type SetPasswordResult struct {
	TenantSlug string
	Email      string
	// Welcome is the message to send when the link activated an invited
	// account, and nil for a reset: someone who has just reset a password
	// has not just joined.
	Welcome *port.Message
}

// SetPassword redeems a link: it sets the password, activates an invited
// account, spends the link and ends every session the user held — a reset
// after a compromise that leaves the intruder signed in has achieved nothing.
func (s *CredentialService) SetPassword(ctx context.Context, link, password string) (SetPasswordResult, error) {
	secret, err := identity.ParseSessionToken(link)
	if err != nil {
		return SetPasswordResult{}, invalidLink(err)
	}
	now := s.clock.Now()
	token, err := s.tokens.ByDigest(ctx, secret.Digest())
	if err != nil {
		return SetPasswordResult{}, err
	}
	if !token.Usable(now) {
		return SetPasswordResult{}, invalidLink(nil)
	}
	if err := identity.ValidatePassword(password); err != nil {
		return SetPasswordResult{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonPasswordTooWeak,
			"The password does not meet the minimum length policy.")
	}

	ctx = security.Into(ctx, security.System(token.TenantID))
	tenant, err := s.tenants.ByID(ctx, token.TenantID)
	if err != nil {
		return SetPasswordResult{}, err
	}
	if !tenant.CanAuthenticate() {
		return SetPasswordResult{}, errs.New(errs.CategoryPolicy, errs.ReasonTenantSuspended,
			"The tenant is suspended. Contact your administrator.")
	}
	user, err := s.users.ByID(ctx, token.UserID)
	if err != nil {
		return SetPasswordResult{}, err
	}
	if user.Status == identity.UserDisabled {
		return SetPasswordResult{}, errs.New(errs.CategoryPolicy, errs.ReasonUserDisabled,
			"The user account is disabled.")
	}
	verifier, err := identity.NewVerifier(password)
	if err != nil {
		return SetPasswordResult{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonPasswordTooWeak,
			"The password does not meet the minimum length policy.")
	}

	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return SetPasswordResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Spent first, and only once: of two requests racing with the same link,
	// the second finds nothing left to spend and sets nothing.
	spent, err := s.tokens.Spend(txCtx, token.Digest, now)
	if err != nil {
		return SetPasswordResult{}, err
	}
	if !spent {
		return SetPasswordResult{}, invalidLink(nil)
	}
	if err := s.users.SetVerifier(txCtx, user.ID, verifier); err != nil {
		return SetPasswordResult{}, err
	}
	if user.Status == identity.UserInvited {
		if err := s.users.SetStatus(txCtx, user.ID, identity.UserActive); err != nil {
			return SetPasswordResult{}, err
		}
	}
	revoked, err := s.sessions.RevokeAllForUser(txCtx, user.ID, "password set by link", now)
	if err != nil {
		return SetPasswordResult{}, err
	}
	if err := s.append(txCtx, tenant.ID, &user.ID, "PASSWORD_SET", user.ID, now,
		canonical.F("purpose", canonical.String(string(token.Purpose))),
		canonical.F("sessionsRevoked", canonical.Integer(int64(revoked))),
	); err != nil {
		return SetPasswordResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SetPasswordResult{}, err
	}
	result := SetPasswordResult{TenantSlug: tenant.Slug, Email: user.Email}
	if user.Status == identity.UserInvited {
		welcome := welcomeMessage(s.publicURL, tenant, user)
		result.Welcome = &welcome
	}
	return result, nil
}

// Welcome prepares the message that greets user, an account an administrator
// has just created with a password. It is returned rather than sent, so the
// caller can send it off the request path: the account exists whether or not
// the greeting arrives.
func (s *CredentialService) Welcome(ctx context.Context, user identity.User) (port.Message, error) {
	sc, err := requireAdmin(ctx)
	if err != nil {
		return port.Message{}, err
	}
	tenant, err := s.tenants.ByID(ctx, sc.Tenant())
	if err != nil {
		return port.Message{}, err
	}
	return welcomeMessage(s.publicURL, tenant, user), nil
}

// issue stores a fresh link for user and returns the message that carries it.
// The link is committed before anything is sent, so a message never carries a
// link the database does not know.
func (s *CredentialService) issue(ctx context.Context, tenant identity.Tenant, user identity.User, purpose identity.TokenPurpose, actor *id.UserID) (port.Message, error) {
	secret, err := identity.NewSessionToken()
	if err != nil {
		return port.Message{}, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal, "The link could not be issued.")
	}
	now := s.clock.Now()
	token := identity.NewCredentialToken(user, purpose, secret, now)

	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return port.Message{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.tokens.Issue(txCtx, token); err != nil {
		return port.Message{}, err
	}
	action := "PASSWORD_RESET_ISSUED"
	if purpose == identity.TokenInvite {
		action = "INVITATION_ISSUED"
	}
	if err := s.append(txCtx, tenant.ID, actor, action, user.ID, now,
		canonical.F("expiresAt", canonical.String(canonical.FormatTime(token.ExpiresAt))),
	); err != nil {
		return port.Message{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return port.Message{}, err
	}
	return credentialMessage(s.publicURL, tenant, user, purpose, secret.Cookie(), token.ExpiresAt), nil
}

// credentialMessage writes the email that carries a link.
func credentialMessage(publicURL string, tenant identity.Tenant, user identity.User, purpose identity.TokenPurpose, secret string, expires time.Time) port.Message {
	// The secret travels in the fragment, which browsers do not send to any
	// server or put in a Referer: it reaches the page, and the page alone.
	link := publicURL + "/#set-password=" + url.QueryEscape(secret)
	workspace := tenant.DisplayName
	if workspace == "" {
		workspace = tenant.Slug
	}
	if purpose == identity.TokenInvite {
		return port.Message{
			To:      user.Email,
			Subject: "You're invited to " + workspace + " on ZoikoTax",
			Text: fmt.Sprintf(`Hello %s,

You have been invited to the %s workspace on ZoikoTax.

Set your password to activate your account:

%s

This link works once and expires on %s.

When you sign in, use:
  Workspace: %s
  Email:     %s

If you were not expecting this invitation, you can ignore this message.

— ZoikoTax
`, user.DisplayName, workspace, link, expires.UTC().Format("2 January 2006 at 15:04 UTC"), tenant.Slug, user.Email),
		}
	}
	return port.Message{
		To:      user.Email,
		Subject: "Reset your ZoikoTax password",
		Text: fmt.Sprintf(`Hello %s,

Someone asked to reset the password for your account in the %s workspace on ZoikoTax.

Choose a new password here:

%s

This link works once and expires in one hour. Setting a new password signs you out everywhere.

If you did not ask for this, ignore this message: your password has not changed.

— ZoikoTax
`, user.DisplayName, workspace, link),
	}
}

// welcomeMessage writes the greeting an account receives when it becomes
// active. It carries no credential: whoever set the password already has it.
func welcomeMessage(publicURL string, tenant identity.Tenant, user identity.User) port.Message {
	workspace := tenant.DisplayName
	if workspace == "" {
		workspace = tenant.Slug
	}
	return port.Message{
		To:      user.Email,
		Subject: "Welcome to " + workspace + " on ZoikoTax",
		Text: fmt.Sprintf(`Hello %s,

Welcome to ZoikoTax. Your account in the %s workspace is ready.

Sign in at:

%s/

  Workspace: %s
  Email:     %s

ZoikoTax answers telecom tax questions across jurisdictions: determine the
tax on a line, see what is owed and when, reconcile it and prove it.

If you forget your password, use "Forgot your password?" on the sign-in
page. If you did not expect this account, tell your administrator.

— ZoikoTax
`, user.DisplayName, workspace, publicURL, tenant.Slug, user.Email),
	}
}

// recordRefusal notes a reset asked for an account that may not have one. Its
// own failure is ignored for the reason recordFailure gives in auth.go: the
// caller's answer must not change with it.
func (s *CredentialService) recordRefusal(ctx context.Context, tenant id.TenantID, user id.UserID, reason string) {
	scoped := security.Into(ctx, security.System(tenant))
	_ = s.append(scoped, tenant, nil, "PASSWORD_RESET_REFUSED", user, s.clock.Now(),
		canonical.F("reason", canonical.String(reason)))
}

func (s *CredentialService) append(ctx context.Context, tenant id.TenantID, actor *id.UserID, action string, subject id.UserID, now time.Time, detail ...canonical.Field) error {
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
		SubjectType: "user",
		SubjectID:   subject.String(),
		Detail:      body,
		RecordedAt:  now,
	})
}

// invalidLink is the one error every unusable link returns.
func invalidLink(cause error) error {
	if cause == nil {
		return errs.New(errs.CategoryPolicy, errs.ReasonCredentialLinkInvalid,
			"The link is not valid, has expired or has already been used. Ask for a new one.")
	}
	return errs.Wrap(cause, errs.CategoryPolicy, errs.ReasonCredentialLinkInvalid,
		"The link is not valid, has expired or has already been used. Ask for a new one.")
}
