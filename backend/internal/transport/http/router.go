package http

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
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
	// so a caller can name the exact combination that produced a response.
	Trains map[string]string
	// Cell and Region identify this cell.
	Cell, Region, Environment string
	// Authoritative is false until A4. The Build Plan permits no authoritative
	// fiscal output before then, and the capabilities surface says so rather
	// than leaving a caller to assume.
	Authoritative bool
}

// NewRouter wires the surface.
func NewRouter(auth *app.AuthService, admin *app.AdminService, users port.UserRepository, ready Readiness, log *slog.Logger, ids idgen.Generator) *Router {
	return &Router{auth: auth, admin: admin, users: users, ready: ready, log: mustLogger(log), ids: ids}
}

// Handler returns the routed, wrapped handler.
//
// ADR-0010 §2.4: stdlib ServeMux with method-and-pattern matching, and a
// middleware chain constructed here so it reads top to bottom rather than being
// assembled from decorators scattered across files.
func (rt *Router) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health probes. Unauthenticated by design: an orchestrator has no session,
	// and a readiness probe that needs a credential is a readiness probe that
	// fails during a credential outage for the wrong reason.
	mux.HandleFunc("GET /healthz", rt.handleHealthz)
	mux.HandleFunc("GET /readyz", rt.handleReadyz)
	mux.HandleFunc("GET /v1/capabilities", rt.handleCapabilities)

	// Authentication. Sign-in is necessarily unauthenticated; the rest require
	// a session but no particular role.
	mux.HandleFunc("POST /v1/auth/sign-in", rt.handleSignIn)
	mux.Handle("POST /v1/auth/sign-out", requireAuth(rt.log, http.HandlerFunc(rt.handleSignOut)))
	mux.Handle("GET /v1/auth/session", requireAuth(rt.log, http.HandlerFunc(rt.handleSession)))
	mux.Handle("POST /v1/auth/password", requireAuth(rt.log, http.HandlerFunc(rt.handleChangePassword)))

	// Administration. Each route names the roles it needs, so an endpoint that
	// forgets is visibly missing a line rather than inheriting a default.
	admin := security.RoleAdmin
	mux.Handle("GET /v1/admin/tenant", requireAuth(rt.log, http.HandlerFunc(rt.handleGetTenant)))
	mux.Handle("GET /v1/admin/users", requireRole(rt.log, http.HandlerFunc(rt.handleListUsers),
		admin, security.RoleAnalyst, security.RoleAuditor))
	mux.Handle("POST /v1/admin/users", requireRole(rt.log, http.HandlerFunc(rt.handleCreateUser), admin))
	mux.Handle("POST /v1/admin/users/{userId}/status", requireRole(rt.log, http.HandlerFunc(rt.handleSetUserStatus), admin))
	mux.Handle("POST /v1/admin/users/{userId}/roles", requireRole(rt.log, http.HandlerFunc(rt.handleGrantRole), admin))
	mux.Handle("DELETE /v1/admin/users/{userId}/roles/{role}", requireRole(rt.log, http.HandlerFunc(rt.handleRevokeRole), admin))
	mux.Handle("GET /v1/admin/sessions", requireRole(rt.log, http.HandlerFunc(rt.handleListSessions), admin))
	mux.Handle("DELETE /v1/admin/sessions/{sessionId}", requireRole(rt.log, http.HandlerFunc(rt.handleRevokeSession), admin))
	mux.Handle("GET /v1/admin/audit", requireRole(rt.log, http.HandlerFunc(rt.handleListAudit),
		admin, security.RoleAuditor))

	// An unrouted path returns a Problem rather than ServeMux's plain-text
	// 404, so every error a client sees has the same shape (ADR-0016 §2.5).
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, rt.log, errs.New(errs.CategoryNotFound, errs.ReasonNotFound,
			"No endpoint is routed at that path."))
	})

	return chain(mux,
		withRecovery(rt.log),
		withRequestID(rt.ids),
		withLogging(rt.log),
		withAuthentication(rt.auth, rt.log),
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

type capabilitiesResponse struct {
	Cell          string            `json:"cell"`
	Region        string            `json:"region"`
	Environment   string            `json:"environment"`
	Trains        map[string]string `json:"trains"`
	CanonProfile  string            `json:"canonProfile"`
	Authoritative bool              `json:"authoritative"`
	ReasonCodes   []string          `json:"reasonCodes"`
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
	writeJSON(w, r, rt.log, http.StatusOK, capabilitiesResponse{
		Cell:          rt.Cell,
		Region:        rt.Region,
		Environment:   rt.Environment,
		Trains:        rt.Trains,
		CanonProfile:  "canon/v1",
		Authoritative: rt.Authoritative,
		ReasonCodes:   names,
	})
}
