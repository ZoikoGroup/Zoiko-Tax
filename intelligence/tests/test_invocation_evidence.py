"""Tests for the AI Invocation Evidence Model.

Chapter 17 §21 of the ZoikoTax Master Specification.

Mirrors the structure and discipline of ``test_human_review.py`` and
``test_tool_broker.py``:

- Plain functions; no test classes.
- ``dataclasses.replace`` for variants so a typo in a field name is a
  type error here, not a silent default.
- Every public type and every public method is exercised.
- Every error path is explicitly asserted so a regression surfaces as a
  test failure, not as a silent wrong answer in an audit log.

Test groups
-----------
  InvocationOutcome           -- enum values, from_authority_outcome mapping
  GuardrailResult             -- construction, immutability, passed property,
                                 validation, as_dict
  ToolCallRecord              -- construction, immutability, validation, as_dict
  UsageMetrics                -- construction, immutability, validation,
                                 total_tokens, as_dict
  InvocationEvidenceError     -- construction, reason attribute
  InvocationEvidenceRecord    -- construction via builder, immutability,
                                 as_dict, any_guardrail_failed
  InvocationEvidenceBuilder   -- happy path, set_input/set_output hashing,
                                 retrieval refs, tool calls, guardrails,
                                 review_ref, usage, build-once guard
  Design rule 1               -- raw bytes never stored on builder
  Design rule 3               -- review_ref <-> REVIEW outcome
  Design rule 4               -- AUTO_ACCEPT ceiling A2
  Integration                 -- full pipeline: provenance -> build -> as_dict
"""

from __future__ import annotations

import hashlib
from dataclasses import FrozenInstanceError, replace
from datetime import UTC, datetime

import pytest

from ztax_gateway.invocation_evidence import (
    GuardrailResult,
    InvocationEvidenceBuilder,
    InvocationEvidenceError,
    InvocationEvidenceRecord,
    InvocationOutcome,
    ToolCallRecord,
    UsageMetrics,
)
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier

# ---------------------------------------------------------------------------
# Shared fixtures
# ---------------------------------------------------------------------------

BASE_PROVENANCE = Provenance(
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

_TS_ISO = "2026-09-25T10:00:00"

BASE_TOOL_CALL = ToolCallRecord(
    call_id="call-abc-001",
    tool_id="corpus-fetch",
    caller_id="agent:change-intel",
    timestamp=_TS_ISO,
    outcome="PERMITTED",
    payload_hash="deadbeef" * 8,
)

BASE_GUARDRAIL_PASS = GuardrailResult(
    guardrail_id="governance.authority",
    verdict="PASS",
)

BASE_GUARDRAIL_FAIL = GuardrailResult(
    guardrail_id="broker.scope_verification",
    verdict="AI_SCOPE_VIOLATION",
    detail="missing scope: write",
)

BASE_USAGE = UsageMetrics(
    latency_ms=240,
    input_tokens=512,
    output_tokens=128,
    monetary_cost_usd=0.004,
)


def _builder(
    provenance: Provenance = BASE_PROVENANCE,
    outcome: InvocationOutcome = InvocationOutcome.PROPOSE,
    *,
    trace_id: str = "trace-test-001",
    ai_invocation_id: str = "inv" + "0" * 29,
) -> InvocationEvidenceBuilder:
    return InvocationEvidenceBuilder(
        provenance=provenance,
        invocation_outcome=outcome,
        trace_id=trace_id,
        ai_invocation_id=ai_invocation_id,
    )


def _build_simple(
    provenance: Provenance = BASE_PROVENANCE,
    outcome: InvocationOutcome = InvocationOutcome.PROPOSE,
) -> InvocationEvidenceRecord:
    return _builder(provenance=provenance, outcome=outcome).build()


# ---------------------------------------------------------------------------
# InvocationOutcome -- enum values
# ---------------------------------------------------------------------------


def test_invocation_outcome_has_six_values() -> None:
    values = {o.value for o in InvocationOutcome}
    assert values == {"PROPOSE", "PRIORITIZE", "AUTO_ACCEPT", "REVIEW", "ABSTAIN", "BLOCK"}


def test_invocation_outcome_is_str_enum() -> None:
    assert InvocationOutcome.PROPOSE == "PROPOSE"
    assert InvocationOutcome.BLOCK == "BLOCK"


# ---------------------------------------------------------------------------
# InvocationOutcome -- from_authority_outcome
# ---------------------------------------------------------------------------


def test_from_authority_outcome_a0_is_propose() -> None:
    result = InvocationOutcome.from_authority_outcome(AuthorityOutcome.A0)
    assert result is InvocationOutcome.PROPOSE


def test_from_authority_outcome_a1_is_propose() -> None:
    result = InvocationOutcome.from_authority_outcome(AuthorityOutcome.A1)
    assert result is InvocationOutcome.PROPOSE


def test_from_authority_outcome_a2_is_prioritize() -> None:
    result = InvocationOutcome.from_authority_outcome(AuthorityOutcome.A2)
    assert result is InvocationOutcome.PRIORITIZE


def test_from_authority_outcome_a3_is_review() -> None:
    assert InvocationOutcome.from_authority_outcome(AuthorityOutcome.A3) is InvocationOutcome.REVIEW


def test_from_authority_outcome_a4_is_review() -> None:
    assert InvocationOutcome.from_authority_outcome(AuthorityOutcome.A4) is InvocationOutcome.REVIEW


def test_from_authority_outcome_a5_is_block() -> None:
    assert InvocationOutcome.from_authority_outcome(AuthorityOutcome.A5) is InvocationOutcome.BLOCK


def test_from_authority_outcome_covers_all_authority_levels() -> None:
    """Every AuthorityOutcome value must map to an InvocationOutcome."""
    for authority in AuthorityOutcome:
        result = InvocationOutcome.from_authority_outcome(authority)
        assert isinstance(result, InvocationOutcome)


# ---------------------------------------------------------------------------
# GuardrailResult -- construction
# ---------------------------------------------------------------------------


def test_guardrail_result_stores_guardrail_id() -> None:
    assert BASE_GUARDRAIL_PASS.guardrail_id == "governance.authority"


def test_guardrail_result_stores_verdict() -> None:
    assert BASE_GUARDRAIL_PASS.verdict == "PASS"


def test_guardrail_result_stores_detail() -> None:
    assert BASE_GUARDRAIL_FAIL.detail == "missing scope: write"


def test_guardrail_result_default_detail_is_empty_string() -> None:
    gr = GuardrailResult(guardrail_id="governance.authority", verdict="PASS")
    assert gr.detail == ""


# ---------------------------------------------------------------------------
# GuardrailResult -- passed property
# ---------------------------------------------------------------------------


def test_guardrail_result_passed_is_true_when_verdict_is_pass() -> None:
    assert BASE_GUARDRAIL_PASS.passed is True


def test_guardrail_result_passed_is_false_when_verdict_is_refusal() -> None:
    assert BASE_GUARDRAIL_FAIL.passed is False


def test_guardrail_result_passed_is_false_for_arbitrary_non_pass_verdict() -> None:
    gr = GuardrailResult(guardrail_id="gk", verdict="AI_KILL_SWITCH_ENGAGED")
    assert gr.passed is False


# ---------------------------------------------------------------------------
# GuardrailResult -- validation
# ---------------------------------------------------------------------------


def test_guardrail_result_empty_guardrail_id_is_refused() -> None:
    with pytest.raises(InvocationEvidenceError, match="guardrail_id"):
        GuardrailResult(guardrail_id="", verdict="PASS")


def test_guardrail_result_empty_verdict_is_refused() -> None:
    with pytest.raises(InvocationEvidenceError, match="verdict"):
        GuardrailResult(guardrail_id="governance.authority", verdict="")


# ---------------------------------------------------------------------------
# GuardrailResult -- immutability
# ---------------------------------------------------------------------------


def test_guardrail_result_is_frozen() -> None:
    with pytest.raises(FrozenInstanceError):
        BASE_GUARDRAIL_PASS.verdict = "TAMPERED"  # type: ignore[misc]


# ---------------------------------------------------------------------------
# GuardrailResult -- as_dict
# ---------------------------------------------------------------------------


def test_guardrail_result_as_dict_contains_expected_keys() -> None:
    d = BASE_GUARDRAIL_PASS.as_dict()
    assert set(d.keys()) == {"guardrail_id", "verdict", "detail"}


def test_guardrail_result_as_dict_values_correct() -> None:
    d = BASE_GUARDRAIL_FAIL.as_dict()
    assert d["guardrail_id"] == "broker.scope_verification"
    assert d["verdict"] == "AI_SCOPE_VIOLATION"
    assert d["detail"] == "missing scope: write"


# ---------------------------------------------------------------------------
# ToolCallRecord -- construction
# ---------------------------------------------------------------------------


def test_tool_call_record_stores_call_id() -> None:
    assert BASE_TOOL_CALL.call_id == "call-abc-001"


def test_tool_call_record_stores_tool_id() -> None:
    assert BASE_TOOL_CALL.tool_id == "corpus-fetch"


def test_tool_call_record_stores_caller_id() -> None:
    assert BASE_TOOL_CALL.caller_id == "agent:change-intel"


def test_tool_call_record_stores_outcome() -> None:
    assert BASE_TOOL_CALL.outcome == "PERMITTED"


def test_tool_call_record_stores_payload_hash() -> None:
    assert BASE_TOOL_CALL.payload_hash == "deadbeef" * 8


def test_tool_call_record_payload_hash_defaults_to_none() -> None:
    tcr = ToolCallRecord(
        call_id="call-001",
        tool_id="tool-x",
        caller_id="agent:x",
        timestamp=_TS_ISO,
        outcome="PERMITTED",
    )
    assert tcr.payload_hash is None


def test_tool_call_record_reviewer_id_defaults_to_none() -> None:
    assert ToolCallRecord(
        call_id="c1", tool_id="t1", caller_id="a1", timestamp=_TS_ISO, outcome="PERMITTED"
    ).reviewer_id is None


# ---------------------------------------------------------------------------
# ToolCallRecord -- validation
# ---------------------------------------------------------------------------


def test_tool_call_record_empty_call_id_is_refused() -> None:
    with pytest.raises(InvocationEvidenceError, match="call_id"):
        ToolCallRecord(
            call_id="", tool_id="t", caller_id="a",
            timestamp=_TS_ISO, outcome="PERMITTED"
        )


def test_tool_call_record_empty_tool_id_is_refused() -> None:
    with pytest.raises(InvocationEvidenceError, match="tool_id"):
        ToolCallRecord(
            call_id="c1", tool_id="", caller_id="a",
            timestamp=_TS_ISO, outcome="PERMITTED"
        )


def test_tool_call_record_empty_outcome_is_refused() -> None:
    with pytest.raises(InvocationEvidenceError, match="outcome"):
        ToolCallRecord(call_id="c1", tool_id="t1", caller_id="a", timestamp=_TS_ISO, outcome="")


# ---------------------------------------------------------------------------
# ToolCallRecord -- immutability
# ---------------------------------------------------------------------------


def test_tool_call_record_is_frozen() -> None:
    with pytest.raises(FrozenInstanceError):
        BASE_TOOL_CALL.outcome = "TAMPERED"  # type: ignore[misc]


# ---------------------------------------------------------------------------
# ToolCallRecord -- as_dict
# ---------------------------------------------------------------------------


def test_tool_call_record_as_dict_contains_mandatory_keys() -> None:
    tcr = ToolCallRecord(
        call_id="c1", tool_id="t1", caller_id="a1", timestamp=_TS_ISO, outcome="PERMITTED"
    )
    d = tcr.as_dict()
    assert {"call_id", "tool_id", "caller_id", "timestamp", "outcome"}.issubset(d.keys())


def test_tool_call_record_as_dict_payload_hash_included_when_present() -> None:
    d = BASE_TOOL_CALL.as_dict()
    assert "payload_hash" in d
    assert d["payload_hash"] == "deadbeef" * 8


def test_tool_call_record_as_dict_payload_hash_absent_when_none() -> None:
    tcr = ToolCallRecord(
        call_id="c1", tool_id="t1", caller_id="a1", timestamp=_TS_ISO, outcome="PERMITTED"
    )
    assert "payload_hash" not in tcr.as_dict()


def test_tool_call_record_as_dict_reviewer_id_included_when_present() -> None:
    tcr = ToolCallRecord(
        call_id="c1", tool_id="t1", caller_id="a1", timestamp=_TS_ISO,
        outcome="PERMITTED", reviewer_id="reviewer:alice"
    )
    assert tcr.as_dict()["reviewer_id"] == "reviewer:alice"


# ---------------------------------------------------------------------------
# UsageMetrics -- construction
# ---------------------------------------------------------------------------


def test_usage_metrics_stores_latency_ms() -> None:
    assert BASE_USAGE.latency_ms == 240


def test_usage_metrics_stores_input_tokens() -> None:
    assert BASE_USAGE.input_tokens == 512


def test_usage_metrics_stores_output_tokens() -> None:
    assert BASE_USAGE.output_tokens == 128


def test_usage_metrics_stores_monetary_cost() -> None:
    assert BASE_USAGE.monetary_cost_usd == pytest.approx(0.004)


def test_usage_metrics_defaults_to_zero() -> None:
    u = UsageMetrics()
    assert u.latency_ms == 0
    assert u.input_tokens == 0
    assert u.output_tokens == 0
    assert u.monetary_cost_usd == pytest.approx(0.0)


# ---------------------------------------------------------------------------
# UsageMetrics -- total_tokens
# ---------------------------------------------------------------------------


def test_usage_metrics_total_tokens_is_sum() -> None:
    assert BASE_USAGE.total_tokens == 640


def test_usage_metrics_total_tokens_zero_when_defaults() -> None:
    assert UsageMetrics().total_tokens == 0


# ---------------------------------------------------------------------------
# UsageMetrics -- validation
# ---------------------------------------------------------------------------


def test_usage_metrics_negative_latency_is_refused() -> None:
    with pytest.raises(InvocationEvidenceError, match="latency_ms"):
        UsageMetrics(latency_ms=-1)


def test_usage_metrics_negative_input_tokens_is_refused() -> None:
    with pytest.raises(InvocationEvidenceError, match="input_tokens"):
        UsageMetrics(input_tokens=-1)


def test_usage_metrics_negative_output_tokens_is_refused() -> None:
    with pytest.raises(InvocationEvidenceError, match="output_tokens"):
        UsageMetrics(output_tokens=-1)


def test_usage_metrics_negative_monetary_cost_is_refused() -> None:
    with pytest.raises(InvocationEvidenceError, match="monetary_cost_usd"):
        UsageMetrics(monetary_cost_usd=-0.01)


# ---------------------------------------------------------------------------
# UsageMetrics -- immutability
# ---------------------------------------------------------------------------


def test_usage_metrics_is_frozen() -> None:
    with pytest.raises(FrozenInstanceError):
        BASE_USAGE.latency_ms = 9999  # type: ignore[misc]


# ---------------------------------------------------------------------------
# UsageMetrics -- as_dict
# ---------------------------------------------------------------------------


def test_usage_metrics_as_dict_contains_expected_keys() -> None:
    d = BASE_USAGE.as_dict()
    assert set(d.keys()) == {"latency_ms", "input_tokens", "output_tokens",
                             "total_tokens", "monetary_cost_usd"}


def test_usage_metrics_as_dict_total_tokens_correct() -> None:
    assert BASE_USAGE.as_dict()["total_tokens"] == 640


# ---------------------------------------------------------------------------
# InvocationEvidenceError
# ---------------------------------------------------------------------------


def test_invocation_evidence_error_is_exception() -> None:
    err = InvocationEvidenceError("test reason")
    assert isinstance(err, Exception)


def test_invocation_evidence_error_reason_attribute() -> None:
    err = InvocationEvidenceError("something went wrong")
    assert err.reason == "something went wrong"


def test_invocation_evidence_error_str_contains_reason() -> None:
    err = InvocationEvidenceError("schema violation")
    assert "schema violation" in str(err)


# ---------------------------------------------------------------------------
# InvocationEvidenceBuilder -- happy path
# ---------------------------------------------------------------------------


def test_builder_produces_invocation_evidence_record() -> None:
    record = _build_simple()
    assert isinstance(record, InvocationEvidenceRecord)


def test_builder_sets_ai_invocation_id_from_override() -> None:
    record = _build_simple()
    assert record.ai_invocation_id == "inv" + "0" * 29


def test_builder_generates_ai_invocation_id_when_not_overridden() -> None:
    b = InvocationEvidenceBuilder(
        provenance=BASE_PROVENANCE,
        invocation_outcome=InvocationOutcome.PROPOSE,
    )
    record = b.build()
    assert len(record.ai_invocation_id) == 32  # UUID4 hex
    assert record.ai_invocation_id.isalnum()


def test_builder_sets_trace_id() -> None:
    record = _build_simple()
    assert record.trace_id == "trace-test-001"


def test_builder_trace_id_defaults_to_empty_string() -> None:
    b = InvocationEvidenceBuilder(
        provenance=BASE_PROVENANCE,
        invocation_outcome=InvocationOutcome.PROPOSE,
    )
    assert b.build().trace_id == ""


def test_builder_sets_ai_release_manifest_id_from_provenance() -> None:
    record = _build_simple()
    assert record.ai_release_manifest_id == "0.6.0"


def test_builder_sets_caller_security_context_ref_from_provenance() -> None:
    record = _build_simple()
    assert record.caller_security_context_ref == "change-intelligence"


def test_builder_sets_region_from_provenance() -> None:
    record = _build_simple()
    assert record.region == "eu-west-1"


def test_builder_sets_provider_from_provenance() -> None:
    record = _build_simple()
    assert record.provider == "provider:eu-hosted"


def test_builder_sets_model_from_provenance() -> None:
    record = _build_simple()
    assert record.model == "model:gemini-pro@2026.09"


def test_builder_sets_prompt_version_from_provenance() -> None:
    record = _build_simple()
    assert record.prompt_version == "prompt:change-extract@3"


def test_builder_sets_authority_outcome_from_provenance() -> None:
    record = _build_simple()
    assert record.authority_outcome is AuthorityOutcome.A1


def test_builder_sets_invocation_outcome() -> None:
    record = _build_simple(outcome=InvocationOutcome.ABSTAIN)
    assert record.invocation_outcome is InvocationOutcome.ABSTAIN


def test_builder_recorded_at_is_utc_aware() -> None:
    record = _build_simple()
    assert record.recorded_at.tzinfo is not None
    assert record.recorded_at.tzinfo == UTC


def test_builder_default_input_hash_is_none() -> None:
    assert _build_simple().input_hash is None


def test_builder_default_output_hash_is_none() -> None:
    assert _build_simple().output_hash is None


def test_builder_default_retrieval_refs_is_empty_frozenset() -> None:
    assert _build_simple().retrieval_refs == frozenset()


def test_builder_default_tool_calls_is_empty_tuple() -> None:
    assert _build_simple().tool_calls == ()


def test_builder_default_guardrail_results_is_empty_tuple() -> None:
    assert _build_simple().guardrail_results == ()


def test_builder_default_review_ref_is_none() -> None:
    assert _build_simple().review_ref is None


def test_builder_default_usage_has_zero_fields() -> None:
    u = _build_simple().usage
    assert u.latency_ms == 0 and u.total_tokens == 0


# ---------------------------------------------------------------------------
# InvocationEvidenceBuilder -- set_input / set_output hashing
# ---------------------------------------------------------------------------


def test_set_input_stores_sha256_digest() -> None:
    raw = b"request payload"
    expected = hashlib.sha256(raw).hexdigest()
    b = _builder()
    b.set_input(raw)
    record = b.build()
    assert record.input_hash == expected


def test_set_output_stores_sha256_digest() -> None:
    raw = b"model response"
    expected = hashlib.sha256(raw).hexdigest()
    b = _builder()
    b.set_output(raw)
    record = b.build()
    assert record.output_hash == expected


def test_set_input_last_call_wins() -> None:
    b = _builder()
    b.set_input(b"first")
    b.set_input(b"second")
    expected = hashlib.sha256(b"second").hexdigest()
    assert b.build().input_hash == expected


def test_set_input_accepts_bytearray() -> None:
    raw = bytearray(b"bytearray input")
    expected = hashlib.sha256(bytes(raw)).hexdigest()
    b = _builder()
    b.set_input(raw)
    assert b.build().input_hash == expected


def test_set_input_rejects_non_bytes() -> None:
    b = _builder()
    with pytest.raises(InvocationEvidenceError, match="bytes"):
        b.set_input("not bytes")  # type: ignore[arg-type]


def test_set_output_rejects_non_bytes() -> None:
    b = _builder()
    with pytest.raises(InvocationEvidenceError, match="bytes"):
        b.set_output(42)  # type: ignore[arg-type]


# Design rule 1: raw bytes must not be held on the builder after hashing.
# We verify this indirectly: the builder has no attribute that could hold
# the raw bytes, and the built record has only hashes.
def test_design_rule_1_builder_holds_no_raw_input_bytes() -> None:
    raw = b"sensitive taxpayer data"
    b = _builder()
    b.set_input(raw)
    # The builder must not expose the raw bytes as an attribute.
    assert not hasattr(b, "_input_bytes")
    assert not hasattr(b, "_raw_input")


def test_design_rule_1_builder_holds_no_raw_output_bytes() -> None:
    raw = b"model classification"
    b = _builder()
    b.set_output(raw)
    assert not hasattr(b, "_output_bytes")
    assert not hasattr(b, "_raw_output")


def test_design_rule_1_record_contains_no_raw_bytes_field() -> None:
    record = _build_simple()
    assert not hasattr(record, "input_bytes")
    assert not hasattr(record, "output_bytes")
    assert not hasattr(record, "raw_input")
    assert not hasattr(record, "raw_output")


# ---------------------------------------------------------------------------
# InvocationEvidenceBuilder -- retrieval refs
# ---------------------------------------------------------------------------


def test_add_retrieval_ref_stores_citation_id() -> None:
    b = _builder()
    b.add_retrieval_ref("abc123def456abcd")
    record = b.build()
    assert "abc123def456abcd" in record.retrieval_refs


def test_add_retrieval_refs_adds_multiple() -> None:
    b = _builder()
    b.add_retrieval_refs(["aaa111", "bbb222", "ccc333"])
    record = b.build()
    assert record.retrieval_refs == frozenset({"aaa111", "bbb222", "ccc333"})


def test_add_retrieval_ref_deduplicates() -> None:
    b = _builder()
    b.add_retrieval_ref("dup-id")
    b.add_retrieval_ref("dup-id")
    record = b.build()
    assert record.retrieval_refs == frozenset({"dup-id"})


def test_add_retrieval_ref_empty_string_refused() -> None:
    b = _builder()
    with pytest.raises(InvocationEvidenceError, match="citation_id"):
        b.add_retrieval_ref("")


def test_retrieval_refs_is_frozenset_in_record() -> None:
    b = _builder()
    b.add_retrieval_ref("ref-1")
    assert isinstance(b.build().retrieval_refs, frozenset)


# ---------------------------------------------------------------------------
# InvocationEvidenceBuilder -- tool calls
# ---------------------------------------------------------------------------


def test_add_tool_call_appends_record() -> None:
    b = _builder()
    b.add_tool_call(BASE_TOOL_CALL)
    record = b.build()
    assert len(record.tool_calls) == 1
    assert record.tool_calls[0].call_id == "call-abc-001"


def test_add_tool_call_multiple_in_order() -> None:
    tcr2 = ToolCallRecord(
        call_id="call-002", tool_id="rule-fetch", caller_id="agent:x",
        timestamp=_TS_ISO, outcome="PERMITTED"
    )
    b = _builder()
    b.add_tool_call(BASE_TOOL_CALL)
    b.add_tool_call(tcr2)
    record = b.build()
    assert record.tool_calls[0].call_id == "call-abc-001"
    assert record.tool_calls[1].call_id == "call-002"


def test_add_tool_call_rejects_non_tool_call_record() -> None:
    b = _builder()
    with pytest.raises(InvocationEvidenceError, match="ToolCallRecord"):
        b.add_tool_call("not a record")  # type: ignore[arg-type]


def test_tool_calls_is_tuple_in_record() -> None:
    b = _builder()
    b.add_tool_call(BASE_TOOL_CALL)
    assert isinstance(b.build().tool_calls, tuple)


# ---------------------------------------------------------------------------
# InvocationEvidenceBuilder -- guardrail results
# ---------------------------------------------------------------------------


def test_add_guardrail_appends_result() -> None:
    b = _builder()
    b.add_guardrail(BASE_GUARDRAIL_PASS)
    record = b.build()
    assert len(record.guardrail_results) == 1
    assert record.guardrail_results[0].guardrail_id == "governance.authority"


def test_add_guardrail_multiple_in_order() -> None:
    b = _builder()
    b.add_guardrail(BASE_GUARDRAIL_PASS)
    b.add_guardrail(BASE_GUARDRAIL_FAIL)
    record = b.build()
    assert record.guardrail_results[0].verdict == "PASS"
    assert record.guardrail_results[1].verdict == "AI_SCOPE_VIOLATION"


def test_add_guardrail_rejects_non_guardrail_result() -> None:
    b = _builder()
    with pytest.raises(InvocationEvidenceError, match="GuardrailResult"):
        b.add_guardrail("not a result")  # type: ignore[arg-type]


def test_guardrail_results_is_tuple_in_record() -> None:
    b = _builder()
    b.add_guardrail(BASE_GUARDRAIL_PASS)
    assert isinstance(b.build().guardrail_results, tuple)


# ---------------------------------------------------------------------------
# InvocationEvidenceRecord -- any_guardrail_failed
# ---------------------------------------------------------------------------


def test_any_guardrail_failed_false_when_all_pass() -> None:
    b = _builder()
    b.add_guardrail(BASE_GUARDRAIL_PASS)
    b.add_guardrail(GuardrailResult("broker.scope", "PASS"))
    assert b.build().any_guardrail_failed() is False


def test_any_guardrail_failed_true_when_one_fails() -> None:
    b = _builder()
    b.add_guardrail(BASE_GUARDRAIL_PASS)
    b.add_guardrail(BASE_GUARDRAIL_FAIL)
    assert b.build().any_guardrail_failed() is True


def test_any_guardrail_failed_false_when_no_guardrails() -> None:
    assert _build_simple().any_guardrail_failed() is False


# ---------------------------------------------------------------------------
# InvocationEvidenceBuilder -- review_ref
# ---------------------------------------------------------------------------


def test_set_review_ref_stored_when_outcome_is_review() -> None:
    prov = replace(BASE_PROVENANCE, authority_outcome=AuthorityOutcome.A3)
    b = _builder(provenance=prov, outcome=InvocationOutcome.REVIEW)
    b.set_review_ref("decision-uuid-001")
    record = b.build()
    assert record.review_ref == "decision-uuid-001"


def test_set_review_ref_empty_string_refused() -> None:
    b = _builder()
    with pytest.raises(InvocationEvidenceError, match="decision_id"):
        b.set_review_ref("")


# ---------------------------------------------------------------------------
# Design rule 3: review_ref <-> REVIEW outcome
# ---------------------------------------------------------------------------


def test_design_rule_3_review_outcome_without_review_ref_is_refused() -> None:
    prov = replace(BASE_PROVENANCE, authority_outcome=AuthorityOutcome.A3)
    b = _builder(provenance=prov, outcome=InvocationOutcome.REVIEW)
    with pytest.raises(InvocationEvidenceError, match="review_ref"):
        b.build()


def test_design_rule_3_review_ref_with_non_review_outcome_is_refused() -> None:
    b = _builder(outcome=InvocationOutcome.PROPOSE)
    b.set_review_ref("decision-001")
    with pytest.raises(InvocationEvidenceError, match="REVIEW"):
        b.build()


def test_design_rule_3_propose_without_review_ref_is_permitted() -> None:
    record = _build_simple(outcome=InvocationOutcome.PROPOSE)
    assert record.review_ref is None


def test_design_rule_3_block_without_review_ref_is_permitted() -> None:
    record = _build_simple(outcome=InvocationOutcome.BLOCK)
    assert record.review_ref is None


def test_design_rule_3_abstain_without_review_ref_is_permitted() -> None:
    record = _build_simple(outcome=InvocationOutcome.ABSTAIN)
    assert record.review_ref is None


# ---------------------------------------------------------------------------
# InvocationEvidenceBuilder -- usage
# ---------------------------------------------------------------------------


def test_set_usage_stored_on_record() -> None:
    b = _builder()
    b.set_usage(BASE_USAGE)
    record = b.build()
    assert record.usage.latency_ms == 240
    assert record.usage.total_tokens == 640


def test_set_usage_rejects_non_usage_metrics() -> None:
    b = _builder()
    with pytest.raises(InvocationEvidenceError, match="UsageMetrics"):
        b.set_usage({"latency_ms": 100})  # type: ignore[arg-type]


# ---------------------------------------------------------------------------
# Design rule 4: AUTO_ACCEPT ceiling at A2
# ---------------------------------------------------------------------------


def test_design_rule_4_auto_accept_at_a0_is_permitted() -> None:
    prov = replace(BASE_PROVENANCE, authority_outcome=AuthorityOutcome.A0)
    record = _build_simple(provenance=prov, outcome=InvocationOutcome.AUTO_ACCEPT)
    assert record.invocation_outcome is InvocationOutcome.AUTO_ACCEPT


def test_design_rule_4_auto_accept_at_a1_is_permitted() -> None:
    prov = replace(BASE_PROVENANCE, authority_outcome=AuthorityOutcome.A1)
    record = _build_simple(provenance=prov, outcome=InvocationOutcome.AUTO_ACCEPT)
    assert record.invocation_outcome is InvocationOutcome.AUTO_ACCEPT


def test_design_rule_4_auto_accept_at_a2_is_permitted() -> None:
    prov = replace(BASE_PROVENANCE, authority_outcome=AuthorityOutcome.A2)
    record = _build_simple(provenance=prov, outcome=InvocationOutcome.AUTO_ACCEPT)
    assert record.invocation_outcome is InvocationOutcome.AUTO_ACCEPT


def test_design_rule_4_auto_accept_at_a3_is_refused() -> None:
    prov = replace(BASE_PROVENANCE, authority_outcome=AuthorityOutcome.A3)
    b = _builder(provenance=prov, outcome=InvocationOutcome.AUTO_ACCEPT)
    with pytest.raises(InvocationEvidenceError, match="AUTO_ACCEPT"):
        b.build()


def test_design_rule_4_auto_accept_at_a4_is_refused() -> None:
    prov = replace(BASE_PROVENANCE, authority_outcome=AuthorityOutcome.A4)
    b = _builder(provenance=prov, outcome=InvocationOutcome.AUTO_ACCEPT)
    with pytest.raises(InvocationEvidenceError, match="AUTO_ACCEPT"):
        b.build()


def test_design_rule_4_error_message_names_ceiling() -> None:
    prov = replace(BASE_PROVENANCE, authority_outcome=AuthorityOutcome.A3)
    b = _builder(provenance=prov, outcome=InvocationOutcome.AUTO_ACCEPT)
    with pytest.raises(InvocationEvidenceError, match="A2"):
        b.build()


# ---------------------------------------------------------------------------
# Build-once guard
# ---------------------------------------------------------------------------


def test_builder_build_once_guard_raises_on_second_call() -> None:
    b = _builder()
    b.build()
    with pytest.raises(InvocationEvidenceError, match="once"):
        b.build()


# ---------------------------------------------------------------------------
# InvocationEvidenceRecord -- immutability
# ---------------------------------------------------------------------------


def test_invocation_evidence_record_is_frozen() -> None:
    record = _build_simple()
    with pytest.raises(FrozenInstanceError):
        record.invocation_outcome = InvocationOutcome.BLOCK  # type: ignore[misc]


def test_invocation_evidence_record_trace_id_is_frozen() -> None:
    record = _build_simple()
    with pytest.raises(FrozenInstanceError):
        record.trace_id = "tampered"  # type: ignore[misc]


# ---------------------------------------------------------------------------
# InvocationEvidenceRecord -- as_dict
# ---------------------------------------------------------------------------


def test_as_dict_contains_mandatory_keys() -> None:
    d = _build_simple().as_dict()
    mandatory = {
        "ai_invocation_id", "trace_id", "ai_release_manifest_id",
        "caller_security_context_ref", "region", "provider", "model",
        "prompt_version", "authority_outcome", "invocation_outcome",
        "retrieval_refs", "tool_calls", "guardrail_results", "usage",
        "recorded_at",
    }
    assert mandatory.issubset(d.keys())


def test_as_dict_input_hash_absent_when_none() -> None:
    assert "input_hash" not in _build_simple().as_dict()


def test_as_dict_input_hash_present_when_set() -> None:
    b = _builder()
    b.set_input(b"data")
    assert "input_hash" in b.build().as_dict()


def test_as_dict_output_hash_absent_when_none() -> None:
    assert "output_hash" not in _build_simple().as_dict()


def test_as_dict_output_hash_present_when_set() -> None:
    b = _builder()
    b.set_output(b"output")
    assert "output_hash" in b.build().as_dict()


def test_as_dict_review_ref_absent_when_none() -> None:
    assert "review_ref" not in _build_simple().as_dict()


def test_as_dict_review_ref_present_when_set() -> None:
    prov = replace(BASE_PROVENANCE, authority_outcome=AuthorityOutcome.A3)
    b = _builder(provenance=prov, outcome=InvocationOutcome.REVIEW)
    b.set_review_ref("dec-001")
    assert b.build().as_dict()["review_ref"] == "dec-001"


def test_as_dict_authority_outcome_is_string_value() -> None:
    d = _build_simple().as_dict()
    assert d["authority_outcome"] == "A1"


def test_as_dict_invocation_outcome_is_string_value() -> None:
    d = _build_simple(outcome=InvocationOutcome.PROPOSE).as_dict()
    assert d["invocation_outcome"] == "PROPOSE"


def test_as_dict_retrieval_refs_is_sorted_list() -> None:
    b = _builder()
    b.add_retrieval_refs(["zzz", "aaa", "mmm"])
    d = b.build().as_dict()
    assert d["retrieval_refs"] == ["aaa", "mmm", "zzz"]


def test_as_dict_tool_calls_is_list_of_dicts() -> None:
    b = _builder()
    b.add_tool_call(BASE_TOOL_CALL)
    d = b.build().as_dict()
    assert isinstance(d["tool_calls"], list)
    assert d["tool_calls"][0]["call_id"] == "call-abc-001"


def test_as_dict_guardrail_results_is_list_of_dicts() -> None:
    b = _builder()
    b.add_guardrail(BASE_GUARDRAIL_PASS)
    d = b.build().as_dict()
    assert isinstance(d["guardrail_results"], list)
    assert d["guardrail_results"][0]["guardrail_id"] == "governance.authority"


def test_as_dict_usage_is_dict() -> None:
    b = _builder()
    b.set_usage(BASE_USAGE)
    assert isinstance(b.build().as_dict()["usage"], dict)


def test_as_dict_recorded_at_is_iso8601_string() -> None:
    d = _build_simple().as_dict()
    # Must be parseable as an ISO-8601 datetime.
    parsed = datetime.fromisoformat(d["recorded_at"])  # type: ignore[arg-type]
    assert parsed.tzinfo is not None


# ---------------------------------------------------------------------------
# Builder chaining (fluent API)
# ---------------------------------------------------------------------------


def test_builder_methods_return_builder_for_chaining() -> None:
    prov = replace(BASE_PROVENANCE, authority_outcome=AuthorityOutcome.A3)
    record = (
        InvocationEvidenceBuilder(
            provenance=prov,
            invocation_outcome=InvocationOutcome.REVIEW,
            trace_id="t-chain-001",
        )
        .set_input(b"payload")
        .set_output(b"response")
        .add_retrieval_ref("cit-001")
        .add_tool_call(BASE_TOOL_CALL)
        .add_guardrail(BASE_GUARDRAIL_PASS)
        .set_review_ref("dec-chain-001")
        .set_usage(BASE_USAGE)
        .build()
    )
    assert record.trace_id == "t-chain-001"
    assert record.review_ref == "dec-chain-001"
    assert record.input_hash is not None
    assert record.output_hash is not None
    assert "cit-001" in record.retrieval_refs
    assert len(record.tool_calls) == 1
    assert len(record.guardrail_results) == 1
    assert record.usage.latency_ms == 240


# ---------------------------------------------------------------------------
# Integration -- full pipeline: provenance -> governed context -> evidence
# ---------------------------------------------------------------------------


def test_integration_propose_pipeline() -> None:
    """
    Simulates: governance permits -> classifier runs -> evidence record built.
    The test verifies every field in the record can be traced back to the
    governed context.
    """
    prov = Provenance(
        use_case="sku-classification",
        model_profile="model:gemini-flash@2026.09",
        provider_profile="provider:eu-vertex",
        prompt_profile="prompt:classify@5",
        region="eu-west-2",
        data_class="RESTRICTED",
        risk_tier=RiskTier.T2,
        authority_outcome=AuthorityOutcome.A1,
        ai_train_version="0.7.0",
    )
    raw_input = b"Is SKU-9900 a telecommunications service?"
    raw_output = b"TAXABLE:TELECOM_SERVICE"

    record = (
        InvocationEvidenceBuilder(
            provenance=prov,
            invocation_outcome=InvocationOutcome.PROPOSE,
            trace_id="trace-integration-001",
        )
        .set_input(raw_input)
        .set_output(raw_output)
        .add_retrieval_ref("cite-sku-9900-a1b2")
        .add_guardrail(GuardrailResult("governance.kill_switch", "PASS"))
        .add_guardrail(GuardrailResult("governance.authority", "PASS"))
        .set_usage(UsageMetrics(latency_ms=310, input_tokens=64, output_tokens=8))
        .build()
    )

    # Provenance fields propagated correctly.
    assert record.ai_release_manifest_id == "0.7.0"
    assert record.caller_security_context_ref == "sku-classification"
    assert record.region == "eu-west-2"
    assert record.provider == "provider:eu-vertex"
    assert record.model == "model:gemini-flash@2026.09"
    assert record.prompt_version == "prompt:classify@5"
    assert record.authority_outcome is AuthorityOutcome.A1

    # Content hashes are correct, raw bytes not present.
    assert record.input_hash == hashlib.sha256(raw_input).hexdigest()
    assert record.output_hash == hashlib.sha256(raw_output).hexdigest()

    # Retrieval and tool data.
    assert "cite-sku-9900-a1b2" in record.retrieval_refs
    assert len(record.guardrail_results) == 2
    assert not record.any_guardrail_failed()

    # Outcome and usage.
    assert record.invocation_outcome is InvocationOutcome.PROPOSE
    assert record.review_ref is None
    assert record.usage.total_tokens == 72

    # as_dict round-trip.
    d = record.as_dict()
    assert d["authority_outcome"] == "A1"
    assert d["invocation_outcome"] == "PROPOSE"
    assert "input_hash" in d
    assert "output_hash" in d


def test_integration_blocked_invocation_pipeline() -> None:
    """
    Simulates: governance refuses -> BLOCK evidence record built.
    A blocked invocation has no output hash and no review_ref.
    """
    prov = Provenance(
        use_case="sku-classification",
        model_profile="model:gemini-flash@2026.09",
        provider_profile="provider:eu-vertex",
        prompt_profile="prompt:classify@5",
        region="eu-west-2",
        data_class="RESTRICTED",
        risk_tier=RiskTier.T2,
        authority_outcome=AuthorityOutcome.A1,
        ai_train_version="0.7.0",
    )

    record = (
        InvocationEvidenceBuilder(
            provenance=prov,
            invocation_outcome=InvocationOutcome.BLOCK,
            trace_id="trace-blocked-002",
        )
        .set_input(b"blocked request payload")
        .add_guardrail(
            GuardrailResult(
                "governance.kill_switch",
                "AI_KILL_SWITCH_ENGAGED",
                detail="global kill switch active",
            )
        )
        .build()
    )

    assert record.invocation_outcome is InvocationOutcome.BLOCK
    assert record.output_hash is None
    assert record.review_ref is None
    assert record.any_guardrail_failed() is True
    assert record.guardrail_results[0].verdict == "AI_KILL_SWITCH_ENGAGED"


def test_integration_review_pipeline() -> None:
    """
    Simulates: A3 action -> governance permits -> submitted to review queue ->
    ReviewDecision issued -> evidence record built with review_ref.
    """
    prov = replace(
        BASE_PROVENANCE,
        authority_outcome=AuthorityOutcome.A3,
        use_case="rate-change-review",
    )

    record = (
        InvocationEvidenceBuilder(
            provenance=prov,
            invocation_outcome=InvocationOutcome.REVIEW,
            trace_id="trace-review-003",
        )
        .set_input(b"rate change proposal")
        .set_output(b"proposed diff: +2%")
        .add_retrieval_ref("cite-rate-2026-aa01")
        .add_guardrail(GuardrailResult("governance.authority", "PASS"))
        .set_review_ref("dec-uuid-review-001")
        .set_usage(UsageMetrics(latency_ms=500, input_tokens=256, output_tokens=64))
        .build()
    )

    assert record.invocation_outcome is InvocationOutcome.REVIEW
    assert record.review_ref == "dec-uuid-review-001"
    assert record.authority_outcome is AuthorityOutcome.A3
    assert not record.any_guardrail_failed()
    d = record.as_dict()
    assert d["review_ref"] == "dec-uuid-review-001"
