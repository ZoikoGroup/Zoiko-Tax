"""Token and cost capture in the evidence record (K2).

The runtime may report how many tokens a call used. The Gateway copies valid counts
into ``UsageMetrics``, computes a cost only from explicitly configured per-1,000-token
prices, and never puts any prompt, answer or provider text in the record. No network,
no key: fake runtimes only, synthetic data only.
"""

from __future__ import annotations

import base64
import json
import logging
from collections.abc import Iterator
from typing import Any

import pytest

from ztax_gateway.evidence_sink import InMemoryEvidenceSink
from ztax_gateway.governance import UseCase, UseCaseRegistry
from ztax_gateway.invocation_evidence import (
    InvocationEvidenceError,
    InvocationEvidenceRecord,
    TokenPricing,
)
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.runtime import MAX_TOKEN_COUNT, Result, Route, RoutingTable, token_count
from ztax_gateway.server import Gateway
from ztax_gateway.wire import Call

_REGION = "eu-west-1"
_UC = "classification-review"
_TENANT = "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f"
_ROUTE = Route("model:fake@1", "provider:fake", "prompt:classification-review-v1")
PROMPT_MARK = "PROMPT-MARK-7f31aa"
ANSWER_MARK = "ANSWER-MARK-b94c02"


class _Runtime:
    def __init__(self, result: Result) -> None:
        self.result = result

    def run(self, route: Route, call: Call) -> Result:
        return self.result


def _rig(
    result: Result, pricing: TokenPricing | None = None
) -> tuple[Gateway, InMemoryEvidenceSink]:
    registry = UseCaseRegistry()
    registry.register(
        UseCase(
            use_case_id=_UC,
            owner="team-ai",
            description="test use case",
            max_risk_tier=RiskTier.T2,
            max_authority=AuthorityOutcome.A2,
            permitted_regions=frozenset([_REGION]),
            permitted_data_classes=frozenset({"P0", "P1"}),
        )
    )
    sink = InMemoryEvidenceSink()
    gateway = Gateway(
        registry,
        RoutingTable({_UC: _ROUTE}),
        _Runtime(result),
        _REGION,
        "0.7.0",
        evidence_sink=sink,
        token_pricing=pricing,
    )
    return gateway, sink


def _call_bytes() -> bytes:
    doc: dict[str, Any] = {
        "kind": "SUGGESTION",
        "governance": {
            "tenant_id": _TENANT,
            "use_case": _UC,
            "authority_outcome": "A1",
            "risk_tier": "T1",
            "region": _REGION,
            "data_classes": ["P1"],
        },
        "subject_ref": "sku:TOKENS-001",
        "input_b64": base64.b64encode(json.dumps({"note": PROMPT_MARK}).encode()).decode("ascii"),
    }
    return json.dumps(doc).encode("utf-8")


def _record(result: Result, pricing: TokenPricing | None = None) -> InvocationEvidenceRecord:
    gateway, sink = _rig(result, pricing)
    gateway.invoke(_call_bytes())
    assert len(sink.records) == 1
    return sink.records[0]


def _result(**kw: Any) -> Result:
    return Result(text=ANSWER_MARK, confidence="0.9", **kw)


# ---------------------------------------------------------------------------
# Tokens reach the record
# ---------------------------------------------------------------------------


def test_tokens_appear_in_the_record_when_the_runtime_returns_them() -> None:
    record = _record(_result(input_tokens=1200, output_tokens=345))
    assert record.usage.input_tokens == 1200
    assert record.usage.output_tokens == 345
    assert record.usage.total_tokens == 1545


def test_tokens_stay_zero_when_the_runtime_returns_none() -> None:
    record = _record(_result())
    assert record.usage.input_tokens == 0
    assert record.usage.output_tokens == 0
    assert record.usage.monetary_cost_usd == 0.0


def test_a_zero_count_is_a_valid_count() -> None:
    record = _record(_result(input_tokens=0, output_tokens=0))
    assert record.usage.input_tokens == 0
    assert record.usage.output_tokens == 0


def test_one_side_only_fills_only_that_side() -> None:
    record = _record(_result(input_tokens=77))
    assert record.usage.input_tokens == 77
    assert record.usage.output_tokens == 0


_BAD_COUNTS: list[object] = [
    -1,
    True,
    False,
    1.5,
    2.0,
    "12",
    "",
    None,
    [3],
    {"n": 3},
    MAX_TOKEN_COUNT + 1,
    10**30,
]


@pytest.mark.parametrize("bad", _BAD_COUNTS, ids=[repr(b) for b in _BAD_COUNTS])
def test_malformed_counts_are_ignored_and_do_not_raise(bad: object) -> None:
    # A runtime can return anything in the dataclass fields; the Gateway re-checks them.
    record = _record(_result(input_tokens=bad, output_tokens=bad))
    assert record.usage.input_tokens == 0
    assert record.usage.output_tokens == 0
    assert record.usage.monetary_cost_usd == 0.0


def test_one_good_and_one_bad_count_keeps_the_good_one() -> None:
    record = _record(_result(input_tokens=500, output_tokens="9"))
    assert record.usage.input_tokens == 500
    assert record.usage.output_tokens == 0


def test_the_largest_accepted_count_is_accepted() -> None:
    record = _record(_result(input_tokens=MAX_TOKEN_COUNT, output_tokens=0))
    assert record.usage.input_tokens == MAX_TOKEN_COUNT


def test_token_count_helper_accepts_only_non_negative_whole_numbers() -> None:
    assert token_count(0) == 0
    assert token_count(41) == 41
    for bad in _BAD_COUNTS:
        if bad is None:
            assert token_count(bad) is None
        else:
            assert token_count(bad) is None, bad


# ---------------------------------------------------------------------------
# Cost: only from configured prices
# ---------------------------------------------------------------------------


def test_cost_is_zero_without_configured_prices() -> None:
    record = _record(_result(input_tokens=2000, output_tokens=500))
    assert record.usage.monetary_cost_usd == 0.0


def test_cost_matches_the_worked_example_when_prices_are_configured() -> None:
    # 2000 input tokens at 0.50 per 1,000 = 1.00; 500 output tokens at 1.50 per 1,000 = 0.75.
    pricing = TokenPricing(input_usd_per_1k=0.50, output_usd_per_1k=1.50)
    record = _record(_result(input_tokens=2000, output_tokens=500), pricing)
    assert record.usage.monetary_cost_usd == 1.75
    assert record.usage.input_tokens == 2000
    assert record.usage.output_tokens == 500


def test_cost_has_no_float_noise() -> None:
    # 3 tokens at 0.1 per 1,000 plus 3 at 0.2 per 1,000 is exactly 0.0009.
    pricing = TokenPricing(input_usd_per_1k=0.1, output_usd_per_1k=0.2)
    assert pricing.cost_usd(3, 3) == 0.0009


def test_cost_stays_zero_when_the_provider_gave_no_counts() -> None:
    pricing = TokenPricing(input_usd_per_1k=0.50, output_usd_per_1k=1.50)
    assert _record(_result(), pricing).usage.monetary_cost_usd == 0.0


def test_cost_stays_zero_when_only_one_count_is_present() -> None:
    pricing = TokenPricing(input_usd_per_1k=0.50, output_usd_per_1k=1.50)
    record = _record(_result(input_tokens=2000), pricing)
    assert record.usage.monetary_cost_usd == 0.0
    assert record.usage.input_tokens == 2000


def test_cost_stays_zero_for_malformed_counts_even_with_prices() -> None:
    pricing = TokenPricing(input_usd_per_1k=0.50, output_usd_per_1k=1.50)
    record = _record(_result(input_tokens=-5, output_tokens=True), pricing)
    assert record.usage.monetary_cost_usd == 0.0


@pytest.mark.parametrize(
    ("input_price", "output_price"),
    [(-0.1, 1.0), (1.0, -0.1), (float("inf"), 1.0), (1.0, float("nan")), (True, 1.0)],
)
def test_prices_must_be_finite_non_negative_numbers(input_price: Any, output_price: Any) -> None:
    with pytest.raises(InvocationEvidenceError):
        TokenPricing(input_usd_per_1k=input_price, output_usd_per_1k=output_price)


def test_zero_prices_are_allowed_and_give_zero_cost() -> None:
    pricing = TokenPricing(input_usd_per_1k=0.0, output_usd_per_1k=0.0)
    assert pricing.cost_usd(10, 10) == 0.0


def test_the_gateway_has_no_built_in_price() -> None:
    import inspect

    from ztax_gateway import server

    assert inspect.signature(server.Gateway).parameters["token_pricing"].default is None


# ---------------------------------------------------------------------------
# No text in the record
# ---------------------------------------------------------------------------


@pytest.fixture()
def debug_logs(caplog: pytest.LogCaptureFixture) -> Iterator[pytest.LogCaptureFixture]:
    caplog.set_level(logging.DEBUG)
    yield caplog


def test_no_prompt_or_answer_text_appears_in_the_record_or_the_logs(
    debug_logs: pytest.LogCaptureFixture,
) -> None:
    pricing = TokenPricing(input_usd_per_1k=0.50, output_usd_per_1k=1.50)
    record = _record(_result(input_tokens=2000, output_tokens=500), pricing)
    stored = json.dumps(record.as_dict(), default=str, sort_keys=True)
    fmt = logging.Formatter()
    logged = "\n".join(fmt.format(r) + repr(vars(r)) for r in debug_logs.records)
    for text in (stored, logged):
        assert PROMPT_MARK not in text
        assert ANSWER_MARK not in text
