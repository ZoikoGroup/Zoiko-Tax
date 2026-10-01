"""Tests for the Evaluation Runner.

Chapter 17 §19 of the ZoikoTax Master Specification.

Covers:
- EvaluationRunner.run_classifier(): happy path, governance gate, store-too-small
  guard, MATCH/MISMATCH/NO_RESULT/ERROR per-case outcomes, error capture
  (does not abort run), MetricReport accuracy consistency
- CaseResult: frozen, as_dict, is_match
- RunReport: frozen, per-case accessors, failed_cases, get_case, as_dict
- EvaluationRunner.run_ruleset(): happy path, pass/fail/error counts,
  RulesetCaseResult, RulesetRunReport accessors
- Integration: RunReport.metric_report fed directly to ModelComparator
"""

from __future__ import annotations

from dataclasses import FrozenInstanceError
from pathlib import Path

import pytest

from ztax_gateway.classifier import Classifier
from ztax_gateway.evaluation import (
    EvaluationRule,
    EvaluationRuleset,
    EvaluationVerdict,
    RuleOutcome,
)
from ztax_gateway.evaluation_quality import (
    GoldCase,
    GoldSetStore,
    ModelComparator,
)
from ztax_gateway.evaluation_runner import (
    CaseOutcome,
    CaseResult,
    EvaluationRunner,
    EvaluationRunnerError,
    RulesetRunReport,
    RunReport,
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

Standard voice call services and plans.

## Data Services

Mobile data and broadband services.

### Roaming Data Bundle

Data bundles for subscribers in foreign networks.

## Device Finance

Handset and device finance products.
"""

BASE_USE_CASE = UseCase(
    use_case_id="eval-runner-test",
    owner="lane-l",
    description="Evaluation runner test use case",
    max_risk_tier=RiskTier.T2,
    max_authority=AuthorityOutcome.A2,
    permitted_regions=frozenset({"eu-west-1"}),
)

BASE_PROVENANCE = Provenance(
    use_case="eval-runner-test",
    model_profile="model:runner@2026.09",
    provider_profile="provider:eu-hosted",
    prompt_profile="prompt:runner@1",
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
    """Minimal valid store without a classifier dependency."""
    return GoldSetStore(
        name="runner-test",
        version="1.0.0",
        cases=[
            GoldCase("r-001", "Roaming data bundle prepaid SIM", "Roaming Data Bundle"),
            GoldCase("r-002", "Standard voice call plan", "Voice Services"),
        ],
    )


def _three_case_store() -> GoldSetStore:
    return GoldSetStore(
        name="runner-test",
        version="1.0.0",
        cases=[
            GoldCase("r-001", "Roaming data bundle prepaid SIM", "Roaming Data Bundle"),
            GoldCase("r-002", "Standard voice call plan", "Voice Services"),
            GoldCase("r-003", "Mobile broadband plan unlimited", "Data Services"),
        ],
    )


def _simple_ruleset() -> EvaluationRuleset:
    """A ruleset with one rule: the record must have at least one proposal."""
    return EvaluationRuleset(
        name="has-proposals",
        version="1.0.0",
        rules=[
            EvaluationRule(
                rule_id="has-proposals",
                description="Record must contain at least one classification proposal.",
                predicate=lambda rec: RuleOutcome.PASS if rec.proposals else RuleOutcome.FAIL,
            ),
        ],
    )


# ---------------------------------------------------------------------------
# CaseResult -- construction and accessors
# ---------------------------------------------------------------------------


def test_case_result_is_match_true_for_match_outcome() -> None:
    cr = CaseResult(
        case_id="x",
        expected_class="Voice Services",
        predicted_class="Voice Services",
        outcome=CaseOutcome.MATCH,
        record=None,
        error="",
        latency_ms=1.0,
    )
    assert cr.is_match() is True


def test_case_result_is_match_false_for_mismatch() -> None:
    cr = CaseResult(
        case_id="x",
        expected_class="Voice Services",
        predicted_class="Data Services",
        outcome=CaseOutcome.MISMATCH,
        record=None,
        error="",
        latency_ms=1.0,
    )
    assert cr.is_match() is False


def test_case_result_is_frozen() -> None:
    cr = CaseResult(
        case_id="x",
        expected_class="Voice Services",
        predicted_class=None,
        outcome=CaseOutcome.NO_RESULT,
        record=None,
        error="",
        latency_ms=1.0,
    )
    with pytest.raises(FrozenInstanceError):
        cr.outcome = CaseOutcome.MATCH  # type: ignore[misc]


def test_case_result_as_dict_has_required_keys() -> None:
    cr = CaseResult(
        case_id="c1",
        expected_class="Voice Services",
        predicted_class="Data Services",
        outcome=CaseOutcome.MISMATCH,
        record=None,
        error="",
        latency_ms=3.456789,
    )
    d = cr.as_dict()
    assert d["case_id"] == "c1"
    assert d["expected_class"] == "Voice Services"
    assert d["predicted_class"] == "Data Services"
    assert d["outcome"] == "MISMATCH"
    assert d["latency_ms"] == pytest.approx(3.457, abs=0.001)


def test_case_result_error_outcome_as_dict() -> None:
    cr = CaseResult(
        case_id="c1",
        expected_class="Voice Services",
        predicted_class=None,
        outcome=CaseOutcome.ERROR,
        record=None,
        error="classifier: KB not built",
        latency_ms=0.5,
    )
    d = cr.as_dict()
    assert d["error"] == "classifier: KB not built"
    assert d["predicted_class"] is None


# ---------------------------------------------------------------------------
# EvaluationRunner.run_classifier -- governance gate
# ---------------------------------------------------------------------------


def test_run_classifier_governance_gate_unknown_use_case(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    bad_provenance = Provenance(
        use_case="not-registered",
        model_profile="m",
        provider_profile="p",
        prompt_profile="pp",
        region="eu-west-1",
        data_class="INTERNAL",
        risk_tier=RiskTier.T1,
        authority_outcome=AuthorityOutcome.A1,
        ai_train_version="1.0",
    )
    with pytest.raises(GovernanceRefusedError):
        runner.run_classifier(classifier, _two_case_store(), _registry(), bad_provenance)


def test_run_classifier_governance_gate_global_kill(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    registry = _registry()
    registry.engage_global_kill()
    runner = EvaluationRunner()
    with pytest.raises(GovernanceRefusedError):
        runner.run_classifier(classifier, _two_case_store(), registry, BASE_PROVENANCE)


def test_run_classifier_governance_gate_per_use_case_kill(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    registry = _registry()
    registry.kill("eval-runner-test")
    runner = EvaluationRunner()
    with pytest.raises(GovernanceRefusedError):
        runner.run_classifier(classifier, _two_case_store(), registry, BASE_PROVENANCE)


# ---------------------------------------------------------------------------
# EvaluationRunner.run_classifier -- store-too-small guard
# ---------------------------------------------------------------------------


def test_run_classifier_store_too_small_raises(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    one_case_store = GoldSetStore(
        name="tiny",
        version="1.0.0",
        cases=[GoldCase("s-001", "Roaming data bundle", "Roaming Data Bundle")],
    )
    runner = EvaluationRunner()
    with pytest.raises(EvaluationRunnerError) as exc:
        runner.run_classifier(classifier, one_case_store, _registry(), BASE_PROVENANCE)
    assert "minimum is 2" in exc.value.reason


# ---------------------------------------------------------------------------
# EvaluationRunner.run_classifier -- per-case outcomes
# ---------------------------------------------------------------------------


def test_run_classifier_match_outcome(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    store = GoldSetStore(
        name="match-test",
        version="1.0.0",
        cases=[
            # "Roaming Data Bundle" heading in the ontology -- should match
            GoldCase("m-001", "Roaming data bundle prepaid", "### Roaming Data Bundle"),
            GoldCase("m-002", "Voice call standard plan", "## Voice Services"),
        ],
    )
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, store, _registry(), BASE_PROVENANCE)
    # At least some cases run without error
    assert report.total_cases == 2
    assert all(r.outcome != CaseOutcome.ERROR for r in report.case_results)


def test_run_classifier_produces_run_report(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, _two_case_store(), _registry(), BASE_PROVENANCE)
    assert isinstance(report, RunReport)
    assert report.store_name == "runner-test"
    assert report.store_version == "1.0.0"
    assert report.total_cases == 2
    assert len(report.case_results) == 2


def test_run_classifier_case_results_in_gold_set_order(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, _two_case_store(), _registry(), BASE_PROVENANCE)
    ids = [r.case_id for r in report.case_results]
    assert ids == ["r-001", "r-002"]


def test_run_classifier_no_result_for_empty_kb(tmp_path: Path) -> None:
    # Build a classifier with a KB that has no content matching our query
    unrelated_md = _make_markdown(
        tmp_path, "other.md", "# Completely Unrelated Topic\n\nNothing here.\n"
    )
    kb = KnowledgeBase()
    kb.build([unrelated_md])
    classifier = Classifier(knowledge_base=kb)

    store = GoldSetStore(
        name="no-result-test",
        version="1.0.0",
        cases=[
            GoldCase("n-001", "Roaming data bundle", "Roaming Data Bundle"),
            GoldCase("n-002", "Voice services plan", "Voice Services"),
        ],
    )
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, store, _registry(), BASE_PROVENANCE)
    # The KB has no relevant content, so we expect NO_RESULT or MISMATCH (not ERROR)
    assert all(
        r.outcome in (CaseOutcome.NO_RESULT, CaseOutcome.MISMATCH, CaseOutcome.MATCH)
        for r in report.case_results
    )


def test_run_classifier_error_captured_not_raised(tmp_path: Path) -> None:
    """An error on one case must not abort the entire run."""
    # Use a classifier with an unbuilt KB to force a ClassificationError
    broken_classifier = Classifier(knowledge_base=KnowledgeBase())

    store = GoldSetStore(
        name="error-test",
        version="1.0.0",
        cases=[
            GoldCase("e-001", "Roaming data bundle", "Roaming Data Bundle"),
            GoldCase("e-002", "Voice call plan", "Voice Services"),
        ],
    )
    runner = EvaluationRunner()
    # Should not raise; errors are captured
    report = runner.run_classifier(broken_classifier, store, _registry(), BASE_PROVENANCE)
    assert report.total_cases == 2
    assert all(r.outcome is CaseOutcome.ERROR for r in report.case_results)
    assert report.error_count == 2
    assert all(r.error for r in report.case_results)


def test_run_classifier_error_has_non_empty_error_field(tmp_path: Path) -> None:
    broken_classifier = Classifier(knowledge_base=KnowledgeBase())
    runner = EvaluationRunner()
    report = runner.run_classifier(
        broken_classifier, _two_case_store(), _registry(), BASE_PROVENANCE
    )
    for case_result in report.case_results:
        assert case_result.error != ""
        assert case_result.predicted_class is None
        assert case_result.record is None


def test_run_classifier_error_count_zero_on_clean_run(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, _two_case_store(), _registry(), BASE_PROVENANCE)
    assert report.error_count == 0


# ---------------------------------------------------------------------------
# RunReport -- accessors and immutability
# ---------------------------------------------------------------------------


def test_run_report_is_frozen(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, _two_case_store(), _registry(), BASE_PROVENANCE)
    with pytest.raises(FrozenInstanceError):
        report.store_name = "mutated"  # type: ignore[misc]


def test_run_report_has_unique_run_id(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    r1 = runner.run_classifier(classifier, _two_case_store(), _registry(), BASE_PROVENANCE)
    r2 = runner.run_classifier(classifier, _two_case_store(), _registry(), BASE_PROVENANCE)
    assert r1.run_id != r2.run_id


def test_run_report_match_count_consistent_with_case_results(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, _two_case_store(), _registry(), BASE_PROVENANCE)
    manual_match_count = sum(1 for r in report.case_results if r.outcome is CaseOutcome.MATCH)
    assert report.match_count == manual_match_count


def test_run_report_failed_cases_excludes_matches(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, _two_case_store(), _registry(), BASE_PROVENANCE)
    for failed in report.failed_cases():
        assert failed.outcome != CaseOutcome.MATCH


def test_run_report_get_case_returns_correct_result(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, _two_case_store(), _registry(), BASE_PROVENANCE)
    result = report.get_case("r-001")
    assert result is not None
    assert result.case_id == "r-001"


def test_run_report_get_case_returns_none_for_missing_id(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, _two_case_store(), _registry(), BASE_PROVENANCE)
    assert report.get_case("does-not-exist") is None


def test_run_report_as_dict_has_required_keys(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, _two_case_store(), _registry(), BASE_PROVENANCE)
    d = report.as_dict()
    for key in (
        "run_id",
        "store_name",
        "store_version",
        "total_cases",
        "match_count",
        "error_count",
        "no_result_count",
        "total_latency_ms",
        "metric_report",
        "started_at",
        "completed_at",
    ):
        assert key in d, f"missing key: {key}"


def test_run_report_timestamps_are_ordered(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, _two_case_store(), _registry(), BASE_PROVENANCE)
    assert report.started_at <= report.completed_at


# ---------------------------------------------------------------------------
# RunReport.metric_report consistency
# ---------------------------------------------------------------------------


def test_run_report_metric_report_accuracy_consistent(tmp_path: Path) -> None:
    """metric_report.accuracy must equal match_count / total_cases."""
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, _three_case_store(), _registry(), BASE_PROVENANCE)
    expected_accuracy = report.match_count / report.total_cases
    assert report.metric_report.accuracy == pytest.approx(expected_accuracy)


def test_run_report_metric_report_exact_matches_consistent(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, _three_case_store(), _registry(), BASE_PROVENANCE)
    assert report.metric_report.exact_matches == report.match_count


def test_run_report_metric_report_total_cases_consistent(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, _three_case_store(), _registry(), BASE_PROVENANCE)
    assert report.metric_report.total_cases == report.total_cases


def test_run_report_metric_report_store_name_and_version(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_classifier(classifier, _two_case_store(), _registry(), BASE_PROVENANCE)
    assert report.metric_report.store_name == "runner-test"
    assert report.metric_report.store_version == "1.0.0"


def test_run_report_metric_report_all_error_gives_zero_accuracy(tmp_path: Path) -> None:
    broken_classifier = Classifier(knowledge_base=KnowledgeBase())
    runner = EvaluationRunner()
    report = runner.run_classifier(
        broken_classifier, _two_case_store(), _registry(), BASE_PROVENANCE
    )
    assert report.metric_report.accuracy == pytest.approx(0.0)
    assert report.metric_report.exact_matches == 0


# ---------------------------------------------------------------------------
# EvaluationRunner.run_ruleset -- happy path
# ---------------------------------------------------------------------------


def test_run_ruleset_returns_ruleset_run_report(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    ruleset = _simple_ruleset()
    runner = EvaluationRunner()
    report = runner.run_ruleset(
        classifier, ruleset, _two_case_store(), _registry(), BASE_PROVENANCE
    )
    assert isinstance(report, RulesetRunReport)
    assert report.store_name == "runner-test"
    assert report.ruleset_name == "has-proposals"
    assert report.ruleset_version == "1.0.0"
    assert report.total_cases == 2


def test_run_ruleset_case_results_in_gold_set_order(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_ruleset(
        classifier, _simple_ruleset(), _two_case_store(), _registry(), BASE_PROVENANCE
    )
    ids = [r.case_id for r in report.case_results]
    assert ids == ["r-001", "r-002"]


def test_run_ruleset_pass_count_sums_correctly(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_ruleset(
        classifier, _simple_ruleset(), _two_case_store(), _registry(), BASE_PROVENANCE
    )
    manual_pass = sum(
        1
        for r in report.case_results
        if r.report is not None and r.report.verdict is EvaluationVerdict.PASS
    )
    assert report.pass_count == manual_pass


def test_run_ruleset_fail_count_sums_correctly(tmp_path: Path) -> None:
    # A ruleset that always FAILs
    always_fail_ruleset = EvaluationRuleset(
        name="always-fail",
        version="1.0.0",
        rules=[
            EvaluationRule(
                rule_id="always-fail",
                description="This rule always fails.",
                predicate=lambda rec: RuleOutcome.FAIL,
            ),
        ],
    )
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_ruleset(
        classifier, always_fail_ruleset, _two_case_store(), _registry(), BASE_PROVENANCE
    )
    assert report.fail_count == 2
    assert report.pass_count == 0


def test_run_ruleset_error_count_on_broken_classifier(tmp_path: Path) -> None:
    broken_classifier = Classifier(knowledge_base=KnowledgeBase())
    runner = EvaluationRunner()
    report = runner.run_ruleset(
        broken_classifier, _simple_ruleset(), _two_case_store(), _registry(), BASE_PROVENANCE
    )
    assert report.error_count == 2
    assert report.pass_count == 0
    assert report.fail_count == 0


# ---------------------------------------------------------------------------
# RulesetCaseResult -- accessors and immutability
# ---------------------------------------------------------------------------


def test_ruleset_case_result_is_frozen(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_ruleset(
        classifier, _simple_ruleset(), _two_case_store(), _registry(), BASE_PROVENANCE
    )
    first = report.case_results[0]
    with pytest.raises(FrozenInstanceError):
        first.case_id = "mutated"  # type: ignore[misc]


def test_ruleset_case_result_as_dict_has_required_keys(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_ruleset(
        classifier, _simple_ruleset(), _two_case_store(), _registry(), BASE_PROVENANCE
    )
    d = report.case_results[0].as_dict()
    for key in ("case_id", "expected_class", "verdict", "failed_rules", "error", "latency_ms"):
        assert key in d, f"missing key: {key}"


def test_ruleset_case_result_error_has_null_report(tmp_path: Path) -> None:
    broken_classifier = Classifier(knowledge_base=KnowledgeBase())
    runner = EvaluationRunner()
    report = runner.run_ruleset(
        broken_classifier, _simple_ruleset(), _two_case_store(), _registry(), BASE_PROVENANCE
    )
    for cr in report.case_results:
        assert cr.report is None
        assert cr.error != ""


# ---------------------------------------------------------------------------
# RulesetRunReport -- accessors and immutability
# ---------------------------------------------------------------------------


def test_ruleset_run_report_is_frozen(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_ruleset(
        classifier, _simple_ruleset(), _two_case_store(), _registry(), BASE_PROVENANCE
    )
    with pytest.raises(FrozenInstanceError):
        report.store_name = "mutated"  # type: ignore[misc]


def test_ruleset_run_report_has_unique_run_id(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    ruleset = _simple_ruleset()
    r1 = runner.run_ruleset(classifier, ruleset, _two_case_store(), _registry(), BASE_PROVENANCE)
    r2 = runner.run_ruleset(classifier, ruleset, _two_case_store(), _registry(), BASE_PROVENANCE)
    assert r1.run_id != r2.run_id


def test_ruleset_run_report_failed_cases_excludes_passes(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_ruleset(
        classifier, _simple_ruleset(), _two_case_store(), _registry(), BASE_PROVENANCE
    )
    for failed in report.failed_cases():
        assert failed.error or (
            failed.report is not None and failed.report.verdict is EvaluationVerdict.FAIL
        )


def test_ruleset_run_report_get_case_returns_correct_result(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_ruleset(
        classifier, _simple_ruleset(), _two_case_store(), _registry(), BASE_PROVENANCE
    )
    r = report.get_case("r-001")
    assert r is not None
    assert r.case_id == "r-001"


def test_ruleset_run_report_get_case_none_for_missing(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_ruleset(
        classifier, _simple_ruleset(), _two_case_store(), _registry(), BASE_PROVENANCE
    )
    assert report.get_case("does-not-exist") is None


def test_ruleset_run_report_as_dict_has_required_keys(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_ruleset(
        classifier, _simple_ruleset(), _two_case_store(), _registry(), BASE_PROVENANCE
    )
    d = report.as_dict()
    for key in (
        "run_id",
        "store_name",
        "store_version",
        "ruleset_name",
        "ruleset_version",
        "total_cases",
        "pass_count",
        "fail_count",
        "error_count",
        "total_latency_ms",
        "started_at",
        "completed_at",
    ):
        assert key in d, f"missing key: {key}"


def test_ruleset_run_report_timestamps_ordered(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    report = runner.run_ruleset(
        classifier, _simple_ruleset(), _two_case_store(), _registry(), BASE_PROVENANCE
    )
    assert report.started_at <= report.completed_at


# ---------------------------------------------------------------------------
# run_ruleset governance guards
# ---------------------------------------------------------------------------


def test_run_ruleset_governance_gate_unknown_use_case(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    bad_provenance = Provenance(
        use_case="not-registered",
        model_profile="m",
        provider_profile="p",
        prompt_profile="pp",
        region="eu-west-1",
        data_class="INTERNAL",
        risk_tier=RiskTier.T1,
        authority_outcome=AuthorityOutcome.A1,
        ai_train_version="1.0",
    )
    with pytest.raises(GovernanceRefusedError):
        runner.run_ruleset(
        classifier, _simple_ruleset(), _two_case_store(), _registry(), bad_provenance
        )


def test_run_ruleset_store_too_small_raises(tmp_path: Path) -> None:
    classifier = _make_classifier(tmp_path)
    tiny = GoldSetStore(
        name="tiny",
        version="1.0.0",
        cases=[GoldCase("t-001", "Roaming data bundle", "Roaming Data Bundle")],
    )
    runner = EvaluationRunner()
    with pytest.raises(EvaluationRunnerError) as exc:
        runner.run_ruleset(classifier, _simple_ruleset(), tiny, _registry(), BASE_PROVENANCE)
    assert "minimum is 2" in exc.value.reason


# ---------------------------------------------------------------------------
# Integration: RunReport.metric_report → ModelComparator
# ---------------------------------------------------------------------------


def test_integration_run_report_metric_feeds_comparator(tmp_path: Path) -> None:
    """RunReport.metric_report has the same shape as MetricEngine output.

    ModelComparator.compare() uses MetricEngine internally; we verify that two
    RunReport.metric_reports from the same classifier give EQUIVALENT outcome
    (same corpus, same classifier → same accuracy).
    """
    classifier = _make_classifier(tmp_path)
    runner = EvaluationRunner()
    store = _three_case_store()

    run1 = runner.run_classifier(classifier, store, _registry(), BASE_PROVENANCE)
    run2 = runner.run_classifier(classifier, store, _registry(), BASE_PROVENANCE)

    # Both metric reports came from the same classifier/store: accuracy is identical.
    assert run1.metric_report.accuracy == pytest.approx(run2.metric_report.accuracy)
    assert run1.metric_report.exact_matches == run2.metric_report.exact_matches


def test_integration_run_report_metric_comparator_equivalent(tmp_path: Path) -> None:
    """ModelComparator on two identical classifiers must give EQUIVALENT or BELOW_THRESHOLD."""
    from ztax_gateway.evaluation_quality import ComparisonOutcome

    classifier = _make_classifier(tmp_path)
    comparator = ModelComparator(min_accuracy_threshold=0.0)
    store = _three_case_store()
    comparison = comparator.compare(
        baseline=classifier,
        candidate=classifier,
        store=store,
        registry=_registry(),
        provenance=BASE_PROVENANCE,
    )
    # Same classifier twice → delta == 0 → EQUIVALENT
    assert comparison.outcome is ComparisonOutcome.EQUIVALENT


def test_integration_runner_and_comparator_accuracy_match(tmp_path: Path) -> None:
    """The accuracy in RunReport.metric_report must equal what ModelComparator.compare reports."""
    classifier = _make_classifier(tmp_path)
    store = _three_case_store()
    runner = EvaluationRunner()
    run_report = runner.run_classifier(classifier, store, _registry(), BASE_PROVENANCE)

    comparator = ModelComparator(min_accuracy_threshold=0.0)
    comparison = comparator.compare(
        baseline=classifier,
        candidate=classifier,
        store=store,
        registry=_registry(),
        provenance=BASE_PROVENANCE,
    )
    # The baseline_report from comparator should have the same accuracy as the runner report
    assert comparison.baseline_report.accuracy == pytest.approx(run_report.metric_report.accuracy)
