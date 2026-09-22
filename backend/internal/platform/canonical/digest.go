package canonical

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// DigestPrefix names the profile-and-algorithm pair (ADR-0011 §2.3). A future
// zt2: can coexist with it, and a bare hex string never appears in a field
// typed as a digest — which is why Digest is a type rather than a string alias.
const DigestPrefix = "zt1:"

// Digest is a canonical digest: SHA-256 over the canonical bytes, lowercase
// hex, carrying its algorithm prefix.
type Digest struct{ s string }

// String returns the prefixed form, such as
// zt1:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08.
func (d Digest) String() string { return d.s }

// IsZero reports whether this is the zero digest, which names nothing.
func (d Digest) IsZero() bool { return d.s == "" }

// Sum digests a value in canonical form. It is the only way to produce a
// Digest from a document, so nothing can digest a non-canonical encoding by
// accident.
func Sum(v Value) (Digest, error) {
	b, err := Encode(v)
	if err != nil {
		return Digest{}, err
	}
	return SumBytes(b), nil
}

// SumBytes digests bytes that are already canonical. It exists for the content
// bundle and the golden-vector loader, which hold canonical bytes on disk and
// must not re-encode them — re-encoding would mean the digest attests to this
// build's serializer rather than to the bytes that were reviewed.
func SumBytes(canonicalBytes []byte) Digest {
	sum := sha256.Sum256(canonicalBytes)
	return Digest{s: DigestPrefix + hex.EncodeToString(sum[:])}
}

// ParseDigest reads a digest from its prefixed string form, rejecting anything
// that is not this profile.
func ParseDigest(s string) (Digest, error) {
	rest, ok := strings.CutPrefix(s, DigestPrefix)
	if !ok {
		return Digest{}, fmt.Errorf("canonical: digest %q does not carry the %s prefix", s, DigestPrefix)
	}
	if len(rest) != sha256.Size*2 {
		return Digest{}, fmt.Errorf("canonical: digest %q is not %d hex characters", s, sha256.Size*2)
	}
	if _, err := hex.DecodeString(rest); err != nil {
		return Digest{}, fmt.Errorf("canonical: digest %q is not lowercase hex: %w", s, err)
	}
	if rest != strings.ToLower(rest) {
		return Digest{}, fmt.Errorf("canonical: digest %q is not lowercase", s)
	}
	return Digest{s: s}, nil
}

// Equal reports whether two digests are the same. It compares the full
// prefixed string, so a zt1 and a future zt2 digest of the same document are
// correctly unequal — they attest under different profiles.
func (d Digest) Equal(other Digest) bool { return d.s == other.s }

// RFC 6962 domain separation. Without it, an interior node can be forged as a
// leaf and a period seal becomes forgeable, which would make a seal worthless.
// It is one byte and it is not optional (ADR-0011 §2.4).
const (
	merkleLeafPrefix     = 0x00
	merkleInteriorPrefix = 0x01
)

// MerkleRoot computes the RFC 6962 root over leaves in the given order.
//
// Leaf order is the canonical order of the sealed records and is declared per
// seal type; this function does not sort, for the same reason Array does not.
//
// The empty tree is the SHA-256 of the empty string, per RFC 6962 §2.1. That is
// a real answer rather than an error: a period with no sealable records is a
// legitimate period, and it still gets a seal.
func MerkleRoot(leaves []Digest) (Digest, error) {
	if len(leaves) == 0 {
		return SumBytes(nil), nil
	}

	level := make([][]byte, len(leaves))
	for i, leaf := range leaves {
		raw, err := leaf.raw()
		if err != nil {
			return Digest{}, fmt.Errorf("canonical: merkle leaf %d: %w", i, err)
		}
		h := sha256.New()
		h.Write([]byte{merkleLeafPrefix})
		h.Write(raw)
		level[i] = h.Sum(nil)
	}

	for len(level) > 1 {
		next := make([][]byte, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			if i+1 == len(level) {
				// An odd node is promoted unchanged, per RFC 6962. It is not
				// duplicated: duplicating it is the Bitcoin variant and admits
				// a distinct-tree collision.
				next = append(next, level[i])
				continue
			}
			h := sha256.New()
			h.Write([]byte{merkleInteriorPrefix})
			h.Write(level[i])
			h.Write(level[i+1])
			next = append(next, h.Sum(nil))
		}
		level = next
	}

	return Digest{s: DigestPrefix + hex.EncodeToString(level[0])}, nil
}

// raw decodes the hex payload for hashing.
func (d Digest) raw() ([]byte, error) {
	rest, ok := strings.CutPrefix(d.s, DigestPrefix)
	if !ok {
		return nil, fmt.Errorf("digest %q does not carry the %s prefix", d.s, DigestPrefix)
	}
	return hex.DecodeString(rest)
}
