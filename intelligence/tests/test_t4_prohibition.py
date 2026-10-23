"""REQ-0107 - T4 hard stop (ZTAX-AIGOV-001 SS5).

ZTAX-AIGOV-001 SS5 defines T4 as "CRITICAL / RESTRICTED - Default prohibited;
only deterministic non-AI control may own final authority. Any exceptional use
requires constitutional amendment."

REQ-0107: "No T4 prohibited authority use may be enabled through feature flags
or customer configuration."

This file verifies all four enforcement surfaces:

  a. authorise() refuses T4 unconditionally regardless of the use-case ceiling
     (including when the ceiling itself is T4).
  b. Lower tiers T0-T3 are unaffected by the hard stop.
  c. Precedence order is preserved: kill switch, unknown use case and A5 all
     fire before the T4 check, matching the documented step order.
  d. load_config() rejects any use case that declares max_risk_tier='T4'.
  e. Through the real Gateway, a T4 call gives PERMISSION_DENIED with reason
     AI_RISK_TIER_EXCEEDED, the runtime is never invoked, and exactly one
     BLOCK evidence record is written to the sink.

Self-contained: helpers are copied from test_ai_boundary_proof.py patterns;
no cross-test-module imports.
"""

from __future__ import annotations

import base64
import json
from dataclasses import replace
from pathlib import Path
from typing import Any

import grpc
import pytest

from ztax_gateway import (
    AuthorityOutcome,
    GovernanceRefusedError,
    Refusal,
    RiskTier,
    UseCase,
    UseCaseRegistry,
    authorise,
)
from ztax_gateway.evidence_sink import InMemoryEvidenceSink
from ztax_gateway.invocation_evidence import InvocationOutcome
from ztax_gateway.provenance import Provenance
from ztax_gateway.runtime import Result, Route, RoutingTable
from ztax_gateway.server import Gateway, GatewayError, load_config

# ---------------------------------------------------------------------------
# Module-level constants (mirror test_ai_boundary_proof.py)
# ---------------------------------------------------------------------------

_REGION = "euc1-dev-01"
_TRAIN = "ai-2027.03.1"
_USE_CASE = "classification-review"
_OWNER = "lane-l"
_TENANT = "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f"

# ---------------------------------------------------------------------------
# Builders
# ---------------------------------------------------------------------------


def _make_use_case(
    use_case_id: str = _USE_CASE,
    max_risk_tier: RiskTier = RiskTier.T3,
    max_authority: AuthorityOutcome = AuthorityOutcome.A1,
) -> UseCase:
    return UseCase(
        use_case_id=use_case_id,
        owner=_OWNER,
        description="Propose an ontology mapping for human review",
        max_risk_tier=max_risk_tier,
        max_authority=max_authority,
        permitted_regions=frozenset({_REGION}),
        permitted_data_classes=frozenset({"P0"}),
    )


def _make_registry(*use_cases: UseCase) -> UseCaseRegistry:
    if not use_cases:
        return UseCaseRegistry([_make_use_case()])
    return UseCaseRegistry(list(use_cases))


def _base_provenance(
    risk_tier: RiskTier = RiskTier.T1,
    authority_outcome: AuthorityOutcome = AuthorityOutcome.A1,
    use_case: str = _USE_CASE,
    region: str = _REGION,
) -> Provenance:
    return Provenance(
        use_case=use_case,
        model_profile="model:cls",
        provider_profile="provider:a",
        prompt_profile="prompt:cls-v4",
        region=region,
        data_class="P0",
        risk_tier=risk_tier,
        authority_outcome=authority_outcome,
        ai_train_version=_TRAIN,
        data_classes=("P0",),
    )


class _RecordingRuntime:
    """Records every call it receives; delegates to a canned-answer stub."""

    def __init__(self) -> None:
        self.seen: list[Any] = []

    def run(self, route: Route, call: Any) -> Result:
        self.seen.append(call)
        return Result(proposed_code="UNCLASSIFIED", confidence="0.90")


def _make_gateway(
    use_case: UseCase | None = None,
    runtime: Any = None,
    sink: InMemoryEvidenceSink | None = None,
) -> tuple[Gateway, InMemoryEvidenceSink]:
    uc = use_case or _make_use_case()
    reg = UseCaseRegistry([uc])
    routes = {uc.use_case_id: Route("model:cls", "provider:a", "prompt:cls-v4")}
    mem_sink = sink or InMemoryEvidenceSink()
    if runtime is None:
        runtime = _RecordingRuntime()
    gw = Gateway(reg, RoutingTable(routes), runtime, _REGION, _TRAIN, evidence_sink=mem_sink)
    return gw, mem_sink


def _t4_call_bytes(
    use_case: str = _USE_CASE,
    region: str = _REGION,
    authority: str = "A1",
) -> bytes:
    """Build a wire-format call with risk_tier=T4."""
    gov: dict[str, Any] = {
        "tenant_id": _TENANT,
        "use_case": use_case,
        "authority_outcome": authority,
        "risk_tier": "T4",
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


# ---------------------------------------------------------------------------
# Typed base provenance for authorise() unit tests
# ---------------------------------------------------------------------------

_BASE_PROVENANCE = _base_provenance()


def _registry_with_ceiling(ceiling: RiskTier) -> UseCaseRegistry:
    """Return a registry whose use case has the given max_risk_tier ceiling.

    Note: UseCaseRegistry.register() does not reject T4 ceilings (see its
    docstring). That allows existing test fixtures in test_capacity.py,
    test_invocation_evidence.py, test_observability.py and test_resilience.py
    to continue working. The call-time check in authorise() is the enforcement
    point, and this function exercises exactly that scenario.
    """
    uc = UseCase(
        use_case_id=_USE_CASE,
        owner=_OWNER,
        description="ceiling test fixture",
        max_risk_tier=ceiling,
        max_authority=AuthorityOutcome.A1,
        permitted_regions=frozenset({_REGION}),
        permitted_data_classes=frozenset({"P0"}),
    )
    return UseCaseRegistry([uc])


# ---------------------------------------------------------------------------
# a. T4 refused unconditionally - parametrized across ALL ceilings T0-T4
#    This is the key proof: even a T4 ceiling cannot permit a T4 call.
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "ceiling",
    [RiskTier.T0, RiskTier.T1, RiskTier.T2, RiskTier.T3, RiskTier.T4],
)
def test_t4_refused_for_every_use_case_ceiling(ceiling: RiskTier) -> None:
    """T4 call is refused regardless of what ceiling the use case declares.

    The ceiling=T4 case is the decisive proof: without the unconditional check,
    T4 > T4 evaluates to False and the call would be permitted.
    """
    prov = replace(_BASE_PROVENANCE, risk_tier=RiskTier.T4)
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(_registry_with_ceiling(ceiling), prov)
    assert exc.value.refusal is Refusal.RISK_TIER_EXCEEDED


@pytest.mark.parametrize(
    "ceiling",
    [RiskTier.T0, RiskTier.T1, RiskTier.T2, RiskTier.T3, RiskTier.T4],
)
def test_t4_refusal_message_names_the_tier_for_every_ceiling(ceiling: RiskTier) -> None:
    """The detail string must mention T4 so operators know what was refused."""
    prov = replace(_BASE_PROVENANCE, risk_tier=RiskTier.T4)
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(_registry_with_ceiling(ceiling), prov)
    assert "T4" in exc.value.detail


def test_t4_refusal_carries_redacted_context() -> None:
    """The audit context must not contain any request payload."""
    prov = replace(_BASE_PROVENANCE, risk_tier=RiskTier.T4)
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(_registry_with_ceiling(RiskTier.T4), prov)
    ctx = exc.value.context
    assert "use_case" in ctx
    assert "risk_tier" in ctx
    assert not any(k in ctx for k in ("prompt", "text", "payload", "subject_ref"))


# ---------------------------------------------------------------------------
# b. Positive controls: T0-T3 calls at their own ceilings are still permitted
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "tier",
    [RiskTier.T0, RiskTier.T1, RiskTier.T2, RiskTier.T3],
)
def test_tier_at_its_own_ceiling_is_permitted(tier: RiskTier) -> None:
    """A call at tier X against a ceiling of X must pass through the T4 gate."""
    prov = replace(_BASE_PROVENANCE, risk_tier=tier)
    # authorise() passes; no GovernanceRefusedError is raised.
    result = authorise(_registry_with_ceiling(tier), prov)
    assert result.use_case_id == _USE_CASE


@pytest.mark.parametrize(
    "tier",
    [RiskTier.T0, RiskTier.T1, RiskTier.T2, RiskTier.T3],
)
def test_tiers_below_t4_are_not_blocked_by_the_hard_stop(tier: RiskTier) -> None:
    """The T4 gate must not over-refuse any tier below T4."""
    prov = replace(_BASE_PROVENANCE, risk_tier=tier)
    try:
        authorise(_registry_with_ceiling(tier), prov)
    except GovernanceRefusedError as exc:
        # The test permits other refusals (e.g. residency if region mismatch)
        # but the T4 hard stop must NOT be the reason.
        assert exc.refusal is not Refusal.RISK_TIER_EXCEEDED, (
            f"tier {tier.value} was incorrectly caught by the T4 hard stop"
        )


# ---------------------------------------------------------------------------
# c. Precedence: kill switch, unknown use case and A5 all fire BEFORE T4
# ---------------------------------------------------------------------------


def test_global_kill_fires_before_t4_check() -> None:
    """A globally killed registry must report KILL_SWITCH_ENGAGED, not T4.

    If T4 checked before the kill switch the refusal code would be wrong during
    an incident when an operator activates the global kill switch.
    """
    reg = _registry_with_ceiling(RiskTier.T3)
    reg.engage_global_kill()
    prov = replace(_BASE_PROVENANCE, risk_tier=RiskTier.T4)
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, prov)
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED


def test_per_use_case_kill_fires_before_t4_check() -> None:
    """A per-use-case kill must report KILL_SWITCH_ENGAGED, not T4."""
    reg = _registry_with_ceiling(RiskTier.T3)
    reg.kill(_USE_CASE)
    prov = replace(_BASE_PROVENANCE, risk_tier=RiskTier.T4)
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, prov)
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED


def test_unknown_use_case_fires_before_t4_check() -> None:
    """An unregistered use case must report UNKNOWN_USE_CASE, not T4.

    The kill switch is not engaged, so the unregistered-use-case check fires.
    """
    reg = _registry_with_ceiling(RiskTier.T3)
    prov = replace(_BASE_PROVENANCE, risk_tier=RiskTier.T4, use_case="never-registered")
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, prov)
    assert exc.value.refusal is Refusal.UNKNOWN_USE_CASE


def test_a5_authority_fires_before_t4_check() -> None:
    """A5 authority must report AUTHORITY_REFUSED, not T4.

    Step 3 (authority) is evaluated before step 4 (risk tier). A T4 + A5 call
    must surface as an authority refusal, not a risk-tier refusal.
    """
    prov = replace(
        _BASE_PROVENANCE,
        risk_tier=RiskTier.T4,
        authority_outcome=AuthorityOutcome.A5,
    )
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(_registry_with_ceiling(RiskTier.T3), prov)
    assert exc.value.refusal is Refusal.AUTHORITY_REFUSED


# ---------------------------------------------------------------------------
# d. load_config() - T4 ceiling is rejected; T0-T3 are accepted
# ---------------------------------------------------------------------------


def _minimal_config(max_risk_tier: str, tmp_path: Path) -> Path:
    doc = {
        "use_cases": [
            {
                "use_case_id": "uc-config-test",
                "owner": "config-owner",
                "description": "Config load test use case",
                "max_risk_tier": max_risk_tier,
                "max_authority": "A1",
                "permitted_regions": [_REGION],
                "permitted_data_classes": ["P0"],
                "route": {
                    "model_profile": "model:cls",
                    "provider_profile": "provider:a",
                    "prompt_profile": "prompt:cls-v4",
                },
            }
        ]
    }
    p = tmp_path / "config.json"
    p.write_text(json.dumps(doc), encoding="utf-8")
    return p


def test_load_config_rejects_t4_ceiling(tmp_path: Path) -> None:
    """A config file that declares max_risk_tier='T4' must raise ValueError."""
    path = _minimal_config("T4", tmp_path)
    with pytest.raises(ValueError, match="T4"):
        load_config(path)


def test_load_config_rejects_t4_ceiling_error_message_names_use_case(tmp_path: Path) -> None:
    """The error message must identify the offending use case by ID."""
    path = _minimal_config("T4", tmp_path)
    with pytest.raises(ValueError, match="uc-config-test"):
        load_config(path)


@pytest.mark.parametrize("tier", ["T0", "T1", "T2", "T3"])
def test_load_config_accepts_legal_ceilings(tier: str, tmp_path: Path) -> None:
    """T0-T3 ceilings must continue to load without error."""
    path = _minimal_config(tier, tmp_path)
    registry, _ = load_config(path)
    assert registry.get("uc-config-test") is not None


# ---------------------------------------------------------------------------
# e. Real Gateway integration: T4 call via Gateway.invoke()
#    - PERMISSION_DENIED / AI_RISK_TIER_EXCEEDED
#    - Runtime never invoked (recorded calls == 0)
#    - Exactly one BLOCK evidence record written to the sink
# ---------------------------------------------------------------------------


def test_gateway_invoke_t4_gives_permission_denied() -> None:
    """A T4 payload through Gateway.invoke() must raise GatewayError with
    status PERMISSION_DENIED and reason AI_RISK_TIER_EXCEEDED.
    """
    gw, _ = _make_gateway()
    data = _t4_call_bytes()
    with pytest.raises(GatewayError) as exc:
        gw.invoke(data)
    err = exc.value
    assert err.code == grpc.StatusCode.PERMISSION_DENIED
    assert err.reason == Refusal.RISK_TIER_EXCEEDED.value


def test_gateway_invoke_t4_runtime_never_reached() -> None:
    """The model runtime must receive zero calls when T4 is blocked."""
    recorder = _RecordingRuntime()
    gw, _ = _make_gateway(runtime=recorder)
    data = _t4_call_bytes()
    with pytest.raises(GatewayError):
        gw.invoke(data)
    assert len(recorder.seen) == 0, (
        f"Runtime was invoked {len(recorder.seen)} time(s); expected 0"
    )


def test_gateway_invoke_t4_writes_exactly_one_block_record() -> None:
    """Exactly one BLOCK evidence record must be written to the sink."""
    gw, sink = _make_gateway()
    data = _t4_call_bytes()
    with pytest.raises(GatewayError):
        gw.invoke(data)
    assert len(sink.records) == 1
    assert sink.last.invocation_outcome == InvocationOutcome.BLOCK


def test_gateway_invoke_t4_block_record_has_failed_guardrail() -> None:
    """The BLOCK evidence record must carry a failed guardrail result."""
    gw, sink = _make_gateway()
    data = _t4_call_bytes()
    with pytest.raises(GatewayError):
        gw.invoke(data)
    assert sink.last.any_guardrail_failed()
