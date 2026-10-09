package gateway

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
)

// The route profile and AI train version this file's temp registry declares
// for classification-review. Shared between the registry JSON startPythonGateway
// writes and the assertions that check a reply actually carries them, rather
// than typing the literals a second time.
const (
	testGatewayAITrain       = "ai-e2e"
	testRouteModelProfile    = "model:cls"
	testRouteProviderProfile = "provider:a"
	testRoutePromptProfile   = "prompt:cls-v4"
)

// startPythonGateway starts the real Python Gateway
// (intelligence/src/ztax_gateway/server.py) on a free local port, with a
// temp registry that permits exactly one use case — classification-review,
// routed to the profile named in the constants above. runtime selects
// ZTAX_GATEWAY_RUNTIME; when runtime is "", any ZTAX_GATEWAY_RUNTIME the
// test process itself inherited is stripped out of the child's environment
// rather than merely left unset by us, so the Python side falls back to its
// own default (the UnconfiguredRuntime) regardless of what the shell running
// `go test` happened to export.
//
// It skips the whole test, via t.Skip, when ZTAX_E2E_PYTHON is not set: the
// Go job does not carry the AI train's toolchain, and each side's own suite
// holds it to the canonical schemas regardless. Once the process is
// started, it blocks until the server answers a real call — i.e. until
// ReasonGatewayUnavailable has cleared — before returning the address, so
// every caller starts from a server that is actually ready.
func startPythonGateway(t *testing.T, runtime string) (addr string) {
	t.Helper()
	python := os.Getenv("ZTAX_E2E_PYTHON")
	if python == "" {
		t.Skip("ZTAX_E2E_PYTHON is not set")
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr = lis.Addr().String()
	_ = lis.Close()

	config := filepath.Join(t.TempDir(), "gateway.json")
	registry := fmt.Sprintf(
		`{"use_cases":[{"use_case_id":"classification-review","owner":"lane-l",`+
			`"description":"Propose an ontology mapping for review","max_risk_tier":"T2","max_authority":"A1",`+
			`"permitted_regions":["euc1-dev-01"],"route":{"model_profile":%q,"provider_profile":%q,`+
			`"prompt_profile":%q}}]}`,
		testRouteModelProfile, testRouteProviderProfile, testRoutePromptProfile,
	)
	if err := os.WriteFile(config, []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(python, "-m", "ztax_gateway.server") //nolint:gosec // a test-only interpreter path
	inherited := os.Environ()
	base := make([]string, 0, len(inherited))
	for _, e := range inherited {
		if !strings.HasPrefix(e, "ZTAX_GATEWAY_RUNTIME=") {
			base = append(base, e)
		}
	}
	env := append(base,
		"ZTAX_GATEWAY_CONFIG="+config, "ZTAX_GATEWAY_REGION=euc1-dev-01", "ZTAX_GATEWAY_AI_TRAIN="+testGatewayAITrain,
		"ZTAX_GATEWAY_ADDRESS="+addr, "ZTAX_GATEWAY_INSECURE_LOCAL=true", "ZTAX_ENVIRONMENT=development")
	if runtime != "" {
		env = append(env, "ZTAX_GATEWAY_RUNTIME="+runtime)
	}
	cmd.Env = env
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	g := transport(t, addr)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		c, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := g.Invoke(c, testCall())
		cancel()
		if errs.ReasonOf(err) != ReasonGatewayUnavailable {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	return addr
}

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
	addr := startPythonGateway(t, "")
	g := transport(t, addr)

	_, err := g.Invoke(ctx(t), testCall())
	if errs.CategoryOf(err) != errs.CategoryUnsupported || errs.ReasonOf(err) != ReasonGatewayNotConfigured {
		t.Fatalf("permitted call: %v; want AI_GATEWAY_NOT_CONFIGURED from the Python runtime", err)
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

// TestFakeRuntimeCrossesTheBoundary runs this transport against the real
// Python Gateway with ZTAX_GATEWAY_RUNTIME=fake. Unlike
// TestCrossLanguageBoundary, governance permits the call AND a runtime is
// configured, so the call succeeds: the assertion is that FakeRuntime's
// canned classification-proposal result crosses back to Go unchanged.
//
// testCall (grpc_test.go) already builds a KindClassificationProposal call
// for classification-review, which is the only kind FakeRuntime's default
// answers with the proposed_code below, so it is reused as-is.
//
// "FAKE-UNCLASSIFIED" and "0.9000" mirror FakeRuntime's default
// CLASSIFICATION_PROPOSAL result (_DEFAULT_PROPOSAL in
// intelligence/src/ztax_gateway/fake_runtime.py) exactly; they are not
// independently chosen here.
func TestFakeRuntimeCrossesTheBoundary(t *testing.T) {
	addr := startPythonGateway(t, "fake")
	g := transport(t, addr)

	reply, err := g.Invoke(ctx(t), testCall())
	if err != nil {
		t.Fatal(err)
	}
	if reply.ProposedCode != "FAKE-UNCLASSIFIED" || reply.Confidence != "0.9000" {
		t.Fatalf("proposal = %+v", reply)
	}
	if reply.ModelProfile != testRouteModelProfile || reply.ProviderProfile != testRouteProviderProfile ||
		reply.PromptProfile != testRoutePromptProfile || reply.AiTrainVersion != testGatewayAITrain {
		t.Fatalf("routing = %+v", reply)
	}
}
