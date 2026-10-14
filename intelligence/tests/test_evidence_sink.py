"""Tests for evidence_sink.py and the Gateway's Ch.17 §21 wiring.

Ch.17 §21 of the ZoikoTax Master Specification:
  Every AI action crossing the governed boundary produces exactly one
  tamper-evident record before its output is consumed.

Test groups
-----------
EvidenceSink protocol      -- LoggingEvidenceSink and InMemoryEvidenceSink
                              satisfy the structural protocol.
InMemoryEvidenceSink       -- write, clear, last, IndexError on empty last.
LoggingEvidenceSink        -- happy path emits JSON to the named logger;
                              serialisation error logs and propagates (fail-closed).
Gateway wiring (success)   -- a successful invocation writes exactly one
                              PROPOSE/PRIORITIZE record with correct fields.
Gateway wiring (BLOCK)     -- a governance-refused invocation writes exactly
                              one BLOCK record carrying the refusal code.
Gateway wiring (residency) -- a residency mismatch writes a BLOCK record
                              and raises GatewayError.
Gateway wiring (runtime)   -- a runtime failure writes a BLOCK record
                              and raises GatewayError (error unchanged).
Fail-closed sink           -- a failing sink makes the call fail and no reply
                              is returned (proves record written before reply).
One record per call        -- two successive calls produce exactly two records
                              with distinct ai_invocation_id values.
"""

from __future__ import annotations

import base64
import hashlib
import json
import logging
from typing import Any
from unittest.mock import MagicMock

import grpc
import pytest

from ztax_gateway.evidence_sink import EvidenceSink, InMemoryEvidenceSink, LoggingEvidenceSink
from ztax_gateway.governance import Refusal, UseCase, UseCaseRegistry
from ztax_gateway.invocation_evidence import InvocationEvidenceRecord, InvocationOutcome
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.runtime import (
    Result,
    Route,
    RoutingTable,
    RuntimeUnavailableError,
)
from ztax_gateway.server import Gateway, GatewayError
from ztax_gateway.wire import Call

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_REGION = "eu-west-1"
_TRAIN = "0.7.0"
_USE_CASE = "classification-review"


def _make_registry(use_case_id: str = _USE_CASE) -> UseCaseRegistry:
    reg = UseCaseRegistry()
    reg.register(
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
    return reg


def _make_routes(use_case_id: str = _USE_CASE) -> RoutingTable:
    return RoutingTable(
        {
            use_case_id: Route(
                model_profile="model:fake@1",
                provider_profile="provider:fake",
                prompt_profile="prompt:fake@1",
            )
        }
    )


class _FakeRuntime:
    """Returns a fixed canned SUGGESTION result."""

    def run(self, route: Route, call: Call) -> Result:
        return Result(
            text="classified",
            payload="",
            fields=None,
            proposed_code="",
            confidence="0.9",
        )


def _encode_call(
    use_case: str = _USE_CASE,
    region: str = _REGION,
    authority: str = "A1",
    risk_tier: str = "T1",
    kind: str = "SUGGESTION",
    input_bytes: bytes = b'{"sku":"PLAN-5G"}',
) -> bytes:
    doc = {
        "kind": kind,
        "governance": {
            "tenant_id": "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f",
            "use_case": use_case,
            "authority_outcome": authority,
            "risk_tier": risk_tier,
            "region": region,
            "data_classes": ["P1"],
        },
        "subject_ref": "sku:PLAN-5G",
        "input_b64": base64.b64encode(input_bytes).decode("ascii"),
    }
    return json.dumps(doc).encode("utf-8")


def _make_gateway(
    *,
    use_case: str = _USE_CASE,
    runtime: Any = None,
    sink: EvidenceSink | None = None,
) -> tuple[Gateway, InMemoryEvidenceSink]:
    # Always wire an InMemoryEvidenceSink for assertion access in tests.
    # If the caller supplies a different sink (e.g. LoggingEvidenceSink),
    # pass it to Gateway directly; the returned InMemoryEvidenceSink is
    # used only for structural-protocol tests that do not check records.
    mem_sink = InMemoryEvidenceSink()
    gw = Gateway(
        registry=_make_registry(use_case),
        routes=_make_routes(use_case),
        runtime=runtime or _FakeRuntime(),
        region=_REGION,
        ai_train_version=_TRAIN,
        evidence_sink=sink if sink is not None else mem_sink,
    )
    return gw, mem_sink


# ---------------------------------------------------------------------------
# EvidenceSink protocol
# ---------------------------------------------------------------------------


def test_in_memory_sink_satisfies_protocol() -> None:
    assert isinstance(InMemoryEvidenceSink(), EvidenceSink)


def test_logging_sink_satisfies_protocol() -> None:
    assert isinstance(LoggingEvidenceSink(), EvidenceSink)


# ---------------------------------------------------------------------------
# InMemoryEvidenceSink
# ---------------------------------------------------------------------------


def test_in_memory_sink_write_accumulates() -> None:
    sink = InMemoryEvidenceSink()
    gw, _ = _make_gateway(sink=sink)
    gw.invoke(_encode_call())
    gw.invoke(_encode_call())
    assert len(sink.records) == 2


def test_in_memory_sink_clear_empties_records() -> None:
    sink = InMemoryEvidenceSink()
    gw, _ = _make_gateway(sink=sink)
    gw.invoke(_encode_call())
    sink.clear()
    assert sink.records == []


def test_in_memory_sink_last_returns_most_recent() -> None:
    sink = InMemoryEvidenceSink()
    gw, _ = _make_gateway(sink=sink)
    gw.invoke(_encode_call())
    gw.invoke(_encode_call())
    assert sink.last is sink.records[-1]


def test_in_memory_sink_last_raises_when_empty() -> None:
    sink = InMemoryEvidenceSink()
    with pytest.raises(IndexError):
        _ = sink.last


# ---------------------------------------------------------------------------
# LoggingEvidenceSink
# ---------------------------------------------------------------------------


def test_logging_sink_emits_json_line(caplog: pytest.LogCaptureFixture) -> None:
    gw, _ = _make_gateway(sink=LoggingEvidenceSink())
    with caplog.at_level(logging.INFO, logger="ztax_gateway.evidence"):
        gw.invoke(_encode_call())
    assert len(caplog.records) == 1
    payload = json.loads(caplog.records[0].message)
    assert payload["caller_security_context_ref"] == _USE_CASE
    assert payload["invocation_outcome"] == InvocationOutcome.PROPOSE


def test_logging_sink_propagates_on_bad_record(caplog: pytest.LogCaptureFixture) -> None:
    """If serialization raises, write() logs the error and propagates it (fail-closed)."""
    sink = LoggingEvidenceSink()
    bad_record = MagicMock(spec=InvocationEvidenceRecord)
    bad_record.ai_invocation_id = "test-id"
    bad_record.as_dict.side_effect = RuntimeError("boom")
    with (
        caplog.at_level(logging.ERROR, logger="ztax_gateway.evidence"),
        pytest.raises(RuntimeError, match="boom"),
    ):
        sink.write(bad_record)
    assert any("THIS IS AN AUDIT GAP" in r.message for r in caplog.records)


# ---------------------------------------------------------------------------
# Gateway wiring: success path
# ---------------------------------------------------------------------------


def test_successful_invocation_writes_exactly_one_record() -> None:
    gw, sink = _make_gateway()
    gw.invoke(_encode_call())
    assert len(sink.records) == 1


def test_successful_invocation_record_fields() -> None:
    input_bytes = b'{"sku":"PLAN-5G"}'
    gw, sink = _make_gateway()
    gw.invoke(_encode_call(input_bytes=input_bytes))
    rec = sink.last
    assert isinstance(rec, InvocationEvidenceRecord)
    assert rec.caller_security_context_ref == _USE_CASE
    assert rec.region == _REGION
    assert rec.ai_release_manifest_id == _TRAIN
    assert rec.invocation_outcome == InvocationOutcome.PROPOSE
    assert rec.authority_outcome == AuthorityOutcome.A1
    assert rec.input_hash == hashlib.sha256(input_bytes).hexdigest()
    assert rec.output_hash is not None  # non-empty reply was produced
    assert not rec.any_guardrail_failed()


def test_a2_invocation_produces_prioritize_outcome() -> None:
    """An A2 governance context maps to PRIORITIZE invocation outcome."""
    gw, sink = _make_gateway()
    gw.invoke(_encode_call(authority="A2", risk_tier="T2"))
    assert sink.last.invocation_outcome == InvocationOutcome.PRIORITIZE


def test_output_hash_matches_encoded_reply() -> None:
    """The output hash in the record must equal SHA-256 of the encoded reply."""
    gw, sink = _make_gateway()
    reply_bytes = gw.invoke(_encode_call())
    rec = sink.last
    assert rec.output_hash == hashlib.sha256(reply_bytes).hexdigest()


def test_guardrail_pass_is_in_record() -> None:
    gw, sink = _make_gateway()
    gw.invoke(_encode_call())
    guardrail_ids = {gr.guardrail_id for gr in sink.last.guardrail_results}
    assert "governance.authority" in guardrail_ids
    assert all(gr.passed for gr in sink.last.guardrail_results)


# ---------------------------------------------------------------------------
# Gateway wiring: BLOCK path (governance refusal)
# ---------------------------------------------------------------------------


def test_governance_refusal_writes_block_record() -> None:
    gw, sink = _make_gateway()
    with pytest.raises(GatewayError):
        # A5 is unconditionally refused by governance
        gw.invoke(_encode_call(authority="A5"))
    assert len(sink.records) == 1
    assert sink.last.invocation_outcome == InvocationOutcome.BLOCK


def test_block_record_has_no_output_hash() -> None:
    gw, sink = _make_gateway()
    with pytest.raises(GatewayError):
        gw.invoke(_encode_call(authority="A5"))
    assert sink.last.output_hash is None


def test_block_record_carries_refusal_guardrail() -> None:
    gw, sink = _make_gateway()
    with pytest.raises(GatewayError):
        gw.invoke(_encode_call(authority="A5"))
    rec = sink.last
    assert rec.any_guardrail_failed()
    refusal_verdicts = [gr.verdict for gr in rec.guardrail_results if not gr.passed]
    assert len(refusal_verdicts) >= 1


def test_block_record_has_input_hash() -> None:
    input_bytes = b'{"sku":"PLAN-5G"}'
    gw, sink = _make_gateway()
    with pytest.raises(GatewayError):
        gw.invoke(_encode_call(authority="A5", input_bytes=input_bytes))
    assert sink.last.input_hash == hashlib.sha256(input_bytes).hexdigest()


# ---------------------------------------------------------------------------
# Gateway wiring: residency mismatch -- writes BLOCK record
# ---------------------------------------------------------------------------


def test_wrong_region_writes_block_record() -> None:
    gw, sink = _make_gateway()
    with pytest.raises(GatewayError) as exc_info:
        gw.invoke(_encode_call(region="us-east-1"))
    assert exc_info.value.reason == Refusal.RESIDENCY_REFUSED.value
    assert len(sink.records) == 1
    rec = sink.last
    assert rec.invocation_outcome == InvocationOutcome.BLOCK
    assert rec.output_hash is None
    assert rec.any_guardrail_failed()
    assert any(gr.verdict == Refusal.RESIDENCY_REFUSED.value for gr in rec.guardrail_results)


# ---------------------------------------------------------------------------
# Gateway wiring: runtime failure -- writes BLOCK record, caller error unchanged
# ---------------------------------------------------------------------------


def test_runtime_failure_writes_block_record() -> None:
    """A runtime failure writes exactly one BLOCK record; caller error is unchanged."""

    class _FailingRuntime:
        def run(self, route: Route, call: Call) -> Result:
            raise RuntimeUnavailableError("provider timed out")

    gw, sink = _make_gateway(runtime=_FailingRuntime())
    with pytest.raises(GatewayError) as exc_info:
        gw.invoke(_encode_call())
    assert exc_info.value.code == grpc.StatusCode.UNAVAILABLE
    assert exc_info.value.reason == "AI_GATEWAY_UNAVAILABLE"
    assert len(sink.records) == 1
    rec = sink.last
    assert rec.invocation_outcome == InvocationOutcome.BLOCK
    assert rec.output_hash is None
    assert rec.any_guardrail_failed()
    assert any(gr.verdict == "AI_GATEWAY_UNAVAILABLE" for gr in rec.guardrail_results)


# ---------------------------------------------------------------------------
# Fail closed: failing sink makes call fail and no reply is returned
# ---------------------------------------------------------------------------


def test_failing_sink_makes_call_fail_and_no_reply_returned() -> None:
    """If the sink write raises, the call fails closed and no reply is returned.

    This proves that the evidence record is written before the reply bytes
    are returned to the caller.
    """
    failing_sink = MagicMock(spec=EvidenceSink)
    failing_sink.write.side_effect = OSError("disk full")
    gw, _ = _make_gateway(sink=failing_sink)
    with pytest.raises(GatewayError) as exc_info:
        gw.invoke(_encode_call())
    assert exc_info.value.code == grpc.StatusCode.UNAVAILABLE
    assert exc_info.value.reason == "AI_GATEWAY_UNAVAILABLE"
# ---------------------------------------------------------------------------
# Malformed request writes no record
# ---------------------------------------------------------------------------


def test_malformed_request_writes_no_record() -> None:
    """A malformed request cannot build provenance; writes no evidence record."""
    gw, sink = _make_gateway()
    with pytest.raises(GatewayError) as exc_info:
        gw.invoke(b"not json at all")
    assert exc_info.value.code == grpc.StatusCode.INVALID_ARGUMENT
    assert exc_info.value.reason == Refusal.MALFORMED_CONTEXT.value
    assert len(sink.records) == 0


# ---------------------------------------------------------------------------
# Serialized record never contains raw request payload text
# ---------------------------------------------------------------------------


def test_serialized_record_never_contains_raw_request_payload_text() -> None:
    """The serialized record contains only the input hash, never the raw text."""
    unique_marker = "UNIQUE_PAYLOAD_MARKER_STRING_XYZ_987654321"
    input_bytes = f'{{"sku": "PLAN-5G", "confidential": "{unique_marker}"}}'.encode()
    gw, sink = _make_gateway()
    gw.invoke(_encode_call(input_bytes=input_bytes))
    rec = sink.last
    serialized = json.dumps(rec.as_dict())
    assert unique_marker not in serialized
    assert rec.input_hash == hashlib.sha256(input_bytes).hexdigest()


# ---------------------------------------------------------------------------
# One record per call / distinct invocation IDs
# ---------------------------------------------------------------------------


def test_two_calls_produce_two_records() -> None:
    gw, sink = _make_gateway()
    gw.invoke(_encode_call())
    gw.invoke(_encode_call())
    assert len(sink.records) == 2
    # Each record has a unique ai_invocation_id
    ids = {r.ai_invocation_id for r in sink.records}
    assert len(ids) == 2


def test_two_calls_give_two_distinct_invocation_ids() -> None:
    """Two calls give two distinct ai_invocation_id values."""
    gw, sink = _make_gateway()
    gw.invoke(_encode_call())
    gw.invoke(_encode_call())
    assert len(sink.records) == 2
    id1, id2 = sink.records[0].ai_invocation_id, sink.records[1].ai_invocation_id
    assert id1 != id2


# ---------------------------------------------------------------------------
# Record is frozen (immutability assertion)
# ---------------------------------------------------------------------------


def test_evidence_record_is_immutable() -> None:
    from dataclasses import FrozenInstanceError

    gw, sink = _make_gateway()
    gw.invoke(_encode_call())
    rec = sink.last
    with pytest.raises(FrozenInstanceError):
        rec.invocation_outcome = InvocationOutcome.BLOCK  # type: ignore[misc]


# ---------------------------------------------------------------------------
# as_dict round-trip
# ---------------------------------------------------------------------------


def test_as_dict_is_json_serialisable() -> None:
    gw, sink = _make_gateway()
    gw.invoke(_encode_call())
    d = sink.last.as_dict()
    serialized = json.dumps(d)
    parsed = json.loads(serialized)
    assert parsed["caller_security_context_ref"] == _USE_CASE
    assert parsed["invocation_outcome"] == InvocationOutcome.PROPOSE


# ---------------------------------------------------------------------------
# Gap 1: RuntimeNotConfiguredError writes exactly one BLOCK record
# ---------------------------------------------------------------------------


def test_unconfigured_runtime_writes_block_record() -> None:
    """Gap 1: RuntimeNotConfiguredError writes one BLOCK record; caller gets
    UNIMPLEMENTED / AI_GATEWAY_NOT_CONFIGURED (unchanged)."""
    from ztax_gateway.runtime import UnconfiguredRuntime

    gw, sink = _make_gateway(runtime=UnconfiguredRuntime())
    with pytest.raises(GatewayError) as exc_info:
        gw.invoke(_encode_call())
    assert exc_info.value.code == grpc.StatusCode.UNIMPLEMENTED
    assert exc_info.value.reason == "AI_GATEWAY_NOT_CONFIGURED"
    assert len(sink.records) == 1
    rec = sink.last
    assert rec.invocation_outcome == InvocationOutcome.BLOCK
    assert rec.output_hash is None
    assert any(gr.verdict == "AI_GATEWAY_NOT_CONFIGURED" for gr in rec.guardrail_results)


# ---------------------------------------------------------------------------
# Gap 2: UnroutedUseCaseError writes exactly one BLOCK record
# ---------------------------------------------------------------------------


def test_unrouted_use_case_writes_block_record() -> None:
    """Gap 2: a use case registered in the registry but absent from the routing
    table writes exactly one BLOCK record; the caller error is unchanged
    (UNIMPLEMENTED / AI_GATEWAY_NOT_CONFIGURED)."""
    from ztax_gateway.runtime import RoutingTable

    # Register the use case but give it an *empty* routing table so
    # RoutingTable.route() raises UnroutedUseCaseError inside work().
    reg = _make_registry(_USE_CASE)
    empty_routes = RoutingTable({})  # no routes at all
    mem_sink = InMemoryEvidenceSink()
    gw = Gateway(
        registry=reg,
        routes=empty_routes,
        runtime=_FakeRuntime(),
        region=_REGION,
        ai_train_version=_TRAIN,
        evidence_sink=mem_sink,
    )
    with pytest.raises(GatewayError) as exc_info:
        gw.invoke(_encode_call())
    assert exc_info.value.code == grpc.StatusCode.UNIMPLEMENTED
    assert exc_info.value.reason == "AI_GATEWAY_NOT_CONFIGURED"
    assert len(mem_sink.records) == 1
    rec = mem_sink.last
    assert rec.invocation_outcome == InvocationOutcome.BLOCK
    assert rec.output_hash is None
    assert any(gr.verdict == "AI_GATEWAY_NOT_CONFIGURED" for gr in rec.guardrail_results)


# ---------------------------------------------------------------------------
# Gap 3: check_result failure routes through RuntimeUnavailableError path
# ---------------------------------------------------------------------------


def test_check_result_failure_writes_block_record() -> None:
    """Gap 3: a runtime that returns an empty SUGGESTION (failing check_result)
    raises RuntimeUnavailableError which is caught by the existing runtime-failure
    handler: one BLOCK record, UNAVAILABLE / AI_GATEWAY_UNAVAILABLE."""

    class _EmptyTextRuntime:
        """Returns a SUGGESTION with no text, which check_result rejects."""

        def run(self, route: Route, call: Call) -> Result:
            return Result(text="", payload="", fields=None, proposed_code="", confidence="")

    gw, sink = _make_gateway(runtime=_EmptyTextRuntime())
    with pytest.raises(GatewayError) as exc_info:
        gw.invoke(_encode_call(kind="SUGGESTION"))
    assert exc_info.value.code == grpc.StatusCode.UNAVAILABLE
    assert exc_info.value.reason == "AI_GATEWAY_UNAVAILABLE"
    assert len(sink.records) == 1
    rec = sink.last
    assert rec.invocation_outcome == InvocationOutcome.BLOCK
    assert rec.output_hash is None
    assert any(gr.verdict == "AI_GATEWAY_UNAVAILABLE" for gr in rec.guardrail_results)


# ---------------------------------------------------------------------------
# Gap 4: failing sink on a BLOCK call preserves the original caller error;
#         failing sink on a PERMIT call returns UNAVAILABLE
# ---------------------------------------------------------------------------


def test_failing_sink_on_a5_refusal_returns_authority_refused() -> None:
    """Gap 4a: a sink failure while recording an A5 (authority refused) BLOCK
    must NOT change the caller's error to UNAVAILABLE; it stays
    PERMISSION_DENIED / AI_AUTHORITY_REFUSED."""
    failing_sink = MagicMock(spec=EvidenceSink)
    failing_sink.write.side_effect = OSError("disk full")
    gw, _ = _make_gateway(sink=failing_sink)
    with pytest.raises(GatewayError) as exc_info:
        gw.invoke(_encode_call(authority="A5"))
    assert exc_info.value.code == grpc.StatusCode.PERMISSION_DENIED
    assert exc_info.value.reason == "AI_AUTHORITY_REFUSED"


def test_failing_sink_on_permitted_call_returns_unavailable() -> None:
    """Gap 4b: a sink failure while recording a PERMIT (success) record must
    fail closed: the caller gets UNAVAILABLE / AI_GATEWAY_UNAVAILABLE and no
    reply bytes are returned."""
    failing_sink = MagicMock(spec=EvidenceSink)
    failing_sink.write.side_effect = OSError("disk full")
    gw, _ = _make_gateway(sink=failing_sink)
    with pytest.raises(GatewayError) as exc_info:
        gw.invoke(_encode_call())  # governance passes, runtime succeeds
    assert exc_info.value.code == grpc.StatusCode.UNAVAILABLE
    assert exc_info.value.reason == "AI_GATEWAY_UNAVAILABLE"
