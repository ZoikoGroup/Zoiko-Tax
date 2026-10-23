"""Step-10 AI Boundary Demo Runner.

One-command demonstration of the playbook's AI boundary claims.  Prints an
honest status for each claim: PASS, FAIL, NOT IMPLEMENTED or NOT RUN.

Usage::

    python tools/step10_demo.py            # run Python + Go tests
    python tools/step10_demo.py --skip-go  # skip the Go rows
    python tools/step10_demo.py --dry-run  # print all rows as NOT RUN
    python tools/step10_demo.py --json     # machine-readable JSON output

Exit code is 1 if any claim is FAIL, else 0.
NOT IMPLEMENTED and NOT RUN never count as PASS.

No live model is connected.  These checks show governance is independent of
model output and payload content.  They do not show that a real model resists
injection.
"""

from __future__ import annotations

import argparse
import dataclasses
import json
import subprocess
import sys
from collections.abc import Sequence
from pathlib import Path
from typing import Final

# ---------------------------------------------------------------------------
# Repo / folder resolution (relative to this script, not cwd)
# ---------------------------------------------------------------------------

_TOOLS_DIR: Final[Path] = Path(__file__).resolve().parent
_INTEL_DIR: Final[Path] = _TOOLS_DIR.parent
_REPO_ROOT: Final[Path] = _INTEL_DIR.parent
_BACKEND_DIR: Final[Path] = _REPO_ROOT / "backend"

# Go source files that prove C8.
_GO_APP_FILE: Final[Path] = _BACKEND_DIR / "internal" / "app" / "ai_independence_test.go"
_GO_HTTP_FILE: Final[Path] = (
    _BACKEND_DIR / "internal" / "transport" / "http" / "classification_test.go"
)

# ---------------------------------------------------------------------------
# Claim model
# ---------------------------------------------------------------------------

_KIND_PYTHON: Final[str] = "python"
_KIND_GO: Final[str] = "go"
_KIND_NONE: Final[str] = "none"

# Status constants -- used as strings so JSON output is human-readable.
STATUS_PASS: Final[str] = "PASS"
STATUS_FAIL: Final[str] = "FAIL"
STATUS_NOT_IMPLEMENTED: Final[str] = "NOT IMPLEMENTED"
STATUS_NOT_RUN: Final[str] = "NOT RUN"


@dataclasses.dataclass(frozen=True)
class Claim:
    """One playbook boundary claim."""

    id: str
    text: str
    playbook_ref: str
    kind: str  # "python" | "go" | "none"
    python_nodes: tuple[str, ...] = dataclasses.field(default_factory=tuple)
    go_tests: tuple[str, ...] = dataclasses.field(default_factory=tuple)
    note: str = ""


# Go test names for C8 -- kept as named constants so the unit tests can
# compare them without touching the filesystem.
C8_APP_TESTS: Final[tuple[str, ...]] = (
    "TestDeterministicFlowIndependentOfGateway",
    "TestDeterminationServiceTakesNoModelGateway",
)
C8_HTTP_TESTS: Final[tuple[str, ...]] = ("TestKilledUseCaseNeverReachesTransport",)

_BOUNDARY_FILE: Final[str] = "tests/test_ai_boundary_proof.py"


def _node(cls: str) -> str:
    return f"{_BOUNDARY_FILE}::{cls}"


CLAIMS: Final[tuple[Claim, ...]] = (
    Claim(
        id="C1",
        text="Hostile text in a request cannot cause an approval or a privileged action.",
        playbook_ref="ZTAX-ENG-RESET-001 §19, §26 step 10",
        kind=_KIND_PYTHON,
        python_nodes=(_node("TestP1HostileInputIsNeutralised"),),
    ),
    Claim(
        id="C2",
        text=(
            "Hostile text inside a retrieved document is treated as data and cannot"
            " cause an action."
        ),
        playbook_ref="ZTAX-ENG-RESET-001 §19, §26 step 10",
        kind=_KIND_PYTHON,
        python_nodes=(_node("TestP2HostileRagDocumentIsNeutralised"),),
    ),
    Claim(
        id="C3",
        text="A fabricated or unsupported citation is rejected.",
        playbook_ref="ZTAX-ENG-RESET-001 §19, §26 step 10",
        kind=_KIND_PYTHON,
        python_nodes=(_node("TestP3FabricatedCitationIsRejected"),),
    ),
    Claim(
        id="C4",
        text="Forbidden (A5) actions are always blocked and the model is never reached.",
        playbook_ref="ZTAX-ENG-RESET-001 §19, §26 step 10",
        kind=_KIND_PYTHON,
        python_nodes=(_node("TestP4A5IsAlwaysBlocked"),),
    ),
    Claim(
        id="C5",
        text=(
            "When the model provider is unavailable the gateway fails with a defined"
            " error, writes a BLOCK record, and recovers on the next call."
        ),
        playbook_ref="ZTAX-ENG-RESET-001 §19, §26 step 10",
        kind=_KIND_PYTHON,
        python_nodes=(_node("TestP5ProviderUnavailability"),),
    ),
    Claim(
        id="C6",
        text="Sensitive data never appears in evidence records, logs or error output.",
        playbook_ref="ZTAX-ENG-RESET-001 §19, §26 step 10",
        kind=_KIND_PYTHON,
        python_nodes=(_node("TestP6SensitiveDataNeverLeaks"),),
    ),
    Claim(
        id="C7",
        text=(
            "Cross-tenant retrieval is isolated, and malformed context is refused"
            " before the model."
        ),
        playbook_ref="ZTAX-ENG-RESET-001 §19, §26 step 10",
        kind=_KIND_PYTHON,
        python_nodes=(
            _node("TestP7TenantIsolation"),
            _node("TestP8WireMalformedDataClass"),
        ),
    ),
    Claim(
        id="C8",
        text=(
            "With the model provider disabled, approved deterministic quotes and"
            " commits continue."
        ),
        playbook_ref="ZTAX-ENG-RESET-001 §19, §26 step 10",
        kind=_KIND_GO,
        go_tests=C8_APP_TESTS + C8_HTTP_TESTS,
    ),
    Claim(
        id="C9",
        text=(
            "With the model provider disabled, new ambiguous mappings enter human"
            " review."
        ),
        playbook_ref="ZTAX-ENG-RESET-001 §19, §26 step 10",
        kind=_KIND_NONE,
        note="Go side not implemented yet",
    ),
)

# ---------------------------------------------------------------------------
# Pure status functions
# ---------------------------------------------------------------------------


def decide_status(exit_code: int) -> str:
    """Map a pytest exit code to PASS or FAIL.

    Exit code 0 is PASS.  Any other code -- including 5 ("no tests collected")
    -- is FAIL, because a missing test is not a passing test.
    """
    return STATUS_PASS if exit_code == 0 else STATUS_FAIL


def go_status(output: str, exit_code: int, expected_names: tuple[str, ...]) -> str:
    """Decide the C8 status from ``go test -v`` output and exit code.

    Rules:
    * Non-zero exit code -> FAIL.
    * "no tests to run" anywhere in output -> FAIL.
    * Any expected name missing from ``--- PASS: <name>`` lines -> FAIL.
    * All expected names present and exit code 0 -> PASS.
    """
    if exit_code != 0:
        return STATUS_FAIL
    if "no tests to run" in output:
        return STATUS_FAIL
    for name in expected_names:
        if f"--- PASS: {name}" not in output:
            return STATUS_FAIL
    return STATUS_PASS


# ---------------------------------------------------------------------------
# Runners
# ---------------------------------------------------------------------------


def _run_python(nodes: tuple[str, ...], intel_dir: Path) -> tuple[str, list[str]]:
    """Run pytest for the given node ids.  Return (status, failing_names)."""
    cmd = [
        sys.executable,
        "-m",
        "pytest",
        "-o",
        "addopts=",
        "-p",
        "no:cacheprovider",
        "-q",
        "--tb=no",
        "-rf",
        *nodes,
    ]
    result = subprocess.run(cmd, cwd=str(intel_dir), capture_output=True, text=True)
    status = decide_status(result.returncode)
    failing: list[str] = []
    if status == STATUS_FAIL:
        for line in result.stdout.splitlines():
            stripped = line.strip()
            if stripped.startswith("FAILED "):
                failing.append(stripped[len("FAILED "):].split(" ")[0])
        failing = failing[:5]
    return status, failing


def _git_short_hash() -> str | None:
    """Return the short HEAD hash, or None if git is unavailable."""
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--short", "HEAD"],
            cwd=str(_INTEL_DIR),
            capture_output=True,
            text=True,
        )
        if result.returncode == 0:
            return result.stdout.strip()
    except FileNotFoundError:
        pass
    return None


def _go_available() -> bool:
    """Return True if the ``go`` binary is on PATH."""
    try:
        result = subprocess.run(["go", "version"], capture_output=True, text=True)
        return result.returncode == 0
    except FileNotFoundError:
        return False


def _run_go(
    pkg: str,
    run_pattern: str,
    expected: tuple[str, ...],
    backend_dir: Path,
) -> tuple[str, str]:
    """Run ``go test -count=1 -v`` for one package.  Return (status, output)."""
    cmd = ["go", "test", "-count=1", "-v", pkg, "-run", run_pattern]
    result = subprocess.run(cmd, cwd=str(backend_dir), capture_output=True, text=True)
    combined = result.stdout + result.stderr
    return go_status(combined, result.returncode, expected), combined


# ---------------------------------------------------------------------------
# Table rendering
# ---------------------------------------------------------------------------


def render_table(results: list[tuple[Claim, str, list[str]]]) -> str:
    """Render a fixed-width table of results.

    ``results`` is a list of (claim, status, evidence_lines).
    PASS is only printed when status is actually STATUS_PASS; the function
    never fabricates a PASS for a row that was not executed.
    """
    id_w = 3
    text_w = max(len(c.text) for c, _, _ in results)
    status_w = max(len(s) for _, s, _ in results)

    header = f"{'ID':<{id_w}}  {'Claim':<{text_w}}  {'Status':<{status_w}}  Evidence"
    sep = "-" * len(header)
    lines = [sep, header, sep]
    for claim, status, evidence in results:
        lines.append(
            f"{claim.id:<{id_w}}  {claim.text:<{text_w}}  {status:<{status_w}}"
        )
        for ev in evidence:
            lines.append(f"{'':>{id_w + 2 + text_w + 2 + status_w + 2}}{ev}")

    lines.append(sep)
    return "\n".join(lines)


def summarise(results: list[tuple[Claim, str, list[str]]]) -> str:
    """Return a one-line count of each status."""
    counts: dict[str, int] = {
        STATUS_PASS: 0,
        STATUS_FAIL: 0,
        STATUS_NOT_IMPLEMENTED: 0,
        STATUS_NOT_RUN: 0,
    }
    for _, status, _ in results:
        base = status.split("(")[0].strip()
        if base in counts:
            counts[base] += 1
        elif STATUS_NOT_RUN in status:
            counts[STATUS_NOT_RUN] += 1
    parts = [f"{v} {k}" for k, v in counts.items() if v]
    return "  |  ".join(parts)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

_FOOTER: Final[str] = (
    "No live model is connected.  These checks show governance is independent of"
    " model output and payload content.  They do not show that a real model resists"
    " injection."
)


def main(argv: Sequence[str] | None = None) -> int:
    """Entry point.  Returns the exit code."""
    parser = argparse.ArgumentParser(
        description="Step-10 AI boundary demo runner.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--skip-go", action="store_true", help="Skip the Go C8 row.")
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print all rows as NOT RUN without executing any test.",
    )
    parser.add_argument(
        "--json",
        action="store_true",
        dest="json_out",
        help="Print machine-readable JSON output.",
    )
    args = parser.parse_args(argv)

    # Header (suppressed in JSON mode so stdout is pure JSON)
    python_ver = sys.version.split()[0]
    git_hash = _git_short_hash()
    hash_str = f"  commit {git_hash}" if git_hash else ""
    if not args.json_out:
        print(f"\nStep-10 AI Boundary Demo  python {python_ver}{hash_str}\n")


    results: list[tuple[Claim, str, list[str]]] = []

    go_available = _go_available()
    go_files_present = _GO_APP_FILE.exists() and _GO_HTTP_FILE.exists()

    for claim in CLAIMS:
        status: str
        evidence: list[str] = []

        if args.dry_run:
            status = f"{STATUS_NOT_RUN} (dry run)"
        elif claim.kind == _KIND_NONE:
            status = STATUS_NOT_IMPLEMENTED
            if claim.note:
                evidence = [claim.note]
        elif claim.kind == _KIND_PYTHON:
            status, evidence = _run_python(claim.python_nodes, _INTEL_DIR)
        elif claim.kind == _KIND_GO:
            if args.skip_go:
                status = f"{STATUS_NOT_RUN} (--skip-go)"
            elif not go_available:
                status = f"{STATUS_NOT_RUN} (go not on PATH)"
            elif not go_files_present:
                status = f"{STATUS_NOT_RUN} (Go test files not found in this checkout)"
            else:
                # Run ./internal/app tests
                app_pattern = (
                    "^("
                    + "|".join(C8_APP_TESTS)
                    + ")$"
                )
                app_status, _app_out = _run_go(
                    "./internal/app", app_pattern, C8_APP_TESTS, _BACKEND_DIR
                )
                # Run ./internal/transport/http tests
                http_pattern = "^(" + "|".join(C8_HTTP_TESTS) + ")$"
                http_status, _http_out = _run_go(
                    "./internal/transport/http",
                    http_pattern,
                    C8_HTTP_TESTS,
                    _BACKEND_DIR,
                )
                if app_status == STATUS_PASS and http_status == STATUS_PASS:
                    status = STATUS_PASS
                else:
                    status = STATUS_FAIL
                    if app_status == STATUS_FAIL:
                        evidence.append("app package: FAIL")
                    if http_status == STATUS_FAIL:
                        evidence.append("http package: FAIL")
        else:
            status = STATUS_NOT_IMPLEMENTED

        results.append((claim, status, evidence))

    if args.json_out:
        out = [
            {
                "id": c.id,
                "text": c.text,
                "status": s,
                "evidence": ev,
            }
            for c, s, ev in results
        ]
        print(json.dumps(out, indent=2))
    else:
        print(render_table(results))
        print(f"\n{summarise(results)}\n")
        print(_FOOTER)

    any_fail = any(s == STATUS_FAIL for _, s, _ in results)
    return 1 if any_fail else 0


if __name__ == "__main__":
    sys.exit(main())
