// Package secretbox encrypts a secret the cell must be able to read back —
// a webhook signing secret, which signs every delivery and so cannot be
// stored as a hash the way a password is.
//
// AES-256-GCM under a key the deployment supplies by reference (ADR-0017
// §2.4: configuration names where a key lives, never the key). The
// associated data binds a ciphertext to the row it belongs to, so a
// ciphertext copied into another subscription's row, or another version's,
// fails to open rather than signing for something it was never issued to.
//
// A sealed value is versioned by its first byte, so the key can be rotated
// later by adding a version rather than by rewriting every row.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// formatV1 is AES-256-GCM, 12-byte random nonce: version || nonce || sealed.
const formatV1 byte = 1

// KeyBytes is the key length.
const KeyBytes = 32

// Box seals and opens under one key.
type Box struct{ aead cipher.AEAD }

// New builds a box from a base64 key of KeyBytes bytes.
func New(base64Key string) (*Box, error) {
	key, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil || len(key) != KeyBytes {
		return nil, fmt.Errorf("secretbox: the key is not %d bytes of base64", KeyBytes)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext, bound to aad.
func (b *Box) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secretbox: %w", err)
	}
	out := append([]byte{formatV1}, nonce...)
	return b.aead.Seal(out, nonce, plaintext, aad), nil
}

// ErrOpen is every failure to open: a wrong key, a wrong binding and a
// tampered ciphertext are indistinguishable on purpose.
var ErrOpen = errors.New("secretbox: the sealed value does not open under this key and binding")

// Open decrypts a sealed value bound to aad.
func (b *Box) Open(sealed, aad []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < 1+n+b.aead.Overhead() || sealed[0] != formatV1 {
		return nil, ErrOpen
	}
	plain, err := b.aead.Open(nil, sealed[1:1+n], sealed[1+n:], aad)
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}
