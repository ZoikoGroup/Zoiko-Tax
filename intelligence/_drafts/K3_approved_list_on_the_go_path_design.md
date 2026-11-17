<!--
  SCRATCH DRAFT: K3 design note
  STATUS: Working draft for Kaushal and the domain owner.
  Every recommendation here is a SUGGESTION until the domain owner agrees.
  The Go code and the contract belong to other people.
-->

# K3: the approved-list check on the Go-to-gateway path

## 1. The problem

The approved-list guard runs only on `POST /demo/classify` (`tools/report_ai_http.py:455`, `EVIDENCE.md:65`). The Go path (`backend/internal/app/classification.go`) checks only that the code starts with `ontology:` and has something after it (`classification.go:119`, `backend/internal/domain/classification/classification.go:35-38`). So a well-formed but wrong code, for example `ontology:telecom/made-up/node`, goes to the reviewer as a normal proposal (`classification.go:122`). The Go test has no case for it (`classification_test.go:78-110`).

## 2. Facts

- **Where Go receives the proposal:** `ProposeClassification` builds the proposal from `reply.ProposedCode` (`backend/internal/adapter/gateway/gateway.go:256-272`).
- **What Go checks:**
  - A reply with any of the four provenance fields empty is discarded as `AI_GATEWAY_REPLY_UNEVIDENCED` (`gateway.go:336-339`).
  - `classification.go:119-121` runs `OntologyID.Validate()`, which only needs the `ontology:` prefix and a non-empty rest. Failure gives review required with `AI_PROPOSAL_NOT_AN_ONTOLOGY_NODE`.
- **Review path:** only three reasons turn an error into "review required": not configured, unavailable, reply unevidenced (`classification.go:70-74`, `:112-113`). Any other error is returned as an error. A governance refusal stays an error (`classification.go:65-69`, `classification_test.go:112`).
- **How gRPC status maps (`grpc.go:~195-235`):** `PERMISSION_DENIED` is accepted only for the fixed refusal codes (`backend/internal/domain/ai/governance.go:132-141`). `UNAVAILABLE` and similar become `AI_GATEWAY_UNAVAILABLE`. Any other status or reason becomes an internal error ("a defect in one half of the boundary").
- **Reply fields (`grpc.go:99-109`, `contracts/schemas/ai/gateway-reply.schema.json`):** `model_profile`, `provider_profile`, `prompt_profile`, `ai_train_version` (required), plus `text`, `payload`, `fields`, `proposed_code`, `confidence`.
- **Does the schema allow a new field?** No. `additionalProperties` is `false` (schema line 7).
- **Python reply:** built in `server.py:~646-657` from the runtime result. A proposal with an empty code is refused before that (`runtime.py:144-146`, which gives `RuntimeUnavailableError`, then `UNAVAILABLE` and `AI_GATEWAY_UNAVAILABLE`, `server.py:~422-436`).
- **The guard today (Python):**
  - `check_code(code, as_of, list)` is an exact, case-sensitive match. It strips no whitespace and never reads the clock (`ontology_guard.py:426-451`).
  - `/demo/classify` passes `datetime.now(UTC).date()` (`report_ai_http.py:614-619`).
  - With no list it answers NOT_CONFIGURED and does not call the gateway (`report_ai_http.py:427-434`).
  - On a bad code it answers ABSTAIN with `GUARD_ONTOLOGY_UNKNOWN`, `_EXPIRED` or `_NOT_YET_EFFECTIVE`, and returns no code (`report_ai_http.py:455-474`). It writes an `AnswerCheckedEvent` through `Gateway.record_answer_check` (`server.py:~257-285`).
- **A Go-side approved list or loader:** NOT FOUND (searched `backend/` and `contracts/` for "approved ontology").
- **Whether Go shows the review reason to the user:** NOT FOUND (I did not read the HTTP transport code).

## 3. Option A: a reply-checker hook in the gateway (Python)

**Where.** In `server.py`, after `check_result(call.kind, result)` (`:645`) and before the `Reply` is built (`:646`), only for `Kind.CLASSIFICATION_PROPOSAL`. The hook would call `check_code(code, as_of, list)`. It needs the approved list path (a new env var, loaded once at start-up in `gateway_factory.py`) and an as-of date (today, UTC, passed in so tests can fix it).

**Not-approved outcome.** The reply has no way to say "not approved". Today there are only two ways out, and neither is clean.
1. Raise `RuntimeUnavailableError`. Go then shows review required, but with the label `AI_GATEWAY_UNAVAILABLE`. That mislabels a rejected code as an outage, and the retry hint in that reason text is wrong.
2. Raise a new `GatewayError` reason. Go's `fromStatus` does not know it and returns an internal error (`grpc.go:~228-234`). `Propose` would then fail the request, not send it to review. This needs a Go change too.

**Reply shape.** Unchanged if we use way 1. A new field needs a contract change (schema line 7).

**Evidence.** A BLOCK record with a fixed reason and no code (`server.py:~422-428` pattern), plus an `AnswerCheckedEvent` with the existing `GUARD_ONTOLOGY_*` reasons (`invocation_check_event.py:75-78`).

## 4. Option B: a new review reason on the Go side

- **File:** `backend/internal/app/classification.go`, next to `:119`. It would need the approved list inside Go and a new reason such as `AI_PROPOSAL_NOT_APPROVED` in `backend/internal/domain/ai/degraded.go` (like `:37`).
- **Contract change:** none for the reply. A new registered reason, and a way to load the list in Go (NOT FOUND today).
- **Owner:** lane-l (the reason owner in `degraded.go`) and whoever owns `classification.go`.

## 5. Comparison

| | A: gateway hook | B: Go review reason |
|---|---|---|
| Who owns the change | Gateway (us), plus a Go change if we want a clean reason | Go side (other people) |
| Contract change | None with way 1; schema change for a new field | None for the reply; new reason registered |
| List missing | We choose: refuse start-up (closed) or skip the check (open) | Go must choose; no loader exists |
| Evidence record | BLOCK record plus check event with the exact reason | The gateway record says PROPOSE; the rejection is only on the Go side |
| Test effort | Python only, fake runtime and fixed date | Go tests and a list loader |
| Live demo | `/demo/classify` already checks; the Go path is not in the demo | None |

## 6. Suggestion (not agreed)

Do A, with the list required. Reason: the gateway already has the guard, the list loader, the check event and the evidence record, and Go does not have a list. The same list file then guards both routes.

Order:
1. Agree the not-approved outcome with the Go owner. Way 1 is a stop-gap; a clean review reason needs a Go change.
2. Load the list at start-up in `gateway_factory.py`, closed on missing or bad.
3. Add the hook with an injected date.
4. Add the Go review reason later, if the owner agrees.

Tests:
- List missing: start-up refused, fixed text.
- List bad (malformed, wrong version, duplicate): start-up refused, no list content in the error.
- Code not on the list, expired, not yet effective.
- Code padded with spaces, a newline, or a leading `#`: refused, because the match is exact.
- Approved code: reply unchanged.
- The error text, log line and evidence record never hold the list content or the proposed code.
- The other kinds (suggestion, extraction) are not checked.

## 7. Questions for the domain owner

1. Is the approved list (decision D-05) a gateway file, or should Go own it? (one name)
2. May a code not on the list reach a reviewer as "review required" with the reason shown? (yes or no)
3. If the list is missing at start-up, may the gateway refuse to start? (yes or no)
4. Is "today, UTC" the right as-of date? (yes or no)
5. Is a stop-gap using `AI_GATEWAY_UNAVAILABLE` for a rejected code acceptable until Go adds a reason? (yes or no)
6. Who owns a new Go reason in `degraded.go`? (one name)
7. Does the real list differ in format from the synthetic `config/approved_ontology.live-demo.json`? (yes or no)

## 8. Out of scope

- Changing Go code, `contracts/`, `.yml` files or `tools/`.
- The content of the real list (decision D-05; not in this repository, `EVIDENCE.md:64`).
- The ABSTAIN reply on `/demo/classify` (already done).
- Calibration or scoring of proposals.
