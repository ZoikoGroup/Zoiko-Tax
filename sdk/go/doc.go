// Package zoikotax is the Go client for the ZoikoTax v1 API.
//
// The types in types.gen.go are generated from the contract and are not
// edited; `make generate-check` fails the build on any drift, which is the same
// mechanism ADR-0010 §2.1 applies to the generated server. What is hand-written
// is client.go and errors.go: a thin layer over net/http, with no dependencies
// outside the standard library.
//
// Three decisions are worth stating, because each is a thing an SDK usually
// does that this one deliberately does not.
//
// # An error is a value, and a 4xx is not a panic
//
// Every method returns (T, error) or error. A failed call's error is either a
// *ZoikoTaxError, which carries the Problem Details document whole, or a
// *TransportError, which means the service did not answer. Both are found with
// errors.As. The field to branch on is ZoikoTaxError.ReasonCode — a closed,
// registered vocabulary that means the same thing in an error, in a decision
// and in evidence (ADR-0016 §2.4). Nothing here matches on a title or a detail,
// and neither should a caller: both may be reworded without notice.
//
// # Nothing retries by itself
//
// ZoikoTaxError.Retryable says whether retrying unchanged could succeed, and
// the caller decides. An SDK that retried on its own would, on the endpoints
// this surface is about to grow, submit a transaction twice — and the thing
// that makes that safe is an Idempotency-Key the caller chose (ADR-0013), not
// a backoff this library picked.
//
// # No token handling
//
// The session is an opaque HttpOnly cookie (ADR-0020). There is nothing to
// store, nothing to refresh and nothing to leak, so this package has no
// credential store. The default *http.Client carries a cookie jar, which is the
// Go equivalent of a browser sending the cookie automatically; SignIn fills it
// and every later call on the same Client presents it.
package zoikotax
