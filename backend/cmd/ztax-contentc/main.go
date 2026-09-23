// Command ztax-contentc compiles and seals rule content.
//
// It is the CONTENT train's tool, and it is a separate deployable for the same
// reason ztax-migrate is: ADR-0005 §2.1 puts authoring and compilation on the
// Z4 content plane, never at request time and never inside a regional cell. The
// depguard rule content-compiler-is-not-in-the-cell keeps that true — this is
// the only command permitted to import the compiler.
//
//	ztax-contentc build   -source pack.ztax -out dir -key k.pem -key-id ID …
//	ztax-contentc compile -source pack.ztax -out dir
//	ztax-contentc sign    -bundle dir/x.manifest.json -key k.pem -key-id ID …
//	ztax-contentc verify  -dir dir -keyring keyring.json [-at RFC3339]
//	ztax-contentc keygen  -key k.pem -keyring keyring.json -key-id ID …
//
// `verify` is the one to reach for in CI. It runs the cell's own loading path —
// the same package, the same order of checks — so a bundle that passes here is
// a bundle a cell will accept, and the pipeline finds out rather than the
// rollout.
//
// Signing here uses an in-process key and therefore refuses outside
// development (ADR-0017 §2.6). A production pack is signed by the KMS signer
// that implements the same interface; this command's shape does not change when
// that lands, because everything it does with a key goes through kms.Signer.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	adaptercontent "github.com/zoikogroup/zoikotax/backend/internal/adapter/content"
	"github.com/zoikogroup/zoikotax/backend/internal/content/bundle"
	"github.com/zoikogroup/zoikotax/backend/internal/content/compile"
	"github.com/zoikogroup/zoikotax/backend/internal/content/dsl"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/kms"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("service.name", "ztax-contentc")

	if err := run(log, os.Args[1:]); err != nil {
		log.Error("content build failed", "error", err.Error())
		os.Exit(1)
	}
}

const usage = "usage: ztax-contentc build|compile|sign|verify|keygen [flags]"

func run(log *slog.Logger, args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	ctx := context.Background()

	switch args[0] {
	case "build":
		return build(ctx, log, args[1:])
	case "compile":
		return compileOnly(log, args[1:])
	case "sign":
		return sign(ctx, log, args[1:])
	case "verify":
		return verify(ctx, log, args[1:])
	case "keygen":
		return keygen(log, args[1:])
	}
	return fmt.Errorf("%q is not a subcommand\n%s", args[0], usage)
}

// ---------------------------------------------------------------------------
// compile
// ---------------------------------------------------------------------------

// compileOptions are shared by compile and build.
type compileOptions struct {
	source string
	out    string
}

func (o *compileOptions) bind(fs *flag.FlagSet) {
	fs.StringVar(&o.source, "source", "", "the .ztax source file")
	fs.StringVar(&o.out, "out", "", "the directory to write the bundle into")
}

// compileSource parses, checks and writes the manifest, returning its path and
// canonical bytes.
func compileSource(log *slog.Logger, o compileOptions) (string, []byte, error) {
	if o.source == "" || o.out == "" {
		return "", nil, errors.New("both -source and -out are required")
	}
	// #nosec G304 -- a path given on the command line by the operator running
	// the compiler, which is the whole interface.
	src, err := os.ReadFile(o.source)
	if err != nil {
		return "", nil, fmt.Errorf("read %s: %w", o.source, err)
	}

	prog, err := dsl.Parse(filepath.Base(o.source), string(src))
	if err != nil {
		// A parse error already carries file:line:col, so it is returned as it
		// is rather than wrapped into something that buries the position.
		return "", nil, err
	}
	manifest, err := compile.Compile(prog)
	if err != nil {
		return "", nil, err
	}

	data, err := bundle.EncodeManifest(manifest)
	if err != nil {
		return "", nil, err
	}
	// 0o750 on the directory and 0o644 on what goes in it: a compiled bundle is
	// public content — it is signed, and its secrecy protects nothing — but the
	// build directory has no reason to be world-traversable.
	if err := os.MkdirAll(o.out, 0o750); err != nil {
		return "", nil, fmt.Errorf("create %s: %w", o.out, err)
	}
	path := filepath.Join(o.out, manifest.BundleID+adaptercontent.ManifestSuffix)
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // a bundle is public content, not a secret
		return "", nil, fmt.Errorf("write %s: %w", path, err)
	}

	log.Info("compiled",
		"bundle.id", manifest.BundleID,
		"bundle.digest", canonical.SumBytes(data).String(),
		"ir.version", manifest.IRVersion,
		"nodes", len(manifest.Nodes),
		"constants", len(manifest.Constants),
		"roots", len(manifest.Roots),
		"path", path)
	return path, data, nil
}

func compileOnly(log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("compile", flag.ContinueOnError)
	var o compileOptions
	o.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, _, err := compileSource(log, o)
	return err
}

// ---------------------------------------------------------------------------
// sign
// ---------------------------------------------------------------------------

type signOptions struct {
	manifest       string
	keyFile        string
	keyID          string
	notBefore      string
	notAfter       string
	contentVersion string
	cell           string
	issuedAt       string
}

func (o *signOptions) bind(fs *flag.FlagSet) {
	fs.StringVar(&o.manifest, "bundle", "", "the compiled .manifest.json to seal")
	fs.StringVar(&o.keyFile, "key", "", "PKCS#8 PEM signing key (development only)")
	fs.StringVar(&o.keyID, "key-id", "", "the key identifier recorded in the seal")
	fs.StringVar(&o.notBefore, "not-before", "", "RFC 3339 start of the key's validity window")
	fs.StringVar(&o.notAfter, "not-after", "", "RFC 3339 end of the key's validity window")
	fs.StringVar(&o.contentVersion, "content-version", "", "the CONTENT train version this pack is released at")
	fs.StringVar(&o.cell, "cell", "", "the cell this bundle is valid in; empty means every cell")
	fs.StringVar(&o.issuedAt, "issued-at", "", "RFC 3339 issue instant; defaults to now")
}

// signManifest seals manifest bytes and writes the seal beside them.
func signManifest(ctx context.Context, log *slog.Logger, o signOptions, manifestPath string, manifestBytes []byte) error {
	if o.keyFile == "" || o.keyID == "" || o.contentVersion == "" {
		return errors.New("-key, -key-id and -content-version are required to seal a bundle")
	}
	notBefore, err := instant("-not-before", o.notBefore)
	if err != nil {
		return err
	}
	notAfter, err := instant("-not-after", o.notAfter)
	if err != nil {
		return err
	}
	issuedAt := time.Now().UTC()
	if o.issuedAt != "" {
		if issuedAt, err = instant("-issued-at", o.issuedAt); err != nil {
			return err
		}
	}

	// #nosec G304 -- an operator-supplied path, as above.
	pemBytes, err := os.ReadFile(o.keyFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", o.keyFile, err)
	}
	signer, err := kms.NewLocalSigner(environment(), pemBytes, o.keyID, notBefore, notAfter)
	if err != nil {
		return err
	}

	manifest, _, err := bundle.DecodeManifest(manifestBytes)
	if err != nil {
		return err
	}

	seal, err := bundle.Sign(ctx, signer, manifestBytes, bundle.SealPayload{
		Artifact:       bundle.Artifact,
		BundleID:       manifest.BundleID,
		IRVersion:      manifest.IRVersion,
		CanonProfile:   canonical.ProfileVersion,
		ContentVersion: o.contentVersion,
		Cell:           o.cell,
		IssuedAt:       issuedAt,
	})
	if err != nil {
		return err
	}

	encoded, err := bundle.EncodeSeal(seal)
	if err != nil {
		return err
	}
	path := manifestPath[:len(manifestPath)-len(adaptercontent.ManifestSuffix)] + adaptercontent.SealSuffix
	if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // a seal is a public signature, not a secret
		return fmt.Errorf("write %s: %w", path, err)
	}

	log.Info("sealed",
		"bundle.id", manifest.BundleID,
		"bundle.digest", canonical.SumBytes(manifestBytes).String(),
		"key.id", signer.KeyID(),
		"content.version", o.contentVersion,
		"cell", o.cell,
		"issued_at", canonical.FormatTime(issuedAt),
		"path", path)
	return nil
}

func sign(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	var o signOptions
	o.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if o.manifest == "" {
		return errors.New("-bundle is required")
	}
	// #nosec G304 -- an operator-supplied path.
	data, err := os.ReadFile(o.manifest)
	if err != nil {
		return fmt.Errorf("read %s: %w", o.manifest, err)
	}
	return signManifest(ctx, log, o, o.manifest, data)
}

// build is compile and sign in one invocation, which is what a content pipeline
// actually runs. Keeping them separately available matters for the case where
// the two happen on different machines, which is what a real KMS implies.
func build(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	var (
		c compileOptions
		s signOptions
	)
	c.bind(fs)
	s.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	path, data, err := compileSource(log, c)
	if err != nil {
		return err
	}
	return signManifest(ctx, log, s, path, data)
}

// ---------------------------------------------------------------------------
// verify
// ---------------------------------------------------------------------------

func verify(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	dir := fs.String("dir", "", "the directory holding the bundle and its seal")
	keyring := fs.String("keyring", "", "the keyring of public keys to verify against")
	cell := fs.String("cell", "", "the cell identity to verify as")
	at := fs.String("at", "", "RFC 3339 instant to judge key validity at; defaults to now")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *keyring == "" {
		return errors.New("-dir and -keyring are required")
	}

	// #nosec G304 -- an operator-supplied path.
	ringBytes, err := os.ReadFile(*keyring)
	if err != nil {
		return fmt.Errorf("read %s: %w", *keyring, err)
	}
	ring, err := kms.ParseKeyring(ringBytes)
	if err != nil {
		return err
	}

	instantAt := time.Now().UTC()
	if *at != "" {
		if instantAt, err = instant("-at", *at); err != nil {
			return err
		}
	}

	// The cell's own loader, not a second implementation of it. A verifier that
	// checked the same things in its own way would eventually disagree with the
	// thing it is meant to predict.
	loader := &adaptercontent.Loader{
		Dir:      *dir,
		Verifier: ring,
		Clock:    clock.Fixed{Instant: instantAt},
		Cell:     *cell,
	}
	loaded, err := loader.Load(ctx)
	if err != nil {
		return err
	}

	log.Info("verified",
		"bundle.id", loaded.Seal.BundleID,
		"bundle.digest", loaded.Digest,
		"ir.version", loaded.Seal.IRVersion,
		"canon.profile", loaded.Seal.CanonProfile,
		"content.version", loaded.Seal.ContentVersion,
		"cell", loaded.Seal.Cell,
		"key.id", loaded.KeyID,
		"nodes", loaded.Bundle.NodeCount(),
		"issued_at", canonical.FormatTime(loaded.Seal.IssuedAt),
		"verified_at", canonical.FormatTime(instantAt))
	return nil
}

// ---------------------------------------------------------------------------
// keygen
// ---------------------------------------------------------------------------

func keygen(log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	keyFile := fs.String("key", "", "where to write the PKCS#8 PEM private key")
	keyringFile := fs.String("keyring", "", "where to write the matching public keyring")
	keyID := fs.String("key-id", "", "the key identifier")
	notBefore := fs.String("not-before", "", "RFC 3339 start of the validity window")
	notAfter := fs.String("not-after", "", "RFC 3339 end of the validity window")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyFile == "" || *keyringFile == "" || *keyID == "" {
		return errors.New("-key, -keyring and -key-id are required")
	}
	from, err := instant("-not-before", *notBefore)
	if err != nil {
		return err
	}
	to, err := instant("-not-after", *notAfter)
	if err != nil {
		return err
	}

	env := environment()
	pemBytes, err := kms.GenerateDevelopmentKey(env)
	if err != nil {
		return err
	}
	signer, err := kms.NewLocalSigner(env, pemBytes, *keyID, from, to)
	if err != nil {
		return err
	}
	entry, err := signer.PublicKeyEntry()
	if err != nil {
		return err
	}
	ring, err := kms.MarshalKeyring([]kms.PublicKey{entry})
	if err != nil {
		return err
	}

	// 0600 on the private key. It is a development key and it is still a
	// private key, and a development habit is the habit that gets repeated.
	if err := os.WriteFile(*keyFile, pemBytes, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", *keyFile, err)
	}
	if err := os.WriteFile(*keyringFile, append(ring, '\n'), 0o644); err != nil { //nolint:gosec // public keys
		return fmt.Errorf("write %s: %w", *keyringFile, err)
	}

	log.Warn("generated a development signing key; a production key is generated inside the KMS and never leaves it (ADR-0017 §2.6)",
		"key.id", *keyID, "key.path", *keyFile, "keyring.path", *keyringFile)
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// environment reads ZTAX_ENVIRONMENT. It is required rather than defaulted: the
// key guards in internal/platform/kms compare against "development", and a
// default would decide which side of that comparison an unset variable lands
// on. Failing with an empty value is the safe side and the honest one.
func environment() string { return os.Getenv("ZTAX_ENVIRONMENT") }

func instant(flagName, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, fmt.Errorf("%s is required as an RFC 3339 instant", flagName)
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: %q is not an RFC 3339 instant", flagName, value)
	}
	return t.UTC(), nil
}
