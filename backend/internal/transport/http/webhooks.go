package http

import (
	"net/http"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/webhook"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The webhook surface (W2 lane K). Administration is ADMIN's; reads are
// ADMIN's and AUDITOR's.

func toWebhook(st port.WebhookState) gen.Webhook {
	sub := st.Subscription
	out := gen.Webhook{
		ID: sub.ID.String(), URL: sub.URL, Status: gen.WebhookStatus(st.Status.Status),
		EventTypes: make([]gen.EventType, len(sub.EventTypes)),
		CreatedAt:  canonical.FormatTime(sub.CreatedAt), StatusChangedAt: canonical.FormatTime(st.Status.RecordedAt),
	}
	for i, t := range sub.EventTypes {
		out.EventTypes[i] = gen.EventType(t)
	}
	if sub.Description != "" {
		d := sub.Description
		out.Description = &d
	}
	return out
}

func toSecret(s app.IssuedSecret) gen.WebhookSecret {
	return gen.WebhookSecret{Version: saturate32(s.Version), Secret: s.Secret}
}

func toDelivery(d webhook.Delivery) gen.WebhookDelivery {
	out := gen.WebhookDelivery{
		ID: d.ID.String(), WebhookID: d.Webhook.String(), EventID: d.EventID.String(),
		EventType: gen.EventType(d.EventType), Status: gen.DeliveryStatus(d.Status),
		Attempts: saturate32(d.Attempts), CreatedAt: canonical.FormatTime(d.CreatedAt),
	}
	if !d.NextAttemptAt.IsZero() && d.Status == webhook.DeliveryPending {
		t := canonical.FormatTime(d.NextAttemptAt)
		out.NextAttemptAt = &t
	}
	if !d.DeliveredAt.IsZero() {
		t := canonical.FormatTime(d.DeliveredAt)
		out.DeliveredAt = &t
	}
	if d.ReplayOf != nil {
		r := d.ReplayOf.String()
		out.ReplayOf = &r
	}
	return out
}

func (rt *Router) webhooksUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Webhooks != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonUnavailable,
		"This cell is not configured for webhooks. The request was not applied."))
	return true
}

func (rt *Router) webhookID(w http.ResponseWriter, r *http.Request) (id.WebhookID, bool) {
	webhookID, err := id.ParseWebhookID(r.PathValue("webhookId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("webhookId", errs.ReasonInvalidValue, "That is not a valid webhook identifier."))
		return id.WebhookID{}, false
	}
	return webhookID, true
}

func (rt *Router) deliveryID(w http.ResponseWriter, r *http.Request) (id.DeliveryID, bool) {
	deliveryID, err := id.ParseDeliveryID(r.PathValue("deliveryId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("deliveryId", errs.ReasonInvalidValue, "That is not a valid delivery identifier."))
		return id.DeliveryID{}, false
	}
	return deliveryID, true
}

func (rt *Router) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	if rt.webhooksUnavailable(w, r) {
		return
	}
	states, err := rt.Webhooks.Webhooks(r.Context(), limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.WebhookList{Webhooks: make([]gen.Webhook, len(states))}
	for i, st := range states {
		out.Webhooks[i] = toWebhook(st)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	if rt.webhooksUnavailable(w, r) {
		return
	}
	var req gen.WebhookCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	in := app.CreateWebhookInput{URL: req.URL, EventTypes: make([]string, len(req.EventTypes))}
	for i, t := range req.EventTypes {
		in.EventTypes[i] = string(t)
	}
	if req.Description != nil {
		in.Description = *req.Description
	}
	st, secret, err := rt.Webhooks.Create(r.Context(), in)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	// The one response that carries the secret. It must not be cached by
	// anything between here and the administrator.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, rt.log, http.StatusCreated, gen.WebhookCreated{Webhook: toWebhook(st), Secret: toSecret(secret)})
}

func (rt *Router) handleGetWebhook(w http.ResponseWriter, r *http.Request) {
	if rt.webhooksUnavailable(w, r) {
		return
	}
	webhookID, ok := rt.webhookID(w, r)
	if !ok {
		return
	}
	st, err := rt.Webhooks.Webhook(r.Context(), webhookID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toWebhook(st))
}

func (rt *Router) handleSetWebhookStatus(w http.ResponseWriter, r *http.Request) {
	if rt.webhooksUnavailable(w, r) {
		return
	}
	webhookID, ok := rt.webhookID(w, r)
	if !ok {
		return
	}
	var req gen.WebhookStatusRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	st, err := rt.Webhooks.SetStatus(r.Context(), webhookID, webhook.Status(req.Status))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toWebhook(st))
}

func (rt *Router) handleRotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	if rt.webhooksUnavailable(w, r) {
		return
	}
	webhookID, ok := rt.webhookID(w, r)
	if !ok {
		return
	}
	secret, retires, err := rt.Webhooks.RotateSecret(r.Context(), webhookID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, rt.log, http.StatusOK, gen.WebhookSecretRotation{
		Secret: toSecret(secret), PreviousRetiresAt: canonical.FormatTime(retires),
	})
}

func (rt *Router) handleListWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	if rt.webhooksUnavailable(w, r) {
		return
	}
	webhookID, ok := rt.webhookID(w, r)
	if !ok {
		return
	}
	status := webhook.DeliveryStatus(r.URL.Query().Get("status"))
	if status != "" && !status.Valid() {
		writeProblem(w, r, rt.log, errs.Invalid("status", errs.ReasonInvalidValue, "That is not a delivery status."))
		return
	}
	deliveries, err := rt.Webhooks.Deliveries(r.Context(), webhookID, status, limitOf(r))
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.WebhookDeliveryList{Deliveries: make([]gen.WebhookDelivery, len(deliveries))}
	for i, d := range deliveries {
		out.Deliveries[i] = toDelivery(d)
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleGetWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	if rt.webhooksUnavailable(w, r) {
		return
	}
	webhookID, ok := rt.webhookID(w, r)
	if !ok {
		return
	}
	deliveryID, ok := rt.deliveryID(w, r)
	if !ok {
		return
	}
	d, attempts, err := rt.Webhooks.Delivery(r.Context(), webhookID, deliveryID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	out := gen.WebhookDeliveryDetail{Delivery: toDelivery(d), Attempts: make([]gen.WebhookAttempt, len(attempts))}
	for i, a := range attempts {
		ga := gen.WebhookAttempt{
			Attempt: saturate32(a.N), StartedAt: canonical.FormatTime(a.StartedAt),
			DurationMs: saturate32(int(a.Duration.Milliseconds())),
		}
		if a.StatusCode != 0 {
			code := saturate32(a.StatusCode)
			ga.StatusCode = &code
		}
		if a.Error != "" {
			e := a.Error
			ga.Error = &e
		}
		out.Attempts[i] = ga
	}
	writeJSON(w, r, rt.log, http.StatusOK, out)
}

func (rt *Router) handleReplayWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	if rt.webhooksUnavailable(w, r) {
		return
	}
	webhookID, ok := rt.webhookID(w, r)
	if !ok {
		return
	}
	deliveryID, ok := rt.deliveryID(w, r)
	if !ok {
		return
	}
	d, err := rt.Webhooks.Replay(r.Context(), webhookID, deliveryID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusAccepted, toDelivery(d))
}
