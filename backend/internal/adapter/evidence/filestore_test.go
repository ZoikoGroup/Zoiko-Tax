package evidence_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	adapterevidence "github.com/zoikogroup/zoikotax/backend/internal/adapter/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

func tenantCtx(u string) context.Context {
	return security.Into(context.Background(), security.System(id.NewTenantID(uuid.MustParse(u))))
}

var (
	tenantA = tenantCtx("01920000-0000-7000-8000-00000000000a")
	tenantB = tenantCtx("01920000-0000-7000-8000-00000000000b")
)

func store(t *testing.T) (*adapterevidence.FileStore, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := adapterevidence.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestPutGetRoundTrip(t *testing.T) {
	s, _ := store(t)
	data := []byte(`{"a":"1.50"}`)
	d, err := s.Put(tenantA, data)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Equal(canonical.SumBytes(data)) {
		t.Fatalf("keyed as %s, want the content digest", d)
	}
	got, err := s.Get(tenantA, d)
	if err != nil || string(got) != string(data) {
		t.Fatalf("Get = %q, %v", got, err)
	}
}

func TestPutIsIdempotent(t *testing.T) {
	s, _ := store(t)
	data := []byte(`{"retry":true}`)
	d1, err := s.Put(tenantA, data)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := s.Put(tenantA, data)
	if err != nil {
		t.Fatalf("a retried Put of the same bytes must succeed: %v", err)
	}
	if !d1.Equal(d2) {
		t.Fatal("a retried Put returned a different key")
	}
}

// The name is the hash of what should be there. Bytes that do not hash to it
// were not written by the store, and are never returned as evidence.
func TestTamperedObjectIsRefusedOnRead(t *testing.T) {
	s, dir := store(t)
	d, err := s.Put(tenantA, []byte(`{"tax":"21.00"}`))
	if err != nil {
		t.Fatal(err)
	}
	path := objectPath(t, dir, "01920000-0000-7000-8000-00000000000a", d)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"tax":"0.00"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = s.Get(tenantA, d)
	if errs.ReasonOf(err) != errs.ReasonEvidenceIntegrity {
		t.Fatalf("tampered object: got %v, want EVIDENCE_INTEGRITY", err)
	}
	// And a Put of the original bytes finds the forgery rather than
	// overwriting it quietly.
	_, err = s.Put(tenantA, []byte(`{"tax":"21.00"}`))
	if errs.ReasonOf(err) != errs.ReasonEvidenceIntegrity {
		t.Fatalf("Put over a forgery: got %v, want EVIDENCE_INTEGRITY", err)
	}
}

func TestObjectsAreTenantScoped(t *testing.T) {
	s, _ := store(t)
	d, err := s.Put(tenantA, []byte(`{"tenant":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(tenantB, d); !errs.IsCategory(err, errs.CategoryNotFound) {
		t.Fatalf("tenant B read tenant A's object: %v", err)
	}
}

func TestNoTenantNoAccess(t *testing.T) {
	s, _ := store(t)
	if _, err := s.Put(context.Background(), []byte("x")); !errs.IsCategory(err, errs.CategoryPolicy) {
		t.Fatalf("a Put with no tenant in scope: %v", err)
	}
}

func TestPublishedObjectsAreReadOnly(t *testing.T) {
	s, dir := store(t)
	d, err := s.Put(tenantA, []byte(`{"ro":true}`))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(objectPath(t, dir, "01920000-0000-7000-8000-00000000000a", d))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("published object is writable: %v", info.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(objectPath(t, dir, "01920000-0000-7000-8000-00000000000a", d)))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".incoming-") {
			t.Fatalf("a staging file was left behind: %s", e.Name())
		}
	}
}

func objectPath(t *testing.T, root, tenant string, d canonical.Digest) string {
	t.Helper()
	hex := strings.TrimPrefix(d.String(), canonical.DigestPrefix)
	return filepath.Join(root, tenant, hex[:2], hex)
}
