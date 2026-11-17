"""Token prices from the environment (T1).

``build_gateway`` reads ``ZTAX_TOKEN_PRICE_INPUT_USD_PER_1K`` and
``ZTAX_TOKEN_PRICE_OUTPUT_USD_PER_1K``. Neither set means no pricing and a cost of 0.0.
Both set and valid means the evidence record carries a cost. Anything else stops start-up
with a fixed text that never holds the value. No network, no key, synthetic data only.
"""

from __future__ import annotations

import base64
import json
import logging
import os
from pathlib import Path
from typing import Any

import pytest

from ztax_gateway.gateway_factory import (
    ENV_PROMPT_PROFILES,
    ENV_TOKEN_PRICE_INPUT,
    ENV_TOKEN_PRICE_OUTPUT,
    GatewayFactoryError,
    build_gateway,
)
from ztax_gateway.invocation_evidence import MAX_PRICE_USD_PER_1K, TokenPricing
from ztax_gateway.production_gates import PromptProfileRegistry
from ztax_gateway.runtime import Result, Route
from ztax_gateway.server import Gateway
from ztax_gateway.wire import Call

_CONFIG_DIR = Path(__file__).resolve().parent.parent / "config"
_REGISTRY = _CONFIG_DIR / "registry.live-demo.json"
_PROFILES = _CONFIG_DIR / "prompt_profiles.live-demo.json"
_SENTINEL = "PRICE-SENTINEL-4e9a1c"
_TOGETHER = f"{ENV_TOKEN_PRICE_INPUT} and {ENV_TOKEN_PRICE_OUTPUT} must be set together"


class _Runtime:
    """Returns a fixed answer with token counts."""

    def run(self, route: Route, call: Call) -> Result:
        return Result(text="ok", confidence="0.9", input_tokens=2000, output_tokens=500)


def _runtime(_: PromptProfileRegistry) -> _Runtime:
    return _Runtime()


@pytest.fixture(autouse=True)
def _profiles(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(_PROFILES))


def _build() -> Gateway:
    return build_gateway(_REGISTRY, "local", "0.0.0-test", runtime_factory=_runtime)


def _other(name: str) -> str:
    return ENV_TOKEN_PRICE_OUTPUT if name == ENV_TOKEN_PRICE_INPUT else ENV_TOKEN_PRICE_INPUT


def _call_bytes() -> bytes:
    doc: dict[str, Any] = {
        "kind": "SUGGESTION",
        "governance": {
            "tenant_id": "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f",
            "use_case": "classification-review",
            "authority_outcome": "A1",
            "risk_tier": "T1",
            "region": "local",
            "data_classes": ["P0"],
        },
        "subject_ref": "sku:PRICING-001",
        "input_b64": base64.b64encode(b'{"note": "x"}').decode("ascii"),
    }
    return json.dumps(doc).encode("utf-8")


def _usage_in_the_record(caplog: pytest.LogCaptureFixture, gateway: Gateway) -> dict[str, Any]:
    with caplog.at_level(logging.INFO, logger="ztax_gateway.evidence"):
        gateway.invoke(_call_bytes())
    lines = [r.getMessage() for r in caplog.records if r.name == "ztax_gateway.evidence"]
    assert len(lines) == 1
    usage: dict[str, Any] = json.loads(lines[0])["usage"]
    return usage


# ---------------------------------------------------------------------------
# What is read
# ---------------------------------------------------------------------------


def test_neither_variable_set_means_no_pricing() -> None:
    assert _build()._token_pricing is None


def test_both_variables_set_build_the_pricing(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(ENV_TOKEN_PRICE_INPUT, "0.5")
    monkeypatch.setenv(ENV_TOKEN_PRICE_OUTPUT, "1.5")
    assert _build()._token_pricing == TokenPricing(0.5, 1.5)


@pytest.mark.parametrize("value", ["0", "0.0", " 2 ", "1e-3", str(MAX_PRICE_USD_PER_1K)])
def test_the_edges_of_the_valid_range_are_accepted(
    monkeypatch: pytest.MonkeyPatch, value: str
) -> None:
    monkeypatch.setenv(ENV_TOKEN_PRICE_INPUT, value)
    monkeypatch.setenv(ENV_TOKEN_PRICE_OUTPUT, value)
    assert _build()._token_pricing == TokenPricing(float(value), float(value))


# ---------------------------------------------------------------------------
# What is refused, with a fixed text
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("name", [ENV_TOKEN_PRICE_INPUT, ENV_TOKEN_PRICE_OUTPUT])
def test_only_one_variable_set_is_refused(monkeypatch: pytest.MonkeyPatch, name: str) -> None:
    monkeypatch.setenv(name, "0.5")
    with pytest.raises(GatewayFactoryError) as info:
        _build()
    assert str(info.value) == _TOGETHER


_BAD = [
    "",
    "   ",
    "abc",
    "nan",
    "NaN",
    "inf",
    "-inf",
    "-0.01",
    "-1",
    str(MAX_PRICE_USD_PER_1K + 1),
    "1e999",
    "1,5",
    "0x10",
]


@pytest.mark.parametrize("bad", _BAD)
@pytest.mark.parametrize("which", [ENV_TOKEN_PRICE_INPUT, ENV_TOKEN_PRICE_OUTPUT])
def test_a_bad_value_is_refused_with_the_fixed_text(
    monkeypatch: pytest.MonkeyPatch, bad: str, which: str
) -> None:
    monkeypatch.setenv(_other(which), "1")
    monkeypatch.setenv(which, bad)
    with pytest.raises(GatewayFactoryError) as info:
        _build()
    assert str(info.value) == (
        f"{which} must be a number from 0 to {MAX_PRICE_USD_PER_1K} (USD per 1,000 tokens)"
    )


@pytest.mark.parametrize("which", [ENV_TOKEN_PRICE_INPUT, ENV_TOKEN_PRICE_OUTPUT])
def test_the_value_never_appears_in_the_error_and_the_cause_is_dropped(
    monkeypatch: pytest.MonkeyPatch, which: str
) -> None:
    monkeypatch.setenv(_other(which), "1")
    monkeypatch.setenv(which, _SENTINEL)
    with pytest.raises(GatewayFactoryError) as info:
        _build()
    assert _SENTINEL not in str(info.value)
    assert _SENTINEL not in repr(info.value)
    assert info.value.__cause__ is None
    assert info.value.__context__ is None


# ---------------------------------------------------------------------------
# End to end: the cost in the evidence record
# ---------------------------------------------------------------------------


def test_with_both_prices_the_record_carries_the_cost(
    monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    monkeypatch.setenv(ENV_TOKEN_PRICE_INPUT, "0.5")
    monkeypatch.setenv(ENV_TOKEN_PRICE_OUTPUT, "1.5")
    usage = _usage_in_the_record(caplog, _build())
    # 2000 in at 0.5 per 1k is 1.0; 500 out at 1.5 per 1k is 0.75.
    assert usage["input_tokens"] == 2000
    assert usage["output_tokens"] == 500
    assert usage["monetary_cost_usd"] == 1.75


def test_without_prices_the_cost_stays_zero(caplog: pytest.LogCaptureFixture) -> None:
    usage = _usage_in_the_record(caplog, _build())
    assert usage["input_tokens"] == 2000
    assert usage["monetary_cost_usd"] == 0.0


def test_the_price_variables_are_cleared_before_every_test() -> None:
    # The autouse fixture in conftest.py does this; a price left in the shell would
    # otherwise put a cost in records of tests that expect none.
    assert ENV_TOKEN_PRICE_INPUT not in os.environ
    assert ENV_TOKEN_PRICE_OUTPUT not in os.environ
