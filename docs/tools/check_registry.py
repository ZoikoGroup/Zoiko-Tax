#!/usr/bin/env python3
"""The register gate — ZTAX-GOV-001 §9.4.

    CI SHALL reject unregistered primary document IDs, broken requirement
    references, missing owners, expired mandatory reviews, duplicate IDs and
    production requirements without a verification reference.

That sentence is the specification for this file, and every check below names
the clause of it that it discharges. The schemas in docs/schema/ enforce shape;
this enforces the rules that span files — which is where a register goes wrong,
because each file can be individually well-formed while the set disagrees.

Usage:

    python docs/tools/check_registry.py                  # the gate
    python docs/tools/check_registry.py --write-index    # regenerate docs/ESTATE.md
    python docs/tools/check_registry.py --rehash-drafts  # re-register DRAFT hashes

The gate exits non-zero and prints one line per finding. It never modifies a
file unless asked to, and it only ever rewrites a hash for a DRAFT: a document
past DRAFT has been approved at the hash it carries, and changing that hash is a
new registration, not a tool run.
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import re
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

import jsonschema
import yaml

# Primary document IDs are ZTAX-<DOMAIN>-<NNN> (GOV-001 §4.1). The look-arounds
# keep a waiver ID (ZTAX-WVR-2026-0041) or a country pack
# (ZTAX-CP-GBR-VAT-v1.2) from reading as a document.
DOC_REF = re.compile(r"(?<![A-Za-z0-9-])ZTAX-([A-Z][A-Z0-9]*)-([0-9]{3})(?![0-9A-Za-z])")
REQ_REF = re.compile(r"(?<![A-Za-z0-9-])ZTAX-([A-Z][A-Z0-9]*)-REQ-([0-9]{4})(?![0-9])")

# Statuses that carry an obligation to be reviewed. PROPOSED reserves an ID and
# nothing more; SUPERSEDED and WITHDRAWN are retained, not reviewed.
LIVE = {"DRAFT", "BASELINED-FOR-BUILD", "APPROVED", "EFFECTIVE"}
# Statuses that assert an approval happened. An approval with nobody named as
# approver is not one.
APPROVED = {"BASELINED-FOR-BUILD", "APPROVED", "EFFECTIVE"}

# Paths the reference scan skips. vendor/ and node_modules/ are upstream code;
# the checker's own test fixtures deliberately contain unregistered IDs.
SKIP_PREFIXES = ("backend/vendor/", "docs/tools/tests/")
SKIP_PARTS = ("node_modules",)

INDEX = "docs/ESTATE.md"


@dataclass
class Findings:
    errors: list[str] = field(default_factory=list)

    def add(self, where: str, message: str) -> None:
        self.errors.append(f"{where}: {message}")


def load_yaml(path: Path) -> object:
    with path.open(encoding="utf-8") as f:
        return yaml.safe_load(f)


def load_schema(root: Path, name: str) -> dict:
    with (root / "docs" / "schema" / name).open(encoding="utf-8") as f:
        return json.load(f)


def file_hash(path: Path) -> str:
    return "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()


def parse_date(s: str) -> dt.date:
    return dt.date.fromisoformat(s)


def validate_shape(doc: object, schema: dict, where: str, out: Findings) -> bool:
    validator = jsonschema.Draft202012Validator(schema)
    ok = True
    for err in sorted(validator.iter_errors(doc), key=lambda e: list(e.absolute_path)):
        path = "/".join(str(p) for p in err.absolute_path) or "(root)"
        out.add(where, f"schema: {path}: {err.message}")
        ok = False
    return ok


# ---------------------------------------------------------------------------
# the checks
# ---------------------------------------------------------------------------


def check_documents(root: Path, register: dict, today: dt.date, out: Findings) -> dict[str, dict]:
    where = "docs/register.yaml"
    docs: dict[str, dict] = {}

    # "duplicate IDs"
    for d in register["documents"]:
        did = d["document_id"]
        if did in docs:
            out.add(where, f"{did} is registered twice")
        docs[did] = d

    open_gaps: dict[str, dict] = {}
    for g in register["gaps"]:
        if g["document_id"] not in docs:
            out.add(where, f"gap names {g['document_id']}, which is not registered")
            continue
        if parse_date(g["due"]) < today:
            # A gap past its date is no longer a registered plan to close it.
            # It is the incompleteness itself, and it fails.
            out.add(where, f"gap for {g['document_id']} was due {g['due']} and is still open")
            continue
        open_gaps[g["document_id"]] = g

    for did, d in docs.items():
        gap = did in open_gaps

        # "missing owners" — the schema requires a non-empty string; this is
        # the belt to its braces, and catches an owner of whitespace.
        if not d["owner"].strip():
            out.add(where, f"{did} has no owner")

        # "expired mandatory reviews"
        if d["status"] in LIVE:
            if d["review_due"] is None:
                if not gap:
                    out.add(where, f"{did} is {d['status']} and has no review date")
            elif parse_date(d["review_due"]) < today:
                out.add(where, f"{did} review was due {d['review_due']}")

        # GOV-001 §4.4: EFFECTIVE means "effective date, version and hash are
        # registered", and every approved status means somebody approved it.
        # Missing any of these is a claim the register cannot support — unless
        # an open, in-date gap says why.
        if d["status"] in APPROVED and not d["approvers"] and not gap:
            out.add(where, f"{did} is {d['status']} with no approvers named")
        if d["status"] == "EFFECTIVE" and not gap:
            for key in ("version", "publication_hash", "effective_date"):
                if d[key] is None:
                    out.add(where, f"{did} is EFFECTIVE with no {key}")

        # The hash is the register's claim about which bytes were approved.
        src = d["source_path"]
        if src is not None:
            path = root / src
            if not path.is_file():
                out.add(where, f"{did} source_path {src} does not exist")
            elif d["publication_hash"] is None:
                out.add(where, f"{did} has a source_path and no publication_hash")
            elif file_hash(path) != d["publication_hash"]:
                hint = " (run --rehash-drafts)" if d["status"] in {"PROPOSED", "DRAFT"} else \
                    " — this document is past DRAFT, so a changed source is a new version"
                out.add(where, f"{did} publication_hash does not match {src}{hint}")
        elif d["publication_hash"] is not None:
            out.add(where, f"{did} registers a hash with no source_path to verify it against")

        # References inside the register are references too.
        for dep in d["dependencies"]:
            if dep not in docs:
                out.add(where, f"{did} depends on {dep}, which is not registered")
            if dep == did:
                out.add(where, f"{did} depends on itself")
        sup = d["supersedes"]
        if sup is not None:
            if sup not in docs:
                out.add(where, f"{did} supersedes {sup}, which is not registered")
            elif docs[sup]["status"] != "SUPERSEDED":
                out.add(where, f"{did} supersedes {sup}, which is still {docs[sup]['status']}")

    return docs


def check_requirements(root: Path, reqs: dict, docs: dict[str, dict], out: Findings) -> dict[str, dict]:
    where = "docs/requirements.yaml"
    seen: dict[str, dict] = {}
    for r in reqs["requirements"]:
        rid = r["id"]
        # "duplicate IDs"
        if rid in seen:
            out.add(where, f"{rid} is registered twice")
        seen[rid] = r

        # "broken requirement references"
        doc = docs.get(r["document_id"])
        if doc is None:
            out.add(where, f"{rid} belongs to {r['document_id']}, which is not registered")
            continue
        domain = rid.split("-")[1]
        if r["document_id"].split("-")[1] != domain:
            out.add(where, f"{rid} is filed under {r['document_id']}; the ID names domain {domain}")
        if r["version"] != doc["version"]:
            out.add(where, f"{rid} cites {r['document_id']} version {r['version']}, "
                           f"the register holds {doc['version']}")

        if not r["owner"].strip():
            out.add(where, f"{rid} has no owner")

        # "production requirements without a verification reference"
        if r["status"] == "EFFECTIVE":
            if r["verification_ref"] is None:
                out.add(where, f"{rid} is EFFECTIVE with no verification_ref")
            if doc["status"] != "EFFECTIVE":
                out.add(where, f"{rid} is EFFECTIVE but {r['document_id']} is {doc['status']}")
        if r["status"] == "BASELINED" and doc["status"] not in APPROVED:
            out.add(where, f"{rid} is BASELINED but {r['document_id']} is {doc['status']}")

        # A verification_ref that looks like a path must resolve. An opaque
        # control identifier cannot be checked here and is not pretended to be.
        ref = r["verification_ref"]
        if ref is not None and "/" in ref:
            path = ref.split("::", 1)[0]
            if not (root / path).exists():
                out.add(where, f"{rid} verification_ref {path} does not exist")

        if r["effective_from"] and r["effective_to"] and r["effective_to"] < r["effective_from"]:
            out.add(where, f"{rid} ends before it begins")
    return seen


def check_waivers(waivers: dict, reqs: dict[str, dict], now: dt.datetime, out: Findings) -> None:
    where = "docs/waivers.yaml"
    seen: set[str] = set()
    for w in waivers["waivers"]:
        wid = w["waiver_id"]
        if wid in seen:
            out.add(where, f"{wid} is registered twice")
        seen.add(wid)
        if w["requirement_id"] not in reqs:
            out.add(where, f"{wid} waives {w['requirement_id']}, which is not registered")
        expires = dt.datetime.strptime(w["expires_at"], "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=dt.UTC)
        if expires < now:
            out.add(where, f"{wid} expired at {w['expires_at']}")
        # "prohibition on self-approval where independent control is required"
        if all(a == w["risk_owner"] for a in w["approvers"]):
            out.add(where, f"{wid} is approved only by its own risk owner")


def tracked_files(root: Path) -> list[Path]:
    try:
        listing = subprocess.run(
            # Untracked files too, so a reference in a file not yet committed
            # fails locally rather than first in CI. Ignored files are skipped.
            ["git", "-C", str(root), "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
            check=True, capture_output=True,
        ).stdout.decode("utf-8")
        names = [n for n in listing.split("\0") if n]
    except (OSError, subprocess.CalledProcessError):
        # Not a checkout (a test fixture, an exported tarball). Walk instead.
        names = [p.relative_to(root).as_posix() for p in root.rglob("*") if p.is_file()]
    out = []
    for n in names:
        if n.startswith(SKIP_PREFIXES) or any(part in SKIP_PARTS for part in n.split("/")):
            continue
        out.append(root / n)
    return out


def check_references(root: Path, docs: dict[str, dict], reqs: dict[str, dict], out: Findings) -> None:
    """"unregistered primary document IDs" and "broken requirement references"
    anywhere in the tree, not only in the register."""
    for path in tracked_files(root):
        try:
            data = path.read_bytes()
        except OSError:
            continue
        if b"\0" in data[:8192]:
            continue  # binary; the docx sources are registered by hash instead
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError:
            continue
        rel = path.relative_to(root).as_posix()
        for lineno, line in enumerate(text.splitlines(), start=1):
            if "ZTAX-" not in line:
                continue
            for m in DOC_REF.finditer(line):
                did = m.group(0)
                if did not in docs:
                    out.add(f"{rel}:{lineno}", f"{did} is not a registered document")
            for m in REQ_REF.finditer(line):
                rid = m.group(0)
                if rid not in reqs:
                    out.add(f"{rel}:{lineno}", f"{rid} is not a registered requirement")


# ---------------------------------------------------------------------------
# the generated index
# ---------------------------------------------------------------------------


def render_index(register: dict, reqs: dict) -> str:
    """GOV-001 §19: the register SHALL generate the human-readable estate index.

    Deterministic by construction — no generation timestamp — so the drift
    check can compare bytes."""
    counts: dict[str, dict[str, int]] = {}
    for r in reqs["requirements"]:
        c = counts.setdefault(r["document_id"], {})
        c[r["status"]] = c.get(r["status"], 0) + 1

    lines = [
        "# ZoikoTax documentation estate",
        "",
        "Generated from [`register.yaml`](register.yaml) and [`requirements.yaml`](requirements.yaml) "
        "by `docs/tools/check_registry.py --write-index`. Do not edit by hand — CI fails if this file "
        "and the register disagree.",
        "",
        "| Document | Title | Class | Tranche | Version | Status | Owner | Review due | Requirements |",
        "|---|---|---|---|---|---|---|---|---|",
    ]
    for d in register["documents"]:
        c = counts.get(d["document_id"], {})
        req = " · ".join(f"{n} {s}" for s, n in sorted(c.items())) or "—"
        title = d["title"].replace("|", "\\|")
        lines.append(
            f"| `{d['document_id']}` | {title} | {d['class']} | {d['tranche']} | "
            f"{d['version'] or '—'} | {d['status']} | {d['owner']} | {d['review_due'] or '—'} | {req} |"
        )

    by_status: dict[str, int] = {}
    for d in register["documents"]:
        by_status[d["status"]] = by_status.get(d["status"], 0) + 1
    lines += [
        "",
        "## Summary",
        "",
        f"{len(register['documents'])} documents: "
        + ", ".join(f"{n} {s}" for s, n in sorted(by_status.items()))
        + f". {len(reqs['requirements'])} registered requirements.",
        "",
    ]

    if register["gaps"]:
        lines += ["## Open gaps", "", "| Document | Kind | Owner | Due | Issue |", "|---|---|---|---|---|"]
        for g in register["gaps"]:
            lines.append(f"| `{g['document_id']}` | {g['kind']} | {g['owner']} | {g['due']} | {g['issue']} |")
        lines.append("")
    return "\n".join(lines)


def rehash_drafts(root: Path) -> int:
    """Rewrite publication_hash for DRAFT and PROPOSED documents in place.

    A textual edit rather than a YAML round-trip, so the file's comments and
    layout survive and the diff is exactly the changed hashes."""
    path = root / "docs" / "register.yaml"
    register = load_yaml(path)
    text = path.read_text(encoding="utf-8")
    changed = 0
    for d in register["documents"]:
        if d["status"] not in {"DRAFT", "PROPOSED"} or d["source_path"] is None:
            continue
        src = root / d["source_path"]
        if not src.is_file():
            continue
        new = file_hash(src)
        old = d["publication_hash"]
        if old == new:
            continue
        entry = re.compile(rf"^\s*- document_id: {re.escape(d['document_id'])}\s*$", re.M)
        m = entry.search(text)
        if m is None:
            raise ValueError(f"register.yaml: cannot locate the entry for {d['document_id']}")
        nxt = re.compile(r"^\s*- document_id: ", re.M).search(text, m.end())
        start, end = m.start(), (nxt.start() if nxt else len(text))
        block = text[start:end]
        replaced = re.sub(r"^(\s*publication_hash: ).*$", lambda g: f'{g.group(1)}"{new}"', block, count=1, flags=re.M)
        text = text[:start] + replaced + text[end:]
        changed += 1
    path.write_text(text, encoding="utf-8", newline="\n")
    return changed


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


def run(root: Path, today: dt.date, now: dt.datetime, write_index: bool) -> Findings:
    out = Findings()
    register = load_yaml(root / "docs" / "register.yaml")
    requirements = load_yaml(root / "docs" / "requirements.yaml")
    waivers = load_yaml(root / "docs" / "waivers.yaml")

    shapes_ok = all([
        validate_shape(register, load_schema(root, "register.schema.json"), "docs/register.yaml", out),
        validate_shape(requirements, load_schema(root, "requirements.schema.json"), "docs/requirements.yaml", out),
        validate_shape(waivers, load_schema(root, "waivers.schema.json"), "docs/waivers.yaml", out),
    ])
    if not shapes_ok:
        # The cross-file rules assume the shapes hold; running them over a
        # malformed file produces noise that buries the actual finding.
        return out

    docs = check_documents(root, register, today, out)
    reqs = check_requirements(root, requirements, docs, out)
    check_waivers(waivers, reqs, now, out)
    check_references(root, docs, reqs, out)

    index = render_index(register, requirements)
    index_path = root / INDEX
    if write_index:
        index_path.write_text(index, encoding="utf-8", newline="\n")
    elif not index_path.is_file() or index_path.read_text(encoding="utf-8") != index:
        out.add(INDEX, "is stale or hand-edited; run --write-index and commit")
    return out


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2],
                        help="repository root (default: inferred from this file)")
    parser.add_argument("--today", type=dt.date.fromisoformat, default=None,
                        help="judge review and expiry dates as of this date (default: today, UTC)")
    parser.add_argument("--write-index", action="store_true", help=f"regenerate {INDEX}")
    parser.add_argument("--rehash-drafts", action="store_true",
                        help="re-register publication hashes for DRAFT and PROPOSED documents")
    args = parser.parse_args(argv)

    root: Path = args.root.resolve()
    if args.rehash_drafts:
        n = rehash_drafts(root)
        print(f"re-registered {n} DRAFT hash(es)")

    now = dt.datetime.now(dt.UTC)
    today = args.today or now.date()
    if args.today:
        now = dt.datetime.combine(args.today, dt.time(0, 0), tzinfo=dt.UTC)

    findings = run(root, today, now, args.write_index)
    for e in findings.errors:
        print(e)
    if findings.errors:
        print(f"\nregister gate: {len(findings.errors)} finding(s)", file=sys.stderr)
        return 1
    print("register gate: green")
    return 0


if __name__ == "__main__":
    sys.exit(main())
