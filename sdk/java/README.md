# com.zoikotax:zoikotax-sdk

The Java client for the ZoikoTax v1 API, usable from Kotlin. Java 17 or later.

Models are generated from [the contract](../../contracts/openapi/ztax.v1.yaml); the client is a thin typed layer over `java.net.http.HttpClient`.

```bash
mvn -B verify                          # regenerate models and fail on drift, compile, test
mvn -B -Pregenerate process-sources    # after a contract change: rewrite the committed models
```

Both read `../../contracts/openapi/export/ztax.v1.3.1.yaml`, so they run from a full checkout of the repository, not from this directory alone.

## Using it

```java
import com.zoikotax.sdk.ZoikoTaxClient;
import com.zoikotax.sdk.ZoikoTaxError;

var client = new ZoikoTaxClient("https://eu-west-1.zoikotax.com");

var result = client.getCapabilities();
if (result.errorOrNull() instanceof ZoikoTaxError e) {
  log.error("{} {}", e.getReasonCode(), e.getRequestId());
} else if (!result.unwrap().getAuthoritative()) {
  // Before A4 no deployment may produce authoritative fiscal output. Figures
  // are advisory and must not be filed.
}
```

The same from Kotlin:

```kotlin
import com.zoikotax.sdk.Result          // explicit, so it wins over kotlin.Result
import com.zoikotax.sdk.ZoikoTaxClient
import com.zoikotax.sdk.ZoikoTaxError
import com.zoikotax.sdk.ZoikoTaxTransportError

val client = ZoikoTaxClient("https://eu-west-1.zoikotax.com")

when (val result = client.getCapabilities()) {
  is Result.Ok -> if (!result.value!!.authoritative) { /* advisory only */ }
  is Result.Err -> when (val e = result.error) {
    is ZoikoTaxError -> log.error("${e.reasonCode} ${e.requestId}")
    is ZoikoTaxTransportError -> log.error("unreachable: ${e.message}")
  }
}
```

Both `when`s are exhaustive with no `else`, because `Result` and the error hierarchy are sealed. Getters carry `jakarta.annotation.Nullable` / `Nonnull`, which the Kotlin compiler reads as real nullability: `e.requestId` is a `String?`. `KotlinUsageTest.kt` is compiled on every build, so this stays true.

Every method blocks the calling thread. On Java 21, call it from a virtual thread; from a coroutine, wrap it in `withContext(Dispatchers.IO)`. An interrupt ends the wait and comes back as a `ZoikoTaxTransportError`, with the thread's interrupt flag left set.

## Three things this SDK deliberately does not do

**It does not throw on a 4xx.** Every method returns a `Result<T>`: `Result.Ok` with the value, or `Result.Err` with the reason. The field to branch on is `getReasonCode()` — a closed, registered vocabulary that means the same thing in an error, in a decision and in evidence ([ADR-0016](../../../adr/ADR-0016-error-model.md) §2.4). It is a `String`, not an enum, because the register grows by addition and an enum would make every new code a breaking change. Never match on the title, the detail or `getMessage()`; all may be reworded without notice.

```java
var result = client.createUser(new CreateUserRequest().email(email).displayName(name));
if (result.errorOrNull() instanceof ZoikoTaxError e && e.getReasonCode().equals("ALREADY_EXISTS")) { … }
```

`unwrap()` is there for callers who prefer exceptions: it returns the value or throws the `ZoikoTaxError` / `ZoikoTaxTransportError` as it is. It is a method on the result rather than a client option, so the client has one behaviour and the choice is visible at the call site.

**It does not retry.** `isRetryable()` says whether retrying unchanged could succeed, and `getRetryAfter()` says when. The caller decides. An SDK that retried on its own would, on the endpoints this surface is about to grow, submit a transaction twice — and what makes that safe is an `Idempotency-Key` the caller chose ([ADR-0013](../../../adr/ADR-0013-idempotency.md)), not a backoff this library picked.

**It does not handle tokens.** The session is an opaque `HttpOnly` cookie ([ADR-0020](../../../adr/ADR-0020-tenant-identity-and-authentication.md)). There is nothing to store, nothing to refresh and nothing to leak, so there is no credential store here. A browser keeps the cookie for you and a JVM does not, so each client gets its own `CookieManager` — the equivalent of TypeScript's `credentials: "include"`. One jar per client, because a cookie jar shared between clients is a session shared between cells.

## One client per cell

A cell is a residency boundary: a tenant's data never leaves its region. So a client is built for one cell's URL, never fails over to another, and **does not follow redirects** — a redirect to another origin would carry the request, cookie included, out of the cell it was meant for. A 3xx comes back as a `ZoikoTaxTransportError` with its status.

## Two error types, and why

| Type | Means |
|---|---|
| `ZoikoTaxError` | The service answered. It carries the Problem Details document whole: `getProblem()` as the generated model, `getProblemDocument()` as the JSON received, including any extension this release does not model |
| `ZoikoTaxTransportError` | The service did not answer, or something between you and it did — a DNS failure, a timeout, an interrupt, a proxy returning HTML |

They are separate because the caller's options differ, and because attributing a load balancer's 502 to the service would give it a reason code nobody registered. Both extend the sealed `ZoikoTaxFailure`.

## Fiscal amounts

Every fiscal amount in this API is a **string** in canonical decimal form, such as `"12.50"`, and the generated models keep it a `String`. Never parse one into a `double` or a `float`:

```java
0.1 + 0.2   // 0.30000000000000004
```

Where you must compute with one — to reconcile against a ledger, say — use `new BigDecimal(amount)`, which keeps the scale: `new BigDecimal("1.50")` and `new BigDecimal("1.5")` are different values to `equals`, as the contract intends, and the same to `compareTo`. Never `BigDecimal.valueOf(double)` or `new BigDecimal(double)`; by then the damage is done. Arithmetic that produces a figure you will file happens in a cell, under a rounding policy that came from signed content ([ADR-0002](../../../adr/ADR-0002-decimal-rounding-context-policy.md)), not in a client.

Timestamps are strings too. The contract fixes their encoding exactly — six fractional digits and a literal `Z` ([ADR-0011](../../../adr/ADR-0011-canonicalization-digest-and-evidence-sealing.md) §2.1 P2) — because two encodings of one instant digest differently, and an `OffsetDateTime` would re-encode them. Parse with `Instant.parse` when you need an instant; pass the string through when you need the value.

This version of the surface carries no fiscal amounts. The rule is stated because it governs every addition to it.

## Additions do not break you

`/v1` changes only by addition ([ADR-0010](../../../adr/ADR-0010-api-contract-pipeline.md) §2.6), and this client is built to survive one: unknown response fields are ignored, and an enum value this release predates reads as `UNKNOWN_DEFAULT_OPEN_API` rather than failing the whole response.

## What it depends on

| Dependency | Why |
|---|---|
| `com.fasterxml.jackson.core:jackson-databind` | The JDK has no JSON parser. The generated models are Jackson-annotated |
| `jakarta.annotation:jakarta.annotation-api` | Nullability annotations only, no runtime behaviour. They are what give Kotlin real nullable types |

HTTP is the JDK's own `java.net.http`. Nothing else is on the runtime classpath. Supplying a `Transport` — one method, so a lambda — is how a test drives the client without a network, and how a server-side caller substitutes an instrumented client; `Transport.of(httpClient)` wraps an `HttpClient` you configured, which must have a cookie handler.

## How the types stay true

```
contracts/openapi/ztax.v1.yaml  ──downgrade──►  export/ztax.v1.3.1.yaml
                                                          │
                                        openapi-generator 7.25.0 (models only)
                                                          ▼
                                   src/main/java/com/zoikotax/sdk/model/   (committed)
                                                          │
                                   src/main/java/com/zoikotax/sdk/*.java   (hand-written)
```

`com.zoikotax.sdk.model` is generated and committed. Every build generates it again into `target/`, and `GeneratedModelsAreCurrentTest` fails on any difference in either direction — a changed file, a missing one, or one the contract no longer produces. A contract change this SDK has not been regenerated for is a build failure rather than a discovery. The output is deterministic: no timestamps, and the only version it names is the generator's, which is pinned in `pom.xml` and changes only as a reviewed edit.

The rest of `com.zoikotax.sdk` is hand-written and is the only code to edit. It is typed *against* the generated models, so a request body whose shape changed in the contract stops compiling here.
