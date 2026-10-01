package content_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptercontent "github.com/zoikogroup/zoikotax/backend/internal/adapter/content"
	"github.com/zoikogroup/zoikotax/backend/internal/content/bundle"
	"github.com/zoikogroup/zoikotax/backend/internal/content/compile"
	"github.com/zoikogroup/zoikotax/backend/internal/content/dsl"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/kms"
)

// approver is one principal with their own key.
type approver struct {
	role      content.Role
	principal string
	keyID     string
}

// writeApprovedBundle seals the fixture with the given approvals, each signed
// by its own freshly generated key, and returns the directory and a keyring
// holding the release key and every approver key.
func writeApprovedBundle(t *testing.T, approvers ...approver) (string, *kms.Keyring) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	prog, err := dsl.Parse("loader.ztax", source)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	manifest, err := compile.Compile(prog)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	manifestBytes, err := bundle.EncodeManifest(manifest)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	newSigner := func(id string) *kms.LocalSigner {
		pemBytes, err := kms.GenerateDevelopmentKey("development")
		if err != nil {
			t.Fatalf("keygen: %v", err)
		}
		s, err := kms.NewLocalSigner("development", pemBytes, id, keyFrom, keyUntil)
		if err != nil {
			t.Fatalf("signer: %v", err)
		}
		return s
	}
	release := newSigner("content-release")
	signers := map[string]*kms.LocalSigner{"content-release": release}

	var approvals []bundle.Approval
	for _, a := range approvers {
		s, ok := signers[a.keyID]
		if !ok {
			s = newSigner(a.keyID)
			signers[a.keyID] = s
		}
		ap, err := bundle.Approve(ctx, s, manifestBytes, bundle.ApprovalStatement{
			BundleID: manifest.BundleID, Role: a.role, Principal: a.principal, ApprovedAt: issuedAt.Add(-time.Hour),
		})
		if err != nil {
			t.Fatalf("approve: %v", err)
		}
		approvals = append(approvals, ap)
	}

	seal, err := bundle.Sign(ctx, release, manifestBytes, bundle.SealPayload{
		Artifact: bundle.Artifact, BundleID: manifest.BundleID, IRVersion: manifest.IRVersion,
		CanonProfile: canonical.ProfileVersion, ContentVersion: "2026.09.1", IssuedAt: issuedAt,
		Approvals: approvals,
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sealBytes, err := bundle.EncodeSeal(seal)
	if err != nil {
		t.Fatalf("encode seal: %v", err)
	}
	write(t, filepath.Join(dir, manifest.BundleID+adaptercontent.ManifestSuffix), manifestBytes)
	write(t, filepath.Join(dir, manifest.BundleID+adaptercontent.SealSuffix), sealBytes)

	var keys []kms.PublicKey
	for _, s := range signers {
		e, err := s.PublicKeyEntry()
		if err != nil {
			t.Fatalf("public key: %v", err)
		}
		keys = append(keys, e)
	}
	raw, err := kms.MarshalKeyring(keys)
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	ring, err := kms.ParseKeyring(raw)
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	return dir, ring
}

func loaderIn(env, dir string, ring kms.Verifier) *adaptercontent.Loader {
	return &adaptercontent.Loader{Dir: dir, Verifier: ring, Clock: clock.Fixed{Instant: loadAt}, Environment: env}
}

var (
	author  = approver{content.RoleAuthor, "analyst:2048", "author-key"}
	counsel = approver{content.RoleApprover, "counsel:113", "approver-key"}
)

// TestProductionActivatesOnlyFourEyesBundles is the Z4 four-eyes rule at the
// point it cannot be bypassed: the cell's own loader.
func TestProductionActivatesOnlyFourEyesBundles(t *testing.T) {
	ctx := context.Background()

	t.Run("an author and a distinct approver", func(t *testing.T) {
		dir, ring := writeApprovedBundle(t, author, counsel)
		holder := &rule.Holder{}
		loaded, err := loaderIn("production", dir, ring).Activate(ctx, holder)
		if err != nil {
			t.Fatalf("activate: %v", err)
		}
		if holder.Current() == nil || len(loaded.Approvals) != 2 {
			t.Fatalf("loaded %d approvals, published %v", len(loaded.Approvals), holder.Current() != nil)
		}
	})

	refusals := map[string][]approver{
		"no approvals at all":           nil,
		"the author alone":              {author},
		"one person, both roles":        {author, {content.RoleApprover, "analyst:2048", "approver-key"}},
		"two names, one key":            {author, {content.RoleApprover, "counsel:113", "author-key"}},
		"approved with the release key": {author, {content.RoleApprover, "counsel:113", "content-release"}},
	}
	for name, approvers := range refusals {
		t.Run(name, func(t *testing.T) {
			dir, ring := writeApprovedBundle(t, approvers...)
			holder := &rule.Holder{}
			_, err := loaderIn("production", dir, ring).Activate(ctx, holder)
			if err == nil || !strings.Contains(err.Error(), "four-eyes") {
				t.Fatalf("got %v, want a four-eyes refusal", err)
			}
			if holder.Current() != nil {
				t.Fatal("an unapproved bundle reached the Holder")
			}
		})
	}
}

// TestAnUnsetEnvironmentIsStrict: the permissive branch is "development" by
// name, so an empty or misspelled environment refuses an unapproved bundle.
func TestAnUnsetEnvironmentIsStrict(t *testing.T) {
	dir, ring := writeApprovedBundle(t)
	for _, env := range []string{"", "Development", "dev", "staging"} {
		if _, err := loaderIn(env, dir, ring).Load(context.Background()); err == nil {
			t.Errorf("environment %q loaded an unapproved bundle", env)
		}
	}
}

// TestDevelopmentStaysUsable: an unapproved local build loads in development,
// but approvals that are present are held to the rule even there.
func TestDevelopmentStaysUsable(t *testing.T) {
	ctx := context.Background()
	dir, ring := writeApprovedBundle(t)
	if _, err := loaderIn(adaptercontent.Development, dir, ring).Load(ctx); err != nil {
		t.Fatalf("development refused an unapproved local bundle: %v", err)
	}

	half, halfRing := writeApprovedBundle(t, author)
	if _, err := loaderIn(adaptercontent.Development, half, halfRing).Load(ctx); err == nil {
		t.Error("development loaded a half-approved bundle")
	}
}

// TestApprovalsVerifyAgainstTheKeyring: a cell that does not hold an
// approver's public key cannot confirm the approval, and refuses.
func TestApprovalsVerifyAgainstTheKeyring(t *testing.T) {
	dir, _ := writeApprovedBundle(t, author, counsel)
	_, otherRing := writeApprovedBundle(t, author, counsel)
	if _, err := loaderIn("production", dir, otherRing).Load(context.Background()); err == nil {
		t.Fatal("approvals signed by keys this cell does not hold were accepted")
	}
}
