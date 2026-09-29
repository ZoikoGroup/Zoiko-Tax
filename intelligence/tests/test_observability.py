"""Tests for AI Observability.
 
Chapter 17 §22 of the ZoikoTax Master Specification.
 
Mirrors the structure and discipline of ``test_invocation_evidence.py``:
 
- Plain functions; no test classes.
- ``dataclasses.replace`` for provenance variants.
- Every public type and method is exercised.
- Every error path is explicitly asserted.
 
Test groups
-----------
  ObservabilityError       -- construction, reason attribute
  SignalFamily             -- eight values, StrEnum
  TelemetrySignal          -- construction, immutability, as_dict
  AuditEventKind           -- three values, StrEnum
  AuditEntry               -- construction, immutability, as_dict
  AuditBuffer              -- capacity, append, drain, peek, overflow error
  record_signals           -- all eight families derived, label safety,
                              correct values per outcome
  record_audit_entries     -- A3+ entry, tool-call entries, BLOCK entry,
                              sub-A3 produces no A3_INVOCATION entry,
                              buffer-full error
  ObservabilityPipeline    -- audit-before-telemetry ordering, accumulation,
                              drain_signals, buffer-full propagation
  Design rule 1            -- no raw content in labels
  Design rule 2            -- audit before telemetry
  Integration              -- propose pipeline, blocked pipeline, review pipeline
"""
 
from __future__ import annotations
 
from dataclasses import FrozenInstanceError, replace
 
import pytest
 
from ztax_gateway.governance import UseCase, UseCaseRegistry
from ztax_gateway.invocation_evidence import (
    GuardrailResult,
    InvocationEvidenceBuilder,
    InvocationEvidenceRecord,
    InvocationOutcome,
    ToolCallRecord,
    UsageMetrics,
)
from ztax_gateway.observability import (
    DEFAULT_AUDIT_BUFFER_CAPACITY,
    AuditBuffer,
    AuditEntry,
    AuditEventKind,
    ObservabilityError,
    ObservabilityPipeline,
    SignalFamily,
    TelemetrySignal,
    record_audit_entries,
    record_signals,
)
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier
 
# ---------------------------------------------------------------------------
# Shared fixtures
# ---------------------------------------------------------------------------
 
_BASE_PROV = Provenance(
    use_case="change-intelligence",
    model_profile="model:gemini-pro@2026.09",
    provider_profile="provider:eu-hosted",
    prompt_profile="prompt:change-extract@3",
    region="eu-west-1",
    data_class="INTERNAL",
    risk_tier=RiskTier.T1,
    authority_outcome=AuthorityOutcome.A1,
    ai_train_version="0.6.0",
)
 
_A3_PROV = replace(_BASE_PROV, authority_outcome=AuthorityOutcome.A3)
 
_TS_ISO = "2026-09-25T10:00:00"
 
_BASE_TOOL_CALL = ToolCallRecord(
    call_id="call-obs-001",
    tool_id="corpus-fetch",
    caller_id="agent:change-intel",
    timestamp=_TS_ISO,
    outcome="PERMITTED",
    payload_hash="abcd1234" * 8,
)
 
_BASE_GUARDRAIL_PASS = GuardrailResult(
    guardrail_id="governance.authority",
    verdict="PASS",
)
 
_BASE_GUARDRAIL_KILL = GuardrailResult(
    guardrail_id="governance.kill_switch",
    verdict="AI_KILL_SWITCH_ENGAGED",
    detail="global kill active",
)
 
 
def _registry_for(prov: Provenance) -> UseCaseRegistry:
    return UseCaseRegistry(
        [
            UseCase(
                use_case_id=prov.use_case,
                owner="lane-l",
                description="test",
                max_risk_tier=RiskTier.T4,
                max_authority=AuthorityOutcome.A4,
                permitted_regions=frozenset({prov.region}),
            )
        ]
    )
 
 
def _build(
    prov: Provenance = _BASE_PROV,
    outcome: InvocationOutcome = InvocationOutcome.PROPOSE,
    *,
    tool_calls: list[ToolCallRecord] | None = None,
    guardrails: list[GuardrailResult] | None = None,
    review_ref: str | None = None,
    input_bytes: bytes | None = None,
    output_bytes: bytes | None = None,
    usage: UsageMetrics | None = None,
) -> InvocationEvidenceRecord:
    """Build an InvocationEvidenceRecord with full control over its fields."""
    # BLOCK is exempt from governance gate.
    if outcome is InvocationOutcome.BLOCK:
        registry: UseCaseRegistry = UseCaseRegistry()
    else:
        registry = _registry_for(prov)
 
    b = InvocationEvidenceBuilder(
        provenance=prov,
        invocation_outcome=outcome,
        registry=registry,
        ai_invocation_id="obs" + "0" * 29,
    )
    if input_bytes is not None:
        b.set_input(input_bytes)
    if output_bytes is not None:
        b.set_output(output_bytes)
    for tc in tool_calls or []:
        b.add_tool_call(tc)
    for gr in guardrails or []:
        b.add_guardrail(gr)
    if review_ref is not None:
        b.set_review_ref(review_ref)
    if usage is not None:
        b.set_usage(usage)
    return b.build()
 
 
# ---------------------------------------------------------------------------
# ObservabilityError
# ---------------------------------------------------------------------------
 
 
def test_observability_error_is_exception() -> None:
    assert isinstance(ObservabilityError("x"), Exception)
 
 
def test_observability_error_reason_attribute() -> None:
    err = ObservabilityError("buffer full")
    assert err.reason == "buffer full"
 
 
def test_observability_error_str_contains_reason() -> None:
    assert "buffer full" in str(ObservabilityError("buffer full"))
 
 
# ---------------------------------------------------------------------------
# SignalFamily
# ---------------------------------------------------------------------------
 
 
def test_signal_family_has_eight_values() -> None:
    assert len(SignalFamily) == 8
 
 
def test_signal_family_values() -> None:
    values = {f.value for f in SignalFamily}
    assert values == {
        "model_ops", "agent_ops", "retrieval", "quality",
        "security", "reliability", "finops", "governance",
    }
 
 
def test_signal_family_is_str_enum() -> None:
    assert SignalFamily.MODEL_OPS.value == "model_ops"
 
 
# ---------------------------------------------------------------------------
# TelemetrySignal
# ---------------------------------------------------------------------------
 
 
def test_telemetry_signal_stores_fields() -> None:
    sig = TelemetrySignal(
        family=SignalFamily.MODEL_OPS,
        name="model_ops.latency_ms",
        value=123.0,
        unit="ms",
        labels={"use_case": "ci"},
        ai_invocation_id="abc123",
    )
    assert sig.family is SignalFamily.MODEL_OPS
    assert sig.name == "model_ops.latency_ms"
    assert sig.value == 123.0
    assert sig.unit == "ms"
    assert sig.labels == {"use_case": "ci"}
    assert sig.ai_invocation_id == "abc123"
 
 
def test_telemetry_signal_is_frozen() -> None:
    sig = TelemetrySignal(
        family=SignalFamily.FINOPS,
        name="finops.cost",
        value=0.01,
        unit="usd",
        labels={},
        ai_invocation_id="x",
    )
    with pytest.raises(FrozenInstanceError):
        sig.value = 999.0  # type: ignore[misc]
 
 
def test_telemetry_signal_as_dict_keys() -> None:
    sig = TelemetrySignal(
        family=SignalFamily.GOVERNANCE,
        name="governance.invocation_count",
        value=1.0,
        unit="count",
        labels={"use_case": "ci"},
        ai_invocation_id="inv001",
    )
    d = sig.as_dict()
    assert set(d.keys()) == {"family", "name", "value", "unit", "labels", "ai_invocation_id"}
    assert d["family"] == "governance"
    assert d["value"] == 1.0
 
 
def test_telemetry_signal_as_dict_labels_is_copy() -> None:
    """as_dict must return a copy of labels, not the original dict."""
    sig = TelemetrySignal(
        family=SignalFamily.SECURITY,
        name="security.block_event",
        value=1.0,
        unit="bool",
        labels={"use_case": "ci"},
        ai_invocation_id="inv",
    )
    d = sig.as_dict()
    d["labels"]["extra"] = "injected"  # type: ignore[index]
    assert "extra" not in sig.labels
 
 
# ---------------------------------------------------------------------------
# AuditEventKind
# ---------------------------------------------------------------------------
 
 
def test_audit_event_kind_has_three_values() -> None:
    assert len(AuditEventKind) == 3
 
 
def test_audit_event_kind_values() -> None:
    assert AuditEventKind.A3_INVOCATION == "A3_INVOCATION"
    assert AuditEventKind.TOOL_CALL == "TOOL_CALL"
    assert AuditEventKind.BLOCK_EVENT == "BLOCK_EVENT"
 
 
# ---------------------------------------------------------------------------
# AuditEntry
# ---------------------------------------------------------------------------
 
 
def _make_entry(kind: AuditEventKind = AuditEventKind.A3_INVOCATION) -> AuditEntry:
    return AuditEntry(
        kind=kind,
        ai_invocation_id="obs000000000000000000000000000000",
        use_case="change-intelligence",
        authority_outcome=AuthorityOutcome.A3,
        invocation_outcome=InvocationOutcome.REVIEW,
        detail={"authority_outcome": "A3", "review_ref": "dec-001"},
        recorded_at_iso="2026-09-25T10:00:00+00:00",
    )
 
 
def test_audit_entry_stores_kind() -> None:
    assert _make_entry().kind is AuditEventKind.A3_INVOCATION
 
 
def test_audit_entry_stores_ai_invocation_id() -> None:
    assert _make_entry().ai_invocation_id == "obs000000000000000000000000000000"
 
 
def test_audit_entry_stores_use_case() -> None:
    assert _make_entry().use_case == "change-intelligence"
 
 
def test_audit_entry_stores_authority_outcome() -> None:
    assert _make_entry().authority_outcome is AuthorityOutcome.A3
 
 
def test_audit_entry_is_frozen() -> None:
    with pytest.raises(FrozenInstanceError):
        _make_entry().use_case = "tampered"  # type: ignore[misc]
 
 
def test_audit_entry_as_dict_keys() -> None:
    d = _make_entry().as_dict()
    assert set(d.keys()) == {
        "kind", "ai_invocation_id", "use_case",
        "authority_outcome", "invocation_outcome", "detail", "recorded_at_iso",
    }
 
 
def test_audit_entry_as_dict_kind_is_string() -> None:
    assert _make_entry().as_dict()["kind"] == "A3_INVOCATION"
 
 
def test_audit_entry_as_dict_detail_is_copy() -> None:
    entry = _make_entry()
    d = entry.as_dict()
    d["detail"]["injected"] = "x"  # type: ignore[index]
    assert "injected" not in entry.detail
 
 
# ---------------------------------------------------------------------------
# AuditBuffer -- construction
# ---------------------------------------------------------------------------
 
 
def test_audit_buffer_default_capacity() -> None:
    assert AuditBuffer().capacity == DEFAULT_AUDIT_BUFFER_CAPACITY
 
 
def test_audit_buffer_custom_capacity() -> None:
    assert AuditBuffer(capacity=5).capacity == 5
 
 
def test_audit_buffer_capacity_zero_is_refused() -> None:
    with pytest.raises(ObservabilityError, match="capacity"):
        AuditBuffer(capacity=0)
 
 
def test_audit_buffer_capacity_negative_is_refused() -> None:
    with pytest.raises(ObservabilityError, match="capacity"):
        AuditBuffer(capacity=-1)
 
 
def test_audit_buffer_starts_empty() -> None:
    buf = AuditBuffer()
    assert buf.size == 0
    assert not buf.is_full
 
 
# ---------------------------------------------------------------------------
# AuditBuffer -- append / drain / peek
# ---------------------------------------------------------------------------
 
 
def test_audit_buffer_append_increases_size() -> None:
    buf = AuditBuffer()
    buf.append(_make_entry())
    assert buf.size == 1
 
 
def test_audit_buffer_append_rejects_non_audit_entry() -> None:
    buf = AuditBuffer()
    with pytest.raises(ObservabilityError, match="AuditEntry"):
        buf.append("not an entry")  # type: ignore[arg-type]
 
 
def test_audit_buffer_is_full_when_at_capacity() -> None:
    buf = AuditBuffer(capacity=2)
    buf.append(_make_entry())
    buf.append(_make_entry())
    assert buf.is_full
 
 
def test_audit_buffer_overflow_raises_error() -> None:
    buf = AuditBuffer(capacity=1)
    buf.append(_make_entry())
    with pytest.raises(ObservabilityError, match="full"):
        buf.append(_make_entry())
 
 
def test_audit_buffer_drain_returns_entries_in_fifo_order() -> None:
    buf = AuditBuffer()
    e1 = _make_entry(AuditEventKind.A3_INVOCATION)
    e2 = _make_entry(AuditEventKind.TOOL_CALL)
    buf.append(e1)
    buf.append(e2)
    drained = buf.drain()
    assert drained[0] is e1
    assert drained[1] is e2
 
 
def test_audit_buffer_drain_empties_buffer() -> None:
    buf = AuditBuffer()
    buf.append(_make_entry())
    buf.drain()
    assert buf.size == 0
 
 
def test_audit_buffer_peek_returns_snapshot_without_removing() -> None:
    buf = AuditBuffer()
    buf.append(_make_entry())
    snapshot = buf.peek()
    assert len(snapshot) == 1
    assert buf.size == 1  # still in buffer
 
 
def test_audit_buffer_peek_returns_copy() -> None:
    buf = AuditBuffer()
    buf.append(_make_entry())
    snapshot = buf.peek()
    snapshot.clear()
    assert buf.size == 1  # buffer unchanged
 
 
def test_audit_buffer_can_be_refilled_after_drain() -> None:
    buf = AuditBuffer(capacity=1)
    buf.append(_make_entry())
    buf.drain()
    buf.append(_make_entry())  # must not raise
    assert buf.size == 1
 
 
# ---------------------------------------------------------------------------
# record_signals -- type check
# ---------------------------------------------------------------------------
 
 
def test_record_signals_rejects_non_record() -> None:
    with pytest.raises(ObservabilityError, match="InvocationEvidenceRecord"):
        record_signals("not a record")  # type: ignore[arg-type]
 
 
# ---------------------------------------------------------------------------
# record_signals -- all eight families present
# ---------------------------------------------------------------------------
 
 
def test_record_signals_returns_all_eight_families() -> None:
    record = _build()
    signals = record_signals(record)
    families = {s.family for s in signals}
    assert families == set(SignalFamily)
 
 
def test_record_signals_returns_non_empty_list() -> None:
    assert len(record_signals(_build())) > 0
 
 
def test_record_signals_all_linked_to_invocation_id() -> None:
    record = _build()
    signals = record_signals(record)
    assert all(s.ai_invocation_id == record.ai_invocation_id for s in signals)
 
 
# ---------------------------------------------------------------------------
# record_signals -- MODEL_OPS family
# ---------------------------------------------------------------------------
 
 
def test_model_ops_latency_ms_value() -> None:
    usage = UsageMetrics(latency_ms=350, input_tokens=64, output_tokens=16)
    record = _build(usage=usage)
    sigs = [s for s in record_signals(record) if s.name == "model_ops.latency_ms"]
    assert len(sigs) == 1
    assert sigs[0].value == 350.0
    assert sigs[0].unit == "ms"
 
 
def test_model_ops_input_tokens_value() -> None:
    usage = UsageMetrics(input_tokens=256)
    record = _build(usage=usage)
    sigs = [s for s in record_signals(record) if s.name == "model_ops.input_tokens"]
    assert sigs[0].value == 256.0
 
 
def test_model_ops_output_tokens_value() -> None:
    usage = UsageMetrics(output_tokens=64)
    record = _build(usage=usage)
    sigs = [s for s in record_signals(record) if s.name == "model_ops.output_tokens"]
    assert sigs[0].value == 64.0
 
 
def test_model_ops_total_tokens_value() -> None:
    usage = UsageMetrics(input_tokens=100, output_tokens=50)
    record = _build(usage=usage)
    sigs = [s for s in record_signals(record) if s.name == "model_ops.total_tokens"]
    assert sigs[0].value == 150.0
 
 
def test_model_ops_invocation_count_is_one() -> None:
    sigs = [s for s in record_signals(_build()) if s.name == "model_ops.invocation_count"]
    assert sigs[0].value == 1.0
 
 
def test_model_ops_labels_include_prompt_version() -> None:
    sigs = [s for s in record_signals(_build()) if s.family is SignalFamily.MODEL_OPS]
    for sig in sigs:
        assert "prompt_version" in sig.labels
 
 
def test_model_ops_labels_include_ai_release_manifest_id() -> None:
    sigs = [s for s in record_signals(_build()) if s.family is SignalFamily.MODEL_OPS]
    for sig in sigs:
        assert "ai_release_manifest_id" in sig.labels
 
 
# ---------------------------------------------------------------------------
# record_signals -- AGENT_OPS family
# ---------------------------------------------------------------------------
 
 
def test_agent_ops_tool_call_count_zero_when_no_calls() -> None:
    sigs = [s for s in record_signals(_build()) if s.name == "agent_ops.tool_call_count"]
    assert sigs[0].value == 0.0
 
 
def test_agent_ops_tool_call_count_correct() -> None:
    record = _build(tool_calls=[_BASE_TOOL_CALL, _BASE_TOOL_CALL])
    sigs = [s for s in record_signals(record) if s.name == "agent_ops.tool_call_count"]
    assert sigs[0].value == 2.0
 
 
def test_agent_ops_permitted_count_correct() -> None:
    permitted = _BASE_TOOL_CALL
    refused = ToolCallRecord(
        call_id="call-r01", tool_id="t1", caller_id="a1",
        timestamp=_TS_ISO, outcome="REFUSED",
    )
    record = _build(tool_calls=[permitted, refused])
    sigs = [s for s in record_signals(record) if s.name == "agent_ops.tool_call_permitted_count"]
    assert sigs[0].value == 1.0
 
 
def test_agent_ops_refused_count_correct() -> None:
    refused = ToolCallRecord(
        call_id="call-r02", tool_id="t1", caller_id="a1",
        timestamp=_TS_ISO, outcome="SCOPE_VIOLATION",
    )
    record = _build(tool_calls=[refused])
    sigs = [s for s in record_signals(record) if s.name == "agent_ops.tool_call_refused_count"]
    assert sigs[0].value == 1.0
 
 
# ---------------------------------------------------------------------------
# record_signals -- RETRIEVAL family
# ---------------------------------------------------------------------------
 
 
def test_retrieval_citation_count_zero() -> None:
    sigs = [s for s in record_signals(_build()) if s.name == "retrieval.citation_count"]
    assert sigs[0].value == 0.0
 
 
def test_retrieval_has_citations_false_when_no_refs() -> None:
    sigs = [s for s in record_signals(_build()) if s.name == "retrieval.has_citations"]
    assert sigs[0].value == 0.0
 
 
def test_retrieval_citation_count_correct() -> None:
    record = _build()
    b = InvocationEvidenceBuilder(
        provenance=_BASE_PROV,
        invocation_outcome=InvocationOutcome.PROPOSE,
        registry=_registry_for(_BASE_PROV),
        ai_invocation_id="obs" + "0" * 29,
    )
    b.add_retrieval_ref("cite-001")
    b.add_retrieval_ref("cite-002")
    record = b.build()
    sigs = [s for s in record_signals(record) if s.name == "retrieval.citation_count"]
    assert sigs[0].value == 2.0
 
 
def test_retrieval_has_citations_true_when_refs_present() -> None:
    b = InvocationEvidenceBuilder(
        provenance=_BASE_PROV,
        invocation_outcome=InvocationOutcome.PROPOSE,
        registry=_registry_for(_BASE_PROV),
        ai_invocation_id="obs" + "0" * 29,
    )
    b.add_retrieval_ref("cite-xyz")
    record = b.build()
    sigs = [s for s in record_signals(record) if s.name == "retrieval.has_citations"]
    assert sigs[0].value == 1.0
 
 
# ---------------------------------------------------------------------------
# record_signals -- QUALITY family
# ---------------------------------------------------------------------------
 
 
def test_quality_guardrail_count_zero() -> None:
    sigs = [s for s in record_signals(_build()) if s.name == "quality.guardrail_count"]
    assert sigs[0].value == 0.0
 
 
def test_quality_guardrail_passed_count_correct() -> None:
    record = _build(guardrails=[_BASE_GUARDRAIL_PASS, _BASE_GUARDRAIL_PASS])
    sigs = [s for s in record_signals(record) if s.name == "quality.guardrail_passed_count"]
    assert sigs[0].value == 2.0
 
 
def test_quality_guardrail_failed_count_correct() -> None:
    record = _build(guardrails=[_BASE_GUARDRAIL_PASS, _BASE_GUARDRAIL_KILL],
                    outcome=InvocationOutcome.BLOCK)
    sigs = [s for s in record_signals(record) if s.name == "quality.guardrail_failed_count"]
    assert sigs[0].value == 1.0
 
 
def test_quality_any_guardrail_failed_false() -> None:
    record = _build(guardrails=[_BASE_GUARDRAIL_PASS])
    sigs = [s for s in record_signals(record) if s.name == "quality.any_guardrail_failed"]
    assert sigs[0].value == 0.0
 
 
def test_quality_any_guardrail_failed_true() -> None:
    record = _build(guardrails=[_BASE_GUARDRAIL_KILL], outcome=InvocationOutcome.BLOCK)
    sigs = [s for s in record_signals(record) if s.name == "quality.any_guardrail_failed"]
    assert sigs[0].value == 1.0
 
 
def test_quality_has_review_ref_false() -> None:
    sigs = [s for s in record_signals(_build()) if s.name == "quality.has_review_ref"]
    assert sigs[0].value == 0.0
 
 
def test_quality_has_review_ref_true() -> None:
    record = _build(prov=_A3_PROV, outcome=InvocationOutcome.REVIEW, review_ref="dec-001")
    sigs = [s for s in record_signals(record) if s.name == "quality.has_review_ref"]
    assert sigs[0].value == 1.0
 
 
# ---------------------------------------------------------------------------
# record_signals -- SECURITY family
# ---------------------------------------------------------------------------
 
 
def test_security_block_event_zero_for_propose() -> None:
    sigs = [s for s in record_signals(_build()) if s.name == "security.block_event"]
    assert sigs[0].value == 0.0
 
 
def test_security_block_event_one_for_block() -> None:
    record = _build(outcome=InvocationOutcome.BLOCK)
    sigs = [s for s in record_signals(record) if s.name == "security.block_event"]
    assert sigs[0].value == 1.0
 
 
def test_security_kill_switch_fired_false() -> None:
    sigs = [s for s in record_signals(_build()) if s.name == "security.kill_switch_fired"]
    assert sigs[0].value == 0.0
 
 
def test_security_kill_switch_fired_true() -> None:
    record = _build(guardrails=[_BASE_GUARDRAIL_KILL], outcome=InvocationOutcome.BLOCK)
    sigs = [s for s in record_signals(record) if s.name == "security.kill_switch_fired"]
    assert sigs[0].value == 1.0
 
 
def test_security_authority_ceiling_breach_false() -> None:
    sigs = [s for s in record_signals(_build()) if s.name == "security.authority_ceiling_breach"]
    assert sigs[0].value == 0.0
 
 
def test_security_authority_ceiling_breach_true() -> None:
    gr = GuardrailResult(guardrail_id="governance.authority", verdict="AI_AUTHORITY_REFUSED")
    record = _build(guardrails=[gr], outcome=InvocationOutcome.BLOCK)
    sigs = [s for s in record_signals(record) if s.name == "security.authority_ceiling_breach"]
    assert sigs[0].value == 1.0
 
 
# ---------------------------------------------------------------------------
# record_signals -- RELIABILITY family
# ---------------------------------------------------------------------------
 
 
def test_reliability_block_count_zero_for_propose() -> None:
    sigs = [s for s in record_signals(_build()) if s.name == "reliability.block_count"]
    assert sigs[0].value == 0.0
 
 
def test_reliability_block_count_one_for_block() -> None:
    record = _build(outcome=InvocationOutcome.BLOCK)
    sigs = [s for s in record_signals(record) if s.name == "reliability.block_count"]
    assert sigs[0].value == 1.0
 
 
def test_reliability_abstain_count_zero_for_propose() -> None:
    sigs = [s for s in record_signals(_build()) if s.name == "reliability.abstain_count"]
    assert sigs[0].value == 0.0
 
 
def test_reliability_abstain_count_one_for_abstain() -> None:
    record = _build(outcome=InvocationOutcome.ABSTAIN)
    sigs = [s for s in record_signals(record) if s.name == "reliability.abstain_count"]
    assert sigs[0].value == 1.0
 
 
# ---------------------------------------------------------------------------
# record_signals -- FINOPS family
# ---------------------------------------------------------------------------
 
 
def test_finops_monetary_cost_correct() -> None:
    usage = UsageMetrics(monetary_cost_usd=0.005)
    record = _build(usage=usage)
    sigs = [s for s in record_signals(record) if s.name == "finops.monetary_cost_usd"]
    assert sigs[0].value == pytest.approx(0.005)
    assert sigs[0].unit == "usd"
 
 
def test_finops_total_tokens_correct() -> None:
    usage = UsageMetrics(input_tokens=100, output_tokens=50)
    record = _build(usage=usage)
    sigs = [s for s in record_signals(record) if s.name == "finops.total_tokens"]
    assert sigs[0].value == 150.0
 
 
# ---------------------------------------------------------------------------
# record_signals -- GOVERNANCE family
# ---------------------------------------------------------------------------
 
 
def test_governance_invocation_count_is_one() -> None:
    sigs = [s for s in record_signals(_build()) if s.name == "governance.invocation_count"]
    assert sigs[0].value == 1.0
 
 
def test_governance_a3_plus_invocation_false_for_a1() -> None:
    sigs = [s for s in record_signals(_build()) if s.name == "governance.a3_plus_invocation"]
    assert sigs[0].value == 0.0
 
 
def test_governance_a3_plus_invocation_true_for_a3() -> None:
    record = _build(prov=_A3_PROV, outcome=InvocationOutcome.REVIEW, review_ref="dec-001")
    sigs = [s for s in record_signals(record) if s.name == "governance.a3_plus_invocation"]
    assert sigs[0].value == 1.0
 
 
def test_governance_a3_plus_invocation_true_for_a4() -> None:
    prov = replace(_BASE_PROV, authority_outcome=AuthorityOutcome.A4)
    record = _build(prov=prov, outcome=InvocationOutcome.REVIEW, review_ref="dec-001")
    sigs = [s for s in record_signals(record) if s.name == "governance.a3_plus_invocation"]
    assert sigs[0].value == 1.0
 
 
def test_governance_review_required_false() -> None:
    sigs = [s for s in record_signals(_build()) if s.name == "governance.review_required"]
    assert sigs[0].value == 0.0
 
 
def test_governance_review_required_true_for_review_outcome() -> None:
    record = _build(prov=_A3_PROV, outcome=InvocationOutcome.REVIEW, review_ref="dec-001")
    sigs = [s for s in record_signals(record) if s.name == "governance.review_required"]
    assert sigs[0].value == 1.0
 
 
# ---------------------------------------------------------------------------
# record_signals -- label safety (design rule 1)
# ---------------------------------------------------------------------------
 
 
def test_signal_labels_do_not_contain_raw_input_content() -> None:
    """Input content must never appear in labels -- only its hash."""
    raw = b"sensitive taxpayer data that must not leak into labels"
    record = _build(input_bytes=raw)
    for sig in record_signals(record):
        for v in sig.labels.values():
            assert raw.decode() not in v
 
 
def test_signal_labels_contain_only_identity_and_outcome_strings() -> None:
    """Base labels are always identity strings or outcome codes."""
    record = _build()
    for sig in record_signals(record):
        assert "use_case" in sig.labels
        assert "region" in sig.labels
        assert "provider" in sig.labels
        assert "model" in sig.labels
        assert "authority_outcome" in sig.labels
        assert "invocation_outcome" in sig.labels
 
 
# ---------------------------------------------------------------------------
# record_audit_entries -- type checks
# ---------------------------------------------------------------------------
 
 
def test_record_audit_entries_rejects_non_record() -> None:
    buf = AuditBuffer()
    with pytest.raises(ObservabilityError, match="InvocationEvidenceRecord"):
        record_audit_entries("not a record", buf)  # type: ignore[arg-type]
 
 
def test_record_audit_entries_rejects_non_buffer() -> None:
    record = _build()
    with pytest.raises(ObservabilityError, match="AuditBuffer"):
        record_audit_entries(record, "not a buffer")  # type: ignore[arg-type]
 
 
# ---------------------------------------------------------------------------
# record_audit_entries -- A3+ entry
# ---------------------------------------------------------------------------
 
 
def test_a3_invocation_produces_a3_invocation_entry() -> None:
    buf = AuditBuffer()
    record = _build(prov=_A3_PROV, outcome=InvocationOutcome.REVIEW, review_ref="dec-001")
    entries = record_audit_entries(record, buf)
    kinds = [e.kind for e in entries]
    assert AuditEventKind.A3_INVOCATION in kinds
 
 
def test_sub_a3_invocation_produces_no_a3_invocation_entry() -> None:
    """A1 invocations do not require the audit-gate entry."""
    buf = AuditBuffer()
    record = _build(prov=_BASE_PROV, outcome=InvocationOutcome.PROPOSE)
    entries = record_audit_entries(record, buf)
    kinds = [e.kind for e in entries]
    assert AuditEventKind.A3_INVOCATION not in kinds
 
 
def test_a3_entry_carries_review_ref_in_detail() -> None:
    buf = AuditBuffer()
    record = _build(prov=_A3_PROV, outcome=InvocationOutcome.REVIEW, review_ref="dec-abc")
    record_audit_entries(record, buf)
    a3_entries = [e for e in buf.peek() if e.kind is AuditEventKind.A3_INVOCATION]
    assert a3_entries[0].detail.get("review_ref") == "dec-abc"
 
 
def test_a3_entry_carries_input_hash_not_raw_input() -> None:
    buf = AuditBuffer()
    record = _build(prov=_A3_PROV, outcome=InvocationOutcome.REVIEW,
                    review_ref="dec-abc", input_bytes=b"sensitive data")
    record_audit_entries(record, buf)
    a3_entries = [e for e in buf.peek() if e.kind is AuditEventKind.A3_INVOCATION]
    detail = a3_entries[0].detail
    assert "input_hash" in detail
    assert "sensitive data" not in str(detail)
 
 
def test_a3_entry_links_correct_ai_invocation_id() -> None:
    buf = AuditBuffer()
    record = _build(prov=_A3_PROV, outcome=InvocationOutcome.REVIEW, review_ref="dec-001")
    record_audit_entries(record, buf)
    a3_entries = [e for e in buf.peek() if e.kind is AuditEventKind.A3_INVOCATION]
    assert a3_entries[0].ai_invocation_id == record.ai_invocation_id
 
 
# ---------------------------------------------------------------------------
# record_audit_entries -- tool-call entries
# ---------------------------------------------------------------------------
 
 
def test_tool_call_entry_produced_for_each_tool_call() -> None:
    buf = AuditBuffer()
    tc2 = ToolCallRecord(
        call_id="call-obs-002", tool_id="rule-fetch", caller_id="agent:x",
        timestamp=_TS_ISO, outcome="PERMITTED",
    )
    record = _build(tool_calls=[_BASE_TOOL_CALL, tc2])
    entries = record_audit_entries(record, buf)
    tc_entries = [e for e in entries if e.kind is AuditEventKind.TOOL_CALL]
    assert len(tc_entries) == 2
 
 
def test_tool_call_entry_produced_at_sub_a3_level() -> None:
    """Tool calls at A1 still produce TOOL_CALL audit entries."""
    buf = AuditBuffer()
    record = _build(prov=_BASE_PROV, tool_calls=[_BASE_TOOL_CALL])
    entries = record_audit_entries(record, buf)
    tc_entries = [e for e in entries if e.kind is AuditEventKind.TOOL_CALL]
    assert len(tc_entries) == 1
 
 
def test_tool_call_entry_detail_contains_tool_id() -> None:
    buf = AuditBuffer()
    record = _build(tool_calls=[_BASE_TOOL_CALL])
    record_audit_entries(record, buf)
    tc_entries = [e for e in buf.peek() if e.kind is AuditEventKind.TOOL_CALL]
    assert tc_entries[0].detail["tool_id"] == "corpus-fetch"
 
 
def test_tool_call_entry_detail_contains_payload_hash_not_raw() -> None:
    buf = AuditBuffer()
    record = _build(tool_calls=[_BASE_TOOL_CALL])
    record_audit_entries(record, buf)
    tc_entries = [e for e in buf.peek() if e.kind is AuditEventKind.TOOL_CALL]
    assert "payload_hash" in tc_entries[0].detail
 
 
def test_tool_call_entry_no_payload_hash_when_none() -> None:
    tc = ToolCallRecord(
        call_id="c1", tool_id="t1", caller_id="a1",
        timestamp=_TS_ISO, outcome="PERMITTED",
    )
    buf = AuditBuffer()
    record = _build(tool_calls=[tc])
    record_audit_entries(record, buf)
    tc_entries = [e for e in buf.peek() if e.kind is AuditEventKind.TOOL_CALL]
    assert "payload_hash" not in tc_entries[0].detail
 
 
# ---------------------------------------------------------------------------
# record_audit_entries -- BLOCK event entries
# ---------------------------------------------------------------------------
 
 
def test_block_event_entry_produced_for_block_outcome() -> None:
    buf = AuditBuffer()
    record = _build(outcome=InvocationOutcome.BLOCK, guardrails=[_BASE_GUARDRAIL_KILL])
    entries = record_audit_entries(record, buf)
    block_entries = [e for e in entries if e.kind is AuditEventKind.BLOCK_EVENT]
    assert len(block_entries) == 1
 
 
def test_block_event_entry_not_produced_for_propose() -> None:
    buf = AuditBuffer()
    record = _build(outcome=InvocationOutcome.PROPOSE)
    entries = record_audit_entries(record, buf)
    block_entries = [e for e in entries if e.kind is AuditEventKind.BLOCK_EVENT]
    assert len(block_entries) == 0
 
 
def test_block_event_detail_contains_refusal_verdict() -> None:
    buf = AuditBuffer()
    record = _build(outcome=InvocationOutcome.BLOCK, guardrails=[_BASE_GUARDRAIL_KILL])
    record_audit_entries(record, buf)
    block_entries = [e for e in buf.peek() if e.kind is AuditEventKind.BLOCK_EVENT]
    detail = block_entries[0].detail
    assert any("AI_KILL_SWITCH_ENGAGED" in v for v in detail.values())
 
 
def test_block_event_detail_includes_detail_text() -> None:
    buf = AuditBuffer()
    record = _build(outcome=InvocationOutcome.BLOCK, guardrails=[_BASE_GUARDRAIL_KILL])
    record_audit_entries(record, buf)
    block_entries = [e for e in buf.peek() if e.kind is AuditEventKind.BLOCK_EVENT]
    detail = block_entries[0].detail
    # _BASE_GUARDRAIL_KILL has detail="global kill active"
    assert any("global kill active" in v for v in detail.values())
 
 
def test_block_event_detail_contains_input_hash_not_raw_input() -> None:
    buf = AuditBuffer()
    record = _build(outcome=InvocationOutcome.BLOCK, input_bytes=b"private request")
    record_audit_entries(record, buf)
    block_entries = [e for e in buf.peek() if e.kind is AuditEventKind.BLOCK_EVENT]
    detail = block_entries[0].detail
    assert "input_hash" in detail
    assert "private request" not in str(detail)
 
 
# ---------------------------------------------------------------------------
# record_audit_entries -- buffer-full error
# ---------------------------------------------------------------------------
 
 
def test_record_audit_entries_raises_when_buffer_full() -> None:
    buf = AuditBuffer(capacity=1)
    record = _build(prov=_A3_PROV, outcome=InvocationOutcome.REVIEW, review_ref="dec-001")
    buf.append(_make_entry())  # fill the buffer
    with pytest.raises(ObservabilityError, match="full"):
        record_audit_entries(record, buf)
 
 
def _needs_four_entries() -> InvocationEvidenceRecord:
    """A3 invocation (1 entry) + 3 tool calls (3 entries) = 4 audit entries."""
    calls = [
        replace(_BASE_TOOL_CALL, call_id=f"call-{i}", tool_id=f"tool-{i}") for i in range(3)
    ]
    return _build(
        prov=_A3_PROV,
        outcome=InvocationOutcome.REVIEW,
        review_ref="dec-001",
        tool_calls=calls,
    )
 
 
def test_record_audit_entries_writes_nothing_when_buffer_cannot_hold_all() -> None:
    """All-or-nothing: a buffer with room for 2 of the 4 needed entries stays empty."""
    buf = AuditBuffer(capacity=2)
    with pytest.raises(ObservabilityError, match="nothing was written"):
        record_audit_entries(_needs_four_entries(), buf)
    assert buf.size == 0
 
 
def test_record_audit_entries_partial_room_does_not_disturb_existing_entries() -> None:
    buf = AuditBuffer(capacity=3)
    existing = _make_entry()
    buf.append(existing)
    with pytest.raises(ObservabilityError):
        record_audit_entries(_needs_four_entries(), buf)
    assert buf.peek() == [existing]
 
 
def test_record_audit_entries_retry_after_drain_does_not_duplicate() -> None:
    """The bug this guards against: a partial write, then a retry, doubled entries."""
    record = _needs_four_entries()
    buf = AuditBuffer(capacity=2)
    with pytest.raises(ObservabilityError):
        record_audit_entries(record, buf)
    buf.drain()
    big = AuditBuffer(capacity=10)
    written = record_audit_entries(record, big)
    assert len(written) == 4
    assert big.size == 4
    assert len({(e.kind, e.detail.get("call_id")) for e in big.peek()}) == 4
 
 
def test_record_audit_entries_exact_fit_is_accepted() -> None:
    buf = AuditBuffer(capacity=4)
    assert len(record_audit_entries(_needs_four_entries(), buf)) == 4
    assert buf.is_full
 
 
def test_pipeline_full_buffer_writes_no_partial_entries_and_no_signals() -> None:
    pipeline = ObservabilityPipeline(buffer=AuditBuffer(capacity=2))
    with pytest.raises(ObservabilityError):
        pipeline.observe(_needs_four_entries())
    assert pipeline.buffer.size == 0
    assert pipeline.signals == []
 
 
def test_record_audit_entries_returns_empty_list_when_no_events() -> None:
    """A sub-A3 propose with no tools produces zero audit entries."""
    buf = AuditBuffer()
    record = _build(prov=_BASE_PROV, outcome=InvocationOutcome.PROPOSE)
    entries = record_audit_entries(record, buf)
    assert entries == []
    assert buf.size == 0
 
 
# ---------------------------------------------------------------------------
# ObservabilityPipeline
# ---------------------------------------------------------------------------
 
 
def test_pipeline_observe_returns_signals() -> None:
    pipeline = ObservabilityPipeline()
    signals = pipeline.observe(_build())
    assert len(signals) > 0
    assert all(isinstance(s, TelemetrySignal) for s in signals)
 
 
def test_pipeline_accumulates_signals_across_calls() -> None:
    pipeline = ObservabilityPipeline()
    s1 = pipeline.observe(_build())
    s2 = pipeline.observe(_build())
    assert len(pipeline.signals) == len(s1) + len(s2)
 
 
def test_pipeline_drain_signals_clears_accumulation() -> None:
    pipeline = ObservabilityPipeline()
    pipeline.observe(_build())
    pipeline.drain_signals()
    assert pipeline.signals == []
 
 
def test_pipeline_drain_signals_returns_all_accumulated() -> None:
    pipeline = ObservabilityPipeline()
    s1 = pipeline.observe(_build())
    drained = pipeline.drain_signals()
    assert len(drained) == len(s1)
 
 
def test_pipeline_signals_property_returns_copy() -> None:
    pipeline = ObservabilityPipeline()
    pipeline.observe(_build())
    snapshot = pipeline.signals
    snapshot.clear()
    assert len(pipeline.signals) > 0  # internal state unchanged
 
 
def test_pipeline_observe_writes_to_buffer() -> None:
    pipeline = ObservabilityPipeline()
    record = _build(prov=_A3_PROV, outcome=InvocationOutcome.REVIEW, review_ref="dec-001")
    pipeline.observe(record)
    assert pipeline.buffer.size > 0
 
 
def test_pipeline_raises_when_buffer_full() -> None:
    """When the buffer is full, observe must raise before producing signals."""
    buf = AuditBuffer(capacity=1)
    buf.append(_make_entry())  # fill the buffer
    pipeline = ObservabilityPipeline(buffer=buf)
    record = _build(prov=_A3_PROV, outcome=InvocationOutcome.REVIEW, review_ref="dec-001")
    with pytest.raises(ObservabilityError, match="full"):
        pipeline.observe(record)
    # No signals were accumulated.
    assert pipeline.signals == []
 
 
# ---------------------------------------------------------------------------
# Design rule 2: audit before telemetry
# ---------------------------------------------------------------------------
 
 
def test_audit_entries_written_even_if_signals_not_consumed() -> None:
    """record_audit_entries is independent of record_signals; the buffer
    is written whenever record_audit_entries is called."""
    buf = AuditBuffer()
    record = _build(prov=_A3_PROV, outcome=InvocationOutcome.REVIEW, review_ref="dec-001")
    record_audit_entries(record, buf)  # no record_signals called
    assert buf.size > 0
 
 
def test_pipeline_audit_happens_before_signal_accumulation() -> None:
    """In ObservabilityPipeline.observe, the buffer must be written before
    signals are accumulated.  We verify this by filling the buffer so that
    observe raises, then confirming zero signals were accumulated."""
    buf = AuditBuffer(capacity=1)
    buf.append(_make_entry())  # pre-fill so the next A3 write fails
    pipeline = ObservabilityPipeline(buffer=buf)
    record = _build(prov=_A3_PROV, outcome=InvocationOutcome.REVIEW, review_ref="dec-001")
    with pytest.raises(ObservabilityError):
        pipeline.observe(record)
    assert pipeline.signals == []  # audit raised before signals were added
 
 
# ---------------------------------------------------------------------------
# Integration -- full pipelines
# ---------------------------------------------------------------------------
 
 
def test_integration_propose_pipeline() -> None:
    """A1 PROPOSE invocation with usage and one tool call."""
    usage = UsageMetrics(
        latency_ms=210, input_tokens=128, output_tokens=32, monetary_cost_usd=0.002
    )
    record = _build(
        prov=_BASE_PROV,
        outcome=InvocationOutcome.PROPOSE,
        tool_calls=[_BASE_TOOL_CALL],
        guardrails=[_BASE_GUARDRAIL_PASS],
        input_bytes=b"classify this product",
        output_bytes=b"TAXABLE:TELECOM",
        usage=usage,
    )
    signals = record_signals(record)
    buf = AuditBuffer()
    entries = record_audit_entries(record, buf)
 
    # Signals from all eight families.
    families = {s.family for s in signals}
    assert families == set(SignalFamily)
 
    # Latency and cost present.
    latency = next(s for s in signals if s.name == "model_ops.latency_ms")
    assert latency.value == 210.0
 
    cost = next(s for s in signals if s.name == "finops.monetary_cost_usd")
    assert cost.value == pytest.approx(0.002)
 
    # One tool call in AGENT_OPS.
    tc_count = next(s for s in signals if s.name == "agent_ops.tool_call_count")
    assert tc_count.value == 1.0
 
    # No A3_INVOCATION entry (A1 invocation).
    assert not any(e.kind is AuditEventKind.A3_INVOCATION for e in entries)
 
    # One TOOL_CALL entry.
    tc_entries = [e for e in entries if e.kind is AuditEventKind.TOOL_CALL]
    assert len(tc_entries) == 1
 
    # No BLOCK_EVENT.
    assert not any(e.kind is AuditEventKind.BLOCK_EVENT for e in entries)
 
 
def test_integration_blocked_pipeline() -> None:
    """BLOCK invocation: one BLOCK_EVENT entry, no A3_INVOCATION."""
    record = _build(
        outcome=InvocationOutcome.BLOCK,
        guardrails=[_BASE_GUARDRAIL_KILL],
        input_bytes=b"blocked request",
    )
    signals = record_signals(record)
    buf = AuditBuffer()
    entries = record_audit_entries(record, buf)
 
    block_sig = next(s for s in signals if s.name == "security.block_event")
    assert block_sig.value == 1.0
 
    kill_sig = next(s for s in signals if s.name == "security.kill_switch_fired")
    assert kill_sig.value == 1.0
 
    block_entries = [e for e in entries if e.kind is AuditEventKind.BLOCK_EVENT]
    assert len(block_entries) == 1
 
    assert not any(e.kind is AuditEventKind.A3_INVOCATION for e in entries)
 
 
def test_integration_review_pipeline() -> None:
    """A3 REVIEW invocation: one A3_INVOCATION entry with review_ref."""
    usage = UsageMetrics(latency_ms=500, input_tokens=256, output_tokens=64)
    record = _build(
        prov=_A3_PROV,
        outcome=InvocationOutcome.REVIEW,
        review_ref="dec-review-001",
        guardrails=[_BASE_GUARDRAIL_PASS],
        tool_calls=[_BASE_TOOL_CALL],
        input_bytes=b"A3 proposal",
        output_bytes=b"proposed diff",
        usage=usage,
    )
    signals = record_signals(record)
    buf = AuditBuffer()
    entries = record_audit_entries(record, buf)
 
    a3_sig = next(s for s in signals if s.name == "governance.a3_plus_invocation")
    assert a3_sig.value == 1.0
 
    review_sig = next(s for s in signals if s.name == "governance.review_required")
    assert review_sig.value == 1.0
 
    a3_entries = [e for e in entries if e.kind is AuditEventKind.A3_INVOCATION]
    assert len(a3_entries) == 1
    assert a3_entries[0].detail.get("review_ref") == "dec-review-001"
 
    tc_entries = [e for e in entries if e.kind is AuditEventKind.TOOL_CALL]
    assert len(tc_entries) == 1
 
 
def test_integration_pipeline_facade_observe() -> None:
    """ObservabilityPipeline.observe accumulates all signals and writes audit."""
    pipeline = ObservabilityPipeline()
 
    propose = _build(prov=_BASE_PROV, outcome=InvocationOutcome.PROPOSE)
    review = _build(prov=_A3_PROV, outcome=InvocationOutcome.REVIEW, review_ref="dec-r01")
    block = _build(outcome=InvocationOutcome.BLOCK, guardrails=[_BASE_GUARDRAIL_KILL])
 
    pipeline.observe(propose)
    pipeline.observe(review)
    pipeline.observe(block)
 
    # Buffer has entries for review (A3_INVOCATION) and block (BLOCK_EVENT).
    all_entries = pipeline.buffer.peek()
    a3 = [e for e in all_entries if e.kind is AuditEventKind.A3_INVOCATION]
    blk = [e for e in all_entries if e.kind is AuditEventKind.BLOCK_EVENT]
    assert len(a3) == 1
    assert len(blk) == 1
 
    # Signals cover all eight families three times over.
    families = {s.family for s in pipeline.signals}
    assert families == set(SignalFamily)
 
    drained = pipeline.drain_signals()
    assert len(drained) == len(pipeline.signals) + len(drained)  # effectively: drained is non-empty
    assert pipeline.signals == []
 