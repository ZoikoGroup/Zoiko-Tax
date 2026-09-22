// Package errs is the estate's error taxonomy.
//
// ADR-0016 §2.3 makes errors a closed typed set rather than an open space of
// strings. Three properties follow, and each is load-bearing somewhere else:
//
//   - CategoryUnavailable exists so that "retry is safe" is a property of the
//     error rather than an inference from a status code. ADR-0013 §2.7 keys the
//     difference between deleting a PENDING idempotency record and recording a
//     terminal FAILED on exactly this, so guessing is not available.
//   - A ReasonCode is a registered string with an owner, and it means the same
//     thing in a decision, in an error and in evidence (ADR-0016 §2.4).
//   - Nothing matches on message text (§2.7). Callers use errors.Is and
//     errors.As, and a message can be reworded without breaking a caller.
//
// What this package deliberately does not do is render anything. RFC 9457
// Problem Details are a transport concern and live in internal/transport/http;
// the domain does not know what a status code is.
package errs

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Category is the closed set from ADR-0016 §2.3.
type Category int

// The categories. Ordering is not meaningful and nothing serializes the
// ordinal — String is what crosses a boundary.
const (
	// CategoryValidation is a malformed or contract-violating request.
	CategoryValidation Category = iota
	// CategoryPolicy is a refusal by security, privacy, entitlement or AI
	// governance. The request was understood and is not permitted.
	CategoryPolicy
	// CategoryNotFound is a reference to something that does not exist, or
	// that the caller may not know exists.
	CategoryNotFound
	// CategoryConflict is idempotency reuse, an optimistic conflict or a
	// state-machine violation.
	CategoryConflict
	// CategoryUnsupported is a capability not available for this tenant, pack
	// or region. It is a fact about our coverage, not a defect.
	CategoryUnsupported
	// CategoryUnavailable is transient. Retry is safe, and that is a property
	// of the error rather than a guess (ADR-0013 §2.7).
	CategoryUnavailable
	// CategoryInternal is a defect.
	CategoryInternal
)

// String renders the category name. Categories cross the wire by name so that
// adding one does not renumber the others.
func (c Category) String() string {
	switch c {
	case CategoryValidation:
		return "VALIDATION"
	case CategoryPolicy:
		return "POLICY"
	case CategoryNotFound:
		return "NOT_FOUND"
	case CategoryConflict:
		return "CONFLICT"
	case CategoryUnsupported:
		return "UNSUPPORTED"
	case CategoryUnavailable:
		return "UNAVAILABLE"
	case CategoryInternal:
		return "INTERNAL"
	}
	return "INTERNAL"
}

// Retryable reports whether retrying this request unchanged could succeed.
// Only CategoryUnavailable is retryable: everything else will fail identically,
// and a client that retries a validation failure is generating load rather than
// making progress.
func (c Category) Retryable() bool { return c == CategoryUnavailable }

// ReasonCode is a registered closed vocabulary (ADR-0016 §2.4). Stable strings,
// never renumbered, SCREAMING_SNAKE_CASE, registered with a description and an
// owner. They appear in decisions, in errors and in evidence, and mean the same
// thing in all three.
type ReasonCode string

// registration is what the register knows about a code.
type registration struct {
	Code        ReasonCode
	Category    Category
	Owner       string
	Description string
}

var (
	registryMu sync.RWMutex
	registry   = map[ReasonCode]registration{}
)

// Register records a reason code. It is called from package init blocks and
// panics on a duplicate or a malformed code, which is correct: an unregistered
// or double-registered code is a build-time defect and the process should not
// start with one (ADR-0016 §2.4).
func Register(code ReasonCode, category Category, owner, description string) ReasonCode {
	if !validCode(code) {
		panic(fmt.Sprintf("errs: reason code %q is not SCREAMING_SNAKE_CASE", code))
	}
	if owner == "" || description == "" {
		panic(fmt.Sprintf("errs: reason code %q needs an owner and a description", code))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if prior, ok := registry[code]; ok {
		panic(fmt.Sprintf("errs: reason code %q already registered by %s", code, prior.Owner))
	}
	registry[code] = registration{Code: code, Category: category, Owner: owner, Description: description}
	return code
}

// Registered reports whether a code is in the register. The transport layer
// checks this before emitting, so an unregistered code cannot reach a client.
func Registered(code ReasonCode) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	_, ok := registry[code]
	return ok
}

// Describe returns a code's registered description and owner.
func Describe(code ReasonCode) (description, owner string, ok bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	r, found := registry[code]
	if !found {
		return "", "", false
	}
	return r.Description, r.Owner, true
}

// Codes returns every registered code in sorted order, for the capabilities
// surface and for the documentation generator.
func Codes() []ReasonCode {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]ReasonCode, 0, len(registry))
	for c := range registry {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// validCode enforces SCREAMING_SNAKE_CASE without a regexp dependency in a path
// that runs at init.
func validCode(c ReasonCode) bool {
	if c == "" {
		return false
	}
	for i := 0; i < len(c); i++ {
		ch := c[i]
		switch {
		case ch >= 'A' && ch <= 'Z':
		case ch >= '0' && ch <= '9':
		case ch == '_' && i != 0 && i != len(c)-1:
		default:
			return false
		}
	}
	return true
}

// Error is the estate's error type.
//
// Detail is drawn from a bounded template and carries no PII and no fiscal
// amount (ADR-0016 §2.6). An error that needs data to be understood carries an
// identifier instead, and the data is fetched through an audited path.
type Error struct {
	Category Category
	Reason   ReasonCode
	// Detail is safe to show a caller. It is a sentence, not a formatted
	// internal error string.
	Detail string
	// Field names the request field at fault, for CategoryValidation. Empty
	// otherwise.
	Field string
	// wrapped is the underlying cause. It is never rendered to a caller — it
	// goes to the log, which is redacted structurally (ADR-0015 §2.2).
	wrapped error
}

// Error implements error. The message is for logs and for tests; no caller
// matches on it (ADR-0016 §2.7).
func (e *Error) Error() string {
	base := fmt.Sprintf("%s/%s: %s", e.Category, e.Reason, e.Detail)
	if e.wrapped != nil {
		return base + ": " + e.wrapped.Error()
	}
	return base
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.wrapped }

// Retryable reports whether retrying is safe.
func (e *Error) Retryable() bool { return e.Category.Retryable() }

// New builds an Error. The reason code must be registered; an unregistered one
// is a defect, so the returned error is CategoryInternal rather than the
// category asked for. Failing closed here means a typo in a reason code shows
// up as a 500 in a test rather than as an unrecognisable code at a customer.
func New(category Category, reason ReasonCode, detail string) *Error {
	if !Registered(reason) {
		return &Error{
			Category: CategoryInternal,
			Reason:   ReasonUnregisteredReasonCode,
			Detail:   "The service produced an error it cannot describe.",
			wrapped:  fmt.Errorf("errs: unregistered reason code %q", reason),
		}
	}
	return &Error{Category: category, Reason: reason, Detail: detail}
}

// Wrap builds an Error carrying a cause.
func Wrap(err error, category Category, reason ReasonCode, detail string) *Error {
	e := New(category, reason, detail)
	if e.Reason == ReasonUnregisteredReasonCode {
		return e
	}
	e.wrapped = err
	return e
}

// Invalid builds a CategoryValidation error naming the field at fault.
func Invalid(field string, reason ReasonCode, detail string) *Error {
	e := New(CategoryValidation, reason, detail)
	e.Field = field
	return e
}

// CategoryOf reports the category of any error, defaulting to CategoryInternal.
// An error that arrived from outside the estate — a driver, the standard
// library — is a defect until something classifies it, and CategoryInternal is
// the honest default.
func CategoryOf(err error) Category {
	var e *Error
	if errors.As(err, &e) {
		return e.Category
	}
	return CategoryInternal
}

// ReasonOf reports the reason code of any error.
func ReasonOf(err error) ReasonCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return ReasonInternal
}

// IsCategory reports whether err is of a given category. It is the supported
// way to branch on an error.
func IsCategory(err error, c Category) bool { return CategoryOf(err) == c }
