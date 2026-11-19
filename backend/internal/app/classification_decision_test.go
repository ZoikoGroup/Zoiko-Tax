package app_test

import (
	"context"
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
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// memReviewDecisions is an in-memory decision store scoped by the tenant in the
// context, as the real adapter is by its query and the row-level policy. It
// deliberately does NOT enforce uniqueness: that is the service's pre-check and
// the database key, and the service test must prove the pre-check on its own.
// leak makes Decision ignore the tenant, to model a store whose filter failed.
type memReviewDecisions struct {
	mu   sync.Mutex
	rows map[string]classification.ReviewDecision
	leak bool
}

func newMemReviewDecisions() *memReviewDecisions {
	return &memReviewDecisions{rows: map[string]classification.ReviewDecision{}}
}

func decisionKey(ctx context.Context, p uuid.UUID) string {
	sc, _ := security.From(ctx)
	return sc.Tenant().String() + "/" + p.String()
}

func (m *memReviewDecisions) Lock(context.Context, uuid.UUID) error { return nil }

func (m *memReviewDecisions) Create(ctx context.Context, d classification.ReviewDecision) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[decisionKey(ctx, d.ProposalID)] = d
	return nil
}

func (m *memReviewDecisions) Decision(ctx context.Context, p uuid.UUID) (classification.ReviewDecision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.leak {
		for _, d := range m.rows {
			if d.ProposalID == p {
				return d, nil
			}
		}
	} else if d, ok := m.rows[decisionKey(ctx, p)]; ok {
		return d, nil
	}
	return classification.ReviewDecision{}, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "none")
}

var (
	decisionProposal = uuid.MustParse("01920a4d-5a6b-7c8d-9e0f-1a2b3c4d5e6f")
	decisionReviewer = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000f1"))
	otherTenant      = id.NewTenantID(uuid.MustParse("01920000-0000-7000-8000-0000000000ab"))
	otherReviewer    = id.NewUserID(uuid.MustParse("01920000-0000-7000-8000-0000000000f2"))
)

func decisionCtx(tenant id.TenantID, user id.UserID, roles ...security.Role) context.Context {
	sc := security.New(tenant, user, id.NewSessionID(uuid.MustParse("01920000-0000-7000-8000-0000000000cc")), roles, time.Now())
	return security.Into(context.Background(), sc)
}

type decisionHarness struct {
	svc   *app.ClassificationDecisionService
	store *memReviewDecisions
	audit *memAudit
}

func newDecisionHarness() *decisionHarness {
	h := &decisionHarness{store: newMemReviewDecisions(), audit: &memAudit{}}
	clk := &settableClock{t: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	h.svc = app.NewClassificationDecisionService(h.store, h.audit, noTx{}, clk, &idgen.Sequential{})
	return h
}

func validDecision(action classification.Action) app.DecisionInput {
	in := app.DecisionInput{
		ProposalID: decisionProposal, SubjectRef: "sku:PLAN-UNL-5G", ProposedCode: "ontology:telecom/voice/mobile",
		Action: action, Reason: "The code fits the plan",
	}
	if action == classification.ActionModify {
		in.ModifiedCode = "ontology:telecom/data/mobile"
	}
	return in
}

func asReviewer(roles ...security.Role) context.Context {
	return decisionCtx(tenantID, decisionReviewer, roles...)
}

func TestEachDecisionActionIsRecorded(t *testing.T) {
	for _, a := range []classification.Action{classification.ActionAccept, classification.ActionReject, classification.ActionModify, classification.ActionEscalate} {
		t.Run(string(a), func(t *testing.T) {
			h := newDecisionHarness()
			d, err := h.svc.Record(asReviewer(security.RoleOperator), validDecision(a))
			if err != nil {
				t.Fatal(err)
			}
			if d.Action != a {
				t.Fatalf("action = %s, want %s", d.Action, a)
			}
		})
	}
}

func TestADecisionThatBreaksARuleIsRefusedAndNothingIsSaved(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	cases := map[string]func(*app.DecisionInput){
		"reason missing":        func(in *app.DecisionInput) { in.Reason = "" },
		"reason blank":          func(in *app.DecisionInput) { in.Reason = "   \t " },
		"reason over 500":       func(in *app.DecisionInput) { in.Reason = long(501) },
		"note over 2000":        func(in *app.DecisionInput) { in.Note = long(2001) },
		"MODIFY without a code": func(in *app.DecisionInput) { in.Action, in.ModifiedCode = classification.ActionModify, "" },
		"MODIFY with a heading": func(in *app.DecisionInput) {
			in.Action, in.ModifiedCode = classification.ActionModify, "#ontology:telecom/data"
		},
		"MODIFY with no grammar": func(in *app.DecisionInput) {
			in.Action, in.ModifiedCode = classification.ActionModify, "GST-DIGITAL-SERVICE"
		},
		"ACCEPT with a code": func(in *app.DecisionInput) { in.ModifiedCode = "ontology:telecom/data/mobile" },
		"REJECT with a code": func(in *app.DecisionInput) {
			in.Action, in.ModifiedCode = classification.ActionReject, "ontology:telecom/data/mobile"
		},
		"ESCALATE with a code": func(in *app.DecisionInput) {
			in.Action, in.ModifiedCode = classification.ActionEscalate, "ontology:telecom/data/mobile"
		},
		"unknown action":             func(in *app.DecisionInput) { in.Action = "APPROVE" },
		"proposed code not ontology": func(in *app.DecisionInput) { in.ProposedCode = "GST-DIGITAL-SERVICE" },
		"subject missing":            func(in *app.DecisionInput) { in.SubjectRef = " " },
		"proposal id missing":        func(in *app.DecisionInput) { in.ProposalID = uuid.Nil },
		"evidence id with a space":   func(in *app.DecisionInput) { in.EvidenceID = "call id" },
		"evidence id over 64":        func(in *app.DecisionInput) { in.EvidenceID = long(65) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newDecisionHarness()
			in := validDecision(classification.ActionAccept)
			mutate(&in)
			_, err := h.svc.Record(asReviewer(security.RoleOperator), in)
			if errs.CategoryOf(err) != errs.CategoryValidation {
				t.Fatalf("err = %v (%s), want a validation error", err, errs.CategoryOf(err))
			}
			if len(h.store.rows) != 0 || len(h.audit.recs) != 0 {
				t.Fatal("a refused decision left a trace")
			}
		})
	}
}

func TestAValidEvidenceIDAndNoteAreKept(t *testing.T) {
	h := newDecisionHarness()
	in := validDecision(classification.ActionAccept)
	in.EvidenceID, in.Note = "call-id-1", "  checked with the product team  "
	d, err := h.svc.Record(asReviewer(security.RoleAnalyst), in)
	if err != nil {
		t.Fatal(err)
	}
	if d.EvidenceID != "call-id-1" || d.Note != "checked with the product team" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestOnlyOperatorsAndAnalystsMayDecide(t *testing.T) {
	h := newDecisionHarness()
	for _, role := range []security.Role{security.RoleAdmin, security.RoleAuditor} {
		if _, err := h.svc.Record(asReviewer(role), validDecision(classification.ActionAccept)); errs.ReasonOf(err) != errs.ReasonForbidden {
			t.Errorf("%s: err = %v, want forbidden", role, err)
		}
		if _, err := h.svc.Decision(asReviewer(role), decisionProposal); errs.ReasonOf(err) != errs.ReasonForbidden {
			t.Errorf("%s read: err = %v, want forbidden", role, err)
		}
	}
	if _, err := h.svc.Record(context.Background(), validDecision(classification.ActionAccept)); errs.ReasonOf(err) != errs.ReasonUnauthenticated {
		t.Errorf("no session: err = %v, want unauthenticated", err)
	}
}

func TestASecondDecisionForTheSameProposalIsAConflictAndChangesNothing(t *testing.T) {
	h := newDecisionHarness()
	ctx := asReviewer(security.RoleOperator)
	if _, err := h.svc.Record(ctx, validDecision(classification.ActionAccept)); err != nil {
		t.Fatal(err)
	}
	second := validDecision(classification.ActionReject)
	second.Reason = "Changed my mind"
	_, err := h.svc.Record(ctx, second)
	if errs.CategoryOf(err) != errs.CategoryConflict {
		t.Fatalf("err = %v, want a conflict", err)
	}
	got, err := h.svc.Decision(ctx, decisionProposal)
	if err != nil || got.Action != classification.ActionAccept || got.Reason != "The code fits the plan" {
		t.Fatalf("stored decision = %+v, %v; want the first one unchanged", got, err)
	}
	if len(h.audit.recs) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(h.audit.recs))
	}
}

func TestTheReviewerAndTenantComeFromTheSession(t *testing.T) {
	h := newDecisionHarness()
	d, err := h.svc.Record(asReviewer(security.RoleOperator), validDecision(classification.ActionAccept))
	if err != nil {
		t.Fatal(err)
	}
	if d.ReviewerID != decisionReviewer || d.TenantID != tenantID {
		t.Fatalf("reviewer/tenant = %s/%s, want the session's %s/%s", d.ReviewerID, d.TenantID, decisionReviewer, tenantID)
	}
	// The input type has no reviewer or tenant, so a caller cannot supply one;
	// a different session gives a different reviewer.
	h2 := newDecisionHarness()
	d2, err := h2.svc.Record(decisionCtx(otherTenant, otherReviewer, security.RoleAnalyst), validDecision(classification.ActionAccept))
	if err != nil || d2.ReviewerID != otherReviewer || d2.TenantID != otherTenant {
		t.Fatalf("second session: %+v, %v", d2, err)
	}
}

func TestADecisionCanBeReadBack(t *testing.T) {
	h := newDecisionHarness()
	ctx := asReviewer(security.RoleOperator)
	in := validDecision(classification.ActionModify)
	in.EvidenceID = "call-id-1"
	want, err := h.svc.Record(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.svc.Decision(ctx, decisionProposal)
	if err != nil || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
	if _, err := h.svc.Decision(ctx, uuid.MustParse("01920a4d-0000-7000-8000-000000000001")); errs.CategoryOf(err) != errs.CategoryNotFound {
		t.Fatalf("an unknown proposal: %v, want not found", err)
	}
}

// Another tenant's decision is not found even if the store hands it back: the
// service checks the tenant itself.
func TestAnotherTenantsDecisionIsNotFound(t *testing.T) {
	h := newDecisionHarness()
	if _, err := h.svc.Record(asReviewer(security.RoleOperator), validDecision(classification.ActionAccept)); err != nil {
		t.Fatal(err)
	}
	theirs := decisionCtx(otherTenant, otherReviewer, security.RoleOperator)
	if _, err := h.svc.Decision(theirs, decisionProposal); errs.CategoryOf(err) != errs.CategoryNotFound {
		t.Fatalf("honest store: %v, want not found", err)
	}
	h.store.leak = true
	if _, err := h.svc.Decision(theirs, decisionProposal); errs.CategoryOf(err) != errs.CategoryNotFound {
		t.Fatalf("a store that ignores the tenant: %v, want the service to say not found", err)
	}
}

func TestTheAuditEntryHoldsTheActionAndNoFreeText(t *testing.T) {
	h := newDecisionHarness()
	in := validDecision(classification.ActionEscalate)
	in.Reason, in.Note = "marker-reason-text", "marker-note-text"
	if _, err := h.svc.Record(asReviewer(security.RoleOperator), in); err != nil {
		t.Fatal(err)
	}
	if len(h.audit.recs) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(h.audit.recs))
	}
	r := h.audit.recs[0]
	if r.Action != "classification.decision.escalate" || r.SubjectID != decisionProposal.String() || r.ActorUserID == nil || *r.ActorUserID != decisionReviewer {
		t.Fatalf("audit = %+v", r)
	}
	if s := string(r.Detail); strings.Contains(s, "marker-") {
		t.Fatalf("free text reached the audit detail: %s", s)
	}
}

func TestNoErrorMessageEchoesTheFreeText(t *testing.T) {
	h := newDecisionHarness()
	in := validDecision(classification.ActionAccept)
	in.Reason = "marker-reason-text" + strings.Repeat("x", 500)
	in.Note = "marker-note-text" + strings.Repeat("y", 2000)
	_, err := h.svc.Record(asReviewer(security.RoleOperator), in)
	if err == nil || strings.Contains(err.Error(), "marker-") {
		t.Fatalf("err = %v, want a refusal that does not echo the text", err)
	}
}
