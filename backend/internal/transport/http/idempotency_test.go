package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zoikogroup/zoikotax/backend/internal/app"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
)

// ZTAX-INT-REQ-0001: Idempotency-Key is mandatory on POST
// /v1/transactions:commit, :adjust, :refund and POST /v1/batches, and a
// request without one is refused before any work begins.
//
// The services behind the handlers are zero values with no stores: any work
// past the key check would dereference nothing and panic, so a clean 400
// here is the proof that the refusal comes first — before the body is even
// read.
func TestINTREQ0001AKeylessWriteIsRefusedBeforeAnyWork(t *testing.T) {
	rt := NewRouter(nil, nil, nil, nil, nil, nil)
	rt.Determination = &app.DeterminationService{}
	rt.Refunds = &app.RefundService{}
	rt.Batches = &app.BatchService{}

	for path, handle := range map[string]http.HandlerFunc{
		"/v1/transactions:commit": rt.handleCommit,
		"/v1/transactions:adjust": rt.handleAdjust,
		"/v1/transactions:refund": rt.handleRefund,
		"/v1/batches":             rt.handleSubmitBatch,
	} {
		body := &panicReader{t: t, path: path}
		w := httptest.NewRecorder()
		handle(w, httptest.NewRequest(http.MethodPost, path, body))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s without a key: %d", path, w.Code)
			continue
		}
		var p struct {
			Reason string `json:"ztx_reason_code"`
		}
		if err := json.NewDecoder(strings.NewReader(w.Body.String())).Decode(&p); err != nil || p.Reason != string(errs.ReasonIdempotencyKeyRequired) {
			t.Errorf("%s without a key: %v %s", path, err, w.Body.String())
		}
	}

	// And the lint's list of idempotent endpoints is this list.
	routed := map[string]bool{}
	for _, r := range rt.Routes() {
		routed[r.Pattern] = true
	}
	for _, p := range []string{"/v1/transactions:commit", "/v1/transactions:adjust", "/v1/transactions:refund", "/v1/batches"} {
		if !routed[p] {
			t.Errorf("%s is not routed", p)
		}
	}
}

// panicReader fails the test if anything reads the body.
type panicReader struct {
	t    *testing.T
	path string
}

func (p *panicReader) Read([]byte) (int, error) {
	p.t.Errorf("%s read the body of a keyless request", p.path)
	return 0, http.ErrBodyNotAllowed
}
