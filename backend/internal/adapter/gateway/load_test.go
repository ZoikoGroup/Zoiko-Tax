package gateway

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
)

// The development registry is the one file both halves load: the Python
// Gateway in compose, and this pre-check. It must be valid for both.
func TestTheDevelopmentRegistryLoads(t *testing.T) {
	p, err := LoadPolicy("../../../../intelligence/config/registry.dev.json")
	if err != nil {
		t.Fatal(err)
	}
	uc, ok := p.UseCase("classification-review")
	if !ok || uc.MaxAuthority != "A1" || len(uc.PermittedRegions) != 1 {
		t.Fatalf("classification-review: %+v %t", uc, ok)
	}
}

func TestLoadPolicyRefusesWhatTheGatewayCouldNotServe(t *testing.T) {
	for name, body := range map[string]string{
		"no route":      `{"use_cases":[{"use_case_id":"x","owner":"o","description":"d","max_risk_tier":"T1","max_authority":"A1"}]}`,
		"A5 ceiling":    `{"use_cases":[{"use_case_id":"x","owner":"o","description":"d","max_risk_tier":"T1","max_authority":"A5","route":{}}]}`,
		"unknown field": `{"use_cases":[],"surprise":true}`,
	} {
		path := filepath.Join(t.TempDir(), "r.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadPolicy(path); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	// An empty registry is valid and refuses everything.
	path := filepath.Join(t.TempDir(), "r.json")
	_ = os.WriteFile(path, []byte(`{"use_cases":[]}`), 0o600)
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if d := ai.Evaluate(p, testCall().Governance); d.Allowed {
		t.Fatal("an empty registry permitted a call")
	}
}
