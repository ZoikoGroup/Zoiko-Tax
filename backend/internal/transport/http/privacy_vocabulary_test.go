package http

import (
	"encoding/json"
	"os"
	"slices"
	"sort"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
)

// This lives in the transport package rather than beside the privacy types
// because it reads a file, and the domain may not (ADR-0007 §2.5). It sits with
// contract_test.go for the same reason that file does: this is where the Go
// side is held to the contracts directory.
//
// The vocabulary is data in contracts/privacy/vocabulary.json, shared with the
// contract lint. This proves the Go constants are exactly that data — a class
// or purpose added on one side only fails here.
func TestVocabularyMatchesTheSharedFile(t *testing.T) {
	raw, err := os.ReadFile("../../../../contracts/privacy/vocabulary.json")
	if err != nil {
		t.Fatalf("read the shared vocabulary: %v", err)
	}
	var v struct {
		Classes        map[string]string `json:"classes"`
		Purposes       map[string]string `json:"purposes"`
		Retention      map[string]string `json:"retention"`
		Redaction      map[string]string `json:"redaction"`
		EvidencePolicy map[string]string `json:"evidencePolicy"`
		AIAllowed      map[string]string `json:"aiAllowed"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	check := func(name string, file map[string]string, code []string) {
		t.Helper()
		keys := make([]string, 0, len(file))
		for k := range file {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sorted := slices.Clone(code)
		sort.Strings(sorted)
		if !slices.Equal(keys, sorted) {
			t.Errorf("%s: vocabulary.json has %v, Go has %v", name, keys, sorted)
		}
	}
	check("classes", v.Classes, strs(privacy.Classes))
	check("purposes", v.Purposes, strs(privacy.Purposes))
	check("retention", v.Retention, strs(privacy.Retentions))
	check("redaction", v.Redaction, strs(privacy.Redactions))
	check("evidencePolicy", v.EvidencePolicy, strs(privacy.EvidencePolicies))
	check("aiAllowed", v.AIAllowed, strs(privacy.AIAllowances))
}

func strs[T ~string](xs []T) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	return out
}
