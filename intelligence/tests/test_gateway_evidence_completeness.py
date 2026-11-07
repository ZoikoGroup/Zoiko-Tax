"""Evidence completeness for the Gateway (ADR-0006 §2.7, AIGOV-001 §14).

Two guarantees, checked for both entry points (``invoke`` and ``invoke_with_id``):

1. Every call that passed decoding leaves exactly one evidence record, including a call
   whose runtime raised an error nobody named. That record is a BLOCK record with the
   same shape as the runtime-unavailable one (verdict AI_GATEWAY_UNAVAILABLE) and a fixed
   detail; it never carries the exception's text or class name. What the caller sees is
   unchanged.
2. Every record of a call that reached the runtime carries the whole milliseconds the
   runtime call took (``usage.latency_ms``). Tokens and cost stay zero: there is no
   provider telemetry. A call refused before the runtime keeps latency 0.

The clock is replaced by a stepping function, so no test depends on real sleeping.
"""

from __future__ import annotations

import base64
import json
import logging
import threading
import time
from collections.abc import Callable, Iterator
from dataclasses import dataclass, field
from typing import Any

import grpc
import pytest

from ztax_gateway import server as server_module
from ztax_gateway.evidence_sink import InMemoryEvidenceSink
from ztax_gateway.governance import Refusal, UseCase, UseCaseRegistry
from ztax_gateway.invocation_evidence import InvocationEvidenceRecord, InvocationOutcome
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.runtime import (
    Result,
    Route,
    RoutingTable,
    RuntimeNotConfiguredError,
    RuntimeUnavailableError,
    UnroutedUseCaseError,
)
from ztax_gateway.server import (
    DETAIL_RUNTIME_UNAVAILABLE,
    FULL_METHOD,
    REASON_KEY,
    REASON_NOT_CONFIGURED,
    REASON_UNAVAILABLE,
    RECORD_DETAIL_UNEXPECTED_ERROR,
    RUNTIME_MAX_IN_FLIGHT,
    Gateway,
    GatewayError,
    handler,
)
from ztax_gateway.wire import Call

SENTINEL = "SENTINEL-COMPLETE-5d8a14"
LATE_ANSWER = "LATE-ANSWER-c2e709"
STEP_MS = 250
_REGION = "eu-west-1"
_UC = "classification-review"
_TENANT = "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f"
_ROUTE = Route("model:fake@1", "provider:fake", "prompt:classification-review-v1")

_ENTRY_POINTS: list[tuple[str, Callable[[Gateway, bytes], Any]]] = [
    ("invoke", lambda gw, data: gw.invoke(data)),
    ("invoke_with_id", lambda gw, data: gw.invoke_with_id(data)),
]


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


class _StepClock:
    """A monotonic clock that moves ``step_ns`` between a thread's 1st and 2nd reading,
    3rd and 4th, and so on. ``_run`` reads the clock exactly twice per runtime call."""

    def __init__(self, step_ns: int = STEP_MS * 1_000_000) -> None:
        self.step_ns = step_ns
        self._local = threading.local()

    def __call__(self) -> int:
        n: int = getattr(self._local, "n", 0)
        self._local.n = n + 1
        return n * self.step_ns


@pytest.fixture(autouse=True)
def _clock(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(server_module, "_monotonic_ns", _StepClock())


class _Runtime:
    def __init__(self, raises: BaseException | None = None, result: Result | None = None) -> None:
        self.raises = raises
        self.result = result if result is not None else Result(text="ok", confidence="0.9")
        self.calls = 0

    def run(self, route: Route, call: Call) -> Result:
        self.calls += 1
        if self.raises is not None:
            raise self.raises
        return self.result


class _GatedRuntime:
    def __init__(self) -> None:
        self.release = threading.Event()
        self.finished = threading.Event()
        self.calls = 0
        self._lock = threading.Lock()

    def run(self, route: Route, call: Call) -> Result:
        with self._lock:
            self.calls += 1
        self.release.wait(10)
        try:
            return Result(text=LATE_ANSWER, confidence="0.9")
        finally:
            self.finished.set()


class _RaisingSink:
    def write(self, record: InvocationEvidenceRecord) -> None:
        raise OSError(f"store down {SENTINEL}")


class _Fatal(BaseException):
    """A BaseException that is not KeyboardInterrupt or SystemExit."""


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


def _doc(
    *,
    use_case: str = _UC,
    authority: str = "A1",
    risk: str = "T1",
    region: str = _REGION,
    classes: tuple[str, ...] = ("P1",),
) -> dict[str, Any]:
    return {
        "kind": "SUGGESTION",
        "governance": {
            "tenant_id": _TENANT,
            "use_case": use_case,
            "authority_outcome": authority,
            "risk_tier": risk,
            "region": region,
            "data_classes": list(classes),
        },
        "subject_ref": "sku:COMPLETE-001",
        "input_b64": base64.b64encode(b'{"note": "x"}').decode("ascii"),
    }


def _call(**kw: Any) -> bytes:
    return json.dumps(_doc(**kw)).encode("utf-8")


def _bad_json() -> bytes:
    return b'{"kind": ' + SENTINEL.encode()


def _bad_kind() -> bytes:
    doc = _doc()
    doc["kind"] = SENTINEL
    return json.dumps(doc).encode("utf-8")


def _log_blob(caplog: pytest.LogCaptureFixture) -> str:
    fmt = logging.Formatter()
    parts: list[str] = []
    for rec in caplog.records:
        parts.append(fmt.format(rec))
        parts.append(repr(vars(rec)))
    return "\n".join(parts)


def _stored_blob(sink: InMemoryEvidenceSink) -> str:
    return json.dumps([r.as_dict() for r in sink.records], default=str, sort_keys=True)


@pytest.fixture()
def debug_logs(caplog: pytest.LogCaptureFixture) -> Iterator[pytest.LogCaptureFixture]:
    with caplog.at_level(logging.DEBUG):
        yield caplog


def _attempt(
    gateway: Gateway, data: bytes, invoke: Callable[[Gateway, bytes], Any]
) -> tuple[str, Any]:
    """('reply', None), ('gateway', GatewayError) or ('raw', the exception raised)."""
    try:
        invoke(gateway, data)
    except GatewayError as err:
        return "gateway", err
    except Exception as exc:  # the unexpected runtime errors surface as themselves
        return "raw", exc
    return "reply", None


class _Ctx:
    """The bits of a grpc ServicerContext the handler touches."""

    def __init__(self) -> None:
        self.code: grpc.StatusCode | None = None
        self.detail: str | None = None
        self.metadata: dict[str, str] = {}

    def set_trailing_metadata(self, md: Any) -> None:
        self.metadata = dict(md)

    def abort(self, code: grpc.StatusCode, detail: str) -> None:
        self.code, self.detail = code, detail
        raise RuntimeError("aborted")


def _through_handler(gateway: Gateway, data: bytes) -> _Ctx:
    method = handler(gateway)._method_handlers[FULL_METHOD].unary_unary
    ctx = _Ctx()
    with pytest.raises(RuntimeError, match="aborted"):
        method(data, ctx)
    return ctx


# ---------------------------------------------------------------------------
# 1 + 2. The matrix: one record per decoded call, right outcome, latency
# ---------------------------------------------------------------------------


@dataclass
class _Case:
    name: str
    data: bytes = field(default_factory=_call)
    runtime: Any = None
    timeout: float | None = None
    routed: bool = True
    kill: bool = False
    # "reply", "gateway" (a GatewayError) or "raw" (the runtime's own exception)
    caller: str = "reply"
    code: grpc.StatusCode | None = None
    reason: str | None = None
    raw_type: type[Exception] | None = None
    records: int = 1
    verdict: str = "PASS"
    detail: str = ""
    latency_ms: int = 0


def _cases() -> list[_Case]:
    unavailable = RuntimeUnavailableError(f"provider said {SENTINEL}")
    return [
        _Case("success", runtime=_Runtime(), latency_ms=STEP_MS),
        _Case(
            "runtime-unavailable",
            runtime=_Runtime(raises=unavailable),
            caller="gateway",
            code=grpc.StatusCode.UNAVAILABLE,
            reason=REASON_UNAVAILABLE,
            verdict=REASON_UNAVAILABLE,
            latency_ms=STEP_MS,
        ),
        _Case(
            "empty-reply",
            runtime=_Runtime(result=Result()),
            caller="gateway",
            code=grpc.StatusCode.UNAVAILABLE,
            reason=REASON_UNAVAILABLE,
            verdict=REASON_UNAVAILABLE,
            latency_ms=STEP_MS,
        ),
        _Case(
            "not-configured",
            runtime=_Runtime(raises=RuntimeNotConfiguredError(f"x {SENTINEL}")),
            caller="gateway",
            code=grpc.StatusCode.UNIMPLEMENTED,
            reason=REASON_NOT_CONFIGURED,
            verdict=REASON_NOT_CONFIGURED,
        ),
        _Case(
            "unrouted-by-runtime",
            runtime=_Runtime(raises=UnroutedUseCaseError(f"x {SENTINEL}")),
            caller="gateway",
            code=grpc.StatusCode.UNIMPLEMENTED,
            reason=REASON_NOT_CONFIGURED,
            verdict=REASON_NOT_CONFIGURED,
        ),
        _Case(
            "unrouted-by-table",
            runtime=_Runtime(),
            routed=False,
            caller="gateway",
            code=grpc.StatusCode.UNIMPLEMENTED,
            reason=REASON_NOT_CONFIGURED,
            verdict=REASON_NOT_CONFIGURED,
        ),
        _Case(
            "unexpected-value-error",
            runtime=_Runtime(raises=ValueError(f"bad {SENTINEL}")),
            caller="raw",
            raw_type=ValueError,
            verdict=REASON_UNAVAILABLE,
            detail=RECORD_DETAIL_UNEXPECTED_ERROR,
            latency_ms=STEP_MS,
        ),
        _Case(
            "unexpected-runtime-error-with-sentinel",
            runtime=_Runtime(raises=RuntimeError(f"model blew up on: {SENTINEL}")),
            caller="raw",
            raw_type=RuntimeError,
            verdict=REASON_UNAVAILABLE,
            detail=RECORD_DETAIL_UNEXPECTED_ERROR,
            latency_ms=STEP_MS,
        ),
        _Case(
            "unexpected-error-with-timeout-set",
            runtime=_Runtime(raises=KeyError(SENTINEL)),
            timeout=5.0,
            caller="raw",
            raw_type=KeyError,
            verdict=REASON_UNAVAILABLE,
            detail=RECORD_DETAIL_UNEXPECTED_ERROR,
            latency_ms=STEP_MS,
        ),
        _Case(
            "refused-unknown-use-case",
            data=_call(use_case="not-registered"),
            caller="gateway",
            code=grpc.StatusCode.PERMISSION_DENIED,
            reason=Refusal.UNKNOWN_USE_CASE.value,
            verdict=Refusal.UNKNOWN_USE_CASE.value,
        ),
        _Case(
            "refused-killed",
            kill=True,
            caller="gateway",
            code=grpc.StatusCode.PERMISSION_DENIED,
            reason=Refusal.KILL_SWITCH_ENGAGED.value,
            verdict=Refusal.KILL_SWITCH_ENGAGED.value,
        ),
        _Case(
            "refused-authority-a5",
            data=_call(authority="A5"),
            caller="gateway",
            code=grpc.StatusCode.PERMISSION_DENIED,
            reason=Refusal.AUTHORITY_REFUSED.value,
            verdict=Refusal.AUTHORITY_REFUSED.value,
        ),
        _Case(
            "refused-risk-t3",
            data=_call(risk="T3"),
            caller="gateway",
            code=grpc.StatusCode.PERMISSION_DENIED,
            reason=Refusal.RISK_TIER_EXCEEDED.value,
            verdict=Refusal.RISK_TIER_EXCEEDED.value,
        ),
        _Case(
            "refused-data-class-p3",
            data=_call(classes=("P3",)),
            caller="gateway",
            code=grpc.StatusCode.PERMISSION_DENIED,
            reason=Refusal.DATA_CLASS_REFUSED.value,
            verdict=Refusal.DATA_CLASS_REFUSED.value,
        ),
        _Case(
            "refused-other-region",
            data=_call(region="us-east-1"),
            caller="gateway",
            code=grpc.StatusCode.PERMISSION_DENIED,
            reason=Refusal.RESIDENCY_REFUSED.value,
            verdict=Refusal.RESIDENCY_REFUSED.value,
        ),
        _Case(
            "malformed-json",
            data=_bad_json(),
            caller="gateway",
            code=grpc.StatusCode.INVALID_ARGUMENT,
            reason=Refusal.MALFORMED_CONTEXT.value,
            records=0,
        ),
        _Case(
            "malformed-kind",
            data=_bad_kind(),
            caller="gateway",
            code=grpc.StatusCode.INVALID_ARGUMENT,
            reason=Refusal.MALFORMED_CONTEXT.value,
            records=0,
        ),
    ]


@pytest.mark.parametrize("case", _cases(), ids=lambda c: c.name)
@pytest.mark.parametrize(("entry", "invoke"), _ENTRY_POINTS, ids=[e[0] for e in _ENTRY_POINTS])
def test_the_evidence_matrix(
    case: _Case,
    entry: str,
    invoke: Callable[[Gateway, bytes], Any],
    debug_logs: pytest.LogCaptureFixture,
) -> None:
    runtime = case.runtime if case.runtime is not None else _Runtime()
    gateway, sink = _rig(runtime, timeout=case.timeout, routed=case.routed, kill=case.kill)
    kind, err = _attempt(gateway, case.data, invoke)

    # What the caller got.
    assert kind == case.caller
    if case.caller == "gateway":
        assert err.code == case.code
        assert err.reason == case.reason
        for text in (err.detail, str(err), repr(err)):
            assert SENTINEL not in text
    if case.caller == "raw":
        assert type(err) is case.raw_type  # the runtime's own error, as before
        assert err.__cause__ is None
        assert err.__context__ is None

    # Exactly one record per call that passed decoding; none for a malformed call.
    assert len(sink.records) == case.records
    assert SENTINEL not in _stored_blob(sink)
    assert SENTINEL not in _log_blob(debug_logs)
    if case.records == 0:
        return
    record = sink.records[0]
    if case.verdict == "PASS":
        assert record.invocation_outcome is not InvocationOutcome.BLOCK
    else:
        assert record.invocation_outcome is InvocationOutcome.BLOCK
        assert record.output_hash is None
        assert [(g.verdict, g.detail) for g in record.guardrail_results] == [
            (case.verdict, case.detail)
        ]
    # Latency is set for a call that reached the runtime and is 0 for one that did not;
    # tokens and cost are never populated.
    assert record.usage.latency_ms == case.latency_ms
    assert record.usage.input_tokens == 0
    assert record.usage.output_tokens == 0
    assert record.usage.monetary_cost_usd == 0.0
    if case.caller == "raw":
        assert case.raw_type is not None
        assert case.raw_type.__name__ not in _stored_blob(sink)


# ---------------------------------------------------------------------------
# 3. The unexpected-error case: what the caller sees is unchanged
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("timeout", [None, 5.0], ids=["no-timeout", "with-timeout"])
@pytest.mark.parametrize(
    "failure",
    [RuntimeError(f"boom {SENTINEL}"), ValueError("bad"), KeyError("k")],
    ids=["RuntimeError", "ValueError", "KeyError"],
)
def test_the_caller_sees_the_same_status_reason_and_detail_through_the_real_handler(
    failure: Exception, timeout: float | None, debug_logs: pytest.LogCaptureFixture
) -> None:
    gateway, sink = _rig(_Runtime(raises=failure), timeout=timeout)
    ctx = _through_handler(gateway, _call())
    assert ctx.code is grpc.StatusCode.UNAVAILABLE
    assert ctx.metadata[REASON_KEY] == REASON_UNAVAILABLE == "AI_GATEWAY_UNAVAILABLE"
    assert ctx.detail == "internal error"
    assert SENTINEL not in json.dumps([ctx.detail, ctx.metadata])
    # ... and now the call is evidenced.
    assert len(sink.records) == 1
    assert SENTINEL not in _stored_blob(sink)
    assert SENTINEL not in _log_blob(debug_logs)


@pytest.mark.parametrize(("entry", "invoke"), _ENTRY_POINTS, ids=[e[0] for e in _ENTRY_POINTS])
def test_the_exception_class_is_logged_once_by_name_only(
    entry: str, invoke: Callable[[Gateway, bytes], Any], debug_logs: pytest.LogCaptureFixture
) -> None:
    gateway, _ = _rig(_Runtime(raises=RuntimeError(f"boom {SENTINEL}")))
    assert _attempt(gateway, _call(), invoke)[0] == "raw"
    lines = [r for r in debug_logs.records if "unexpected error" in r.getMessage()]
    assert len(lines) == 1
    assert lines[0].__dict__["error_type"] == "RuntimeError"
    assert SENTINEL not in _log_blob(debug_logs)


# ---------------------------------------------------------------------------
# 4. A failing sink never replaces the caller's error
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("timeout", [None, 5.0], ids=["no-timeout", "with-timeout"])
@pytest.mark.parametrize(("entry", "invoke"), _ENTRY_POINTS, ids=[e[0] for e in _ENTRY_POINTS])
def test_a_sink_failure_on_the_unexpected_error_record_keeps_the_original_error(
    entry: str,
    invoke: Callable[[Gateway, bytes], Any],
    timeout: float | None,
    debug_logs: pytest.LogCaptureFixture,
) -> None:
    original = ValueError(f"bad {SENTINEL}")
    gateway, _ = _rig(_Runtime(raises=original), timeout=timeout, sink=_RaisingSink())
    kind, err = _attempt(gateway, _call(), invoke)
    assert kind == "raw"
    assert err is original
    assert SENTINEL not in _log_blob(debug_logs)  # the sink's own text stays out too
    lines = [r for r in debug_logs.records if "sink write failed" in r.getMessage()]
    assert len(lines) == 1
    assert lines[0].__dict__["error_type"] == "OSError"


def test_a_sink_failure_through_the_handler_still_gives_the_same_refusal() -> None:
    gateway, _ = _rig(_Runtime(raises=ValueError("bad")), sink=_RaisingSink())
    ctx = _through_handler(gateway, _call())
    assert ctx.code is grpc.StatusCode.UNAVAILABLE
    assert ctx.metadata[REASON_KEY] == REASON_UNAVAILABLE
    assert ctx.detail == "internal error"


# ---------------------------------------------------------------------------
# 5. Not every exception is ours to catch
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("timeout", [None, 5.0], ids=["no-timeout", "with-timeout"])
@pytest.mark.parametrize(("entry", "invoke"), _ENTRY_POINTS, ids=[e[0] for e in _ENTRY_POINTS])
def test_a_base_exception_from_the_runtime_still_propagates_and_writes_no_record(
    entry: str, invoke: Callable[[Gateway, bytes], Any], timeout: float | None
) -> None:
    failure = _Fatal("fatal")
    gateway, sink = _rig(_Runtime(raises=failure), timeout=timeout)
    with pytest.raises(_Fatal) as info:
        invoke(gateway, _call())
    assert info.value is failure
    assert sink.records == []


# ---------------------------------------------------------------------------
# 6. Timeout and busy slots are evidenced with a latency
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(("entry", "invoke"), _ENTRY_POINTS, ids=[e[0] for e in _ENTRY_POINTS])
def test_a_timeout_writes_one_block_record_with_the_measured_latency(
    entry: str, invoke: Callable[[Gateway, bytes], Any]
) -> None:
    runtime = _GatedRuntime()
    gateway, sink = _rig(runtime, timeout=0.05)
    try:
        kind, err = _attempt(gateway, _call(), invoke)
        assert kind == "gateway"
        assert err.code == grpc.StatusCode.UNAVAILABLE
        assert err.reason == REASON_UNAVAILABLE
        assert err.detail == DETAIL_RUNTIME_UNAVAILABLE
        assert len(sink.records) == 1
        record = sink.records[0]
        assert record.invocation_outcome is InvocationOutcome.BLOCK
        assert [(g.verdict, g.detail) for g in record.guardrail_results] == [
            (REASON_UNAVAILABLE, "")
        ]
        assert record.usage.latency_ms == STEP_MS
    finally:
        runtime.release.set()
    assert runtime.finished.wait(5)
    time.sleep(0.05)
    assert len(sink.records) == 1  # the late answer is never sealed
    assert LATE_ANSWER not in _stored_blob(sink)


def test_a_busy_slot_writes_one_block_record_with_a_latency() -> None:
    runtime = _GatedRuntime()
    gateway, sink = _rig(runtime, timeout=10.0)
    fillers = [
        threading.Thread(target=lambda: gateway.invoke(_call()))
        for _ in range(RUNTIME_MAX_IN_FLIGHT)
    ]
    try:
        for t in fillers:
            t.start()
        deadline = time.monotonic() + 5
        while runtime.calls < RUNTIME_MAX_IN_FLIGHT and time.monotonic() < deadline:
            time.sleep(0.005)
        assert runtime.calls == RUNTIME_MAX_IN_FLIGHT
        kind, err = _attempt(gateway, _call(), lambda gw, d: gw.invoke(d))
        assert kind == "gateway"
        assert err.reason == REASON_UNAVAILABLE
        assert runtime.calls == RUNTIME_MAX_IN_FLIGHT  # the busy call never ran
        assert len(sink.records) == 1  # only the busy call has finished
        record = sink.records[0]
        assert record.invocation_outcome is InvocationOutcome.BLOCK
        assert record.usage.latency_ms == STEP_MS
    finally:
        runtime.release.set()
    for t in fillers:
        t.join(15)
    assert len(sink.records) == RUNTIME_MAX_IN_FLIGHT + 1  # one record per call
    successes = [r for r in sink.records if r.invocation_outcome is not InvocationOutcome.BLOCK]
    assert len(successes) == RUNTIME_MAX_IN_FLIGHT
    assert all(r.usage.latency_ms == STEP_MS for r in successes)


# ---------------------------------------------------------------------------
# 7. The latency itself
# ---------------------------------------------------------------------------


def test_latency_is_whole_milliseconds_rounded_down(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(server_module, "_monotonic_ns", _StepClock(step_ns=1_999_999))
    gateway, sink = _rig(_Runtime())
    gateway.invoke(_call())
    assert sink.records[0].usage.latency_ms == 1


def test_a_clock_that_goes_backwards_never_gives_a_negative_latency(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(server_module, "_monotonic_ns", _StepClock(step_ns=-5_000_000))
    gateway, sink = _rig(_Runtime())
    gateway.invoke(_call())
    assert sink.records[0].usage.latency_ms == 0


def test_the_unexpected_error_record_has_the_same_shape_as_the_unavailable_one() -> None:
    gateway, sink = _rig(_Runtime(raises=ValueError(f"bad {SENTINEL}")))
    _attempt(gateway, _call(), lambda gw, d: gw.invoke(d))
    gateway2, sink2 = _rig(_Runtime(raises=RuntimeUnavailableError("down")))
    _attempt(gateway2, _call(), lambda gw, d: gw.invoke(d))
    assert set(sink.records[0].as_dict()) == set(sink2.records[0].as_dict())
