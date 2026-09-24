# github.com/zoikogroup/zoikotax/sdk/go

The Go client for the ZoikoTax v1 API.

Types are generated from [the contract](../../contracts/openapi/ztax.v1.yaml); the client is a thin layer over `net/http` with **no dependencies outside the standard library**. It is its own module, separate from the backend's: nothing here imports the server, and nothing that imports this inherits the server's module graph.

```bash
make check   # gofmt, vet, go mod tidy, stdlib-only, regenerate models and fail on drift, test -race, lint
```

## Using it

```go
import zoikotax "github.com/zoikogroup/zoikotax/sdk/go"

client, err := zoikotax.NewClient("https://eu-west-1.zoikotax.com")
if err != nil { … }

caps, err := client.GetCapabilities(ctx)
var zerr *zoikotax.ZoikoTaxError
switch {
case errors.As(err, &zerr):
	slog.Error("the cell refused", "reason", zerr.ReasonCode, "request", zerr.RequestID)
case err != nil:
	slog.Error("the cell was not reached", "err", err)
case !caps.Authoritative:
	// Before A4 no deployment may produce authoritative fiscal output.
	// Figures are advisory and must not be filed.
}
```

Every method takes a `context.Context` first. Its deadline and cancellation are the per-call timeout; `WithTimeout` sets one for every call as well, and the earlier wins.

## Three things this SDK deliberately does not do

**It does not panic, or hide an error, on a 4xx.** Every method returns `(T, error)` or `error`, and a failure is a `*ZoikoTaxError` or a `*TransportError`, found with `errors.As`. The field to branch on is `ReasonCode` — a closed, registered vocabulary that means the same thing in an error, in a decision and in evidence ([ADR-0016](../../../adr/ADR-0016-error-model.md) §2.4). Never match on `Problem.Title`, `Problem.Detail` or `Error()`; all three may be reworded without notice.

```go
_, err := client.CreateUser(ctx, zoikotax.CreateUserRequest{Email: email, DisplayName: name})
var zerr *zoikotax.ZoikoTaxError
if errors.As(err, &zerr) && zerr.ReasonCode == "ALREADY_EXISTS" { … }
```

`ReasonCode` is a `string`, not a closed set of constants: the register grows by addition ([ADR-0010](../../../adr/ADR-0010-api-contract-pipeline.md) §2.6), and a closed set would make an SDK release a prerequisite for every new code.

**It does not retry.** `Retryable` says whether retrying unchanged could succeed, and `RetryAfter` says when. The caller decides. An SDK that retried on its own would, on the endpoints this surface is about to grow, submit a transaction twice — and what makes that safe is an `Idempotency-Key` the caller chose ([ADR-0013](../../../adr/ADR-0013-idempotency.md)), not a backoff this library picked.

**It does not handle tokens.** The session is an opaque `HttpOnly` cookie ([ADR-0020](../../../adr/ADR-0020-tenant-identity-and-authentication.md)). There is nothing to store, nothing to refresh and nothing to leak, so there is no credential store here. The default `*http.Client` has a cookie jar — the Go equivalent of a browser sending the cookie by itself — so `SignIn` fills it and every later call on the same `Client` presents it. Two `Client`s are two sessions.

If you inject your own client with `WithHTTPClient`, **give it a `Jar`**. Without one, sign-in succeeds and every call after it is `UNAUTHENTICATED`, for a reason that looks like a server problem. `WithTransport` keeps the default jar and swaps only the `http.RoundTripper`.

## One client per cell

A cell is a residency boundary: data never leaves its region. A client that transparently failed over to another cell would be moving a tenant's data across one, so a `Client` has exactly one base URL and no fallback.

## Two error types, and why

| Type | Means |
|---|---|
| `*ZoikoTaxError` | The service answered. It carries the Problem Details document whole — decoded in `Problem`, and byte for byte in `Raw` for an extension this release predates |
| `*TransportError` | The service did not answer, or something between you and it did — a DNS failure, a cancelled context, a proxy returning HTML. `Status` is the HTTP status where there was a response, and `Unwrap` reaches the cause, so `errors.Is(err, context.DeadlineExceeded)` works |

They are separate because the caller's options differ, and because attributing a load balancer's 502 to the service would give it a reason code nobody registered. An error body counts as a Problem only if it carries `ztx_reason_code` as a string and `status` as a number; anything else is a `TransportError`.

## Fiscal amounts

Every fiscal amount in this API is a **string** in canonical decimal form, such as `"12.50"`, and it stays a `string` in these types. Never parse one into a `float64`:

```go
a, _ := strconv.ParseFloat("0.1", 64)
b, _ := strconv.ParseFloat("0.2", 64)
fmt.Println(a + b) // 0.30000000000000004
```

A client-side subtotal is a binary-float tax calculation with no rounding policy, no evidence record and no replay. Amounts arrive as strings and are displayed or passed on; arithmetic on them happens in a cell, under a rounding policy that came from signed content ([ADR-0002](../../../adr/ADR-0002-decimal-rounding-context-policy.md)). Trailing zeros are significant: `"1.50"` and `"1.5"` are different assertions about precision.

This version of the surface carries no fiscal amounts. The rule is stated because it governs every addition to it.

## Timestamps and recorded bytes

`Timestamp` is `time.Time`. Decoding is exact — the contract's six fractional digits fit in a `time.Time` with room to spare — but `encoding/json` re-encodes with *variable* precision, so `2026-09-23T12:00:00.000000Z` comes back out as `2026-09-23T12:00:00Z`. That is the same instant and not the same bytes, and the contract fixes the bytes because two encodings of one instant must not produce two digests ([ADR-0011](../../../adr/ADR-0011-canonicalization-digest-and-evidence-sealing.md) §2.1). No request in this version carries a timestamp; if you forward one, format it with `t.UTC().Format("2006-01-02T15:04:05.000000Z")`.

`AuditRecord.Detail` is a string for the same reason: it is the exact canonical JSON that was recorded and digested. Pass it on as it is (§2.7).

## How the types stay true

```
contracts/openapi/ztax.v1.yaml  ──downgrade──►  export/ztax.v1.3.1.yaml
                                                          │
                                           oapi-codegen v2.8.0 (models only)
                                                          ▼
                                                   types.gen.go   (committed)
                                                          │
                                        client.go, errors.go   (hand-written)
```

`types.gen.go` is generated and committed, and `make generate-check` regenerates it into a temporary directory and fails on any difference. A contract change this SDK has not been regenerated for is a build failure rather than a discovery. The generator is pinned in the `Makefile` and run with `go run …@v2.8.0`, so it never enters `go.mod`.

Three settings in [`oapi-codegen.yaml`](oapi-codegen.yaml) keep the module on the standard library, and each is there for a reason:

- **`format: email` is mapped to `string`.** The default is `openapi_types.Email`, which would put `github.com/oapi-codegen/runtime` into every integration's module graph for a string with a validating `UnmarshalJSON` — and a client that validated addresses would reject a response the contract later relaxes.
- **The imports template lists only the standard library.** The stock one lists the runtime and relies on `goimports` to prune it. With this one, a future contract construct that needs the runtime fails to compile here, in this module's CI, instead of quietly adding a dependency.
- **`make deps-check` fails if `go list -m all` names anything but this module.** The two settings above make a dependency unlikely; this makes it impossible to merge.

`client.go` and `errors.go` are hand-written and are the only files to edit. They are written *against* the generated types, so a request body whose shape changed in the contract stops compiling here.

## For CI

The module needs Go and nothing else; lint uses golangci-lint v2.

```bash
cd sdk/go
make fmt-check vet tidy-check deps-check generate-check test   # golang:1.25
golangci-lint run ./...                                         # golangci/golangci-lint:v2.13.2
```
