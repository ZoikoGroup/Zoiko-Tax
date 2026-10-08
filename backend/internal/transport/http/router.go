package http

import (
	"context"
	"log/slog"
	"math"
	"net/http"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/rule"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// Readiness reports whether the cell can serve. It is an interface so the
// router does not import the persistence adapter (ADR-0007 §2.5).
type Readiness interface {
	Ready(ctx context.Context) error
}

// Router builds the cell's HTTP surface.
type Router struct {
	auth  *app.AuthService
	admin *app.AdminService
	users port.UserRepository
	ready Readiness
	log   *slog.Logger
	ids   idgen.Generator

	// SecureCookies sets the Secure flag and the __Host- cookie prefix. It is
	// false only for plain-HTTP local development; see setSessionCookie for
	// why the name changes with it.
	SecureCookies bool
	// TrustProxy honours X-Forwarded-For. False unless the deployment actually
	// sits behind a proxy, because an unconditionally trusted header is a
	// header a client can forge.
	TrustProxy bool

	// Trains are the seven release-train versions, reported by /v1/capabilities
	// so a caller can name the exact combination that produced a response. A
	// struct rather than a map, so a train the contract adds is a field the
	// caller must set rather than a key it can forget.
	Trains Trains
	// Cell and Region identify this cell.
	Cell, Region, Environment string
	// Authoritative is false until A4. The Build Plan permits no authoritative
	// fiscal output before then, and the capabilities surface says so rather
	// than leaving a caller to assume.
	Authoritative bool

	// Tracer opens the request spans. Nil is a no-op provider, so a router
	// built without telemetry traces nothing rather than failing.
	Tracer trace.TracerProvider

	// Content is the active rule bundle, or nil in a cell deployed without one.
	// It is the Holder rather than the Bundle, so that a later activation swaps
	// under a live process and /v1/capabilities reports what is running now
	// rather than what was running at boot (ADR-0005 §2.6).
	Content *rule.Holder

	// Determination serves quote, commit, decisions and replay. Nil in a cell
	// deployed without an evidence store, which answers all four with 503
	// rather than serving the parts that happen not to write.
	Determination *app.DeterminationService

	// Models is the Governed Model Gateway client (ADR-0006): the only route
	// from this process to the AI plane. Every result it returns is an
	// advisory type, and nothing in the transport converts one into a fiscal
	// value (ADR-0006 §2.6). Nil in a router built without one.
	Models port.ModelGateway

	// Classification proposes advisory ontology mappings through Models. Nil
	// in a router built without one, which answers 503.
	Classification *app.ClassificationService
	// Refunds serves POST /v1/transactions:refund and the refund reads and
	// provider reports. Nil in a cell deployed without the determination
	// surface, which answers 503.
	Refunds *app.RefundService

	// Webhooks administers webhook subscriptions and reads their deliveries.
	// Nil in a cell with no webhook key, which answers 503.
	Webhooks *app.WebhookService

	// Batches serves POST /v1/batches and GET /v1/jobs/{id}. Nil wherever
	// Determination is, since every item takes the commit path.
	Batches *app.BatchService

	// Seals serves the period seals, their verification and decisions'
	// inclusion proofs. Nil in a cell with no seal keyring, which answers
	// 503.
	Seals *app.SealService

	// Documents serves the fiscal documents. Nil wherever Determination is,
	// since documents present its decisions.
	Documents *app.DocumentService

	// Periods serves the subledger's legal periods: close, amendment
	// windows, and reopening on approval. Nil wherever Determination is.
	Periods *app.PeriodService
}

// Trains are the seven release-train versions, as the contract names them.
type Trains = gen.Trains

// NewRouter wires the surface.
func NewRouter(auth *app.AuthService, admin *app.AdminService, users port.UserRepository, ready Readiness, log *slog.Logger, ids idgen.Generator) *Router {
	return &Router{auth: auth, admin: admin, users: users, ready: ready, log: mustLogger(log), ids: ids}
}

// Route is one entry in the surface.
//
// The routes are a table rather than a sequence of mux.Handle calls so that
// they are *data*, and can be compared with the contract. The request and
// response types are generated from contracts/openapi (package gen); routing is
// not, because the generated server interface would bring a runtime module into
// the request path. contract_test.go covers that half instead, comparing this
// table against the same contract in both directions — an endpoint the contract
// does not declare fails, and so does a declared endpoint nothing routes.
//
// Roles are part of the table for the same reason they were part of each
// Handle call: an endpoint that forgets its authorization is a visibly missing
// field rather than a default it inherited.
type Route struct {
	Method  string
	Pattern string
	// Public marks a route that requires no session. There are three, and each
	// has a reason recorded at its declaration.
	Public bool
	// Roles are the roles permitted. Empty with Public false means any
	// authenticated subject.
	Roles []security.Role
}

// routes is the cell's HTTP surface.
func (rt *Router) routes() []struct {
	Route
	handler http.HandlerFunc
} {
	admin := security.RoleAdmin
	operator, analyst, auditor := security.RoleOperator, security.RoleAnalyst, security.RoleAuditor
	return []struct {
		Route
		handler http.HandlerFunc
	}{
		// Health probes. Unauthenticated by design: an orchestrator has no
		// session, and a readiness probe that needs a credential is a readiness
		// probe that fails during a credential outage for the wrong reason.
		// They are deliberately absent from the contract — see its preamble.
		{Route{"GET", "/healthz", true, nil}, rt.handleHealthz},
		{Route{"GET", "/readyz", true, nil}, rt.handleReadyz},

		// Discovery. Unauthenticated because a client needs to know whether a
		// cell can serve it before it has a session.
		{Route{"GET", "/v1/capabilities", true, nil}, rt.handleCapabilities},

		// Authentication. Sign-in is necessarily unauthenticated; the rest
		// require a session but no particular role.
		{Route{"POST", "/v1/auth/sign-in", true, nil}, rt.handleSignIn},
		{Route{"POST", "/v1/auth/sign-out", false, nil}, rt.handleSignOut},
		{Route{"GET", "/v1/auth/session", false, nil}, rt.handleSession},
		{Route{"POST", "/v1/auth/password", false, nil}, rt.handleChangePassword},

		// Administration.
		{Route{"GET", "/v1/admin/tenant", false, nil}, rt.handleGetTenant},
		{Route{"GET", "/v1/admin/users", false, []security.Role{admin, security.RoleAnalyst, security.RoleAuditor}}, rt.handleListUsers},
		{Route{"POST", "/v1/admin/users", false, []security.Role{admin}}, rt.handleCreateUser},
		{Route{"POST", "/v1/admin/users/{userId}/status", false, []security.Role{admin}}, rt.handleSetUserStatus},
		{Route{"POST", "/v1/admin/users/{userId}/roles", false, []security.Role{admin}}, rt.handleGrantRole},
		{Route{"DELETE", "/v1/admin/users/{userId}/roles/{role}", false, []security.Role{admin}}, rt.handleRevokeRole},
		{Route{"GET", "/v1/admin/sessions", false, []security.Role{admin}}, rt.handleListSessions},
		{Route{"DELETE", "/v1/admin/sessions/{sessionId}", false, []security.Role{admin}}, rt.handleRevokeSession},
		{Route{"GET", "/v1/admin/audit", false, []security.Role{admin, security.RoleAuditor}}, rt.handleListAudit},

		// Webhooks. Which events leave the cell, and to where, is an egress
		// decision and ADMIN's; it moves no figure, so no fiscal role holds
		// it.
		{Route{"GET", "/v1/webhooks", false, []security.Role{admin, auditor}}, rt.handleListWebhooks},
		{Route{"POST", "/v1/webhooks", false, []security.Role{admin}}, rt.handleCreateWebhook},
		{Route{"GET", "/v1/webhooks/{webhookId}", false, []security.Role{admin, auditor}}, rt.handleGetWebhook},
		{Route{"POST", "/v1/webhooks/{webhookId}/status", false, []security.Role{admin}}, rt.handleSetWebhookStatus},
		{Route{"POST", "/v1/webhooks/{webhookId}/secrets:rotate", false, []security.Role{admin}}, rt.handleRotateWebhookSecret},
		{Route{"GET", "/v1/webhooks/{webhookId}/deliveries", false, []security.Role{admin, auditor}}, rt.handleListWebhookDeliveries},
		{Route{"GET", "/v1/webhooks/{webhookId}/deliveries/{deliveryId}", false, []security.Role{admin, auditor}}, rt.handleGetWebhookDelivery},
		{Route{"POST", "/v1/webhooks/{webhookId}/deliveries/{deliveryId}/replay", false, []security.Role{admin}}, rt.handleReplayWebhookDelivery},

		// Determination. ADMIN is on none of them: administering a tenant's
		// users is not a fiscal operation, and a role that could do both
		// could grant itself the approval it then exercises.
		{Route{"POST", "/v1/quotes", false, []security.Role{operator, analyst}}, rt.handleQuote},
		{Route{"POST", "/v1/transactions:commit", false, []security.Role{operator}}, rt.handleCommit},
		{Route{"POST", "/v1/transactions:adjust", false, []security.Role{operator}}, rt.handleAdjust},
		{Route{"POST", "/v1/transactions:refund", false, []security.Role{operator}}, rt.handleRefund},
		{Route{"GET", "/v1/refunds/{refundId}", false, []security.Role{operator, analyst, auditor}}, rt.handleGetRefund},
		{Route{"POST", "/v1/batches", false, []security.Role{operator}}, rt.handleSubmitBatch},
		{Route{"POST", "/v1/documents", false, []security.Role{operator}}, rt.handleIssueDocument},
		{Route{"GET", "/v1/documents/{documentId}", false, []security.Role{operator, analyst, auditor}}, rt.handleGetDocument},
		{Route{"POST", "/v1/documents/{documentId}/corrections", false, []security.Role{operator}}, rt.handleCorrectDocument},
		{Route{"GET", "/v1/documents/{documentId}/lineage", false, []security.Role{operator, analyst, auditor}}, rt.handleDocumentLineage},
		{Route{"GET", "/v1/jobs/{jobId}", false, []security.Role{operator, analyst, auditor}}, rt.handleGetJob},
		{Route{"POST", "/v1/refunds/{refundId}/reports", false, []security.Role{operator}}, rt.handleReportRefund},
		{Route{"GET", "/v1/decisions/{decisionId}", false, []security.Role{operator, analyst, auditor}}, rt.handleGetDecision},
		{Route{"POST", "/v1/replay/{decisionId}", false, []security.Role{operator, analyst, auditor}}, rt.handleReplay},
		{Route{"GET", "/v1/decisions/{decisionId}/journals", false, []security.Role{operator, analyst, auditor}}, rt.handleDecisionJournals},
		{Route{"GET", "/v1/decisions/{decisionId}/inclusion", false, []security.Role{operator, analyst, auditor, admin}}, rt.handleDecisionInclusion},

		// Period seals. Making one is ADMIN's — it is a statement about the
		// record, not a fiscal act; reading and verifying one is for anyone
		// who audits.
		{Route{"GET", "/v1/seals", false, []security.Role{admin, auditor, analyst}}, rt.handleListSeals},
		{Route{"POST", "/v1/seals", false, []security.Role{admin}}, rt.handleSealPeriod},
		{Route{"GET", "/v1/seals/{sealId}", false, []security.Role{admin, auditor, analyst}}, rt.handleGetSeal},
		{Route{"GET", "/v1/seals/{sealId}/verification", false, []security.Role{admin, auditor, analyst}}, rt.handleVerifySeal},
		{Route{"GET", "/v1/subledger/balances", false, []security.Role{operator, analyst, auditor}}, rt.handleSubledgerBalances},
		{Route{"GET", "/v1/subledger/periods/{period}", false, []security.Role{operator, analyst, auditor}}, rt.handleGetPeriod},
		{Route{"POST", "/v1/subledger/periods/{period}/transitions", false, []security.Role{operator}}, rt.handleTransitionPeriod},
		{Route{"POST", "/v1/subledger/periods/{period}/reopen-requests", false, []security.Role{operator}}, rt.handleRequestReopen},
		// Approval is ADMIN's, and never the requester's: the service refuses
		// a self-approval whatever roles the approver holds.
		{Route{"POST", "/v1/subledger/periods/{period}/reopen-requests/{requestId}/approval", false, []security.Role{admin}}, rt.handleApproveReopen},
		{Route{"GET", "/v1/obligations", false, []security.Role{operator, analyst, auditor}}, rt.handleListObligations},
		{Route{"GET", "/v1/obligations/{obligationId}", false, []security.Role{operator, analyst, auditor}}, rt.handleGetObligation},
		{Route{"POST", "/v1/obligations/{obligationId}/transitions", false, []security.Role{operator}}, rt.handleTransitionObligation},

		// Classification assistance: advisory, through the Governed Model
		// Gateway only.
		{Route{"POST", "/v1/classifications:propose", false, []security.Role{operator, analyst}}, rt.handleProposeClassification},
	}
}

// Routes reports the surface this router serves. It exists for the contract
// conformance test and for the startup log; nothing in the request path uses it.
func (rt *Router) Routes() []Route {
	all := rt.routes()
	out := make([]Route, 0, len(all))
	for _, r := range all {
		out = append(out, r.Route)
	}
	return out
}

// Handler returns the routed, wrapped handler.
//
// ADR-0010 §2.4: stdlib ServeMux with method-and-pattern matching, and a
// middleware chain constructed here so it reads top to bottom rather than being
// assembled from decorators scattered across files.
func (rt *Router) Handler() http.Handler {
	mux := http.NewServeMux()

	for _, r := range rt.routes() {
		handler := http.Handler(r.handler)
		switch {
		case r.Public:
		case len(r.Roles) == 0:
			handler = requireAuth(rt.log, handler)
		default:
			handler = requireRole(rt.log, handler, r.Roles...)
		}
		mux.Handle(r.Method+" "+r.Pattern, handler)
	}

	// An unrouted path returns a Problem rather than ServeMux's plain-text
	// 404, so every error a client sees has the same shape (ADR-0016 §2.5).
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, rt.log, errs.New(errs.CategoryNotFound, errs.ReasonNotFound,
			"No endpoint is routed at that path."))
	})

	tp := rt.Tracer
	if tp == nil {
		tp = noop.NewTracerProvider()
	}
	return chain(mux,
		withRecovery(rt.log),
		withRequestID(rt.ids),
		withTracing(tp, mux),
		withLogging(rt.log),
		withAuthentication(rt.auth, rt.log),
		withResidency(rt.Cell, rt.log),
	)
}

// handleHealthz is liveness: the process is running and can serve. It
// deliberately touches no dependency — a liveness probe that fails when the
// database is down gets the process killed and restarted, which does not fix a
// database and does lose the in-memory content bundle.
func (rt *Router) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// handleReadyz is readiness: this replica can take traffic right now.
//
// This one does check the database, because a replica that cannot reach its
// cell's store should be taken out of rotation rather than serving 503s.
func (rt *Router) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if rt.ready != nil {
		if err := rt.ready.Ready(r.Context()); err != nil {
			rt.log.WarnContext(r.Context(), "not ready", "error", err.Error())
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready\n"))
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
}

// handleCapabilities is ADR-0010 §2.6's runtime discovery mechanism.
//
// It reports effective capability, and the field that matters most is
// authoritative: no authoritative fiscal output is permitted before A4, and a
// client is entitled to discover that from the service rather than from a
// release note.
func (rt *Router) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	codes := errs.Codes()
	names := make([]string, 0, len(codes))
	for _, c := range codes {
		names = append(names, string(c))
	}
	// Content is absent in a cell with no bundle loaded. Absent rather than an
	// empty object: "this cell has no content" and "this cell has a content
	// bundle with no identity" are different facts, and a client that has to
	// tell them apart should not have to guess (ADR-0011 P3, at the API).
	var content *gen.ContentCapability
	if rt.Content != nil {
		if b := rt.Content.Current(); b != nil {
			content = &gen.ContentCapability{
				BundleID:  b.ID(),
				Digest:    b.Digest(),
				IrVersion: saturate32(b.IRVersion()),
				NodeCount: saturate32(b.NodeCount()),
			}
		}
	}

	events := app.Events()
	eventTypes := make([]gen.EventType, len(events))
	for i, e := range events {
		eventTypes[i] = gen.EventType(e.Type)
	}

	writeJSON(w, r, rt.log, http.StatusOK, gen.Capabilities{
		Cell:          rt.Cell,
		Region:        rt.Region,
		Environment:   rt.Environment,
		Trains:        rt.Trains,
		CanonProfile:  "canon/v1",
		Authoritative: rt.Authoritative,
		ReasonCodes:   names,
		EventTypes:    eventTypes,
		Content:       content,
	})
}

// saturate32 narrows a count to the contract's int32. Nothing a real bundle
// holds comes near the limit, and nothing enforces one either, so an
// out-of-range count is reported as the maximum rather than wrapped into a
// negative number a client would have to explain.
func saturate32(n int) int32 {
	switch {
	case n > math.MaxInt32:
		return math.MaxInt32
	case n < 0:
		return 0
	default:
		return int32(n)
	}
}
