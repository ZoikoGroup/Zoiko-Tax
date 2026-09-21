# Postman — ZoikoTax API

One file — `ZoikoTax.postman_collection.json`: 20 requests across 8 folders, with the local cell's values carried as collection variables. Import it and run; there is no environment to select.

`baseUrl` defaults to `http://localhost:8080`. Point the collection at another cell by editing that variable, or by passing `--env-var baseUrl=...` to newman. The rest — `tenantId`, `sellerLegalEntityId`, `decisionId`, `jobId` and the computed timestamps — are collection state that the requests read and write as they run.

A separate environment file used to hold the same five values with the same defaults. An import that needs two files and a dropdown selection is one more thing to get wrong, for no benefit.

## Read this before you run it

**Only `/healthz` and `/readyz` exist today.** Every `/v1` request 404s until W2 lane K builds the surface. The collection-level test script detects a 404 on a `/v1` path and skips the contract assertions for that request rather than drowning the run in red — so a fresh run against the current binary should show two passing folders and eighteen skipped requests, not eighteen failures.

**The payloads are illustrative.** `ZTAX-INT-001` was not supplied to the specification pipeline (Build Plan §2), so request bodies are derived from the domain model in the ADRs, not from a published contract. Field names will change.

**When `contracts/openapi` lands, regenerate this from the contract.** Do not maintain it by hand. ADR-0010 §2.1 makes the contract the source of truth and treats drift as a build failure; a hand-edited collection is drift with extra steps.

## What is actually worth running

Folder **`07 · Contract conformance`**. Those seven requests are the point of this collection — each one *should* fail, in a specific way, and each asserts a control from the ADRs.

| Request | Asserts | ADR |
|---|---|---|
| Idempotency 1–3 | Retry returns the original response; same key with a different body is a `409`, not a silent replay | 0013 §2.4 |
| Idempotency 4 | `Idempotency-Key` is mandatory on `:commit` | 0013 §2.1 |
| **Fiscal amount as a JSON number** | `"netAmount": 45.0` is rejected, never coerced | 0010 §2.9 |
| Unknown field | Rejected, not ignored — `canon/v1` P5 | 0011 §2.1 |
| Explicit null | Rejected — "cleared" and "not supplied" are different facts | 0011 §2.1 P3 |

The JSON-number request is the one that matters most. A `2xx` there is a **Critical finding, not a test failure**: Build Plan §8 rates binary floating-point on fiscal amounts as High risk, and ADR-0011 §1.1 explains why it bites at the evidence layer too — RFC 8785 serializes numbers through IEEE 754 doubles, so an amount that is ever a JSON number has lost precision before canonicalization ever sees it.

## Cross-cutting assertions

The collection-level test script runs after **every** request and encodes controls rather than checking endpoints:

- **No fiscal amount is a JSON number** in any response. Walks the body and flags numeric values under fiscally-named keys. This is a heuristic while the schema is unpublished — tighten it to the real schema when the contract lands.
- **Errors are RFC 9457** Problem Details with `type`, `title`, `status` and `ztx_reason_code` (ADR-0016 §2.5).
- **No `UNCERTAIN_*` state appears in a 4xx or 5xx.** An uncertain submission expressed as a 5xx tells every SDK retry policy in existence to try again — and the retry files the return a second time (ADR-0016 §3.2).
- **No fiscal amount in an error body** (ADR-0016 §2.6).

## Notes on two details

**Timestamps are computed, not `{{$isoTimestamp}}`.** The collection pre-request script builds RFC 3339 UTC with exactly six fractional digits, because that is what `canon/v1` P2 requires and Postman's dynamic variable gives seconds only.

**`decisionId` and `jobId` capture themselves.** `:commit` and `POST /v1/batches` write them into collection variables, so folders 03 and 06 work after a Collection Runner pass without manual copying.

## Running headless

```bash
npm install -g newman
newman run ZoikoTax.postman_collection.json
```

Do **not** wire this into CI as the contract gate. Contract testing is tier 4 in ADR-0018 §2.5 and is generated from the contract with `oasdiff` as the release blocker. This collection is for exploration and manual verification — a second, hand-maintained source of truth about the API is exactly what ADR-0010 §3.1 exists to prevent.
