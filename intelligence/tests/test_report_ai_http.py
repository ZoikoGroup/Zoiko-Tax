"""Unit tests for tools/report_ai_http.py -- no network, no key, no Docker.

DEMO ONLY source under test. These tests never call Groq: every Gateway
built below uses a stub ModelRuntime, never GroqDemoRuntime, and the one
HTTP-surface test binds 127.0.0.1 on an OS-assigned loopback port, not a
real network address.

tools/ is not on pythonpath (pyproject.toml's [tool.pytest.ini_options] sets
pythonpath = ["src"] only), so every module under test is loaded directly
from its file path via importlib -- report_ai_http.py's own top-level
``import report_fixture`` / ``import serve_live_demo`` / ``from
groq_demo_runtime import ...`` need those three names visible in sys.modules
while it is exec'd. They are visible only for that moment: the loader below
puts back whatever sys.modules held under those four names afterwards, so no
other test module sees them and import order between test files stops
mattering.
"""

from __future__ import annotations

import http.client
import importlib.util
import json
import logging
import sys
import threading
from pathlib import Path
from types import ModuleType
from typing import Any

import pytest

from ztax_gateway.runtime import Result, Route
from ztax_gateway.server import Gateway, load_config
from ztax_gateway.wire import Call

# ---------------------------------------------------------------------------
# Load groq_demo_runtime, report_fixture, serve_live_demo and report_ai_http
# by path, in dependency order. Each is registered in sys.modules only while
# the load runs (the next file's top-level import needs to find it), then
# sys.modules is put back exactly as it was for those four names -- nothing is
# left registered globally.
# ---------------------------------------------------------------------------

_TOOLS_DIR = Path(__file__).resolve().parent.parent / "tools"
_CONFIG_DIR = Path(__file__).resolve().parent.parent / "config"

_MODULE_NAMES = ("groq_demo_runtime", "report_fixture", "serve_live_demo", "report_ai_http")


def _load_modules() -> dict[str, ModuleType]:
    before = {name: sys.modules.get(name) for name in _MODULE_NAMES}
    loaded: dict[str, ModuleType] = {}
    try:
        for name in _MODULE_NAMES:
            spec = importlib.util.spec_from_file_location(name, _TOOLS_DIR / f"{name}.py")
            assert spec is not None and spec.loader is not None
            module = importlib.util.module_from_spec(spec)
            sys.modules[name] = module
            spec.loader.exec_module(module)
            loaded[name] = module
    finally:
        for name, previous in before.items():
            if previous is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = previous
    return loaded


_MODULES = _load_modules()
groq_demo_runtime = _MODULES["groq_demo_runtime"]
report_fixture = _MODULES["report_fixture"]
serve_live_demo = _MODULES["serve_live_demo"]
report_ai_http = _MODULES["report_ai_http"]

# ---------------------------------------------------------------------------
# A stub ModelRuntime -- never GroqDemoRuntime, never a network call. Each
# test configures what it returns via a closure or a small subclass.
# ---------------------------------------------------------------------------


class _ScriptedRuntime:
    """Returns one fixed Result (or raises one fixed exception) per call."""

    def __init__(self, result: Result | None = None, raises: Exception | None = None) -> None:
        self._result = result
        self._raises = raises
        self.calls: list[Call] = []

    def run(self, route: Route, call: Call) -> Result:
        self.calls.append(call)
        if self._raises is not None:
            raise self._raises
        assert self._result is not None
        return self._result


def _fields_result(
    explanation: str, cited_ids: list[str], abstain: bool, reason: str = ""
) -> Result:
    return Result(
        fields={
            "explanation": explanation,
            "cited_ids": json.dumps(cited_ids),
            "abstain": "true" if abstain else "false",
            "reason": reason,
        },
        confidence="0.5000",
    )


def _gateway(runtime: Any) -> Gateway:
    registry, routes = load_config(_CONFIG_DIR / "registry.live-demo.json")
    return Gateway(registry, routes, runtime, "local", "0.0.0-test")


_FIXTURE = report_fixture.generate_fixture()
_OBL_001_AMOUNT: str = report_fixture.rows_for_kind(_FIXTURE, "obligation-row", ["OBL-001"])[0][
    "amount"
]


def _row(kind: str, row_id: str) -> dict[str, Any]:
    rows: list[dict[str, Any]] = report_fixture.rows_for_kind(_FIXTURE, kind, [row_id])
    assert len(rows) == 1
    return rows[0]

# ---------------------------------------------------------------------------
# The deterministic guard, directly -- no Gateway, no fixture even.
# ---------------------------------------------------------------------------

_ROWS = [{"id": "OBL-001", "amount": "1233.38"}, {"id": "OBL-002", "amount": "7321.06"}]


def test_guard_accepts_a_value_present_in_the_rows() -> None:
    ok, reason = report_ai_http.apply_guard(_ROWS, ["OBL-001"], "OBL-001 is 1233.38.")
    assert ok
    assert reason == ""


def test_guard_rejects_a_cited_id_not_in_the_rows() -> None:
    ok, reason = report_ai_http.apply_guard(_ROWS, ["OBL-999"], "OBL-999 is great.")
    assert not ok
    assert "OBL-999" in reason


def test_guard_rejects_an_invented_number() -> None:
    ok, reason = report_ai_http.apply_guard(_ROWS, ["OBL-001"], "OBL-001 is 999999.99.")
    assert not ok
    assert "999999.99" in reason


def test_guard_does_not_mistake_a_row_ids_own_digits_for_a_number() -> None:
    """"OBL-001" contains "001"; citing the id is not a number claim."""
    ok, reason = report_ai_http.apply_guard(_ROWS, ["OBL-001"], "See OBL-001 for detail.")
    assert ok, reason


def test_guard_allows_a_recomputable_percentage_of_two_supplied_numbers() -> None:
    rows = [{"id": "A", "value": "50"}, {"id": "B", "value": "200"}]
    ok, reason = report_ai_http.apply_guard(rows, ["A", "B"], "A is 25% of B.")
    assert ok, reason


def test_guard_rejects_a_percentage_that_does_not_recompute() -> None:
    rows = [{"id": "A", "value": "50"}, {"id": "B", "value": "200"}]
    ok, reason = report_ai_http.apply_guard(rows, ["A", "B"], "A is 90% of B.")
    assert not ok
    assert "90%" in reason


def test_guard_accepts_multiple_valid_numbers_and_citations() -> None:
    ok, _ = report_ai_http.apply_guard(
        _ROWS, ["OBL-001", "OBL-002"], "OBL-001 is 1233.38 and OBL-002 is 7321.06."
    )
    assert ok


# --- the guard on the new row types (payments, trend, derived figures) ------


def test_guard_accepts_a_payments_linked_id_and_date_quoted_verbatim() -> None:
    pay = _row("payment-row", "PAY-001")
    text = (
        f"{pay['id']} paid {pay['obligation_id']} {pay['amount']} {pay['currency']} "
        f"on {pay['date']}; status {pay['status']}."
    )
    ok, reason = report_ai_http.apply_guard([pay], [pay["id"]], text)
    assert ok, reason


def test_guard_still_rejects_a_date_the_rows_do_not_hold() -> None:
    pay = _row("payment-row", "PAY-001")
    ok, reason = report_ai_http.apply_guard([pay], [pay["id"]], f"{pay['id']} paid on 2027-12-01.")
    assert not ok
    assert "2027" in reason


def test_guard_still_rejects_an_invented_amount_beside_verbatim_row_strings() -> None:
    pay = _row("payment-row", "PAY-001")
    text = f"{pay['id']} for {pay['obligation_id']} was 99.99 on {pay['date']}."
    ok, reason = report_ai_http.apply_guard([pay], [pay["id"]], text)
    assert not ok
    assert "99.99" in reason


def test_guard_still_rejects_citing_a_linked_id_that_was_not_supplied() -> None:
    """A payment names its obligation, but only supplied rows may be cited."""
    pay = _row("payment-row", "PAY-001")
    ok, reason = report_ai_http.apply_guard([pay], [pay["obligation_id"]], "See the obligation.")
    assert not ok
    assert pay["obligation_id"] in reason


def test_guard_accepts_a_trend_month_quoted_verbatim() -> None:
    point = _row("trend-point", "TRD-006")
    text = f"{point['id']}: {point['month']} total tax assessed was {point['value']} USD."
    ok, reason = report_ai_http.apply_guard([point], [point["id"]], text)
    assert ok, reason


def test_guard_accepts_the_derived_vs_last_month_percentage() -> None:
    fig = _row("report-figure", "FIG-005")
    pct = fig["value"].lstrip("-")
    text = f"Total tax assessed changed by {pct}% vs last month ({fig['change_amount']} USD)."
    ok, reason = report_ai_http.apply_guard([fig], [fig["id"]], text)
    assert ok, reason


# --- thousands separators and currency symbols -------------------------------

_TOTAL = _row("report-figure", "FIG-001")
_OBL_022 = _row("obligation-row", "OBL-022")


def test_the_rows_these_formatting_tests_rely_on() -> None:
    assert _TOTAL["value"] == "1443155.15"
    assert (_OBL_022["amount"], _OBL_022["currency"]) == ("182500.00", "EUR")


@pytest.mark.parametrize(
    "written",
    ["1,443,155.15", "$1,443,155.15", "USD 1,443,155.15", "1443155.15", "US$1,443,155.15"],
)
def test_guard_reads_a_formatted_total_as_the_row_value(written: str) -> None:
    text = f"{_TOTAL['id']}: total tax assessed is {written}."
    ok, reason = report_ai_http.apply_guard([_TOTAL], [_TOTAL["id"]], text)
    assert ok, reason


@pytest.mark.parametrize("written", ["€182,500.00", "EUR 182,500.00", "182,500"])
def test_guard_reads_a_formatted_euro_amount_as_the_row_value(written: str) -> None:
    text = f"{_OBL_022['id']} was paid as {written}."
    ok, reason = report_ai_http.apply_guard([_OBL_022], [_OBL_022["id"]], text)
    assert ok, reason


@pytest.mark.parametrize(
    "invented",
    ["1,443,155.16", "$1,443,155.25", "USD 1,443,156.15", "$2,000,000.00", "€182,500.01"],
)
def test_guard_rejects_a_comma_formatted_invented_number(invented: str) -> None:
    rows = [_TOTAL, _OBL_022]
    text = f"{_TOTAL['id']} and {_OBL_022['id']} come to {invented}."
    ok, reason = report_ai_http.apply_guard(rows, [_TOTAL["id"], _OBL_022["id"]], text)
    assert not ok
    assert repr(invented.lstrip("$€USD ")) in reason


@pytest.mark.parametrize("malformed", ["1,44,3155.15", "14,43,155.15", "1443,155.15"])
def test_guard_does_not_read_malformed_grouping_as_the_row_value(malformed: str) -> None:
    """Wrong group sizes are not a thousands-separated number: the pieces are
    checked one by one, and they are not row values."""
    text = f"{_TOTAL['id']}: total tax assessed is {malformed}."
    ok, _ = report_ai_http.apply_guard([_TOTAL], [_TOTAL["id"]], text)
    assert not ok


# --- years, days and quarters that quote a supplied date, month or period -----
#
# OBL-022 carries due_date 2026-10-31, assessed_month 2026-10, period 2026-Q3.


def _guard_obl_022(text: str) -> tuple[bool, str]:
    result: tuple[bool, str] = report_ai_http.apply_guard([_OBL_022], [_OBL_022["id"]], text)
    return result


def test_the_dates_these_tests_rely_on() -> None:
    assert (_OBL_022["due_date"], _OBL_022["assessed_month"], _OBL_022["period"]) == (
        "2026-10-31", "2026-10", "2026-Q3",
    )


def test_guard_accepts_the_exact_sentence_the_live_model_wrote_for_obl_022() -> None:
    ok, reason = _guard_obl_022("OBL-022 assessed month is October 2026.")
    assert ok, reason


@pytest.mark.parametrize(
    "text",
    [
        "It is due on 31 October 2026.",
        "It is due on October 31, 2026.",
        "It is due on Oct 31, 2026.",
        "It is due on 31 of October 2026.",
        "Period Q3 2026.",
        "The 2026-Q3 period, quarter Q3 2026, is due October 31.",
    ],
)
def test_guard_accepts_dates_and_periods_that_quote_a_supplied_row(text: str) -> None:
    ok, reason = _guard_obl_022(text)
    assert ok, reason


@pytest.mark.parametrize(
    ("text", "bad"),
    [
        ("It is due in 2025.", "2025"),  # a year no supplied date has
        ("It is due on October 30, 2026.", "30"),  # right month, day not in a supplied date
        ("It is due on March 31, 2026.", "31"),  # day exists, but not in that month
        ("There are 10 obligations.", "10"),  # month digits are not a licence for a count
        ("There are 31 obligations.", "31"),  # likewise a day
        ("There are 3 obligations in Q3.", "3"),  # a bare 3 is not the quarter
        ("Period Q4 2026.", "4"),  # no supplied period is a Q4
        ("Assessed 2026.5 in total.", "2026.5"),  # a decimal is never a year
        ("Assessed 2,026 in total.", "2,026"),  # nor a grouped number
        ("Up 2026% on last month.", "2026%"),  # nor a percentage
    ],
)
def test_guard_still_rejects_anything_that_is_not_a_quote_of_a_supplied_date(
    text: str, bad: str
) -> None:
    ok, reason = _guard_obl_022(text)
    assert not ok
    assert repr(bad) in reason


def test_guard_does_not_excuse_a_year_or_day_when_no_date_was_supplied() -> None:
    rows = [{"id": "X-1", "label": "no dates here", "value": "5"}]
    ok, reason = report_ai_http.apply_guard(rows, ["X-1"], "It was 2026, on October 31.")
    assert not ok
    assert "2026" in reason


def test_guard_accepts_a_month_name_with_a_year_from_a_trend_point_but_not_a_day() -> None:
    point = _row("trend-point", "TRD-006")
    assert point["month"] == "2026-10"
    ok, reason = report_ai_http.apply_guard([point], [point["id"]], "Assessed in October 2026.")
    assert ok, reason
    ok, reason = report_ai_http.apply_guard([point], [point["id"]], "Assessed on October 31.")
    assert not ok  # a month string carries no day


# --- no sum / aggregate rule: the FIG-001 "equivalent total in EUR" case ------
#
# The live model wrote "the equivalent total in EUR (347,654.81 EUR)" for
# FIG-001. That number is the sum of the two EUR obligations. It was accepted
# -- not through any sum rule (there is none) but because FIG-001 then carried
# it as a field. The fixture no longer does, so the sentence is rejected for
# the FIG-001 row as the fixture now builds it.

_EQUIVALENT_SENTENCE = "the equivalent total in EUR (347,654.81 EUR)"
_EUR_SUM = "347654.81"


def test_the_fig_001_row_no_longer_carries_an_eur_total() -> None:
    assert "eur_amount_total" not in _TOTAL
    assert _EUR_SUM not in {v for v in _TOTAL.values() if isinstance(v, str)}


def test_guard_rejects_the_equivalent_eur_total_sentence_for_the_fig_001_row() -> None:
    ok, reason = report_ai_http.apply_guard([_TOTAL], [_TOTAL["id"]], _EQUIVALENT_SENTENCE)
    assert not ok
    assert "'347,654.81'" in reason


def test_guard_rejects_the_sentence_for_every_row_in_the_fixture() -> None:
    """No row anywhere holds that number, so no explain target can restate it."""
    for row in report_fixture.flatten_rows(_FIXTURE).values():
        ok, _ = report_ai_http.apply_guard([row], [row["id"]], _EQUIVALENT_SENTENCE)
        assert not ok, f"{row['id']} would let the EUR total through"


def test_guard_accepts_the_individual_eur_amounts_each_row_really_holds() -> None:
    """Rejecting the sum must not stop a row's own EUR amount being restated."""
    ok, reason = report_ai_http.apply_guard(
        [_OBL_022], ["OBL-022"], "OBL-022 is EUR 182,500.00, or 197,100.00 USD at 1.0800."
    )
    assert ok, reason


def test_guard_rejects_the_exact_sentence_computed_as_a_sum_of_the_two_eur_rows() -> None:
    """The same figure, derived by adding OBL-011 and OBL-022: no sum rule exists."""
    eur_rows = [_row("obligation-row", "OBL-011"), _OBL_022]
    assert sum(float(r["amount"]) for r in eur_rows) == pytest.approx(float(_EUR_SUM))
    ok, reason = report_ai_http.apply_guard(eur_rows, ["OBL-011", "OBL-022"], _EQUIVALENT_SENTENCE)
    assert not ok
    assert "'347,654.81'" in reason


@pytest.mark.parametrize(
    "text",
    ["together 35.00", "a subtotal of 35.00", "an average of 17.50", "a difference of 15.00"],
)
def test_guard_has_no_aggregate_rule_of_any_kind(text: str) -> None:
    # 35 = 10 + 25, 17.5 = their mean, 15 = their difference: none is a row value.
    rows = [{"id": "A-1", "value": "10.00"}, {"id": "A-2", "value": "25.00"}]
    ok, _ = report_ai_http.apply_guard(rows, ["A-1", "A-2"], text)
    assert not ok, "a sum / average / difference of supplied rows must not pass"


def test_guard_accepts_filing_compliance_as_a_recomputable_percentage() -> None:
    fig = _row("report-figure", "FIG-002")
    text = f"{fig['filed_count']} of {fig['total_filings']} filings are filed: {fig['value']}%."
    ok, reason = report_ai_http.apply_guard([fig], [fig["id"]], text)
    assert ok, reason


# --- the stricter guard: citations, exact amounts, status words --------------


def test_guard_rejects_a_status_that_contradicts_the_cited_row() -> None:
    """OBL-022 is PAID in the fixture; saying it is OVERDUE must abstain."""
    assert _OBL_022["status"] == "PAID"
    ok, reason = _guard_obl_022("OBL-022 is OVERDUE.")
    assert not ok
    assert "OVERDUE" in reason


def test_guard_rejects_a_contradicting_status_written_in_lower_case() -> None:
    ok, reason = _guard_obl_022("OBL-022 is overdue.")
    assert not ok
    assert "OVERDUE" in reason


def test_guard_accepts_the_status_the_cited_row_really_has() -> None:
    ok, reason = _guard_obl_022("OBL-022 is PAID.")
    assert ok, reason


def test_guard_rejects_a_payment_status_the_cited_payment_row_does_not_have() -> None:
    pay = _row("payment-row", "PAY-001")
    assert pay["status"] == "SETTLED"
    ok, reason = report_ai_http.apply_guard([pay], [pay["id"]], f"{pay['id']} is PAYMENT DUE.")
    assert not ok
    assert "PAYMENT_DUE" in reason


def test_guard_checks_status_words_against_cited_rows_only() -> None:
    """A status held by a supplied but uncited row does not excuse the word."""
    overdue = dict(_row("obligation-row", "OBL-001"), id="OBL-900", status="OVERDUE")
    ok, _ = report_ai_http.apply_guard([_OBL_022, overdue], ["OBL-022"], "OBL-022 is OVERDUE.")
    assert not ok
    ok, reason = report_ai_http.apply_guard(
        [_OBL_022, overdue], ["OBL-022", "OBL-900"], "OBL-022 is PAID; OBL-900 is OVERDUE."
    )
    assert ok, reason


def test_guard_rejects_an_empty_citation_list() -> None:
    ok, reason = report_ai_http.apply_guard([_OBL_022], [], "OBL-022 is EUR 182,500.00.")
    assert not ok
    assert "cites no row" in reason


def test_guard_rejects_182500_004_where_the_row_says_182500_00() -> None:
    """No 0.005 tolerance: an amount must equal a row value exactly."""
    assert _OBL_022["amount"] == "182500.00"
    ok, reason = _guard_obl_022("OBL-022 is EUR 182500.004.")
    assert not ok
    assert "182500.004" in reason
    ok, reason = _guard_obl_022("OBL-022 is EUR 182,500.00.")
    assert ok, reason


def test_guard_cannot_catch_swapped_numbers_or_an_invented_cause() -> None:
    """Pins the guard's known limit (see apply_guard's docstring): every number
    here is a real row value, just on the wrong row, and the cause is made up."""
    ok, _ = report_ai_http.apply_guard(
        _ROWS, ["OBL-001", "OBL-002"], "OBL-001 is 7321.06 because the filer was late."
    )
    assert ok


# ---------------------------------------------------------------------------
# explain(): through the real Gateway (governance, kill switch, evidence all
# run), with a scripted stub runtime standing in for GroqDemoRuntime.
# ---------------------------------------------------------------------------


def test_explain_returns_explained_for_a_grounded_reply() -> None:
    runtime = _ScriptedRuntime(
        _fields_result(f"OBL-001 is {_OBL_001_AMOUNT} USD.", ["OBL-001"], abstain=False)
    )
    gateway = _gateway(runtime)
    result = report_ai_http.explain(gateway, _FIXTURE, "obligation-row", ["OBL-001"])
    assert result["outcome"] == "EXPLAINED"
    assert result["cited_ids"] == ["OBL-001"]
    assert result["provenance"]["useCase"] == "explain-obligation-row"
    # Proves this went through the real Gateway, not a hand-rolled shortcut.
    assert len(runtime.calls) == 1
    assert runtime.calls[0].governance.use_case == "explain-obligation-row"


def test_explain_returns_abstained_when_the_model_itself_abstains() -> None:
    runtime = _ScriptedRuntime(
        _fields_result("", [], abstain=True, reason="not enough information")
    )
    gateway = _gateway(runtime)
    result = report_ai_http.explain(gateway, _FIXTURE, "obligation-row", ["OBL-001"])
    assert result["outcome"] == "ABSTAINED"
    assert result["reason"] == "not enough information"


def test_explain_returns_abstained_when_the_guard_rejects_a_grounded_but_false_claim() -> None:
    """The model says abstain=false, but the guard disagrees and wins."""
    runtime = _ScriptedRuntime(
        _fields_result("OBL-001 is actually 999999.99.", ["OBL-001"], abstain=False)
    )
    gateway = _gateway(runtime)
    result = report_ai_http.explain(gateway, _FIXTURE, "obligation-row", ["OBL-001"])
    assert result["outcome"] == "ABSTAINED"
    assert "999999.99" in result["reason"]


def test_explain_returns_review_required_when_the_runtime_is_unavailable() -> None:
    from ztax_gateway.runtime import RuntimeUnavailableError

    runtime = _ScriptedRuntime(raises=RuntimeUnavailableError("synthetic failure"))
    gateway = _gateway(runtime)
    result = report_ai_http.explain(gateway, _FIXTURE, "obligation-row", ["OBL-001"])
    assert result["outcome"] == "REVIEW_REQUIRED"
    assert result["provenance"] is None


def test_explain_returns_review_required_for_an_unknown_kind() -> None:
    runtime = _ScriptedRuntime(_fields_result("x", [], abstain=False))
    gateway = _gateway(runtime)
    result = report_ai_http.explain(gateway, _FIXTURE, "not-a-real-kind", ["OBL-001"])
    assert result["outcome"] == "REVIEW_REQUIRED"
    assert runtime.calls == []  # never reached the Gateway at all


def test_explain_returns_review_required_when_no_id_resolves() -> None:
    runtime = _ScriptedRuntime(_fields_result("x", [], abstain=False))
    gateway = _gateway(runtime)
    result = report_ai_http.explain(gateway, _FIXTURE, "obligation-row", ["OBL-999"])
    assert result["outcome"] == "REVIEW_REQUIRED"
    assert runtime.calls == []


def test_explain_never_sends_a_row_the_caller_did_not_ask_for() -> None:
    runtime = _ScriptedRuntime(
        _fields_result(f"OBL-001 is {_OBL_001_AMOUNT}.", ["OBL-001"], abstain=False)
    )
    gateway = _gateway(runtime)
    report_ai_http.explain(gateway, _FIXTURE, "obligation-row", ["OBL-001"])
    sent_rows = json.loads(runtime.calls[0].input.decode())
    assert [r["id"] for r in sent_rows] == ["OBL-001"]


@pytest.mark.parametrize(
    ("kind", "row_id", "value_field", "use_case"),
    [
        ("report-figure", "FIG-001", "value", "explain-report-figure"),
        ("trend-point", "TRD-003", "value", "explain-report-figure"),
        ("filing-bucket", "FIL-001", "count", "explain-report-figure"),
        ("payment-row", "PAY-002", "amount", "explain-obligation-row"),
        ("shadow-variance", "SHD-003", "absolute_variance", "explain-shadow-variance"),
    ],
)
def test_explain_works_for_every_row_type_under_its_governed_use_case(
    kind: str, row_id: str, value_field: str, use_case: str
) -> None:
    row = _row(kind, row_id)
    runtime = _ScriptedRuntime(
        _fields_result(f"{row_id} shows {row[value_field]}.", [row_id], abstain=False)
    )
    result = report_ai_http.explain(_gateway(runtime), _FIXTURE, kind, [row_id])
    assert result["outcome"] == "EXPLAINED", result["reason"]
    assert result["provenance"]["useCase"] == use_case
    assert runtime.calls[0].governance.use_case == use_case
    sent = json.loads(runtime.calls[0].input.decode())
    assert [r["id"] for r in sent] == [row_id]


def test_explain_refused_by_governance_surfaces_as_review_required(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """A killed use case still returns an outcome, not an exception."""
    runtime = _ScriptedRuntime(_fields_result("x", [], abstain=False))
    gateway = _gateway(runtime)
    gateway._registry.kill("explain-obligation-row")
    result = report_ai_http.explain(gateway, _FIXTURE, "obligation-row", ["OBL-001"])
    assert result["outcome"] == "REVIEW_REQUIRED"
    assert "KILL_SWITCH" in result["reason"]
    assert runtime.calls == []


def test_explain_abstains_when_an_explained_reply_cites_nothing() -> None:
    runtime = _ScriptedRuntime(_fields_result(f"OBL-001 is {_OBL_001_AMOUNT}.", [], abstain=False))
    result = report_ai_http.explain(_gateway(runtime), _FIXTURE, "obligation-row", ["OBL-001"])
    assert result["outcome"] == "ABSTAINED"
    assert "cites no row" in result["reason"]


def test_explain_reply_never_carries_provider_or_runtime_text() -> None:
    """A runtime failure reaches the browser as the Gateway's fixed reason code
    only -- not the exception text, which can hold what the provider said."""
    from ztax_gateway.runtime import RuntimeUnavailableError

    secret = "synthetic-provider-text-do-not-leak"
    runtime = _ScriptedRuntime(
        raises=RuntimeUnavailableError(f"groq demo runtime: provider returned HTTP 500; {secret}")
    )
    result = report_ai_http.explain(_gateway(runtime), _FIXTURE, "obligation-row", ["OBL-001"])
    assert result["outcome"] == "REVIEW_REQUIRED"
    assert result["reason"] == "AI_GATEWAY_UNAVAILABLE"
    assert secret not in json.dumps(result)


def test_each_explain_call_emits_one_gateway_evidence_line(
    caplog: pytest.LogCaptureFixture,
) -> None:
    runtime = _ScriptedRuntime(
        _fields_result(f"OBL-001 is {_OBL_001_AMOUNT}.", ["OBL-001"], abstain=False)
    )
    gateway = _gateway(runtime)
    with caplog.at_level(logging.INFO, logger="ztax_gateway.evidence"):
        report_ai_http.explain(gateway, _FIXTURE, "obligation-row", ["OBL-001"])
        assert len([r for r in caplog.records if r.name == "ztax_gateway.evidence"]) == 1
        report_ai_http.explain(gateway, _FIXTURE, "obligation-row", ["OBL-002"])
    lines = [r for r in caplog.records if r.name == "ztax_gateway.evidence"]
    assert len(lines) == 2
    assert all(isinstance(json.loads(r.getMessage()), dict) for r in lines)


def test_main_configures_logging_at_info_so_evidence_lines_are_not_dropped(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    seen: dict[str, Any] = {}
    monkeypatch.setattr(logging, "basicConfig", lambda **kw: seen.update(kw))
    monkeypatch.delenv("GROQ_API_KEY", raising=False)  # main() then returns 2, no network
    assert report_ai_http.main() == 2
    assert seen["level"] == logging.INFO


def test_guard_decision_is_logged_as_outcome_and_reason_code_never_the_model_text(
    caplog: pytest.LogCaptureFixture,
) -> None:
    gateway = _gateway(
        _ScriptedRuntime(_fields_result("OBL-001 is actually 999999.99.", ["OBL-001"], False))
    )
    with caplog.at_level(logging.INFO, logger="ztax_gateway.report_ai_http"):
        report_ai_http.explain(gateway, _FIXTURE, "obligation-row", ["OBL-001"])
    own = [r.getMessage() for r in caplog.records if r.name == "ztax_gateway.report_ai_http"]
    assert own == [
        "explain use_case=explain-obligation-row outcome=ABSTAINED reason=GUARD_NUMBER_NOT_IN_ROWS"
    ]
    assert "999999.99" not in " ".join(own)

    caplog.clear()
    ok_gateway = _gateway(
        _ScriptedRuntime(_fields_result(f"OBL-001 is {_OBL_001_AMOUNT}.", ["OBL-001"], False))
    )
    with caplog.at_level(logging.INFO, logger="ztax_gateway.report_ai_http"):
        report_ai_http.explain(ok_gateway, _FIXTURE, "obligation-row", ["OBL-001"])
    own = [r.getMessage() for r in caplog.records if r.name == "ztax_gateway.report_ai_http"]
    assert own == ["explain use_case=explain-obligation-row outcome=EXPLAINED reason=-"]


# ---------------------------------------------------------------------------
# The HTTP surface itself: loopback only, no real network, no key.
# ---------------------------------------------------------------------------


def test_http_endpoints_serve_fixture_and_explain_with_cors(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # NOTE -- the one test in this file that opens a real socket: it binds
    # 127.0.0.1 on an OS-assigned free port (port 0) and talks only to that
    # loopback server in this process. It reaches no external host, uses no
    # key, and the Gateway behind it is the stub-runtime one.
    from http.server import ThreadingHTTPServer

    runtime = _ScriptedRuntime(
        _fields_result(f"OBL-001 is {_OBL_001_AMOUNT} USD.", ["OBL-001"], abstain=False)
    )
    gateway = _gateway(runtime)
    handler_cls = report_ai_http._make_handler(gateway, _FIXTURE)
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler_cls)
    port = server.server_address[1]
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)

        conn.request("GET", "/demo/fixture")
        resp = conn.getresponse()
        assert resp.status == 200
        assert resp.getheader("Access-Control-Allow-Origin") == "http://localhost:13000"
        fixture_body = json.loads(resp.read())
        assert fixture_body["disclaimer"] == "demo fixture, synthetic, not tax data"

        body = json.dumps({"kind": "obligation-row", "ids": ["OBL-001"]}).encode()
        conn.request(
            "POST", "/demo/explain", body=body, headers={"Content-Type": "application/json"}
        )
        resp2 = conn.getresponse()
        assert resp2.status == 200
        assert resp2.getheader("Access-Control-Allow-Origin") == "http://localhost:13000"
        explain_body = json.loads(resp2.read())
        assert explain_body["outcome"] == "EXPLAINED"

        conn.request(
            "POST", "/demo/explain", body=b"{}", headers={"Content-Type": "application/json"}
        )
        resp3 = conn.getresponse()
        assert resp3.status == 400
        resp3.read()

        conn.close()
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


# ---------------------------------------------------------------------------
# Registry entries (RULES item 2): only in registry.live-demo.json.
# ---------------------------------------------------------------------------

_NEW_USE_CASES = ("explain-report-figure", "explain-obligation-row", "explain-shadow-variance")


def test_new_use_cases_are_registered_at_t1_with_p0_only() -> None:
    """The explain-* use cases are narrower than classification-review on
    purpose: tier T1 (not its T2), and P0 (synthetic public data) only. The
    live demo's classification-review is P0-only as well, but keeps T2."""
    live_doc = json.loads((_CONFIG_DIR / "registry.live-demo.json").read_text(encoding="utf-8"))
    by_id = {uc["use_case_id"]: uc for uc in live_doc["use_cases"]}
    baseline = by_id["classification-review"]
    assert baseline["max_risk_tier"] == "T2"
    assert baseline["permitted_data_classes"] == ["P0"]
    for use_case_id in _NEW_USE_CASES:
        assert use_case_id in by_id, f"{use_case_id} is not registered in registry.live-demo.json"
        uc = by_id[use_case_id]
        assert uc["max_authority"] == baseline["max_authority"] == "A1"
        assert uc["max_risk_tier"] == "T1"
        assert uc["permitted_regions"] == baseline["permitted_regions"] == ["local"]
        assert uc["permitted_data_classes"] == ["P0"]
        assert uc["suspended"] is False
        assert uc["route"]["model_profile"] == "model:groq-live-demo"
        assert uc["route"]["provider_profile"] == "provider:groq-demo"


def test_new_use_cases_are_not_in_registry_dev_json() -> None:
    dev_doc = json.loads((_CONFIG_DIR / "registry.dev.json").read_text(encoding="utf-8"))
    dev_ids = {uc["use_case_id"] for uc in dev_doc["use_cases"]}
    for use_case_id in _NEW_USE_CASES:
        assert use_case_id not in dev_ids


def test_patched_routes_already_covers_the_new_use_cases_without_any_code_change() -> None:
    """serve_live_demo._patched_routes iterates registry.ids() generically --
    adding the three new use cases to the registry file is enough."""
    registry, routes = load_config(_CONFIG_DIR / "registry.live-demo.json")
    patched = serve_live_demo._patched_routes(registry, routes, "llama-3.1-8b-instant")
    for use_case_id in (*_NEW_USE_CASES, "classification-review"):
        route = patched.route(use_case_id)
        assert route.model_profile == "model:groq-live-demo:llama-3.1-8b-instant"
        assert route.provider_profile == "provider:groq-demo"


# ---------------------------------------------------------------------------
# chart_geometry tests
# ---------------------------------------------------------------------------


def test_fixture_includes_chart_geometry() -> None:
    """GET /demo/fixture includes chart_geometry with the three required keys."""
    fx = report_fixture.generate_fixture()
    assert "chart_geometry" in fx
    geom = fx["chart_geometry"]
    assert "authority_bars" in geom
    assert "trend_points" in geom
    assert "pipeline_segments" in geom


def test_rows_for_kind_output_has_no_geometry_keys() -> None:
    """rows_for_kind never leaks chart_geometry keys into the rows sent to the guard."""
    fx = report_fixture.generate_fixture()
    geometry_top_keys = {"chart_geometry", "authority_bars", "trend_points", "pipeline_segments"}
    for kind in report_fixture.EXPLAIN_KINDS:
        rows = report_fixture._collection(fx, kind)
        for row in rows:
            assert geometry_top_keys.isdisjoint(row.keys()), \
                f"row {row.get('id')} in {kind!r} contains a geometry key"


def test_a_number_only_in_chart_geometry_is_rejected_by_apply_guard() -> None:
    """A number that appears only in chart_geometry and not in any row must be rejected.

    The guard calls _numeric_values(rows) which scans the supplied rows only;
    chart_geometry is a top-level key and is never in rows_for_kind output.
    """
    fx = report_fixture.generate_fixture()
    # Find a permille value that does NOT appear verbatim in any row (all are "0"..."1000").
    # Use "1000" -- the max bar; it never appears as a fiscal decimal in the rows.
    geom_only_number = "1000"

    # Confirm it is not a numeric value in any row.
    all_rows = list(report_fixture.flatten_rows(fx).values())
    allowed = report_ai_http._numeric_values(all_rows)
    from decimal import Decimal
    assert Decimal(geom_only_number) not in allowed, (
        f"{geom_only_number!r} was found in row numeric values -- "
        "pick a different test value"
    )

    # Pick a real obligation row to explain.
    rows = report_fixture.rows_for_kind(fx, "obligation-row", ["OBL-001"])
    assert rows
    cited_ids = [rows[0]["id"]]

    # Craft an explanation that mentions only the geometry number (not a row value).
    explanation = f"The filing covers a share of {geom_only_number} per mille."
    passed, reason = report_ai_http.apply_guard(rows, cited_ids, explanation)
    assert not passed, "guard should reject a number that is only in chart_geometry"
    assert reason
