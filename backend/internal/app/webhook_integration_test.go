//go:build integration

package app_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	adapterwebhook "github.com/zoikogroup/zoikotax/backend/internal/adapter/webhook"
	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/webhook"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/secretbox"
)

// Webhooks end to end against PostgreSQL and a real HTTP receiver: a commit's
// event is fanned out the way the relay does it, dispatched, signed, retried,
// dead-lettered, replayed, and refused at a private address.

// receiver is an HTTP endpoint that records what it is sent and answers with
// whatever status the test sets.
type receiver struct {
	srv    *httptest.Server
	mu     sync.Mutex
	status int
	got    []received
}

type received struct {
	header http.Header
	body   []byte
}

func newReceiver(t *testing.T) *receiver {
	r := &receiver{status: http.StatusNoContent}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.got = append(r.got, received{req.Header.Clone(), body})
		status := r.status
		r.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) answer(status int) { r.mu.Lock(); r.status = status; r.mu.Unlock() }

func (r *receiver) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.got) }

func (r *receiver) last() received { r.mu.Lock(); defer r.mu.Unlock(); return r.got[len(r.got)-1] }

type webhookCell struct {
	*fiscalCell
	admin context.Context
	svc   *app.WebhookService
	disp  *app.Dispatcher
}

func openWebhookCell(t *testing.T, allowPrivate bool) *webhookCell {
	c := openFiscalCell(t)
	box, err := secretbox.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, secretbox.KeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	admin := security.New(c.tenant, id.NewUserID(uuid.Must(uuid.NewV7())), id.NewSessionID(uuid.Must(uuid.NewV7())),
		[]security.Role{security.RoleAdmin}, time.Now())
	quiesceDeliveries(t, c)
	return &webhookCell{
		fiscalCell: c,
		admin:      security.Into(c.ctx, admin),
		// The administration service admits a loopback endpoint so the test
		// can subscribe its receiver; the sender's guard is what is under
		// test for egress, separately.
		svc:  app.NewWebhookService(c.store.Webhooks(), box, c.store, c.clock, idgen.V7{}, true),
		disp: app.NewDispatcher(c.store.Webhooks(), box, adapterwebhook.NewSender(allowPrivate), c.store, c.clock, idgen.V7{}),
	}
}

// quiesceDeliveries dead-letters every pending delivery in the cell before a
// test dispatches. The dispatcher serves the whole cell, as it must, so a
// delivery an earlier test's tenant left scheduled would otherwise be claimed
// by this one's pass and miscounted. The test database connects as the
// schema owner, which may do this; the application role may not.
func quiesceDeliveries(t *testing.T, c *fiscalCell) {
	t.Helper()
	if _, err := c.store.Pool().Exec(c.ctx,
		`UPDATE ztax.webhook_delivery SET status = 'DEAD', next_attempt_at = NULL WHERE status = 'PENDING'`); err != nil {
		t.Fatal(err)
	}
}

// relay does what ztax-outbox-relay does with this tenant's unpublished
// events: renders each CloudEvent once, fans it out, and marks it published,
// in one transaction.
func (c *webhookCell) relay(t *testing.T) {
	t.Helper()
	tx, txCtx, err := c.store.Begin(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	events, err := c.store.Outbox().Claim(txCtx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		body, err := canonical.Encode(e.CloudEvent("local-dev", "local", map[string]string{"app": "test"}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.disp.FanOut(txCtx, e, body); err != nil {
			t.Fatal(err)
		}
		if err := c.store.Outbox().MarkPublished(txCtx, e.ID, c.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(txCtx); err != nil {
		t.Fatal(err)
	}
}

func (c *webhookCell) dispatch(t *testing.T) app.DispatchResult {
	t.Helper()
	res, err := c.disp.Dispatch(c.ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (c *webhookCell) subscribe(t *testing.T, url string, types ...string) (id.WebhookID, []byte) {
	t.Helper()
	st, secret, err := c.svc.Create(c.admin, app.CreateWebhookInput{URL: url, EventTypes: types})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := webhook.DecodeSecret(secret.Secret)
	if err != nil {
		t.Fatal(err)
	}
	return st.Subscription.ID, raw
}

func verify(t *testing.T, got received, secret []byte) {
	t.Helper()
	ts, err := strconv.ParseInt(got.header.Get(webhook.HeaderTimestamp), 10, 64)
	if err != nil {
		t.Fatalf("timestamp header %q", got.header.Get(webhook.HeaderTimestamp))
	}
	if !webhook.Verify(secret, got.header.Get(webhook.HeaderID), time.Unix(ts, 0), got.body, got.header.Get(webhook.HeaderSignature)) {
		t.Fatalf("the delivery does not verify under the subscription's secret: %v", got.header)
	}
}

func TestIntegrationWebhookDeliversSignedEventsOnceEach(t *testing.T) {
	c := openWebhookCell(t, true)
	rcv := newReceiver(t)
	webhookID, secret := c.subscribe(t, rcv.srv.URL+"/hook", app.EventDecisionCommitted)

	d := c.mustCommit(t, "k-wh1", "INV-WH/1", "100.00", "3", nil)
	c.relay(t)
	c.relay(t) // a second relay pass schedules nothing new
	if res := c.dispatch(t); res.Delivered != 1 || res.Claimed != 1 {
		t.Fatalf("dispatch %+v", res)
	}
	if rcv.count() != 1 {
		t.Fatalf("%d requests received, want 1: only the committed event was subscribed", rcv.count())
	}
	got := rcv.last()
	verify(t, got, secret)
	var ce map[string]any
	if err := json.Unmarshal(got.body, &ce); err != nil {
		t.Fatal(err)
	}
	data, _ := ce["data"].(map[string]any)
	if ce["type"] != app.EventDecisionCommitted || ce["id"] != got.header.Get(webhook.HeaderID) || data["decisionId"] != d.String() {
		t.Fatalf("delivered %v", ce)
	}
	if _, has := data["amount"]; has {
		t.Fatal("a decision event carried an amount")
	}

	deliveries, err := c.svc.Deliveries(c.admin, webhookID, "", 0)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != webhook.DeliveryDelivered || deliveries[0].Attempts != 1 {
		t.Fatalf("deliveries %v %+v", err, deliveries)
	}
	if res := c.dispatch(t); res.Claimed != 0 {
		t.Fatalf("a delivered delivery was claimed again: %+v", res)
	}
}

func TestIntegrationWebhookRetriesThenDeadLettersThenReplays(t *testing.T) {
	c := openWebhookCell(t, true)
	rcv := newReceiver(t)
	rcv.answer(http.StatusServiceUnavailable)
	webhookID, secret := c.subscribe(t, rcv.srv.URL+"/hook", app.EventDecisionCommitted)
	c.mustCommit(t, "k-wh2", "INV-WH2/1", "100.00", "3", nil)
	c.relay(t)

	for n := 1; n <= webhook.MaxAttempts; n++ {
		res := c.dispatch(t)
		if res.Claimed != 1 {
			t.Fatalf("attempt %d claimed %+v", n, res)
		}
		// Not due again until its backoff has passed.
		if again := c.dispatch(t); again.Claimed != 0 {
			t.Fatalf("attempt %d: claimed again before its backoff", n)
		}
		c.clock.Advance(7 * time.Hour)
	}
	ds, err := c.svc.Deliveries(c.admin, webhookID, webhook.DeliveryDead, 0)
	if err != nil || len(ds) != 1 || ds[0].Attempts != webhook.MaxAttempts {
		t.Fatalf("dead letters %v %+v", err, ds)
	}
	_, attempts, err := c.svc.Delivery(c.admin, webhookID, ds[0].ID)
	if err != nil || len(attempts) != webhook.MaxAttempts || attempts[0].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("attempts %v %+v", err, attempts)
	}

	// The receiver recovers; a replay delivers the same event, same id.
	rcv.answer(http.StatusOK)
	replay, err := c.svc.Replay(c.admin, webhookID, ds[0].ID)
	if err != nil || replay.ReplayOf == nil || *replay.ReplayOf != ds[0].ID {
		t.Fatalf("replay %v %+v", err, replay)
	}
	if res := c.dispatch(t); res.Delivered != 1 {
		t.Fatalf("replay dispatch %+v", res)
	}
	got := rcv.last()
	verify(t, got, secret)
	if got.header.Get(webhook.HeaderID) != ds[0].EventID.String() {
		t.Fatal("a replay was sent under a new message id; the receiver could not deduplicate it")
	}
}

func TestIntegrationWebhookRotationAndPause(t *testing.T) {
	c := openWebhookCell(t, true)
	rcv := newReceiver(t)
	webhookID, oldSecret := c.subscribe(t, rcv.srv.URL+"/hook", app.EventDecisionCommitted, app.EventObligationStatusChanged)

	issued, retires, err := c.svc.RotateSecret(c.admin, webhookID)
	if err != nil || issued.Version != 2 || !retires.After(c.clock.Now()) {
		t.Fatalf("rotate: %v %+v %s", err, issued, retires)
	}
	newSecret, _ := webhook.DecodeSecret(issued.Secret)

	c.mustCommit(t, "k-wh3", "INV-WH3/1", "100.00", "3", nil)
	c.relay(t)
	if res := c.dispatch(t); res.Delivered != 2 {
		t.Fatalf("dispatch %+v: want the decision and the obligation it opened", res)
	}
	// During the overlap each delivery verifies under both secrets.
	verify(t, rcv.last(), oldSecret)
	verify(t, rcv.last(), newSecret)

	// After it, only under the new one.
	c.clock.Advance(webhook.RotationOverlap + time.Minute)
	c.mustCommit(t, "k-wh4", "INV-WH4/1", "100.00", "3", nil)
	c.relay(t)
	if res := c.dispatch(t); res.Delivered != 1 {
		t.Fatalf("dispatch after the overlap %+v", res)
	}
	verify(t, rcv.last(), newSecret)
	ts, _ := strconv.ParseInt(rcv.last().header.Get(webhook.HeaderTimestamp), 10, 64)
	if webhook.Verify(oldSecret, rcv.last().header.Get(webhook.HeaderID), time.Unix(ts, 0), rcv.last().body, rcv.last().header.Get(webhook.HeaderSignature)) {
		t.Fatal("the retired secret still verifies")
	}

	// Paused: a scheduled delivery is dead-lettered and nothing new is
	// fanned out to it.
	c.mustCommit(t, "k-wh5", "INV-WH5/1", "100.00", "3", nil)
	c.relay(t)
	if _, err := c.svc.SetStatus(c.admin, webhookID, webhook.StatusPaused); err != nil {
		t.Fatal(err)
	}
	before := rcv.count()
	if res := c.dispatch(t); res.Dead != 1 || rcv.count() != before {
		t.Fatalf("dispatch while paused %+v, %d new requests", res, rcv.count()-before)
	}
	c.mustCommit(t, "k-wh6", "INV-WH6/1", "100.00", "3", nil)
	c.relay(t)
	if res := c.dispatch(t); res.Claimed != 0 {
		t.Fatalf("a paused webhook was fanned out to: %+v", res)
	}
	// A replay to a paused webhook is refused until it resumes.
	dead, _ := c.svc.Deliveries(c.admin, webhookID, webhook.DeliveryDead, 0)
	if _, err := c.svc.Replay(c.admin, webhookID, dead[0].ID); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("a replay to a paused webhook: %v", err)
	}
	if _, err := c.svc.SetStatus(c.admin, webhookID, webhook.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if _, err := c.svc.SetStatus(c.admin, webhookID, webhook.StatusActive); errs.ReasonOf(err) != errs.ReasonStateTransitionInvalid {
		t.Fatalf("a disabled webhook was reactivated: %v", err)
	}
}

func TestIntegrationWebhookEgressGuardRefusesTheCellsOwnNetwork(t *testing.T) {
	c := openWebhookCell(t, false) // production's sender
	rcv := newReceiver(t)
	webhookID, _ := c.subscribe(t, rcv.srv.URL+"/hook", app.EventDecisionCommitted)

	c.mustCommit(t, "k-wh7", "INV-WH7/1", "100.00", "3", nil)
	c.relay(t)
	if res := c.dispatch(t); res.Failed != 1 {
		t.Fatalf("dispatch %+v", res)
	}
	if rcv.count() != 0 {
		t.Fatal("a delivery reached a loopback address")
	}
	ds, _ := c.svc.Deliveries(c.admin, webhookID, "", 0)
	_, attempts, err := c.svc.Delivery(c.admin, webhookID, ds[0].ID)
	if err != nil || len(attempts) != 1 || attempts[0].StatusCode != 0 || attempts[0].Error == "" {
		t.Fatalf("the refused attempt: %v %+v", err, attempts)
	}

	// And the administration surface refuses a private literal outright when
	// it is not development's.
	strict := app.NewWebhookService(c.store.Webhooks(), nil, c.store, c.clock, idgen.V7{}, false)
	for _, u := range []string{"https://169.254.169.254/latest", "https://10.0.0.5/hook", "http://hooks.example.com/"} {
		if _, _, err := strict.Create(c.admin, app.CreateWebhookInput{URL: u, EventTypes: []string{app.EventDecisionCommitted}}); errs.ReasonOf(err) != errs.ReasonInvalidValue {
			t.Errorf("%s: %v", u, err)
		}
	}
	if _, _, err := strict.Create(c.admin, app.CreateWebhookInput{URL: "https://hooks.example.com/", EventTypes: nil}); errs.ReasonOf(err) != errs.ReasonInvalidValue {
		t.Errorf("a subscription to no events: %v", err)
	}
}

func TestIntegrationWebhookAdministrationIsAdmins(t *testing.T) {
	c := openWebhookCell(t, true)
	// c.ctx is an OPERATOR: fiscal, and so not trusted with egress.
	if _, _, err := c.svc.Create(c.ctx, app.CreateWebhookInput{URL: "https://hooks.example.com/", EventTypes: []string{app.EventDecisionCommitted}}); errs.ReasonOf(err) != errs.ReasonForbidden {
		t.Fatalf("an operator created a webhook: %v", err)
	}
}
