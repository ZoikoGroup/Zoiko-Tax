//go:build integration

package app_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/batch"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
)

// Batches against PostgreSQL: submitted once, executed in order on the
// single-commit path, refused items recorded without stopping the job, and a
// worker that died mid-job resumed without committing anything twice.

func (c *fiscalCell) batches() *app.BatchService {
	return app.NewBatchService(c.store.Batches(), c.svc, app.NewIdempotency(c.store.Idempotency(), c.store, c.clock), c.clock, idgen.V7{})
}

func (c *fiscalCell) line(t *testing.T, businessKey, net string, supersedes *id.DecisionID) app.DetermineInput {
	t.Helper()
	return app.DetermineInput{BusinessKey: businessKey, Supersedes: supersedes, EventTime: c.event, Input: lineInput(t, net, "3", false)}
}

func (c *fiscalCell) submit(t *testing.T, svc *app.BatchService, key string, items ...app.DetermineInput) (app.Settled, id.JobID) {
	t.Helper()
	var jobID id.JobID
	s, err := svc.Submit(c.ctx, app.SubmitInput{
		IdempotencyKey: key, Items: items,
		Render: func(v app.JobView) ([]byte, error) { return []byte(v.Job.ID.String()), nil },
		RenderFailure: func(err error) app.Response {
			return app.Response{Status: 400, Body: []byte(errs.ReasonOf(err))}
		},
	})
	if err != nil || s.Status != 202 {
		t.Fatalf("submit %s: %v %d %s", key, err, s.Status, s.Body)
	}
	jobID, err = id.ParseJobID(string(s.Body))
	if err != nil {
		t.Fatal(err)
	}
	return s, jobID
}

// drain runs the worker until no job in the cell is left. Other tests' jobs
// may be among them; this one's is what is asserted.
func drain(t *testing.T, c *fiscalCell, svc *app.BatchService) {
	t.Helper()
	for range 100 {
		found, err := svc.Work(c.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			return
		}
	}
	t.Fatal("the worker never ran out of jobs")
}

func TestIntegrationBatchCommitsInOrderAndRecordsRefusals(t *testing.T) {
	c := openFiscalCell(t)
	svc := c.batches()
	missing := id.NewDecisionID(uuid.Must(uuid.NewV7()))
	first, jobID := c.submit(t, svc, "b-1",
		c.line(t, "INV-B1/1", "100.00", nil),
		c.line(t, "INV-B1/2", "50.00", &missing), // corrects a decision that does not exist
		c.line(t, "INV-B1/3", "40.00", nil),
	)
	// The same key and body is the same job.
	again, sameJob := c.submit(t, svc, "b-1",
		c.line(t, "INV-B1/1", "100.00", nil),
		c.line(t, "INV-B1/2", "50.00", &missing),
		c.line(t, "INV-B1/3", "40.00", nil),
	)
	if !again.Replayed || sameJob != jobID || string(again.Body) != string(first.Body) {
		t.Fatalf("a retried submission: replayed=%v %s vs %s", again.Replayed, sameJob, jobID)
	}

	queued, err := svc.Job(c.ctx, jobID)
	if err != nil || queued.Job.Status != batch.StatusQueued {
		t.Fatalf("queued: %v %+v", err, queued.Job)
	}
	drain(t, c, svc)

	done, err := svc.Job(c.ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	succeeded, failed, pending := done.Counts()
	if done.Job.Status != batch.StatusCompleted || succeeded != 2 || failed != 1 || pending != 0 || done.Job.FinishedAt.IsZero() {
		t.Fatalf("completed job %+v: %d/%d/%d", done.Job, succeeded, failed, pending)
	}
	if r := done.Items[1].Result; r.Status != batch.ItemFailed || r.Reason != errs.ReasonNotFound {
		t.Fatalf("the refused item: %+v", r)
	}
	// A succeeded item's decision is a decision like any other.
	for _, i := range []int{0, 2} {
		r := done.Items[i].Result
		if r == nil || r.Decision == nil {
			t.Fatalf("item %d: %+v", i, r)
		}
		d, err := c.svc.Decision(c.ctx, *r.Decision)
		if err != nil || d.Record.BusinessKey != done.Items[i].BusinessKey {
			t.Fatalf("item %d's decision: %v %+v", i, err, d.Record)
		}
		if report, err := c.svc.Replay(c.ctx, *r.Decision); err != nil || report.Verdict != evidence.ReplayMatch {
			t.Fatalf("item %d does not replay: %v %+v", i, err, report)
		}
	}
}

func TestIntegrationBatchWorkerResumesWithoutCommittingTwice(t *testing.T) {
	c := openFiscalCell(t)
	svc := c.batches()
	_, jobID := c.submit(t, svc, "b-2",
		c.line(t, "INV-B2/1", "100.00", nil),
		c.line(t, "INV-B2/2", "100.00", nil),
	)

	// A worker committed item 0 and died before recording its result: the
	// commit went through under the item's own key and scope.
	died, err := c.svc.Commit(c.ctx, app.CommitInput{
		IdempotencyKey: batch.ItemKey(jobID, 0), Scope: app.BatchItemScope,
		Determination: c.line(t, "INV-B2/1", "100.00", nil),
		Render:        func(d evidence.Decision) ([]byte, error) { return []byte(d.ID.String()), nil },
		RenderFailure: func(err error) app.Response { return app.Response{Status: 400} },
	})
	if err != nil || died.ResultRef == nil {
		t.Fatalf("the dead worker's commit: %v", err)
	}

	drain(t, c, svc)
	done, err := svc.Job(c.ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if r := done.Items[0].Result; r == nil || r.Decision == nil || *r.Decision != *died.ResultRef {
		t.Fatalf("item 0 resumed as %+v; the dead worker committed %s", r, died.ResultRef)
	}
	history, err := c.store.Decisions().History(c.ctx, "INV-B2/1")
	if err != nil || len(history) != 1 {
		t.Fatalf("INV-B2/1 has %d decisions, want 1: %v", len(history), err)
	}

	// The item key is scoped apart from :commit: a client using the same
	// string as its own commit key gets a commit of its own.
	own, err := c.commit(t, batch.ItemKey(jobID, 1), "INV-B2/own", "10.00", "3", nil)
	if err != nil || own.Replayed || own.Status != 201 {
		t.Fatalf("a client's commit under a batch item's key string: %v replayed=%v %d", err, own.Replayed, own.Status)
	}
}
