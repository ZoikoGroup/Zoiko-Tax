package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway"
	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway/gatewaytest"
	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// What an HTTP caller actually receives from POST /v1/classifications:propose
// for every way the AI plane can answer or fail to. No Python and no real
// Gateway: fakeModelGateway stands in for port.ModelGateway, so these are pure
// Go tests of the transport layer's own error-to-Problem mapping.

// fakeModelGateway implements port.ModelGateway. Only ProposeClassification
// is exercised by this file; Suggest and Extract exist to satisfy the
// interface and are never called by /v1/classifications:propose.
type fakeModelGateway struct {
	proposal ai.AiClassificationProposal
	err      error

	calls int
	seen  ai.Invocation
}

func (f *fakeModelGateway) Suggest(context.Context, ai.Invocation) (ai.AiSuggestion, error) {
	panic("fakeModelGateway: Suggest is not exercised by this test file")
}

func (f *fakeModelGateway) Extract(context.Context, ai.Invocation) (ai.AiExtraction, error) {
	panic("fakeModelGateway: Extract is not exercised by this test file")
}

func (f *fakeModelGateway) ProposeClassification(_ context.Context, inv ai.Invocation) (ai.AiClassificationProposal, error) {
	f.calls++
	f.seen = inv
	return f.proposal, f.err
}

// testCell and testClassificationContext give every case the same resident,
// authenticated identity, so the only thing that varies between cases is
// what the fake gateway does (or isn't reached at all). rt.Cell and the
// context's home cell are both set to the same non-empty value deliberately,
// to put residency's exact empty-vs-empty comparison out of scope for this
// file -- it is not what these tests are about.
const testCell = "eu-west-1"

func testClassificationContext(roles ...security.Role) security.Context {
	return security.New(
		id.NewTenantID(uuid.MustParse("01920a4d-0000-7000-8000-000000000001")),
		id.NewUserID(uuid.MustParse("01920a4d-0000-7000-8000-000000000002")),
		id.NewSessionID(uuid.MustParse("01920a4d-0000-7000-8000-000000000003")),
		roles, time.Unix(0, 0),
	).WithHomeCell(testCell)
}

// testClassificationProposal is a valid advisory record a fake gateway can
// return for the success case, built through the same constructors
// production uses (ai.NewAiClassificationProposal, ai.Provenance), not a
// struct literal that could drift from what the constructor actually
// requires.
func testClassificationProposal(t *testing.T) ai.AiClassificationProposal {
	t.Helper()
	id, err := ai.NewAiClassificationProposalID("01920a4d-5a6b-7c8d-9e0f-1a2b3c4d5e6f")
	if err != nil {
		t.Fatalf("NewAiClassificationProposalID: %v", err)
	}
	prov := ai.Provenance{
		UseCase:          app.ClassificationReviewUseCase,
		ModelProfile:     "model:cls-test",
		ProviderProfile:  "provider:test",
		PromptProfile:    "prompt:classification-review-v1",
		Region:           testCell,
		DataClass:        "P0",
		RiskTier:         ai.RiskTierT1,
		AuthorityOutcome: ai.AuthorityOutcomeA1,
		AiTrainVersion:   "ai-test-1",
	}
	p, err := ai.NewAiClassificationProposal(id, "sku:PLAN-UNL-5G", "ontology:telecom/voice/mobile", "0.97", prov)
	if err != nil {
		t.Fatalf("NewAiClassificationProposal: %v", err)
	}
	return p
}

func TestProposeClassificationErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		// nilService leaves rt.Classification unset, exercising the
		// "not configured for this cell" path before the fake is ever
		// reached (ledger.go:93-96).
		nilService bool
		roles      []security.Role
		// emptyDescription exercises app.classification.go's own input
		// validation, which also runs before the fake is reached.
		emptyDescription bool
		fakeErr          error
		fakeProposal     ai.AiClassificationProposal

		wantStatus     int
		wantReason     errs.ReasonCode
		wantRetryable  bool
		wantRetryAfter string // "" means the header must be absent
		wantCalls      int
	}{
		{
			name:       "classification_service_nil",
			nilService: true,
			roles:      []security.Role{security.RoleOperator},
			wantStatus: http.StatusServiceUnavailable,
			// The generic "not configured for this cell" reason
			// (ledger.go:94), deliberately NOT AI_GATEWAY_NOT_CONFIGURED:
			// this cell has no Classification service wired at all, which
			// is a different fact from "this cell's Gateway has no
			// approved provider".
			wantReason: errs.ReasonUnavailable,
			// CategoryUnavailable is retryable by category (errs.go:80);
			// true here is correct, not a leftover default. Retry-After is
			// keyed on the 503 status itself (problem.go:134-137), not on
			// which specific reason produced it.
			wantRetryable:  true,
			wantRetryAfter: "2",
			wantCalls:      0,
		},
		{
			name:       "wrong_role",
			roles:      []security.Role{security.RoleAuditor},
			wantStatus: http.StatusForbidden,
			wantReason: errs.ReasonForbidden,
			wantCalls:  0,
		},
		{
			name:             "empty_description",
			roles:            []security.Role{security.RoleOperator},
			emptyDescription: true,
			wantStatus:       http.StatusBadRequest,
			wantReason:       errs.ReasonMissingField,
			wantCalls:        0,
		},
		{
			name:  "gateway_not_configured",
			roles: []security.Role{security.RoleOperator},
			fakeErr: errs.New(errs.CategoryUnsupported, gateway.ReasonGatewayNotConfigured,
				"AI assistance is not available in this region yet."),
			wantStatus:    http.StatusUnprocessableEntity,
			wantReason:    gateway.ReasonGatewayNotConfigured,
			wantRetryable: false,
			wantCalls:     1,
		},
		{
			name:  "policy_refusal",
			roles: []security.Role{security.RoleAnalyst},
			fakeErr: errs.New(errs.CategoryPolicy, ai.ReasonResidencyRefused,
				"The AI use case is not approved to process data in this region."),
			wantStatus: http.StatusForbidden,
			wantReason: ai.ReasonResidencyRefused,
			wantCalls:  1,
		},
		{
			// The per-use-case (or global) kill switch, governance.go:94-95.
			// CategoryPolicy, so 403 like policy_refusal above -- this case
			// exists to pin the EXACT reason code a caller sees for a killed
			// use case, not just that some policy reason maps to 403.
			name:  "kill_switch_use_case",
			roles: []security.Role{security.RoleOperator},
			fakeErr: errs.New(errs.CategoryPolicy, ai.ReasonKillSwitchEngaged,
				"The AI use case is not available right now."),
			wantStatus: http.StatusForbidden,
			wantReason: ai.ReasonKillSwitchEngaged,
			wantCalls:  1,
		},
		{
			// A registered use case taken out of service, governance.go:104-105.
			name:  "use_case_suspended",
			roles: []security.Role{security.RoleOperator},
			fakeErr: errs.New(errs.CategoryPolicy, ai.ReasonUseCaseSuspended,
				"The AI use case is not available right now."),
			wantStatus: http.StatusForbidden,
			wantReason: ai.ReasonUseCaseSuspended,
			wantCalls:  1,
		},
		{
			// 500 is by design here, not a bug to fix: AI_MALFORMED_CONTEXT
			// means the Go side itself built or forwarded a request the AI
			// plane could not read, which is a defect in ZoikoTax, not in
			// the caller's input (governance.go:90-92 registers it under
			// errs.CategoryInternal for exactly this reason). statusFor has
			// no case for CategoryInternal, so it falls through to the
			// default, StatusInternalServerError.
			name:  "malformed_context",
			roles: []security.Role{security.RoleOperator},
			fakeErr: errs.New(errs.CategoryInternal, ai.ReasonMalformedContext,
				"The AI Gateway could not read the request this cell sent."),
			wantStatus: http.StatusInternalServerError,
			wantReason: ai.ReasonMalformedContext,
			wantCalls:  1,
		},
		{
			name:  "gateway_unavailable",
			roles: []security.Role{security.RoleOperator},
			fakeErr: errs.New(errs.CategoryUnavailable, gateway.ReasonGatewayUnavailable,
				"AI assistance is temporarily unavailable."),
			wantStatus:     http.StatusServiceUnavailable,
			wantReason:     gateway.ReasonGatewayUnavailable,
			wantRetryable:  true, // CategoryUnavailable is retryable by category (errs.go:80)
			wantRetryAfter: "2",
			wantCalls:      1,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeModelGateway{proposal: c.fakeProposal, err: c.fakeErr}

			rt := NewRouter(nil, nil, nil, nil, slog.New(slog.DiscardHandler), idgen.V7{})
			rt.Cell = testCell
			if !c.nilService {
				rt.Classification = app.NewClassificationService(fake)
			}
			h := rt.Handler()

			description := "Unlimited 5G mobile plan with 20 GB hotspot"
			if c.emptyDescription {
				description = ""
			}
			body := `{"subjectRef":"sku:PLAN-UNL-5G","description":"` + description + `"}`
			req := httptest.NewRequest(http.MethodPost, "/v1/classifications:propose", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(security.Into(req.Context(), testClassificationContext(c.roles...)))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, c.wantStatus, rec.Body.String())
			}
			if fake.calls != c.wantCalls {
				t.Fatalf("fake gateway called %d times, want %d", fake.calls, c.wantCalls)
			}

			var p Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatalf("decode problem body: %v; body=%s", err, rec.Body.String())
			}
			if p.ReasonCode != string(c.wantReason) {
				t.Fatalf("ztx_reason_code = %q, want %q", p.ReasonCode, c.wantReason)
			}
			if p.Retryable != c.wantRetryable {
				t.Fatalf("ztx_retryable = %v, want %v", p.Retryable, c.wantRetryable)
			}
			if got := rec.Header().Get("Retry-After"); got != c.wantRetryAfter {
				t.Fatalf("Retry-After = %q, want %q", got, c.wantRetryAfter)
			}

			if c.name == "malformed_context" {
				const wantDetail = "The AI Gateway could not read the request this cell sent."
				if p.Detail != wantDetail {
					t.Fatalf("detail = %q, want exactly %q", p.Detail, wantDetail)
				}
			}

			if c.name == "malformed_context" || c.name == "kill_switch_use_case" || c.name == "use_case_suspended" {
				// Neither the subject reference nor the description this
				// request carried may appear in the response: a policy or
				// defect report must not echo back the caller's content.
				for _, leaked := range []string{"PLAN-UNL-5G", "5G mobile plan", "20 GB hotspot"} {
					if strings.Contains(rec.Body.String(), leaked) {
						t.Fatalf("response leaks request content %q: %s", leaked, rec.Body.String())
					}
				}
			}

			if c.wantCalls == 1 {
				if fake.seen.UseCase != app.ClassificationReviewUseCase {
					t.Fatalf("invocation use case = %q, want %q", fake.seen.UseCase, app.ClassificationReviewUseCase)
				}
				if fake.seen.AuthorityOutcome != ai.AuthorityOutcomeA1 {
					t.Fatalf("invocation authority = %q, want %q", fake.seen.AuthorityOutcome, ai.AuthorityOutcomeA1)
				}
				if fake.seen.RiskTier != ai.RiskTierT1 {
					t.Fatalf("invocation risk tier = %q, want %q", fake.seen.RiskTier, ai.RiskTierT1)
				}
				if len(fake.seen.DataClasses) != 1 || fake.seen.DataClasses[0] != privacy.P0 {
					t.Fatalf("invocation data classes = %v, want [%s]", fake.seen.DataClasses, privacy.P0)
				}
			}
		})
	}
}

func TestProposeClassificationSuccess(t *testing.T) {
	proposal := testClassificationProposal(t)
	fake := &fakeModelGateway{proposal: proposal}

	rt := NewRouter(nil, nil, nil, nil, slog.New(slog.DiscardHandler), idgen.V7{})
	rt.Cell = testCell
	rt.Classification = app.NewClassificationService(fake)
	h := rt.Handler()

	body := `{"subjectRef":"sku:PLAN-UNL-5G","description":"Unlimited 5G mobile plan with 20 GB hotspot"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/classifications:propose", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(security.Into(req.Context(), testClassificationContext(security.RoleOperator)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if fake.calls != 1 {
		t.Fatalf("fake gateway called %d times, want 1", fake.calls)
	}

	var out struct {
		Authoritative bool   `json:"authoritative"`
		ProposedCode  string `json:"proposedCode"`
		SubjectRef    string `json:"subjectRef"`
		Confidence    string `json:"confidence"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode proposal: %v; body=%s", err, rec.Body.String())
	}
	if out.Authoritative {
		t.Fatal("authoritative = true; an AI proposal can never be authoritative (ADR-0006 §2.6)")
	}
	if out.ProposedCode != proposal.ProposedCode() || out.SubjectRef != proposal.SubjectRef() ||
		out.Confidence != proposal.Confidence() {
		t.Fatalf("proposal fields = %+v, want to match %+v", out, proposal)
	}

	if fake.seen.UseCase != app.ClassificationReviewUseCase || fake.seen.AuthorityOutcome != ai.AuthorityOutcomeA1 ||
		fake.seen.RiskTier != ai.RiskTierT1 || len(fake.seen.DataClasses) != 1 || fake.seen.DataClasses[0] != privacy.P0 {
		t.Fatalf("invocation = %+v", fake.seen)
	}
}

// fixedPolicy is a gateway.PolicySource that never changes. Unlike
// gateway_test.go's switchablePolicy, this test does not need to flip the
// switch mid-flight -- the use case is already killed before the first call.
type fixedPolicy struct{ p ai.Policy }

func (f fixedPolicy) Policy() ai.Policy { return f.p }

// TestKilledUseCaseNeverReachesTransport wires a REAL gateway.Client -- not
// fakeModelGateway -- in front of the same router the table test above uses.
// This proves the kill switch stops the call inside gateway.Client itself,
// before any transport call, not merely that *some* fake error happens to
// map to 403 at this layer.
func TestKilledUseCaseNeverReachesTransport(t *testing.T) {
	killedPolicy, err := ai.NewPolicy([]ai.UseCasePolicy{{
		UseCase:              app.ClassificationReviewUseCase,
		Owner:                "lane-l",
		MaxAuthority:         ai.AuthorityOutcomeA1,
		MaxRiskTier:          ai.RiskTierT1,
		PermittedRegions:     []string{testCell},
		PermittedDataClasses: []privacy.Class{privacy.P0},
	}}, []ai.AiUseCase{app.ClassificationReviewUseCase}, false)
	if err != nil {
		t.Fatal(err)
	}

	transport := &gatewaytest.Fake{}
	client, err := gateway.New(gateway.Config{
		Transport: transport,
		Policy:    fixedPolicy{p: killedPolicy},
		Recorder:  &gatewaytest.Recorder{},
		Clock:     clock.System{},
		IDs:       idgen.V7{},
		Region:    testCell,
		Deadline:  time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	rt := NewRouter(nil, nil, nil, nil, slog.New(slog.DiscardHandler), idgen.V7{})
	rt.Cell = testCell
	rt.Classification = app.NewClassificationService(client)
	h := rt.Handler()

	body := `{"subjectRef":"sku:PLAN-UNL-5G","description":"Unlimited 5G mobile plan with 20 GB hotspot"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/classifications:propose", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(security.Into(req.Context(), testClassificationContext(security.RoleOperator)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem body: %v; body=%s", err, rec.Body.String())
	}
	if p.ReasonCode != string(ai.ReasonKillSwitchEngaged) {
		t.Fatalf("ztx_reason_code = %q, want %q", p.ReasonCode, ai.ReasonKillSwitchEngaged)
	}
	if len(transport.Calls()) != 0 {
		t.Fatalf("a killed use case reached the transport: %d call(s)", len(transport.Calls()))
	}
}
