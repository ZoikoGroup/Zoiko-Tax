# Infrastructure

The regional execution cell and the global metadata plane, as code. Release train `INFRA`. Approver: Platform, with CISO for anything that changes a trust boundary ([ADR-0007](../../adr/ADR-0007-repository-topology-and-go-module-layout.md) §2.2).

> A cell is the unit of residency, deployment and failure. Cells share no database, no broker, no cache and no bundle-serving path. — [ADR-0009](../../adr/ADR-0009-service-decomposition.md) §2.6

## The cloud provider is not decided

No ADR selects one. Hosting is an open question in ADR-0007 §7, and the secret-store choice in ADR-0017 §7 is constrained by residency and sovereign profiles that are not settled yet. **AWS is the reference implementation here, not a decision.** What *is* decided is the shape of a cell, and that is written down as a provider-neutral contract — [`tofu/modules/regional-cell/contract.json`](tofu/modules/regional-cell/contract.json) — which `tools/check_tofu.py` enforces on every implementation. A second provider is a sibling directory, `regional-cell/<provider>/`, that declares exactly the same variables and outputs, or CI fails.

## What is here

| Path | What |
|---|---|
| `tofu/modules/regional-cell/aws/` | One cell: its own network, PostgreSQL 17, KMS keys, retention-locked evidence store, Kubernetes cluster, secrets and workload identities |
| `tofu/modules/regional-cell/aws/tests/` | `tofu test` against a mocked provider: each residency guard refuses |
| `tofu/modules/global-metadata-plane/aws/` | Cell directory and tenant→cell routing. The one deliberately multi-region thing in the estate |
| `tofu/envs/dev/cell-euc1-dev-01/` | A development cell in `eu-central-1`, one root and one state file of its own |
| `tofu/envs/dev/global-metadata-plane/` | The development metadata plane, its own root and state |
| `kubernetes/base/` | The provider-neutral cell baseline: namespace, service accounts, network policy, `ztax-core`, `ztax-outbox-relay`, the `ztax-migrate` Job |
| `kubernetes/overlays/euc1-dev-01/` | That cell's identity, credential references, workload identities and Cilium egress |
| `nfr/ztax-core/` | ServiceCriticality, SLIs, SLOs, CapacityProfile and ResilienceProfile for the cell binary |
| `observability/prometheus/` | Alert rules **generated** from the SLOs — never edited by hand |
| `tools/` | The checks below, and their tests |

The admission policy is not here: it is security policy, under [`../policy/`](../policy/README.md) with its own approver.

## The cell contract

Inputs are tiers and facts, never provider identifiers; outputs are shapes whose values are opaque to callers.

| Input | Meaning |
|---|---|
| `cell_id` | `ZTAX_CELL`. Prefixes every resource. Never reused |
| `environment` | `dev` · `staging` · `production` — never `development`, which the binary treats as a laptop |
| `region`, `residency` | The one region, and the envelope it must be inside |
| `zones` | Zones inside `region`; three or more in production |
| `network` | The cell CIDR — the only destination a Go workload can reach |
| `database` | PostgreSQL major (17, ADR-0008 §2.1), size tier, HA, backup retention |
| `kubernetes` | Version, node size tier, node counts, whether the API is public |
| `evidence` | Retention days and lock mode; production is `COMPLIANCE` |
| `workload_namespace`, `labels` | Where workloads run; extra tags |

| Output | Consumed by |
|---|---|
| `cell_id`, `region` | Overlay `ZTAX_CELL`, `ZTAX_REGION` |
| `network` | Overlay egress policy (cell CIDR, database subnets) |
| `kubernetes`, `database`, `evidence_store` | Bootstrap and conformance |
| `keys` | Key identifiers by purpose — storage, evidence, seal (ADR-0017 §2.6) |
| `workload_identities` | Overlay service-account annotations |
| `secret_refs` | Overlay `ZTAX_DATABASE_URL_REF` — `aws-sm://<name>`, a reference, never a value |

## Why it is shaped this way

**Residency is enforced three times, because its failure is silent.** A cell built in the wrong region looks exactly like one in the right region, down to its labels. Variable validation keeps `region` inside the envelope and every zone inside `region`; a precondition checks the provider is really configured for `region`; and `check_tofu.py` refuses the AWS provider's per-resource `region` argument, region literals, multi-Region keys, replica blocks, replication and cross-region backup copies anywhere in a cell module. `tofu test` proves each guard refuses.

**Nothing in a cell is shared, including state.** One root and one state file per cell, in a bucket in the cell's own region. The database, evidence store and secrets have no cross-region replica: disaster recovery may not fail data into another geography (NFR-001 doctrine), so recovery into another cell is an evidenced transfer, not a setting.

**Keys never leave the KMS and are split by purpose** (ADR-0017 §2.6, §2.7): a storage key, an evidence-store key, and an ECDSA P-384 seal key (ADR-0011 §2.5) that cannot be destroyed by `tofu destroy`. Content-signing keys are absent on purpose — they belong to the content build plane, not to anything that consumes bundles.

**Secrets are declared, never written.** The module creates each secret and no secret version: a value written by OpenTofu is a value in the state file every pipeline reads (ADR-0017 §2.4). The RDS master credential is generated and held by Secrets Manager and never passes through state.

**The evidence store is write-once.** Object Lock is on from creation, every object gets a default retention, and the bucket policy denies version deletion, governance bypass and replication. The application's role can add and read evidence and nothing else.

**The Kubernetes baseline is deny-everything, then name each flow.** Pod Security `restricted` is enforced on the namespace. A default-deny NetworkPolicy covers ingress and egress, and the base allows only DNS to the cluster resolver, `ztax-web` → `ztax-core`, Go workloads → the cell's OpenTelemetry collector, and `ztax-core` → the Model Gateway. None of those leaves the cluster, so **ADR-0006 §2.1 holds by construction on any conformant CNI**: no allow rule names an external destination, so no model provider is reachable.

**Cilium, because the rest of the allow-list needs names and an explicit deny.** The cell's KMS, Secrets Manager and STS are reached by regional DNS name through private endpoints, and vanilla NetworkPolicy can neither name a host nor deny. The overlay's CiliumNetworkPolicies add two more layers, each sufficient alone: Cilium's DNS proxy answers only the names Go workloads need, so `api.openai.com` or `bedrock-runtime.<region>.amazonaws.com` does not resolve; and an `egressDeny` of everything outside the cell CIDR overrides any allow a future change might add. The list of provider hosts lives once, in [`../policy/egress/ai-provider-endpoints.json`](../policy/egress/ai-provider-endpoints.json), and `check_manifests.py` proves no Go-workload allowance can match any of them. The cell module refuses a VPC endpoint for any model-provider service for the same reason. (Cilium deny rules cannot take FQDNs, which is why the explicit deny is by CIDR and the name-based layer is the DNS allow-list.)

**Workloads hold no credential.** Each deployable has its own service account bound to its own IAM role (IRSA), scoped to exactly its secrets and keys; no Kubernetes API token is mounted, and nodes enforce IMDSv2 with a hop limit of one so a pod cannot borrow the node's role. Configuration names where a secret lives — `ZTAX_DATABASE_URL_REF=aws-sm://…` — and the process resolves it at start.

**Every `ZTAX_` variable is one the binary knows.** `config.Load` refuses to start on an unknown one, so `check_manifests.py` parses `config.go`'s own table (and `os.Getenv` calls for `ztax-migrate`) and fails CI on a typo rather than a crash loop. It also requires `enableServiceLinks: false`: with service links on, Kubernetes injects `ZTAX_CORE_SERVICE_HOST` and friends for the `ztax-core` Service, and every replica would refuse to start.

**SLOs are code, and the alerts are derived from them** (ADR-0015 §2.10). The schemas are contracts in [`../contracts/schemas/nfr/`](../contracts/schemas/nfr); the documents are here. `tools/nfr.py` validates both and the references between them — every SLO names an SLI that exists and fits its objective, every latency SLO names a CapacityProfile, nothing approved rests on a placeholder, no C0 journey calls AI synchronously, resilience and criticality agree — then generates the Prometheus rules. CI fails if the committed rules are not what the SLOs generate. The `ztax-core` capacity numbers are **placeholders pending the load test**, listed by JSON pointer in the profile itself, and the profile cannot be marked measured while any remain.

## Verification

```bash
tofu fmt -check -recursive infra/tofu
tofu -chdir=infra/tofu/envs/dev/cell-euc1-dev-01 init -backend=false && tofu -chdir=infra/tofu/envs/dev/cell-euc1-dev-01 validate
tofu -chdir=infra/tofu/modules/regional-cell/aws init -backend=false && tofu -chdir=infra/tofu/modules/regional-cell/aws test
python infra/tools/check_tofu.py

kustomize build infra/kubernetes/overlays/euc1-dev-01 > /tmp/cell.yaml
kubeconform -strict -summary -schema-location default \
  -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json' /tmp/cell.yaml
python infra/tools/check_manifests.py /tmp/cell.yaml          # needs PyYAML (tools/requirements.txt)

python infra/tools/nfr.py validate
python infra/tools/nfr.py rules --check
python -m unittest discover -s infra/tools -p "test_*.py"
```

All of it runs in [`.github/workflows/infra.yml`](../.github/workflows/infra.yml), with every tool a pinned release binary checked against its published SHA-256.

## Not done yet, and where it lands

| Gap | Consequence today | Owner |
|---|---|---|
| No `aws-sm://` resolver in `internal/platform/secrets` (only `local://` exists) | `ztax-core` and `ztax-outbox-relay` refuse to start in this cell: "no resolver for scheme" | W1 lane B/C, backend |
| `ztax-migrate` takes a DSN value, not a reference | Its credential is materialised into its environment through External Secrets — a registered deviation from ADR-0017 §2.4, confined to that Job and refused anywhere else by `check_manifests.py` | Backend: add `ZTAX_MIGRATE_DATABASE_URL_REF` |
| `release.yml` publishes only `ztax-core` and `ztax-web`, and attaches provenance unsigned | Admission refuses every image until a signed SLSA attestation exists, and refuses `ztax-migrate` and `ztax-outbox-relay` until they are published at all — see [`../policy/README.md`](../policy/README.md) | Lane B, APP train |
| No per-cell broker (ADR-0014 §2.4) | The relay logs envelopes, as `cmd/ztax-outbox-relay` already says | Lane B |
| Evidence is a filesystem prototype in the binary | The S3 store exists with its lock and IAM, but no pod egress to S3 is opened until the adapter that uses it lands | W1 lane D |
| Cluster bootstrap (Cilium, CoreDNS, Kyverno, External Secrets, the OTel collector) is not in OpenTofu | Installed before any workload namespace is labelled; the module creates the cluster without the default CNI so nothing schedules without policy | Lane B |
| SLIs are declared ahead of the metrics | The generated rules evaluate to no data until `ztax-core` emits them; `instrumented: false` keeps every SLO from being approved | Lane B / D |
| Model Gateway port 8443 is provisional | The allow rule names a port the Gateway does not serve yet | W2 lane K |

Nothing here has been applied to a real account or cluster. What is proven is what can be proven without one: the configuration validates, the residency guards refuse, the manifests are schema-valid and say what the ADRs require, and the admission policy refuses unsigned and wrongly-signed images against the real registry and transparency log.
