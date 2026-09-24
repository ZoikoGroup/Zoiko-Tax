# zoikotax-sdk

The Python client for the ZoikoTax v1 API.

Types are generated from [the contract](../../contracts/openapi/ztax.v1.yaml); the client is a thin typed layer over `urllib.request` with **no runtime dependencies**. Python 3.10 or later.

```bash
python -m pip install -r requirements-dev.txt
python scripts/check.py   # regenerate types and fail on drift, typecheck, test
```

## Using it

```python
from zoikotax import ZoikoTaxClient

client = ZoikoTaxClient("https://eu-west-1.zoikotax.com")

result = client.get_capabilities()
if not result.ok:
    print(result.error, getattr(result.error, "request_id", None))
elif not result.data["authoritative"]:
    # Before A4 no deployment may produce authoritative fiscal output. Figures
    # are advisory and must not be filed.
    ...
```

A result is `Ok(data)` or `Err(error)`. Branching on `result.ok` narrows it for a type checker, and so does `match`:

```python
from zoikotax import Err, Ok, ZoikoTaxError

match client.get_tenant():
    case Ok(tenant):
        print(tenant["residencyRegion"])
    case Err(ZoikoTaxError(reason_code="UNAUTHENTICATED")):
        sign_in_again()
    case Err(error):
        log.warning("tenant lookup failed: %r", error)
```

Response bodies are the generated `TypedDict`s — plain `dict`s at runtime, typed for a checker — so a field is `tenant["residencyRegion"]`, spelled as the contract spells it.

## Three things this SDK deliberately does not do

**It does not raise on a 4xx.** Every method returns `Ok` or `Err`. The field to branch on is `error.reason_code` — a closed, registered vocabulary that means the same thing in an error, in a decision and in evidence ([ADR-0016](../../../adr/ADR-0016-error-model.md) §2.4). Never match on `title` or `detail`; both may be reworded without notice.

```python
result = client.create_user({"email": email, "displayName": display_name})
if isinstance(result, Err) and isinstance(result.error, ZoikoTaxError) \
        and result.error.reason_code == "ALREADY_EXISTS":
    ...
```

`unwrap()` is there for callers who prefer exceptions. It is a free function rather than a client option, so the choice is visible at the call site.

**It does not retry.** `error.retryable` says whether retrying unchanged could succeed, and `error.retry_after` says when. The caller decides. An SDK that retried on its own would, on the endpoints this surface is about to grow, submit a transaction twice — and what makes that safe is an `Idempotency-Key` the caller chose ([ADR-0013](../../../adr/ADR-0013-idempotency.md)), not a backoff this library picked.

**It does not handle tokens.** The session is an opaque `HttpOnly` cookie ([ADR-0020](../../../adr/ADR-0020-tenant-identity-and-authentication.md)). There is nothing to store, nothing to refresh and nothing to leak, so there is no credential store here. Outside a browser something still has to send the cookie back, so each client's default transport keeps an in-memory `http.cookiejar.CookieJar` — the counterpart of the TypeScript SDK's `credentials: "include"`. It is never written to disk, and it is never shared: two clients are two sessions.

## One client per cell

A cell is a residency boundary. Data never leaves its region, and a client that transparently failed over to another cell would be moving a tenant's data across one. So a client takes one base URL and never talks to another host — which is also why the default transport **does not follow redirects**. The contract declares none, and following one would carry a request body, and possibly the session, somewhere nobody reviewed. A 3xx comes back as a `ZoikoTaxTransportError` carrying its status.

The base URL must be `http` or `https`. `urllib` will otherwise open `file:`, `ftp:` and `data:` URLs, and a base URL comes from configuration.

## Two error types, and why

| Type | Means |
|---|---|
| `ZoikoTaxError` | The service answered. It carries the Problem Details document whole, as `problem` |
| `ZoikoTaxTransportError` | The service did not answer, or something between you and it did — a DNS failure, a timeout, a proxy returning HTML, a redirect |

They are separate because the caller's options differ, and because attributing a load balancer's 502 to the service would give it a reason code nobody registered. A transport error keeps what went wrong underneath as `cause`, which is also its `__cause__`, so a traceback shows it.

## Fiscal amounts

Every fiscal amount in this API is a **string** in canonical decimal form, such as `"12.50"`. Never parse one into a `float`:

```python
float("0.1") + float("0.2")   # 0.30000000000000004
```

A float subtotal is a binary-float tax calculation with no rounding policy, no evidence record and no replay. Python does have a decimal type, and `Decimal("12.50")` is exact — but it is still arithmetic outside a cell, under a rounding context this process chose rather than one that came from signed content ([ADR-0002](../../../adr/ADR-0002-decimal-rounding-context-policy.md)). Amounts arrive as strings and are displayed or passed on as strings; arithmetic on them happens in a cell. Trailing zeros are significant: `"1.50"` and `"1.5"` are different assertions about precision, and `Decimal` preserves that where `float` cannot.

This version of the surface carries no fiscal amounts. The rule is stated because it governs every addition to it.

## Testing against it

The client sends every request through a *transport*: a callable taking an `HttpRequest` and returning an `HttpResponse`, whatever the status, and raising only when there is no response. Supplying one is how a test drives the client without a network, and how a server-side caller substitutes an instrumented client:

```python
def transport(request: HttpRequest) -> HttpResponse:
    return HttpResponse(204)

client = ZoikoTaxClient("https://eu-west-1.zoikotax.com", transport=transport)
```

The default is `UrllibTransport`, which accepts a `CookieJar` of your own if you need to inspect it.

## How the types stay true

```
contracts/openapi/ztax.v1.yaml  ──downgrade──►  export/ztax.v1.3.1.yaml
                                                          │
                                           datamodel-code-generator
                                                          ▼
                                            src/zoikotax/schema.py    (committed)
                                                          │
                                            src/zoikotax/__init__.py  (hand-written)
```

`src/zoikotax/schema.py` is generated and committed, and `scripts/check.py` regenerates it and fails on any diff. A contract change that this SDK has not been regenerated for is a build failure rather than a discovery. The generator is pinned exactly in `requirements-dev.txt` and `scripts/generate.py` refuses to run under any other version, because a different generator writes a different file.

`src/zoikotax/__init__.py` is hand-written and is the only file to edit. It is typed *against* the generated types and checked by `mypy --strict` at Python 3.10, so a request body whose shape changed in the contract stops type-checking here.

### Why `TypedDict`s

The generated types are `TypedDict`s rather than pydantic models or dataclasses. A `TypedDict` is a `dict` at runtime: it describes the JSON as it arrives, converts nothing and costs nothing, which is what keeps the runtime dependency list empty. The trade is that nothing validates a response at runtime — which is the same trade the TypeScript SDK makes, and the right one for a client: the server is held to the contract by its own tests, and a client that rejected a response carrying a field it predates would break on the additive changes ADR-0010 §2.6 makes normal.

Wire formats stay strings for the same reason. A timestamp is a `str`, not a `datetime`, because nothing would convert it, and because the contract's exact six-digit form is what a digest was computed over.

One import is redirected. The generator would take `NotRequired` from `typing_extensions`, since it reached `typing` only in 3.11; `src/zoikotax/_compat.py` supplies it from the standard library instead, so 3.10 needs no third-party package for the sake of one annotation.
