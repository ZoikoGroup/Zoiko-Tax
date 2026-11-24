package http

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/fiscal"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/obligation"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/fiscaltest"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// What an HTTP caller receives from POST /v1/obligations/{obligationId}/explanation
// for every way the AI plane can answer or fail to. No real Gateway: the fake
// stands in for port.ModelGateway, so these are pure Go tests of the transport
// layer's mapping. Synthetic rows only.

const (
	explainText   = "SYNTHETIC-EXPLANATION-TEXT"
	explainMarker = "SYNTHETIC-ROW-TYPE-MARKER"
)

// explainFake implements port.ModelGateway. Only Extract is exercised.
type explainFake struct {
	fields map[string]string
	err    error
	calls  int
}

func (f *explainFake) Suggest(context.Context, ai.Invocation) (ai.AiSuggestion, error) {
	panic("explainFake: Suggest is not exercised by this test file")
}

func (f *explainFake) ProposeClassification(context.Context, ai.Invocation) (ai.AiClassificationProposal, error) {
	panic("explainFake: ProposeClassification is not exercised by this test file")
}

func (f *explainFake) Extract(_ context.Context, inv ai.Invocation) (ai.AiExtraction, error) {
	f.calls++
	if f.err != nil {
		return ai.AiExtraction{}, f.err
	}
	eid, err := ai.NewAiExtractionID("01920a4d-5a6b-7c8d-9e0f-1a2b3c4d5e6f")
	if err != nil {
		panic(err)
	}
	return ai.NewAiExtraction(eid, inv.SubjectRef, f.fields, ai.Provenance{
		UseCase: inv.UseCase, ModelProfile: "model:explain-test", ProviderProfile: "provider:test",
		PromptProfile: "prompt:explain-obligation-row-v1", Region: testCell, DataClass: "P5",
		RiskTier: inv.RiskTier, AuthorityOutcome: inv.AuthorityOutcome, AiTrainVersion: "ai-test-1", InvocationID: "call-id-1",
	})
}

// explainRows reads under the session's tenant, as the store does.
type explainRows map[id.ObligationID]obligation.Obligation

func (r explainRows) Obligation(ctx context.Context, oid id.ObligationID) (app.ObligationView, error) {
	sc, _ := security.From(ctx)
	o, ok := r[oid]
	if !ok || o.TenantID != sc.Tenant() {
		return app.ObligationView{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "not found")
	}
	return app.ObligationView{Obligation: o, EffectiveStatus: o.Status}, nil
}

func explainObligationRow(t *testing.T, tenant id.TenantID) obligation.Obligation {
	t.Helper()
	amount := fiscaltest.Money(t, "42.00", fiscal.Currency("EUR"))
	return obligation.Obligation{
		ID: id.NewObligationID(uuid.Must(uuid.NewV7())), TenantID: tenant, BusinessKey: "k",
		JurisdictionID: "jurisdiction:xa", Type: explainMarker, Authority: "authority:xa-revenue",
		Duty: obligation.DutyTransactionMonetary, Status: obligation.StatusOpen, Timezone: "UTC",
		Period: obligation.Period{
			Start: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
			Due: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
		},
		Assessed: &amount,
	}
}

func explainSessionTenant() id.TenantID {
	return testClassificationContext(security.RoleOperator).Tenant()
}

func answeredFields(rowID string) map[string]string {
	return map[string]string{
		"status": "ANSWERED", "cited_ids": `["` + rowID + `"]`, "validator_version": "explanation_guard@1", "explanation": explainText,
	}
}

type explainCall struct {
	rt     *Router
	row    obligation.Obligation
	fake   *explainFake
	logbuf *bytes.Buffer
}

func newExplainCall(t *testing.T) *explainCall {
	t.Helper()
	row := explainObligationRow(t, explainSessionTenant())
	fake := &explainFake{fields: answeredFields(row.ID.String())}
	logbuf := &bytes.Buffer{}
	rt := NewRouter(nil, nil, nil, nil, slog.New(slog.NewTextHandler(logbuf, &slog.HandlerOptions{Level: slog.LevelDebug})), idgen.V7{})
	rt.Cell = testCell
	rt.ObligationExplanation = app.NewObligationExplanationService(fake, explainRows{row.ID: row})
	return &explainCall{rt: rt, row: row, fake: fake, logbuf: logbuf}
}

func (c *explainCall) post(path string, roles ...security.Role) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req = req.WithContext(security.Into(req.Context(), testClassificationContext(roles...)))
	rec := httptest.NewRecorder()
	c.rt.Handler().ServeHTTP(rec, req)
	return rec
}

func (c *explainCall) explain(roles ...security.Role) *httptest.ResponseRecorder {
	return c.post("/v1/obligations/"+c.row.ID.String()+"/explanation", roles...)
}

func explainBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestExplainObligationAnsweredReply(t *testing.T) {
	c := newExplainCall(t)
	rec := c.explain(security.RoleAnalyst)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	out := explainBody(t, rec)
	if out["status"] != "ANSWERED" || out["explanation"] != explainText || out["authoritative"] != false ||
		out["obligationId"] != c.row.ID.String() || out["validatorVersion"] != "explanation_guard@1" {
		t.Fatalf("reply = %v", out)
	}
	if ids, _ := out["citedIds"].([]any); len(ids) != 1 || ids[0] != c.row.ID.String() {
		t.Fatalf("citedIds = %v", out["citedIds"])
	}
	prov, _ := out["provenance"].(map[string]any)
	if prov["useCase"] != "explain-obligation-row" || prov["invocationId"] != "call-id-1" {
		t.Fatalf("provenance = %v", prov)
	}
	if _, has := out["reviewReason"]; has {
		t.Fatal("an answered explanation carries a reviewReason")
	}
}

func TestExplainObligationEveryOtherEndingHasNoText(t *testing.T) {
	cases := []struct {
		name       string
		fields     map[string]string
		err        error
		wantStatus string
		wantReason string
		wantProv   bool
	}{
		{"abstained", map[string]string{"status": "ABSTAINED", "explanation": explainText}, nil, "ABSTAINED", "AI_EXPLANATION_ABSTAINED", true},
		{"refused", map[string]string{"status": "REFUSED", "explanation": explainText}, nil, "ABSTAINED", "AI_EXPLANATION_REFUSED", true},
		{"unusable", map[string]string{"status": "SOMETHING", "explanation": explainText}, nil, "REVIEW_REQUIRED", "AI_EXPLANATION_REPLY_UNUSABLE", true},
		{"gateway down", nil, errs.New(errs.CategoryUnavailable, ai.ReasonGatewayUnavailable, "down"),
			"REVIEW_REQUIRED", "AI_GATEWAY_UNAVAILABLE", false},
		{"gateway not configured", nil, errs.New(errs.CategoryUnsupported, ai.ReasonGatewayNotConfigured, "none"),
			"REVIEW_REQUIRED", "AI_GATEWAY_NOT_CONFIGURED", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newExplainCall(t)
			c.fake.fields, c.fake.err = tc.fields, tc.err
			rec := c.explain(security.RoleOperator)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: a degraded AI plane is an answer", rec.Code)
			}
			out := explainBody(t, rec)
			if out["status"] != tc.wantStatus || out["reviewReason"] != tc.wantReason || out["authoritative"] != false {
				t.Fatalf("reply = %v", out)
			}
			if _, has := out["explanation"]; has || strings.Contains(rec.Body.String(), explainText) {
				t.Fatal("text came back with an answer that has none")
			}
			if ids, ok := out["citedIds"].([]any); !ok || len(ids) != 0 {
				t.Fatalf("citedIds = %v, want an empty array", out["citedIds"])
			}
			if _, has := out["provenance"]; has != tc.wantProv {
				t.Fatalf("provenance present = %v, want %v", has, tc.wantProv)
			}
		})
	}
}

func TestExplainObligationErrorMapping(t *testing.T) {
	foreign := explainObligationRow(t, id.NewTenantID(uuid.MustParse("01920a4d-0000-7000-8000-0000000000ee")))
	cases := []struct {
		name       string
		nilService bool
		path       func(c *explainCall) string
		roles      []security.Role
		fakeErr    error
		wantStatus int
		wantReason errs.ReasonCode
		wantCalls  int
	}{
		{"no service wired", true, nil, []security.Role{security.RoleOperator}, nil, http.StatusServiceUnavailable, errs.ReasonUnavailable, 0},
		{"malformed id", false, func(*explainCall) string { return "/v1/obligations/not-an-id/explanation" },
			[]security.Role{security.RoleOperator}, nil, http.StatusBadRequest, errs.ReasonInvalidValue, 0},
		{"unknown id", false, func(*explainCall) string {
			return "/v1/obligations/" + uuid.Must(uuid.NewV7()).String() + "/explanation"
		}, []security.Role{security.RoleOperator}, nil, http.StatusNotFound, errs.ReasonNotFound, 0},
		{"another tenant's obligation", false, func(*explainCall) string { return "/v1/obligations/" + foreign.ID.String() + "/explanation" },
			[]security.Role{security.RoleOperator}, nil, http.StatusNotFound, errs.ReasonNotFound, 0},
		{"an auditor", false, nil, []security.Role{security.RoleAuditor}, nil, http.StatusForbidden, errs.ReasonForbidden, 0},
		{"governance refusal of the data class", false, nil, []security.Role{security.RoleAnalyst},
			errs.New(errs.CategoryPolicy, ai.ReasonDataClassRefused, "refused"), http.StatusForbidden, ai.ReasonDataClassRefused, 1},
		{"kill switch", false, nil, []security.Role{security.RoleOperator},
			errs.New(errs.CategoryPolicy, ai.ReasonKillSwitchEngaged, "refused"), http.StatusForbidden, ai.ReasonKillSwitchEngaged, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newExplainCall(t)
			// A row of another tenant exists in the store; the session cannot see it.
			c.rt.ObligationExplanation = app.NewObligationExplanationService(c.fake,
				explainRows{c.row.ID: c.row, foreign.ID: foreign})
			if tc.nilService {
				c.rt.ObligationExplanation = nil
			}
			c.fake.err = tc.fakeErr
			path := "/v1/obligations/" + c.row.ID.String() + "/explanation"
			if tc.path != nil {
				path = tc.path(c)
			}
			rec := c.post(path, tc.roles...)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			var p Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.ReasonCode != string(tc.wantReason) {
				t.Fatalf("reason = %q (%v), want %q", p.ReasonCode, err, tc.wantReason)
			}
			if c.fake.calls != tc.wantCalls {
				t.Fatalf("the gateway was called %d times, want %d", c.fake.calls, tc.wantCalls)
			}
		})
	}
}

// Neither the explanation, nor a reason text, nor an obligation field is ever
// written to a log line, whatever the ending.
func TestExplainObligationLogsNothingOfTheAnswerOrTheRow(t *testing.T) {
	endings := []map[string]string{
		nil, // the answered reply
		{"status": "ABSTAINED", "explanation": explainText, "reason": explainText, "reason_code": explainText},
		{"status": "nonsense", "explanation": explainText, "reason": explainText},
	}
	for i, fields := range endings {
		c := newExplainCall(t)
		if fields != nil {
			c.fake.fields = fields
		}
		rec := c.explain(security.RoleAnalyst)
		if rec.Code != http.StatusOK {
			t.Fatalf("ending %d: status = %d", i, rec.Code)
		}
		logged := c.logbuf.String()
		for _, secret := range []string{explainText, explainMarker, "42.00", "authority:xa-revenue"} {
			if strings.Contains(logged, secret) {
				t.Fatalf("ending %d: a log line holds %q", i, secret[:min(len(secret), 12)]+"...")
			}
		}
		// The obligation id is in the request path, which the access log line
		// records for every route. No other line may hold it.
		for _, line := range strings.Split(logged, "\n") {
			if strings.Contains(line, c.row.ID.String()) && !strings.Contains(line, "msg=request") {
				t.Fatalf("ending %d: a line other than the access log holds the obligation id", i)
			}
		}
	}
}
