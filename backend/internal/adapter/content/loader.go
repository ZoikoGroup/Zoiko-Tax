// Package content loads a signed rule bundle from disk into a cell.
//
// It is the boundary internal/domain/rule's Load deliberately stops short of.
// That package says so in its own comment: signature verification is a KMS call
// and belongs where the bundle is fetched, so that the executor stays free of
// I/O and stays testable without a key. This is that place.
//
// The order of operations is ADR-0005 §2.6, and it is the whole design:
//
//	verify the seal → verify the digest → check four-eyes → load and check the graph → swap
//
// Nothing is published until every step has passed, so a replacement that fails
// verification never reaches the Holder and the cell keeps serving the bundle it
// already had. There is no partial activation, and no request blocks on any of
// it.
//
// Four-eyes is a load-time check, not only a build-time one (ZTAX-DOM-001 Z4,
// CONT-001 §8). The build plane can refuse to seal an unapproved bundle, but
// the build plane is not the only thing that can put two files in a directory;
// a cell that accepted any well-signed bundle would make the release signer
// the sole pair of eyes in practice, whatever the pipeline intended.
package content

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/content/bundle"
	domaincontent "github.com/zoikogroup/zoikotax/backend/internal/domain/content"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/kms"
)

// ManifestSuffix and SealSuffix name the two files a bundle is.
//
// A detached seal rather than one combined file, because the digest is taken
// over the manifest bytes exactly as they sit on disk. Embedding the manifest
// inside a wrapper would mean extracting a byte range from a parsed document
// before digesting it, and "which bytes exactly" is the question this format
// exists to have an obvious answer to.
const (
	ManifestSuffix = ".manifest.json"
	SealSuffix     = ".seal.json"
)

// Loader reads and verifies the bundle a cell should run.
type Loader struct {
	// Dir holds exactly one bundle: <name>.manifest.json and <name>.seal.json.
	Dir string
	// Verifier resolves the seal's key by identifier (ADR-0017 §2.7).
	Verifier kms.Verifier
	// Clock supplies the instant the key's validity is judged at.
	Clock clock.Clock
	// Cell is this cell's identity. A bundle sealed for another cell is refused
	// here rather than at the point it would have produced a figure.
	Cell string
	// Environment is the process's ZTAX_ENVIRONMENT. Outside "development" a
	// bundle is refused unless its seal carries four-eyes approvals — an
	// AUTHOR and an APPROVER, distinct people with distinct keys, neither
	// holding the release key (domain/content.CheckFourEyes).
	//
	// The comparison is against "development" and nothing else, so an empty or
	// misspelled environment lands on the strict side. That is the shape every
	// environment guard in the estate has (kms.NewLocalSigner, the config
	// validators): the permissive branch has to be asked for by name.
	// Development stays usable — a locally built, unapproved bundle loads — but
	// approvals that *are* present are verified in every environment, because
	// a forged approval is not a development convenience.
	Environment string
}

// Development is the one environment in which an unapproved bundle loads.
const Development = "development"

// Loaded is a verified bundle and the seal that vouches for it.
type Loaded struct {
	Bundle *rule.Bundle
	Seal   bundle.SealPayload
	// Digest is the manifest's digest, which is what a decision records and
	// what a replay compares (ADR-0011 §2.8).
	Digest string
	// KeyID is the key the seal was verified against. It travels into the
	// release evidence manifest (ADR-0017 §2.8), so the loader reports it
	// rather than leaving a caller to re-read the seal for it.
	KeyID string
	// Approvals are the verified four-eyes approvals the seal carries, which
	// release evidence records as author, reviewer and approver
	// (ZTAX-CONT-REQ-0058). Empty only for an unapproved development bundle.
	Approvals []domaincontent.Approval
	// Pack is the signed pack section, or nil for a bundle compiled before
	// packs existed.
	Pack *domaincontent.PackManifest
}

// Load reads, verifies and builds the bundle. It publishes nothing.
func (l *Loader) Load(ctx context.Context) (Loaded, error) {
	if l.Dir == "" {
		return Loaded{}, errs.New(errs.CategoryUnavailable, errs.ReasonNoContentBundle,
			"This cell is not configured with a content directory.")
	}
	if l.Verifier == nil {
		// A loader with no verifier would load unsigned content, which is the
		// one failure mode worth refusing to start over.
		return Loaded{}, fmt.Errorf("content: no verifier; a bundle cannot be loaded without a keyring")
	}

	name, err := l.soleBundle()
	if err != nil {
		return Loaded{}, err
	}

	// #nosec G304 -- the paths are derived from a directory named in this
	// process's own configuration, not from a request. A cell that cannot read
	// its configured content directory refuses to start.
	manifestBytes, err := os.ReadFile(filepath.Join(l.Dir, name+ManifestSuffix))
	if err != nil {
		return Loaded{}, fmt.Errorf("content: read manifest for %s: %w", name, err)
	}
	// #nosec G304 -- same path, same reason.
	sealBytes, err := os.ReadFile(filepath.Join(l.Dir, name+SealSuffix))
	if err != nil {
		return Loaded{}, fmt.Errorf("content: read seal for %s: %w", name, err)
	}

	seal, err := bundle.DecodeSeal(sealBytes)
	if err != nil {
		return Loaded{}, err
	}

	at := time.Time{}
	if l.Clock != nil {
		at = l.Clock.Now()
	}
	payload, err := bundle.Verify(ctx, l.Verifier, seal, manifestBytes, at)
	if err != nil {
		return Loaded{}, err
	}
	if payload.Cell != "" && payload.Cell != l.Cell {
		// Residency content is cell-scoped, and a bundle that travelled between
		// cells is either a deployment mistake or an attempt to apply one
		// region's law in another.
		return Loaded{}, fmt.Errorf("content: bundle %s is sealed for cell %q, this cell is %q",
			payload.BundleID, payload.Cell, l.Cell)
	}
	if payload.IRVersion < rule.MinSupportedIRVersion || payload.IRVersion > rule.IRVersion {
		return Loaded{}, errs.New(errs.CategoryUnsupported, errs.ReasonIRVersionUnsupported,
			fmt.Sprintf("The content bundle requires rule-IR version %d; this runtime supports %d to %d.",
				payload.IRVersion, rule.MinSupportedIRVersion, rule.IRVersion))
	}

	manifest, digest, err := bundle.DecodeManifest(manifestBytes)
	if err != nil {
		return Loaded{}, err
	}
	if manifest.BundleID != payload.BundleID {
		return Loaded{}, fmt.Errorf("content: seal names bundle %q, the manifest is %q",
			payload.BundleID, manifest.BundleID)
	}
	if manifest.IRVersion != payload.IRVersion {
		return Loaded{}, fmt.Errorf("content: seal names IR version %d, the manifest is %d",
			payload.IRVersion, manifest.IRVersion)
	}
	// The digest the runtime records in every decision is the one the seal
	// vouches for, and Verify has already established the two agree.
	manifest.Digest = digest.String()

	// Verify has already checked every approval present is genuine and covers
	// this digest; this re-reads them to apply the policy. Re-verifying costs
	// one signature check per approval at startup and keeps "the approvals a
	// caller acts on are verified ones" true without trusting the order of two
	// calls.
	approvals, err := bundle.VerifyApprovals(ctx, l.Verifier, payload, at)
	if err != nil {
		return Loaded{}, err
	}
	if l.Environment != Development || len(approvals) > 0 {
		// Outside development four-eyes is required. Inside it, a bundle that
		// carries approvals is held to them: a half-approved bundle is not an
		// unapproved one, and loading it quietly would teach the wrong lesson
		// about which of the two it is.
		if err := domaincontent.CheckFourEyes(approvals, seal.Signature.KeyID); err != nil {
			return Loaded{}, fmt.Errorf("content: bundle %s is not four-eyes approved (environment %q): %w",
				payload.BundleID, l.Environment, err)
		}
	}

	b, err := rule.Load(manifest)
	if err != nil {
		return Loaded{}, err
	}
	return Loaded{
		Bundle: b, Seal: payload, Digest: digest.String(), KeyID: seal.Signature.KeyID,
		Approvals: approvals, Pack: manifest.Pack,
	}, nil
}

// Activate loads and, only if every step passed, publishes by atomic pointer
// swap (ADR-0005 §2.6).
//
// Splitting Load from Activate is what makes "a bundle that fails verification
// never reaches Publish" a property of the code rather than of the caller's
// discipline: there is no path from a failed Load to a Publish, because Load
// returns an error and Activate returns before the swap.
func (l *Loader) Activate(ctx context.Context, h *rule.Holder) (Loaded, error) {
	loaded, err := l.Load(ctx)
	if err != nil {
		return Loaded{}, err
	}
	h.Publish(loaded.Bundle)
	return loaded, nil
}

// soleBundle finds the one bundle in the directory.
//
// One, not the first: a directory holding two manifests is a deployment whose
// author expected one of them to be chosen, and any choice this function made
// would be a guess about which law to apply.
func (l *Loader) soleBundle() (string, error) {
	entries, err := os.ReadDir(l.Dir)
	if err != nil {
		return "", fmt.Errorf("content: read %s: %w", l.Dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n, ok := strings.CutSuffix(e.Name(), ManifestSuffix); ok {
			names = append(names, n)
		}
	}
	switch len(names) {
	case 1:
		return names[0], nil
	case 0:
		return "", errs.New(errs.CategoryUnavailable, errs.ReasonNoContentBundle,
			"The cell's content directory holds no bundle manifest.")
	default:
		return "", fmt.Errorf("content: %s holds %d bundles (%s); a cell runs one",
			l.Dir, len(names), strings.Join(names, ", "))
	}
}

// IsMissing reports a content directory that is not there.
//
// main distinguishes it from a directory that is there and unreadable: a cell
// deployed with no content is legitimate before A4 and starts without a bundle,
// whereas a content directory it cannot read is a fault and should stop it.
func IsMissing(err error) bool { return errors.Is(err, fs.ErrNotExist) }
