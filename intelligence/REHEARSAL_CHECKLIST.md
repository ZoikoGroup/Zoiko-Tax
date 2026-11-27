# Rehearsal Checklist — Live Demo
<!-- DEMO ONLY. Synthetic data only. Never for production. Not part of CI. -->

> **Source notes are inline.** Every port, URL, command, and screen text is
> taken from the files listed; nothing is invented. Items the files do not
> settle are marked `[TO CONFIRM: reason]`.

---

## 1. Setup on a Clean Checkout

### 1.1 Prerequisites

Before starting, confirm:

| Check | Command / action |
|---|---|
| `.env` exists at repo root | `ls .env` |
| `.env` is **not** committed | `cat .gitignore` — it lists `.env` |
| `GROQ_API_KEY` is set | Use your own key; see `.env` line 79 (never share or commit it) |
| `GROQ_MODEL` names a pinned model | `.env` line 80; must not be `latest`, `default`, `stable` or `preview` — `groq_demo_runtime.py` refuses those before any call |
| `.env.example` has been copied and filled in | `LIVE_DEMO.md §2` |

**Source:** `intelligence/LIVE_DEMO.md §1–2`, `docker-compose.live-demo.yml` comments.

### 1.2 Environment Variables in the Container

These are the variables the overlay injects. You do **not** type them: they come from `.env` and the compose overlay.

| Variable | Where set | Value in running demo |
|---|---|---|
| `GROQ_API_KEY` | `.env` → overlay line 65 | Your own key |
| `GROQ_MODEL` | `.env` → overlay line 66 | Pinned model name |
| `ZTAX_GATEWAY_PROMPT_PROFILES` | overlay line 67 | `/etc/ztax/ai/prompt_profiles.json` |
| `ZTAX_ONTOLOGY_APPROVED_LIST` | overlay line 68 | `/etc/ztax/ai/approved_ontology.json` |

**Source:** `docker-compose.live-demo.yml` lines 64–68.

### 1.3 Start the Stack

From the **repo root** (not `intelligence/`):

```bash
docker compose -f docker-compose.yml -f docker-compose.live-demo.yml up --build -d
```

**Source:** `LIVE_DEMO.md §2`, `docker-compose.live-demo.yml` comment line 8.

### 1.4 Confirm the Stack is Up

Run each check and tick it off:

| # | Check | Expected result | Failure sign |
|---|---|---|---|
| A | `docker compose ps` | All containers show `Up`; none show `Exit` | A container exited — check `docker compose logs` |
| B | `curl http://localhost:18080/healthz` | `ok` | No response or error — backend is down |
| C | `curl http://127.0.0.1:18090/demo/fixture` | HTTP 200 with a JSON object | No response — `report_ai_http.py` did not start (check for exit-code 2) |
| D | `curl -s -o /dev/null -w "%{http_code}" http://localhost:13000` | `200` | Frontend container not up |

**Ports (source: `.env` lines 22–23, `docker-compose.live-demo.yml` line 63):**

| Service | Host address |
|---|---|
| Backend / core (`ztax-core`) | `localhost:18080` |
| Frontend (`ztax-web`) | `localhost:13000` |
| Demo HTTP (`report_ai_http.py`) | `127.0.0.1:18090` |
| Model Gateway gRPC | Internal container port 8443 (not published to host) |

> **Exit-code 2 warning (LIVE_DEMO.md §6):** If `ZTAX_ONTOLOGY_APPROVED_LIST` points to an
> invalid or missing file, `report_ai_http.py` exits with code 2 before it opens port 18090.
> The container may still appear `Up` (the other process keeps it running). Check C above will
> fail. Fix: confirm `intelligence/config/approved_ontology.live-demo.json` is present and
> valid, then restart the container.

---

## 2. The Five Demo Steps

### Step 1 — Classify

**What to do**

Open `http://localhost:13000`, sign in, open **Intelligence**. Submit a subject reference and
description (catalog text only — never customer data).

Alternatively, call the HTTP endpoint directly (adds the approved-list check):

```bash
# Expect PROPOSED — voice mobile is on the synthetic list
curl -X POST http://127.0.0.1:18090/demo/classify \
  -H 'Content-Type: application/json' \
  -d '{"subject_ref": "sku:DEMO-VOICE-PLAN", "description": "Mobile voice calling subscription for mobile phones (synthetic demo)"}'
```

**What you should see**

*Main Intelligence page (Go → gRPC path) — source: `LIVE_DEMO.md §3`, `outcomes.ts`*

| Screen text | Meaning |
|---|---|
| "The AI suggested a code", the proposed code, a confidence line "Not calibrated", the provenance table and "took N seconds". On the base stack (fake runtime with `BASE_STACK_CANNED`, returning `ontology:telecom/voice/mobile`) a badge "Test runtime: not a real AI answer" shows too (`viewOfProposal.ts`); with the live Groq overlay it does not. | Model passed format check (`ontology:*`). Disclaimer: "whether that code is a real ontology node is not verified here" (format check only; the Go path's approved-list check is built but off unless `ZTAX_ONTOLOGY_APPROVED_LIST` is configured — no demo stack sets it on this path). |
| Headline "Set aside for a person to review", a plain sentence ("Classify it by hand"), "took N seconds". No code and no reason code. | Model's reply failed even the format check — governed boundary working as designed. (`AI_PROPOSAL_NOT_AN_ONTOLOGY_NODE` is in the API response only.) |
| After 3 seconds with no answer: "Still waiting for the AI" and "The AI is taking longer than usual." (Go call times out at 5 s; screen cuts off at 10 s. Source: `frontend/src/pages/intelligence/viewOfProposal.ts`, `tests/test_slow_response_threshold.py`.) | Not a failure. The call may still finish. |

*HTTP `/demo/classify` — source: `LIVE_DEMO.md §6`, `report_ai_http.py` docstring*

| `outcome` | Screen meaning |
|---|---|
| `PROPOSED` | Code is on the synthetic approved list and currently effective. Badge: "Synthetic demo data". Note: "Confidence: not calibrated" |
| `ABSTAIN` | Code not recognised, expired, or not yet effective. No code shown anywhere. |
| `REFUSED` with `AI_KILL_SWITCH_ENGAGED` | Kill switch is on — see Step 3. |
| `NOT_CONFIGURED` | `ZTAX_ONTOLOGY_APPROVED_LIST` not set in the container. |

**Confidence note (say aloud at this step):** Confidence is `0.5000`, a fixed placeholder set in
`groq_demo_runtime.py`. Groq returns no probability; the number is not the model's score.
Source: `LIVE_DEMO.md §3`.

**Failure signs**

| Sign | Likely cause |
|---|---|
| No response from `/demo/classify` | `report_ai_http.py` not running — check exit-code 2 |
| `NOT_CONFIGURED` | `ZTAX_ONTOLOGY_APPROVED_LIST` not set |
| Containers up but classify crashes page | Frontend/backend wiring issue |

---

### Step 2 — Explain

**What to do**

Open `http://localhost:13000`, navigate to **Reports (demo)**, and click **Explain** on any row.

> [!NOTE]
> The dev registry (`config/registry.dev.json`) registers three explainers (`explain-report-figure`,
> `explain-obligation-row`, `explain-shadow-variance`). An obligation explain route is present in
> the Go router (`POST /v1/obligations/{obligationId}/explanation` in `backend/internal/transport/http/router.go`,
> read from source, not run here), while the buttons on Reports (demo) reach the demo HTTP service
> (`report_ai_http.py`) on port 18090 directly. On the base stack (no Groq overlay), the development registry
> routes to `FakeRuntime(canned=BASE_STACK_CANNED)` which answers fixed `[fake]`-prefixed texts (`src/ztax_gateway/fake_runtime.py`).

Or via curl:

```bash
curl http://127.0.0.1:18090/demo/fixture   # see available ids

curl -X POST http://127.0.0.1:18090/demo/explain \
  -H 'Content-Type: application/json' \
  -d '{"kind": "obligation-row", "ids": ["OBL-001"]}'
```

**What you should see (source: `LIVE_DEMO.md §5`, `report_ai_http.py` docstring)**

| `outcome` | Meaning |
|---|---|
| `EXPLAINED` | Reply passed the guard: at least one cited row id, every number matches a row value exactly, every status word is a cited row's status. |
| `ABSTAINED` | Model abstained, or deterministic guard overruled the reply. No model text is shown. |
| `REVIEW_REQUIRED` | Gateway refused (e.g., kill switch) or unexpected error. Only the Gateway's reason code is returned, never provider text. |

**Guard limits (say aloud):** The guard cannot prove a sentence is true — swapped numbers and
invented causes still pass. A person must read the text. Source: `LIVE_DEMO.md §5`.

**Failure signs**

| Sign | Likely cause |
|---|---|
| `REVIEW_REQUIRED` with `AI_GATEWAY_UNAVAILABLE` | Model or gateway is down |
| 404 from `/demo/explain` | Wrong path or service not up |

---

### Step 3 — Kill Switch

**What to do**

Edit `intelligence/config/registry.live-demo.json` **in place** (open, change, save — do not
copy a new file over it; a replaced file may not appear inside the container).

```json
// To suspend one use case:
{
  "killed": ["classification-review"],
  "global_kill": false,
  ...
}

// To suspend everything:
{
  "killed": [],
  "global_kill": true,
  ...
}
```

No container restart needed for the Python gateway container: the gateway re-reads `killed` and `global_kill` on every call
(`Gateway._refresh_kill_state` in `server.py` line 507, verified by `tests/test_server.py::test_unknown_use_case_and_kill_switch_are_refused`). Note per layer: on the Go side, `wireModelGateway` loads the policy file once at startup into `gateway.StaticPolicy` (`backend/cmd/ztax-core/main.go` around lines 761 and 781, read from source, not run here), so any Go-side policy update or kill switch requires a service restart.

**What you should see**

Send a classify or explain request immediately after saving the file.

| Route | Expected response |
|---|---|
| `POST /demo/classify` | `{"outcome": "REFUSED", "reason_code": "AI_KILL_SWITCH_ENGAGED", ...}` |
| `POST /demo/explain` | `{"outcome": "REVIEW_REQUIRED", "reason": "AI_USE_CASE_SUSPENDED" or "AI_KILL_SWITCH_ENGAGED", ...}` |
| Frontend classify panel | Headline: "Blocked by governance". Sentence: "This AI capability has been switched off. A person should check this." — source: `outcomes.ts` `BLOCKED_SENTENCES` |

**To release the kill:** Remove the use case id from `"killed"`, or set `"global_kill": false`,
and save in place. Service restores on the next request with no restart.

**Fail-closed rule (source: `LIVE_DEMO.md §Kill switch`):** If the registry file becomes
unreadable or malformed JSON, the gateway engages the global kill until the file parses cleanly.

**What it does not do:** A killed call aborts before reaching the model runtime, so no prompt
hash is sealed and no check event is written. Source: `LIVE_DEMO.md §Verification in Demo`.

**Failure signs**

| Sign | Likely cause |
|---|---|
| Kill does not take effect | File was replaced instead of edited in place (atomic write by editor) — restart the container |
| `AI_KILL_SWITCH_ENGAGED` even after removing the kill | Same cause |

---

### Step 4 — Evidence Record

**What to do**

After any successful classify or explain call, inspect the container logs:

```bash
docker compose logs ztax-model-gateway
```

**What to point at (source: `LIVE_DEMO.md §7` row "4. Evidence & events")**

1. **Gateway evidence record** — logger `ztax_gateway.evidence`, level `INFO`.
   - Always records outcome `PROPOSE` (the authority state at invocation time).
   - Fields to show: prompt hash (`prompt_hash`), latency, token metrics (`UsageMetrics`:
     `prompt_tokens`, `completion_tokens` captured from the hosted provider; zero when
     unreported), `monetary_cost_usd` (a `float`, default `0.0`; no `TokenPricing` is wired
     in `gateway_factory.py` for the demo — cost is not calculated).
   - **No `tenant_id` field** in `InvocationEvidenceRecord`. Source: `intelligence/EVIDENCE.md`.

2. **Linked answer-checked event** — logger `ztax_gateway.evidence`, level `INFO`.
   - Validator: `ontology_guard@1` (classify) or `explanation_guard@1` (explain).
   - Linked by `ai_invocation_id` (trailer key `ztax-invocation-id` from `server.py` line 92).
   - Under B6, when `ZTAX_EVIDENCE_FILE_PATH` is set, `FileEvidenceSink` appends check events to the file; an append failure raises `FileSinkError` and fails closed (`AI_GATEWAY_UNAVAILABLE`). When unset, the logging sink remains primary and unchanged.
   - Under B2a, the evidence ID propagates through Go provenance (`ids.go`), API response (`ledger.go`), OpenAPI contract, and UI proposal card (read from source, not run here). Audit record and error-path evidence ID remain PENDING under Decision Q3 (B2b).

3. **Status of pending decisions and items:**
   - **B2b (audit record & error-path evidence id):** PENDING on Decision Q3.
   - **Full T21 (no-leak evidence with tenant reference):** PENDING on Decision Q3.
   - **B3b (real approved list for Go check and gateway guard):** PENDING on Decision Q5.
   - **Evidence retention days:** PENDING on Decision Q9.
   - **Model hosting (Google Cloud / self-hosting):** PENDING on Decision Q6.
   - **54-case pass rate and catalogue:** PENDING on Decision Q7.
   - The actual guard decision (`PASS` or `ABSTAIN`) lives here, **not** in the initial record.
   - Source: `LIVE_DEMO.md §6` "Evidence record vs answer-checked event".

**Say aloud:** Evidence records are sealed but not tamper-evident in the sense that an
independent audit system would require. Source: `EVIDENCE.md`.

**Failure signs**

| Sign | Likely cause |
|---|---|
| No evidence log line | Kill switch was engaged — killed calls emit no evidence record |
| `monetary_cost_usd` is zero | Expected — no pricing is wired for the demo |

---

### Step 5 — Screen Notes (Governed Fallback)

**What to do**

Cause a model failure, timeout (> 45 s), or format error. Options:

- Stop the Groq key from working (temporarily use a bad key in `.env` and restart the container).
- Use a description that reliably makes the model produce a non-`ontology:` reply on the main
  Intelligence page.

**What you should see (source: `LIVE_DEMO.md §3` and `§7` row "5. Governed fallback")**

| Path | Screen state |
|---|---|
| Main Intelligence page | Headline "The AI could not be reached". Sentence: "Try again in a moment. If it keeps happening, classify the item by hand. Deterministic processing is unaffected. Nothing was changed." and "took N seconds". No reason code on the screen. |
| `/demo/classify` (API response) | `{"outcome": "REFUSED", "reason_code": "AI_GATEWAY_UNAVAILABLE"}` |
| Frontend classify panel | Headline: "AI service could not answer". Sentence includes "Try again in a moment." Button: "Try again". Source: `outcomes.ts` `FAILURE_SENTENCES.service`. |

**Key point to show:** The UI never crashes with an unhandled exception. It degrades gracefully
to human review (screen: "Set aside for a person to review" or "The AI could not be reached";
API responses and logs: `AI_PROPOSAL_NOT_AN_ONTOLOGY_NODE`, `AI_GATEWAY_UNAVAILABLE`,
`REVIEW_REQUIRED`), proving fail-closed governance. Source: `LIVE_DEMO.md §7`.

---

## 3. Special Screen States

### Slow-service State

**How to trigger (source: `LIVE_DEMO.md §6` checklist item 5)**

Use a stand-in service that answers `POST /demo/classify` on `127.0.0.1:18090` but waits longer
than 30 seconds before replying. The real service must be stopped first so port 18090 is free.
The stand-in must allow CORS from `http://localhost:13000`.

| Constant | Value | Source |
|---|---|---|
| `DEMO_AI_TIMEOUT_MS` | 30000 ms (30 s) | `demoAi.ts` line `export const DEMO_AI_TIMEOUT_MS = 30_000` |
| Gateway model-call deadline | 4 s (starting point, to be re-checked after real models) | `LIVE_DEMO.md §3`, `groq_demo_runtime.py` `GATEWAY_RUNTIME_TIMEOUT_SECONDS` |

**What you should see**

Frontend classify panel: headline **"Taking too long"**, sentence: "The AI service did not answer
within 30 seconds. It may still finish in the background. Try again. If it keeps happening, ask a
reviewer. Deterministic processing is unaffected." Button: **"Try again"** (manual retry, no
automatic retry). Source: `outcomes.ts` `FAILURE_SENTENCES.timeout`.

Pressing "Try again" resubmits the form. Source: `LIVE_DEMO.md §6` checklist item 5.

---

### Unknown-result State

**How to trigger (source: `LIVE_DEMO.md §6` checklist item 6)**

Use a stand-in service that answers with `{"outcome": "SOMETHING_NEW", ...}` and valid other
fields.

**What you should see**

Frontend classify panel: headline **"Result could not be interpreted"**, sentence: "This result
could not be interpreted. A person should check it." No code shown. Source: `outcomes.ts`
`HEADLINES.needs_review` and `NEEDS_REVIEW_SENTENCE`.

---

## 4. "Say Aloud" Script

Read this box word for word during the demo. These are the facts to state; the list below the
box is what **not** to claim.

---

> **The list of codes is synthetic.**
> `intelligence/config/approved_ontology.live-demo.json` contains 8 codes for demonstration
> only (`list_version: "synthetic-demo-v0"`). The authoritative approved ontology list is
> pending decision D-05 and is not in this repository.
>
> **Confidence is not calibrated.**
> The runtime sets a fixed placeholder confidence (`0.5000`). The model returns no probability,
> so the number has no meaning. The Intelligence page does not show it: it shows "Not calibrated".
>
> **The Go path approved-list check is built but off.**
> On the main Intelligence page (the Go → gRPC path), the output check is format-only: the code
> must start with `ontology:`. The Go path also has an approved-list check built in
> `backend/internal/app/classification.go`, but it is off unless the environment variable
> `ZTAX_ONTOLOGY_APPROVED_LIST` names a file; no demo stack sets that variable on the Go path.
> The check applies to the synthetic list only. Only `POST /demo/classify` runs the approved-list
> guard on a configured list.
>
> **Nothing is released until the decisions are signed.**
> This is a demo of the governed boundary. No determination, obligation, or fiscal decision is
> made here. All outcome and reason names are provisional — the lead has not confirmed the
> AIGOV-001 §11 vocabulary.

---

**Facts to avoid claiming:**

| Do NOT say | Why |
|---|---|
| Any accuracy figure | No gold set has been evaluated; thresholds are null drafts. |
| "This is the approved list" | The list is synthetic (`synthetic-demo-v0`). The real list awaits D-05. |
| "Evidence is tamper-evident" | Records are sealed but independent audit-system tamper-evidence is not in place. |
| "The local runtime enforces loopback" | `LocalModelRuntime` validates the endpoint is HTTP/HTTPS with a non-empty hostname; it does not yet enforce that the host is loopback-only. Source: `LIVE_DEMO.md §Evaluation harness`. |
| "Cost is calculated" | `monetary_cost_usd` defaults to `0.0`; no `TokenPricing` is wired in `gateway_factory.py` for the demo. |

---

## 5. Results Table

Fill in one row per step immediately after running it.

| Step | Description | Pass / Fail | Notes | Date | Run by |
|---|---|---|---|---|---|
| 1 — Classify (main page) | Submit description on Intelligence UI | | | | |
| 1 — Classify (HTTP `/demo/classify`) | `curl` PROPOSED and ABSTAIN cases | | | | |
| 2 — Explain | Click Explain on a Reports (demo) row | | | | |
| 3 — Kill switch (per use case) | Add `classification-review` to `"killed"`, send classify | | | | |
| 3 — Kill switch (global) | Set `"global_kill": true`, send classify and explain | | | | |
| 3 — Kill release | Remove kill, confirm service restores | | | | |
| 4 — Evidence record | `docker compose logs ztax-model-gateway`; identify evidence record and check event | | | | |
| 5 — Screen: governed fallback | Trigger a gateway failure (`AI_GATEWAY_UNAVAILABLE` in logs); confirm "The AI could not be reached", no crash | | | | |
| 5 — Screen: slow service | Stand-in waits > 30 s; confirm timeout headline and "Try again" | | | | |
| 5 — Screen: unknown result | Stand-in returns `SOMETHING_NEW`; confirm "could not be interpreted" | | | | |

---

## 6. Base-Stack Evidence Volume Rehearsal (Task B6 Follow-up)

> [!NOTE]
> The rehearsal commands below are written as exact reference steps (marked **not run here**, per sandbox rules).
> This rehearsal is a test of the base stack only. A platform that mounts volumes differently (for example with its own user or group settings) must repeat it.
> The rehearsal uses standalone `docker run`, never a compose change, matching compose hardening: `--read-only`, `--cap-drop ALL`, `--security-opt no-new-privileges:true`, and identical environment variables with `ZTAX_EVIDENCE_FILE_PATH` pointing inside `/var/log/ztax/evidence`.

### Step 1 — Build the Gateway Target
```bash
docker build -f intelligence/Dockerfile --target ztax-model-gateway -t ztax-model-gateway ./intelligence
```
* **Expected result:** Image builds successfully with target `ztax-model-gateway`.
* **Observed:** `[                                                  ]`

### Step 2 — Run with a New Named Volume (Writable)
```bash
# Create a dedicated named volume (separate from the core service's ./.evidence)
docker volume create ztax-rehearsal-evidence

docker run -d --name ztax-gateway-rehearsal \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  -p 8443:8443 \
  -e ZTAX_ENVIRONMENT=development \
  -e ZTAX_GATEWAY_CONFIG=/etc/ztax/ai/registry.json \
  -e ZTAX_GATEWAY_REGION=local \
  -e ZTAX_GATEWAY_AI_TRAIN=0.0.0-dev \
  -e ZTAX_GATEWAY_ADDRESS="[::]:8443" \
  -e ZTAX_GATEWAY_INSECURE_LOCAL="true" \
  -e ZTAX_GATEWAY_RUNTIME="fake" \
  -e ZTAX_EVIDENCE_FILE_PATH=/var/log/ztax/evidence/gateway-evidence.jsonl \
  -v $(pwd)/intelligence/config/registry.dev.json:/etc/ztax/ai/registry.json:ro \
  -v ztax-rehearsal-evidence:/var/log/ztax/evidence \
  ztax-model-gateway
```
*Tool note:* No existing tool in `intelligence/tools/` is a standalone gRPC client dialing port 8443 directly (existing tools in `tools/` run in-process test harnesses or demo HTTP servers). Trigger a call through the core service stack (`curl -s -X POST http://localhost:8080/v1/classifications/propose ...`) or verify startup logs.
* **Read back command:**
```bash
# Inspect records from the volume using python and read_evidence_file
docker run --rm -v ztax-rehearsal-evidence:/data -w /data python:3.12-slim python -c "
import json
with open('/data/gateway-evidence.jsonl', 'r') as f:
    for line in f:
        print(json.loads(line))
"
```
* **Expected result:** Gateway starts cleanly; invocation writes a valid line to `/var/log/ztax/evidence/gateway-evidence.jsonl`; reading back yields valid JSON records.
* **Observed:** `[                                                  ]`

### Step 3 — Full Volume Simulation (Tiny tmpfs)
```bash
docker rm -f ztax-gateway-rehearsal

docker run -d --name ztax-gateway-full-vol \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  -p 8443:8443 \
  --tmpfs /var/log/ztax/evidence:size=1k,mode=0700 \
  -e ZTAX_ENVIRONMENT=development \
  -e ZTAX_GATEWAY_CONFIG=/etc/ztax/ai/registry.json \
  -e ZTAX_GATEWAY_REGION=local \
  -e ZTAX_GATEWAY_AI_TRAIN=0.0.0-dev \
  -e ZTAX_GATEWAY_ADDRESS="[::]:8443" \
  -e ZTAX_GATEWAY_INSECURE_LOCAL="true" \
  -e ZTAX_GATEWAY_RUNTIME="fake" \
  -e ZTAX_EVIDENCE_FILE_PATH=/var/log/ztax/evidence/gateway-evidence.jsonl \
  -v $(pwd)/intelligence/config/registry.dev.json:/etc/ztax/ai/registry.json:ro \
  ztax-model-gateway
```
* **Expected result:** When write exceeds 1 KiB, `FileEvidenceSink` raises `FileSinkError`; the call is refused with `AI_GATEWAY_UNAVAILABLE`; deterministic core processing is unaffected; logs contain no prompt or response content.
* **Observed:** `[                                                  ]`

### Step 4 — Unwritable Volume (Root-Owned)
```bash
docker rm -f ztax-gateway-full-vol
docker volume create ztax-root-evidence

# Set root:root ownership on volume
docker run --rm -v ztax-root-evidence:/vol alpine chown 0:0 /vol

docker run --rm --name ztax-gateway-unwritable \
  --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  -p 8443:8443 \
  -e ZTAX_ENVIRONMENT=development \
  -e ZTAX_GATEWAY_CONFIG=/etc/ztax/ai/registry.json \
  -e ZTAX_GATEWAY_REGION=local \
  -e ZTAX_GATEWAY_AI_TRAIN=0.0.0-dev \
  -e ZTAX_GATEWAY_ADDRESS="[::]:8443" \
  -e ZTAX_GATEWAY_INSECURE_LOCAL="true" \
  -e ZTAX_GATEWAY_RUNTIME="fake" \
  -e ZTAX_EVIDENCE_FILE_PATH=/var/log/ztax/evidence/gateway-evidence.jsonl \
  -v $(pwd)/intelligence/config/registry.dev.json:/etc/ztax/ai/registry.json:ro \
  -v ztax-root-evidence:/var/log/ztax/evidence \
  ztax-model-gateway
```
* **Expected result:** **Start-up refusal.** `FileEvidenceSink.__init__` opens the file at startup (`evidence_file_sink.py:333`, `gateway_factory.py:136-138`). Permission denial raises `EvidenceFileConfigError` ("evidence file sink: the file could not be opened for appending"), terminating container startup before port 8443 opens.
* **Observed:** `[                                                  ]`

### Step 5 — Clean Up
```bash
docker rm -f ztax-gateway-rehearsal ztax-gateway-full-vol ztax-gateway-unwritable 2>/dev/null || true
docker volume rm ztax-rehearsal-evidence ztax-root-evidence 2>/dev/null || true
```
* **Expected result:** All test containers and temporary rehearsal volumes removed.
* **Observed:** `[                                                  ]`

---

## Stop the Stack

```bash
docker compose -f docker-compose.yml -f docker-compose.live-demo.yml down
```

Afterwards, `docker compose up -d` (base file only, no overlay) returns the stack to the fake
runtime. Source: `LIVE_DEMO.md §4`.

**After the demo:** Rotate `GROQ_API_KEY`. It was used for a live, unreviewed, temporary
exercise — treat it as spent. Source: `LIVE_DEMO.md §Rules`.

---

## Fallback: No Frontend, No Docker

If the stack will not come up in time (source: `LIVE_DEMO.md §Fallback`):

```bash
cd intelligence
python tools/live_demo.py      # REAL Groq call — needs GROQ_API_KEY / GROQ_MODEL
python tools/step10_demo.py    # NO model at all — boundary claims only
```

---

## Discrepancies Found

| Item | LIVE_DEMO.md | Code |
|---|---|---|
| `GROQ_MODEL` example | `llama-3.1-8b-instant` (§1) | `.env` line 80: `openai/gpt-oss-20b` — the file shows an example value; the actual model is whatever you set in `.env`. Not a conflict. |
| `WEB_PORT` | `13000` (§3: "set WEB_PORT=13000 in .env") | `.env` line 23: `WEB_PORT=13000` ✓ |
| `CORE_PORT` | `8080` (docker-compose.yml comment line 10) | `.env` line 22: `CORE_PORT=18080`; docker-compose.yml line 79: `${CORE_PORT:-8080}:8080` — the default is 8080 but the actual `.env` uses 18080. Health check URL is `localhost:${CORE_PORT}/healthz`. |

> [!NOTE]
> `LIVE_DEMO.md` says "open `http://localhost:13000`" and also "set WEB_PORT=13000 in .env
> (the base default is 3000)". The base default in `.env.example` is `WEB_PORT=3000`; the
> actual `.env` overrides it to `13000`. Both are consistent.

---

*Checklist version: aligned with code read on 2026-11-20 (slow-line threshold 3 s, Go path approved-list check built but off unless `ZTAX_ONTOLOGY_APPROVED_LIST` configured). No git command was run.*
