# Live Groq demo -- DEMO ONLY

A temporary way to show a real, pinned Groq-hosted model sitting behind the
existing governed Gateway, with nothing else about the stack changed.
**Never for production. Synthetic data only. Not part of CI.**

## 1. Add two lines to your local `.env`

`.env` is already gitignored, so the key is not committed. The overlay then
passes the key into the `ztax-model-gateway` container as a plain environment
variable, read from your local `.env`. That is a **demo-only deviation from
ADR-0017**, not something it permits. Add:

```
GROQ_API_KEY=your-own-groq-api-key
GROQ_MODEL=llama-3.1-8b-instant
```

`GROQ_MODEL` must name one exact, pinned model -- not `latest`, `default`,
`stable` or `preview`; `groq_demo_runtime.py` refuses those before making any
call.

## 2. Start the demo stack

From the repo root, with the base stack already configured per the main
[`docker-compose.yml`](../docker-compose.yml) instructions (`.env` copied
from `.env.example` and filled in):

```bash
docker compose -f docker-compose.yml -f docker-compose.live-demo.yml up --build -d
```

This changes only the `ztax-model-gateway` service: it now runs
[`intelligence/tools/serve_live_demo.py`](tools/serve_live_demo.py) instead
of the fake runtime, calling out to Groq for real. `ztax-core` and `ztax-web`
are unchanged and need no edits -- their existing wiring already points at
`ztax-model-gateway:8443` insecurely in development.

## 3. Open the frontend

```
open http://localhost:13000     # set WEB_PORT=13000 in .env (the base default is 3000)
```

Sign in, open **Intelligence**, and submit a subject reference and
description (catalog text only -- never customer data; the description goes
to the Model Gateway and, this time, on to Groq).

### What to expect

- **A proposal**: if the model's reply passes the output check, the page
  shows a proposed code, a confidence value, and the provenance table (use
  case, model profile, provider profile, prompt profile, AI train). On this
  main Intelligence page (the Go-to-gRPC path), the output check is
  **format-only**: the code must start with `ontology:` and name something
  after it; it does **not** check that the code is on an approved list (the Go
  core holds no such list, and the screen note "whether that code is a real
  ontology node is not verified here" is accurate). For the approved-list check
  that validates proposed codes against an approved list and yields `ABSTAIN`
  on unknown codes, see [Section 6](#6-optional-classification-with-the-approved-list-check-democlassify)
  below (`POST /demo/classify`).
- **A review-required result**: unlike the fake runtime (which always
  returns a valid placeholder code), a real, unconstrained model's free-text
  answer can fail even that format check. The page then shows its seven-step
  view stopped at step 4, **Output checks**, with the note "The model
  proposed a code that is not an ontology node, so the proposal was
  discarded. Deterministic processing is unaffected." and the code
  `AI_PROPOSAL_NOT_AN_ONTOLOGY_NODE`; steps 5-7 read "Not reached". This is
  the governed boundary working as designed
  (`backend/internal/app/classification.go`), not a bug in the demo.
- A provider failure, a timeout, or a malformed reply stops the same view at
  step 3, **Model call**, with "The Model Gateway is down or slow right now.
  Deterministic processing is unaffected." and `AI_GATEWAY_UNAVAILABLE`,
  rather than an error page -- the governed path degrades to review, it does
  not crash.
- **Time and concurrency limits**: the demo Gateway gives up on a model call
  after 45 seconds, and at most 8 model calls run at once. A call that hits
  either limit is refused with "model runtime unavailable" and one evidence
  record, and the screen shows the same review-required view as above.
- **Confidence is a fixed placeholder** (`0.5000`, set in
  `groq_demo_runtime.py`). Groq returns no probability, so the number is not
  the model's score and means nothing, whatever the page's caption says.

## 4. Stop the demo

```bash
docker compose -f docker-compose.yml -f docker-compose.live-demo.yml down
```

Afterwards, `docker compose up -d` (the base file alone, no overlay) returns
the stack to the fake runtime.

## 5. Optional: the synthetic Reports explanation demo

The same `up` command also starts a second, unrelated demo on the
`ztax-model-gateway` container: a small HTTP endpoint
([`intelligence/tools/report_ai_http.py`](tools/report_ai_http.py)) that
explains one row of a synthetic Reports dashboard (KPI figures, an
obligations register, a Shadow Assurance comparison -- all fake, generated
by [`intelligence/tools/report_fixture.py`](tools/report_fixture.py)),
through the same governed Gateway and the same real model.

```bash
curl http://127.0.0.1:18090/demo/fixture                       # the whole fixture, as JSON
curl -X POST http://127.0.0.1:18090/demo/explain \
  -H 'Content-Type: application/json' \
  -d '{"kind": "obligation-row", "ids": ["OBL-001"]}'
```

The reply's `outcome` is one of `EXPLAINED` (the reply passed the guard
below), `ABSTAINED` (the model itself abstained, or the deterministic guard in
`report_ai_http.py` rejected the reply -- a real model answer is overruled
here, not trusted), or `REVIEW_REQUIRED` (the Gateway itself could not help,
same reasons as the classification demo above; the reply carries only the
Gateway's reason code, never provider text).

What the guard checks: at least one row id is cited and every cited id exists
in the rows supplied; every number equals a value in those rows exactly (or is
a percentage that recomputes, or a year / day quoting a supplied date); and any
lifecycle or payment status word (`PAID`, `OVERDUE`, `SETTLED`...) is the status
of a cited row. What it **cannot** do is prove a sentence is true: swapped
numbers and invented causes still pass, and a person must read the text.

The **Reports (demo)** page in the web app uses this endpoint. It needs the
web app on `http://localhost:13000` (CORS allows only that origin) and this
service on `127.0.0.1:18090`; the page's Explain buttons call it from your
browser.

**The report service is unauthenticated and loopback-only.** It has no login;
the compose overlay publishes it on `127.0.0.1` only. Never forward, tunnel or
otherwise expose that port. Its use cases (`explain-*`) are registered at risk
tier T1 and data class P0 only.

## 6. Optional: classification with the approved-list check (/demo/classify)

The demo HTTP server ([`intelligence/tools/report_ai_http.py`](tools/report_ai_http.py))
also provides `POST /demo/classify`, which validates model-proposed classification
codes against an approved ontology list using `apply_ontology_guard()`
([`intelligence/tools/report_ai_http.py`](tools/report_ai_http.py), wrapping [`ontology_guard.py`](src/ztax_gateway/ontology_guard.py)).

### What it shows

A real model proposes an ontology class for catalog text through `Gateway.invoke_with_id()`.
The ontology guard then checks the proposal against the approved list:
- A code present and currently effective ends as **`PROPOSED`** (`CHECKS_PASSED`).
- A code not in the list, expired, not yet effective, or prefixed with a Markdown heading
  marker (`#`) ends as **`ABSTAIN`** with the corresponding reason code
  (`GUARD_ONTOLOGY_UNKNOWN`, `GUARD_ONTOLOGY_EXPIRED`, or `GUARD_ONTOLOGY_NOT_YET_EFFECTIVE`).
- On `ABSTAIN`, the response **never returns the rejected code** or any model text.

### Approved list configuration and compose follow-up

The guard reads the approved list path from the **`ZTAX_ONTOLOGY_APPROVED_LIST`** environment
variable. A synthetic demo list is provided at
[`intelligence/config/approved_ontology.live-demo.json`](config/approved_ontology.live-demo.json)
(`list_version: "synthetic-demo-v0"`). **The list is synthetic** (8 telecom codes for demonstration;
the authoritative list awaits Decision D-05) and **confidence is not calibrated** (`confidence_calibrated: false`).

> [!NOTE]
> `docker-compose.live-demo.yml` does **not** mount this list file yet. Mounting it is a documented
> follow-up for the deployment owner. Two lines are required under `ztax-model-gateway`:
> ```yaml
> volumes:
>   - ./intelligence/config/approved_ontology.live-demo.json:/etc/ztax/ai/approved_ontology.json:ro
> environment:
>   ZTAX_ONTOLOGY_APPROVED_LIST: /etc/ztax/ai/approved_ontology.json
> ```
> Do not edit the compose file now; merging to main deploys without this gate, and an unset variable
> safely returns `NOT_CONFIGURED` without affecting server startup or `/demo/explain`.

### Invocation and response shapes

```bash
curl -X POST http://127.0.0.1:18090/demo/classify \
  -H 'Content-Type: application/json' \
  -d '{"subject_ref": "sku:PLAN-PRE-VOICE-500", "description": "Prepaid voice bundle, 500 minutes, no data included"}'
```

The four response shapes are (all have the same seven keys, plus `proposed_code` on `PROPOSED` only):

1. **`PROPOSED`** (code approved and effective):
   ```json
   {
     "outcome": "PROPOSED",
     "evidence_id": "019318f0-...",
     "verdict": "PASS",
     "reason_code": "CHECKS_PASSED",
     "list_version": "synthetic-demo-v0",
     "proposed_code": "ontology:telecom/voice/mobile",
     "confidence_calibrated": false,
     "synthetic": true
   }
   ```

2. **`ABSTAIN`** (code unknown, expired, future, or heading-marked — rejected code is omitted):
   ```json
   {
     "outcome": "ABSTAIN",
     "evidence_id": "019318f0-...",
     "verdict": "ABSTAIN",
     "reason_code": "GUARD_ONTOLOGY_UNKNOWN",
     "list_version": "synthetic-demo-v0",
     "confidence_calibrated": false,
     "synthetic": true
   }
   ```

3. **`NOT_CONFIGURED`** (`ZTAX_ONTOLOGY_APPROVED_LIST` is unset):
   ```json
   {
     "outcome": "NOT_CONFIGURED",
     "evidence_id": null,
     "verdict": "ABSTAIN",
     "reason_code": "GUARD_ONTOLOGY_NOT_CONFIGURED",
     "list_version": null,
     "confidence_calibrated": false,
     "synthetic": true
   }
   ```

4. **`REFUSED`** (the Gateway refused the call, or an unexpected error was caught; no model text is returned):
   ```json
   {
     "outcome": "REFUSED",
     "evidence_id": null,
     "verdict": "BLOCK",
     "reason_code": "AI_KILL_SWITCH_ENGAGED",
     "list_version": "synthetic-demo-v0",
     "confidence_calibrated": false,
     "synthetic": true
   }
   ```
   `list_version` is the approved list's version when a list is loaded, otherwise `null`.

### Status codes, error texts and limits

- **Every outcome is HTTP 200**, including `REFUSED` and `NOT_CONFIGURED`. Read `outcome`, not the status.
- **400** with a fixed text, and nothing of the request echoed:

  | Case | `error` text |
  |---|---|
  | Content-Length is not a non-negative whole number | `invalid content length` |
  | Body over 32 KiB | `request body too large` |
  | Body is not JSON | `request body is not JSON` |
  | JSON body that is not an object (`null`, list, string, number) | `request body must be an object` |
  | `subject_ref` missing, not a string or blank | `subject_ref must be a non-empty string` |
  | `subject_ref` over 512 characters | `subject_ref is too long` |
  | `description` missing, not a string or blank | `description must be a non-empty string` |
  | `description` over 1000 characters | `description is too long` |

- **404** `{"error": "not found"}` for any other path. The 32 KiB body limit and the Content-Length rule also apply to `POST /demo/explain`.
- **Limits:** 32 KiB request body, 512 characters for `subject_ref`, 1000 characters for `description`.

### Reason codes the frontend can meet

| Outcome | `reason_code` values |
|---|---|
| `PROPOSED` | `CHECKS_PASSED` |
| `ABSTAIN` | `GUARD_ONTOLOGY_UNKNOWN`, `GUARD_ONTOLOGY_EXPIRED`, `GUARD_ONTOLOGY_NOT_YET_EFFECTIVE` |
| `NOT_CONFIGURED` | `GUARD_ONTOLOGY_NOT_CONFIGURED` |
| `REFUSED` | From `governance.py`: `AI_UNKNOWN_USE_CASE`, `AI_USE_CASE_SUSPENDED`, `AI_KILL_SWITCH_ENGAGED`, `AI_AUTHORITY_REFUSED`, `AI_RISK_TIER_EXCEEDED`, `AI_RESIDENCY_REFUSED`, `AI_MALFORMED_CONTEXT`, `AI_DATA_CLASS_REFUSED`. From `server.py`: `AI_GATEWAY_UNAVAILABLE`, `AI_GATEWAY_NOT_CONFIGURED`. |

`REFUSED` passes the Gateway's reason through, so this set may grow. A client should treat an unknown `reason_code` as a refusal, not as an error. `AI_GATEWAY_NOT_CONFIGURED` (the model runtime is not configured) is a different thing from the `NOT_CONFIGURED` outcome (no approved list).

### Evidence record vs answer-checked event

Two separate records are logged:
1. **Gateway evidence record**: `Gateway.invoke_with_id()` emits one tamper-evident record
   via logger `ztax_gateway.evidence` at level `INFO`. The outcome recorded there is always
   `PROPOSE` (reflecting the authority state at model invocation time).
2. **Linked answer-checked event**: `_record_answer_check_safe()` records an `AnswerCheckedEvent`
   with validator `ontology_guard@1`, linked to the evidence record by `ai_invocation_id`.
   The actual guard decision (`PASS` or `ABSTAIN`) lives in this check event, **not** in the initial
   gateway evidence record.

## 7. Demo script

This script maps the five demonstration capabilities to what is operational in the codebase today:

| Step | Action | What actually happens today |
|---|---|---|
| **1. Classify** | Submit item description on the Intelligence UI page or via `POST /demo/classify`. | • **Main page (Go gRPC path):** Runs format check only (`ontology:*`). UI disclaimer *"whether that code is a real ontology node is not verified here"* is correct.<br>• **HTTP endpoint (`/demo/classify`):** Runs the approved-list check against `config/approved_ontology.live-demo.json`; returns `PROPOSED` or `ABSTAIN`. |
| **2. Explain** | Open Reports (demo) page and click Explain on any row. | `report_ai_http.py` (`POST /demo/explain`) runs `explanation_guard`. Checks model reply against cited row IDs, numbers, and statuses; returns `EXPLAINED` or `ABSTAINED`. |
| **3. Kill switch** | Edit `intelligence/config/registry.live-demo.json` in place to add a use case to `"killed"` or set `"global_kill": true`. | Python gateway re-reads flags on the next request without container restart (`Gateway._refresh_kill_state`). Call is immediately refused with `KILL_SWITCH_ENGAGED` / `REVIEW_REQUIRED` (explain route). The classify route answers `REFUSED` with `AI_KILL_SWITCH_ENGAGED`. |
| **4. Evidence & events** | Inspect container logs (`docker compose logs ztax-model-gateway`). | Every model invocation seals a tamper-evident `PROPOSE` record (`ztax_gateway.evidence`) with prompt hash and latency. Answer checks append a linked `AnswerCheckedEvent` naming the invocation ID. |
| **5. Governed fallback** | Cause a model failure, timeout (>45s), or format error. | The UI never crashes with an unhandled exception. It degrades gracefully to human review (`AI_PROPOSAL_NOT_AN_ONTOLOGY_NODE`, `AI_GATEWAY_UNAVAILABLE`, `REVIEW_REQUIRED`), proving fail-closed governance. The classify route answers `REFUSED` with `AI_GATEWAY_UNAVAILABLE`. |

## Fallback: no frontend, no Docker

If the stack won't come up in time, these run from a terminal with no compose
change at all. Only the first uses a real model:

```bash
cd intelligence
python tools/live_demo.py        # REAL Groq model (calls Groq; needs GROQ_API_KEY / GROQ_MODEL)
python tools/step10_demo.py      # NO model at all -- the boundary claims on their own
```

`live_demo.py` loads `config/registry.live-demo.json` and labels its routes with
your `GROQ_MODEL`, so its provenance names the real model. `step10_demo.py`
only runs the existing Python and Go test suites; no live model is connected.

## Prompt profiles

- **Required env var:** `ZTAX_GATEWAY_PROMPT_PROFILES` names the prompt-profile file. There is no default path and no fallback.
- **Mount:** the compose overlay mounts `config/prompt_profiles.live-demo.json` read-only (`:ro`) at `/etc/ztax/ai/prompt_profiles.json`, next to the registry, and sets the variable to that path.
- **Startup fails closed:** `serve_live_demo.py` and `report_ai_http.py` exit with status 2 if the variable is unset, the file is missing or invalid, a route's `prompt_profile` label has no profile, or a profile's text differs from the text the runtime sends. A profile that no route uses only logs one warning.
- **Read once, never live:** the profiles are read at startup only, so after editing the file the service must be restarted. The single-file mount has the same edit-in-place caveat as the registry mount (see the warning under "Kill switch"): a replaced file may not show up inside the container.
- **`live_demo.py`** reads the same variable (and exits with status 1 on the same failures), so set it before running it from a terminal.

## Kill switch

The Python gateway monitors its registry file (`config/registry.live-demo.json`) for live kill-switch changes:

- **Per-call sync without restart:** Before every call, the gateway re-reads its registry file (`Gateway._refresh_kill_state` in `server.py`). When the file's bytes change, it syncs `"killed"` (a list of use case ids) and `"global_kill"` (`true` or `false`). Any kill takes effect on the next call, with no restart required.
- **Releasing a kill:** Removing an id from `"killed"`, or setting `"global_kill"` back to `false`, restores service on the next call. Only kills that the file set are released.
- **Fail closed:** If the file is missing, unreadable, malformed JSON, or a key has the wrong type, the gateway engages the global kill until the file parses cleanly again.
- **Missing keys:** Missing `"killed"` or `"global_kill"` keys mean "not killed".
- **Startup-only keys:** `"suspended"` and every other registry key are still read only at start-up, so changing them needs a restart of the `ztax-model-gateway` container.
- **Go side:** The Go services still read the kill keys at start-up only.

> [!WARNING]
> **Edit the registry file IN PLACE:** Open the file, change it, and save to the same file. Do not replace the file (such as copying a new file over it, or an editor that saves by writing a new file and renaming it). The registry is mounted into the container as a single file, and a replaced file may not show up inside the container, so you could believe a use case is stopped when the gateway never saw the change.

### Verification in Demo

For demo verification: call a killed use case, see it refused with the expected error, and check `EVIDENCE.md` / audit log to confirm no prompt content hash was sealed and no check event was written (because killed calls abort before reaching generation).

## Rules

- **Each person uses their own `GROQ_API_KEY`.** Never share one, never put
  it in `.env.example`, never commit `.env`.
- **Rotate the key after the demo.** It was used for a live, unreviewed,
  temporary exercise -- treat it as spent.

## Deviations

### Raw fetch in `frontend/src/platform/demoAi.ts`

| Field | Detail |
|---|---|
| **What** | A single hand-written `fetch` call to the demo AI service, with one `// eslint-disable-next-line no-restricted-globals` comment on that line. |
| **Which rule** | ADR-0019 C4 ("the fiscal surface is reached through the SDK"). |
| **Why not a violation** | The demo AI service (`intelligence/tools/report_ai_http.py`) is not a ZoikoTax API endpoint and has no OpenAPI contract. C4 governs hand-written fetches against ZoikoTax's own fiscal API. This fetch reaches an uncontracted, synthetic-only, demo-only service that is explicitly labelled as such. |
| **Compensating controls** | (1) The exception is in exactly one file (`demoAi.ts`) and the lint disable is on exactly one line. (2) The service returns only synthetic, advisory data -- never a determination, obligation, or fiscal decision. (3) The fetch never throws: all errors are returned as typed failure results so a downed service cannot break the surrounding page. (4) Response shape is validated by the caller before any value is used. (5) CORS is limited to `http://localhost:13000` in `report_ai_http.py`. (6) The service binds to `0.0.0.0` inside the container but is published on `127.0.0.1` only (see `docker-compose.live-demo.yml`). |
| **Removal condition** | Delete the Reports (demo) page and `frontend/src/platform/demoAi.ts` together. The lint exception disappears with the file. |
| **Approver** | Lane N |
| **Expiry / removal date** | TBD - to be set by Lane N |
