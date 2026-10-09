#!/usr/bin/env python3
"""Builds and verifies the ReleaseCertificationManifest — QA-REQ-0109 to
0113 (0112 partly: a declared ``rollback_ref``, not yet a documented
procedure).

    "Release certification MUST produce a ReleaseCertificationManifest."
    "ReleaseCertificationManifest MUST include traceability snapshot."
    "ReleaseCertificationManifest MUST include known defects/waivers."
    "ReleaseCertificationManifest MUST identify rollback/suspension path."
    "ReleaseCertificationManifest MUST be tamper-evident."
    (docs/requirements.yaml, ZTAX-QA-REQ-0109 to 0113)

This file follows the conventions of docs/tools/check_quarantine.py: the
same jsonschema shape validation before anything else runs, the same
`Findings.add(where, message)` error format, the same `--root` inference,
and an injectable clock (``--generated-at`` plays ``--today``'s role here)
so a test never depends on wall-clock time.

Two modes:

    build   Reads docs/evidence/traceability_summary.json, docs/waivers.yaml
            and docs/quarantine.yaml as they are -- none of the three is
            re-derived here -- and writes one JSON manifest carrying a
            SHA-256 hash (QA-REQ-0113) over every other field.

    verify  Recomputes that hash against a manifest file and reports
            whether it is still intact. This is the tamper-evidence proof:
            anyone holding the file can run this with no other input.

Usage:

    python docs/tools/build_release_manifest.py build --version 1.2.3 --commit <sha>
    python docs/tools/build_release_manifest.py verify docs/evidence/release_manifest.json
"""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import sys
from dataclasses import dataclass, field
from pathlib import Path

import jsonschema
import yaml

SCHEMA_VERSION = "1.0.0"


class ManifestSourceError(ValueError):
    """A source register (docs/waivers.yaml or docs/quarantine.yaml) is not
    shaped as assemble() requires: a mapping with a list under the expected
    top-level key. An empty list is fine; a missing key, a non-mapping
    document or a non-list value is not -- those are silently-wrong input,
    not an empty register."""


@dataclass
class Findings:
    errors: list[str] = field(default_factory=list)

    def add(self, where: str, message: str) -> None:
        self.errors.append(f"{where}: {message}")


def load_schema(root: Path) -> dict:
    with (root / "docs" / "schema" / "release_manifest.schema.json").open(encoding="utf-8") as f:
        return json.load(f)


def validate_shape(doc: object, schema: dict, where: str, out: Findings) -> bool:
    validator = jsonschema.Draft202012Validator(schema)
    ok = True
    for err in sorted(validator.iter_errors(doc), key=lambda e: list(e.absolute_path)):
        path = "/".join(str(p) for p in err.absolute_path) or "(root)"
        out.add(where, f"schema: {path}: {err.message}")
        ok = False
    return ok


def canonical_bytes(obj: object) -> bytes:
    """The exact byte string manifest_hash is computed over: sorted keys, no
    extra whitespace, UTF-8. Independent of how the file on disk happens to
    be formatted -- verify reparses before hashing, so pretty-printing the
    file never breaks its own proof."""
    return json.dumps(obj, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def compute_hash(manifest_without_hash: dict) -> str:
    return hashlib.sha256(canonical_bytes(manifest_without_hash)).hexdigest()


def check_manifest(manifest: object, schema: dict, where: str) -> Findings:
    """Schema validity, then the hash recompute -- the one check that proves
    tamper-evidence rather than merely shape (QA-REQ-0113)."""
    out = Findings()
    if not validate_shape(manifest, schema, where, out):
        return out
    claimed = manifest.get("manifest_hash")
    body = {k: v for k, v in manifest.items() if k != "manifest_hash"}
    actual = compute_hash(body)
    if actual != claimed:
        out.add(where, f"manifest_hash mismatch (recorded {claimed}, recomputed {actual})")
    return out


def _read_json(path: Path) -> object:
    with path.open(encoding="utf-8") as f:
        return json.load(f)


def _read_yaml(path: Path) -> object:
    with path.open(encoding="utf-8") as f:
        return yaml.safe_load(f)


def _now_iso() -> str:
    return dt.datetime.now(dt.UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


def _require_list(doc: object, key: str, path: Path) -> list:
    """doc must be a mapping with a list under key. Raises
    ManifestSourceError naming exactly what is wrong -- never silently
    substitutes an empty list for a malformed source."""
    if not isinstance(doc, dict):
        raise ManifestSourceError(f"{path} must be a mapping, got {type(doc).__name__}")
    if key not in doc:
        raise ManifestSourceError(f"{path} has no top-level {key!r} key")
    value = doc[key]
    if not isinstance(value, list):
        raise ManifestSourceError(f"{path}'s {key!r} must be a list, got {type(value).__name__}")
    return value


# ---------------------------------------------------------------------------
# build
# ---------------------------------------------------------------------------


def assemble(root: Path, version: str, commit: str, generated_at: str, rollback_ref: str | None) -> dict:
    """Reads the three existing registers as-is and assembles the manifest
    body, then seals it with manifest_hash. Raises FileNotFoundError if the
    traceability snapshot has never been generated, or ManifestSourceError
    if docs/waivers.yaml or docs/quarantine.yaml is not a mapping with a
    list under its expected key."""
    summary_path = root / "docs" / "evidence" / "traceability_summary.json"
    if not summary_path.is_file():
        raise FileNotFoundError(
            f"{summary_path} does not exist -- run docs/generate_traceability_report.py first"
        )
    traceability = _read_json(summary_path)

    waivers_path = root / "docs" / "waivers.yaml"
    waivers_list = _require_list(_read_yaml(waivers_path), "waivers", waivers_path)

    quarantine_path = root / "docs" / "quarantine.yaml"
    quarantine_list = _require_list(_read_yaml(quarantine_path), "quarantine", quarantine_path)

    body = {
        "schema_version": SCHEMA_VERSION,
        "version": version,
        "commit": commit,
        "generated_at": generated_at,
        "traceability": traceability,
        "waivers": {"list": waivers_list, "count": len(waivers_list)},
        "quarantine": {"entries": quarantine_list, "count": len(quarantine_list)},
        "rollback_ref": rollback_ref,
    }
    return {**body, "manifest_hash": compute_hash(body)}


def cmd_build(args: argparse.Namespace) -> int:
    root: Path = args.root.resolve()
    generated_at = args.generated_at or _now_iso()
    out: Path = args.out if args.out is not None else root / "docs" / "evidence" / "release_manifest.json"

    try:
        manifest = assemble(root, args.version, args.commit, generated_at, args.rollback_ref)
    except (FileNotFoundError, ManifestSourceError) as exc:
        print(f"build: {exc}", file=sys.stderr)
        return 1

    if manifest["rollback_ref"] is None:
        if args.require_rollback:
            print("build: QA-REQ-0112 unmet: no rollback_ref, and --require-rollback was given",
                  file=sys.stderr)
            return 1
        print("WARNING: QA-REQ-0112 open: no rollback_ref")

    schema = load_schema(root)
    findings = Findings()
    if not validate_shape(manifest, schema, str(out), findings):
        for e in findings.errors:
            print(e)
        print(f"\nbuild: {len(findings.errors)} schema finding(s)", file=sys.stderr)
        return 1

    out.parent.mkdir(parents=True, exist_ok=True)
    text = json.dumps(manifest, indent=2, sort_keys=True, ensure_ascii=False) + "\n"
    out.write_text(text, encoding="utf-8", newline="\n")
    print(f"release manifest: built {out}")
    return 0


# ---------------------------------------------------------------------------
# verify
# ---------------------------------------------------------------------------


def cmd_verify(args: argparse.Namespace) -> int:
    root: Path = args.root.resolve()
    path: Path = args.path

    try:
        text = path.read_text(encoding="utf-8")
    except OSError as exc:
        print(f"verify: cannot read {path}: {exc}", file=sys.stderr)
        return 1

    try:
        manifest = json.loads(text)
    except json.JSONDecodeError as exc:
        print(f"verify: {path} is not valid JSON: {exc}", file=sys.stderr)
        return 1

    schema = load_schema(root)
    findings = check_manifest(manifest, schema, str(path))
    for e in findings.errors:
        print(e)
    if findings.errors:
        print(f"\nrelease manifest: {len(findings.errors)} finding(s)", file=sys.stderr)
        return 1
    print("release manifest: intact")
    return 0


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2],
                        help="repository root (default: inferred from this file)")
    sub = parser.add_subparsers(dest="mode", required=True)

    build = sub.add_parser("build", help="assemble and write a release certification manifest")
    build.add_argument("--version", required=True)
    build.add_argument("--commit", required=True)
    build.add_argument("--out", type=Path, default=None,
                        help="default: docs/evidence/release_manifest.json under --root")
    build.add_argument("--generated-at", default=None, help="ISO 8601 UTC timestamp (default: now)")
    build.add_argument("--rollback-ref", default=None,
                        help="path or URL to the rollback/suspension procedure (QA-REQ-0112)")
    build.add_argument("--require-rollback", action="store_true",
                        help="exit 1 instead of warning when rollback_ref is not given")
    build.set_defaults(func=cmd_build)

    verify = sub.add_parser("verify", help="recompute and check a manifest's hash")
    verify.add_argument("path", type=Path)
    verify.set_defaults(func=cmd_verify)

    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
