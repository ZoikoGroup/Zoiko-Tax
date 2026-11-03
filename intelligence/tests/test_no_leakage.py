"""No raw prompt or answer text in stored evidence, events, logs or errors.

Three unique sentinels stand in for sensitive text:

* ``SENTINEL_PROMPT``  -- inside the system prompt of a registered PromptProfile
* ``SENTINEL_INPUT``   -- inside the user content of the request
* ``SENTINEL_ANSWER``  -- returned by the fake runtime

The answer may appear in the reply returned to the caller and nowhere else: not
in the evidence record, not in the answer-checked event, not in any log line,
and not in the detail of an error handed back to the caller. The prompt may
leave only as its SHA-256 (``prompt_content_hash``).
"""

from __future__ import annotations

import base64
import hashlib
import importlib.util
import json
import logging
import sys
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import grpc
import pytest

from ztax_gateway.evidence_sink import InMemoryEvidenceSink, LoggingEvidenceSink
from ztax_gateway.explanation_guard import check_guard
from ztax_gateway.governance import UseCase, UseCaseRegistry
from ztax_gateway.invocation_check_event import (
    CheckReason,
    InMemoryCheckEventSink,
    LoggingCheckEventSink,
    Verdict,
)
from ztax_gateway.invocation_evidence import InvocationEvidenceRecord
from ztax_gateway.production_gates import PromptProfileRegistry
from ztax_gateway.production_registries import PromptProfile
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.runtime import Result, Route, RoutingTable, RuntimeUnavailableError
from ztax_gateway.server import FULL_METHOD, Gateway, GatewayError, load_config, serve
from ztax_gateway.wire import Call

SENTINEL_PROMPT = "SENTINEL-PROMPT-f3a9c1"
SENTINEL_INPUT = "SENTINEL-INPUT-7d20be"
SENTINEL_ANSWER = "SENTINEL-ANSWER-b81d4e"
SENTINELS = (SENTINEL_PROMPT, SENTINEL_INPUT, SENTINEL_ANSWER)

_REGION = "eu-west-1"
_UC = "classification-review"
_LABEL = "prompt:classification-review-v1"
_SYSTEM_PROMPT = f"You are a tax classification assistant. {SENTINEL_PROMPT}"
_TENANT = "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f"


# ---------------------------------------------------------------------------
# Fixtures and helpers
# ---------------------------------------------------------------------------


class _Runtime:
    """Returns the answer sentinel, or raises what it was told to raise."""

    def __init__(self, raises: BaseException | None = None) -> None:
        self.raises = raises
        self.calls = 0

    def run(self, route: Route, call: Call) -> Result:
        self.calls += 1
        if self.raises is not None:
            raise self.raises
        return Result(text=SENTINEL_ANSWER, confidence="0.9")


class _TeeEvidenceSink:
    """Keeps records for inspection AND writes the production log line."""

    def __init__(self) -> None:
        self.memory = InMemoryEvidenceSink()
        self._logging = LoggingEvidenceSink()

    def write(self, record: InvocationEvidenceRecord) -> None:
        self.memory.write(record)
        self._logging.write(record)


class _TeeCheckSink:
    def __init__(self) -> None:
        self.memory = InMemoryCheckEventSink()
        self._logging = LoggingCheckEventSink()

    def write_check_event(self, event: Any) -> None:
        self.memory.write_check_event(event)
        self._logging.write_check_event(event)


class _RaisingEvidenceSink:
    def write(self, record: InvocationEvidenceRecord) -> None:
        raise RuntimeError(f"evidence store down: {SENTINEL_ANSWER}")


class _RaisingCheckSink:
    def write_check_event(self, event: Any) -> None:
        raise RuntimeError(f"check store down: {SENTINEL_ANSWER}")


class _Rig:
    def __init__(
        self,
        *,
        runtime: _Runtime | None = None,
        evidence_sink: Any = None,
        check_sink: Any = None,
    ) -> None:
        self.registry = UseCaseRegistry()
        self.registry.register(
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
        routes = RoutingTable({_UC: Route("model:fake@1", "provider:fake", _LABEL)})
        self.prompts = PromptProfileRegistry()
        self.profile = PromptProfile(
            profile_id=_LABEL,
            owner="team-ai",
            version="1.0.0",
            system_prompt=_SYSTEM_PROMPT,
            variable_schema={},
            output_schema={},
            max_risk_tier=RiskTier.T2,
            authority_ceiling=AuthorityOutcome.A2,
            safety_rules=("Do not invent values.",),
        )
        self.prompts.register(self.profile)
        self.runtime = runtime or _Runtime()
        self.evidence = evidence_sink if evidence_sink is not None else _TeeEvidenceSink()
        self.checks = check_sink if check_sink is not None else _TeeCheckSink()
        self.gateway = Gateway(
            self.registry,
            routes,
            self.runtime,
            _REGION,
            "0.7.0",
            evidence_sink=self.evidence,
            prompt_profile_registry=self.prompts,
            check_event_sink=self.checks,
        )

    @property
    def records(self) -> tuple[InvocationEvidenceRecord, ...]:
        return tuple(self.evidence.memory.records)

    @property
    def events(self) -> tuple[Any, ...]:
        return tuple(self.checks.memory.events)


def _call(
    use_case: str = _UC,
    data_classes: tuple[str, ...] = ("P1",),
) -> bytes:
    doc = {
        "kind": "SUGGESTION",
        "governance": {
            "tenant_id": _TENANT,
            "use_case": use_case,
            "authority_outcome": "A1",
            "risk_tier": "T1",
            "region": _REGION,
            "data_classes": list(data_classes),
        },
        "subject_ref": "sku:LEAK-001",
        "input_b64": base64.b64encode(json.dumps({"note": SENTINEL_INPUT}).encode()).decode(
            "ascii"
        ),
    }
    return json.dumps(doc).encode("utf-8")


def _log_blob(caplog: pytest.LogCaptureFixture) -> str:
    """Everything the logging system saw: message, traceback and extra fields."""
    fmt = logging.Formatter()
    parts: list[str] = []
    for rec in caplog.records:
        parts.append(fmt.format(rec))
        parts.append(repr(vars(rec)))
    return "\n".join(parts)


def _json(obj: Any) -> str:
    return json.dumps(obj, default=str, sort_keys=True)


def _assert_clean(label: str, text: str, *, allow: tuple[str, ...] = ()) -> None:
    for sentinel in SENTINELS:
        if sentinel in allow:
            continue
        assert sentinel not in text, f"{label} leaks {sentinel}"


def _stored_blob(rig: _Rig) -> str:
    return _json([r.as_dict() for r in rig.records] + [e.to_dict() for e in rig.events])


@pytest.fixture()
def debug_logs(caplog: pytest.LogCaptureFixture) -> Iterator[pytest.LogCaptureFixture]:
    with caplog.at_level(logging.DEBUG):
        yield caplog


# ---------------------------------------------------------------------------
# 2. A successful call: nothing but the reply carries the answer
# ---------------------------------------------------------------------------


def test_successful_call_leaves_no_sentinel_in_records_events_or_logs(
    debug_logs: pytest.LogCaptureFixture,
) -> None:
    rig = _Rig()
    reply, invocation_id = rig.gateway.invoke_with_id(_call())
    assert invocation_id is not None
    rig.gateway.record_answer_check(
        invocation_id, Verdict.PASS, CheckReason.CHECKS_PASSED, "explanation_guard@1"
    )

    # The answer is in the reply to the caller, and only there.
    assert SENTINEL_ANSWER in reply.decode("utf-8")

    assert len(rig.records) == 1 and len(rig.events) == 1
    record = rig.records[0]
    event = rig.events[0]
    _assert_clean("evidence record dict", _json(record.as_dict()))
    _assert_clean("evidence record repr", repr(record))
    _assert_clean("check event dict", _json(event.to_dict()))
    _assert_clean("check event repr", repr(event))

    blob = _log_blob(debug_logs)
    _assert_clean("log output (DEBUG, all loggers)", blob)

    # The two logging sinks really wrote a line each, so the check is not vacuous.
    evidence_lines = [r for r in debug_logs.records if r.name == "ztax_gateway.evidence"]
    check_lines = [r for r in debug_logs.records if r.name == "ztax_gateway.evidence.check"]
    assert len(evidence_lines) == 1
    assert len(check_lines) == 1
    _assert_clean("LoggingEvidenceSink line", evidence_lines[0].getMessage())
    _assert_clean("LoggingCheckEventSink line", check_lines[0].getMessage())
    assert json.loads(evidence_lines[0].getMessage())["ai_invocation_id"] == invocation_id


def test_invoke_without_id_also_leaves_no_sentinel(
    debug_logs: pytest.LogCaptureFixture,
) -> None:
    rig = _Rig()
    reply = rig.gateway.invoke(_call())
    assert SENTINEL_ANSWER in reply.decode("utf-8")
    _assert_clean("evidence record", _stored_blob(rig))
    _assert_clean("log output", _log_blob(debug_logs))


# ---------------------------------------------------------------------------
# 3. Failure paths
# ---------------------------------------------------------------------------


def _refuse_killed(rig: _Rig) -> bytes:
    rig.registry.kill(_UC)
    return _call()


def _refuse_unregistered(rig: _Rig) -> bytes:
    return _call(use_case="not-registered")


def _refuse_bad_data_class(rig: _Rig) -> bytes:
    return _call(data_classes=("P3",))


@pytest.mark.parametrize(
    "prepare",
    [_refuse_killed, _refuse_unregistered, _refuse_bad_data_class],
    ids=["killed", "unregistered-use-case", "bad-data-class"],
)
def test_refused_calls_leak_nothing(debug_logs: pytest.LogCaptureFixture, prepare: Any) -> None:
    rig = _Rig()
    payload = prepare(rig)
    with pytest.raises(GatewayError) as info:
        rig.gateway.invoke_with_id(payload)
    err = info.value
    assert rig.runtime.calls == 0
    _assert_clean("GatewayError.detail", err.detail)
    _assert_clean("GatewayError.reason", err.reason)
    _assert_clean("GatewayError str", str(err))
    assert len(rig.records) == 1
    assert rig.records[0].prompt_content_hash is None
    _assert_clean("stored records", _stored_blob(rig))
    _assert_clean("log output", _log_blob(debug_logs))


def test_runtime_unavailable_with_sentinel_in_its_message_leaks_nothing(
    debug_logs: pytest.LogCaptureFixture,
) -> None:
    rig = _Rig(runtime=_Runtime(RuntimeUnavailableError(f"provider said: {SENTINEL_ANSWER}")))
    with pytest.raises(GatewayError) as info:
        rig.gateway.invoke_with_id(_call())
    _assert_clean("GatewayError.detail", info.value.detail)
    _assert_clean("stored records", _stored_blob(rig))
    _assert_clean("log output", _log_blob(debug_logs))


@pytest.fixture()
def connect() -> Iterator[Any]:
    servers: list[grpc.Server] = []
    channels: list[grpc.Channel] = []

    def make(gw: Gateway) -> grpc.Channel:
        server, port = serve(gw, "127.0.0.1:0", None, insecure_local=True)
        servers.append(server)
        channel = grpc.insecure_channel(f"127.0.0.1:{port}")
        channels.append(channel)
        return channel

    yield make
    for channel in channels:
        channel.close()
    for server in servers:
        server.stop(0)


def test_runtime_raising_an_unexpected_exception_leaks_nothing_over_the_wire(
    debug_logs: pytest.LogCaptureFixture, connect: Any
) -> None:
    rig = _Rig(runtime=_Runtime(RuntimeError(f"model blew up on: {SENTINEL_ANSWER}")))
    channel = connect(rig.gateway)
    with pytest.raises(grpc.RpcError) as info:
        channel.unary_unary(FULL_METHOD)(_call(), timeout=5)
    err: Any = info.value
    _assert_clean("gRPC error details", err.details())
    _assert_clean("gRPC trailing metadata", repr(err.trailing_metadata()))
    _assert_clean("stored records", _stored_blob(rig))
    _assert_clean("log output", _log_blob(debug_logs))


def test_evidence_sink_failing_on_a_permitted_call_leaks_nothing(
    debug_logs: pytest.LogCaptureFixture,
) -> None:
    rig = _Rig(evidence_sink=_RaisingEvidenceSink())
    with pytest.raises(GatewayError) as info:
        rig.gateway.invoke_with_id(_call())
    # The permitted reply is withheld; the caller gets only the fixed reason.
    _assert_clean("GatewayError.detail", info.value.detail)
    _assert_clean("GatewayError str", str(info.value))
    _assert_clean("log output", _log_blob(debug_logs))


def test_evidence_sink_failing_on_a_refusal_leaks_nothing(
    debug_logs: pytest.LogCaptureFixture,
) -> None:
    rig = _Rig(evidence_sink=_RaisingEvidenceSink())
    with pytest.raises(GatewayError) as info:
        rig.gateway.invoke_with_id(_call(data_classes=("P3",)))
    _assert_clean("GatewayError.detail", info.value.detail)
    _assert_clean("log output", _log_blob(debug_logs))


def test_check_event_sink_failing_leaks_nothing_into_records_or_logs(
    debug_logs: pytest.LogCaptureFixture,
) -> None:
    rig = _Rig(check_sink=_RaisingCheckSink())
    _, invocation_id = rig.gateway.invoke_with_id(_call())
    assert invocation_id is not None
    with pytest.raises(RuntimeError):
        rig.gateway.record_answer_check(
            invocation_id, Verdict.PASS, CheckReason.CHECKS_PASSED, "explanation_guard@1"
        )
    _assert_clean("stored records", _json([r.as_dict() for r in rig.records]))
    _assert_clean("log output", _log_blob(debug_logs))


def test_gateway_error_detail_is_fixed_when_the_evidence_sink_fails() -> None:
    """Records what ``_write_sink`` puts in ``GatewayError.detail``.

    It is a fixed string, never the sink's exception text. This test pins it
    so a change is a conscious one; the leak assertion lives in
    ``test_evidence_sink_failing_on_a_permitted_call_leaks_nothing``.
    """
    rig = _Rig(evidence_sink=_RaisingEvidenceSink())
    with pytest.raises(GatewayError) as info:
        rig.gateway.invoke_with_id(_call())
    assert info.value.reason == "AI_GATEWAY_UNAVAILABLE"
    assert info.value.detail == "evidence write failed"


# ---------------------------------------------------------------------------
# 4. The prompt hash is the only trace of the prompt
# ---------------------------------------------------------------------------


def test_prompt_hash_is_the_sha256_of_the_system_prompt_and_the_only_trace() -> None:
    rig = _Rig()
    rig.gateway.invoke_with_id(_call())
    record = rig.records[0]
    expected = hashlib.sha256(_SYSTEM_PROMPT.encode("utf-8")).hexdigest()
    assert record.prompt_content_hash == expected
    assert rig.profile.content_hash == expected

    fields = record.as_dict()
    assert fields["prompt_content_hash"] == expected
    rest = {k: v for k, v in fields.items() if k != "prompt_content_hash"}
    rest_json = _json(rest)
    assert SENTINEL_PROMPT not in rest_json
    assert _SYSTEM_PROMPT not in rest_json
    assert expected not in rest_json
    # Nor is the prompt text anywhere in the record or its hash field.
    assert SENTINEL_PROMPT not in _json(fields)
    # The hash does not appear in the check event either.
    assert expected not in _json([e.to_dict() for e in rig.events])


# ---------------------------------------------------------------------------
# 5. The guard path through report_ai_http.explain
# ---------------------------------------------------------------------------


def _load_tools() -> dict[str, Any]:
    tools_dir = Path(__file__).resolve().parent.parent / "tools"
    names = ("groq_demo_runtime", "report_fixture", "serve_live_demo", "report_ai_http")
    before = {name: sys.modules.get(name) for name in names}
    loaded: dict[str, Any] = {}
    try:
        for name in names:
            spec = importlib.util.spec_from_file_location(name, tools_dir / f"{name}.py")
            assert spec is not None and spec.loader is not None
            module = importlib.util.module_from_spec(spec)
            sys.modules[name] = module
            spec.loader.exec_module(module)
            loaded[name] = module
    finally:
        for name, previous in before.items():
            if previous is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = previous
    return loaded


class _ExplainRig:
    def __init__(self, result: Result, *, check_sink: Any = None) -> None:
        mods = _load_tools()
        self.report_ai_http: Any = mods["report_ai_http"]
        self.report_fixture: Any = mods["report_fixture"]
        self.fixture: Any = self.report_fixture.generate_fixture()
        config = Path(__file__).resolve().parent.parent / "config" / "registry.live-demo.json"
        registry, routes = load_config(config)

        class _Rt:
            def run(self, route: Route, call: Call) -> Result:
                return result

        self.evidence = InMemoryEvidenceSink()
        self.checks = check_sink if check_sink is not None else InMemoryCheckEventSink()
        self.gateway = Gateway(
            registry,
            routes,
            _Rt(),
            "local",
            "0.0.0-test",
            evidence_sink=self.evidence,
            check_event_sink=self.checks,
        )

    def explain(self) -> dict[str, Any]:
        out: dict[str, Any] = self.report_ai_http.explain(
            self.gateway, self.fixture, "obligation-row", ["OBL-001"]
        )
        return out


def _answer(explanation: str) -> Result:
    return Result(
        fields={
            "explanation": explanation,
            "cited_ids": json.dumps(["OBL-001"]),
            "abstain": "false",
            "reason": "",
        },
        confidence="0.9",
    )


def test_a_guard_failure_leaks_the_answer_into_no_event_record_or_log(
    debug_logs: pytest.LogCaptureFixture,
) -> None:
    # A number that is in no cited row makes the guard reject the answer.
    rig = _ExplainRig(_answer(f"{SENTINEL_ANSWER} says OBL-001 is 987654321 USD."))
    out = rig.explain()
    assert out["outcome"] == "ABSTAINED"

    events = rig.checks.events
    assert len(events) == 1
    assert events[0].verdict is Verdict.ABSTAIN
    assert events[0].reason_code is CheckReason.GUARD_NUMBER_NOT_IN_ROWS
    _assert_clean("check event", _json(events[0].to_dict()), allow=())
    _assert_clean("evidence records", _json([r.as_dict() for r in rig.evidence.records]))
    _assert_clean("log output", _log_blob(debug_logs))


def test_the_guard_rejection_message_does_not_quote_the_answer() -> None:
    rows = [{"id": "OBL-001", "amount": "100.00", "status": "OPEN"}]
    result = check_guard(rows, ["OBL-001"], f"{SENTINEL_ANSWER} is 987654321 USD")
    assert not result.ok
    assert SENTINEL_ANSWER not in result.code
    assert SENTINEL_ANSWER not in result.message


# ---------------------------------------------------------------------------
# 6. KNOWN ISSUE: a failing check sink drops the demo reply
# ---------------------------------------------------------------------------


def test_a_raising_check_event_sink_must_not_drop_the_reply() -> None:
    amount_rig = _ExplainRig(_answer("placeholder"))
    amount = str(
        amount_rig.report_fixture.rows_for_kind(amount_rig.fixture, "obligation-row", ["OBL-001"])[
            0
        ]["amount"]
    )
    rig = _ExplainRig(_answer(f"OBL-001 is {amount} USD."), check_sink=_RaisingCheckSink())
    out = rig.explain()
    assert out["outcome"] == "EXPLAINED"
    assert out["explanation"]
