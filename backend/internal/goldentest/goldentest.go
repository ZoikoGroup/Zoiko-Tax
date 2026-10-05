// Package goldentest loads the golden corpora that are not decimal vectors:
// determination schedules, classification launch corpora and the rest of
// ZTAX-QA-001's GoldenCase sets (Build Plan W2 lane A).
//
// It applies the discipline fiscaltest applies to the decimal corpus, to any
// set: every file is registered in its directory's manifest.json with a
// sha256 over its exact bytes, a set that has moved is a failure rather than a
// silently relaxed assertion, and every set carries the provenance that makes
// it evidence rather than a regression snapshot — an oracle, a source, a
// reviewer, a registration date and the content version it was checked
// against. Each case also names its own oracle.
//
// Cases are returned as raw JSON. The package does not know what a
// determination is; the runner beside each domain package decodes its own
// cases, through that package's production decoders wherever one exists.
//
// Production code may not import this package (.golangci.yml,
// golden-loader-is-test-only), and the import of testing makes that a second
// barrier.
package goldentest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Manifest registers the sets in one corpus directory.
type Manifest struct {
	Schema string     `json:"schema"`
	Note   string     `json:"note"`
	Sets   []Registry `json:"sets"`
}

// Registry is one registered set.
type Registry struct {
	File    string `json:"file"`
	Set     string `json:"set"`
	Version string `json:"version"`
	Cases   int    `json:"cases"`
	SHA256  string `json:"sha256"`
}

// Provenance is what makes a set evidence (ADR-0018 §2.3).
type Provenance struct {
	Oracle         string `json:"oracle"`
	Source         string `json:"source"`
	Reviewer       string `json:"reviewer"`
	Registered     string `json:"registered"`
	ContentVersion string `json:"contentVersion"`
}

// Set is one corpus file.
type Set struct {
	Set         string     `json:"set"`
	Version     string     `json:"version"`
	Description string     `json:"description"`
	Provenance  Provenance `json:"provenance"`
	// Fixtures is set-wide input every case shares — an ontology release, a
	// catalog — decoded by the runner like the cases.
	Fixtures json.RawMessage   `json:"fixtures,omitempty"`
	Cases    []json.RawMessage `json:"cases"`
}

// Header is the part of every case the loader checks: an id and an oracle.
type Header struct {
	ID           string   `json:"id"`
	Oracle       string   `json:"oracle"`
	Requirements []string `json:"requirements"`
}

// Load verifies the corpus at root against its manifest and returns its sets.
func Load(tb testing.TB, root string) []Set {
	tb.Helper()
	Check(tb, root)
	var m Manifest
	read(tb, filepath.Join(root, "manifest.json"), &m)
	var out []Set
	for _, r := range m.Sets {
		var s Set
		read(tb, filepath.Join(root, r.File), &s)
		if len(s.Cases) != r.Cases {
			tb.Fatalf("%s: %d cases, manifest registers %d", r.File, len(s.Cases), r.Cases)
		}
		requireProvenance(tb, s)
		seen := map[string]bool{}
		for i, raw := range s.Cases {
			var h Header
			if err := json.Unmarshal(raw, &h); err != nil {
				tb.Fatalf("%s case %d: %v", r.File, i, err)
			}
			if strings.TrimSpace(h.ID) == "" || strings.TrimSpace(h.Oracle) == "" {
				tb.Fatalf("%s case %d has no id or no oracle; a case with no oracle proves only that we agree with ourselves", r.File, i)
			}
			if seen[h.ID] {
				tb.Fatalf("%s repeats case %s", r.File, h.ID)
			}
			seen[h.ID] = true
		}
		out = append(out, s)
	}
	return out
}

// Check reports every disagreement between a corpus and its manifest: a set
// whose digest moved, a set present and unregistered, a set registered and
// absent.
func Check(tb testing.TB, root string) {
	tb.Helper()
	var m Manifest
	read(tb, filepath.Join(root, "manifest.json"), &m)
	if len(m.Sets) == 0 {
		tb.Fatalf("%s/manifest.json registers no sets", root)
	}
	registered := map[string]string{}
	for _, r := range m.Sets {
		registered[r.File] = r.SHA256
	}
	present, err := filepath.Glob(filepath.Join(root, "*.json"))
	if err != nil {
		tb.Fatal(err)
	}
	for _, path := range present {
		name := filepath.Base(path)
		if name == "manifest.json" {
			continue
		}
		want, ok := registered[name]
		if !ok {
			tb.Errorf("%s is present but not registered", name)
			continue
		}
		if got := digest(tb, path); got != want {
			tb.Errorf("%s: digest %s, manifest registers %s; re-register it and have the change reviewed as content, not as a test fix", name, got, want)
		}
		delete(registered, name)
	}
	for name := range registered {
		tb.Errorf("manifest registers %s, which is not present", name)
	}
}

func requireProvenance(tb testing.TB, s Set) {
	tb.Helper()
	for field, v := range map[string]string{
		"version": s.Version, "description": s.Description, "provenance.oracle": s.Provenance.Oracle,
		"provenance.source": s.Provenance.Source, "provenance.reviewer": s.Provenance.Reviewer,
		"provenance.registered": s.Provenance.Registered, "provenance.contentVersion": s.Provenance.ContentVersion,
	} {
		if strings.TrimSpace(v) == "" {
			tb.Errorf("set %s: %s is empty", s.Set, field)
		}
	}
}

func digest(tb testing.TB, path string) string {
	tb.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		tb.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func read(tb testing.TB, path string, into any) {
	tb.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("read %s: %v", path, err)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		tb.Fatalf("parse %s: %v", path, err)
	}
}
