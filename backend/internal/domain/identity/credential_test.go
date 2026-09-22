package identity_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

const goodPassword = "correct-horse-battery-staple"

func TestVerifierRoundTrips(t *testing.T) {
	v, err := identity.NewVerifier(goodPassword)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if err := v.Verify(goodPassword); err != nil {
		t.Errorf("the correct password did not verify: %v", err)
	}
	if err := v.Verify(goodPassword + "x"); !errors.Is(err, identity.ErrInvalidCredential) {
		t.Errorf("a wrong password gave %v, want ErrInvalidCredential", err)
	}
}

// The parameters travel with the hash, which is the whole reason for the PHC
// form: raising them later must not invalidate anyone's existing credential.
func TestVerifierCarriesItsParameters(t *testing.T) {
	v, err := identity.NewVerifier(goodPassword)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	s := v.String()
	for _, want := range []string{"$argon2id$", "v=19", "m=65536", "t=3", "p=4"} {
		if !strings.Contains(s, want) {
			t.Errorf("verifier %q does not carry %q", s, want)
		}
	}
	// It must survive a round trip through storage.
	parsed, err := identity.ParseVerifier(s)
	if err != nil {
		t.Fatalf("ParseVerifier: %v", err)
	}
	if err := parsed.Verify(goodPassword); err != nil {
		t.Errorf("a stored verifier did not verify: %v", err)
	}
}

// Every salt is fresh, so two users with one password have different verifiers
// and a stolen table cannot be attacked once for all of them.
func TestVerifiersAreSalted(t *testing.T) {
	a, err := identity.NewVerifier(goodPassword)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	b, err := identity.NewVerifier(goodPassword)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if a.String() == b.String() {
		t.Error("two verifiers for one password are identical; the salt is not being drawn")
	}
}

// An account with no password cannot sign in, and the refusal looks exactly
// like a wrong password from outside.
func TestZeroVerifierNeverMatches(t *testing.T) {
	var v identity.Verifier
	if !v.IsZero() {
		t.Fatal("the zero verifier does not report itself as zero")
	}
	for _, candidate := range []string{"", goodPassword, "anything"} {
		if err := v.Verify(candidate); !errors.Is(err, identity.ErrInvalidCredential) {
			t.Errorf("the zero verifier accepted %q", candidate)
		}
	}
}

func TestMalformedVerifiersAreRejected(t *testing.T) {
	for _, tc := range []struct{ name, phc string }{
		{"empty", ""},
		{"not phc", "hello"},
		{"wrong algorithm", "$argon2i$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA"},
		{"wrong version", "$argon2id$v=16$m=65536,t=3,p=4$c2FsdA$aGFzaA"},
		{"missing parameters", "$argon2id$v=19$$c2FsdA$aGFzaA"},
		{"bad base64", "$argon2id$v=19$m=65536,t=3,p=4$!!!$aGFzaA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := identity.ParseVerifier(tc.phc); err == nil {
				t.Errorf("accepted %q", tc.phc)
			}
		})
	}
}

func TestPasswordPolicy(t *testing.T) {
	if err := identity.ValidatePassword(strings.Repeat("a", identity.MinPasswordLength-1)); err == nil {
		t.Error("a password below the minimum was accepted")
	}
	if err := identity.ValidatePassword(strings.Repeat("a", identity.MinPasswordLength)); err != nil {
		t.Errorf("a password at the minimum was refused: %v", err)
	}
	if err := identity.ValidatePassword(strings.Repeat("a", identity.MaxPasswordLength+1)); err == nil {
		t.Error("an unbounded password was accepted")
	}
	// The minimum counts runes, not bytes: a passphrase in a non-Latin script
	// would otherwise face a much longer effective minimum than one in ASCII.
	if err := identity.ValidatePassword(strings.Repeat("パ", identity.MinPasswordLength)); err != nil {
		t.Errorf("a multi-byte passphrase at the minimum was refused: %v", err)
	}
}

// The session token exists in the clear exactly once. The database holds only
// its digest, so a disclosure of the session table yields no usable sessions.
func TestSessionTokenIsNeverStoredInTheClear(t *testing.T) {
	token, err := identity.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	cookie := token.Cookie()
	if cookie == "" {
		t.Fatal("the token produced no cookie value")
	}
	if strings.Contains(string(token.Digest()), cookie) {
		t.Error("the digest contains the token")
	}

	// The digest is reproducible from the cookie, which is what makes lookup
	// possible without storing the secret.
	parsed, err := identity.ParseSessionToken(cookie)
	if err != nil {
		t.Fatalf("ParseSessionToken: %v", err)
	}
	if string(parsed.Digest()) != string(token.Digest()) {
		t.Error("a round-tripped token produced a different digest")
	}
}

func TestSessionTokensAreDistinct(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 200; i++ {
		token, err := identity.NewSessionToken()
		if err != nil {
			t.Fatalf("NewSessionToken: %v", err)
		}
		if _, dup := seen[token.Cookie()]; dup {
			t.Fatal("two session tokens collided")
		}
		seen[token.Cookie()] = struct{}{}
	}
}

func TestMalformedCookiesAreRejected(t *testing.T) {
	for _, tc := range []struct{ name, cookie string }{
		{"empty", ""},
		{"not base64", "!!!!"},
		{"too short", "YWJj"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := identity.ParseSessionToken(tc.cookie); err == nil {
				t.Errorf("accepted %q", tc.cookie)
			}
		})
	}
}

// Two limits, and the absolute one cannot be extended by use. A session that is
// merely long is not the same risk as one that is long and unattended.
func TestSessionExpiry(t *testing.T) {
	var gen idgen.Sequential
	sessionID, err := idgen.SessionID(&gen)
	if err != nil {
		t.Fatalf("SessionID: %v", err)
	}
	userID, err := idgen.UserID(&gen)
	if err != nil {
		t.Fatalf("UserID: %v", err)
	}
	tenantID, err := idgen.TenantID(&gen)
	if err != nil {
		t.Fatalf("TenantID: %v", err)
	}
	token, err := identity.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}

	start := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	user := identity.User{TenantID: tenantID, ID: userID, Status: identity.UserActive}
	s := identity.NewSession(sessionID, user, token, start, "curl", "203.0.113.1")

	if got := s.State(start.Add(time.Minute)); got != identity.SessionValid {
		t.Errorf("a fresh session is %v, want valid", got)
	}
	if got := s.State(start.Add(identity.IdleTimeout + time.Second)); got != identity.SessionExpired {
		t.Errorf("an idle session is %v, want expired", got)
	}
	if got := s.State(start.Add(identity.AbsoluteTimeout + time.Second)); got != identity.SessionExpired {
		t.Errorf("an old session is %v, want expired", got)
	}

	// Sliding cannot push a session past its absolute limit. This is the
	// property that makes the second limit worth having: without it, a
	// background tab polling an endpoint keeps a session alive indefinitely.
	nearEnd := start.Add(identity.AbsoluteTimeout - time.Minute)
	if slid := s.SlideTo(nearEnd); slid.After(s.AbsoluteExpiresAt) {
		t.Errorf("sliding extended the session past its absolute limit: %v > %v", slid, s.AbsoluteExpiresAt)
	}

	// Revocation is reported as revocation, not as expiry: an administrator
	// revoking a session should see that in the audit trail rather than having
	// it turn into an expiry a few minutes later.
	revokedAt := start.Add(time.Minute)
	s.RevokedAt = &revokedAt
	if got := s.State(start.Add(2 * time.Minute)); got != identity.SessionRevoked {
		t.Errorf("a revoked session is %v, want revoked", got)
	}
	if got := s.State(start.Add(identity.AbsoluteTimeout + time.Hour)); got != identity.SessionRevoked {
		t.Errorf("a revoked, expired session is %v, want revoked", got)
	}
}

// Writing the session row on every request would make it the busiest write in
// the system. Sliding only past a threshold keeps the guarantee while leaving
// most requests read-only.
func TestSlideIsRateLimited(t *testing.T) {
	var gen idgen.Sequential
	sessionID, _ := idgen.SessionID(&gen)
	userID, _ := idgen.UserID(&gen)
	tenantID, _ := idgen.TenantID(&gen)
	token, err := identity.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}

	start := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	s := identity.NewSession(sessionID, identity.User{TenantID: tenantID, ID: userID}, token, start, "curl", "")

	if s.ShouldSlide(start.Add(time.Minute)) {
		t.Error("a session slid one minute after creation; that is a write per request")
	}
	if !s.ShouldSlide(start.Add(identity.SlideThreshold + time.Minute)) {
		t.Error("a session past the slide threshold did not slide; it would expire while in use")
	}
}

func TestUserCanAuthenticate(t *testing.T) {
	v, err := identity.NewVerifier(goodPassword)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	for _, tc := range []struct {
		name string
		user identity.User
		want bool
	}{
		{"active with a password", identity.User{Status: identity.UserActive, Verifier: v}, true},
		{"active with no password", identity.User{Status: identity.UserActive}, false},
		{"invited", identity.User{Status: identity.UserInvited, Verifier: v}, false},
		{"disabled", identity.User{Status: identity.UserDisabled, Verifier: v}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.user.CanAuthenticate(); got != tc.want {
				t.Errorf("CanAuthenticate is %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTenantSlugGrammar(t *testing.T) {
	for _, tc := range []struct {
		slug string
		ok   bool
	}{
		{"acme", true},
		{"acme-telecom", true},
		{"a1", true},
		{"a", false},
		{"-leading-hyphen", false},
		{"Upper", false},
		{"has space", false},
		{"under_score", false},
		{strings.Repeat("a", 64), false},
	} {
		t.Run(tc.slug, func(t *testing.T) {
			err := identity.ValidateSlug(tc.slug)
			if tc.ok && err != nil {
				t.Errorf("rejected %q: %v", tc.slug, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("accepted %q", tc.slug)
			}
		})
	}
}

// Normalization is deliberately minimal. Stripping dots or +suffixes is
// provider policy dressed up as normalization, and it would make two genuinely
// different addresses collide inside one tenant.
func TestEmailNormalizationIsMinimal(t *testing.T) {
	if got := identity.NormalizeEmail("  Admin@Acme.Example "); got != "admin@acme.example" {
		t.Errorf("got %q, want %q", got, "admin@acme.example")
	}
	if got := identity.NormalizeEmail("first.last+tag@acme.example"); got != "first.last+tag@acme.example" {
		t.Errorf("normalization altered the local part: %q", got)
	}
}

func TestEmailValidation(t *testing.T) {
	for _, tc := range []struct {
		email string
		ok    bool
	}{
		{"a@b.example", true},
		{"first.last+tag@acme.example", true},
		{"no-at-sign", false},
		{"@leading", false},
		{"trailing@", false},
		{"has space@acme.example", false},
	} {
		t.Run(tc.email, func(t *testing.T) {
			err := identity.ValidateEmail(tc.email)
			if tc.ok && err != nil {
				t.Errorf("rejected %q: %v", tc.email, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("accepted %q", tc.email)
			}
		})
	}
}

// The zero security context is unauthenticated, which is what makes a missing
// authentication step fail closed rather than silently granting access.
func TestZeroSecurityContextIsUnauthenticated(t *testing.T) {
	var sc security.Context
	if sc.Authenticated() {
		t.Error("the zero security context reports itself as authenticated")
	}
	if !sc.Tenant().IsZero() || !sc.Subject().IsZero() {
		t.Error("the zero security context names a tenant or a subject")
	}
	for _, r := range security.AllRoles {
		if sc.HasRole(r) {
			t.Errorf("the zero security context holds %s", r)
		}
	}
}

// A role this build does not understand cannot be checked, so carrying it would
// let a future rename silently grant access.
func TestUnknownRolesAreDropped(t *testing.T) {
	var gen idgen.Sequential
	tenantID, _ := idgen.TenantID(&gen)
	userID, _ := idgen.UserID(&gen)
	sessionID, _ := idgen.SessionID(&gen)

	sc := security.New(tenantID, userID, sessionID,
		[]security.Role{security.RoleAdmin, "SUPERUSER", security.RoleAdmin},
		time.Now().UTC())

	roles := sc.Roles()
	if len(roles) != 1 || roles[0] != security.RoleAdmin {
		t.Errorf("roles are %v, want exactly [ADMIN]", roles)
	}
	if sc.HasRole("SUPERUSER") {
		t.Error("an unknown role was honoured")
	}
}

// Background work has a tenant and no subject, and must still be able to read;
// but it holds no roles, so it cannot pass an authorization check meant for a
// user.
func TestSystemContextCarriesATenantAndNoAuthority(t *testing.T) {
	var gen idgen.Sequential
	tenantID, _ := idgen.TenantID(&gen)

	sc := security.System(tenantID)
	if sc.Tenant() != tenantID {
		t.Error("the system context lost its tenant")
	}
	if sc.Authenticated() {
		t.Error("the system context reports a subject")
	}
	for _, r := range security.AllRoles {
		if sc.HasRole(r) {
			t.Errorf("the system context holds %s", r)
		}
	}
}
