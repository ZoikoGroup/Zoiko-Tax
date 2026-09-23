package content_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptercontent "github.com/zoikogroup/zoikotax/backend/internal/adapter/content"
	"github.com/zoikogroup/zoikotax/backend/internal/content/bundle"
	"github.com/zoikogroup/zoikotax/backend/internal/content/compile"
	"github.com/zoikogroup/zoikotax/backend/internal/content/dsl"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/kms"
)

const source = `
bundle "loader-fixture"
ir 1

policy line2 = HALF_UP scale 2 basis LINE

rule "vat" version "2026.09.1" semantic "ZTAX-RULE-LOADER-FIXTURE" {
    const standard rate "0.2100" basis NET
    input net money "line.netAmount"
    let vat = apply_rate net standard policy line2
    emit "TAX_VAT" vat
}
`

var (
	keyFrom  = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	keyUntil = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	issuedAt = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	loadAt   = time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
)

// writeBundle produces the two files a cell reads, through the same functions
// ztax-contentc uses. A fixture assembled by hand would be a second, kinder
// implementation of the format, and the loader would then be tested against
// something no compiler produces.
func writeBundle(t *testing.T, cell string) (dir string, ring *kms.Keyring) {
	t.Helper()
	dir = t.TempDir()

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

	pemBytes, err := kms.GenerateDevelopmentKey("development")
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	signer, err := kms.NewLocalSigner("development", pemBytes, "content-test-2026", keyFrom, keyUntil)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	seal, err := bundle.Sign(context.Background(), signer, manifestBytes, bundle.SealPayload{
		Artifact:       bundle.Artifact,
		BundleID:       manifest.BundleID,
		IRVersion:      manifest.IRVersion,
		CanonProfile:   canonical.ProfileVersion,
		ContentVersion: "2026.09.1",
		Cell:           cell,
		IssuedAt:       issuedAt,
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

	entry, err := signer.PublicKeyEntry()
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	ringBytes, err := kms.MarshalKeyring([]kms.PublicKey{entry})
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	if ring, err = kms.ParseKeyring(ringBytes); err != nil {
		t.Fatalf("parse keyring: %v", err)
	}
	return dir, ring
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func loaderFor(dir string, ring kms.Verifier, cell string) *adaptercontent.Loader {
	return &adaptercontent.Loader{
		Dir:      dir,
		Verifier: ring,
		Clock:    clock.Fixed{Instant: loadAt},
		Cell:     cell,
	}
}

// TestActivateSwapsAVerifiedBundle is the whole path: two files on disk become
// a bundle a request can be evaluated against.
func TestActivateSwapsAVerifiedBundle(t *testing.T) {
	dir, ring := writeBundle(t, "eu-west-1")
	holder := &rule.Holder{}

	loaded, err := loaderFor(dir, ring, "eu-west-1").Activate(context.Background(), holder)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if holder.Current() == nil {
		t.Fatal("nothing was published")
	}
	if loaded.Seal.BundleID != "loader-fixture" || loaded.KeyID != "content-test-2026" {
		t.Errorf("loaded %+v", loaded.Seal)
	}
	// The digest a decision records is the seal's, and the bundle carries it.
	if holder.Current().Digest() != loaded.Digest {
		t.Errorf("bundle digest %s, seal digest %s", holder.Current().Digest(), loaded.Digest)
	}

	net, err := fiscal.ParseMoney("100.00", "EUR")
	if err != nil {
		t.Fatalf("parse money: %v", err)
	}
	got, err := rule.Evaluate(holder.Current(), rule.Frame{
		Money: map[string]fiscal.Money{"line.netAmount": net},
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if v := got.Emitted["TAX_VAT"].Money.CanonicalString(); v != "21.00" {
		t.Errorf("TAX_VAT = %s, want 21.00", v)
	}
}

// TestNothingUnverifiedIsPublished is ADR-0005 §2.6's all-or-nothing
// activation. Each case is a bundle a cell might be handed, and in every one the
// Holder must still be empty afterwards — a cell keeps the content it had.
func TestNothingUnverifiedIsPublished(t *testing.T) {
	ctx := context.Background()

	t.Run("a manifest edited after sealing", func(t *testing.T) {
		dir, ring := writeBundle(t, "")
		path := filepath.Join(dir, "loader-fixture"+adaptercontent.ManifestSuffix)
		data, err := os.ReadFile(path) // #nosec G304 -- a path this test just wrote
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		write(t, path, []byte(strings.Replace(string(data), "0.2100", "0.2500", 1)))

		holder := &rule.Holder{}
		if _, err := loaderFor(dir, ring, "eu-west-1").Activate(ctx, holder); err == nil {
			t.Fatal("activated an edited manifest")
		}
		if holder.Current() != nil {
			t.Fatal("an unverified bundle reached the Holder")
		}
	})

	t.Run("a bundle sealed for another cell", func(t *testing.T) {
		dir, ring := writeBundle(t, "eu-west-1")
		holder := &rule.Holder{}
		_, err := loaderFor(dir, ring, "us-east-1").Activate(ctx, holder)
		if err == nil || !strings.Contains(err.Error(), "sealed for cell") {
			t.Fatalf("got %v, want a refusal naming the cell", err)
		}
		if holder.Current() != nil {
			t.Fatal("a bundle for another cell reached the Holder")
		}
	})

	t.Run("a seal whose key the cell does not hold", func(t *testing.T) {
		dir, _ := writeBundle(t, "")
		// A second, unrelated keyring: what a cell has after a rotation it was
		// not told about, and what an attacker has after signing with their own
		// key.
		_, otherRing := writeBundle(t, "")
		holder := &rule.Holder{}
		if _, err := loaderFor(dir, otherRing, "eu-west-1").Activate(ctx, holder); err == nil {
			t.Fatal("activated a bundle signed by a key the cell does not hold")
		}
		if holder.Current() != nil {
			t.Fatal("an unverified bundle reached the Holder")
		}
	})

	t.Run("no verifier at all", func(t *testing.T) {
		dir, _ := writeBundle(t, "")
		holder := &rule.Holder{}
		if _, err := loaderFor(dir, nil, "eu-west-1").Activate(ctx, holder); err == nil {
			t.Fatal("loaded content with no keyring")
		}
	})

	t.Run("two bundles in one directory", func(t *testing.T) {
		dir, ring := writeBundle(t, "")
		write(t, filepath.Join(dir, "second"+adaptercontent.ManifestSuffix), []byte("{}"))
		holder := &rule.Holder{}
		_, err := loaderFor(dir, ring, "eu-west-1").Activate(ctx, holder)
		if err == nil || !strings.Contains(err.Error(), "a cell runs one") {
			t.Fatalf("got %v, want a refusal to choose between bundles", err)
		}
	})

	t.Run("a directory that is not there", func(t *testing.T) {
		loader := loaderFor(filepath.Join(t.TempDir(), "absent"), &kms.Keyring{}, "eu-west-1")
		_, err := loader.Load(ctx)
		if !adaptercontent.IsMissing(err) {
			t.Fatalf("got %v, want a missing-directory error main can distinguish", err)
		}
	})
}
