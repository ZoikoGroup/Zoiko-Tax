// Package http is the cell's integration surface.
//
// ADR-0010 §2.4 uses stdlib net/http.ServeMux with method-and-pattern matching
// and no web framework: a framework would add a dependency to the request path
// of a system whose replay guarantee names its dependency graph (ADR-0001
// §3.3), in exchange for features the standard library has provided since
// Go 1.22.
//
// ADR-0010 §2.1 makes contracts/openapi the source of truth. The wire types
// are generated from it into internal/transport/http/gen, and `make
// api-gen-check` fails when they drift; the handler bodies are hand-written
// against those types (§4.5), and contract_test.go holds the route table to the
// same contract.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"

	"go.opentelemetry.io/otel/trace"

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
	status, body, reason := renderProblem(r, log, err)
	writeProblemBytes(w, status, body, reason)
}

// writeProblemBytes writes a Problem that has already been rendered — by
// writeProblem, or read back from an idempotency record, in which case these
// are the bytes the first caller was given.
func writeProblemBytes(w http.ResponseWriter, status int, body []byte, reason errs.ReasonCode) {
	// Retry-After is what makes a 503 or a 429 actionable rather than a hint.
	if status == http.StatusServiceUnavailable || status == http.StatusTooManyRequests ||
		reason == errs.ReasonRequestInProgress {
		w.Header().Set("Retry-After", "2")
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// renderProblem builds the Problem for err, logs the cause, and returns the
// status and the encoded document.
func renderProblem(r *http.Request, log *slog.Logger, err error) (int, []byte, errs.ReasonCode) {
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
		TraceID:    traceIDOf(r.Context()),
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

	// encoding/json is correct here: this is a transport representation, not
	// evidence. ADR-0011 §2.7's prohibition is on digesting through it.
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(p); err != nil {
		log.ErrorContext(r.Context(), "could not encode problem response", "error", err.Error())
	}
	return status, buf.Bytes(), e.Reason
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
//
// DisallowUnknownFields alone does not do this. encoding/json matches object
// keys to struct fields case-insensitively, so `displayname` is taken as
// `displayName` — and when both are present the later one silently wins. That
// is the common shape of a real integration bug, and it was accepted until
// exactKeys was added. A duplicated key is refused for the same reason: two
// documents that differ only in which duplicate comes last are two requests.
func decodeJSON(r *http.Request, dst any) error {
	const maxBody = 1 << 20
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil {
		return malformed(err)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		// encoding/json has no typed error for this; the message prefix is the
		// one DisallowUnknownFields documents, and a change to it would fall
		// through to MALFORMED_REQUEST — still a refusal, with a vaguer code.
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			return unknownField()
		}
		return malformed(err)
	}
	return exactKeys(body, dst)
}

// exactKeys refuses a key that is not, byte for byte, one of the JSON field
// names its struct declares, and a key that appears twice — at every depth.
//
// Every depth, because the determination bodies nest: a miscased `Currency`
// beside `currency` inside one amount is the same defect as a miscased
// top-level field, and there it decides which currency a decision is recorded
// in. Map keys are the pack's names and are not checked against anything, but
// a map key that repeats is refused for the same reason a field that repeats
// is.
func exactKeys(body []byte, dst any) error {
	return exactKeysOf(body, reflect.TypeOf(dst))
}

func exactKeysOf(raw []byte, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var fields map[string]reflect.Type
	switch t.Kind() {
	case reflect.Struct:
		fields = jsonFields(t)
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil
		}
	case reflect.Slice:
		return exactKeysOfArray(raw, t.Elem())
	default:
		return nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil // not an object; Decode has already accepted or refused it
	}
	seen := make(map[string]bool)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return malformed(err)
		}
		key, _ := tok.(string)
		if seen[key] {
			return unknownField()
		}
		seen[key] = true
		var elem reflect.Type
		if fields == nil {
			elem = t.Elem()
		} else if elem = fields[key]; elem == nil {
			return unknownField()
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return malformed(err)
		}
		if err := exactKeysOf(value, elem); err != nil {
			return err
		}
	}
	return nil
}

func exactKeysOfArray(raw []byte, elem reflect.Type) error {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil // not an array; Decode has already accepted or refused it
	}
	for _, item := range items {
		if err := exactKeysOf(item, elem); err != nil {
			return err
		}
	}
	return nil
}

// jsonFields maps the JSON field names a struct declares to their types.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		fields[name] = f.Type
	}
	return fields
}

func malformed(err error) error {
	return errs.Wrap(err, errs.CategoryValidation, errs.ReasonMalformedRequest,
		"The request body could not be parsed. Check that it is valid JSON and that Content-Type is application/json.")
}

// unknownField names no field. The key at fault is one the contract does not
// declare, so it is not a contract field for ztx_field to carry, and it is
// caller-supplied text that has no business in a log line.
func unknownField() error {
	return errs.New(errs.CategoryValidation, errs.ReasonUnknownField,
		"The request contained a field the contract does not declare. Field names are matched exactly, including case, and may not repeat.")
}

// traceIDOf is the request's trace id, where it is being traced. Quoting it in
// a support request finds the span; it carries no data of its own.
func traceIDOf(ctx context.Context) string {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() && sc.IsSampled() {
		return sc.TraceID().String()
	}
	return ""
}
