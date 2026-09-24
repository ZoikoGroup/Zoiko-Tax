# ZoikoTax.Sdk

The .NET client for the ZoikoTax v1 API.

Model types are generated from [the contract](../../contracts/openapi/ztax.v1.yaml); the client is a thin layer over `HttpClient`, targets `net8.0`, and has **no runtime package dependencies** — `System.Net.Http` and `System.Text.Json` are in the shared framework.

```bash
sh scripts/check.sh          # regenerate models and fail on drift, build with warnings as errors, test
sh scripts/check-docker.sh   # the same, inside the pinned SDK image, for a machine with no SDK
```

The SDK version is pinned in `global.json`, the generator in `.config/dotnet-tools.json`, and the test packages' resolved graph in `packages.lock.json` (restored with `--locked-mode`).

## Using it

```csharp
using ZoikoTax.Sdk;

using var client = new ZoikoTaxClient(new Uri("https://eu-west-1.zoikotax.com"));

var result = await client.GetCapabilitiesAsync(cancellationToken);
if (result.Error is ZoikoTaxError error)
{
    logger.LogError("{Reason} {RequestId}", error.ReasonCode, error.RequestId);
}
else if (!result.IsOk)
{
    // ZoikoTaxTransportError: the service was not reached, or something in
    // between answered. There is no reason code, because the service gave none.
}
else if (!result.Data.Authoritative)
{
    // Before A4 no deployment may produce authoritative fiscal output. Figures
    // are advisory and must not be filed.
}
```

Every method is `async`, takes a `CancellationToken` last, and returns a `Result<T>` — or a `Result` for the operations that answer `204` — rather than throwing on anything the service sends.

| Method | Operation |
|---|---|
| `GetCapabilitiesAsync` | `GET /v1/capabilities` |
| `SignInAsync` · `SignOutAsync` · `GetSessionAsync` · `ChangePasswordAsync` | `/v1/auth/…` |
| `GetTenantAsync` | `GET /v1/admin/tenant` |
| `ListUsersAsync` · `CreateUserAsync` · `SetUserStatusAsync` · `GrantRoleAsync` · `RevokeRoleAsync` | `/v1/admin/users/…` |
| `ListSessionsAsync` · `RevokeSessionAsync` | `/v1/admin/sessions/…` |
| `ListAuditAsync` | `GET /v1/admin/audit` |

## Three things this SDK deliberately does not do

**It does not throw on a 4xx.** Every method returns a result with `IsOk`, `Data` and `Error`. The field to branch on is `ReasonCode` — a closed, registered vocabulary that means the same thing in an error, in a decision and in evidence ([ADR-0016](../../../adr/ADR-0016-error-model.md) §2.4). Never match on `Title`, `Detail` or `Message`; all of them may be reworded without notice.

```csharp
var result = await client.CreateUserAsync(new CreateUserRequest { Email = email, DisplayName = name });
if (result.Error is ZoikoTaxError { ReasonCode: "ALREADY_EXISTS" }) { … }
```

`ReasonCode` is a `string`, not an enum. The register grows by addition ([ADR-0010](../../../adr/ADR-0010-api-contract-pipeline.md) §2.6), and an enum would make an SDK release a prerequisite for every new code.

`Unwrap()` is there for callers who prefer exceptions. It is a method on the result rather than a client option, so the client has one behaviour and the choice is visible at the call site:

```csharp
var capabilities = (await client.GetCapabilitiesAsync()).Unwrap();   // throws ZoikoTaxError or ZoikoTaxTransportError
```

Both derive from `ZoikoTaxException`, so an unwrapping caller can write one `catch`.

**It does not retry.** `Retryable` says whether retrying unchanged could succeed, and `RetryAfter` says when. The caller decides. An SDK that retried on its own would, on the endpoints this surface is about to grow, submit a transaction twice — and what makes that safe is an `Idempotency-Key` the caller chose ([ADR-0013](../../../adr/ADR-0013-idempotency.md)), not a backoff this library picked. For the same reason, do not put a resilience handler (Polly, `AddStandardResilienceHandler`) in front of this client: it would be this library retrying, with extra steps.

**It does not handle tokens.** The session is an opaque `HttpOnly` cookie ([ADR-0020](../../../adr/ADR-0020-tenant-identity-and-authentication.md)). There is nothing to store, nothing to refresh and nothing to leak, so there is no credential store here. The default handler keeps a `CookieContainer` — for a .NET process, that is what `credentials: "include"` is for a browser — and it is exposed as `client.Cookies` for a caller that needs to share or persist a session.

If you supply your own `HttpClient` or `HttpMessageHandler` (for instrumentation, or a test), it must keep cookies. One from `IHttpClientFactory` does not by default; without it every call after sign-in is `UNAUTHENTICATED`, for a reason that looks like a server problem.

## Two error types, and why

| Type | Means |
|---|---|
| `ZoikoTaxError` | The service answered. It carries the Problem Details document whole — `Problem`, and `RawProblem` for any extension this release does not model |
| `ZoikoTaxTransportError` | The service did not answer, or something between you and it did — a DNS failure, a timeout, a proxy returning HTML, a body that is not JSON. The cause is `InnerException` |

They are separate because the caller's options differ, and because attributing a load balancer's 502 to the service would give it a reason code nobody registered.

Cancellation through your own `CancellationToken` is neither: it throws `OperationCanceledException`, as it does everywhere else in .NET, because you asked for it. A timeout set through `ZoikoTaxClientOptions.Timeout` is a `ZoikoTaxTransportError`.

## One client per cell

A cell is a residency boundary: data never leaves its region. A client that transparently failed over to another cell would be moving a tenant's data across one, so a `ZoikoTaxClient` is bound to one base URL and never changes it. The client is thread-safe; create one per cell and reuse it.

## Fiscal amounts

Every fiscal amount in this API is a **string** in canonical decimal form, such as `"12.50"`, and it is a `string` in these types. Never parse one into a `double` or a `float`:

```csharp
0.1 + 0.2 == 0.3   // false
```

If you must hold one as a number, use `decimal`, parsed with the invariant culture so that a machine configured for `de-DE` does not read `"12.50"` as twelve hundred and fifty:

```csharp
var amount = decimal.Parse(line.Amount, NumberStyles.AllowDecimalPoint | NumberStyles.AllowLeadingSign, CultureInfo.InvariantCulture);
```

Even then, arithmetic on amounts belongs in a cell, under a rounding policy that came from signed content and with an evidence record behind it ([ADR-0002](../../../adr/ADR-0002-decimal-rounding-context-policy.md)). A client-side subtotal has neither. Display amounts; do not recompute them.

Timestamps are strings for a related reason. A `Timestamp` is RFC 3339 with exactly six fractional digits ([ADR-0011](../../../adr/ADR-0011-canonicalization-digest-and-evidence-sealing.md) §2.1 P2); a `DateTimeOffset` would re-encode it differently on the way back out, and two encodings of one instant are two digests. Parse one with `DateTimeOffset.Parse(value, CultureInfo.InvariantCulture)` when you need to compare it.

This version of the surface carries no fiscal amounts. The rule is stated because it governs every addition to it.

## How the types stay true

```
contracts/openapi/ztax.v1.yaml  ──downgrade──►  export/ztax.v1.3.1.yaml
                                                          │
                                              NSwag 14.7.1 (pinned)
                                                          ▼
                                     src/ZoikoTax.Sdk/Generated/Models.g.cs   (committed)
                                                          │
                                     src/ZoikoTax.Sdk/ZoikoTaxClient.cs       (hand-written)
```

`Models.g.cs` is generated and committed, and `scripts/check.sh` regenerates it and fails on any diff. A contract change that this SDK has not been regenerated for is a build failure rather than a discovery. Every generator option is spelled out in `scripts/generate.sh`, with the reason for each; the output carries the generator's pinned version and no timestamp, so the same contract always produces the same bytes.

The hand-written files are `ZoikoTaxClient.cs`, `ZoikoTaxClientOptions.cs`, `Result.cs`, `Errors.cs`, `Wire.cs` and `Lists.cs` — the last because the contract declares the three list responses inline, and NSwag names inline schemas by position (`Response2`), which would silently change meaning if an operation were added above them. They are the only hand-written shapes, and they are declared over the generated item types.

Enums are written by name, and a test checks that every generated name is its contract value, because `System.Text.Json` on .NET 8 ignores `[EnumMember]`. Unknown members in a response are ignored rather than rejected, because `/v1` grows by addition.
