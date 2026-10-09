// Package batch is the asynchronous submission of many commits at once
// (Build Plan W2 lane K: POST /v1/batches, GET /v1/jobs/{id}).
//
// A batch changes how commits arrive, never what a commit is. Each item is
// executed by the same commit path a single request takes, under an
// idempotency key of its own derived from the job and the item's position, so
// a worker that dies halfway resumes at the first item with no result and
// cannot commit any item twice. Items run in submission order, so an item
// that corrects an earlier one in the same batch sees it.
package batch

import (
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
)

// MaxItems bounds a batch. A larger workload is several batches: one job is
// one lease, and a job that runs for hours is a job whose failure costs hours.
const MaxItems = 1000

// Operation is what each item of a batch does.
type Operation string

// OperationCommit commits each item, or corrects with it when it names the
// decision it supersedes.
const OperationCommit Operation = "COMMIT"

// Status is a job's execution state.
type Status string

// The job statuses. A job that ran to the end is COMPLETED however its items
// fared; the item results say which succeeded.
const (
	StatusQueued    Status = "QUEUED"
	StatusRunning   Status = "RUNNING"
	StatusCompleted Status = "COMPLETED"
)

// Job is one batch.
type Job struct {
	ID          id.JobID
	TenantID    id.TenantID
	Operation   Operation
	Status      Status
	ItemCount   int
	RequestedAt time.Time
	RequestedBy id.UserID
	StartedAt   time.Time
	FinishedAt  time.Time
	LeaseUntil  time.Time
}

// Item is one queued commit: its position and its canonical request.
type Item struct {
	Job         id.JobID
	Index       int
	BusinessKey string
	Request     []byte
}

// ResultStatus is what an item came to.
type ResultStatus string

// The item results.
const (
	ItemSucceeded ResultStatus = "SUCCEEDED"
	ItemFailed    ResultStatus = "FAILED"
)

// Result is one item's outcome: the decision it recorded, or the registered
// reason it was refused.
type Result struct {
	Job        id.JobID
	Index      int
	Status     ResultStatus
	Decision   *id.DecisionID
	Reason     errs.ReasonCode
	RecordedAt time.Time
}

// Validate refuses a result that is neither a decision nor a reason.
func (r Result) Validate() error {
	switch r.Status {
	case ItemSucceeded:
		if r.Decision == nil || r.Reason != "" {
			return fmt.Errorf("batch: item %d succeeded without a decision", r.Index)
		}
	case ItemFailed:
		if r.Decision != nil || r.Reason == "" {
			return fmt.Errorf("batch: item %d failed without a reason", r.Index)
		}
	default:
		return fmt.Errorf("batch: item %d has result %q", r.Index, r.Status)
	}
	if r.RecordedAt.IsZero() {
		return fmt.Errorf("batch: item %d result has no time", r.Index)
	}
	return nil
}

// ItemKey is the idempotency key an item commits under. It is a function of
// the job and the position alone, so every attempt at the item — the first,
// and any after a worker dies — is the same request to the idempotency guard.
func ItemKey(job id.JobID, index int) string {
	return fmt.Sprintf("%s/%d", job, index)
}
