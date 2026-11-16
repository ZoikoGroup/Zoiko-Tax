<!--
  SCRATCH DRAFT: S7 Reviewer Storage Design Note
  STATUS: Working draft for Kaushal and Domain Owner review.
  DO NOT COMMIT TO MAIN: This is a design exploration document under _drafts/.
  All items below are suggestions until Decision D-10 is signed.
-->

# S7 Design Note: Human Review Record Schema & Storage

## 1. Purpose

The Intelligence service operates strictly as an advisory system that generates non-binding classification proposals. When an operator or tax specialist acts on a proposal, the resulting decision record is owned, validated, and persisted by the Fiscal Domain rather than the AI Gateway. This design note outlines the suggested fields, lifecycle rules, and integration touchpoints for linking human review decisions back to the AI invocation evidence trail.

## 2. Suggested Review-Record Fields

The following fields are recommended for the human review record stored in the Fiscal Domain:

| Suggested Field Name | Meaning & Constraints | Matches Existing Code? |
| :--- | :--- | :--- |
| `review_action` | Action taken by reviewer: `ACCEPT` (or `APPROVE`), `MODIFY`, `REJECT`, `ESCALATE`. | **Yes (partial)**: `ReviewOutcome` in `src/ztax_gateway/human_review.py` uses `APPROVE`, `MODIFY`, `REJECT`, `ESCALATE`. |
| `reason_code` | Structured reason enum explaining why the action was taken without parsing free text. | **Yes**: `DecisionReasonCode` in `src/ztax_gateway/human_review.py` (`FACTUALLY_CORRECT`, `WITHIN_TOLERANCE`, `FACTUALLY_INCORRECT`, `CITATION_INSUFFICIENT`, `OUT_OF_SCOPE`, `STALE_SOURCE`). |
| `reviewer_note` | Free-text rationale, audit comments, or justification from the reviewer. | **Yes**: `rationale` in `ReviewDecision` (`human_review.py`); `notes` / `reviewer_comments` in `gold/README.md` and `gold_set_status.py`. |
| `reviewer_id` | Unique identifier (email, employee ID, or auth principal) of the human reviewer. | **Yes**: `reviewer_id` in `ReviewDecision` / `ReviewerProfile` (`human_review.py`); `reviewer` in `gold/README.md` and `gold_set_file.py`. |
| `decided_at` | ISO 8601 UTC timestamp recording when the decision was finalized. | **Yes**: `decided_at` in `ReviewDecision` (`human_review.py`); `reviewed_on` (YYYY-MM-DD) in `gold/README.md`. |
| `proposal_id` | Identifier of the specific AI classification proposal being evaluated. | **Yes**: Corresponds to `output_id` in `ReviewItem` / `ReviewDecision` (`human_review.py`) and `case_id` in gold fixtures. |
| `ai_invocation_id` | UUID4 string linking directly to the immutable gateway evidence record. | **Yes**: `ai_invocation_id` in `src/ztax_gateway/invocation_evidence.py` and protobuf `AiInvocationEvidence`. |
| `output_hash` | SHA-256 hex digest of the raw proposal payload at inference time. | **Yes**: `output_hash` in `InvocationEvidenceRecord` (`src/ztax_gateway/invocation_evidence.py`). |
| `corrected_code` | Revised ontology class selected by reviewer (populated iff action is `MODIFY`). | **Yes**: `correction` in `ReviewDecision` (`src/ztax_gateway/human_review.py`). |
| `source_ref` | Statutory, regulatory, or tariff citation justifying the human decision. | **Yes**: `source_ref` in `gold/README.md` and `gold_set_file.py`. |

## 3. Recommended Architectural Rules

1. **Immutable AI Proposal Trail:** The original AI proposal and its evidence record (`ai_invocation_id`, `output_hash`) are strictly immutable and must never be updated, overwritten, or purged when a human acts.
2. **Append-Only Decision Log:** A human review action is always appended as an independent, cryptographically verifiable record referencing the parent `ai_invocation_id`.
3. **Decision Scope Boundary:** A reviewer's decision on a classification proposal does *not* constitute a final tax calculation or financial approval. Tax determination remains governed by downstream fiscal rules.
4. **No Chain-of-Thought Storage:** Intermediate model reasoning steps, raw chain-of-thought tokens, or internal prompt scratchpads must never be stored in the persistent review record (per ADR-0017 and AIGOV-001 privacy requirements).

## 4. Open Questions for Kaushal and Domain Owner

1. **Storage Subsystem Ownership:** Does the Fiscal service store review records in its primary transactional PostgreSQL database (`ztax_fiscal`), or should records be streamed to a dedicated compliance audit ledger? *(TO CONFIRM with Kaushal)*
2. **Data Retention & Archival:** What is the legal retention period for human review records on live tax transactions (e.g., 6 years per HMRC / statutory VAT requirements)? *(TO CONFIRM with Domain Owner)*
3. **`MODIFY` Original Preservation:** When a reviewer selects `MODIFY`, does the downstream schema preserve the AI-proposed ontology code alongside the reviewer's revised code in the same row, or rely entirely on joining `ai_invocation_id`? *(TO CONFIRM with Kaushal and Domain Owner)*
4. **Action Terminology Alignment:** Should the system standardize on `ACCEPT` or `APPROVE` across both the Go service and gateway Python schemas? *(TO CONFIRM with Kaushal)*

---
*All items above are suggestions until D-10 is signed.*
