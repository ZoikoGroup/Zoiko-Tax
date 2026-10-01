"""Tests for evaluation_evidence.py (Chapter 17 §19 — Evidence Record).

Coverage:
- GateVerdict enum values and identity checks
- EvaluationEvidenceError carries reason
- _derive_gate_verdict: SKIPPED (None), SKIPPED (empty), PASS (all pass),
  FAIL (some fail), FAIL (all fail)
- build(): governance gate fires first (GovernanceRefusedError)
- build(): empty release_manifest_id raises EvaluationEvidenceError
- build(): empty gold_set_name raises EvaluationEvidenceError
- build(): empty gold_set_version raises EvaluationEvidenceError
- build(): minimal happy path (gate_results=None → SKIPPED)
- build(): gate_results all EXPECTED_REFUSAL → PASS
- build(): gate_results with one non-EXPECTED_REFUSAL → FAIL + failures tuple
- build(): comparison_report optional (None and non-None)
- EvaluationEvidenceRecord.passed property
- EvaluationEvidenceRecord.gate_failure_count property
- EvaluationEvidenceRecord.as_dict() structure and types
- as_dict() comparison_report key is None when not provided
- as_dict() gate_failures list is empty on PASS
- as_dict() gate_failures list has entries on FAIL
- Fields populated from provenance (use_case, model_profile, prompt_profile)
- sealed_at is UTC datetime close to now
- evidence_id is a 16-char hex string
- Two consecutive build() calls produce different evidence_ids
"""

from __future__ import annotations

import pathlib
import re
from datetime import UTC, datetime

import pytest

from ztax_gateway.evaluation_adversarial import (
    BUILT_IN_SUITE,
    AdversarialRunner,
    CaseResult,
    CaseVerdict,
    GovernanceRefusal,
)
from ztax_gateway.evaluation_evidence import (
    EvaluationEvidenceError,
    EvaluationEvidenceRecord,
    GateVerdict,
    _derive_gate_verdict,
    build,
)
from ztax_gateway.evaluation_quality import (
    GoldCase,
    GoldSetStore,
    MetricEngine,
    MetricReport,
    ModelComparator,
)
from ztax_gateway.governance import (
    GovernanceRefusedError,
    UseCase,
    UseCaseRegistry,
)
from ztax_gateway.production_registries import (
    ManifestRegistry,
    ManifestRegistryError,
    build_manifest,
)
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier

# ---------------------------------------------------------------------------
# Shared constants
# ---------------------------------------------------------------------------

_REGION = "eu-west-1"

_ONTOLOGY_MD = """\
# Telecom Product Classes

## Voice Services

Standard voice call services and plans.

## Roaming Data Bundle

Data bundles for subscribers in foreign networks.

## Data Services

Mobile data and broadband services.
"""


_USE_CASE = UseCase(
    use_case_id="test-evidence",
    owner="lane-test",
    description="Test use case for evaluation evidence tests.",
    max_risk_tier=RiskTier.T2,
    max_authority=AuthorityOutcome.A2,
    permitted_regions=frozenset({_REGION}),
)

_PROVENANCE = Provenance(
    use_case="test-evidence",
    model_profile="model:test@2026.09",
    provider_profile="provider:eu-hosted",
    prompt_profile="prompt:test@1",
    region=_REGION,
    data_class="INTERNAL",
    risk_tier=RiskTier.T1,
    authority_outcome=AuthorityOutcome.A1,
    ai_train_version="0.5.0",
)


@pytest.fixture()
def registry() -> UseCaseRegistry:
    return UseCaseRegistry([_USE_CASE])


@pytest.fixture()
def provenance() -> Provenance:
    return _PROVENANCE


@pytest.fixture()
def metric_report(
    registry: UseCaseRegistry, provenance: Provenance, tmp_path: pathlib.Path
) -> MetricReport:
    """A real MetricReport built with a classifier that has a loaded knowledge base."""
    from ztax_gateway.classifier import Classifier
    from ztax_gateway.rag import KnowledgeBase

    ontology_md = tmp_path / "ontology.md"
    ontology_md.write_text(
        "# Telecom Product Classes\n\n"
        "## Voice Services\n\nStandard voice call services.\n\n"
        "## Roaming Data Bundle\n\nData bundles for roaming subscribers.\n\n"
        "## Data Services\n\nMobile data and broadband.\n",
        encoding="utf-8",
    )
    kb = KnowledgeBase()
    kb.build([ontology_md])
    classifier = Classifier(knowledge_base=kb)

    store = GoldSetStore(
        name="test-gold",
        version="1.0",
        cases=[
            GoldCase("c-001", "Roaming data bundle", "Roaming Data Bundle"),
            GoldCase("c-002", "Voice call plan standard", "Voice Services"),
        ],
    )
    engine = MetricEngine()
    return engine.run(classifier, store, registry, provenance)


def _passing_case_result(case_id: str = "adv-001") -> CaseResult:
    """A CaseResult that represents a successful EXPECTED_REFUSAL outcome."""
    return CaseResult(
        case_id=case_id,
        verdict=CaseVerdict.EXPECTED_REFUSAL,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        actual_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        detail="expected refusal received",
    )


def _failing_case_result(case_id: str = "adv-fail") -> CaseResult:
    """A CaseResult that represents a failed UNEXPECTED_PASS outcome."""
    return CaseResult(
        case_id=case_id,
        verdict=CaseVerdict.UNEXPECTED_PASS,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        actual_refusal=None,
        detail="trigger returned without raising",
    )


# ---------------------------------------------------------------------------
# GateVerdict enum
# ---------------------------------------------------------------------------


def test_gate_verdict_values() -> None:
    assert GateVerdict.PASS == "PASS"
    assert GateVerdict.FAIL == "FAIL"
    assert GateVerdict.SKIPPED == "SKIPPED"


def test_gate_verdict_identity() -> None:
    assert GateVerdict("PASS") is GateVerdict.PASS
    assert GateVerdict("FAIL") is GateVerdict.FAIL
    assert GateVerdict("SKIPPED") is GateVerdict.SKIPPED


# ---------------------------------------------------------------------------
# EvaluationEvidenceError
# ---------------------------------------------------------------------------


def test_evaluation_evidence_error_carries_reason() -> None:
    err = EvaluationEvidenceError("something went wrong")
    assert err.reason == "something went wrong"
    assert "something went wrong" in str(err)


# ---------------------------------------------------------------------------
# _derive_gate_verdict (internal helper)
# ---------------------------------------------------------------------------


def test_derive_gate_verdict_none_is_skipped() -> None:
    verdict, failures = _derive_gate_verdict(None)
    assert verdict is GateVerdict.SKIPPED
    assert failures == ()


def test_derive_gate_verdict_empty_list_is_skipped() -> None:
    verdict, failures = _derive_gate_verdict([])
    assert verdict is GateVerdict.SKIPPED
    assert failures == ()


def test_derive_gate_verdict_all_pass_is_pass() -> None:
    results = [_passing_case_result("a1"), _passing_case_result("a2")]
    verdict, failures = _derive_gate_verdict(results)
    assert verdict is GateVerdict.PASS
    assert failures == ()


def test_derive_gate_verdict_one_fail_is_fail() -> None:
    results = [_passing_case_result("a1"), _failing_case_result("a2")]
    verdict, failures = _derive_gate_verdict(results)
    assert verdict is GateVerdict.FAIL
    assert len(failures) == 1
    assert failures[0].case_id == "a2"


def test_derive_gate_verdict_all_fail_is_fail() -> None:
    results = [_failing_case_result("f1"), _failing_case_result("f2")]
    verdict, failures = _derive_gate_verdict(results)
    assert verdict is GateVerdict.FAIL
    assert len(failures) == 2


# ---------------------------------------------------------------------------
# build() — governance gate fires first
# ---------------------------------------------------------------------------


def test_build_governance_gate_fires_for_unknown_use_case(metric_report: MetricReport) -> None:
    empty_registry = UseCaseRegistry([])
    with pytest.raises(GovernanceRefusedError):
        build(
            release_manifest_id="manifest-001",
            gold_set_name="gs",
            gold_set_version="1.0",
            metric_report=metric_report,
            registry=empty_registry,
            provenance=_PROVENANCE,
        )


def test_build_governance_gate_fires_for_killed_use_case(metric_report: MetricReport) -> None:
    reg = UseCaseRegistry([_USE_CASE])
    reg.kill("test-evidence")
    with pytest.raises(GovernanceRefusedError):
        build(
            release_manifest_id="manifest-001",
            gold_set_name="gs",
            gold_set_version="1.0",
            metric_report=metric_report,
            registry=reg,
            provenance=_PROVENANCE,
        )


# ---------------------------------------------------------------------------
# build() — mandatory field validation
# ---------------------------------------------------------------------------


def test_build_empty_release_manifest_id_raises(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    with pytest.raises(EvaluationEvidenceError) as exc_info:
        build(
            release_manifest_id="",
            gold_set_name="gs",
            gold_set_version="1.0",
            metric_report=metric_report,
            registry=registry,
            provenance=provenance,
        )
    assert "release_manifest_id" in exc_info.value.reason


def test_build_empty_gold_set_name_raises(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    with pytest.raises(EvaluationEvidenceError) as exc_info:
        build(
            release_manifest_id="manifest-001",
            gold_set_name="",
            gold_set_version="1.0",
            metric_report=metric_report,
            registry=registry,
            provenance=provenance,
        )
    assert "gold_set_name" in exc_info.value.reason


def test_build_empty_gold_set_version_raises(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    with pytest.raises(EvaluationEvidenceError) as exc_info:
        build(
            release_manifest_id="manifest-001",
            gold_set_name="gs",
            gold_set_version="",
            metric_report=metric_report,
            registry=registry,
            provenance=provenance,
        )
    assert "gold_set_version" in exc_info.value.reason


# ---------------------------------------------------------------------------
# build() — happy paths
# ---------------------------------------------------------------------------


def test_build_minimal_returns_record(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    rec = build(
        release_manifest_id="manifest-001",
        gold_set_name="gs",
        gold_set_version="1.0",
        metric_report=metric_report,
        registry=registry,
        provenance=provenance,
    )
    assert isinstance(rec, EvaluationEvidenceRecord)
    assert rec.gate_verdict is GateVerdict.SKIPPED
    assert rec.gate_failures == ()
    assert rec.comparison_report is None


def test_build_gate_results_all_pass(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    results = [_passing_case_result("a1"), _passing_case_result("a2")]
    rec = build(
        release_manifest_id="manifest-002",
        gold_set_name="gs",
        gold_set_version="1.0",
        metric_report=metric_report,
        registry=registry,
        provenance=provenance,
        gate_results=results,
    )
    assert rec.gate_verdict is GateVerdict.PASS
    assert rec.gate_failures == ()
    assert rec.passed is True
    assert rec.gate_failure_count == 0


def test_build_gate_results_one_fail(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    results = [_passing_case_result("a1"), _failing_case_result("a2")]
    rec = build(
        release_manifest_id="manifest-003",
        gold_set_name="gs",
        gold_set_version="1.0",
        metric_report=metric_report,
        registry=registry,
        provenance=provenance,
        gate_results=results,
    )
    assert rec.gate_verdict is GateVerdict.FAIL
    assert rec.passed is False
    assert rec.gate_failure_count == 1
    assert rec.gate_failures[0].case_id == "a2"


def test_build_with_comparison_report(
    registry: UseCaseRegistry,
    provenance: Provenance,
    metric_report: MetricReport,
    tmp_path: pathlib.Path,
) -> None:
    from ztax_gateway.classifier import Classifier
    from ztax_gateway.evaluation_quality import GoldCase, GoldSetStore
    from ztax_gateway.rag import KnowledgeBase

    md = tmp_path / "ont.md"
    md.write_text(
        "# Telecom\n## Voice Services\nVoice.\n## Roaming Data Bundle\nRoaming.\n",
        encoding="utf-8",
    )
    kb = KnowledgeBase()
    kb.build([md])

    store = GoldSetStore(
        name="gs",
        version="1.0",
        cases=[
            GoldCase("c-001", "Roaming data bundle", "Roaming Data Bundle"),
            GoldCase("c-002", "Voice call plan standard", "Voice Services"),
        ],
    )
    comparator = ModelComparator()
    baseline = Classifier(knowledge_base=kb)
    candidate = Classifier(knowledge_base=kb)
    comp_report = comparator.compare(baseline, candidate, store, registry, provenance)

    rec = build(
        release_manifest_id="manifest-004",
        gold_set_name="gs",
        gold_set_version="1.0",
        metric_report=metric_report,
        registry=registry,
        provenance=provenance,
        comparison_report=comp_report,
    )
    assert rec.comparison_report is not None
    assert rec.comparison_report.comparison_id == comp_report.comparison_id


# ---------------------------------------------------------------------------
# EvaluationEvidenceRecord fields
# ---------------------------------------------------------------------------


def test_record_fields_from_provenance(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    rec = build(
        release_manifest_id="manifest-fields",
        gold_set_name="gs-name",
        gold_set_version="2.0",
        metric_report=metric_report,
        registry=registry,
        provenance=provenance,
    )
    assert rec.use_case == provenance.use_case
    assert rec.model_profile == provenance.model_profile
    assert rec.prompt_profile == provenance.prompt_profile
    assert rec.release_manifest_id == "manifest-fields"
    assert rec.gold_set_name == "gs-name"
    assert rec.gold_set_version == "2.0"


def test_record_sealed_at_is_utc_and_recent(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    before = datetime.now(tz=UTC)
    rec = build(
        release_manifest_id="manifest-time",
        gold_set_name="gs",
        gold_set_version="1.0",
        metric_report=metric_report,
        registry=registry,
        provenance=provenance,
    )
    after = datetime.now(tz=UTC)
    assert before <= rec.sealed_at <= after
    assert rec.sealed_at.tzinfo is UTC


def test_record_evidence_id_is_16_char_hex(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    rec = build(
        release_manifest_id="manifest-id",
        gold_set_name="gs",
        gold_set_version="1.0",
        metric_report=metric_report,
        registry=registry,
        provenance=provenance,
    )
    assert re.fullmatch(r"[0-9a-f]{16}", rec.evidence_id) is not None


def test_two_builds_produce_different_evidence_ids(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    rec1 = build(
        release_manifest_id="m", gold_set_name="g", gold_set_version="v",
        metric_report=metric_report, registry=registry, provenance=provenance,
    )
    rec2 = build(
        release_manifest_id="m", gold_set_name="g", gold_set_version="v",
        metric_report=metric_report, registry=registry, provenance=provenance,
    )
    assert rec1.evidence_id != rec2.evidence_id


def test_record_is_immutable(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    rec = build(
        release_manifest_id="m", gold_set_name="g", gold_set_version="v",
        metric_report=metric_report, registry=registry, provenance=provenance,
    )
    with pytest.raises((AttributeError, TypeError)):
        rec.gate_verdict = GateVerdict.PASS  # type: ignore[misc]


# ---------------------------------------------------------------------------
# as_dict()
# ---------------------------------------------------------------------------


def test_as_dict_structure_minimal(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    rec = build(
        release_manifest_id="manifest-dict",
        gold_set_name="gs",
        gold_set_version="1.0",
        metric_report=metric_report,
        registry=registry,
        provenance=provenance,
    )
    d = rec.as_dict()
    expected_keys = {
        "evidence_id", "release_manifest_id", "resolved_manifest_hash",
        "gold_set_name", "gold_set_version",
        "metric_report", "comparison_report", "gate_verdict", "gate_failure_count",
        "gate_failures", "use_case", "model_profile", "prompt_profile", "sealed_at",
    }
    assert set(d.keys()) == expected_keys


def test_as_dict_comparison_report_none_when_absent(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    rec = build(
        release_manifest_id="m", gold_set_name="g", gold_set_version="v",
        metric_report=metric_report, registry=registry, provenance=provenance,
    )
    assert rec.as_dict()["comparison_report"] is None


def test_as_dict_gate_failures_empty_on_pass(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    results = [_passing_case_result("a1")]
    rec = build(
        release_manifest_id="m", gold_set_name="g", gold_set_version="v",
        metric_report=metric_report, registry=registry, provenance=provenance,
        gate_results=results,
    )
    d = rec.as_dict()
    assert d["gate_verdict"] == "PASS"
    assert d["gate_failures"] == []
    assert d["gate_failure_count"] == 0


def test_as_dict_gate_failures_populated_on_fail(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    results = [_failing_case_result("f1")]
    rec = build(
        release_manifest_id="m", gold_set_name="g", gold_set_version="v",
        metric_report=metric_report, registry=registry, provenance=provenance,
        gate_results=results,
    )
    d = rec.as_dict()
    assert d["gate_verdict"] == "FAIL"
    assert d["gate_failure_count"] == 1
    failures = d["gate_failures"]
    assert isinstance(failures, list)
    assert failures[0]["case_id"] == "f1"
    assert failures[0]["verdict"] == "UNEXPECTED_PASS"


def test_as_dict_sealed_at_is_isoformat_string(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    rec = build(
        release_manifest_id="m", gold_set_name="g", gold_set_version="v",
        metric_report=metric_report, registry=registry, provenance=provenance,
    )
    sealed = rec.as_dict()["sealed_at"]
    assert isinstance(sealed, str)
    parsed = datetime.fromisoformat(sealed)
    assert parsed.tzinfo is not None


def test_as_dict_metric_report_is_dict(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    rec = build(
        release_manifest_id="m", gold_set_name="g", gold_set_version="v",
        metric_report=metric_report, registry=registry, provenance=provenance,
    )
    assert isinstance(rec.as_dict()["metric_report"], dict)


def test_as_dict_comparison_report_is_dict_when_present(
    registry: UseCaseRegistry,
    provenance: Provenance,
    metric_report: MetricReport,
    tmp_path: pathlib.Path,
) -> None:
    from ztax_gateway.classifier import Classifier
    from ztax_gateway.evaluation_quality import GoldCase, GoldSetStore
    from ztax_gateway.rag import KnowledgeBase

    md = tmp_path / "ont2.md"
    md.write_text(
        "# Telecom\n## Voice Services\nVoice.\n## Roaming Data Bundle\nRoaming.\n",
        encoding="utf-8",
    )
    kb = KnowledgeBase()
    kb.build([md])

    store = GoldSetStore(
        name="gs", version="1.0",
        cases=[
            GoldCase("c-001", "Roaming data bundle", "Roaming Data Bundle"),
            GoldCase("c-002", "Voice call plan standard", "Voice Services"),
        ],
    )
    comp_report = ModelComparator().compare(
        Classifier(knowledge_base=kb), Classifier(knowledge_base=kb), store, registry, provenance
    )
    rec = build(
        release_manifest_id="m", gold_set_name="g", gold_set_version="v",
        metric_report=metric_report, registry=registry, provenance=provenance,
        comparison_report=comp_report,
    )
    assert isinstance(rec.as_dict()["comparison_report"], dict)


# ---------------------------------------------------------------------------
# Integration — full pipeline end-to-end
# ---------------------------------------------------------------------------


def test_full_pipeline_with_built_in_suite(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    """Run the built-in adversarial suite and seal the evidence record."""

    runner = AdversarialRunner()
    gate_results = runner.run(BUILT_IN_SUITE)

    rec = build(
        release_manifest_id="manifest-pipeline",
        gold_set_name="test-gold",
        gold_set_version="1.0",
        metric_report=metric_report,
        registry=registry,
        provenance=provenance,
        gate_results=gate_results,
    )
    # The built-in suite should all pass.
    assert rec.gate_verdict is GateVerdict.PASS
    assert rec.passed is True
    d = rec.as_dict()
    assert d["gate_verdict"] == "PASS"
    assert d["gate_failures"] == []


# ---------------------------------------------------------------------------
# ManifestRegistry integration — the gap the spec review identified
# ---------------------------------------------------------------------------


def _make_manifest_registry(
    registry: UseCaseRegistry, provenance: Provenance
) -> tuple[ManifestRegistry, str]:
    """Build a ManifestRegistry with one active manifest; return (reg, manifest_id)."""
    manifest = build_manifest(
        ai_use_case_id=provenance.use_case,
        release_version="1.0.0",
        authority_level=provenance.authority_outcome,
        risk_tier=provenance.risk_tier,
        model_profile_id=provenance.model_profile,
        resolved_model_id="model-pinned-abc",
        prompt_profile_id=provenance.prompt_profile,
        evaluation_profile_id="eval-profile-001",
        policy_bundle_id="policy-bundle-001",
        allowed_regions=frozenset({provenance.region}),
        manifest_id="test-manifest-001",
    )
    man_reg = ManifestRegistry()
    man_reg.add(manifest)
    return man_reg, manifest.manifest_id


def test_build_with_manifest_registry_resolves_active_manifest(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    """When manifest_registry is supplied and the ID is active, it resolves cleanly."""
    man_reg, mid = _make_manifest_registry(registry, provenance)
    rec = build(
        release_manifest_id=mid,
        gold_set_name="gs", gold_set_version="1.0",
        metric_report=metric_report, registry=registry, provenance=provenance,
        manifest_registry=man_reg,
    )
    assert rec.resolved_manifest is not None
    assert rec.resolved_manifest.manifest_id == mid
    assert rec.release_manifest_id == mid


def test_build_without_manifest_registry_resolved_manifest_is_none(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    """Backwards-compatible: omitting manifest_registry leaves resolved_manifest None."""
    rec = build(
        release_manifest_id="any-string-no-check",
        gold_set_name="gs", gold_set_version="1.0",
        metric_report=metric_report, registry=registry, provenance=provenance,
    )
    assert rec.resolved_manifest is None


def test_build_dangling_manifest_id_raises(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    """An ID that was never registered is refused before the record is sealed."""
    man_reg = ManifestRegistry()  # empty — nothing registered
    with pytest.raises(ManifestRegistryError) as exc_info:
        build(
            release_manifest_id="nonexistent-manifest-id-xyz",
            gold_set_name="gs", gold_set_version="1.0",
            metric_report=metric_report, registry=registry, provenance=provenance,
            manifest_registry=man_reg,
        )
    assert "nonexistent-manifest-id-xyz" in exc_info.value.reason


def test_build_revoked_manifest_id_raises(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    """A revoked manifest ID is refused; the revocation gate fires before sealing."""
    man_reg, mid = _make_manifest_registry(registry, provenance)
    man_reg.revoke(mid)
    with pytest.raises(ManifestRegistryError) as exc_info:
        build(
            release_manifest_id=mid,
            gold_set_name="gs", gold_set_version="1.0",
            metric_report=metric_report, registry=registry, provenance=provenance,
            manifest_registry=man_reg,
        )
    assert "revoked" in exc_info.value.reason.lower()


def test_build_superseded_manifest_id_raises(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    """A superseded manifest ID is refused before the record is sealed."""
    man_reg, mid = _make_manifest_registry(registry, provenance)
    man_reg.supersede(mid)
    with pytest.raises(ManifestRegistryError) as exc_info:
        build(
            release_manifest_id=mid,
            gold_set_name="gs", gold_set_version="1.0",
            metric_report=metric_report, registry=registry, provenance=provenance,
            manifest_registry=man_reg,
        )
    assert "superseded" in exc_info.value.reason.lower()


def test_as_dict_resolved_manifest_hash_populated(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    """resolved_manifest_hash in as_dict() is a SHA-256 hex string when resolved."""
    import re as _re
    man_reg, mid = _make_manifest_registry(registry, provenance)
    rec = build(
        release_manifest_id=mid,
        gold_set_name="gs", gold_set_version="1.0",
        metric_report=metric_report, registry=registry, provenance=provenance,
        manifest_registry=man_reg,
    )
    h = rec.as_dict()["resolved_manifest_hash"]
    assert isinstance(h, str)
    assert _re.fullmatch(r"[0-9a-f]{64}", h) is not None


def test_as_dict_resolved_manifest_hash_none_when_no_registry(
    registry: UseCaseRegistry, provenance: Provenance, metric_report: MetricReport
) -> None:
    """resolved_manifest_hash is None in as_dict() when manifest_registry is omitted."""
    rec = build(
        release_manifest_id="bare-string",
        gold_set_name="gs", gold_set_version="1.0",
        metric_report=metric_report, registry=registry, provenance=provenance,
    )
    assert rec.as_dict()["resolved_manifest_hash"] is None


def test_build_manifest_registry_governance_gate_fires_first(
    provenance: Provenance, metric_report: MetricReport
) -> None:
    """The governance gate in ManifestRegistry.get() fires before any manifest check."""
    man_reg, mid = _make_manifest_registry(UseCaseRegistry([_USE_CASE]), provenance)
    killed_registry = UseCaseRegistry([_USE_CASE])
    killed_registry.kill(provenance.use_case)
    with pytest.raises(GovernanceRefusedError):
        build(
            release_manifest_id=mid,
            gold_set_name="gs", gold_set_version="1.0",
            metric_report=metric_report,
            registry=killed_registry,
            provenance=provenance,
            manifest_registry=man_reg,
        )
