package gateway_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
)

// A reply that carries an id is one success to the breaker, the same as a reply
// without one; and the id does not reach the transfer record.
func TestAnInvocationIDDoesNotChangeBreakerCountingOrTheRecords(t *testing.T) {
	h := newHarness(t, func(c *gateway.Config) { c.Breaker = gateway.NewBreaker(2, time.Minute) })
	r := goodReply()
	r.InvocationID = "call-id-1"
	h.transport.Reply = r

	// Failures and successes alternate: a success resets the count, so the
	// breaker never opens, exactly as without an id.
	for i := range 3 {
		h.transport.Err = errors.New("connection refused")
		if _, err := h.client.Suggest(tenantCtx(), invocation()); errs.ReasonOf(err) != gateway.ReasonGatewayUnavailable {
			t.Fatalf("round %d failure: %v", i, err)
		}
		h.transport.Err = nil
		if _, err := h.client.Suggest(tenantCtx(), invocation()); err != nil {
			t.Fatalf("round %d success: %v", i, err)
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

// The id from the reply is carried into the Provenance of each kind of record.
func TestAnInvocationIDIsCarriedIntoProvenance(t *testing.T) {
	h := newHarness(t, nil)
	r := goodReply()
	r.InvocationID = "call-id-1"
	h.transport.Reply = r

	s, err := h.client.Suggest(tenantCtx(), invocation())
	if err != nil {
		t.Fatal(err)
	}
	e, err := h.client.Extract(tenantCtx(), invocation())
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.client.ProposeClassification(tenantCtx(), invocation())
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]string{
		"suggestion": s.Provenance().InvocationID,
		"extraction": e.Provenance().InvocationID,
		"proposal":   p.Provenance().InvocationID,
	} {
		if got != "call-id-1" {
			t.Errorf("%s provenance InvocationID = %q, want call-id-1", name, got)
		}
	}
}

// A reply with no id leaves the field empty; the rest of the provenance is as before.
func TestAnAbsentInvocationIDLeavesProvenanceEmpty(t *testing.T) {
	h := newHarness(t, nil)
	p, err := h.client.ProposeClassification(tenantCtx(), invocation())
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Provenance().InvocationID; got != "" {
		t.Fatalf("InvocationID = %q, want empty", got)
	}
	if p.Provenance().ModelProfile != "model:classifier" {
		t.Fatalf("provenance lost its other fields: %+v", p.Provenance())
	}
}

// A failed call returns an error and a record with no provenance, even when the
// fake reply carries an id.
func TestAFailedCallReturnsNoProvenanceAndNoID(t *testing.T) {
	h := newHarness(t, nil)
	r := goodReply()
	r.InvocationID = "call-id-1"
	h.transport.Reply = r
	h.transport.Err = errors.New("connection refused")

	p, err := h.client.ProposeClassification(tenantCtx(), invocation())
	if err == nil {
		t.Fatal("want an error")
	}
	if p.Provenance() != (ai.Provenance{}) {
		t.Fatalf("a failed call returned provenance: %+v", p.Provenance())
	}
}
