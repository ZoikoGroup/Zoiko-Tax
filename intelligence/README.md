# Intelligence Fabric

Python. Release train `AI`. Separate trust zone, separate cadence, separate approver.

This directory holds the **Governed Model Gateway** — the only path between the Go fiscal core and any model provider ([ADR-0006](../adr/ADR-0006-go-python-boundary-and-model-gateway.md) §2.1).

## Why this is a directory and not a repository

ADR-0007 §2.1 makes the estate one repository with class-labelled top-level directories, and §5.3 registers that as a deviation from the eight-repository mandate, with an expiry at W1 exit. This directory extends that deviation's scope to a ninth class; the extension is recorded in ADR-0007 §5.3 rather than absorbed quietly, because the ADR is explicit that the reading "should not be allowed to become permanent by inattention."

**Co-location is not integration.** Every separation ADR-0006 requires is a runtime property and none of it depends on where source files live:

| Separation | Where it is enforced |
|---|---|
| Separate process and deployable | Deployment |
| Separate network zone | Cell network policy denies Go workloads egress to provider endpoints |
| gRPC over mTLS as the only transport | §2.1 — no HTTP, no shared database, cache or filesystem |
| No conversion from AI types to fiscal types | `depguard`, CI-blocking, on the Go side |
| Separate credentials and vault namespace | ADR-0017 §2.5 |

The cost that *is* real is coarse access control: everyone with repository access reads everything, including prompt profiles and model configuration. ADR-0007 §5.2 names it, CODEOWNERS narrows write authority, and the W1 exit review is where extraction gets decided.

## What is here

```
src/ztax_gateway/
  governance.py            the enforcement point — ADR-0006 §2.5's three refusals
  provenance.py            the governance context, mirroring ai.Provenance on the Go side
  decimal_wire.py          the fiscal boundary — ADR-0006 §2.3, canonical strings only
  service.py               guarded(): the four steps every call takes
  wire.py                  the JSON wire form, strict both ways
  runtime.py               routing and the model-runtime seam
  fake_runtime.py          development-only canned runtime (see "The model runtime is a seam")
  server.py                the gRPC server (see "The transport")
  tool_broker.py           ToolCatalog, authorise(), guarded() — Chapter 17 §13, §16
  classifier.py            SKU/ontology classifier, A3 constrained auto-accept guard
  rag.py                   Provenance RAG over spec documents — Chapter 17 §9, §10
  citation.py              tamper-evident source citation references
  change_intelligence.py   ChangeCandidate extraction — Chapter 17 §8
  human_review.py          ReviewQueue, ReviewDecision, EvidencePanel — Chapter 17 §20
  invocation_evidence.py   InvocationEvidenceRecord, no-CoT ledger — Chapter 17 §14, §21
  capacity.py              CapacityLedger, QuotaPolicy, FinOpsReport — Chapter 17 §23
  observability.py         AuditBuffer, TelemetrySignal, OpenTelemetry — Chapter 17 §22
  resilience.py            ProviderRegistry, FallbackRouter, DegradedMode — Chapter 17 §6, §24
  evaluation.py            Evaluator, EvaluationRuleset — Chapter 17 §19
  evaluation_adversarial.py adversarial harness, ADV-001..005, ReleaseGate — Chapter 17 §28
  evaluation_quality.py    GoldSetStore, MetricEngine, ModelComparator — Chapter 17 §19
  evaluation_runner.py     EvaluationRunner — Chapter 17 §19
  evaluation_evidence.py   EvaluationEvidenceRecord — Chapter 17 §19, §21
  ai_security_controls.py  OutputSchemaValidator, TenantScopedIndex — Chapter 17 §28
  production_registries.py AIReleaseManifest, ManifestRegistry — Chapter 17 §14
  production_gates.py      release gates G-AIARCH-01..21 — Chapter 17 §29
  release_lifecycle.py     9-state lifecycle machine — Chapter 17 §29
  privacy_source_rights.py source-rights and privacy controls — Chapter 17 §15
  explanation.py           CustomerExplanationService — Chapter 17 §12
  drift.py                 BehaviorFingerprint, DriftDetector — Chapter 17 §19, §21
```

### The enforcement point

ADR-0006 §2.5 is the Gateway's reason for existing:

> The Gateway refuses unknown use cases, refuses A5 actions regardless of what any model or tool prompt says, and applies the per-use-case kill switch. The Go caller cannot override any of this, because the decision is made after the call leaves it.

That last clause is the design constraint, and it is visible in the signatures: `authorise(registry, provenance)` takes no options, no flags and no overrides. A parameter that could relax a refusal is a parameter that will eventually be passed.

`authorise` checks in order of decreasing blast radius — kill switch, registration, authority, risk tier, residency. The order does not change which calls are permitted; it changes what an operator sees during an incident, and "unknown use case" appearing while the global kill is engaged would send somebody looking in the wrong place.

Three decisions worth knowing:

- **A5 is refused before the per-use-case ceiling is consulted**, and `UseCaseRegistry.register` refuses to register a use case that declares A5 authority. No registry state can permit it.
- **A risk tier above the ceiling is refused, not downgraded.** Downgrading would let a caller reach a lower-scrutiny path by mislabelling its own request.
- **`kill()` does not validate the identifier.** During an incident the identifier in hand may be one nobody can find in the registry, and refusing to act on it because it is unrecognised is the wrong behaviour for a kill switch.

### The decimal boundary

ADR-0006 §2.3 sends every fiscal quantity as a canonical string, with `double` and `float` forbidden in every ZoikoTax proto. `decimal_wire.parse` refuses a `float` **argument**, not just a malformed string — the failure it guards is `json.loads`, which yields a float for any JSON number, silently.

It also refuses `int`, because an int carries no scale and cannot express whether `1` or `1.00` was meant, and scale is semantic (ADR-0011 §2.2).

> The test suite caught a real bug here. `Context.create_decimal` applies its precision by **rounding**, and rounding to 34 digits is not a trapped condition — so a 35-digit value arrived silently truncated rather than refused. It now parses at arbitrary precision and checks the width explicitly, which is what the Go side does and for the same reason.

### The transport

`server.py` serves the boundary: one gRPC method, `/ztax.gateway.v1.ModelGateway/Invoke`, over mTLS (ADR-0006 §2.1). Every call takes the four steps `service.guarded` enforces, in order — decode, `authorise` before the payload is touched, run, log the crossing — and leaves as a gRPC status with the estate reason code in the `ztax-reason` trailer, so the Go client (`backend/internal/adapter/gateway/grpc.go`) rebuilds the exact `errs.Error`.

**The codec is JSON, not protobuf, and that is a deviation to record in ADR-0006.** §2.2 wants protobuf *derived* from `contracts/schemas` with a drift gate, so that there is one schema authority. No schema-to-proto generator exists, and hand-writing `.proto` files would create the second authority §2.2 forbids. So the messages are the JSON of `contracts/schemas/ai/gateway-call.schema.json` and `gateway-reply.schema.json`, carried by gRPC with a registered `ztax-json` codec. Everything §3.1 chose gRPC for survives: a typed client on both sides (the wire structs and `wire.py`, each tested against the schemas), deadline propagation, mTLS identity per call. What is deferred is protobuf's compact encoding. Replacing the codec later changes `wire.py` and the Go codec and nothing else.

Both decoders are strict: a JSON number anywhere is refused (ADR-0006 §2.3 — `json.loads` would make it a float), and so is an unknown field.

**The model runtime is a seam.** `runtime.UnconfiguredRuntime` is the production default: a permitted call answers `AI_GATEWAY_NOT_CONFIGURED` until a provider adapter is approved and wired with credentials from the Gateway's own vault namespace (ADR-0017 §2.5). The governance path is real end to end; the model is not, yet.

`fake_runtime.py` provides a development-only alternative. `FakeRuntime` returns canned, deterministic responses for every `Kind` and never makes any real provider call. It is enabled with two environment variables:

```
ZTAX_GATEWAY_RUNTIME=fake        # selects FakeRuntime instead of UnconfiguredRuntime
ZTAX_ENVIRONMENT=development     # required guard; any other value exits non-zero
```

With neither variable set (the default), the Gateway behaves exactly as before: `UnconfiguredRuntime`. Setting `ZTAX_GATEWAY_RUNTIME=fake` in any environment other than `development` is refused at startup with a clear error and a non-zero exit code, so the fake cannot accidentally reach a real cell. Any unrecognised value of `ZTAX_GATEWAY_RUNTIME` is also refused at startup.

**One registry, two readers.** The use-case registry and routing are a reviewed AI-train file (`config/registry.dev.json` for the local stack). The Gateway loads it with `server.load_config`; the Go pre-check loads the same file with `gateway.LoadPolicy`, so the two decisions are made against one registry.

```
python -m ztax_gateway.server   # needs ZTAX_GATEWAY_CONFIG, _REGION, _AI_TRAIN and mTLS material
```

`backend/internal/adapter/gateway/e2e_python_test.go` drives the real server from Go when `ZTAX_E2E_PYTHON` names an interpreter with this package installed.

## Running it

```
pip install -e '.[dev]'
python -m pytest          # ~1900 tests across governance, codec, tools, classifiers, evaluation, RAG and more
python -m ruff check .
python -m mypy src tests  # strict, nothing waived
```

`mypy` is strict with no waivers because the Gateway decides whether a call may proceed — a type error here is an authorization bug.

## The Tool Registry & Broker

`tool_broker.py` is the authorization gate for every tool call the AI plane
makes.  It extends the "control before content" shape of `governance.py` from
*use cases* to *tools* (Chapter 17 §13, §16).

```
src/ztax_gateway/
  tool_broker.py   ToolCatalog, authorise(), guarded(), AgentAudit
```

### Action classes

Five ordered classes, increasing in privilege:

| Class | Description |
|---|---|
| `READ` | Read-only; no side effects |
| `PREPARE` | Stages a change; does not commit it |
| `MUTATE` | Writes to a mutable store; requires an idempotency token if flagged |
| `COMMIT` | Finalises a prepared change; requires an idempotency token if flagged |
| `PRIVILEGED` | Refused unconditionally — same shape as A5 in `governance.py` |

`PRIVILEGED` cannot be registered in the `ToolCatalog`: `register()` rejects
it, so no catalog state can permit it.

### The check order

`authorise(catalog, provenance)` refuses before work happens, in decreasing
blast-radius order:

1. **Malformed context** — invalid provenance refused immediately.
2. **Kill switch** — global kill checked before per-tool, so a global kill never
   surfaces as `UNKNOWN_TOOL` during an incident.
3. **Registration** — unknown tools refused.
4. **Suspension** — a suspended tool keeps its registration (readable history).
5. **`PRIVILEGED`** — refused unconditionally before any ceiling is consulted.
6. **Action-class ceiling** — `COMMIT` is the maximum; `PRIVILEGED` is refused
   at step 5, not here.
7. **Scope verification** — the caller must hold *every* scope the tool declares.
8. **Idempotency token** — required for `MUTATE` / `COMMIT` tools flagged
   `idempotent=True`; missing token is refused rather than silently tolerated.
9. **Budget** — steps, duration, token cost, monetary cost; refused rather than
   downgraded.

### Tests

35 tests in `tests/test_tool_broker.py`.  Every refusal code is covered,
the `ToolProfile` and `ToolProvenance` immutability are verified, and the
`guarded()` helper is exercised for both the permit and the refusal path.

## What is not here

- **Any live model provider integration.** `FakeRuntime` covers local development and CI. A real provider adapter requires Security/Lane C approval and vault credentials (ADR-0017 §2.5).
- **Temporal filtering in `rag.py`.** `KnowledgeBase` does FTS5 BM25 search today. Chapter 17 §9 requires `CURRENT` vs `AS_KNOWN_THEN` bitemporal filtering — deferred to W2 lane L.
- **Production mTLS.** `ZTAX_GATEWAY_INSECURE_LOCAL=true` is accepted only in `development`. Any deployed cell requires mutual TLS (ADR-0006 §2.1).

## The rule that holds regardless

Nothing in `backend/` imports across into this directory, and nothing here imports anything fiscal. The first is guaranteed by Go and Python being different languages; the second is a CI gate in `.github/workflows/intelligence.yml`, because the violation would arrive as a new import rather than as a new signature.

ADR-0007 §6 keeps extraction cheap precisely because no import reaches across a class boundary. That is what makes the W1 exit review a real choice rather than a formality.
