# Tests for ztax_gateway.drift -- Behavioral Fingerprint and Drift Detector.
from __future__ import annotations

import math
import uuid
from datetime import UTC, datetime
from unittest.mock import MagicMock

import pytest

from ztax_gateway.citation import SourceChunk, combine
from ztax_gateway.classifier import ClassificationProposal, ClassificationRecord, Classifier
from ztax_gateway.drift import (
    BehaviorFingerprint,
    CanaryProbe,
    DriftDetector,
    DriftError,
    DriftStatus,
    FingerprintComparison,
    FingerprintError,
    FingerprintReport,
    InvocationSignal,
    ProbeResult,
    compare_fingerprints,
)
from ztax_gateway.governance import GovernanceRefusedError, UseCase, UseCaseRegistry
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier

# ---------------------------------------------------------------------------
# Shared fixtures
# ---------------------------------------------------------------------------

UC_ID = "sku-classification"
REGION = "eu-west-1"


def _registry(suspended: bool = False) -> UseCaseRegistry:
    reg = UseCaseRegistry()
    reg.register(
        UseCase(
            use_case_id=UC_ID,
            owner="lane-l",
            description="Classify SKUs",
            max_risk_tier=RiskTier.T2,
            max_authority=AuthorityOutcome.A2,
            permitted_regions=frozenset({REGION}),
            suspended=suspended,
        )
    )
    return reg


def _provenance(use_case: str = UC_ID, region: str = REGION) -> Provenance:
    return Provenance(
        use_case=use_case,
        model_profile="bm25",
        provider_profile="local",
        prompt_profile="classify-v1",
        region=region,
        data_class="SKU",
        risk_tier=RiskTier.T2,
        authority_outcome=AuthorityOutcome.A2,
        ai_train_version="v2.3.1",
    )


def _probe(
    probe_id: str = "p1",
    desc: str = "test description five words long",
    expected: str = "CAT_A",
) -> CanaryProbe:
    return CanaryProbe(probe_id=probe_id, description=desc, expected_class=expected)


def _signal(
    abstained: bool = False,
    ood: bool = False,
    confidence: float = 0.9,
) -> InvocationSignal:
    return InvocationSignal(
        invocation_id=str(uuid.uuid4()),
        abstained=abstained,
        ood=ood,
        confidence=confidence,
        observed_at=datetime.now(UTC),
    )


def _mock_classifier(top_class: str = "CAT_A", rank: float = 5.0) -> MagicMock:
    """Return a Classifier mock whose classify() returns a record with one proposal."""
    chunk = SourceChunk(
        source_id="test://doc", content=b"test content", byte_start=0, byte_end=12
    )
    citation = combine([chunk])
    proposal = ClassificationProposal(
        ontology_class=top_class,
        citation=citation,
        rank=rank,
    )
    record = MagicMock(spec=ClassificationRecord)
    record.proposals = (proposal,)
    classifier = MagicMock(spec=Classifier)
    classifier.classify.return_value = record
    return classifier


def _empty_classifier() -> MagicMock:
    """Return a Classifier mock whose classify() returns no proposals (abstention)."""
    record = MagicMock(spec=ClassificationRecord)
    record.proposals = ()
    classifier = MagicMock(spec=Classifier)
    classifier.classify.return_value = record
    return classifier


# ---------------------------------------------------------------------------
# CanaryProbe data class
# ---------------------------------------------------------------------------


def test_canary_probe_fields() -> None:
    p = CanaryProbe(probe_id="p1", description="desc", expected_class="CLS")
    assert p.probe_id == "p1"
    assert p.description == "desc"
    assert p.expected_class == "CLS"


def test_canary_probe_default_expected_class() -> None:
    p = CanaryProbe(probe_id="p2", description="desc")
    assert p.expected_class == ""


def test_canary_probe_is_frozen() -> None:
    p = CanaryProbe(probe_id="p1", description="desc")
    with pytest.raises((AttributeError, TypeError)):
        p.probe_id = "changed"  # type: ignore[misc]


# ---------------------------------------------------------------------------
# BehaviorFingerprint construction
# ---------------------------------------------------------------------------


def test_empty_probes_raises() -> None:
    with pytest.raises(FingerprintError, match="empty"):
        BehaviorFingerprint(
            probes=[],
            classifier=_mock_classifier(),
            registry=_registry(),
            provenance=_provenance(),
        )


def test_duplicate_probe_ids_raises() -> None:
    with pytest.raises(FingerprintError, match="unique"):
        BehaviorFingerprint(
            probes=[_probe("p1"), _probe("p1")],
            classifier=_mock_classifier(),
            registry=_registry(),
            provenance=_provenance(),
        )


# ---------------------------------------------------------------------------
# Governance gate
# ---------------------------------------------------------------------------


def test_governance_gate_unknown_use_case() -> None:
    fp = BehaviorFingerprint(
        probes=[_probe()],
        classifier=_mock_classifier(),
        registry=_registry(),
        provenance=_provenance(use_case="unknown-uc"),
    )
    with pytest.raises(GovernanceRefusedError):
        fp.run()


def test_governance_gate_global_kill() -> None:
    reg = _registry()
    reg.engage_global_kill()
    fp = BehaviorFingerprint(
        probes=[_probe()],
        classifier=_mock_classifier(),
        registry=reg,
        provenance=_provenance(),
    )
    with pytest.raises(GovernanceRefusedError):
        fp.run()


def test_governance_gate_suspended() -> None:
    reg = _registry(suspended=True)
    fp = BehaviorFingerprint(
        probes=[_probe()],
        classifier=_mock_classifier(),
        registry=reg,
        provenance=_provenance(),
    )
    with pytest.raises(GovernanceRefusedError):
        fp.run()


# ---------------------------------------------------------------------------
# BehaviorFingerprint.run() happy path
# ---------------------------------------------------------------------------


def test_run_returns_fingerprint_report() -> None:
    reg = _registry()
    prov = _provenance()
    fp = BehaviorFingerprint(
        probes=[_probe("p1", "voice roaming bundle", "CAT_A")],
        classifier=_mock_classifier("CAT_A", 3.5),
        registry=reg,
        provenance=prov,
    )
    report = fp.run()
    assert isinstance(report, FingerprintReport)
    assert len(report.probe_results) == 1
    assert report.probe_results[0].probe_id == "p1"
    assert report.probe_results[0].actual_class == "CAT_A"
    assert report.probe_results[0].matched_expected is True


def test_run_accuracy_all_match() -> None:
    reg = _registry()
    fp = BehaviorFingerprint(
        probes=[
            _probe("p1", "desc one two three four", "CAT_A"),
            _probe("p2", "desc five six seven eight", "CAT_A"),
        ],
        classifier=_mock_classifier("CAT_A"),
        registry=reg,
        provenance=_provenance(),
    )
    report = fp.run()
    assert report.overall_accuracy == pytest.approx(1.0)


def test_run_accuracy_none_match() -> None:
    reg = _registry()
    fp = BehaviorFingerprint(
        probes=[_probe("p1", "desc one two three four", "CAT_B")],
        classifier=_mock_classifier("CAT_A"),  # returns CAT_A, expected CAT_B
        registry=reg,
        provenance=_provenance(),
    )
    report = fp.run()
    assert report.overall_accuracy == pytest.approx(0.0)


def test_run_abstention_probe() -> None:
    """A probe with no expected_class: abstention classifier should match."""
    reg = _registry()
    fp = BehaviorFingerprint(
        probes=[CanaryProbe(probe_id="p_empty", description="desc words words words")],
        classifier=_empty_classifier(),
        registry=reg,
        provenance=_provenance(),
    )
    report = fp.run()
    # Probe with no expected_class: result should record empty class without crashing
    assert report.probe_results[0].actual_class == ""
    assert report.probe_results[0].score == 0.0
    assert math.isnan(report.overall_accuracy)  # no expected_class probes


def test_run_fingerprint_id_unique() -> None:
    reg = _registry()
    fp = BehaviorFingerprint(
        probes=[_probe()],
        classifier=_mock_classifier(),
        registry=reg,
        provenance=_provenance(),
    )
    r1 = fp.run()
    r2 = fp.run()
    assert r1.fingerprint_id != r2.fingerprint_id


def test_run_classify_called_with_provenance() -> None:
    """classify() must receive provenance and registry."""
    reg = _registry()
    prov = _provenance()
    classifier = _mock_classifier()
    fp = BehaviorFingerprint(
        probes=[_probe()],
        classifier=classifier,
        registry=reg,
        provenance=prov,
    )
    fp.run()
    classifier.classify.assert_called_once()
    call_kwargs = classifier.classify.call_args
    assert call_kwargs.kwargs.get("provenance") is prov
    assert call_kwargs.kwargs.get("registry") is reg


# ---------------------------------------------------------------------------
# compare_fingerprints
# ---------------------------------------------------------------------------


def _make_report(results: list[tuple[str, str, float]]) -> FingerprintReport:
    """Build a synthetic FingerprintReport from (probe_id, class, score) tuples."""
    probe_results = tuple(
        ProbeResult(
            probe_id=pid,
            actual_class=cls,
            score=score,
            matched_expected=True,
        )
        for pid, cls, score in results
    )
    return FingerprintReport(
        fingerprint_id=str(uuid.uuid4()),
        run_at=datetime.now(UTC),
        probe_results=probe_results,
        overall_accuracy=1.0,
    )


def test_compare_equivalent() -> None:
    base = _make_report([("p1", "CAT_A", 3.0), ("p2", "CAT_B", 2.5)])
    cand = _make_report([("p1", "CAT_A", 3.0), ("p2", "CAT_B", 2.5)])
    assert compare_fingerprints(base, cand) == FingerprintComparison.EQUIVALENT


def test_compare_diverged_class_change() -> None:
    base = _make_report([("p1", "CAT_A", 3.0)])
    cand = _make_report([("p1", "CAT_B", 3.0)])  # class changed
    assert compare_fingerprints(base, cand) == FingerprintComparison.DIVERGED


def test_compare_diverged_score_change() -> None:
    base = _make_report([("p1", "CAT_A", 3.0)])
    cand = _make_report([("p1", "CAT_A", 9.9)])  # score changed beyond tolerance
    assert compare_fingerprints(base, cand) == FingerprintComparison.DIVERGED


def test_compare_incomparable_different_probe_sets() -> None:
    base = _make_report([("p1", "CAT_A", 3.0)])
    cand = _make_report([("p2", "CAT_A", 3.0)])  # different probe IDs
    assert compare_fingerprints(base, cand) == FingerprintComparison.INCOMPARABLE


def test_compare_equivalent_tiny_score_difference() -> None:
    """Differences below _SCORE_TOLERANCE must be treated as EQUIVALENT."""
    base = _make_report([("p1", "CAT_A", 3.0)])
    cand = _make_report([("p1", "CAT_A", 3.0 + 1e-9)])  # below 1e-6 tolerance
    assert compare_fingerprints(base, cand) == FingerprintComparison.EQUIVALENT


# ---------------------------------------------------------------------------
# InvocationSignal
# ---------------------------------------------------------------------------


def test_invocation_signal_fields() -> None:
    sig = _signal(abstained=False, ood=False, confidence=0.95)
    assert not sig.abstained
    assert not sig.ood
    assert sig.confidence == 0.95
    assert isinstance(sig.observed_at, datetime)


def test_invocation_signal_is_frozen() -> None:
    sig = _signal()
    with pytest.raises((AttributeError, TypeError)):
        sig.abstained = True  # type: ignore[misc]


# ---------------------------------------------------------------------------
# DriftDetector construction validation
# ---------------------------------------------------------------------------


def test_drift_detector_invalid_window_size() -> None:
    with pytest.raises(DriftError, match="window_size"):
        DriftDetector(window_size=1)


def test_drift_detector_invalid_abstention_threshold() -> None:
    with pytest.raises(DriftError, match="abstention"):
        DriftDetector(abstention_threshold=0.0)


def test_drift_detector_invalid_ood_threshold() -> None:
    with pytest.raises(DriftError, match="ood"):
        DriftDetector(ood_threshold=0.0)


def test_drift_detector_invalid_calibration_threshold() -> None:
    with pytest.raises(DriftError, match="calibration"):
        DriftDetector(calibration_threshold=1.1)


def test_drift_detector_invalid_min_window() -> None:
    with pytest.raises(DriftError, match="min_window"):
        DriftDetector(min_window=0)


# ---------------------------------------------------------------------------
# DriftDetector: NOMINAL when window too small
# ---------------------------------------------------------------------------


def test_check_nominal_when_window_small() -> None:
    det = DriftDetector(window_size=20, min_window=5)
    for _ in range(4):
        det.record(_signal(abstained=True))  # would trigger drift but window too small
    report = det.check()
    assert report.status == DriftStatus.NOMINAL
    assert report.window_size == 4


# ---------------------------------------------------------------------------
# DriftDetector: happy path NOMINAL
# ---------------------------------------------------------------------------


def test_check_nominal_with_healthy_signals() -> None:
    det = DriftDetector(
        window_size=20,
        abstention_threshold=0.3,
        ood_threshold=0.3,
        calibration_threshold=0.4,
        min_window=5,
    )
    for _ in range(10):
        det.record(_signal(abstained=False, ood=False, confidence=0.9))
    report = det.check()
    assert report.status == DriftStatus.NOMINAL
    assert report.abstention_rate == pytest.approx(0.0)
    assert report.ood_rate == pytest.approx(0.0)
    assert report.mean_confidence == pytest.approx(0.9)


# ---------------------------------------------------------------------------
# DriftDetector: ABSTENTION_DRIFT
# ---------------------------------------------------------------------------


def test_abstention_drift_detected() -> None:
    det = DriftDetector(
        window_size=10,
        abstention_threshold=0.3,
        min_window=5,
    )
    # 4 out of 10 abstained = 40 % > 30 % threshold
    for _ in range(4):
        det.record(_signal(abstained=True, confidence=0.0))
    for _ in range(6):
        det.record(_signal(abstained=False, confidence=0.9))
    report = det.check()
    assert DriftStatus.ABSTENTION_DRIFT.value in report.drift_kinds or report.status in (
        DriftStatus.ABSTENTION_DRIFT,
        DriftStatus.MULTI_DRIFT,
    )


# ---------------------------------------------------------------------------
# DriftDetector: OOD_DRIFT
# ---------------------------------------------------------------------------


def test_ood_drift_detected() -> None:
    det = DriftDetector(
        window_size=10,
        ood_threshold=0.3,
        min_window=5,
    )
    for _ in range(4):
        det.record(_signal(ood=True))
    for _ in range(6):
        det.record(_signal(ood=False))
    report = det.check()
    assert "OOD_DRIFT" in report.drift_kinds or report.status in (
        DriftStatus.OOD_DRIFT,
        DriftStatus.MULTI_DRIFT,
    )


# ---------------------------------------------------------------------------
# DriftDetector: CALIBRATION_DRIFT
# ---------------------------------------------------------------------------


def test_calibration_drift_detected() -> None:
    det = DriftDetector(
        window_size=10,
        calibration_threshold=0.5,
        min_window=5,
    )
    # All non-abstained but very low confidence
    for _ in range(10):
        det.record(_signal(abstained=False, ood=False, confidence=0.2))
    report = det.check()
    assert "CALIBRATION_DRIFT" in report.drift_kinds or report.status in (
        DriftStatus.CALIBRATION_DRIFT,
        DriftStatus.MULTI_DRIFT,
    )


# ---------------------------------------------------------------------------
# DriftDetector: MULTI_DRIFT
# ---------------------------------------------------------------------------


def test_multi_drift_when_multiple_thresholds_breached() -> None:
    det = DriftDetector(
        window_size=10,
        abstention_threshold=0.3,
        calibration_threshold=0.9,
        min_window=5,
    )
    # 4 abstentions AND low confidence on the 6 non-abstentions
    for _ in range(4):
        det.record(_signal(abstained=True, confidence=0.0))
    for _ in range(6):
        det.record(_signal(abstained=False, confidence=0.1))
    report = det.check()
    assert len(report.drift_kinds) >= 2 or report.status == DriftStatus.MULTI_DRIFT


# ---------------------------------------------------------------------------
# DriftDetector: reset
# ---------------------------------------------------------------------------


def test_reset_clears_window() -> None:
    det = DriftDetector(min_window=2)
    det.record(_signal())
    det.record(_signal())
    assert det.window_count == 2
    det.reset()
    assert det.window_count == 0


def test_after_reset_nominal() -> None:
    det = DriftDetector(min_window=2)
    det.reset()
    report = det.check()
    assert report.status == DriftStatus.NOMINAL


# ---------------------------------------------------------------------------
# DriftDetector: rolling window eviction
# ---------------------------------------------------------------------------


def test_rolling_window_evicts_oldest() -> None:
    det = DriftDetector(window_size=5, min_window=2)
    # Fill with abstentions
    for _ in range(5):
        det.record(_signal(abstained=True))
    # Now add 5 healthy signals -- the abstentions should be evicted
    for _ in range(5):
        det.record(_signal(abstained=False, confidence=0.9))
    report = det.check()
    assert report.abstention_rate == pytest.approx(0.0)


# ---------------------------------------------------------------------------
# DriftReport is frozen
# ---------------------------------------------------------------------------


def test_drift_report_is_frozen() -> None:
    det = DriftDetector(min_window=2)
    det.record(_signal())
    det.record(_signal())
    report = det.check()
    with pytest.raises((AttributeError, TypeError)):
        report.status = DriftStatus.NOMINAL  # type: ignore[misc]


# ---------------------------------------------------------------------------
# window_count property
# ---------------------------------------------------------------------------


def test_window_count() -> None:
    det = DriftDetector(window_size=10, min_window=2)
    assert det.window_count == 0
    det.record(_signal())
    assert det.window_count == 1
    det.record(_signal())
    assert det.window_count == 2


# ---------------------------------------------------------------------------
# CanaryEvaluator helpers
# ---------------------------------------------------------------------------

from ztax_gateway.drift import (  # noqa: E402
    CanaryEvaluator,
    CanaryEvaluatorError,
    CanaryVerdict,
    DriftReport,
)
from ztax_gateway.evaluation_quality import (  # noqa: E402
    ComparisonOutcome,
    ComparisonReport,
    MetricReport,
)


def _metric_report(accuracy: float) -> MetricReport:
    """Minimal MetricReport for constructing a ComparisonReport."""
    return MetricReport(
        run_id="run-base",
        store_name="gs",
        store_version="v1",
        total_cases=10,
        exact_matches=int(accuracy * 10),
        accuracy=accuracy,
        per_class=(),
        evaluated_at=datetime.now(UTC),
    )


def _comparison(
    outcome: ComparisonOutcome,
    delta: float = 0.05,
) -> ComparisonReport:
    return ComparisonReport(
        comparison_id="cmp-001",
        baseline_report=_metric_report(0.80),
        candidate_report=_metric_report(0.80 + delta),
        min_accuracy_threshold=0.50,
        accuracy_delta=delta,
        outcome=outcome,
        compared_at=datetime.now(UTC),
    )


def _nominal_drift() -> DriftReport:
    return DriftReport(
        report_id=str(uuid.uuid4()),
        window_size=10,
        abstention_rate=0.0,
        ood_rate=0.0,
        mean_confidence=0.9,
        status=DriftStatus.NOMINAL,
        drift_kinds=frozenset(),
        checked_at=datetime.now(UTC),
    )


def _active_drift(status: DriftStatus = DriftStatus.ABSTENTION_DRIFT) -> DriftReport:
    return DriftReport(
        report_id=str(uuid.uuid4()),
        window_size=10,
        abstention_rate=0.5,
        ood_rate=0.0,
        mean_confidence=0.9,
        status=status,
        drift_kinds=frozenset({status.value}),
        checked_at=datetime.now(UTC),
    )


def _equivalent_fp_pair() -> tuple[FingerprintReport, FingerprintReport]:
    results = (
        ProbeResult(probe_id="p1", actual_class="CAT_A", score=3.0, matched_expected=True),
    )
    base = FingerprintReport(
        fingerprint_id=str(uuid.uuid4()),
        run_at=datetime.now(UTC),
        probe_results=results,
        overall_accuracy=1.0,
    )
    cand = FingerprintReport(
        fingerprint_id=str(uuid.uuid4()),
        run_at=datetime.now(UTC),
        probe_results=results,
        overall_accuracy=1.0,
    )
    return base, cand


def _diverged_fp_pair() -> tuple[FingerprintReport, FingerprintReport]:
    base = FingerprintReport(
        fingerprint_id=str(uuid.uuid4()),
        run_at=datetime.now(UTC),
        probe_results=(
            ProbeResult(probe_id="p1", actual_class="CAT_A", score=3.0, matched_expected=True),
        ),
        overall_accuracy=1.0,
    )
    cand = FingerprintReport(
        fingerprint_id=str(uuid.uuid4()),
        run_at=datetime.now(UTC),
        probe_results=(
            ProbeResult(probe_id="p1", actual_class="CAT_B", score=3.0, matched_expected=False),
        ),
        overall_accuracy=0.0,
    )
    return base, cand


def _incomparable_fp_pair() -> tuple[FingerprintReport, FingerprintReport]:
    base = FingerprintReport(
        fingerprint_id=str(uuid.uuid4()),
        run_at=datetime.now(UTC),
        probe_results=(
            ProbeResult(probe_id="p1", actual_class="CAT_A", score=3.0, matched_expected=True),
        ),
        overall_accuracy=1.0,
    )
    cand = FingerprintReport(
        fingerprint_id=str(uuid.uuid4()),
        run_at=datetime.now(UTC),
        probe_results=(
            ProbeResult(probe_id="p99", actual_class="CAT_A", score=3.0, matched_expected=True),
        ),
        overall_accuracy=1.0,
    )
    return base, cand


def _evaluator(suspended: bool = False) -> CanaryEvaluator:
    return CanaryEvaluator(registry=_registry(suspended=suspended), provenance=_provenance())


# ---------------------------------------------------------------------------
# CanaryEvaluator: construction
# ---------------------------------------------------------------------------


def test_canary_evaluator_constructs() -> None:
    ev = _evaluator()
    assert isinstance(ev, CanaryEvaluator)


# ---------------------------------------------------------------------------
# CanaryEvaluator: governance gate
# ---------------------------------------------------------------------------


def test_canary_evaluator_governance_unknown_use_case() -> None:
    ev = CanaryEvaluator(registry=_registry(), provenance=_provenance(use_case="unknown"))
    base_fp, cand_fp = _equivalent_fp_pair()
    with pytest.raises(GovernanceRefusedError):
        ev.evaluate(
            comparison=_comparison(ComparisonOutcome.IMPROVED),
            drift_report=_nominal_drift(),
            baseline_fingerprint=base_fp,
            candidate_fingerprint=cand_fp,
        )


def test_canary_evaluator_governance_global_kill() -> None:
    reg = _registry()
    reg.engage_global_kill()
    ev = CanaryEvaluator(registry=reg, provenance=_provenance())
    base_fp, cand_fp = _equivalent_fp_pair()
    with pytest.raises(GovernanceRefusedError):
        ev.evaluate(
            comparison=_comparison(ComparisonOutcome.IMPROVED),
            drift_report=_nominal_drift(),
            baseline_fingerprint=base_fp,
            candidate_fingerprint=cand_fp,
        )


def test_canary_evaluator_governance_suspended() -> None:
    ev = _evaluator(suspended=True)
    base_fp, cand_fp = _equivalent_fp_pair()
    with pytest.raises(GovernanceRefusedError):
        ev.evaluate(
            comparison=_comparison(ComparisonOutcome.IMPROVED),
            drift_report=_nominal_drift(),
            baseline_fingerprint=base_fp,
            candidate_fingerprint=cand_fp,
        )


# ---------------------------------------------------------------------------
# CanaryEvaluator: PROMOTE verdict
# ---------------------------------------------------------------------------


def test_canary_evaluator_promote_when_improved_and_nominal() -> None:
    """IMPROVED accuracy + NOMINAL drift + EQUIVALENT fingerprints -> PROMOTE."""
    ev = _evaluator()
    base_fp, cand_fp = _equivalent_fp_pair()
    report = ev.evaluate(
        comparison=_comparison(ComparisonOutcome.IMPROVED, delta=0.05),
        drift_report=_nominal_drift(),
        baseline_fingerprint=base_fp,
        candidate_fingerprint=cand_fp,
    )
    assert report.verdict == CanaryVerdict.PROMOTE
    assert report.comparison_outcome == ComparisonOutcome.IMPROVED
    assert report.drift_status == DriftStatus.NOMINAL
    assert report.fingerprint_comparison == FingerprintComparison.EQUIVALENT


# ---------------------------------------------------------------------------
# CanaryEvaluator: ROLLBACK verdict
# ---------------------------------------------------------------------------


def test_canary_evaluator_rollback_when_regressed() -> None:
    ev = _evaluator()
    base_fp, cand_fp = _equivalent_fp_pair()
    report = ev.evaluate(
        comparison=_comparison(ComparisonOutcome.REGRESSED, delta=-0.05),
        drift_report=_nominal_drift(),
        baseline_fingerprint=base_fp,
        candidate_fingerprint=cand_fp,
    )
    assert report.verdict == CanaryVerdict.ROLLBACK


def test_canary_evaluator_rollback_when_below_threshold() -> None:
    ev = _evaluator()
    base_fp, cand_fp = _equivalent_fp_pair()
    report = ev.evaluate(
        comparison=_comparison(ComparisonOutcome.BELOW_THRESHOLD, delta=-0.30),
        drift_report=_nominal_drift(),
        baseline_fingerprint=base_fp,
        candidate_fingerprint=cand_fp,
    )
    assert report.verdict == CanaryVerdict.ROLLBACK


# ---------------------------------------------------------------------------
# CanaryEvaluator: HOLD verdict -- sub-cases
# ---------------------------------------------------------------------------


def test_canary_evaluator_hold_when_equivalent_accuracy() -> None:
    ev = _evaluator()
    base_fp, cand_fp = _equivalent_fp_pair()
    report = ev.evaluate(
        comparison=_comparison(ComparisonOutcome.EQUIVALENT, delta=0.0),
        drift_report=_nominal_drift(),
        baseline_fingerprint=base_fp,
        candidate_fingerprint=cand_fp,
    )
    assert report.verdict == CanaryVerdict.HOLD


def test_canary_evaluator_hold_when_improved_but_drift_active() -> None:
    ev = _evaluator()
    base_fp, cand_fp = _equivalent_fp_pair()
    report = ev.evaluate(
        comparison=_comparison(ComparisonOutcome.IMPROVED, delta=0.1),
        drift_report=_active_drift(DriftStatus.ABSTENTION_DRIFT),
        baseline_fingerprint=base_fp,
        candidate_fingerprint=cand_fp,
    )
    assert report.verdict == CanaryVerdict.HOLD
    assert report.drift_status == DriftStatus.ABSTENTION_DRIFT


def test_canary_evaluator_hold_when_improved_but_fingerprint_diverged() -> None:
    ev = _evaluator()
    base_fp, cand_fp = _diverged_fp_pair()
    report = ev.evaluate(
        comparison=_comparison(ComparisonOutcome.IMPROVED, delta=0.05),
        drift_report=_nominal_drift(),
        baseline_fingerprint=base_fp,
        candidate_fingerprint=cand_fp,
    )
    assert report.verdict == CanaryVerdict.HOLD
    assert report.fingerprint_comparison == FingerprintComparison.DIVERGED


def test_canary_evaluator_hold_when_improved_but_fingerprint_incomparable() -> None:
    ev = _evaluator()
    base_fp, cand_fp = _incomparable_fp_pair()
    report = ev.evaluate(
        comparison=_comparison(ComparisonOutcome.IMPROVED, delta=0.05),
        drift_report=_nominal_drift(),
        baseline_fingerprint=base_fp,
        candidate_fingerprint=cand_fp,
    )
    assert report.verdict == CanaryVerdict.HOLD
    assert report.fingerprint_comparison == FingerprintComparison.INCOMPARABLE


def test_canary_evaluator_hold_when_ood_drift() -> None:
    ev = _evaluator()
    base_fp, cand_fp = _equivalent_fp_pair()
    report = ev.evaluate(
        comparison=_comparison(ComparisonOutcome.IMPROVED),
        drift_report=_active_drift(DriftStatus.OOD_DRIFT),
        baseline_fingerprint=base_fp,
        candidate_fingerprint=cand_fp,
    )
    assert report.verdict == CanaryVerdict.HOLD


def test_canary_evaluator_hold_when_calibration_drift() -> None:
    ev = _evaluator()
    base_fp, cand_fp = _equivalent_fp_pair()
    report = ev.evaluate(
        comparison=_comparison(ComparisonOutcome.IMPROVED),
        drift_report=_active_drift(DriftStatus.CALIBRATION_DRIFT),
        baseline_fingerprint=base_fp,
        candidate_fingerprint=cand_fp,
    )
    assert report.verdict == CanaryVerdict.HOLD


def test_canary_evaluator_hold_when_multi_drift() -> None:
    ev = _evaluator()
    base_fp, cand_fp = _equivalent_fp_pair()
    report = ev.evaluate(
        comparison=_comparison(ComparisonOutcome.IMPROVED),
        drift_report=_active_drift(DriftStatus.MULTI_DRIFT),
        baseline_fingerprint=base_fp,
        candidate_fingerprint=cand_fp,
    )
    assert report.verdict == CanaryVerdict.HOLD


# ---------------------------------------------------------------------------
# CanaryEvaluator: report fields and invariants
# ---------------------------------------------------------------------------


def test_canary_verdict_report_fields_populated() -> None:
    ev = _evaluator()
    base_fp, cand_fp = _equivalent_fp_pair()
    comparison = _comparison(ComparisonOutcome.IMPROVED, delta=0.07)
    report = ev.evaluate(
        comparison=comparison,
        drift_report=_nominal_drift(),
        baseline_fingerprint=base_fp,
        candidate_fingerprint=cand_fp,
    )
    assert report.comparison_id == "cmp-001"
    assert report.accuracy_delta == pytest.approx(0.07)
    assert isinstance(report.evaluated_at, datetime)
    assert report.evaluated_at.tzinfo is not None


def test_canary_verdict_report_is_frozen() -> None:
    ev = _evaluator()
    base_fp, cand_fp = _equivalent_fp_pair()
    report = ev.evaluate(
        comparison=_comparison(ComparisonOutcome.IMPROVED),
        drift_report=_nominal_drift(),
        baseline_fingerprint=base_fp,
        candidate_fingerprint=cand_fp,
    )
    with pytest.raises((AttributeError, TypeError)):
        report.verdict = CanaryVerdict.ROLLBACK  # type: ignore[misc]


def test_canary_verdict_id_is_unique_per_call() -> None:
    ev = _evaluator()
    base_fp, cand_fp = _equivalent_fp_pair()
    r1 = ev.evaluate(
        comparison=_comparison(ComparisonOutcome.IMPROVED),
        drift_report=_nominal_drift(),
        baseline_fingerprint=base_fp,
        candidate_fingerprint=cand_fp,
    )
    r2 = ev.evaluate(
        comparison=_comparison(ComparisonOutcome.IMPROVED),
        drift_report=_nominal_drift(),
        baseline_fingerprint=base_fp,
        candidate_fingerprint=cand_fp,
    )
    assert r1.verdict_id != r2.verdict_id


def test_canary_verdict_report_as_dict() -> None:
    ev = _evaluator()
    base_fp, cand_fp = _equivalent_fp_pair()
    report = ev.evaluate(
        comparison=_comparison(ComparisonOutcome.IMPROVED),
        drift_report=_nominal_drift(),
        baseline_fingerprint=base_fp,
        candidate_fingerprint=cand_fp,
    )
    d = report.as_dict()
    assert d["verdict"] == "PROMOTE"
    assert d["comparison_outcome"] == "IMPROVED"
    assert d["drift_status"] == "NOMINAL"
    assert d["fingerprint_comparison"] == "EQUIVALENT"
    assert "verdict_id" in d
    assert "evaluated_at" in d


# ---------------------------------------------------------------------------
# CanaryEvaluatorError: non-finite accuracy_delta
# ---------------------------------------------------------------------------


def test_canary_evaluator_error_on_non_finite_delta() -> None:
    ev = _evaluator()
    base_fp, cand_fp = _equivalent_fp_pair()
    bad_comparison = ComparisonReport(
        comparison_id="cmp-bad",
        baseline_report=_metric_report(0.80),
        candidate_report=_metric_report(0.80),
        min_accuracy_threshold=0.50,
        accuracy_delta=float("nan"),
        outcome=ComparisonOutcome.IMPROVED,
        compared_at=datetime.now(UTC),
    )
    with pytest.raises(CanaryEvaluatorError, match="non-finite"):
        ev.evaluate(
            comparison=bad_comparison,
            drift_report=_nominal_drift(),
            baseline_fingerprint=base_fp,
            candidate_fingerprint=cand_fp,
        )
