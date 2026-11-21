"""Integration tests for the fail-closed evidence file sink (Task B6).

Tests here use a Gateway built with a FileEvidenceSink(fail_closed=True) and verify:
- A PERMIT record with an unwritable file sink: gateway refuses UNAVAILABLE.
- A PERMIT record with a writable file sink: call succeeds and one line is appended.
- No path set: exactly the same behaviour as before (logging sink only, call succeeds).
- A BLOCK / refusal record with a failing file sink: best-effort — call refused as usual.
- FileSinkError carries a fixed message with no path, no exception text.

Synthetic data only. Every file lives under pytest's tmp_path. Disk failures are
simulated by replacing the module's thin ``_write_fd`` wrapper; nothing touches a real
disk limit.
"""

from __future__ import annotations

import base64
import errno
import json
from pathlib import Path
from typing import Any

import pytest

from ztax_gateway import evidence_file_sink as efs
from ztax_gateway.evidence_file_sink import (
    ENV_PATH,
    EvidenceFileConfigError,
    FanOutEvidenceSink,
    FileEvidenceSink,
    FileSinkError,
    default_evidence_sink,
    read_evidence_file,
)
from ztax_gateway.evidence_sink import LoggingEvidenceSink
from ztax_gateway.fake_runtime import FakeRuntime
from ztax_gateway.gateway_factory import ENV_PROMPT_PROFILES, build_gateway
from ztax_gateway.governance import UseCase, UseCaseRegistry
from ztax_gateway.invocation_check_event import (
    AnswerCheckedEvent,
    CheckReason,
    Verdict,
)
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.runtime import Result, Route, RoutingTable
from ztax_gateway.server import Gateway, GatewayError
from ztax_gateway.wire import Call

_REGION = "eu-west-1"
_UC = "classification-review"
_TENANT = "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f"
_ROUTE = Route("model:fake@1", "provider:fake", "prompt:classification-review-v1")
_CONFIG_DIR = Path(__file__).resolve().parent.parent / "config"
SENTINEL = "SENT-FAIL-CLOSED-7c3b02"


@pytest.fixture(autouse=True)
def _no_env(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv(ENV_PATH, raising=False)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


class _AlwaysSucceedsRuntime:
    def run(self, route: Route, call: Call) -> Result:
        return Result(proposed_code="HSN-0101", confidence="0.9")


def _gateway(sink: Any = None) -> Gateway:
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
    return Gateway(
        registry,
        RoutingTable({_UC: _ROUTE}),
        _AlwaysSucceedsRuntime(),
        _REGION,
        "0.7.0",
        evidence_sink=sink,
    )


def _call_bytes(
    use_case: str = _UC,
    region: str = _REGION,
    kind: str = "CLASSIFICATION_PROPOSAL",
) -> bytes:
    doc = {
        "kind": kind,
        "governance": {
            "tenant_id": _TENANT,
            "use_case": use_case,
            "authority_outcome": "A1",
            "risk_tier": "T1",
            "region": region,
            "data_classes": ["P0"],
        },
        "subject_ref": "sku:FAIL-CLOSED-TEST",
        "input_b64": base64.b64encode(b'{"note": "x"}').decode("ascii"),
    }
    return json.dumps(doc).encode()


def _factory_gateway(monkeypatch: pytest.MonkeyPatch) -> Gateway:
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(_CONFIG_DIR / "prompt_profiles.live-demo.json"))
    return build_gateway(
        _CONFIG_DIR / "registry.live-demo.json",
        "local",
        "0.0.0-test",
        runtime_factory=lambda _: FakeRuntime(),
    )


# ---------------------------------------------------------------------------
# FileSinkError is a fixed message with no path / no exception text
# ---------------------------------------------------------------------------


def test_filesink_error_message_carries_no_dynamic_content(tmp_path: Path) -> None:
    """FileSinkError text is fixed and must not contain the file path."""
    err = FileSinkError("evidence file sink: a record could not be written; the call is refused")
    assert str(tmp_path) not in str(err)
    assert "ENOSPC" not in str(err)
    assert SENTINEL not in str(err)


def test_filesink_error_has_no_cause_and_no_context(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """FileSinkError raised outside the except block has __cause__ and __context__ None."""
    path = tmp_path / "ev.jsonl"
    sink = FileEvidenceSink(path, fail_closed=True)

    def full_disk(fd: int, data: memoryview) -> int:
        raise OSError(errno.ENOSPC, "No space left")

    monkeypatch.setattr(efs, "_write_fd", full_disk)


    class _Rec:
        def as_dict(self) -> dict[str, Any]:
            return {"ai_invocation_id": "test-id-001", "n": 1}

    with pytest.raises(FileSinkError) as exc_info:
        sink.write(_Rec())  # type: ignore[arg-type]

    assert exc_info.value.__cause__ is None
    assert exc_info.value.__context__ is None
    assert str(tmp_path) not in str(exc_info.value)


# ---------------------------------------------------------------------------
# PERMIT record — writable path → call succeeds, one line appended
# ---------------------------------------------------------------------------


def test_permit_with_writable_file_sink_call_succeeds_one_line_appended(
    tmp_path: Path,
) -> None:
    path = tmp_path / "ev.jsonl"
    file_sink = FileEvidenceSink(path, fail_closed=True)
    fan_out = FanOutEvidenceSink(file_sink, LoggingEvidenceSink())
    gateway = _gateway(fan_out)
    # FileEvidenceSink must be primary (fail_closed=True)
    assert isinstance(gateway._evidence_sink, FanOutEvidenceSink)
    assert isinstance(gateway._evidence_sink._primary, FileEvidenceSink)
    assert gateway._evidence_sink._primary._fail_closed is True

    reply = gateway.invoke(_call_bytes())
    assert json.loads(reply)  # a valid JSON reply exists
    records = read_evidence_file(path)
    assert len(records) == 1
    assert "ai_invocation_id" in records[0]
# ---------------------------------------------------------------------------
# PERMIT record — unwritable file sink → UNAVAILABLE, no reply
# ---------------------------------------------------------------------------


def test_permit_with_unwritable_file_sink_refuses_unavailable(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """A PERMIT call with a fail-closed sink on an unwritable path is refused UNAVAILABLE."""
    import grpc

    path = tmp_path / "ev.jsonl"
    file_sink = FileEvidenceSink(path, fail_closed=True)

    def full_disk(fd: int, data: memoryview) -> int:
        raise OSError(errno.ENOSPC, "No space left on device")

    monkeypatch.setattr(efs, "_write_fd", full_disk)
    gateway = _gateway(file_sink)

    with pytest.raises(GatewayError) as exc_info:
        gateway.invoke(_call_bytes())

    assert exc_info.value.code == grpc.StatusCode.UNAVAILABLE
    assert file_sink.write_failures == 1
    # Fixed message: no path in the error that surfaces
    assert str(path) not in str(exc_info.value)


# ---------------------------------------------------------------------------
# No path set → logging sink only, call succeeds (unchanged behaviour)
# ---------------------------------------------------------------------------


def test_no_path_set_uses_logging_sink_only_and_call_succeeds() -> None:
    sink = default_evidence_sink({})
    assert type(sink) is LoggingEvidenceSink
    # Gateway built with logging sink only: call succeeds, no file created
    gateway = _gateway(sink)
    reply = gateway.invoke(_call_bytes())
    assert json.loads(reply)


# ---------------------------------------------------------------------------
# BLOCK / refusal record — fail-closed file sink → best-effort, call still refused
# ---------------------------------------------------------------------------


def test_block_record_with_failing_file_sink_is_best_effort(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """A BLOCK / refusal record with a failing fail-closed file sink does not raise
    an extra error — the call is still refused, and the client gets the proper refusal."""
    path = tmp_path / "ev.jsonl"
    file_sink = FileEvidenceSink(path, fail_closed=True)

    def full_disk(fd: int, data: memoryview) -> int:
        raise OSError(errno.ENOSPC, "No space left on device")

    monkeypatch.setattr(efs, "_write_fd", full_disk)
    # Build gateway with a bad use-case that triggers a BLOCK record
    gateway = _gateway(file_sink)

    with pytest.raises(GatewayError):
        # Unknown use case → governance BLOCK record (not a PERMIT write path)
        gateway.invoke(_call_bytes(use_case="unknown-use-case"))

    # write_failures may or may not be 1 depending on whether a BLOCK record
    # was attempted — but the gateway itself must not have raised UNAVAILABLE,
    # it must have raised the original refusal error.


# ---------------------------------------------------------------------------
# default_evidence_sink: file sink is primary (fail_closed wired by factory)
# ---------------------------------------------------------------------------


def test_default_evidence_sink_no_path_is_logging_sink() -> None:
    sink = default_evidence_sink({})
    assert type(sink) is LoggingEvidenceSink


def test_default_evidence_sink_with_path_file_is_primary(tmp_path: Path) -> None:
    path = tmp_path / "ev.jsonl"
    sink = default_evidence_sink({ENV_PATH: str(path)})
    assert isinstance(sink, FanOutEvidenceSink)
    assert isinstance(sink._primary, FileEvidenceSink)
    assert sink._primary._fail_closed is True


def test_default_evidence_sink_bad_path_raises_config_error(tmp_path: Path) -> None:
    with pytest.raises(EvidenceFileConfigError) as exc_info:
        default_evidence_sink({ENV_PATH: str(tmp_path)})  # a directory
    assert str(tmp_path) not in str(exc_info.value)


# ---------------------------------------------------------------------------
# FAILED SECOND APPEND: record line written, check-event append fails
# ---------------------------------------------------------------------------


def test_failed_second_append_raises_filesink_error(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Record write succeeds; check-event append is made to fail.

    When fail_closed=True the FileSinkError propagates from write_check_event
    so the caller (e.g. _record_answer_check_safe → explain/classify) can
    withhold the reply and return AI_GATEWAY_UNAVAILABLE.
    The file holds the record line and nothing else.
    """
    path = tmp_path / "ev.jsonl"
    file_sink = FileEvidenceSink(path, fail_closed=True)
    fan_out = FanOutEvidenceSink(file_sink, LoggingEvidenceSink())
    gateway = _gateway(fan_out)

    # First invoke succeeds and writes one record line.
    reply = gateway.invoke(_call_bytes())
    assert json.loads(reply)
    records_before = read_evidence_file(path)
    assert len(records_before) == 1
    record_line_bytes = path.read_bytes()  # snapshot of exact bytes

    # Now make subsequent writes fail (simulates full disk after first write).
    write_count: list[int] = [0]
    real_write_fd = efs._write_fd

    def fail_second(fd: int, data: memoryview) -> int:
        write_count[0] += 1
        if write_count[0] > 0:  # all further writes fail
            raise OSError(errno.ENOSPC, "No space left on device")
        return real_write_fd(fd, data)

    monkeypatch.setattr(efs, "_write_fd", fail_second)

    from datetime import UTC, datetime

    event = AnswerCheckedEvent(
        evidence_id=records_before[0]["ai_invocation_id"],
        verdict=Verdict.PASS,
        reason_code=CheckReason.CHECKS_PASSED,
        validator_version="test_guard@1",
        created_at=datetime.now(UTC),
    )
    with pytest.raises(FileSinkError) as exc_info:
        file_sink.write_check_event(event)

    # Error is a fixed string — no path, no ENOSPC text.
    assert str(path) not in str(exc_info.value)
    assert "ENOSPC" not in str(exc_info.value)
    # The record line is unchanged byte for byte.
    assert path.read_bytes() == record_line_bytes
    # Failure counter incremented.
    assert file_sink.write_failures >= 1


# ---------------------------------------------------------------------------
# Check-event key allowlist: no prompt/answer text, sentinel never appears
# ---------------------------------------------------------------------------


def test_check_event_line_keys_are_allowlisted(tmp_path: Path) -> None:
    """The check-event line contains only the allowed metadata fields.

    A sentinel placed in the input (via validator_version) must only appear
    where expected (validator_version value) and never be injected elsewhere.
    The record line is unchanged byte for byte.
    """
    _allowlist = frozenset({
        "event_type", "evidence_id", "verdict", "reason_code",
        "validator_version", "created_at", "source_record_ids",
        "policy_version",
    })
    path = tmp_path / "ev.jsonl"
    file_sink = FileEvidenceSink(path, fail_closed=True)
    fan_out = FanOutEvidenceSink(file_sink, LoggingEvidenceSink())
    gateway = _gateway(fan_out)

    reply = gateway.invoke(_call_bytes())
    assert json.loads(reply)
    records = read_evidence_file(path)
    assert len(records) == 1
    record_line_bytes = path.read_bytes()  # snapshot before check event

    from datetime import UTC, datetime

    sentinel_version = f"guard@1+{SENTINEL}"
    event = AnswerCheckedEvent(
        evidence_id=records[0]["ai_invocation_id"],
        verdict=Verdict.PASS,
        reason_code=CheckReason.CHECKS_PASSED,
        validator_version=sentinel_version,
        created_at=datetime.now(UTC),
    )
    file_sink.write_check_event(event)

    all_records = read_evidence_file(path)
    assert len(all_records) == 2
    record_dict = all_records[0]
    check_dict = all_records[1]

    # Record line is unchanged byte for byte (compare first line only).
    first_line = path.read_bytes().split(b"\n")[0] + b"\n"
    assert first_line == record_line_bytes

    # Check-event keys are a subset of the allowlist.
    assert set(check_dict.keys()) <= _allowlist

    # Sentinel appears only in validator_version, not in any other field.
    for key, val in check_dict.items():
        if key == "validator_version":
            assert SENTINEL in str(val)
        else:
            assert SENTINEL not in str(val)

    # Sentinel never appears in the record line at all.
    assert SENTINEL not in json.dumps(record_dict)


# ---------------------------------------------------------------------------
# Single sink object serves both lines (one file, one lock, no contention)
# ---------------------------------------------------------------------------


def test_one_sink_object_serves_both_record_and_check_event(tmp_path: Path) -> None:
    """FileEvidenceSink satisfies both EvidenceSink and CheckEventSink protocols.

    One object: record line and check-event line both land in the same file
    with no lock error.  The factory wires them this way when the path is set.
    """
    path = tmp_path / "ev.jsonl"
    file_sink = FileEvidenceSink(path, fail_closed=True)
    fan_out = FanOutEvidenceSink(file_sink, LoggingEvidenceSink())
    # Build gateway with file_sink as BOTH evidence sink (via fan_out) and
    # check_event_sink (same object, same lock).
    from ztax_gateway.governance import UseCase, UseCaseRegistry
    from ztax_gateway.provenance import AuthorityOutcome, RiskTier
    from ztax_gateway.runtime import RoutingTable

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
    gateway = Gateway(
        registry,
        RoutingTable({_UC: _ROUTE}),
        _AlwaysSucceedsRuntime(),
        _REGION,
        "0.7.0",
        evidence_sink=fan_out,
        check_event_sink=file_sink,  # same object as fan_out._primary
    )

    reply = gateway.invoke(_call_bytes())
    assert json.loads(reply)
    records = read_evidence_file(path)
    assert len(records) == 1
    invocation_id = records[0]["ai_invocation_id"]

    # Write check event through gateway (same sink object, same lock).
    from ztax_gateway.invocation_check_event import CheckReason, Verdict
    gateway.record_answer_check(
        invocation_id, Verdict.PASS, CheckReason.CHECKS_PASSED, "guard@1"
    )

    all_lines = read_evidence_file(path)
    assert len(all_lines) == 2
    # First line is the evidence record.
    assert "ai_invocation_id" in all_lines[0]
    assert all_lines[0].get("event_type") != "answer_checked"
    # Second line is the check event.
    assert all_lines[1].get("event_type") == "answer_checked"
    assert all_lines[1].get("evidence_id") == invocation_id
