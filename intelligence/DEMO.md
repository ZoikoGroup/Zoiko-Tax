# Step 10: AI Boundary Demo Runbook

This document is for **stakeholders, auditors, and tax-content specialists**
who want to verify that the ZoikoTax AI gateway enforces its authority boundary
independently of any live model output.

---

## What Step 10 Demonstrates

The Engineering Reset Playbook (ZTAX-ENG-RESET-001 §26) requires a reproducible
demo that shows, claim by claim, that the AI gateway:

- cannot be tricked into elevating authority (A5 block),
- cannot be fed a forged citation,
- cannot leak sensitive data or cross tenant boundaries,
- fails closed when the provider is down, and
- rejects malformed wire inputs before governance even runs.

This demo runner collects the formal proof tests written in
`tests/test_ai_boundary_proof.py` and prints a table of results.

---

## How to Run

From the `intelligence/` directory:

```
python tools/step10_demo.py
```

That is the only command needed. It runs the boundary proof tests internally
and prints a formatted table to the terminal, followed by a Markdown evidence
table you can copy into a review document.

**Exit codes**
- `0` — every claim is PASS or PARTIAL; none FAILED. (C9 reports PARTIAL even
  on a clean run — see below for why that is correct, not a bug.)
- `1` — at least one claim FAILED.

---

## The 10 Claims

| ID | Description | Scope |
|----|-------------|-------|
| C1 | Hostile text / prompt injection causes no action | Intelligence |
| C2 | Hostile text inside a RAG document is neutralised | Intelligence |
| C3 | Fabricated citations and unsupported rules are rejected | Intelligence |
| C4 | A5 authority escalation is always blocked | Intelligence |
| C5 | Provider unavailability surfaces a defined failure; kill switches work | Intelligence |
| C6 | Sensitive data never leaks; tenant isolation strictly enforced | Intelligence |
| C7 | Malformed context is refused at the wire layer before governance | Intelligence |
| C8 | Commits and determination continue with AI off | Go Core Engine |
| C9 | Ambiguous mappings route to human review (PARTIAL — see below) | Go Core Engine |
| C10 | A T4 (prohibited) risk tier is refused unconditionally; the model runtime is never reached | Intelligence |

---

## What Is Proven Inside the Intelligence Fabric

Claims C1–C7 are proven by **library-level tests** in
`tests/test_ai_boundary_proof.py`.  They run purely against Python objects —
no live model, no network, no external service.  The tests prove that the
**gateway's own controls** (authority caps, citation grounding, evidence
redaction, tenant scoping, wire validation) work correctly regardless of what
a model might return.

Claim C10 is proven the same way, in a separate file, `tests/test_t4_prohibition.py`:
the T4 risk tier is refused unconditionally, including when the use case's
own ceiling is declared T4, and the model runtime is never reached.

---

## What Is NOT IMPLEMENTED Here

**C8 — Commits and determination continue with AI off**  
This claim belongs to the Go core engine (the tax determination service
written in Go).  When the Python AI gateway is unavailable, Go determination
must continue without interruption.  This is structurally enforced by Go and
Python being separate processes with separate failure domains; it cannot be
verified from the Python side.

This row runs real Go tests (see `tools/step10_demo.py`'s `C8_APP_TESTS` /
`C8_HTTP_TESTS`) and reports PASS or FAIL from them, not a placeholder.

---

## What C9 Actually Proves — PARTIAL, Not "Ambiguous Mappings"

**C9 — Ambiguous mappings route to human review**  
The claim as named is broader than what is built. What the Go tests actually
prove is narrower and different in kind: when the AI gateway cannot help at
all — unavailable, not configured in this cell, or it answers with a
proposal that is not a valid ontology node — the classification endpoint
returns a `REVIEW_REQUIRED` disposition instead of a fabricated proposal, and
the deterministic tax path is unaffected either way.

**What this does NOT prove:**
- **"Ambiguous" mappings.** There is no confidence-based routing. A
  successful AI proposal with any confidence score, however low, is still
  returned as a proposal for a person to confirm — it is never itself routed
  to review for being uncertain.
- **A content-review queue.** Nothing is persisted. The disposition is
  returned synchronously in the API response for that one request; there is
  no table, no repository and no endpoint to list held items.

This row runs real Go tests (`C9_APP_TESTS` / `C9_HTTP_TESTS`) and reports
**PARTIAL** when they pass — never PASS, because passing them proves less
than the claim's full text. It reports FAIL if they do not pass.

---

## Limitation — No Live Model

These tests prove that **gateway governance is independent of model output**.
They do not prove that a live, unverified model would produce accurate tax
answers.  Connecting a real model provider requires Security/Lane C approval
and vault credentials (ADR-0017 §2.5) and is outside the scope of this demo.

---

## For Reviewers

If any claim shows **FAIL**, it means a boundary proof test in
`tests/test_ai_boundary_proof.py` did not pass.  The test file contains the
full specification basis, the exact proof obligation, and the evidence
produced.  Read the pytest output in the terminal above the table to see
which assertion failed.
