"""Tests against the SHIPPED config/registry.dev.json.

Loads the actual file via server.load_config and asserts what Python derives
from it: registration fields, permitted region, route profiles. Then fires a
real gRPC call through Gateway over an in-process channel (same pattern as
test_server.py) using a recording wrapper around FakeRuntime, and checks that
the permitted call succeeds and that the four boundary conditions are each
refused before the runtime is ever called.

ZTAX-AIGOV-REQ-0001: every production AI capability must have a registered
AIUseCase.  This file shows that classification-review is registered with the
expected fields.
"""

from __future__ import annotations

import base64
import json
from pathlib import Path
from typing import Any

import grpc
import pytest

from ztax_gateway.fake_runtime import FakeRuntime
from ztax_gateway.governance import Refusal
from ztax_gateway.runtime import Result, Route
from ztax_gateway.server import FULL_METHOD, REASON_KEY, Gateway, load_config, serve
from ztax_gateway.wire import Call, Kind

# ---------------------------------------------------------------------------
# Path to the shipped registry file (one directory up from this test file)
# ---------------------------------------------------------------------------

_REGISTRY_PATH = Path(__file__).parent.parent / "config" / "registry.dev.json"

# Stable tenant UUID used throughout
_TENANT = "00000000-0000-0000-0000-000000000001"


# ---------------------------------------------------------------------------
# Recording wrapper: delegates to FakeRuntime and records every Call it sees.
# ---------------------------------------------------------------------------


class _RecordingRuntime:
    """Thin delegation wrapper that records calls for assertion."""

    def __init__(self) -> None:
        self._inner = FakeRuntime()
        self.seen: list[Call] = []

    def run(self, route: Route, call: Call) -> Result:
        self.seen.append(call)
        return self._inner.run(route, call)


# ---------------------------------------------------------------------------
# Helpers reused from test_server.py's pattern
# ---------------------------------------------------------------------------


def _raw_call(**overrides: Any) -> bytes:
    """Build a raw gateway-call JSON payload. overrides apply to the top-level
    doc; pass governance={...} to override specific governance fields."""
    gov: dict[str, Any] = {
        "tenant_id": _TENANT,
        "use_case": "classification-review",
        "authority_outcome": "A1",
        "risk_tier": "T1",
        "region": "local",
        "data_classes": ["P0"],
    }
    gov.update(overrides.pop("governance", {}))
    doc: dict[str, Any] = {
        "kind": "CLASSIFICATION_PROPOSAL",
        "governance": gov,
        "subject_ref": "sku:PLAN-UNL-5G",
        "input_b64": base64.b64encode(
            b'{"description":"Unlimited 5G mobile plan with 20 GB hotspot"}'
        ).decode(),
    }
    doc.update(overrides)
    return json.dumps(doc).encode()


@pytest.fixture()
def dev_server() -> Any:
    """Spin up an in-process gRPC server wired to the shipped registry.dev.json."""
    registry, routes = load_config(_REGISTRY_PATH)
    recorder = _RecordingRuntime()
    gw = Gateway(registry, routes, recorder, "local", "0.0.0-dev")
    server, port = serve(gw, "127.0.0.1:0", None, insecure_local=True)
    ch = grpc.insecure_channel(f"127.0.0.1:{port}")
    yield ch, recorder
    ch.close()
    server.stop(None)


def _invoke(ch: grpc.Channel, body: bytes) -> dict[str, Any]:
    stub = ch.unary_unary(FULL_METHOD)
    raw: bytes = stub(body, timeout=5)
    result: dict[str, Any] = json.loads(raw)
    return result


def _refusal(ch: grpc.Channel, body: bytes) -> tuple[grpc.StatusCode, str]:
    stub = ch.unary_unary(FULL_METHOD)
    with pytest.raises(grpc.RpcError) as info:
        stub(body, timeout=5)
    err: Any = info.value
    md = dict(err.trailing_metadata() or ())
    return err.code(), str(md.get(REASON_KEY, ""))


# ---------------------------------------------------------------------------
# Task 2a: assert what load_config derives from registry.dev.json
# ---------------------------------------------------------------------------


class TestRegistryDevConfig:
    """Load the shipped registry.dev.json and assert every field Python derives."""

    def setup_method(self) -> None:
        self.registry, self.routes = load_config(_REGISTRY_PATH)

    def test_classification_review_is_registered(self) -> None:
        uc = self.registry.get("classification-review")
        assert uc is not None, "classification-review must be registered"

    def test_owner_is_lane_l(self) -> None:
        uc = self.registry.get("classification-review")
        assert uc is not None
        assert uc.owner == "lane-l"

    def test_max_authority_is_a1(self) -> None:
        from ztax_gateway.provenance import AuthorityOutcome
        uc = self.registry.get("classification-review")
        assert uc is not None
        assert uc.max_authority == AuthorityOutcome.A1

    def test_max_risk_tier_is_t2(self) -> None:
        from ztax_gateway.provenance import RiskTier
        uc = self.registry.get("classification-review")
        assert uc is not None
        assert uc.max_risk_tier == RiskTier.T2

    def test_permitted_regions_is_exactly_local(self) -> None:
        uc = self.registry.get("classification-review")
        assert uc is not None
        assert uc.permitted_regions == frozenset({"local"})

    def test_not_suspended(self) -> None:
        uc = self.registry.get("classification-review")
        assert uc is not None
        assert uc.suspended is False

    def test_route_model_profile(self) -> None:
        route = self.routes.route("classification-review")
        assert route.model_profile == "model:fake-v1"

    def test_route_provider_profile(self) -> None:
        route = self.routes.route("classification-review")
        assert route.provider_profile == "provider:fake"

    def test_route_prompt_profile(self) -> None:
        route = self.routes.route("classification-review")
        assert route.prompt_profile == "prompt:classification-review-v1"


# ---------------------------------------------------------------------------
# Task 2b: permitted call over real gRPC — returns proposal and route
# ---------------------------------------------------------------------------


class TestPermittedCallOverGrpc:
    """The exact call the Go service sends is permitted and returns what the
    FakeRuntime produced, plus the routing from the registry."""

    def test_permitted_call_returns_proposal_and_route(self, dev_server: Any) -> None:
        ch, recorder = dev_server
        reply = _invoke(ch, _raw_call())
        # Route comes from registry, not from anything the caller said
        assert reply["model_profile"] == "model:fake-v1"
        assert reply["provider_profile"] == "provider:fake"
        assert reply["prompt_profile"] == "prompt:classification-review-v1"
        assert reply["ai_train_version"] == "0.0.0-dev"
        # FakeRuntime default proposal for CLASSIFICATION_PROPOSAL
        assert "proposed_code" in reply
        assert reply["proposed_code"]  # non-empty
        # Runtime was called exactly once
        assert len(recorder.seen) == 1

    def test_permitted_call_runtime_received_call(self, dev_server: Any) -> None:
        ch, recorder = dev_server
        _invoke(ch, _raw_call())
        call = recorder.seen[0]
        assert call.kind == Kind.CLASSIFICATION_PROPOSAL
        assert call.governance.use_case == "classification-review"
        assert call.governance.region == "local"


# ---------------------------------------------------------------------------
# Task 2c: boundary refusals — runtime must NOT be called in any of these
# ---------------------------------------------------------------------------


class TestRefusalsRuntimeNotCalled:
    """Each boundary condition is refused before the runtime is touched."""

    def test_wrong_region_is_refused(self, dev_server: Any) -> None:
        ch, recorder = dev_server
        code, reason = _refusal(ch, _raw_call(governance={"region": "us-east-1"}))
        assert code == grpc.StatusCode.PERMISSION_DENIED
        # Could be RESIDENCY_REFUSED from either Gateway or authorise
        assert reason in (
            Refusal.RESIDENCY_REFUSED.value,
        )
        assert recorder.seen == [], "runtime must not be called on a residency refusal"

    def test_authority_above_a1_ceiling_is_refused(self, dev_server: Any) -> None:
        ch, recorder = dev_server
        code, reason = _refusal(ch, _raw_call(governance={"authority_outcome": "A2"}))
        assert code == grpc.StatusCode.PERMISSION_DENIED
        assert reason == Refusal.AUTHORITY_REFUSED.value
        assert recorder.seen == [], "runtime must not be called on an authority refusal"

    def test_risk_tier_above_t2_ceiling_is_refused(self, dev_server: Any) -> None:
        ch, recorder = dev_server
        code, reason = _refusal(ch, _raw_call(governance={"risk_tier": "T3"}))
        assert code == grpc.StatusCode.PERMISSION_DENIED
        assert reason == Refusal.RISK_TIER_EXCEEDED.value
        assert recorder.seen == [], "runtime must not be called on a tier refusal"

    def test_unknown_use_case_is_refused(self, dev_server: Any) -> None:
        ch, recorder = dev_server
        code, reason = _refusal(ch, _raw_call(governance={"use_case": "no-such-use-case"}))
        assert code == grpc.StatusCode.PERMISSION_DENIED
        assert reason == Refusal.UNKNOWN_USE_CASE.value
        assert recorder.seen == [], "runtime must not be called for an unknown use case"
