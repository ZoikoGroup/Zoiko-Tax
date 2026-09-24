"""The full gate: regenerate and fail on drift, typecheck, test.

    python scripts/check.py

The Python counterpart of the TypeScript SDK's `npm run check`, and the one
command CI runs. It is a script rather than a Makefile so that it runs the
same way on every platform the SDK is developed on.

Run it from an environment with `requirements-dev.txt` installed. The SDK
itself needs nothing beyond the standard library, which is why the tests run
from `src/` rather than from an installed copy.
"""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path

SDK = Path(__file__).resolve().parents[1]


def step(name: str, *argv: str, env: dict[str, str] | None = None) -> None:
    print(f"\n== {name}: {' '.join(argv)}", flush=True)
    if subprocess.run(argv, cwd=SDK, env=env).returncode != 0:
        sys.exit(f"check: {name} failed")


def main() -> None:
    python = sys.executable
    step("generate", python, "scripts/generate.py")
    # A regenerated file that differs from the committed one is drift: the
    # contract changed and this SDK was not regenerated, or schema.py was
    # edited by hand. Either way the build stops here (ADR-0010 §2.1).
    step("generate:check", "git", "diff", "--exit-code", "--", "src/zoikotax/schema.py")
    step("typecheck", python, "-m", "mypy")
    env = {**os.environ, "PYTHONPATH": str(SDK / "src")}
    step("test", python, "-m", "unittest", "discover", "-s", "tests", "-v", env=env)
    print("\ncheck: ok")


if __name__ == "__main__":
    main()
