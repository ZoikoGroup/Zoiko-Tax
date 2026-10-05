package gateway

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
)

// TestCrossLanguageBoundary runs this transport against the real Python
// Gateway (intelligence/src/ztax_gateway/server.py) over gRPC. It needs a
// Python with the Gateway installed — `pip install -e intelligence` — named by
// ZTAX_E2E_PYTHON, and is skipped without it: the Go job does not carry the AI
// train's toolchain, and each side's own suite holds it to the canonical
// schemas regardless.
//
// The production server runs the UnconfiguredRuntime, so a permitted call
// comes back AI_GATEWAY_NOT_CONFIGURED. That is the assertion: governance on
// the Python side permitted it, the runtime was reached, and the answer
// travelled back as the estate error the Go side expects.
func TestCrossLanguageBoundary(t *testing.T) {
	python := os.Getenv("ZTAX_E2E_PYTHON")
	if python == "" {
		t.Skip("ZTAX_E2E_PYTHON is not set")
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	config := filepath.Join(t.TempDir(), "gateway.json")
	if err := os.WriteFile(config, []byte(`{"use_cases":[{"use_case_id":"classification-review","owner":"lane-l",
"description":"Propose an ontology mapping for review","max_risk_tier":"T2","max_authority":"A1",
"permitted_regions":["euc1-dev-01"],"route":{"model_profile":"model:cls","provider_profile":"provider:a",
"prompt_profile":"prompt:cls-v4"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, "-m", "ztax_gateway.server") //nolint:gosec // a test-only interpreter path
	cmd.Env = append(os.Environ(),
		"ZTAX_GATEWAY_CONFIG="+config, "ZTAX_GATEWAY_REGION=euc1-dev-01", "ZTAX_GATEWAY_AI_TRAIN=ai-e2e",
		"ZTAX_GATEWAY_ADDRESS="+addr, "ZTAX_GATEWAY_INSECURE_LOCAL=true", "ZTAX_ENVIRONMENT=development")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	g := transport(t, addr)
	deadline := time.Now().Add(20 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		c, cancel := context.WithTimeout(context.Background(), time.Second)
		_, last = g.Invoke(c, testCall())
		cancel()
		if errs.ReasonOf(last) != ReasonGatewayUnavailable {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if errs.CategoryOf(last) != errs.CategoryUnsupported || errs.ReasonOf(last) != ReasonGatewayNotConfigured {
		t.Fatalf("permitted call: %v; want AI_GATEWAY_NOT_CONFIGURED from the Python runtime", last)
	}

	unknown := testCall()
	unknown.Governance.UseCase = "not-registered"
	if _, err := g.Invoke(ctx(t), unknown); errs.ReasonOf(err) != ai.ReasonUnknownUseCase || errs.CategoryOf(err) != errs.CategoryPolicy {
		t.Fatalf("unknown use case: %v", err)
	}
	a5 := testCall()
	a5.Governance.AuthorityOutcome = "A5"
	if _, err := g.Invoke(ctx(t), a5); errs.ReasonOf(err) != ai.ReasonAuthorityRefused {
		t.Fatalf("A5: %v", err)
	}
	away := testCall()
	away.Governance.Region = "use1-prod-01"
	if _, err := g.Invoke(ctx(t), away); errs.ReasonOf(err) != ai.ReasonResidencyRefused {
		t.Fatalf("another region: %v", err)
	}
}
