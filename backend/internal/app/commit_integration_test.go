//go:build integration

package app_test

import (
	"bytes"
	"sync"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
)

// ADR-0013 §5.1 control 2 against PostgreSQL. The unit tests prove the
// sequencing; these prove the two things only the database can: that the
// primary key refuses a concurrent duplicate, and that a stored response comes
// back byte for byte.

func (c *cell) guarded() *app.DeterminationService {
	return c.decisions.WithIdempotency(app.NewIdempotency(c.store.Idempotency(), c.store, c.clock))
}

func TestIntegrationCommitRetryIsVerbatim(t *testing.T) {
	c := openCell(t)
	svc := c.guarded()

	first, err := svc.Commit(c.ctx, commitInput(t, "k-verbatim", "100.00"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Commit(c.ctx, commitInput(t, "k-verbatim", "100.00"))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Replayed || !bytes.Equal(first.Body, again.Body) {
		t.Fatalf("the stored response did not survive the database:\n%s\n%s", first.Body, again.Body)
	}
	history, err := c.store.Decisions().History(c.ctx, "INV-0001/1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].DecisionID != *first.ResultRef {
		t.Fatalf("want exactly the first decision, got %d", len(history))
	}
}

func TestIntegrationConcurrentDuplicatesCommitOnce(t *testing.T) {
	c := openCell(t)
	svc := c.guarded()

	const callers = 8
	var (
		wg                       sync.WaitGroup
		mu                       sync.Mutex
		executed, replayed, busy int
		unexpected               []error
	)
	start := make(chan struct{})
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			settled, err := svc.Commit(c.ctx, commitInput(t, "k-race", "100.00"))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && !settled.Replayed:
				executed++
			case err == nil:
				replayed++
			case errs.ReasonOf(err) == errs.ReasonRequestInProgress:
				busy++
			default:
				unexpected = append(unexpected, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(unexpected) > 0 {
		t.Fatalf("unexpected failures: %v", unexpected)
	}
	if executed != 1 || executed+replayed+busy != callers {
		t.Fatalf("executed %d, replayed %d, in progress %d; exactly one may execute", executed, replayed, busy)
	}
	history, err := c.store.Decisions().History(c.ctx, "INV-0001/1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("%d concurrent retries recorded %d decisions", callers, len(history))
	}
}

func TestIntegrationDeterministicFailureIsReplayed(t *testing.T) {
	c := openCell(t)
	svc := c.guarded()
	in := commitInput(t, "k-fail", "100.00")
	in.Determination.Accumulators = nil

	first, err := svc.Commit(c.ctx, in)
	if err != nil || first.Status != 400 {
		t.Fatalf("first: %+v, %v", first, err)
	}
	again, err := svc.Commit(c.ctx, in)
	if err != nil || !again.Replayed || !bytes.Equal(first.Body, again.Body) {
		t.Fatalf("the failure was not replayed: %+v, %v", again, err)
	}
}
