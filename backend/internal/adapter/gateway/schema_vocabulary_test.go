package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
)

// This lives in the adapter rather than beside the constants because it reads
// files, and the domain may not (ADR-0007 §2.5). It is the Go half of the
// schema drift gate; intelligence/tests/test_ai_schemas.py is the Python half.
//
// ADR-0006 §2.2 makes contracts/schemas/ the schema authority. These lists are
// compared in order, not as sets: the order is the order of the levels, and
// the policy gate's ceiling comparisons are index comparisons.

const schemaDir = "../../../../contracts/schemas/ai"

func readSchema(t *testing.T, name string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(schemaDir, name+".schema.json"))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return doc
}

func stringList(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func strs[T ~string](xs []T) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	return out
}

func TestVocabulariesMatchTheCanonicalSchemas(t *testing.T) {
	check := func(name string, file, code []string) {
		t.Helper()
		if !slices.Equal(file, code) {
			t.Errorf("%s: schema has %v, Go has %v", name, file, code)
		}
	}
	authority := readSchema(t, "authority-outcome")
	check("authority-outcome", stringList(t, authority["enum"]), strs(ai.AuthorityOutcomes))
	check("risk-tier", stringList(t, readSchema(t, "risk-tier")["enum"]), strs(ai.RiskTiers))
	check("governance-refusal", stringList(t, readSchema(t, "governance-refusal")["enum"]), strs(ai.RefusalCodes))

	// The prohibited level is the last one, and the permitted ceiling is the
	// one immediately below it.
	prohibited := stringList(t, authority["x-ztax-prohibited"])
	if !slices.Equal(prohibited, []string{string(ai.AuthorityOutcomeA5)}) {
		t.Errorf("x-ztax-prohibited = %v, want [A5]", prohibited)
	}
	if n := len(ai.AuthorityOutcomes); ai.AuthorityOutcomes[n-2] != ai.MaxPermittedAuthority {
		t.Errorf("MaxPermittedAuthority = %s, want the level below A5", ai.MaxPermittedAuthority)
	}

	var common struct {
		Defs struct {
			PrivacyClass struct {
				Enum []string `json:"enum"`
			} `json:"privacyClass"`
		} `json:"$defs"`
	}
	raw, err := os.ReadFile(filepath.Join(schemaDir, "common.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &common); err != nil {
		t.Fatal(err)
	}
	check("privacyClass", common.Defs.PrivacyClass.Enum, strs(privacy.Classes))
}

// Every schema in the directory parses and declares draft 2020-12, so a file
// added there with a typo fails here rather than in a generator later.
func TestEverySchemaIsDraft2020(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(schemaDir, "*.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 13 {
		t.Fatalf("found %d schemas, want the nine registry objects and their four shared files", len(files))
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Schema string `json:"$schema"`
			Title  string `json:"title"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		if doc.Schema != "https://json-schema.org/draft/2020-12/schema" || doc.Title == "" {
			t.Errorf("%s: $schema %q, title %q", filepath.Base(f), doc.Schema, doc.Title)
		}
	}
}
