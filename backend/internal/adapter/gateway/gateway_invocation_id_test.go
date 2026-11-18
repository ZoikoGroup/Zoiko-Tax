package gateway_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
)

// A reply that carries an id is one success to the breaker, the same as a reply
// without one; and the id does not reach the advisory record or the transfer.
func TestAnInvocationIDDoesNotChangeBreakerCountingOrTheRecords(t *testing.T) {
	h := newHarness(t, func(c *gateway.Config) { c.Breaker = gateway.NewBreaker(2, time.Minute) })
	r := goodReply()
	r.InvocationID = "0190f3a21b2c7d3e8f405a6b7c8d9e0f"
	h.transport.Reply = r

	// Failures and successes alternate: a success resets the count, so the
	// breaker never opens, exactly as without an id.
	for i := range 3 {
		h.transport.Err = errors.New("connection refused")
		if _, err := h.client.Suggest(tenantCtx(), invocation()); errs.ReasonOf(err) != gateway.ReasonGatewayUnavailable {
			t.Fatalf("round %d failure: %v", i, err)
		}
		h.transport.Err = nil
		s, err := h.client.Suggest(tenantCtx(), invocation())
		if err != nil {
			t.Fatalf("round %d success: %v", i, err)
		}
		if got := fmt.Sprintf("%+v", s); containsID(got, r.InvocationID) {
			t.Fatalf("the id reached the advisory record: %s", got)
		}
	}
	if n := len(h.transport.Calls()); n != 6 {
		t.Fatalf("calls = %d, want 6 (the breaker must not have opened)", n)
	}
	for _, tr := range h.recorder.Transfers() {
		if containsID(fmt.Sprintf("%+v", tr), r.InvocationID) {
			t.Fatalf("the id reached the transfer record: %+v", tr)
		}
	}
	// Two failures in a row still open it.
	h.transport.Err = errors.New("connection refused")
	for range 2 {
		_, _ = h.client.Suggest(tenantCtx(), invocation())
	}
	if _, err := h.client.Suggest(tenantCtx(), invocation()); errs.ReasonOf(err) != gateway.ReasonGatewayUnavailable {
		t.Fatalf("open breaker: %v", err)
	}
	if n := len(h.transport.Calls()); n != 8 {
		t.Fatalf("calls = %d, want 8 (third failure refused without calling)", n)
	}
}

func containsID(s, id string) bool {
	for i := 0; i+len(id) <= len(s); i++ {
		if s[i:i+len(id)] == id {
			return true
		}
	}
	return false
}
