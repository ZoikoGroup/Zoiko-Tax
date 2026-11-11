# Intelligence: AI Authority Boundary — Evidence Matrix

**Status:** Working draft — not a registered document.

## A. What this is

ZoikoTax's AI component proposes and explains classification mappings; it never
decides tax outcomes. This page lists exactly what is proven by automated tests,
what is not, and what is not yet built. No live AI model is connected to any test
on this page.

## B. How to read this

Run `python tools/step10_demo.py` from the `intelligence/` directory to re-run
every claim below. Claims C8 and C9 need Go installed; with `--skip-go` they show
`NOT RUN`, meaning **unverified**. Go test names are checked by the runner when Go
is available. Three statuses are used:

| Status | Meaning |
|---|---|
| `PROVEN` | All checks for this claim pass; the claim is fully demonstrated. |
| `PARTIAL` | Checks pass but prove less than the claim's full text — see the row. |
| `NOT IMPLEMENTED` | No test exists for this claim yet. |

## C. Claims demonstrated by the runner

| ID | Claim (runner's exact text) | Status | Re-run needs | Tests | Proves | Does NOT prove | Supports |
|---|---|---|---|---|---|---|---|
| C1 | Hostile text in a request cannot cause an approval or a privileged action. | PROVEN | Python | `tests/test_ai_boundary_proof.py::TestP1HostileInputIsNeutralised` | That a real governance gate blocks injection before the model. | That a live model resists injection end-to-end. | REQ-0061 |
| C2 | Hostile text inside a retrieved document is treated as data and cannot cause an action. | PROVEN | Python | `tests/test_ai_boundary_proof.py::TestP2HostileRagDocumentIsNeutralised` | That hostile RAG content is treated as data and never triggers an action. | That corpus poisoning is prevented at ingestion time. | REQ-0062, REQ-0052, REQ-0084 |
| C3 | A fabricated or unsupported citation is rejected. | PROVEN | Python | `tests/test_ai_boundary_proof.py::TestP3FabricatedCitationIsRejected` | That fabricated and mismatched citations are rejected before the model. | That all invalid rule references are caught (only fabricated/mismatched/missing citations are tested). | REQ-0046 |
| C4 | Forbidden (A5) actions are always blocked and the model is never reached. | PROVEN | Python | `tests/test_ai_boundary_proof.py::TestP4A5IsAlwaysBlocked` | That A5-authority actions are blocked and the model runtime is never called. | That other forbidden authority levels are blocked (only A5 is tested). | REQ-0006-0009, REQ-0083 |
| C5 | When the model provider is unavailable the gateway fails with a defined error, writes a BLOCK record, and recovers on the next call. | PROVEN | Python | `tests/test_ai_boundary_proof.py::TestP5ProviderUnavailability` | That the gateway writes a BLOCK record and recovers cleanly on the subsequent call. | That distributed recovery or persistent retry queues work. | - |
| C6 | Sensitive data never appears in evidence records, logs or error output. | PROVEN | Python | `tests/test_ai_boundary_proof.py::TestP6SensitiveDataNeverLeaks` | That sensitive fields are absent from evidence records, logs, and error output. | That external pipelines outside the gateway are scoped. | REQ-0063 |
| C7 | Cross-tenant retrieval is isolated, and malformed context is refused before the model. | PROVEN | Python | `tests/test_ai_boundary_proof.py::TestP7TenantIsolation`, `tests/test_ai_boundary_proof.py::TestP8WireMalformedDataClass` | That cross-tenant retrieval is blocked and malformed context is rejected before the model. | That tenant isolation holds in Go-side storage or transport. | ZTAX-SEC-REQ-0004, ADR-0006 |
| C8 | With the model provider disabled, approved deterministic quotes and commits continue. | PROVEN | Python + Go | Go: `TestDeterministicFlowIndependentOfGateway`, `TestDeterminationServiceTakesNoModelGateway`, `TestKilledUseCaseNeverReachesTransport` | That quote and commit flows continue when the AI gateway is unavailable (Go tests; runner checks names when Go is available). | That Python-side controls enforce this; Go tests run outside this folder. | - |
| C9 | With the model provider disabled, new ambiguous mappings enter human review. | PARTIAL | Python + Go | Go: `TestProposalGoesToReviewWhenTheAIPlaneCannotHelp`, `TestProposeClassificationReviewRequired` | That a review-required response is produced when the AI gateway is unavailable, not configured, or returns a proposal that is not an ontology node. | Low-confidence routing or durable storage in a review queue. | - |
| C10 | A T4 (prohibited) risk-tier ceiling is refused unconditionally for every use-case ceiling, including when the ceiling itself is T4, and the model runtime is never invoked. | PROVEN | Python | `tests/test_t4_prohibition.py::test_t4_refused_for_every_use_case_ceiling`, `tests/test_t4_prohibition.py::test_gateway_invoke_t4_runtime_never_reached` | That T4 risk-tier is refused for every use-case ceiling and the model runtime is never called. | That T4 is enforced at the Go transport layer. | REQ-0107 |

## D. Controls built but not wired

| Module | What it does | Tests | Why it is not wired yet |
|---|---|---|---|
| `approval_quorum.py` | Ensures AI cannot count as one of two required human approvers (REQ-0077). | `tests/test_approval_quorum.py` | Integration point with approval workflow not yet defined. |
| `classification_assessment.py` | BM25-rank-based routing for classification proposals. | `tests/test_classification_assessment.py` | Not connected to any gateway runtime. |
| `gold_set_file.py` | Provenance-tracked benchmark loader for evaluation. | `tests/test_gold_set_file.py` | Gold-set cases are placeholder data; no reviewer assigned. |
| `ontology_alignment.py` | Reports whether ontology headings and gold-set expected classes align. | `tests/test_ontology_alignment.py` | Ontology descriptions not yet authored. |
| `review_metrics.py` | Human override and rejection rate monitor (REQ-0076). | `tests/test_review_metrics.py` | No live review queue to feed observations. |
| `screening.py` | Deterministic prompt-injection, corpus-poisoning, and disclosure detection. | `tests/test_screening.py` | Not connected to gateway runtime; used only in tests. |

## E. Known gaps (verified)

- No live model or provider adapter exists — only an unconfigured runtime (`runtime.py`) and a fake runtime for tests (`fake_runtime.py`) are present.
- The Classifier is not connected to any gateway runtime.
- The A3 constrained-automation guard is not implemented (no ADR-AIGOV-005 text committed).
- `AgentMemoryPolicy` is declared but nothing in `src/` enforces it at runtime.
- `source_class` is not used in any governance decision (verified by import-line search).
- Gold-set cases in the repo are placeholder records, not reviewed evidence.
- ZTAX-CLS-001 (Telecom Product Ontology) is registered as a DRAFT in `docs/register.yaml`; no plain-text or Markdown spec exists under `docs/specs/`. The registered document is a binary DOCX; `ontology_alignment.py` requires a Markdown input and cannot read it. No alignment run has been completed and no match count is available.
- Go-side behaviour for T4 is not checked from this folder.
- Fixed: an error the Gateway does not name now leaves one BLOCK record (AI_GATEWAY_UNAVAILABLE, fixed detail, no exception text).
- `usage.latency_ms` is set on every record of a call that reached the runtime; tokens and cost are unpopulated: no provider telemetry.
- Refusal details for kill switch, unknown or suspended use case, region and malformed context are fixed text; an unknown id is logged and recorded as `unregistered`. Data-class refusal details still name the PRIV-001 class, because existing tests assert it.
- The approved ontology list (`config/approved_ontology.live-demo.json`) is synthetic; the real list needs Decision D-05 (`tools/report_ai_http.py`).
- The approved-list check runs only on `POST /demo/classify` (`tools/report_ai_http.py`); the Go-to-gRPC classification path (`backend/internal/app/classification.go`) gets only the `ontology:` format check (`backend/internal/app/classification.go`, test `TestProposalGoesToReviewWhenTheAIPlaneCannotHelp` in `backend/internal/app/classification_test.go`).
- The check-event sink is the logging sink; there is no file-based sink yet (`src/ztax_gateway/server.py`).

## F. Open decisions (from the project team, not verifiable from code)

provider policy (CTO, Security, Privacy); an owner for the ontology descriptions;
a reviewer for the gold-set cases; the text of ADR-AIGOV-005 for the A3 guard.

## G. Limits

All tests use a fake model and prove that governance is independent of model output
and payload content, not that a real model resists injection. The gateway trusts the
declared authority level; no test validates that the caller's identity assertion is
unforgeable. This document does not close any requirement.

## H. Live demo path: evidence record versus explanation guard

This section covers only the **live Groq demo** path
(`intelligence/tools/report_ai_http.py` + `docker-compose.live-demo.yml`).
It does not affect any production gateway path.

### What the evidence record contains

When the demo Reports page calls `POST /demo/explain`, `report_ai_http.py`
calls `Gateway.invoke()` in `intelligence/src/ztax_gateway/server.py`.
The Gateway writes one evidence record (ZTAX-AIGOV-001 §14) through the
`ztax_gateway.evidence` logger at `INFO` level **before** it returns a
reply to the caller. The outcome recorded in that line is always **`PROPOSE`**
-- the authority outcome at the moment of the model invocation. It does not
reflect anything that happens after `Gateway.invoke()` returns.

### What the explanation guard does (and where its decision is logged)

After `Gateway.invoke()` returns, `report_ai_http.py` calls `apply_guard()` / `_check()` from
`intelligence/src/ztax_gateway/explanation_guard.py` (WP6: the guard was extracted into the governed `src/` path
so the gateway can use it without depending on demo tooling). The guard checks the model's reply against
the cited rows (citation existence, number matching, status-word matching). If rejected, the browser receives `ABSTAINED`
and the reason is the guard's fixed code (e.g. `GUARD_NUMBER_NOT_IN_ROWS`), logged at `INFO` level (`ztax_gateway.report_ai_http`).
The log is kept for the lifetime of the running container; it is not persisted after container restart.

### What the approved-list check on /demo/classify does

On `POST /demo/classify`, `report_ai_http.py` runs `apply_ontology_guard()` (`src/ztax_gateway/ontology_guard.py::check_code`).
The validator ID is `ontology_guard@1`. An allowed code yields `PASS` with `CHECKS_PASSED`;
an unknown, expired, or not-yet-effective code yields `ABSTAIN` with `GUARD_ONTOLOGY_UNKNOWN`,
`GUARD_ONTOLOGY_EXPIRED`, or `GUARD_ONTOLOGY_NOT_YET_EFFECTIVE`. The result is a separate
linked append-only `AnswerCheckedEvent` naming the invocation ID (`src/ztax_gateway/invocation_check_event.py`).
On `ABSTAIN`, the response never returns the rejected code, and the gateway evidence record contains
no prompt or answer text (`tests/test_report_ai_http_classify.py::test_rejected_code_never_in_response_body`).

### What this means

The evidence record (written by `Gateway.invoke()`) and the guard log (written
by `report_ai_http.py`, which calls `explanation_guard.apply_guard()`) are two
separate lines. A call that the guard later rejects still shows `PROPOSE` in
the evidence record. The full picture -- `PROPOSE` in the gateway evidence and
`ABSTAINED` in the `report_ai_http` log -- requires reading both.

This is acceptable for the demo because no downstream system reads demo
evidence records and no compliance decision depends on them.

### Status of guard outcome follow-up

`AnswerCheckedEvent` and sinks are merged in `src/ztax_gateway/invocation_check_event.py`.
Check events are emitted by the demo through `record_answer_check` for every guard
outcome; a refused or killed call emits none. Production gRPC emission remains unwired.

---

## I. Prompt content hash (Decision D-09 / ZTAX-AI-CONF-ADD01)

`InvocationEvidenceRecord` carries an optional `prompt_content_hash` (lowercase SHA-256
hex of `system_prompt`). Format is validated by the record and normalised (lowercased)
by the builder; omitted when unset.

**Status:** `Gateway.invoke()` sets the prompt hash only when a `PromptProfileRegistry` is
injected AND the call reaches the runtime; blocked, refused or killed calls keep the hash
empty. The two live demo servers (`tools/serve_live_demo.py`, `tools/report_ai_http.py`) load prompt profiles at startup (`ZTAX_GATEWAY_PROMPT_PROFILES`), so their records carry the hash: SHA-256 of the profile's `system_prompt`, computed from the text and never read from the file, checked at startup against the text the runtime sends. `server.py` main (fake runtime) injects nothing and carries no hash on purpose. The check-event sink is the logging sink; there is no file-based sink yet. With a registry, `classification-review` accepts only `CLASSIFICATION_PROPOSAL`; other kinds are refused before any request is built (BLOCK record, no hash).

**Known gap (wire kind):** the registry and the Gateway do not restrict which wire kind a use case may receive; the runtime guard is demo-only. An allowed-kinds field per use case needs a registry contract decision.

**Demo-path resilience:** If the check-event sink raises when `record_answer_check` is
called, `report_ai_http.py` catches the exception, logs a fixed WARNING
(`check_event_write_failed reason=CHECK_EVENT_WRITE_FAILED`) without echoing the exception
text, and still returns the explanation reply. Demo only; production paths are unaffected.

**Kill flags live on demo servers:** `tools/report_ai_http.py` and `tools/serve_live_demo.py`
now pass `kill_state_path` to `Gateway`, so the demo servers re-read `killed` and
`global_kill` before every call without a container restart (same as `server.py`).

**Scope:** Covers `system_prompt` only. Never added to `observability.py` labels
(`tests/test_observability.py::test_model_ops_labels_do_not_include_prompt_content_hash`).
