package bundle_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/content/bundle"
	"github.com/zoikogroup/zoikotax/backend/internal/content/compile"
	"github.com/zoikogroup/zoikotax/backend/internal/content/dsl"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/kms"
)

const source = `
bundle "seal-fixture"
ir 1

policy line2 = HALF_UP scale 2 basis LINE

rule "vat" version "2026.09.1" semantic "ZTAX-RULE-SEAL-FIXTURE" {
    const standard rate "0.2100" basis NET
    input net money "line.netAmount"
    let vat = apply_rate net standard policy line2
    emit "TAX_VAT" vat
}
`

var (
	validFrom = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	validTo   = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	issuedAt  = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
)

func fixture(t *testing.T) (manifest []byte, signer *kms.LocalSigner, ring *kms.Keyring) {
	t.Helper()

	prog, err := dsl.Parse("seal.ztax", source)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m, err := compile.Compile(prog)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	manifest, err = bundle.EncodeManifest(m)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	pemBytes, err := kms.GenerateDevelopmentKey("development")
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	signer, err = kms.NewLocalSigner("development", pemBytes, "content-test-2026", validFrom, validTo)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	entry, err := signer.PublicKeyEntry()
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	keyring, err := kms.MarshalKeyring([]kms.PublicKey{entry})
	if err != nil {
		t.Fatalf("marshal keyring: %v", err)
	}
	ring, err = kms.ParseKeyring(keyring)
	if err != nil {
		t.Fatalf("parse keyring: %v", err)
	}
	return manifest, signer, ring
}

func payloadFor() bundle.SealPayload {
	return bundle.SealPayload{
		Artifact:       bundle.Artifact,
		BundleID:       "seal-fixture",
		IRVersion:      1,
		CanonProfile:   canonical.ProfileVersion,
		ContentVersion: "2026.09.1",
		Cell:           "eu-west-1",
		IssuedAt:       issuedAt,
	}
}

func TestSealVerifies(t *testing.T) {
	manifest, signer, ring := fixture(t)
	ctx := context.Background()

	seal, err := bundle.Sign(ctx, signer, manifest, payloadFor())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Through the on-disk form, because that is the path a cell takes.
	encoded, err := bundle.EncodeSeal(seal)
	if err != nil {
		t.Fatalf("encode seal: %v", err)
	}
	decoded, err := bundle.DecodeSeal(encoded)
	if err != nil {
		t.Fatalf("decode seal: %v", err)
	}

	got, err := bundle.Verify(ctx, ring, decoded, manifest, issuedAt)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.ManifestDigest != canonical.SumBytes(manifest).String() {
		t.Errorf("payload digest %s does not match the manifest", got.ManifestDigest)
	}
	if got.BundleID != "seal-fixture" || got.ContentVersion != "2026.09.1" || got.Cell != "eu-west-1" {
		t.Errorf("payload lost metadata: %+v", got)
	}
}

// TestSubstitutionIsRefused is the property a seal exists for. Each case swaps
// one thing for another that is individually valid, which is what an attacker
// with access to the content store actually has.
func TestSubstitutionIsRefused(t *testing.T) {
	ctx := context.Background()
	manifest, signer, ring := fixture(t)

	seal, err := bundle.Sign(ctx, signer, manifest, payloadFor())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	t.Run("a manifest the seal does not cover", func(t *testing.T) {
		// One byte of the rate, which is the edit that matters: a bundle that
		// still loads, still evaluates, and charges the wrong tax.
		swapped := []byte(strings.Replace(string(manifest), "0.2100", "0.2500", 1))
		if string(swapped) == string(manifest) {
			t.Fatal("fixture no longer contains the rate")
		}
		if _, err := bundle.Verify(ctx, ring, seal, swapped, issuedAt); err == nil {
			t.Fatal("verified a manifest the seal does not cover")
		}
	})

	t.Run("a payload edited after signing", func(t *testing.T) {
		raw, err := base64.StdEncoding.DecodeString(seal.Payload)
		if err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		edited := strings.Replace(string(raw), `"eu-west-1"`, `"us-east-1"`, 1)
		if edited == string(raw) {
			t.Fatal("fixture no longer names a cell")
		}
		tampered := seal
		tampered.Payload = base64.StdEncoding.EncodeToString([]byte(edited))
		if _, err := bundle.Verify(ctx, ring, tampered, manifest, issuedAt); !errors.Is(err, kms.ErrInvalidSignature) {
			t.Fatalf("edited payload gave %v, want an invalid signature", err)
		}
	})

	t.Run("a key the verifier does not hold", func(t *testing.T) {
		renamed := seal
		renamed.Signature.KeyID = "content-someone-elses-key"
		if _, err := bundle.Verify(ctx, ring, renamed, manifest, issuedAt); !errors.Is(err, kms.ErrUnknownKey) {
			t.Fatalf("unknown key gave %v, want ErrUnknownKey", err)
		}
	})

	t.Run("an instant outside the key's window", func(t *testing.T) {
		after := validTo.Add(time.Hour)
		if _, err := bundle.Verify(ctx, ring, seal, manifest, after); !errors.Is(err, kms.ErrInvalidSignature) {
			t.Fatalf("expired key gave %v, want an invalid signature", err)
		}
		before := validFrom.Add(-time.Hour)
		if _, err := bundle.Verify(ctx, ring, seal, manifest, before); !errors.Is(err, kms.ErrInvalidSignature) {
			t.Fatalf("not-yet-valid key gave %v, want an invalid signature", err)
		}
	})

	t.Run("a seal for a different kind of artifact", func(t *testing.T) {
		p := payloadFor()
		p.Artifact = "evidence-period"
		if _, err := bundle.Sign(ctx, signer, manifest, p); err == nil {
			t.Fatal("signed a content bundle as an evidence period")
		}
	})
}

// TestManifestMustBeCanonical guards the one thing a digest cannot: bytes that
// decode to the right thing and are not the bytes anybody else would produce.
func TestManifestMustBeCanonical(t *testing.T) {
	manifest, _, _ := fixture(t)

	// Whitespace: semantically identical JSON, a different digest.
	padded := append([]byte(" "), manifest...)
	if _, _, err := bundle.DecodeManifest(padded); err == nil {
		t.Fatal("accepted a manifest that is not in canonical form")
	}
}

// TestKeysStayOutsideTheProcess checks the guard rather than the cryptography:
// the local signer is a laptop affordance and must refuse to exist anywhere
// else (ADR-0017 §2.6).
func TestKeysStayOutsideTheProcess(t *testing.T) {
	pemBytes, err := kms.GenerateDevelopmentKey("development")
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	for _, env := range []string{"production", "staging", "", "Development"} {
		if _, err := kms.GenerateDevelopmentKey(env); err == nil {
			t.Errorf("generated a key in environment %q", env)
		}
		if _, err := kms.NewLocalSigner(env, pemBytes, "k", validFrom, validTo); err == nil {
			t.Errorf("built an in-process signer in environment %q", env)
		}
	}
}
