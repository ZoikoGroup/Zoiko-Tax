# Postman — ZoikoTax API

This is the single Postman document for the API. [`ZoikoTax.postman_collection.json`](ZoikoTax.postman_collection.json) is the single collection: **86 requests in 11 folders, covering every one of the 71 routes the cell serves**. Every value is a collection variable, so you import it and run it without choosing an environment.

```bash
cd ../../contracts && npm run postman        # regenerate (or `make contract` in backend/)
cd ../../contracts && npm run postman:check  # what CI runs
```

> **Do not edit the collection by hand.** `postman:check` regenerates it and compares the result, so a hand edit fails the build. ADR-0010 §2.1 makes the contract the source of truth and treats drift as a build failure. A hand-maintained collection would be a second, kinder statement of what the API is, which is what §3.1 exists to prevent.

## Where each part comes from

| Part | Source |
|---|---|
| Folders `01`–`04`, `06`, `08`, `98` | [`contracts/openapi/ztax.v1.yaml`](../../contracts/openapi/ztax.v1.yaml) — operations, bodies, saved response examples |
| Folder `00` (health probes) | The generator. The cell serves them, and they are deliberately left out of the contract |
| Folders `05`, `07`, `90` | [`contracts/postman/overlay.json`](../../contracts/postman/overlay.json) — requests the contract cannot express |
| Collection scripts | [`contracts/postman/scripts/`](../../contracts/postman/scripts) — kept as JavaScript, not as a JSON array of strings |
| Run order, per-request assertions, variable captures | `FOLDERS`, `OPERATION_TESTS` and `ACCEPTED_OUTCOMES` in [`contracts/tools/postman.mjs`](../../contracts/tools/postman.mjs) |

**The run order lives in the generator, not in the contract.** A collection runs top to bottom, so sign-in has to come before the administrative calls and sign-out has to come after them. That is a fact about a test run, not about the API, so it does not belong in the contract. Generation *fails* if the run plan does not place a contract operation. A new endpoint therefore cannot be silently dropped from the collection; somebody has to decide where in the run it belongs.

Two gates keep the collection and the server in step. [`contract_test.go`](../internal/transport/http/contract_test.go) compares the router's routes with the contract in both directions, and `postman:check` compares the contract with this collection. A route added to the router therefore cannot reach `main` without a request here.

## Running it

Bring the stack up first. It bootstraps the `acme` tenant and its administrator:

```bash
make up            # or: docker compose up --build
```

Then run the whole collection. `02 · Sign in` opens the session everything below depends on, and Postman's cookie jar carries it, so there is no token to copy.

```bash
npm install -g newman
newman run ZoikoTax.postman_collection.json
```

Or, with nothing installed, from `backend/`:

```bash
docker run --rm -v "${PWD}/postman:/etc/newman" postman/newman:alpine \
  run ZoikoTax.postman_collection.json \
  --env-var "baseUrl=http://host.docker.internal:8080"
```

On the local stack, a default run makes folders `00`–`05`, `08` and `90` green — 49 requests, 234 assertions. Folders `06`, `07` and `98` are skipped on purpose; see [Switches](#switches).

`tenantSlug`, `adminEmail` and `adminPassword` default to the local stack's bootstrap values. The generator reads them from [`docker-compose.yml`](../../docker-compose.yml) instead of copying them. An earlier copy drifted: the collection carried a password the stack never set, so every run failed at sign-in and every request after it returned 401. Against any other cell, set `baseUrl`, `tenantSlug`, `adminEmail` and `adminPassword`:

```bash
newman run ZoikoTax.postman_collection.json \
  --env-var baseUrl=https://eu-west-1.zoikotax.com \
  --env-var tenantSlug=acme --env-var adminEmail=… --env-var adminPassword=…
```

### Switches

| Variable | Default | Turns on |
|---|---|---|
| `runDetermination` | `false` | Folders `06` and `07`. They need a cell with a content bundle (`ZTAX_CONTENT_DIR`; build one with `make content`) and a signed-in user who holds `OPERATOR`, which the bootstrap administrator does not |
| `runDestructive` | `false` | Folder `98`. Read [its section](#98--destructive) before you turn it on |

```bash
newman run ZoikoTax.postman_collection.json --env-var runDetermination=true \
  --env-var adminEmail=<an OPERATOR> --env-var adminPassword=<their password>
```

### Variables the run captures

Requests that create something store its id, so a Collection Runner pass needs no manual copying:

| Variable | Captured by |
|---|---|
| `bundleDigest` | `GET /v1/capabilities`, when a content bundle is loaded |
| `userId` | `POST /v1/admin/users` |
| `sessionId` | `GET /v1/admin/sessions`. It captures a session that is *not* the caller's, so revoking it does not sign the run out |
| `webhookId` | `POST /v1/webhooks` |
| `sealId` | `POST /v1/seals` |
| `holdId` | `POST /v1/legal-holds` |
| `authorizationId` | `POST /v1/legal/authorizations` |
| `decisionId` | `POST /v1/transactions:commit`, then replaced by `POST /v1/transactions:adjust` |
| `obligationId` | `GET /v1/obligations` |
| `documentId` | `POST /v1/documents` |
| `refundId` | `POST /v1/transactions:refund` |
| `jobId` | `POST /v1/batches` |
| `requestId` | `POST /v1/subledger/periods/{period}/reopen-requests` |
| `runId` | `POST /v1/reconciliations` |
| `reusedKey` | Folder `07`, once per run |
| `newUserEmail`, `decisionTime`, `eventTime` | The collection pre-request script: a fresh address per run, and timestamps in canonical form |

`period` defaults to `2000-01`, a month nothing posts into, so a run never closes a period that matters. `deliveryId` and the starting `decisionId` are well-formed ids that name nothing. Requests that use them before anything exists answer `404`, and the generator accepts that `404` as a pass.

## The folders

| Folder | Requests | Session |
|---|---|---|
| `00 · Health` | 2 | none |
| `01 · Discovery` | 1 | none |
| `02 · Sign in` | 1 | opens it |
| `03 · Who am I` | 2 | any signed-in user |
| `04 · Administration` | 35 | `ADMIN`; the last request, the dashboard, `ANALYST` (both held by the local bootstrap administrator) |
| `05 · Contract conformance — with a session` | 3 | `ADMIN` |
| `06 · Determination` | 28 | `OPERATOR`; skipped unless `runDetermination` |
| `07 · Determination conformance` | 8 | `OPERATOR`; skipped unless `runDetermination` |
| `08 · Sign out` | 1 | ends it |
| `90 · Contract conformance — without a session` | 4 | none |
| `98 · Destructive` | 1 | skipped unless `runDestructive` |

### `00`–`03` · Health, discovery, sign-in, who am I

| Request | What it does | Roles |
|---|---|---|
| `GET /healthz` | Liveness. It touches no dependency on purpose | public |
| `GET /readyz` | Readiness. It checks the cell store | public |
| `GET /v1/capabilities` | What this deployment can do, before you have a session | public |
| `POST /v1/auth/sign-in` | Opens a session. The session is an `HttpOnly` cookie, and the body carries no token | public |
| `GET /v1/auth/session` | Who am I, and until when | any signed-in user |
| `GET /v1/admin/tenant` | The authenticated subject's tenant | any signed-in user |

### `04` · Administration

The requests run in this order: users, sessions and the audit trail; webhooks; period seals; retention and legal holds; legal authorization; then the dashboard. On a fresh local stack, some correct answers are not `2xx`, and the collection accepts them:

- **Seals** answer `503` on a cell with no seal keyring. `POST /v1/seals` answers `409` once an earlier run has sealed the period.
- **Webhook delivery** read and replay answer `404`, because a fresh run has delivered nothing.
- **`DELETE /v1/admin/sessions/{sessionId}`** answers `404` when the run's own session is the only one.
- **The retention verdict** answers `404`, because this folder has committed no decision.
- **Webhooks** need `ZTAX_WEBHOOK_KEY_REF`, which the local stack sets. A cell without it answers `503`.
- **The dashboard** needs `OPERATOR`, `ANALYST` or `AUDITOR`. The local stack grants its bootstrap administrator `ANALYST` (`ZTAX_BOOTSTRAP_ADMIN_EXTRA_ROLES`, refused outside development); elsewhere an `ADMIN`-only user gets `403`. It needs no content bundle: an empty cell reports zeros.

| Request | What it does | Roles |
|---|---|---|
| `GET /v1/admin/users` | List the tenant's users | ADMIN, ANALYST, AUDITOR |
| `POST /v1/admin/users` | Create a user | ADMIN |
| `POST /v1/admin/users/{userId}/roles` | Grant a role | ADMIN |
| `POST /v1/admin/users/{userId}/status` | Enable or disable a user | ADMIN |
| `GET /v1/admin/sessions` | List the tenant's sessions | ADMIN |
| `GET /v1/admin/audit` | Read the tenant's audit trail | ADMIN, AUDITOR |
| `DELETE /v1/admin/users/{userId}/roles/{role}` | Revoke a role | ADMIN |
| `DELETE /v1/admin/sessions/{sessionId}` | Revoke a session | ADMIN |
| `GET /v1/webhooks` | The tenant's webhook subscriptions | ADMIN, AUDITOR |
| `POST /v1/webhooks` | Subscribe an endpoint to named event types | ADMIN |
| `GET /v1/webhooks/{webhookId}` | One webhook subscription | ADMIN, AUDITOR |
| `POST /v1/webhooks/{webhookId}/secrets:rotate` | Issue a new signing secret | ADMIN |
| `GET /v1/webhooks/{webhookId}/deliveries` | A subscription's deliveries, newest first | ADMIN, AUDITOR |
| `GET /v1/webhooks/{webhookId}/deliveries/{deliveryId}` | One delivery and its attempts | ADMIN, AUDITOR |
| `POST /v1/webhooks/{webhookId}/deliveries/{deliveryId}/replay` | Deliver an event again | ADMIN |
| `POST /v1/webhooks/{webhookId}/status` | Pause, resume or disable a subscription | ADMIN |
| `GET /v1/seals` | The tenant's period seals, latest first | ADMIN, AUDITOR, ANALYST |
| `POST /v1/seals` | Seal a period's decisions | ADMIN |
| `GET /v1/seals/{sealId}` | One seal, with the bytes its signature covers | ADMIN, AUDITOR, ANALYST |
| `GET /v1/seals/{sealId}/verification` | Verify a seal end to end | ADMIN, AUDITOR, ANALYST |
| `POST /v1/retention/policies` | Record the next version of a retention policy | ADMIN |
| `GET /v1/retention/policies` | Every version of every retention policy | ADMIN, AUDITOR |
| `POST /v1/legal-holds` | Place a legal hold | ADMIN |
| `GET /v1/legal-holds` | The tenant's legal holds | ADMIN, AUDITOR |
| `GET /v1/legal-holds/{holdId}` | One legal hold and its history | ADMIN, AUDITOR |
| `POST /v1/legal-holds/{holdId}/scope` | Change an active hold's scope | ADMIN |
| `POST /v1/legal-holds/{holdId}/release` | Release a legal hold | ADMIN |
| `GET /v1/decisions/{decisionId}/retention` | Whether a decision's evidence may be disposed of now | ADMIN, AUDITOR |
| `GET /v1/legal/matrix` | The LegalAuthorization matrix this cell resolves against | ADMIN, OPERATOR, ANALYST, AUDITOR |
| `POST /v1/legal/authorizations` | Record a customer authorization | ADMIN |
| `GET /v1/legal/authorizations` | The customer's authorizations | ADMIN, OPERATOR, AUDITOR |
| `GET /v1/legal/authorizations/{authorizationId}` | One customer authorization and its history | ADMIN, OPERATOR, AUDITOR |
| `POST /v1/legal/authorization-checks` | Resolve whether an action may execute now | ADMIN, OPERATOR, ANALYST |
| `POST /v1/legal/authorizations/{authorizationId}/revocation` | Revoke a customer authorization | ADMIN |
| `GET /v1/dashboard` | The Tax Command Center for one legal period | OPERATOR, ANALYST, AUDITOR |

### `06` · Determination

This folder quotes, commits, reads and replays one line against the cell's active pack. The replay must say `MATCH`; those four requests are the W2 exit gate. After that, the folder:

1. Works the return the commit assessed into.
2. Invoices the decision, then voids the invoice.
3. Adjusts the decision under the bundle that made it.
4. Refunds the adjustment, and reports the refund as paid.
5. Queues a batch.
6. Hard-closes `{{period}}` and requests a reopen. The requester's own approval is refused (ZTAX-FIN-REQ-0091).
7. Runs a reconciliation.

`POST /v1/classifications:propose` also accepts `422 AI_GATEWAY_NOT_CONFIGURED`, which every local stack answers.

| Request | What it does | Roles |
|---|---|---|
| `POST /v1/quotes` | Quote a transaction without recording it | OPERATOR, ANALYST |
| `POST /v1/transactions:commit` | Commit a transaction and record its decision | OPERATOR |
| `GET /v1/decisions/{decisionId}` | Read a recorded decision | OPERATOR, ANALYST, AUDITOR |
| `POST /v1/replay/{decisionId}` | Replay a recorded decision and compare it byte for byte | OPERATOR, ANALYST, AUDITOR |
| `GET /v1/decisions/{decisionId}/journals` | The Tax Control Subledger journals a decision posted | OPERATOR, ANALYST, AUDITOR |
| `GET /v1/subledger/balances` | Control balances for the tenant's default legal entity | OPERATOR, ANALYST, AUDITOR |
| `GET /v1/obligations` | The tenant's current obligations | OPERATOR, ANALYST, AUDITOR |
| `GET /v1/obligations/{obligationId}` | One obligation row, current or superseded | OPERATOR, ANALYST, AUDITOR |
| `POST /v1/obligations/{obligationId}/transitions` | Move an obligation to a status a user sets | OPERATOR |
| `POST /v1/documents` | Commit a fiscal document that presents committed decisions | OPERATOR |
| `GET /v1/documents/{documentId}` | One fiscal document and its status history | OPERATOR, ANALYST, AUDITOR |
| `POST /v1/documents/{documentId}/corrections` | Void, credit or rebill a committed document | OPERATOR |
| `GET /v1/documents/{documentId}/lineage` | Every document in this one's chain, original first | OPERATOR, ANALYST, AUDITOR |
| `POST /v1/transactions:adjust` | Correct a recorded decision under the content that made it | OPERATOR |
| `POST /v1/transactions:refund` | Refund tax a committed decision charged | OPERATOR |
| `GET /v1/refunds/{refundId}` | One refund and its history | OPERATOR, ANALYST, AUDITOR |
| `POST /v1/refunds/{refundId}/reports` | Record what the payment provider reported about a refund | OPERATOR |
| `POST /v1/classifications:propose` | Ask the AI plane for an advisory ontology mapping | OPERATOR, ANALYST |
| `POST /v1/batches` | Queue many commits as one job | OPERATOR |
| `GET /v1/jobs/{jobId}` | A job's progress and each item's result | OPERATOR, ANALYST, AUDITOR |
| `GET /v1/decisions/{decisionId}/inclusion` | Prove a decision is in its period's seal | OPERATOR, ANALYST, AUDITOR, ADMIN |
| `GET /v1/subledger/periods/{period}` | A legal period's state, history and close manifest | OPERATOR, ANALYST, AUDITOR |
| `POST /v1/subledger/periods/{period}/transitions` | Close a period, open an amendment window, or seal it | OPERATOR |
| `POST /v1/subledger/periods/{period}/reopen-requests` | Ask to reopen a hard-closed period | OPERATOR |
| `POST /v1/subledger/periods/{period}/reopen-requests/{requestId}/approval` | Approve someone else's request to reopen a period | ADMIN |
| `POST /v1/reconciliations` | Reconcile a legal period stage by stage | OPERATOR |
| `GET /v1/reconciliations/{runId}` | A reconciliation run, with its items and their resolutions | OPERATOR, ANALYST, AUDITOR |
| `POST /v1/reconciliations/{runId}/items/{itemId}/resolution` | Resolve one exception, with its evidence | OPERATOR |

### `05`, `07` and `90` · Conformance

These folders are the point of the collection. Each request sends something a well-behaved client would never send, and each asserts a control against an endpoint that exists today. They are split by what they need:

- `05` needs an administrator's session.
- `07` needs an `OPERATOR` and a content bundle.
- `90` needs *no* session. It runs after sign-out and clears the cookie jar for itself.

| Folder | Request | Asserts | ADR |
|---|---|---|---|
| `05` | Unknown field | Rejected, not ignored. Silently dropping it would let two different requests digest identically. `displayname` for `displayName` is refused too, although `encoding/json` matches keys case-insensitively | 0011 P5 |
| `05` | Explicit null | Rejected. "Cleared" and "not supplied" are different facts | 0011 P3 |
| `05` | Invented role | Refused. `Role` is a closed enum, not a string a caller may invent | — |
| `07` | Idempotency 1–2 | The first commit under a fixed key executes. A retry with the same body returns the original response verbatim and records nothing | 0013 §2.4 |
| `07` | Idempotency 3–4 | The same key with a different body, or with the same value at a different scale (`"45.0"` vs `"45.00"`), is `409 IDEMPOTENCY_KEY_REUSE` and does not execute | 0013 §2.4, 0011 §2.2 |
| `07` | Idempotency 5 | A commit with no `Idempotency-Key` is `400 IDEMPOTENCY_KEY_REQUIRED` | 0013 §2.1 |
| `07` | A fiscal amount sent as a JSON number | Refused with `400`. **A `2xx` here is a Critical finding, not a test failure**: RFC 8785 serializes JSON numbers through IEEE 754 doubles, so the amount has lost precision before canonicalization sees it. The loss is invisible, because `45.0` survives and `0.1` does not | 0011 §1.1 |
| `07` | Explicit null in a fiscal position | Refused, not normalized | 0011 P3 |
| `07` | A miscased field inside the input | `Currency` beside `currency` is `UNKNOWN_FIELD`, so the later key cannot silently win | 0011 P5 |
| `90` | Unrouted path | Still a Problem Details document, not `ServeMux`'s plain text | 0016 §2.5 |
| `90` | No session on an admin endpoint | `401`, not `403`: "we do not know who you are", not "you lack a role" | 0016 §2.5 |
| `90` | Unknown user **and** wrong password | Both are `INVALID_CREDENTIALS`, and the pair asserts the codes are *identical*. Any difference would be an account-enumeration oracle | 0020 |

The four idempotency requests that share a key take it from `reusedKey`, which is minted once per run. A second run against the same cell therefore exercises the same outcomes instead of replaying the first run's.

### `08` · Sign out

`POST /v1/auth/sign-out` ends the session after everything that needs it, including `05` and `07`.

### `98` · Destructive

This folder holds `POST /v1/auth/password` alone. It revokes every session, including the calling one. It also leaves the bootstrap administrator on a password the collection's variables no longer name, so a pass that included it would pass once and fail every time after. It is therefore **skipped unless `runDestructive` is `true`**. To run it, pair it with the sign-in it needs:

```bash
newman run ZoikoTax.postman_collection.json --env-var runDestructive=true \
  --folder "02 · Sign in" --folder "98 · Destructive — run deliberately, not as part of a pass"
```

Afterwards the administrator's password is `{{newPassword}}`. Change it back, pass the new one as `adminPassword`, or re-bootstrap the tenant.

## Cross-cutting assertions

Every request gets a generated assertion that its status is one the contract declares, or one of the accepted outcomes listed above. On top of that, [`collection.test.js`](../../contracts/postman/scripts/collection.test.js) runs after **every** request and encodes controls rather than checking endpoints:

- **No fiscal amount is a JSON number** in any response (ADR-0010 §2.9).
- **Every timestamp carries exactly six fractional digits** and a literal `Z` (`canon/v1` P2). A response with an offset or variable precision would digest differently from the record it describes.
- **Errors are RFC 9457** Problem Details with `type`, `title`, `status`, `ztx_reason_code` **and `ztx_retryable`** (ADR-0016 §2.5). The last one matters: a client that infers retryability from the status code is guessing, and the guess that matters is the one that files a return twice.
- **No `UNCERTAIN_*` state appears in a 4xx or 5xx** (ADR-0016 §3.2).
- **No fiscal amount appears in an error body** (ADR-0016 §2.6).

Timestamps in request bodies are computed rather than taken from `{{$isoTimestamp}}`, which gives seconds only. A timestamp at second precision is a different document from one at microsecond precision, and the two digest differently.

The overlay's `pendingPaths` lists endpoints that are specified but not built, so a `404` on one of them reports as *not implemented yet* rather than as a failure. It is empty today: every path the contract declares is routed, so a `404` anywhere is a real failure. Add a path to it only while its endpoint is specified and not yet routed.

## Not covered here

- **The model gateway** (`intelligence/`) speaks gRPC to the cell, not HTTP. Callers reach it only through `POST /v1/classifications:propose`.
- **Webhook payloads** are outbound. They are described in [`contracts/asyncapi/ztax.events.v1.yaml`](../../contracts/asyncapi/ztax.events.v1.yaml), not here.

## This is not the contract gate

Contract testing is tier 4 in ADR-0018 §2.5, generated from the contract with `oasdiff` as the release blocker. The gates that actually hold the contract run without a server:

- `contracts`: the API lint, the hash-checked 3.1 export, and this collection's own regenerate-and-diff check.
- [`contract_test.go`](../internal/transport/http/contract_test.go): the router's routes compared against the contract in both directions.

This collection is for exploration and manual checks against a running cell. It is generated so that it cannot disagree with the contract, but it is still not what decides whether the contract is kept.
