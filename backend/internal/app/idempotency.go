package app

import (
	"context"
	"errors"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/id"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/idempotency"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/canonical"
	"github.com/zoikogroup/zoikotax/backend/internal/platform/clock"
	"github.com/zoikogroup/zoikotax/backend/internal/port"
)

// Idempotency runs a mutating request at most once per key (ADR-0013).
//
// It is shared by every endpoint §2.1 makes idempotent — commit today, adjust,
// refund and batches when they land — so the four outcomes of §2.4 are decided
// in one place rather than re-derived per handler.
//
// The sequence, and why each step is where it is:
//
//  1. A PENDING record is inserted and committed on its own. A concurrent
//     duplicate's insert then fails on the primary key and it is told the
//     request is in progress (§2.5); it never waits on, or races, the first.
//  2. The domain effect and the record's completion commit in one
//     transaction (§2.6). There is no instant at which a decision exists with
//     no binding to the key that created it.
//  3. A deterministic failure is completed FAILED and replayed; a transient
//     one releases the key so a retry is a genuine attempt (§2.7).
type Idempotency struct {
	repo  port.IdempotencyRepository
	tx    port.TxManager
	clock clock.Clock
}

// NewIdempotency wires the guard.
func NewIdempotency(repo port.IdempotencyRepository, tx port.TxManager, clk clock.Clock) *Idempotency {
	return &Idempotency{repo: repo, tx: tx, clock: clk}
}

// Response is what a request answered, as the bytes a retry is given.
type Response struct {
	Status int
	Body   []byte
	// ResultRef names what the request created, where it created something
	// (§2.8).
	ResultRef *id.DecisionID
}

// Settled is a response and whether it was replayed rather than produced now.
type Settled struct {
	Response
	Replayed bool
}

// Call is one idempotent request.
type Call struct {
	Key       idempotency.Key
	Digest    canonical.Digest
	Retention time.Duration
	// Execute applies the effect inside the transaction that settles the
	// record, and returns the response. Everything it writes through the
	// context's transaction commits with the record or not at all.
	Execute func(ctx context.Context) (Response, error)
	// RenderFailure renders a deterministic failure as the response that is
	// recorded and replayed. It is the transport's, because the bytes a retry
	// receives must be the bytes the first caller received.
	RenderFailure func(error) Response
}

// Do runs the call.
//
// A nil error means a response exists — produced now or replayed — and the
// caller writes it as given. An error means nothing was executed and nothing
// was recorded: the key was reused, is in progress, or the attempt failed
// transiently.
func (g *Idempotency) Do(ctx context.Context, c Call) (Settled, error) {
	pending, err := idempotency.NewPending(c.Key, c.Digest, g.clock.Now(), c.Retention)
	if err != nil {
		return Settled{}, err
	}
	existing, err := g.claim(ctx, pending)
	if err != nil {
		return Settled{}, err
	}
	if existing != nil {
		return Settled{Replayed: true, Response: Response{
			Status: existing.ResponseStatus, Body: existing.ResponseBody, ResultRef: existing.ResultRef,
		}}, nil
	}

	resp, err := g.execute(ctx, pending, c.Execute)
	if err == nil {
		return Settled{Response: resp}, nil
	}
	if !recordable(err) {
		// A released key is the whole point of a transient failure, so a
		// failure to release is reported with it: the client's retry will be
		// told the request is in progress until the sweep reaps the record.
		if rerr := g.repo.Release(ctx, pending.Key); rerr != nil {
			return Settled{}, errors.Join(err, rerr)
		}
		return Settled{}, err
	}

	failure := c.RenderFailure(err)
	if serr := g.settle(ctx, pending.Complete(failure.Status, failure.Body, nil, g.clock.Now())); serr != nil {
		if rerr := g.repo.Release(ctx, pending.Key); rerr != nil {
			return Settled{}, errors.Join(err, serr, rerr)
		}
		return Settled{}, err
	}
	return Settled{Response: failure}, nil
}

// claim inserts the PENDING record, or classifies the one already there.
// It returns a settled record to replay, nil to proceed, or the refusal.
func (g *Idempotency) claim(ctx context.Context, pending idempotency.Record) (*idempotency.Record, error) {
	// Twice at most: once for the key as it stands, and once more if the
	// record found was expired, or vanished between the insert and the read
	// because a transient failure released it.
	for range 2 {
		inserted, err := g.repo.Insert(ctx, pending)
		if err != nil {
			return nil, err
		}
		if inserted {
			return nil, nil
		}
		existing, err := g.repo.Get(ctx, pending.Key)
		if errs.IsCategory(err, errs.CategoryNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !existing.ExpiresAt.After(g.clock.Now()) {
			if _, err := g.repo.Expire(ctx, pending.Key, g.clock.Now()); err != nil {
				return nil, err
			}
			continue
		}
		disposition, err := idempotency.Decide(&existing, pending.RequestDigest)
		if disposition == idempotency.Replay {
			return &existing, nil
		}
		return nil, err
	}
	return nil, errs.New(errs.CategoryConflict, errs.ReasonRequestInProgress,
		"A request with this idempotency key is still being processed. The request was not applied. Retry after the interval given in Retry-After.")
}

// execute runs the effect and settles the record in one transaction.
func (g *Idempotency) execute(ctx context.Context, pending idempotency.Record, fn func(context.Context) (Response, error)) (Response, error) {
	tx, txCtx, err := g.tx.Begin(ctx)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = tx.Rollback(txCtx) }()

	resp, err := fn(txCtx)
	if err != nil {
		return Response{}, err
	}
	if err := g.repo.Complete(txCtx, pending.Complete(resp.Status, resp.Body, resp.ResultRef, g.clock.Now())); err != nil {
		return Response{}, err
	}
	if err := tx.Commit(txCtx); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// settle records a deterministic failure in a transaction of its own; the
// effect's transaction has already rolled back.
func (g *Idempotency) settle(ctx context.Context, rec idempotency.Record) error {
	tx, txCtx, err := g.tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(txCtx) }()
	if err := g.repo.Complete(txCtx, rec); err != nil {
		return err
	}
	return tx.Commit(txCtx)
}

// recordable reports whether a failure is deterministic — the same request
// would fail the same way — and so is recorded FAILED and replayed (§2.7).
//
// It is narrower than "not transient". A defect is not recorded: a 500 replayed
// for the life of the key would make a bug that has since been fixed
// permanently unfixable for every client that met it. Neither is a policy
// refusal, which depends on who is asking rather than on what was asked.
func recordable(err error) bool {
	switch errs.CategoryOf(err) {
	case errs.CategoryValidation, errs.CategoryNotFound, errs.CategoryConflict, errs.CategoryUnsupported:
		return true
	}
	return false
}
