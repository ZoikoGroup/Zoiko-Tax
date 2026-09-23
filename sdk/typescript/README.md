# @zoikotax/sdk

The TypeScript client for the ZoikoTax v1 API.

Types are generated from [the contract](../../contracts/openapi/ztax.v1.yaml); the client is a thin typed layer over `fetch` with **no runtime dependencies**.

```bash
npm ci
npm run check   # regenerate types and fail on drift, typecheck, test
```

## Using it

```ts
import { ZoikoTaxClient } from "@zoikotax/sdk";

const client = new ZoikoTaxClient({ baseUrl: "https://eu-west-1.zoikotax.com" });

const result = await client.getCapabilities();
if (!result.ok) {
  console.error(result.error.reasonCode, result.error.requestId);
} else if (!result.data.authoritative) {
  // Before A4 no deployment may produce authoritative fiscal output. Figures
  // are advisory and must not be filed.
}
```

## Three things this SDK deliberately does not do

**It does not throw on a 4xx.** Every method returns `{ ok: true, data }` or `{ ok: false, error }`. The field to branch on is `error.reasonCode` — a closed, registered vocabulary that means the same thing in an error, in a decision and in evidence ([ADR-0016](../../../adr/ADR-0016-error-model.md) §2.4). Never match on `title` or `detail`; both may be reworded without notice.

```ts
const result = await client.createUser({ email, displayName });
if (!result.ok && result.error.reasonCode === "ALREADY_EXISTS") { … }
```

`unwrap()` is there for callers who prefer exceptions. It is a free function rather than a client option, so the choice is visible at the call site.

**It does not retry.** `error.retryable` says whether retrying unchanged could succeed, and `error.retryAfter` says when. The caller decides. An SDK that retried on its own would, on the endpoints this surface is about to grow, submit a transaction twice — and what makes that safe is an `Idempotency-Key` the caller chose ([ADR-0013](../../../adr/ADR-0013-idempotency.md)), not a backoff this library picked.

**It does not handle tokens.** The session is an opaque `HttpOnly` cookie a browser sends automatically ([ADR-0020](../../../adr/ADR-0020-tenant-identity-and-authentication.md)). There is nothing to store, nothing to refresh and nothing to leak, so there is no credential store here — the client sets `credentials: "include"` and gets out of the way.

## Two error types, and why

| Type | Means |
|---|---|
| `ZoikoTaxError` | The service answered. It carries the Problem Details document whole |
| `ZoikoTaxTransportError` | The service did not answer, or something between you and it did — a DNS failure, a proxy returning HTML, a cancelled request |

They are separate because the caller's options differ, and because attributing a load balancer's 502 to the service would give it a reason code nobody registered.

## Fiscal amounts

Every fiscal amount in this API is a **string** in canonical decimal form, such as `"12.50"`. Never parse one into a `number`:

```ts
Number("0.1") + Number("0.2")   // 0.30000000000000004
```

JavaScript has no decimal type, so a browser-side subtotal is a binary-float tax calculation with no rounding policy, no evidence record and no replay ([ADR-0019](../../../adr/ADR-0019-frontend-stack-deferred.md) C1). Amounts arrive as strings and are displayed; arithmetic on them happens in a cell, under a rounding policy that came from signed content.

This version of the surface carries no fiscal amounts. The rule is stated because it governs every addition to it.

## How the types stay true

```
contracts/openapi/ztax.v1.yaml  ──downgrade──►  export/ztax.v1.3.1.yaml
                                                          │
                                            openapi-typescript
                                                          ▼
                                                   src/schema.ts   (committed)
                                                          │
                                                   src/index.ts    (hand-written)
```

`src/schema.ts` is generated and committed, and `npm run generate:check` regenerates it and fails on any diff. A contract change that this SDK has not been regenerated for is a build failure rather than a discovery.

`src/index.ts` is hand-written and is the only file to edit. It is typed *against* the generated types, so a request body whose shape changed in the contract stops compiling here.
