# Security policy

Policy-as-code for the regional cells. Approver: CISO ([ADR-0017](../../adr/ADR-0017-configuration-secrets-and-keys.md) §1 — policy is "what is permitted", reviewed as a change, on its own path; [ADR-0007](../../adr/ADR-0007-repository-topology-and-go-module-layout.md) §2.1 reserves this directory for it).

| Path | What |
|---|---|
| `admission/cell-image-origin.yaml` | Images in a cell namespace come from `ghcr.io/zoikogroup/zoiko-tax/`, by digest |
| `admission/cell-release-signature.yaml` | …and are signed keylessly by this repository's release workflow, with a signed SLSA provenance attestation |
| `admission/tests/offline/` | Offline proof of the origin rule (`kyverno test`) |
| `admission/tests/online/` | Proof of the signature rule against real images, the real registry and Rekor |
| `egress/ai-provider-endpoints.json` | The model-provider hosts no Go workload may reach (ADR-0006 §2.1). One list, read by the checks in `infra/tools/` and nowhere copied |

## Signed-artifact admission

Build Plan W1 lane B requires "signature, provenance and certification admission policy in regional clusters", and the W1 exit gate requires it **proven in a regional cell: no unsigned workload admits**. A namespace opts in with `ztax.zoikogroup.com/admission: signed-only` (the cell namespace does, in `infra/kubernetes/base/namespace.yaml`). Inside it, an image is admitted only if:

1. it is under `ghcr.io/zoikogroup/zoiko-tax/` and referenced by digest — a tag can be re-pointed after the check;
2. its Sigstore signature's certificate was issued by `https://token.actions.githubusercontent.com` to `https://github.com/ZoikoGroup/Zoiko-Tax/.github/workflows/release.yml@refs/tags/v…`, logged in Rekor — exactly what `release.yml`'s `cosign sign` produces, and nothing else: the same image signed by another workflow, branch, repository or key is refused;
3. it carries a SLSA provenance attestation signed by that same identity whose builder is a GitHub Actions run of this repository.

Both policies fail closed: `failurePolicy: Fail`, so a webhook outage refuses rather than admits. Autogen applies them to Deployments, Jobs and the other controllers, so a refusal is reported on the object someone applied, not on a ReplicaSet that silently never makes a pod.

### Why Kyverno

The choice was between Sigstore's policy-controller (`ClusterImagePolicy`) and Kyverno.

- **It can be proven without a cluster.** `kyverno test` evaluates the origin rule offline and deterministically in CI, and evaluates the signature rule against real images through the registry and Rekor. policy-controller's tester needs a registry for every case and has no equivalent test-file format. The exit gate is a proof, so the engine that can carry the proof wins.
- **One engine for both halves.** The origin rule is plain validation and the signature rule is image verification; policy-controller does only the second, so the first would need a second admission controller.
- **CEL policies, not the deprecated API.** Kyverno 1.19 deprecates `kyverno.io/v1` `ClusterPolicy`; these are `policies.kyverno.io/v1` `ValidatingPolicy` and `ImageValidatingPolicy`, which the CLI and the CRD schemas both validate.

The cost is a broader controller with more privileges than policy-controller. Kyverno is installed by the cluster bootstrap in its own namespace, and its own images are outside the signed-only namespaces it polices.

### How "no unsigned workload admits" is proven

| Proof | What it shows | Where |
|---|---|---|
| `kyverno test policy/admission/tests/offline` | A public image, a tag, an unsigned init container and a look-alike repository are refused; a digest-pinned release path passes the origin rule | CI, offline |
| `kyverno test policy/admission/tests/online` | A real unsigned image (busybox) is refused; `gcr.io/distroless/static-debian12` — **genuinely signed**, by Google's distroless identity, and the base image `ztax-core` is built from — is refused | CI, network |
| Positive control (manual) | With the attestor swapped for the distroless identity (`issuer: https://accounts.google.com`, `subject: keyless@distroless.iam.gserviceaccount.com`) the same signature check **passes** on that image — so the refusal is the identity binding, not a verifier that refuses everything | Run once while writing the policy; repeat when changing the attestor |
| Live cell | In a cell with Kyverno and these policies installed, apply `admission/tests/online/resources.yaml` with a restricted-compliant security context (so Pod Security does not refuse it first): each pod is denied with the policies' messages. Apply the released `ztax-core` Deployment: admitted once rule 3's evidence exists | W1 exit gate, cell conformance suite |

### Where rule 3's evidence comes from

`docker/build-push-action` with `provenance: mode=max` attaches BuildKit's SLSA provenance to the image index **unsigned**, and an unsigned attestation proves nothing at admission — anyone who can push can attach one. `release.yml`'s `attest the provenance` step, immediately after `sign the image`, re-publishes that provenance as a keyless `cosign attest` under the same workflow identity, which is the attestation rule 3 verifies. A release built before that step existed carries no signed attestation and is refused, including when its image signature is correct — a gate whose evidence does not exist fails closed.

`--type slsaprovenance` is the `https://slsa.dev/provenance/v0.2` predicate BuildKit emits by default, which is what the policy expects. Two more things the pipeline must do for the cell to run at all: publish `ztax-migrate` (the Dockerfile has the stage; the matrix does not build it) and `ztax-outbox-relay` (no Dockerfile stage yet). Until then admission refuses both, which is the correct outcome for images nothing has signed.

`release.yml` pins `sigstore/cosign-installer` at a commit whose default is cosign v2.5.2, which stores signatures as `.sig` tags beside the image — the format the verifier reads. A move to cosign v3's bundle format must be checked against this policy before it ships.

## Verification

```bash
kyverno test policy/admission/tests/offline    # no network
kyverno test policy/admission/tests/online     # needs the registries and Rekor
```

Both run in [`.github/workflows/infra.yml`](../.github/workflows/infra.yml).
