package http

import (
	"context"
	"net/http"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/identity"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// mailUnavailable answers 503 for a cell that sends no mail.
func (rt *Router) mailUnavailable(w http.ResponseWriter, r *http.Request, configured bool) bool {
	if configured {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"This cell is not configured to send email. The request was not applied."))
	return true
}

// sendLater sends m off the request path, for a message whose fate the
// response does not report. The request's context is detached from its
// cancellation, because the response is about to end it; a failure is logged.
func (rt *Router) sendLater(r *http.Request, m port.Message, what string) {
	ctx := context.WithoutCancel(r.Context())
	go func() {
		if err := rt.Credentials.Deliver(ctx, m); err != nil {
			rt.log.WarnContext(ctx, what+" email not sent", "error", err)
		}
	}()
}

func (rt *Router) handleSendInvitation(w http.ResponseWriter, r *http.Request) {
	if rt.mailUnavailable(w, r, rt.Credentials != nil) {
		return
	}
	userID, err := id.ParseUserID(r.PathValue("userId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("userId", errs.ReasonInvalidValue, "That is not a valid user identifier."))
		return
	}
	if err := rt.Credentials.Invite(r.Context(), userID); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (rt *Router) handleRequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	if rt.mailUnavailable(w, r, rt.Credentials != nil) {
		return
	}
	var req gen.PasswordResetRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if req.Tenant == "" || req.Email == "" {
		writeProblem(w, r, rt.log, errs.Invalid("tenant", errs.ReasonMissingField,
			"A tenant and an email address are required."))
		return
	}
	if rt.refuseIfLimited(w, r, rt.limits.resetByClient, clientIP(r, rt.TrustProxy)) ||
		rt.refuseIfLimited(w, r, rt.limits.resetByAccount, req.Tenant+"\x00"+identity.NormalizeEmail(req.Email)) {
		return
	}

	msg, err := rt.Credentials.RequestReset(r.Context(), req.Tenant, req.Email)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if msg != nil {
		// Off the request path, so the mail server's round trip does not tell
		// the caller the account exists.
		rt.sendLater(r, *msg, "password reset")
	}
	w.WriteHeader(http.StatusAccepted)
}

func (rt *Router) handleSetPasswordWithLink(w http.ResponseWriter, r *http.Request) {
	if rt.mailUnavailable(w, r, rt.Credentials != nil) {
		return
	}
	var req gen.SetPasswordWithLinkRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if req.Token == "" || req.Password == "" {
		writeProblem(w, r, rt.log, errs.Invalid("token", errs.ReasonMissingField,
			"A link token and a password are required."))
		return
	}
	if rt.refuseIfLimited(w, r, rt.limits.linkByClient, clientIP(r, rt.TrustProxy)) {
		return
	}
	result, err := rt.Credentials.SetPassword(r.Context(), req.Token, req.Password)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if result.Welcome != nil {
		rt.sendLater(r, *result.Welcome, "welcome")
	}
	writeJSON(w, r, rt.log, http.StatusOK, gen.PasswordSet{Tenant: result.TenantSlug, Email: result.Email})
}

func (rt *Router) handleSubmitContact(w http.ResponseWriter, r *http.Request) {
	if rt.mailUnavailable(w, r, rt.Contact != nil) {
		return
	}
	var req gen.ContactRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	ip := clientIP(r, rt.TrustProxy)
	if rt.refuseIfLimited(w, r, rt.limits.contactByClient, ip) {
		return
	}
	in := app.ContactInput{Name: req.Name, Email: req.Email, Message: req.Message, ClientIP: ip}
	if req.Company != nil {
		in.Company = *req.Company
	}
	if err := rt.Contact.Submit(r.Context(), in); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
