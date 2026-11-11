"""Tests for invocation_check_event.py: append-only "answer checked" events.

ZTAX-AI-CONF-ADD01 §6: a later check result is appended as a linked immutable
event; the original invocation evidence record is never overwritten.
"""

from __future__ import annotations

import base64
import dataclasses
import json
import logging
from datetime import UTC, datetime, timedelta, timezone
from typing import Any

import pytest

from ztax_gateway.evidence_sink import InMemoryEvidenceSink
from ztax_gateway.governance import UseCase, UseCaseRegistry
from ztax_gateway.invocation_check_event import (
    AnswerCheckedEvent,
    CheckEventError,
    CheckEventSink,
    CheckReason,
    InMemoryCheckEventSink,
    LoggingCheckEventSink,
    Verdict,
)
from ztax_gateway.invocation_evidence import InvocationEvidenceRecord
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.runtime import Result, Route, RoutingTable
from ztax_gateway.server import Gateway
from ztax_gateway.wire import Call

_REGION = "eu-west-1"
_USE_CASE = "classification-review"
_NOW = datetime(2026, 11, 2, 9, 30, tzinfo=UTC)


class _Runtime:
    def run(self, route: Route, call: Call) -> Result:
        return Result(text="classified", confidence="0.9")


def _record() -> InvocationEvidenceRecord:
    """A real invocation record, produced by the Gateway into an in-memory sink."""
    reg = UseCaseRegistry()
    reg.register(
        UseCase(
            use_case_id=_USE_CASE,
            owner="team-ai",
            description="test use case",
            max_risk_tier=RiskTier.T2,
            max_authority=AuthorityOutcome.A2,
            permitted_regions=frozenset([_REGION]),
            permitted_data_classes=frozenset({"P0", "P1"}),
        )
    )
    routes = RoutingTable({_USE_CASE: Route("model:fake@1", "provider:fake", "prompt:fake@1")})
    sink = InMemoryEvidenceSink()
    gw = Gateway(reg, routes, _Runtime(), _REGION, "0.7.0", evidence_sink=sink)
    doc = {
        "kind": "SUGGESTION",
        "governance": {
            "tenant_id": "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f",
            "use_case": _USE_CASE,
            "authority_outcome": "A1",
            "risk_tier": "T1",
            "region": _REGION,
            "data_classes": ["P1"],
        },
        "subject_ref": "sku:PLAN-5G",
        "input_b64": base64.b64encode(b'{"sku":"PLAN-5G"}').decode("ascii"),
    }
    gw.invoke(json.dumps(doc).encode("utf-8"))
    return sink.last


def _event(evidence_id: str = "abc123", **overrides: Any) -> AnswerCheckedEvent:
    fields: dict[str, Any] = {
        "evidence_id": evidence_id,
        "verdict": Verdict.BLOCK,
        "reason_code": CheckReason.GUARD_NUMBER_NOT_IN_ROWS,
        "validator_version": "guard-1",
        "created_at": _NOW,
    }
    fields.update(overrides)
    return AnswerCheckedEvent(**fields)


# 1. A rejected answer produces an event with the right verdict and reason code.
def test_rejected_answer_event_carries_verdict_and_reason() -> None:
    event = _event(verdict=Verdict.ABSTAIN, reason_code=CheckReason.GUARD_STATUS_MISMATCH)
    assert event.verdict is Verdict.ABSTAIN
    assert event.reason_code is CheckReason.GUARD_STATUS_MISMATCH
    d = event.to_dict()
    assert d["verdict"] == "ABSTAIN"
    assert d["reason_code"] == "GUARD_STATUS_MISMATCH"
    assert d["event_type"] == "answer_checked"


def test_pass_event_uses_checks_passed() -> None:
    event = _event(verdict=Verdict.PASS, reason_code=CheckReason.CHECKS_PASSED)
    assert event.to_dict()["reason_code"] == "CHECKS_PASSED"


def test_optional_fields_default_and_serialise() -> None:
    d = _event().to_dict()
    assert d["source_record_ids"] == []
    assert "policy_version" not in d
    d2 = _event(source_record_ids=("row-1", "row-2"), policy_version="p-3").to_dict()
    assert d2["source_record_ids"] == ["row-1", "row-2"]
    assert d2["policy_version"] == "p-3"
    assert d2["created_at"] == "2026-11-02T09:30:00+00:00"


# 2. A second event for the same evidence_id never overwrites the first.
def test_second_event_is_appended_and_first_is_unchanged() -> None:
    sink = InMemoryCheckEventSink()
    first = _event(verdict=Verdict.BLOCK, reason_code=CheckReason.GUARD_NO_CITATION)
    snapshot = first.to_dict()
    sink.write_check_event(first)
    second = _event(
        verdict=Verdict.PASS,
        reason_code=CheckReason.CHECKS_PASSED,
        validator_version="guard-2",
        created_at=_NOW + timedelta(minutes=5),
    )
    sink.write_check_event(second)
    assert sink.events == (first, second)
    assert sink.events[0].to_dict() == snapshot
    assert sink.for_evidence("abc123") == (first, second)


def test_events_snapshot_cannot_mutate_the_log() -> None:
    sink = InMemoryCheckEventSink()
    sink.write_check_event(_event())
    assert isinstance(sink.events, tuple)
    assert len(sink.events) == 1


# 3. The original invocation record is unchanged.
def test_original_record_is_unchanged_by_events() -> None:
    record = _record()
    before = record.as_dict()
    sink = InMemoryCheckEventSink()
    for reason in (CheckReason.GUARD_NO_CITATION, CheckReason.GUARD_FOREIGN_CITATION):
        sink.write_check_event(_event(record.ai_invocation_id, reason_code=reason))
    assert record.as_dict() == before
    with pytest.raises(dataclasses.FrozenInstanceError):
        record.ai_invocation_id = "other"  # type: ignore[misc]


# 4. The event links to the correct evidence_id.
def test_event_links_to_the_records_invocation_id() -> None:
    a, b = _record(), _record()
    assert a.ai_invocation_id != b.ai_invocation_id
    sink = InMemoryCheckEventSink()
    sink.write_check_event(_event(a.ai_invocation_id))
    sink.write_check_event(
        _event(b.ai_invocation_id, verdict=Verdict.PASS, reason_code=CheckReason.CHECKS_PASSED)
    )
    assert [e.evidence_id for e in sink.for_evidence(a.ai_invocation_id)] == [a.ai_invocation_id]
    assert [e.evidence_id for e in sink.for_evidence(b.ai_invocation_id)] == [b.ai_invocation_id]
    assert sink.for_evidence("no-such-id") == ()


# 5. Invalid inputs are rejected.
@pytest.mark.parametrize(
    "overrides",
    [
        {"verdict": "MAYBE"},
        {"verdict": "PASS"},  # a bare string, not a Verdict
        {"reason_code": ""},
        {"reason_code": "SOMETHING_ELSE"},
        {"reason_code": "GUARD_NO_CITATION"},  # a bare string, not a CheckReason
        {"evidence_id": ""},
        {"evidence_id": "   "},
        {"validator_version": ""},
        {"validator_version": "  "},
        {"created_at": datetime(2026, 11, 2, 9, 30)},  # naive
        {"created_at": datetime(2026, 11, 2, 9, 30, tzinfo=timezone(timedelta(hours=2)))},
        {"created_at": "2026-11-02T09:30:00+00:00"},
        {"source_record_ids": ["row-1"]},  # list, not tuple
        {"source_record_ids": ("",)},
        {"policy_version": ""},
    ],
)
def test_invalid_events_are_rejected(overrides: dict[str, Any]) -> None:
    with pytest.raises(CheckEventError):
        _event(**overrides)


def test_verdict_and_reason_lists_are_fixed() -> None:
    assert {v.value for v in Verdict} == {"PASS", "REVIEW_REQUIRED", "ABSTAIN", "BLOCK"}
    assert {r.value for r in CheckReason} == {
        "CHECKS_PASSED",
        "GUARD_NO_CITATION",
        "GUARD_FOREIGN_CITATION",
        "GUARD_STATUS_MISMATCH",
        "GUARD_NUMBER_NOT_IN_ROWS",
        "MODEL_ABSTAINED",
        "GUARD_ONTOLOGY_UNKNOWN",
        "GUARD_ONTOLOGY_NOT_YET_EFFECTIVE",
        "GUARD_ONTOLOGY_EXPIRED",
        "GUARD_ONTOLOGY_NOT_CONFIGURED",
    }


# 6. The event is immutable.
def test_event_is_immutable() -> None:
    event = _event()
    with pytest.raises(dataclasses.FrozenInstanceError):
        event.verdict = Verdict.PASS  # type: ignore[misc]
    with pytest.raises(dataclasses.FrozenInstanceError):
        event.evidence_id = "other"  # type: ignore[misc]


# 7. Sinks: one line per event; in-memory keeps append order.
def test_sinks_satisfy_the_protocol() -> None:
    assert isinstance(LoggingCheckEventSink(), CheckEventSink)
    assert isinstance(InMemoryCheckEventSink(), CheckEventSink)


def test_logging_sink_writes_one_line_per_event(caplog: pytest.LogCaptureFixture) -> None:
    sink = LoggingCheckEventSink()
    first = _event(reason_code=CheckReason.GUARD_NO_CITATION)
    second = _event(verdict=Verdict.PASS, reason_code=CheckReason.CHECKS_PASSED)
    with caplog.at_level(logging.INFO, logger="ztax_gateway.evidence.check"):
        sink.write_check_event(first)
        sink.write_check_event(second)
    assert len(caplog.records) == 2
    payloads = [json.loads(r.message) for r in caplog.records]
    assert all("\n" not in r.message for r in caplog.records)
    assert payloads[0] == first.to_dict()
    assert payloads[1] == second.to_dict()
    assert caplog.records[0].ai_invocation_id == "abc123"  # type: ignore[attr-defined]


def test_in_memory_sink_returns_events_in_append_order() -> None:
    sink = InMemoryCheckEventSink()
    events = [
        _event(created_at=_NOW + timedelta(seconds=i), validator_version=f"guard-{i}")
        for i in range(3)
    ]
    for e in events:
        sink.write_check_event(e)
    assert list(sink.events) == events
