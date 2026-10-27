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


def _ast_info(path: Path) -> tuple[dict[str, list[str]], list[str]]:
    """Parse a test file.

    Return ({class_name: [test method names]}, [top-level test function
    names]) -- a node id's last segment is either a class (C1-C9's boundary
    proof classes) or a bare function (C10's test_t4_prohibition.py nodes,
    which are plain parametrized functions with no enclosing class).
    """
    source = path.read_text(encoding="utf-8")
    tree = ast.parse(source)
    classes: dict[str, list[str]] = {}
    functions: list[str] = []
    for node in tree.body:
        if isinstance(node, ast.ClassDef) and node.name.startswith("Test"):
            classes[node.name] = [
                n.name
                for n in node.body
                if isinstance(n, ast.FunctionDef) and n.name.startswith("test")
            ]
        elif isinstance(node, ast.FunctionDef) and node.name.startswith("test"):
            functions.append(node.name)
    return classes, functions


def _classes_in_boundary() -> dict[str, list[str]]:
    """Parse test_ai_boundary_proof.py and return {class_name: [method_names]}."""
    classes, _functions = _ast_info(_BOUNDARY_PATH)
    return classes


def _resolvable_test_names(path: Path) -> set[str]:
    """Names (class or top-level function) usable as `path.py::NAME`, limited
    to names that would actually select at least one test."""
    classes, functions = _ast_info(path)
    names = set(functions)
    names.update(cls for cls, methods in classes.items() if methods)
    return names


# ---------------------------------------------------------------------------
# Claim structure
# ---------------------------------------------------------------------------


class TestClaimIds:
    def test_ids_are_c1_to_c10(self) -> None:
        ids = [c.id for c in _demo.CLAIMS]
        assert ids == ["C1", "C2", "C3", "C4", "C5", "C6", "C7", "C8", "C9", "C10"]

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
# C9 can never be PASS -- it reports PARTIAL on success, never PASS, because
# its tests prove less than the claim's full text.
# ---------------------------------------------------------------------------


class TestC9NeverPass:
    def test_c9_kind_is_go(self) -> None:
        c9 = next(c for c in _demo.CLAIMS if c.id == "C9")
        assert c9.kind == "go"

    def test_c9_success_status_is_partial_not_pass(self) -> None:
        c9 = next(c for c in _demo.CLAIMS if c.id == "C9")
        assert c9.success_status == _demo.STATUS_PARTIAL
        assert c9.success_status != _demo.STATUS_PASS

    def test_c9_has_a_note_explaining_its_scope(self) -> None:
        c9 = next(c for c in _demo.CLAIMS if c.id == "C9")
        assert c9.note.strip(), "C9 must say what it does and does not prove"

    def test_c9_status_never_shows_pass_in_dry_run(self) -> None:
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
    def test_all_python_nodes_resolve_to_a_real_test(self) -> None:
        """Every python-kind node id names either an existing boundary-proof
        class with at least one test method (C1-C9's shape), or an existing
        top-level test function (C10's shape, test_t4_prohibition.py)."""
        for claim in _demo.CLAIMS:
            if claim.kind != "python":
                continue
            for node in claim.python_nodes:
                # node has the form "tests/<file>.py::<ClassOrFunctionName>"
                file_part, name = node.split("::")
                path = _TOOLS_DIR.parent / file_part
                assert path.exists(), f"{claim.id}: {file_part!r} does not exist"
                resolvable = _resolvable_test_names(path)
                assert name in resolvable, (
                    f"{claim.id}: node {node!r} does not resolve to an existing"
                    f" function or a test class with at least one test method"
                    f" in {file_part}"
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

    def test_dry_run_shows_every_claim_id(self) -> None:
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
# C9 Go test name constants (no file access -- constants only)
# ---------------------------------------------------------------------------


class TestC9GoTestNames:
    def test_app_test_name(self) -> None:
        assert _demo.C9_APP_TESTS == ("TestProposalGoesToReviewWhenTheAIPlaneCannotHelp",)

    def test_http_test_name(self) -> None:
        assert _demo.C9_HTTP_TESTS == ("TestProposeClassificationReviewRequired",)

    def test_c9_go_tests_includes_both(self) -> None:
        c9 = next(c for c in _demo.CLAIMS if c.id == "C9")
        for name in _demo.C9_APP_TESTS + _demo.C9_HTTP_TESTS:
            assert name in c9.go_tests


# ---------------------------------------------------------------------------
# C10 -- the T4 hard stop
# ---------------------------------------------------------------------------


class TestC10:
    def test_c10_exists_and_is_python(self) -> None:
        c10 = next(c for c in _demo.CLAIMS if c.id == "C10")
        assert c10.kind == "python"

    def test_c10_nodes_point_at_t4_prohibition_file(self) -> None:
        c10 = next(c for c in _demo.CLAIMS if c.id == "C10")
        assert c10.python_nodes, "C10 must name at least one test node"
        for node in c10.python_nodes:
            assert node.startswith("tests/test_t4_prohibition.py::"), node

    def test_c10_names_the_two_required_tests(self) -> None:
        c10 = next(c for c in _demo.CLAIMS if c.id == "C10")
        names = {node.split("::")[-1] for node in c10.python_nodes}
        assert names == {
            "test_t4_refused_for_every_use_case_ceiling",
            "test_gateway_invoke_t4_runtime_never_reached",
        }


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
        assert len(data) == len(_demo.CLAIMS)
        for item in data:
            assert "id" in item
            assert "text" in item
            assert "status" in item
