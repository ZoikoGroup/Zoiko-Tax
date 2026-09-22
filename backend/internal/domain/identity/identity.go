package identity

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
)

// Session lifetimes.
//
// Two limits rather than one, because a session that is merely long is not the
// same risk as one that is long and unattended. Idle expiry slides forward on
// use; absolute expiry does not move, so a session cannot be kept alive
// indefinitely by a background tab polling an endpoint.
const (
	// IdleTimeout ends a session that has not been used.
	IdleTimeout = 30 * time.Minute
	// AbsoluteTimeout ends a session however active it has been.
	AbsoluteTimeout = 12 * time.Hour
	// SlideThreshold is how much of the idle window must have elapsed before a
	// request writes a new idle expiry. Without it every request writes to the
	// session row, which turns a read-mostly table into the busiest write in
	// the system for no security benefit.
	SlideThreshold = 5 * time.Minute
)

// TenantStatus is the lifecycle state of a tenant.
type TenantStatus string

// The tenant states.
const (
	TenantActive    TenantStatus = "ACTIVE"
	TenantSuspended TenantStatus = "SUSPENDED"
	TenantClosed    TenantStatus = "CLOSED"
)

// Tenant is one customer of the estate, inside one cell.
type Tenant struct {
	ID              id.TenantID
	Slug            string
	DisplayName     string
	ResidencyRegion string
	Status          TenantStatus
	CreatedAt       time.Time
}

// CanAuthenticate reports whether sign-in is permitted for this tenant.
func (t Tenant) CanAuthenticate() bool { return t.Status == TenantActive }

// ErrInvalidSlug is returned for a handle that does not meet the grammar.
var ErrInvalidSlug = errors.New("identity: invalid tenant slug")

// ValidateSlug applies the tenant handle grammar, which matches the CHECK
// constraint on the column. Both exist deliberately: the application gives a
// usable message, and the database guarantees the invariant against any writer.
func ValidateSlug(slug string) error {
	if len(slug) < 2 || len(slug) > 63 {
		return fmt.Errorf("%w: %q is %d characters, want 2 to 63", ErrInvalidSlug, slug, len(slug))
	}
	for i := 0; i < len(slug); i++ {
		c := slug[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i != 0:
		default:
			return fmt.Errorf("%w: %q may hold only lowercase letters, digits and hyphens, and may not start with a hyphen", ErrInvalidSlug, slug)
		}
	}
	return nil
}

// UserStatus is the lifecycle state of a user.
type UserStatus string

// The user states. INVITED is distinct from ACTIVE because an invited user
// exists, holds roles and appears in the admin list, but cannot yet sign in.
const (
	UserActive   UserStatus = "ACTIVE"
	UserInvited  UserStatus = "INVITED"
	UserDisabled UserStatus = "DISABLED"
)

// User is a principal within a tenant.
type User struct {
	TenantID    id.TenantID
	ID          id.UserID
	Email       string
	DisplayName string
	Verifier    Verifier
	Status      UserStatus
	Roles       []security.Role
	CreatedAt   time.Time
}

// CanAuthenticate reports whether this user may sign in.
func (u User) CanAuthenticate() bool {
	return u.Status == UserActive && !u.Verifier.IsZero()
}

// NormalizeEmail lowercases and trims an address for comparison.
//
// It deliberately does not do more. Stripping dots or +suffixes is provider
// policy dressed up as normalization, it is wrong for most of the world's mail
// servers, and it would make two genuinely different addresses collide inside
// one tenant.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidateEmail applies a deliberately minimal check.
//
// Anything stricter is a well-known way to reject valid addresses; the address
// is proven by sending mail to it, not by a regular expression.
func ValidateEmail(email string) error {
	e := NormalizeEmail(email)
	at := strings.LastIndex(e, "@")
	if at <= 0 || at == len(e)-1 {
		return fmt.Errorf("identity: %q is not an email address", email)
	}
	if strings.ContainsAny(e, " \t\r\n") {
		return fmt.Errorf("identity: %q contains whitespace", email)
	}
	if len(e) > 254 {
		return fmt.Errorf("identity: email address exceeds 254 characters")
	}
	return nil
}

// Session is an authenticated session.
//
// The token is not here. This struct is what the database holds, and the
// database holds only the digest.
type Session struct {
	TenantID          id.TenantID
	ID                id.SessionID
	UserID            id.UserID
	TokenDigest       []byte
	CreatedAt         time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	RevokedAt         *time.Time
	RevokedReason     string
	UserAgent         string
	ClientIP          string
}

// SessionState is why a session is or is not usable.
type SessionState int

// The session states. They are distinguished because the client action differs
// for each: re-authenticate, re-authenticate, or check how the cookie is being
// sent.
const (
	// SessionValid is usable.
	SessionValid SessionState = iota
	// SessionExpired passed a time limit.
	SessionExpired
	// SessionRevoked was ended by an administrator or a sign-out.
	SessionRevoked
)

// NewSession builds a session record for a fresh sign-in.
func NewSession(sessionID id.SessionID, user User, token SessionToken, now time.Time, userAgent, clientIP string) Session {
	now = now.UTC()
	return Session{
		TenantID:          user.TenantID,
		ID:                sessionID,
		UserID:            user.ID,
		TokenDigest:       token.Digest(),
		CreatedAt:         now,
		IdleExpiresAt:     now.Add(IdleTimeout),
		AbsoluteExpiresAt: now.Add(AbsoluteTimeout),
		UserAgent:         truncate(userAgent, 512),
		ClientIP:          clientIP,
	}
}

// State reports whether the session is usable at an instant.
//
// Revocation is checked before expiry so that an administrator revoking a
// session sees SessionRevoked in the audit trail rather than having it turn
// into SessionExpired a few minutes later.
func (s Session) State(now time.Time) SessionState {
	if s.RevokedAt != nil {
		return SessionRevoked
	}
	now = now.UTC()
	if !now.Before(s.AbsoluteExpiresAt) || !now.Before(s.IdleExpiresAt) {
		return SessionExpired
	}
	return SessionValid
}

// ShouldSlide reports whether this request should extend the idle window.
//
// Writing on every request would make the session row the hottest write in the
// system. Writing only once the threshold has passed keeps the guarantee — an
// unattended session still expires within IdleTimeout — while leaving most
// requests read-only.
func (s Session) ShouldSlide(now time.Time) bool {
	return now.UTC().After(s.IdleExpiresAt.Add(-IdleTimeout + SlideThreshold))
}

// SlideTo returns the new idle expiry, never beyond the absolute limit. A
// session cannot outlive AbsoluteTimeout by sliding, which is the property that
// makes the second limit worth having.
func (s Session) SlideTo(now time.Time) time.Time {
	next := now.UTC().Add(IdleTimeout)
	if next.After(s.AbsoluteExpiresAt) {
		return s.AbsoluteExpiresAt
	}
	return next
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
