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
- `0` — all 7 Intelligence boundary claims PASS and the 2 Go-side claims are
  correctly reported as NOT IMPLEMENTED.
- `1` — at least one Intelligence claim FAILED.

---

## The 9 Claims

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
| C9 | Ambiguous mappings route to human review | Go Core Engine |

---

## What Is Proven Inside the Intelligence Fabric

Claims C1–C7 are proven by **library-level tests** in
`tests/test_ai_boundary_proof.py`.  They run purely against Python objects —
no live model, no network, no external service.  The tests prove that the
**gateway's own controls** (authority caps, citation grounding, evidence
redaction, tenant scoping, wire validation) work correctly regardless of what
a model might return.

---

## What Is NOT IMPLEMENTED Here

**C8 — Commits and determination continue with AI off**  
This claim belongs to the Go core engine (the tax determination service
written in Go).  When the Python AI gateway is unavailable, Go determination
must continue without interruption.  This is structurally enforced by Go and
Python being separate processes with separate failure domains; it cannot be
verified from the Python side.

**C9 — Ambiguous mappings route to human review**  
This is a content-review workflow: when a SKU classification is ambiguous, the
system must route it to a human reviewer rather than accepting an uncertain
AI proposal.  This workflow lives in the Go service and the content-review
queue, not in the Intelligence gateway.

These two rows will always show **NOT IMPLEMENTED** in this runner.  They must
be demonstrated through Go integration tests and the content-review workflow,
not through this script.

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
