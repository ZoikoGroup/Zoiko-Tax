
"""Tests for the Change Intelligence Engine.
 
Chapter 17 §8.1 and §37 of the ZoikoTax Master Specification.
 
Mirrors the discipline of ``test_human_review.py`` and ``test_evaluation.py``:
 
- Plain functions; no test classes.
- ``dataclasses.replace`` for variants so a typo in a field name is a
  type error here, not a silent default.
- Every public type and every public method is exercised.
- Every error path is explicitly asserted so a regression surfaces as a
  test failure, not as a silent wrong answer in an audit log.
 
Test groups
-----------
  ChangeType              -- 8 spec values (RATE/DEFINITION/EFFECTIVE_DATE/
                             FORM/THRESHOLD/OBLIGATION/PROCEDURE/OTHER)
  CandidateStatus         -- enum values, state-machine transitions
  AuthorityLevel          -- enum values
  EffectiveDateEvidence   -- construction, validation, as_dict
  ChangeCandidateError    -- construction, reason attribute
  ChangeCandidate         -- construction, immutability, deterministic ID,
                             dual-citation (REQ-0048), validation, as_dict,
                             citation property, new optional fields
  CandidateStatusRecord   -- PROPOSED -> VALIDATED -> PROMOTED_TO_CONTENT_CHANGE,
                             PROPOSED -> REJECTED, invalid transitions refused
  CorpusEntry             -- construction, immutability, validation
  ChangeIntelligenceEngine.__init__  -- happy path, bad min_confidence
  extract_changes         -- governance gate, empty corpora, additions,
                             deletions, modifications, rate/threshold/
                             effective-date heuristics, confidence filtering,
                             identical corpora, ai_release_manifest propagation,
                             dual-citation on modifications
  to_review_item          -- happy path, summary format, duplicate guard,
                             EvidencePanel attached, priority forwarded,
                             governance refusal
  Integration             -- full pipeline: extract -> submit -> decide(APPROVE)
                             full pipeline: extract -> submit -> decide(MODIFY)
                             full pipeline: extract -> decide(ESCALATE) -> decide(REJECT)
"""
 
from __future__ import annotations
 
from dataclasses import FrozenInstanceError
from datetime import UTC, date, datetime
 
import pytest
 
from ztax_gateway.change_intelligence import (
    AuthorityLevel,
    CandidateStatus,
    CandidateStatusRecord,
    ChangeCandidate,
    ChangeCandidateError,
    ChangeIntelligenceEngine,
    ChangeType,
    CorpusEntry,
    EffectiveDateEvidence,
)
from ztax_gateway.citation import Citation, SourceChunk, combine
from ztax_gateway.governance import GovernanceRefusedError, UseCase, UseCaseRegistry
from ztax_gateway.human_review import (
    ReviewItem,
    ReviewOutcome,
    ReviewPriority,
    ReviewQueue,
    ReviewStatus,
)
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier
 
# ---------------------------------------------------------------------------
# Shared fixtures
# ---------------------------------------------------------------------------
 
BASE_USE_CASE = UseCase(
    use_case_id="change-intel",
    owner="lane-l",
    description="Change Intelligence extraction",
    max_risk_tier=RiskTier.T2,
    max_authority=AuthorityOutcome.A2,
    permitted_regions=frozenset({"eu-west-1"}),
)
 
BASE_PROVENANCE = Provenance(
    use_case="change-intel",
    model_profile="model:change-intel@2026.09",
    provider_profile="provider:eu-hosted",
    prompt_profile="prompt:change@1",
    region="eu-west-1",
    data_class="INTERNAL",
    risk_tier=RiskTier.T1,
    authority_outcome=AuthorityOutcome.A1,
    ai_train_version="0.5.0",
)
 
 
def _registry(*extra: UseCase) -> UseCaseRegistry:
    return UseCaseRegistry([BASE_USE_CASE, *extra])
 
 
def _citation(
    source_id: str = "bulletin-2026-01",
    content: bytes = b"VAT rate section 12",
) -> Citation:
    chunk = SourceChunk(
        source_id=source_id,
        content=content,
        byte_start=0,
        byte_end=len(content),
    )
    return combine([chunk])
 
 
def _entry(
    document_id: str = "bulletin-2026-01",
    section_id: str = "§12",
    content: str = "VAT standard rate: 20%",
    citation: Citation | None = None,
) -> CorpusEntry:
    return CorpusEntry(
        document_id=document_id,
        section_id=section_id,
        content=content,
        citation=citation or _citation(source_id=document_id, content=content.encode()),
    )
 
 
def _engine(
    min_confidence: float = 0.0,
    ai_release_manifest: str = "",
) -> ChangeIntelligenceEngine:
    return ChangeIntelligenceEngine(
        min_confidence=min_confidence,
        ai_release_manifest=ai_release_manifest,
    )
 
 
def _candidate(
    source_document_id: str = "bulletin-2026-01",
    target_rule_or_section: str = "§12",
    change_type: ChangeType = ChangeType.RATE,
    proposed_diff: str = "VAT standard rate: 20% -> 23%",
    confidence_score: float = 0.9,
    source_snapshot_before: Citation | None = None,
    source_snapshot_after: Citation | None = None,
    provenance: Provenance = BASE_PROVENANCE,
    authority_level: AuthorityLevel = AuthorityLevel.PRIMARY,
    impact_targets: frozenset[str] = frozenset(),
    ai_release_manifest: str = "",
    review_priority: ReviewPriority = ReviewPriority.NORMAL,
    effective_date_evidence: EffectiveDateEvidence | None = None,
) -> ChangeCandidate:
    # Provide at least one citation when both are None.
    if source_snapshot_before is None and source_snapshot_after is None:
        source_snapshot_after = _citation()
    return ChangeCandidate(
        source_document_id=source_document_id,
        target_rule_or_section=target_rule_or_section,
        change_type=change_type,
        proposed_diff=proposed_diff,
        confidence_score=confidence_score,
        provenance=provenance,
        source_snapshot_before=source_snapshot_before,
        source_snapshot_after=source_snapshot_after,
        authority_level=authority_level,
        impact_targets=impact_targets,
        ai_release_manifest=ai_release_manifest,
        review_priority=review_priority,
        effective_date_evidence=effective_date_evidence,
    )
 
 
# ===========================================================================
# ChangeType
# ===========================================================================
 
 
def test_change_type_has_eight_members() -> None:
    assert len(ChangeType) == 8
 
 
def test_change_type_rate_value() -> None:
    assert ChangeType.RATE == "RATE"
 
 
def test_change_type_definition_value() -> None:
    assert ChangeType.DEFINITION == "DEFINITION"
 
 
def test_change_type_effective_date_value() -> None:
    assert ChangeType.EFFECTIVE_DATE == "EFFECTIVE_DATE"
 
 
def test_change_type_form_value() -> None:
    assert ChangeType.FORM == "FORM"
 
 
def test_change_type_threshold_value() -> None:
    assert ChangeType.THRESHOLD == "THRESHOLD"
 
 
def test_change_type_obligation_value() -> None:
    assert ChangeType.OBLIGATION == "OBLIGATION"
 
 
def test_change_type_procedure_value() -> None:
    assert ChangeType.PROCEDURE == "PROCEDURE"
 
 
def test_change_type_other_value() -> None:
    assert ChangeType.OTHER == "OTHER"
 
 
def test_change_type_is_str() -> None:
    for ct in ChangeType:
        assert isinstance(ct, str)
 
 
# ===========================================================================
# CandidateStatus
# ===========================================================================
 
 
def test_candidate_status_proposed_value() -> None:
    assert CandidateStatus.PROPOSED == "PROPOSED"
 
 
def test_candidate_status_validated_value() -> None:
    assert CandidateStatus.VALIDATED == "VALIDATED"
 
 
def test_candidate_status_rejected_value() -> None:
    assert CandidateStatus.REJECTED == "REJECTED"
 
 
def test_candidate_status_promoted_value() -> None:
    assert CandidateStatus.PROMOTED_TO_CONTENT_CHANGE == "PROMOTED_TO_CONTENT_CHANGE"
 
 
# ===========================================================================
# AuthorityLevel
# ===========================================================================
 
 
def test_authority_level_primary_value() -> None:
    assert AuthorityLevel.PRIMARY == "PRIMARY"
 
 
def test_authority_level_secondary_value() -> None:
    assert AuthorityLevel.SECONDARY == "SECONDARY"
 
 
def test_authority_level_guidance_value() -> None:
    assert AuthorityLevel.GUIDANCE == "GUIDANCE"
 
 
def test_authority_level_other_value() -> None:
    assert AuthorityLevel.OTHER == "OTHER"
 
 
def test_authority_level_has_four_members() -> None:
    assert len(AuthorityLevel) == 4
 
 
# ===========================================================================
# EffectiveDateEvidence
# ===========================================================================
 
 
def test_effective_date_evidence_with_date_and_text() -> None:
    ev = EffectiveDateEvidence(
        candidate_date=date(2026, 1, 1),
        date_confidence=0.9,
        source_text="with effect from 1 January 2026",
    )
    assert ev.candidate_date == date(2026, 1, 1)
    assert ev.date_confidence == 0.9
    assert ev.source_text == "with effect from 1 January 2026"
 
 
def test_effective_date_evidence_none_date_requires_zero_confidence() -> None:
    ev = EffectiveDateEvidence(candidate_date=None, date_confidence=0.0)
    assert ev.candidate_date is None
 
 
def test_effective_date_evidence_none_date_nonzero_confidence_refused() -> None:
    with pytest.raises(ChangeCandidateError, match="date_confidence"):
        EffectiveDateEvidence(candidate_date=None, date_confidence=0.5)
 
 
def test_effective_date_evidence_confidence_above_one_refused() -> None:
    with pytest.raises(ChangeCandidateError, match="date_confidence"):
        EffectiveDateEvidence(candidate_date=date(2026, 1, 1), date_confidence=1.01)
 
 
def test_effective_date_evidence_confidence_below_zero_refused() -> None:
    with pytest.raises(ChangeCandidateError, match="date_confidence"):
        EffectiveDateEvidence(candidate_date=date(2026, 1, 1), date_confidence=-0.1)
 
 
def test_effective_date_evidence_as_dict() -> None:
    d = EffectiveDateEvidence(
        candidate_date=date(2026, 4, 1),
        date_confidence=0.85,
        source_text="applies from 1 April 2026",
    ).as_dict()
    assert d["candidate_date"] == "2026-04-01"
    assert d["date_confidence"] == 0.85
    assert d["source_text"] == "applies from 1 April 2026"
 
 
def test_effective_date_evidence_as_dict_none_date() -> None:
    d = EffectiveDateEvidence(candidate_date=None, date_confidence=0.0).as_dict()
    assert d["candidate_date"] is None
 
 
def test_effective_date_evidence_is_frozen() -> None:
    ev = EffectiveDateEvidence(candidate_date=date(2026, 1, 1), date_confidence=0.8)
    with pytest.raises(FrozenInstanceError):
        ev.date_confidence = 0.0  # type: ignore[misc]
 
 
# ===========================================================================
# ChangeCandidateError
# ===========================================================================
 
 
def test_change_candidate_error_stores_reason() -> None:
    err = ChangeCandidateError("something went wrong")
    assert err.reason == "something went wrong"
    assert str(err) == "something went wrong"
 
 
def test_change_candidate_error_is_exception() -> None:
    with pytest.raises(ChangeCandidateError):
        raise ChangeCandidateError("test")
 
 
# ===========================================================================
# ChangeCandidate — construction
# ===========================================================================
 
 
def test_change_candidate_construction_happy_path() -> None:
    c = _candidate()
    assert c.source_document_id == "bulletin-2026-01"
    assert c.target_rule_or_section == "§12"
    assert c.change_type is ChangeType.RATE
    assert c.proposed_diff == "VAT standard rate: 20% -> 23%"
    assert c.confidence_score == 0.9
    assert c.candidate_id  # non-empty
    assert c.extracted_at is not None
 
 
def test_change_candidate_candidate_id_is_16_hex_chars() -> None:
    c = _candidate()
    assert len(c.candidate_id) == 16
    assert all(ch in "0123456789abcdef" for ch in c.candidate_id)
 
 
def test_change_candidate_id_is_deterministic() -> None:
    cit = _citation()
    c1 = _candidate(source_snapshot_after=cit)
    c2 = _candidate(source_snapshot_after=cit)
    assert c1.candidate_id == c2.candidate_id
 
 
def test_change_candidate_id_changes_with_diff() -> None:
    c1 = _candidate(proposed_diff="rate: 20% -> 23%")
    c2 = _candidate(proposed_diff="rate: 20% -> 25%")
    assert c1.candidate_id != c2.candidate_id
 
 
def test_change_candidate_id_changes_with_source() -> None:
    c1 = _candidate(source_document_id="bulletin-A")
    c2 = _candidate(source_document_id="bulletin-B")
    assert c1.candidate_id != c2.candidate_id
 
 
def test_change_candidate_id_changes_with_change_type() -> None:
    c1 = _candidate(change_type=ChangeType.RATE)
    c2 = _candidate(change_type=ChangeType.DEFINITION)
    assert c1.candidate_id != c2.candidate_id
 
 
def test_change_candidate_id_changes_with_target() -> None:
    c1 = _candidate(target_rule_or_section="§12")
    c2 = _candidate(target_rule_or_section="§13")
    assert c1.candidate_id != c2.candidate_id
 
 
def test_change_candidate_extracted_at_is_utc() -> None:
    c = _candidate()
    assert c.extracted_at.tzinfo is not None
 
 
def test_change_candidate_all_change_types_accepted() -> None:
    for ct in ChangeType:
        c = _candidate(change_type=ct)
        assert c.change_type is ct
 
 
def test_change_candidate_confidence_zero_accepted() -> None:
    c = _candidate(confidence_score=0.0)
    assert c.confidence_score == 0.0
 
 
def test_change_candidate_confidence_one_accepted() -> None:
    c = _candidate(confidence_score=1.0)
    assert c.confidence_score == 1.0
 
 
# ===========================================================================
# ChangeCandidate — dual-citation (ZTAX-AI-REQ-0048)
# ===========================================================================
 
 
def test_change_candidate_addition_has_only_after_citation() -> None:
    after = _citation(content=b"new provision text")
    c = _candidate(source_snapshot_before=None, source_snapshot_after=after)
    assert c.source_snapshot_before is None
    assert c.source_snapshot_after is after
 
 
def test_change_candidate_deletion_has_only_before_citation() -> None:
    before = _citation(content=b"old provision text")
    c = _candidate(source_snapshot_before=before, source_snapshot_after=None)
    assert c.source_snapshot_before is before
    assert c.source_snapshot_after is None
 
 
def test_change_candidate_modification_has_both_citations() -> None:
    before = _citation(content=b"rate: 20%")
    after = _citation(content=b"rate: 23%")
    c = _candidate(source_snapshot_before=before, source_snapshot_after=after)
    assert c.source_snapshot_before is before
    assert c.source_snapshot_after is after
 
 
def test_change_candidate_no_citation_at_all_refused() -> None:
    with pytest.raises(ChangeCandidateError, match="ZTAX-AI-REQ-0048"):
        ChangeCandidate(
            source_document_id="doc-a",
            target_rule_or_section="§1",
            change_type=ChangeType.OTHER,
            proposed_diff="some diff",
            confidence_score=0.9,
            provenance=BASE_PROVENANCE,
            source_snapshot_before=None,
            source_snapshot_after=None,
        )
 
 
def test_change_candidate_citation_property_returns_after_when_present() -> None:
    before = _citation(content=b"before")
    after = _citation(content=b"after")
    c = _candidate(source_snapshot_before=before, source_snapshot_after=after)
    assert c.citation is after
 
 
def test_change_candidate_citation_property_returns_before_for_deletion() -> None:
    before = _citation(content=b"before")
    c = _candidate(source_snapshot_before=before, source_snapshot_after=None)
    assert c.citation is before
 
 
# ===========================================================================
# ChangeCandidate — optional spec fields
# ===========================================================================
 
 
def test_change_candidate_effective_date_evidence_stored() -> None:
    ev = EffectiveDateEvidence(
        candidate_date=date(2026, 1, 1),
        date_confidence=0.9,
        source_text="with effect from 1 January 2026",
    )
    c = _candidate(effective_date_evidence=ev)
    assert c.effective_date_evidence is ev
 
 
def test_change_candidate_default_effective_date_evidence_is_none() -> None:
    c = _candidate()
    assert c.effective_date_evidence is None
 
 
def test_change_candidate_authority_level_stored() -> None:
    c = _candidate(authority_level=AuthorityLevel.SECONDARY)
    assert c.authority_level is AuthorityLevel.SECONDARY
 
 
def test_change_candidate_default_authority_level_is_other() -> None:
    c = ChangeCandidate(
        source_document_id="doc",
        target_rule_or_section="§1",
        change_type=ChangeType.OTHER,
        proposed_diff="diff",
        confidence_score=0.5,
        provenance=BASE_PROVENANCE,
        source_snapshot_after=_citation(),
    )
    assert c.authority_level is AuthorityLevel.OTHER
 
 
def test_change_candidate_impact_targets_stored() -> None:
    targets = frozenset({"EU-VAT/standard-rate", "EU-VAT/reduced-rate"})
    c = _candidate(impact_targets=targets)
    assert c.impact_targets == targets
 
 
def test_change_candidate_default_impact_targets_is_empty_frozenset() -> None:
    c = _candidate()
    assert c.impact_targets == frozenset()
 
 
def test_change_candidate_ai_release_manifest_stored() -> None:
    c = _candidate(ai_release_manifest="extractor:ci-engine@2026.09.25")
    assert c.ai_release_manifest == "extractor:ci-engine@2026.09.25"
 
 
def test_change_candidate_default_ai_release_manifest_is_empty() -> None:
    c = _candidate()
    assert c.ai_release_manifest == ""
 
 
def test_change_candidate_review_priority_stored() -> None:
    c = _candidate(review_priority=ReviewPriority.CRITICAL)
    assert c.review_priority is ReviewPriority.CRITICAL
 
 
def test_change_candidate_default_review_priority_is_normal() -> None:
    c = _candidate()
    assert c.review_priority is ReviewPriority.NORMAL
 
 
# ===========================================================================
# ChangeCandidate — validation
# ===========================================================================
 
 
def test_change_candidate_empty_source_document_id_raises() -> None:
    with pytest.raises(ChangeCandidateError, match="source_document_id"):
        _candidate(source_document_id="")
 
 
def test_change_candidate_empty_target_raises() -> None:
    with pytest.raises(ChangeCandidateError, match="target_rule_or_section"):
        _candidate(target_rule_or_section="")
 
 
def test_change_candidate_empty_proposed_diff_raises() -> None:
    with pytest.raises(ChangeCandidateError, match="proposed_diff"):
        _candidate(proposed_diff="")
 
 
def test_change_candidate_confidence_below_zero_raises() -> None:
    with pytest.raises(ChangeCandidateError, match="confidence_score"):
        _candidate(confidence_score=-0.1)
 
 
def test_change_candidate_confidence_above_one_raises() -> None:
    with pytest.raises(ChangeCandidateError, match="confidence_score"):
        _candidate(confidence_score=1.01)
 
 
# ===========================================================================
# ChangeCandidate — immutability
# ===========================================================================
 
 
def test_change_candidate_is_frozen() -> None:
    c = _candidate()
    with pytest.raises(FrozenInstanceError):
        c.proposed_diff = "mutated"  # type: ignore[misc]
 
 
def test_change_candidate_candidate_id_cannot_be_set() -> None:
    c = _candidate()
    with pytest.raises(FrozenInstanceError):
        c.candidate_id = "hacked"  # type: ignore[misc]
 
 
def test_change_candidate_source_snapshot_before_cannot_be_set() -> None:
    c = _candidate()
    with pytest.raises(FrozenInstanceError):
        c.source_snapshot_before = _citation()  # type: ignore[misc]
 
 
def test_change_candidate_source_snapshot_after_cannot_be_set() -> None:
    c = _candidate()
    with pytest.raises(FrozenInstanceError):
        c.source_snapshot_after = None  # type: ignore[misc]
 
 
# ===========================================================================
# ChangeCandidate — as_dict
# ===========================================================================
 
 
def test_change_candidate_as_dict_contains_required_keys() -> None:
    c = _candidate()
    d = c.as_dict()
    for key in (
        "candidate_id",
        "source_document_id",
        "target_rule_or_section",
        "change_type",
        "proposed_diff",
        "confidence_score",
        "authority_level",
        "review_priority",
        "extracted_at",
        "use_case",
        "region",
        "authority_outcome",
    ):
        assert key in d, f"Missing key {key!r} in as_dict()"
 
 
def test_change_candidate_as_dict_change_type_is_string() -> None:
    c = _candidate(change_type=ChangeType.THRESHOLD)
    assert c.as_dict()["change_type"] == "THRESHOLD"
 
 
def test_change_candidate_as_dict_snapshot_before_id() -> None:
    before = _citation(content=b"before state")
    after = _citation(content=b"after state")
    c = _candidate(source_snapshot_before=before, source_snapshot_after=after)
    d = c.as_dict()
    assert d["source_snapshot_before"] == before.citation_id
    assert d["source_snapshot_after"] == after.citation_id
 
 
def test_change_candidate_as_dict_omits_before_for_addition() -> None:
    c = _candidate(source_snapshot_before=None, source_snapshot_after=_citation())
    d = c.as_dict()
    assert "source_snapshot_before" not in d
    assert "source_snapshot_after" in d
 
 
def test_change_candidate_as_dict_omits_after_for_deletion() -> None:
    c = _candidate(source_snapshot_before=_citation(), source_snapshot_after=None)
    d = c.as_dict()
    assert "source_snapshot_after" not in d
    assert "source_snapshot_before" in d
 
 
def test_change_candidate_as_dict_includes_effective_date_evidence() -> None:
    ev = EffectiveDateEvidence(
        candidate_date=date(2026, 4, 1),
        date_confidence=0.8,
        source_text="applies from 1 April 2026",
    )
    c = _candidate(effective_date_evidence=ev)
    d = c.as_dict()
    assert "effective_date_evidence" in d
    assert d["effective_date_evidence"]["candidate_date"] == "2026-04-01"  # type: ignore[index]
 
 
def test_change_candidate_as_dict_omits_effective_date_when_none() -> None:
    c = _candidate(effective_date_evidence=None)
    assert "effective_date_evidence" not in c.as_dict()
 
 
def test_change_candidate_as_dict_includes_impact_targets_sorted() -> None:
    targets = frozenset({"EU-VAT/standard-rate", "EU-VAT/reduced-rate"})
    c = _candidate(impact_targets=targets)
    d = c.as_dict()
    assert d["impact_targets"] == ["EU-VAT/reduced-rate", "EU-VAT/standard-rate"]
 
 
def test_change_candidate_as_dict_omits_empty_impact_targets() -> None:
    c = _candidate(impact_targets=frozenset())
    assert "impact_targets" not in c.as_dict()
 
 
def test_change_candidate_as_dict_includes_ai_release_manifest() -> None:
    c = _candidate(ai_release_manifest="extractor:v1")
    assert c.as_dict()["ai_release_manifest"] == "extractor:v1"
 
 
def test_change_candidate_as_dict_omits_empty_ai_release_manifest() -> None:
    c = _candidate(ai_release_manifest="")
    assert "ai_release_manifest" not in c.as_dict()
 
 
def test_change_candidate_as_dict_use_case_matches_provenance() -> None:
    c = _candidate()
    assert c.as_dict()["use_case"] == BASE_PROVENANCE.use_case
 
 
# ===========================================================================
# CandidateStatusRecord
# ===========================================================================
 
 
def test_candidate_status_record_starts_as_proposed() -> None:
    c = _candidate()
    record = CandidateStatusRecord(c)
    assert record.status is CandidateStatus.PROPOSED
 
 
def test_candidate_status_record_transition_proposed_to_validated() -> None:
    record = CandidateStatusRecord(_candidate())
    record.transition(CandidateStatus.VALIDATED)
    assert record.status is CandidateStatus.VALIDATED
 
 
def test_candidate_status_record_transition_proposed_to_rejected() -> None:
    record = CandidateStatusRecord(_candidate())
    record.transition(CandidateStatus.REJECTED)
    assert record.status is CandidateStatus.REJECTED
 
 
def test_candidate_status_record_transition_validated_to_promoted() -> None:
    record = CandidateStatusRecord(_candidate())
    record.transition(CandidateStatus.VALIDATED)
    record.transition(CandidateStatus.PROMOTED_TO_CONTENT_CHANGE)
    assert record.status is CandidateStatus.PROMOTED_TO_CONTENT_CHANGE
 
 
def test_candidate_status_record_transition_validated_to_rejected() -> None:
    record = CandidateStatusRecord(_candidate())
    record.transition(CandidateStatus.VALIDATED)
    record.transition(CandidateStatus.REJECTED)
    assert record.status is CandidateStatus.REJECTED
 
 
def test_candidate_status_record_rejected_is_terminal() -> None:
    record = CandidateStatusRecord(_candidate())
    record.transition(CandidateStatus.REJECTED)
    with pytest.raises(ChangeCandidateError, match="cannot transition"):
        record.transition(CandidateStatus.VALIDATED)
 
 
def test_candidate_status_record_promoted_is_terminal() -> None:
    record = CandidateStatusRecord(_candidate())
    record.transition(CandidateStatus.VALIDATED)
    record.transition(CandidateStatus.PROMOTED_TO_CONTENT_CHANGE)
    with pytest.raises(ChangeCandidateError, match="cannot transition"):
        record.transition(CandidateStatus.REJECTED)
 
 
def test_candidate_status_record_cannot_skip_to_promoted_from_proposed() -> None:
    record = CandidateStatusRecord(_candidate())
    with pytest.raises(ChangeCandidateError, match="cannot transition"):
        record.transition(CandidateStatus.PROMOTED_TO_CONTENT_CHANGE)
 
 
def test_candidate_status_record_transitioned_at_updated() -> None:
    import time
    record = CandidateStatusRecord(_candidate())
    before = datetime.now(tz=UTC)
    time.sleep(0.01)
    record.transition(CandidateStatus.VALIDATED)
    assert record.transitioned_at > before
 
 
def test_candidate_status_record_holds_reference_to_candidate() -> None:
    c = _candidate()
    record = CandidateStatusRecord(c)
    assert record.candidate is c
 
 
# ===========================================================================
# CorpusEntry — construction
# ===========================================================================
 
 
def test_corpus_entry_construction_happy_path() -> None:
    e = _entry()
    assert e.document_id == "bulletin-2026-01"
    assert e.section_id == "§12"
    assert e.content == "VAT standard rate: 20%"
 
 
def test_corpus_entry_is_frozen() -> None:
    e = _entry()
    with pytest.raises(FrozenInstanceError):
        e.content = "mutated"  # type: ignore[misc]
 
 
def test_corpus_entry_empty_document_id_raises() -> None:
    with pytest.raises(ChangeCandidateError, match="document_id"):
        CorpusEntry(
            document_id="",
            section_id="§1",
            content="text",
            citation=_citation(),
        )
 
 
def test_corpus_entry_empty_section_id_raises() -> None:
    with pytest.raises(ChangeCandidateError, match="section_id"):
        CorpusEntry(
            document_id="doc-a",
            section_id="",
            content="text",
            citation=_citation(),
        )
 
 
def test_corpus_entry_empty_content_raises() -> None:
    with pytest.raises(ChangeCandidateError, match="content"):
        CorpusEntry(
            document_id="doc-a",
            section_id="§1",
            content="",
            citation=_citation(),
        )
 
 
# ===========================================================================
# ChangeIntelligenceEngine.__init__
# ===========================================================================
 
 
def test_engine_default_min_confidence() -> None:
    engine = ChangeIntelligenceEngine()
    assert engine._min_confidence == 0.0
 
 
def test_engine_custom_min_confidence() -> None:
    engine = ChangeIntelligenceEngine(min_confidence=0.7)
    assert engine._min_confidence == 0.7
 
 
def test_engine_min_confidence_zero_accepted() -> None:
    ChangeIntelligenceEngine(min_confidence=0.0)
 
 
def test_engine_min_confidence_one_accepted() -> None:
    ChangeIntelligenceEngine(min_confidence=1.0)
 
 
def test_engine_min_confidence_below_zero_raises() -> None:
    with pytest.raises(ChangeCandidateError, match="min_confidence"):
        ChangeIntelligenceEngine(min_confidence=-0.1)
 
 
def test_engine_min_confidence_above_one_raises() -> None:
    with pytest.raises(ChangeCandidateError, match="min_confidence"):
        ChangeIntelligenceEngine(min_confidence=1.01)
 
 
def test_engine_ai_release_manifest_stored() -> None:
    engine = ChangeIntelligenceEngine(ai_release_manifest="ci-engine@v2")
    assert engine._ai_release_manifest == "ci-engine@v2"
 
 
def test_engine_default_ai_release_manifest_is_empty() -> None:
    engine = ChangeIntelligenceEngine()
    assert engine._ai_release_manifest == ""
 
 
# ===========================================================================
# extract_changes — governance gate
# ===========================================================================
 
 
def test_extract_changes_governance_gate_unknown_use_case() -> None:
    engine = _engine()
    empty_registry = UseCaseRegistry()
    with pytest.raises(GovernanceRefusedError):
        engine.extract_changes([], [], BASE_PROVENANCE, empty_registry)
 
 
def test_extract_changes_governance_gate_global_kill() -> None:
    engine = _engine()
    registry = _registry()
    registry.engage_global_kill()
    with pytest.raises(GovernanceRefusedError):
        engine.extract_changes([], [], BASE_PROVENANCE, registry)
 
 
def test_extract_changes_governance_gate_use_case_killed() -> None:
    engine = _engine()
    registry = _registry()
    registry.kill("change-intel")
    with pytest.raises(GovernanceRefusedError):
        engine.extract_changes([], [], BASE_PROVENANCE, registry)
 
 
def test_extract_changes_governance_gate_wrong_region() -> None:
    engine = _engine()
    wrong_region = Provenance(
        use_case="change-intel",
        model_profile="m",
        provider_profile="p",
        prompt_profile="pp",
        region="ap-southeast-1",
        data_class="INTERNAL",
        risk_tier=RiskTier.T1,
        authority_outcome=AuthorityOutcome.A1,
        ai_train_version="1.0",
    )
    with pytest.raises(GovernanceRefusedError):
        engine.extract_changes([], [], wrong_region, _registry())
 
 
# ===========================================================================
# extract_changes — empty corpora
# ===========================================================================
 
 
def test_extract_changes_both_empty_returns_empty_tuple() -> None:
    result = _engine().extract_changes([], [], BASE_PROVENANCE, _registry())
    assert result == ()
 
 
def test_extract_changes_returns_tuple() -> None:
    result = _engine().extract_changes([], [], BASE_PROVENANCE, _registry())
    assert isinstance(result, tuple)
 
 
def test_extract_changes_same_corpus_no_candidates() -> None:
    cit = _citation()
    entry = _entry(citation=cit)
    result = _engine().extract_changes([entry], [entry], BASE_PROVENANCE, _registry())
    assert result == ()
 
 
# ===========================================================================
# extract_changes — additions (source_snapshot_before=None)
# ===========================================================================
 
 
def test_extract_changes_new_section_produces_candidate() -> None:
    new_entry = _entry(section_id="§99", content="new provision text")
    result = _engine().extract_changes([], [new_entry], BASE_PROVENANCE, _registry())
    assert len(result) == 1
    assert result[0].target_rule_or_section == "§99"
 
 
def test_extract_changes_addition_has_no_before_snapshot() -> None:
    new_entry = _entry(section_id="§99", content="new provision text")
    result = _engine().extract_changes([], [new_entry], BASE_PROVENANCE, _registry())
    assert result[0].source_snapshot_before is None
 
 
def test_extract_changes_addition_has_after_snapshot() -> None:
    cit = _citation(source_id="new-bulletin", content=b"new content")
    new_entry = _entry(section_id="§99", content="new content", citation=cit)
    result = _engine().extract_changes([], [new_entry], BASE_PROVENANCE, _registry())
    assert result[0].source_snapshot_after is not None
    assert result[0].source_snapshot_after.citation_id == cit.citation_id
 
 
def test_extract_changes_addition_proposed_diff_contains_content() -> None:
    new_entry = _entry(section_id="§99", content="new provision text")
    result = _engine().extract_changes([], [new_entry], BASE_PROVENANCE, _registry())
    assert "new provision text" in result[0].proposed_diff
 
 
def test_extract_changes_addition_confidence_is_0_9() -> None:
    new_entry = _entry(section_id="§99", content="some new text")
    result = _engine().extract_changes([], [new_entry], BASE_PROVENANCE, _registry())
    assert result[0].confidence_score == 0.9
 
 
def test_extract_changes_multiple_additions() -> None:
    entries = [
        _entry(section_id=f"§{i}", content=f"new section {i}")
        for i in range(3)
    ]
    result = _engine().extract_changes([], entries, BASE_PROVENANCE, _registry())
    assert len(result) == 3
 
 
# ===========================================================================
# extract_changes — deletions (source_snapshot_after=None)
# ===========================================================================
 
 
def test_extract_changes_removed_section_produces_candidate() -> None:
    old_entry = _entry(section_id="§12", content="old provision text")
    result = _engine().extract_changes([old_entry], [], BASE_PROVENANCE, _registry())
    assert len(result) == 1
    assert result[0].target_rule_or_section == "§12"
 
 
def test_extract_changes_deletion_has_before_snapshot() -> None:
    cit = _citation(content=b"old provision text")
    old_entry = _entry(section_id="§12", content="old provision text", citation=cit)
    result = _engine().extract_changes([old_entry], [], BASE_PROVENANCE, _registry())
    assert result[0].source_snapshot_before is not None
    assert result[0].source_snapshot_before.citation_id == cit.citation_id
 
 
def test_extract_changes_deletion_has_no_after_snapshot() -> None:
    old_entry = _entry(section_id="§12", content="old provision text")
    result = _engine().extract_changes([old_entry], [], BASE_PROVENANCE, _registry())
    assert result[0].source_snapshot_after is None
 
 
def test_extract_changes_deletion_proposed_diff_contains_old_content() -> None:
    old_entry = _entry(section_id="§12", content="old provision text")
    result = _engine().extract_changes([old_entry], [], BASE_PROVENANCE, _registry())
    assert "old provision text" in result[0].proposed_diff
 
 
def test_extract_changes_deletion_confidence_is_0_9() -> None:
    old_entry = _entry(section_id="§12", content="some text")
    result = _engine().extract_changes([old_entry], [], BASE_PROVENANCE, _registry())
    assert result[0].confidence_score == 0.9
 
 
def test_extract_changes_multiple_deletions() -> None:
    entries = [
        _entry(section_id=f"§{i}", content=f"old section {i}")
        for i in range(4)
    ]
    result = _engine().extract_changes(entries, [], BASE_PROVENANCE, _registry())
    assert len(result) == 4
 
 
# ===========================================================================
# extract_changes — modifications (both snapshots set)
# ===========================================================================
 
 
def test_extract_changes_modification_has_both_snapshots() -> None:
    prior = _entry(section_id="§5", content="clarification note alpha")
    incoming = _entry(section_id="§5", content="clarification note beta")
    result = _engine().extract_changes([prior], [incoming], BASE_PROVENANCE, _registry())
    assert len(result) == 1
    c = result[0]
    assert c.source_snapshot_before is not None
    assert c.source_snapshot_after is not None
    # The two snapshots must differ (different content means different citations).
    assert c.source_snapshot_before.citation_id != c.source_snapshot_after.citation_id
 
 
def test_extract_changes_modification_confidence_is_0_85() -> None:
    prior = _entry(section_id="§5", content="text A")
    incoming = _entry(section_id="§5", content="text B")
    result = _engine().extract_changes([prior], [incoming], BASE_PROVENANCE, _registry())
    assert result[0].confidence_score == 0.85
 
 
def test_extract_changes_modification_diff_shows_before_and_after() -> None:
    prior = _entry(section_id="§5", content="before text")
    incoming = _entry(section_id="§5", content="after text")
    result = _engine().extract_changes([prior], [incoming], BASE_PROVENANCE, _registry())
    assert "before text" in result[0].proposed_diff
    assert "after text" in result[0].proposed_diff
 
 
def test_extract_changes_identical_content_not_flagged() -> None:
    prior = _entry(section_id="§5", content="same content")
    incoming = _entry(
        section_id="§5",
        content="same content",
        citation=prior.citation,
    )
    result = _engine().extract_changes([prior], [incoming], BASE_PROVENANCE, _registry())
    assert result == ()
 
 
# ===========================================================================
# extract_changes — RATE heuristic
# ===========================================================================
 
 
def test_extract_changes_rate_keyword_triggers_rate_change_type() -> None:
    prior = _entry(section_id="§12", content="VAT standard rate: 20%")
    incoming = _entry(section_id="§12", content="VAT standard rate: 23%")
    result = _engine().extract_changes([prior], [incoming], BASE_PROVENANCE, _registry())
    assert result[0].change_type is ChangeType.RATE
 
 
def test_extract_changes_percent_keyword_triggers_rate_change_type() -> None:
    prior = _entry(section_id="§3", content="withholding: 10 percent applies")
    incoming = _entry(section_id="§3", content="withholding: 15 percent applies")
    result = _engine().extract_changes([prior], [incoming], BASE_PROVENANCE, _registry())
    assert result[0].change_type is ChangeType.RATE
 
 
def test_extract_changes_gst_keyword_triggers_rate_change_type() -> None:
    prior = _entry(section_id="§7", content="GST base rate applies")
    incoming = _entry(section_id="§7", content="GST base rate revised upwards")
    result = _engine().extract_changes([prior], [incoming], BASE_PROVENANCE, _registry())
    assert result[0].change_type is ChangeType.RATE
 
 
# ===========================================================================
# extract_changes — THRESHOLD heuristic
# ===========================================================================
 
 
def test_extract_changes_threshold_keyword_triggers_threshold_change_type() -> None:
    prior = _entry(section_id="§18", content="registration threshold: £85,000")
    incoming = _entry(section_id="§18", content="registration threshold: £90,000")
    result = _engine().extract_changes([prior], [incoming], BASE_PROVENANCE, _registry())
    assert result[0].change_type is ChangeType.THRESHOLD
 
 
def test_extract_changes_limit_keyword_triggers_threshold_change_type() -> None:
    prior = _entry(section_id="§4", content="de minimis limit of £135 applies")
    incoming = _entry(section_id="§4", content="de minimis limit of £150 applies")
    result = _engine().extract_changes([prior], [incoming], BASE_PROVENANCE, _registry())
    assert result[0].change_type is ChangeType.THRESHOLD
 
 
def test_extract_changes_ceiling_keyword_triggers_threshold_change_type() -> None:
    prior = _entry(section_id="§9", content="ceiling of €10,000 applies")
    incoming = _entry(section_id="§9", content="ceiling of €12,000 applies")
    result = _engine().extract_changes([prior], [incoming], BASE_PROVENANCE, _registry())
    assert result[0].change_type is ChangeType.THRESHOLD
 
 
# ===========================================================================
# extract_changes — EFFECTIVE_DATE heuristic
# ===========================================================================
 
 
def test_extract_changes_effective_date_keywords_trigger_effective_date_type() -> None:
    prior = _entry(section_id="§5", content="clarification note alpha")
    incoming = _entry(section_id="§5", content="with effect from 1 April 2026")
    result = _engine().extract_changes([prior], [incoming], BASE_PROVENANCE, _registry())
    assert result[0].change_type is ChangeType.EFFECTIVE_DATE
 
 
def test_extract_changes_applies_from_keyword_triggers_effective_date_type() -> None:
    prior = _entry(section_id="§6", content="old text")
    incoming = _entry(section_id="§6", content="new provision applies from 2026")
    result = _engine().extract_changes([prior], [incoming], BASE_PROVENANCE, _registry())
    assert result[0].change_type is ChangeType.EFFECTIVE_DATE
 
 
# ===========================================================================
# extract_changes — confidence filtering
# ===========================================================================
 
 
def test_extract_changes_min_confidence_filters_low_confidence() -> None:
    # All engine-generated candidates have score 0.85 or 0.9;
    # setting min_confidence=1.0 drops all of them.
    prior = _entry(section_id="§12", content="VAT rate 20%")
    incoming = _entry(section_id="§12", content="VAT rate 25%")
    result = _engine(min_confidence=1.0).extract_changes(
        [prior], [incoming], BASE_PROVENANCE, _registry()
    )
    assert result == ()
 
 
def test_extract_changes_min_confidence_keeps_above_threshold() -> None:
    new_entry = _entry(section_id="§99", content="new text")
    # Engine sets confidence=0.9 for additions; threshold 0.5 should keep it.
    result = _engine(min_confidence=0.5).extract_changes(
        [], [new_entry], BASE_PROVENANCE, _registry()
    )
    assert len(result) == 1
 
 
def test_extract_changes_min_confidence_boundary_exactly_at_threshold() -> None:
    # Boundary: score exactly == threshold => kept.
    new_entry = _entry(section_id="§1", content="new text")
    result = _engine(min_confidence=0.9).extract_changes(
        [], [new_entry], BASE_PROVENANCE, _registry()
    )
    assert len(result) == 1
 
 
def test_extract_changes_min_confidence_just_above_modification_drops() -> None:
    # modification candidates are 0.85; threshold 0.86 drops them.
    prior = _entry(section_id="§5", content="alpha")
    incoming = _entry(section_id="§5", content="beta")
    result = _engine(min_confidence=0.86).extract_changes(
        [prior], [incoming], BASE_PROVENANCE, _registry()
    )
    assert result == ()
 
 
# ===========================================================================
# extract_changes — provenance and ai_release_manifest propagation
# ===========================================================================
 
 
def test_extract_changes_candidate_carries_provenance() -> None:
    new_entry = _entry(section_id="§1", content="text")
    result = _engine().extract_changes([], [new_entry], BASE_PROVENANCE, _registry())
    assert result[0].provenance is BASE_PROVENANCE
 
 
def test_extract_changes_candidate_source_document_id_matches_entry() -> None:
    new_entry = _entry(document_id="bulletin-xyz", section_id="§1", content="text")
    result = _engine().extract_changes([], [new_entry], BASE_PROVENANCE, _registry())
    assert result[0].source_document_id == "bulletin-xyz"
 
 
def test_extract_changes_ai_release_manifest_stamped_on_candidates() -> None:
    new_entry = _entry(section_id="§1", content="text")
    result = _engine(ai_release_manifest="ci@v99").extract_changes(
        [], [new_entry], BASE_PROVENANCE, _registry()
    )
    assert result[0].ai_release_manifest == "ci@v99"
 
 
def test_extract_changes_no_manifest_on_engine_results_in_empty_field() -> None:
    new_entry = _entry(section_id="§1", content="text")
    result = _engine().extract_changes([], [new_entry], BASE_PROVENANCE, _registry())
    assert result[0].ai_release_manifest == ""
 
 
# ===========================================================================
# extract_changes — mixed operations
# ===========================================================================
 
 
def test_extract_changes_mixed_add_modify_delete() -> None:
    prior = [
        _entry(document_id="doc-a", section_id="§1", content="old text"),
        _entry(document_id="doc-a", section_id="§2", content="VAT rate 20%"),
    ]
    incoming = [
        _entry(document_id="doc-a", section_id="§2", content="VAT rate 23%"),
        _entry(document_id="doc-a", section_id="§3", content="new section"),
    ]
    result = _engine().extract_changes(prior, incoming, BASE_PROVENANCE, _registry())
    change_types = {c.change_type for c in result}
    assert ChangeType.RATE in change_types
    assert ChangeType.OTHER in change_types  # addition and deletion both map to OTHER
 
 
# ===========================================================================
# to_review_item
# ===========================================================================
 
 
def test_to_review_item_returns_review_item() -> None:
    engine = _engine()
    cand = _candidate()
    queue = ReviewQueue()
    item = engine.to_review_item(cand, queue, _registry())
    assert isinstance(item, ReviewItem)
 
 
def test_to_review_item_output_id_is_candidate_id() -> None:
    engine = _engine()
    cand = _candidate()
    queue = ReviewQueue()
    item = engine.to_review_item(cand, queue, _registry())
    assert item.output_id == cand.candidate_id
 
 
def test_to_review_item_summary_contains_change_type() -> None:
    engine = _engine()
    cand = _candidate(change_type=ChangeType.THRESHOLD)
    queue = ReviewQueue()
    item = engine.to_review_item(cand, queue, _registry())
    assert "THRESHOLD" in item.summary
 
 
def test_to_review_item_summary_contains_target() -> None:
    engine = _engine()
    cand = _candidate(target_rule_or_section="§42")
    queue = ReviewQueue()
    item = engine.to_review_item(cand, queue, _registry())
    assert "§42" in item.summary
 
 
def test_to_review_item_summary_contains_source_document_id() -> None:
    engine = _engine()
    cand = _candidate(source_document_id="regulatory-bulletin-99")
    queue = ReviewQueue()
    item = engine.to_review_item(cand, queue, _registry())
    assert "regulatory-bulletin-99" in item.summary
 
 
def test_to_review_item_summary_shows_confidence() -> None:
    engine = _engine()
    cand = _candidate(confidence_score=0.85)
    queue = ReviewQueue()
    item = engine.to_review_item(cand, queue, _registry())
    assert "85%" in item.summary
 
 
def test_to_review_item_attaches_evidence_panel() -> None:
    engine = _engine()
    cand = _candidate()
    queue = ReviewQueue()
    item = engine.to_review_item(cand, queue, _registry())
    assert item.evidence_panel is not None
 
 
def test_to_review_item_evidence_panel_has_ai_answer_as_after_snapshot() -> None:
    """The 'after' citation ID should appear in ai_answer (the incoming state)."""
    after = _citation(content=b"incoming state")
    before = _citation(content=b"prior state")
    engine = _engine()
    cand = _candidate(source_snapshot_before=before, source_snapshot_after=after)
    queue = ReviewQueue()
    item = engine.to_review_item(cand, queue, _registry())
    assert item.evidence_panel is not None
    assert after.citation_id in item.evidence_panel.ai_answer
 
 
def test_to_review_item_evidence_panel_has_rule_answer_as_before_snapshot() -> None:
    after = _citation(content=b"incoming state")
    before = _citation(content=b"prior state")
    engine = _engine()
    cand = _candidate(source_snapshot_before=before, source_snapshot_after=after)
    queue = ReviewQueue()
    item = engine.to_review_item(cand, queue, _registry())
    assert item.evidence_panel is not None
    assert before.citation_id in item.evidence_panel.rule_answer
 
 
def test_to_review_item_addition_evidence_panel_shows_no_prior() -> None:
    engine = _engine()
    cand = _candidate(source_snapshot_before=None, source_snapshot_after=_citation())
    queue = ReviewQueue()
    item = engine.to_review_item(cand, queue, _registry())
    assert item.evidence_panel is not None
    assert "no prior snapshot" in item.evidence_panel.rule_answer
 
 
def test_to_review_item_forwards_review_priority() -> None:
    engine = _engine()
    cand = _candidate(review_priority=ReviewPriority.CRITICAL)
    queue = ReviewQueue()
    item = engine.to_review_item(cand, queue, _registry())
    assert item.priority is ReviewPriority.CRITICAL
 
 
def test_to_review_item_duplicate_submission_raises() -> None:
    engine = _engine()
    cand = _candidate()
    queue = ReviewQueue()
    engine.to_review_item(cand, queue, _registry())
    from ztax_gateway.human_review import ReviewError
    with pytest.raises(ReviewError, match="already in the queue"):
        engine.to_review_item(cand, queue, _registry())
 
 
def test_to_review_item_governance_refusal_propagates() -> None:
    engine = _engine()
    cand = _candidate()
    queue = ReviewQueue()
    empty_registry = UseCaseRegistry()
    with pytest.raises(GovernanceRefusedError):
        engine.to_review_item(cand, queue, empty_registry)
 
 
# ===========================================================================
# Integration — full pipeline
# ===========================================================================
 
 
def test_integration_extract_submit_approve() -> None:
    """extract_changes -> to_review_item -> decide(APPROVE) -> APPROVED."""
    from ztax_gateway.human_review import ReviewerProfile
    engine = _engine()
    prior = [_entry(section_id="§12", content="VAT rate: 20%")]
    incoming = [_entry(section_id="§12", content="VAT rate: 23%")]
 
    candidates = engine.extract_changes(prior, incoming, BASE_PROVENANCE, _registry())
    assert len(candidates) == 1
    assert candidates[0].change_type is ChangeType.RATE
 
    queue = ReviewQueue()
    item = engine.to_review_item(candidates[0], queue, _registry())
    assert queue.status(item.review_item_id) is ReviewStatus.PENDING
    assert item.evidence_panel is not None
 
    reviewer = ReviewerProfile(
        reviewer_id="reviewer:bob",
        max_authority=AuthorityOutcome.A2,
        permitted_regions=frozenset({"eu-west-1"}),
        permitted_jurisdictions=frozenset({"EU-VAT"}),
        qualified_since=datetime(2026, 1, 1, tzinfo=UTC),
    )
    queue.decide(
        review_item_id=item.review_item_id,
        reviewer_profile=reviewer,
        outcome=ReviewOutcome.APPROVE,
        rationale="Source confirms rate change to 23% from April 2026",
    )
    assert queue.status(item.review_item_id) is ReviewStatus.APPROVED
 
 
def test_integration_extract_submit_modify() -> None:
    """extract_changes -> to_review_item -> decide(MODIFY)."""
    from ztax_gateway.human_review import ReviewerProfile
    engine = _engine()
    prior = [_entry(section_id="§12", content="VAT rate: 20%")]
    incoming = [_entry(section_id="§12", content="VAT rate: 23%")]
 
    candidates = engine.extract_changes(prior, incoming, BASE_PROVENANCE, _registry())
    queue = ReviewQueue()
    item = engine.to_review_item(candidates[0], queue, _registry())
 
    reviewer = ReviewerProfile(
        reviewer_id="reviewer:bob",
        max_authority=AuthorityOutcome.A2,
        permitted_regions=frozenset({"eu-west-1"}),
        permitted_jurisdictions=frozenset({"EU-VAT"}),
        qualified_since=datetime(2026, 1, 1, tzinfo=UTC),
    )
    decision = queue.decide(
        review_item_id=item.review_item_id,
        reviewer_profile=reviewer,
        outcome=ReviewOutcome.MODIFY,
        correction="Rate is 21%, not 23% — clerical error in bulletin",
    )
    assert queue.status(item.review_item_id) is ReviewStatus.MODIFIED
    assert decision.correction == "Rate is 21%, not 23% — clerical error in bulletin"
 
 
def test_integration_escalate_then_reject() -> None:
    """to_review_item -> decide(ESCALATE) -> decide(REJECT) by higher reviewer."""
    from ztax_gateway.human_review import ReviewerProfile
    engine = _engine()
    new_entry = _entry(section_id="§42", content="new obligation text")
    candidates = engine.extract_changes([], [new_entry], BASE_PROVENANCE, _registry())
    queue = ReviewQueue()
    item = engine.to_review_item(candidates[0], queue, _registry())
 
    reviewer_a1 = ReviewerProfile(
        reviewer_id="reviewer:junior",
        max_authority=AuthorityOutcome.A1,
        permitted_regions=frozenset({"eu-west-1"}),
        permitted_jurisdictions=frozenset({"EU-VAT"}),
        qualified_since=datetime(2026, 1, 1, tzinfo=UTC),
    )
    queue.decide(
        review_item_id=item.review_item_id,
        reviewer_profile=reviewer_a1,
        outcome=ReviewOutcome.ESCALATE,
        rationale="Need senior sign-off",
    )
    assert queue.status(item.review_item_id) is ReviewStatus.ESCALATED
 
    reviewer_a2 = ReviewerProfile(
        reviewer_id="reviewer:senior",
        max_authority=AuthorityOutcome.A2,
        permitted_regions=frozenset({"eu-west-1"}),
        permitted_jurisdictions=frozenset({"EU-VAT"}),
        qualified_since=datetime(2026, 1, 1, tzinfo=UTC),
    )
    queue.decide(
        review_item_id=item.review_item_id,
        reviewer_profile=reviewer_a2,
        outcome=ReviewOutcome.REJECT,
        rationale="Bulletin withdrawn — obligation does not apply",
    )
    assert queue.status(item.review_item_id) is ReviewStatus.REJECTED
 
 
def _current_status(record: CandidateStatusRecord) -> CandidateStatus:
    """Read ``record.status`` through a function call.
 
    mypy narrows ``record.status`` after the first ``is`` assertion and cannot
    see that ``transition()`` mutates it, which reports the later assertions
    as unreachable.  Reading through a helper defeats the narrowing without
    changing what is asserted.
    """
    return record.status
 
 
def test_integration_candidate_status_record_through_review_lifecycle() -> None:
    """Extract, validate with CandidateStatusRecord, approve, promote to content."""
    engine = _engine()
    prior = [_entry(section_id="§12", content="VAT rate: 20%")]
    incoming = [_entry(section_id="§12", content="VAT rate: 23%")]
    candidates = engine.extract_changes(prior, incoming, BASE_PROVENANCE, _registry())
 
    record = CandidateStatusRecord(candidates[0])
    assert _current_status(record) is CandidateStatus.PROPOSED
 
    record.transition(CandidateStatus.VALIDATED)
    assert _current_status(record) is CandidateStatus.VALIDATED
 
    record.transition(CandidateStatus.PROMOTED_TO_CONTENT_CHANGE)
    assert _current_status(record) is CandidateStatus.PROMOTED_TO_CONTENT_CHANGE