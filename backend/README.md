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

**The backend half of the W1 exit gate is in:**

- **One decision type replays exactly.** `app.DeterminationService` records a determination from the active bundle as two canonical evidence objects — the envelope (ADR-0011 §2.8, now carrying the accumulator read set of ZTAX-DET-REQ-0034) and the result with its trace — and an append-only `tax_decision` row with ADR-0003's as-of reads. `Replay` rebuilds the result from the envelope alone, against the bundle the envelope names rather than the active one, and compares bytes. Proven against the worked pack in unit tests and against a real PostgreSQL in the integration tier, including divergence, a later content release, a tampered object and a record that disagrees with its envelope.
- **Evidence store and period sealing.** A content-addressed, write-once, tenant-scoped object store (`internal/adapter/evidence`, filesystem prototype of the retention-locked object store), and `app.SealService`: an RFC 6962 Merkle root over one leaf per decision, signed through `kms.Signer`, verified end to end. ADR-0011 control 4 — tamper with one record, the root changes and verification fails — is a test, against the real store too.
- **Privacy metadata.** Every field of the API contract carries PRIV-001 §24 metadata (`x-ztax-privacy`), and the contract lint's `privacy-classified-fields` rule is PRIV-FIELD-CONF. The vocabulary is one file, `contracts/privacy/vocabulary.json`, shared by the lint and by `internal/domain/privacy`.
- **Redaction.** Classified values travel as `privacy.Value`, whose only log, format, JSON and span-attribute form is redacted (ADR-0015 §2.2); every binary's logger sits on a redacting handler that is the denylist backstop and keeps fiscal amounts out of telemetry (§2.3).
- **Tracing.** OpenTelemetry to a cell-local collector over OTLP/gRPC, all seven trains as resource attributes, a head sampler that never drops commit, adjust or refund, and trace ids on every log line and Problem. Off until a deployment names its collector (`ZTAX_OTLP_ENDPOINT`).

**W2 lane K's core surface is in.** Quote, commit, adjust, decisions and replay were first; since then:

- **Refunds** — `POST /v1/transactions:refund`, a lifecycle of its own (`REQUESTED → PENDING/COMPLETED/FAILED/UNCERTAIN`) bounded by the tax the decision posted, never a decision and never a credit (ZTAX-FIN-REQ-0015, -0059). Only `COMPLETED` posts.
- **Events** — commits, corrections, obligation moves, threshold crossings and refunds leave through the outbox under an AsyncAPI contract (`contracts/asyncapi`), with payload schemas checked against what the code emits.
- **Webhooks** — subscriptions to named event types, Standard Webhooks signing with rotation, retry to a dead letter, replay, and an egress guard in the dialer that refuses the cell's own network after name resolution.
- **Batches and jobs** — `POST /v1/batches` and `GET /v1/jobs/{id}`: up to 1000 commits as one job, each item on the single-commit path under its own idempotency key, so a worker that dies resumes without committing twice.
- **Period seals** — `POST /v1/seals`, read, verify, and `GET /v1/decisions/{id}/inclusion`, an RFC 6962 audit path from one decision to a signed root that an auditor checks with the published keyring alone.
- **Compliance packages** (W4 lane M, `ZTAX-COMP-001`) — `/v1/compliance/packages`: one package per obligation, prepared as immutable hashed versions, reviewed and approved by someone other than the preparer, approved as an exact payload hash, submitted through the legal gate, and answered by the authority. `TRANSMITTING` is not `FILED`, `UNCERTAIN_SUBMISSION` leaves only through a recorded resolution, and the obligation moves only on that evidence. Not yet: workpapers, registrations, notice and amendment cases, credential vault integration, managed-service profiles, and compliance events on the outbox.
- **Remittance** (W4 lane M, `ZTAX-REM-001`) — `/v1/remittances`: versioned payee master data verified by someone other than its author, and one immutable, hashed payment instruction per accepted return, approved by someone other than its maker. `SUBMITTED` is not `ACCEPTED_BY_PROVIDER`, which is not `SETTLED`; only settlement evidence settles and pays the obligation, and `UNCERTAIN_PAYMENT` leaves only through a recorded resolution. The posture is `NO_CUSTODY`: accounts are vault references, and ZoikoTax holds nothing. Not yet: provider adapters, ISO 20022, FX, batches, scheduling and cut-offs, remittance events on the outbox, and returns un-paying the obligation (its state machine has no edge back from `PAID`).
- **Shadow assurance** (W4 lane M, `ZTAX-SHD-001`) — `/v1/shadow`: comparator profiles that say what a match may claim, and runs that pair an incumbent's imported results with committed decisions by business key and compare them component by component through an explicit code mapping. Unmapped codes stay unmapped, equal totals do not hide component differences, unpaired records are population variances, and material cases open discrepancies with a typed root cause and lifecycle. Non-impact: a run writes nothing but itself. Not yet: expected-difference rules, fuzzy or mapped-ID pairing, classification/jurisdiction/obligation dimensions, live-shadow traffic capture, and shadow events.
- **Migration** (W4 lane M, `ZTAX-MIG-001`) — `/v1/migrations`: programs bounded by legal entity and period, immutable hashed source snapshots with declared control totals, versioned legacy-code mappings, and runs that convert AS_KNOWN into migration's own tables with a source-to-canonical crosswalk. Unmapped codes, duplicates, bad dates or currencies and precision loss are exceptions decided once by a person; counts, monetary totals and the crosswalk are reconciled separately; the cutover follows §26, and the switch needs a reconciled run with no open high-severity exception and someone other than the program's creator. Not yet: waves, source inventory and profiling, CDC deltas, rehearsals, onboarding manifests, evidence sealing of the migration manifest, and migration events.

- **Location-aware determination** (`ZTAX-DET-001` §2, `ZTAX-JUR-001`; playbook ZTAX-ENG-RESET-001 §6–§12) — `POST /v1/quotes`, `:commit` and `:adjust` take a typed `document` as well as named `input`, and `GET /v1/coverage` reports what the active content covers. `internal/domain/taxation` runs DET-001's pipeline over packages that existed and were unwired: situs per line (`jurisdiction`), the service family (`ontology`), component selection by jurisdiction, tax point, service, customer role and supply, exemption certificates, then the component DAG (`determination`). Every line ends `RESOLVED` or in an explicit `REVIEW_REQUIRED`, `UNSUPPORTED`, `AMBIGUOUS` or `CONFLICTED` with its reason and evidence — a missing location, an uncovered country or an unmapped SKU is never a zero. Customer charges, provider-borne contributions and reverse-charged amounts are separate totals; each component carries its five responsibility roles and its source. The document and result go into the envelope (absent for every earlier decision, whose bytes are unchanged), so documents replay, post and assess through the existing machinery. Content is a signed `taxation` section of the bundle: `content/packs/global-telecom` (EU-27, UK, NO, CH, Canada by province, AU, NZ, SG, JP, ZA, AE, SA; RESEARCH) and `content/packs/training-telecom` (the playbook's fixtures). Not yet: US state and local telecom taxes, India, spatial (point-in-polygon) situs, caps and thresholds inside a document, FX-pinned reporting currency for obligations in a currency other than the document's, and reviewed (PRODUCTION) content.

What does not exist: reviewed production tax content — both location-aware packs are RESEARCH and every decision is ADVISORY; deterministic classification from a tenant SKU (a document arrives with its ontology family, or the line is held as `CLASSIFICATION_REQUIRED`); fiscal documents issued automatically from a commit; and a KMS signer, so outside development a cell verifies seals and does not make them. `ZTAX-DET-001` and `ZTAX-JUR-001` are drafted and registered, still DRAFT.

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

These, the database credentials and the host ports all live in [`.env`](../.env.example) at the repository root, which `docker-compose.yml` reads; the compose file itself carries no credential. `make up` creates `.env` from [`.env.example`](../.env.example) on first run, and `docker compose` refuses to start without it. Edit `.env`, never the example — and when something else on the machine holds port 8080 or 3000, set `CORE_PORT` and `WEB_PORT` there.

Open <http://localhost:3000> (`WEB_PORT`) and sign in. The cell is on <http://localhost:8080> (`CORE_PORT`). The administrator also holds `ANALYST` (`ZTAX_BOOTSTRAP_ADMIN_EXTRA_ROLES`), so one local user sees the dashboard as well as administration.

**These credentials are development-only and the mechanism says so.** The password is resolved through `internal/platform/secrets`, which refuses `local://` references outside `development` — so the pattern ADR-0017 §2.4 forbids in a cell cannot be the thing that ships. `ZTAX_SECURE_COOKIES=false` is refused outside `development` for the same reason, and `ZTAX_AUTHORITATIVE=true` is refused outside `production`, because no authoritative fiscal output is permitted before A4.

### Running against the cell directly

```
# Tier 3 — the NUMERIC conformance suite and the repository tests
export ZTAX_TEST_DATABASE_URL='postgres://ztax_owner:local-dev-only@localhost:5432/ztax?sslmode=disable&search_path=ztax,public'
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

[`postman/`](postman/README.md) holds the one collection: 86 requests covering every route the cell serves. It is **generated from [`contracts/openapi/ztax.v1.yaml`](../contracts/openapi/ztax.v1.yaml)** and hash-checked, so it cannot disagree with the contract. Regenerate it with `make contract`. How to run it, what each folder asserts and which switches turn on the determination and destructive folders are all in [`postman/README.md`](postman/README.md), the single Postman doc.

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
