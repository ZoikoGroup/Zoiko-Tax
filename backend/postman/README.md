# Postman — ZoikoTax API

`ZoikoTax.postman_collection.json` — **generated from the contract.** 29 requests in 9 folders, with every value carried as a collection variable. Import it and run; there is no environment to select.

```bash
cd ../../contracts && npm run postman        # regenerate
cd ../../contracts && npm run postman:check  # what CI runs
```

> **Do not edit the collection by hand.** `postman:check` regenerates it and compares; a hand edit fails the build. ADR-0010 §2.1 makes the contract the source of truth and treats drift as a build failure, and a hand-maintained collection is a second, kinder statement of what the API is — which is what §3.1 exists to prevent.

The previous version of this file said to regenerate from the contract once `contracts/openapi` landed. It has landed, and this is that.

## Where each part comes from

| Part | Source |
|---|---|
| Folders `01`–`05`, `98` | [`contracts/openapi/ztax.v1.yaml`](../../contracts/openapi/ztax.v1.yaml) — operations, bodies, saved response examples |
| Folder `00` (health probes) | The generator. They are served and deliberately not in the contract |
| Folders `90`, `99` | [`contracts/postman/overlay.json`](../../contracts/postman/overlay.json) — what the contract cannot express |
| Collection scripts | [`contracts/postman/scripts/`](../../contracts/postman/scripts) — kept as JavaScript, not as a JSON array of strings |
| The run order | `FOLDERS` in [`contracts/tools/postman.mjs`](../../contracts/tools/postman.mjs) |

**The run order lives in the generator, not in the contract.** A collection runs top to bottom, so sign-in has to precede the administrative calls and sign-out has to follow them — a fact about a test run rather than about the API, and the contract has no business carrying it. Generation *fails* if a contract operation is not placed in that plan, so a new endpoint cannot be silently dropped from the collection: somebody has to decide where in the run it belongs.

## Running it

Bring the stack up first — it bootstraps the `acme` tenant and its administrator:

```bash
docker compose up --build
```

Then run the whole collection. `02 · Sign in` opens the session everything below depends on, and Postman's cookie jar carries it; there is no token to copy.

```bash
npm install -g newman
newman run ZoikoTax.postman_collection.json
```

`adminPassword` defaults to the local stack's bootstrap value. Against any other cell, set `baseUrl`, `tenantSlug`, `adminEmail` and `adminPassword`:

```bash
newman run ZoikoTax.postman_collection.json \
  --env-var baseUrl=https://eu-west-1.zoikotax.com \
  --env-var tenantSlug=acme --env-var adminEmail=… --env-var adminPassword=…
```

Three variables capture themselves as the run proceeds — `userId` from `POST /v1/admin/users`, `sessionId` from `GET /v1/admin/sessions` (a session that is *not* the caller's, so revoking it does not sign the run out), and `bundleDigest` from `/v1/capabilities`. A Collection Runner pass therefore works with no manual copying.

## The folders worth reading

**`90 · Contract conformance`** is the point of this collection. Seven requests, each sending something a well-behaved client would never send, each asserting a control — and, unlike the previous version of this collection, **each running against an endpoint that exists today**, so a green row is evidence rather than an aspiration.

| Request | Asserts | ADR |
|---|---|---|
| Unknown field | Rejected, not ignored — silently dropping it would let two different requests digest identically | 0011 P5 |
| Explicit null | Rejected — "cleared" and "not supplied" are different facts | 0011 P3 |
| Unrouted path | Still a Problem Details document, not `ServeMux`'s plain text | 0016 §2.5 |
| No session on an admin endpoint | `401` and not `403`: "we do not know who you are", not "you lack a role" | 0016 §2.5 |
| Unknown user **and** wrong password | Both `INVALID_CREDENTIALS`, and the pair asserts the codes are *identical* — the difference is an account-enumeration oracle | 0020 |
| Invented role | Refused; `Role` is a closed enum, not a string a caller may invent | — |

**`99 · Awaiting the determination surface`** records intent for what `ZTAX-INT-001` specifies and nobody has built: commit, adjust, refund and their idempotency and canonical-form behaviour. Those paths are listed in the `pendingPaths` variable, so a 404 there reports as *not implemented yet* rather than as a wall of red that hides the rows that matter. The payloads are derived from the domain model in the ADRs rather than from a published contract, so field names will change.

The request that matters most is still **`Canonical form · a fiscal amount sent as a JSON number`**. A `2xx` there is a **Critical finding, not a test failure**: RFC 8785 serializes JSON numbers through IEEE 754 doubles (ADR-0011 §1.1), so an amount that is ever a JSON number has lost precision before canonicalization sees it — and the loss is invisible, because `45.0` survives and `0.1` does not. The contract's own lint enforces the other half of this, rejecting `type: number` on any fiscal field.

**`98 · Destructive`** holds `POST /v1/auth/password` alone. It revokes every session including the calling one, and it leaves the bootstrap administrator on a password the collection's variables no longer name — so a pass that included it would pass once and fail forever after. Run it deliberately, then update `adminPassword` or re-bootstrap the tenant.

## Cross-cutting assertions

[`collection.test.js`](../../contracts/postman/scripts/collection.test.js) runs after **every** request and encodes controls rather than checking endpoints:

- **No fiscal amount is a JSON number** in any response (ADR-0010 §2.9).
- **Every timestamp carries exactly six fractional digits** and a literal `Z` — `canon/v1` P2. A response emitting an offset or a variable precision would digest differently from the record it describes.
- **Errors are RFC 9457** Problem Details with `type`, `title`, `status`, `ztx_reason_code` **and `ztx_retryable`** (ADR-0016 §2.5). The last one matters: a client that infers retryability from the status code guesses, and the guess that matters is the one that files a return twice.
- **No `UNCERTAIN_*` state appears in a 4xx or 5xx** (ADR-0016 §3.2).
- **No fiscal amount in an error body** (ADR-0016 §2.6).

Timestamps in request bodies are computed rather than `{{$isoTimestamp}}`, which gives seconds only — a timestamp at second precision is a different document from one at microsecond precision, and the two digest differently.

## This is not the contract gate

Contract testing is tier 4 in ADR-0018 §2.5, generated from the contract with `oasdiff` as the release blocker. The gates that actually hold the contract are elsewhere and run without a server:

- `contracts`: the API lint, the hash-checked 3.1 export, and this collection's own regenerate-and-diff check.
- [`contract_test.go`](../internal/transport/http/contract_test.go): the router's routes compared against the contract in both directions.

This collection is for exploration and manual verification against a running cell. It is generated so that it cannot disagree with the contract, and it is still not the thing that decides whether the contract is kept.
