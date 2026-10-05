package gateway

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ADR-0006 §2.1: "no direct call from Go to any model provider." The network
// enforces it — the regional cell's egress policy denies Go workloads every
// provider endpoint — and the depguard rule no-direct-provider-call refuses
// the SDK imports. This test is the third expression of the same rule, and the
// one that also catches what depguard cannot: a provider reached with net/http
// and a hard-coded hostname, no SDK required.
//
// It scans every non-vendor .go file in the module, tests included: a test
// that calls a provider is a test that sends data to one. This file is the
// only exemption, because the lists below have to be written somewhere.
//
// The lists are deliberately broad. A false positive costs a reviewer a
// minute; a false negative is a provider call nobody governed.

// providerImportPrefixes are module paths of model-provider SDKs and of the
// orchestration libraries that wrap them.
var providerImportPrefixes = []string{
	"github.com/openai/",
	"github.com/sashabaranov/go-openai",
	"github.com/anthropics/",
	"github.com/liushuangls/go-anthropic",
	"google.golang.org/genai",
	"github.com/google/generative-ai-go",
	"cloud.google.com/go/vertexai",
	"cloud.google.com/go/aiplatform",
	"github.com/aws/aws-sdk-go-v2/service/bedrock",
	"github.com/aws/aws-sdk-go/service/bedrock",
	"github.com/aws/aws-sdk-go-v2/service/sagemakerruntime",
	"github.com/Azure/azure-sdk-for-go/sdk/ai/",
	"github.com/Azure/azure-sdk-for-go/sdk/cognitiveservices/",
	"github.com/cohere-ai/",
	"github.com/gage-technologies/mistral-go",
	"github.com/replicate/replicate-go",
	"github.com/ollama/ollama",
	"github.com/tmc/langchaingo",
	"github.com/firebase/genkit",
	"github.com/cloudwego/eino",
}

// providerHosts matches provider API endpoints, including the regional and
// per-resource forms (bedrock-runtime.<region>.amazonaws.com,
// <resource>.openai.azure.com, <region>-aiplatform.googleapis.com).
var providerHosts = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`api\.openai\.com`,
	`[a-z0-9-]+\.openai\.azure\.com`,
	`[a-z0-9-]+\.cognitiveservices\.azure\.com`,
	`[a-z0-9-]+\.services\.ai\.azure\.com`,
	`(models\.)?inference\.ai\.azure\.com`,
	`api\.anthropic\.com`,
	`generativelanguage\.googleapis\.com`,
	`([a-z0-9-]+-)?aiplatform\.googleapis\.com`,
	`bedrock(-runtime|-agent-runtime)?(-fips)?\.[a-z0-9-]+\.amazonaws\.com`,
	`runtime\.sagemaker\.[a-z0-9-]+\.amazonaws\.com`,
	`api\.cohere\.(ai|com)`,
	`api\.mistral\.ai`,
	`api\.groq\.com`,
	`api\.together\.(xyz|ai)`,
	`api\.deepseek\.com`,
	`api\.x\.ai`,
	`api\.perplexity\.ai`,
	`api\.fireworks\.ai`,
	`api-inference\.huggingface\.co`,
	`router\.huggingface\.co`,
	`api\.replicate\.com`,
	`openrouter\.ai`,
}, "|"))

const thisFile = "nodirect_test.go"

func TestNoDirectProviderCall(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found at %s: %v", root, err)
	}

	scanned := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "vendor" || (strings.HasPrefix(name, ".") && path != root) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if filepath.ToSlash(rel) == "internal/adapter/gateway/"+thisFile {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for _, v := range violations(t, rel, src) {
			t.Error(v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 50 {
		t.Fatalf("scanned only %d files; the walk is not seeing the module", scanned)
	}
}

func violations(t *testing.T, name string, src []byte) []string {
	t.Helper()
	var out []string
	f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.ImportsOnly)
	if err != nil {
		// A file that does not parse fails the build elsewhere; the hostname
		// scan below still runs on it.
		t.Logf("%s: %v", name, err)
	} else {
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			for _, prefix := range providerImportPrefixes {
				if strings.HasPrefix(p, prefix) {
					out = append(out, name+": imports model-provider SDK "+p+
						" (ADR-0006 §2.1: AI calls go through internal/adapter/gateway)")
				}
			}
		}
	}
	for _, host := range providerHosts.FindAll(src, -1) {
		out = append(out, name+": names model-provider host "+string(host)+
			" (ADR-0006 §2.1: Go workloads never reach a provider)")
	}
	return out
}

// The detector itself is tested, so a regex or prefix typo cannot make the
// module scan pass vacuously.
func TestNoDirectProviderCallDetects(t *testing.T) {
	cases := map[string]string{
		"sdk import":       "package x\nimport _ \"github.com/openai/openai-go/v2\"\n",
		"bedrock import":   "package x\nimport _ \"github.com/aws/aws-sdk-go-v2/service/bedrockruntime\"\n",
		"azure import":     "package x\nimport _ \"github.com/Azure/azure-sdk-for-go/sdk/ai/azopenai\"\n",
		"genai import":     "package x\nimport _ \"google.golang.org/genai\"\n",
		"anthropic import": "package x\nimport _ \"github.com/anthropics/anthropic-sdk-go\"\n",
		"raw host":         "package x\nconst u = \"https://API.ANTHROPIC.COM/v1/messages\"\n",
		"regional host":    "package x\nconst u = \"bedrock-runtime.eu-west-1.amazonaws.com\"\n",
		"azure resource":   "package x\nconst u = \"https://zoiko.openai.azure.com\"\n",
		"vertex regional":  "package x\nconst u = \"europe-west4-aiplatform.googleapis.com\"\n",
	}
	for name, src := range cases {
		if len(violations(t, name, []byte(src))) == 0 {
			t.Errorf("%s: not detected", name)
		}
	}
	clean := "package x\nimport _ \"github.com/google/uuid\"\nconst u = \"https://ztax-gateway.internal:8443\"\n"
	if v := violations(t, "clean", []byte(clean)); len(v) != 0 {
		t.Errorf("false positive: %v", v)
	}
}
