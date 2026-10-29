"""Synthetic Reports dashboard fixture -- DEMO ONLY.

DEMO ONLY. Never production. Synthetic public data only. No authority.
Demo fixture, synthetic, not tax data (see ``DISCLAIMER``, carried in the
fixture itself).

Deterministic: a fixed seed, a fixed "as of" date (``AS_OF``, never the
clock), no network. Two calls, in one process or two, give identical output.

Every figure is computed from rows, never typed a second time:

* ``report_figures``' KPIs and per-authority bars are sums/counts over
  ``obligations`` (EUR amounts converted at the labelled fixed demo rate).
* ``trend.points`` are per-month sums of the same obligations, so the
  points add up to the "total tax assessed" KPI; the "vs last month"
  figure is computed from the last two points and marked ``"kind":
  "derived"``.
* ``filing_pipeline`` buckets partition every obligation exactly once, so
  their counts add up to the number of filings; percentages are computed.
* every ``payments`` row copies its linked obligation's amount, and every
  ``shadow_assurance`` row's ``zoiko_value`` is its linked obligation's
  ``amount_usd``, with ``absolute_variance`` computed from the row itself.

All numbers are decimal strings (like MoneyValue.amount), so the explain
guard in report_ai_http.py can check them; every row has a stable id with a
per-collection prefix (FIG-, TRD-, FIL-, OBL-, PAY-, SHD-).

Obligation lifecycle states are exactly ZTAX-OBL-001 chapter 15's list (see
``LIFECYCLE_STATES``). SETTLED is not one of them: it is a remittance-domain
state and appears only on payment rows, whose states are a subset of the real
remittance ``Status`` enum (backend/internal/domain/remittance/remittance.go).
Shadow Assurance case states are a subset of shadow.go's ``State`` enum.
"""

from __future__ import annotations

import random
from datetime import date, timedelta
from decimal import ROUND_HALF_UP, Decimal
from typing import Any, Final

__all__: list[str] = ["AS_OF", "flatten_rows", "generate_fixture", "rows_for_kind"]

DISCLAIMER: Final[str] = "demo fixture, synthetic, not tax data"
REPORTING_CURRENCY: Final[str] = "USD"

# A fixed, labelled demo rate -- never a live FX lookup.
FX_DEMO_RATE: Final[dict[str, str]] = {
    "from": "EUR",
    "to": REPORTING_CURRENCY,
    "rate": "1.0800",
    "label": "fixed demo rate, not live FX -- demo fixture only",
}
_EUR_TO_USD: Final[Decimal] = Decimal(FX_DEMO_RATE["rate"])

# The fixture's "today". Fixed so "due within 15 days" never drifts.
AS_OF: Final[str] = "2026-10-20"
UPCOMING_WINDOW_DAYS: Final[int] = 15

_SEED: Final[int] = 20260101

# ZTAX-OBL-001 chapter 15, "Obligation Lifecycle State Machine", exactly.
LIFECYCLE_STATES: Final[tuple[str, ...]] = (
    "POTENTIAL", "ACTIVE", "DATA_REQUIRED", "READY", "IN_REVIEW", "APPROVED",
    "FILED", "ACCEPTED", "PAYMENT_DUE", "PAID", "CLOSED", "AMENDMENT_REQUIRED",
    "OVERDUE", "DISPUTED", "SUSPENDED", "CANCELLED",
)
_FILED_STATES: Final[frozenset[str]] = frozenset(
    {"FILED", "ACCEPTED", "PAYMENT_DUE", "PAID", "CLOSED"}
)
_REVIEW_STATES: Final[frozenset[str]] = frozenset({"IN_REVIEW", "APPROVED"})
_DRAFT_STATES: Final[frozenset[str]] = frozenset({"POTENTIAL", "ACTIVE", "DATA_REQUIRED", "READY"})
_PAID_STATES: Final[frozenset[str]] = frozenset({"PAID", "CLOSED"})

_PIPELINE_LABELS: Final[tuple[str, ...]] = (
    "Filed & Acknowledged", "Under Legal Review", "Draft Prepared", "Overdue / At Risk",
)

# (type, jurisdiction, authority, currency, low, high) -- amount range in whole units.
_AUTHORITIES: Final[dict[str, tuple[str, str, str, str, int, int]]] = {
    "USF": ("USF Telecom", "US-FEDERAL", "FCC-USAC", "USD", 60_000, 80_000),
    "NY": ("Sales Tax", "US-NY", "NY Dept of Taxation", "USD", 40_000, 55_000),
    "CA": ("State 911", "US-CA", "CA CDTFA", "USD", 8_000, 12_000),
    "UK": ("VAT", "UK-HMRC", "UK HMRC", "USD", 170_000, 210_000),
    "OSS": ("Cross-Border", "EU-OSS", "EU OSS", "EUR", 160_000, 190_000),
}

# (authority key, period, assessed month, due date, status, fixed amount or None).
# US filings are monthly (assessed the month after the period); UK VAT runs
# on a staggered quarter and EU OSS on calendar quarters, so each month from
# June on carries one quarterly return and the six trend points stay comparable.
_OBLIGATION_SPECS: Final[tuple[tuple[str, str, str, str, str, str | None], ...]] = (
    ("NY", "2026-04", "2026-05", "2026-05-20", "CLOSED", None),
    ("USF", "2026-04", "2026-05", "2026-05-31", "CLOSED", None),
    ("CA", "2026-04", "2026-05", "2026-05-31", "CLOSED", None),
    ("NY", "2026-05", "2026-06", "2026-06-20", "CLOSED", None),
    ("USF", "2026-05", "2026-06", "2026-06-30", "CLOSED", None),
    ("CA", "2026-05", "2026-06", "2026-06-30", "CLOSED", None),
    ("UK", "2026-03..2026-05", "2026-06", "2026-07-07", "CLOSED", None),
    ("NY", "2026-06", "2026-07", "2026-07-20", "CLOSED", None),
    ("USF", "2026-06", "2026-07", "2026-07-31", "CLOSED", None),
    ("CA", "2026-06", "2026-07", "2026-07-31", "CLOSED", None),
    ("OSS", "2026-Q2", "2026-07", "2026-07-31", "CLOSED", None),
    ("NY", "2026-07", "2026-08", "2026-08-20", "CLOSED", None),
    ("USF", "2026-07", "2026-08", "2026-08-31", "FILED", None),
    ("CA", "2026-07", "2026-08", "2026-08-31", "DATA_REQUIRED", None),
    ("NY", "2026-08", "2026-09", "2026-09-20", "FILED", None),
    ("USF", "2026-08", "2026-09", "2026-09-30", "ACCEPTED", None),
    ("CA", "2026-08", "2026-09", "2026-09-30", "CLOSED", None),
    ("UK", "2026-06..2026-08", "2026-09", "2026-10-07", "ACCEPTED", None),
    ("NY", "2026-09", "2026-10", "2026-10-20", "PAYMENT_DUE", None),
    ("USF", "2026-09", "2026-10", "2026-10-31", "IN_REVIEW", None),
    ("CA", "2026-09", "2026-10", "2026-10-31", "READY", None),
    # The one PAID row the dashboard calls for, amount fixed by request. Its
    # payment row (the Payments tab) is the one that carries SETTLED.
    ("OSS", "2026-Q3", "2026-10", "2026-10-31", "PAID", "182500.00"),
)

# The fixed PAID obligation, and the date its payment was confirmed (before due).
_FIXED_PAID_ID: Final[str] = "OBL-022"
_FIXED_PAID_PAYMENT_DATE: Final[str] = "2026-10-15"

# (obligation id, comparator delta in cents; 0 means the comparator agrees)
_SHADOW_SPECS: Final[tuple[tuple[str, int], ...]] = (
    ("OBL-005", 0),
    ("OBL-008", 0),
    ("OBL-013", 23_973),
    ("OBL-015", -19_507),
    ("OBL-016", 19_197),
)
_SHADOW_CASE_STATES: Final[tuple[str, ...]] = ("NEW", "TRIAGED", "ANALYSIS")

_CENT: Final[Decimal] = Decimal("0.01")
_TENTH: Final[Decimal] = Decimal("0.1")


def _money(value: Decimal) -> str:
    return str(value.quantize(_CENT, rounding=ROUND_HALF_UP))


def _percent(numerator: Decimal, denominator: Decimal) -> str:
    return str((numerator / denominator * 100).quantize(_TENTH, rounding=ROUND_HALF_UP))


def _to_usd(amount: Decimal, currency: str) -> Decimal:
    if currency == "EUR":
        return (amount * _EUR_TO_USD).quantize(_CENT, rounding=ROUND_HALF_UP)
    return amount


def _seeded_amount(rng: random.Random, low: int, high: int) -> Decimal:
    return Decimal(rng.randint(low * 100, high * 100)) / 100


def _generate_obligations(rng: random.Random) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    for i, (key, period, assessed, due, status, fixed) in enumerate(_OBLIGATION_SPECS, start=1):
        obligation_type, jurisdiction, authority, currency, low, high = _AUTHORITIES[key]
        amount = Decimal(fixed) if fixed is not None else _seeded_amount(rng, low, high)
        row: dict[str, Any] = {
            "id": f"OBL-{i:03d}",
            "explain_kind": "obligation-row",
            "type": obligation_type,
            "jurisdiction": jurisdiction,
            "authority": authority,
            "period": period,
            "assessed_month": assessed,
            "status": status,
            "due_date": due,
            "amount": _money(amount),
            "currency": currency,
            "amount_usd": _money(_to_usd(amount, currency)),
        }
        if currency != REPORTING_CURRENCY:
            row["fx_rate"] = FX_DEMO_RATE["rate"]
            row["fx_label"] = FX_DEMO_RATE["label"]
        rows.append(row)
    return rows


def _generate_payments(obligations: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """One payment per filed obligation, copying its amount; real remittance states."""
    rows: list[dict[str, Any]] = []
    for o in obligations:
        status = o["status"]
        if status not in _FILED_STATES:
            continue
        if status in _PAID_STATES:
            # An obligation that is PAID/CLOSED has a payment that SETTLED --
            # the remittance-domain word for confirmed receipt.
            payment_status = "SETTLED"
            if o["id"] == _FIXED_PAID_ID:
                paid_on = _FIXED_PAID_PAYMENT_DATE
            else:
                paid_on = str(date.fromisoformat(o["due_date"]) - timedelta(days=3))
        elif status == "ACCEPTED":
            payment_status, paid_on = "PROCESSING", o["due_date"]
        else:
            payment_status, paid_on = "SUBMITTED", min(o["due_date"], AS_OF)
        rows.append(
            {
                "id": f"PAY-{len(rows) + 1:03d}",
                "explain_kind": "payment-row",
                "obligation_id": o["id"],
                "authority": o["authority"],
                "amount": o["amount"],
                "currency": o["currency"],
                "amount_usd": o["amount_usd"],
                "date": paid_on,
                "status": payment_status,
            }
        )
    return rows


def _bucket_of(obligation: dict[str, Any], as_of: date) -> str:
    """Exactly one pipeline bucket per obligation."""
    status = obligation["status"]
    if status in _FILED_STATES:
        return "Filed & Acknowledged"
    if date.fromisoformat(obligation["due_date"]) < as_of:
        return "Overdue / At Risk"
    if status in _REVIEW_STATES:
        return "Under Legal Review"
    return "Draft Prepared"


def _generate_filing_pipeline(obligations: list[dict[str, Any]]) -> dict[str, Any]:
    as_of = date.fromisoformat(AS_OF)
    total = len(obligations)
    members: dict[str, list[str]] = {label: [] for label in _PIPELINE_LABELS}
    for o in obligations:
        members[_bucket_of(o, as_of)].append(o["id"])
    buckets = [
        {
            "id": f"FIL-{i:03d}",
            "explain_kind": "filing-bucket",
            "label": label,
            "count": str(len(members[label])),
            "total_filings": str(total),
            "percent": _percent(Decimal(len(members[label])), Decimal(total)),
            "as_of": AS_OF,
            "source_ids": members[label],
        }
        for i, label in enumerate(_PIPELINE_LABELS, start=1)
    ]
    return {"as_of": AS_OF, "total_filings": str(total), "buckets": buckets}


def _generate_trend(obligations: list[dict[str, Any]]) -> list[dict[str, Any]]:
    months = sorted({o["assessed_month"] for o in obligations})
    points = []
    for i, month in enumerate(months, start=1):
        members = [o for o in obligations if o["assessed_month"] == month]
        points.append(
            {
                "id": f"TRD-{i:03d}",
                "explain_kind": "trend-point",
                "month": month,
                "label": f"Total tax assessed -- {month}",
                "value": _money(sum((Decimal(o["amount_usd"]) for o in members), Decimal(0))),
                "unit": REPORTING_CURRENCY,
                "source_ids": [o["id"] for o in members],
            }
        )
    return points


def _figure(
    fig_id: str, group: str, label: str, value: str, unit: str, basis: str, **extra: Any
) -> dict[str, Any]:
    return {
        "id": fig_id,
        "explain_kind": "report-figure",
        "group": group,
        "kind": "computed",
        "label": label,
        "value": value,
        "unit": unit,
        "basis": basis,
        **extra,
    }


def _generate_report_figures(
    obligations: list[dict[str, Any]],
    pipeline: dict[str, Any],
    trend: list[dict[str, Any]],
) -> list[dict[str, Any]]:
    as_of = date.fromisoformat(AS_OF)
    window_end = as_of + timedelta(days=UPCOMING_WINDOW_DAYS)

    total_usd = sum((Decimal(o["amount_usd"]) for o in obligations), Decimal(0))

    filed = pipeline["buckets"][0]
    upcoming = [
        o for o in obligations
        if o["status"] not in _PAID_STATES
        and as_of <= date.fromisoformat(o["due_date"]) <= window_end
    ]
    upcoming_usd = sum((Decimal(o["amount_usd"]) for o in upcoming), Decimal(0))
    authorities = sorted({o["authority"] for o in obligations})

    latest, previous = trend[-1], trend[-2]
    latest_v, previous_v = Decimal(latest["value"]), Decimal(previous["value"])
    change = latest_v - previous_v

    figures = [
        _figure(
            "FIG-001", "kpi", "Total tax assessed", _money(total_usd), REPORTING_CURRENCY,
            "sum of obligations[].amount_usd; EUR amounts converted at the fixed demo rate",
            fx_rate=FX_DEMO_RATE["rate"],
            source_ids=[o["id"] for o in obligations],
        ),
        _figure(
            "FIG-002", "kpi", "Filing compliance", filed["percent"], "%",
            "Filed & Acknowledged filings / all filings x 100",
            filed_count=filed["count"], total_filings=filed["total_filings"],
            source_ids=[filed["id"]],
        ),
        _figure(
            "FIG-003", "kpi", "Upcoming remittance (next 15 days)", _money(upcoming_usd),
            REPORTING_CURRENCY,
            "sum of amount_usd for obligations not PAID or CLOSED whose due date "
            "is on or after the as-of date and within 15 days of it",
            as_of=AS_OF, window_days=str(UPCOMING_WINDOW_DAYS),
            source_ids=[o["id"] for o in upcoming],
        ),
        _figure(
            "FIG-004", "kpi", "Active jurisdictions", str(len(authorities)), "count",
            "count of distinct authorities in obligations",
            authorities=authorities,
        ),
        {
            **_figure(
                "FIG-005", "trend", "Total tax assessed vs last month",
                _percent(change, previous_v), "%",
                "(latest month - previous month) / previous month x 100, computed from "
                "the last two trend points",
                previous_value=previous["value"], latest_value=latest["value"],
                change_amount=_money(abs(change)),
                direction="increase" if change >= 0 else "decrease",
                source_ids=[previous["id"], latest["id"]],
            ),
            "kind": "derived",
        },
    ]

    by_authority: dict[str, Decimal] = {}
    for o in obligations:
        by_authority[o["authority"]] = by_authority.get(o["authority"], Decimal(0)) + Decimal(
            o["amount_usd"]
        )
    ranked = sorted(by_authority.items(), key=lambda kv: (-kv[1], kv[0]))
    for n, (authority, amount) in enumerate(ranked, start=6):
        figures.append(
            _figure(
                f"FIG-{n:03d}", "by_authority", f"Tax assessed -- {authority}", _money(amount),
                REPORTING_CURRENCY, "sum of amount_usd for this authority's obligations",
                authority=authority,
                source_ids=[o["id"] for o in obligations if o["authority"] == authority],
            )
        )
    return figures


def _generate_shadow_assurance(obligations: list[dict[str, Any]]) -> list[dict[str, Any]]:
    by_id = {o["id"]: o for o in obligations}
    rows: list[dict[str, Any]] = []
    states = iter(_SHADOW_CASE_STATES)
    for i, (obligation_id, delta_cents) in enumerate(_SHADOW_SPECS, start=1):
        zoiko = Decimal(by_id[obligation_id]["amount_usd"])
        comparator = zoiko + Decimal(delta_cents) / 100
        variance = abs(comparator - zoiko)
        rows.append(
            {
                "id": f"SHD-{i:03d}",
                "explain_kind": "shadow-variance",
                "obligation_id": obligation_id,
                "authority": by_id[obligation_id]["authority"],
                "comparator_value": _money(comparator),
                "zoiko_value": _money(zoiko),
                "absolute_variance": _money(variance),
                "currency": REPORTING_CURRENCY,
                "result": "MATCH" if variance == 0 else "VARIANCE",
                "case_state": "" if variance == 0 else next(states),
            }
        )
    return rows


def generate_fixture() -> dict[str, Any]:
    """Build the whole demo fixture. Deterministic -- same output every call."""
    rng = random.Random(_SEED)
    obligations = _generate_obligations(rng)
    payments = _generate_payments(obligations)
    pipeline = _generate_filing_pipeline(obligations)
    trend = _generate_trend(obligations)
    figures = _generate_report_figures(obligations, pipeline, trend)
    return {
        "disclaimer": DISCLAIMER,
        "currency": REPORTING_CURRENCY,
        "fx_demo_rate": dict(FX_DEMO_RATE),
        "as_of": AS_OF,
        "lifecycle_states": list(LIFECYCLE_STATES),
        "report_figures": figures,
        "trend": {"points": trend, "vs_last_month_id": "FIG-005"},
        "filing_pipeline": pipeline,
        "obligations": obligations,
        "payments": payments,
        "shadow_assurance": _generate_shadow_assurance(obligations),
    }


def _collection(fixture: dict[str, Any], kind: str) -> list[dict[str, Any]]:
    """The rows one explain kind draws from; [] for an unknown kind."""
    if kind == "report-figure":
        return list(fixture["report_figures"])
    if kind == "trend-point":
        return list(fixture["trend"]["points"])
    if kind == "filing-bucket":
        return list(fixture["filing_pipeline"]["buckets"])
    if kind == "obligation-row":
        return list(fixture["obligations"])
    if kind == "payment-row":
        return list(fixture["payments"])
    if kind == "shadow-variance":
        return list(fixture["shadow_assurance"])
    return []


EXPLAIN_KINDS: Final[tuple[str, ...]] = (
    "report-figure", "trend-point", "filing-bucket", "obligation-row", "payment-row",
    "shadow-variance",
)


def flatten_rows(fixture: dict[str, Any]) -> dict[str, dict[str, Any]]:
    """Every citable row from every collection, indexed by id."""
    return {row["id"]: row for kind in EXPLAIN_KINDS for row in _collection(fixture, kind)}


def rows_for_kind(fixture: dict[str, Any], kind: str, ids: list[str]) -> list[dict[str, Any]]:
    """The rows named by *ids* in *kind*'s collection, in request order.

    An id outside *kind*'s collection, or unknown, is omitted; the caller
    treats an empty result as a precondition failure rather than guessing.
    """
    by_id = {row["id"]: row for row in _collection(fixture, kind)}
    return [by_id[i] for i in ids if i in by_id]


if __name__ == "__main__":
    import json

    print(json.dumps(generate_fixture(), indent=2, sort_keys=True))
