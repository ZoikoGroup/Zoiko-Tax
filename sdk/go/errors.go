package zoikotax

import (
	"encoding/json"
	"fmt"
	"time"
)

// ZoikoTaxError is a failed request that the service answered: the response
// carried an RFC 9457 Problem Details document.
//
// It carries the document whole — typed as Problem, and as received in Raw —
// so nothing a caller might need is lost in translation. The convenience
// fields below are copies of the Problem's, flattened because they are the ones
// a caller acts on.
//
// The name stutters (zoikotax.ZoikoTaxError) on purpose: it is the same name in
// every ZoikoTax SDK, so a support conversation, a log search or a runbook can
// say "a ZoikoTaxError with reason code X" without naming a language.
type ZoikoTaxError struct { //nolint:revive // cross-SDK name; see the type comment.
	// ReasonCode is the registered reason code. This is the field to branch on.
	//
	// It is a string rather than a closed set of constants: the register grows
	// by addition (ADR-0010 §2.6), and a closed set would make an SDK release a
	// prerequisite for every new code.
	ReasonCode ReasonCode
	// Status is the HTTP status, for logging and for the cases where it is the
	// clearer signal.
	Status int
	// Retryable is whether retrying this request unchanged could succeed. Only
	// a transient failure is retryable; everything else fails identically.
	Retryable bool
	// RequestID is this request's identifier. Quote it in a support request.
	RequestID string
	// Field is the contract field at fault, for a validation failure.
	Field string
	// RetryAfter is how long the server asked the caller to wait before
	// retrying, from the Retry-After header. Zero where it said nothing, which
	// for a retryable error means the same as "now".
	RetryAfter time.Duration
	// Problem is the Problem Details document, decoded.
	Problem Problem
	// Raw is the Problem Details document exactly as received. It is here for
	// an extension member this SDK release predates, which the typed Problem
	// cannot hold; ADR-0010 §2.6 makes additions the normal case.
	Raw json.RawMessage
}

// Error reports the reason code, the status and the service's own summary. It
// is for a log line, not for matching: branch on ReasonCode.
func (e *ZoikoTaxError) Error() string {
	summary := e.Problem.Title
	if e.Problem.Detail != nil && *e.Problem.Detail != "" {
		summary = *e.Problem.Detail
	}
	if e.RequestID != "" {
		return fmt.Sprintf("zoikotax: %s (%d): %s [request %s]", e.ReasonCode, e.Status, summary, e.RequestID)
	}
	return fmt.Sprintf("zoikotax: %s (%d): %s", e.ReasonCode, e.Status, summary)
}

// TransportError is a failure that produced no Problem document at all: a DNS
// failure, a TLS failure, a cancelled context, a proxy that returned HTML.
//
// It is a separate type because the caller's options differ. A ZoikoTaxError
// is the service answering; this is not reaching it, or something between the
// client and the cell answering instead, and the reason is outside anything the
// contract describes. Reporting a load balancer's 502 as a ZoikoTaxError would
// attribute it to the service and give it a reason code nobody registered.
type TransportError struct {
	// Method and Path name the request, without the base URL or query.
	Method string
	Path   string
	// Status is the HTTP status, where there was a response at all; zero
	// where there was none.
	Status int
	// Message says what went wrong, in words.
	Message string
	// Err is the underlying cause, where there is one — a *url.Error, a
	// context.Canceled, a JSON syntax error. Reachable with errors.Is and
	// errors.As through Unwrap.
	Err error
}

// Error describes the failure. It is for a log line, not for matching.
func (e *TransportError) Error() string {
	msg := fmt.Sprintf("zoikotax: %s %s %s", e.Method, e.Path, e.Message)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying cause, so that errors.Is(err, context.Canceled)
// and errors.Is(err, context.DeadlineExceeded) work through a TransportError.
func (e *TransportError) Unwrap() error { return e.Err }
