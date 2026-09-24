package http

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The wire types.
//
// They are separate from the domain types on purpose. A domain type serialized
// directly is a domain type whose every future field becomes public API by
// accident — and in this estate that includes fields like a password verifier,
// which must never appear in a response at all.
//
// They are generated from the contract (package gen, ADR-0010 §2.1) rather than
// written here, so a field the contract renames or adds is a compile failure in
// this file instead of a response that silently stops matching the SDKs. What
// stays hand-written is the mapping from the domain, which is the part that
// needs judgement.

func toTenant(t identity.Tenant) gen.Tenant {
	return gen.Tenant{
		ID: t.ID.String(), Slug: t.Slug, DisplayName: t.DisplayName,
		ResidencyRegion: t.ResidencyRegion, Status: gen.TenantStatus(t.Status),
	}
}

func toUser(u identity.User) gen.User {
	roles := make([]gen.Role, 0, len(u.Roles))
	for _, r := range u.Roles {
		roles = append(roles, gen.Role(r))
	}
	return gen.User{
		ID: u.ID.String(), Email: u.Email, DisplayName: u.DisplayName,
		Status: gen.UserStatus(u.Status), Roles: roles,
		CreatedAt: canonical.FormatTime(u.CreatedAt),
	}
}

// optional renders an empty string as an absent field. The contract declares
// these fields optional rather than nullable, so absence is the only way to say
// "not recorded"; an empty string would claim a value was recorded and was empty.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---------------------------------------------------------------------------
// authentication
// ---------------------------------------------------------------------------

func (rt *Router) handleSignIn(w http.ResponseWriter, r *http.Request) {
	var req gen.SignInRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if req.Tenant == "" || req.Email == "" || req.Password == "" {
		writeProblem(w, r, rt.log, errs.Invalid("tenant", errs.ReasonMissingField,
			"A tenant, an email address and a password are required."))
		return
	}

	result, err := rt.auth.SignIn(r.Context(), app.SignInInput{
		TenantSlug: req.Tenant,
		Email:      req.Email,
		Password:   req.Password,
		UserAgent:  r.UserAgent(),
		ClientIP:   clientIP(r, rt.TrustProxy),
	})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}

	// The cookie's lifetime matches the session's absolute limit, so a browser
	// stops sending a cookie that the server would refuse anyway.
	setSessionCookie(w, result.Token.Cookie(), rt.SecureCookies, identity.AbsoluteTimeout)

	writeJSON(w, r, rt.log, http.StatusOK, gen.Session{
		Tenant:    toTenant(result.Tenant),
		User:      toUser(result.User),
		ExpiresAt: canonical.FormatTime(result.Session.AbsoluteExpiresAt),
	})
}

func (rt *Router) handleSignOut(w http.ResponseWriter, r *http.Request) {
	if err := rt.auth.SignOut(r.Context()); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleSession is what the client calls on load to discover whether it is
// signed in, and as whom. It is the only way the frontend learns its own
// identity — nothing is stored in the browser (ADR-0019 C8).
func (rt *Router) handleSession(w http.ResponseWriter, r *http.Request) {
	sc, _ := security.From(r.Context())
	tenant, err := rt.admin.CurrentTenant(r.Context())
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	user, err := rt.users.ByID(r.Context(), sc.Subject())
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, gen.Session{
		Tenant:    toTenant(tenant),
		User:      toUser(user),
		ExpiresAt: canonical.FormatTime(sc.AuthenticatedAt().Add(identity.AbsoluteTimeout)),
	})
}

func (rt *Router) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req gen.ChangePasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if err := rt.auth.ChangePassword(r.Context(), req.CurrentPassword, req.NewPassword); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	// Every session was revoked, including this one. Clearing the cookie is
	// what makes the client show a sign-in rather than discovering it on the
	// next request.
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// administration
// ---------------------------------------------------------------------------

func (rt *Router) handleGetTenant(w http.ResponseWriter, r *http.Request) {
	tenant, err := rt.admin.CurrentTenant(r.Context())
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toTenant(tenant))
}

func (rt *Router) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := rt.admin.ListUsers(r.Context(), limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.ListUsers200JSONResponseBody{Users: make([]gen.User, 0, len(users))}
	for _, u := range users {
		out.Users = append(out.Users, toUser(u))
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req gen.CreateUserRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	roles := make([]security.Role, 0, len(req.Roles))
	for _, s := range req.Roles {
		roles = append(roles, security.Role(s))
	}
	var password string
	if req.Password != nil {
		password = *req.Password
	}
	user, err := rt.admin.CreateUser(r.Context(), app.CreateUserInput{
		Email: req.Email, DisplayName: req.DisplayName, Roles: roles, Password: password,
	})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusCreated, toUser(user))
}

func (rt *Router) handleSetUserStatus(w http.ResponseWriter, r *http.Request) {
	userID, err := id.ParseUserID(r.PathValue("userId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("userId", errs.ReasonInvalidValue, "That is not a valid user identifier."))
		return
	}
	var req gen.SetUserStatusRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if err := rt.admin.SetUserStatus(r.Context(), userID, identity.UserStatus(req.Status)); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (rt *Router) handleGrantRole(w http.ResponseWriter, r *http.Request) {
	userID, err := id.ParseUserID(r.PathValue("userId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("userId", errs.ReasonInvalidValue, "That is not a valid user identifier."))
		return
	}
	var req gen.RoleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if err := rt.admin.GrantRole(r.Context(), userID, security.Role(req.Role)); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (rt *Router) handleRevokeRole(w http.ResponseWriter, r *http.Request) {
	userID, err := id.ParseUserID(r.PathValue("userId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("userId", errs.ReasonInvalidValue, "That is not a valid user identifier."))
		return
	}
	if err := rt.admin.RevokeRole(r.Context(), userID, security.Role(r.PathValue("role"))); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (rt *Router) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := rt.admin.ListSessions(r.Context(), limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	sc, _ := security.From(r.Context())
	out := gen.ListSessions200JSONResponseBody{Sessions: make([]gen.SessionSummary, 0, len(sessions))}
	for _, s := range sessions {
		out.Sessions = append(out.Sessions, gen.SessionSummary{
			ID:        s.ID.String(),
			UserID:    s.UserID.String(),
			CreatedAt: canonical.FormatTime(s.CreatedAt),
			ExpiresAt: canonical.FormatTime(s.AbsoluteExpiresAt),
			UserAgent: optional(s.UserAgent),
			ClientIP:  optional(s.ClientIP),
			Revoked:   s.RevokedAt != nil,
			// So the admin screen can mark "this is you" and warn before
			// revoking it, rather than signing the administrator out with no
			// explanation.
			Current: s.ID == sc.Session(),
		})
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	sessionID, err := id.ParseSessionID(r.PathValue("sessionId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("sessionId", errs.ReasonInvalidValue, "That is not a valid session identifier."))
		return
	}
	if err := rt.admin.RevokeSession(r.Context(), sessionID); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (rt *Router) handleListAudit(w http.ResponseWriter, r *http.Request) {
	records, err := rt.admin.ListAudit(r.Context(), limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.ListAudit200JSONResponseBody{Records: make([]gen.AuditRecord, 0, len(records))}
	for _, rec := range records {
		a := gen.AuditRecord{
			ID: rec.ID.String(), Action: rec.Action,
			SubjectType: rec.SubjectType, SubjectID: rec.SubjectID,
			Detail:     string(rec.Detail),
			RecordedAt: canonical.FormatTime(rec.RecordedAt),
		}
		if rec.ActorUserID != nil {
			actor := rec.ActorUserID.String()
			a.ActorUserID = &actor
		}
		out.Records = append(out.Records, a)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

// limitOf reads a page size, defaulting rather than erroring on nonsense. The
// repository caps it again, so this is a convenience and not a control.
func limitOf(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return 100
	}
	return n
}

// mustLogger is a small guard so a zero Router fails loudly rather than
// panicking on a nil logger deep inside a handler.
func mustLogger(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	return l
}
