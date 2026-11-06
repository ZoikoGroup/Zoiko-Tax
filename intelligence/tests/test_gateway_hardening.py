"""Gateway hardening: fixed caller-facing error text, and the runtime timeout.

Part 1 -- no exception text reaches the caller. A sentinel is placed in each text
an exception could carry (a runtime's own message, a use case id, the caller's
bytes) and must be absent from ``GatewayError.detail``, ``.reason``, ``str(err)``,
the stored records and the debug logs. The error handed to the transport must also
carry no ``__cause__`` and no ``__context__``. Both entry points, ``invoke`` and
``invoke_with_id``, are run through every case.

Part 2 -- ``runtime_timeout_seconds``. A slow runtime fails closed through the
existing UNAVAILABLE path (one BLOCK record, no PERMIT record), the late result is
discarded, and the number of runtime calls in flight is bounded.
"""

from __future__ import annotations

import base64
import json
import logging
import math
import threading
import time
from collections.abc import Callable, Iterator
from pathlib import Path
from typing import Any

import grpc
import pytest

from ztax_gateway.evidence_sink import InMemoryEvidenceSink
from ztax_gateway.fake_runtime import FakeRuntime
from ztax_gateway.gateway_factory import ENV_PROMPT_PROFILES, build_gateway
from ztax_gateway.governance import Refusal, UseCase, UseCaseRegistry
from ztax_gateway.invocation_evidence import InvocationEvidenceRecord, InvocationOutcome
from ztax_gateway.production_gates import PromptProfileRegistry
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
    DETAIL_CALL_MALFORMED,
    DETAIL_NOT_CONFIGURED,
    DETAIL_RESIDENCY_REFUSED,
    DETAIL_RUNTIME_UNAVAILABLE,
    REASON_NOT_CONFIGURED,
    REASON_UNAVAILABLE,
    RUNTIME_MAX_IN_FLIGHT,
    Gateway,
    GatewayError,
)
from ztax_gateway.wire import Call, WireError, decode_call

SENTINEL = "SENTINEL-HARDEN-4c9e27"
LATE_ANSWER = "LATE-ANSWER-91ab03"
_REGION = "eu-west-1"
_UC = "classification-review"
_TENANT = "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f"
_ROUTE = Route("model:fake@1", "provider:fake", "prompt:classification-review-v1")

_CONFIG_DIR = Path(__file__).resolve().parent.parent / "config"

# Both entry points take the same bytes; invoke_with_id returns a tuple.
_ENTRY_POINTS: list[tuple[str, Callable[[Gateway, bytes], Any]]] = [
    ("invoke", lambda gw, data: gw.invoke(data)),
    ("invoke_with_id", lambda gw, data: gw.invoke_with_id(data)),
]


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


class _Runtime:
    """Returns text, or raises what it was told to; counts and records its thread."""

    def __init__(self, raises: BaseException | None = None, text: str = "ok") -> None:
        self.raises = raises
        self.text = text
        self.calls = 0
        self.thread: threading.Thread | None = None
        self._lock = threading.Lock()

    def run(self, route: Route, call: Call) -> Result:
        with self._lock:
            self.calls += 1
        self.thread = threading.current_thread()
        if self.raises is not None:
            raise self.raises
        return Result(text=self.text, confidence="0.9")


class _GatedRuntime:
    """Blocks until ``release`` is set, then returns the late answer."""

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
        raise RuntimeError(f"store down {SENTINEL}")


def _registry(use_case_id: str = _UC) -> UseCaseRegistry:
    registry = UseCaseRegistry()
    registry.register(
        UseCase(
            use_case_id=use_case_id,
            owner="team-ai",
            description="test use case",
            max_risk_tier=RiskTier.T2,
            max_authority=AuthorityOutcome.A2,
            permitted_regions=frozenset([_REGION]),
            permitted_data_classes=frozenset({"P0", "P1"}),
        )
    )
    return registry


def _gateway(
    runtime: Any,
    *,
    sink: Any = None,
    timeout: float | None = None,
    use_case_id: str = _UC,
    routed: bool = True,
) -> tuple[Gateway, InMemoryEvidenceSink]:
    memory = InMemoryEvidenceSink()
    routes = RoutingTable({use_case_id: _ROUTE} if routed else {})
    gateway = Gateway(
        _registry(use_case_id),
        routes,
        runtime,
        _REGION,
        "0.7.0",
        evidence_sink=sink if sink is not None else memory,
        runtime_timeout_seconds=timeout,
    )
    return gateway, memory


def _call(
    *,
    use_case: str = _UC,
    region: str = _REGION,
    kind: str = "SUGGESTION",
    data_classes: tuple[str, ...] = ("P1",),
) -> bytes:
    doc = {
        "kind": kind,
        "governance": {
            "tenant_id": _TENANT,
            "use_case": use_case,
            "authority_outcome": "A1",
            "risk_tier": "T1",
            "region": region,
            "data_classes": list(data_classes),
        },
        "subject_ref": "sku:HARDEN-001",
        "input_b64": base64.b64encode(b'{"note": "x"}').decode("ascii"),
    }
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


def _assert_clean(label: str, text: str) -> None:
    assert SENTINEL not in text, f"{label} leaks the sentinel"


def _assert_no_chain(err: BaseException) -> None:
    assert err.__cause__ is None
    assert err.__context__ is None


def _assert_error_is_clean(err: GatewayError) -> None:
    _assert_clean("GatewayError.detail", err.detail)
    _assert_clean("GatewayError.reason", err.reason)
    _assert_clean("GatewayError str", str(err))
    _assert_clean("GatewayError repr", repr(err))
    _assert_no_chain(err)


@pytest.fixture()
def debug_logs(caplog: pytest.LogCaptureFixture) -> Iterator[pytest.LogCaptureFixture]:
    with caplog.at_level(logging.DEBUG):
        yield caplog


# ---------------------------------------------------------------------------
# Part 1a. WireError: the caller's own bytes never come back
# ---------------------------------------------------------------------------


def _doc() -> dict[str, Any]:
    doc: dict[str, Any] = json.loads(_call())
    return doc


def _bad_kind() -> bytes:
    doc = _doc()
    doc["kind"] = SENTINEL
    return json.dumps(doc).encode()


def _bad_data_class() -> bytes:
    doc = _doc()
    doc["governance"]["data_classes"] = [SENTINEL]
    return json.dumps(doc).encode()


def _unknown_field() -> bytes:
    doc = _doc()
    doc[SENTINEL] = "x"
    return json.dumps(doc).encode()


def _bad_json() -> bytes:
    return b'{"kind": ' + SENTINEL.encode() + b"}"


def _bad_utf8() -> bytes:
    return b'{"kind": "' + SENTINEL.encode() + b'\xff"}'


_MALFORMED = [_bad_kind, _bad_data_class, _unknown_field, _bad_json, _bad_utf8]


@pytest.mark.parametrize("make", _MALFORMED, ids=lambda f: f.__name__)
@pytest.mark.parametrize(("entry", "invoke"), _ENTRY_POINTS, ids=[e[0] for e in _ENTRY_POINTS])
def test_a_malformed_call_gets_a_fixed_detail_and_no_chain(
    make: Callable[[], bytes],
    entry: str,
    invoke: Callable[[Gateway, bytes], Any],
    debug_logs: pytest.LogCaptureFixture,
) -> None:
    gateway, sink = _gateway(_Runtime())
    data = make()
    with pytest.raises(GatewayError) as info:
        invoke(gateway, data)
    err = info.value
    assert err.code == grpc.StatusCode.INVALID_ARGUMENT
    assert err.reason == Refusal.MALFORMED_CONTEXT.value
    assert err.detail == DETAIL_CALL_MALFORMED
    _assert_error_is_clean(err)
    _assert_clean("stored records", _stored_blob(sink))
    _assert_clean("log output", _log_blob(debug_logs))


@pytest.mark.parametrize(
    "make", [_bad_kind, _bad_data_class, _unknown_field], ids=lambda f: f.__name__
)
def test_the_wire_error_itself_does_carry_the_sentinel(make: Callable[[], bytes]) -> None:
    """Guards the test above against passing vacuously: the decoder's message does
    echo the caller's value, so the fixed detail is what keeps it out."""
    with pytest.raises(WireError) as info:
        decode_call(make())
    assert SENTINEL in str(info.value)


# ---------------------------------------------------------------------------
# Part 1b. Not configured / unrouted
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "exc_type", [RuntimeNotConfiguredError, UnroutedUseCaseError], ids=lambda t: t.__name__
)
@pytest.mark.parametrize(("entry", "invoke"), _ENTRY_POINTS, ids=[e[0] for e in _ENTRY_POINTS])
@pytest.mark.parametrize("timeout", [None, 5.0], ids=["no-timeout", "with-timeout"])
def test_a_runtime_that_is_not_configured_gets_a_fixed_detail_and_no_chain(
    exc_type: type[Exception],
    entry: str,
    invoke: Callable[[Gateway, bytes], Any],
    timeout: float | None,
    debug_logs: pytest.LogCaptureFixture,
) -> None:
    gateway, sink = _gateway(_Runtime(raises=exc_type(f"x {SENTINEL}")), timeout=timeout)
    with pytest.raises(GatewayError) as info:
        invoke(gateway, _call())
    err = info.value
    assert err.code == grpc.StatusCode.UNIMPLEMENTED
    assert err.reason == REASON_NOT_CONFIGURED
    assert err.detail == DETAIL_NOT_CONFIGURED
    _assert_error_is_clean(err)
    assert len(sink.records) == 1
    assert sink.records[0].invocation_outcome is InvocationOutcome.BLOCK
    assert sink.records[0].output_hash is None
    assert [g.verdict for g in sink.records[0].guardrail_results] == [REASON_NOT_CONFIGURED]
    _assert_clean("stored records", _stored_blob(sink))
    _assert_clean("log output", _log_blob(debug_logs))


@pytest.mark.parametrize(("entry", "invoke"), _ENTRY_POINTS, ids=[e[0] for e in _ENTRY_POINTS])
def test_a_use_case_with_no_route_gets_a_fixed_detail_and_no_chain(
    entry: str, invoke: Callable[[Gateway, bytes], Any]
) -> None:
    """The use case id carries the sentinel, as the old detail echoed it. The
    operator log keeps the id (``log.error`` with ``use_case``); the caller does not
    get it."""
    use_case = f"uc-{SENTINEL}"
    gateway, sink = _gateway(_Runtime(), use_case_id=use_case, routed=False)
    with pytest.raises(GatewayError) as info:
        invoke(gateway, _call(use_case=use_case))
    err = info.value
    assert err.code == grpc.StatusCode.UNIMPLEMENTED
    assert err.reason == REASON_NOT_CONFIGURED
    assert err.detail == DETAIL_NOT_CONFIGURED
    _assert_error_is_clean(err)
    assert len(sink.records) == 1
    assert sink.records[0].invocation_outcome is InvocationOutcome.BLOCK
    assert [g.verdict for g in sink.records[0].guardrail_results] == [REASON_NOT_CONFIGURED]


def test_the_missing_route_is_still_logged_for_the_operator(
    debug_logs: pytest.LogCaptureFixture,
) -> None:
    gateway, _ = _gateway(_Runtime(), routed=False)
    with pytest.raises(GatewayError):
        gateway.invoke(_call())
    lines = [r for r in debug_logs.records if "without a route" in r.getMessage()]
    assert len(lines) == 1
    assert lines[0].__dict__["use_case"] == _UC


@pytest.mark.parametrize(("entry", "invoke"), _ENTRY_POINTS, ids=[e[0] for e in _ENTRY_POINTS])
def test_a_failing_sink_on_a_not_configured_call_keeps_the_caller_error_and_no_chain(
    entry: str, invoke: Callable[[Gateway, bytes], Any], debug_logs: pytest.LogCaptureFixture
) -> None:
    gateway, _ = _gateway(
        _Runtime(raises=RuntimeNotConfiguredError(f"x {SENTINEL}")), sink=_RaisingSink()
    )
    with pytest.raises(GatewayError) as info:
        invoke(gateway, _call())
    err = info.value
    assert err.reason == REASON_NOT_CONFIGURED
    assert err.detail == DETAIL_NOT_CONFIGURED
    _assert_error_is_clean(err)
    _assert_clean("log output", _log_blob(debug_logs))


# ---------------------------------------------------------------------------
# Part 1c. Residency
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(("entry", "invoke"), _ENTRY_POINTS, ids=[e[0] for e in _ENTRY_POINTS])
def test_a_residency_refusal_names_neither_region(
    entry: str, invoke: Callable[[Gateway, bytes], Any]
) -> None:
    caller_region = f"ap-{SENTINEL}"
    gateway, sink = _gateway(_Runtime())
    with pytest.raises(GatewayError) as info:
        invoke(gateway, _call(region=caller_region))
    err = info.value
    assert err.code == grpc.StatusCode.PERMISSION_DENIED
    assert err.reason == "AI_RESIDENCY_REFUSED"
    assert err.detail == DETAIL_RESIDENCY_REFUSED
    for text in (err.detail, str(err)):
        assert caller_region not in text
        assert _REGION not in text
    _assert_no_chain(err)
    assert len(sink.records) == 1
    assert sink.records[0].invocation_outcome is InvocationOutcome.BLOCK


# ---------------------------------------------------------------------------
# Part 2. runtime_timeout_seconds
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "bad",
    [True, False, 0, 0.0, -1, -0.5, math.nan, math.inf, -math.inf, "5", b"5", [1], 1j],
    ids=repr,
)
def test_an_invalid_timeout_is_rejected_without_echoing_the_value(bad: Any) -> None:
    with pytest.raises(ValueError) as info:
        _gateway(_Runtime(), timeout=bad)
    assert "runtime_timeout_seconds" in str(info.value)
    assert repr(bad) not in str(info.value)


def test_the_rejection_message_does_not_echo_a_distinctive_value() -> None:
    with pytest.raises(ValueError) as info:
        _gateway(_Runtime(), timeout=-1234.5678)
    assert "1234" not in str(info.value)


@pytest.mark.parametrize("good", [None, 1, 0.05, 30, 30.0])
def test_a_valid_timeout_is_accepted(good: float | None) -> None:
    _gateway(_Runtime(), timeout=good)


def test_without_a_timeout_the_runtime_runs_on_the_callers_thread() -> None:
    runtime = _Runtime()
    gateway, sink = _gateway(runtime)
    gateway.invoke(_call())
    assert runtime.thread is threading.current_thread()
    assert len(sink.records) == 1
    assert sink.records[0].invocation_outcome is not InvocationOutcome.BLOCK


def test_with_a_timeout_a_fast_runtime_passes_unchanged() -> None:
    plain_runtime, timed_runtime = _Runtime(text="same"), _Runtime(text="same")
    plain, plain_sink = _gateway(plain_runtime)
    timed, timed_sink = _gateway(timed_runtime, timeout=5.0)
    assert timed.invoke(_call()) == plain.invoke(_call())
    assert timed_runtime.calls == 1
    assert len(timed_sink.records) == len(plain_sink.records) == 1
    assert timed_sink.records[0].invocation_outcome is plain_sink.records[0].invocation_outcome
    # It did run in a worker: a daemon thread, so a hung runtime cannot hold exit.
    assert timed_runtime.thread is not None
    assert timed_runtime.thread is not threading.current_thread()
    assert timed_runtime.thread.daemon is True


@pytest.mark.parametrize(("entry", "invoke"), _ENTRY_POINTS, ids=[e[0] for e in _ENTRY_POINTS])
def test_a_slow_runtime_times_out_through_the_unavailable_path(
    entry: str, invoke: Callable[[Gateway, bytes], Any]
) -> None:
    runtime = _GatedRuntime()
    gateway, sink = _gateway(runtime, timeout=0.05)
    try:
        started = time.monotonic()
        with pytest.raises(GatewayError) as info:
            invoke(gateway, _call())
        assert time.monotonic() - started < 5
        err = info.value
        assert err.code == grpc.StatusCode.UNAVAILABLE
        assert err.reason == REASON_UNAVAILABLE
        assert err.detail == DETAIL_RUNTIME_UNAVAILABLE
        assert len(sink.records) == 1
        record = sink.records[0]
        assert record.invocation_outcome is InvocationOutcome.BLOCK
        assert record.output_hash is None
        assert [g.verdict for g in record.guardrail_results] == [REASON_UNAVAILABLE]
        assert not any(
            r.invocation_outcome is not InvocationOutcome.BLOCK for r in sink.records
        )
    finally:
        runtime.release.set()
    # The late answer arrives after the timeout; nothing seals or returns it.
    assert runtime.finished.wait(5)
    time.sleep(0.05)
    assert len(sink.records) == 1
    assert LATE_ANSWER not in _stored_blob(sink)


def test_slot_exhaustion_fails_closed_without_calling_the_runtime(
    debug_logs: pytest.LogCaptureFixture,
) -> None:
    runtime = _GatedRuntime()
    gateway, sink = _gateway(runtime, timeout=0.02)
    try:
        for _ in range(RUNTIME_MAX_IN_FLIGHT):
            with pytest.raises(GatewayError) as info:
                gateway.invoke(_call())
            assert info.value.reason == REASON_UNAVAILABLE
        deadline = time.monotonic() + 5
        while runtime.calls < RUNTIME_MAX_IN_FLIGHT and time.monotonic() < deadline:
            time.sleep(0.01)
        assert runtime.calls == RUNTIME_MAX_IN_FLIGHT

        # Every slot is held by a hung worker: the next call is refused at once.
        started = time.monotonic()
        with pytest.raises(GatewayError) as info:
            gateway.invoke(_call())
        assert time.monotonic() - started < 0.5  # no wait for the timeout, no queue
        err = info.value
        assert err.code == grpc.StatusCode.UNAVAILABLE
        assert err.reason == REASON_UNAVAILABLE
        assert err.detail == DETAIL_RUNTIME_UNAVAILABLE
        assert runtime.calls == RUNTIME_MAX_IN_FLIGHT  # the runtime was not called
        assert len(sink.records) == RUNTIME_MAX_IN_FLIGHT + 1
        assert all(r.invocation_outcome is InvocationOutcome.BLOCK for r in sink.records)
    finally:
        runtime.release.set()

    # Once the hung workers return, their slots are free again.
    deadline = time.monotonic() + 5
    while True:
        try:
            gateway.invoke(_call())
            break
        except GatewayError:
            assert time.monotonic() < deadline
            time.sleep(0.01)


@pytest.mark.parametrize(
    ("raised", "code", "reason"),
    [
        (RuntimeNotConfiguredError("x"), grpc.StatusCode.UNIMPLEMENTED, REASON_NOT_CONFIGURED),
        (UnroutedUseCaseError("x"), grpc.StatusCode.UNIMPLEMENTED, REASON_NOT_CONFIGURED),
        (RuntimeUnavailableError("x"), grpc.StatusCode.UNAVAILABLE, REASON_UNAVAILABLE),
    ],
    ids=["not-configured", "unrouted", "unavailable"],
)
def test_runtime_exceptions_keep_their_type_through_the_worker(
    raised: Exception, code: grpc.StatusCode, reason: str
) -> None:
    results = []
    for timeout in (None, 5.0):
        gateway, sink = _gateway(_Runtime(raises=raised), timeout=timeout)
        with pytest.raises(GatewayError) as info:
            gateway.invoke(_call())
        results.append((info.value.code, info.value.reason, info.value.detail, len(sink.records)))
    assert results[0] == results[1]
    assert results[0][0] == code
    assert results[0][1] == reason


def test_other_runtime_exceptions_keep_their_type_through_the_worker() -> None:
    for timeout in (None, 5.0):
        gateway, _ = _gateway(_Runtime(raises=ValueError("x")), timeout=timeout)
        with pytest.raises(ValueError):
            gateway.invoke(_call())


# ---------------------------------------------------------------------------
# build_gateway passes the timeout through
# ---------------------------------------------------------------------------


def test_build_gateway_passes_the_timeout_to_the_gateway(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(_CONFIG_DIR / "prompt_profiles.live-demo.json"))
    registry = _CONFIG_DIR / "registry.live-demo.json"

    def factory(_: PromptProfileRegistry) -> FakeRuntime:
        return FakeRuntime()

    default = build_gateway(registry, "local", "0.0.0-test", runtime_factory=factory)
    timed = build_gateway(
        registry, "local", "0.0.0-test", runtime_factory=factory, runtime_timeout_seconds=2.5
    )
    assert default._runtime_timeout is None
    assert timed._runtime_timeout == 2.5
    with pytest.raises(ValueError):
        build_gateway(
            registry, "local", "0.0.0-test", runtime_factory=factory, runtime_timeout_seconds=0
        )


# ---------------------------------------------------------------------------
# Review follow-ups: timeout bounds, slot safety, BaseException from a runtime
# ---------------------------------------------------------------------------


class _Fatal(BaseException):
    """A BaseException that is not KeyboardInterrupt or SystemExit."""


class _BarrierRuntime:
    """Raises ``first`` on the first call. Every other call waits at a barrier of
    RUNTIME_MAX_IN_FLIGHT parties, so they succeed only if that many runtime calls
    can be in flight at the same moment (that is, every slot is free)."""

    def __init__(self, first: BaseException | None = None) -> None:
        self.first = first
        self.barrier = threading.Barrier(RUNTIME_MAX_IN_FLIGHT, timeout=10)
        self.calls = 0
        self._lock = threading.Lock()

    def run(self, route: Route, call: Call) -> Result:
        with self._lock:
            self.calls += 1
            first, self.first = self.first, None
        if first is not None:
            raise first
        self.barrier.wait()
        return Result(text="ok", confidence="0.9")


def _assert_all_slots_free(gateway: Gateway) -> None:
    errors: list[BaseException] = []

    def one() -> None:
        try:
            gateway.invoke(_call())
        except BaseException as exc:  # collected and asserted below
            errors.append(exc)

    threads = [threading.Thread(target=one) for _ in range(RUNTIME_MAX_IN_FLIGHT)]
    for t in threads:
        t.start()
    for t in threads:
        t.join(15)
    assert not errors, "not every slot was free"


@pytest.mark.parametrize(
    "bad",
    [threading.TIMEOUT_MAX * 2, 1e10, 10**400, -(10**400), math.inf, math.nan, True, 0, -1, "5"],
    ids=lambda v: type(v).__name__ + ":" + repr(v)[:12],
)
def test_a_timeout_the_wait_cannot_take_is_rejected_with_value_error(bad: Any) -> None:
    with pytest.raises(ValueError, match="runtime_timeout_seconds"):
        _gateway(_Runtime(), timeout=bad)


def test_an_object_that_is_not_a_number_is_rejected_with_value_error() -> None:
    with pytest.raises(ValueError, match="runtime_timeout_seconds"):
        _gateway(_Runtime(), timeout=object())  # type: ignore[arg-type]


@pytest.mark.parametrize("good", [2.5, 1, threading.TIMEOUT_MAX])
def test_a_timeout_within_the_bound_is_accepted(good: float) -> None:
    _gateway(_Runtime(), timeout=good)


def test_a_thread_that_cannot_start_fails_closed_and_frees_the_slot(
    monkeypatch: pytest.MonkeyPatch, debug_logs: pytest.LogCaptureFixture
) -> None:
    runtime = _BarrierRuntime()
    gateway, sink = _gateway(runtime, timeout=10.0)

    def cannot_start(self: threading.Thread) -> None:
        raise MemoryError(f"no thread {SENTINEL}")  # not a RuntimeError

    with monkeypatch.context() as patch:
        patch.setattr(threading.Thread, "start", cannot_start)
        for _ in range(RUNTIME_MAX_IN_FLIGHT + 2):  # more attempts than slots
            with pytest.raises(GatewayError) as info:
                gateway.invoke(_call())
            err = info.value
            assert err.code == grpc.StatusCode.UNAVAILABLE
            assert err.reason == REASON_UNAVAILABLE
            assert err.detail == DETAIL_RUNTIME_UNAVAILABLE
            cause = err.__cause__
            assert isinstance(cause, RuntimeUnavailableError)
            assert cause.__context__ is None  # the MemoryError is not attached
            assert SENTINEL not in str(cause)
    assert runtime.calls == 0
    assert len(sink.records) == RUNTIME_MAX_IN_FLIGHT + 2
    assert all(r.invocation_outcome is InvocationOutcome.BLOCK for r in sink.records)
    _assert_clean("stored records", _stored_blob(sink))
    _assert_clean("log output", _log_blob(debug_logs))

    # No slot leaked: all RUNTIME_MAX_IN_FLIGHT can be in flight at the same time.
    _assert_all_slots_free(gateway)


def test_a_base_exception_from_the_runtime_keeps_its_type_with_a_timeout(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    hooked: list[Any] = []
    monkeypatch.setattr(threading, "excepthook", lambda args: hooked.append(args))

    plain_error = _Fatal(f"x {SENTINEL}")
    plain, _ = _gateway(_BarrierRuntime(first=plain_error))
    with pytest.raises(_Fatal) as plain_info:
        plain.invoke(_call())

    timed_error = _Fatal(f"x {SENTINEL}")
    runtime = _BarrierRuntime(first=timed_error)
    timed, sink = _gateway(runtime, timeout=10.0)
    with pytest.raises(_Fatal) as timed_info:
        timed.invoke(_call())

    assert plain_info.value is plain_error
    assert timed_info.value is timed_error  # the same object, as without a timeout
    assert timed_info.value.__context__ is None
    assert hooked == []  # threading.excepthook never saw it, so nothing was printed
    assert sink.records == []  # as without a timeout: nothing is sealed
    # The slot was released: every slot is free again.
    _assert_all_slots_free(timed)


# ---------------------------------------------------------------------------
# Second review follow-ups: the validator never raises; slot safety around start
# ---------------------------------------------------------------------------


class _RaisingFloat(float):
    def __float__(self) -> float:
        raise RuntimeError("no float")


class _RaisingInt(int):
    def __float__(self) -> float:
        raise RuntimeError("no float")


class _HostileClass:
    """isinstance() reaches for ``__class__`` and gets an exception."""

    @property  # type: ignore[misc]
    def __class__(self) -> type:
        raise RuntimeError("no class")


class _ModalRuntime:
    """Blocks on ``gate`` until released; in ``barrier_mode`` every call waits at a
    barrier of RUNTIME_MAX_IN_FLIGHT parties instead. Counts runtime calls in flight."""

    def __init__(self) -> None:
        self.gate = threading.Event()
        self.barrier_mode = False
        self.barrier = threading.Barrier(RUNTIME_MAX_IN_FLIGHT, timeout=10)
        self.lock = threading.Lock()
        self.in_flight = 0
        self.peak = 0
        self.calls = 0

    def run(self, route: Route, call: Call) -> Result:
        with self.lock:
            self.calls += 1
            self.in_flight += 1
            self.peak = max(self.peak, self.in_flight)
            barrier_mode = self.barrier_mode
        try:
            if barrier_mode:
                self.barrier.wait()
            else:
                self.gate.wait(10)
            return Result(text="ok", confidence="0.9")
        finally:
            with self.lock:
                self.in_flight -= 1


def _wait_for(predicate: Callable[[], bool], timeout: float = 5.0) -> None:
    deadline = time.monotonic() + timeout
    while not predicate():
        assert time.monotonic() < deadline, "condition not reached in time"
        time.sleep(0.005)


@pytest.mark.parametrize(
    "make_bad",
    [lambda: _RaisingFloat(2.5), lambda: _RaisingInt(3), lambda: _HostileClass()],
    ids=["float-subclass", "int-subclass", "hostile-class"],
)
def test_the_validator_returns_false_and_never_raises(make_bad: Callable[[], Any]) -> None:
    from ztax_gateway.server import _valid_timeout

    bad = make_bad()  # built here: pytest must not touch the hostile object at collection
    assert _valid_timeout(bad) is False
    with pytest.raises(ValueError, match="runtime_timeout_seconds"):
        _gateway(_Runtime(), timeout=bad)


@pytest.mark.parametrize("good", [2.5, 1, threading.TIMEOUT_MAX])
def test_the_validator_still_accepts_numbers_within_the_bound(good: float) -> None:
    from ztax_gateway.server import _valid_timeout

    assert _valid_timeout(good) is True


def test_a_failure_between_acquire_and_thread_start_frees_the_slot(
    monkeypatch: pytest.MonkeyPatch, debug_logs: pytest.LogCaptureFixture
) -> None:
    runtime = _BarrierRuntime()
    gateway, sink = _gateway(runtime, timeout=10.0)
    real_event = threading.Event
    attempts: list[int] = []

    def flaky_event() -> threading.Event:
        attempts.append(1)
        if len(attempts) == 1:
            raise MemoryError(f"no event {SENTINEL}")
        return real_event()

    with monkeypatch.context() as patch:
        patch.setattr(threading, "Event", flaky_event)
        with pytest.raises(GatewayError) as info:
            gateway.invoke(_call())
    err = info.value
    assert err.code == grpc.StatusCode.UNAVAILABLE
    assert err.reason == REASON_UNAVAILABLE
    assert err.detail == DETAIL_RUNTIME_UNAVAILABLE
    cause = err.__cause__
    assert isinstance(cause, RuntimeUnavailableError)
    assert cause.__context__ is None
    assert runtime.calls == 0
    assert len(sink.records) == 1
    assert sink.records[0].invocation_outcome is InvocationOutcome.BLOCK
    _assert_clean("stored records", _stored_blob(sink))
    _assert_clean("log output", _log_blob(debug_logs))
    # All 8 slots are free: 8 calls can be in flight together.
    _assert_all_slots_free(gateway)


def test_a_start_that_fails_after_the_thread_began_keeps_the_slot_until_the_runtime_returns(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    runtime = _ModalRuntime()
    gateway, sink = _gateway(runtime, timeout=10.0)
    real_thread = threading.Thread

    class _FailsAfterStart(real_thread):  # type: ignore[valid-type, misc]
        def start(self) -> None:
            super().start()
            raise OSError("start failed after the thread began")

    with monkeypatch.context() as patch:
        patch.setattr(threading, "Thread", _FailsAfterStart)
        with pytest.raises(GatewayError) as info:
            gateway.invoke(_call())
    assert info.value.reason == REASON_UNAVAILABLE
    assert info.value.detail == DETAIL_RUNTIME_UNAVAILABLE
    assert len(sink.records) == 1
    assert sink.records[0].invocation_outcome is InvocationOutcome.BLOCK

    # That call's runtime is still running (blocked on the gate) and still owns its slot.
    _wait_for(lambda: runtime.in_flight == 1)
    errors: list[BaseException] = []

    def filler() -> None:
        try:
            gateway.invoke(_call())
        except BaseException as exc:  # collected and asserted below
            errors.append(exc)

    fillers = [threading.Thread(target=filler) for _ in range(RUNTIME_MAX_IN_FLIGHT - 1)]
    for t in fillers:
        t.start()
    try:
        _wait_for(lambda: runtime.in_flight == RUNTIME_MAX_IN_FLIGHT)
        # 1 orphaned + 7 fillers fill all 8 slots: one more call is refused at once.
        started = time.monotonic()
        with pytest.raises(GatewayError) as busy:
            gateway.invoke(_call())
        assert time.monotonic() - started < 0.5
        assert busy.value.reason == REASON_UNAVAILABLE
        assert runtime.calls == RUNTIME_MAX_IN_FLIGHT
        assert runtime.peak == RUNTIME_MAX_IN_FLIGHT
    finally:
        runtime.gate.set()
    for t in fillers:
        t.join(15)
    assert not errors

    # Once the runtime returned the worker released its slot: all 8 are free again.
    _wait_for(lambda: not any(t.name == "ztax-gateway-runtime" for t in threading.enumerate()))
    runtime.barrier_mode = True
    _assert_all_slots_free(gateway)
    assert runtime.peak <= RUNTIME_MAX_IN_FLIGHT
