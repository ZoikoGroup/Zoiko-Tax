#!/usr/bin/env python3
"""The known-defects gate — QA-REQ-0099, QA-REQ-0100.

    "QA-SEV-1 defects MUST block authoritative production release."
    (QA-REQ-0099 -- no exception clause: a QA-SEV-1 defect cannot be waived,
    only resolved.)
    "QA-SEV-2 defects MUST normally block release unless explicit
    governance permits and risk is bounded." (QA-REQ-0100 -- the "explicit
    governance" is a waiver, registered in docs/waivers.yaml, whose own
    requirement_id names this requirement.)

This file follows the conventions of docs/tools/check_quarantine.py: the
same YAML loading, the same jsonschema shape validation before any
cross-file rule runs, the same `Findings.add(where, message)` error format,
the same `--today` injection point for testing, and the same exit-code
contract (0 and "defects gate: green" on success; 1 and one printed line
per finding otherwise).

What this file does NOT do: re-check a waiver's own expiry or self-approval
rules as a register error -- docs/tools/check_registry.py already owns
that. Here, expiry only ever affects whether a defect is currently
*blocking* a release; a waiver going stale is not a malformed register.

Usage:

    python docs/tools/check_defects.py                 # the gate
    python docs/tools/check_defects.py --today 2026-12-01
    python docs/tools/check_defects.py --release        # also fail if anything blocks
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import sys
from dataclasses import dataclass, field
from pathlib import Path

import jsonschema
import yaml

QA_SEV_2_REQUIREMENT = "ZTAX-QA-REQ-0100"


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


def validate_shape(doc: object, schema: dict, where: str, out: Findings) -> bool:
    validator = jsonschema.Draft202012Validator(schema)
    ok = True
    for err in sorted(validator.iter_errors(doc), key=lambda e: list(e.absolute_path)):
        path = "/".join(str(p) for p in err.absolute_path) or "(root)"
        out.add(where, f"schema: {path}: {err.message}")
        ok = False
    return ok


def _waiver_expiry(w: dict) -> dt.datetime:
    return dt.datetime.strptime(w["expires_at"], "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=dt.UTC)


def _is_expired(waiver: dict, now: dt.datetime) -> bool:
    """A waiver's expires_at is a full RFC 3339 instant, not a calendar
    date -- judging it against a date would silently round an afternoon
    expiry up to the next midnight. Compare against the instant directly,
    the same as check_registry.py's own waiver-expiry rule."""
    return _waiver_expiry(waiver) < now


# ---------------------------------------------------------------------------
# the checks
# ---------------------------------------------------------------------------


def check_entries(defects: dict, waivers_by_id: dict[str, dict], out: Findings) -> dict[str, dict]:
    """Rules a-d: schema is already validated by the caller. This handles
    the cross-file rules: uniqueness, waiver existence, and the two
    severity-specific waiver rules."""
    where = "docs/defects.yaml"
    seen: dict[str, dict] = {}

    for d in defects["defects"]:
        did = d["defect_id"]
        if did in seen:
            out.add(where, f"{did} is registered twice")
        seen[did] = d

        waiver_id = d.get("waiver_id")
        if waiver_id is None:
            continue

        waiver = waivers_by_id.get(waiver_id)
        if waiver is None:
            out.add(where, f"{did} names waiver_id {waiver_id} which is not in docs/waivers.yaml")
            continue

        if d["severity"] == "QA-SEV-1":
            out.add(where, f"{did} is QA-SEV-1 and carries waiver_id {waiver_id} "
                            f"(QA-REQ-0099 has no exception: QA-SEV-1 cannot be waived, only resolved)")
        elif d["severity"] == "QA-SEV-2" and waiver["requirement_id"] != QA_SEV_2_REQUIREMENT:
            out.add(where, f"{did}'s waiver_id {waiver_id} is for {waiver['requirement_id']}, "
                            f"not {QA_SEV_2_REQUIREMENT}; it does not cover this defect's severity")

    return seen


def blocking_defects(root: Path, now: dt.datetime) -> list[dict]:
    """Every OPEN QA-SEV-1 defect, plus every OPEN QA-SEV-2 defect that does
    not carry a waiver_id resolving to a ZTAX-QA-REQ-0100 waiver unexpired
    as of `now` (a full instant, not a calendar date -- see _is_expired).
    A defect whose register entry is itself invalid (rules a-d) is treated
    as unwaived here -- an invalid reference covers nothing."""
    defects = load_yaml(root / "docs" / "defects.yaml")
    waivers = load_yaml(root / "docs" / "waivers.yaml")
    waivers_by_id = {w["waiver_id"]: w for w in waivers["waivers"]}

    blockers: list[dict] = []
    for d in defects["defects"]:
        if d["status"] != "OPEN":
            continue
        if d["severity"] == "QA-SEV-1":
            blockers.append(d)
            continue
        # QA-SEV-2
        waiver_id = d.get("waiver_id")
        waiver = waivers_by_id.get(waiver_id) if waiver_id else None
        if (
            waiver is None
            or waiver["requirement_id"] != QA_SEV_2_REQUIREMENT
            or _is_expired(waiver, now)
        ):
            blockers.append(d)
    return blockers


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


def run(root: Path) -> Findings:
    """Rules a-d only -- schema, duplicate IDs, and the two severity-specific
    waiver rules. None of them turns on the date or time, so unlike
    check_registry.py's run(), this one takes no clock."""
    out = Findings()
    defects = load_yaml(root / "docs" / "defects.yaml")
    waivers = load_yaml(root / "docs" / "waivers.yaml")

    shapes_ok = all([
        validate_shape(defects, load_schema(root, "defects.schema.json"), "docs/defects.yaml", out),
        validate_shape(waivers, load_schema(root, "waivers.schema.json"), "docs/waivers.yaml", out),
    ])
    if not shapes_ok:
        return out

    waivers_by_id = {w["waiver_id"]: w for w in waivers["waivers"]}
    check_entries(defects, waivers_by_id, out)
    return out


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2],
                        help="repository root (default: inferred from this file)")
    parser.add_argument("--today", type=dt.date.fromisoformat, default=None,
                        help="judge waiver expiry as of midnight UTC on this date (default: now, UTC)")
    parser.add_argument("--release", action="store_true",
                        help="also exit 1 and list blockers when blocking_defects is non-empty")
    args = parser.parse_args(argv)

    root: Path = args.root.resolve()
    now = dt.datetime.now(dt.UTC)
    if args.today:
        now = dt.datetime.combine(args.today, dt.time(0, 0), tzinfo=dt.UTC)

    findings = run(root)
    for e in findings.errors:
        print(e)
    if findings.errors:
        print(f"\ndefects gate: {len(findings.errors)} finding(s)", file=sys.stderr)
        return 1

    blockers = blocking_defects(root, now)
    for b in blockers:
        print(f"blocking: {b['defect_id']} ({b['severity']}, {b['status']})")

    if args.release and blockers:
        print(f"\ndefects gate: {len(blockers)} release blocker(s)", file=sys.stderr)
        return 1

    print("defects gate: green")
    return 0


if __name__ == "__main__":
    sys.exit(main())
