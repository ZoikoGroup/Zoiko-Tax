"""Tests for tools/step10_demo.py.

Self-contained: no imports from ztax_gateway, no pytest or go execution,
no backend/ file reads.  Runs in under 1 second.

Loading strategy: importlib.util.spec_from_file_location so the tool can
live in tools/ (not on sys.path) and remain unimported by src/.
"""

from __future__ import annotations

import ast
import importlib.util
import sys
import types
from pathlib import Path

# ---------------------------------------------------------------------------
# Load the module under test
# ---------------------------------------------------------------------------

_TOOLS_DIR = Path(__file__).resolve().parents[1] / "tools"
_DEMO_PATH = _TOOLS_DIR / "step10_demo.py"
_BOUNDARY_PATH = Path(__file__).resolve().parent / "test_ai_boundary_proof.py"


def _load_demo() -> types.ModuleType:
    spec = importlib.util.spec_from_file_location("step10_demo", _DEMO_PATH)
    assert spec is not None and spec.loader is not None
    mod = importlib.util.module_from_spec(spec)
    # Register before exec so dataclasses can resolve cls.__module__ in sys.modules.
    sys.modules["step10_demo"] = mod
    try:
        spec.loader.exec_module(mod)
    except Exception:
        del sys.modules["step10_demo"]
        raise
    return mod


_demo = _load_demo()

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _classes_in_boundary() -> dict[str, list[str]]:
    """Parse test_ai_boundary_proof.py and return {class_name: [method_names]}."""
    source = _BOUNDARY_PATH.read_text(encoding="utf-8")
    tree = ast.parse(source)
    result: dict[str, list[str]] = {}
    for node in ast.walk(tree):
        if isinstance(node, ast.ClassDef) and node.name.startswith("Test"):
            methods = [
                n.name
                for n in node.body
                if isinstance(n, ast.FunctionDef) and n.name.startswith("test")
            ]
            result[node.name] = methods
    return result


# ---------------------------------------------------------------------------
# Claim structure
# ---------------------------------------------------------------------------


class TestClaimIds:
    def test_ids_are_c1_to_c9(self) -> None:
        ids = [c.id for c in _demo.CLAIMS]
        assert ids == ["C1", "C2", "C3", "C4", "C5", "C6", "C7", "C8", "C9"]

    def test_ids_are_unique(self) -> None:
        ids = [c.id for c in _demo.CLAIMS]
        assert len(ids) == len(set(ids))

    def test_every_claim_has_text(self) -> None:
        for claim in _demo.CLAIMS:
            assert claim.text.strip(), f"{claim.id} has empty text"

    def test_every_claim_has_playbook_ref(self) -> None:
        for claim in _demo.CLAIMS:
            assert claim.playbook_ref.strip(), f"{claim.id} has no playbook_ref"

    def test_c8_text_exact(self) -> None:
        c8 = next(c for c in _demo.CLAIMS if c.id == "C8")
        expected = (
            "With the model provider disabled, approved deterministic quotes and"
            " commits continue."
        )
        assert c8.text == expected

    def test_c9_text_exact(self) -> None:
        c9 = next(c for c in _demo.CLAIMS if c.id == "C9")
        expected = (
            "With the model provider disabled, new ambiguous mappings enter human"
            " review."
        )
        assert c9.text == expected


# ---------------------------------------------------------------------------
# C9 can never be PASS
# ---------------------------------------------------------------------------


class TestC9NeverPass:
    def test_c9_kind_is_none(self) -> None:
        c9 = next(c for c in _demo.CLAIMS if c.id == "C9")
        assert c9.kind == "none"

    def test_decide_status_cannot_produce_c9_pass(self) -> None:
        """decide_status works on exit codes, which C9 never produces (kind=none)."""
        # Any exit code given to decide_status could theoretically yield PASS,
        # but C9 is never routed through decide_status because kind == "none".
        c9 = next(c for c in _demo.CLAIMS if c.id == "C9")
        assert c9.kind != "python" and c9.kind != "go"

    def test_c9_status_is_not_implemented_in_main(self) -> None:
        """main(--dry-run) never shows PASS for C9."""
        import io
        from contextlib import redirect_stdout

        buf = io.StringIO()
        with redirect_stdout(buf):
            ret = _demo.main(["--dry-run"])
        assert ret == 0
        # dry-run shows every row as NOT RUN; C9 must NOT show PASS
        output = buf.getvalue()
        # Find the C9 line
        for line in output.splitlines():
            if "C9" in line:
                assert _demo.STATUS_PASS not in line, f"C9 showed PASS: {line!r}"


# ---------------------------------------------------------------------------
# decide_status
# ---------------------------------------------------------------------------


class TestDecideStatus:
    def test_zero_is_pass(self) -> None:
        assert _demo.decide_status(0) == _demo.STATUS_PASS

    def test_one_is_fail(self) -> None:
        assert _demo.decide_status(1) == _demo.STATUS_FAIL

    def test_two_is_fail(self) -> None:
        assert _demo.decide_status(2) == _demo.STATUS_FAIL

    def test_five_is_fail(self) -> None:
        # Exit code 5 = "no tests collected" -- not a passing run.
        assert _demo.decide_status(5) == _demo.STATUS_FAIL


# ---------------------------------------------------------------------------
# go_status
# ---------------------------------------------------------------------------


class TestGoStatus:
    _names: tuple[str, ...] = ("TestFoo", "TestBar")

    def _pass_output(self) -> str:
        return "--- PASS: TestFoo (0.00s)\n--- PASS: TestBar (0.00s)\nok  pkg\n"

    def test_all_names_present_is_pass(self) -> None:
        assert _demo.go_status(self._pass_output(), 0, self._names) == _demo.STATUS_PASS

    def test_one_name_missing_is_fail(self) -> None:
        out = "--- PASS: TestFoo (0.00s)\nok  pkg\n"
        assert _demo.go_status(out, 0, self._names) == _demo.STATUS_FAIL

    def test_no_tests_to_run_is_fail(self) -> None:
        out = "testing: warning: no tests to run\nPASS\nok  pkg\n"
        assert _demo.go_status(out, 0, self._names) == _demo.STATUS_FAIL

    def test_nonzero_exit_is_fail(self) -> None:
        assert _demo.go_status(self._pass_output(), 1, self._names) == _demo.STATUS_FAIL

    def test_empty_names_zero_exit_is_pass(self) -> None:
        # Edge: no expected names, exit 0 -> PASS (nothing to verify missing)
        assert _demo.go_status("ok  pkg\n", 0, ()) == _demo.STATUS_PASS


# ---------------------------------------------------------------------------
# Python node ids vs test_ai_boundary_proof.py
# ---------------------------------------------------------------------------


class TestPythonNodeIds:
    def test_all_python_nodes_name_existing_classes(self) -> None:
        known = _classes_in_boundary()
        for claim in _demo.CLAIMS:
            if claim.kind != "python":
                continue
            for node in claim.python_nodes:
                # node has the form "tests/test_ai_boundary_proof.py::ClassName"
                class_name = node.split("::")[-1]
                assert class_name in known, (
                    f"{claim.id}: node {node!r} references class {class_name!r}"
                    " which does not exist in test_ai_boundary_proof.py"
                )

    def test_all_referenced_classes_have_at_least_one_test_method(self) -> None:
        known = _classes_in_boundary()
        for claim in _demo.CLAIMS:
            if claim.kind != "python":
                continue
            for node in claim.python_nodes:
                class_name = node.split("::")[-1]
                methods = known.get(class_name, [])
                assert methods, (
                    f"{claim.id}: class {class_name!r} exists but has no test methods"
                )


# ---------------------------------------------------------------------------
# render_table
# ---------------------------------------------------------------------------


class TestRenderTable:
    def _fake_results(self) -> list[tuple[object, str, list[str]]]:
        return [(c, _demo.STATUS_NOT_RUN, []) for c in _demo.CLAIMS]

    def test_table_contains_every_claim_id(self) -> None:
        table = _demo.render_table(self._fake_results())
        for claim in _demo.CLAIMS:
            assert claim.id in table

    def test_table_is_deterministic(self) -> None:
        results = self._fake_results()
        assert _demo.render_table(results) == _demo.render_table(results)

    def test_table_never_shows_pass_for_not_run_row(self) -> None:
        table = _demo.render_table(self._fake_results())
        # No row should show PASS when status is NOT_RUN
        for line in table.splitlines():
            if any(c.id in line for c in _demo.CLAIMS):
                assert _demo.STATUS_PASS not in line, (
                    f"Found PASS in a NOT_RUN row: {line!r}"
                )


# ---------------------------------------------------------------------------
# main --dry-run
# ---------------------------------------------------------------------------


class TestDryRun:
    def test_dry_run_returns_zero(self) -> None:
        import io
        from contextlib import redirect_stdout

        buf = io.StringIO()
        with redirect_stdout(buf):
            ret = _demo.main(["--dry-run"])
        assert ret == 0

    def test_dry_run_shows_all_nine_claim_ids(self) -> None:
        import io
        from contextlib import redirect_stdout

        buf = io.StringIO()
        with redirect_stdout(buf):
            _demo.main(["--dry-run"])
        output = buf.getvalue()
        for claim in _demo.CLAIMS:
            assert claim.id in output, f"Claim {claim.id} not found in dry-run output"


# ---------------------------------------------------------------------------
# C8 Go test name constants (no file access -- constants only)
# ---------------------------------------------------------------------------


class TestC8GoTestNames:
    def test_app_test_name_1(self) -> None:
        assert "TestDeterministicFlowIndependentOfGateway" in _demo.C8_APP_TESTS

    def test_app_test_name_2(self) -> None:
        assert "TestDeterminationServiceTakesNoModelGateway" in _demo.C8_APP_TESTS

    def test_c8_go_tests_includes_app_tests(self) -> None:
        c8 = next(c for c in _demo.CLAIMS if c.id == "C8")
        for name in _demo.C8_APP_TESTS:
            assert name in c8.go_tests


# ---------------------------------------------------------------------------
# --json flag produces valid JSON
# ---------------------------------------------------------------------------


class TestJsonOutput:
    def test_json_flag_produces_valid_json(self) -> None:
        import io
        from contextlib import redirect_stdout

        buf = io.StringIO()
        with redirect_stdout(buf):
            _demo.main(["--dry-run", "--json"])
        data = __import__("json").loads(buf.getvalue())
        assert isinstance(data, list)
        assert len(data) == 9
        for item in data:
            assert "id" in item
            assert "text" in item
            assert "status" in item
