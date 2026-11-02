"""Denied, failed and isolated calls at the Gateway (AI-09 and Doc 13 AT13-31).

A denied call never reaches the model. A runtime that fails returns an
unavailable result, never a partial answer and never an exception to the caller.
The Gateway is stateless: it does not carry one tenant's data into another call.
"""

from __future__ import annotations

import base64
import json
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import grpc
import pytest

from ztax_gateway.evidence_sink import InMemoryEvidenceSink
from ztax_gateway.governance import Refusal
from ztax_gateway.invocation_evidence import InvocationOutcome
from ztax_gateway.runtime import Result, Route, RuntimeUnavailableError
from ztax_gateway.server import (
    FULL_METHOD,
    REASON_KEY,
    REASON_UNAVAILABLE,
    Gateway,
    GatewayError,
    load_config,
    serve,
)
from ztax_gateway.wire import Call

_REGION = "eu-west-1"
_UC = "classification-review"
_TENANT_A = "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f"
_TENANT_B = "0190f3a2-1b2c-7d3e-8f40-ffffffffffff"


class _Runtime:
    """Counts calls; optionally raises."""

    def __init__(self, raises: BaseException | None = None) -> None:
        self.calls = 0
        self.raises = raises

    def run(self, route: Route, call: Call) -> Result:
        self.calls += 1
        if self.raises is not None:
            raise self.raises
        return Result(text="ok", confidence="0.5")


def _registry_file(tmp_path: Path) -> Path:
    path = tmp_path / "registry.json"
    path.write_text(
        json.dumps(
            {
                "use_cases": [
                    {
                        "use_case_id": _UC,
                        "owner": "lane-l",
                        "description": "test use case",
                        "max_risk_tier": "T2",
                        "max_authority": "A1",
                        "permitted_regions": [_REGION],
                        "permitted_data_classes": ["P0", "P1"],
                        "suspended": False,
                        "route": {
                            "model_profile": "model:fake-v1",
                            "provider_profile": "provider:fake",
                            "prompt_profile": "prompt:classification-review-v1",
                        },
                    }
                ],
                "killed": [],
                "global_kill": False,
            }
        ),
        encoding="utf-8",
    )
    return path


def _gateway(tmp_path: Path, runtime: _Runtime) -> tuple[Gateway, InMemoryEvidenceSink]:
    path = _registry_file(tmp_path)
    registry, routes = load_config(path)
    sink = InMemoryEvidenceSink()
    gw = Gateway(
        registry, routes, runtime, _REGION, "0.7.0", evidence_sink=sink, kill_state_path=path
    )
    return gw, sink


def _call(
    *,
    use_case: str = _UC,
    region: str = _REGION,
    authority: str = "A1",
    risk_tier: str = "T1",
    data_classes: tuple[str, ...] = ("P1",),
    tenant: str = _TENANT_A,
    marker: str = "PLAN-5G",
) -> bytes:
    doc = {
        "kind": "SUGGESTION",
        "governance": {
            "tenant_id": tenant,
            "use_case": use_case,
            "authority_outcome": authority,
            "risk_tier": risk_tier,
            "region": region,
            "data_classes": list(data_classes),
        },
        "subject_ref": f"sku:{marker}",
        "input_b64": base64.b64encode(json.dumps({"sku": marker}).encode()).decode("ascii"),
    }
    return json.dumps(doc).encode("utf-8")


# 5. Refused BEFORE the runtime: the runtime call counter stays at zero.
@pytest.mark.parametrize(
    ("kwargs", "reason"),
    [
        ({"use_case": "not-registered"}, Refusal.UNKNOWN_USE_CASE),
        ({"region": "us-east-1"}, Refusal.RESIDENCY_REFUSED),
        ({"data_classes": ("P3",)}, Refusal.DATA_CLASS_REFUSED),
        ({"data_classes": ("P7",)}, Refusal.DATA_CLASS_REFUSED),
        ({"authority": "A5"}, Refusal.AUTHORITY_REFUSED),
        ({"authority": "A4"}, Refusal.AUTHORITY_REFUSED),
        ({"risk_tier": "T4"}, Refusal.RISK_TIER_EXCEEDED),
        ({"risk_tier": "T3"}, Refusal.RISK_TIER_EXCEEDED),
    ],
)
def test_refused_calls_never_reach_the_runtime(
    tmp_path: Path, kwargs: dict[str, Any], reason: Refusal
) -> None:
    runtime = _Runtime()
    gw, sink = _gateway(tmp_path, runtime)
    with pytest.raises(GatewayError) as info:
        gw.invoke(_call(**kwargs))
    assert info.value.reason == reason.value
    assert runtime.calls == 0
    assert sink.last.invocation_outcome is InvocationOutcome.BLOCK


def test_a_permitted_call_does_reach_the_runtime_once(tmp_path: Path) -> None:
    runtime = _Runtime()
    gw, _ = _gateway(tmp_path, runtime)
    gw.invoke(_call())
    assert runtime.calls == 1


# 6. A runtime that fails: unavailable, no partial answer, no escaping exception.
def test_runtime_unavailable_error_is_unavailable_and_returns_no_answer(tmp_path: Path) -> None:
    runtime = _Runtime(RuntimeUnavailableError("provider down"))
    gw, sink = _gateway(tmp_path, runtime)
    with pytest.raises(GatewayError) as info:
        gw.invoke(_call())
    assert info.value.code is grpc.StatusCode.UNAVAILABLE
    assert info.value.reason == REASON_UNAVAILABLE
    assert runtime.calls == 1
    assert sink.last.invocation_outcome is InvocationOutcome.BLOCK
    assert sink.last.output_hash is None


@pytest.fixture()
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
        s.stop(0)


@pytest.mark.parametrize(
    "failure",
    [RuntimeError("boom"), ValueError("bad"), KeyError("k"), RuntimeUnavailableError("down")],
)
def test_a_failing_runtime_never_escapes_to_the_caller_over_the_wire(
    tmp_path: Path, connect: Any, failure: BaseException
) -> None:
    runtime = _Runtime(failure)
    gw, _ = _gateway(tmp_path, runtime)
    ch = connect(gw)
    with pytest.raises(grpc.RpcError) as info:
        ch.unary_unary(FULL_METHOD)(_call(), timeout=5)
    err: Any = info.value
    assert err.code() is grpc.StatusCode.UNAVAILABLE
    assert dict(err.trailing_metadata() or ())[REASON_KEY] == REASON_UNAVAILABLE
    # The error carries no runtime detail and no partial answer.
    assert "boom" not in err.details()
    assert runtime.calls == 1


def test_the_gateway_keeps_serving_after_a_runtime_failure(tmp_path: Path, connect: Any) -> None:
    runtime = _Runtime(RuntimeError("boom"))
    gw, _ = _gateway(tmp_path, runtime)
    ch = connect(gw)
    with pytest.raises(grpc.RpcError):
        ch.unary_unary(FULL_METHOD)(_call(), timeout=5)
    runtime.raises = None
    reply = ch.unary_unary(FULL_METHOD)(_call(), timeout=5)
    assert json.loads(reply)["text"] == "ok"


# 7. Tenant: the Gateway is stateless and the record carries no tenant data.
def test_tenant_b_response_and_record_hold_nothing_of_tenant_a(tmp_path: Path) -> None:
    runtime = _Runtime()
    gw, sink = _gateway(tmp_path, runtime)
    gw.invoke(_call(tenant=_TENANT_A, marker="TENANT-A-SECRET"))
    reply_b = gw.invoke(_call(tenant=_TENANT_B, marker="TENANT-B-ITEM"))
    record_b = sink.last
    blob = reply_b.decode("utf-8") + json.dumps(record_b.as_dict(), default=str)
    assert "TENANT-A-SECRET" not in blob
    assert _TENANT_A not in blob
    # Each record hashes only its own input.
    assert sink.records[0].input_hash != record_b.input_hash
    assert runtime.calls == 2


def test_the_evidence_record_does_not_carry_a_tenant_id_at_all(tmp_path: Path) -> None:
    # The record has no tenant field today; this documents that, so a future
    # field is a conscious change rather than a silent one.
    gw, sink = _gateway(tmp_path, _Runtime())
    gw.invoke(_call(tenant=_TENANT_A))
    assert _TENANT_A not in json.dumps(sink.last.as_dict(), default=str)
