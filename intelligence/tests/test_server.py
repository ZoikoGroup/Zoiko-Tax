"""The Gateway server, end to end over a real gRPC channel.

What crosses here is exactly what the Go client sends: the JSON of
``contracts/schemas/ai/gateway-call.schema.json`` as raw bytes on
``/ztax.gateway.v1.ModelGateway/Invoke``. The call and the reply are both
validated against the canonical schemas, so the two halves of the boundary are
held to one authority rather than to each other.
"""

from __future__ import annotations

import base64
import json
from collections.abc import Iterator
from typing import Any

import grpc
import pytest

from test_ai_schemas import _Validator
from ztax_gateway.governance import UseCase, UseCaseRegistry
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.runtime import (
    Result,
    Route,
    RoutingTable,
    RuntimeUnavailableError,
    UnconfiguredRuntime,
)
from ztax_gateway.server import FULL_METHOD, REASON_KEY, Gateway, serve
from ztax_gateway.wire import Call, Kind, WireError, decode_call

TENANT = "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f"
REGION = "euc1-dev-01"
TRAIN = "ai-2027.03.1"


class StaticRuntime:
    """A deterministic runtime for tests. Never shipped: production wires
    UnconfiguredRuntime until a provider is approved."""

    def __init__(self, fail: bool = False) -> None:
        self.fail = fail
        self.seen: list[Call] = []

    def run(self, route: Route, call: Call) -> Result:
        self.seen.append(call)
        if self.fail:
            raise RuntimeUnavailableError("provider timed out")
        match call.kind:
            case Kind.SUGGESTION:
                return Result(text="Consider the reduced rate.", payload="{}")
            case Kind.EXTRACTION:
                return Result(fields={"invoiceNumber": "INV-1"})
            case _:
                return Result(proposed_code="ontology:telecom/voice/mobile", confidence="0.97")


def registry() -> tuple[UseCaseRegistry, RoutingTable]:
    reg = UseCaseRegistry(
        [
            UseCase(
                use_case_id="classification-review",
                owner="lane-l",
                description="Propose an ontology mapping for human review",
                max_risk_tier=RiskTier.T2,
                max_authority=AuthorityOutcome.A1,
                permitted_regions=frozenset({REGION}),
            )
        ]
    )
    routes = RoutingTable(
        {
            "classification-review": Route(
                "model:cls-2027.03", "provider:approved-a", "prompt:cls-v4"
            )
        }
    )
    return reg, routes


def call(**over: Any) -> bytes:
    gov: dict[str, Any] = {
        "tenant_id": TENANT,
        "use_case": "classification-review",
        "authority_outcome": "A1",
        "risk_tier": "T1",
        "region": REGION,
        "data_classes": ["P0"],
    }
    gov.update(over.pop("governance", {}))
    doc: dict[str, Any] = {
        "kind": "CLASSIFICATION_PROPOSAL",
        "governance": gov,
        "subject_ref": "sku:PLAN-UNL-5G",
        "input_b64": base64.b64encode(b'{"sku":"PLAN-UNL-5G"}').decode(),
    }
    doc.update(over)
    return json.dumps(doc).encode()


def gateway(runtime: Any = None) -> Gateway:
    reg, routes = registry()
    return Gateway(reg, routes, runtime if runtime is not None else StaticRuntime(), REGION, TRAIN)


@pytest.fixture
def connect() -> Iterator[Any]:
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


def invoke(ch: grpc.Channel, body: bytes) -> bytes:
    stub = ch.unary_unary(FULL_METHOD)
    out: bytes = stub(body, timeout=5)
    return out


def refusal(ch: grpc.Channel, body: bytes) -> tuple[grpc.StatusCode, str]:
    with pytest.raises(grpc.RpcError) as info:
        invoke(ch, body)
    err: Any = info.value
    md = dict(err.trailing_metadata() or ())
    return err.code(), str(md.get(REASON_KEY, ""))


def test_wire_conforms_to_the_canonical_schemas() -> None:
    v = _Validator()
    assert v.validate("gateway-call", json.loads(call())) == []
    assert v.validate("gateway-reply", json.loads(gateway().invoke(call()))) == []


def test_a_permitted_call_returns_the_routing_and_the_advice(connect: Any) -> None:
    runtime = StaticRuntime()
    reply = json.loads(invoke(connect(gateway(runtime)), call()))
    assert reply == {
        "model_profile": "model:cls-2027.03",
        "provider_profile": "provider:approved-a",
        "prompt_profile": "prompt:cls-v4",
        "ai_train_version": TRAIN,
        "proposed_code": "ontology:telecom/voice/mobile",
        "confidence": "0.97",
    }
    assert runtime.seen[0].input == b'{"sku":"PLAN-UNL-5G"}'


def test_a5_is_refused_before_the_runtime_sees_the_payload(connect: Any) -> None:
    runtime = StaticRuntime()
    code, why = refusal(connect(gateway(runtime)), call(governance={"authority_outcome": "A5"}))
    assert code == grpc.StatusCode.PERMISSION_DENIED
    assert why == "AI_AUTHORITY_REFUSED"
    assert runtime.seen == []


def test_unknown_use_case_and_kill_switch_are_refused(connect: Any) -> None:
    gw = gateway()
    ch = connect(gw)
    assert refusal(ch, call(governance={"use_case": "not-registered"}))[1] == "AI_UNKNOWN_USE_CASE"
    gw._registry.kill("classification-review")
    assert refusal(ch, call())[1] == "AI_KILL_SWITCH_ENGAGED"


def test_a_call_from_another_region_is_refused(connect: Any) -> None:
    code, why = refusal(connect(gateway()), call(governance={"region": "use1-prod-01"}))
    assert (code, why) == (grpc.StatusCode.PERMISSION_DENIED, "AI_RESIDENCY_REFUSED")


def test_malformed_calls_are_invalid_argument(connect: Any) -> None:
    ch = connect(gateway())
    for body in (
        b"not json",
        call(subject_ref=12),
        call(extra="field"),
        call(governance={"data_classes": []}),
    ):
        assert refusal(ch, body) == (grpc.StatusCode.INVALID_ARGUMENT, "AI_MALFORMED_CONTEXT")


def test_an_unconfigured_runtime_says_so(connect: Any) -> None:
    code, why = refusal(connect(gateway(UnconfiguredRuntime())), call())
    assert (code, why) == (grpc.StatusCode.UNIMPLEMENTED, "AI_GATEWAY_NOT_CONFIGURED")


def test_a_failing_runtime_is_unavailable(connect: Any) -> None:
    code, why = refusal(connect(gateway(StaticRuntime(fail=True))), call())
    assert (code, why) == (grpc.StatusCode.UNAVAILABLE, "AI_GATEWAY_UNAVAILABLE")


def test_serve_refuses_to_listen_without_mtls() -> None:
    with pytest.raises(ValueError, match="mTLS"):
        serve(gateway(), "127.0.0.1:0", None)


def test_numbers_and_unknown_fields_are_refused_by_the_decoder() -> None:
    with pytest.raises(WireError, match="number"):
        decode_call(call(subject_ref=12))
    with pytest.raises(WireError, match="unknown"):
        decode_call(call(extra="field"))
