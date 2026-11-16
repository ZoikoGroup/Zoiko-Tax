"""The invocation id on the wire (ADR-0006 §2.7).

Every call that leaves an evidence record also names it: ``invoke_with_id`` makes one
``ai_invocation_id`` right after the call is decoded, writes it into the record (PROPOSE
on success, BLOCK on a refusal or a failure), and the handler sends it back as the
trailing metadata ``ztax-invocation-id``. The reply body does not change, so a client that
ignores trailers sees the same bytes as before.

When no record exists there is no id: a call that cannot be decoded, and a sink that
failed to write (on a success or on a BLOCK). The error the caller sees is unchanged then.
"""

from __future__ import annotations

import base64
import json
import re
import threading
import time
from collections.abc import Callable
from typing import Any

import grpc
import pytest

from ztax_gateway import server as server_module
from ztax_gateway.evidence_sink import InMemoryEvidenceSink
from ztax_gateway.governance import UseCase, UseCaseRegistry
from ztax_gateway.invocation_evidence import InvocationEvidenceRecord, InvocationOutcome
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.runtime import (
    Result,
    Route,
    RoutingTable,
    RuntimeNotConfiguredError,
    RuntimeUnavailableError,
)
from ztax_gateway.server import (
    FULL_METHOD,
    INVOCATION_ID_KEY,
    REASON_KEY,
    REASON_UNAVAILABLE,
    RUNTIME_MAX_IN_FLIGHT,
    Gateway,
    GatewayError,
    RecordedId,
    _abort,
    handler,
    serve,
)
from ztax_gateway.wire import Call

CALLER_TEXT = "CALLER-TEXT-71c0e5"
_REGION = "eu-west-1"
_UC = "classification-review"
_TENANT = "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f"
_SUBJECT = f"sku:{CALLER_TEXT}"
_ROUTE = Route("model:fake@1", "provider:fake", "prompt:classification-review-v1")
_UUID4_HEX = re.compile(r"^[0-9a-f]{12}4[0-9a-f]{3}[89ab][0-9a-f]{15}$")

# The reply body for the runtime result below, exactly as it was before the id existed.
_GOLDEN_REPLY = (
    b'{"ai_train_version":"0.7.0","confidence":"0.9","model_profile":"model:fake@1",'
    b'"prompt_profile":"prompt:classification-review-v1","provider_profile":"provider:fake",'
    b'"text":"ok"}'
)


class _Runtime:
    def __init__(self, raises: BaseException | None = None) -> None:
        self.raises = raises

    def run(self, route: Route, call: Call) -> Result:
        if self.raises is not None:
            raise self.raises
        return Result(text="ok", confidence="0.9")


class _GatedRuntime:
    """Blocks every call until released, then answers."""

    def __init__(self) -> None:
        self.release = threading.Event()
        self.finished = threading.Event()
        self.started = 0
        self._lock = threading.Lock()

    def run(self, route: Route, call: Call) -> Result:
        with self._lock:
            self.started += 1
        self.release.wait(10)
        try:
            return Result(text="LATE", confidence="0.9")
        finally:
            self.finished.set()


class _RaisingSink:
    def write(self, record: InvocationEvidenceRecord) -> None:
        raise OSError("store down")


def _rig(
    runtime: Any,
    *,
    timeout: float | None = None,
    sink: Any = None,
    routed: bool = True,
    kill: bool = False,
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
    memory = InMemoryEvidenceSink()
    gateway = Gateway(
        registry,
        RoutingTable({_UC: _ROUTE} if routed else {}),
        runtime,
        _REGION,
        "0.7.0",
        evidence_sink=sink if sink is not None else memory,
        runtime_timeout_seconds=timeout,
    )
    if kill:
        registry.kill(_UC)
    return gateway, memory


def _call(region: str = _REGION) -> bytes:
    doc = {
        "kind": "SUGGESTION",
        "governance": {
            "tenant_id": _TENANT,
            "use_case": _UC,
            "authority_outcome": "A1",
            "risk_tier": "T1",
            "region": region,
            "data_classes": ["P1"],
        },
        "subject_ref": _SUBJECT,
        "input_b64": base64.b64encode(b'{"note": "x"}').decode("ascii"),
    }
    return json.dumps(doc).encode("utf-8")


class _Ctx:
    """The bits of a grpc ServicerContext the handler touches."""

    def __init__(self) -> None:
        self.code: grpc.StatusCode | None = None
        self.detail: str | None = None
        self.metadata: dict[str, str] = {}
        self.trailer_calls = 0

    def set_trailing_metadata(self, md: Any) -> None:
        self.trailer_calls += 1
        self.metadata = dict(md)

    def abort(self, code: grpc.StatusCode, detail: str) -> None:
        self.code, self.detail = code, detail
        raise RuntimeError("aborted")


def _through_handler(gateway: Gateway, data: bytes) -> tuple[_Ctx, bytes | None]:
    """Run one call through the real handler: (context, reply bytes or None if aborted)."""
    method = handler(gateway)._method_handlers[FULL_METHOD].unary_unary
    ctx = _Ctx()
    try:
        reply = method(data, ctx)
    except RuntimeError as exc:
        assert str(exc) == "aborted"
        return ctx, None
    return ctx, reply


def _only(sink: InMemoryEvidenceSink) -> InvocationEvidenceRecord:
    assert len(sink.records) == 1
    return sink.records[0]


# ---------------------------------------------------------------------------
# One id per call: the record, the returned value and the trailer agree
# ---------------------------------------------------------------------------


def test_the_record_the_returned_value_and_the_trailer_carry_the_same_id() -> None:
    gateway, sink = _rig(_Runtime())
    _, returned = gateway.invoke_with_id(_call())
    assert returned is not None
    assert _only(sink).ai_invocation_id == returned

    gateway2, sink2 = _rig(_Runtime())
    ctx, reply = _through_handler(gateway2, _call())
    assert reply is not None
    assert ctx.metadata[INVOCATION_ID_KEY] == _only(sink2).ai_invocation_id
    assert _only(sink2).invocation_outcome is not InvocationOutcome.BLOCK


def test_success_sets_the_trailer_once_and_returns_the_reply() -> None:
    gateway, _ = _rig(_Runtime())
    ctx, reply = _through_handler(gateway, _call())
    assert reply == _GOLDEN_REPLY
    assert INVOCATION_ID_KEY in ctx.metadata
    assert ctx.trailer_calls == 1
    assert ctx.code is None


def test_two_calls_give_two_different_ids() -> None:
    gateway, sink = _rig(_Runtime())
    first = gateway.invoke_with_id(_call())[1]
    second = gateway.invoke_with_id(_call())[1]
    assert first is not None
    assert second is not None
    assert first != second
    assert {r.ai_invocation_id for r in sink.records} == {first, second}


def test_the_trailer_value_is_a_random_uuid4_with_nothing_from_the_caller() -> None:
    gateway, _ = _rig(_Runtime())
    ctx, _ = _through_handler(gateway, _call())
    value = ctx.metadata[INVOCATION_ID_KEY]
    assert _UUID4_HEX.match(value), value
    for caller_text in (CALLER_TEXT, _SUBJECT, _TENANT, _UC, _REGION):
        assert caller_text not in value
    assert all(CALLER_TEXT not in v for v in ctx.metadata.values())


def test_invoke_still_returns_the_reply_bytes_only() -> None:
    gateway, sink = _rig(_Runtime())
    reply = gateway.invoke(_call())
    assert isinstance(reply, bytes)
    assert reply == _GOLDEN_REPLY
    assert len(sink.records) == 1


def test_gateway_error_has_no_id_unless_one_is_given() -> None:
    err = GatewayError(grpc.StatusCode.UNAVAILABLE, "R", "d")
    assert err.invocation_id is None
    assert GatewayError(grpc.StatusCode.UNAVAILABLE, "R", "d", "abc").invocation_id == "abc"


def test_abort_adds_the_id_next_to_the_reason_only_when_the_error_has_one() -> None:
    ctx = _Ctx()
    with pytest.raises(RuntimeError, match="aborted"):
        _abort(ctx, GatewayError(grpc.StatusCode.UNAVAILABLE, "R", "d", "idvalue"))
    assert ctx.metadata == {REASON_KEY: "R", INVOCATION_ID_KEY: "idvalue"}

    ctx = _Ctx()
    with pytest.raises(RuntimeError, match="aborted"):
        _abort(ctx, GatewayError(grpc.StatusCode.UNAVAILABLE, "R", "d"))
    assert ctx.metadata == {REASON_KEY: "R"}


# ---------------------------------------------------------------------------
# Every path that writes a BLOCK record names it
# ---------------------------------------------------------------------------


class _Scenario:
    """One way a call ends with a BLOCK record."""

    def __init__(
        self,
        name: str,
        code: grpc.StatusCode,
        *,
        runtime: Callable[[], Any] = _Runtime,
        region: str = _REGION,
        **rig: Any,
    ) -> None:
        self.name = name
        self.code = code
        self.runtime = runtime
        self.region = region
        self.rig = rig

    def build(self, *, sink: Any = None) -> tuple[Gateway, InMemoryEvidenceSink, bytes]:
        gateway, memory = _rig(self.runtime(), sink=sink, **self.rig)
        return gateway, memory, _call(region=self.region)


_SCENARIOS = [
    _Scenario("governance-refusal", grpc.StatusCode.PERMISSION_DENIED, kill=True),
    _Scenario("residency-refusal", grpc.StatusCode.PERMISSION_DENIED, region="us-east-1"),
    _Scenario(
        "not-configured",
        grpc.StatusCode.UNIMPLEMENTED,
        runtime=lambda: _Runtime(raises=RuntimeNotConfiguredError("no runtime")),
    ),
    _Scenario("no-route", grpc.StatusCode.UNIMPLEMENTED, routed=False),
    _Scenario(
        "runtime-unavailable",
        grpc.StatusCode.UNAVAILABLE,
        runtime=lambda: _Runtime(raises=RuntimeUnavailableError("down")),
    ),
    _Scenario(
        "unexpected-runtime-error",
        grpc.StatusCode.UNAVAILABLE,
        runtime=lambda: _Runtime(raises=RuntimeError("boom")),
    ),
]
_IDS = [s.name for s in _SCENARIOS]


def _fixed_ids(monkeypatch: pytest.MonkeyPatch) -> list[str]:
    """Make the Gateway's id source hand out known values, and list what it handed out."""
    handed: list[str] = []

    def make() -> str:
        value = f"{len(handed):012x}4000800000000000000"[:32]
        handed.append(value)
        return value

    monkeypatch.setattr(server_module, "_new_invocation_id", make)
    return handed


def test_the_id_is_made_once_per_call_and_is_the_id_of_the_success_record(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    handed = _fixed_ids(monkeypatch)
    gateway, sink = _rig(_Runtime())
    ctx, _ = _through_handler(gateway, _call())
    assert len(handed) == 1
    assert _only(sink).ai_invocation_id == handed[0]
    assert ctx.metadata[INVOCATION_ID_KEY] == handed[0]


@pytest.mark.parametrize("scenario", _SCENARIOS, ids=_IDS)
def test_the_id_is_made_once_per_call_and_is_the_id_of_the_block_record(
    scenario: _Scenario, monkeypatch: pytest.MonkeyPatch
) -> None:
    handed = _fixed_ids(monkeypatch)
    gateway, sink, data = scenario.build()
    ctx, _ = _through_handler(gateway, data)
    assert len(handed) == 1
    assert _only(sink).ai_invocation_id == handed[0]
    assert ctx.metadata[INVOCATION_ID_KEY] == handed[0]


def test_a_malformed_call_makes_no_id(monkeypatch: pytest.MonkeyPatch) -> None:
    handed = _fixed_ids(monkeypatch)
    gateway, _ = _rig(_Runtime())
    _through_handler(gateway, b"not json")
    assert handed == []


@pytest.mark.parametrize("scenario", _SCENARIOS, ids=_IDS)
def test_the_trailer_carries_the_id_of_the_block_record(scenario: _Scenario) -> None:
    gateway, sink, data = scenario.build()
    ctx, reply = _through_handler(gateway, data)
    assert reply is None
    assert ctx.code is scenario.code
    record = _only(sink)
    assert record.invocation_outcome is InvocationOutcome.BLOCK
    assert ctx.metadata[INVOCATION_ID_KEY] == record.ai_invocation_id
    assert _UUID4_HEX.match(ctx.metadata[INVOCATION_ID_KEY])
    assert REASON_KEY in ctx.metadata  # the reason is still sent beside it


@pytest.mark.parametrize("scenario", _SCENARIOS, ids=_IDS)
def test_the_error_and_the_recorded_slot_carry_the_id_of_the_block_record(
    scenario: _Scenario,
) -> None:
    gateway, sink, data = scenario.build()
    recorded = RecordedId()
    with pytest.raises(Exception) as info:
        gateway.invoke_with_id(data, recorded=recorded)
    record = _only(sink)
    assert recorded.value == record.ai_invocation_id
    if isinstance(info.value, GatewayError):
        assert info.value.invocation_id == record.ai_invocation_id


# ---------------------------------------------------------------------------
# No record, no id
# ---------------------------------------------------------------------------


def test_a_malformed_call_has_no_id_and_no_record() -> None:
    gateway, sink = _rig(_Runtime())
    ctx, reply = _through_handler(gateway, b"not json")
    assert reply is None
    assert ctx.code is grpc.StatusCode.INVALID_ARGUMENT
    assert INVOCATION_ID_KEY not in ctx.metadata
    assert sink.records == []
    recorded = RecordedId()
    with pytest.raises(GatewayError) as info:
        gateway.invoke_with_id(b"not json", recorded=recorded)
    assert info.value.invocation_id is None
    assert recorded.value is None


def test_a_sink_failure_on_success_gives_unavailable_and_no_id() -> None:
    gateway, _ = _rig(_Runtime(), sink=_RaisingSink())
    ctx, reply = _through_handler(gateway, _call())
    assert reply is None
    assert ctx.code is grpc.StatusCode.UNAVAILABLE
    assert ctx.detail == "evidence write failed"
    assert ctx.metadata == {REASON_KEY: REASON_UNAVAILABLE}
    recorded = RecordedId()
    with pytest.raises(GatewayError) as info:
        gateway.invoke_with_id(_call(), recorded=recorded)
    assert info.value.invocation_id is None
    assert recorded.value is None


@pytest.mark.parametrize("scenario", _SCENARIOS, ids=_IDS)
def test_a_sink_failure_on_a_block_record_gives_no_id_and_keeps_the_original_error(
    scenario: _Scenario,
) -> None:
    healthy_gateway, _, data = scenario.build()
    healthy_ctx, _ = _through_handler(healthy_gateway, data)

    failing_gateway, _, _ = scenario.build(sink=_RaisingSink())
    ctx, reply = _through_handler(failing_gateway, data)
    assert reply is None
    assert INVOCATION_ID_KEY not in ctx.metadata
    assert ctx.code is scenario.code
    assert ctx.detail == healthy_ctx.detail
    assert ctx.metadata[REASON_KEY] == healthy_ctx.metadata[REASON_KEY]
    recorded = RecordedId()
    with pytest.raises(Exception) as info:
        failing_gateway.invoke_with_id(data, recorded=recorded)
    assert recorded.value is None
    if isinstance(info.value, GatewayError):
        assert info.value.invocation_id is None


# ---------------------------------------------------------------------------
# Timeout and slots
# ---------------------------------------------------------------------------


def test_a_late_result_after_the_timeout_seals_no_success_and_the_id_is_the_block_one() -> None:
    runtime = _GatedRuntime()
    gateway, sink = _rig(runtime, timeout=0.05)
    ctx, reply = _through_handler(gateway, _call())
    assert reply is None
    assert ctx.code is grpc.StatusCode.UNAVAILABLE
    record = _only(sink)
    assert record.invocation_outcome is InvocationOutcome.BLOCK
    assert ctx.metadata[INVOCATION_ID_KEY] == record.ai_invocation_id

    runtime.release.set()  # the abandoned worker now returns its answer
    assert runtime.finished.wait(5)
    time.sleep(0.2)
    assert len(sink.records) == 1  # nothing was sealed for the late answer
    assert sink.records[0].invocation_outcome is InvocationOutcome.BLOCK


def test_the_ninth_call_against_eight_slots_fails_at_once_with_its_own_block_id() -> None:
    runtime = _GatedRuntime()
    gateway, sink = _rig(runtime, timeout=30)
    results: list[tuple[_Ctx, bytes | None]] = []
    threads = [
        threading.Thread(target=lambda: results.append(_through_handler(gateway, _call())))
        for _ in range(RUNTIME_MAX_IN_FLIGHT)
    ]
    for t in threads:
        t.start()
    deadline = time.monotonic() + 5
    while runtime.started < RUNTIME_MAX_IN_FLIGHT and time.monotonic() < deadline:
        time.sleep(0.01)
    assert runtime.started == RUNTIME_MAX_IN_FLIGHT
    try:
        started = time.monotonic()
        ctx, reply = _through_handler(gateway, _call())
        elapsed = time.monotonic() - started
        assert reply is None
        assert elapsed < 2  # no queueing behind the held slots
        assert ctx.code is grpc.StatusCode.UNAVAILABLE
        record = _only(sink)  # the eight held calls have written nothing yet
        assert record.invocation_outcome is InvocationOutcome.BLOCK
        assert ctx.metadata[INVOCATION_ID_KEY] == record.ai_invocation_id
    finally:
        runtime.release.set()
        for t in threads:
            t.join(10)
    assert len(results) == RUNTIME_MAX_IN_FLIGHT
    ids = {c.metadata[INVOCATION_ID_KEY] for c, _ in results}
    assert len(ids) == RUNTIME_MAX_IN_FLIGHT


# ---------------------------------------------------------------------------
# A client that ignores trailers
# ---------------------------------------------------------------------------


def test_a_client_that_ignores_trailers_decodes_the_same_reply_bytes() -> None:
    gateway, sink = _rig(_Runtime())
    server, port = serve(gateway, "127.0.0.1:0", None, insecure_local=True)
    try:
        with grpc.insecure_channel(f"127.0.0.1:{port}") as channel:
            call = channel.unary_unary(FULL_METHOD)
            plain = call(_call())  # reads the body only, as an old client does
            body, rpc = call.with_call(_call())
    finally:
        server.stop(None)
    assert plain == _GOLDEN_REPLY
    assert body == _GOLDEN_REPLY
    assert set(json.loads(body)) <= {
        "model_profile",
        "provider_profile",
        "prompt_profile",
        "ai_train_version",
        "text",
        "payload",
        "fields",
        "proposed_code",
        "confidence",
    }
    trailer = dict(rpc.trailing_metadata() or ())
    assert trailer[INVOCATION_ID_KEY] == sink.records[-1].ai_invocation_id
    assert trailer[INVOCATION_ID_KEY] not in body.decode("utf-8")
