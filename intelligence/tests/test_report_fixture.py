"""Unit tests for tools/report_fixture.py -- no network, ever.

DEMO ONLY source under test. These tests recompute every claim from the raw
rows (Decimal, never float) instead of trusting a second typed number:
determinism, the requested authorities and PAID row, the fixed as-of
date, the trend and its derived "vs last month", payments linked to
obligations, the four-bucket filing pipeline, the KPIs, chart order, and
that every row type resolves through rows_for_kind().

tools/ is not on pythonpath (pyproject.toml sets pythonpath = ["src"] only),
so the module is loaded from its file path, as test_groq_demo_runtime.py
does for its sibling.
"""

from __future__ import annotations

import importlib.util
import json
import re
import sys
from datetime import date, timedelta
from decimal import ROUND_HALF_UP, Decimal
from pathlib import Path
from types import ModuleType
from typing import Any

import pytest

_MODULE_PATH = Path(__file__).resolve().parent.parent / "tools" / "report_fixture.py"
_spec = importlib.util.spec_from_file_location("report_fixture", _MODULE_PATH)
assert _spec is not None and _spec.loader is not None
report_fixture: ModuleType = importlib.util.module_from_spec(_spec)
sys.modules["report_fixture"] = report_fixture
_spec.loader.exec_module(report_fixture)

# ZTAX-OBL-001 chapter 15, "Obligation Lifecycle State Machine", exactly.
# SETTLED is deliberately absent: it is a remittance-domain state.
_CHAPTER_15_LIFECYCLE = frozenset(
    {
        "POTENTIAL", "ACTIVE", "DATA_REQUIRED", "READY", "IN_REVIEW", "APPROVED",
        "FILED", "ACCEPTED", "PAYMENT_DUE", "PAID", "CLOSED", "AMENDMENT_REQUIRED",
        "OVERDUE", "DISPUTED", "SUSPENDED", "CANCELLED",
    }
)
# backend/internal/domain/remittance/remittance.go's Status values.
_REMITTANCE_STATES = frozenset(
    {
        "APPROVAL_REQUIRED", "APPROVED", "SUBMITTED", "ACCEPTED_BY_PROVIDER", "PROCESSING",
        "UNCERTAIN_PAYMENT", "SETTLED", "REJECTED", "RETURNED", "CANCELLED",
    }
)
# backend/internal/domain/shadow/shadow.go's State values (case workflow).
_SHADOW_CASE_STATES = frozenset(
    {
        "NEW", "TRIAGED", "DATA_REQUIRED", "ANALYSIS", "SME_REVIEW", "COMPARATOR_QUERY",
        "ZOIKO_DEFECT_CONFIRMED", "COMPARATOR_ISSUE_CONFIRMED", "EXPECTED_DIFFERENCE_APPROVED",
        "LEGAL_AMBIGUITY", "FIX_PENDING", "RETEST_REQUIRED", "RESOLVED", "ACCEPTED_RISK",
        "CLOSED",
    }
)
_EXPECTED_AUTHORITIES = {
    ("USF Telecom", "US-FEDERAL", "FCC-USAC"),
    ("Sales Tax", "US-NY", "NY Dept of Taxation"),
    ("State 911", "US-CA", "CA CDTFA"),
    ("VAT", "UK-HMRC", "UK HMRC"),
    ("Cross-Border", "EU-OSS", "EU OSS"),
}
_RATE = Decimal("1.0800")


def _d(value: str) -> Decimal:
    return Decimal(value)


def _q(value: Decimal, places: str = "0.01") -> Decimal:
    return value.quantize(Decimal(places), rounding=ROUND_HALF_UP)


@pytest.fixture(scope="module")
def fx() -> dict[str, Any]:
    fixture: dict[str, Any] = report_fixture.generate_fixture()
    return fixture


def _fig(fx: dict[str, Any], fig_id: str) -> dict[str, Any]:
    return next(f for f in fx["report_figures"] if f["id"] == fig_id)


# --- determinism, disclaimer, ids ------------------------------------------


def test_fixture_is_deterministic_across_calls() -> None:
    first = json.dumps(report_fixture.generate_fixture(), sort_keys=True)
    second = json.dumps(report_fixture.generate_fixture(), sort_keys=True)
    assert first == second


def test_fixture_carries_the_demo_disclaimer_and_fixed_fx_label(fx: dict[str, Any]) -> None:
    assert fx["disclaimer"] == "demo fixture, synthetic, not tax data"
    assert fx["currency"] == "USD"
    assert fx["fx_demo_rate"] == {
        "from": "EUR", "to": "USD", "rate": "1.0800",
        "label": "fixed demo rate, not live FX -- demo fixture only",
    }


def test_as_of_is_a_fixed_constant_not_the_clock(fx: dict[str, Any]) -> None:
    assert fx["as_of"] == report_fixture.AS_OF == "2026-10-20"
    assert fx["filing_pipeline"]["as_of"] == fx["as_of"]


def test_every_row_id_is_unique_and_prefixed_by_its_collection(fx: dict[str, Any]) -> None:
    prefixes = {
        "report-figure": "FIG-", "trend-point": "TRD-", "filing-bucket": "FIL-",
        "obligation-row": "OBL-", "payment-row": "PAY-", "shadow-variance": "SHD-",
    }
    flat = report_fixture.flatten_rows(fx)
    total = sum(len(report_fixture._collection(fx, k)) for k in prefixes)
    assert len(flat) == total, "row ids must be unique across the whole fixture"
    for kind, prefix in prefixes.items():
        for row in report_fixture._collection(fx, kind):
            assert row["id"].startswith(prefix)
            assert row["explain_kind"] == kind


# --- obligations register ----------------------------------------------------


def test_register_covers_exactly_the_requested_authorities(fx: dict[str, Any]) -> None:
    seen = {(o["type"], o["jurisdiction"], o["authority"]) for o in fx["obligations"]}
    assert seen == _EXPECTED_AUTHORITIES


def test_lifecycle_states_are_exactly_the_chapter_15_list(fx: dict[str, Any]) -> None:
    assert set(fx["lifecycle_states"]) == _CHAPTER_15_LIFECYCLE
    assert len(fx["lifecycle_states"]) == len(_CHAPTER_15_LIFECYCLE) == 16
    assert tuple(fx["lifecycle_states"]) == report_fixture.LIFECYCLE_STATES


def test_every_obligation_status_is_a_chapter_15_state(fx: dict[str, Any]) -> None:
    for o in fx["obligations"]:
        assert o["status"] in _CHAPTER_15_LIFECYCLE, f"{o['id']} has status {o['status']}"


def test_no_obligation_has_settled_and_the_lifecycle_list_does_not_contain_it(
    fx: dict[str, Any],
) -> None:
    """SETTLED is a remittance-domain state: payment rows only."""
    assert "SETTLED" not in fx["lifecycle_states"]
    assert "SETTLED" not in report_fixture.LIFECYCLE_STATES
    assert [o["id"] for o in fx["obligations"] if o["status"] == "SETTLED"] == []


def test_the_requested_paid_row_is_present_exactly(fx: dict[str, Any]) -> None:
    paid = [o for o in fx["obligations"] if o["status"] == "PAID"]
    assert len(paid) == 1
    row = paid[0]
    assert row["id"] == "OBL-022"
    assert (row["type"], row["jurisdiction"], row["authority"]) == (
        "Cross-Border", "EU-OSS", "EU OSS",
    )
    assert row["period"] == "2026-Q3"
    assert row["due_date"] == "2026-10-31"
    assert row["amount"] == "182500.00"
    assert row["currency"] == "EUR"


def test_eur_rows_are_converted_at_the_labelled_demo_rate_and_usd_rows_are_unchanged(
    fx: dict[str, Any],
) -> None:
    for o in fx["obligations"]:
        if o["currency"] == "EUR":
            assert o["authority"] == "EU OSS"
            assert _d(o["amount_usd"]) == _q(_d(o["amount"]) * _RATE)
            assert o["fx_rate"] == "1.0800"
            assert o["fx_label"].startswith("fixed demo rate")
        else:
            assert o["currency"] == "USD"
            assert o["amount_usd"] == o["amount"]


# --- trend and the derived "vs last month" ----------------------------------


def test_trend_has_six_monthly_points_each_the_sum_of_its_obligations(
    fx: dict[str, Any],
) -> None:
    points = fx["trend"]["points"]
    assert len(points) == 6
    months = [p["month"] for p in points]
    assert months == sorted(months) and len(set(months)) == 6
    for p in points:
        members = [o for o in fx["obligations"] if o["assessed_month"] == p["month"]]
        assert p["source_ids"] == [o["id"] for o in members]
        assert _d(p["value"]) == sum((_d(o["amount_usd"]) for o in members), Decimal(0))


def test_trend_points_add_up_to_total_tax_assessed(fx: dict[str, Any]) -> None:
    total = sum((_d(p["value"]) for p in fx["trend"]["points"]), Decimal(0))
    assert total == _d(_fig(fx, "FIG-001")["value"])


def test_vs_last_month_is_computed_from_the_last_two_points_and_marked_derived(
    fx: dict[str, Any],
) -> None:
    points = fx["trend"]["points"]
    fig = _fig(fx, fx["trend"]["vs_last_month_id"])
    assert fig["kind"] == "derived"
    previous, latest = _d(points[-2]["value"]), _d(points[-1]["value"])
    assert fig["source_ids"] == [points[-2]["id"], points[-1]["id"]]
    assert fig["previous_value"] == points[-2]["value"]
    assert fig["latest_value"] == points[-1]["value"]
    assert _d(fig["value"]) == _q((latest - previous) / previous * 100, "0.1")
    assert _d(fig["change_amount"]) == abs(latest - previous)
    assert fig["direction"] == ("increase" if latest >= previous else "decrease")


# --- payments ----------------------------------------------------------------


def test_payments_link_to_real_obligations_and_copy_their_amount(fx: dict[str, Any]) -> None:
    by_id = {o["id"]: o for o in fx["obligations"]}
    assert fx["payments"], "the payments tab needs rows"
    for i, p in enumerate(fx["payments"], start=1):
        assert p["id"] == f"PAY-{i:03d}"
        o = by_id[p["obligation_id"]]
        assert (p["amount"], p["currency"], p["amount_usd"]) == (
            o["amount"], o["currency"], o["amount_usd"],
        )
        assert p["status"] in _REMITTANCE_STATES
        assert date.fromisoformat(p["date"]) <= date.fromisoformat(fx["as_of"])


def test_paid_and_closed_obligations_have_a_settled_payment(fx: dict[str, Any]) -> None:
    by_obligation = {p["obligation_id"]: p for p in fx["payments"]}
    for o in fx["obligations"]:
        if o["status"] in {"PAID", "CLOSED"}:
            assert by_obligation[o["id"]]["status"] == "SETTLED"
    paid_row = next(o for o in fx["obligations"] if o["status"] == "PAID")
    assert by_obligation[paid_row["id"]]["date"] <= paid_row["due_date"]


def test_pay_019_is_the_settled_payment_of_the_paid_eur_obligation(fx: dict[str, Any]) -> None:
    pay = next(p for p in fx["payments"] if p["id"] == "PAY-019")
    assert pay["status"] == "SETTLED"
    assert pay["obligation_id"] == "OBL-022"
    assert (pay["amount"], pay["currency"]) == ("182500.00", "EUR")


def test_payment_rows_use_remittance_states_and_settled_lives_only_there(
    fx: dict[str, Any],
) -> None:
    states = {p["status"] for p in fx["payments"]}
    assert states <= _REMITTANCE_STATES
    assert "SETTLED" in states
    # The payments tab genuinely varies, so it is not just SETTLED everywhere.
    assert states >= {"SETTLED", "SUBMITTED", "PROCESSING"}
    # ...and none of those words is an obligation lifecycle state except the shared ones.
    assert "SETTLED" not in _CHAPTER_15_LIFECYCLE
    assert {"SUBMITTED", "PROCESSING"}.isdisjoint(_CHAPTER_15_LIFECYCLE)


# --- filing pipeline -----------------------------------------------------------


def test_pipeline_has_the_four_requested_buckets_in_order(fx: dict[str, Any]) -> None:
    labels = [b["label"] for b in fx["filing_pipeline"]["buckets"]]
    assert labels == [
        "Filed & Acknowledged", "Under Legal Review", "Draft Prepared", "Overdue / At Risk",
    ]


def test_pipeline_buckets_partition_every_filing_exactly_once(fx: dict[str, Any]) -> None:
    buckets = fx["filing_pipeline"]["buckets"]
    members = [i for b in buckets for i in b["source_ids"]]
    assert sorted(members) == sorted(o["id"] for o in fx["obligations"])
    assert len(members) == len(set(members))
    total = int(fx["filing_pipeline"]["total_filings"])
    assert total == len(fx["obligations"])
    assert sum(int(b["count"]) for b in buckets) == total
    for b in buckets:
        assert int(b["count"]) == len(b["source_ids"])
        assert b["total_filings"] == str(total)
        assert _d(b["percent"]) == _q(Decimal(int(b["count"])) / total * 100, "0.1")


def test_overdue_bucket_is_only_unfiled_obligations_past_the_as_of_date(
    fx: dict[str, Any],
) -> None:
    as_of = date.fromisoformat(fx["as_of"])
    by_id = {o["id"]: o for o in fx["obligations"]}
    overdue = next(b for b in fx["filing_pipeline"]["buckets"] if b["label"] == "Overdue / At Risk")
    assert overdue["source_ids"], "the demo should show at least one overdue filing"
    for oid in overdue["source_ids"]:
        o = by_id[oid]
        assert date.fromisoformat(o["due_date"]) < as_of
        assert o["status"] not in {"FILED", "ACCEPTED", "PAYMENT_DUE", "PAID", "CLOSED"}


# --- KPIs ----------------------------------------------------------------------


def test_total_tax_assessed_is_the_sum_of_usd_converted_rows(fx: dict[str, Any]) -> None:
    fig = _fig(fx, "FIG-001")
    assert _d(fig["value"]) == sum((_d(o["amount_usd"]) for o in fx["obligations"]), Decimal(0))


def test_fig_001_carries_no_eur_total_so_none_can_be_restated(fx: dict[str, Any]) -> None:
    fig = _fig(fx, "FIG-001")
    assert "eur_amount_total" not in fig
    eur_amounts = [_d(o["amount"]) for o in fx["obligations"] if o["currency"] == "EUR"]
    eur_sum = sum(eur_amounts, Decimal(0))
    assert str(eur_sum) == "347654.81"  # the number the live model once restated
    # It is held by no row of any kind, FIG-001 or otherwise.
    for row in report_fixture.flatten_rows(fx).values():
        assert str(eur_sum) not in {v for v in row.values() if isinstance(v, str)}


def test_eur_rows_keep_amount_usd_fx_rate_and_the_labelled_demo_rate(fx: dict[str, Any]) -> None:
    eur_rows = [o for o in fx["obligations"] if o["currency"] == "EUR"]
    assert eur_rows
    for o in eur_rows:
        assert o["amount_usd"] and o["fx_rate"] == "1.0800"
        assert o["fx_label"] == "fixed demo rate, not live FX -- demo fixture only"
    assert fx["fx_demo_rate"]["rate"] == "1.0800"


def test_filing_compliance_is_computed_from_the_filed_bucket(fx: dict[str, Any]) -> None:
    fig = _fig(fx, "FIG-002")
    filed = fx["filing_pipeline"]["buckets"][0]
    assert fig["filed_count"] == filed["count"]
    assert fig["total_filings"] == filed["total_filings"]
    expected = _q(Decimal(int(filed["count"])) / int(filed["total_filings"]) * 100, "0.1")
    assert _d(fig["value"]) == expected


def test_upcoming_remittance_sums_unpaid_rows_due_within_15_days(fx: dict[str, Any]) -> None:
    fig = _fig(fx, "FIG-003")
    as_of = date.fromisoformat(fx["as_of"])
    end = as_of + timedelta(days=15)
    rows = [
        o for o in fx["obligations"]
        if o["status"] not in {"PAID", "CLOSED"}
        and as_of <= date.fromisoformat(o["due_date"]) <= end
    ]
    assert rows, "the demo should have something due in the window"
    assert fig["source_ids"] == [o["id"] for o in rows]
    assert _d(fig["value"]) == sum((_d(o["amount_usd"]) for o in rows), Decimal(0))
    assert fig["window_days"] == "15" and fig["as_of"] == fx["as_of"]


def test_a_paid_obligation_is_not_upcoming_even_though_its_due_date_is_in_the_window(
    fx: dict[str, Any],
) -> None:
    paid = next(o for o in fx["obligations"] if o["status"] == "PAID")
    as_of = date.fromisoformat(fx["as_of"])
    assert as_of <= date.fromisoformat(paid["due_date"]) <= as_of + timedelta(days=15)
    assert paid["id"] not in _fig(fx, "FIG-003")["source_ids"]


def test_a_paid_obligation_counts_as_filed_and_acknowledged(fx: dict[str, Any]) -> None:
    paid = next(o for o in fx["obligations"] if o["status"] == "PAID")
    buckets = fx["filing_pipeline"]["buckets"]
    filed = next(b for b in buckets if b["label"] == "Filed & Acknowledged")
    assert paid["id"] in filed["source_ids"]


def test_active_jurisdictions_counts_distinct_authorities(fx: dict[str, Any]) -> None:
    fig = _fig(fx, "FIG-004")
    authorities = {o["authority"] for o in fx["obligations"]}
    assert int(fig["value"]) == len(authorities)
    assert set(fig["authorities"]) == authorities


def test_every_kpi_figure_is_computed_or_derived_never_typed(fx: dict[str, Any]) -> None:
    for fig in fx["report_figures"]:
        assert fig["kind"] in {"computed", "derived"}
        assert fig["basis"]


# --- chart -----------------------------------------------------------------------


def test_by_authority_chart_is_sorted_descending_and_matches_the_register(
    fx: dict[str, Any],
) -> None:
    bars = [f for f in fx["report_figures"] if f["group"] == "by_authority"]
    values = [_d(b["value"]) for b in bars]
    assert values == sorted(values, reverse=True)
    for b in bars:
        rows = [o for o in fx["obligations"] if o["authority"] == b["authority"]]
        assert b["source_ids"] == [o["id"] for o in rows]
        assert _d(b["value"]) == sum((_d(o["amount_usd"]) for o in rows), Decimal(0))
    assert sum(values, Decimal(0)) == _d(_fig(fx, "FIG-001")["value"])


# --- shadow assurance ------------------------------------------------------------


def test_shadow_rows_compare_against_their_linked_obligation(fx: dict[str, Any]) -> None:
    by_id = {o["id"]: o for o in fx["obligations"]}
    results = set()
    for s in fx["shadow_assurance"]:
        assert s["zoiko_value"] == by_id[s["obligation_id"]]["amount_usd"]
        variance = abs(_d(s["comparator_value"]) - _d(s["zoiko_value"]))
        assert _d(s["absolute_variance"]) == variance
        results.add(s["result"])
        if variance == 0:
            assert (s["result"], s["case_state"]) == ("MATCH", "")
        else:
            assert s["result"] == "VARIANCE"
            assert s["case_state"] in _SHADOW_CASE_STATES
    assert results == {"MATCH", "VARIANCE"}


# --- rows_for_kind ---------------------------------------------------------------


@pytest.mark.parametrize(
    ("kind", "row_id"),
    [
        ("report-figure", "FIG-001"),
        ("trend-point", "TRD-001"),
        ("filing-bucket", "FIL-001"),
        ("obligation-row", "OBL-001"),
        ("payment-row", "PAY-001"),
        ("shadow-variance", "SHD-001"),
    ],
)
def test_rows_for_kind_resolves_every_row_type(
    fx: dict[str, Any], kind: str, row_id: str
) -> None:
    rows = report_fixture.rows_for_kind(fx, kind, [row_id])
    assert [r["id"] for r in rows] == [row_id]


def test_rows_for_kind_ignores_an_id_from_another_kind(fx: dict[str, Any]) -> None:
    rows = report_fixture.rows_for_kind(fx, "payment-row", ["PAY-001", "OBL-001", "TRD-001"])
    assert [r["id"] for r in rows] == ["PAY-001"]


def test_rows_for_kind_unknown_kind_returns_nothing(fx: dict[str, Any]) -> None:
    assert report_fixture.rows_for_kind(fx, "not-a-real-kind", ["OBL-001"]) == []


# --- chart_geometry ----------------------------------------------------------


def test_chart_geometry_key_exists(fx: dict[str, Any]) -> None:
    assert "chart_geometry" in fx
    geom = fx["chart_geometry"]
    assert "authority_bars" in geom
    assert "trend_points" in geom
    assert "pipeline_segments" in geom


def test_chart_geometry_authority_bars_are_integer_strings_0_to_1000(fx: dict[str, Any]) -> None:
    for fig_id, val in fx["chart_geometry"]["authority_bars"].items():
        assert isinstance(val, str), f"{fig_id}: not a string"
        assert re.fullmatch(r"\d{1,4}", val), f"{fig_id}: not a 1-4 digit string: {val!r}"
        assert 0 <= int(val) <= 1000, f"{fig_id}: out of range: {val}"


def test_chart_geometry_trend_points_are_integer_strings_0_to_1000(fx: dict[str, Any]) -> None:
    for pt_id, val in fx["chart_geometry"]["trend_points"].items():
        assert isinstance(val, str), f"{pt_id}: not a string"
        assert re.fullmatch(r"\d{1,4}", val), f"{pt_id}: not a 1-4 digit string: {val!r}"
        assert 0 <= int(val) <= 1000, f"{pt_id}: out of range: {val}"


def test_chart_geometry_pipeline_segments_are_integer_strings_0_to_1000(fx: dict[str, Any]) -> None:
    for seg_id, seg in fx["chart_geometry"]["pipeline_segments"].items():
        for field in ("start", "width"):
            val = seg[field]
            assert isinstance(val, str), f"{seg_id}.{field}: not a string"
            assert re.fullmatch(r"\d{1,4}", val), f"{seg_id}.{field}: {val!r}"
            assert 0 <= int(val) <= 1000, f"{seg_id}.{field}: out of range"


def test_chart_geometry_authority_bars_max_is_1000(fx: dict[str, Any]) -> None:
    vals = [int(v) for v in fx["chart_geometry"]["authority_bars"].values()]
    assert vals, "authority_bars must not be empty"
    assert max(vals) == 1000, f"expected max 1000, got {max(vals)}"


def test_chart_geometry_trend_points_max_is_1000(fx: dict[str, Any]) -> None:
    vals = [int(v) for v in fx["chart_geometry"]["trend_points"].values()]
    assert vals, "trend_points must not be empty"
    assert max(vals) == 1000, f"expected max 1000, got {max(vals)}"


def test_chart_geometry_authority_bars_ids_match_by_authority_figures(fx: dict[str, Any]) -> None:
    bar_figs = {f["id"] for f in fx["report_figures"] if f["group"] == "by_authority"}
    geom_ids = set(fx["chart_geometry"]["authority_bars"].keys())
    assert geom_ids == bar_figs


def test_chart_geometry_trend_point_ids_match_trend_points(fx: dict[str, Any]) -> None:
    trend_ids = {p["id"] for p in fx["trend"]["points"]}
    geom_ids = set(fx["chart_geometry"]["trend_points"].keys())
    assert geom_ids == trend_ids


def test_chart_geometry_pipeline_segment_ids_match_buckets(fx: dict[str, Any]) -> None:
    bucket_ids = {b["id"] for b in fx["filing_pipeline"]["buckets"]}
    geom_ids = set(fx["chart_geometry"]["pipeline_segments"].keys())
    assert geom_ids == bucket_ids


def test_chart_geometry_pipeline_segments_tile_correctly(fx: dict[str, Any]) -> None:
    """First start must be 0; each start = previous start+width; total must be 1000."""
    buckets = fx["filing_pipeline"]["buckets"]
    geom = fx["chart_geometry"]["pipeline_segments"]
    total = int(fx["filing_pipeline"]["total_filings"])
    prev_end = 0
    for bucket in buckets:
        seg = geom[bucket["id"]]
        start, width = int(seg["start"]), int(seg["width"])
        assert start == prev_end, f"{bucket['id']}: start {start} != expected {prev_end}"
        prev_end = start + width
    if total > 0:
        assert prev_end == 1000, f"pipeline segments do not tile to 1000 (got {prev_end})"


def test_chart_geometry_authority_bars_recompute_with_decimal(fx: dict[str, Any]) -> None:
    """Re-derive each bar share with Decimal and confirm the fixture matches."""
    bar_figs = [f for f in fx["report_figures"] if f["group"] == "by_authority"]
    values = [Decimal(f["value"]) for f in bar_figs]
    bar_max = max(values)
    for fig, val in zip(bar_figs, values, strict=True):
        expected = str(int(val / bar_max * 1000))
        actual = fx["chart_geometry"]["authority_bars"][fig["id"]]
        assert actual == expected, f"{fig['id']}: expected {expected}, got {actual}"


def test_chart_geometry_trend_points_recompute_with_decimal(fx: dict[str, Any]) -> None:
    """Re-derive each trend share with Decimal and confirm the fixture matches."""
    points = fx["trend"]["points"]
    values = [Decimal(p["value"]) for p in points]
    trend_max = max(values)
    for point, val in zip(points, values, strict=True):
        expected = str(int(val / trend_max * 1000))
        actual = fx["chart_geometry"]["trend_points"][point["id"]]
        assert actual == expected, f"{point['id']}: expected {expected}, got {actual}"


def test_chart_geometry_not_in_any_row(fx: dict[str, Any]) -> None:
    """geometry keys must not appear in any row (guard isolation)."""
    geom_keys = {"authority_bars", "trend_points", "pipeline_segments", "chart_geometry"}
    for row in report_fixture.flatten_rows(fx).values():
        assert not geom_keys.intersection(row.keys()), \
            f"row {row.get('id')} contains a geometry key"


def test_chart_geometry_is_deterministic(fx: dict[str, Any]) -> None:
    second = report_fixture.generate_fixture()
    assert json.dumps(fx["chart_geometry"], sort_keys=True) == \
        json.dumps(second["chart_geometry"], sort_keys=True)
