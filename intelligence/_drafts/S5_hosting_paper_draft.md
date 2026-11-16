<!--
  SCRATCH DRAFT: S5 Hosting Decision Paper Rewrite (Self-Hosting)
  STATUS: Working draft for Kaushal to check numbers and deployment facts.
  DO NOT COMMIT TO MAIN: This is a scratch draft under intelligence/_drafts/.
  NOTICE: Self-hosting is a recorded architectural direction, but nothing is
  approved, released, or compliant for production.
-->

# S5 Design Paper: AI Model Gateway Self-Hosting Architecture & Economics

## 1. D-01 Decision & Executive Recommendation

**Decision Status:** The formal written D-01 decision text and its signatory are **NOT YET WRITTEN** (to be supplied by Kaushal / Architecture Committee).

**Working Recommendation:**
In alignment with the engineering direction to eliminate external third-party data egress, ZoikoTax adopts an on-premises / private VPC self-hosted model runtime architecture (`LocalModelRuntime`). This architecture deploys an open-weights inference engine speaking standard OpenAI-compatible chat completions protocol within a governed, air-gapped network boundary. 

*Crucial Boundary Notice:* Although self-hosting is an active architectural stream, **no self-hosted deployment is currently approved, released, or certified compliant for production use**.

---

## 2. Earlier Corrections & Baseline Alignment

The following corrections from earlier reviews are recorded and applied:

1. **Provider Identification:** The temporary live demonstration runtime relies exclusively on Groq Cloud infrastructure (`groq_demo_runtime.py`) as an unapproved demo-only path. The proposed target architecture completely replaces this external SaaS dependency with private, self-hosted instances.
2. **Rate Limiting & Concurrency Ceiling:** The earlier documentation claimed uncalibrated external concurrency. In the actual gateway implementation, concurrency is strictly bounded: the demo gateway enforces a hard concurrency ceiling of at most 8 in-flight model calls (`RUNTIME_MAX_IN_FLIGHT = 8`) and a 45-second deadline, failing closed with a `BLOCK` record upon saturation (`tests/test_live_demo_timeout_wiring.py`).
3. **Break-Even Economic Analysis:** Earlier assumptions that external API pricing was always cheaper at low volume neglected sovereign data isolation compliance overhead and network egress fees. The revised model establishes that private compute parity is reached once invoice classification volume exceeds regular threshold levels. *(Exact break-even point: [TO CONFIRM - Kaushal]).*

---

## 3. Infrastructure & Operational Cost Model

### A. Two-Server Hardware / Cloud Compute Costs

To satisfy high-availability (HA) and zero-downtime failover requirements without relying on external SaaS multi-tenancy, the minimum recommended deployment consists of two dedicated inference servers:

| Compute Specification | Purpose | Monthly Cost (USD) | Source / Notes |
| :--- | :--- | :--- | :--- |
| **Primary Inference Node (Server 1)** | Active LLM inference execution | [TO CONFIRM - Kaushal] | Requires dedicated GPU/vGPU (e.g., NVIDIA A10G/L4/A100) |
| **Secondary Standby Node (Server 2)** | Warm failover & rolling updates | [TO CONFIRM - Kaushal] | Identical sizing to Server 1 |
| **Inter-node Egress / VPC Peering** | Private cluster communication | [TO CONFIRM - Kaushal] | Zero public internet exposure |
| **Total Monthly Infrastructure** | High-availability compute floor | **[TO CONFIRM - Kaushal]** | Excludes base cloud tenancy fees |

### B. People & Operational Maintenance Estimate

Self-hosting introduces infrastructure management responsibilities not present with SaaS APIs:

- **Site Reliability & GPU Driver Maintenance:** Patching host OS, NVIDIA drivers, CUDA runtimes, and inference server containers (vLLM / TGI).
- **Model Weight Integrity & Quantization:** Verifying SHA-256 digests of model checkpoints and testing tensor precision stability.
- **Estimated Engineering Overhead:** [TO CONFIRM - Kaushal] engineering days per quarter.

---

## 4. Model Weights & Licensing Governance

Under ADR-0017 and the CTO Directive (ZTAX-INT-CTO-001):

- **Target Model Family:** Pinned open-weights models (e.g., Llama 3 / 3.1 family).
- **Licence Terms:** [TO CONFIRM - Kaushal] (commercial usage thresholds, redistribution clauses, and attribution mandates must be confirmed by legal).
- **Prohibition on Floating Aliases:** The runtime strictly forbids unpinned or floating model tags (such as `latest`, `default`, `stable`, `preview`). All deployments must bind to immutable, versioned model names (`src/ztax_gateway/local_runtime.py::_FLOATING_ALIASES`).

---

## 5. Cloud Platform, Regional Deployment & Residency

- **Cloud Provider Target:** [TO CONFIRM - Kaushal] (AWS, GCP, Azure, or bare-metal).
- **Regional Deployment Zone:** [TO CONFIRM - Kaushal] (e.g., `eu-west-1` / `eu-west-2` to comply with UK/EU VAT data sovereignty).
- **Data Residency Boundary:** As proven in test suites, customer SKU descriptions and tenant identifiers must never leave the sanctioned legal jurisdiction (`tests/test_ai_boundary_proof.py::TestP7TenantIsolation`).

---

## 6. Regulatory & Conformance Compliance Status

As defined by **CTO Directive ZTAX-INT-CTO-001** and **Conformance Addendum ZTAX-AI-CONF-ADD01**:

1. **Uncalibrated Model Guard:** The self-hosted runtime adapter (`LocalModelRuntime`) returns a fixed placeholder confidence of `0.5000` because raw model probabilities cannot be treated as calibrated tax probabilities without an empirical gold-set benchmark.
2. **Gold Fixture Dependency:** Under ADD01 §9, no production pass rate or accuracy metric may be published until the 54-case assistant catalogue (assistant Document 06) is pinned and reviewed.
3. **Unapproved Status:** The `LocalModelRuntime` skeleton in `src/ztax_gateway/local_runtime.py` is currently verified only as a harness evaluation target (`tools/evaluate_gateway.py --runtime local`). It is **not yet wired into `server.py` or the production gateway factory**.

---

## 7. What Needs Kaushal's Check

The following parameters must be confirmed by Kaushal before this paper can be submitted for architectural sign-off:

| # | Item Requiring Confirmation | Assigned Owner | Status / Value |
|---|---|---|---|
| 1 | Formal text of written Decision D-01 and authorized signatory | Kaushal | [TO CONFIRM - Kaushal] |
| 2 | Target cloud provider (AWS / GCP / Azure / On-Premises) | Kaushal | [TO CONFIRM - Kaushal] |
| 3 | Target primary and secondary regions (e.g. `eu-west-2`) | Kaushal | [TO CONFIRM - Kaushal] |
| 4 | Exact instance types and monthly cost for 2-server setup | Kaushal | [TO CONFIRM - Kaushal] |
| 5 | Concrete break-even classification volume threshold | Kaushal | [TO CONFIRM - Kaushal] |
| 6 | Exact model family, checkpoint tag, and commercial licence terms | Kaushal | [TO CONFIRM - Kaushal] |
| 7 | Dedicated SRE/DevOps maintenance allocation (days/month) | Kaushal | [TO CONFIRM - Kaushal] |
| 8 | Cloud quota reservation status for production accelerator compute | Kaushal | [TO CONFIRM - Kaushal] |

---
*All items above are suggestions until D-01 is signed.*
