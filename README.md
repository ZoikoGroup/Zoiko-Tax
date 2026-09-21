# ZoikoTax

A global tax determination, obligation and compliance platform.

> The specification does not commission a backlog. It commissions a control system that decides when code may touch a customer's tax liability.

**Where the project is: W0 — governance bootstrap.** Build waves run W0 through W5 and authorization runs A0 through A4. No authoritative fiscal output is permitted before **A4**, which requires all governing specifications EFFECTIVE, the applicable country pack PRODUCTION, conformance verification green and release evidence sealed — simultaneously.

## The stack in one command

```bash
docker compose up --build
```

| | Address | What it is |
|---|---|---|
| `ztax-web` | http://localhost:3000 | The browser client. Static bundle on nginx, proxying `/api` to the cell |
| `ztax-core` | http://localhost:8080/healthz | The regional cell binary. Go, distroless, 3.3 MB |
| `postgres` | localhost:5432 | PostgreSQL 17 + PostGIS 3.5 — one database per cell, sharing nothing |

`docker compose down` stops it and keeps the volume; `docker compose down -v` destroys the database too.

Each service builds from **its own Dockerfile in its own directory**: [`backend/Dockerfile`](backend/Dockerfile) and [`frontend/Dockerfile`](frontend/Dockerfile). They share no toolchain, no base image and no build context — the Go build cannot see the frontend's `node_modules` and the bundle cannot see the Go source.

## Layout

| Directory | What | Train |
|---|---|---|
| [`backend/`](backend/README.md) | The fiscal core. Go, layered, no I/O in the domain | `APP` · `ADAPTER` |
| [`frontend/`](frontend/README.md) | The browser client. React + TypeScript + Vite | `APP` |
| [`.github/workflows/`](.github/workflows) | The pipeline: gates on every push, release on a tag | — |

Directories for `contracts/`, `content/`, `infra/`, `policy/` and `runbooks/` are created when their lane opens, at the paths the topology already reserves for them, so nothing moves later.

**The ADR set lives outside this repository**, in the estate workspace alongside the Build Plan and the Master Specification. Links of the form `../../adr/...` in the service READMEs resolve there. Nineteen records cover the decisions that shape everything here — the ones that explain the choices below.

## Why the code looks the way it does

Four that surprise people, each recorded in an ADR, each with the same shape: the alternative fails silently.

**Division requires a rounding policy, and a policy cannot be written in Go.** `fiscal.Quo` takes a `RoundingPolicy` and there is no default, because rounding is jurisdiction-specific law rather than a runtime convention. The policy type has unexported fields and no literal constructor — the only way to obtain one is to decode it from a signed content bundle. ADR-0002 §2.2, §2.3.

**No `float64` is reachable from a fiscal value, and a CI-blocking analyzer proves it.** Not a review convention: `backend/tools/fiscalfloat` fails the build on a conversion, on a method returning a float, and on a struct that holds both kinds of number. ADR-0001 c1.

**The browser never computes a fiscal amount.** Money arrives as a canonical decimal string and is displayed. JavaScript has no decimal type, so a browser-side subtotal is a binary-float tax calculation with no rounding policy, no evidence record and no replay. Held by a branded string type with no arithmetic on it, plus lint. ADR-0019 C1.

**Nothing is mutated; everything is superseded.** Append-only storage, corrections as new rows, immutable sealed evidence, identifiers never recycled. A seal over mutable data is not a seal. ADR-0003, ADR-0011, ADR-0012.

## Verification

Every gate runs in CI on each push and pull request, and each is one job named after the control it enforces, so a red run says which guarantee broke.

```bash
cd backend && make check     # vet, analyzer, lint, tests, decimal cross-check
cd frontend && npm run lint && npm run build
```

The one worth knowing about: the decimal corpus in `backend/testdata/golden/decimal` is evaluated twice — once by the Go runtime and once by an independent Python implementation reading nothing but the vectors. Two conformant decimal implementations disagree on a tax total if they round at different points, and both answers look plausible. ADR-0002 §5.1 c2.
