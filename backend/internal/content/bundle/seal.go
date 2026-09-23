package bundle

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/kms"
)

// SealPayload is what a content-bundle signature actually covers.
//
// ADR-0011 §2.5 says a signature covers the root, the seal metadata, the period
// identity, the cell identity and the canon profile version — never the
// artifact alone. The reason is substitution: a signature over a bare digest is
// a signature that can be lifted onto a different bundle, in a different cell,
// under a different profile, and still verify. Every field here exists to make
// one such substitution impossible.
type SealPayload struct {
	// Artifact distinguishes a content-bundle seal from an evidence-period
	// seal signed by the same hierarchy.
	Artifact string `json:"artifact"`
	// BundleID must match the manifest's, so a seal cannot be moved to another
	// bundle that happens to digest the same way — which it cannot, but a
	// verifier should not be resting on that.
	BundleID string `json:"bundleId"`
	// ManifestDigest is the canon/v1 digest of the manifest bytes.
	ManifestDigest string `json:"manifestDigest"`
	// IRVersion is the instruction-set version the bundle was compiled for. It
	// is signed so that a runtime's refusal to load (ADR-0005 §2.8) rests on
	// something nobody can edit.
	IRVersion int `json:"irVersion"`
	// CanonProfile is the profile the digest was taken under. A future canon/v2
	// digest of the same bundle is a different digest, and a verifier that did
	// not check the profile would compare the two.
	CanonProfile string `json:"canonProfile"`
	// ContentVersion is the CONTENT train version this pack was released at. It
	// travels into the release evidence manifest beside the other six trains.
	ContentVersion string `json:"contentVersion"`
	// Cell scopes the seal to one cell, or is empty for a bundle valid in every
	// cell. Residency content is cell-scoped; a rate table generally is not.
	Cell string `json:"cell,omitempty"`
	// IssuedAt is when the seal was made. It is the instant a cell judges key
	// validity at, so that a bundle signed last year by a key that has since
	// expired still loads — which it must, or every rotation would invalidate
	// the content estate.
	IssuedAt time.Time `json:"issuedAt"`
}

// Seal is a detached signature over a manifest.
//
// The payload is carried base64-encoded rather than as a nested object, so that
// the bytes that were signed are the bytes that are verified — exactly, with no
// re-encoding step between them. A seal format that re-serializes its payload
// before verifying is a format whose signature covers the verifier's serializer
// rather than the signer's document.
type Seal struct {
	// Payload is the base64 of the canonical SealPayload bytes.
	Payload string `json:"payload"`
	// Signature is over those bytes.
	Signature kms.Signature `json:"signature"`
}

// EncodeSealPayload renders a payload as the canonical bytes that get signed.
func EncodeSealPayload(p SealPayload) ([]byte, error) {
	if p.Artifact != Artifact {
		return nil, fmt.Errorf("bundle: seal payload names artifact %q, want %q", p.Artifact, Artifact)
	}
	if p.BundleID == "" || p.ManifestDigest == "" || p.ContentVersion == "" {
		return nil, fmt.Errorf("bundle: seal payload for %q needs a bundle id, a manifest digest and a content version", p.BundleID)
	}
	if p.CanonProfile != canonical.ProfileVersion {
		return nil, fmt.Errorf("bundle: seal payload names profile %q, this build writes %q", p.CanonProfile, canonical.ProfileVersion)
	}
	if _, err := canonical.ParseDigest(p.ManifestDigest); err != nil {
		return nil, fmt.Errorf("bundle: seal payload: %w", err)
	}
	if p.IssuedAt.IsZero() {
		return nil, fmt.Errorf("bundle: seal payload for %q names no issue instant", p.BundleID)
	}

	return canonical.Encode(canonical.Object(
		canonical.F("artifact", canonical.String(p.Artifact)),
		canonical.F("bundleId", canonical.String(p.BundleID)),
		canonical.F("manifestDigest", canonical.String(p.ManifestDigest)),
		canonical.F("irVersion", canonical.Integer(int64(p.IRVersion))),
		canonical.F("canonProfile", canonical.String(p.CanonProfile)),
		canonical.F("contentVersion", canonical.String(p.ContentVersion)),
		canonical.F("cell", canonical.OptString(p.Cell)),
		canonical.F("issuedAt", canonical.Time(p.IssuedAt)),
	))
}

// Sign produces a seal over manifest bytes.
//
// It digests the bytes it was given rather than re-encoding a manifest, which
// is the same discipline the loader follows on the other side: the signature
// ends up covering the file that ships, not a reconstruction of it.
func Sign(ctx context.Context, signer kms.Signer, manifestBytes []byte, p SealPayload) (Seal, error) {
	digest := canonical.SumBytes(manifestBytes)
	if p.ManifestDigest == "" {
		p.ManifestDigest = digest.String()
	}
	if p.ManifestDigest != digest.String() {
		// The caller stated a digest and the bytes disagree. Signing the
		// caller's version would produce a seal that verifies against a file
		// nobody has.
		return Seal{}, fmt.Errorf("bundle: seal payload claims digest %s, the manifest bytes digest to %s",
			p.ManifestDigest, digest)
	}

	payload, err := EncodeSealPayload(p)
	if err != nil {
		return Seal{}, err
	}
	sig, err := signer.Sign(ctx, payload)
	if err != nil {
		return Seal{}, fmt.Errorf("bundle: seal %s: %w", p.BundleID, err)
	}
	return Seal{Payload: base64.StdEncoding.EncodeToString(payload), Signature: sig}, nil
}

// EncodeSeal renders a seal for disk.
//
// A seal is not digested by anything, so encoding/json is the right tool and
// ADR-0011 §2.7's prohibition does not reach it. The *payload* inside it is
// canonical, and that is the part that matters.
func EncodeSeal(s Seal) ([]byte, error) {
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("bundle: encode seal: %w", err)
	}
	return append(out, '\n'), nil
}

// DecodeSeal reads a seal file. It does not verify anything: verification needs
// a verifier and an instant, and separating the two keeps it impossible to read
// a seal and forget to check it — Verify is the only function that returns a
// payload a caller may act on.
func DecodeSeal(data []byte) (Seal, error) {
	var s Seal
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Seal{}, fmt.Errorf("bundle: decode seal: %w", err)
	}
	if dec.More() {
		return Seal{}, fmt.Errorf("bundle: seal file carries more than one document")
	}
	return s, nil
}

// Verify checks a seal against manifest bytes and returns the payload.
//
// The order of the steps is the security property, so it is worth stating:
//
//  1. Decode the payload from base64. This is the only processing of unverified
//     input, and base64 is the smallest surface that can carry bytes.
//  2. Verify the signature over those bytes. Nothing has been interpreted yet,
//     so nothing an attacker chose has reached a parser.
//  3. Only then parse the payload, and check it is canonical — a payload that
//     verifies but is not canonical is a payload some other implementation
//     would read differently.
//  4. Check the artifact kind, the profile and the digest.
//
// A verifier that parsed first and verified second would be making decisions on
// attacker-controlled structure. It is the same order a signature library uses,
// and it is worth keeping when the format is ours.
func Verify(ctx context.Context, v kms.Verifier, s Seal, manifestBytes []byte, at time.Time) (SealPayload, error) {
	payload, err := base64.StdEncoding.DecodeString(s.Payload)
	if err != nil {
		return SealPayload{}, fmt.Errorf("bundle: seal payload is not base64: %w", err)
	}
	if len(payload) == 0 {
		return SealPayload{}, fmt.Errorf("bundle: seal carries no payload")
	}

	// The instant is the caller's, never the seal's. A seal that named the
	// instant its own key is judged at could sit forever inside a compromised
	// key's window. A cell passes the current instant; a replay passes the
	// historical one and gets the historical answer (ADR-0003 §2.5).
	if err := v.Verify(ctx, s.Signature, payload, at); err != nil {
		return SealPayload{}, fmt.Errorf("bundle: %w", err)
	}

	var p SealPayload
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return SealPayload{}, fmt.Errorf("bundle: decode seal payload: %w", err)
	}
	if dec.More() {
		return SealPayload{}, fmt.Errorf("bundle: seal payload carries more than one document")
	}

	round, err := EncodeSealPayload(p)
	if err != nil {
		return SealPayload{}, err
	}
	if !bytes.Equal(round, payload) {
		return SealPayload{}, fmt.Errorf("bundle: seal payload is not in %s form", canonical.ProfileVersion)
	}

	digest := canonical.SumBytes(manifestBytes)
	if p.ManifestDigest != digest.String() {
		// The signature was good and the manifest is not the one it covers.
		// This is the substitution the seal exists to catch, and it says so.
		return SealPayload{}, fmt.Errorf("bundle: seal covers manifest %s, this manifest digests to %s",
			p.ManifestDigest, digest)
	}
	return p, nil
}
