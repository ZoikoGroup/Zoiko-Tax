# ZoikoTax — Backend

Go implementation of the fiscal core. Release trains `APP` and `ADAPTER`.

Every structural choice here is recorded in [the ADR set](../../adr/README.md). If something in this tree looks unusual — a required rounding parameter, a package that cannot import `time`, a database role with no `UPDATE` grant — the ADR explains why, and the reason is usually that the alternative fails silently.

## Status

**W0 complete for the foundation; persistence, identity and the execution model are in.** What exists:

- The decimal runtime of ADR-0002 in full — arithmetic context, `Money`, `Rate`, `Quantity`, rounding policies decoded from content, largest-remainder allocation, the golden corpus with its Python cross-check and the property suite.
- Canonicalization and digests (ADR-0011) — RFC 8785 JCS under `canon/v1`, the decimal normal form, RFC 6962 Merkle roots.
- Persistence (ADR-0008) — `pgx` v5 native, the registered `NUMERIC` ↔ `apd.Decimal` codec, the cell schema, and `ztax-migrate`.
- Tenancy and authentication (ADR-0020) — tenants, users, roles, opaque server-side sessions, the administration surface and its audit trail.
- The rule execution model (ADR-0005) — the typed IR, bundle load with acyclicity and type checking, and the deterministic evaluator.
- The error taxonomy (ADR-0016), identifiers (ADR-0012), the transactional outbox (ADR-0014) and the authority adapter boundary (ADR-0009 §2.2).

The content compiler and bundle signing (`internal/content`, `cmd/ztax-contentc`) and the v1 API contract with its generated wire types (`internal/transport/http/gen`, ADR-0010 §2.1) have landed since.

What does not: any actual tax content beyond the `eu-vat` sample pack, the Model Gateway, telemetry, and evidence sealing. Two of those wait on specifications that were never produced — `ZTAX-DET-001` and `ZTAX-JUR-001` — so the interfaces are here and the rule semantics are not, which is exactly where the Build Plan says W0 should leave them.

**Both remaining W0 controls are closed.** ADR-0001 control 2 (`NUMERIC` bound to `apd.Decimal`, no path narrowing to `float64`) is discharged by the conformance suite in `internal/adapter/postgres`, which runs against a real PostgreSQL. Control 6 (`apd` vendored) is done, and `make vendor-verify` detects drift or a local patch.

Verified 22 September 2026 in a pinned `golang:1.25-bookworm` container: `gofmt`, `go vet` and `go build` clean, `go test -race ./...` green, `golangci-lint run` reporting 0 issues, the fiscalfloat analyzer clean over the module and its own suite passing, the Python cross-check agreeing on all 62 shared vectors, the migrations applying from nothing, the NUMERIC conformance suite green against a live database, and the whole stack answering end to end through the browser origin. Every gate runs in CI on each push and pull request. There is no Go toolchain on the authoring machine, so everything below goes through Docker; install Go locally and the `make` targets work directly.

### One defect worth knowing about

The conformance suite found a real bug on its first run, and the fix is load-bearing. pgx's **binary** `NUMERIC` decoder short-circuits when a value has no significant digits — true only of zero — and returns exponent 0, so `0.00` came back as `0`. Every non-zero value round-tripped exactly.

That matters here more than it would elsewhere. Scale is semantic (ADR-0011 §2.2): `0.00` and `0` digest differently, so a zero-tax line read back from the database would canonicalize differently from the one written, and the decision would fail to replay against its own evidence — broken by the one value most likely to appear on a zero-rated or exempt line. The codec is pinned to the text wire format, which carries the scale in the digits and has nowhere to lose it. See `textNumericCodec` in `internal/adapter/postgres/pool.go`.

## Layout

```
cmd/
  ztax-core/            regional cell binary — the transactional core (ADR-0009)
  ztax-outbox-relay/    outbox publisher; separate lifecycle from request handling
  ztax-migrate/         migration runner; runs under a DDL role the app never holds
internal/
  domain/               pure business types and rules. No I/O, no transport, no SQL
    fiscal/             decimal context, Money, Rate — the shared kernel
    classification/  jurisdiction/  obligation/  evidence/  ai/
    rule/eval/          the deterministic rule-DAG evaluator (ADR-0005)
  app/                  use cases. Transaction boundaries live here and nowhere else
  port/                 interfaces the app layer depends on
  adapter/              implementations of port — postgres, gateway, broker, authority
  transport/            http (generated server + middleware), grpc
  platform/             config, canonical, idgen, telemetry, kms
  fiscaltest/           test-only constructors, and the golden-vector reader.
                        Production code cannot import this — depguard, and the
                        package imports testing
tools/
  fiscalfloat/          the float64 analyzer (ADR-0001 c1). Own module, so its
                        dependencies stay out of the shipped binary's graph
  decimalcrosscheck/    the Go ↔ Python decimal cross-check (ADR-0002 c2)
migrations/             plain versioned SQL (ADR-0008)
testdata/golden/        golden vectors — legal artifacts, not fixtures (ADR-0018)
  decimal/              the ADR-0002 corpus, with its own README
postman/                one collection, generated from the contract (see below)
vendor/                 committed deliberately (ADR-0001 c6); CI builds from it and
                        `make vendor-verify` fails on drift or a local patch
```

Import direction, enforced by `depguard` in [.golangci.yml](.golangci.yml):

```
transport → app → port ← adapter
                ↘ domain ↙
```

`domain` imports nothing from this module but other `domain` packages.

## Getting started

The fastest path to a running cell needs only Docker:

```bash
make up          # postgres + ztax-core
curl localhost:8080/healthz
make logs
make down
```

With a local Go toolchain (1.25+):

```bash
make tidy              # resolve the module graph
make vendor            # ADR-0001 control 6 — refresh vendor/ after any go.mod change
make api-gen           # ADR-0010 §2.1 — regenerate the wire types from the contract
make fiscalfloat-test  # the analyzer's own suite
make golden            # tier 2 — the decimal vectors
make golden-crosscheck # the same vectors under Python decimal (needs python3)
make check             # vet + fiscalfloat + lint + test + cross-check
make run               # needs .env.local; see .env.local.example
```

`golangci-lint` for `make lint`. Docker for `make test-integration` and the image targets.

Without a local toolchain, anything can run in a container:

```bash
docker run --rm -v "${PWD}:/src" -w /src golang:1.25-bookworm go build ./...
```

## Container and local cell

```
make up            # postgres + migrations + ztax-core + ztax-web
make logs          # follow the cell
make down          # stop, keeping the database volume
make down-clean    # stop and destroy the volume
```

`docker compose up` runs `ztax-migrate` to completion before `ztax-core` starts, which is the same ordering a cell gets from a pre-deploy job. The stack provisions one tenant on first boot, because every administrative endpoint requires an administrator and the first one cannot come through the API without an unauthenticated endpoint that creates tenants (ADR-0020 §3.3):

| | |
|---|---|
| Tenant | `acme` |
| Administrator | `admin@acme.example` |
| Password | `local-dev-only-password` |

Open <http://localhost:3000> and sign in. The cell is on <http://localhost:8080>.

**These credentials are development-only and the mechanism says so.** The password is resolved through `internal/platform/secrets`, which refuses `local://` references outside `development` — so the pattern ADR-0017 §2.4 forbids in a cell cannot be the thing that ships. `ZTAX_SECURE_COOKIES=false` is refused outside `development` for the same reason, and `ZTAX_AUTHORITATIVE=true` is refused outside `production`, because no authoritative fiscal output is permitted before A4.

### Running against the cell directly

```
# Tier 3 — the NUMERIC conformance suite and the repository tests
export ZTAX_TEST_DATABASE_URL='postgres://ztax_app:local-dev-only@localhost:5432/ztax?sslmode=disable&search_path=ztax,public'
make test-integration

# Apply or inspect the schema by hand
export ZTAX_ENVIRONMENT=development
export ZTAX_MIGRATE_DATABASE_URL="$ZTAX_TEST_DATABASE_URL"
make migrate-status
```

## Pipeline

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs on every push to `main` and every pull request. One job per control, each named after the control it enforces, so a red run says which guarantee broke rather than that CI failed.

| Job | Gate | Source |
|---|---|---|
| build · format · vet | `gofmt`, `go vet`, `go build`, and `go.mod`/`go.sum` tidy — the module graph is named in the release evidence manifest | ADR-0001 c3 |
| architectural controls | `golangci-lint`: layering, `fiscal` may not import `ai`, the evaluator cannot reach a clock or a network, production may not import `fiscaltest` | ADR-0007 §2.5, ADR-0006 §2.6, ADR-0005 §2.4, ADR-0002 §2.3 |
| float64 unreachable | the `fiscalfloat` analyzer over the module, after its own suite | ADR-0001 c1, ADR-0002 §2.8 |
| tier 1 and 2 | unit, property and golden-vector tests under `-race` | ADR-0018 §2.1 |
| decimal agreement | the vectors under Python `decimal`, in a job with no Go toolchain | ADR-0002 §5.1 c2 |
| image builds | the distroless image, built and not pushed | ADR-0001 §3.3 |

Two details worth knowing. The cross-check job has **no Go toolchain in it on purpose** — the check is worth something only because a second implementation, reading nothing but the vectors, reaches the same answers. And actions are pinned to commit SHAs rather than tags, for the reason ADR-0001 control 6 vendors `apd`: a tag is a moving pointer to someone else's code.

[`release.yml`](../.github/workflows/release.yml) runs on a `v*` tag and produces what Build Plan §7 requires of the `APP` train — image digest, CycloneDX SBOM, provenance, and a keyless signature **over the digest rather than the tag**. It also writes a release evidence file naming the versions it can attest, marked partial, listing the ones that do not exist yet. A manual dispatch builds and publishes nothing, so the path can be exercised without minting something that looks releasable.

Still lane B: signed-artifact admission in the regional clusters, and the tier 3 integration job, which arrives with persistence.

## Postman

[`postman/`](postman/README.md) — one collection, 29 requests, **generated from [`contracts/openapi/ztax.v1.yaml`](../contracts/openapi/ztax.v1.yaml)** and hash-checked, so it cannot disagree with the contract. The local cell's values ride along as collection variables, so there is no environment file to import beside it.

The seven requests in `90 · Contract conformance` are the ones worth running: each sends something a well-behaved client would never send — an unknown field, an explicit null, an invented role, a sign-in for a user that does not exist — and each asserts a control against an endpoint that exists today.

Run it against a live cell:

```bash
make up
docker run --rm -v "${PWD}/postman:/etc/newman" postman/newman:alpine \
  run ZoikoTax.postman_collection.json \
  --env-var "baseUrl=http://host.docker.internal:8080"
```

Regenerate it after any contract change:

```bash
make contract
```

Folder `99` holds the determination surface, which is specified and not built. Its paths are listed in the collection's `pendingPaths`, so a 404 there reports as not-yet-implemented rather than as a wall of red that hides the rows that matter. Chief among those is a commit whose `netAmount` is the JSON number `45.0` rather than the string `"45.00"` — a 2xx there is a Critical finding, not a test failure.

The collection is for exploration, not for CI. Contract testing is tier 4 in ADR-0018 §2.5, with `oasdiff` as the release blocker. What CI does run is `npm run postman:check`, which regenerates the collection and fails on a hand edit — the collection is generated precisely so that it cannot become the second source of truth ADR-0010 §3.1 rejects.

## Things that will surprise you, and why

**Division requires a rounding policy.** `fiscal.Quo` takes a `RoundingPolicy` and there is no default, because rounding is jurisdiction-specific law rather than a runtime convention. A required parameter fails closed at compile time; a default fails open in production. ADR-0002 §2.2.

**Rounding is content, and a policy cannot be written in Go.** `RoundingPolicy` has unexported fields and no literal constructor; the only way to obtain one is `fiscal.DecodeRoundingPolicy`, from the JSON a signed content bundle carries. A composite literal elsewhere produces the zero value, which every operation refuses. Tests construct policies through `internal/fiscaltest`, which builds the same JSON and decodes it — so a test exercises the production ingress rather than a parallel one. ADR-0002 §2.3.

**Distributing a total is one operation, not several divisions.** `fiscal.Allocate` does largest-remainder with an ascending-ordinal tie-break, and the parts sum to the total exactly. Dividing each line separately loses the residual: 100.00 across three lines is 33.34 / 33.33 / 33.33, not 33.33 three times. ADR-0002 §2.6.

**`Money` has no float accessor.** Not even for display. The parse path is from a string, because a string is the only ingress that cannot have passed through a binary float. ADR-0001 §4.2 has the worked example.

**Nothing calls `time.Now()` in `domain` or `app`.** Decision time arrives in the request envelope, so replay is parameter substitution rather than simulation, and it exercises the same code path as production. ADR-0003 §2.5.

**The application's database role cannot `UPDATE` or `DELETE`.** Corrections are new rows linked by supersession. A seal over mutable data is not a seal. ADR-0003 §2.1.

**`internal/domain/fiscal` cannot import `internal/domain/ai`**, and no function converts between them. The only path from an AI suggestion to a fiscal record runs through human review. ADR-0006 §2.6.

**Feature flags are prohibited on any path that changes a fiscal outcome.** That variation is content, which carries effective dating, approval and replay; a flag carries none of those and appears in no evidence manifest. ADR-0017 §2.3.

**Coverage is reported and never gated.** The gates are mutation score on the rule compiler and evaluator, and golden coverage against the pack certification dimensions. ADR-0018 §2.9.

## Next, in order

Done since this list was written: `Quantity`, `ReasonCode` and the identity primitives (ADR-0012); `internal/platform/canonical` with its vectors (ADR-0011); the migrations and the `NUMERIC` ↔ `apd.Decimal` conformance suite (ADR-0008 §5.1 c1, discharging ADR-0001 control 2); and `vendor/` (ADR-0001 control 6).

1. `TimeInterval` — ADR-0012, the one primitive from the original step 1 still missing.
2. Signed-artifact admission in a regional cell, so that an unsigned or unattested workload cannot start — W1 lane B. The release workflow now signs; nothing yet refuses an image that is not signed.
3. Content-side validation that rejects a `RuleVersion` applying a rate or a division without a rounding policy — ADR-0002 §5.1 c5, lane F, when `content/` opens.
