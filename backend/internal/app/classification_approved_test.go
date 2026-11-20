package app_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway"
	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway/gatewaytest"
	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
)

// A small synthetic list, in the format the Python side reads.
const approvedListJSON = `{
  "list_version": "test-list-1",
  "approved": [
    {"code": "ontology:telecom/voice/mobile", "effective_from": "2024-01-01"},
    {"code": "ontology:telecom/data/roaming", "effective_from": "2020-01-01", "effective_to": "2023-12-31"},
    {"code": "ontology:telecom/esim/digital", "effective_from": "2030-01-01"},
    {"code": "ontology:telecom/edge/window", "effective_from": "2025-03-01", "effective_to": "2025-03-31"}
  ]
}`

var approvedNow = clock.Fixed{Instant: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)}

func approvedList(t *testing.T) *app.ApprovedList {
	t.Helper()
	l, err := app.ParseApprovedList([]byte(approvedListJSON))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func proposeWith(t *testing.T, code string, clk clock.Clock) app.ProposeOutcome {
	t.Helper()
	svc := app.NewClassificationService(&fakeModels{code: code}).WithApprovedCodes(approvedList(t), clk)
	o, err := svc.Propose(as(security.RoleOperator), app.ProposeInput{SubjectRef: "sku:PLAN-UNL-5G", Description: "Unlimited 5G plan"})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	return o
}

func wantNotApproved(t *testing.T, o app.ProposeOutcome) {
	t.Helper()
	if o.Disposition != app.DispositionReviewRequired || o.ReviewReason != ai.ReasonProposalCodeNotApproved {
		t.Fatalf("outcome = %+v, want review required with the not-approved reason", o)
	}
	if o.Proposal.ProposedCode() != "" {
		t.Fatalf("a not-approved code is still in the outcome: %q", o.Proposal.ProposedCode())
	}
}

func TestAnApprovedAndEffectiveCodeIsProposed(t *testing.T) {
	o := proposeWith(t, "ontology:telecom/voice/mobile", approvedNow)
	if o.Disposition != app.DispositionProposed || o.Proposal.ProposedCode() != "ontology:telecom/voice/mobile" {
		t.Fatalf("outcome = %+v, want a proposal", o)
	}
}

func TestAnUnlistedCodeIsSetAsideForAPerson(t *testing.T) {
	wantNotApproved(t, proposeWith(t, "ontology:telecom/made-up/node", approvedNow))
}

func TestAnExpiredCodeIsSetAsideForAPerson(t *testing.T) {
	wantNotApproved(t, proposeWith(t, "ontology:telecom/data/roaming", approvedNow))
}

func TestANotYetEffectiveCodeIsSetAsideForAPerson(t *testing.T) {
	wantNotApproved(t, proposeWith(t, "ontology:telecom/esim/digital", approvedNow))
}

func TestTheEffectiveWindowIsInclusiveOnBothEnds(t *testing.T) {
	code := "ontology:telecom/edge/window"
	day := func(s string) clock.Fixed {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return clock.Fixed{Instant: d.Add(23 * time.Hour)}
	}
	for _, in := range []string{"2025-03-01", "2025-03-15", "2025-03-31"} {
		if o := proposeWith(t, code, day(in)); o.Disposition != app.DispositionProposed {
			t.Errorf("%s: %+v, want a proposal", in, o)
		}
	}
	for _, out := range []string{"2025-02-28", "2025-04-01"} {
		wantNotApproved(t, proposeWith(t, code, day(out)))
	}
}

func TestTheMatchIsExactAndCaseSensitive(t *testing.T) {
	l := approvedList(t)
	for _, code := range []string{"Ontology:telecom/voice/mobile", "ontology:telecom/voice/mobile ", " ontology:telecom/voice/mobile", "ontology:telecom/voice/MOBILE"} {
		if l.Approved(code, approvedNow.Now()) {
			t.Errorf("%q was approved", code)
		}
	}
}

func TestWithNoCheckerAnUnlistedCodeIsProposedAsToday(t *testing.T) {
	svc := app.NewClassificationService(&fakeModels{code: "ontology:telecom/made-up/node"})
	o, err := svc.Propose(as(security.RoleOperator), app.ProposeInput{SubjectRef: "sku:PLAN-UNL-5G", Description: "Unlimited 5G plan"})
	if err != nil || o.Disposition != app.DispositionProposed {
		t.Fatalf("outcome = %+v, %v; with no list the check is off", o, err)
	}
}

// Twenty unlisted answers are twenty successful Gateway calls: the breaker
// (threshold 5) never opens, so the transport is reached every time.
func TestUnlistedCodesNeverOpenTheBreaker(t *testing.T) {
	reply := gateway.Reply{
		ModelProfile: "model:m", ProviderProfile: "provider:p", PromptProfile: "prompt:q", AiTrainVersion: "ai-1",
		ProposedCode: "ontology:telecom/made-up/node", Confidence: "0.9",
	}
	transport := &gatewaytest.Fake{Reply: reply}
	client := downGateway(t, transport, gateway.NewBreaker(5, time.Minute))
	svc := app.NewClassificationService(client).WithApprovedCodes(approvedList(t), approvedNow)
	for i := range 20 {
		o, err := svc.Propose(bounded(t), independenceInput)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		wantNotApproved(t, o)
	}
	if n := len(transport.Calls()); n != 20 {
		t.Fatalf("transport calls = %d, want 20 (a call refused by an open breaker never reaches it)", n)
	}
}

func TestABadApprovedListIsAnErrorWithAFixedMessage(t *testing.T) {
	secret := "ontology:telecom/secret/value"
	bad := map[string]string{
		"not json":          `{nope`,
		"unknown key":       `{"list_version":"v","approved":[{"code":"` + secret + `","effective_from":"2024-01-01"}],"extra":1}`,
		"unknown entry key": `{"list_version":"v","approved":[{"code":"` + secret + `","effective_from":"2024-01-01","x":1}]}`,
		"no version":        `{"approved":[{"code":"` + secret + `","effective_from":"2024-01-01"}]}`,
		"empty list":        `{"list_version":"v","approved":[]}`,
		"bad code":          `{"list_version":"v","approved":[{"code":"` + secret + ` x","effective_from":"2024-01-01"}]}`,
		"bad date":          `{"list_version":"v","approved":[{"code":"` + secret + `","effective_from":"2024-13-01"}]}`,
		"to before from":    `{"list_version":"v","approved":[{"code":"` + secret + `","effective_from":"2024-02-01","effective_to":"2024-01-01"}]}`,
		"duplicate":         `{"list_version":"v","approved":[{"code":"` + secret + `","effective_from":"2024-01-01"},{"code":"` + secret + `","effective_from":"2025-01-01"}]}`,
		"trailing data":     approvedListJSON + `{}`,
	}
	for name, body := range bad {
		_, err := app.ParseApprovedList([]byte(body))
		if !errors.Is(err, app.ErrApprovedListInvalid) || strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: err = %v, want the fixed message", name, err)
		}
	}
	// An unreadable file is the same error, and a good file loads.
	if _, err := app.LoadApprovedList(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, app.ErrApprovedListInvalid) {
		t.Errorf("missing file: %v", err)
	}
	path := filepath.Join(t.TempDir(), "list.json")
	if err := os.WriteFile(path, []byte(approvedListJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.LoadApprovedList(path); err != nil {
		t.Errorf("good file: %v", err)
	}
}
