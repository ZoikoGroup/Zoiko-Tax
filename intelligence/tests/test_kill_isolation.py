"""Killing one AI use case must not affect any other (ADR-0006 §2.5).

The kill switch has two granularities on purpose: a per-use-case kill is a
targeted response to one capability behaving badly, and the global kill is
for "stop everything". This file proves the per-use-case kill is actually
scoped to the one use case named -- a different, un-killed use case must
keep working exactly as before, with its calls still reaching the model
runtime and returning normally.

Self-contained: helpers are copied from test_t4_prohibition.py's pattern
(itself copied from test_ai_boundary_proof.py); no cross-test-module imports.
"""

from __future__ import annotations

import base64
import json
from typing import Any

import grpc
import pytest

from ztax_gateway import (
    AuthorityOutcome,
    RiskTier,
    UseCase,
    UseCaseRegistry,
)
from ztax_gateway.evidence_sink import InMemoryEvidenceSink
from ztax_gateway.runtime import Result, Route, RoutingTable
from ztax_gateway.server import Gateway, GatewayError

# ---------------------------------------------------------------------------
# Module-level constants
# ---------------------------------------------------------------------------

_REGION = "euc1-dev-01"
_TRAIN = "ai-2027.03.1"
_TENANT = "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f"
_USE_CASE_A = "classification-review"
_USE_CASE_B = "entity-extraction"
_OWNER = "lane-l"

# ---------------------------------------------------------------------------
# Builders
# ---------------------------------------------------------------------------


def _use_case(use_case_id: str) -> UseCase:
    return UseCase(
        use_case_id=use_case_id,
        owner=_OWNER,
        description=f"Test use case {use_case_id!r}",
        max_risk_tier=RiskTier.T3,
        max_authority=AuthorityOutcome.A1,
        permitted_regions=frozenset({_REGION}),
        permitted_data_classes=frozenset({"P0"}),
    )


class _RecordingRuntime:
    """Records every call it receives, tagged by which use case it was for;
    delegates to a canned-answer stub."""

    def __init__(self) -> None:
        self.seen: list[Any] = []

    def run(self, route: Route, call: Any) -> Result:
        self.seen.append(call)
        return Result(proposed_code="UNCLASSIFIED", confidence="0.90")

    def seen_use_cases(self) -> list[str]:
        return [c.governance.use_case for c in self.seen]


def _make_two_use_case_gateway() -> tuple[Gateway, UseCaseRegistry, _RecordingRuntime]:
    """A Gateway with TWO registered, routed use cases and one shared
    recording runtime, so a test can see exactly which use case's calls
    reached it."""
    reg = UseCaseRegistry([_use_case(_USE_CASE_A), _use_case(_USE_CASE_B)])
    routes = RoutingTable(
        {
            _USE_CASE_A: Route("model:cls", "provider:a", "prompt:cls-v4"),
            _USE_CASE_B: Route("model:extract", "provider:a", "prompt:extract-v1"),
        }
    )
    runtime = _RecordingRuntime()
    gw = Gateway(reg, routes, runtime, _REGION, _TRAIN, evidence_sink=InMemoryEvidenceSink())
    return gw, reg, runtime


def _call_bytes(use_case: str, region: str = _REGION) -> bytes:
    """Build a wire-format call for one use case, otherwise ordinary (T1/A1)."""
    gov: dict[str, Any] = {
        "tenant_id": _TENANT,
        "use_case": use_case,
        "authority_outcome": "A1",
        "risk_tier": "T1",
        "region": region,
        "data_classes": ["P0"],
    }
    doc: dict[str, Any] = {
        "kind": "CLASSIFICATION_PROPOSAL",
        "governance": gov,
        "subject_ref": "sku:PLAN-UNL-5G",
        "input_b64": base64.b64encode(b"{}").decode(),
    }
    return json.dumps(doc).encode()


def _assert_refused_by_kill_switch(gw: Gateway, use_case: str) -> None:
    with pytest.raises(GatewayError) as exc:
        gw.invoke(_call_bytes(use_case))
    assert exc.value.code == grpc.StatusCode.PERMISSION_DENIED
    assert exc.value.reason == "AI_KILL_SWITCH_ENGAGED"


def _assert_permitted(gw: Gateway, use_case: str) -> bytes:
    """Invoke succeeds; returns the reply bytes."""
    return gw.invoke(_call_bytes(use_case))


# ---------------------------------------------------------------------------
# 1. Killing A refuses only A, and the runtime is never reached for it.
# ---------------------------------------------------------------------------


def test_killing_one_use_case_refuses_it_and_never_reaches_the_runtime() -> None:
    gw, reg, runtime = _make_two_use_case_gateway()
    reg.kill(_USE_CASE_A)

    _assert_refused_by_kill_switch(gw, _USE_CASE_A)

    assert runtime.seen == [], "the killed use case's call reached the runtime"


# ---------------------------------------------------------------------------
# 2. B is unaffected: still permitted, and its call reaches the runtime.
# ---------------------------------------------------------------------------


def test_other_use_case_is_unaffected_and_reaches_the_runtime() -> None:
    gw, reg, runtime = _make_two_use_case_gateway()
    reg.kill(_USE_CASE_A)

    reply = _assert_permitted(gw, _USE_CASE_B)

    assert reply, "B's call did not return normally"
    assert runtime.seen_use_cases() == [_USE_CASE_B]


# ---------------------------------------------------------------------------
# 3. Un-killing A restores it; B was never affected by any of this.
# ---------------------------------------------------------------------------


def test_unkilling_restores_the_use_case_without_affecting_the_other() -> None:
    gw, reg, runtime = _make_two_use_case_gateway()
    reg.kill(_USE_CASE_A)

    _assert_refused_by_kill_switch(gw, _USE_CASE_A)
    _assert_permitted(gw, _USE_CASE_B)

    reg.revive(_USE_CASE_A)
    _assert_permitted(gw, _USE_CASE_A)

    # B's second call, after A was revived, must still work -- A's lifecycle
    # has no bearing on B at any point.
    _assert_permitted(gw, _USE_CASE_B)

    assert runtime.seen_use_cases() == [_USE_CASE_B, _USE_CASE_A, _USE_CASE_B]


# ---------------------------------------------------------------------------
# 4. Global kill refuses both -- the contrast that makes test 1's isolation
#    meaningful: a per-use-case kill is narrow, the global kill is not.
# ---------------------------------------------------------------------------


def test_global_kill_refuses_both_use_cases() -> None:
    gw, reg, runtime = _make_two_use_case_gateway()
    reg.engage_global_kill()

    _assert_refused_by_kill_switch(gw, _USE_CASE_A)
    _assert_refused_by_kill_switch(gw, _USE_CASE_B)

    assert runtime.seen == [], "a globally killed call reached the runtime"


# ---------------------------------------------------------------------------
# 5. Order independence: kill A then B, or B then A -- same end state.
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("order", [(_USE_CASE_A, _USE_CASE_B), (_USE_CASE_B, _USE_CASE_A)])
def test_kill_order_does_not_change_the_result(order: tuple[str, str]) -> None:
    gw, reg, runtime = _make_two_use_case_gateway()
    first, second = order
    reg.kill(first)
    reg.kill(second)

    # Both use cases are killed regardless of which was killed first.
    _assert_refused_by_kill_switch(gw, _USE_CASE_A)
    _assert_refused_by_kill_switch(gw, _USE_CASE_B)
    assert runtime.seen == []

    # Reviving one at a time, in the same order, restores each independently.
    reg.revive(first)
    _assert_permitted(gw, first)
    _assert_refused_by_kill_switch(gw, second)

    reg.revive(second)
    _assert_permitted(gw, second)
