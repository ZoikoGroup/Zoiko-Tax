# Self-Hosted Model Licence and Governance Note (DRAFT)

**Status:** DRAFT -- NOT APPROVED  
**Owner:** [TO CONFIRM - Kaushal]  
**Target Delivery:** K4 model choice / Self-hosted gateway runtime  
**Disclaimer:** This document is for architectural planning and record-keeping purposes only. Self-hosting is a recorded direction, not an approval, and nothing here constitutes legal advice.

---

## 1. Scope & Recorded Direction

This draft note outlines the due-diligence preconditions that must be satisfied before any self-hosted model profile or provider profile can be activated for evaluation or production use within the ZoikoTax estate.

Under AIGOV-001 and Chapter 17 §7, self-hosted deployment requires verified licensing, integrity verification, and explicit compliance sign-off. Until an approved model is selected and verified, all self-hosted profiles remain in status `DRAFT` with `suspended: true`.

---

## 2. Model Selection and Licence Terms

The specific model family, parameter count, and revision remain subject to review:

- **Model Family & Identity:** [TO CONFIRM - Kaushal]
- **Base Licence Name:** [TO CONFIRM - Kaushal] (e.g. Apache-2.0, MIT, Llama Community License, or custom commercial terms)
- **Licence URL / Reference:** [TO CONFIRM - Kaushal]
- **Redistribution & Hosting Rights:** [TO CONFIRM - Kaushal] (Verification that the model weights and runtime may be self-hosted on private infrastructure or tenant clouds without breaching distribution or hosting clauses)
- **Commercial-Use Terms & Revenue Thresholds:** [TO CONFIRM - Kaushal] (Confirmation of whether commercial use is permitted unconditionally or subject to monthly active user (MAU) / revenue thresholds)
- **Acceptable Use Policy (AUP) Restrictions:** [TO CONFIRM - Kaushal] (Review of domain-specific restrictions, such as financial or tax calculation limitations in the model provider's acceptable use terms)

---

## 3. Weights Integrity and Supply-Chain Verification

Before self-hosted weights may be mounted into a runtime container:

- **Source Repository / Canonical Upstream:** [TO CONFIRM - Kaushal]
- **Weights Cryptographic Digest (SHA-256):** [TO CONFIRM - Kaushal]
- **Model Checkpoint Format:** [TO CONFIRM - Kaushal] (safetensors required; pickle-based checkpoints prohibited)
- **Malware & Security Scan Result:** [TO CONFIRM - Kaushal] (Independent static analysis / weights scanning result)
- **Quantisation & Conversion Verification:** [TO CONFIRM - Kaushal] (If quantised checkpoints are used, verification that quantization introduces no material drift or untracked modifications)

---

## 4. Required Approvals Before Activation

Activation of any self-hosted profile requires:
1. Pinned model and provider profiles signed off by the accountable owner.
2. Independent legal/licensing clearance on commercial use and AUP compliance.
3. Verification that `suspended` is flipped to `false` only in the active runtime registry after successful gateway integration tests pass.
