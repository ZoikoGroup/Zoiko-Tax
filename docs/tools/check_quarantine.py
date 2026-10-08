#!/usr/bin/env python3
"""The flaky-test quarantine gate — ADR-0018 §2.12, control 6; QA-REQ-0102 to
QA-REQ-0105.

    "Flaky tests are quarantined and fixed, never retried — in a
    certification chain, a retry that turns red into green is destroyed
    evidence." (ADR-0018 §2.12)

A flake is never rerun until green in this estate; it is quarantined here
instead, with an owner, a defect, a reason, an expiry and an assessed
coverage impact (QA-REQ-0103, QA-REQ-0104). Quarantining the sole
verification of a requirement is itself a gate failure (QA-REQ-0105): a
requirement with no live verification is not a quieter requirement, it is an
unverified one.

This file follows the conventions of docs/tools/check_registry.py: the same
YAML loading, the same jsonschema shape validation before any cross-file
rule runs, the same `Findings.add(where, message)` error format, the same
`--today` injection point for testing, and the same exit-code contract
(0 and "quarantine gate: green" on success; 1 and one printed line per
finding otherwise).

Usage:

    python docs/tools/check_quarantine.py
    python docs/tools/check_quarantine.py --today 2026-12-01
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import re
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

import jsonschema
import yaml

QUARANTINE_ID = re.compile(r"^ZTAX-QUAR-[0-9]{4}-[0-9]{4}$")
MARKER = re.compile(r"QUARANTINE:\s*(ZTAX-QUAR-[0-9]{4}-[0-9]{4})")

# A marker is only a quarantine if the line that carries it is itself the
# skip call or decorator — a comment near a skip, or a marker with no skip
# at all, does not stop the test from running. One alternative per real
# skip form actually used by a toolchain under TEST_PREFIXES:
#   Go (backend/, sdk/go):            t.Skip(/t.Skipf(/t.SkipNow(
#   Python (intelligence/tests, sdk/python): pytest.skip(, pytest.mark.skip(,
#                                      unittest.skip(, .skipTest(
#   JavaScript/node:test (contracts/, sdk/typescript): test.skip(, it.skip(,
#                                      describe.skip(, or the inline option
#                                      form test(name, { skip: "..." }, fn)
#                                      -- confirmed against this repo's
#                                      actual usage: contracts/tools/
#                                      lint.test.mjs and sdk/typescript/
#                                      test/client.test.js both call bare
#                                      test() from "node:test", not
#                                      describe/it, and neither has a skip
#                                      yet, so it.skip(/describe.skip( have
#                                      no existing precedent here but are
#                                      included for the node:test/Mocha/Jest
#                                      convention generally.
#   Java/Kotlin JUnit 5 (sdk/java):    @Disabled( -- confirmed sdk/java uses
#                                      JUnit 5 Jupiter (pom.xml
#                                      junit.version 5.14.4, `import
#                                      org.junit.jupiter.api.Test`), not
#                                      JUnit 4, so @Disabled is correct, not
#                                      @Ignore. No @Disabled used yet.
#   .NET xUnit (sdk/dotnet):           Skip = " -- confirmed sdk/dotnet uses
#                                      xUnit [Fact(DisplayName = "...")]
#                                      throughout; no Skip = "..." used yet.
SKIP_CALL = re.compile(
    r"\bt\.Skip(f|Now)?\("
    r"|\bpytest\.mark\.skip\("
    r"|\bpytest\.skip\("
    r"|\bunittest\.skip\("
    r"|\.skipTest\("
    r"|\btest\.skip\("
    r"|\bit\.skip\("
    r"|\bdescribe\.skip\("
    r"|\{\s*skip\s*:\s*[\"']"
    r"|@Disabled\("
    r"|\bSkip\s*=\s*[\"']"
)


def _is_comment_line(line: str) -> bool:
    """A commented-out skip call is not a skip call: the test still runs."""
    stripped = line.strip()
    return stripped.startswith("//") or stripped.startswith("#")


# Where a quarantine skip marker is looked for. contracts/ was added after
# confirming it has a real test entry point (package.json's "test" script
# runs `node --test tools/lint.test.mjs`); frontend/ was checked and has no
# test files today, so it is not included. docs/tools/tests/ is
# deliberately NOT included, for the identical reason check_registry.py's
# own check_references excludes it from the unregistered-ID scan: "the
# checker's own test fixtures deliberately contain unregistered IDs." This
# file's own test suite (test_check_quarantine.py) plants literal
# "QUARANTINE: ZTAX-QUAR-..." strings as fixtures to test the marker logic
# itself, and those are not real markers — confirmed by actually running
# this scan against the real repo before excluding the directory, which
# produced four false findings pointing at this file's own test fixtures.
TEST_PREFIXES = ("backend/", "intelligence/tests/", "sdk/", "contracts/")
SKIP_PARTS = ("node_modules", "vendor")


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


def tracked_files(root: Path, prefixes: tuple[str, ...]) -> list[Path]:
    try:
        listing = subprocess.run(
            ["git", "-C", str(root), "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
            check=True, capture_output=True,
        ).stdout.decode("utf-8")
        names = [n for n in listing.split("\0") if n]
    except (OSError, subprocess.CalledProcessError):
        names = [p.relative_to(root).as_posix() for p in root.rglob("*") if p.is_file()]
    out = []
    for n in names:
        if not n.startswith(prefixes):
            continue
        if any(part in SKIP_PARTS for part in n.split("/")):
            continue
        out.append(root / n)
    return out


def _ref_path(ref: str) -> str:
    return ref.split("::", 1)[0]


def _covers(test_ref: str, verification_ref: str | None) -> bool:
    """True if quarantining test_ref removes verification_ref's coverage.

    Exact match (same path and, if given, the same ::TestName), or test_ref
    is the bare file and verification_ref is a specific test within that
    same file — quarantining the whole file quarantines every test in it."""
    if verification_ref is None:
        return False
    if test_ref == verification_ref:
        return True
    if "::" not in test_ref and test_ref == _ref_path(verification_ref):
        return True
    return False


# ---------------------------------------------------------------------------
# the checks
# ---------------------------------------------------------------------------


def check_entries(root: Path, register: dict, reqs: dict, today: dt.date, out: Findings) -> dict[str, dict]:
    where = "docs/quarantine.yaml"
    seen: dict[str, dict] = {}
    req_ids = {r["id"] for r in reqs["requirements"]}

    for e in register["quarantine"]:
        qid = e["quarantine_id"]
        if qid in seen:
            out.add(where, f"{qid} is registered twice")
        seen[qid] = e

        # Belt to the schema's braces: minLength catches "", not whitespace.
        for field_name in ("owner", "reason", "defect", "coverage_impact"):
            if not e[field_name].strip():
                out.add(where, f"{qid} has no {field_name}")

        if parse_date(e["expires_at"]) < today:
            out.add(where, f"{qid} expired at {e['expires_at']}")

        for rid in e.get("requirement_ids") or []:
            if rid not in req_ids:
                out.add(where, f"{qid} requirement_ids names {rid} which is not in docs/requirements.yaml")

        ref_path = _ref_path(e["test_ref"])
        test_path = root / ref_path
        if not test_path.is_file():
            out.add(where, f"{qid} test_ref {ref_path} does not exist")
        elif not ref_path.startswith(TEST_PREFIXES):
            scanned = ", ".join(TEST_PREFIXES)
            out.add(where, f"{qid} test_ref {ref_path} is outside the scanned test trees ({scanned}); it cannot be verified")

        # QA-REQ-0105: the sole verification of a mandatory gate must not be
        # quarantined. There is exactly one verification_ref per requirement
        # in this register's schema — no requirement names a second, backup
        # test — so any requirement whose verification_ref this test_ref
        # covers loses its only verification the moment the test is
        # quarantined. That is "no other test covers that requirement" by
        # construction, not a separately computed condition.
        affected = sorted(
            r["id"] for r in reqs["requirements"] if _covers(e["test_ref"], r["verification_ref"])
        )
        if affected:
            out.add(where, f"{qid} is the sole verification_ref for {', '.join(affected)} (QA-REQ-0105)")

    return seen


def check_markers(root: Path, register: dict[str, dict], out: Findings) -> None:
    """The register and the skip markers in source must agree in both
    directions: every marker names a registered ID, and every registered
    entry's test file actually carries its marker — and the marker must sit
    on the skip call itself, or it does not quarantine anything."""
    all_markers: dict[str, list[str]] = {}    # quarantine_id -> ["file:line", ...], any marker
    valid_markers: dict[str, list[str]] = {}  # same, but only where the line is a real skip call
    for path in tracked_files(root, TEST_PREFIXES):
        try:
            text = path.read_bytes().decode("utf-8")
        except (OSError, UnicodeDecodeError):
            continue
        rel = path.relative_to(root).as_posix()
        for lineno, line in enumerate(text.splitlines(), start=1):
            for m in MARKER.finditer(line):
                qid, loc = m.group(1), f"{rel}:{lineno}"
                all_markers.setdefault(qid, []).append(loc)
                if SKIP_CALL.search(line) and not _is_comment_line(line):
                    valid_markers.setdefault(qid, []).append(loc)
                else:
                    out.add(loc, "QUARANTINE marker is not inside a skip call, the test would still run")

    for qid, locations in all_markers.items():
        if qid not in register:
            for loc in locations:
                out.add(loc, f"QUARANTINE marker {qid} is not in docs/quarantine.yaml")

    for qid, e in register.items():
        test_file = root / _ref_path(e["test_ref"])
        if not test_file.is_file():
            continue  # already reported by check_entries
        rel_file = test_file.relative_to(root).as_posix()
        if qid not in valid_markers or rel_file not in {
            loc.rsplit(":", 1)[0] for loc in valid_markers.get(qid, [])
        }:
            out.add("docs/quarantine.yaml", f"{qid} has no QUARANTINE: {qid} marker in {_ref_path(e['test_ref'])}")


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


def run(root: Path, today: dt.date) -> Findings:
    out = Findings()
    register = load_yaml(root / "docs" / "quarantine.yaml")
    requirements = load_yaml(root / "docs" / "requirements.yaml")

    shapes_ok = all([
        validate_shape(register, load_schema(root, "quarantine.schema.json"), "docs/quarantine.yaml", out),
        validate_shape(requirements, load_schema(root, "requirements.schema.json"), "docs/requirements.yaml", out),
    ])
    if not shapes_ok:
        return out

    entries = check_entries(root, register, requirements, today, out)
    check_markers(root, entries, out)
    return out


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2],
                        help="repository root (default: inferred from this file)")
    parser.add_argument("--today", type=dt.date.fromisoformat, default=None,
                        help="judge expiry dates as of this date (default: today, UTC)")
    args = parser.parse_args(argv)

    root: Path = args.root.resolve()
    today = args.today or dt.datetime.now(dt.UTC).date()

    findings = run(root, today)
    for e in findings.errors:
        print(e)
    if findings.errors:
        print(f"\nquarantine gate: {len(findings.errors)} finding(s)", file=sys.stderr)
        return 1
    print("quarantine gate: green")
    return 0


if __name__ == "__main__":
    sys.exit(main())
