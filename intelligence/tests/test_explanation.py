# Tests for ztax_gateway.explanation -- Customer Explanation Service (Chapter 17 s12).
from __future__ import annotations

import uuid
from datetime import datetime

import pytest

from ztax_gateway.citation import Citation, SourceChunk, combine
from ztax_gateway.explanation import (
    CitationBundle,
    CitationResolutionError,
    CustomerExplanationService,
    DecisionTrace,
    ExplanationError,
    ExplanationResult,
    ExplanationSegment,
    TraceStep,
    build_explanation,
)
from ztax_gateway.governance import GovernanceRefusedError, UseCase, UseCaseRegistry
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

UC_ID = "explanation"
REGION = "ap-southeast-2"


def _registry(suspended: bool = False) -> UseCaseRegistry:
    reg = UseCaseRegistry()
    reg.register(
        UseCase(
            use_case_id=UC_ID,
            owner="lane-k",
            description="Customer explanation",
            max_risk_tier=RiskTier.T1,
            max_authority=AuthorityOutcome.A0,
            permitted_regions=frozenset({REGION}),
            suspended=suspended,
        )
    )
    return reg


def _provenance(
    authority: AuthorityOutcome = AuthorityOutcome.A0,
    use_case: str = UC_ID,
    region: str = REGION,
) -> Provenance:
    return Provenance(
        use_case=use_case,
        model_profile="gpt-4o",
        provider_profile="openai",
        prompt_profile="explain-v1",
        region=region,
        data_class="TAX_DETERMINATION",
        risk_tier=RiskTier.T1,
        authority_outcome=authority,
        ai_train_version="v2.3.1",
    )


def _chunk(content: bytes = b"GST Act s.9 rate 10%") -> SourceChunk:
    return SourceChunk(
        source_id="statute://gst-act",
        content=content,
        byte_start=0,
        byte_end=len(content),
    )


def _citation(text: str = "GST Act s.9 rate 10%") -> Citation:
    return combine([_chunk(text.encode())])


def _bundle(*rule_ids: str) -> CitationBundle:
    bundle = CitationBundle()
    for rid in rule_ids:
        bundle.add(rid, _citation(f"source for {rid}"))
    return bundle


def _trace(
    *steps: TraceStep,
    decision_id: str | None = None,
    emitted: dict[str, str] | None = None,
    outcome: str = "AUTHORITATIVE",
) -> DecisionTrace:
    return DecisionTrace(
        decision_id=decision_id or str(uuid.uuid4()),
        business_key="bk-001",
        outcome=outcome,
        reason_code="STANDARD_RATE",
        event_time="2026-01-15T12:00:00.000000Z",
        emitted=emitted or {"TAX_VAT": "21.00"},
        steps=tuple(steps),
        bundle_id="bundle-v1",
        bundle_digest="abc123",
    )


def _emit_step(rule_id: str = "RULE-GST", version: str = "1.0") -> TraceStep:
    return TraceStep(
        node="emit_vat",
        op="EMIT",
        rule_semantic_id=rule_id,
        rule_version=version,
        output="21.00",
    )


def _refuse_step(rule_id: str = "RULE-GST-REFUSE", version: str = "1.0") -> TraceStep:
    return TraceStep(
        node="refuse_node",
        op="REFUSE",
        rule_semantic_id=rule_id,
        rule_version=version,
        output="",
    )


def _arithmetic_step() -> TraceStep:
    return TraceStep(
        node="add_node",
        op="ADD",
        args=("a", "b"),
        rule_semantic_id="",
        rule_version="",
        output="42.00",
    )


# ---------------------------------------------------------------------------
# Construction validation
# ---------------------------------------------------------------------------


def test_wrong_authority_raises() -> None:
    reg = _registry()
    with pytest.raises(ExplanationError, match="authority must be A0"):
        CustomerExplanationService(registry=reg, provenance=_provenance(AuthorityOutcome.A1))


def test_a0_authority_accepted() -> None:
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance(AuthorityOutcome.A0))
    assert svc is not None


# ---------------------------------------------------------------------------
# Governance gate
# ---------------------------------------------------------------------------


def test_unknown_use_case_refused() -> None:
    reg = _registry()
    svc = CustomerExplanationService(
        registry=reg, provenance=_provenance(use_case="no-such-uc")
    )
    t = _trace(_emit_step())
    b = _bundle("RULE-GST")
    with pytest.raises(GovernanceRefusedError):
        svc.explain(trace=t, citations=b)


def test_global_kill_switch_engaged() -> None:
    reg = _registry()
    reg.engage_global_kill()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    with pytest.raises(GovernanceRefusedError):
        svc.explain(trace=_trace(_emit_step()), citations=_bundle("RULE-GST"))


def test_suspended_use_case_refused() -> None:
    reg = _registry(suspended=True)
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    with pytest.raises(GovernanceRefusedError):
        svc.explain(trace=_trace(_emit_step()), citations=_bundle("RULE-GST"))


def test_wrong_region_refused() -> None:
    reg = _registry()
    svc = CustomerExplanationService(
        registry=reg, provenance=_provenance(region="us-east-1")
    )
    with pytest.raises(GovernanceRefusedError):
        svc.explain(trace=_trace(_emit_step()), citations=_bundle("RULE-GST"))


# ---------------------------------------------------------------------------
# Happy path: EMIT step
# ---------------------------------------------------------------------------


def test_happy_path_emit_step() -> None:
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    trace = _trace(_emit_step("RULE-GST"), emitted={"TAX_VAT": "21.00"})
    bundle = _bundle("RULE-GST")
    result = svc.explain(trace=trace, citations=bundle)

    assert isinstance(result, ExplanationResult)
    assert result.decision_id == trace.decision_id
    assert result.business_key == trace.business_key
    assert result.outcome == "AUTHORITATIVE"
    assert result.authority_outcome == AuthorityOutcome.A0
    assert len(result.segments) == 1
    assert len(result.provenance_spans) == 1
    assert isinstance(result.generated_at, datetime)


def test_emit_segment_fields() -> None:
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    step = _emit_step("RULE-GST", "1.2")
    trace = _trace(step)
    bundle = _bundle("RULE-GST")
    result = svc.explain(trace=trace, citations=bundle)

    seg: ExplanationSegment = result.segments[0]
    assert seg.rule_semantic_id == "RULE-GST"
    assert seg.rule_version == "1.2"
    assert seg.op == "EMIT"
    assert seg.output == "21.00"
    assert len(seg.citation_id) > 0
    assert "statute://gst-act" in seg.source_ids


# ---------------------------------------------------------------------------
# Happy path: REFUSE step
# ---------------------------------------------------------------------------


def test_happy_path_refuse_step() -> None:
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    trace = _trace(_refuse_step("RULE-GST-REFUSE"), outcome="UNSUPPORTED")
    bundle = _bundle("RULE-GST-REFUSE")
    result = svc.explain(trace=trace, citations=bundle)

    assert len(result.segments) == 1
    seg = result.segments[0]
    assert seg.op == "REFUSE"
    assert "UNSUPPORTED" in seg.narrative


# ---------------------------------------------------------------------------
# Arithmetic steps are not narrated
# ---------------------------------------------------------------------------


def test_arithmetic_steps_skipped_emit_narrated() -> None:
    """ADD/SUB/MUL steps must not appear as segments; only EMIT is narrated."""
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    trace = _trace(_arithmetic_step(), _emit_step("RULE-GST"))
    bundle = _bundle("RULE-GST")
    result = svc.explain(trace=trace, citations=bundle)
    assert len(result.segments) == 1
    assert result.segments[0].op == "EMIT"


def test_multiple_emit_steps_all_narrated() -> None:
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    steps = (
        _emit_step("RULE-A"),
        _arithmetic_step(),
        _emit_step("RULE-B"),
    )
    trace = _trace(*steps, emitted={"TAX_VAT": "21.00", "TAX_BASE": "210.00"})
    bundle = _bundle("RULE-A", "RULE-B")
    result = svc.explain(trace=trace, citations=bundle)
    assert len(result.segments) == 2
    rule_ids = {s.rule_semantic_id for s in result.segments}
    assert rule_ids == {"RULE-A", "RULE-B"}


# ---------------------------------------------------------------------------
# Fail-closed: CitationResolutionError (ZTAX-AIGOV-REQ-0046)
# ---------------------------------------------------------------------------


def test_missing_citation_causes_abstention() -> None:
    """A material step with no citation must cause CitationResolutionError."""
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    trace = _trace(_emit_step("RULE-MISSING"))
    empty_bundle = CitationBundle()  # nothing registered
    with pytest.raises(CitationResolutionError) as exc_info:
        svc.explain(trace=trace, citations=empty_bundle)
    assert exc_info.value.rule_semantic_id == "RULE-MISSING"


def test_citation_resolution_error_message_mentions_abstain() -> None:
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    trace = _trace(_emit_step("RULE-X"))
    bundle = CitationBundle()
    with pytest.raises(CitationResolutionError, match="ZTAX-AIGOV-REQ-0046"):
        svc.explain(trace=trace, citations=bundle)


# ---------------------------------------------------------------------------
# Empty / degenerate traces
# ---------------------------------------------------------------------------


def test_empty_steps_raises() -> None:
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    trace = _trace()
    with pytest.raises(ExplanationError, match="empty trace"):
        svc.explain(trace=trace, citations=CitationBundle())


def test_only_arithmetic_steps_raises() -> None:
    """A trace with only arithmetic ops (no EMIT/REFUSE) cannot be narrated."""
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    trace = _trace(_arithmetic_step(), _arithmetic_step())
    with pytest.raises(ExplanationError, match="no EMIT or REFUSE steps"):
        svc.explain(trace=trace, citations=CitationBundle())


def test_emit_step_without_rule_id_raises() -> None:
    """A material step with no rule_semantic_id is an incomplete trace."""
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    step = TraceStep(node="x", op="EMIT", rule_semantic_id="", rule_version="1.0", output="1.00")
    trace = _trace(step)
    with pytest.raises(ExplanationError, match="no rule_semantic_id"):
        svc.explain(trace=trace, citations=CitationBundle())


# ---------------------------------------------------------------------------
# No arithmetic: amounts reproduced verbatim (ZTAX-AI-REQ-0057)
# ---------------------------------------------------------------------------


def test_emitted_summary_verbatim() -> None:
    """Emitted amounts must appear verbatim in the summary; no recomputation."""
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    trace = _trace(_emit_step("RULE-GST"), emitted={"TAX_VAT": "999.99"})
    bundle = _bundle("RULE-GST")
    result = svc.explain(trace=trace, citations=bundle)
    assert "999.99" in result.emitted_summary


def test_segment_output_verbatim() -> None:
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    step = TraceStep(
        node="emit", op="EMIT", rule_semantic_id="RULE-GST",
        rule_version="1.0", output="88888.88"
    )
    trace = _trace(step)
    bundle = _bundle("RULE-GST")
    result = svc.explain(trace=trace, citations=bundle)
    assert result.segments[0].output == "88888.88"


# ---------------------------------------------------------------------------
# Schema-distinct (ZTAX-DOM-REQ-0045)
# ---------------------------------------------------------------------------


def test_result_is_not_decision_type() -> None:
    """ExplanationResult must be its own distinct type."""
    from ztax_gateway.explanation import ExplanationResult
    assert not hasattr(ExplanationResult, "components")  # not a Decision
    assert not hasattr(ExplanationResult, "proposals")   # not a ClassificationRecord
    assert not hasattr(ExplanationResult, "review_item_id")  # not a ReviewItem
    assert hasattr(ExplanationResult, "explanation_id")


def test_result_is_frozen() -> None:
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    trace = _trace(_emit_step("RULE-GST"))
    bundle = _bundle("RULE-GST")
    result = svc.explain(trace=trace, citations=bundle)
    with pytest.raises((AttributeError, TypeError)):
        result.decision_id = "tampered"  # type: ignore[misc]


# ---------------------------------------------------------------------------
# CitationBundle
# ---------------------------------------------------------------------------


def test_bundle_add_duplicate_raises() -> None:
    bundle = CitationBundle()
    bundle.add("RULE-A", _citation())
    with pytest.raises(ExplanationError, match="already registered"):
        bundle.add("RULE-A", _citation())


def test_bundle_empty_id_raises() -> None:
    bundle = CitationBundle()
    with pytest.raises(ExplanationError, match="empty rule_semantic_id"):
        bundle.add("", _citation())


def test_bundle_resolve_none_for_missing() -> None:
    bundle = CitationBundle()
    assert bundle.resolve("NONEXISTENT") is None


def test_bundle_len() -> None:
    bundle = _bundle("A", "B", "C")
    assert len(bundle) == 3


# ---------------------------------------------------------------------------
# build_explanation convenience function
# ---------------------------------------------------------------------------


def test_build_explanation_convenience() -> None:
    reg = _registry()
    prov = _provenance()
    trace = _trace(_emit_step("RULE-GST"))
    bundle = _bundle("RULE-GST")
    result = build_explanation(trace=trace, citations=bundle, registry=reg, provenance=prov)
    assert isinstance(result, ExplanationResult)


def test_build_explanation_wrong_authority_raises() -> None:
    reg = _registry()
    prov = _provenance(AuthorityOutcome.A2)
    trace = _trace(_emit_step("RULE-GST"))
    bundle = _bundle("RULE-GST")
    with pytest.raises(ExplanationError):
        build_explanation(trace=trace, citations=bundle, registry=reg, provenance=prov)


# ---------------------------------------------------------------------------
# ProvenanceSpans
# ---------------------------------------------------------------------------


def test_provenance_spans_match_segments() -> None:
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    trace = _trace(_emit_step("RULE-GST"))
    bundle = _bundle("RULE-GST")
    result = svc.explain(trace=trace, citations=bundle)
    assert len(result.provenance_spans) == len(result.segments)
    for span, seg in zip(result.provenance_spans, result.segments, strict=False):
        assert span.output_id == seg.segment_id
        assert span.citation.citation_id == seg.citation_id


# ---------------------------------------------------------------------------
# Explanation ID is unique per run
# ---------------------------------------------------------------------------


def test_explanation_ids_unique() -> None:
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    trace = _trace(_emit_step("RULE-GST"))
    bundle = _bundle("RULE-GST")
    r1 = svc.explain(trace=trace, citations=bundle)
    r2 = svc.explain(trace=trace, citations=bundle)
    assert r1.explanation_id != r2.explanation_id


# ---------------------------------------------------------------------------
# authority_outcome always A0
# ---------------------------------------------------------------------------


def test_result_authority_always_a0() -> None:
    reg = _registry()
    svc = CustomerExplanationService(registry=reg, provenance=_provenance())
    trace = _trace(_emit_step("RULE-GST"))
    bundle = _bundle("RULE-GST")
    result = svc.explain(trace=trace, citations=bundle)
    assert result.authority_outcome == AuthorityOutcome.A0

