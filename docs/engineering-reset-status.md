# Engineering reset — status against the playbook

Working note against *Engineering Reset & Delivery Playbook* (ZTAX-ENG-RESET-001 v1.0, 15 October 2026). It records what the build now demonstrates, what it does not, and what needs an owner. It is engineering evidence, not a normative document; the playbook itself is not yet in `register.yaml`, and registering it is its owners' decision under GOV-001.

## What changed

The transaction spine now determines tax **by location**. Before this change, quote and commit evaluated one rule graph (the `eu-vat` worked pack: one fictional 21% rate, whatever the place of supply), and the jurisdiction, situs and DET-001 component packages existed but nothing called them.

| Playbook | Now |
|---|---|
| §6 step 3, §14 — situs, place of supply, responsibility | `internal/domain/taxation` resolves situs per line (`jurisdiction`), selects components by jurisdiction, tax point, service family, customer role and domestic/cross-border supply, applies exemption certificates, and evaluates the component DAG (`determination`). Every component carries its five roles and its source. |
| §7 — the totals stay apart | Customer charges, provider-borne contributions and reverse-charged amounts are separate totals and separate emitted slots. |
| §9 — the hard parts | Tax on tax, inclusive extraction, per-unit charges with their unit, and a rate change at the authority's local midnight, against the shipped training pack. |
| §10–§12 — one contract, explicit states | `document` on the existing `POST /v1/quotes`, `:commit` and `:adjust` (no new commit semantics); `GET /v1/coverage`. A line ends `RESOLVED` or `REVIEW_REQUIRED` / `UNSUPPORTED` / `AMBIGUOUS` / `CONFLICTED` with a registered reason; an unresolved document emits nothing, so it never posts or assesses a zero. |
| §13 — content supply chain | Location content is a signed `taxation` section of the bundle: compiled, rights-gated, four-eyes approved, sealed, verified at load. |
| §18 — evidence and replay | The document and its result are in the envelope and result (absent for every earlier decision, whose bytes and digests are unchanged — the `eu-vat` manifest still digests to `zt1:9786faf5…`). Replay re-runs the same path. |
| §25 — truthful coverage | `GET /v1/coverage` lists every country, service and component in force with its citation. Anything not listed is `UNSUPPORTED`. |

## §26 acceptance script, run 16 October 2026

Against the `ztax-core` binary built from this tree, a fresh PostgreSQL 17/PostGIS database migrated to version 25, and the `training-telecom` bundle compiled, approved by author and approver, sealed and verified in production mode. Environment: development, cell `e2e-training`. Every outcome is ADVISORY; nothing here is authoritative.

| # | Step | Verdict | Evidence |
|---|---|---|---|
| 1 | Explain the product | — | Not a system step. |
| 2 | Seller, mapping and pack | PASS | `GET /v1/coverage`: bundle `training-telecom-2026.10`, digest `zt1:a923014d…`, regimes XQ, XT, XV, XW. SKU→family mapping is supplied on the line; there is no tenant mapping store yet (gap G2). |
| 3 | Quote the §7 bundle | PASS | Customer total 104.45, customer tax 4.45, provider accrual 0.80 shown separately; quote `authoritative: false`; no decision recorded. |
| 4 | Commit and retry concurrently | PASS | Four concurrent commits under one key: one 201, three 409, one decision, one balanced journal (receivable/collected 4.45; GL bridge/accrued 0.80). |
| 5 | Reuse the key with a changed amount | PASS | 409; the original decision unchanged. |
| 6 | Remove location / unknown SKU | PASS | No location: `REVIEW_REQUIRED` / `SITUS_EVIDENCE_INSUFFICIENT`, needed evidence named, nothing emitted. No family: `CLASSIFICATION_REQUIRED`. |
| 7 | Period obligation | PASS | Three duties from one commit: telecom return 3.70 (due 2026-11-20), emergency remittance 0.75 (to a different authority, due 2026-11-15), annual sector contribution 0.80 (due 2027-03-31). Five roles are on each component. Workpaper: see the compliance package surface; not re-run here. |
| 8 | Linked credit | PASS | `:adjust` credit note superseding the decision: −104.45 under the original bundle; original still reads 104.45. Per-imposition refund semantics (§7 last bullet) are not distinguished yet (gap G5). |
| 9 | New content; replay | PASS (replay) | Replay of the committed decision: `MATCH`. Old-versus-new content in one cell is covered by unit tests (rate versions by tax point); a cell holds the bundles it has loaded, so a decision under a bundle no longer loaded reports `BUNDLE_UNAVAILABLE` (gap G6). |
| 10 | Disable AI; hostile source text | NOT RE-RUN | Unchanged by this work; the cell runs with no AI gateway and determination is unaffected. |
| 11 | Filing timeout | NOT RE-RUN | Unchanged by this work (compliance package `UNCERTAIN_SUBMISSION`). |
| 12 | Global and operational cases | PASS (sourcing) | Same cell with `global-telecom`: DE consumer 19%; FR consumer of a DE seller 20% French VAT; FR business of a DE seller reverse-charged; UK consumer of a US seller 20%; Québec GST + QST 114.98; Ontario HST 113.00; BC GST + PST 112.00; Canada without a province `JURISDICTION_LEVEL_REQUIRED`; Canary Islands and the US `UNSUPPORTED`. R1–R7, tenant denial and load were not re-run. |

Unit and use-case coverage: `internal/domain/taxation` (training and global packs, expected values worked by hand from the playbook and each authority's published rate), `internal/app/location_test.go` (quote isolation, idempotency, conflict, refusal states, replay). All five SDKs are regenerated and pass their own suites.

## Gaps that need an owner

| ID | Gap | Owner (playbook §24) |
|---|---|---|
| G1 | `global-telecom` is RESEARCH. Each rate needs confirmation against its citation by a qualified content reviewer, the reuse terms of each cited publication need content-legal sign-off, and independent golden cases need registering, before PRODUCTION. | Tax-content lead, Compliance/Legal |
| G2 | No tenant SKU→family mapping store on the commit path; the caller supplies the family. | Lead engineer (card D) |
| G3 | US state and local communications taxes, 911/988 fees and federal USF are not modelled; India, Brazil and others are absent and answer `UNSUPPORTED`. | Product (pack scope), tax-content lead |
| G4 | Obligations assess in one currency; a document in another currency fails the commit with `CURRENCY_MISMATCH` rather than converting, because no pinned FX source exists (§9 "Cross-currency"). The global pack therefore declares postings but no obligations. | Lead engineer + Finance |
| G5 | Credits reverse every component at the original's rules; per-imposition refund semantics (a per-line fee that is not refundable) are not declared in content yet. | Tax-content lead |
| G6 | A cell holds only the bundles it has loaded, so replay of a decision under retired content reports `BUNDLE_UNAVAILABLE`. A bundle archive for the library is needed. | Platform |
| G7 | Caps and thresholds inside a location-aware document (§9 "Stateful cap") are not supported; accumulators bind only to rule-graph reads. | Lead engineer (card E/F) |
| G8 | Spatial (point-in-polygon) situs is refused at compile: the taxation section carries no boundary dataset. Addresses arrive already mapped to a jurisdiction or ISO code. | Platform + content |
| G9 | Allocation ties: `fiscal.Allocate` gives a tie's residual to the first part (ADR-0002 §2.6), so 10.00 over three is 3.34 / 3.33 / 3.33; playbook §9 shows 3.33 / 3.33 / 3.34. Reconcile the playbook example or the ADR. | CTO |
