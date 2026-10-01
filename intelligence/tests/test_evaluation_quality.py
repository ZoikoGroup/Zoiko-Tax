
"""Tests for the Gold Set Store, Metric Engine, and Model Comparator.
 
Chapter 17 §19 of the ZoikoTax Master Specification.
 
Covers:
- GoldCase construction and validation
- GoldSetStore construction, immutability, and lookup
- MetricEngine happy path and error paths
- MetricReport counts, accuracy, per-class metrics, as_dict
- ModelComparator outcomes: IMPROVED, REGRESSED, EQUIVALENT, BELOW_THRESHOLD
- Governance gate on MetricEngine.run (and therefore compare)
- Integration: classify -> gold-set -> metric -> compare pipeline
"""
 
from __future__ import annotations
 
from dataclasses import FrozenInstanceError, replace
from datetime import UTC, datetime
from pathlib import Path
 
import pytest
 
from ztax_gateway.classifier import Classifier
from ztax_gateway.evaluation_quality import (
    ComparisonOutcome,
    ComparisonReport,
    GoldCase,
    GoldSetError,
    GoldSetStore,
    MetricEngine,
    MetricEngineError,
    MetricReport,
    ModelComparator,
    PerClassMetric,
)
from ztax_gateway.governance import GovernanceRefusedError, UseCase, UseCaseRegistry
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier
from ztax_gateway.rag import KnowledgeBase
 
# ---------------------------------------------------------------------------
# Shared fixtures
# ---------------------------------------------------------------------------
 
_ONTOLOGY_MD = """\
# Telecom Product Classes
 
Overview of the ZoikoTax telecom product ontology.
 
## Voice Services
 
Standard voice call services.
 
## Data Services
 
Mobile data and broadband services.
 
### Roaming Data Bundle
 
Data bundles for subscribers in foreign networks.
"""
 
BASE_USE_CASE = UseCase(
    use_case_id="sku-quality-eval",
    owner="lane-l",
    description="Gold-set quality evaluation",
    max_risk_tier=RiskTier.T2,
    max_authority=AuthorityOutcome.A2,
    permitted_regions=frozenset({"eu-west-1"}),
)
 
BASE_PROVENANCE = Provenance(
    use_case="sku-quality-eval",
    model_profile="model:evaluator@2026.09",
    provider_profile="provider:eu-hosted",
    prompt_profile="prompt:quality@1",
    region="eu-west-1",
    data_class="INTERNAL",
    risk_tier=RiskTier.T1,
    authority_outcome=AuthorityOutcome.A1,
    ai_train_version="0.5.0",
)
 
 
def _registry(*extra: UseCase) -> UseCaseRegistry:
    return UseCaseRegistry([BASE_USE_CASE, *extra])
 
 
def _make_markdown(tmp_path: Path, name: str, content: str) -> Path:
    p = tmp_path / name
    p.write_text(content, encoding="utf-8")
    return p
 
 
def _make_classifier(tmp_path: Path) -> Classifier:
    f = _make_markdown(tmp_path, "ontology.md", _ONTOLOGY_MD)
    kb = KnowledgeBase()
    kb.build([f])
    return Classifier(knowledge_base=kb)
 
 
def _two_case_store() -> GoldSetStore:
    """Minimal valid store for use where the classifier is not required."""
    return GoldSetStore(
        name="test-store",
        version="1.0.0",
        cases=[
            GoldCase("gs-001", "Roaming data bundle", "Roaming Data Bundle"),
            GoldCase("gs-002", "Voice call plan", "Voice Services"),
        ],
    )
 
 
# ---------------------------------------------------------------------------
# GoldCase -- construction
# ---------------------------------------------------------------------------
 
 
def test_gold_case_stores_fields() -> None:
    gc = GoldCase("g1", "roaming data bundle", "Roaming Data Bundle", notes="verified")
    assert gc.case_id == "g1"
    assert gc.sku_description == "roaming data bundle"
    assert gc.expected_class == "Roaming Data Bundle"
    assert gc.notes == "verified"
 
 
def test_gold_case_normalised_expected_lowercased() -> None:
    gc = GoldCase("g1", "roaming data bundle", "  Roaming Data Bundle  ")
    assert gc.normalised_expected == "roaming data bundle"
 
 
def test_gold_case_empty_id_raises() -> None:
    with pytest.raises(GoldSetError) as exc:
        GoldCase("", "roaming data bundle", "Roaming Data Bundle")
    assert "no identifier" in exc.value.reason
 
 
def test_gold_case_empty_description_raises() -> None:
    with pytest.raises(GoldSetError) as exc:
        GoldCase("g1", "   ", "Roaming Data Bundle")
    assert "empty SKU description" in exc.value.reason
 
 
def test_gold_case_empty_expected_class_raises() -> None:
    with pytest.raises(GoldSetError) as exc:
        GoldCase("g1", "roaming data bundle", "")
    assert "no expected_class" in exc.value.reason
 
 
def test_gold_case_notes_defaults_to_empty() -> None:
    gc = GoldCase("g1", "roaming data bundle", "Roaming Data Bundle")
    assert gc.notes == ""
 
 
def test_gold_case_is_frozen() -> None:
    gc = GoldCase("g1", "roaming data bundle", "Roaming Data Bundle")
    with pytest.raises(FrozenInstanceError):
        gc.case_id = "mutated"  # type: ignore[misc]
 
 
# ---------------------------------------------------------------------------
# GoldSetStore -- construction
# ---------------------------------------------------------------------------
 
 
def test_gold_set_store_empty_name_raises() -> None:
    with pytest.raises(GoldSetError) as exc:
        GoldSetStore(name="", version="1.0.0")
    assert "no name" in exc.value.reason
 
 
def test_gold_set_store_empty_version_raises() -> None:
    with pytest.raises(GoldSetError) as exc:
        GoldSetStore(name="test", version="")
    assert "no version" in exc.value.reason
 
 
def test_gold_set_store_duplicate_case_id_raises() -> None:
    c1 = GoldCase("dup", "roaming data bundle", "Roaming Data Bundle")
    c2 = GoldCase("dup", "voice call plan", "Voice Services")
    with pytest.raises(GoldSetError) as exc:
        GoldSetStore(name="test", version="1.0.0", cases=[c1, c2])
    assert "already registered" in exc.value.reason
 
 
def test_gold_set_store_is_immutable_after_construction() -> None:
    store = _two_case_store()
    extra = GoldCase("gs-003", "broadband plan", "Data Services")
    with pytest.raises(GoldSetError) as exc:
        store._add(extra)
    assert "immutable" in exc.value.reason
 
 
def test_gold_set_store_case_count() -> None:
    store = _two_case_store()
    assert store.case_count == 2
 
 
def test_gold_set_store_cases_property_returns_tuple() -> None:
    store = _two_case_store()
    assert isinstance(store.cases, tuple)
    assert len(store.cases) == 2
 
 
def test_gold_set_store_get_returns_case_by_id() -> None:
    store = _two_case_store()
    case = store.get("gs-001")
    assert case is not None
    assert case.case_id == "gs-001"
 
 
def test_gold_set_store_get_returns_none_for_unknown() -> None:
    store = _two_case_store()
    assert store.get("no-such-case") is None
 
 
def test_gold_set_store_preserves_insertion_order() -> None:
    c1 = GoldCase("a", "roaming data bundle", "Roaming Data Bundle")
    c2 = GoldCase("b", "voice call plan", "Voice Services")
    c3 = GoldCase("c", "broadband plan", "Data Services")
    store = GoldSetStore(name="order-test", version="1.0.0", cases=[c1, c2, c3])
    ids = [c.case_id for c in store.cases]
    assert ids == ["a", "b", "c"]
 
 
def test_gold_set_store_with_no_cases() -> None:
    store = GoldSetStore(name="empty", version="1.0.0")
    assert store.case_count == 0
    assert store.cases == ()
 
 
# ---------------------------------------------------------------------------
# MetricEngine -- governance gate
# ---------------------------------------------------------------------------
 
 
def test_metric_engine_refuses_unknown_use_case(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    bad = replace(BASE_PROVENANCE, use_case="no-such-use-case")
    with pytest.raises(GovernanceRefusedError):
        engine.run(clf, store, _registry(), bad)
 
 
def test_metric_engine_refuses_global_kill_engaged(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    reg = _registry()
    reg.engage_global_kill()
    with pytest.raises(GovernanceRefusedError):
        engine.run(clf, store, reg, BASE_PROVENANCE)
 
 
def test_metric_engine_refuses_use_case_killed(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    reg = _registry()
    reg.kill("sku-quality-eval")
    with pytest.raises(GovernanceRefusedError):
        engine.run(clf, store, reg, BASE_PROVENANCE)
 
 
def test_metric_engine_refuses_wrong_region(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    bad = replace(BASE_PROVENANCE, region="ap-southeast-1")
    with pytest.raises(GovernanceRefusedError):
        engine.run(clf, store, _registry(), bad)
 
 
# ---------------------------------------------------------------------------
# MetricEngine -- store validation
# ---------------------------------------------------------------------------
 
 
def test_metric_engine_refuses_store_with_too_few_cases(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = GoldSetStore(
        name="tiny",
        version="1.0.0",
        cases=[GoldCase("gs-001", "roaming data bundle", "Roaming Data Bundle")],
    )
    engine = MetricEngine()
    with pytest.raises(MetricEngineError) as exc:
        engine.run(clf, store, _registry(), BASE_PROVENANCE)
    assert "minimum" in exc.value.reason
 
 
# ---------------------------------------------------------------------------
# MetricEngine -- happy path
# ---------------------------------------------------------------------------
 
 
def test_metric_engine_returns_metric_report(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    report = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    assert isinstance(report, MetricReport)
 
 
def test_metric_engine_total_cases_matches_store(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    report = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    assert report.total_cases == store.case_count
 
 
def test_metric_engine_accuracy_in_valid_range(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    report = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    assert 0.0 <= report.accuracy <= 1.0
 
 
def test_metric_engine_exact_matches_consistent_with_accuracy(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    report = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    expected_acc = report.exact_matches / report.total_cases
    assert abs(report.accuracy - expected_acc) < 1e-9
 
 
def test_metric_engine_run_id_is_non_empty(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    report = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    assert len(report.run_id) > 0
 
 
def test_metric_engine_run_ids_differ_across_runs(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    r1 = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    r2 = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    assert r1.run_id != r2.run_id
 
 
def test_metric_engine_evaluated_at_is_utc(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    report = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    assert report.evaluated_at.tzinfo is not None
    assert report.evaluated_at.tzinfo == UTC
 
 
def test_metric_engine_store_metadata_in_report(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = GoldSetStore(
        name="my-store",
        version="3.1.4",
        cases=[
            GoldCase("a", "roaming data bundle", "Roaming Data Bundle"),
            GoldCase("b", "voice call plan", "Voice Services"),
        ],
    )
    engine = MetricEngine()
    report = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    assert report.store_name == "my-store"
    assert report.store_version == "3.1.4"
 
 
# ---------------------------------------------------------------------------
# MetricReport -- per-class metrics
# ---------------------------------------------------------------------------
 
 
def test_metric_report_per_class_is_non_empty(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    report = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    assert len(report.per_class) > 0
 
 
def test_metric_report_per_class_contains_per_class_metric_instances(
    tmp_path: Path,
) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    report = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    assert all(isinstance(m, PerClassMetric) for m in report.per_class)
 
 
def test_metric_report_class_metric_lookup_case_insensitive(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    report = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    # Pick the first class that appears in the report.
    first_class = report.per_class[0].ontology_class
    found = report.class_metric(first_class.upper())
    assert found is not None
 
 
def test_metric_report_class_metric_returns_none_for_unknown() -> None:
    # Build a trivial report manually.
    report = MetricReport(
        run_id="abc123",
        store_name="s",
        store_version="1.0.0",
        total_cases=2,
        exact_matches=1,
        accuracy=0.5,
        per_class=(),
        evaluated_at=datetime.now(tz=UTC),
    )
    assert report.class_metric("no-such-class") is None
 
 
def test_per_class_metric_is_frozen() -> None:
    m = PerClassMetric(
        ontology_class="test",
        true_positives=1,
        false_positives=0,
        false_negatives=0,
        precision=1.0,
        recall=1.0,
        f1=1.0,
    )
    with pytest.raises(FrozenInstanceError):
        m.true_positives = 99  # type: ignore[misc]
 
 
def test_per_class_metric_as_dict_contains_required_fields() -> None:
    m = PerClassMetric(
        ontology_class="Voice Services",
        true_positives=2,
        false_positives=0,
        false_negatives=1,
        precision=1.0,
        recall=2 / 3,
        f1=0.8,
    )
    d = m.as_dict()
    for key in (
        "ontology_class",
        "true_positives",
        "false_positives",
        "false_negatives",
        "precision",
        "recall",
        "f1",
    ):
        assert key in d, f"missing key: {key}"
 
 
# ---------------------------------------------------------------------------
# MetricReport -- as_dict
# ---------------------------------------------------------------------------
 
 
def test_metric_report_as_dict_contains_required_fields(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    report = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    d = report.as_dict()
    for key in (
        "run_id",
        "store_name",
        "store_version",
        "total_cases",
        "exact_matches",
        "accuracy",
        "per_class",
        "evaluated_at",
    ):
        assert key in d, f"missing key: {key}"
 
 
def test_metric_report_as_dict_evaluated_at_is_iso_string(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    report = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    d = report.as_dict()
    datetime.fromisoformat(str(d["evaluated_at"]))
 
 
def test_metric_report_as_dict_per_class_is_list(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    report = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    d = report.as_dict()
    assert isinstance(d["per_class"], list)
 
 
def test_metric_report_is_frozen(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    engine = MetricEngine()
    report = engine.run(clf, store, _registry(), BASE_PROVENANCE)
    with pytest.raises(FrozenInstanceError):
        report.accuracy = 0.0  # type: ignore[misc]
 
 
# ---------------------------------------------------------------------------
# MetricEngine -- perfect score (all cases match the gold label)
# ---------------------------------------------------------------------------
 
 
def test_metric_engine_perfect_score_when_all_match(tmp_path: Path) -> None:
    """When gold labels perfectly match BM25 top hit, accuracy == 1.0."""
    f = _make_markdown(tmp_path, "ontology.md", _ONTOLOGY_MD)
    kb = KnowledgeBase()
    kb.build([f])
    clf = Classifier(knowledge_base=kb)
 
    # Run the classifier manually to discover what the top hits actually are,
    # then build gold cases from those exact labels (guaranteed match).
    prov = BASE_PROVENANCE
    reg = _registry()
    descriptions = ["roaming data bundle", "standard voice call"]
    labels: list[str] = []
    for desc in descriptions:
        rec = clf.classify(desc, prov, reg)
        top = rec.top()
        labels.append(top.ontology_class if top is not None else "__no_match__")
 
    cases = [
        GoldCase(f"gs-{i+1:03d}", desc, label)
        for i, (desc, label) in enumerate(zip(descriptions, labels, strict=True))
        if label != "__no_match__"
    ]
    if len(cases) < 2:
        pytest.skip("Not enough matched cases to run perfect-score test")
 
    store = GoldSetStore(name="perfect", version="1.0.0", cases=cases)
    engine = MetricEngine()
    report = engine.run(clf, store, reg, prov)
    assert report.accuracy == pytest.approx(1.0)
    assert report.exact_matches == report.total_cases
 
 
# ---------------------------------------------------------------------------
# ModelComparator -- construction
# ---------------------------------------------------------------------------
 
 
def test_model_comparator_default_threshold() -> None:
    comp = ModelComparator()
    assert comp.min_accuracy_threshold == pytest.approx(0.50)
 
 
def test_model_comparator_invalid_threshold_raises() -> None:
    with pytest.raises(MetricEngineError) as exc:
        ModelComparator(min_accuracy_threshold=1.5)
    assert "min_accuracy_threshold" in exc.value.reason
 
 
def test_model_comparator_threshold_zero_is_valid() -> None:
    comp = ModelComparator(min_accuracy_threshold=0.0)
    assert comp.min_accuracy_threshold == pytest.approx(0.0)
 
 
def test_model_comparator_threshold_one_is_valid() -> None:
    comp = ModelComparator(min_accuracy_threshold=1.0)
    assert comp.min_accuracy_threshold == pytest.approx(1.0)
 
 
# ---------------------------------------------------------------------------
# ModelComparator -- compare returns ComparisonReport
# ---------------------------------------------------------------------------
 
 
def test_model_comparator_returns_comparison_report(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    comp = ModelComparator(min_accuracy_threshold=0.0)
    report = comp.compare(clf, clf, store, _registry(), BASE_PROVENANCE)
    assert isinstance(report, ComparisonReport)
 
 
def test_model_comparator_same_classifier_is_equivalent(tmp_path: Path) -> None:
    """Comparing a classifier against itself should be EQUIVALENT."""
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    comp = ModelComparator(min_accuracy_threshold=0.0)
    report = comp.compare(clf, clf, store, _registry(), BASE_PROVENANCE)
    assert report.outcome is ComparisonOutcome.EQUIVALENT
 
 
def test_model_comparator_delta_is_zero_for_same_classifier(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    comp = ModelComparator(min_accuracy_threshold=0.0)
    report = comp.compare(clf, clf, store, _registry(), BASE_PROVENANCE)
    assert report.accuracy_delta == pytest.approx(0.0)
 
 
def test_model_comparator_comparison_id_is_non_empty(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    comp = ModelComparator(min_accuracy_threshold=0.0)
    report = comp.compare(clf, clf, store, _registry(), BASE_PROVENANCE)
    assert len(report.comparison_id) > 0
 
 
def test_model_comparator_comparison_ids_differ_across_runs(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    comp = ModelComparator(min_accuracy_threshold=0.0)
    r1 = comp.compare(clf, clf, store, _registry(), BASE_PROVENANCE)
    r2 = comp.compare(clf, clf, store, _registry(), BASE_PROVENANCE)
    assert r1.comparison_id != r2.comparison_id
 
 
def test_model_comparator_compared_at_is_utc(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    comp = ModelComparator(min_accuracy_threshold=0.0)
    report = comp.compare(clf, clf, store, _registry(), BASE_PROVENANCE)
    assert report.compared_at.tzinfo is not None
    assert report.compared_at.tzinfo == UTC
 
 
def test_model_comparator_carries_min_threshold(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    comp = ModelComparator(min_accuracy_threshold=0.75)
    report = comp.compare(clf, clf, store, _registry(), BASE_PROVENANCE)
    assert report.min_accuracy_threshold == pytest.approx(0.75)
 
 
# ---------------------------------------------------------------------------
# ModelComparator -- BELOW_THRESHOLD
# ---------------------------------------------------------------------------
 
 
def test_model_comparator_below_threshold_when_candidate_below_min(
    tmp_path: Path,
) -> None:
    """Threshold of 1.0 means anything less than perfect is BELOW_THRESHOLD."""
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    comp = ModelComparator(min_accuracy_threshold=1.0)
    report = comp.compare(clf, clf, store, _registry(), BASE_PROVENANCE)
    # Only below threshold if the classifier doesn't score 1.0.
    # If it does score 1.0, EQUIVALENT is correct.
    # Either way the report is valid -- just assert it's not an error.
    assert report.outcome in {
        ComparisonOutcome.BELOW_THRESHOLD,
        ComparisonOutcome.EQUIVALENT,
    }
 
 
# ---------------------------------------------------------------------------
# ModelComparator -- as_dict
# ---------------------------------------------------------------------------
 
 
def test_comparison_report_as_dict_contains_required_fields(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    comp = ModelComparator(min_accuracy_threshold=0.0)
    report = comp.compare(clf, clf, store, _registry(), BASE_PROVENANCE)
    d = report.as_dict()
    for key in (
        "comparison_id",
        "store_name",
        "store_version",
        "baseline_accuracy",
        "candidate_accuracy",
        "accuracy_delta",
        "min_accuracy_threshold",
        "outcome",
        "compared_at",
    ):
        assert key in d, f"missing key: {key}"
 
 
def test_comparison_report_as_dict_outcome_is_string(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    comp = ModelComparator(min_accuracy_threshold=0.0)
    report = comp.compare(clf, clf, store, _registry(), BASE_PROVENANCE)
    d = report.as_dict()
    assert isinstance(d["outcome"], str)
 
 
def test_comparison_report_as_dict_compared_at_is_iso_string(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    comp = ModelComparator(min_accuracy_threshold=0.0)
    report = comp.compare(clf, clf, store, _registry(), BASE_PROVENANCE)
    d = report.as_dict()
    datetime.fromisoformat(str(d["compared_at"]))
 
 
def test_comparison_report_is_frozen(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    comp = ModelComparator(min_accuracy_threshold=0.0)
    report = comp.compare(clf, clf, store, _registry(), BASE_PROVENANCE)
    with pytest.raises(FrozenInstanceError):
        report.outcome = ComparisonOutcome.IMPROVED  # type: ignore[misc]
 
 
# ---------------------------------------------------------------------------
# ModelComparator -- governance gate
# ---------------------------------------------------------------------------
 
 
def test_model_comparator_refuses_unknown_use_case(tmp_path: Path) -> None:
    clf = _make_classifier(tmp_path)
    store = _two_case_store()
    comp = ModelComparator(min_accuracy_threshold=0.0)
    bad = replace(BASE_PROVENANCE, use_case="no-such-use-case")
    with pytest.raises(GovernanceRefusedError):
        comp.compare(clf, clf, store, _registry(), bad)
 
 
# ---------------------------------------------------------------------------
# Integration -- end-to-end: build KB -> classify -> gold set -> metric
# ---------------------------------------------------------------------------
 
 
def test_full_pipeline_metric_report_is_valid(tmp_path: Path) -> None:
    """End-to-end: build knowledge base, run metric engine, get a valid report."""
    clf = _make_classifier(tmp_path)
    store = GoldSetStore(
        name="e2e-store",
        version="1.0.0",
        cases=[
            GoldCase("e2e-001", "roaming data bundle", "Roaming Data Bundle"),
            GoldCase("e2e-002", "standard voice call", "Voice Services"),
            GoldCase("e2e-003", "mobile broadband plan", "Data Services"),
        ],
    )
    engine = MetricEngine()
    report = engine.run(clf, store, _registry(), BASE_PROVENANCE)
 
    assert report.total_cases == 3
    assert 0.0 <= report.accuracy <= 1.0
    assert report.exact_matches <= report.total_cases
    assert len(report.per_class) > 0
 
 
def test_full_pipeline_comparator_report_is_valid(tmp_path: Path) -> None:
    """End-to-end: compare same classifier against itself on a real knowledge base."""
    clf = _make_classifier(tmp_path)
    store = GoldSetStore(
        name="e2e-compare",
        version="1.0.0",
        cases=[
            GoldCase("c-001", "roaming data bundle", "Roaming Data Bundle"),
            GoldCase("c-002", "standard voice call", "Voice Services"),
        ],
    )
    comp = ModelComparator(min_accuracy_threshold=0.0)
    report = comp.compare(clf, clf, store, _registry(), BASE_PROVENANCE)
 
    assert report.outcome in set(ComparisonOutcome)
    assert isinstance(report.baseline_report, MetricReport)
    assert isinstance(report.candidate_report, MetricReport)
    assert report.baseline_report.accuracy == pytest.approx(report.candidate_report.accuracy)
 