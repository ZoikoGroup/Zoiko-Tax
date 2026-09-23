// Package kms is the signing and verification boundary (ADR-0017 §2.6).
//
// The shape of this package is the decision it encodes: signing is an *API
// call*, and the private key has no in-process representation. Signer is
// therefore an interface with one method that takes bytes and returns a
// signature, because that is all a caller may know about a key. A KMS or HSM
// implementation slots in at the call site in cmd/ztax-contentc without
// changing anything that produces a payload.
//
// Verification is deliberately a different shape. ADR-0017 §2.7 requires a
// historical signature to verify against the key it was made with, resolved by
// identifier and not by "the current key" — so Verifier takes the signature's
// own key identifier and the instant to judge validity at. Rotation then does
// not touch sealed evidence, which it must not, because sealed evidence is
// immutable.
//
// Three key hierarchies exist and they do not overlap (ADR-0017 §2.6):
// evidence-seal signing, content-bundle signing and artifact signing. This
// package carries no notion of which is which — that is the key policy's job,
// and a key that is permitted to sign both is a policy failure rather than a
// code failure.
package kms

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Algorithm names a signature suite by name, never by ordinal, so a signature
// recorded today is still legible after a suite is added.
type Algorithm string

// AlgorithmECDSAP384SHA384 is the suite ADR-0011 §2.5 mandates: ECDSA on the
// P-384 curve over a SHA-384 digest of the payload.
//
// The digest is SHA-384 rather than the SHA-256 used for content digests
// (ADR-0011 §2.3) because the hash under a signature should match the curve's
// strength; a P-384 signature over a SHA-256 digest is limited by the digest.
// The two hashes answer different questions and there is no reason for them to
// agree.
const AlgorithmECDSAP384SHA384 Algorithm = "ECDSA_P384_SHA384"

func (a Algorithm) valid() bool { return a == AlgorithmECDSAP384SHA384 }

// Signature is a signature together with everything needed to verify it later.
//
// The key identifier and validity window travel *with* the signature rather
// than being looked up at verification time. That is ADR-0017 §2.7: a seal from
// three years ago names the key that made it, and verification resolves that
// key, so rotation is routine.
type Signature struct {
	KeyID     string    `json:"keyId"`
	Algorithm Algorithm `json:"algorithm"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	// Bytes is the ASN.1 DER ECDSA signature.
	Bytes []byte `json:"signature"`
}

// Signer produces a signature over a payload.
//
// It takes a context because the production implementation is a network call to
// a KMS, and a signing call that cannot be cancelled is a signing call that can
// hang a release pipeline.
type Signer interface {
	// KeyID reports which key this signer will use, so a caller can record it
	// before signing and refuse if it is not the key it expected.
	KeyID() string
	// Sign returns a signature over payload. The payload is signed whole; this
	// interface has no notion of a digest supplied by the caller, because a
	// caller that can choose the digest can sign something it never saw.
	Sign(ctx context.Context, payload []byte) (Signature, error)
}

// Verifier checks a signature against the key it names.
type Verifier interface {
	// Verify reports whether sig is a valid signature over payload, made by a
	// key that was valid at instant at.
	//
	// The instant is a parameter rather than a clock read, for the same reason
	// decision time is (ADR-0003 §2.5): verifying a historical seal asks
	// whether the key was valid *then*, and a wall-clock read would answer a
	// different question and fail every seal signed by a retired key.
	Verify(ctx context.Context, sig Signature, payload []byte, at time.Time) error
}

// ErrUnknownKey is returned when a signature names a key the verifier does not
// hold. It is a sentinel because a caller's response differs from every other
// verification failure: an unknown key usually means an out-of-date keyring
// rather than a forgery, and the operator action is to fetch the keyring.
var ErrUnknownKey = errors.New("kms: signature names a key this verifier does not hold")

// ErrInvalidSignature is returned when the signature does not verify, when the
// key was outside its validity window at the given instant, or when the
// algorithm does not match. They are one sentinel on purpose: a caller must not
// be able to branch on *why* a signature failed, because the branch is an
// oracle and none of the answers change what the caller does — it refuses.
var ErrInvalidSignature = errors.New("kms: signature does not verify")

// ---------------------------------------------------------------------------
// keyring
// ---------------------------------------------------------------------------

// PublicKey is one entry in a keyring: a public key with the window in which
// signatures made by its private half are to be trusted.
type PublicKey struct {
	KeyID     string    `json:"keyId"`
	Algorithm Algorithm `json:"algorithm"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	// SubjectPublicKeyInfo is the base64 DER form (RFC 5280 SPKI), which is
	// what every other tool in the estate will hand us.
	SubjectPublicKeyInfo string `json:"subjectPublicKeyInfo"`
}

// Keyring is a Verifier over a fixed set of public keys.
//
// It holds public keys only, which is why it is safe to ship in an image and to
// commit next to the content it verifies. A cell needs no secret at all in
// order to refuse a bundle nobody signed.
type Keyring struct {
	keys map[string]keyringEntry
}

type keyringEntry struct {
	pub       *ecdsa.PublicKey
	algorithm Algorithm
	notBefore time.Time
	notAfter  time.Time
}

// keyringDocument is the on-disk form.
type keyringDocument struct {
	Keys []PublicKey `json:"keys"`
}

// ParseKeyring reads a keyring document.
//
//	{"keys":[{"keyId":"...","algorithm":"ECDSA_P384_SHA384",
//	          "notBefore":"...","notAfter":"...","subjectPublicKeyInfo":"<base64 DER>"}]}
//
// Unknown fields are rejected: a keyring is a security artifact, and a field
// this build does not understand means the document was authored against a
// different schema, which is a fault rather than something to ignore.
func ParseKeyring(data []byte) (*Keyring, error) {
	var doc keyringDocument
	if err := strictUnmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("kms: parse keyring: %w", err)
	}
	if len(doc.Keys) == 0 {
		// An empty keyring verifies nothing, and a caller that loaded one is
		// about to refuse every bundle for a reason it will misdiagnose.
		return nil, fmt.Errorf("kms: keyring holds no keys")
	}

	ring := &Keyring{keys: make(map[string]keyringEntry, len(doc.Keys))}
	for _, k := range doc.Keys {
		if k.KeyID == "" {
			return nil, fmt.Errorf("kms: keyring holds a key with no identifier")
		}
		if _, dup := ring.keys[k.KeyID]; dup {
			// Two keys under one identifier makes "which key signed this"
			// unanswerable, which is the one question the identifier exists to
			// answer.
			return nil, fmt.Errorf("kms: keyring declares key %q twice", k.KeyID)
		}
		if !k.Algorithm.valid() {
			return nil, fmt.Errorf("kms: key %q names unsupported algorithm %q", k.KeyID, k.Algorithm)
		}
		if k.NotBefore.IsZero() || k.NotAfter.IsZero() || !k.NotAfter.After(k.NotBefore) {
			return nil, fmt.Errorf("kms: key %q has no usable validity window", k.KeyID)
		}
		pub, err := parsePublicKey(k.SubjectPublicKeyInfo)
		if err != nil {
			return nil, fmt.Errorf("kms: key %q: %w", k.KeyID, err)
		}
		ring.keys[k.KeyID] = keyringEntry{
			pub:       pub,
			algorithm: k.Algorithm,
			notBefore: k.NotBefore.UTC(),
			notAfter:  k.NotAfter.UTC(),
		}
	}
	return ring, nil
}

// KeyIDs reports the identifiers this keyring holds, for the startup log line.
// It is the keyring's identity in an incident investigation: "which keys was
// this cell willing to trust" is otherwise unanswerable after the fact.
func (r *Keyring) KeyIDs() []string {
	out := make([]string, 0, len(r.keys))
	for id := range r.keys {
		out = append(out, id)
	}
	// Sorted, because this reaches a log line and a set that reorders between
	// restarts is a set nobody can diff.
	sortStrings(out)
	return out
}

// Verify implements Verifier.
func (r *Keyring) Verify(_ context.Context, sig Signature, payload []byte, at time.Time) error {
	entry, ok := r.keys[sig.KeyID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownKey, sig.KeyID)
	}
	if sig.Algorithm != entry.algorithm {
		return fmt.Errorf("%w: key %s is %s, signature claims %s",
			ErrInvalidSignature, sig.KeyID, entry.algorithm, sig.Algorithm)
	}

	// The keyring's window governs, not the window the signature carries. A
	// signature stating its own validity and being believed about it is a
	// signature that can extend its own key's life.
	t := at.UTC()
	if t.Before(entry.notBefore) || !t.Before(entry.notAfter) {
		return fmt.Errorf("%w: key %s was not valid at %s", ErrInvalidSignature, sig.KeyID, t.Format(time.RFC3339))
	}

	sum := sha512.Sum384(payload)
	if !ecdsa.VerifyASN1(entry.pub, sum[:], sig.Bytes) {
		return fmt.Errorf("%w: key %s", ErrInvalidSignature, sig.KeyID)
	}
	return nil
}

func parsePublicKey(b64 string) (*ecdsa.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("public key is not base64: %w", err)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("public key is not a DER SubjectPublicKeyInfo: %w", err)
	}
	pub, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is %T, want an ECDSA key", parsed)
	}
	if pub.Curve != elliptic.P384() {
		return nil, fmt.Errorf("public key is on %s, want P-384", pub.Curve.Params().Name)
	}
	return pub, nil
}

// ---------------------------------------------------------------------------
// local signer — development only
// ---------------------------------------------------------------------------

// LocalSigner signs with a private key held in this process.
//
// This is exactly the pattern ADR-0017 §2.6 forbids in a cell — the private key
// has an in-process representation, where a crash dump or a debugger can reach
// it. It exists so that `make content` works on a laptop and so that the
// compiler's tests can sign without a KMS, and NewLocalSigner refuses outright
// outside development so it cannot be the thing that signs a production pack.
//
// The same shape as secrets.EnvResolver, for the same reason: a development
// affordance that fails closed is a development affordance; one that fails open
// is a production incident with a long fuse.
type LocalSigner struct {
	key       *ecdsa.PrivateKey
	keyID     string
	notBefore time.Time
	notAfter  time.Time
}

// NewLocalSigner builds a signer from a PKCS#8 PEM private key.
//
// environment is the process's ZTAX_ENVIRONMENT and must be "development".
func NewLocalSigner(environment string, pemBytes []byte, keyID string, notBefore, notAfter time.Time) (*LocalSigner, error) {
	if environment != "development" {
		return nil, fmt.Errorf("kms: in-process signing keys are refused in environment %q; use a KMS signer (ADR-0017 §2.6)", environment)
	}
	if keyID == "" {
		return nil, fmt.Errorf("kms: a signing key needs an identifier; a signature that cannot name its key cannot be verified after rotation (ADR-0017 §2.7)")
	}
	if notBefore.IsZero() || notAfter.IsZero() || !notAfter.After(notBefore) {
		return nil, fmt.Errorf("kms: signing key %q needs a validity window", keyID)
	}

	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("kms: signing key %q is not PEM", keyID)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("kms: signing key %q is not a PKCS#8 private key: %w", keyID, err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("kms: signing key %q is %T, want an ECDSA key", keyID, parsed)
	}
	if key.Curve != elliptic.P384() {
		return nil, fmt.Errorf("kms: signing key %q is on %s, want P-384 (ADR-0011 §2.5)", keyID, key.Curve.Params().Name)
	}

	return &LocalSigner{key: key, keyID: keyID, notBefore: notBefore.UTC(), notAfter: notAfter.UTC()}, nil
}

// KeyID implements Signer.
func (s *LocalSigner) KeyID() string { return s.keyID }

// Sign implements Signer.
//
// ECDSA signing draws a random nonce, so two signatures over identical bytes
// differ. That is correct for a signature and is worth stating, because it
// means a *seal* is not byte-reproducible while the artifact it seals is — the
// reproducibility guarantee in ADR-0001 §3.3 is about the manifest digest,
// which is what a replay compares.
func (s *LocalSigner) Sign(_ context.Context, payload []byte) (Signature, error) {
	if len(payload) == 0 {
		// Signing nothing produces a signature that verifies against nothing
		// anybody meant, and it is always a caller that built no document.
		return Signature{}, fmt.Errorf("kms: refusing to sign an empty payload")
	}
	sum := sha512.Sum384(payload)
	der, err := ecdsa.SignASN1(rand.Reader, s.key, sum[:])
	if err != nil {
		return Signature{}, fmt.Errorf("kms: sign with %s: %w", s.keyID, err)
	}
	return Signature{
		KeyID:     s.keyID,
		Algorithm: AlgorithmECDSAP384SHA384,
		NotBefore: s.notBefore,
		NotAfter:  s.notAfter,
		Bytes:     der,
	}, nil
}

// PublicKeyEntry renders this signer's public half as a keyring entry, so that
// `ztax-contentc keygen` can emit a key pair and a keyring in one step and the
// two cannot disagree.
func (s *LocalSigner) PublicKeyEntry() (PublicKey, error) {
	der, err := x509.MarshalPKIXPublicKey(&s.key.PublicKey)
	if err != nil {
		return PublicKey{}, fmt.Errorf("kms: marshal public key %s: %w", s.keyID, err)
	}
	return PublicKey{
		KeyID:                s.keyID,
		Algorithm:            AlgorithmECDSAP384SHA384,
		NotBefore:            s.notBefore,
		NotAfter:             s.notAfter,
		SubjectPublicKeyInfo: base64.StdEncoding.EncodeToString(der),
	}, nil
}

// GenerateDevelopmentKey produces a P-384 key pair as PKCS#8 PEM.
//
// It is for `ztax-contentc keygen` and for tests. A production key is generated
// inside a KMS and never exists outside it, so this function has no production
// counterpart and deliberately does not grow one.
func GenerateDevelopmentKey(environment string) ([]byte, error) {
	if environment != "development" {
		return nil, fmt.Errorf("kms: key generation outside the KMS is refused in environment %q (ADR-0017 §2.6)", environment)
	}
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("kms: generate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("kms: marshal key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// MarshalKeyring renders a keyring document from public key entries. It is how
// keygen writes the file ParseKeyring reads, so the two forms have one
// definition.
func MarshalKeyring(keys []PublicKey) ([]byte, error) {
	// encoding/json is correct here and nowhere near an evidence path: a
	// keyring is configuration that is never digested, and ADR-0011 §2.7's
	// prohibition is about what gets digested.
	return json.MarshalIndent(keyringDocument{Keys: keys}, "", "  ")
}

// strictUnmarshal decodes JSON rejecting unknown fields (ADR-0011 P5 applied to
// a security artifact). It is here rather than in a shared helper because the
// only other package that needs it decodes a different document for a different
// reason, and one shared "strict decode" helper is how a rejection policy
// quietly becomes a default somebody changes once.
func strictUnmarshal(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	// A second value in the stream is a second document, and taking the first
	// silently would mean signing or trusting something nobody read.
	if dec.More() {
		return fmt.Errorf("trailing content after the document")
	}
	return nil
}

func sortStrings(s []string) { sort.Strings(s) }
