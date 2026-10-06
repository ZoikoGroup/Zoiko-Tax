"""Tests for the "verify every gate passed" step of the `ci` job in
.github/workflows/ci.yml.

Run standalone (no discover needed):
    cd docs/tools/tests && python -m unittest test_ci_aggregate_gate
Or via discover:
    python -m unittest discover -s docs/tools/tests
"""

from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import unittest
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parents[3]
CI_YML = REPO_ROOT / ".github" / "workflows" / "ci.yml"

# Matches the GitHub Actions expression `${{ toJSON(needs) }}`, tolerant of
# the whitespace variants GitHub itself accepts around the braces/parens.
_TOJSON_NEEDS_EXPR = re.compile(r"\$\{\{\s*toJSON\(\s*needs\s*\)\s*\}\}")


def _load_verify_step() -> dict:
    """Parse ci.yml, find job `ci`, and return its "verify every gate
    passed" step (the full step mapping, including `run:` and any `env:`)."""
    with open(CI_YML, "r", encoding="utf-8") as f:
        workflow = yaml.safe_load(f)

    steps = workflow["jobs"]["ci"]["steps"]
    for step in steps:
        if step.get("name") == "verify every gate passed":
            return step

    raise AssertionError('step "verify every gate passed" not found in the ci job')


def _needs_json(results: dict[str, str]) -> str:
    """Build realistic, pretty-printed JSON shaped like GitHub Actions'
    own toJSON(needs) output: {"<job>": {"result": "...", "outputs": {}}}."""
    needs = {name: {"result": result, "outputs": {}} for name, result in results.items()}
    return json.dumps(needs, indent=2)


# The real needs: list from the ci job in ci.yml, so "one failure among 10
# successes" exercises the actual shape this step runs against in CI.
REAL_JOB_NAMES = [
    "backend", "crosscheck", "contracts", "sdk-python", "sdk-go",
    "sdk-java", "sdk-dotnet", "frontend", "stack", "register",
]


class TestCiAggregateGate(unittest.TestCase):

    @classmethod
    def setUpClass(cls) -> None:
        # Do not skip silently: a missing interpreter here means the test
        # proves nothing, so that must be loud, not a quiet pass.
        if shutil.which("bash") is None:
            raise AssertionError("bash is not available -- cannot verify the gate script, failing loudly")
        if shutil.which("python3") is None:
            raise AssertionError("python3 is not available -- the gate script requires it, failing loudly")
        cls.step = _load_verify_step()

    def _run(self, results: dict[str, str]) -> subprocess.CompletedProcess:
        """Run the step's script the way GitHub Actions actually would:
        any `${{ toJSON(needs) }}` expression literally inlined in the
        `run:` text is textually substituted with the raw JSON (GitHub's
        own expression-templating happens before the shell ever sees the
        script, which is exactly how the original bug's quote-breaking
        occurs), and any `env:` entry set to that same expression becomes a
        real environment variable instead -- never both skipped."""
        script = self.step["run"]
        needs_json = _needs_json(results)
        script = _TOJSON_NEEDS_EXPR.sub(lambda _m: needs_json, script)

        env = dict(os.environ)
        for key, value in self.step.get("env", {}).items():
            if _TOJSON_NEEDS_EXPR.fullmatch(value.strip()):
                env[key] = needs_json
            else:
                env[key] = value

        return subprocess.run(
            ["bash", "-c", script],
            env=env,
            capture_output=True,
            text=True,
        )

    def test_all_success_passes(self):
        results = {name: "success" for name in REAL_JOB_NAMES}
        proc = self._run(results)
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)

    def test_one_failure_fails(self):
        proc = self._run({"backend": "failure", "frontend": "success"})
        self.assertEqual(proc.returncode, 1, proc.stdout + proc.stderr)
        self.assertIn("backend", proc.stdout)
        self.assertIn("failure", proc.stdout)

    def test_one_skipped_fails(self):
        proc = self._run({"backend": "skipped", "frontend": "success"})
        self.assertEqual(proc.returncode, 1, proc.stdout + proc.stderr)
        self.assertIn("backend", proc.stdout)
        self.assertIn("skipped", proc.stdout)

    def test_one_cancelled_fails(self):
        proc = self._run({"backend": "cancelled", "frontend": "success"})
        self.assertEqual(proc.returncode, 1, proc.stdout + proc.stderr)
        self.assertIn("backend", proc.stdout)
        self.assertIn("cancelled", proc.stdout)

    def test_one_failure_among_ten_successes_names_the_job(self):
        results = {name: "success" for name in REAL_JOB_NAMES}
        results["sdk-go"] = "failure"
        proc = self._run(results)
        self.assertEqual(proc.returncode, 1, proc.stdout + proc.stderr)
        self.assertIn("sdk-go", proc.stdout)
        self.assertIn("failure", proc.stdout)
        # Every other job is "success" and must not be named as a failure.
        for name in REAL_JOB_NAMES:
            if name == "sdk-go":
                continue
            self.assertNotIn(f"{name}=", proc.stdout)


if __name__ == "__main__":
    unittest.main()
