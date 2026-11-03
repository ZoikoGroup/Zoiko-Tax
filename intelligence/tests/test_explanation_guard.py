"""Tests for ztax_gateway.explanation_guard -- no fixture, no network, no Gateway.

These tests import directly from the governed src/ module (not from the demo
tool), confirming that the guard is self-contained and has no dependency on
report_fixture or any demo code.

Coverage notes
--------------
* test_report_ai_http.py already covers the guard exhaustively through the
  demo shim.  This file adds:

  1. Direct import from ``ztax_gateway.explanation_guard`` (the production path).
  2. The stale-status test demanded by the WP6 prompt: a cited row whose
     ``status`` field is ``"PENDING"`` while the explanation says ``"FILED"``
     → ``GUARD_STATUS_MISMATCH``.
  3. Explicit tests for the ``invocation_id`` keyword (plumbing for WP5).
  4. Basic smoke tests ensuring each reason code is reachable from this module.
  5. Parity test: guard's lifecycle state list matches the demo fixture (Gap 3).
  6. Alignment test: all GUARD_* constants are members of CheckReason (Gap 5).
  7. GuardResult / check_guard structured-result tests (Gap 6).

What is deliberately NOT done in this PR
-----------------------------------------
* No ``AnswerCheckedEvent`` is emitted -- that is WP5 (wired after the
  kill-switch PR lands).
* ``report_ai_http.py`` is not changed beyond the import-only swap.
* ``server.py``, ``governance.py``, ``invocation_evidence.py``,
  ``contracts/schemas/ai`` are not touched.
* ``Gateway.invoke()`` does not call the guard -- wiring happens in WP5.
"""

from __future__ import annotations

import importlib.util
import sys
from pathlib import Path
from types import ModuleType

from ztax_gateway.explanation_guard import (
    _LIFECYCLE_STATES,
    GUARD_FOREIGN_CITATION,
    GUARD_NO_CITATION,
    GUARD_NUMBER_NOT_IN_ROWS,
    GUARD_STATUS_MISMATCH,
    GuardResult,
    apply_guard,
    check_guard,
)

# ---------------------------------------------------------------------------
# Fixtures used across tests -- pure Python dicts, no demo fixture dependency.
# ---------------------------------------------------------------------------

_ROWS = [
    {"id": "OBL-001", "amount": "1233.38", "status": "PAID"},
    {"id": "OBL-002", "amount": "7321.06", "status": "FILED"},
]

_ROWS_NO_STATUS = [
    {"id": "FIG-001", "value": "1443155.15"},
    {"id": "FIG-002", "value": "98.7"},
]

_DATE_ROW = [{"id": "OBL-022", "amount": "182500.00", "status": "PAID", "due_date": "2026-10-31"}]


# ---------------------------------------------------------------------------
# 1. Smoke: pass path.
# ---------------------------------------------------------------------------


def test_pass_returns_true_and_empty_reason() -> None:
    ok, reason = apply_guard(_ROWS, ["OBL-001"], "OBL-001 amount is 1233.38.")
    assert ok is True
    assert reason == ""


def test_pass_with_multiple_cited_rows() -> None:
    ok, reason = apply_guard(
        _ROWS, ["OBL-001", "OBL-002"], "OBL-001 is 1233.38 and OBL-002 is 7321.06."
    )
    assert ok is True, reason


# ---------------------------------------------------------------------------
# 2. GUARD_NO_CITATION -- no cited ids at all.
# ---------------------------------------------------------------------------


def test_no_citation_returns_guard_no_citation() -> None:
    ok, reason = apply_guard(_ROWS, [], "OBL-001 amount is 1233.38.")
    assert ok is False
    assert reason == "the explanation cites no row id"


def test_reason_code_constant_matches_string_in_reason_text() -> None:
    """GUARD_NO_CITATION is only the code; the reason text is separate."""
    ok, reason = apply_guard(_ROWS, [], "x")
    assert not ok
    # The code is returned via _check but apply_guard returns the reason *text*.
    assert "cites no row" in reason


# ---------------------------------------------------------------------------
# 3. GUARD_FOREIGN_CITATION -- cited id not present in rows.
# ---------------------------------------------------------------------------


def test_foreign_citation_returns_guard_foreign_citation() -> None:
    ok, reason = apply_guard(_ROWS, ["OBL-999"], "OBL-999 is relevant.")
    assert ok is False
    assert "OBL-999" in reason


def test_reason_code_for_foreign_is_in_constant() -> None:
    """Ensure the module exposes the constant and it matches the shim value."""
    assert GUARD_FOREIGN_CITATION == "GUARD_FOREIGN_CITATION"


# ---------------------------------------------------------------------------
# 4. GUARD_STATUS_MISMATCH -- the stale-status test required by the prompt.
#
#    The cited row has status "PAID"; the explanation says "FILED".
#    → guard must reject with GUARD_STATUS_MISMATCH.
# ---------------------------------------------------------------------------


def test_stale_status_returns_guard_status_mismatch() -> None:
    """Core WP6 requirement: a cited row's status differs from the status word used."""
    row = {"id": "OBL-001", "amount": "1233.38", "status": "PAID"}
    ok, reason = apply_guard([row], ["OBL-001"], "OBL-001 is FILED.")
    assert ok is False
    assert "FILED" in reason


def test_stale_status_lower_case_also_rejected() -> None:
    """Lower-case status words in _ANY_CASE_STATUS_WORDS are caught too."""
    row = {"id": "OBL-001", "amount": "1233.38", "status": "PAID"}
    ok, reason = apply_guard([row], ["OBL-001"], "OBL-001 is overdue.")
    assert ok is False
    assert "OVERDUE" in reason


def test_correct_status_passes() -> None:
    """Guard accepts the status the cited row actually holds."""
    row = {"id": "OBL-001", "amount": "1233.38", "status": "PAID"}
    ok, reason = apply_guard([row], ["OBL-001"], "OBL-001 is PAID.")
    assert ok is True, reason


def test_status_of_uncited_row_does_not_excuse_status_word() -> None:
    """A status that belongs to a supplied-but-uncited row must not pass."""
    paid_row = {"id": "OBL-001", "amount": "100.00", "status": "PAID"}
    overdue_row = {"id": "OBL-002", "amount": "200.00", "status": "OVERDUE"}
    # OBL-001 is cited; OBL-002 is not; saying OVERDUE must fail.
    ok, _ = apply_guard([paid_row, overdue_row], ["OBL-001"], "OBL-001 is OVERDUE.")
    assert ok is False


def test_rows_without_status_field_skip_status_check() -> None:
    """Rows with no 'status' key (figures, trend points) skip the status check."""
    ok, reason = apply_guard(_ROWS_NO_STATUS, ["FIG-001"], "FIG-001 value is FILED.")
    # "FILED" is in the status word list, but no cited row has a status field
    # -- so the check is skipped and the guard passes on the number side.
    # (No number other than "FILED" present and FIG-001 has "1443155.15".)
    # "FILED" is not a number so there is no number violation either.
    assert ok is True, reason


# ---------------------------------------------------------------------------
# 5. GUARD_NUMBER_NOT_IN_ROWS -- invented number.
# ---------------------------------------------------------------------------


def test_invented_number_returns_guard_number_not_in_rows() -> None:
    ok, reason = apply_guard(_ROWS, ["OBL-001"], "OBL-001 is 999999.99.")
    assert ok is False
    assert "999999.99" in reason


def test_reason_code_constant_value() -> None:
    assert GUARD_NUMBER_NOT_IN_ROWS == "GUARD_NUMBER_NOT_IN_ROWS"


# ---------------------------------------------------------------------------
# 6. invocation_id keyword -- plumbing for WP5 (not used yet).
# ---------------------------------------------------------------------------


def test_invocation_id_kwarg_accepted_without_error() -> None:
    """Passing invocation_id must not raise; behaviour is unchanged."""
    ok, reason = apply_guard(
        _ROWS,
        ["OBL-001"],
        "OBL-001 is 1233.38.",
        invocation_id="abc123",
    )
    assert ok is True, reason


def test_invocation_id_none_is_default() -> None:
    """Omitting invocation_id is equivalent to None -- no error."""
    ok, reason = apply_guard(_ROWS, ["OBL-001"], "OBL-001 is 1233.38.")
    assert ok is True, reason


def test_invocation_id_does_not_change_guard_outcome_on_failure() -> None:
    """Providing an invocation_id must not suppress a rejection."""
    ok, reason = apply_guard(
        _ROWS, ["OBL-001"], "OBL-001 is 999.00.", invocation_id="xyz"
    )
    assert ok is False
    assert "999.00" in reason


# ---------------------------------------------------------------------------
# 7. Module-level constant exports.
# ---------------------------------------------------------------------------


def test_reason_code_constants_are_exported() -> None:
    """All four GUARD_* constants must be importable from the module."""
    assert GUARD_NO_CITATION == "GUARD_NO_CITATION"
    assert GUARD_FOREIGN_CITATION == "GUARD_FOREIGN_CITATION"
    assert GUARD_STATUS_MISMATCH == "GUARD_STATUS_MISMATCH"
    assert GUARD_NUMBER_NOT_IN_ROWS == "GUARD_NUMBER_NOT_IN_ROWS"


# ---------------------------------------------------------------------------
# 8. No dependency on demo fixture -- smoke.
# ---------------------------------------------------------------------------


def test_guard_has_no_report_fixture_dependency() -> None:
    """apply_guard works on arbitrary dicts with no demo fixture dependency."""
    # (report_fixture may be in sys.modules from sibling tests -- that is fine.
    # The point is that explanation_guard does not import it itself.)
    row = {"id": "X-1", "value": "42.00"}
    ok, reason = apply_guard([row], ["X-1"], "X-1 value is 42.00.")
    assert ok is True, reason


# ---------------------------------------------------------------------------
# 9. Gap 3: lifecycle states parity -- guard list must match the fixture.
#
# The guard inlines _LIFECYCLE_STATES from ZTAX-OBL-001 ch15 (no import from
# tools/).  The fixture independently declares the same list.  This test loads
# report_fixture via importlib (the same way test_report_ai_http.py does) and
# asserts the two lists are identical, so any change to either side fails here.
# ---------------------------------------------------------------------------

_TOOLS_DIR = Path(__file__).resolve().parent.parent / "tools"


def _load_report_fixture() -> ModuleType:
    """Load report_fixture.py without registering it in sys.modules globally."""
    fixture_deps = ("groq_demo_runtime", "report_fixture", "serve_live_demo", "report_ai_http")
    before = {n: sys.modules.get(n) for n in fixture_deps}
    try:
        for name in ("groq_demo_runtime", "serve_live_demo"):
            spec = importlib.util.spec_from_file_location(name, _TOOLS_DIR / f"{name}.py")
            assert spec and spec.loader
            mod = importlib.util.module_from_spec(spec)
            sys.modules[name] = mod
            spec.loader.exec_module(mod)
        fixture_path = _TOOLS_DIR / "report_fixture.py"
        spec = importlib.util.spec_from_file_location("report_fixture", fixture_path)
        assert spec and spec.loader
        fixture_mod = importlib.util.module_from_spec(spec)
        sys.modules["report_fixture"] = fixture_mod
        spec.loader.exec_module(fixture_mod)
        return fixture_mod
    finally:
        for name, previous in before.items():
            if previous is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = previous


def test_guard_lifecycle_states_match_fixture() -> None:
    """Guard's inlined _LIFECYCLE_STATES must exactly match report_fixture.LIFECYCLE_STATES.

    The guard cannot import from tools/ (governed code must not depend on demo
    tooling), so it inlines the same constant from ZTAX-OBL-001 ch15.  This test
    prevents the two lists from drifting apart silently.
    """
    fixture = _load_report_fixture()
    assert list(_LIFECYCLE_STATES) == list(fixture.LIFECYCLE_STATES), (
        "Guard's _LIFECYCLE_STATES differs from report_fixture.LIFECYCLE_STATES -- "
        "update explanation_guard.py to match ZTAX-OBL-001 ch15"
    )


# ---------------------------------------------------------------------------
# 10. Gap 5: reason-code alignment -- every GUARD_* constant must be a
#     member of CheckReason in invocation_check_event.py.  A rename on
#     either side now fails immediately rather than silently in production.
# ---------------------------------------------------------------------------


def test_guard_reason_codes_are_all_members_of_check_reason() -> None:
    """Every GUARD_* constant exported by explanation_guard must be a CheckReason value.

    This test couples the two modules explicitly: if anyone renames a reason code
    in invocation_check_event.CheckReason or in explanation_guard, this test fails
    immediately, preventing a silent mismatch that would break WP5's event emission.
    """
    from ztax_gateway.invocation_check_event import CheckReason

    check_reason_values = {r.value for r in CheckReason}
    guard_codes = {
        GUARD_NO_CITATION,
        GUARD_FOREIGN_CITATION,
        GUARD_STATUS_MISMATCH,
        GUARD_NUMBER_NOT_IN_ROWS,
    }
    missing = guard_codes - check_reason_values
    assert not missing, (
        f"GUARD_* constants not in CheckReason: {missing!r}. "
        "Update invocation_check_event.CheckReason or explanation_guard constants."
    )


# ---------------------------------------------------------------------------
# 11. Gap 6: GuardResult and check_guard structured-result tests.
#
# apply_guard returns (bool, str) for backward compatibility.  check_guard
# returns GuardResult with .ok, .code, and .message, so WP5 can emit an
# AnswerCheckedEvent(reason_code=result.code) without parsing free text.
# ---------------------------------------------------------------------------


def test_check_guard_pass_returns_guard_result_with_empty_code_and_message() -> None:
    result = check_guard(_ROWS, ["OBL-001"], "OBL-001 amount is 1233.38.")
    assert isinstance(result, GuardResult)
    assert result.ok is True
    assert result.code == ""
    assert result.message == ""


def test_check_guard_no_citation_gives_code_and_message() -> None:
    result = check_guard(_ROWS, [], "OBL-001 is 1233.38.")
    assert result.ok is False
    assert result.code == GUARD_NO_CITATION
    assert "cites no row" in result.message


def test_check_guard_foreign_citation_gives_code_and_message() -> None:
    result = check_guard(_ROWS, ["OBL-999"], "OBL-999 is relevant.")
    assert result.ok is False
    assert result.code == GUARD_FOREIGN_CITATION
    assert "OBL-999" in result.message


def test_check_guard_status_mismatch_gives_code_and_message() -> None:
    row = {"id": "OBL-001", "amount": "1233.38", "status": "PAID"}
    result = check_guard([row], ["OBL-001"], "OBL-001 is FILED.")
    assert result.ok is False
    assert result.code == GUARD_STATUS_MISMATCH
    assert "FILED" in result.message


def test_check_guard_invented_number_gives_code_and_message() -> None:
    result = check_guard(_ROWS, ["OBL-001"], "OBL-001 is 999999.99.")
    assert result.ok is False
    assert result.code == GUARD_NUMBER_NOT_IN_ROWS
    assert "999999.99" in result.message


def test_guard_result_is_truthy_on_pass() -> None:
    result = check_guard(_ROWS, ["OBL-001"], "OBL-001 amount is 1233.38.")
    assert bool(result) is True


def test_guard_result_is_falsy_on_failure() -> None:
    result = check_guard(_ROWS, [], "x")
    assert bool(result) is False


def test_guard_result_two_element_unpacking_still_works() -> None:
    """GuardResult.__iter__ yields (ok, message) so old callers don't break."""
    result_pass = check_guard(_ROWS, ["OBL-001"], "OBL-001 amount is 1233.38.")
    assert result_pass.ok is True
    assert result_pass.message == ""

    result_fail = check_guard(_ROWS, [], "OBL-001 is 1233.38.")
    assert result_fail.ok is False
    assert "cites no row" in result_fail.message

    # Old-style two-element unpacking via __iter__ still resolves correctly:
    items_pass = list(result_pass)
    assert items_pass[0] is True   # ok
    assert items_pass[1] == ""     # message

    items_fail = list(result_fail)
    assert items_fail[0] is False
    assert "cites no row" in str(items_fail[1])


def test_apply_guard_result_matches_check_guard_result() -> None:
    """apply_guard and check_guard must always agree on ok and message."""
    cases = [
        (_ROWS, ["OBL-001"], "OBL-001 amount is 1233.38."),
        (_ROWS, [], "OBL-001 is 1233.38."),
        (_ROWS, ["OBL-999"], "OBL-999 is relevant."),
        (_ROWS, ["OBL-001"], "OBL-001 is 999999.99."),
    ]
    for rows, cited_ids, explanation in cases:
        ok, msg = apply_guard(rows, cited_ids, explanation)
        result = check_guard(rows, cited_ids, explanation)
        assert ok == result.ok
        assert msg == result.message


def test_check_guard_invocation_id_kwarg_accepted() -> None:
    result = check_guard(_ROWS, ["OBL-001"], "OBL-001 is 1233.38.", invocation_id="abc")
    assert result.ok is True
