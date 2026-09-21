# ZoikoTax — Backend

Go implementation of the fiscal core. Release trains `APP` and `ADAPTER`.

Every structural choice here is recorded in [`../adr/`](../adr/README.md). If something in this tree looks unusual — a required rounding parameter, a package that cannot import `time`, a database role with no `UPDATE` grant — the ADR explains why, and the reason is usually that the alternative fails silently.

## Status

**W0 skeleton, compiling and running.** What exists: module definition, layout, boot path, configuration, the decimal runtime of ADR-0002 in full — arithmetic context, `Money` and `Rate`, rounding policies decoded from content, largest-remainder allocation, the golden corpus with its Python cross-check and the property suite — the ADR-0001 control 1 analyzer, a container image and a local cell stack. What does not: persistence, transport beyond health checks, and every domain module.

Verified 21 September 2026 in a pinned `golang:1.25-bookworm` container — `go vet ./...` clean, `go test -race ./...` green across the 75 golden vectors, six property suites and the unit tests, `golangci-lint run` reporting 0 issues, the fiscalfloat analyzer clean over the module and its own suite passing, and the Python cross-check agreeing on every vector. Every gate below also runs in CI on each push and pull request, so the verification above is repeated by a machine that has no local state. There is no Go toolchain on the authoring machine, so everything below goes through Docker; install Go locally and the `make` targets work directly.

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
postman/                one collection, variables included (see below)
vendor/                 committed deliberately (ADR-0001 c6) — not yet generated
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
make vendor            # ADR-0001 control 6 — commit the apd source
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

[`Dockerfile`](Dockerfile) builds `ztax-core` on `gcr.io/distroless/static-debian12:nonroot` — **3.3 MB**, static, no shell, no package manager, runs as `nonroot`. `-trimpath`, a zeroed `buildid` and `CGO_ENABLED=0` make the build reproducible, which ADR-0001 §3.3 folds into the C3 replay guarantee.

Two consequences of that base image worth knowing before they surprise you:

- **No `HEALTHCHECK` in the image.** There is no shell and no `curl` to run one. Liveness and readiness are HTTP probes against `/healthz` and `/readyz`, owned by the orchestrator.
- **`read_only: true` in compose.** The binary writes nothing, and enforcing it locally means a future change that starts writing fails here rather than in a cell.

[`docker-compose.yml`](docker-compose.yml) models one regional execution cell (ADR-0009 §2.6): its own PostgreSQL 17 + PostGIS 3.5, sharing nothing. `make up`, `make logs`, `make down`, `make down-clean`.

`make docker-release` attaches SBOM and provenance. Note that buildx emits SPDX while Build Plan §7 requires **CycloneDX** — conversion and signing are W1 lane B work and are not done here.

Stages for `ztax-outbox-relay` and `ztax-migrate` are commented placeholders until those commands exist. `ztax-migrate` stays a separate image on purpose: it runs under a DDL role the application never holds (ADR-0008 §2.8).

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

[`release.yml`](.github/workflows/release.yml) runs on a `v*` tag and produces what Build Plan §7 requires of the `APP` train — image digest, CycloneDX SBOM, provenance, and a keyless signature **over the digest rather than the tag**. It also writes a release evidence file naming the versions it can attest, marked partial, listing the ones that do not exist yet. A manual dispatch builds and publishes nothing, so the path can be exercised without minting something that looks releasable.

Still lane B: signed-artifact admission in the regional clusters, and the tier 3 integration job, which arrives with persistence.

## Postman

[`postman/`](postman/README.md) — one collection, 20 requests covering the two live endpoints and the planned `/v1` surface from ADR-0010 §2.5. The local cell's values ride along as collection variables, so there is no environment file to import beside it.

Run it against a live cell:

```bash
make up
docker run --rm -v "${PWD}/postman:/etc/newman" postman/newman:alpine \
  run ZoikoTax.postman_collection.json \
  --env-var "baseUrl=http://host.docker.internal:8080"
```

Against the current binary this gives 20 requests, 22 assertions, 0 failures: the health folder asserts for real, and the `/v1` requests report as not-yet-implemented rather than failing.

The folder that matters is **`07 · Contract conformance`** — seven requests that *should* fail in specific ways, each asserting an ADR control. Chief among them, a commit whose `netAmount` is the JSON number `45.0` rather than the string `"45.00"`. A 2xx there is a Critical finding, not a test failure.

The collection is for exploration, not for CI. Contract testing is tier 4 in ADR-0018 §2.5, generated from the contract with `oasdiff` as the release blocker — a second, hand-maintained source of truth about the API is what ADR-0010 §3.1 exists to prevent.

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

1. `Quantity`, `TimeInterval`, `ReasonCode` and the identity primitives — ADR-0012. `Quantity` is already named in the fiscalfloat analyzer's guarded type list and does not exist yet.
2. `internal/platform/canonical` with its golden vectors — ADR-0011, a W1 exit gate.
3. First migration and the pgx `NUMERIC` ↔ `apd.Decimal` conformance test — ADR-0008 §5.1 c1, which discharges ADR-0001 control 2. Compose already runs PostgreSQL 17 + PostGIS 3.5, so the dependency is waiting.
4. `make vendor` and commit `vendor/` — ADR-0001 control 6, the last open W0 control.
5. Signed-artifact admission in a regional cell, so that an unsigned or unattested workload cannot start — W1 lane B. The release workflow now signs; nothing yet refuses an image that is not signed.
6. Content-side validation that rejects a `RuleVersion` applying a rate or a division without a rounding policy — ADR-0002 §5.1 c5, lane F, when `content/` opens.

`go mod tidy` dropped `google/uuid` because nothing imports it yet; it returns with `internal/platform/idgen` in step 3.
