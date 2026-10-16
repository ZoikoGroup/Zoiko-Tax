package ai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
)

// The Go half of the Go<->Python data-class parity check. Phase 1 (read-only
// research, same branch's earlier report) found both languages refuse the
// same inputs but sometimes disagree on WHICH reason code, and that no test
// proved either fact. This file and intelligence/tests/test_data_class_parity.py
// read the one shared fixture (contracts/privacy/data-class-parity-cases.json)
// so the two suites can never silently drift apart from each other -- same
// pattern as schema_vocabulary_test.go / test_ai_schemas.py reading
// contracts/schemas/ai/*.schema.json, and load_test.go / test_data_class_enforcement.py
// reading intelligence/config/registry.dev.json.
const parityFixturePath = "../../../../contracts/privacy/data-class-parity-cases.json"

type parityCase struct {
	Name                 string   `json:"name"`
	PermittedDataClasses []string `json:"permitted_data_classes"`
	RequestDataClasses   []string `json:"request_data_classes"`
	ExpectGoReason       *string  `json:"expect_go_reason"`
	ExpectPythonReason   *string  `json:"expect_python_reason"`
	KnownDivergence      bool     `json:"known_divergence"`
	Note                 string   `json:"note"`
}

type parityFixture struct {
	Cases []parityCase `json:"cases"`
}

func loadParityFixture(t *testing.T) parityFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(parityFixturePath))
	if err != nil {
		t.Fatalf("read parity fixture: %v", err)
	}
	var f parityFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse parity fixture: %v", err)
	}
	return f
}

// classesOrNil converts a JSON string list (which may be nil, meaning the
// fixture's null) into the []privacy.Class the Go types use. A JSON null and
// a JSON [] both arrive here as a nil Go slice -- encoding/json does not
// distinguish them for a []string field, which is itself the point: Go's own
// len(nil) == len([]T{}) == 0 makes the distinction meaningless downstream,
// exactly as parityCase's note fields for the null/empty pairs explain.
func classesOrNil(labels []string) []privacy.Class {
	if labels == nil {
		return nil
	}
	out := make([]privacy.Class, len(labels))
	for i, l := range labels {
		out[i] = privacy.Class(l)
	}
	return out
}

var parityTestTenant = id.NewTenantID(uuid.MustParse("22222222-2222-7222-8222-222222222222"))

// evaluateParityCase builds the minimal policy and context the fixture case
// needs and returns the reason Evaluate actually produced ("" for allowed).
// NewPolicy's own PermittedDataClasses validation (governance.go:237-244)
// never rejects these fixtures: every case's permitted_data_classes is
// either empty/nil or a list of real P0-P7 labels never containing P7 itself
// (register-time validation is not what this fixture is about -- see Phase
// 1's report on why the request side, not the registry side, carries the
// unknown/lowercase/whitespace labels).
func evaluateParityCase(t *testing.T, c parityCase) string {
	t.Helper()
	policy, err := NewPolicy([]UseCasePolicy{
		{
			UseCase:              "parity-test",
			Owner:                "lane-l",
			MaxAuthority:         AuthorityOutcomeA1,
			MaxRiskTier:          RiskTierT2,
			PermittedRegions:     []string{"eu-west-1"},
			PermittedDataClasses: classesOrNil(c.PermittedDataClasses),
		},
	}, nil, false)
	if err != nil {
		t.Fatalf("%s: NewPolicy: %v", c.Name, err)
	}

	ctx := GovernanceContext{
		Tenant:           parityTestTenant,
		UseCase:          "parity-test",
		AuthorityOutcome: AuthorityOutcomeA1,
		RiskTier:         RiskTierT2,
		Region:           "eu-west-1",
		DataClasses:      classesOrNil(c.RequestDataClasses),
	}

	d := Evaluate(policy, ctx)
	if d.Allowed {
		return ""
	}
	return string(d.Reason)
}

func TestDataClassParity(t *testing.T) {
	fixture := loadParityFixture(t)
	if len(fixture.Cases) == 0 {
		t.Fatal("parity fixture has no cases")
	}

	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := evaluateParityCase(t, c)
			want := ""
			if c.ExpectGoReason != nil {
				want = *c.ExpectGoReason
			}
			if got != want {
				t.Fatalf("%s: Evaluate reason = %q, want %q (fixture expect_go_reason)", c.Name, got, want)
			}
		})
	}
}

// TestDataClassParityAgreementIsExplicit guards against a case being marked
// "the two sides agree" (known_divergence: false) while its two expected
// reasons actually differ -- that combination would be a silently wrong
// fixture, not a real agreement.
func TestDataClassParityAgreementIsExplicit(t *testing.T) {
	fixture := loadParityFixture(t)
	for _, c := range fixture.Cases {
		if c.KnownDivergence {
			continue
		}
		goReason := ""
		if c.ExpectGoReason != nil {
			goReason = *c.ExpectGoReason
		}
		pyReason := ""
		if c.ExpectPythonReason != nil {
			pyReason = *c.ExpectPythonReason
		}
		if goReason != pyReason {
			t.Errorf("%s: known_divergence is false but expect_go_reason %q != expect_python_reason %q",
				c.Name, goReason, pyReason)
		}
	}
}

// TestDataClassParityDivergenceCasesAreNoted: every case recorded as a known
// divergence must say why, so a future reader does not have to reconstruct
// the reasoning from the two implementations by hand.
func TestDataClassParityDivergenceCasesAreNoted(t *testing.T) {
	fixture := loadParityFixture(t)
	for _, c := range fixture.Cases {
		if c.KnownDivergence && c.Note == "" {
			t.Errorf("%s: known_divergence is true but note is empty", c.Name)
		}
	}
}

// TestDataClassParityCaseNamesMatchPython is the cross-language guard: this
// file hard-codes the exact case-name list Phase 1's fixture defines, so a
// case renamed, added or removed on the Python side without a matching edit
// here (or vice versa) fails loudly instead of one side silently covering
// fewer cases than the other. intelligence/tests/test_data_class_parity.py
// carries the identical literal list for the same reason.
func TestDataClassParityCaseNamesMatchPython(t *testing.T) {
	want := []string{
		"request_empty_list",
		"request_null_missing",
		"request_unknown_label",
		"request_lowercase_label",
		"request_whitespace_label",
		"request_mixed_valid_and_unknown",
		"request_p7_alone",
		"request_p7_with_allowed_class",
		"request_valid_not_permitted",
		"request_valid_permitted",
		"request_duplicates",
		"permitted_empty_list",
		"permitted_null_missing",
		"order_p7_and_unknown_together",
	}
	fixture := loadParityFixture(t)
	got := make([]string, len(fixture.Cases))
	for i, c := range fixture.Cases {
		got[i] = c.Name
	}
	if len(got) != len(want) {
		t.Fatalf("fixture has %d cases, this test expects %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("case %d: fixture has %q, want %q", i, got[i], want[i])
		}
	}
}
