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
  case, model profile, provider profile, prompt profile, AI train). The
  output check is **format-only**: the code must start with `ontology:` and
  name something after it. It does **not** check that the code is a real
  ontology node -- nothing in a running service holds that list.
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

## Kill switch

`registry.live-demo.json` keeps `killed` and `global_kill` keys only because a
test compares them with the dev file. **`load_config` ignores both.** The only
lever that stops a use case in this stack is `"suspended": true` on that use
case in `registry.live-demo.json`, followed by a restart of the
`ztax-model-gateway` container.

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
