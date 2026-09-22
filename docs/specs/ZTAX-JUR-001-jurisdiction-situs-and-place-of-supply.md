# ZTAX-JUR-001 — Jurisdiction, Situs and Place of Supply

| | |
|---|---|
| **Document ID** | `ZTAX-JUR-001` |
| **Version** | 1.0.0-draft |
| **Status** | **DRAFT** — commissioned to close the Build Plan §2 gap register entry |
| **Date** | 22 September 2026 |
| **Tranche** | F1 — global core product contracts |
| **Owning lane** | H — Determination & Jurisdiction |
| **Authorization level** | Gates A2; required EFFECTIVE for A4 |
| **Consumed by** | `ZTAX-DET-001` · `ZTAX-OBL-001` · `ZTAX-FIN-001` · every country pack |
| **Depends on** | `ZTAX-CLS-001` (classification) · `ZTAX-DOM-001` (domain model) |
| **Implemented by** | [ADR-0003](../../../adr/ADR-0003-bitemporal-storage-pattern.md) · [ADR-0005](../../../adr/ADR-0005-rule-dsl-execution-model.md) · [ADR-0008](../../../adr/ADR-0008-persistence-and-migrations.md) · [ADR-0011](../../../adr/ADR-0011-canonicalization-digest-and-evidence-sealing.md) |

---

## 0. Why this document exists

Build Plan §2 records `ZTAX-JUR-001` as **absent from the consolidated specification** and on the critical path. Until it exists, W2 lane H can build the jurisdiction graph, the PostGIS dataset versioning and the resolution interface — and cannot baseline ordered situs evidence precedence, primary place of use, roaming or place-of-supply resolution. `ZTAX-DET-001` and `ZTAX-OBL-001` both declare it as a hard input.

This document is that baseline.

It is written to be **implementable without further interpretation**. Where a rule is jurisdiction-specific it is expressed as a content obligation with a named schema, never as prose that an engineer would have to encode — the distinction `ZTAX-PRD-REQ-0002` exists to detect.

### 0.1 Normative language

**MUST**, **MUST NOT**, **SHALL**, **SHOULD** and **MAY** are used per RFC 2119. Every **MUST** in this document is a conformance requirement with a registered identifier in §11 and a golden vector in §12.

### 0.2 What this document does not do

It does not name a single jurisdiction, boundary, rate or evidence rank. Those are **content**, they live on the `CONTENT` train, and they are versioned, signed and effective-dated. This document specifies the *shape* those things take and the *algorithm* that consumes them.

---

## 1. Scope

Jurisdiction resolution answers one question: **for this transaction, which taxing authorities have a claim, and on what evidence?**

It does not answer what tax is due — that is `ZTAX-DET-001`. It does not answer who must file — that is `ZTAX-OBL-001`. It produces a `JurisdictionResolution`, and both of those consume it.

The question is harder than it looks for three reasons this document is organised around:

1. **Evidence conflicts.** A customer with a French billing address, a German SIM, an Italian IP address and a Spanish bank account is not unusual. Every one of those is legally relevant evidence somewhere, and no single one is authoritative everywhere.
2. **The rules differ by what is being sold and where.** Mobile telecommunications in the United States are sourced to a primary place of use under 4 U.S.C. §§ 116–126. Electronically supplied services to EU consumers are sourced under Council Implementing Regulation (EU) 282/2011 Art. 24b, which requires *two non-contradictory items of evidence* rather than a precedence order. These are not variations on one algorithm; they are two algorithms, and both must be expressible.
3. **Boundaries move.** A point-in-polygon test is only deterministic relative to a boundary dataset, and boundaries are redrawn. A replay that uses today's boundaries to re-decide a transaction from 2027 produces a defensible-looking wrong answer.

---

## 2. The jurisdiction graph

**2.1** A `Jurisdiction` is a node in a versioned, acyclic graph. Each node **MUST** carry:

| Field | Meaning |
|---|---|
| `jurisdictionId` | Stable content identifier under the ADR-0012 §2.4 grammar, e.g. `jurisdiction:us-ca/alameda/berkeley` |
| `parentId` | The containing jurisdiction, or absent for a country |
| `level` | `COUNTRY` · `STATE` · `COUNTY` · `CITY` · `DISTRICT` · `SPECIAL` |
| `countryCode` | ISO 3166-1 alpha-2 |
| `timezone` | IANA zone identifier for the jurisdiction's civil time |
| `effectiveFrom` / `effectiveTo` | The dates this node exists as a taxing jurisdiction |

**2.2 The graph is acyclic and single-parent, and `SPECIAL` is the exception that proves it.**
A special-purpose district — a transit authority, a stadium district, a tourism improvement area — routinely overlaps several general jurisdictions and belongs to none of them cleanly. A `SPECIAL` node **MAY** therefore have no parent, and **MUST NOT** be resolved by hierarchy. It is resolved only by its own boundary or by an explicit membership list.

**2.3 Jurisdiction identity is never a surrogate key.**
`jurisdictionId` is a stable human-meaningful string. A content reviewer, an auditor, a golden vector and an authority instrument all need to name the same jurisdiction across every re-authoring of the content that describes it, and a UUID satisfies uniqueness while destroying exactly that property.

**2.4 A jurisdiction that ceases to exist is closed, never deleted.**
`effectiveTo` is set. Historical decisions reference it and must continue to resolve.

### 2.5 Boundary datasets are versioned artifacts

**2.5.1** Geospatial boundaries **MUST** be carried in a `BoundaryDataset` with an identifier, a version, a publication date, a source attribution and a content digest.

**2.5.2** Every `JurisdictionResolution` produced by a spatial method **MUST** record the `boundaryDatasetVersion` it used.

**2.5.3** A replay **MUST** resolve against the recorded version, not the current one.

> This is the requirement most likely to be skipped and most expensive to retrofit. A point-in-polygon test is a pure function of a point and a polygon set; it is deterministic only if the polygon set is pinned. Boundaries are redrawn by annexation, incorporation and dissolution — a US city annexing an unincorporated strip changes the correct answer for addresses in that strip from a date. Without §2.5.2, a replay of a 2027 transaction against 2031 boundaries returns a *different jurisdiction* with no indication that anything is wrong, and the reconciliation that discovers it will not be able to say which answer was right.

**2.5.4** A boundary dataset **MUST NOT** be mutated in place. A correction is a new version.

---

## 3. Situs evidence

**3.1** `SitusEvidence` is one typed, attributed signal about where a transaction occurred or where a service is used.

```
SitusEvidence {
  type        EvidenceType
  value       CanonicalLocation | string
  source      EvidenceSource       // who asserted it
  collectedAt timestamptz          // when it was asserted
  reliability ReliabilityClass     // see 3.4
}
```

**3.2 The closed evidence-type vocabulary.**

Adding a member is a `SCHEMA` train change with a new golden-vector set. The set is closed because an open one means a country pack can invent an evidence type that no other pack understands, and the precedence rules of §4 would then be comparing incommensurable things.

| Type | What it asserts |
|---|---|
| `PRIMARY_PLACE_OF_USE` | The street address where the customer primarily uses the service — a declared, contractually-held address |
| `SERVICE_ADDRESS` | The address where the service is delivered |
| `BILLING_ADDRESS` | The address the invoice is sent to |
| `FIXED_LINE_LOCATION` | The physical location of a fixed line or terminating circuit |
| `DEVICE_COORDINATES` | Latitude and longitude from the device |
| `NETWORK_LOCATION` | Cell site, access-point or network-element location |
| `SIM_COUNTRY` | The country of the SIM's mobile country code |
| `IP_GEOLOCATION` | Country or region inferred from an IP address |
| `BANK_LOCATION` | The country of the payment instrument |
| `CUSTOMER_DECLARATION` | An address or country the customer asserted |
| `SELLER_ESTABLISHMENT` | The supplier's fixed establishment relevant to the supply |
| `CUSTOMER_ESTABLISHMENT` | The customer's fixed establishment, for B2B supplies |
| `VAT_IDENTIFICATION` | A validated VAT identification number and its country |

**3.3 Evidence carries its source, and the source is not decoration.**
`EvidenceSource` is one of `CUSTOMER_ASSERTED`, `SELLER_RECORDED`, `NETWORK_DERIVED`, `THIRD_PARTY_VERIFIED`, `AUTHORITY_VERIFIED`. A jurisdiction's rules routinely distinguish them — a customer-asserted address and a validated VAT number are both evidence of establishment, and no authority treats them as equal.

**3.4 Reliability is content, not a property of the evidence type.**
`ReliabilityClass` is `STRONG`, `SUPPORTING` or `WEAK`, and **the assignment is made by the applicable `SitusRule`, not by this document**. An IP geolocation is supporting evidence for EU B2C electronically supplied services and is close to irrelevant for US mobile telecommunications sourcing. A single global ranking would be wrong in one of those two cases, and arguing about which is the wrong argument to have.

**3.5 Evidence is never discarded.**
Evidence that was collected and not used **MUST** be recorded in the resolution alongside the evidence that was. A resolution that records only the winning evidence cannot answer "what else did you know at the time", which is the first question in a dispute.

**3.6 Evidence has no default.**
An absent evidence item is absent. It **MUST NOT** be substituted with a value from another field, and **MUST NOT** be inferred from an unrelated one. A missing service address is not a billing address.

---

## 4. Resolution modes

**4.1** A `SitusRule` is content. It **MUST** declare exactly one `mode`, and this document defines three. A jurisdiction whose law does not fit one of the three is a finding against this document, not a licence to improvise.

### 4.2 `PRECEDENCE` — an ordered list, first match wins

The rule declares an ordered sequence of evidence types. The first type for which usable evidence exists determines the situs; the rest are recorded and not used.

```
SitusRule {
  mode: PRECEDENCE
  order: [PRIMARY_PLACE_OF_USE, SERVICE_ADDRESS, BILLING_ADDRESS]
  minimumReliability: STRONG
}
```

This is the shape of US mobile telecommunications sourcing under the Mobile Telecommunications Sourcing Act, where 4 U.S.C. § 117 sources charges to the customer's place of primary use — defined at § 124 — and everything else is fallback.

> The citations here and at §4.3 are illustrative of the *shape* each mode takes, and are not themselves content. No pack inherits them. A pack asserting US mobile sourcing declares its own rule, its own evidence order and its own authority reference, and this document's job is only to guarantee that both shapes are expressible.

**4.2.1** Evaluation **MUST** stop at the first usable item. It **MUST NOT** continue to look for agreement, and **MUST NOT** prefer a later item because more evidence supports it. A precedence rule that quietly becomes a vote is a different rule.

### 4.3 `QUORUM` — N non-contradictory items

The rule declares a required count and an eligible set. Resolution requires at least `required` items from the set that resolve to the **same** jurisdiction.

```
SitusRule {
  mode: QUORUM
  required: 2
  eligible: [BILLING_ADDRESS, IP_GEOLOCATION, BANK_LOCATION, SIM_COUNTRY, FIXED_LINE_LOCATION]
  contradictionPolicy: AMBIGUOUS
}
```

This is Council Implementing Regulation (EU) 282/2011 Art. 24b as it applies to electronically supplied services to non-taxable persons, and it is why §4.2 is not sufficient on its own: the EU rule is not "prefer the billing address", it is "two items that do not contradict each other".

**4.3.1** "Non-contradictory" means **resolving to the same jurisdiction at the level the rule names**, not being byte-identical. A billing address in Lyon and an IP geolocation in Marseille are non-contradictory at `COUNTRY` level and contradictory at `CITY` level. The rule **MUST** declare `agreementLevel`.

**4.3.2** When fewer than `required` items agree, the outcome is `INSUFFICIENT_EVIDENCE`. When two distinct jurisdictions each reach the quorum, the outcome is `AMBIGUOUS`. These are different facts and **MUST NOT** collapse: the first is fixable by collecting more evidence, the second is not.

### 4.4 `SPATIAL` — point in polygon

The rule resolves coordinates against a boundary dataset.

```
SitusRule {
  mode: SPATIAL
  coordinateSource: [DEVICE_COORDINATES, NETWORK_LOCATION]
  datasetId: "boundary:us-ca@2026.02"
  onOutsideAllBoundaries: FALL_BACK_TO_PARENT
}
```

**4.4.1** A spatial resolution **MUST** record the dataset version (§2.5.2).

**4.4.2** A point lying in more than one boundary at the same `level` is a **dataset defect**, not an ambiguity to resolve at request time. The resolution **MUST** return `AMBIGUOUS` and the dataset **MUST** be flagged. Silently picking one is how a systematic misallocation runs for a year.

**4.4.3** `SPECIAL` jurisdictions are resolved spatially and independently of the hierarchy, and a point **MAY** fall inside any number of them.

### 4.5 Composition

**4.5.1** A pack **MAY** declare different `SitusRule`s per `level`, per ontology class and per customer role (B2B/B2C). Selection among them is content and **MUST** be deterministic.

**4.5.2** Where two rules select at the same specificity, the content is invalid and the bundle **MUST** be refused at build. Runtime **MUST NOT** break the tie.

---

## 5. Primary place of use

**5.1** `PRIMARY_PLACE_OF_USE` is a held, contractual attribute of a customer's service, not a per-transaction input. It is the address the customer has declared as the place where the service is primarily used.

**5.2** A PPU address **MUST** be validated against the jurisdiction graph when it is recorded, not when it is used. A determination is not the place to discover that a customer's declared address does not exist.

**5.3** A PPU **MUST** carry an effective period. A customer who moves has two PPUs with adjacent periods, and a transaction resolves against the one effective at its **event time**, never at decision time.

**5.4** A change to a PPU **MUST NOT** retroactively alter decisions already made. Correcting a historical period is an explicit correction that produces new decisions superseding the originals, with the reason recorded.

> §5.3 and §5.4 together are the reason the estate is bitemporal. A customer who moved in March and told us in June has transactions in April and May whose correct answer depends on which question is being asked: "what did we decide" and "what should we have decided" are both legitimate, and both have to be answerable.

---

## 6. Roaming

**6.1** A transaction is **roaming** when the network location of use falls outside the jurisdiction of the subscriber's home network. This is a derived fact and **MUST** be recorded on the resolution rather than inferred downstream.

**6.2** Roaming does not have one sourcing answer, and this document **MUST NOT** invent one.
A pack **MUST** declare, per jurisdiction and per ontology class, whether a roaming transaction sources to the home jurisdiction, the visited jurisdiction, or both. Where both have a claim, the resolution returns both and `ZTAX-DET-001` applies each jurisdiction's rules independently.

**6.3** Where both jurisdictions have a claim, the resolution **MUST NOT** apportion. Apportionment is a determination concern with its own rounding policy and evidence, and doing it here would hide it from the trace.

**6.4** The home jurisdiction is derived from the SIM's mobile country code or the subscriber's registered home network, and the visited jurisdiction from `NETWORK_LOCATION`. Both **MUST** be recorded as evidence even when only one is used.

---

## 7. Place of supply and cross-border

**7.1** For supplies capable of crossing a border, the resolution **MUST** determine and record:

| Field | Meaning |
|---|---|
| `supplierJurisdiction` | Where the supplier is established for this supply |
| `customerJurisdiction` | Where the customer is established or belongs |
| `placeOfSupply` | The jurisdiction whose rules govern, per the applicable `SitusRule` |
| `customerRole` | `B2B` · `B2C` · `UNDETERMINED` |
| `crossBorder` | Whether supplier and place of supply differ |

**7.2 `customerRole` is evidence-driven and has three values, not two.**
`UNDETERMINED` is a real outcome — a customer who has supplied no VAT identification and no establishment evidence is not thereby a consumer. A pack **MUST** declare how `UNDETERMINED` is treated, and the common answer, treating it as B2C, **MUST** be stated in content rather than assumed by the runtime.

**7.3** A validated `VAT_IDENTIFICATION` is the strongest evidence of B2B status where a jurisdiction recognises it. Validation status — validated, unvalidated, validation-unavailable — **MUST** be recorded. "Unvalidated" and "validation service was down" are different facts and a pack **MAY** treat them differently.

**7.4 Reverse charge is a determination outcome, not a jurisdiction outcome.**
This document resolves *which jurisdiction*. Whether the liability shifts to the customer is a `ZTAX-DET-001` question answered from this resolution plus the rate rules. Resolving it here would put a liability decision in a component with no access to rates.

---

## 8. The resolution algorithm

**8.1** Resolution is a pure function of the canonical input, the content bundle and the boundary dataset. It **MUST NOT** read a clock, a network, a database or a geocoding service.

**8.2 The C0 hot path makes no uncached geocoding call.**
Address-to-coordinate resolution, where required, happens at ingestion and is carried on the canonical input as `DEVICE_COORDINATES` or an equivalent. A determination that needed to geocode is a determination that cannot meet the 150 ms p99 budget and cannot replay.

**8.3** The algorithm, normatively:

```
1. Select the applicable SitusRule from content, by jurisdiction level,
   ontology class and customer role. Deterministic; ties are invalid content.
2. Collect every SitusEvidence item on the canonical input.
3. Apply the rule's mode:
     PRECEDENCE — first usable item at or above minimumReliability
     QUORUM     — smallest set of >= required items agreeing at agreementLevel
     SPATIAL    — point in polygon against the pinned dataset version
4. Map the winning evidence to a jurisdiction node valid at event time.
5. Walk the hierarchy from that node to the root, collecting every ancestor
   valid at event time.
6. Collect every SPECIAL jurisdiction whose boundary contains the point, or
   whose membership list names the resolved node.
7. Order the applicable set outermost-first, breaking ties on jurisdictionId.
8. Record: the applicable set, the mode, the winning evidence, every unused
   evidence item, the dataset version, and the outcome.
```

**8.4 Step 7's tie-break is not cosmetic.** Two jurisdictions at one level would otherwise be returned in whatever order the store produced them, and a decision whose evidence lists them differently on two replicas is a decision that does not replay.

**8.5 Every step is traced.** The resolution **MUST** emit a step-by-step record in the same form as the determination trace: which rule was selected and why, which evidence was considered, which won, and what was discarded.

---

## 9. Outcomes

**9.1** A resolution **MUST** carry exactly one outcome.

| Outcome | Meaning | Recorded as |
|---|---|---|
| `RESOLVED` | One jurisdiction set, on sufficient evidence | A decision |
| `INSUFFICIENT_EVIDENCE` | Not enough evidence to satisfy the rule | A decision |
| `AMBIGUOUS` | Evidence supports two or more incompatible answers | A decision |
| `UNSUPPORTED` | Resolved, but no active pack covers the jurisdiction | A decision |
| `DATASET_DEFECT` | Overlapping boundaries at one level (§4.4.2) | A decision, and an incident |

**9.2 Every one of these is a recorded decision with full evidence, returned `200` with a non-authoritative marker. None is an error response.**
"We could not determine your jurisdiction" is a fact about our coverage and our inputs that a customer is entitled to have recorded against their transaction. An error response records nothing, and a customer who receives one has no evidence that they asked.

**9.3** `INSUFFICIENT_EVIDENCE` **MUST** name which evidence types would have satisfied the rule. A refusal that does not say what would fix it is a refusal the customer cannot act on.

**9.4** `DATASET_DEFECT` **MUST** raise an operational incident as well as recording a decision. It is the one outcome that indicates our data is wrong rather than the input being thin.

---

## 10. Determinism and replay

**10.1** Given the same canonical input, content bundle version and boundary dataset version, resolution **MUST** produce a byte-identical result, including the order of the applicable set and the order of the trace.

**10.2** The replay envelope for a resolution **MUST** carry: the canonical input, the event time, the decision time, the content bundle identity and digest, the boundary dataset identity and version, and the `canon` profile version.

**10.3** Anything not in the envelope is something resolution is not allowed to depend on. There is no third category.

**10.4** Resolution **MUST NOT** depend on the order in which evidence items appear on the input. Evidence is canonicalised and ordered by type before the algorithm runs.

---

## 11. Requirements register

Each is CI-verifiable and carries a golden vector.

| ID | Requirement |
|---|---|
| `ZTAX-JUR-REQ-0001` | Every jurisdiction node carries a stable content identifier, a level, a country, a timezone and an effective period |
| `ZTAX-JUR-REQ-0002` | A jurisdiction is closed by effective date, never deleted |
| `ZTAX-JUR-REQ-0003` | Every spatial resolution records the boundary dataset version it used |
| `ZTAX-JUR-REQ-0004` | A replay resolves against the recorded boundary dataset version |
| `ZTAX-JUR-REQ-0005` | A boundary dataset version is immutable |
| `ZTAX-JUR-REQ-0006` | The evidence-type vocabulary is closed; an unknown type refuses the bundle |
| `ZTAX-JUR-REQ-0007` | Evidence reliability is assigned by the situs rule, never globally |
| `ZTAX-JUR-REQ-0008` | Unused evidence is recorded on the resolution |
| `ZTAX-JUR-REQ-0009` | Absent evidence is never substituted or inferred |
| `ZTAX-JUR-REQ-0010` | A `PRECEDENCE` rule stops at the first usable item |
| `ZTAX-JUR-REQ-0011` | A `QUORUM` rule distinguishes insufficient evidence from contradiction |
| `ZTAX-JUR-REQ-0012` | A `QUORUM` rule declares its agreement level |
| `ZTAX-JUR-REQ-0013` | Overlapping boundaries at one level return `DATASET_DEFECT`, never a silent pick |
| `ZTAX-JUR-REQ-0014` | Two situs rules selecting at equal specificity refuse the bundle at build |
| `ZTAX-JUR-REQ-0015` | A PPU resolves against the period effective at event time, not decision time |
| `ZTAX-JUR-REQ-0016` | A PPU change does not retroactively alter existing decisions |
| `ZTAX-JUR-REQ-0017` | Roaming is recorded as a derived fact on the resolution |
| `ZTAX-JUR-REQ-0018` | Roaming sourcing is declared per pack; the runtime has no default |
| `ZTAX-JUR-REQ-0019` | Resolution never apportions between competing jurisdictions |
| `ZTAX-JUR-REQ-0020` | `customerRole` admits `UNDETERMINED`, and its treatment is content |
| `ZTAX-JUR-REQ-0021` | VAT identification validation status is recorded, including unavailability |
| `ZTAX-JUR-REQ-0022` | Resolution performs no clock, network, database or geocoding access |
| `ZTAX-JUR-REQ-0023` | The applicable set is ordered outermost-first with a deterministic tie-break |
| `ZTAX-JUR-REQ-0024` | Every refusal is a recorded decision returned `200`, never an error response |
| `ZTAX-JUR-REQ-0025` | `INSUFFICIENT_EVIDENCE` names the evidence that would have satisfied the rule |
| `ZTAX-JUR-REQ-0026` | Resolution is independent of the input order of evidence items |
| `ZTAX-JUR-REQ-0027` | Every resolution emits a step-by-step trace |

---

## 12. Conformance

**12.1** A country pack reaches `VALIDATION` only when it declares a `SitusRule` for every ontology class it covers, at every level it asserts.

**12.2** The launch-critical golden corpus **MUST** cover, at minimum:

- each resolution mode, with a passing and a failing case
- an evidence conflict resolved by precedence, and the same conflict under quorum reaching a different answer
- a quorum rule reaching `INSUFFICIENT_EVIDENCE`, and one reaching `AMBIGUOUS`
- a boundary change between two dataset versions, asserting that a replay against the historical version returns the historical jurisdiction
- a PPU change mid-period, asserting event-time resolution
- a roaming transaction under home-sourcing and under visited-sourcing content
- a `SPECIAL` district overlapping two cities
- a point outside all boundaries
- a B2B supply with validated VAT identification, one with unvalidated, and one with the validation service unavailable
- a resolution whose evidence arrives in two different input orders, asserting identical output

**12.3** `ZTAX-QA-001` treats any divergence in §12.2 as a release blocker, not a finding.

---

## 13. Implementation notes

The runtime in `backend/internal/domain/jurisdiction` implements §2, the ordering of §8.7 and the outcome model of §9. Three things in this document are specified and not yet built, and they are the W2 lane H backlog:

1. **`SitusRule` and the three modes (§4).** The type does not exist. `Method` on the current `Resolution` records how a resolution was reached but nothing selects or executes a rule.
2. **Boundary dataset versioning (§2.5).** The `jurisdiction` table carries a `boundary` column and no dataset version, so §2.5.2 cannot be satisfied as the schema stands. This needs a migration.
3. **The evidence model (§3).** `Location` carries an address and coordinates; it does not carry typed, attributed, ranked evidence, and §3.5's "record what you did not use" has nowhere to go.

None of these is a change to the decisions already taken. All three are additive.

---

## 14. Open questions

1. **Geocoding provenance.** §8.2 requires coordinates to arrive pre-resolved. Which geocoder, at what accuracy class, and how its version is recorded, is unsettled — and a geocoder change is a boundary change by another route, with the same replay consequence as §2.5.
2. **Boundary dataset sourcing and licensing.** Authoritative boundary data is licensed, and licence expiry is a registered High risk with a defined pack path. Which datasets, under what rights profile, is lane F's.
3. **Sub-address granularity.** A single street address can straddle a district boundary in some US jurisdictions. Whether the estate resolves below the address level, and on what evidence, is deferred.
4. **Establishment for supplies with several fixed establishments.** §7.1 assumes one supplier establishment per supply. Where a supplier has several, which one is relevant is a determination the content must make, and the schema for that determination is not written.
5. **Aggregated evidence from a single source.** A telecommunications operator supplying both SIM country and network location is one source asserting two evidence types. Whether that counts as two items for a `QUORUM` rule is a legal question per jurisdiction and is not answered here.
