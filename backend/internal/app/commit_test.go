package app_test

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
)

// The ADR-0013 §5.1 control 2 conformance cases, at the use case: retry after
// success, key reuse with a different body, a concurrent duplicate, and what a
// failure leaves behind.

var eventAt = time.Date(2026, 9, 24, 18, 0, 0, 0, time.UTC)

func commitInput(t testing.TB, key, net string) app.CommitInput {
	t.Helper()
	return app.CommitInput{
		IdempotencyKey: key,
		Determination: app.DetermineInput{
			BusinessKey:  "INV-0001/1",
			EventTime:    eventAt,
			Input:        lineInput(t, net, "3", false),
			Accumulators: readSet(t, "9999.99"),
		},
		Render: func(d evidence.Decision) ([]byte, error) {
			return []byte(`{"id":"` + d.ID.String() + `","vat":"` + d.Result.Emitted["TAX_VAT"].Money.CanonicalString() + `"}`), nil
		},
		RenderFailure: func(err error) app.Response {
			return app.Response{Status: 400, Body: []byte(`{"reason":"` + string(errs.ReasonOf(err)) + `"}`)}
		},
	}
}

func (h *harness) rows() int {
	h.decisions.mu.Lock()
	defer h.decisions.mu.Unlock()
	return len(h.decisions.rows)
}

func TestCommitRetryReplaysTheOriginal(t *testing.T) {
	h := newHarness(t)
	first, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, "k-1", "100.00"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.Status != 201 || first.ResultRef == nil {
		t.Fatalf("first commit: %+v", first)
	}

	h.clock.Advance(time.Minute)
	again, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, "k-1", "100.00"))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Replayed || again.Status != 201 || !bytes.Equal(again.Body, first.Body) || *again.ResultRef != *first.ResultRef {
		t.Fatalf("the retry was not the original response verbatim:\n%s\n%s", first.Body, again.Body)
	}
	if h.rows() != 1 {
		t.Fatalf("a retry created a second decision: %d rows", h.rows())
	}
}

func TestCommitKeyReuseWithADifferentBodyIsRefused(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, "k-1", "100.00")); err != nil {
		t.Fatal(err)
	}
	// A different scale is a different request: "100.0" asserts less
	// precision than "100.00", and the digest must say so (ADR-0011 §2.2).
	for _, net := range []string{"99.00", "100.0"} {
		_, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, "k-1", net))
		if errs.ReasonOf(err) != errs.ReasonIdempotencyKeyReuse {
			t.Fatalf("net %s under a used key: got %v, want IDEMPOTENCY_KEY_REUSE", net, err)
		}
	}
	if h.rows() != 1 {
		t.Fatalf("a reused key executed: %d rows", h.rows())
	}
}

func TestCommitConcurrentDuplicateIsInProgress(t *testing.T) {
	h := newHarness(t)
	entered, release := make(chan struct{}), make(chan struct{})

	blocking := commitInput(t, "k-1", "100.00")
	render := blocking.Render
	blocking.Render = func(d evidence.Decision) ([]byte, error) {
		close(entered)
		<-release
		return render(d)
	}

	var wg sync.WaitGroup
	var firstErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, firstErr = h.svc.Commit(as(security.RoleOperator), blocking)
	}()
	<-entered

	_, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, "k-1", "100.00"))
	close(release)
	wg.Wait()

	if firstErr != nil {
		t.Fatalf("the first commit failed: %v", firstErr)
	}
	if errs.ReasonOf(err) != errs.ReasonRequestInProgress {
		t.Fatalf("a concurrent duplicate: got %v, want REQUEST_IN_PROGRESS", err)
	}
	if h.rows() != 1 {
		t.Fatalf("the duplicate executed: %d rows", h.rows())
	}
}

func TestCommitWithoutAKeyIsRefusedBeforeAnyWork(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, "", "100.00"))
	if errs.ReasonOf(err) != errs.ReasonIdempotencyKeyRequired {
		t.Fatalf("got %v, want IDEMPOTENCY_KEY_REQUIRED", err)
	}
	if h.rows() != 0 || h.idem.count() != 0 {
		t.Fatalf("a keyless commit left %d decisions and %d records", h.rows(), h.idem.count())
	}
}

func TestDeterministicFailureIsRecordedAndReplayed(t *testing.T) {
	h := newHarness(t)
	in := commitInput(t, "k-1", "100.00")
	in.Determination.Accumulators = nil // the pack reads one; this is a validation failure

	first, err := h.svc.Commit(as(security.RoleOperator), in)
	if err != nil {
		t.Fatalf("a deterministic failure is a response, not an error: %v", err)
	}
	if first.Status != 400 || first.ResultRef != nil {
		t.Fatalf("first: %+v", first)
	}
	again, err := h.svc.Commit(as(security.RoleOperator), in)
	if err != nil || !again.Replayed || !bytes.Equal(again.Body, first.Body) {
		t.Fatalf("the failure was not replayed: %+v, %v", again, err)
	}
	if h.rows() != 0 {
		t.Fatalf("a failed commit recorded a decision")
	}
}

func TestTransientFailureReleasesTheKey(t *testing.T) {
	h := newHarness(t)
	b := h.holder.Current()
	h.holder.Publish(nil)

	_, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, "k-1", "100.00"))
	if errs.ReasonOf(err) != errs.ReasonNoContentBundle {
		t.Fatalf("got %v, want NO_CONTENT_BUNDLE", err)
	}
	if h.idem.count() != 0 {
		t.Fatal("a transient failure left the key held; the retry could never succeed")
	}

	h.holder.Publish(b)
	settled, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, "k-1", "100.00"))
	if err != nil || settled.Replayed || settled.Status != 201 {
		t.Fatalf("the retry after content arrived: %+v, %v", settled, err)
	}
}

func TestExpiredKeyBehavesAsANewKey(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, "k-1", "100.00")); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(app.CommitRetention + time.Second)
	settled, err := h.svc.Commit(as(security.RoleOperator), commitInput(t, "k-1", "99.00"))
	if err != nil || settled.Replayed {
		t.Fatalf("an expired key: %+v, %v", settled, err)
	}
	if h.rows() != 2 {
		t.Fatalf("want 2 decisions, got %d", h.rows())
	}
}

func TestCommitAuthorization(t *testing.T) {
	h := newHarness(t)
	for _, role := range []security.Role{security.RoleAnalyst, security.RoleAuditor, security.RoleAdmin} {
		if _, err := h.svc.Commit(as(role), commitInput(t, "k-1", "100.00")); !errs.IsCategory(err, errs.CategoryPolicy) {
			t.Errorf("%s committed: %v", role, err)
		}
	}
	if h.idem.count() != 0 {
		t.Fatal("a refused caller claimed a key")
	}
}

func TestQuoteRecordsNothing(t *testing.T) {
	h := newHarness(t)
	q, err := h.svc.Quote(as(security.RoleAnalyst), app.QuoteInput{
		EventTime: eventAt, Input: lineInput(t, "100.00", "3", false), Accumulators: readSet(t, "9999.99"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if q.Result.Outcome != evidence.OutcomeAdvisory {
		t.Fatalf("quote outcome %s", q.Result.Outcome)
	}
	if h.rows() != 0 || len(h.store.m) != 0 || h.idem.count() != 0 {
		t.Fatal("a quote wrote something")
	}

	// The same input committed produces the same figures: one evaluation path.
	d := h.determine(t, "INV-0001/1")
	for slot, v := range d.Result.Emitted {
		if got := q.Result.Emitted[slot]; got.Money.CanonicalString() != v.Money.CanonicalString() {
			t.Errorf("slot %s: quote %s, commit %s", slot, got.Money.CanonicalString(), v.Money.CanonicalString())
		}
	}
}

func TestQuoteAuthorization(t *testing.T) {
	h := newHarness(t)
	in := app.QuoteInput{EventTime: eventAt, Input: lineInput(t, "100.00", "3", false), Accumulators: readSet(t, "9999.99")}
	for _, role := range []security.Role{security.RoleAuditor, security.RoleAdmin} {
		if _, err := h.svc.Quote(as(role), in); !errs.IsCategory(err, errs.CategoryPolicy) {
			t.Errorf("%s quoted: %v", role, err)
		}
	}
}

func TestRecordedDecisionReadsBackItsResult(t *testing.T) {
	h := newHarness(t)
	d := h.determine(t, "INV-0001/1")

	got, err := h.svc.Decision(as(security.RoleAuditor), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Record.DecisionID != d.ID || len(got.Result.Emitted) != len(d.Result.Emitted) {
		t.Fatalf("read back %+v", got.Record)
	}
	for slot, v := range d.Result.Emitted {
		if got.Result.Emitted[slot].Money.CanonicalString() != v.Money.CanonicalString() {
			t.Errorf("slot %s read back differently", slot)
		}
	}

	h.store.tamper(d.ResultDigest, []byte(`{"outcome":"AUTHORITATIVE"}`))
	if _, err := h.svc.Decision(as(security.RoleAuditor), d.ID); errs.ReasonOf(err) != errs.ReasonEvidenceIntegrity {
		t.Fatalf("a tampered result: got %v, want EVIDENCE_INTEGRITY", err)
	}
}
