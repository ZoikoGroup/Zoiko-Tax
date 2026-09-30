// Package evidence is the evidence object store adapter.
//
// FileStore is the filesystem implementation of port.EvidenceStore: the W1
// prototype of the regional immutable evidence object store. A cell's
// production store is object storage with a retention lock (S3 Object Lock in
// compliance mode, or its equivalent in the cell's provider) so immutability
// is enforced by the platform rather than by this process. The interface is
// the same either way, and nothing above this package can tell the difference.
//
// What this implementation guarantees on its own:
//
//   - Content addressing. The key is SHA-256 over the bytes, computed here, so
//     a caller cannot choose where bytes land.
//   - Write once. An object is written to a temporary file, synced, and
//     published with link(2), which fails rather than replacing an existing
//     name. A second Put of identical bytes is a no-op; a Put that finds
//     different bytes under its own digest's name has found tampering.
//   - Read verification. Get re-hashes what it read and refuses bytes that do
//     not match, so corrupted evidence is an integrity error rather than a
//     wrong answer.
//   - Tenant scoping. Objects live under the tenant in the security context.
//
// What it cannot guarantee is that an operator with shell access does not
// delete a file. That is the property the platform retention lock provides and
// a local filesystem does not, and it is why this is the prototype.
package evidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// FileStore stores evidence objects under a root directory.
type FileStore struct {
	root string
}

var _ port.EvidenceStore = (*FileStore)(nil)

// NewFileStore opens a store rooted at dir, creating it if needed.
func NewFileStore(dir string) (*FileStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("evidence: no store directory configured")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("evidence: create store %s: %w", dir, err)
	}
	return &FileStore{root: dir}, nil
}

// Put stores bytes and returns their digest.
func (s *FileStore) Put(ctx context.Context, data []byte) (canonical.Digest, error) {
	if len(data) == 0 {
		return canonical.Digest{}, fmt.Errorf("evidence: refusing to store an empty object")
	}
	if err := ctx.Err(); err != nil {
		return canonical.Digest{}, err
	}
	digest := canonical.SumBytes(data)
	final, err := s.path(ctx, digest)
	if err != nil {
		return canonical.Digest{}, err
	}

	// Already present is the common case for a retried write.
	if existing, err := s.read(final); err == nil {
		return digest, s.sameOrTampered(digest, existing, data)
	}

	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return canonical.Digest{}, unavailable(err, "create evidence directory")
	}
	tmp, err := os.CreateTemp(dir, ".incoming-*")
	if err != nil {
		return canonical.Digest{}, unavailable(err, "stage evidence object")
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return canonical.Digest{}, unavailable(err, "write evidence object")
	}
	// Durable before it is visible: an object that is published and then lost
	// in a crash is evidence a committed decision names and nobody can read.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return canonical.Digest{}, unavailable(err, "sync evidence object")
	}
	if err := tmp.Close(); err != nil {
		return canonical.Digest{}, unavailable(err, "close evidence object")
	}

	// link, not rename: rename replaces an existing name silently, and "never
	// replace" is the property this store exists to have.
	if err := os.Link(tmpName, final); err != nil {
		if errors.Is(err, fs.ErrExist) {
			existing, rerr := s.read(final)
			if rerr != nil {
				return canonical.Digest{}, unavailable(rerr, "read concurrently written evidence object")
			}
			return digest, s.sameOrTampered(digest, existing, data)
		}
		return canonical.Digest{}, unavailable(err, "publish evidence object")
	}
	// Read-only once published. Not a security control on its own — the
	// owner can change it back — but it turns an accidental overwrite into an
	// error instead of a success.
	_ = os.Chmod(final, 0o400)
	return digest, nil
}

// Get returns the bytes stored under digest, verified.
func (s *FileStore) Get(ctx context.Context, digest canonical.Digest) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := s.path(ctx, digest)
	if err != nil {
		return nil, err
	}
	data, err := s.read(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errs.Wrap(err, errs.CategoryNotFound, errs.ReasonNotFound,
				"The evidence object does not exist, or is outside the caller's tenant.")
		}
		return nil, unavailable(err, "read evidence object")
	}
	if got := canonical.SumBytes(data); !got.Equal(digest) {
		return nil, errs.New(errs.CategoryInternal, errs.ReasonEvidenceIntegrity,
			fmt.Sprintf("Evidence object %s now hashes to %s.", digest, got))
	}
	return data, nil
}

// path is <root>/<tenant>/<first two hex digits>/<hex>. The fan-out directory
// keeps any one directory from holding every object a tenant ever produced.
func (s *FileStore) path(ctx context.Context, digest canonical.Digest) (string, error) {
	tenant, ok := security.MustTenant(ctx)
	if !ok {
		return "", errs.New(errs.CategoryPolicy, errs.ReasonUnauthenticated,
			"The request reached the evidence store with no tenant in scope.")
	}
	// ParseDigest guarantees the rest is 64 lowercase hex characters, so
	// nothing here can climb out of the root.
	parsed, err := canonical.ParseDigest(digest.String())
	if err != nil {
		return "", fmt.Errorf("evidence: %w", err)
	}
	hex := strings.TrimPrefix(parsed.String(), canonical.DigestPrefix)
	return filepath.Join(s.root, tenant.String(), hex[:2], hex), nil
}

func (s *FileStore) read(path string) ([]byte, error) {
	// #nosec G304 -- the path is built from the configured root, a tenant UUID
	// and a validated hex digest; no caller-supplied text reaches it.
	return os.ReadFile(path)
}

func (s *FileStore) sameOrTampered(digest canonical.Digest, existing, data []byte) error {
	if bytes.Equal(existing, data) {
		return nil
	}
	// The name is the hash of the content that should be there. Different
	// content under that name was not written by this store.
	return errs.New(errs.CategoryInternal, errs.ReasonEvidenceIntegrity,
		fmt.Sprintf("Evidence object %s exists with different content.", digest))
}

func unavailable(err error, op string) error {
	return errs.Wrap(fmt.Errorf("evidence: %s: %w", op, err), errs.CategoryUnavailable, errs.ReasonUnavailable,
		"The evidence store was unavailable. The request was not applied and may be retried unchanged.")
}
