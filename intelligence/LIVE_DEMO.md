# Live Groq demo -- DEMO ONLY

A temporary way to show a real, pinned Groq-hosted model sitting behind the
existing governed Gateway, with nothing else about the stack changed.
**Never for production. Synthetic data only. Not part of CI.**

## 1. Add two lines to your local `.env`

`.env` is already gitignored (`ADR-0017 §2.4`: a credential never lives in
the tree). Add:

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
open http://localhost:3000     # or the WEB_PORT you set in .env
```

Sign in, open **Intelligence**, and submit a subject reference and
description (catalog text only -- never customer data; the description goes
to the Model Gateway and, this time, on to Groq).

### What to expect

- **A proposal**: if the model's reply happens to be a valid ontology-class
  string, the page shows a proposed code, a confidence value, and the
  provenance table (use case, model profile, provider profile, prompt
  profile, AI train).
- **A review-required result**: unlike the fake runtime (which always
  returns a valid placeholder code), a real, unconstrained model's free-text
  answer is quite likely to **not** be a valid ontology node. When that
  happens the page shows "Needs human review" with reason
  `AI_PROPOSAL_NOT_AN_ONTOLOGY_NODE` -- this is the governed boundary
  working as designed (`backend/internal/app/classification.go`), not a bug
  in the demo. Expect to see this outcome at least as often as a clean
  proposal, depending on what the model returns.
- A provider failure, a timeout, or a malformed reply also surfaces as
  "Needs human review" (`AI_GATEWAY_UNAVAILABLE`) rather than an error page --
  the governed path degrades to review, it does not crash.

## 4. Stop the demo

```bash
docker compose -f docker-compose.yml -f docker-compose.live-demo.yml down
```

Afterwards, `docker compose up -d` (the base file alone, no overlay) returns
the stack to the fake runtime.

## Fallback: no frontend, no Docker

If the stack won't come up in time, both of these show the same governance
claims against a real model, from a terminal, with no compose change at all:

```bash
cd intelligence
python tools/live_demo.py        # real Groq model, same GROQ_API_KEY / GROQ_MODEL
python tools/step10_demo.py      # no live model at all -- the boundary claims on their own
```

## Rules

- **Each person uses their own `GROQ_API_KEY`.** Never share one, never put
  it in `.env.example`, never commit `.env`.
- **Rotate the key after the demo.** It was used for a live, unreviewed,
  temporary exercise -- treat it as spent.
