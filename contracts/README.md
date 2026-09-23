# Contracts

The API contract and the gates that keep it honest. Train: `SCHEMA`.

> Five generated SDKs and an externally-consumed contract settle the spec-first question: the contract is read and approved by people who do not read Go, and it drives code generation in five other languages. A contract inferred from Go annotations is a contract nobody outside the Go team can review before it ships. — [ADR-0010](../../adr/ADR-0010-api-contract-pipeline.md) §1

## What is here

| Path | What |
|---|---|
| `openapi/ztax.v1.yaml` | **The contract.** OpenAPI 3.2.0, hand-authored, reviewed |
| `openapi/export/ztax.v1.3.1.yaml` | The 3.1.0 export the toolchain reads. Generated, hash-checked |
| `openapi/export/ztax.v1.routes.json` | The route table the server is checked against. Generated, hash-checked |
| `tools/` | The downgrade step, the API lint and the route extractor |

## The gates

```bash
npm ci
npm run check     # lint + export:check + routes:check
```

| Gate | What it refuses | ADR |
|---|---|---|
| `lint` | A fiscal amount typed as a JSON number; an error response that is not the shared Problem schema; an operation with no `operationId`, summary or example; an idempotent endpoint that does not declare `Idempotency-Key`; an orphan schema | §5.1 c3, c6 |
| `export:check` | An export that was hand-edited, or that the contract no longer generates | §5.1 c4 |
| `routes:check` | A route manifest that the contract no longer generates | §2.1 |

The Go side adds the other half of the drift gate: [`contract_test.go`](../backend/internal/transport/http/contract_test.go) compares the router's route table against `routes.json` **in both directions**. An endpoint the server serves and the contract does not declare fails, because an undocumented endpoint is a surface nobody reviewed. An endpoint the contract declares and nothing routes fails too, because the generated SDKs already have a method for it.

## Why there are two OpenAPI versions

OpenAPI 3.2.0 is mandated and its tooling ecosystem is immature — generators, linters and SDK toolchains have uneven 3.2 support, and the Go generators target 3.0 and 3.1. ADR-0010 §2.2 neither pretends otherwise nor quietly downgrades:

```
ztax.v1.yaml          3.2.0   authoritative, hand-authored, reviewed, published
export/…3.1.yaml      3.1.0   generated, never edited, hash-checked
```

The export is a **build artifact, not a second contract**. Two properties make that true rather than aspirational, and both are `tools/downgrade.mjs`:

- **Generated, not edited.** `export:check` regenerates and compares. A hand edit fails CI, and that check is the only thing stopping the export becoming a fork.
- **Refuses, not degrades.** A 3.2-only construct with no 3.1 representation stops the export rather than being silently dropped. A dropped construct is a contract the SDKs do not implement and nobody noticed.

Retiring the export is [ADR-0010 §5.3](../../adr/ADR-0010-api-contract-pipeline.md), reviewed at each wave exit. When a 3.2-capable toolchain is certified, `tools/downgrade.mjs` and `openapi/export/` are deleted and nothing else changes.

## What is deliberately not in the contract

`GET /healthz` and `GET /readyz`. They are served, and they are not a customer surface: an orchestrator calls them, they carry no session, and their bodies are plain text on purpose. Declaring them would generate SDK methods nobody should call and would make a liveness probe a compatibility commitment governed by §2.6.

The exception register for that is in `contract_test.go`, not in a flag — anything added to it is a route that will never appear in a customer's SDK, which is a decision rather than an oversight.

## Not yet: generated handlers

ADR-0010 §2.1 generates the server interface and types with `oapi-codegen` into `backend/internal/transport/http/gen/`, with a regenerate-and-diff check. **That generator is not wired up.** The handlers in `backend/internal/transport/http` are hand-written against this contract, and the drift gate they are missing is discharged by `contract_test.go` above — which checks routing and authentication, but not request or response schemas.

That is a real gap and it is named here rather than left to be discovered: a response field that stops matching the contract is not currently caught by anything. Closing it is the next step on this lane, and it is a mechanical change now that the contract exists and the routes are a table.

## Versioning

`/v1` changes **only by addition** (§2.6). Anything breaking is `/v2`, running alongside. `oasdiff` as a release blocker against the previous released contract is ADR-0010 §5.1 control 2 and is not wired up yet either — it needs a previous release to compare against, which `/v1` does not have.

The additive rule already shapes what is written here. `ReasonCode` is a pattern rather than an enum, because the register grows by addition and a closed enum would make every new code a breaking change for a client that validates.
