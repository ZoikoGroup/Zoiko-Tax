package http

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// The chain is constructed in Router and reads top to bottom:
//
//	recovery → request id → logging → security context → authorization → handler
//
// ADR-0010 §2.4 names tracing, residency and idempotency in this chain too.
// Tracing waits on internal/platform/telemetry (ADR-0015 §2.1); residency is a
// property of the session's tenant rather than a header, so it is enforced
// where the tenant is resolved; idempotency applies to the fiscal write path
// and is wired there rather than globally, because applying it to reads would
// make every GET take a write lock on a key.

// Middleware is a handler decorator.
type Middleware func(http.Handler) http.Handler

// chain applies middleware so that the first argument is the outermost.
func chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

type requestIDKey struct{}

// requestIDOf returns the request id carried on a context, for the Problem
// document and for the log.
func requestIDOf(ctx context.Context) string {
	s, _ := ctx.Value(requestIDKey{}).(string)
	return s
}

// withRecovery converts a panic into CategoryInternal (ADR-0016 §2.8).
//
// A panic in a request is a defect. It is logged with a stack trace, alerted
// on, and returned as a generic Problem — the stack never reaches the client,
// because a stack trace names internal paths and sometimes values.
func withRecovery(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					log.ErrorContext(r.Context(), "panic in request handler",
						"http.method", r.Method,
						"http.path", r.URL.Path,
						"panic", v,
						"stack", string(debug.Stack()),
					)
					writeProblem(w, r, log, errs.New(errs.CategoryInternal, errs.ReasonPanicRecovered,
						"A defect interrupted request handling. The request was not applied."))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// withRequestID assigns an identifier to every request.
//
// A client-supplied X-Request-Id is deliberately not honoured: it would let a
// caller collide two requests in our logs, and correlation across a client
// boundary is what the traceparent header is for.
func withRequestID(ids idgen.Generator) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			value := ""
			if rid, err := idgen.RequestID(ids); err == nil {
				value = rid.String()
			}
			w.Header().Set("X-Request-Id", value)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, value)))
		})
	}
}

// statusRecorder captures the status for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// withLogging emits one structured line per request (ADR-0015 §2.7).
//
// It logs the path as routed, never the raw URL with its query string: a query
// string is caller-controlled and is a common way for an identifier or an email
// address to end up in a log that is retained differently from the data it
// describes (ADR-0015 §2.2).
func withLogging(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			attrs := []any{
				"http.method", r.Method,
				"http.path", r.URL.Path,
				"http.status", rec.status,
				"http.duration_ms", time.Since(start).Milliseconds(),
				"ztx.request_id", requestIDOf(r.Context()),
			}
			// The tenant and subject are logged when present, because "which
			// tenant was this" is the first question of any investigation.
			if sc, ok := security.From(r.Context()); ok && sc.Authenticated() {
				attrs = append(attrs, "ztx.tenant_id", sc.Tenant().String(), "ztx.subject", sc.Subject().String())
			}
			log.InfoContext(r.Context(), "request", attrs...)
		})
	}
}

// SessionCookieName is the cookie the session token travels in.
//
// The __Host- prefix is a browser-enforced guarantee, not a convention: a
// cookie with this prefix must be Secure, must have Path=/, and must have no
// Domain attribute. That last one is what matters — it makes the cookie
// unsettable by a subdomain, so a compromised subdomain cannot fixate a session
// on the parent origin.
const SessionCookieName = "__Host-ztax_session"

// withAuthentication resolves the session cookie into a security context.
//
// It does not reject an unauthenticated request. Authorization is the next
// stage's job, and endpoints differ in what they require — sign-in and the
// health probes require nothing. Separating them means an endpoint that forgets
// to require a role is visibly missing a line rather than silently inheriting a
// permissive default.
func withAuthentication(auth *app.AuthService, log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			value := readSessionCookie(r)
			if value == "" {
				next.ServeHTTP(w, r)
				return
			}
			result, err := auth.Authenticate(r.Context(), value)
			if err != nil {
				// A cookie that no longer resolves is cleared, so the browser
				// stops sending it and the user sees a clean sign-in rather
				// than a loop of rejected requests.
				clearSessionCookie(w)
				writeProblem(w, r, log, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(security.Into(r.Context(), result.Security)))
		})
	}
}

// requireAuth rejects a request with no security context.
func requireAuth(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc, ok := security.From(r.Context())
		if !ok || !sc.Authenticated() {
			writeProblem(w, r, log, errs.New(errs.CategoryPolicy, errs.ReasonUnauthenticated,
				"The request carried no valid session. Sign in and retry."))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireRole rejects a request whose subject holds none of the given roles.
func requireRole(log *slog.Logger, next http.Handler, roles ...security.Role) http.Handler {
	return requireAuth(log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc, _ := security.From(r.Context())
		if !sc.HasAny(roles...) {
			writeProblem(w, r, log, errs.New(errs.CategoryPolicy, errs.ReasonForbidden,
				"The authenticated subject does not hold a role permitting this action."))
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// insecureSessionCookieName is what the cookie is called when Secure cannot be
// set. The __Host- prefix requires Secure, and a browser silently drops a
// cookie carrying the prefix without it, so the two have to move together.
const insecureSessionCookieName = "ztax_session"

// readSessionCookie returns the session token from whichever cookie name this
// deployment uses.
//
// It reads both names rather than deriving one from configuration, and that is
// deliberate: the writer picks a name from the Secure flag, and a reader that
// derived the same name independently would be a second place to get the rule
// right. Checking both means flipping ZTAX_SECURE_COOKIES, or rolling a
// deployment that flips it, does not sign everybody out — and an unauthenticated
// request is the failure mode either way, so accepting both costs nothing.
//
// The prefixed name wins when both are present, because it is the one with the
// browser-enforced guarantees behind it.
func readSessionCookie(r *http.Request) string {
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	if c, err := r.Cookie(insecureSessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

// setSessionCookie writes the session cookie.
//
// httpOnly is the whole design: the token is unreachable from JavaScript, which
// is what lets the frontend satisfy ADR-0019 C8 without storing anything. There
// is nothing for a cross-site scripting flaw to read and nothing in
// localStorage to outlive the session.
//
// SameSite=Strict rather than Lax: this is an administrative surface, every
// action on it is consequential, and there is no cross-site flow that needs the
// cookie to travel on a top-level navigation.
func setSessionCookie(w http.ResponseWriter, token string, secure bool, maxAge time.Duration) {
	name := SessionCookieName
	if !secure {
		// Over plain HTTP in local development, keeping the prefix would mean
		// sign-in appearing to succeed while the browser discarded the cookie
		// and every subsequent request was unauthenticated.
		name = insecureSessionCookieName
	}
	// #nosec G124 -- HttpOnly and SameSite=Strict are set unconditionally. Secure
	// is a variable because plain-HTTP local development cannot set it, and
	// config.Load refuses ZTAX_SECURE_COOKIES=false outside development, so it
	// is true in every deployment that is not a laptop. The cookie name changes
	// with it so the __Host- prefix is never sent without Secure.
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(maxAge.Seconds()),
	})
}

// clearSessionCookie expires both cookie names, because a deployment may have
// switched between them.
//
// The expiring cookie carries the same attributes as the one it replaces. That
// is not decoration: a browser matches a Set-Cookie against an existing cookie
// on name, domain and path, and the __Host- prefix additionally requires Secure.
// An expiry that dropped Secure would be rejected outright for the prefixed
// name, leaving the session cookie in place after a sign-out.
func clearSessionCookie(w http.ResponseWriter) {
	for _, name := range []string{SessionCookieName, insecureSessionCookieName} {
		// #nosec G124 -- see setSessionCookie. Secure tracks the name because the
		// __Host- prefix requires it and the unprefixed name is only ever used
		// where TLS is absent; this cookie carries no value in either case.
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   name == SessionCookieName,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   -1,
		})
	}
}

// clientIP extracts the caller's address.
//
// X-Forwarded-For is honoured only when the deployment says it is behind a
// proxy, because an unconditionally trusted header is a header a client can
// forge. It is recorded for the admin session list and is never used for
// authentication or authorization.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			// Leftmost is the original client, as set by the first proxy.
			if i := strings.IndexByte(fwd, ','); i >= 0 {
				return strings.TrimSpace(fwd[:i])
			}
			return strings.TrimSpace(fwd)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
