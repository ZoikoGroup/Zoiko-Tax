package bundle_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/content/bundle"
	"github.com/zoikogroup/zoikotax/backend/internal/content/compile"
	"github.com/zoikogroup/zoikotax/backend/internal/content/dsl"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/sourcing"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/kms"
)

func signerFor(t *testing.T, keyID string) (*kms.LocalSigner, kms.PublicKey) {
	t.Helper()
	pemBytes, err := kms.GenerateDevelopmentKey("development")
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	s, err := kms.NewLocalSigner("development", pemBytes, keyID, validFrom, validTo)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	e, err := s.PublicKeyEntry()
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return s, e
}

// fourEyes is a manifest, a release signer, an author, an approver and a
// keyring holding all three public keys.
type fourEyes struct {
	manifest                  []byte
	release, author, approver *kms.LocalSigner
	ring                      *kms.Keyring
}

func newFourEyes(t *testing.T) fourEyes {
	t.Helper()
	manifest, _, _ := fixture(t)
	var f fourEyes
	f.manifest = manifest
	var keys []kms.PublicKey
	for _, k := range []struct {
		dst **kms.LocalSigner
		id  string
	}{{&f.release, "content-release"}, {&f.author, "author-key"}, {&f.approver, "approver-key"}} {
		s, e := signerFor(t, k.id)
		*k.dst = s
		keys = append(keys, e)
	}
	raw, err := kms.MarshalKeyring(keys)
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	if f.ring, err = kms.ParseKeyring(raw); err != nil {
		t.Fatalf("keyring: %v", err)
	}
	return f
}

func (f fourEyes) approve(t *testing.T, s *kms.LocalSigner, role content.Role, principal string) bundle.Approval {
	t.Helper()
	a, err := bundle.Approve(context.Background(), s, f.manifest, bundle.ApprovalStatement{
		BundleID: "seal-fixture", Role: role, Principal: principal, ApprovedAt: issuedAt.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	return a
}

func (f fourEyes) seal(t *testing.T, approvals ...bundle.Approval) bundle.Seal {
	t.Helper()
	p := payloadFor()
	p.Approvals = approvals
	s, err := bundle.Sign(context.Background(), f.release, f.manifest, p)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Through the on-disk form, as a cell reads it.
	enc, err := bundle.EncodeSeal(s)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if s, err = bundle.DecodeSeal(enc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return s
}

func TestApprovalsTravelInsideTheSeal(t *testing.T) {
	ctx := context.Background()
	f := newFourEyes(t)
	author := f.approve(t, f.author, content.RoleAuthor, "analyst:1")
	approver := f.approve(t, f.approver, content.RoleApprover, "counsel:1")

	// Order of the approvals given to Sign does not reach the bytes.
	a := f.seal(t, author, approver)
	b := f.seal(t, approver, author)
	pa, _ := base64.StdEncoding.DecodeString(a.Payload)
	pb, _ := base64.StdEncoding.DecodeString(b.Payload)
	if !bytes.Equal(pa, pb) {
		t.Error("the approval order given to Sign changed the signed payload")
	}

	p, err := bundle.Verify(ctx, f.ring, a, f.manifest, issuedAt)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	got, err := bundle.VerifyApprovals(ctx, f.ring, p, issuedAt)
	if err != nil {
		t.Fatalf("approvals: %v", err)
	}
	if err := content.CheckFourEyes(got, a.Signature.KeyID); err != nil {
		t.Fatalf("four-eyes over verified approvals: %v", err)
	}
}

func TestASealWithoutApprovalsIsUnchanged(t *testing.T) {
	// A seal made before approvals existed must still verify: the approvals
	// member is absent from the canonical payload, not an empty array.
	f := newFourEyes(t)
	s := f.seal(t)
	raw, _ := base64.StdEncoding.DecodeString(s.Payload)
	if strings.Contains(string(raw), "approvals") {
		t.Fatalf("an unapproved seal payload mentions approvals: %s", raw)
	}
	if _, err := bundle.Verify(context.Background(), f.ring, s, f.manifest, issuedAt); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestForgedApprovalsAreRefusedBySealVerification(t *testing.T) {
	ctx := context.Background()
	f := newFourEyes(t)
	author := f.approve(t, f.author, content.RoleAuthor, "analyst:1")

	t.Run("an approval whose statement was edited", func(t *testing.T) {
		raw, _ := base64.StdEncoding.DecodeString(author.Payload)
		edited := author
		edited.Payload = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(raw), "analyst:1", "analyst:2", 1)))
		s := f.seal(t, edited)
		if _, err := bundle.Verify(ctx, f.ring, s, f.manifest, issuedAt); !errors.Is(err, kms.ErrInvalidSignature) {
			t.Fatalf("got %v, want an invalid signature", err)
		}
	})

	t.Run("an approval of a different manifest", func(t *testing.T) {
		prog, err := dsl.Parse("other.ztax", strings.Replace(source, "0.2100", "0.2500", 1))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		m, err := compile.Compile(prog)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		other, err := bundle.EncodeManifest(m)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		lifted, err := bundle.Approve(ctx, f.author, other, bundle.ApprovalStatement{
			BundleID: "seal-fixture", Role: content.RoleApprover, Principal: "counsel:1", ApprovedAt: issuedAt.Add(-time.Hour),
		})
		if err != nil {
			t.Fatalf("approve: %v", err)
		}
		s := f.seal(t, author, lifted)
		if _, err := bundle.Verify(ctx, f.ring, s, f.manifest, issuedAt); err == nil || !strings.Contains(err.Error(), "covers") {
			t.Fatalf("got %v, want a refusal naming the manifest the approval covers", err)
		}
	})

	t.Run("an approval dated after the seal", func(t *testing.T) {
		late, err := bundle.Approve(ctx, f.approver, f.manifest, bundle.ApprovalStatement{
			BundleID: "seal-fixture", Role: content.RoleApprover, Principal: "counsel:1", ApprovedAt: issuedAt.Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("approve: %v", err)
		}
		s := f.seal(t, author, late)
		if _, err := bundle.Verify(ctx, f.ring, s, f.manifest, issuedAt); err == nil || !strings.Contains(err.Error(), "after the seal") {
			t.Fatalf("got %v, want a refusal of a post-dated approval", err)
		}
	})

	t.Run("an AI principal", func(t *testing.T) {
		if _, err := bundle.Approve(ctx, f.approver, f.manifest, bundle.ApprovalStatement{
			BundleID: "seal-fixture", Role: content.RoleApprover, Principal: "ai:proposer", ApprovedAt: issuedAt,
		}); err == nil {
			t.Fatal("an AI principal signed an approval (ZTAX-CONT-REQ-0012)")
		}
	})
}

// TestPackSectionRoundTrips: a manifest with a pack section is canonical, and
// one without is byte-identical to what it was before packs existed.
func TestPackSectionRoundTrips(t *testing.T) {
	prog, err := dsl.Parse("seal.ztax", source)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m, err := compile.Compile(prog)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	without, err := bundle.EncodeManifest(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(string(without), `"pack"`) {
		t.Fatal("a manifest with no pack section encodes a pack member")
	}

	m.Pack = &content.PackManifest{
		ID: "ZTAX-CP-TEST", Version: content.Version{Major: 1, Minor: 2}, Level: content.LevelNational, Status: content.PackResearch,
		Capabilities:    []content.Capability{content.CapabilityDetermine, content.CapabilityClassify},
		DeploymentModes: []sourcing.DeploymentMode{sourcing.DeployZoikoOnly},
		Dependencies: []content.Dependency{
			{Pack: "ZTAX-CP-REGIONAL", Constraint: content.MustConstraint("^2.1")},
			{Pack: "ZTAX-CP-GLOBAL", Constraint: content.MustConstraint(">=1.4.0 <2")},
		},
		Sources: []content.SourceDependency{{Source: "SRC-TEST-0001", LicenceRef: "LIC-1", RecordDigest: "zt1:00", Uses: []sourcing.Right{sourcing.RightQuote}}},
	}
	with, err := bundle.EncodeManifest(m)
	if err != nil {
		t.Fatalf("encode with pack: %v", err)
	}
	decoded, digest, err := bundle.DecodeManifest(with)
	if err != nil {
		t.Fatalf("decode with pack: %v", err)
	}
	if digest != canonical.SumBytes(with) {
		t.Error("digest is not over the bytes as read")
	}
	if decoded.Pack == nil || decoded.Pack.Dependencies[0].Pack != "ZTAX-CP-GLOBAL" ||
		decoded.Pack.Dependencies[1].Constraint.String() != "^2.1" {
		t.Errorf("pack section did not survive, or is not ordered by key: %+v", decoded.Pack)
	}

	m.Pack.Level = "CONTINENTAL"
	if _, err := bundle.EncodeManifest(m); err == nil {
		t.Error("an invalid pack section was encoded for signing")
	}
}
