// Package identity holds tenants, users, credentials and sessions.
//
// ADR-0020 records the decisions. Two of them shape everything here:
//
//   - A password is stored as an Argon2id verifier in PHC string form, so the
//     parameters travel with the hash and can be raised later without
//     invalidating anyone's credential.
//   - A session is an opaque server-side record. The cookie carries 256 bits
//     from crypto/rand; the database stores only its SHA-256. A disclosure of
//     the session table therefore yields no usable sessions, which is the
//     property a signed token in a cookie cannot offer — there, the cookie is
//     the credential and the server's copy is the verifier for it.
//
// Nothing in this package reads a clock. Instants arrive as parameters, so a
// test can establish a session, expire it and revoke it without sleeping, and
// so the rule in ADR-0003 §2.5 holds here as it does in the fiscal path.
package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// ErrInvalidCredential is returned when verification fails. It does not
// distinguish an unknown user from a wrong password: that difference is an
// account-enumeration oracle and the caller can do nothing useful with it.
var ErrInvalidCredential = errors.New("identity: invalid credential")

// Argon2id parameters.
//
// These are the reference "second recommended option" from RFC 9106 §4 —
// 64 MiB, three passes, four lanes — which targets roughly a tenth of a second
// on server hardware. The cost is deliberate: it is what makes an offline
// attack against a stolen verifier expensive, and a sign-in is not a hot path.
//
// Raising them later does not invalidate existing credentials, because each
// verifier carries the parameters it was produced with. A verifier below
// current policy is upgraded on the next successful sign-in, when the plaintext
// is momentarily available.
const (
	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024 // KiB
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// MinPasswordLength is the floor. Length dominates every other password rule
// for actual resistance to guessing, so this is the one rule enforced —
// composition rules push people towards predictable substitutions and a
// password manager makes them irrelevant.
const MinPasswordLength = 12

// MaxPasswordLength bounds the input. Argon2 will hash any length, and an
// unbounded one is a way to make the server do expensive work on demand.
const MaxPasswordLength = 1024

// maxHashLen bounds a stored verifier's hash. Current policy produces 32 bytes;
// the ceiling exists so that a malformed stored value cannot drive an unbounded
// allocation or a length conversion that wraps.
const maxHashLen = 1024

// Verifier is a stored password verifier in PHC string form:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
//
// It is a distinct type rather than a string so that it cannot be confused with
// a password at a call site. There is no accessor returning the underlying
// hash for comparison; comparison happens here, in constant time.
type Verifier struct{ phc string }

// String returns the PHC form, for storage.
func (v Verifier) String() string { return v.phc }

// IsZero reports whether this is the empty verifier, which never matches.
func (v Verifier) IsZero() bool { return v.phc == "" }

// ParseVerifier reads a stored verifier. It does not validate the embedded
// parameters beyond structure: a verifier written under older parameters must
// still verify, which is the entire reason the parameters are stored.
func ParseVerifier(s string) (Verifier, error) {
	if _, err := decodePHC(s); err != nil {
		return Verifier{}, err
	}
	return Verifier{phc: s}, nil
}

// ValidatePassword applies the length policy, returning a reason a caller can
// surface. It is separate from NewVerifier so that a caller can check a
// candidate before spending 100ms hashing it.
func ValidatePassword(password string) error {
	// Count runes, not bytes: a passphrase in a non-Latin script would
	// otherwise face a much longer effective minimum than one in ASCII.
	if n := utf8.RuneCountInString(password); n < MinPasswordLength {
		return fmt.Errorf("identity: password is %d characters, minimum is %d", n, MinPasswordLength)
	}
	if len(password) > MaxPasswordLength {
		return fmt.Errorf("identity: password exceeds %d bytes", MaxPasswordLength)
	}
	return nil
}

// NewVerifier hashes a password under current policy.
func NewVerifier(password string) (Verifier, error) {
	if err := ValidatePassword(password); err != nil {
		return Verifier{}, err
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return Verifier{}, fmt.Errorf("identity: read entropy: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return Verifier{phc: fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		b64(salt), b64(hash))}, nil
}

// Verify reports whether password matches v.
//
// It returns ErrInvalidCredential for both a structural failure and a
// mismatch, so that a caller cannot accidentally branch on the difference and
// leak it into a response.
func (v Verifier) Verify(password string) error {
	if v.IsZero() {
		// An account with no password set. Deliberately still runs no
		// comparison but returns the same error: "this account cannot sign in
		// with a password" and "wrong password" look identical from outside.
		return ErrInvalidCredential
	}
	p, err := decodePHC(v.phc)
	if err != nil {
		return ErrInvalidCredential
	}
	// The stored hash length drives the comparison length. It is bounded on the
	// way in by decodePHC, so the conversion cannot overflow; the explicit
	// check is here because a silent wrap would produce a short comparison,
	// which is a weakened credential check rather than a crash.
	keyLen := len(p.hash)
	if keyLen <= 0 || keyLen > maxHashLen {
		return ErrInvalidCredential
	}
	// #nosec G115 -- keyLen is bounded to [1, maxHashLen] on the line above, so
	// the conversion cannot wrap. The bound is not decorative: a wrapped length
	// would shorten the comparison, which weakens the credential check rather
	// than crashing, and that is the failure mode worth spending three lines on.
	candidate := argon2.IDKey([]byte(password), p.salt, p.time, p.memory, p.threads, uint32(keyLen))
	if subtle.ConstantTimeCompare(candidate, p.hash) != 1 {
		return ErrInvalidCredential
	}
	return nil
}

// NeedsRehash reports whether v was produced under weaker parameters than
// current policy. A caller rehashes on the next successful sign-in, which is
// the only moment the plaintext is available.
func (v Verifier) NeedsRehash() bool {
	p, err := decodePHC(v.phc)
	if err != nil {
		return true
	}
	return p.time < argonTime || p.memory < argonMemory || p.threads < argonThreads
}

// phcParts is a decoded verifier.
type phcParts struct {
	memory  uint32
	time    uint32
	threads uint8
	salt    []byte
	hash    []byte
}

func decodePHC(s string) (phcParts, error) {
	// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
	fields := strings.Split(s, "$")
	if len(fields) != 6 || fields[0] != "" {
		return phcParts{}, fmt.Errorf("identity: verifier is not a PHC string")
	}
	if fields[1] != "argon2id" {
		return phcParts{}, fmt.Errorf("identity: verifier algorithm is %q, want argon2id", fields[1])
	}

	var version int
	if _, err := fmt.Sscanf(fields[2], "v=%d", &version); err != nil {
		return phcParts{}, fmt.Errorf("identity: verifier version: %w", err)
	}
	if version != argon2.Version {
		return phcParts{}, fmt.Errorf("identity: verifier version %d, want %d", version, argon2.Version)
	}

	var p phcParts
	if _, err := fmt.Sscanf(fields[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return phcParts{}, fmt.Errorf("identity: verifier parameters: %w", err)
	}

	var err error
	if p.salt, err = unb64(fields[4]); err != nil {
		return phcParts{}, fmt.Errorf("identity: verifier salt: %w", err)
	}
	if p.hash, err = unb64(fields[5]); err != nil {
		return phcParts{}, fmt.Errorf("identity: verifier hash: %w", err)
	}
	if len(p.salt) == 0 || len(p.hash) == 0 {
		return phcParts{}, fmt.Errorf("identity: verifier salt or hash is empty")
	}
	return p, nil
}

// PHC uses unpadded standard base64.
func b64(b []byte) string            { return base64.RawStdEncoding.EncodeToString(b) }
func unb64(s string) ([]byte, error) { return base64.RawStdEncoding.DecodeString(s) }

// SessionTokenBytes is the entropy in a session cookie. 256 bits from
// crypto/rand has no guessable structure, which is why the stored digest can be
// a plain SHA-256 rather than a work-factored hash: there is nothing for a work
// factor to defend against.
const SessionTokenBytes = 32

// SessionToken is the secret a session cookie carries. It exists only in the
// response that creates it and in the request that presents it; the estate
// never stores it.
type SessionToken struct{ raw []byte }

// NewSessionToken draws a fresh token.
func NewSessionToken() (SessionToken, error) {
	b := make([]byte, SessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return SessionToken{}, fmt.Errorf("identity: read entropy: %w", err)
	}
	return SessionToken{raw: b}, nil
}

// ParseSessionToken reads a token from its cookie form. A malformed cookie is
// not an error worth distinguishing — it yields a token whose digest matches
// nothing, and the lookup fails as an unknown session.
func ParseSessionToken(s string) (SessionToken, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return SessionToken{}, ErrInvalidCredential
	}
	if len(b) != SessionTokenBytes {
		return SessionToken{}, ErrInvalidCredential
	}
	return SessionToken{raw: b}, nil
}

// Cookie returns the value to set in the cookie. It is the only way the raw
// secret leaves this package.
func (t SessionToken) Cookie() string { return base64.RawURLEncoding.EncodeToString(t.raw) }

// Digest returns the SHA-256 stored in the session row. Lookup is by this
// value, so the database never holds anything that could be replayed as a
// credential.
func (t SessionToken) Digest() []byte {
	sum := sha256.Sum256(t.raw)
	return sum[:]
}

// IsZero reports whether this is the empty token.
func (t SessionToken) IsZero() bool { return len(t.raw) == 0 }
