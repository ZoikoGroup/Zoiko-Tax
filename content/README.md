# Content

Rule DSL sources and the packs compiled from them. Train: `CONTENT`.

> A tax rate in an environment variable is a legal statement with no provenance, no effective date, no approver and no replay record — and it would work perfectly, until someone asked why a decision from last March produced that number. — [ADR-0017](../../adr/ADR-0017-configuration-secrets-and-keys.md) §1

## What is here

| Path | What |
|---|---|
| `packs/` | Authored `.ztax` sources and each pack's `pack.json` declaration. The reviewed artifacts |
| `sources/register.json` | The source register: one `SourceLicenseRecord` per source (ZTAX-SRC-001 §5) |
| `build/` | Compiled manifests, their approvals and seals. Generated, gitignored |
| `.keys/` | Development signing, author and approver keys and their keyring. Generated, gitignored |

## The pipeline

```bash
cd ../backend
make content          # compile the pack and seal it
make content-verify   # verify it the way a cell will
```

`make content` runs four steps, separately, because in production they are done by different people on different machines:

```
pack.ztax ─┐                                approve (AUTHOR)    approve (APPROVER)
pack.json ─┼─compile──► <bundle>.manifest.json ──────► <bundle>.approvals.json ──sign──► <bundle>.seal.json
register ──┘   │                  │                   │                                 │
          type check, DAG    canonical bytes     each signed by the            ECDSA P-384 in the KMS,
          resolve packs      digested as zt1:…   approver's own key            covering the approvals
          rights gate
```

`ztax-contentc build` is still there — compile and sign with nothing in between — and what it produces loads in development only.

The manifest file **is** the canonical bytes (ADR-0011 §2.1). It is not JSON that could be canonicalized — it is the output of `internal/platform/canonical`, written verbatim, and a cell digests the bytes it read rather than re-encoding them. The digest is not in the manifest, because a document cannot contain its own digest; it is in the seal, which is what gets signed.

`make content-verify` runs the **cell's own loading path** — the same package, in the same order — so a bundle that passes in CI is a bundle a cell will accept.

## Packs, levels and sources

Each pack directory holds its `.ztax` source and a `pack.json` declaration: the pack's identity and semantic version, its package level, its status, what it may be claimed to support, where it may be deployed, which packs it depends on (with version constraints) and which sources it was derived from. The compiler writes that into the manifest's `pack` section, so it is signed with the rules.

```json
{
  "bundleId": "eu-vat-worked-2026.09",
  "packId": "ZTAX-CP-WORKED-EU-VAT",
  "version": "0.1.0",
  "level": "REGIONAL",
  "status": "RESEARCH",
  "capabilities": ["DETERMINE"],
  "deploymentModes": ["P0_ZOIKO_ONLY"],
  "dependencies": [{ "packId": "ZTAX-CP-GLOBAL-COMMON", "version": "^2.0" }],
  "sources": [{ "sourceId": "SRC-ZOIKO-WORKED-0001" }]
}
```

`compile` refuses the pack, and writes nothing, unless both gates pass:

**Dependency resolution** (ZTAX-CONT-001 §10–11). Every pack under `packs/` is available to resolve against, one version each. Levels are ordered `GLOBAL → REGIONAL → NATIONAL → SUBNATIONAL → SECTOR → AUTHORITY-ADAPTER → CUSTOMER-OVERLAY`, and a pack may depend only on its own level or a more general one — national law that depended on a customer overlay would be law that changes per customer. A missing dependency, an unsatisfied constraint (`1.2.3`, `^1.2`, `~1.2.3`, `>=1.4 <2`), a declared conflict or a cycle refuses the build. The load order is deterministic.

**Rights** (ZTAX-SRC-001 §4–6, §15). Every declared source must have a record in `sources/register.json` that is in `PRODUCTION` or `REVIEW_RENEW`, inside its licence term, not past its review date, licensed for every deployment mode the pack declares, and granting every right a bundle needs: `internal_use`, `transform`, `derived_output`, `commercial_use`, `historical_replay`, plus `private_bundle` for P2 and `edge_bundle` for P3, plus any `uses` the pack adds. A right the record does not mention is `UNKNOWN`, and `UNKNOWN` is `DENY`. The manifest records each source's licence reference and the digest of the record it was judged against.

The worked pack's one source is Zoiko-authored (class S7). It is in the register anyway, so the pack goes through the same gate a licensed source would. Its `reviewDue` is real: when it passes, `make content` stops working until somebody reviews the record and moves the date, which is the point.

## Four-eyes

No content is released on one person's say-so (CONT-001 §8). Between `compile` and `sign`, the author and an approver each run `ztax-contentc approve` with their own key; each approval is a signature over the manifest's exact digest. `sign` puts the approvals inside the seal, where the release signature covers them, and outside development refuses to sign without an `AUTHOR` and an `APPROVER` who are different people with different keys, neither of them the release key.

A cell enforces the same rule when it loads: outside `ZTAX_ENVIRONMENT=development` an unapproved bundle is refused. In development an unapproved local build loads, but approvals that are present are always verified. `make content-verify` verifies as production would (`-environment production`).

## What a cell does with it

Two environment variables, set together or not at all:

```
ZTAX_CONTENT_DIR=/srv/content        # holds one <bundle>.manifest.json + .seal.json
ZTAX_CONTENT_KEYRING=/srv/keyring.json
```

At startup, before the listener opens: verify the seal and every approval in it → verify the digest → check four-eyes (outside development) → type-check the graph → check it is acyclic → publish by atomic pointer swap. A bundle that fails any step never reaches the swap, so the cell keeps the bundle it already had ([ADR-0005](../../adr/ADR-0005-rule-dsl-execution-model.md) §2.6). A cell with neither variable set starts anyway and refuses determination with `NO_CONTENT_BUNDLE`, which is a legitimate deployment before A4.

`GET /v1/capabilities` reports the active bundle's identity, digest and IR version, so a caller can name the exact content that produced a response.

## The language

The full grammar is in the package comment of [`backend/internal/content/dsl`](../backend/internal/content/dsl/lex.go). It is small on purpose: no expression nesting, no operator precedence, no control flow. Every step is a named binding over earlier bindings, which is the rule DAG written out longhand — shared subexpressions are visible rather than discovered, and every node has a name a human chose, so the execution trace names it too.

```
bundle "eu-vat-worked-2026.09"
ir 1

policy line2 = HALF_UP scale 2 basis LINE

rule "vat" version "2026.09.1" semantic "ZTAX-RULE-WORKED-VAT" {
    const zero     money "0.00"   currency EUR
    const standard rate  "0.2100" basis NET

    input net    money "line.netAmount"
    input exempt bool  "line.exemptCertificateHeld"

    let vat     = apply_rate net standard policy line2
    let taxable = not exempt
    let payable = select taxable vat zero

    emit "TAX_VAT" payable
}
```

Three things the surface deliberately does not have:

**No `mul`, no `quo`.** IR version 1 implements neither for `Money`. Multiplication is `apply_rate`, so that every product landing at a currency scale carries a rounding policy; division waits on the inclusive-extraction semantics of [`ZTAX-DET-001`](../docs/specs/ZTAX-DET-001-global-tax-determination-engine.md) §7.4. A surface that could express them would compile bundles the evaluator refuses at request time, which is the worst place to find out.

**No default rounding.** `apply_rate` and `round` name a declared policy or they do not compile. There is no default because rounding is jurisdiction-specific law rather than a runtime convention ([ADR-0002](../../adr/ADR-0002-decimal-rounding-context-policy.md) §2.2).

**No unused bindings.** The executor walks every node, so an unreferenced binding costs latency and appears in the evidence trace as a step that contributed to nothing. It is also what a misspelled reference looks like from the compiler's side.

## What this pack is not

`packs/eu-vat` names no real jurisdiction, no real rate and no real threshold. It is a shape test that exercises every construct the IR offers, and it is the "worked pack" the [specification register](../docs/specs/README.md) asks for — a specification nobody has authored content against is a specification with untested assumptions.

A reader who finds a real jurisdiction named here has found the defect `ZTAX-PRD-REQ-0002` exists to detect.

## Signing

Development uses in-process P-384 keys — release signer, author and approver — generated by `ztax-contentc keygen` into `.keys/`; each `keygen` adds its key to the keyring rather than replacing it. `internal/platform/kms` refuses to build an in-process signer, or to generate a key, in any environment but `development` — the same fail-closed shape `secrets.EnvResolver` has, for the same reason.

Production signing is a KMS call and the private key has no in-process representation ([ADR-0017](../../adr/ADR-0017-configuration-secrets-and-keys.md) §2.6). Nothing in `ztax-contentc` changes when that lands: everything it does with a key goes through `kms.Signer`.

Verification resolves the key by the identifier in the seal, never by "the current key", so rotation does not invalidate a pack signed last year (§2.7).

## Location

This belongs at the estate root under `content/`, per the repository topology in [ADR-0007](../../adr/ADR-0007-repository-topology-and-go-module-layout.md) §2.1. It is here because this work was scoped to `backend/`, the same deviation `docs/specs/` carries. Moving it is a path change and nothing else.
