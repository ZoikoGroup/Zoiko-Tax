package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/batch"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/evidence"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/idempotency"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/security"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/idgen"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Batches (W2 lane K): POST /v1/batches queues many commits as one job, and a
// worker in the cell executes them in order (internal/domain/batch has the
// rules). The submission is idempotent like a commit; each item is idempotent
// on its own key, so a worker that dies resumes without committing twice.

// BatchEndpoint scopes a submission's idempotency keys.
const BatchEndpoint = "POST /v1/batches"

// BatchItemScope scopes the per-item keys the worker commits under, apart
// from every key a client can choose.
const BatchItemScope = "POST /v1/batches#item"

// BatchLease is how long a worker holds a job between renewals. A worker
// that dies is replaced when it lapses.
const BatchLease = 2 * time.Minute

// BatchService accepts batches and executes them.
type BatchService struct {
	batches     port.BatchRepository
	det         *DeterminationService
	idempotency *Idempotency
	clock       clock.Clock
	ids         idgen.Generator
}

// NewBatchService wires the service over the determination service whose
// commit path every item takes.
func NewBatchService(batches port.BatchRepository, det *DeterminationService, g *Idempotency, clk clock.Clock, ids idgen.Generator) *BatchService {
	return &BatchService{batches: batches, det: det, idempotency: g, clock: clk, ids: ids}
}

// JobItem is one item of a job as read back: its position, its business key,
// and its result once it has one.
type JobItem struct {
	Index       int
	BusinessKey string
	Result      *batch.Result
}

// JobView is a job and its items.
type JobView struct {
	Job   batch.Job
	Items []JobItem
}

// Counts reports how the items have fared so far.
func (v JobView) Counts() (succeeded, failed, pending int) {
	for _, it := range v.Items {
		switch {
		case it.Result == nil:
			pending++
		case it.Result.Status == batch.ItemSucceeded:
			succeeded++
		default:
			failed++
		}
	}
	return succeeded, failed, pending
}

// SubmitInput is one batch submission.
type SubmitInput struct {
	IdempotencyKey string
	Items          []DetermineInput
	Render         func(JobView) ([]byte, error)
	RenderFailure  func(error) Response
}

// Submit queues a batch at most once per idempotency key.
func (s *BatchService) Submit(ctx context.Context, in SubmitInput) (Settled, error) {
	sc, err := requireRoleOrSystem(ctx, security.RoleOperator)
	if err != nil {
		return Settled{}, err
	}
	if len(in.Items) == 0 || len(in.Items) > batch.MaxItems {
		return Settled{}, errs.Invalid("items", errs.ReasonInvalidValue,
			fmt.Sprintf("A batch carries between 1 and %d items.", batch.MaxItems))
	}
	requests := make([][]byte, len(in.Items))
	values := make([]canonical.Value, len(in.Items))
	for i, it := range in.Items {
		if it.EventTime.IsZero() || it.BusinessKey == "" {
			return Settled{}, errs.Invalid(fmt.Sprintf("items[%d]", i), errs.ReasonMissingField,
				"Every item carries a business key and an event time.")
		}
		values[i] = commitCanonical(it)
		if requests[i], err = canonical.Encode(values[i]); err != nil {
			return Settled{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue,
				fmt.Sprintf("Item %d cannot be put in canonical form.", i))
		}
	}
	digest, err := canonical.Sum(canonical.Object(
		canonical.F("operation", canonical.String(string(batch.OperationCommit))),
		canonical.F("items", canonical.Array(values...)),
	))
	if err != nil {
		return Settled{}, errs.Wrap(err, errs.CategoryValidation, errs.ReasonInvalidValue, "The batch cannot be put in canonical form.")
	}
	return s.idempotency.Do(ctx, Call{
		Key:       idempotency.Key{TenantID: sc.Tenant(), Endpoint: BatchEndpoint, Value: in.IdempotencyKey},
		Digest:    digest,
		Retention: CommitRetention,
		Execute: func(ctx context.Context) (Response, error) {
			jobID, err := idgen.JobID(s.ids)
			if err != nil {
				return Response{}, internal(err, "The batch could not be queued.")
			}
			j := batch.Job{
				ID: jobID, TenantID: sc.Tenant(), Operation: batch.OperationCommit, Status: batch.StatusQueued,
				ItemCount: len(in.Items), RequestedAt: s.clock.Now().UTC().Truncate(time.Microsecond), RequestedBy: sc.Subject(),
			}
			items := make([]batch.Item, len(in.Items))
			view := JobView{Job: j, Items: make([]JobItem, len(in.Items))}
			for i, it := range in.Items {
				items[i] = batch.Item{Job: jobID, Index: i, BusinessKey: it.BusinessKey, Request: requests[i]}
				view.Items[i] = JobItem{Index: i, BusinessKey: it.BusinessKey}
			}
			if err := s.batches.Create(ctx, j, items); err != nil {
				return Response{}, err
			}
			body, err := in.Render(view)
			if err != nil {
				return Response{}, internal(err, "The batch could not be rendered.")
			}
			return Response{Status: 202, Body: body}, nil
		},
		RenderFailure: in.RenderFailure,
	})
}

// Job reads a job and its items' results.
func (s *BatchService) Job(ctx context.Context, jobID id.JobID) (JobView, error) {
	if _, err := requireRoleOrSystem(ctx, security.RoleOperator, security.RoleAnalyst, security.RoleAuditor); err != nil {
		return JobView{}, err
	}
	j, items, results, err := s.batches.Job(ctx, jobID)
	if err != nil {
		return JobView{}, err
	}
	byIndex := make(map[int]batch.Result, len(results))
	for _, r := range results {
		byIndex[r.Index] = r
	}
	v := JobView{Job: j, Items: make([]JobItem, len(items))}
	for i, it := range items {
		v.Items[i] = JobItem{Index: it.Index, BusinessKey: it.BusinessKey}
		if r, ok := byIndex[it.Index]; ok {
			v.Items[i].Result = &r
		}
	}
	return v, nil
}

// Work claims the next job in the cell, if there is one, and runs it to the
// end. It reports whether it found a job.
//
// An item that is refused — invalid input, a superseded decision, a closed
// period — is a FAILED result and the job carries on: one bad line does not
// hold up a thousand good ones. A failure that is not the item's — the
// database gone, the content bundle missing — stops the pass with the item
// unrecorded, and the job is taken up again when its lease lapses.
func (s *BatchService) Work(ctx context.Context) (bool, error) {
	now := s.clock.Now().UTC()
	j, ok, err := s.batches.ClaimNext(ctx, now, now.Add(BatchLease))
	if err != nil || !ok {
		return false, err
	}
	ctx = security.Into(ctx, security.System(j.TenantID))
	items, err := s.batches.Pending(ctx, j.ID)
	if err != nil {
		return true, err
	}
	renewed := now
	for _, it := range items {
		if t := s.clock.Now().UTC(); t.Sub(renewed) > BatchLease/2 {
			if err := s.batches.Renew(ctx, j.ID, t.Add(BatchLease)); err != nil {
				return true, err
			}
			renewed = t
		}
		res, err := s.runItem(ctx, j.ID, it)
		if err != nil {
			return true, err
		}
		if _, err := s.batches.AppendResult(ctx, res); err != nil {
			return true, err
		}
	}
	return true, s.batches.Complete(ctx, j.ID, s.clock.Now().UTC())
}

// runItem commits one item through the commit path, under its own key.
func (s *BatchService) runItem(ctx context.Context, job id.JobID, it batch.Item) (batch.Result, error) {
	res := batch.Result{Job: job, Index: it.Index}
	in, err := decodeItem(it.Request)
	if err != nil {
		// Stored by Submit from a value it built itself; one that does not
		// decode is the cell's defect, and the item says so rather than
		// stalling the job behind it forever.
		res.Status, res.Reason, res.RecordedAt = batch.ItemFailed, errs.ReasonInternal, s.clock.Now().UTC()
		return res, nil
	}
	call := CommitInput{
		IdempotencyKey: batch.ItemKey(job, it.Index),
		Scope:          BatchItemScope,
		Determination:  in,
		Render:         func(d evidence.Decision) ([]byte, error) { return []byte(d.ID.String()), nil },
		RenderFailure: func(err error) Response {
			return Response{Status: 400, Body: []byte(errs.ReasonOf(err))}
		},
	}
	commit := s.det.Commit
	if in.Supersedes != nil {
		// A correction is evaluated under the content that made what it
		// corrects (ZTAX-DET-REQ-0030), exactly as :adjust does it.
		commit = s.det.Adjust
	}
	settled, err := commit(ctx, call)
	if err != nil {
		if recordable(err) {
			res.Status, res.Reason, res.RecordedAt = batch.ItemFailed, errs.ReasonOf(err), s.clock.Now().UTC()
			return res, nil
		}
		return batch.Result{}, err
	}
	res.RecordedAt = s.clock.Now().UTC()
	if settled.Status >= 400 {
		res.Status, res.Reason = batch.ItemFailed, errs.ReasonCode(settled.Body)
		return res, nil
	}
	if settled.ResultRef == nil {
		return batch.Result{}, errs.New(errs.CategoryInternal, errs.ReasonInternal, "A committed batch item names no decision.")
	}
	res.Status, res.Decision = batch.ItemSucceeded, settled.ResultRef
	return res, nil
}

// itemWire is commitCanonical's shape, read back.
type itemWire struct {
	BusinessKey  string          `json:"businessKey"`
	Supersedes   *string         `json:"supersedes"`
	EventTime    string          `json:"eventTime"`
	Input        json.RawMessage `json:"input"`
	Accumulators json.RawMessage `json:"accumulators"`
}

// decodeItem reads a queued item back into the commit request it was.
func decodeItem(data []byte) (DetermineInput, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w itemWire
	if err := dec.Decode(&w); err != nil {
		return DetermineInput{}, err
	}
	eventTime, err := time.Parse("2006-01-02T15:04:05.000000Z", w.EventTime)
	if err != nil {
		return DetermineInput{}, err
	}
	in, err := evidence.DecodeInput(w.Input)
	if err != nil {
		return DetermineInput{}, err
	}
	reads, err := evidence.DecodeReadSet(w.Accumulators)
	if err != nil {
		return DetermineInput{}, err
	}
	out := DetermineInput{BusinessKey: w.BusinessKey, EventTime: eventTime.UTC(), Input: in, Accumulators: reads}
	if w.Supersedes != nil {
		prior, err := id.ParseDecisionID(*w.Supersedes)
		if err != nil {
			return DetermineInput{}, err
		}
		out.Supersedes = &prior
	}
	return out, nil
}
