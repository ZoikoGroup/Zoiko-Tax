#!/usr/bin/env python3
"""Re-register the golden decimal vector sets — ADR-0002 §2.7 and §6.

This writes manifest.json with the current digest of every set. Running it is
how a vector re-baseline is recorded, and a re-baseline is a reviewed act: the
diff it produces is the evidence that someone intended the expected answers to
change. Precision and trap settings can change up to A4 with a re-baseline;
after A4 they cannot, because historical decisions must replay byte-identically.

    python3 tools/decimalcrosscheck/register.py testdata/golden/decimal

It is deliberately a separate script from crosscheck.py. A checker that can
silence itself is not a check.
"""

from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path

NOTE = (
    "Digests are sha256 over the exact bytes of each set. The Go runner and the "
    "Python cross-check both verify them before reading a set, so an unreviewed "
    "edit fails the build rather than relaxing an assertion. Re-register with "
    "`make golden-register` (ADR-0002 §2.7, §6)."
)


def main(argv: list[str]) -> int:
    root = Path(argv[1] if len(argv) > 1 else "testdata/golden/decimal")
    manifest_path = root / "manifest.json"

    sets = []
    for path in sorted(root.glob("*.json")):
        if path.name == "manifest.json":
            continue
        vectors = json.loads(path.read_text(encoding="utf-8"))
        sets.append(
            {
                "file": path.name,
                "set": vectors["set"],
                "version": vectors["version"],
                "cases": len(vectors["cases"]),
                "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
            }
        )

    if not sets:
        raise SystemExit(f"{root}: no vector sets to register")

    previous = {}
    if manifest_path.exists():
        previous = {
            entry["file"]: entry["sha256"]
            for entry in json.loads(manifest_path.read_text(encoding="utf-8"))["sets"]
        }

    manifest = {
        "schema": "ztax.golden.decimal/v1",
        "note": NOTE,
        "sets": sets,
    }
    manifest_path.write_text(
        json.dumps(manifest, indent=2, ensure_ascii=False) + "\n", encoding="utf-8"
    )

    for entry in sets:
        was = previous.get(entry["file"])
        state = "registered" if was is None else ("unchanged" if was == entry["sha256"] else "RE-BASELINED")
        print(f"{state:12} {entry['file']:24} {entry['cases']:3} cases  {entry['sha256'][:16]}...")
    for gone in sorted(set(previous) - {entry["file"] for entry in sets}):
        print(f"{'REMOVED':12} {gone}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
