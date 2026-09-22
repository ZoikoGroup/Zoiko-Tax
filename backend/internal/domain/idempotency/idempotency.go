// Package idempotency implements ADR-0013.
//
// The design rests on one idea worth stating plainly: the uniqueness constraint
// on (tenant_id, endpoint, idempotency_key) *is* the concurrency control
// (ADR-0013 §2.5). The second concurrent request's insert fails, and it takes
// the request-in-progress path. There is no window in which two requests both
// believe they are first, because the exclusion is a constraint rather than a
// check-then-act — and no amount of application-level locking would be as
// reliable as the thing the database is already guaranteeing.
//
// The second idea is that this is the outer of three layers (ADR-0013 §2.10).
// ADR-0004 §2.4's UNIQUE (accumulator_key, source_decision_id) is the inner one
// and holds independently; SubmissionAttempt resolution is a third at the
// authority boundary. The highest-rated risk in the register does not get one
// control.
package idempotency

import (
	"fmt"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
)

// State is where a record is in its lifecycle.
type State string

// The states.
const (
	// StatePending was inserted and the work has not finished. A second
	// request seeing this is refused rather than queued.
	StatePending State = "PENDING"
	// StateSucceeded completed; the stored response is replayed verbatim.
	StateSucceeded State = "SUCCEEDED"
	// StateFailed completed deterministically; the stored failure is replayed,
	// because the same request will fail identically and re-executing wastes
	// work (ADR-0013 §2.7).
	StateFailed State = "FAILED"
)

// Key identifies a record. Scoping by tenant prevents cross-tenant
// interference; scoping by endpoint prevents a key used for a commit from
// matching an adjust (ADR-0013 §2.2).
type Key struct {
	TenantID id.TenantID
	Endpoint string
	Value    string
}

// MaxKeyLength bounds a caller-supplied key. We do not generate them, do not
// parse them and assume no structure (ADR-0012 §2.8) — but an unbounded string
// is a way to fill an index, so there is a ceiling.
const MaxKeyLength = 255

// Validate refuses a malformed key.
func (k Key) Validate() error {
	switch {
	case k.TenantID.IsZero():
		return fmt.Errorf("idempotency: key has no tenant")
	case k.Endpoint == "":
		return fmt.Errorf("idempotency: key has no endpoint")
	case k.Value == "":
		return errs.New(errs.CategoryValidation, errs.ReasonIdempotencyKeyRequired,
			"This endpoint requires an Idempotency-Key header. The request was not applied.")
	case len(k.Value) > MaxKeyLength:
		return errs.Invalid("Idempotency-Key", errs.ReasonInvalidValue,
			fmt.Sprintf("An idempotency key may be at most %d characters.", MaxKeyLength))
	}
	return nil
}

// Record is one idempotency row.
type Record struct {
	Key Key
	// RequestDigest is the ADR-0011 canonical digest of the request body, not a
	// hash of raw bytes. Two byte-different but semantically identical retries
	// — a reordered JSON object, a client library upgrade — must match, and
	// raw-byte hashing would produce a false conflict precisely when a client is
	// behaving correctly (ADR-0013 §2.3).
	RequestDigest canonical.Digest
	State         State

	ResponseStatus int
	ResponseBody   []byte
	ResultRef      *id.DecisionID

	CreatedAt   time.Time
	CompletedAt *time.Time
	ExpiresAt   time.Time
}

// Settled reports whether the record carries a replayable outcome.
func (r Record) Settled() bool { return r.State == StateSucceeded || r.State == StateFailed }

// Disposition is what the caller should do, and it is exhaustive: ADR-0013
// §2.4 defines four situations and this is all four.
type Disposition int

// The dispositions.
const (
	// Proceed means no record existed; one was inserted PENDING and the caller
	// executes, completing the record in the same transaction.
	Proceed Disposition = iota
	// Replay means a settled record matched; return the stored response
	// verbatim with Idempotent-Replay: true. The request is not executed.
	Replay
	// Conflict means the key was reused with a different body. The request is
	// not executed.
	Conflict
	// InProgress means a record is PENDING. The request is not executed.
	InProgress
)

// Decide classifies an existing record against an incoming request.
//
// It is a pure function so the branching is testable without a database, and so
// the four outcomes can be read in one place rather than inferred from the
// control flow of a repository method.
func Decide(existing *Record, incoming canonical.Digest) (Disposition, error) {
	if existing == nil {
		return Proceed, nil
	}

	// The digest comparison comes before the state check on purpose. A key
	// reused with a different body is a client defect whichever state the
	// original is in, and reporting it as "in progress" would invite the client
	// to retry the wrong request until the original settled.
	if !existing.RequestDigest.Equal(incoming) {
		return Conflict, errs.New(errs.CategoryConflict, errs.ReasonIdempotencyKeyReuse,
			"This idempotency key was already used for a different request. The request was not applied. Use a new key, or resend the original request unchanged.")
	}

	if existing.State == StatePending {
		return InProgress, errs.New(errs.CategoryConflict, errs.ReasonRequestInProgress,
			"A request with this idempotency key is still being processed. The request was not applied. Retry after the interval given in Retry-After.")
	}
	return Replay, nil
}

// TransientFailureIsNotTerminal reports whether a failed attempt should delete
// its PENDING record rather than recording a terminal FAILED (ADR-0013 §2.7).
//
// The rule in one line: only CategoryUnavailable is transient. Recording a
// transient failure as terminal would leave the client permanently unable to
// complete a legitimate request with that key — they would retry, match the
// stored failure, and be handed the same error forever with no way to make
// progress short of changing the key, which they have no reason to think is
// necessary.
func TransientFailureIsNotTerminal(err error) bool {
	return errs.IsCategory(err, errs.CategoryUnavailable)
}

// DefaultRetention is how long a non-fiscal record is kept (ADR-0013 §2.9). A
// record for a fiscal effect follows the statutory retention of what it
// created, which the caller supplies instead.
const DefaultRetention = 30 * 24 * time.Hour

// NewPending builds the record to insert before executing.
func NewPending(key Key, digest canonical.Digest, now time.Time, retention time.Duration) (Record, error) {
	if err := key.Validate(); err != nil {
		return Record{}, err
	}
	if digest.IsZero() {
		return Record{}, fmt.Errorf("idempotency: record has no request digest")
	}
	if retention <= 0 {
		retention = DefaultRetention
	}
	return Record{
		Key:           key,
		RequestDigest: digest,
		State:         StatePending,
		CreatedAt:     now.UTC(),
		ExpiresAt:     now.UTC().Add(retention),
	}, nil
}

// Complete returns the record settled with a response.
func (r Record) Complete(status int, body []byte, result *id.DecisionID, now time.Time) Record {
	settled := r
	settled.State = StateSucceeded
	if status >= 400 {
		settled.State = StateFailed
	}
	settled.ResponseStatus = status
	settled.ResponseBody = body
	settled.ResultRef = result
	at := now.UTC()
	settled.CompletedAt = &at
	return settled
}
