package fiscal_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
)

func TestDecodeRoundingPolicyFromContent(t *testing.T) {
	p, err := fiscal.DecodeRoundingPolicy([]byte(`{"mode":"HALF_EVEN","scale":0,"basis":"JURISDICTION_TOTAL"}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Mode() != fiscal.RoundHalfEven {
		t.Errorf("mode: got %s, want HALF_EVEN", p.Mode())
	}
	// Scale 0 is a legal scale — JPY, and any jurisdiction that rounds to the
	// whole unit. It must survive decoding as a stated value rather than as an
	// absence that happens to read the same way.
	if p.Scale() != 0 {
		t.Errorf("scale: got %d, want 0", p.Scale())
	}
	if p.Basis() != fiscal.BasisJurisdictionTotal {
		t.Errorf("basis: got %s, want JURISDICTION_TOTAL", p.Basis())
	}
	if got, want := p.String(), "HALF_EVEN@0/JURISDICTION_TOTAL"; got != want {
		t.Errorf("String(): got %s, want %s", got, want)
	}
}

func TestDecodeRoundingPolicyRejections(t *testing.T) {
	cases := map[string]struct {
		content string
		wants   string
	}{
		"unknown mode":        {`{"mode":"BANKERS","scale":2,"basis":"LINE"}`, "unknown rounding mode"},
		"unknown basis":       {`{"mode":"HALF_UP","scale":2,"basis":"INVOICE"}`, "unknown rounding basis"},
		"scale above maximum": {`{"mode":"HALF_UP","scale":13,"basis":"LINE"}`, "out of range"},
		"negative scale":      {`{"mode":"HALF_UP","scale":-1,"basis":"LINE"}`, "out of range"},
		"absent mode":         {`{"scale":2,"basis":"LINE"}`, "no mode"},
		"absent scale":        {`{"mode":"HALF_UP","basis":"LINE"}`, "no scale"},
		"absent basis":        {`{"mode":"HALF_UP","scale":2}`, "no basis"},
		"unknown key":         {`{"mode":"HALF_UP","scale":2,"basis":"LINE","rounding":"HALF_EVEN"}`, "unknown field"},
		"not an object":       {`"HALF_UP@2/LINE"`, "decode rounding policy"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := fiscal.DecodeRoundingPolicy([]byte(c.content))
			if err == nil {
				t.Fatalf("accepted %s as %s", c.content, p)
			}
			if !strings.Contains(err.Error(), c.wants) {
				t.Errorf("error %q does not contain %q", err, c.wants)
			}
		})
	}
}

func TestRoundingPolicyIsRecordedByName(t *testing.T) {
	// ADR-0002 §2.4 and §5.1 control 3. A decision that cannot name its
	// rounding does not replay, and an ordinal stops naming it the moment a
	// mode is added to the enum.
	encoded, err := json.Marshal(fiscaltest.Policy(t, fiscal.RoundHalfEven, 3, fiscal.BasisTaxComponent))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"mode":"HALF_EVEN","scale":3,"basis":"TAX_COMPONENT"}`
	if got := string(encoded); got != want {
		t.Fatalf("evidence form:\n got %s\nwant %s", got, want)
	}
}

func TestRoundingPolicyRoundTripsThroughEvidence(t *testing.T) {
	// The replay path: a policy read out of a sealed evidence record has to be
	// the policy that produced it.
	for _, mode := range []fiscal.RoundingMode{
		fiscal.RoundHalfUp, fiscal.RoundHalfEven, fiscal.RoundHalfDown,
		fiscal.RoundDown, fiscal.RoundUp, fiscal.RoundCeiling, fiscal.RoundFloor,
	} {
		t.Run(string(mode), func(t *testing.T) {
			original := fiscaltest.Policy(t, mode, 2, fiscal.BasisLine)
			encoded, err := json.Marshal(original)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var replayed fiscal.RoundingPolicy
			if err := json.Unmarshal(encoded, &replayed); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if replayed != original {
				t.Errorf("got %s, want %s", replayed, original)
			}
		})
	}
}

func TestZeroValuePolicyCannotBeRecorded(t *testing.T) {
	// A policy that never came from content has nothing to record. Marshalling
	// it into evidence would produce a record naming a mode of "", which would
	// replay as an error much later and much further from the cause.
	var unset fiscal.RoundingPolicy
	if _, err := json.Marshal(unset); err == nil {
		t.Fatal("the zero-value policy marshalled into evidence")
	}
}

func TestPolicyNestsInAContentDocument(t *testing.T) {
	// The shape a RuleVersion carries: the policy is a field of a larger signed
	// document, not a standalone file, so decoding has to work in place.
	const ruleVersion = `{
		"ruleSemanticId": "ZTAX-RULE-VAT-STANDARD",
		"roundingPolicy": {"mode":"HALF_UP","scale":2,"basis":"LINE"}
	}`
	var decoded struct {
		RuleSemanticID string                `json:"ruleSemanticId"`
		RoundingPolicy fiscal.RoundingPolicy `json:"roundingPolicy"`
	}
	if err := json.Unmarshal([]byte(ruleVersion), &decoded); err != nil {
		t.Fatalf("decode rule version: %v", err)
	}
	if got, want := decoded.RoundingPolicy.String(), "HALF_UP@2/LINE"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
