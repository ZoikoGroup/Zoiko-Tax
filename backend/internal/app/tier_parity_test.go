package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
)

// Tier parity between the Go caller and the AI registry (Decision D-04).
//
// This file lives in package app because the tiers the Go core sends are
// literals inside ClassificationService.Propose (classification.go), not
// exported constants. The only way to read them without changing production
// code is to run the real service against a fake gateway and inspect the
// Invocation it sends, which is what tierRecorder does.
//
// The registries are read as plain JSON from intelligence/config, never
// through gateway.LoadPolicy, so this test cannot change how they load.
//
// D-04 (not yet signed): classification A1/T2 and explanations A0/T1.
// The ratchet pins today's values; the target test states the D-04 values and
// is skipped. Un-skip it, then update the ratchet, in one change (see
// d04Signed below).

// d04Signed flips the target test on. Set it to true in the same change that
// aligns the Go caller (classification.go) and the registries, and replace the
// expectations in TestTierRatchet with the D-04 values.
const d04Signed = false

const d04SkipReason = "D-04 not yet signed: classification A1/T2, explanations A0/T1"

const ratchetMessage = "D-04 tier alignment: update both sides together and flip the target test"

const (
	registryDev  = "registry.dev.json"
	registryLive = "registry.live-demo.json"
)

const classificationUC = "classification-review"

var explainUCs = []string{"explain-report-figure", "explain-obligation-row", "explain-shadow-variance"}

// tierRecorder is a port.ModelGateway that records what the service sends.
type tierRecorder struct{ seen []ai.Invocation }

func (r *tierRecorder) Suggest(context.Context, ai.Invocation) (ai.AiSuggestion, error) {
	return ai.AiSuggestion{}, nil
}

func (r *tierRecorder) Extract(context.Context, ai.Invocation) (ai.AiExtraction, error) {
	return ai.AiExtraction{}, nil
}

func (r *tierRecorder) ProposeClassification(_ context.Context, inv ai.Invocation) (ai.AiClassificationProposal, error) {
	r.seen = append(r.seen, inv)
	pid, _ := ai.NewAiClassificationProposalID("01920a4d-5a6b-7c8d-9e0f-1a2b3c4d5e6f")
	return ai.NewAiClassificationProposal(pid, inv.SubjectRef, "ontology:telecom/voice/mobile", "0.5", ai.Provenance{
		UseCase: inv.UseCase, ModelProfile: "model:m", ProviderProfile: "provider:p", PromptProfile: "prompt:q",
		Region: "local", DataClass: "P0", RiskTier: inv.RiskTier, AuthorityOutcome: inv.AuthorityOutcome, AiTrainVersion: "ai-1",
	})
}

// sentByGo is the invocation the Go core sends for classification today.
func sentByGo(t *testing.T) ai.Invocation {
	t.Helper()
	rec := &tierRecorder{}
	if _, err := app.NewClassificationService(rec).Propose(as(security.RoleAnalyst),
		app.ProposeInput{SubjectRef: "sku:TIER-PARITY", Description: "tier parity probe"}); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(rec.seen) != 1 {
		t.Fatalf("expected exactly one gateway call, got %d", len(rec.seen))
	}
	return rec.seen[0]
}

type registryEntry struct {
	UseCaseID    string   `json:"use_case_id"`
	MaxRiskTier  string   `json:"max_risk_tier"`
	MaxAuthority string   `json:"max_authority"`
	DataClasses  []string `json:"permitted_data_classes"`
}

// repoRoot walks up from the test's directory to the repository root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		_, e1 := os.Stat(filepath.Join(dir, "backend", "go.mod"))
		_, e2 := os.Stat(filepath.Join(dir, "intelligence", "config"))
		if e1 == nil && e2 == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root (backend/go.mod beside intelligence/config) not found above the test directory")
		}
		dir = parent
	}
}

// readRegistry reads one registry file as plain JSON, keyed by use case id.
func readRegistry(root, name string) (map[string]registryEntry, error) {
	raw, err := os.ReadFile(filepath.Join(root, "intelligence", "config", name))
	if err != nil {
		return nil, fmt.Errorf("registry file %s: %w", name, err)
	}
	var doc struct {
		UseCases []registryEntry `json:"use_cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("registry file %s is not valid JSON: %w", name, err)
	}
	out := make(map[string]registryEntry, len(doc.UseCases))
	for _, u := range doc.UseCases {
		out[u.UseCaseID] = u
	}
	return out, nil
}

// lookup returns a use case, or an error naming the file and the use case.
func lookup(reg map[string]registryEntry, file, useCase string) (registryEntry, error) {
	e, ok := reg[useCase]
	if !ok {
		return registryEntry{}, fmt.Errorf("use case %q is not in %s", useCase, file)
	}
	return e, nil
}

// loadRegistry fails the test on a missing or unparseable file; it never skips.
func loadRegistry(t *testing.T, name string) map[string]registryEntry {
	t.Helper()
	reg, err := readRegistry(repoRoot(t), name)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// entry fails the test on an unknown use case; it never skips.
func entry(t *testing.T, reg map[string]registryEntry, file, useCase string) registryEntry {
	t.Helper()
	e, err := lookup(reg, file, useCase)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// rank turns "T2" or "A1" into 2 or 1 so tiers can be compared. Anything else fails.
func rank(t *testing.T, label, want string) int {
	t.Helper()
	if len(label) != 2 || label[0] != want[0] || label[1] < '0' || label[1] > '5' {
		t.Fatalf("%q is not a valid %s label", label, want)
	}
	n, err := strconv.Atoi(label[1:])
	if err != nil {
		t.Fatalf("%q is not a valid %s label: %v", label, want, err)
	}
	return n
}

// (a) RATCHET: today's values, exactly.
func TestTierRatchet(t *testing.T) {
	sent := sentByGo(t)
	if string(sent.UseCase) != classificationUC {
		t.Fatalf("Go sends use case %q, test expects %q", sent.UseCase, classificationUC)
	}
	if sent.AuthorityOutcome != "A1" || sent.RiskTier != "T1" {
		t.Errorf("Go classification sends %s/%s, want A1/T1. %s", sent.AuthorityOutcome, sent.RiskTier, ratchetMessage)
	}

	live := loadRegistry(t, registryLive)
	dev := loadRegistry(t, registryDev)
	for file, reg := range map[string]map[string]registryEntry{registryDev: dev, registryLive: live} {
		c := entry(t, reg, file, classificationUC)
		if c.MaxAuthority != "A1" || c.MaxRiskTier != "T2" {
			t.Errorf("%s %s is %s/%s, want A1/T2. %s", file, classificationUC, c.MaxAuthority, c.MaxRiskTier, ratchetMessage)
		}
	}
	for _, uc := range explainUCs {
		e := entry(t, live, registryLive, uc)
		if e.MaxAuthority != "A1" || e.MaxRiskTier != "T1" {
			t.Errorf("%s %s is %s/%s, want A1/T1. %s", registryLive, uc, e.MaxAuthority, e.MaxRiskTier, ratchetMessage)
		}
	}
}

// (b) CEILING: Go never sends more than the registry allows, in either file.
func TestGoNeverSendsAboveTheRegistryCeiling(t *testing.T) {
	sent := sentByGo(t)
	sentRisk := rank(t, string(sent.RiskTier), "T")
	sentAuth := rank(t, string(sent.AuthorityOutcome), "A")
	for file, reg := range map[string]map[string]registryEntry{
		registryDev:  loadRegistry(t, registryDev),
		registryLive: loadRegistry(t, registryLive),
	} {
		c := entry(t, reg, file, string(sent.UseCase))
		if max := rank(t, c.MaxRiskTier, "T"); sentRisk > max {
			t.Errorf("%s: Go sends risk %s above the registry ceiling %s", file, sent.RiskTier, c.MaxRiskTier)
		}
		if max := rank(t, c.MaxAuthority, "A"); sentAuth > max {
			t.Errorf("%s: Go sends authority %s above the registry maximum %s", file, sent.AuthorityOutcome, c.MaxAuthority)
		}
		allowed := map[string]bool{}
		for _, d := range c.DataClasses {
			allowed[d] = true
		}
		for _, d := range sent.DataClasses {
			if !allowed[string(d)] {
				t.Errorf("%s: Go sends data class %s, which %s does not permit", file, d, c.UseCaseID)
			}
		}
	}
}

// (c) TARGET: the D-04 values. Skipped until D-04 is signed.
func TestTierTargetD04(t *testing.T) {
	if !d04Signed {
		t.Skip(d04SkipReason)
	}
	sent := sentByGo(t)
	if sent.AuthorityOutcome != "A1" || sent.RiskTier != "T2" {
		t.Errorf("Go classification sends %s/%s, D-04 wants A1/T2", sent.AuthorityOutcome, sent.RiskTier)
	}
	live := loadRegistry(t, registryLive)
	for _, uc := range explainUCs {
		e := entry(t, live, registryLive, uc)
		if e.MaxAuthority != "A0" || e.MaxRiskTier != "T1" {
			t.Errorf("%s is %s/%s, D-04 wants A0/T1", uc, e.MaxAuthority, e.MaxRiskTier)
		}
	}
}

// (d) BOTH REGISTRY FILES agree on what they share.
//
// registry.dev.json carries only classification-review; the three explanation
// use cases exist only in registry.live-demo.json (a test in intelligence/
// keeps them out of the dev file). The live demo deliberately narrows the data
// classes to P0, so for data classes the rule is "never wider than dev", not
// equality.
func TestRegistryFilesAgreeOnTiersAndDoNotWidenDataClasses(t *testing.T) {
	dev := loadRegistry(t, registryDev)
	live := loadRegistry(t, registryLive)

	d := entry(t, dev, registryDev, classificationUC)
	l := entry(t, live, registryLive, classificationUC)
	if d.MaxAuthority != l.MaxAuthority {
		t.Errorf("%s authority differs: dev %s, live-demo %s", classificationUC, d.MaxAuthority, l.MaxAuthority)
	}
	if d.MaxRiskTier != l.MaxRiskTier {
		t.Errorf("%s risk differs: dev %s, live-demo %s", classificationUC, d.MaxRiskTier, l.MaxRiskTier)
	}
	devClasses := map[string]bool{}
	for _, c := range d.DataClasses {
		devClasses[c] = true
	}
	for _, c := range l.DataClasses {
		if !devClasses[c] {
			t.Errorf("live-demo permits data class %s for %s but registry.dev.json does not", c, classificationUC)
		}
	}

	// The explanation use cases must be present in the live-demo file with
	// well-formed tiers and no class above P1 (nothing about a customer).
	for _, uc := range explainUCs {
		e := entry(t, live, registryLive, uc)
		rank(t, e.MaxRiskTier, "T")
		rank(t, e.MaxAuthority, "A")
		for _, c := range e.DataClasses {
			if c != "P0" && c != "P1" {
				t.Errorf("%s permits data class %s", uc, c)
			}
		}
	}
}

// (e) An unknown use case or a missing file is an error, which the helpers
// above turn into a failed test (t.Fatal), never a skip.
func TestUnknownUseCaseAndMissingFileAreErrors(t *testing.T) {
	root := repoRoot(t)
	if _, err := readRegistry(root, "no-such-registry.json"); err == nil {
		t.Error("a missing registry file was not reported as an error")
	}
	reg, err := readRegistry(root, registryDev)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lookup(reg, registryDev, "no-such-use-case"); err == nil {
		t.Error("an unknown use case was not reported as an error")
	}
	if _, err := lookup(reg, registryDev, classificationUC); err != nil {
		t.Errorf("a known use case was reported as an error: %v", err)
	}
}
