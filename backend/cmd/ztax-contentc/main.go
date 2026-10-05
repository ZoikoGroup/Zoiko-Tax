// Command ztax-contentc compiles and seals rule content.
//
// It is the CONTENT train's tool, and it is a separate deployable for the same
// reason ztax-migrate is: ADR-0005 §2.1 puts authoring and compilation on the
// Z4 content plane, never at request time and never inside a regional cell. The
// depguard rule content-compiler-is-not-in-the-cell keeps that true — this is
// the only command permitted to import the compiler.
//
//	ztax-contentc build   -source pack.ztax -out dir -key k.pem -key-id ID …
//	ztax-contentc compile -source pack.ztax -out dir [-pack -packs -sources -rights-at]
//	ztax-contentc approve -bundle dir/x.manifest.json -role AUTHOR -principal kind:id -key k.pem -key-id ID …
//	ztax-contentc sign    -bundle dir/x.manifest.json -key k.pem -key-id ID …
//	ztax-contentc verify  -dir dir -keyring keyring.json [-at RFC3339] [-environment E]
//	ztax-contentc keygen  -key k.pem -keyring keyring.json -key-id ID …
//
// `verify` is the one to reach for in CI. It runs the cell's own loading path —
// the same package, the same order of checks — so a bundle that passes here is
// a bundle a cell will accept, and the pipeline finds out rather than the
// rollout. `-environment production` verifies it as a production cell would,
// four-eyes included.
//
// Compilation is gated twice before anything is written (ZTAX-SRC-REQ-0050):
// the pack declaration beside the source (pack.json) must resolve against every
// pack in the tree — levels, version constraints, conflicts, no cycles
// (ZTAX-CONT-REQ-0008, -0087) — and every source it declares must have a
// current SourceLicenseRecord in the source register permitting every right a
// bundle needs for every deployment mode the pack targets (ZTAX-SRC-REQ-0051,
// ZTAX-CONT-REQ-0021). A pack that fails either is not compiled, so there is
// nothing to sign.
//
// Four-eyes is the flow between compile and sign (CONT-001 §8). `approve` is
// run once by the author and once by the approver, each with their own key; it
// writes <bundle>.approvals.json beside the manifest. `sign` embeds those
// approvals in the seal, where the release signature covers them, and outside
// development refuses to sign without an AUTHOR and a distinct APPROVER. The
// cell's loader enforces the same rule at activation. `build` is compile and
// sign with no approvals in between, which is a development convenience: what
// it produces loads in development and nowhere else.
//
// Signing here uses an in-process key and therefore refuses outside
// development (ADR-0017 §2.6). A production pack is signed by the KMS signer
// that implements the same interface; this command's shape does not change when
// that lands, because everything it does with a key goes through kms.Signer.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	adaptercontent "github.com/zoikogroup/zoikotax/backend/internal/adapter/content"
	"github.com/zoikogroup/zoikotax/backend/internal/content/bundle"
	"github.com/zoikogroup/zoikotax/backend/internal/content/compile"
	"github.com/zoikogroup/zoikotax/backend/internal/content/dsl"
	"github.com/zoikogroup/zoikotax/backend/internal/content/pack"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/kms"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/telemetry"
)

func main() {
	log := slog.New(telemetry.NewRedactingHandler(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))).
		With("service.name", "ztax-contentc")

	if err := run(log, os.Args[1:]); err != nil {
		log.Error("content build failed", "error", err.Error())
		os.Exit(1)
	}
}

const usage = "usage: ztax-contentc build|compile|approve|sign|verify|keygen [flags]"

// ApprovalsSuffix names the approvals file `approve` writes beside a manifest
// and `sign` reads. It is a build-plane intermediate: the cell never reads it,
// because by then its contents are inside the seal.
const ApprovalsSuffix = ".approvals.json"

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
	case "approve":
		return approve(ctx, log, args[1:])
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
	source   string
	out      string
	pack     string
	packs    string
	sources  string
	rightsAt string
}

func (o *compileOptions) bind(fs *flag.FlagSet) {
	fs.StringVar(&o.source, "source", "", "the .ztax source file")
	fs.StringVar(&o.out, "out", "", "the directory to write the bundle into")
	fs.StringVar(&o.pack, "pack", "", "the pack declaration; defaults to "+pack.DeclarationFile+" beside -source")
	fs.StringVar(&o.packs, "packs", "", "the directory of packs dependencies resolve against; defaults to the parent of the source's directory")
	fs.StringVar(&o.sources, "sources", "", "the source register; defaults to sources/register.json beside the packs directory")
	fs.StringVar(&o.rightsAt, "rights-at", "", "RFC 3339 instant licences are judged at; defaults to now")
}

// defaults fills the conventional layout of content/:
//
//	content/packs/<name>/<name>.ztax
//	content/packs/<name>/pack.json
//	content/sources/register.json
func (o *compileOptions) defaults() {
	if o.pack == "" {
		o.pack = filepath.Join(filepath.Dir(o.source), pack.DeclarationFile)
	}
	if o.packs == "" {
		o.packs = filepath.Dir(filepath.Dir(o.source))
	}
	if o.sources == "" {
		o.sources = filepath.Join(filepath.Dir(o.packs), "sources", "register.json")
	}
}

// assemblePack runs both build-time gates and returns the pack section.
func assemblePack(log *slog.Logger, o compileOptions, bundleID string) (*content.PackManifest, error) {
	// #nosec G304 -- operator-supplied paths, as everywhere in this command.
	declBytes, err := os.ReadFile(o.pack)
	if err != nil {
		return nil, fmt.Errorf("read pack declaration %s: %w (every pack declares its level, dependencies and sources)", o.pack, err)
	}
	decl, err := pack.DecodeDeclaration(declBytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", o.pack, err)
	}

	available, err := availablePacks(o.packs, o.pack, decl)
	if err != nil {
		return nil, err
	}

	// #nosec G304 -- an operator-supplied path.
	regBytes, err := os.ReadFile(o.sources)
	if err != nil {
		return nil, fmt.Errorf("read source register %s: %w", o.sources, err)
	}
	reg, err := pack.DecodeRegister(regBytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", o.sources, err)
	}

	at := time.Now().UTC()
	if o.rightsAt != "" {
		if at, err = instant("-rights-at", o.rightsAt); err != nil {
			return nil, err
		}
	}

	res, err := pack.Assemble(decl, bundleID, available, reg, at)
	if err != nil {
		return nil, err
	}

	order := make([]string, len(res.LoadOrder))
	for i, n := range res.LoadOrder {
		order[i] = string(n.ID) + "@" + n.Version.String()
	}
	for _, e := range res.Evaluations {
		var obligations []string
		for _, d := range e.Decisions {
			obligations = append(obligations, d.Obligations...)
		}
		// The release-time rights evaluation is evidence (ZTAX-SRC-REQ-0104);
		// the record digest in the manifest pins which record it was made
		// against, and this line says what it concluded.
		log.Info("rights gate passed",
			"source.id", string(e.Source),
			"deployment.mode", string(e.Mode),
			"rights.evaluated", len(e.Decisions),
			"obligations", obligations,
			"evaluated_at", canonical.FormatTime(at))
	}
	log.Info("pack resolved",
		"pack.id", string(res.Pack.ID),
		"pack.version", res.Pack.Version.String(),
		"pack.level", string(res.Pack.Level),
		"pack.status", string(res.Pack.Status),
		"load.order", order)
	return &res.Pack, nil
}

// availablePacks reads every pack declaration under the packs directory —
// one per immediate subdirectory — which is the set dependency resolution
// runs against. The pack being built is included whether or not its
// declaration lives under that directory, and is read once either way.
func availablePacks(root, declPath string, decl pack.Declaration) ([]content.PackNode, error) {
	self, err := filepath.Abs(declPath)
	if err != nil {
		return nil, err
	}
	nodes := []content.PackNode{decl.Node()}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read packs directory %s: %w", root, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, e.Name(), pack.DeclarationFile)
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		if abs == self {
			continue
		}
		// #nosec G304 -- a path under an operator-supplied directory.
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			// A directory without a declaration is not a pack, and a pack
			// without a declaration fails its own build; it is not this
			// build's business to fail on it.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		d, err := pack.DecodeDeclaration(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		nodes = append(nodes, d.Node())
	}
	return nodes, nil
}

// compileSource parses, checks and writes the manifest, returning its path and
// canonical bytes.
func compileSource(log *slog.Logger, o compileOptions) (string, []byte, error) {
	if o.source == "" || o.out == "" {
		return "", nil, errors.New("both -source and -out are required")
	}
	o.defaults()
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

	// Both gates run before anything is written: a refused pack leaves no
	// manifest behind for a later `sign` to pick up.
	if manifest.Pack, err = assemblePack(log, o, manifest.BundleID); err != nil {
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
	if err := discardStaleApprovals(log, path, manifest.BundleID, canonical.SumBytes(data).String()); err != nil {
		return "", nil, err
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

	manifest, digest, err := bundle.DecodeManifest(manifestBytes)
	if err != nil {
		return err
	}

	approvals, err := readApprovals(manifestPath)
	if err != nil {
		return err
	}
	claims, err := bundle.ApprovalsFor(approvals, manifest.BundleID, digest.String())
	if err != nil {
		return fmt.Errorf("%w; re-run approve against this manifest", err)
	}
	for _, c := range claims {
		if c.At.After(issuedAt) {
			return fmt.Errorf("%s approved at %s, after the seal's issue instant %s",
				c.Principal, canonical.FormatTime(c.At), canonical.FormatTime(issuedAt))
		}
	}
	if err := content.CheckFourEyes(claims, signer.KeyID()); err != nil {
		// Signing without four eyes is refused outside development before the
		// signer is asked for anything — a KMS call that produces a seal no
		// cell will load is a release the pipeline then has to explain.
		//
		// Approvals that are present but do not make four eyes are refused in
		// development too, as the loader refuses them: half an approval is not
		// the absence of one.
		if environment() != adaptercontent.Development || len(claims) > 0 {
			return fmt.Errorf("refusing to seal %s: %w", manifest.BundleID, err)
		}
		log.Warn("sealing without four-eyes approval; the bundle loads in development only",
			"bundle.id", manifest.BundleID, "reason", err.Error())
	}

	seal, err := bundle.Sign(ctx, signer, manifestBytes, bundle.SealPayload{
		Artifact:       bundle.Artifact,
		BundleID:       manifest.BundleID,
		IRVersion:      manifest.IRVersion,
		CanonProfile:   canonical.ProfileVersion,
		ContentVersion: o.contentVersion,
		Cell:           o.cell,
		IssuedAt:       issuedAt,
		Approvals:      approvals,
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
		"approvals", describeApprovals(claims),
		"path", path)
	return nil
}

// ---------------------------------------------------------------------------
// approve
// ---------------------------------------------------------------------------

// approve signs one four-eyes approval of a compiled manifest with the
// approver's own key and records it beside the manifest.
//
// Run once per person. The author approves first, as the attestation "this is
// what I wrote", and the approver second; the order is not enforced, because
// what four-eyes needs is two people, not a sequence. Re-approving in the same
// role replaces that person's earlier approval, so re-running the pipeline
// over an unchanged manifest is idempotent.
func approve(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("approve", flag.ContinueOnError)
	manifestPath := fs.String("bundle", "", "the compiled .manifest.json to approve")
	role := fs.String("role", "", "AUTHOR, REVIEWER, APPROVER or SENIOR_APPROVER")
	principal := fs.String("principal", "", "who is approving, as kind:identifier (e.g. analyst:2048)")
	keyFile := fs.String("key", "", "the approver's PKCS#8 PEM signing key (development only)")
	keyID := fs.String("key-id", "", "the approver's key identifier")
	notBefore := fs.String("not-before", "", "RFC 3339 start of the key's validity window")
	notAfter := fs.String("not-after", "", "RFC 3339 end of the key's validity window")
	approvedAt := fs.String("approved-at", "", "RFC 3339 approval instant; defaults to now")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *manifestPath == "" || *role == "" || *principal == "" || *keyFile == "" || *keyID == "" {
		return errors.New("-bundle, -role, -principal, -key and -key-id are required")
	}
	from, err := instant("-not-before", *notBefore)
	if err != nil {
		return err
	}
	to, err := instant("-not-after", *notAfter)
	if err != nil {
		return err
	}
	at := time.Now().UTC()
	if *approvedAt != "" {
		if at, err = instant("-approved-at", *approvedAt); err != nil {
			return err
		}
	}

	// #nosec G304 -- operator-supplied paths.
	manifestBytes, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", *manifestPath, err)
	}
	manifest, digest, err := bundle.DecodeManifest(manifestBytes)
	if err != nil {
		return err
	}
	// #nosec G304 -- an operator-supplied path.
	pemBytes, err := os.ReadFile(*keyFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", *keyFile, err)
	}
	signer, err := kms.NewLocalSigner(environment(), pemBytes, *keyID, from, to)
	if err != nil {
		return err
	}

	a, err := bundle.Approve(ctx, signer, manifestBytes, bundle.ApprovalStatement{
		BundleID:       manifest.BundleID,
		ManifestDigest: digest.String(),
		Role:           content.Role(*role),
		Principal:      *principal,
		ApprovedAt:     at,
	})
	if err != nil {
		return err
	}

	existing, err := readApprovals(*manifestPath)
	if err != nil {
		return err
	}
	kept := []bundle.Approval{a}
	for _, e := range existing {
		s, _, err := bundle.DecodeApprovalStatement(e)
		if err != nil {
			return fmt.Errorf("%s: %w", approvalsPath(*manifestPath), err)
		}
		if s.ManifestDigest != digest.String() {
			continue // stale: covers a manifest this file no longer holds
		}
		if s.Principal == *principal && s.Role == content.Role(*role) {
			continue // replaced by the approval just made
		}
		kept = append(kept, e)
	}
	if err := writeApprovals(*manifestPath, kept); err != nil {
		return err
	}

	log.Info("approved",
		"bundle.id", manifest.BundleID,
		"bundle.digest", digest.String(),
		"role", *role,
		"principal", *principal,
		"key.id", signer.KeyID(),
		"approved_at", canonical.FormatTime(at),
		"approvals", len(kept),
		"path", approvalsPath(*manifestPath))
	return nil
}

func approvalsPath(manifestPath string) string {
	return strings.TrimSuffix(manifestPath, adaptercontent.ManifestSuffix) + ApprovalsSuffix
}

// readApprovals reads the approvals beside a manifest. No file is no
// approvals, which is a legitimate state between compile and approve.
func readApprovals(manifestPath string) ([]bundle.Approval, error) {
	path := approvalsPath(manifestPath)
	// #nosec G304 -- derived from an operator-supplied path.
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	as, err := bundle.DecodeApprovals(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return as, nil
}

func writeApprovals(manifestPath string, as []bundle.Approval) error {
	data, err := bundle.EncodeApprovals(as)
	if err != nil {
		return err
	}
	path := approvalsPath(manifestPath)
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // signed attestations, not secrets
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// discardStaleApprovals removes approvals of a manifest that compile has just
// overwritten with different bytes. They approve law that is no longer in the
// file beside them, and leaving them would make `sign` refuse with a message
// about approvals when the real news is that the source changed. Approvals of
// identical bytes — a reproducible rebuild — are kept.
func discardStaleApprovals(log *slog.Logger, manifestPath, bundleID, digest string) error {
	as, err := readApprovals(manifestPath)
	if err != nil || len(as) == 0 {
		return err
	}
	if _, err := bundle.ApprovalsFor(as, bundleID, digest); err == nil {
		return nil
	}
	log.Warn("discarding approvals of a previous compilation; the manifest changed and must be approved again",
		"bundle.id", bundleID, "bundle.digest", digest, "path", approvalsPath(manifestPath))
	return os.Remove(approvalsPath(manifestPath))
}

func describeApprovals(as []content.Approval) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = string(a.Role) + "=" + a.Principal + "(" + a.KeyID + ")"
	}
	sort.Strings(out)
	return out
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
	env := fs.String("environment", environment(), "the environment to verify as; anything but development requires four-eyes approval")
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
		Dir:         *dir,
		Verifier:    ring,
		Clock:       clock.Fixed{Instant: instantAt},
		Cell:        *cell,
		Environment: *env,
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
		"approvals", describeApprovals(loaded.Approvals),
		"pack", describePack(loaded.Pack),
		"environment", *env,
		"issued_at", canonical.FormatTime(loaded.Seal.IssuedAt),
		"verified_at", canonical.FormatTime(instantAt))
	return nil
}

func describePack(p *content.PackManifest) string {
	if p == nil {
		return "none"
	}
	return string(p.ID) + "@" + p.Version.String() + " " + string(p.Level) + " " + string(p.Status)
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
	keys, err := existingKeys(*keyringFile, *keyID)
	if err != nil {
		return err
	}
	ring, err := kms.MarshalKeyring(append(keys, entry))
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

// existingKeys reads the public keys already in a keyring file, minus any
// under keyID, so that keygen adds a key rather than replacing the ring.
//
// Four-eyes needs more than one key in the ring — the release signer, the
// author and the approver each hold their own — and a keygen that overwrote
// the ring would leave only the last key generated. A key regenerated under
// an identifier it already had replaces the old entry, because the private
// half of that entry is gone.
func existingKeys(path, keyID string) ([]kms.PublicKey, error) {
	// #nosec G304 -- an operator-supplied path.
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc struct {
		Keys []kms.PublicKey `json:"keys"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// Parsing it as a keyring too checks it is one, rather than appending to a
	// file this command would then write out as something it never validated.
	if _, err := kms.ParseKeyring(data); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := doc.Keys[:0]
	for _, k := range doc.Keys {
		if k.KeyID != keyID {
			out = append(out, k)
		}
	}
	return out, nil
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
