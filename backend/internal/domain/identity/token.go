package identity

import (
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// TokenPurpose says what a credential token may do. A token issued for one
// purpose is never accepted for the other: an invitation opens an account
// that has never had a password, a reset replaces one that has.
type TokenPurpose string

// The purposes a credential token can carry.
const (
	TokenInvite TokenPurpose = "INVITE"
	TokenReset  TokenPurpose = "RESET"
)

// Valid reports whether p is a purpose this package defines.
func (p TokenPurpose) Valid() bool { return p == TokenInvite || p == TokenReset }

// Lifetime is how long a token of this purpose stays usable. An invitation
// waits for someone to read their mail; a reset was asked for a moment ago,
// and a long-lived one is a standing credential in an inbox.
func (p TokenPurpose) Lifetime() time.Duration {
	if p == TokenInvite {
		return 72 * time.Hour
	}
	return time.Hour
}

// CredentialToken is a one-time link that sets a user's password.
//
// The secret is a SessionToken — 256 bits from crypto/rand — and, as with a
// session, only its SHA-256 is stored, so a disclosure of the table yields no
// usable links. The secret exists in the message that carries it and in the
// request that presents it.
type CredentialToken struct {
	TenantID  id.TenantID
	UserID    id.UserID
	Purpose   TokenPurpose
	Digest    []byte
	CreatedAt time.Time
	ExpiresAt time.Time
	// UsedAt is set when the token sets a password, and when a newer token
	// for the same user and purpose supersedes it. Either way it is spent.
	UsedAt *time.Time
}

// NewCredentialToken records a token for user, issued at now.
func NewCredentialToken(user User, purpose TokenPurpose, secret SessionToken, now time.Time) CredentialToken {
	return CredentialToken{
		TenantID:  user.TenantID,
		UserID:    user.ID,
		Purpose:   purpose,
		Digest:    secret.Digest(),
		CreatedAt: now.UTC(),
		ExpiresAt: now.UTC().Add(purpose.Lifetime()),
	}
}

// Usable reports whether the token can still set a password at now.
func (t CredentialToken) Usable(now time.Time) bool {
	return t.UsedAt == nil && now.Before(t.ExpiresAt)
}
