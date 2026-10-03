// Package gatewaytest holds the in-memory Gateway transport and transfer
// recorder for tests.
//
// It is a separate package, rather than a Fake beside the real transport, so
// that nothing that ships can reach it: a stand-in transport that returns
// canned model output, wired into a production binary by mistake, would be an
// AI result with no Gateway, no governance and no evidence behind it. The
// depguard rule gateway-fakes-are-test-only makes importing it from non-test
// code a CI failure, the same treatment internal/fiscaltest gets.
package gatewaytest

import (
	"context"
	"sync"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway"
)

// Fake is an in-memory gateway.Transport. It records every Call it receives
// and answers with Respond, or with Reply and Err when Respond is nil.
type Fake struct {
	mu    sync.Mutex
	calls []gateway.Call

	// Respond, when set, computes the answer to each call.
	Respond func(ctx context.Context, call gateway.Call) (gateway.Reply, error)
	// Reply and Err are the fixed answer when Respond is nil.
	Reply gateway.Reply
	Err   error
}

// Invoke records the call and answers it.
func (f *Fake) Invoke(ctx context.Context, call gateway.Call) (gateway.Reply, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	respond, reply, err := f.Respond, f.Reply, f.Err
	f.mu.Unlock()
	if respond != nil {
		return respond(ctx, call)
	}
	return reply, err
}

// Calls returns every call received, in order. A test that expects a refusal
// before the boundary asserts this is empty.
func (f *Fake) Calls() []gateway.Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]gateway.Call, len(f.calls))
	copy(out, f.calls)
	return out
}

// Recorder is an in-memory gateway.TransferRecorder.
type Recorder struct {
	mu        sync.Mutex
	transfers []gateway.Transfer
	// Err, when set, is returned by every RecordTransfer after recording.
	Err error
}

// RecordTransfer records the transfer.
func (r *Recorder) RecordTransfer(_ context.Context, t gateway.Transfer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transfers = append(r.transfers, t)
	return r.Err
}

// Transfers returns every transfer recorded, in order.
func (r *Recorder) Transfers() []gateway.Transfer {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]gateway.Transfer, len(r.transfers))
	copy(out, r.transfers)
	return out
}
