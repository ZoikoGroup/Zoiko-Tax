package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/batch"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/transport/http/gen"
)

// The batch surface (W2 lane K): POST /v1/batches and GET /v1/jobs/{id}.

func toJob(v app.JobView) gen.Job {
	succeeded, failed, pending := v.Counts()
	out := gen.Job{
		ID: v.Job.ID.String(), Operation: gen.JobOperation(v.Job.Operation), Status: gen.JobStatus(v.Job.Status),
		ItemCount: saturate32(v.Job.ItemCount), Succeeded: saturate32(succeeded), Failed: saturate32(failed),
		Pending: saturate32(pending), RequestedAt: canonical.FormatTime(v.Job.RequestedAt),
		Items: make([]gen.JobItem, len(v.Items)),
	}
	if !v.Job.StartedAt.IsZero() {
		t := canonical.FormatTime(v.Job.StartedAt)
		out.StartedAt = &t
	}
	if !v.Job.FinishedAt.IsZero() {
		t := canonical.FormatTime(v.Job.FinishedAt)
		out.FinishedAt = &t
	}
	for i, it := range v.Items {
		gi := gen.JobItem{Index: saturate32(it.Index), BusinessKey: it.BusinessKey, Status: gen.JobItemStatus("PENDING")}
		if it.Result != nil {
			gi.Status = gen.JobItemStatus(it.Result.Status)
			if it.Result.Decision != nil {
				d := it.Result.Decision.String()
				gi.DecisionID = &d
			}
			if it.Result.Status == batch.ItemFailed {
				reason := gen.ReasonCode(it.Result.Reason)
				gi.ReasonCode = &reason
			}
		}
		out.Items[i] = gi
	}
	return out
}

func (rt *Router) batchesUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if rt.Batches != nil {
		return false
	}
	writeProblem(w, r, rt.log, errs.New(errs.CategoryUnavailable, errs.ReasonNoContentBundle,
		"This cell is not configured for determination. The request was not applied."))
	return true
}

// handleSubmitBatch is POST /v1/batches.
func (rt *Router) handleSubmitBatch(w http.ResponseWriter, r *http.Request) {
	if rt.batchesUnavailable(w, r) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeProblem(w, r, rt.log, errs.New(errs.CategoryValidation, errs.ReasonIdempotencyKeyRequired,
			"This endpoint requires an Idempotency-Key header. The request was not applied."))
		return
	}
	var req gen.BatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if req.Operation != gen.BatchRequestOperation(batch.OperationCommit) {
		writeProblem(w, r, rt.log, errs.Invalid("operation", errs.ReasonInvalidValue, "A batch's operation is COMMIT."))
		return
	}
	if len(req.Items) == 0 || len(req.Items) > batch.MaxItems {
		writeProblem(w, r, rt.log, errs.Invalid("items", errs.ReasonInvalidValue,
			fmt.Sprintf("A batch carries between 1 and %d items.", batch.MaxItems)))
		return
	}
	items := make([]app.DetermineInput, len(req.Items))
	for i, it := range req.Items {
		in, err := commitInput(it)
		if err != nil {
			// The whole batch is refused for one malformed item: shape is
			// checked before anything is queued, so a queued item can only
			// fail for a reason the commit path gives.
			writeProblem(w, r, rt.log, itemProblem(i, err))
			return
		}
		items[i] = in
	}
	settled, err := rt.Batches.Submit(r.Context(), app.SubmitInput{
		IdempotencyKey: key, Items: items,
		Render: func(v app.JobView) ([]byte, error) {
			body, err := json.Marshal(toJob(v))
			if err != nil {
				return nil, err
			}
			return append(body, '\n'), nil
		},
		RenderFailure: func(err error) app.Response {
			status, body, _ := renderProblem(r, rt.log, err)
			return app.Response{Status: status, Body: body}
		},
	})
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	if settled.Replayed {
		w.Header().Set("Idempotent-Replay", "true")
	}
	if settled.Status >= 400 {
		writeProblemBytes(w, settled.Status, settled.Body, "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(settled.Status)
	_, _ = w.Write(settled.Body)
}

// itemProblem places an item's validation failure at its position, so the
// Problem names items[3].eventTime rather than an eventTime somewhere.
func itemProblem(i int, err error) error {
	var e *errs.Error
	if !errors.As(err, &e) || e.Category != errs.CategoryValidation {
		return err
	}
	field := fmt.Sprintf("items[%d]", i)
	if e.Field != "" {
		field += "." + e.Field
	}
	return errs.Invalid(field, e.Reason, fmt.Sprintf("Item %d: %s", i, e.Detail))
}

// handleGetJob is GET /v1/jobs/{jobId}.
func (rt *Router) handleGetJob(w http.ResponseWriter, r *http.Request) {
	if rt.batchesUnavailable(w, r) {
		return
	}
	jobID, err := id.ParseJobID(r.PathValue("jobId"))
	if err != nil {
		writeProblem(w, r, rt.log, errs.Invalid("jobId", errs.ReasonInvalidValue, "That is not a valid job identifier."))
		return
	}
	v, err := rt.Batches.Job(r.Context(), jobID)
	if err != nil {
		writeProblem(w, r, rt.log, err)
		return
	}
	writeJSON(w, r, rt.log, http.StatusOK, toJob(v))
}
