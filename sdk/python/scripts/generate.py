"""Regenerate `src/zoikotax/schema.py` from the contract.

The types are generated from the 3.1.0 export, never from the 3.2.0 document,
because the export is what the toolchain reads until a 3.2-capable one is
certified (ADR-0010 §2.2). They are committed, and `scripts/check.py` runs this
and then `git diff --exit-code`, so a contract change this SDK has not been
regenerated for is a build failure rather than a discovery (ADR-0010 §2.1).

Three things make the output a function of the contract and nothing else:

- The generator version is pinned in `requirements-dev.txt`, and this script
  refuses to run under any other. A different generator is a different file,
  and the diff it produced would be noise that trains people to ignore the gate.
- The generator's own header, which carries a timestamp and the input path, is
  replaced by a fixed one.
- Line endings are written as LF whatever the platform, so a regeneration on
  Windows does not rewrite every line.

Usage: python scripts/generate.py
"""

from __future__ import annotations

import json
import re
import subprocess
import sys
import tempfile
from importlib import metadata
from pathlib import Path

SDK = Path(__file__).resolve().parents[1]
REPO = SDK.parents[1]
CONTRACT = REPO / "contracts" / "openapi" / "export" / "ztax.v1.3.1.yaml"
OUTPUT = SDK / "src" / "zoikotax" / "schema.py"
GENERATOR = "datamodel-code-generator"


def pinned_version() -> str:
    """The generator version `requirements-dev.txt` pins. One pin, one place."""
    text = (SDK / "requirements-dev.txt").read_text(encoding="utf-8")
    match = re.search(rf"^{re.escape(GENERATOR)}==(\S+)\s*$", text, re.MULTILINE)
    if match is None:
        sys.exit(f"generate: requirements-dev.txt does not pin {GENERATOR} exactly")
    return match.group(1)


HEADER = """\
# GENERATED — do not edit.
#
# TypedDicts for the ZoikoTax v1 API, generated from
# contracts/openapi/export/ztax.v1.3.1.yaml by {generator} {version}
# (sdk/python/scripts/generate.py).
#
# A hand edit here fails CI: `python scripts/check.py` regenerates this file
# and fails on any diff. Edit the contract, then regenerate."""

ARGS = [
    "--input-file-type", "openapi",
    # Schemas, plus the inline response bodies of the list operations, which
    # have no schema of their own in the contract.
    "--openapi-scopes", "schemas", "paths",
    # TypedDicts describe the JSON as it arrives, and cost nothing at runtime.
    # The pydantic model types would make pydantic a runtime dependency.
    "--output-model-type", "typing.TypedDict",
    "--target-python-version", "3.10",
    "--use-standard-collections",
    "--use-union-operator",
    "--use-schema-description",
    "--use-field-description",
    # Wire formats stay strings. A TypedDict does no conversion, so typing a
    # timestamp as `datetime` or an email as pydantic's `EmailStr` would be a
    # claim about the value that nothing makes true.
    "--type-mappings", "date-time=string", "email=string", "uri=string", "password=string",
    # PEP 728 closed TypedDicts are not yet understood by every type checker.
    "--no-use-closed-typed-dict",
    # `NotRequired` is in `typing` only from 3.11; see `_compat.py`.
    "--import-overrides", json.dumps({"NotRequired": "zoikotax._compat"}),
    # The builtin formatter, so the output does not depend on black's or
    # isort's version as well as the generator's.
    "--formatters", "builtin",
    "--disable-timestamp",
]


def main() -> None:
    version = pinned_version()
    try:
        installed = metadata.version(GENERATOR)
    except metadata.PackageNotFoundError:
        sys.exit(f"generate: {GENERATOR} is not installed; pip install -r requirements-dev.txt")
    if installed != version:
        sys.exit(f"generate: {GENERATOR} {installed} is installed and {version} is pinned")

    with tempfile.TemporaryDirectory() as tmp:
        out = Path(tmp) / "schema.py"
        subprocess.run(
            [
                sys.executable, "-m", "datamodel_code_generator",
                "--input", str(CONTRACT),
                "--output", str(out),
                "--custom-file-header", HEADER.format(generator=GENERATOR, version=version),
                *ARGS,
            ],
            check=True,
        )
        text = out.read_text(encoding="utf-8")

    text = text.replace("\r\n", "\n").rstrip("\n") + "\n"
    OUTPUT.write_text(text, encoding="utf-8", newline="\n")
    print(f"generate: wrote {OUTPUT.relative_to(SDK).as_posix()}")


if __name__ == "__main__":
    main()
