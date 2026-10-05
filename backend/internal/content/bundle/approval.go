package bundle

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/kms"
)

// ApprovalArtifact names what an approval signature covers, for the same
// reason Artifact does for a seal: an approval of a content bundle must not be
// presentable as a seal, or as an approval of anything else signed by the same
// person's key.
const ApprovalArtifact = "content-approval"

// ApprovalStatement is what one principal signs when they approve a bundle:
// "I, in this role, approve the manifest with exactly this digest".
//
// This is the Z4 content plane's four-eyes governance (ZTAX-DOM-001 Z4,
// CONT-001 §8) as a format. Each approval is signed by the approver's *own*
// key, not by the release signer on their behalf, because a release signer
// that recorded two names would be one pair of eyes reporting two. The
// statement names the manifest digest, not the bundle identity alone, so an
// approval cannot be carried forward onto a recompiled manifest that kept its
// name and changed its law.
type ApprovalStatement struct {
	Artifact       string       `json:"artifact"`
	BundleID       string       `json:"bundleId"`
	ManifestDigest string       `json:"manifestDigest"`
	Role           content.Role `json:"role"`
	Principal      string       `json:"principal"`
	ApprovedAt     time.Time    `json:"approvedAt"`
}

// Approval is a signed statement, carried the way a Seal is: the canonical
// statement bytes base64-encoded beside a detached signature, so the bytes
// verified are the bytes signed with no re-encoding in between.
type Approval struct {
	Payload   string        `json:"payload"`
	Signature kms.Signature `json:"signature"`
}

// EncodeApprovalStatement renders a statement as the canonical bytes that get
// signed.
func EncodeApprovalStatement(s ApprovalStatement) ([]byte, error) {
	if s.Artifact != ApprovalArtifact {
		return nil, fmt.Errorf("bundle: approval names artifact %q, want %q", s.Artifact, ApprovalArtifact)
	}
	if s.BundleID == "" || s.ManifestDigest == "" {
		return nil, fmt.Errorf("bundle: an approval names the bundle and the manifest digest it approves")
	}
	if _, err := canonical.ParseDigest(s.ManifestDigest); err != nil {
		return nil, fmt.Errorf("bundle: approval: %w", err)
	}
	if !s.Role.Valid() {
		return nil, fmt.Errorf("bundle: approval role %q is not one CONT-001 §8 defines", s.Role)
	}
	if err := content.ValidatePrincipal(s.Principal); err != nil {
		return nil, fmt.Errorf("bundle: approval: %w", err)
	}
	if s.ApprovedAt.IsZero() {
		return nil, fmt.Errorf("bundle: approval by %s names no instant", s.Principal)
	}
	return canonical.Encode(canonical.Object(
		canonical.F("artifact", canonical.String(s.Artifact)),
		canonical.F("bundleId", canonical.String(s.BundleID)),
		canonical.F("manifestDigest", canonical.String(s.ManifestDigest)),
		canonical.F("role", canonical.String(string(s.Role))),
		canonical.F("principal", canonical.String(s.Principal)),
		canonical.F("approvedAt", canonical.Time(s.ApprovedAt)),
	))
}

// Approve signs an approval of manifest bytes with the approver's key.
//
// Like Sign, it digests the bytes it was given rather than trusting a digest
// it was told, so an approval always covers a file that exists.
func Approve(ctx context.Context, signer kms.Signer, manifestBytes []byte, s ApprovalStatement) (Approval, error) {
	digest := canonical.SumBytes(manifestBytes).String()
	if s.ManifestDigest == "" {
		s.ManifestDigest = digest
	}
	if s.ManifestDigest != digest {
		return Approval{}, fmt.Errorf("bundle: approval claims digest %s, the manifest bytes digest to %s", s.ManifestDigest, digest)
	}
	s.Artifact = ApprovalArtifact
	payload, err := EncodeApprovalStatement(s)
	if err != nil {
		return Approval{}, err
	}
	sig, err := signer.Sign(ctx, payload)
	if err != nil {
		return Approval{}, fmt.Errorf("bundle: approve %s as %s: %w", s.BundleID, s.Role, err)
	}
	return Approval{Payload: base64.StdEncoding.EncodeToString(payload), Signature: sig}, nil
}

// DecodeApprovalStatement reads an approval's statement without verifying it.
// It checks the statement is canonical, for the reason Verify checks a seal
// payload is: a statement that decodes but is not canonical is one another
// implementation would read differently.
func DecodeApprovalStatement(a Approval) (ApprovalStatement, []byte, error) {
	payload, err := base64.StdEncoding.DecodeString(a.Payload)
	if err != nil || len(payload) == 0 {
		return ApprovalStatement{}, nil, fmt.Errorf("bundle: approval payload is not base64 or is empty")
	}
	var s ApprovalStatement
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return ApprovalStatement{}, nil, fmt.Errorf("bundle: decode approval: %w", err)
	}
	if dec.More() {
		return ApprovalStatement{}, nil, fmt.Errorf("bundle: approval carries more than one document")
	}
	round, err := EncodeApprovalStatement(s)
	if err != nil {
		return ApprovalStatement{}, nil, err
	}
	if !bytes.Equal(round, payload) {
		return ApprovalStatement{}, nil, fmt.Errorf("bundle: approval is not in %s form", canonical.ProfileVersion)
	}
	return s, payload, nil
}

// VerifyApproval checks one approval: the signature first, over the bytes as
// carried, and only then the statement — the same order Verify follows for a
// seal, for the same reason.
func VerifyApproval(ctx context.Context, v kms.Verifier, a Approval, bundleID, manifestDigest string, at time.Time) (content.Approval, error) {
	payload, err := base64.StdEncoding.DecodeString(a.Payload)
	if err != nil || len(payload) == 0 {
		return content.Approval{}, fmt.Errorf("bundle: approval payload is not base64 or is empty")
	}
	if err := v.Verify(ctx, a.Signature, payload, at); err != nil {
		return content.Approval{}, fmt.Errorf("bundle: approval: %w", err)
	}
	s, _, err := DecodeApprovalStatement(a)
	if err != nil {
		return content.Approval{}, err
	}
	if s.BundleID != bundleID || s.ManifestDigest != manifestDigest {
		// A good signature over a different manifest: an approval lifted from
		// an earlier compilation. This is the substitution the digest in the
		// statement exists to catch.
		return content.Approval{}, fmt.Errorf("bundle: %s's approval covers %s %s, not %s %s",
			s.Principal, s.BundleID, s.ManifestDigest, bundleID, manifestDigest)
	}
	return content.Approval{Role: s.Role, Principal: s.Principal, KeyID: a.Signature.KeyID, At: s.ApprovedAt}, nil
}

// VerifyApprovals verifies every approval a seal payload carries and returns
// them as the four-eyes rule reads them. It does not apply the rule: whether
// four eyes are *required* is the caller's policy (the loader requires it
// outside development), but whether the approvals that are present are
// genuine is not anybody's policy — a forged approval is refused everywhere.
func VerifyApprovals(ctx context.Context, v kms.Verifier, p SealPayload, at time.Time) ([]content.Approval, error) {
	out := make([]content.Approval, 0, len(p.Approvals))
	for _, a := range p.Approvals {
		ap, err := VerifyApproval(ctx, v, a, p.BundleID, p.ManifestDigest, at)
		if err != nil {
			return nil, err
		}
		if ap.At.After(p.IssuedAt) {
			// An approval dated after the seal it is inside is an approval
			// nobody had given when the release was signed.
			return nil, fmt.Errorf("bundle: %s approved at %s, after the seal was issued at %s",
				ap.Principal, canonical.FormatTime(ap.At), canonical.FormatTime(p.IssuedAt))
		}
		out = append(out, ap)
	}
	return out, nil
}

// EncodeApprovals renders the approvals file that sits beside a manifest
// between `ztax-contentc approve` and `ztax-contentc sign`. Like a seal file it
// is not digested, so encoding/json is the right tool; each entry's payload is
// canonical, and that is the part that is signed.
func EncodeApprovals(as []Approval) ([]byte, error) {
	sorted := sortApprovals(as)
	out, err := json.MarshalIndent(sorted, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("bundle: encode approvals: %w", err)
	}
	return append(out, '\n'), nil
}

// DecodeApprovals reads an approvals file.
func DecodeApprovals(data []byte) ([]Approval, error) {
	var as []Approval
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&as); err != nil {
		return nil, fmt.Errorf("bundle: decode approvals: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("bundle: approvals file carries more than one document")
	}
	return as, nil
}

// sortApprovals orders approvals by payload. The payload is canonical, so
// ordering by it is ordering by (artifact, approvedAt, bundleId, …) as
// canon/v1 lays the members out — arbitrary, but fixed, which is all P4 asks
// of a collection whose order carries no meaning.
func sortApprovals(as []Approval) []Approval {
	out := append([]Approval(nil), as...)
	sort.Slice(out, func(i, j int) bool { return out[i].Payload < out[j].Payload })
	return out
}

// canonicalApprovals renders the approvals inside a seal payload, or Absent
// when there are none — which keeps a seal without approvals byte-identical to
// one written before approvals existed.
func canonicalApprovals(as []Approval) canonical.Value {
	if len(as) == 0 {
		return canonical.Absent()
	}
	items := make([]canonical.Value, 0, len(as))
	for _, a := range sortApprovals(as) {
		items = append(items, canonical.Object(
			canonical.F("payload", canonical.String(a.Payload)),
			canonical.F("signature", canonicalSignature(a.Signature)),
		))
	}
	return canonical.Array(items...)
}

// canonicalSignature renders a kms.Signature in the shape encoding/json gives
// it, so that a seal payload decoded into SealPayload and re-encoded here is
// byte-identical: the key identifier, algorithm and window as strings, the
// signature bytes as standard base64 — which is how encoding/json renders a
// []byte.
func canonicalSignature(s kms.Signature) canonical.Value {
	return canonical.Object(
		canonical.F("keyId", canonical.String(s.KeyID)),
		canonical.F("algorithm", canonical.String(string(s.Algorithm))),
		canonical.F("notBefore", canonical.Time(s.NotBefore)),
		canonical.F("notAfter", canonical.Time(s.NotAfter)),
		canonical.F("signature", canonical.String(base64.StdEncoding.EncodeToString(s.Bytes))),
	)
}

// ApprovalsFor reads approvals and checks each covers this bundle and digest,
// *without* verifying signatures. It is for the signing side, which has the
// manifest but need not hold the approvers' public keys: it lets `sign` refuse
// to embed an approval of some other manifest, and lets it report what it is
// about to embed. Nothing may act on its result as an authorisation — the
// signatures are checked by Verify, on the cell's side, against the keyring.
func ApprovalsFor(as []Approval, bundleID, manifestDigest string) ([]content.Approval, error) {
	out := make([]content.Approval, 0, len(as))
	for _, a := range as {
		s, _, err := DecodeApprovalStatement(a)
		if err != nil {
			return nil, err
		}
		if s.BundleID != bundleID || s.ManifestDigest != manifestDigest {
			return nil, fmt.Errorf("bundle: %s's approval covers %s %s, not %s %s",
				s.Principal, s.BundleID, s.ManifestDigest, bundleID, manifestDigest)
		}
		out = append(out, content.Approval{Role: s.Role, Principal: s.Principal, KeyID: a.Signature.KeyID, At: s.ApprovedAt})
	}
	return out, nil
}
