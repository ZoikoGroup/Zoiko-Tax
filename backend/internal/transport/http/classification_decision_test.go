package http

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/classification"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// decisionStore is an in-memory store scoped by the session's tenant. It does
// not enforce uniqueness itself; the service does, and the database key backs it.
type decisionStore struct {
	mu   sync.Mutex
	rows map[string]classification.ReviewDecision
}

func tenantKey(ctx context.Context, p uuid.UUID) string {
	sc, _ := security.From(ctx)
	return sc.Tenant().String() + "/" + p.String()
}

func (s *decisionStore) Lock(context.Context, uuid.UUID) error { return nil }

func (s *decisionStore) Create(ctx context.Context, d classification.ReviewDecision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[tenantKey(ctx, d.ProposalID)] = d
	return nil
}

func (s *decisionStore) Decision(ctx context.Context, p uuid.UUID) (classification.ReviewDecision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.rows[tenantKey(ctx, p)]; ok {
		return d, nil
	}
	return classification.ReviewDecision{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
}

type quietAudit struct{}

func (quietAudit) Append(context.Context, port.AuditRecord) error { return nil }
func (quietAudit) List(context.Context, int) ([]port.AuditRecord, error) {
	return nil, nil
}
func (quietAudit) ListForSubject(context.Context, string, string, int) ([]port.AuditRecord, error) {
	return nil, nil
}

type noTxManager struct{}

func (noTxManager) Begin(ctx context.Context) (port.Tx, context.Context, error) {
	return noTxManager{}, ctx, nil
}
func (noTxManager) Commit(context.Context) error   { return nil }
func (noTxManager) Rollback(context.Context) error { return nil }

const (
	markerReason = "marker-reason-text"
	markerNote   = "marker-note-text"
)

type decisionRig struct {
	h    http.Handler
	logs *bytes.Buffer
}

func newDecisionRig(t *testing.T, wired bool) *decisionRig {
	t.Helper()
	logs := &bytes.Buffer{}
	rt := NewRouter(nil, nil, nil, nil, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), idgen.V7{})
	rt.Cell = testCell
	if wired {
		rt.ClassificationDecisions = app.NewClassificationDecisionService(&decisionStore{rows: map[string]classification.ReviewDecision{}},
			quietAudit{}, noTxManager{}, clock.Fixed{Instant: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}, &idgen.Sequential{})
	}
	return &decisionRig{h: rt.Handler(), logs: logs}
}

func otherTenantContext(roles ...security.Role) security.Context {
	return security.New(
		id.NewTenantID(uuid.MustParse("01920a4d-0000-7000-8000-0000000000ff")),
		id.NewUserID(uuid.MustParse("01920a4d-0000-7000-8000-0000000000fe")),
		id.NewSessionID(uuid.MustParse("01920a4d-0000-7000-8000-0000000000fd")),
		roles, time.Unix(0, 0),
	).WithHomeCell(testCell)
}

func (r *decisionRig) do(method, path, body string, sc security.Context) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(security.Into(req.Context(), sc))
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

const decisionPath = "/v1/classifications/decisions"
const proposalIDText = "01920a4d-5a6b-7c8d-9e0f-1a2b3c4d5e6f"

func decisionBody(extra string) string {
	return `{"proposalId":"` + proposalIDText + `","subjectRef":"sku:PLAN-UNL-5G","proposedCode":"ontology:telecom/voice/mobile",` +
		`"action":"ACCEPT","reason":"` + markerReason + `","note":"` + markerNote + `","evidenceId":"call-id-1"` + extra + `}`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	return out
}

func TestRecordClassificationDecisionIs201WithTheSessionIdentity(t *testing.T) {
	rig := newDecisionRig(t, true)
	sc := testClassificationContext(security.RoleOperator)
	rec := rig.do(http.MethodPost, decisionPath, decisionBody(""), sc)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	out := decode(t, rec)
	if out["reviewerUserId"] != sc.Subject().String() || out["tenantId"] != sc.Tenant().String() {
		t.Fatalf("reviewer/tenant = %v/%v, want the session's %s/%s", out["reviewerUserId"], out["tenantId"], sc.Subject(), sc.Tenant())
	}
	if out["action"] != "ACCEPT" || out["proposalId"] != proposalIDText || out["evidenceId"] != "call-id-1" || out["decidedAt"] == nil {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// The body cannot name a reviewer or a tenant: a body that tries is refused as
// a whole, and nothing is saved.
func TestADecisionBodyThatNamesAReviewerOrTenantIsRefused(t *testing.T) {
	rig := newDecisionRig(t, true)
	sc := testClassificationContext(security.RoleOperator)
	for _, extra := range []string{
		`,"reviewerUserId":"01920a4d-0000-7000-8000-0000000000fe"`,
		`,"tenantId":"01920a4d-0000-7000-8000-0000000000ff"`,
	} {
		if rec := rig.do(http.MethodPost, decisionPath, decisionBody(extra), sc); rec.Code != http.StatusBadRequest {
			t.Fatalf("body with %q: status = %d, want 400; body=%s", extra, rec.Code, rec.Body.String())
		}
	}
	if rec := rig.do(http.MethodGet, decisionPath+"/"+proposalIDText, "", sc); rec.Code != http.StatusNotFound {
		t.Fatalf("a refused body saved something: GET = %d", rec.Code)
	}
}

func TestASecondDecisionIs409(t *testing.T) {
	rig := newDecisionRig(t, true)
	sc := testClassificationContext(security.RoleAnalyst)
	if rec := rig.do(http.MethodPost, decisionPath, decisionBody(""), sc); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	if rec := rig.do(http.MethodPost, decisionPath, decisionBody(""), sc); rec.Code != http.StatusConflict {
		t.Fatalf("second: status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

func TestADecisionNeedsOperatorOrAnalystAndAValidBody(t *testing.T) {
	rig := newDecisionRig(t, true)
	for _, role := range []security.Role{security.RoleAdmin, security.RoleAuditor} {
		if rec := rig.do(http.MethodPost, decisionPath, decisionBody(""), testClassificationContext(role)); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", role, rec.Code)
		}
	}
	modifyNoCode := strings.Replace(decisionBody(""), `"action":"ACCEPT"`, `"action":"MODIFY"`, 1)
	if rec := rig.do(http.MethodPost, decisionPath, modifyNoCode, testClassificationContext(security.RoleOperator)); rec.Code != http.StatusBadRequest {
		t.Errorf("MODIFY without a code: status = %d, want 400", rec.Code)
	}
}

func TestAGetReturnsTheDecisionAndAnotherTenantGets404(t *testing.T) {
	rig := newDecisionRig(t, true)
	mine := testClassificationContext(security.RoleOperator)
	if rec := rig.do(http.MethodPost, decisionPath, decisionBody(""), mine); rec.Code != http.StatusCreated {
		t.Fatalf("record: %d", rec.Code)
	}
	rec := rig.do(http.MethodGet, decisionPath+"/"+proposalIDText, "", mine)
	if rec.Code != http.StatusOK || decode(t, rec)["proposalId"] != proposalIDText {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	if rec := rig.do(http.MethodGet, decisionPath+"/"+proposalIDText, "", otherTenantContext(security.RoleOperator)); rec.Code != http.StatusNotFound {
		t.Fatalf("another tenant's GET = %d, want 404", rec.Code)
	}
	if rec := rig.do(http.MethodGet, decisionPath+"/not-a-uuid", "", mine); rec.Code != http.StatusBadRequest {
		t.Fatalf("a bad proposal id = %d, want 400", rec.Code)
	}
}

func TestNoDecisionServiceIs503(t *testing.T) {
	rig := newDecisionRig(t, false)
	if rec := rig.do(http.MethodPost, decisionPath, decisionBody(""), testClassificationContext(security.RoleOperator)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// The free text is on the request and in the reply, never in the logs: not on
// success, on a refusal, on a conflict or on a rejected body.
func TestTheLogsNeverHoldTheReasonOrNote(t *testing.T) {
	rig := newDecisionRig(t, true)
	sc := testClassificationContext(security.RoleOperator)
	rig.do(http.MethodPost, decisionPath, decisionBody(""), sc)
	rig.do(http.MethodPost, decisionPath, decisionBody(""), sc)
	rig.do(http.MethodPost, decisionPath, decisionBody(`,"reviewerUserId":"x"`), sc)
	rig.do(http.MethodPost, decisionPath, strings.Replace(decisionBody(""), `"action":"ACCEPT"`, `"action":"MODIFY"`, 1), sc)
	rig.do(http.MethodPost, decisionPath, decisionBody(""), testClassificationContext(security.RoleAuditor))
	rig.do(http.MethodGet, decisionPath+"/"+proposalIDText, "", sc)
	if rig.logs.Len() == 0 {
		t.Fatal("nothing was logged, so the test proved nothing")
	}
	if strings.Contains(rig.logs.String(), "marker-") {
		t.Fatalf("free text reached the logs:\n%s", rig.logs.String())
	}
}
