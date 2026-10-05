"""Invariants on the rendered Kubernetes manifests of a cell.

kubeconform proves the manifests are well-formed Kubernetes. This proves they
say what the ADRs require, against the source of truth for each rule rather
than a copy of it:

configuration (ADR-0017 §2.1)
    Every ZTAX_ variable a container sets is one its binary recognises —
    parsed from backend/internal/platform/config/config.go for the binaries
    that call config.Load, and from os.Getenv calls in cmd/<binary>/main.go
    for the one that does not. An unknown ZTAX_ variable refuses to start, so
    this turns a crash-looping rollout into a CI failure. Every required one is
    present, and enableServiceLinks is off, because service links inject
    ZTAX_<SERVICE>_* names config.Load would refuse.

secrets (ADR-0017 §2.4, control 3)
    No credential value in a manifest, no envFrom, and no secretKeyRef except
    the one registered deviation (ztax-migrate's DSN; see
    infra/kubernetes/base/ztax-migrate-job.yaml).

hardening
    Non-root, read-only root filesystem, no privilege escalation, every
    capability dropped, RuntimeDefault seccomp, no Kubernetes API token,
    images pinned by digest.

egress (ADR-0006 §2.1)
    No allowance granted to a Go workload can reach a host in
    policy/egress/ai-provider-endpoints.json; vanilla NetworkPolicies carry no
    ipBlock; no Cilium allow names the world; Go workloads carry an explicit
    egressDeny of everything outside the cell.

Reads rendered YAML (kustomize build) from a file or stdin:

    kustomize build infra/kubernetes/overlays/euc1-dev-01 | python infra/tools/check_manifests.py -

Requires PyYAML (the one non-stdlib dependency in infra/tools, pinned by hash
in infra/tools/requirements.txt).
"""

from __future__ import annotations

import functools
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CONFIG_GO = ROOT / "backend" / "internal" / "platform" / "config" / "config.go"
CMD = ROOT / "backend" / "cmd"
PROVIDER_ENDPOINTS = ROOT / "policy" / "egress" / "ai-provider-endpoints.json"

GO_RUNTIME_LABEL = "ztax.zoikogroup.com/runtime"

# Which binary reads which variables. A container not listed here cannot be
# checked against its binary, and is refused rather than waved through.
CONFIG_LOAD_BINARIES = {"ztax-core", "ztax-outbox-relay"}
GETENV_BINARIES = {"ztax-migrate"}

# The registered exception to "no secretKeyRef" (ADR-0017 §2.4): container,
# variable. Anything else materialised from a Secret fails.
SECRET_REF_EXCEPTIONS = {("ztax-migrate", "ZTAX_MIGRATE_DATABASE_URL")}

CREDENTIAL_NAME = re.compile(r"(PASSWORD|SECRET|TOKEN|DSN|DATABASE_URL|PRIVATE_KEY|API_KEY)(?!_REF)")
DIGEST_IMAGE = re.compile(r"^[^@\s]+@sha256:[a-f0-9]{64}$")
WORKLOAD_KINDS = {"Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "Pod", "ReplicaSet"}


# --- sources of truth ----------------------------------------------------------

def config_load_variables() -> tuple[set[str], set[str], str]:
    """(known, required, local-secret prefix) from config.go's `known` table."""
    text = CONFIG_GO.read_text(encoding="utf-8")
    table = re.search(r"var known = map\[string\]struct \{.*?\}\{(.*?)\n\}", text, re.DOTALL)
    if not table:
        raise SystemExit("check_manifests: cannot find the `known` table in config.go")
    known, required = set(), set()
    for name, spec in re.findall(r'"(ZTAX_[A-Z0-9_]+)":\s*\{([^}]*)\}', table.group(1)):
        known.add(name)
        if "required: true" in spec:
            required.add(name)
    prefix = re.search(r'LocalSecretPrefix = Prefix \+ "([A-Z_]+)"', text)
    return known, required, "ZTAX_" + (prefix.group(1) if prefix else "LOCAL_SECRET_")


def getenv_variables(binary: str) -> set[str]:
    text = (CMD / binary / "main.go").read_text(encoding="utf-8")
    return set(re.findall(r'os\.(?:Getenv|LookupEnv)\("(ZTAX_[A-Z0-9_]+)"\)', text))


def provider_hosts() -> list[str]:
    return json.loads(PROVIDER_ENDPOINTS.read_text(encoding="utf-8"))["hosts"]


# --- glob intersection ---------------------------------------------------------

def globs_intersect(a: str, b: str) -> bool:
    """True if some hostname matches both patterns. '*' matches any run of
    characters, dots included — deliberately wider than Cilium's single-label
    '*', so this errs towards refusing."""
    a, b = a.lower().rstrip("."), b.lower().rstrip(".")

    @functools.lru_cache(maxsize=None)
    def f(i: int, j: int) -> bool:
        if i == len(a) and j == len(b):
            return True
        if i < len(a) and a[i] == "*":
            if f(i + 1, j) or (j < len(b) and f(i, j + 1)):
                return True
        if j < len(b) and b[j] == "*":
            if f(i, j + 1) or (i < len(a) and f(i + 1, j)):
                return True
        if i < len(a) and j < len(b) and a[i] != "*" and b[j] != "*" and a[i] == b[j]:
            return f(i + 1, j + 1)
        return False

    return f(0, 0)


# --- helpers -------------------------------------------------------------------

def pod_spec(doc: dict) -> dict | None:
    kind = doc.get("kind")
    spec = doc.get("spec") or {}
    if kind == "Pod":
        return spec
    if kind == "CronJob":
        return (((spec.get("jobTemplate") or {}).get("spec") or {}).get("template") or {}).get("spec")
    if kind in WORKLOAD_KINDS:
        return (spec.get("template") or {}).get("spec")
    return None


def pod_labels(doc: dict) -> dict:
    kind = doc.get("kind")
    spec = doc.get("spec") or {}
    if kind == "Pod":
        return (doc.get("metadata") or {}).get("labels") or {}
    if kind == "CronJob":
        spec = (spec.get("jobTemplate") or {}).get("spec") or {}
    return ((spec.get("template") or {}).get("metadata") or {}).get("labels") or {}


def name_of(doc: dict) -> str:
    return f"{doc.get('kind')}/{(doc.get('metadata') or {}).get('name')}"


def selects_go(selector: dict) -> bool:
    labels = (selector or {}).get("matchLabels") or {}
    return labels.get(GO_RUNTIME_LABEL) == "go" or labels.get("app.kubernetes.io/name") in CONFIG_LOAD_BINARIES | GETENV_BINARIES


# --- checks --------------------------------------------------------------------

def check_workload(doc: dict, known: set[str], required: set[str], local_prefix: str) -> list[str]:
    spec = pod_spec(doc)
    if spec is None:
        return []
    where = name_of(doc)
    problems = []

    if spec.get("automountServiceAccountToken") is not False:
        problems.append(f"{where}: automountServiceAccountToken must be false")
    if spec.get("enableServiceLinks") is not False:
        problems.append(f"{where}: enableServiceLinks must be false (service links inject ZTAX_-prefixed names config.Load refuses)")
    psc = spec.get("securityContext") or {}
    if psc.get("runAsNonRoot") is not True:
        problems.append(f"{where}: pod securityContext.runAsNonRoot must be true")
    if (psc.get("seccompProfile") or {}).get("type") != "RuntimeDefault":
        problems.append(f"{where}: pod seccompProfile must be RuntimeDefault")

    containers = (spec.get("containers") or []) + (spec.get("initContainers") or [])
    for c in containers:
        cname = c.get("name")
        at = f"{where} container {cname}"
        image = c.get("image", "")
        if not DIGEST_IMAGE.match(image):
            problems.append(f"{at}: image {image!r} is not pinned by digest")

        sc = c.get("securityContext") or {}
        if sc.get("readOnlyRootFilesystem") is not True:
            problems.append(f"{at}: readOnlyRootFilesystem must be true")
        if sc.get("allowPrivilegeEscalation") is not False:
            problems.append(f"{at}: allowPrivilegeEscalation must be false")
        if sc.get("privileged"):
            problems.append(f"{at}: privileged")
        if "ALL" not in ((sc.get("capabilities") or {}).get("drop") or []):
            problems.append(f"{at}: capabilities must drop ALL")
        if (sc.get("capabilities") or {}).get("add"):
            problems.append(f"{at}: adds capabilities")

        if c.get("envFrom"):
            problems.append(f"{at}: envFrom hides variable names from review and from this check")

        env = c.get("env") or []
        names = [e.get("name", "") for e in env]
        if cname in CONFIG_LOAD_BINARIES:
            allowed = known
        elif cname in GETENV_BINARIES:
            allowed = getenv_variables(cname)
        else:
            problems.append(f"{at}: no known binary for this container; add it to check_manifests.py with its variable source")
            continue

        for e in env:
            n = e.get("name", "")
            if n.startswith(local_prefix):
                problems.append(f"{at}: {n}: local:// secrets are development-only and refused in a cell")
            elif n.startswith("ZTAX_") and n not in allowed:
                problems.append(f"{at}: {n} is not a variable {cname} recognises; it would refuse to start")
            vf = e.get("valueFrom") or {}
            if "secretKeyRef" in vf and (cname, n) not in SECRET_REF_EXCEPTIONS:
                problems.append(f"{at}: {n} materialises a Secret into the environment (ADR-0017 §2.4)")
            if "value" in e and CREDENTIAL_NAME.search(n):
                problems.append(f"{at}: {n} carries a credential value; configuration names where a secret lives, never the secret")
            if n.endswith("_REF") and "value" in e and "://" not in str(e["value"]):
                problems.append(f"{at}: {n} is not a scheme://path reference")

        if cname in CONFIG_LOAD_BINARIES:
            missing = sorted(required - set(names))
            if missing:
                problems.append(f"{at}: required variables missing: {', '.join(missing)}")
            env_value = {e.get("name"): e.get("value") for e in env}
            if env_value.get("ZTAX_ENVIRONMENT") == "development":
                problems.append(f"{at}: ZTAX_ENVIRONMENT=development unlocks laptop-only affordances; a cell is never development")
            if env_value.get("ZTAX_AUTHORITATIVE") == "true":
                problems.append(f"{at}: ZTAX_AUTHORITATIVE=true is an A4 authorization act, not a manifest value")
    return problems


def check_service_account(doc: dict) -> list[str]:
    if doc.get("kind") == "ServiceAccount" and doc.get("automountServiceAccountToken") is not False:
        return [f"{name_of(doc)}: automountServiceAccountToken must be false"]
    return []


def check_network_policy(doc: dict) -> list[str]:
    if doc.get("kind") != "NetworkPolicy":
        return []
    problems = []
    spec = doc.get("spec") or {}
    for rule in (spec.get("egress") or []) + (spec.get("ingress") or []):
        for peer in (rule.get("to") or []) + (rule.get("from") or []):
            if "ipBlock" in peer:
                problems.append(f"{name_of(doc)}: ipBlock in a vanilla NetworkPolicy; the baseline is CIDR-free, cell CIDRs live in the overlay's Cilium policy")
    return problems


def check_cilium_policy(doc: dict, hosts: list[str]) -> tuple[list[str], bool]:
    """Problems, and whether this policy denies Go workloads everything
    outside the cell."""
    if doc.get("kind") not in {"CiliumNetworkPolicy", "CiliumClusterwideNetworkPolicy"}:
        return [], False
    specs = [doc["spec"]] if doc.get("spec") else doc.get("specs") or []
    problems, denies_world = [], False
    where = name_of(doc)
    for spec in specs:
        if not selects_go(spec.get("endpointSelector") or {}):
            continue
        for rule in spec.get("egress") or []:
            ents = set(rule.get("toEntities") or [])
            if ents & {"world", "all", "world-ipv4", "world-ipv6"}:
                problems.append(f"{where}: egress to entity {sorted(ents)} for a Go workload")
            for c in rule.get("toCIDR") or []:
                if c in {"0.0.0.0/0", "::/0"}:
                    problems.append(f"{where}: egress to {c} for a Go workload")
            for c in rule.get("toCIDRSet") or []:
                if c.get("cidr") in {"0.0.0.0/0", "::/0"}:
                    problems.append(f"{where}: egress to {c.get('cidr')} for a Go workload")
            patterns = []
            for f in rule.get("toFQDNs") or []:
                patterns.append(f.get("matchName") or f.get("matchPattern") or "")
            for port in rule.get("toPorts") or []:
                for d in ((port.get("rules") or {}).get("dns") or []):
                    patterns.append(d.get("matchName") or d.get("matchPattern") or "")
            for p in patterns:
                for h in hosts:
                    if globs_intersect(p, h):
                        problems.append(f"{where}: Go workload allowance {p!r} can reach model provider {h!r} (ADR-0006 §2.1)")
        for rule in spec.get("egressDeny") or []:
            for c in rule.get("toCIDRSet") or []:
                if c.get("cidr") == "0.0.0.0/0":
                    denies_world = True
            if set(rule.get("toEntities") or []) & {"world", "all"}:
                denies_world = True
    return problems, denies_world


def check(docs: list[dict]) -> list[str]:
    known, required, local_prefix = config_load_variables()
    hosts = provider_hosts()
    problems: list[str] = []
    has_cilium = any(d.get("kind", "").startswith("Cilium") for d in docs)
    go_world_deny = False
    go_workloads = 0

    for doc in docs:
        if not isinstance(doc, dict):
            continue
        problems += check_workload(doc, known, required, local_prefix)
        problems += check_service_account(doc)
        problems += check_network_policy(doc)
        p, denies = check_cilium_policy(doc, hosts)
        problems += p
        go_world_deny = go_world_deny or denies
        if pod_spec(doc) is not None and pod_labels(doc).get(GO_RUNTIME_LABEL) == "go":
            go_workloads += 1

    kinds = {d.get("kind") for d in docs if isinstance(d, dict)}
    if "NetworkPolicy" in kinds and not any(
        d.get("kind") == "NetworkPolicy" and (d.get("spec") or {}).get("podSelector") == {}
        and set((d.get("spec") or {}).get("policyTypes") or []) == {"Ingress", "Egress"}
        and not (d.get("spec") or {}).get("ingress") and not (d.get("spec") or {}).get("egress")
        for d in docs if isinstance(d, dict)
    ):
        problems.append("no default-deny NetworkPolicy (empty podSelector, Ingress+Egress, no rules)")
    if has_cilium and go_workloads and not go_world_deny:
        problems.append("Go workloads have no explicit egressDeny of 0.0.0.0/0 outside the cell (ADR-0006 §2.1)")
    return problems


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    import yaml  # imported here so the checks themselves are testable without it

    text = sys.stdin.read() if argv[1] == "-" else Path(argv[1]).read_text(encoding="utf-8")
    docs = [d for d in yaml.safe_load_all(text) if d]
    problems = check(docs)
    if problems:
        print("check_manifests: FAILED", file=sys.stderr)
        for p in problems:
            print(f"  - {p}", file=sys.stderr)
        return 1
    print(f"check_manifests: ok - {len(docs)} resources; configuration, secrets, hardening and egress invariants hold")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
