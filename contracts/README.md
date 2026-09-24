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
npm run check     # lint + export:check + routes:check + postman:check
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

## Generated wire types, and what is still hand-written

ADR-0010 §2.1 generates the server's types from this contract. `oapi-codegen` v2.8.0 reads the 3.1 export and writes [`backend/internal/transport/http/gen/types.gen.go`](../backend/internal/transport/http/gen), which is committed; `make api-gen-check` regenerates it and fails on any diff, and CI runs it. The handlers build their requests and responses from those types, so a field the contract renames, adds or retypes is a compile error in the handler rather than a response that quietly stops matching the SDKs.

Two things are deliberately not generated, and each is a decision rather than a gap:

- **The server interface.** The generator can emit a `net/http` interface, and that output imports `github.com/oapi-codegen/runtime` for parameter binding: a new module in the request path of a system whose replay guarantee names its dependency graph (ADR-0001 §3.3). Routing stays the table in `router.go`, which `contract_test.go` holds to this contract in both directions.
- **`email` and `date-time`.** Both are mapped to `string` in [the generator config](../backend/internal/transport/http/gen/oapi-codegen.yaml). The defaults would be the runtime's `Email` type and `time.Time`, and `time.Time` marshals with variable fractional precision where the contract's `Timestamp` is exactly six digits (ADR-0011 §2.1 P2).

## SDKs

Five, as ADR-0010 §1 and Build Plan lane K require, each generated from the 3.1 export and each with its own regenerate-and-diff check. The generated part is the types; the client over them is hand-written and thin, and all five make the same three decisions: an error is a value carrying the Problem document, nothing retries by itself, and there is no token handling because the session is a cookie.

| SDK | Directory | Types generated by | Gate |
|---|---|---|---|
| TypeScript | [`sdk/typescript`](../sdk/typescript) | `openapi-typescript` 7.13.0 | `npm run check` |
| Python | [`sdk/python`](../sdk/python) | `datamodel-code-generator` 0.83.0 (`TypedDict`, no runtime dependency) | `python scripts/check.py` |
| Go | [`sdk/go`](../sdk/go) | `oapi-codegen` v2.8.0 (stdlib only) | `make check` |
| Java / Kotlin | [`sdk/java`](../sdk/java) | `openapi-generator-maven-plugin` 7.25.0 (models only; Jackson at runtime) | `mvn -B verify` |
| .NET | [`sdk/dotnet`](../sdk/dotnet) | NSwag 14.7.1 (no package dependency) | `sh scripts/check.sh` |

ADR-0010 §5.1 control 5 — contract tests against a running server for every SDK — is not wired yet. Each SDK's tests run against a stub transport; the server side of the contract is held by `contract_test.go` and the Postman collection.

## Versioning

`/v1` changes **only by addition** (§2.6). Anything breaking is `/v2`, running alongside. `oasdiff` as a release blocker against the previous released contract is ADR-0010 §5.1 control 2 and is not wired up yet either — it needs a previous release to compare against, which `/v1` does not have.

The additive rule already shapes what is written here. `ReasonCode` is a pattern rather than an enum, because the register grows by addition and a closed enum would make every new code a breaking change for a client that validates.
