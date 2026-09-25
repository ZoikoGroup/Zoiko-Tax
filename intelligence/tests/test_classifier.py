"""Tests for the SKU / Ontology Classifier.

Chapter 17 §15 of the ZoikoTax Master Specification.

Mirrors the structure of ``test_rag.py`` and ``test_tool_broker.py``.
Every public API is exercised; every error path is explicitly asserted so a
regression surfaces as a test failure rather than a silent wrong answer in an
AI audit log.

The tests use in-memory Markdown for speed (no network, no embedding model).
Real spec documents are used where the ``docs/specs/`` directory is available,
guarded by ``@pytest.mark.skipif``.
"""

from __future__ import annotations

from dataclasses import FrozenInstanceError, replace
from pathlib import Path

import pytest

from ztax_gateway.citation import Citation
from ztax_gateway.classifier import (
    ClassificationError,
    ClassificationProposal,
    ClassificationRecord,
    Classifier,
)
from ztax_gateway.governance import GovernanceRefusedError, UseCase, UseCaseRegistry
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier
from ztax_gateway.rag import KnowledgeBase

# ---------------------------------------------------------------------------
# Paths to the real spec documents (optional)
# ---------------------------------------------------------------------------

_REPO_ROOT = Path(__file__).resolve().parents[2]
_SPECS_DIR = _REPO_ROOT / "docs" / "specs"
_DET_SPEC = _SPECS_DIR / "ZTAX-DET-001-global-tax-determination-engine.md"
_JUR_SPEC = _SPECS_DIR / "ZTAX-JUR-001-jurisdiction-situs-and-place-of-supply.md"

_SPECS_AVAILABLE = _DET_SPEC.exists() and _JUR_SPEC.exists()

# ---------------------------------------------------------------------------
# Shared fixtures
# ---------------------------------------------------------------------------

_ONTOLOGY_MD = """\
# Telecom Product Classes

Overview of the ZoikoTax telecom product ontology.

## Voice Services

Standard voice call services including local, national and international.

### International Voice Roaming

Voice calls made while the subscriber is roaming outside their home region.

## Data Services

Broadband and mobile data services.

### Mobile Data Bundle

Prepaid or postpaid mobile data allowance packages.

### Roaming Data Bundle

Data bundles activated when the subscriber is in a foreign network.

## Device Sales

Hardware and device sales subject to customs and import duties.

### SIM Cards

SIM card sales, eSIM provisioning and physical SIM replacement.
"""

BASE_USE_CASE = UseCase(
    use_case_id="sku-classification",
    owner="lane-l",
    description="Classify telecom SKUs against the ZoikoTax ontology",
    max_risk_tier=RiskTier.T2,
    max_authority=AuthorityOutcome.A2,
    permitted_regions=frozenset({"eu-west-1"}),
)

BASE_PROVENANCE = Provenance(
    use_case="sku-classification",
    model_profile="model:classifier@2026.09",
    provider_profile="provider:eu-hosted",
    prompt_profile="prompt:sku-classification@1",
    region="eu-west-1",
    data_class="INTERNAL",
    risk_tier=RiskTier.T1,
    authority_outcome=AuthorityOutcome.A1,
    ai_train_version="0.5.0",
)


def _registry(*use_cases: UseCase) -> UseCaseRegistry:
    return UseCaseRegistry(list(use_cases) if use_cases else [BASE_USE_CASE])


def _make_markdown(tmp_path: Path, name: str, content: str) -> Path:
    p = tmp_path / name
    p.write_text(content, encoding="utf-8")
    return p


def _built_kb(tmp_path: Path) -> KnowledgeBase:
    f = _make_markdown(tmp_path, "ontology.md", _ONTOLOGY_MD)
    kb = KnowledgeBase()
    kb.build([f])
    return kb


# ---------------------------------------------------------------------------
# Happy path — classify returns a ClassificationRecord
# ---------------------------------------------------------------------------


def test_classify_returns_classification_record(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    record = clf.classify("roaming data bundle prepaid", BASE_PROVENANCE, _registry())
    assert isinstance(record, ClassificationRecord)


def test_classify_proposals_are_classification_proposal_objects(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    record = clf.classify("roaming data bundle", BASE_PROVENANCE, _registry())
    assert all(isinstance(p, ClassificationProposal) for p in record.proposals)


def test_classify_proposals_carry_citations(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    record = clf.classify("mobile data bundle prepaid", BASE_PROVENANCE, _registry())
    assert all(isinstance(p.citation, Citation) for p in record.proposals)


def test_classify_top_returns_best_match(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    record = clf.classify("international voice roaming", BASE_PROVENANCE, _registry())
    assert record.top() is not None
    assert isinstance(record.top(), ClassificationProposal)


def test_classify_top_for_empty_proposals_returns_none(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    # A query that matches nothing yields no proposals.
    record = clf.classify("xyzzy completely absent product code", BASE_PROVENANCE, _registry())
    assert record.top() is None


def test_classify_respects_top_k(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    record = clf.classify("SIM card eSIM device", BASE_PROVENANCE, _registry(), top_k=2)
    assert len(record.proposals) <= 2


def test_classify_proposals_are_best_first(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    record = clf.classify("roaming data bundle prepaid", BASE_PROVENANCE, _registry())
    ranks = [p.rank for p in record.proposals]
    assert ranks == sorted(ranks)  # FTS5 rank: lower (more negative) = better


def test_classify_record_preserves_sku_description(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    desc = "mobile data bundle postpaid"
    record = clf.classify(desc, BASE_PROVENANCE, _registry())
    assert record.sku_description == desc


def test_classify_record_preserves_provenance(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    record = clf.classify("SIM card eSIM", BASE_PROVENANCE, _registry())
    assert record.provenance is BASE_PROVENANCE


def test_classify_strips_whitespace_from_description(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    record = clf.classify("  roaming data bundle  ", BASE_PROVENANCE, _registry())
    assert record.sku_description == "roaming data bundle"


# ---------------------------------------------------------------------------
# Governance gate — classification is gated, not autonomous
# ---------------------------------------------------------------------------


def test_classify_refuses_unknown_use_case(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    bad_prov = replace(BASE_PROVENANCE, use_case="no-such-use-case")
    with pytest.raises(GovernanceRefusedError) as exc:
        clf.classify("roaming data bundle", bad_prov, _registry())
    from ztax_gateway.governance import Refusal
    assert exc.value.refusal is Refusal.UNKNOWN_USE_CASE


def test_classify_refuses_when_global_kill_engaged(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    reg = _registry()
    reg.engage_global_kill()
    with pytest.raises(GovernanceRefusedError) as exc:
        clf.classify("mobile data bundle", BASE_PROVENANCE, reg)
    from ztax_gateway.governance import Refusal
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED


def test_classify_refuses_when_use_case_killed(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    reg = _registry()
    reg.kill("sku-classification")
    with pytest.raises(GovernanceRefusedError) as exc:
        clf.classify("SIM card", BASE_PROVENANCE, reg)
    from ztax_gateway.governance import Refusal
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED


def test_classify_refuses_when_use_case_suspended(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    suspended = replace(BASE_USE_CASE, suspended=True)
    with pytest.raises(GovernanceRefusedError) as exc:
        clf.classify("SIM card", BASE_PROVENANCE, _registry(suspended))
    from ztax_gateway.governance import Refusal
    assert exc.value.refusal is Refusal.USE_CASE_SUSPENDED


def test_classify_refuses_wrong_region(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    bad_prov = replace(BASE_PROVENANCE, region="ap-southeast-1")
    with pytest.raises(GovernanceRefusedError) as exc:
        clf.classify("data bundle", bad_prov, _registry())
    from ztax_gateway.governance import Refusal
    assert exc.value.refusal is Refusal.RESIDENCY_REFUSED


def test_classify_refuses_risk_tier_above_ceiling(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    bad_prov = replace(BASE_PROVENANCE, risk_tier=RiskTier.T4)
    with pytest.raises(GovernanceRefusedError) as exc:
        clf.classify("data bundle", bad_prov, _registry())
    from ztax_gateway.governance import Refusal
    assert exc.value.refusal is Refusal.RISK_TIER_EXCEEDED


def test_governance_gate_fires_before_retrieval(tmp_path: Path) -> None:
    """Governance is checked before any KB work — even if the KB is not built."""
    kb = KnowledgeBase()  # not built
    clf = Classifier(knowledge_base=kb)
    bad_prov = replace(BASE_PROVENANCE, use_case="no-such-use-case")
    with pytest.raises(GovernanceRefusedError):
        clf.classify("roaming data bundle prepaid", bad_prov, _registry())


# ---------------------------------------------------------------------------
# Precondition errors — ClassificationError (not GovernanceRefusedError)
# ---------------------------------------------------------------------------


def test_classify_too_short_description_raises(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    with pytest.raises(ClassificationError) as exc:
        clf.classify("ab", BASE_PROVENANCE, _registry())
    assert "too short" in exc.value.reason


def test_classify_whitespace_only_raises(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    with pytest.raises(ClassificationError) as exc:
        clf.classify("   ", BASE_PROVENANCE, _registry())
    assert "too short" in exc.value.reason


def test_classify_empty_string_raises(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    with pytest.raises(ClassificationError):
        clf.classify("", BASE_PROVENANCE, _registry())


def test_classify_before_build_raises_classification_error(tmp_path: Path) -> None:
    kb = KnowledgeBase()  # not built
    clf = Classifier(knowledge_base=kb)
    with pytest.raises(ClassificationError) as exc:
        clf.classify("roaming data bundle prepaid", BASE_PROVENANCE, _registry())
    assert "retrieval failed" in exc.value.reason


# ---------------------------------------------------------------------------
# ClassificationProposal — immutability and confidence
# ---------------------------------------------------------------------------


def test_proposal_is_frozen(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    record = clf.classify("mobile data bundle prepaid", BASE_PROVENANCE, _registry())
    assert len(record.proposals) > 0
    with pytest.raises(FrozenInstanceError):
        record.proposals[0].ontology_class = "mutated"  # type: ignore[misc]


def test_proposal_confidence_out_of_range_raises() -> None:
    """ClassificationProposal rejects confidence outside [0, 1]."""
    from ztax_gateway.citation import SourceChunk, combine

    chunk = SourceChunk(source_id="x", content=b"x", byte_start=0, byte_end=1)
    citation = combine([chunk])
    with pytest.raises(ClassificationError):
        ClassificationProposal(
            ontology_class="## Voice Services",
            citation=citation,
            rank=-1.0,
            confidence=1.5,  # invalid
        )


def test_proposal_confidence_none_is_valid() -> None:
    from ztax_gateway.citation import SourceChunk, combine

    chunk = SourceChunk(source_id="x", content=b"x", byte_start=0, byte_end=1)
    citation = combine([chunk])
    p = ClassificationProposal(
        ontology_class="## Voice Services",
        citation=citation,
        rank=-1.0,
        confidence=None,
    )
    assert p.confidence is None


def test_proposal_confidence_boundary_values_are_valid() -> None:
    from ztax_gateway.citation import SourceChunk, combine

    chunk = SourceChunk(source_id="x", content=b"x", byte_start=0, byte_end=1)
    citation = combine([chunk])
    for conf in (0.0, 1.0):
        p = ClassificationProposal(
            ontology_class="## Data Services",
            citation=citation,
            rank=-0.5,
            confidence=conf,
        )
        assert p.confidence == conf


# ---------------------------------------------------------------------------
# ClassificationRecord — immutability and as_dict
# ---------------------------------------------------------------------------


def test_classification_record_is_frozen(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    record = clf.classify("roaming data bundle prepaid", BASE_PROVENANCE, _registry())
    with pytest.raises(FrozenInstanceError):
        record.sku_description = "mutated"  # type: ignore[misc]


def test_classification_record_as_dict_contains_required_fields(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    record = clf.classify("international voice roaming", BASE_PROVENANCE, _registry())
    d = record.as_dict()
    assert "sku_description" in d
    assert "proposal_count" in d
    assert "top_class" in d
    assert "top_citation_id" in d
    assert "use_case" in d
    assert "region" in d
    assert "risk_tier" in d
    assert "authority_outcome" in d


def test_classification_record_as_dict_no_matches(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    record = clf.classify("xyzzy completely absent product code", BASE_PROVENANCE, _registry())
    d = record.as_dict()
    assert d["proposal_count"] == 0
    assert d["top_class"] is None
    assert d["top_citation_id"] is None


def test_classification_record_as_dict_proposal_count(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    record = clf.classify("roaming data bundle prepaid", BASE_PROVENANCE, _registry(), top_k=2)
    d = record.as_dict()
    assert d["proposal_count"] == len(record.proposals)


# ---------------------------------------------------------------------------
# Citation determinism
# ---------------------------------------------------------------------------


def test_citation_id_is_deterministic_across_classifier_instances(tmp_path: Path) -> None:
    kb1 = _built_kb(tmp_path)
    clf1 = Classifier(knowledge_base=kb1)
    r1 = clf1.classify("roaming data bundle prepaid", BASE_PROVENANCE, _registry())

    f = tmp_path / "ontology.md"
    kb2 = KnowledgeBase()
    kb2.build([f])
    clf2 = Classifier(knowledge_base=kb2)
    r2 = clf2.classify("roaming data bundle prepaid", BASE_PROVENANCE, _registry())

    if r1.proposals and r2.proposals:
        assert r1.proposals[0].citation.citation_id == r2.proposals[0].citation.citation_id


def test_all_proposal_citations_verify(tmp_path: Path) -> None:
    clf = Classifier(knowledge_base=_built_kb(tmp_path))
    record = clf.classify("roaming voice international", BASE_PROVENANCE, _registry())
    assert all(p.citation.verify_all() for p in record.proposals)


# ---------------------------------------------------------------------------
# Default KnowledgeBase is created if not supplied
# ---------------------------------------------------------------------------


def test_classifier_creates_default_knowledge_base() -> None:
    clf = Classifier()
    assert isinstance(clf.knowledge_base, KnowledgeBase)


# ---------------------------------------------------------------------------
# Real spec documents (skipped if docs/specs/ not present)
# ---------------------------------------------------------------------------


@pytest.mark.skipif(not _SPECS_AVAILABLE, reason="docs/specs/ not found")
def test_real_spec_classify_determination_pipeline() -> None:
    kb = KnowledgeBase()
    kb.build([_DET_SPEC])
    clf = Classifier(knowledge_base=kb)
    record = clf.classify(
        "determination pipeline", BASE_PROVENANCE, _registry()
    )
    assert len(record.proposals) > 0
    assert all(isinstance(p.citation, Citation) for p in record.proposals)


@pytest.mark.skipif(not _SPECS_AVAILABLE, reason="docs/specs/ not found")
def test_real_spec_classify_jurisdiction_resolution() -> None:
    kb = KnowledgeBase()
    kb.build([_JUR_SPEC])
    clf = Classifier(knowledge_base=kb)
    record = clf.classify(
        "jurisdiction resolution situs evidence", BASE_PROVENANCE, _registry()
    )
    assert len(record.proposals) > 0


@pytest.mark.skipif(not _SPECS_AVAILABLE, reason="docs/specs/ not found")
def test_real_spec_classify_both_docs_together() -> None:
    kb = KnowledgeBase()
    kb.build([_DET_SPEC, _JUR_SPEC])
    clf = Classifier(knowledge_base=kb)
    record = clf.classify("tax determination", BASE_PROVENANCE, _registry())
    assert len(record.proposals) > 0


@pytest.mark.skipif(not _SPECS_AVAILABLE, reason="docs/specs/ not found")
def test_real_spec_all_proposal_citations_verify() -> None:
    kb = KnowledgeBase()
    kb.build([_DET_SPEC])
    clf = Classifier(knowledge_base=kb)
    record = clf.classify(
        "determination pipeline inclusive extraction", BASE_PROVENANCE, _registry()
    )
    assert all(p.citation.verify_all() for p in record.proposals)
