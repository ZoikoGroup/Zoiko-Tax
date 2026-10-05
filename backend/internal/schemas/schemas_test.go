package schemas_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/classification"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ontology"
)

const contracts = "../../../contracts/schemas"

func defs(t *testing.T, dir string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(contracts, dir, "common.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s.Defs
}

func enum(t *testing.T, d map[string]json.RawMessage, name string) []string {
	t.Helper()
	var e struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(d[name], &e); err != nil || len(e.Enum) == 0 {
		t.Fatalf("$defs/%s has no enum (%v)", name, err)
	}
	out := append([]string(nil), e.Enum...)
	sort.Strings(out)
	return out
}

func strs[T ~string](xs []T) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	sort.Strings(out)
	return out
}

func same(t *testing.T, what string, schema, golang []string) {
	t.Helper()
	if !slices.Equal(schema, golang) {
		t.Errorf("%s: schema %v, Go %v", what, schema, golang)
	}
}

// The classification schemas' closed vocabularies are the ontology's and
// classification's (ZTAX-CLS-001).
func TestClassificationVocabulariesMatchTheDomain(t *testing.T) {
	d := defs(t, "classification")
	same(t, "family", enum(t, d, "family"), strs(ontology.Families()))
	same(t, "qualifier", enum(t, d, "qualifier"), strs(ontology.Qualifiers()))
	same(t, "chargeType", enum(t, d, "chargeType"), strs(ontology.ChargeTypes()))
	same(t, "mappingSource", enum(t, d, "mappingSource"), strs(ontology.MappingSources()))

	statuses := enum(t, d, "status")
	for _, s := range statuses {
		if !classification.Status(s).Valid() {
			t.Errorf("schema status %s is not a classification.Status", s)
		}
	}
	if len(statuses) != 5 {
		t.Errorf("schema has %d statuses; classification has 5", len(statuses))
	}
	same(t, "revenueTreatment", enum(t, d, "revenueTreatment"), strs([]classification.RevenueTreatment{
		classification.RevenueIncluded, classification.RevenueExcluded, classification.RevenuePartial,
		classification.RevenueConditional, classification.RevenueAmbiguous, classification.RevenueUnsupported,
	}))
}

// Every requirement id a golden case cites is well-formed under the
// verification schema's pattern, and every case is registered with an oracle
// (the loader enforces the second; this enforces the first).
func TestGoldenCaseRequirementIDsMatchTheVerificationSchema(t *testing.T) {
	d := defs(t, "verification")
	var p struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(d["requirementId"], &p); err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(p.Pattern)
	for _, dir := range []string{"determination", "classification"} {
		files, err := filepath.Glob(filepath.Join("../../testdata/golden", dir, "*.json"))
		if err != nil || len(files) < 2 {
			t.Fatalf("%s corpus: %v %v", dir, files, err)
		}
		for _, f := range files {
			if filepath.Base(f) == "manifest.json" {
				continue
			}
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var set struct {
				Cases []struct {
					ID   string   `json:"id"`
					Reqs []string `json:"requirements"`
				} `json:"cases"`
			}
			if err := json.Unmarshal(raw, &set); err != nil {
				t.Fatal(err)
			}
			for _, c := range set.Cases {
				for _, r := range c.Reqs {
					if !re.MatchString(r) {
						t.Errorf("%s %s cites %q", filepath.Base(f), c.ID, r)
					}
				}
			}
		}
	}
}
