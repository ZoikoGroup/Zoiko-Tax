package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/outbox"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/webhook"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/secretbox"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Webhooks (W2 lane K): a tenant's administrators subscribe endpoints to the
// cell's events; the outbox relay fans each published event out to the
// subscriptions that asked for its type; the dispatcher delivers, retries and
// dead-letters (internal/domain/webhook has the rules).
//
// Administering webhooks is ADMIN's. It is not a fiscal act — no figure moves
// — and it is an egress decision: which of the tenant's events leave the
// cell, and to where. A role that could also commit could arrange for its
// own decisions to be announced somewhere nobody reviews.

// WebhookService administers subscriptions and reads deliveries.
type WebhookService struct {
	repo          port.WebhookRepository
	box           *secretbox.Box
	tx            port.TxManager
	clock         clock.Clock
	ids           idgen.Generator
	allowInsecure bool
}

// NewWebhookService wires the service. allowInsecure admits http and private
// endpoints, and is development's alone.
func NewWebhookService(repo port.WebhookRepository, box *secretbox.Box, tx port.TxManager, clk clock.Clock,
	ids idgen.Generator, allowInsecure bool) *WebhookService {
	return &WebhookService{repo: repo, box: box, tx: tx, clock: clk, ids: ids, allowInsecure: allowInsecure}
}

// secretBinding is the associated data a sealed secret is bound to.
func secretBinding(tenant id.TenantID, webhookID id.WebhookID, version int) []byte {
	return []byte(fmt.Sprintf("ztax:webhook-secret:%s/%s/%d", tenant, webhookID, version))
}

// issueSecret mints a signing secret and seals it for one version.
func (s *WebhookService) issueSecret(tenant id.TenantID, webhookID id.WebhookID, version int) (string, []byte, error) {
	raw := make([]byte, webhook.SecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, internal(err, "A signing secret could not be issued.")
	}
	sealed, err := s.box.Seal(raw, secretBinding(tenant, webhookID, version))
	if err != nil {
		return "", nil, internal(err, "A signing secret could not be issued.")
	}
	return webhook.EncodeSecret(raw), sealed, nil
}

// CreateWebhookInput is one subscription request.
type CreateWebhookInput struct {
	URL         string
	EventTypes  []string
	Description string
}

// IssuedSecret is a signing secret in clear. It exists only in the response
// that issued it: nothing reads it back afterwards.
type IssuedSecret struct {
	Version int
	Secret  string
}

// Create subscribes an endpoint and issues its first secret.
func (s *WebhookService) Create(ctx context.Context, in CreateWebhookInput) (port.WebhookState, IssuedSecret, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleAdmin)
	if err != nil {
		return port.WebhookState{}, IssuedSecret{}, err
	}
	endpoint, err := webhook.ValidateEndpoint(in.URL, s.allowInsecure)
	if err != nil {
		return port.WebhookState{}, IssuedSecret{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, endpointRefusal(err))
	}
	known := make([]string, 0, len(Events()))
	for _, e := range Events() {
		known = append(known, e.Type)
	}
	types, err := webhook.NormaliseEventTypes(in.EventTypes, known)
	if err != nil {
		return port.WebhookState{}, IssuedSecret{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
			"Name each event type the webhook receives, from those GET /v1/capabilities reports; there is no wildcard.")
	}
	if len(in.Description) > webhook.MaxDescriptionLength {
		return port.WebhookState{}, IssuedSecret{}, errs.Invalid("description", errs.ReasonInvalidValue,
			fmt.Sprintf("A description is at most %d characters.", webhook.MaxDescriptionLength))
	}
	webhookID, err := idgen.WebhookID(s.ids)
	if err != nil {
		return port.WebhookState{}, IssuedSecret{}, internal(err, "The webhook could not be created.")
	}
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	secret, sealed, err := s.issueSecret(sc.Tenant(), webhookID, 1)
	if err != nil {
		return port.WebhookState{}, IssuedSecret{}, err
	}
	sub := webhook.Subscription{
		ID: webhookID, TenantID: sc.Tenant(), URL: endpoint, EventTypes: types, Description: in.Description,
		CreatedAt: now, CreatedBy: sc.Subject(),
	}
	first := webhook.StatusChange{Webhook: webhookID, Seq: 1, Status: webhook.StatusActive, RecordedAt: now, RecordedBy: sc.Subject()}
	sealedSecret := port.SealedSecret{
		SecretVersion: webhook.SecretVersion{Webhook: webhookID, Version: 1, CreatedAt: now, CreatedBy: sc.Subject()},
		Sealed:        sealed,
	}
	if err := s.repo.Create(ctx, sub, first, sealedSecret); err != nil {
		return port.WebhookState{}, IssuedSecret{}, err
	}
	return port.WebhookState{Subscription: sub, Status: first}, IssuedSecret{Version: 1, Secret: secret}, nil
}

// endpointRefusal phrases an endpoint refusal for the caller without echoing
// the URL, which may carry a customer identifier.
func endpointRefusal(err error) string {
	return "The endpoint is refused: " + strings.TrimPrefix(err.Error(), "webhook: ") + "."
}

// Webhooks lists the tenant's subscriptions.
func (s *WebhookService) Webhooks(ctx context.Context, limit int) ([]port.WebhookState, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleAdmin, security.RoleAuditor); err != nil {
		return nil, err
	}
	return s.repo.List(ctx, limit)
}

// Webhook reads one subscription.
func (s *WebhookService) Webhook(ctx context.Context, webhookID id.WebhookID) (port.WebhookState, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleAdmin, security.RoleAuditor); err != nil {
		return port.WebhookState{}, err
	}
	return s.repo.ByID(ctx, webhookID)
}

// SetStatus pauses, resumes or disables a subscription.
func (s *WebhookService) SetStatus(ctx context.Context, webhookID id.WebhookID, to webhook.Status) (port.WebhookState, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleAdmin)
	if err != nil {
		return port.WebhookState{}, err
	}
	if !to.Valid() {
		return port.WebhookState{}, errs.Invalid("status", errs.ReasonInvalidValue, "That is not a webhook status.")
	}
	var out port.WebhookState
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.Lock(ctx, webhookID); err != nil {
			return err
		}
		cur, err := s.repo.ByID(ctx, webhookID)
		if err != nil {
			return err
		}
		if cur.Status.Status == to {
			// Asking for the status it has is a retry, not a move.
			out = cur
			return nil
		}
		if !cur.Status.Status.CanMoveTo(to) {
			return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
				fmt.Sprintf("The webhook is %s and cannot become %s.", cur.Status.Status, to))
		}
		next := webhook.StatusChange{
			Webhook: webhookID, Seq: cur.Status.Seq + 1, Status: to,
			RecordedAt: s.clock.Now().UTC().Truncate(time.Microsecond), RecordedBy: sc.Subject(),
		}
		if err := s.repo.AppendStatus(ctx, next); err != nil {
			return err
		}
		out = port.WebhookState{Subscription: cur.Subscription, Status: next}
		return nil
	})
	return out, err
}

// RotateSecret issues a new signing secret. The previous one keeps signing
// for webhook.RotationOverlap, so a receiver has time to switch.
func (s *WebhookService) RotateSecret(ctx context.Context, webhookID id.WebhookID) (IssuedSecret, time.Time, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleAdmin)
	if err != nil {
		return IssuedSecret{}, time.Time{}, err
	}
	var (
		issued  IssuedSecret
		retires time.Time
	)
	err = s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.Lock(ctx, webhookID); err != nil {
			return err
		}
		cur, err := s.repo.ByID(ctx, webhookID)
		if err != nil {
			return err
		}
		if cur.Status.Status == webhook.StatusDisabled {
			return errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid, "A disabled webhook signs nothing.")
		}
		versions, err := s.repo.Secrets(ctx, webhookID)
		if err != nil {
			return err
		}
		next := len(versions) + 1
		secret, sealed, err := s.issueSecret(sc.Tenant(), webhookID, next)
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC().Truncate(time.Microsecond)
		retires = now.Add(webhook.RotationOverlap)
		if err := s.repo.AppendSecret(ctx, port.SealedSecret{
			SecretVersion: webhook.SecretVersion{Webhook: webhookID, Version: next, CreatedAt: now, CreatedBy: sc.Subject(), RetiresPreviousAt: retires},
			Sealed:        sealed,
		}); err != nil {
			return err
		}
		issued = IssuedSecret{Version: next, Secret: secret}
		return nil
	})
	return issued, retires, err
}

// Deliveries lists a subscription's deliveries, newest first.
func (s *WebhookService) Deliveries(ctx context.Context, webhookID id.WebhookID, status webhook.DeliveryStatus, limit int) ([]webhook.Delivery, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleAdmin, security.RoleAuditor); err != nil {
		return nil, err
	}
	if status != "" && !status.Valid() {
		return nil, errs.Invalid("status", errs.ReasonInvalidValue, "That is not a delivery status.")
	}
	if _, err := s.repo.ByID(ctx, webhookID); err != nil {
		return nil, err
	}
	return s.repo.Deliveries(ctx, webhookID, status, limit)
}

// Delivery reads one delivery of one subscription, with its attempts.
func (s *WebhookService) Delivery(ctx context.Context, webhookID id.WebhookID, deliveryID id.DeliveryID) (webhook.Delivery, []webhook.Attempt, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleAdmin, security.RoleAuditor); err != nil {
		return webhook.Delivery{}, nil, err
	}
	d, attempts, err := s.repo.Delivery(ctx, deliveryID)
	if err != nil {
		return webhook.Delivery{}, nil, err
	}
	if d.Webhook != webhookID {
		// The delivery exists, under another webhook. Saying so would tell
		// the caller something the path did not ask about.
		return webhook.Delivery{}, nil, errs.New(errs.CategoryNotFound, errs.ReasonNotFound, "No such delivery for this webhook.")
	}
	return d, attempts, nil
}

// Replay delivers an event again, as a new delivery of the same bytes. It is
// how a receiver catches up after an outage that outlasted the retries, or
// after a pause. The receiver sees the same message id, which is the point:
// it deduplicates on it.
func (s *WebhookService) Replay(ctx context.Context, webhookID id.WebhookID, deliveryID id.DeliveryID) (webhook.Delivery, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleAdmin)
	if err != nil {
		return webhook.Delivery{}, err
	}
	orig, _, err := s.Delivery(ctx, webhookID, deliveryID)
	if err != nil {
		return webhook.Delivery{}, err
	}
	cur, err := s.repo.ByID(ctx, webhookID)
	if err != nil {
		return webhook.Delivery{}, err
	}
	if cur.Status.Status != webhook.StatusActive {
		return webhook.Delivery{}, errs.New(errs.CategoryConflict, errs.ReasonStateTransitionInvalid,
			fmt.Sprintf("The webhook is %s; resume it before replaying to it.", cur.Status.Status))
	}
	newID, err := idgen.DeliveryID(s.ids)
	if err != nil {
		return webhook.Delivery{}, internal(err, "The replay could not be scheduled.")
	}
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	d := webhook.Delivery{
		ID: newID, TenantID: sc.Tenant(), Webhook: webhookID, EventID: orig.EventID, EventType: orig.EventType,
		Body: orig.Body, Status: webhook.DeliveryPending, NextAttemptAt: now, CreatedAt: now,
		ReplayOf: &orig.ID, ReplayedBy: sc.Subject(),
	}
	if _, err := s.repo.InsertDelivery(ctx, d); err != nil {
		return webhook.Delivery{}, err
	}
	return d, nil
}

func (s *WebhookService) inTx(ctx context.Context, fn func(context.Context) error) error {
	tx, txCtx, err := s.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(txCtx)
}

// ---------------------------------------------------------------------------
// fan-out and dispatch
// ---------------------------------------------------------------------------

// Dispatcher fans published events out to subscriptions and delivers them.
// It is the outbox relay's: one per cell process, serving every tenant, each
// unit of work scoped to its tenant as system work (security.System).
type Dispatcher struct {
	repo   port.WebhookRepository
	box    *secretbox.Box
	sender port.WebhookSender
	tx     port.TxManager
	clock  clock.Clock
	ids    idgen.Generator
}

// NewDispatcher wires the dispatcher.
func NewDispatcher(repo port.WebhookRepository, box *secretbox.Box, sender port.WebhookSender, tx port.TxManager,
	clk clock.Clock, ids idgen.Generator) *Dispatcher {
	return &Dispatcher{repo: repo, box: box, sender: sender, tx: tx, clock: clk, ids: ids}
}

// FanOut schedules one event for every ACTIVE subscription of its tenant that
// receives its type. body is the CloudEvent exactly as every attempt will
// send it. It runs in the caller's transaction — the relay's, which marks the
// event published in the same commit — and is idempotent per event and
// subscription, so a relay that fans out twice schedules once.
func (d *Dispatcher) FanOut(ctx context.Context, e outbox.Event, body []byte) (int, error) {
	ctx = security.Into(ctx, security.System(e.TenantID))
	subs, err := d.repo.Matching(ctx, e.Type)
	if err != nil {
		return 0, err
	}
	now := d.clock.Now().UTC().Truncate(time.Microsecond)
	scheduled := 0
	for _, sub := range subs {
		deliveryID, err := idgen.DeliveryID(d.ids)
		if err != nil {
			return scheduled, internal(err, "A webhook delivery could not be scheduled.")
		}
		inserted, err := d.repo.InsertDelivery(ctx, webhook.Delivery{
			ID: deliveryID, TenantID: e.TenantID, Webhook: sub.ID, EventID: e.ID, EventType: e.Type,
			Body: body, Status: webhook.DeliveryPending, NextAttemptAt: now, CreatedAt: now,
		})
		if err != nil {
			return scheduled, err
		}
		if inserted {
			scheduled++
		}
	}
	return scheduled, nil
}

// DispatchLease is how long a claimed delivery is held before another
// dispatcher may retry it: longer than an attempt can take.
const DispatchLease = 2 * time.Minute

// DispatchResult counts one pass.
type DispatchResult struct {
	Claimed, Delivered, Failed, Dead int
}

// Dispatch makes one attempt at up to limit due deliveries.
//
// The claim commits before any request is sent, so no database lock is held
// across a receiver's response time; the lease is what keeps a second
// dispatcher off the delivery meanwhile. Each attempt and its outcome commit
// together afterwards.
func (d *Dispatcher) Dispatch(ctx context.Context, limit int) (DispatchResult, error) {
	var claimed []webhook.Delivery
	err := d.inTx(ctx, func(ctx context.Context) error {
		var err error
		claimed, err = d.repo.ClaimDue(ctx, d.clock.Now().UTC(), DispatchLease, limit)
		return err
	})
	if err != nil {
		return DispatchResult{}, err
	}
	res := DispatchResult{Claimed: len(claimed)}
	for _, del := range claimed {
		status, err := d.attempt(ctx, del)
		if err != nil {
			return res, err
		}
		switch status {
		case webhook.DeliveryDelivered:
			res.Delivered++
		case webhook.DeliveryDead:
			res.Dead++
		default:
			res.Failed++
		}
	}
	return res, nil
}

// attempt makes one attempt at a claimed delivery and records it.
func (d *Dispatcher) attempt(ctx context.Context, del webhook.Delivery) (webhook.DeliveryStatus, error) {
	ctx = security.Into(ctx, security.System(del.TenantID))
	sub, err := d.repo.ByID(ctx, del.Webhook)
	if err != nil {
		return "", err
	}
	if sub.Status.Status != webhook.StatusActive {
		// Paused or disabled after the event was fanned out: the delivery
		// is dead-lettered, not held. A replay delivers it on purpose later.
		return webhook.DeliveryDead, d.inTx(ctx, func(ctx context.Context) error { return d.repo.Bury(ctx, del.ID) })
	}
	secrets, err := d.signingSecrets(ctx, del)
	if err != nil {
		return "", err
	}

	started := d.clock.Now().UTC()
	headers := map[string]string{
		webhook.HeaderID:        del.EventID.String(),
		webhook.HeaderTimestamp: fmt.Sprint(started.Unix()),
		webhook.HeaderSignature: webhook.SignatureHeader(secrets, del.EventID.String(), started, del.Body),
	}
	code, sendErr := d.sender.Send(ctx, sub.Subscription.URL, headers, del.Body)
	finished := d.clock.Now().UTC()

	a := webhook.Attempt{Delivery: del.ID, N: del.Attempts + 1, StartedAt: started, Duration: finished.Sub(started), StatusCode: code}
	next := del
	next.Attempts = a.N
	switch {
	case sendErr == nil && webhook.Succeeded(code):
		next.Status, next.DeliveredAt, next.NextAttemptAt = webhook.DeliveryDelivered, finished, time.Time{}
	default:
		if sendErr != nil {
			a.Error = sendErr.Error()
		} else {
			a.Error = fmt.Sprintf("the receiver answered %d", code)
		}
		next.Status, next.NextAttemptAt = webhook.AfterFailure(a.N, finished)
	}
	err = d.inTx(ctx, func(ctx context.Context) error { return d.repo.RecordAttempt(ctx, a, next) })
	if errs.ReasonOf(err) == errs.ReasonOptimisticConflict {
		// Another dispatcher settled it after this one's lease lapsed: its
		// record stands, and at-least-once has been honoured twice.
		return next.Status, nil
	}
	return next.Status, err
}

// signingSecrets opens the versions that sign now, newest first.
func (d *Dispatcher) signingSecrets(ctx context.Context, del webhook.Delivery) ([][]byte, error) {
	sealed, err := d.repo.Secrets(ctx, del.Webhook)
	if err != nil {
		return nil, err
	}
	versions := make([]webhook.SecretVersion, len(sealed))
	for i, s := range sealed {
		versions[i] = s.SecretVersion
	}
	var out [][]byte
	for _, v := range webhook.Signing(versions, d.clock.Now().UTC()) {
		i := slices.IndexFunc(sealed, func(s port.SealedSecret) bool { return s.Version == v })
		raw, err := d.box.Open(sealed[i].Sealed, secretBinding(del.TenantID, del.Webhook, v))
		if err != nil {
			return nil, errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal,
				"A webhook signing secret does not open under this cell's key.")
		}
		out = append(out, raw)
	}
	if len(out) == 0 {
		return nil, errs.New(errs.CategoryInternal, errs.ReasonInternal, "A webhook has no signing secret.")
	}
	return out, nil
}

func (d *Dispatcher) inTx(ctx context.Context, fn func(context.Context) error) error {
	tx, txCtx, err := d.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	if err := fn(txCtx); err != nil {
		return err
	}
	return tx.Commit(txCtx)
}
