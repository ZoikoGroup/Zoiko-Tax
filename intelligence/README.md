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
  governance.py    the enforcement point — ADR-0006 §2.5's three refusals
  provenance.py    the governance context, mirroring ai.Provenance on the Go side
  decimal_wire.py  the fiscal boundary — ADR-0006 §2.3, canonical strings only
  service.py       the transport seam (see below)
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

### The transport seam

`service.py` serves no traffic, and that is deliberate rather than unfinished.

ADR-0006 §2.2 makes `contracts/schemas` the canonical schema authority with protobuf *derived* from it. Hand-writing `.proto` files here would create the second schema authority that clause exists to prevent — in the component where the two languages have to agree most precisely. So the transport waits on W2 lane K's contract pipeline.

The ordering is not convenience. The governance logic is testable with no transport, no provider and no model, and it is the half that has to be right. Building it the other way round produces a Gateway that can carry a request before it can refuse one.

## Running it

```
pip install -e '.[dev]'
python -m pytest          # 47 tests: the refusals, and the decimal boundary
python -m ruff check .
python -m mypy src tests  # strict, nothing waived
```

`mypy` is strict with no waivers because the Gateway decides whether a call may proceed — a type error here is an authorization bug.

## What is not here

- **The Intelligence Fabric proper** — provenance RAG, the SKU/ontology classifier, Change Intelligence extraction, the Evaluation Service and the adversarial harness. W2 lane L.
- **The Tool Registry and Broker** — action classes, independent authorization, idempotency, budgets. W2 lane L.
- **Any model provider integration.** The Gateway routes; nothing routes yet.

## The rule that holds regardless

Nothing in `backend/` imports across into this directory, and nothing here imports anything fiscal. The first is guaranteed by Go and Python being different languages; the second is a CI gate in `.github/workflows/intelligence.yml`, because the violation would arrive as a new import rather than as a new signature.

ADR-0007 §6 keeps extraction cheap precisely because no import reaches across a class boundary. That is what makes the W1 exit review a real choice rather than a formality.
