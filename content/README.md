# Content

Rule DSL sources and the packs compiled from them. Train: `CONTENT`.

> A tax rate in an environment variable is a legal statement with no provenance, no effective date, no approver and no replay record — and it would work perfectly, until someone asked why a decision from last March produced that number. — [ADR-0017](../../adr/ADR-0017-configuration-secrets-and-keys.md) §1

## What is here

| Path | What |
|---|---|
| `packs/` | Authored `.ztax` sources. The reviewed artifact |
| `build/` | Compiled manifests and their seals. Generated, gitignored |
| `.keys/` | Development signing key and keyring. Generated, gitignored |

## The pipeline

```bash
cd ../backend
make content          # compile the pack and seal it
make content-verify   # verify it the way a cell will
```

`make content` runs `ztax-contentc build`, which is two steps that are also available separately because in production they happen on different machines:

```
pack.ztax ──compile──► <bundle>.manifest.json ──sign──► <bundle>.seal.json
             │                    │                          │
        type check           canonical bytes             ECDSA P-384
        DAG build            digested as zt1:…           in the KMS
```

The manifest file **is** the canonical bytes (ADR-0011 §2.1). It is not JSON that could be canonicalized — it is the output of `internal/platform/canonical`, written verbatim, and a cell digests the bytes it read rather than re-encoding them. The digest is not in the manifest, because a document cannot contain its own digest; it is in the seal, which is what gets signed.

`make content-verify` runs the **cell's own loading path** — the same package, in the same order — so a bundle that passes in CI is a bundle a cell will accept.

## What a cell does with it

Two environment variables, set together or not at all:

```
ZTAX_CONTENT_DIR=/srv/content        # holds one <bundle>.manifest.json + .seal.json
ZTAX_CONTENT_KEYRING=/srv/keyring.json
```

At startup, before the listener opens: verify the seal → verify the digest → type-check the graph → check it is acyclic → publish by atomic pointer swap. A bundle that fails any step never reaches the swap, so the cell keeps the bundle it already had ([ADR-0005](../../adr/ADR-0005-rule-dsl-execution-model.md) §2.6). A cell with neither variable set starts anyway and refuses determination with `NO_CONTENT_BUNDLE`, which is a legitimate deployment before A4.

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

Development uses an in-process P-384 key, generated by `ztax-contentc keygen` into `.keys/`. `internal/platform/kms` refuses to build an in-process signer, or to generate a key, in any environment but `development` — the same fail-closed shape `secrets.EnvResolver` has, for the same reason.

Production signing is a KMS call and the private key has no in-process representation ([ADR-0017](../../adr/ADR-0017-configuration-secrets-and-keys.md) §2.6). Nothing in `ztax-contentc` changes when that lands: everything it does with a key goes through `kms.Signer`.

Verification resolves the key by the identifier in the seal, never by "the current key", so rotation does not invalidate a pack signed last year (§2.7).

## Location

This belongs at the estate root under `content/`, per the repository topology in [ADR-0007](../../adr/ADR-0007-repository-topology-and-go-module-layout.md) §2.1. It is here because this work was scoped to `backend/`, the same deviation `docs/specs/` carries. Moving it is a path change and nothing else.
