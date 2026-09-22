# ZTAX-DET-001 — Global Tax Determination Engine

| | |
|---|---|
| **Document ID** | `ZTAX-DET-001` |
| **Version** | 1.0.0-draft |
| **Status** | **DRAFT** — commissioned to close the Build Plan §2 gap register entry |
| **Date** | 22 September 2026 |
| **Tranche** | F1 — global core product contracts |
| **Owning lane** | H — Determination & Jurisdiction |
| **Authorization level** | Gates A2; required EFFECTIVE for A4 |
| **Consumed by** | `ZTAX-FIN-001` · `ZTAX-SHD-001` · `ZTAX-CTC-001` · every customer-facing calculation |
| **Depends on** | `ZTAX-DOM-001` · `ZTAX-CONT-001` · `ZTAX-CLS-001` · **`ZTAX-JUR-001`** · `ZTAX-EVID-001` |
| **Implemented by** | [ADR-0002](../../../adr/ADR-0002-decimal-rounding-context-policy.md) · [ADR-0003](../../../adr/ADR-0003-bitemporal-storage-pattern.md) · [ADR-0004](../../../adr/ADR-0004-accumulator-concurrency-pattern.md) · [ADR-0005](../../../adr/ADR-0005-rule-dsl-execution-model.md) · [ADR-0011](../../../adr/ADR-0011-canonicalization-digest-and-evidence-sealing.md) · [ADR-0016](../../../adr/ADR-0016-error-model.md) |

---

## 0. Why this document exists

Build Plan §2 describes `ZTAX-DET-001` as **the mathematical constitution for every supported calculation** and records it as absent. `FIN`, `SHD`, `CTC` and every customer-facing calculation depend on it. Until it exists, W2 lane H can build the decimal runtime, the rule-DAG executor, the rounding and allocation primitives and the golden-vector runner — and cannot baseline tax formula semantics, tax-on-tax ordering, inclusive extraction, or threshold interaction with accumulators.

This document is that baseline. It is the one place where the arithmetic of a tax calculation is written down, and it is written to be implementable without interpretation.

### 0.1 Normative language

RFC 2119. Every **MUST** carries an identifier in §13 and a golden vector in §14.

### 0.2 The rule this document exists to enforce

> **No tax logic is Go.** Every formula, rate, ordering, threshold and rounding instruction in this document is a property of a signed content bundle. This document specifies what the runtime does with them, and nothing about what any of them are.

A reader who finds a jurisdiction named, a rate written or an ordering assumed anywhere below has found a defect in this document.

---

## 1. Scope

Determination answers: **given a transaction, a set of applicable jurisdictions and a classification, what tax is due, on what base, at what rate, rounded how, and why?**

It consumes a `JurisdictionResolution` from `ZTAX-JUR-001` and a `Classification` from `ZTAX-CLS-001`. It produces a `TaxDecision` carrying an execution trace sufficient to explain and to replay it.

It does not decide who files, who remits, or when — that is `ZTAX-OBL-001`, which consumes this.

---

## 2. The determination pipeline

**2.1** Determination proceeds in eight stages, in this order. The order is normative.

```
1. Canonicalise      the input, per ADR-0011. Digest it.
2. Resolve           jurisdiction, per ZTAX-JUR-001.
3. Classify          each line, per ZTAX-CLS-001.
4. Select            the applicable TaxComponents from content.
5. Read              every accumulator the selected components reference.
6. Evaluate          the component DAG.
7. Round and allocate at the points content declares.
8. Emit              the TaxDecision, its trace, and its contributions.
```

**2.2 Stage 5 completes before stage 6 begins.**
Every accumulator value the evaluation may read is read first and passed in. The evaluator **MUST NOT** query. This is what keeps evaluation pure, keeps the database interaction inside one transaction boundary, and makes the read set an explicit part of the replay envelope.

**2.3 Stages 6 and 7 are separate, and the separation is the point.**
Evaluation carries full precision throughout. Rounding happens only where content names a rounding point. A runtime that rounded as it went would produce answers that differ from every authority's worked examples, because authority instruments specify rounding points precisely to remove that ambiguity.

**2.4** A stage that cannot complete produces a **recorded outcome**, not an exception. §12.

---

## 3. The taxable base

**3.1** A `TaxableBase` is a `Money` amount plus a declaration of what it is a base *of*. It is never a bare number.

**3.2 Base kinds.** Closed set; adding one is a `SCHEMA` change.

| Kind | Definition |
|---|---|
| `LINE_NET` | The line's net amount before any tax |
| `LINE_GROSS` | The line's net plus every tax already applied to it |
| `COMPONENT_SUM` | The sum of named tax components' amounts — the tax-on-tax case |
| `NET_PLUS_COMPONENTS` | `LINE_NET` plus named components' amounts |
| `QUANTITY` | A `Quantity`, for per-unit charges |
| `DOCUMENT_NET` | The document's net total, for taxes assessed at document level |

**3.3** `COMPONENT_SUM` and `NET_PLUS_COMPONENTS` **MUST** name the components they include, by component identifier, explicitly. A base that says "all taxes applied so far" is a base that depends on evaluation order rather than on a declaration, and evaluation order is not a legal instrument.

**3.4 Base adjustments are ordered and declared.**
Exemptions, deductions, allowances and discounts apply to a base in a declared order, because they do not commute. A 10% discount and a fixed 50-unit exemption give different results depending on which applies first, and which is correct is a matter of law. Content **MUST** declare the sequence; the runtime **MUST NOT** have a default.

**3.5** A base **MUST NOT** go below zero through adjustment unless content declares `allowNegativeBase`. The default is to floor at zero and record that the floor was applied.

---

## 4. Rate application

**4.1 Rate kinds.** Closed set.

| Kind | Applied as |
|---|---|
| `AD_VALOREM` | `base × rate`, where rate is a proportion |
| `SPECIFIC` | `quantity × amountPerUnit`, where the amount is a `Money` |
| `FLAT` | A fixed `Money` amount, independent of base and quantity |
| `BRACKET` | A tiered schedule; §4.4 |

**4.2** Every `AD_VALOREM` rate **MUST** declare its `RateBasis` — `NET`, `GROSS`, `PER_UNIT` or `COMPOUND`. A bare proportion is not a rate: `0.06` means nothing until it is known what it applies to.

**4.3** Every rate application **MUST** carry a `RoundingPolicy` naming mode, scale and basis. There is no default, and the parameter is required, so a caller that does not know which rounding applies does not yet know enough to compute.

### 4.4 Bracket rates

**4.4.1** A `BRACKET` schedule is an ordered list of `{ threshold, rate }` with a declared `bracketMode`:

| Mode | Semantics |
|---|---|
| `MARGINAL` | Each tier's rate applies only to the portion of the base within that tier |
| `CLIFF` | The rate of the highest tier reached applies to the whole base |

**4.4.2** The two produce materially different answers and **MUST NOT** be inferred. A schedule that omits `bracketMode` refuses the bundle at build.

**4.4.3** A `MARGINAL` schedule's tiers **MUST** be contiguous and ascending, with no gap and no overlap. Validated at bundle build, not at evaluation.

---

## 5. Tax components and ordering

**5.1** A `TaxComponent` is the atomic unit of determination: one tax, in one jurisdiction, at one rate, on one base.

```
TaxComponent {
  componentId     ContentId      // stable, human-meaningful
  jurisdictionId  JurisdictionId
  taxType         string         // content vocabulary
  base            TaxableBase
  rate            Rate
  policy          RoundingPolicy
  inclusive       bool           // §7
}
```

**5.2 Components form a directed acyclic graph.**
An edge runs from component *B* to component *A* when *A*'s base includes *B*'s amount. This is tax-on-tax, and it is the structure that makes ordering a property of the content rather than of the code.

**5.3 The graph MUST be acyclic, and the check happens at bundle build.**
Two components that each compound on the other describe a fixed point, not a calculation. Content declaring one is invalid and the bundle **MUST** be refused whole.

**5.4 Evaluation is in topological order, with a deterministic tie-break on `componentId`.**
Where two components are independent, their relative order does not affect their values — but it does affect the trace, and a trace that varies between replicas is a decision that does not replay.

**5.5 Ordering is never inferred from jurisdiction level.**
It is tempting to assume federal compounds before state before city. It is wrong often enough to matter, and where it is right, content says so. The runtime **MUST NOT** hold a hierarchy-derived default.

**5.6 A component whose base names a component that did not apply uses zero for it, and records that it did so.**
This is the one place a zero substitution is permitted, and it is permitted because "the component did not apply" is a determinate fact rather than missing data. The trace **MUST** record it, so a reviewer can see that a compounding base was thinner than it looks.

---

## 6. The exclusive calculation

**6.1** For a component with `inclusive = false`:

```
base_i    = resolve(base declaration)      // full precision
raw_i     = apply(rate_i, base_i)          // full precision
amount_i  = round(raw_i, policy_i)         // the only rounding
```

**6.2** `base_i` **MUST** be resolved at full context precision from already-evaluated components' **rounded** amounts, not their raw values.

> This is a real choice and it goes the other way from the usual instinct. Compounding on unrounded intermediates would be more "accurate" in an abstract sense and would disagree with every authority's worked example, because the authority's example compounds on the tax amount as it appears on the invoice. The invoice number is the legal number.

**6.3** The result of §6.1 is exact for the declared policy. There is no second rounding.

---

## 7. Inclusive extraction

The hard case, and the one most often got wrong.

**7.1** A component with `inclusive = true` asserts that its amount is *already contained* in the stated price. The stated price is a `LINE_GROSS`, and the net must be extracted from it.

**7.2 The extraction MUST be exact: the extracted net plus every extracted tax MUST equal the stated gross, to the minor unit, with no residual.**

A calculation that extracts each tax independently and hopes the parts sum to the whole will be out by a minor unit often enough to be a standing reconciliation defect. §7.4 is the construction that cannot be out.

### 7.3 The effective multiplier

**7.3.1** Let the inclusive components be *i ∈ I*, evaluated in the topological order of §5.4. Define each component's **multiplier on net**, `m_i`:

- If component *i*'s base is `LINE_NET`: `m_i = r_i`
- If component *i*'s base is `NET_PLUS_COMPONENTS(J)`: `m_i = r_i × (1 + Σ_{j∈J} m_j)`
- If component *i*'s base is `COMPONENT_SUM(J)`: `m_i = r_i × Σ_{j∈J} m_j`

Every *j ∈ J* precedes *i* in topological order, so every `m_j` is known when `m_i` is computed.

**7.3.2** The effective multiplier is `M = 1 + Σ_{i∈I} m_i`, and by construction `G = N × M`.

**7.3.3** `m_i` and `M` are carried at full context precision and are **never** rounded. They are intermediate quantities with no legal existence; rounding them would introduce error into a calculation whose whole purpose is to be exact.

**7.3.4** `SPECIFIC` and `FLAT` components cannot participate in `M`, because they are not proportional to net. An inclusive component of either kind **MUST** be subtracted from the gross before extraction begins, and content **MUST** declare that subtraction order. A bundle mixing them without a declared order refuses at build.

### 7.4 The extraction

```
1. G  := the stated gross, after subtracting any inclusive FLAT/SPECIFIC amounts (§7.3.4)
2. M  := the effective multiplier (§7.3.2), full precision
3. N  := round(G / M, extractionPolicy)          // the ONLY rounding in extraction
4. T  := G − N                                    // exact by construction, never computed from rates
5. allocate T across I by largest remainder, weights m_i, ties on componentId
```

**7.5** Step 4 **MUST** be a subtraction, not a rate application. Computing each tax from the rate and summing is what produces the residual §7.2 forbids.

**7.6** Step 5 **MUST** use the largest-remainder method with ascending `componentId` as the tie-break. It is the only allocation that is simultaneously exact, stable and explainable to an auditor in one sentence.

**7.7** `extractionPolicy` is content. It is frequently, but not always, the currency's minor-unit scale, and where a jurisdiction specifies otherwise, content says so.

**7.8** Mixed inclusive and exclusive components on one line are permitted. The inclusive set is extracted first per §7.4, then the exclusive components evaluate per §6 against the resulting net.

---

## 8. Rounding points

**8.1** Rounding occurs **only** where content names a point, through `RoundingPolicy.basis`:

| Basis | Rounds at |
|---|---|
| `LINE` | Each line, per component |
| `TAX_COMPONENT` | Each component, across the document |
| `JURISDICTION_TOTAL` | Each jurisdiction's total, across the document |
| `DOCUMENT` | The document total |

**8.2** Where the basis is coarser than the line, the rounded total **MUST** be allocated back across the contributing lines by largest remainder, so that the line amounts sum to the rounded total exactly.

**8.3 This is the requirement that makes UI totals match filed totals.**
A total displayed as the sum of independently-rounded lines can differ from the total on the return by a minor unit per line. §8.2 removes the possibility rather than documenting the discrepancy.

**8.4** Intermediate values between two rule steps **MUST NOT** be materialised at a currency scale. A `Money` reaches its minor-unit scale when it is written to a `FiscalDocument`, a `FiscalLine` or an API response, and not before.

---

## 9. Accumulators and thresholds

**9.1** An `Accumulator` is a running total over a key. The key **MUST** be composed only of: tenant, jurisdiction, threshold identifier, period identifier and an optional customer or registration scope. Nothing else.

**9.2 The value an evaluation reads is the value before this transaction.**
Read at stage 5, passed into the frame, never re-read. A rule that needs the post-transaction total computes it as `pre + thisTransaction`, which it can, because both are in the frame.

**9.3 Threshold kinds.** Closed set.

| Kind | Semantics |
|---|---|
| `REGISTRATION` | Above it, a registration obligation arises |
| `DE_MINIMIS` | Below it, no tax is due |
| `CAP` | Above it, no further tax is due in the period |
| `BRACKET` | Selects a tier in a `BRACKET` rate |

**9.4 Crossing semantics MUST be declared.**

| Mode | On the transaction that crosses |
|---|---|
| `PROSPECTIVE` | The new treatment applies from the next transaction |
| `INCLUSIVE` | The new treatment applies to the crossing transaction in full |
| `SPLIT` | The crossing transaction is split at the threshold; each part is treated under its own side |

**9.5 `SPLIT` is mandatory for `CAP` unless content declares otherwise.**
A transaction that straddles a cap is the common case, not the edge case, and treating it as wholly above or wholly below the cap is wrong by construction. The split amounts **MUST** be recorded as separate contributions so the trace shows both parts.

**9.6** A threshold crossing **MUST** emit a `ThresholdCrossing` record and an event, in the same transaction as the decision that crossed it. A crossing discovered later by reconciliation is a crossing that did not drive the obligation it should have.

**9.7** Every accumulator contribution **MUST** be idempotent on `(accumulatorKey, sourceDecisionId)`. One decision contributes to one accumulator at most once, whatever happens above it in the stack.

**9.8** A threshold comparison **MUST** declare whether it is strict. "Exceeds" and "reaches" differ by exactly one minor unit at exactly the threshold, which is exactly where it will be tested.

---

## 10. Adjustments, credits and refunds

**10.1** An adjustment references the decision it adjusts, by `decisionId`.

**10.2 An adjustment MUST be evaluated against the content bundle that produced the original decision, not the current one.**

> This is the single most consequential rule in this document. A credit note issued in 2029 against an invoice from 2027 reverses tax that was charged at 2027 rates under 2027 rules. Evaluating it against current content credits an amount that was never charged, and the difference is a permanent error in the subledger that reconciliation will find and will not be able to explain.

**10.3** The original bundle is named in the original decision's replay envelope, so §10.2 requires no new machinery — it requires the runtime to use it.

**10.4 Adjustment kinds.**

| Kind | Effect |
|---|---|
| `FULL_REVERSAL` | Negates every component of the original |
| `PARTIAL_CREDIT` | Credits a stated amount; §10.5 |
| `CORRECTION` | Supersedes the original with a re-determination on corrected input |
| `REFUND` | A payment-side reversal that references, and does not re-determine, the original |

**10.5** A `PARTIAL_CREDIT` **MUST** allocate the credited amount across the original's lines and components by largest remainder, weighted by each component's share of the original. It **MUST NOT** re-apply rates to the credited amount: doing so recomputes rather than reverses, and the two differ wherever rounding occurred.

**10.6** A `CORRECTION` produces a **new decision superseding the original**, under ADR-0003's append-only regime. The original is never modified, and both remain retrievable.

**10.7** The net effect of an adjustment chain **MUST** be derivable by summing the chain. A chain whose sum does not equal the intended position is a defect, and the property test in §14 asserts it over generated chains.

---

## 11. Determinism and replay

**11.1** Given the same canonical input, content bundle, accumulator read set, decision time and event time, determination **MUST** produce a byte-identical decision and a byte-identical trace.

**11.2** The replay envelope **MUST** carry: canonical input, decision time, event time, bundle identity and digest, the seven release-train versions, the `IR_VERSION`, the `canon` profile version, every rounding policy applied, the boundary dataset version from the jurisdiction resolution, and the accumulator read set.

**11.3 The accumulator read set is part of the envelope.**
A determination that depended on a running total cannot be replayed without the total it saw. Omitting it makes every threshold-sensitive decision unreplayable, and those are the decisions most likely to be disputed.

**11.4** Anything not in the envelope is something determination is not allowed to depend on.

**11.5** The evaluator **MUST NOT** have access to a clock, a random source, a network, a filesystem, a database or the AI plane — not by policy but structurally, because the evaluation frame does not carry them.

---

## 12. Outcomes

**12.1** A determination **MUST** carry exactly one outcome.

| Outcome | Meaning |
|---|---|
| `AUTHORITATIVE` | A complete determination this deployment is authorized to make authoritative |
| `ADVISORY` | A complete determination the deployment is not authorized to make authoritative |
| `AMBIGUOUS` | Applicable content admits more than one answer and does not resolve precedence |
| `CONFLICTED` | Applicable rules produced contradictory outcomes |
| `UNSUPPORTED` | No active content covers this transaction shape or jurisdiction |
| `REVIEW_REQUIRED` | Held for human review before it may be treated as authoritative |

**12.2** Every outcome is a **recorded decision with full evidence**, returned `200` with an explicit non-authoritative marker where applicable. None is an error response.

**12.3** `AUTHORITATIVE` requires **all** of: A4 authorization, the applicable pack in `PRODUCTION`, conformance green, release evidence sealed, and an authoritative jurisdiction resolution. Absent any one, the outcome is `ADVISORY`.

**12.4** An `ADVISORY` decision **MUST** be structurally distinguishable from an `AUTHORITATIVE` one at every layer — in the record, on the wire and in the UI — and **MUST NOT** be distinguished only by a boolean field on a shared type.

---

## 13. Requirements register

| ID | Requirement |
|---|---|
| `ZTAX-DET-REQ-0001` | The eight pipeline stages execute in the declared order |
| `ZTAX-DET-REQ-0002` | Every accumulator is read before evaluation begins; the evaluator never queries |
| `ZTAX-DET-REQ-0003` | A base declaration names its kind; a bare amount is never a base |
| `ZTAX-DET-REQ-0004` | A compounding base names its components explicitly, never "all taxes so far" |
| `ZTAX-DET-REQ-0005` | Base adjustment order is declared; the runtime has no default |
| `ZTAX-DET-REQ-0006` | A base floors at zero unless content permits negative |
| `ZTAX-DET-REQ-0007` | Every rate declares its basis |
| `ZTAX-DET-REQ-0008` | Every rate application carries a rounding policy; there is no default |
| `ZTAX-DET-REQ-0009` | A bracket schedule declares `MARGINAL` or `CLIFF`; neither is inferred |
| `ZTAX-DET-REQ-0010` | Marginal tiers are contiguous and ascending, validated at bundle build |
| `ZTAX-DET-REQ-0011` | The component graph is acyclic, validated at bundle build |
| `ZTAX-DET-REQ-0012` | Components evaluate in topological order with a deterministic tie-break |
| `ZTAX-DET-REQ-0013` | Compounding order is never inferred from jurisdiction hierarchy |
| `ZTAX-DET-REQ-0014` | A base referencing an inapplicable component uses zero and records it |
| `ZTAX-DET-REQ-0015` | Compounding uses rounded component amounts, not raw values |
| `ZTAX-DET-REQ-0016` | Exclusive calculation rounds exactly once, at the declared policy |
| `ZTAX-DET-REQ-0017` | Inclusive extraction is exact: net plus taxes equals gross, no residual |
| `ZTAX-DET-REQ-0018` | The effective multiplier is carried at full precision and never rounded |
| `ZTAX-DET-REQ-0019` | Extracted tax is computed by subtraction, never by re-applying rates |
| `ZTAX-DET-REQ-0020` | Extracted tax is allocated by largest remainder with a deterministic tie-break |
| `ZTAX-DET-REQ-0021` | Inclusive flat and specific amounts are subtracted before extraction, in declared order |
| `ZTAX-DET-REQ-0022` | Rounding occurs only at content-declared points |
| `ZTAX-DET-REQ-0023` | A coarse rounding basis allocates back to lines so lines sum to the rounded total |
| `ZTAX-DET-REQ-0024` | Intermediate values are not materialised at currency scale between rule steps |
| `ZTAX-DET-REQ-0025` | Accumulator keys are composed only of the permitted elements |
| `ZTAX-DET-REQ-0026` | Threshold crossing mode is declared; `CAP` defaults to `SPLIT` |
| `ZTAX-DET-REQ-0027` | A crossing emits a record and an event in the decision's transaction |
| `ZTAX-DET-REQ-0028` | Accumulator contribution is idempotent on decision identity |
| `ZTAX-DET-REQ-0029` | Threshold comparison declares whether it is strict |
| `ZTAX-DET-REQ-0030` | An adjustment evaluates against the original decision's content bundle |
| `ZTAX-DET-REQ-0031` | A partial credit allocates; it never re-applies rates |
| `ZTAX-DET-REQ-0032` | A correction supersedes; the original is never modified |
| `ZTAX-DET-REQ-0033` | An adjustment chain sums to the intended net position |
| `ZTAX-DET-REQ-0034` | The replay envelope carries the accumulator read set |
| `ZTAX-DET-REQ-0035` | The evaluator has no access to clock, randomness, network, storage or AI |
| `ZTAX-DET-REQ-0036` | Every outcome is a recorded decision returned `200`, never an error response |
| `ZTAX-DET-REQ-0037` | `AUTHORITATIVE` requires all five A4 conditions simultaneously |
| `ZTAX-DET-REQ-0038` | Advisory and authoritative results are structurally distinct types |

---

## 14. Conformance

**14.1 Golden vectors.** `ZTAX-QA-001` requires, at minimum:

- each rate kind, each base kind, each bracket mode
- a two-level compound: B on net, A on net + B, asserting A uses B's **rounded** amount
- a three-level compound with a diamond dependency
- a cyclic component graph, asserting refusal at build
- inclusive extraction with one component; with two on net; with two compounding
- inclusive extraction where naive per-rate computation leaves a residual, asserting exactness
- inclusive extraction mixing an inclusive flat amount with proportional components
- mixed inclusive and exclusive components on one line
- `DOCUMENT` rounding basis, asserting lines sum exactly to the rounded total
- each threshold kind, each crossing mode
- a transaction straddling a `CAP` under `SPLIT`, asserting both parts are recorded
- a threshold comparison exactly at the threshold, strict and non-strict
- a full reversal, a partial credit, a correction, and a chain of three summing to a known position
- an adjustment against a superseded bundle, asserting the **original** rates were used
- a determination evaluated twice, asserting byte-identical traces

**14.2 Property tests.** Asserted over generated inputs:

| Property | Statement |
|---|---|
| Extraction exactness | `net + Σ tax = gross`, always |
| Allocation totality | Allocated parts sum to the allocated whole, always |
| Compounding acyclicity | Every accepted bundle has a topological order |
| Adjustment closure | A reversal of a determination nets to zero |
| Idempotence | Re-determining identical input yields an identical decision |
| Threshold monotonicity | Crossing a threshold never decreases the cumulative total |

**14.3** Mutation testing runs against the rule DSL and compiler. A mutant that survives is a gap in §14.1.

---

## 15. Runtime requirements

The runtime in `backend/internal/domain/rule` implements the execution model of ADR-0005 — the typed IR, bundle load with acyclicity and type checking, and the deterministic evaluator. This document requires instructions it does not yet have. They are additive and are the W2 lane H backlog.

| # | Required | Status |
|---|---|---|
| 1 | `QUO` on `Money` under an explicit policy | Declared in the IR, refuses at evaluation. **Required by §7.4 step 3** — inclusive extraction is impossible without it |
| 2 | `ALLOCATE` — distribute an amount across weights by largest remainder | `fiscal.Allocate` exists and no IR instruction reaches it. **Required by §7.4 step 5, §8.2, §10.5** |
| 3 | `BRACKET` — tiered lookup with `MARGINAL` and `CLIFF` | Not present. Required by §4.4 |
| 4 | `MIN` / `MAX` on `Money` | Not present. Required by §9 cap and de-minimis handling |
| 5 | Component-DAG evaluation above the node DAG | Not present. §5's component graph is a distinct structure from the instruction graph, and §7.3's multiplier walk needs it |
| 6 | `SPLIT` at a threshold, emitting two contributions | Not present. Required by §9.5 |

**15.1** Each addition is an `IR_VERSION` increment with a new golden-vector set, and a bundle declaring a newer IR version than the runtime supports **MUST** be refused rather than partially executed.

**15.2** The existing evaluator's determinism properties — stable topological order, no clock, no I/O — are preconditions for all six and **MUST NOT** be relaxed to accommodate them.

---

## 16. Open questions

1. **Currency conversion inside determination.** Where a base is in one currency and a rate is assessed in another, whether conversion happens in determination or in `FIN-001`, and which rate date governs, is unsettled. It interacts with §11.2, because a conversion rate becomes part of the replay envelope.
2. **Zero-rate versus exempt versus out-of-scope.** All three produce no tax and are legally distinct, with different reporting consequences. The vocabulary belongs in `CLS-001` and this document consumes it; the boundary needs settling jointly.
3. **Negative-amount rounding under `HALF_UP`.** ADR-0002 §7.2 left open whether "away from zero" or "toward positive infinity" applies to credits. §10 makes this live: a reversal must round symmetrically with the original or the chain will not sum to zero.
4. **Document-level thresholds across periods.** A document spanning a period boundary contributes to two accumulators. Whether it splits, and on what basis, is not answered here.
5. **Successive inclusive extraction at document level.** §7 is specified per line. A jurisdiction assessing an inclusive tax on a document total requires the same construction one level up, and whether the two compose or conflict needs a worked example before it is asserted.
6. **Rate effective-dating within a period.** A rate change mid-period against a `DOCUMENT` rounding basis raises the question of which rate applies to the document total. `CONT-001` owns effective dating; this needs a joint answer.
