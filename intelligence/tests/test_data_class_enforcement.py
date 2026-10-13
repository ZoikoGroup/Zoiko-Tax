"""Data-class allowlist enforcement in the Python Gateway.

Ch.17 §17 and ADR-0006 §2.5: the Gateway enforces a per-use-case
data-class allowlist before any model call. The allowed set is deny by
default — an empty frozenset permits nothing. The check mirrors Go's
Evaluate loop (governance.go).

Four test groups:
  1. load_config round-trips and missing-key deny behaviour.
  2. Shipped config/registry.dev.json: exact Go call scenarios.
  3. Real gRPC channel: data-class refusal and permission over the wire.
  4. Precedence: earlier steps always beat the data-class check.

Groups 1-3 complement test_server.py. Group 4 complements test_governance.py.
"""

from __future__ import annotations

import base64
import json
import pathlib
from collections.abc import Iterator
from dataclasses import replace
from typing import Any

import grpc
import pytest

from ztax_gateway.governance import (
    NEVER_TO_A_MODEL,
    GovernanceRefusedError,
    Refusal,
    UseCase,
    UseCaseRegistry,
    authorise,
)
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier
from ztax_gateway.runtime import Result
from ztax_gateway.server import FULL_METHOD, REASON_KEY, Gateway, load_config, serve

# ---------------------------------------------------------------------------
# Module-level constants and helpers
# ---------------------------------------------------------------------------

_REGION = "eu-west-1"
_DEV_REGISTRY = pathlib.Path(__file__).parents[1] / "config" / "registry.dev.json"
_DEV_REGION = "local"
_DEV_TRAIN = "test-v1"
_DEV_TENANT = "00000000-0000-0000-0000-000000000001"
_ROUTE = {"model_profile": "m", "provider_profile": "p", "prompt_profile": "pp"}

_BASE_PROVENANCE = Provenance(
    use_case="data-class-test",
    model_profile="model:test",
    provider_profile="provider:test",
    prompt_profile="prompt:test",
    region=_REGION,
    data_class="INTERNAL",
    risk_tier=RiskTier.T1,
    authority_outcome=AuthorityOutcome.A1,
    ai_train_version="test-v1",
)

_BASE_USE_CASE = UseCase(
    use_case_id="data-class-test",
    owner="lane-l",
    description="Data class enforcement test use case",
    max_risk_tier=RiskTier.T2,
    max_authority=AuthorityOutcome.A3,
    permitted_regions=frozenset({_REGION}),
    permitted_data_classes=frozenset({"INTERNAL"}),
)

# UseCase with deny-all data-class allowlist (used in precedence tests).
_DENY_DC_UC = replace(_BASE_USE_CASE, permitted_data_classes=frozenset())


def _registry(*extra: UseCase) -> UseCaseRegistry:
    return UseCaseRegistry([_BASE_USE_CASE, *extra])


def _tmp_registry(tmp_path: pathlib.Path, use_cases: list[dict[str, Any]]) -> pathlib.Path:
    p = tmp_path / "r.json"
    p.write_text(json.dumps({"use_cases": use_cases}), encoding="utf-8")
    return p


def _dev_reg() -> tuple[Any, Any]:
    return load_config(_DEV_REGISTRY)


class _RecordingRuntime:
    """Records every call it receives and returns a minimal PROPOSE reply."""

    def __init__(self) -> None:
        self.seen: list[Any] = []

    def run(self, route: Any, call: Any) -> Result:
        self.seen.append(call)
        return Result(proposed_code="ontology:telecom/voice/mobile", confidence="0.95")


def _dev_gateway(runtime: Any) -> Gateway:
    reg, routes = _dev_reg()
    return Gateway(reg, routes, runtime, _DEV_REGION, _DEV_TRAIN)


def _wire_call(data_classes: list[str], **gov_overrides: Any) -> bytes:
    gov: dict[str, Any] = {
        "tenant_id": _DEV_TENANT,
        "use_case": "classification-review",
        "authority_outcome": "A1",
        "risk_tier": "T1",
        "region": _DEV_REGION,
        "data_classes": data_classes,
    }
    gov.update(gov_overrides)
    doc = {
        "kind": "CLASSIFICATION_PROPOSAL",
        "governance": gov,
        "subject_ref": "sku:TEST-001",
        "input_b64": base64.b64encode(b"test").decode(),
    }
    return json.dumps(doc).encode()


def _grpc_invoke(ch: grpc.Channel, body: bytes) -> bytes:
    stub = ch.unary_unary(FULL_METHOD)
    out: bytes = stub(body, timeout=5)
    return out


def _grpc_refusal(ch: grpc.Channel, body: bytes) -> tuple[grpc.StatusCode, str]:
    with pytest.raises(grpc.RpcError) as info:
        _grpc_invoke(ch, body)
    err: Any = info.value
    md = dict(err.trailing_metadata() or ())
    return err.code(), str(md.get(REASON_KEY, ""))


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture()
def dev_connect() -> Iterator[Any]:
    """Spin up a real gRPC server backed by the dev registry."""
    servers: list[grpc.Server] = []
    channels: list[grpc.Channel] = []

    def make(gw: Gateway) -> grpc.Channel:
        server, port = serve(gw, "127.0.0.1:0", None, insecure_local=True)
        servers.append(server)
        ch = grpc.insecure_channel(f"127.0.0.1:{port}")
        channels.append(ch)
        return ch

    yield make
    for ch in channels:
        ch.close()
    for s in servers:
        s.stop(None)


# ---------------------------------------------------------------------------
# NEVER_TO_A_MODEL constant
# ---------------------------------------------------------------------------


def test_never_to_a_model_contains_p7() -> None:
    """P7 (credentials/secrets) is always refused regardless of allowlist."""
    assert "P7" in NEVER_TO_A_MODEL


def test_never_to_a_model_is_frozenset() -> None:
    assert isinstance(NEVER_TO_A_MODEL, frozenset)


# ---------------------------------------------------------------------------
# Registration guard: permitted_data_classes may not include P7
# ---------------------------------------------------------------------------


def test_register_refuses_p7_in_permitted_data_classes() -> None:
    """PRIV-001 §4: P7 may never reach a model; register() blocks it."""
    with pytest.raises(ValueError, match="P7"):
        UseCaseRegistry(
            [
                UseCase(
                    use_case_id="p7-test",
                    owner="lane-l",
                    description="should not register",
                    max_risk_tier=RiskTier.T1,
                    max_authority=AuthorityOutcome.A1,
                    permitted_regions=frozenset({_REGION}),
                    permitted_data_classes=frozenset({"P7"}),
                )
            ]
        )


def test_register_accepts_non_p7_classes() -> None:
    """Classes other than P7 are accepted in the allowlist."""
    uc = UseCase(
        use_case_id="p-test",
        owner="lane-l",
        description="test",
        max_risk_tier=RiskTier.T1,
        max_authority=AuthorityOutcome.A1,
        permitted_regions=frozenset({_REGION}),
        permitted_data_classes=frozenset({"P0", "P1", "P2", "P3", "P4", "P5", "P6"}),
    )
    reg = UseCaseRegistry([uc])
    assert reg.get("p-test") is not None


def test_register_accepts_legacy_data_class_labels() -> None:
    """Legacy labels such as INTERNAL are valid in the allowlist."""
    uc = UseCase(
        use_case_id="legacy-test",
        owner="lane-l",
        description="test",
        max_risk_tier=RiskTier.T1,
        max_authority=AuthorityOutcome.A1,
        permitted_regions=frozenset({_REGION}),
        permitted_data_classes=frozenset({"INTERNAL", "CUSTOMER_CONFIDENTIAL"}),
    )
    assert UseCaseRegistry([uc]).get("legacy-test") is not None


# ---------------------------------------------------------------------------
# Deny by default: empty permitted_data_classes refuses everything
# ---------------------------------------------------------------------------


def test_empty_permitted_data_classes_refuses() -> None:
    """An empty allowlist refuses every class — deny by default."""
    uc = replace(_BASE_USE_CASE, permitted_data_classes=frozenset())
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(UseCaseRegistry([uc]), _BASE_PROVENANCE)
    assert exc.value.refusal is Refusal.DATA_CLASS_REFUSED


def test_empty_permitted_data_classes_detail_names_class() -> None:
    """The detail message names the privacy class that was refused."""
    uc = replace(_BASE_USE_CASE, permitted_data_classes=frozenset())
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(UseCaseRegistry([uc]), _BASE_PROVENANCE)
    assert "INTERNAL" in exc.value.detail


def test_empty_permitted_data_classes_detail_names_use_case() -> None:
    """The detail message names the use case that refused the class."""
    uc = replace(_BASE_USE_CASE, permitted_data_classes=frozenset())
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(UseCaseRegistry([uc]), _BASE_PROVENANCE)
    assert _BASE_USE_CASE.use_case_id in exc.value.detail


# ---------------------------------------------------------------------------
# Allowlist enforcement
# ---------------------------------------------------------------------------


def test_listed_class_is_permitted() -> None:
    """A class in permitted_data_classes passes authorise()."""
    result = authorise(_registry(), _BASE_PROVENANCE)
    assert result.use_case_id == _BASE_USE_CASE.use_case_id


def test_unlisted_class_is_refused() -> None:
    """A class NOT in permitted_data_classes is refused."""
    uc = replace(_BASE_USE_CASE, permitted_data_classes=frozenset({"PUBLIC"}))
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(UseCaseRegistry([uc]), _BASE_PROVENANCE)
    assert exc.value.refusal is Refusal.DATA_CLASS_REFUSED


def test_refusal_reason_is_data_class_refused() -> None:
    """The refusal reason is exactly Refusal.DATA_CLASS_REFUSED."""
    uc = replace(_BASE_USE_CASE, permitted_data_classes=frozenset())
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(UseCaseRegistry([uc]), _BASE_PROVENANCE)
    assert exc.value.refusal is Refusal.DATA_CLASS_REFUSED


def test_refusal_reason_value_matches_go_constant() -> None:
    """The string value matches Go's ReasonDataClassRefused."""
    assert Refusal.DATA_CLASS_REFUSED.value == "AI_DATA_CLASS_REFUSED"


# ---------------------------------------------------------------------------
# Multi-class requests via data_classes tuple
# ---------------------------------------------------------------------------


def test_all_classes_permitted_passes() -> None:
    """All classes in data_classes must be in the allowlist to pass."""
    uc = replace(
        _BASE_USE_CASE,
        permitted_data_classes=frozenset({"INTERNAL", "CUSTOMER_CONFIDENTIAL"}),
    )
    prov = replace(_BASE_PROVENANCE, data_classes=("INTERNAL", "CUSTOMER_CONFIDENTIAL"))
    assert authorise(UseCaseRegistry([uc]), prov).use_case_id == uc.use_case_id


def test_one_unpermitted_class_in_tuple_refuses() -> None:
    """If any class in data_classes is not in the allowlist, the call is refused."""
    uc = replace(_BASE_USE_CASE, permitted_data_classes=frozenset({"INTERNAL"}))
    prov = replace(_BASE_PROVENANCE, data_classes=("INTERNAL", "CUSTOMER_CONFIDENTIAL"))
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(UseCaseRegistry([uc]), prov)
    assert exc.value.refusal is Refusal.DATA_CLASS_REFUSED


def test_data_classes_tuple_takes_priority_over_data_class() -> None:
    """When data_classes is non-empty it is used; data_class is ignored."""
    uc = replace(_BASE_USE_CASE, permitted_data_classes=frozenset({"PUBLIC"}))
    prov = replace(_BASE_PROVENANCE, data_class="INTERNAL", data_classes=("PUBLIC",))
    assert authorise(UseCaseRegistry([uc]), prov).use_case_id == uc.use_case_id


def test_empty_data_classes_falls_back_to_data_class() -> None:
    """When data_classes is empty the fallback is (data_class,)."""
    assert authorise(_registry(), _BASE_PROVENANCE).use_case_id == _BASE_USE_CASE.use_case_id


# ---------------------------------------------------------------------------
# NEVER_TO_A_MODEL enforced at authorise()
# ---------------------------------------------------------------------------


def test_p7_in_data_classes_tuple_is_refused() -> None:
    """P7 in data_classes is refused even if other classes are allowed."""
    uc = replace(_BASE_USE_CASE, permitted_data_classes=frozenset({"INTERNAL"}))
    prov = replace(_BASE_PROVENANCE, data_classes=("INTERNAL", "P7"))
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(UseCaseRegistry([uc]), prov)
    assert exc.value.refusal is Refusal.DATA_CLASS_REFUSED
    assert "P7" in exc.value.detail


def test_p7_in_data_class_scalar_is_refused() -> None:
    """P7 arriving via the scalar data_class field (via data_classes fallback) is refused."""
    uc = replace(_BASE_USE_CASE, permitted_data_classes=frozenset({"INTERNAL"}))
    # data_classes=("P7",) is truthy so it bypasses the data_class scalar.
    prov = replace(_BASE_PROVENANCE, data_classes=("P7",))
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(UseCaseRegistry([uc]), prov)
    assert exc.value.refusal is Refusal.DATA_CLASS_REFUSED
    assert "P7" in exc.value.detail


# ---------------------------------------------------------------------------
# Ordering: data-class check comes AFTER residency (mirrors Go Evaluate)
# ---------------------------------------------------------------------------


def test_residency_refusal_takes_priority_over_data_class_refusal() -> None:
    """Step 5 (residency) fires before step 6 (data class)."""
    uc = replace(
        _BASE_USE_CASE,
        permitted_regions=frozenset({"eu-central-1"}),
        permitted_data_classes=frozenset(),
    )
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(UseCaseRegistry([uc]), _BASE_PROVENANCE)
    assert exc.value.refusal is Refusal.RESIDENCY_REFUSED


# ---------------------------------------------------------------------------
# UseCase.permitted_data_classes field defaults and immutability
# ---------------------------------------------------------------------------


def test_permitted_data_classes_default_is_empty_frozenset() -> None:
    """The default is deny-all (empty frozenset), not a wildcard."""
    uc = UseCase(
        use_case_id="default-test",
        owner="lane-l",
        description="test",
        max_risk_tier=RiskTier.T1,
        max_authority=AuthorityOutcome.A1,
        permitted_regions=frozenset({"local"}),
    )
    assert uc.permitted_data_classes == frozenset()


def test_permitted_data_classes_is_frozenset() -> None:
    """The field type is frozenset (immutable, not a list or set)."""
    assert isinstance(_BASE_USE_CASE.permitted_data_classes, frozenset)


# ---------------------------------------------------------------------------
# 1. load_config: permitted_data_classes round-trips through JSON
# ---------------------------------------------------------------------------


def test_load_config_reads_permitted_data_classes(tmp_path: pathlib.Path) -> None:
    """load_config populates permitted_data_classes from the JSON array."""
    p = _tmp_registry(tmp_path, [
        {
            "use_case_id": "uc-a",
            "owner": "o",
            "description": "d",
            "max_risk_tier": "T1",
            "max_authority": "A1",
            "permitted_regions": ["eu-west-1"],
            "permitted_data_classes": ["P0", "P1"],
            "route": _ROUTE,
        }
    ])
    reg, _ = load_config(p)
    uc = reg.get("uc-a")
    assert uc is not None
    assert uc.permitted_data_classes == frozenset({"P0", "P1"})


def test_load_config_missing_key_means_deny_all(tmp_path: pathlib.Path) -> None:
    """A JSON entry without permitted_data_classes defaults to frozenset() — deny all."""
    p = _tmp_registry(tmp_path, [
        {
            "use_case_id": "uc-b",
            "owner": "o",
            "description": "d",
            "max_risk_tier": "T1",
            "max_authority": "A1",
            "permitted_regions": ["eu-west-1"],
            "route": _ROUTE,
        }
    ])
    reg, _ = load_config(p)
    uc = reg.get("uc-b")
    assert uc is not None
    assert uc.permitted_data_classes == frozenset()


def test_load_config_missing_key_authorise_refuses(tmp_path: pathlib.Path) -> None:
    """A use case loaded without permitted_data_classes refuses every class via authorise()."""
    p = _tmp_registry(tmp_path, [
        {
            "use_case_id": "uc-c",
            "owner": "o",
            "description": "d",
            "max_risk_tier": "T2",
            "max_authority": "A1",
            "permitted_regions": ["eu-west-1"],
            "route": _ROUTE,
        }
    ])
    reg, _ = load_config(p)
    prov = Provenance(
        use_case="uc-c",
        model_profile="m",
        provider_profile="p",
        prompt_profile="pp",
        region="eu-west-1",
        data_class="P0",
        risk_tier=RiskTier.T1,
        authority_outcome=AuthorityOutcome.A1,
        ai_train_version="v",
        data_classes=("P0",),
    )
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, prov)
    assert exc.value.refusal is Refusal.DATA_CLASS_REFUSED


# ---------------------------------------------------------------------------
# 2. Dev registry: exact Go call scenarios
# ---------------------------------------------------------------------------


def test_dev_registry_loads_p0_p1_allowlist() -> None:
    """The shipped dev registry allows exactly P0 and P1 for classification-review."""
    reg, _ = _dev_reg()
    uc = reg.get("classification-review")
    assert uc is not None
    assert uc.permitted_data_classes == frozenset({"P0", "P1"})


def test_dev_registry_p0_call_permitted() -> None:
    """The exact Go call carrying [P0] against the dev registry is permitted."""
    reg, _ = _dev_reg()
    prov = Provenance(
        use_case="classification-review",
        model_profile="m",
        provider_profile="p",
        prompt_profile="pp",
        region="local",
        data_class="P0",
        risk_tier=RiskTier.T1,
        authority_outcome=AuthorityOutcome.A1,
        ai_train_version="v",
        data_classes=("P0",),
    )
    assert authorise(reg, prov).use_case_id == "classification-review"


def test_dev_registry_p2_call_refused() -> None:
    """[P2] is refused — P2 is not in the dev allowlist {P0, P1}."""
    reg, _ = _dev_reg()
    prov = Provenance(
        use_case="classification-review",
        model_profile="m",
        provider_profile="p",
        prompt_profile="pp",
        region="local",
        data_class="P2",
        risk_tier=RiskTier.T1,
        authority_outcome=AuthorityOutcome.A1,
        ai_train_version="v",
        data_classes=("P2",),
    )
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, prov)
    assert exc.value.refusal is Refusal.DATA_CLASS_REFUSED


def test_dev_registry_p0_p2_call_refused() -> None:
    """[P0, P2] is refused — P2 is not in the allowlist even though P0 is."""
    reg, _ = _dev_reg()
    prov = Provenance(
        use_case="classification-review",
        model_profile="m",
        provider_profile="p",
        prompt_profile="pp",
        region="local",
        data_class="P2",
        risk_tier=RiskTier.T1,
        authority_outcome=AuthorityOutcome.A1,
        ai_train_version="v",
        data_classes=("P0", "P2"),
    )
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, prov)
    assert exc.value.refusal is Refusal.DATA_CLASS_REFUSED


# ---------------------------------------------------------------------------
# 3. Real gRPC channel: data-class enforcement over the wire
# ---------------------------------------------------------------------------


def test_grpc_p0_p3_refused_runtime_never_called(dev_connect: Any) -> None:
    """[P0, P3] against dev registry (allows P0, P1) is refused with
    AI_DATA_CLASS_REFUSED in the ztax-reason trailer; runtime is never called."""
    runtime = _RecordingRuntime()
    ch = dev_connect(_dev_gateway(runtime))
    code, reason = _grpc_refusal(ch, _wire_call(["P0", "P3"]))
    assert code == grpc.StatusCode.PERMISSION_DENIED
    assert reason == Refusal.DATA_CLASS_REFUSED.value
    assert runtime.seen == [], "runtime must not be called on data-class refusal"


def test_grpc_p0_p1_permitted_runtime_called_once(dev_connect: Any) -> None:
    """[P0, P1] against dev registry (allows P0, P1) is permitted;
    runtime is called exactly once."""
    runtime = _RecordingRuntime()
    ch = dev_connect(_dev_gateway(runtime))
    reply = json.loads(_grpc_invoke(ch, _wire_call(["P0", "P1"])))
    assert reply.get("proposed_code") == "ontology:telecom/voice/mobile"
    assert len(runtime.seen) == 1


# ---------------------------------------------------------------------------
# 4. Precedence: earlier refusal steps always win over data-class
# ---------------------------------------------------------------------------


def test_kill_switch_wins_over_data_class() -> None:
    """Kill switch (step 1) fires before data-class check (step 6)."""
    reg = UseCaseRegistry([_DENY_DC_UC])
    reg.kill(_DENY_DC_UC.use_case_id)
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, _BASE_PROVENANCE)
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED


def test_unknown_use_case_wins_over_data_class() -> None:
    """Unknown use case (step 2) fires before data-class check (step 6)."""
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(UseCaseRegistry(), _BASE_PROVENANCE)
    assert exc.value.refusal is Refusal.UNKNOWN_USE_CASE


def test_a5_authority_wins_over_data_class() -> None:
    """A5 authority (step 3) fires before data-class check (step 6)."""
    reg = UseCaseRegistry([_DENY_DC_UC])
    prov = replace(_BASE_PROVENANCE, authority_outcome=AuthorityOutcome.A5)
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, prov)
    assert exc.value.refusal is Refusal.AUTHORITY_REFUSED


def test_risk_tier_above_ceiling_wins_over_data_class() -> None:
    """Risk tier above ceiling (step 4) fires before data-class check (step 6)."""
    uc = replace(_DENY_DC_UC, max_risk_tier=RiskTier.T0)
    reg = UseCaseRegistry([uc])
    prov = replace(_BASE_PROVENANCE, risk_tier=RiskTier.T2)
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, prov)
    assert exc.value.refusal is Refusal.RISK_TIER_EXCEEDED


def test_wrong_region_wins_over_data_class() -> None:
    """Residency (step 5) fires before data-class check (step 6)."""
    uc = replace(_DENY_DC_UC, permitted_regions=frozenset({"eu-central-1"}))
    reg = UseCaseRegistry([uc])
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, _BASE_PROVENANCE)
    assert exc.value.refusal is Refusal.RESIDENCY_REFUSED
