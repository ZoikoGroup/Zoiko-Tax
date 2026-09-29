# Specifications

The two documents Build Plan §2 recorded as **absent from the consolidated specification and on the critical path**.

| ID | Title | Status | Requirements |
|---|---|---|---|
| [`ZTAX-JUR-001`](ZTAX-JUR-001-jurisdiction-situs-and-place-of-supply.md) | Jurisdiction, Situs and Place of Supply | DRAFT | 27 |
| [`ZTAX-DET-001`](ZTAX-DET-001-global-tax-determination-engine.md) | Global Tax Determination Engine | DRAFT | 38 |

Read `JUR-001` first. `DET-001` consumes its output and assumes its vocabulary.

## What these unblock

Build Plan §5 W2 lane H is **GATED**, and names exactly what each document releases:

- *Blocked on `DET-001`* — tax formula semantics, tax-on-tax ordering, inclusive extraction, threshold interaction with accumulators
- *Blocked on `JUR-001`* — ordered situs evidence precedence, PPU, roaming and place-of-supply resolution

`ZTAX-QA-001`'s golden corpus and `ZTAX-OBL-001`'s obligation resolution both consume them, and no country pack can pass determination certification until they are EFFECTIVE.

## How to read them

Both are **normative**. RFC 2119 language throughout, every **MUST** carries a registered requirement identifier and a golden vector.

Neither names a jurisdiction, a rate, an evidence rank or an ordering. Those are content on the `CONTENT` train — versioned, signed and effective-dated. These documents specify the *shape* content takes and the *algorithm* that consumes it. A reader who finds a jurisdiction named in either has found a defect, and it is the defect `ZTAX-PRD-REQ-0002` exists to detect.

## Status and what happens next

**DRAFT.** They are commissioned to close a gap register entry, not approved. The lifecycle is DRAFT → REVIEW → APPROVED → EFFECTIVE, and A4 requires EFFECTIVE.

Before REVIEW, three things are owed:

1. **Named owners.** Build Plan W0 requires these commissioned with named owners. Neither has one.
2. **Answers to the open questions.** `JUR-001` §14 carries five, `DET-001` §16 carries six. Several are joint with `CLS-001`, `CONT-001` and `FIN-001` and cannot be closed unilaterally.
3. **A worked pack.** A specification nobody has authored content against is a specification with untested assumptions. The cheapest test is one non-US pack, which is also the standing `ZTAX-PRD-REQ-0002` gate.

## What the runtime still owes them

`DET-001` §15 lists six instructions the rule engine does not have, verified against the code:

| Required by | Instruction | State |
|---|---|---|
| §7.4 inclusive extraction | `QUO` on Money | Declared in the IR; refuses at evaluation |
| §7.4, §8.2, §10.5 | `ALLOCATE` | `fiscal.Allocate` exists; no instruction reaches it |
| §4.4 bracket rates | `BRACKET` | Absent |
| §9 caps and de-minimis | `MIN` / `MAX` | Absent |
| §5, §7.3 | Component-DAG evaluation | Absent — distinct from the instruction DAG |
| §9.5 cap straddling | `SPLIT` | Absent |

`JUR-001` §13 lists three: the `SitusRule` type and its three modes, boundary dataset versioning (which needs a migration), and the typed evidence model.

All nine are additive. None reopens a decision already taken, and each is an `IR_VERSION` increment with its own golden-vector set.

## Location

These belong at the estate root under `docs/`, per the repository topology in ADR-0007 §2.1. They are here because this work was scoped to `backend/`. Moving them is a path change and nothing else.
