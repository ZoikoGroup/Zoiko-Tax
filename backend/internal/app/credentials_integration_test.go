//go:build integration

package app_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/postgres"
	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// outbox is a mailer that keeps what it was asked to send.
type outbox struct {
	mu   sync.Mutex
	sent []port.Message
}

func (o *outbox) Send(_ context.Context, m port.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sent = append(o.sent, m)
	return nil
}

func (o *outbox) last(t *testing.T) port.Message {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.sent) == 0 {
		t.Fatal("nothing was sent")
	}
	return o.sent[len(o.sent)-1]
}

// linkIn reads the secret from the set-password link a message carries.
func linkIn(t *testing.T, m port.Message) string {
	t.Helper()
	const marker = "#set-password="
	i := strings.Index(m.Text, marker)
	if i < 0 {
		t.Fatalf("no link in %q", m.Text)
	}
	raw := m.Text[i+len(marker):]
	raw = raw[:strings.IndexAny(raw, " \n")]
	secret, err := url.QueryUnescape(raw)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

type credentialCell struct {
	ctx    context.Context
	admin  context.Context
	tenant identity.Tenant
	clock  *settableClock
	mail   *outbox
	auth   *app.AuthService
	users  *app.AdminService
	creds  *app.CredentialService
}

func openCredentialCell(t *testing.T) *credentialCell {
	t.Helper()
	dsn := os.Getenv("ZTAX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ZTAX_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := postgres.Open(ctx, postgres.DefaultConfig(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewStore(pool)

	tenantID := id.NewTenantID(uuid.Must(uuid.NewV7()))
	tenant := identity.Tenant{
		ID: tenantID, Slug: "creds-" + strings.ReplaceAll(tenantID.String(), "-", "")[20:], DisplayName: "Credentials integration",
		ResidencyRegion: "local", HomeCell: "local-dev", Status: identity.TenantActive, CreatedAt: time.Now().UTC(),
	}
	if err := store.Tenants().Create(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	clk := &settableClock{t: time.Now().UTC()}
	mail := &outbox{}
	admin := security.New(tenantID, id.NewUserID(uuid.Must(uuid.NewV7())), id.NewSessionID(uuid.Must(uuid.NewV7())),
		[]security.Role{security.RoleAdmin}, time.Now())
	return &credentialCell{
		ctx: ctx, admin: security.Into(ctx, admin), tenant: tenant, clock: clk, mail: mail,
		auth:  app.NewAuthService(store.Tenants(), store.Users(), store.Sessions(), store.Audit(), store, clk, idgen.V7{}),
		users: app.NewAdminService(store.Tenants(), store.Users(), store.Sessions(), store.Audit(), store, clk, idgen.V7{}, "local"),
		creds: app.NewCredentialService(store.Tenants(), store.Users(), store.Sessions(), store.CredentialTokens(),
			store.Audit(), store, clk, idgen.V7{}, mail, "https://app.zoikotax.example/"),
	}
}

func (c *credentialCell) signIn(email, password string) error {
	_, err := c.auth.SignIn(c.ctx, app.SignInInput{TenantSlug: c.tenant.Slug, Email: email, Password: password})
	return err
}

const (
	firstPassword  = "a first passphrase, long enough"
	secondPassword = "a second passphrase, also long enough"
)

func TestIntegrationAnInvitationActivatesTheAccountOnce(t *testing.T) {
	c := openCredentialCell(t)
	user, err := c.users.CreateUser(c.admin, app.CreateUserInput{
		Email: "invitee@acme.example", DisplayName: "Invitee", Roles: []security.Role{security.RoleAnalyst},
	})
	if err != nil {
		t.Fatal(err)
	}
	if user.Status != identity.UserInvited {
		t.Fatalf("status %s, want INVITED", user.Status)
	}
	if err := c.creds.Invite(c.admin, user.ID); err != nil {
		t.Fatal(err)
	}
	msg := c.mail.last(t)
	if msg.To != "invitee@acme.example" || !strings.Contains(msg.Text, "https://app.zoikotax.example/#set-password=") {
		t.Fatalf("invitation: %+v", msg)
	}
	if !strings.Contains(msg.Text, "Workspace: "+c.tenant.Slug) {
		t.Fatalf("the invitation does not name the workspace to sign in to: %q", msg.Text)
	}

	result, err := c.creds.SetPassword(c.ctx, linkIn(t, msg), firstPassword)
	if err != nil {
		t.Fatal(err)
	}
	if result.TenantSlug != c.tenant.Slug || result.Email != "invitee@acme.example" {
		t.Fatalf("result %+v", result)
	}
	if result.Welcome == nil || result.Welcome.To != "invitee@acme.example" ||
		!strings.Contains(result.Welcome.Text, "Workspace: "+c.tenant.Slug) || strings.Contains(result.Welcome.Text, firstPassword) {
		t.Fatalf("welcome on activation: %+v", result.Welcome)
	}
	if err := c.signIn("invitee@acme.example", firstPassword); err != nil {
		t.Fatalf("sign in with the password the invitation set: %v", err)
	}
	if _, err := c.creds.SetPassword(c.ctx, linkIn(t, msg), secondPassword); errs.ReasonOf(err) != errs.ReasonCredentialLinkInvalid {
		t.Fatalf("the same link a second time: %v; want CREDENTIAL_LINK_INVALID", err)
	}
	if err := c.creds.Invite(c.admin, user.ID); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("inviting an active user: %v; want a conflict", err)
	}
}

func TestIntegrationAResetReplacesThePasswordAndOnlyTheNewestLinkWorks(t *testing.T) {
	c := openCredentialCell(t)
	if _, err := c.users.CreateUser(c.admin, app.CreateUserInput{
		Email: "Forgetful@acme.example", DisplayName: "Forgetful", Password: firstPassword,
	}); err != nil {
		t.Fatal(err)
	}

	first, err := c.creds.RequestReset(c.ctx, c.tenant.Slug, "forgetful@acme.example")
	if err != nil || first == nil {
		t.Fatalf("first reset: %v, %v", first, err)
	}
	second, err := c.creds.RequestReset(c.ctx, c.tenant.Slug, "FORGETFUL@acme.example")
	if err != nil || second == nil {
		t.Fatalf("second reset: %v, %v", second, err)
	}
	if !strings.Contains(second.Subject, "Reset") {
		t.Fatalf("subject %q", second.Subject)
	}
	if _, err := c.creds.SetPassword(c.ctx, linkIn(t, *first), secondPassword); errs.ReasonOf(err) != errs.ReasonCredentialLinkInvalid {
		t.Fatalf("a superseded link: %v; want CREDENTIAL_LINK_INVALID", err)
	}
	if _, err := c.creds.SetPassword(c.ctx, linkIn(t, *second), "short"); errs.ReasonOf(err) != errs.ReasonPasswordTooWeak {
		t.Fatalf("a weak password: %v; want PASSWORD_TOO_WEAK", err)
	}
	reset, err := c.creds.SetPassword(c.ctx, linkIn(t, *second), secondPassword)
	if err != nil {
		t.Fatal(err)
	}
	if reset.Welcome != nil {
		t.Fatal("a reset greeted the user as if they had just joined")
	}
	if err := c.signIn("forgetful@acme.example", firstPassword); errs.ReasonOf(err) != errs.ReasonInvalidCredentials {
		t.Fatalf("the old password after a reset: %v", err)
	}
	if err := c.signIn("forgetful@acme.example", secondPassword); err != nil {
		t.Fatalf("the new password: %v", err)
	}
}

func TestIntegrationAResetLinkExpiresAfterAnHour(t *testing.T) {
	c := openCredentialCell(t)
	if _, err := c.users.CreateUser(c.admin, app.CreateUserInput{
		Email: "late@acme.example", DisplayName: "Late", Password: firstPassword,
	}); err != nil {
		t.Fatal(err)
	}
	msg, err := c.creds.RequestReset(c.ctx, c.tenant.Slug, "late@acme.example")
	if err != nil || msg == nil {
		t.Fatalf("reset: %v, %v", msg, err)
	}
	c.clock.Set(c.clock.Now().Add(61 * time.Minute))
	if _, err := c.creds.SetPassword(c.ctx, linkIn(t, *msg), secondPassword); errs.ReasonOf(err) != errs.ReasonCredentialLinkInvalid {
		t.Fatalf("an expired link: %v; want CREDENTIAL_LINK_INVALID", err)
	}
}

func TestIntegrationAResetRevealsNothingAboutWhichAccountsExist(t *testing.T) {
	c := openCredentialCell(t)
	// Disabling anyone needs the tenant to keep an administrator.
	if _, err := c.users.CreateUser(c.admin, app.CreateUserInput{
		Email: "admin@acme.example", DisplayName: "Admin", Roles: []security.Role{security.RoleAdmin}, Password: firstPassword,
	}); err != nil {
		t.Fatal(err)
	}
	disabled, err := c.users.CreateUser(c.admin, app.CreateUserInput{
		Email: "gone@acme.example", DisplayName: "Gone", Password: firstPassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.users.SetUserStatus(c.admin, disabled.ID, identity.UserDisabled); err != nil {
		t.Fatal(err)
	}
	for name, ask := range map[string][2]string{
		"unknown tenant":   {"no-such-tenant-anywhere", "gone@acme.example"},
		"unknown address":  {c.tenant.Slug, "nobody@acme.example"},
		"disabled account": {c.tenant.Slug, "gone@acme.example"},
	} {
		msg, err := c.creds.RequestReset(c.ctx, ask[0], ask[1])
		if err != nil || msg != nil {
			t.Errorf("%s: %v, %v; want no message and no error", name, msg, err)
		}
	}
}

func TestIntegrationAResetForAnInvitedUserSendsAnInvitation(t *testing.T) {
	c := openCredentialCell(t)
	if _, err := c.users.CreateUser(c.admin, app.CreateUserInput{
		Email: "never-came@acme.example", DisplayName: "Never came",
	}); err != nil {
		t.Fatal(err)
	}
	msg, err := c.creds.RequestReset(c.ctx, c.tenant.Slug, "never-came@acme.example")
	if err != nil || msg == nil {
		t.Fatalf("reset: %v, %v", msg, err)
	}
	if !strings.Contains(msg.Subject, "invited") {
		t.Fatalf("subject %q; want an invitation", msg.Subject)
	}
	if _, err := c.creds.SetPassword(c.ctx, linkIn(t, *msg), firstPassword); err != nil {
		t.Fatal(err)
	}
	if err := c.signIn("never-came@acme.example", firstPassword); err != nil {
		t.Fatal(err)
	}
}
