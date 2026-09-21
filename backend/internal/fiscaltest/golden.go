package fiscaltest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The golden decimal corpus is read here rather than in the package it tests.
// internal/domain is pure — no I/O, enforced by depguard (ADR-0007 §2.5) — and
// a test that reads its own vectors is still a test that opens a file. Nothing
// in this file touches a decimal: it hands the fiscal package strings, and the
// arithmetic stays where the arithmetic belongs.

// GoldenManifest registers the vector sets and their digests. ADR-0002 §2.7
// makes the corpus a build artifact: a set that can be edited without ceremony
// is a fixture, and a fixture cannot be evidence.
type GoldenManifest struct {
	Schema string              `json:"schema"`
	Note   string              `json:"note"`
	Sets   []GoldenManifestSet `json:"sets"`
}

// GoldenManifestSet is one registered vector set.
type GoldenManifestSet struct {
	File    string `json:"file"`
	Set     string `json:"set"`
	Version string `json:"version"`
	Cases   int    `json:"cases"`
	SHA256  string `json:"sha256"`
}

// GoldenSet is one vector file: a version, its provenance and its cases.
type GoldenSet struct {
	Set         string           `json:"set"`
	Version     string           `json:"version"`
	Description string           `json:"description"`
	Provenance  GoldenProvenance `json:"provenance"`
	Cases       []GoldenCase     `json:"cases"`
}

// GoldenProvenance is what makes a set evidence rather than a regression
// snapshot: where the expected answers come from, who reviewed them, and
// against which content version (ADR-0018 §2.3).
type GoldenProvenance struct {
	Oracle         string `json:"oracle"`
	Source         string `json:"source"`
	Reviewer       string `json:"reviewer"`
	Registered     string `json:"registered"`
	ContentVersion string `json:"contentVersion"`
}

// GoldenCase is one vector. Inputs and expectations are canonical decimal
// strings, never numbers: a JSON number would be parsed as a float on the way
// in, which is the failure the corpus exists to detect.
type GoldenCase struct {
	ID string `json:"id"`
	Op string `json:"op"`

	Inputs []string `json:"inputs"`

	// Policy is the content-bundle form of the rounding policy. PolicyRaw
	// carries a malformed policy verbatim, for the cases that assert what the
	// decoder refuses.
	Policy    json.RawMessage `json:"policy"`
	PolicyRaw string          `json:"policyRaw"`

	Expect      []string `json:"expect"`
	ExpectError string   `json:"expectError"`
	Oracle      string   `json:"oracle"`
}

// HasPolicy reports whether the case carries a policy in either form.
func (c GoldenCase) HasPolicy() bool { return len(c.Policy) > 0 || c.PolicyRaw != "" }

// PolicyContent returns the bytes to decode a policy from.
func (c GoldenCase) PolicyContent() []byte {
	if c.PolicyRaw != "" {
		return []byte(c.PolicyRaw)
	}
	return c.Policy
}

// LoadGoldenSets reads every registered set under root, verifying its digest
// first. Verifying before reading is the point: an unregistered edit must not
// be able to relax an assertion in the same run that reports the corpus green.
func LoadGoldenSets(tb testing.TB, root string) []GoldenSet {
	tb.Helper()

	manifest := LoadGoldenManifest(tb, root)
	sets := make([]GoldenSet, 0, len(manifest.Sets))
	for _, entry := range manifest.Sets {
		path := filepath.Join(root, entry.File)
		if got := GoldenDigest(tb, path); got != entry.SHA256 {
			tb.Fatalf("%s: digest %s does not match the registered %s", entry.File, got, entry.SHA256)
		}
		var set GoldenSet
		readGoldenJSON(tb, path, &set)
		if len(set.Cases) == 0 {
			tb.Fatalf("%s: no cases", entry.File)
		}
		requireGoldenProvenance(tb, set)
		sets = append(sets, set)
	}
	return sets
}

// LoadGoldenManifest reads the registration file.
func LoadGoldenManifest(tb testing.TB, root string) GoldenManifest {
	tb.Helper()
	var manifest GoldenManifest
	readGoldenJSON(tb, filepath.Join(root, "manifest.json"), &manifest)
	if len(manifest.Sets) == 0 {
		tb.Fatal("manifest.json registers no vector sets")
	}
	return manifest
}

// CheckGoldenRegistration reports every way the corpus and its manifest can
// disagree: a set whose digest has moved, a set present but unregistered, and a
// set registered but absent.
func CheckGoldenRegistration(tb testing.TB, root string) {
	tb.Helper()

	registered := make(map[string]string)
	for _, entry := range LoadGoldenManifest(tb, root).Sets {
		registered[entry.File] = entry.SHA256
	}

	present, err := filepath.Glob(filepath.Join(root, "*.json"))
	if err != nil {
		tb.Fatalf("list vector sets: %v", err)
	}
	for _, path := range present {
		name := filepath.Base(path)
		if name == "manifest.json" {
			continue
		}
		want, ok := registered[name]
		if !ok {
			tb.Errorf("%s is present but not registered in manifest.json", name)
			continue
		}
		if got := GoldenDigest(tb, path); got != want {
			tb.Errorf("%s: digest %s, manifest registers %s — run `make golden-register` "+
				"and have the change reviewed as content, not as a test fix", name, got, want)
		}
		delete(registered, name)
	}
	for name := range registered {
		tb.Errorf("manifest.json registers %s, which is not present", name)
	}
}

// GoldenDigest is the sha256 of a file's exact bytes.
func GoldenDigest(tb testing.TB, path string) string {
	tb.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func requireGoldenProvenance(tb testing.TB, set GoldenSet) {
	tb.Helper()
	for field, value := range map[string]string{
		"version":                   set.Version,
		"description":               set.Description,
		"provenance.oracle":         set.Provenance.Oracle,
		"provenance.source":         set.Provenance.Source,
		"provenance.reviewer":       set.Provenance.Reviewer,
		"provenance.registered":     set.Provenance.Registered,
		"provenance.contentVersion": set.Provenance.ContentVersion,
	} {
		if strings.TrimSpace(value) == "" {
			tb.Errorf("set %s: %s is empty", set.Set, field)
		}
	}
}

func readGoldenJSON(tb testing.TB, path string, into any) {
	tb.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		tb.Fatalf("parse %s: %v", path, err)
	}
}
