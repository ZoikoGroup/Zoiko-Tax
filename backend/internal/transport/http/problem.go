// Package http is the cell's integration surface.
//
// ADR-0010 §2.4 uses stdlib net/http.ServeMux with method-and-pattern matching
// and no web framework: a framework would add a dependency to the request path
// of a system whose replay guarantee names its dependency graph (ADR-0001
// §3.3), in exchange for features the standard library has provided since
// Go 1.22.
//
// ADR-0010 §2.1 makes contracts/openapi the source of truth with handlers
// generated into internal/transport/http/gen. That generator is not wired up
// yet; the handlers here are hand-written against the same contract and are
// replaced when it is. The contract in contracts/openapi/ztax.v1.yaml is
// authored alongside them so the generator has something to generate from.
package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
)

// ProblemBaseURI is where error type URIs resolve. They are stable, versioned
// and documented (ADR-0016 §2.5).
const ProblemBaseURI = "https://errors.zoikotax.com/v1/"

// Problem is an RFC 9457 Problem Details document.
//
// The ztx_ extensions are ADR-0016 §2.5's. They carry identifiers rather than
// data: an error that needs data to be understood names a decision or a
// request, and the data is fetched through an audited path (§2.6).
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`

	ReasonCode string `json:"ztx_reason_code"`
	RequestID  string `json:"ztx_request_id,omitempty"`
	TraceID    string `json:"ztx_trace_id,omitempty"`
	DecisionID string `json:"ztx_decision_id,omitempty"`
	// Field names the request field at fault, for a validation failure. It is
	// a field name from the contract, never a value.
	Field string `json:"ztx_field,omitempty"`
	// Retryable states whether retrying unchanged could succeed, so a client
	// does not have to infer it from the status code (ADR-0016 §2.3).
	Retryable bool `json:"ztx_retryable"`
}

// statusFor maps a category to an HTTP status.
//
// The mapping is one-way and lives here rather than in the domain, because the
// domain does not know what a status code is. Two choices are worth stating:
//
//   - CategoryPolicy is 403, not 401, except for the unauthenticated and
//     session reason codes. "You are not allowed" and "we do not know who you
//     are" need different client behaviour, and the reason code is what
//     distinguishes them rather than a second category.
//   - CategoryUnsupported is 422 rather than 501. The request was well formed
//     and we understood it; we do not cover it. 501 would say the endpoint is
//     unimplemented, which is a different fact.
func statusFor(e *errs.Error) int {
	switch e.Category {
	case errs.CategoryValidation:
		return http.StatusBadRequest
	case errs.CategoryPolicy:
		switch e.Reason {
		case errs.ReasonUnauthenticated, errs.ReasonSessionExpired, errs.ReasonSessionRevoked, errs.ReasonInvalidCredentials:
			return http.StatusUnauthorized
		case errs.ReasonRateLimited:
			return http.StatusTooManyRequests
		}
		return http.StatusForbidden
	case errs.CategoryNotFound:
		return http.StatusNotFound
	case errs.CategoryConflict:
		return http.StatusConflict
	case errs.CategoryUnsupported:
		return http.StatusUnprocessableEntity
	case errs.CategoryUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// typeURI builds the stable type URI for a reason code.
func typeURI(reason errs.ReasonCode) string {
	return ProblemBaseURI + kebab(string(reason))
}

func kebab(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_':
			b = append(b, '-')
		case c >= 'A' && c <= 'Z':
			b = append(b, c-'A'+'a')
		default:
			b = append(b, c)
		}
	}
	return string(b)
}

// writeProblem renders an error as a Problem Details response.
//
// An unclassified error becomes a generic internal Problem, and the cause goes
// to the log rather than to the client: an error string from a driver is
// exactly the kind of thing that carries a table name, a query fragment or a
// value (ADR-0016 §2.6).
func writeProblem(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var e *errs.Error
	if !asError(err, &e) {
		e = errs.Wrap(err, errs.CategoryInternal, errs.ReasonInternal,
			"An unexpected condition prevented the request from completing.")
	}

	status := statusFor(e)
	title, _, ok := errs.Describe(e.Reason)
	if !ok {
		title = "Request failed"
	}

	p := Problem{
		Type:       typeURI(e.Reason),
		Title:      title,
		Status:     status,
		Detail:     e.Detail,
		Instance:   r.URL.Path,
		ReasonCode: string(e.Reason),
		RequestID:  requestIDOf(r.Context()),
		Field:      e.Field,
		Retryable:  e.Retryable(),
	}

	// The log gets the cause; the client does not.
	level := slog.LevelWarn
	if status >= 500 {
		level = slog.LevelError
	}
	log.Log(r.Context(), level, "request failed",
		"http.method", r.Method,
		"http.path", r.URL.Path,
		"http.status", status,
		"ztx.reason_code", string(e.Reason),
		"ztx.category", e.Category.String(),
		"error", err.Error(),
	)

	// Retry-After is what makes a 503 or a 429 actionable rather than a hint.
	if status == http.StatusServiceUnavailable || status == http.StatusTooManyRequests ||
		e.Reason == errs.ReasonRequestInProgress {
		w.Header().Set("Retry-After", "2")
	}

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	// encoding/json is correct here: this is a transport representation, not
	// evidence. ADR-0011 §2.7's prohibition is on digesting through it.
	if err := json.NewEncoder(w).Encode(p); err != nil {
		log.ErrorContext(r.Context(), "could not write problem response", "error", err.Error())
	}
}

// writeJSON renders a success response.
func writeJSON(w http.ResponseWriter, r *http.Request, log *slog.Logger, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.ErrorContext(r.Context(), "could not write response", "error", err.Error())
	}
}

// asError finds an *errs.Error in a chain.
//
// It is errors.As with a shorter name, kept so the import list of every handler
// stays short. Hand-unwrapping would miss an error wrapped with %w, which is
// how every error in internal/adapter reaches here.
func asError(err error, target **errs.Error) bool {
	return errors.As(err, target)
}

// decodeJSON reads a request body, rejecting unknown fields.
//
// ADR-0011 P5 at the transport boundary: an undeclared field is rejected rather
// than ignored, because silently dropping it would let two different requests
// digest identically — which is precisely the property ADR-0013's idempotency
// record depends on.
func decodeJSON(r *http.Request, dst any) error {
	const maxBody = 1 << 20
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errs.Wrap(err, errs.CategoryValidation, errs.ReasonMalformedRequest,
			"The request body could not be parsed. Check that it is valid JSON and that Content-Type is application/json.")
	}
	return nil
}
