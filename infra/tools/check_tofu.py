"""Static checks on the OpenTofu tree that `tofu validate` cannot express.

Two properties, each of which fails silently if broken:

contract
    Every implementation of the regional cell under
    infra/tofu/modules/regional-cell/<provider>/ declares exactly the input
    variables and outputs listed in modules/regional-cell/contract.json. That is
    what makes the cell interface provider-neutral in fact rather than in a
    README: a second provider either implements the same contract or fails here.

residency
    Nothing inside a cell implementation can point a resource at another region
    (ADR-0009 §2.6). The AWS provider (v6+) accepts a per-resource ``region``
    argument, so a single line could quietly build part of a cell elsewhere;
    this refuses that argument, any literal region identifier, multi-Region
    keys, replica blocks and replication configuration, and any VPC endpoint
    for a model-provider service (ADR-0006 §2.1 — an endpoint inside the cell
    would put the provider inside the CIDR Go workloads may reach).

Stdlib only. Run from the repository root:

    python infra/tools/check_tofu.py
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CELL_MODULES = ROOT / "infra" / "tofu" / "modules" / "regional-cell"
PROVIDER_ENDPOINTS = ROOT / "policy" / "egress" / "ai-provider-endpoints.json"

# A provider region identifier: eu-central-1, us-gov-west-1, ap-southeast-2.
REGION_LITERAL = re.compile(r"\b[a-z]{2}(?:-gov|-iso[a-z]?)?-(?:central|north|south|east|west|northeast|northwest|southeast|southwest|mid)-\d\b")
REGION_ARGUMENT = re.compile(r"^\s*region\s*=", re.MULTILINE)
DECLARATION = re.compile(r'^(variable|output)\s+"([^"]+)"', re.MULTILINE)

# Constructs that copy or place cell state outside the cell's region.
FORBIDDEN_CONSTRUCTS = {
    re.compile(r"^\s*multi_region\s*=\s*true", re.MULTILINE): "a multi-Region key replicates key material out of the cell's region",
    re.compile(r"^\s*replica\s*\{", re.MULTILINE): "a replica block copies cell state to another region",
    re.compile(r'resource\s+"aws_s3_bucket_replication_configuration"'): "bucket replication copies evidence out of the cell",
    re.compile(r'resource\s+"aws_db_instance_automated_backups_replication"'): "backup replication copies the cell database to another region",
    re.compile(r'resource\s+"aws_kms_replica_key"'): "a replica key places cell key material in another region",
    re.compile(r"^\s*replicate_source_db\s*=", re.MULTILINE): "a cross-region read replica is a resident copy elsewhere",
    re.compile(r"^\s*provider\s*=\s*aws\.", re.MULTILINE): "an aliased provider can be configured for any region",
}


def strip_comments(text: str) -> str:
    """Drop # and // line comments, so prose can name regions and constructs."""
    out = []
    for line in text.splitlines():
        in_string = False
        cut = len(line)
        i = 0
        while i < len(line):
            ch = line[i]
            if ch == '"' and (i == 0 or line[i - 1] != "\\"):
                in_string = not in_string
            elif not in_string and (ch == "#" or line.startswith("//", i)):
                cut = i
                break
            i += 1
        out.append(line[:cut])
    return "\n".join(out)


def module_sources(impl: Path) -> dict[Path, str]:
    """The .tf files of one implementation, excluding its tests."""
    return {p: strip_comments(p.read_text(encoding="utf-8")) for p in sorted(impl.glob("*.tf"))}


def check_contract(impl: Path, contract: dict) -> list[str]:
    declared: dict[str, set[str]] = {"variable": set(), "output": set()}
    for text in module_sources(impl).values():
        for kind, name in DECLARATION.findall(text):
            declared[kind].add(name)

    problems = []
    for kind, key in (("variable", "variables"), ("output", "outputs")):
        want = set(contract[key])
        missing = sorted(want - declared[kind])
        extra = sorted(declared[kind] - want)
        if missing:
            problems.append(f"{impl.name}: missing contract {kind}s: {', '.join(missing)}")
        if extra:
            problems.append(f"{impl.name}: {kind}s outside the contract: {', '.join(extra)} (add them to contract.json for every provider, or remove them)")
    return problems


def check_residency(impl: Path, forbidden_services: list[str]) -> list[str]:
    problems = []
    for path, text in module_sources(impl).items():
        rel = path.relative_to(ROOT).as_posix() if path.is_relative_to(ROOT) else path.as_posix()
        for m in REGION_ARGUMENT.finditer(text):
            line = text.count("\n", 0, m.start()) + 1
            problems.append(f"{rel}:{line}: per-resource `region` argument; a cell builds in var.region only")
        for m in REGION_LITERAL.finditer(text):
            line = text.count("\n", 0, m.start()) + 1
            problems.append(f"{rel}:{line}: region literal {m.group(0)!r}; use var.region")
        for pattern, why in FORBIDDEN_CONSTRUCTS.items():
            for m in pattern.finditer(text):
                line = text.count("\n", 0, m.start()) + 1
                problems.append(f"{rel}:{line}: {why}")
        for service in forbidden_services:
            if re.search(r'"' + re.escape(service) + r'"', text):
                problems.append(f"{rel}: VPC endpoint for model-provider service {service!r} (ADR-0006 §2.1)")
    return problems


def implementations() -> list[Path]:
    return sorted(p for p in CELL_MODULES.iterdir() if p.is_dir())


def main() -> int:
    contract = json.loads((CELL_MODULES / "contract.json").read_text(encoding="utf-8"))
    forbidden = json.loads(PROVIDER_ENDPOINTS.read_text(encoding="utf-8"))["aws_vpc_endpoint_services"]

    impls = implementations()
    if not impls:
        print("check_tofu: no regional-cell implementation found", file=sys.stderr)
        return 1

    problems: list[str] = []
    for impl in impls:
        problems += check_contract(impl, contract)
        problems += check_residency(impl, forbidden)

    if problems:
        print("check_tofu: FAILED", file=sys.stderr)
        for p in problems:
            print(f"  - {p}", file=sys.stderr)
        return 1

    names = ", ".join(i.name for i in impls)
    print(f"check_tofu: ok - {len(impls)} cell implementation(s) [{names}] honour the contract "
          f"({len(contract['variables'])} variables, {len(contract['outputs'])} outputs) and reference no other region")
    return 0


if __name__ == "__main__":
    sys.exit(main())
