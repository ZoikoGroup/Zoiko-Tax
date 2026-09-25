"""Tests for the Evaluation Service.

Chapter 17 §17 of the ZoikoTax Master Specification.

Mirrors the structure of ``test_classifier.py`` and ``test_tool_broker.py``.
Every public API is exercised; every error path is explicitly asserted so a
regression surfaces as a test failure rather than a silent wrong answer in an
AI audit log.
"""

from __future__ import annotations

from dataclasses import FrozenInstanceError, replace
from datetime import UTC, datetime
from pathlib import Path

import pytest

from ztax_gateway.classifier import ClassificationRecord, Classifier
from ztax_gateway.evaluation import (
    EvaluationError,
    EvaluationReport,
    EvaluationRule,
    EvaluationRuleset,
    EvaluationVerdict,
    Evaluator,
    RuleOutcome,
    RuleResult,
    evaluate,
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
    use_case_id="sku-evaluation",
    owner="lane-l",
    description="Evaluate SKU classification records",
    max_risk_tier=RiskTier.T2,
    max_authority=AuthorityOutcome.A2,
    permitted_regions=frozenset({"eu-west-1"}),
)

BASE_PROVENANCE = Provenance(
    use_case="sku-evaluation",
    model_profile="model:evaluator@2026.09",
    provider_profile="provider:eu-hosted",
    prompt_profile="prompt:evaluation@1",
    region="eu-west-1",
    data_class="INTERNAL",
    risk_tier=RiskTier.T1,
    authority_outcome=AuthorityOutcome.A1,
    ai_train_version="0.5.0",
)

# Classifier use case (needed to produce records)
_CLF_USE_CASE = UseCase(
    use_case_id="sku-classification",
    owner="lane-l",
    description="Classify telecom SKUs",
    max_risk_tier=RiskTier.T2,
    max_authority=AuthorityOutcome.A2,
    permitted_regions=frozenset({"eu-west-1"}),
)
_CLF_PROVENANCE = replace(BASE_PROVENANCE, use_case="sku-classification")


def _registry(*extra: UseCase) -> UseCaseRegistry:
    return UseCaseRegistry([BASE_USE_CASE, _CLF_USE_CASE, *extra])


def _make_markdown(tmp_path: Path, name: str, content: str) -> Path:
    p = tmp_path / name
    p.write_text(content, encoding="utf-8")
    return p


def _make_record(tmp_path: Path, desc: str = "roaming data bundle") -> ClassificationRecord:
    f = _make_markdown(tmp_path, "ontology.md", _ONTOLOGY_MD)
    kb = KnowledgeBase()
    kb.build([f])
    clf = Classifier(knowledge_base=kb)
    return clf.classify(desc, _CLF_PROVENANCE, _registry())


# ---------------------------------------------------------------------------
# Standard rules used across many tests
# ---------------------------------------------------------------------------

_RULE_HAS_PROPOSALS = EvaluationRule(
    rule_id="has-proposals",
    description="Record must contain at least one classification proposal.",
    predicate=lambda rec: RuleOutcome.PASS if rec.proposals else RuleOutcome.FAIL,
)

_RULE_CITATION_VERIFIES = EvaluationRule(
    rule_id="citation-verifies",
    description="Top proposal citation must verify its hash.",
    predicate=lambda rec: (
        RuleOutcome.SKIP
        if rec.top() is None
        else (RuleOutcome.PASS if rec.top().citation.verify_all() else RuleOutcome.FAIL)  # type: ignore[union-attr]
    ),
)

_RULE_ALWAYS_PASS = EvaluationRule(
    rule_id="always-pass",
    description="Always passes.",
    predicate=lambda _: RuleOutcome.PASS,
)

_RULE_ALWAYS_FAIL = EvaluationRule(
    rule_id="always-fail",
    description="Always fails.",
    predicate=lambda _: RuleOutcome.FAIL,
)

_RULE_ALWAYS_SKIP = EvaluationRule(
    rule_id="always-skip",
    description="Always skips.",
    predicate=lambda _: RuleOutcome.SKIP,
)


def _ruleset(
    *rules: EvaluationRule, name: str = "test-ruleset", version: str = "1.0.0"
) -> EvaluationRuleset:
    return EvaluationRuleset(name=name, version=version, rules=list(rules))


# ---------------------------------------------------------------------------
# Happy path — evaluate returns a report
# ---------------------------------------------------------------------------


def test_evaluate_returns_report(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_HAS_PROPOSALS, _RULE_CITATION_VERIFIES)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert isinstance(report, EvaluationReport)


def test_evaluate_all_pass_gives_pass_verdict(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert report.verdict is EvaluationVerdict.PASS


def test_evaluate_one_fail_gives_fail_verdict(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS, _RULE_ALWAYS_FAIL)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert report.verdict is EvaluationVerdict.FAIL


def test_evaluate_all_skip_gives_inconclusive_verdict(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_SKIP)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert report.verdict is EvaluationVerdict.INCONCLUSIVE


def test_evaluate_skip_and_pass_gives_pass_verdict(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_SKIP, _RULE_ALWAYS_PASS)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert report.verdict is EvaluationVerdict.PASS


def test_evaluate_all_rules_are_run_no_short_circuit(tmp_path: Path) -> None:
    """Even when one rule fails, all others still run."""
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_FAIL, _RULE_ALWAYS_PASS, _RULE_ALWAYS_SKIP)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert len(report.results) == 3


def test_evaluate_results_are_in_registration_order(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS, _RULE_ALWAYS_FAIL, _RULE_ALWAYS_SKIP)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert report.results[0].rule_id == "always-pass"
    assert report.results[1].rule_id == "always-fail"
    assert report.results[2].rule_id == "always-skip"


def test_evaluate_report_carries_ruleset_metadata(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS, name="my-ruleset", version="2.3.1")
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert report.ruleset_name == "my-ruleset"
    assert report.ruleset_version == "2.3.1"


def test_evaluate_report_id_is_non_empty(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert len(report.report_id) > 0


def test_evaluate_report_ids_differ_across_runs(tmp_path: Path) -> None:
    """Each evaluation gets a unique report_id (UUID mixing)."""
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS)
    r1 = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    r2 = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert r1.report_id != r2.report_id


def test_evaluate_evaluated_at_is_utc_aware(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert report.evaluated_at.tzinfo is not None
    assert report.evaluated_at.tzinfo == UTC


# ---------------------------------------------------------------------------
# Citation traceability
# ---------------------------------------------------------------------------


def test_evaluate_report_carries_record_citation_id_when_proposals_exist(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    assert record.top() is not None
    rs = _ruleset(_RULE_ALWAYS_PASS)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert report.record_citation_id == record.top().citation.citation_id  # type: ignore[union-attr]


def test_evaluate_report_citation_id_is_none_when_no_proposals(tmp_path: Path) -> None:
    # Force a record with no proposals by querying a term that doesn't match.
    record = _make_record(tmp_path, desc="xyzzy completely absent term here")
    assert record.top() is None
    rs = _ruleset(_RULE_ALWAYS_PASS)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert report.record_citation_id is None


# ---------------------------------------------------------------------------
# Counts and helpers
# ---------------------------------------------------------------------------


def test_pass_count(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    pass_a = EvaluationRule("pass-a", "passes first", lambda _: RuleOutcome.PASS)
    pass_b = EvaluationRule("pass-b", "passes second", lambda _: RuleOutcome.PASS)
    rs = _ruleset(pass_a, pass_b, _RULE_ALWAYS_FAIL)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert report.pass_count() == 2


def test_fail_count(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    fail_a = EvaluationRule("fail-a-cnt", "fails first", lambda _: RuleOutcome.FAIL)
    fail_b = EvaluationRule("fail-b-cnt", "fails second", lambda _: RuleOutcome.FAIL)
    rs = _ruleset(fail_a, fail_b, _RULE_ALWAYS_PASS)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert report.fail_count() == 2


def test_skip_count(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    skip_a = EvaluationRule("skip-a-cnt", "skips first", lambda _: RuleOutcome.SKIP)
    skip_b = EvaluationRule("skip-b-cnt", "skips second", lambda _: RuleOutcome.SKIP)
    rs = _ruleset(skip_a, skip_b, _RULE_ALWAYS_PASS)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert report.skip_count() == 2


def test_failed_rules_returns_ids_in_order(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    fail_a = EvaluationRule("fail-a", "fails", lambda _: RuleOutcome.FAIL)
    fail_b = EvaluationRule("fail-b", "also fails", lambda _: RuleOutcome.FAIL)
    rs = _ruleset(_RULE_ALWAYS_PASS, fail_a, _RULE_ALWAYS_SKIP, fail_b)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert report.failed_rules() == ["fail-a", "fail-b"]


def test_failed_rules_empty_when_all_pass(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert report.failed_rules() == []


# ---------------------------------------------------------------------------
# as_dict
# ---------------------------------------------------------------------------


def test_report_as_dict_contains_required_fields(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS, _RULE_ALWAYS_FAIL)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    d = report.as_dict()
    for key in (
        "report_id", "record_citation_id", "ruleset_name", "ruleset_version",
        "verdict", "pass_count", "fail_count", "skip_count",
        "failed_rules", "evaluated_at",
    ):
        assert key in d, f"missing key: {key}"


def test_report_as_dict_evaluated_at_is_iso_string(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    d = report.as_dict()
    # Should not raise — it's a valid ISO timestamp string.
    datetime.fromisoformat(str(d["evaluated_at"]))


# ---------------------------------------------------------------------------
# Governance gate
# ---------------------------------------------------------------------------


def test_evaluate_refuses_unknown_use_case(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS)
    bad = replace(BASE_PROVENANCE, use_case="no-such-use-case")
    with pytest.raises(GovernanceRefusedError) as exc:
        evaluate(record, rs, _registry(), bad)
    from ztax_gateway.governance import Refusal
    assert exc.value.refusal is Refusal.UNKNOWN_USE_CASE


def test_evaluate_refuses_when_global_kill_engaged(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS)
    reg = _registry()
    reg.engage_global_kill()
    with pytest.raises(GovernanceRefusedError) as exc:
        evaluate(record, rs, reg, BASE_PROVENANCE)
    from ztax_gateway.governance import Refusal
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED


def test_evaluate_refuses_when_use_case_killed(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS)
    reg = _registry()
    reg.kill("sku-evaluation")
    with pytest.raises(GovernanceRefusedError) as exc:
        evaluate(record, rs, reg, BASE_PROVENANCE)
    from ztax_gateway.governance import Refusal
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED


def test_evaluate_refuses_wrong_region(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS)
    bad = replace(BASE_PROVENANCE, region="ap-southeast-1")
    with pytest.raises(GovernanceRefusedError) as exc:
        evaluate(record, rs, _registry(), bad)
    from ztax_gateway.governance import Refusal
    assert exc.value.refusal is Refusal.RESIDENCY_REFUSED


def test_governance_fires_before_any_rule_runs(tmp_path: Path) -> None:
    """No rule should run if the governance gate refuses."""
    ran: list[str] = []

    def spy(_: object) -> RuleOutcome:
        ran.append("ran")
        return RuleOutcome.PASS

    rule = EvaluationRule("spy", "spy rule", spy)
    record = _make_record(tmp_path)
    rs = _ruleset(rule)
    bad = replace(BASE_PROVENANCE, use_case="no-such-use-case")
    with pytest.raises(GovernanceRefusedError):
        evaluate(record, rs, _registry(), bad)
    assert ran == []


# ---------------------------------------------------------------------------
# Precondition errors
# ---------------------------------------------------------------------------


def test_evaluate_empty_ruleset_raises(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = EvaluationRuleset(name="empty", version="1.0.0", rules=[])
    with pytest.raises(EvaluationError) as exc:
        evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert "no rules" in exc.value.reason


def test_evaluate_rule_that_raises_produces_fail_result(tmp_path: Path) -> None:
    """A rule predicate that raises is treated as FAIL, not as an unhandled exception."""
    def exploding(_: object) -> RuleOutcome:
        raise RuntimeError("predicate exploded")

    rule = EvaluationRule("exploder", "always explodes", exploding)
    record = _make_record(tmp_path)
    rs = _ruleset(rule)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    assert report.verdict is EvaluationVerdict.FAIL
    assert report.results[0].outcome is RuleOutcome.FAIL
    assert "predicate exploded" in report.results[0].detail


# ---------------------------------------------------------------------------
# EvaluationRule — construction and immutability
# ---------------------------------------------------------------------------


def test_rule_with_no_id_raises() -> None:
    with pytest.raises(EvaluationError):
        EvaluationRule(rule_id="", description="desc", predicate=lambda _: RuleOutcome.PASS)


def test_rule_with_no_description_raises() -> None:
    with pytest.raises(EvaluationError):
        EvaluationRule(rule_id="some-id", description="", predicate=lambda _: RuleOutcome.PASS)


def test_rule_is_frozen() -> None:
    rule = EvaluationRule("r", "desc", lambda _: RuleOutcome.PASS)
    with pytest.raises(FrozenInstanceError):
        rule.rule_id = "mutated"  # type: ignore[misc]


# ---------------------------------------------------------------------------
# EvaluationRuleset — construction and immutability
# ---------------------------------------------------------------------------


def test_ruleset_with_no_name_raises() -> None:
    with pytest.raises(EvaluationError):
        EvaluationRuleset(name="", version="1.0.0")


def test_ruleset_with_no_version_raises() -> None:
    with pytest.raises(EvaluationError):
        EvaluationRuleset(name="test", version="")


def test_ruleset_duplicate_rule_id_raises() -> None:
    r1 = EvaluationRule("dup", "first", lambda _: RuleOutcome.PASS)
    r2 = EvaluationRule("dup", "second", lambda _: RuleOutcome.FAIL)
    with pytest.raises(EvaluationError) as exc:
        EvaluationRuleset(name="rs", version="1.0.0", rules=[r1, r2])
    assert "already registered" in exc.value.reason


def test_ruleset_is_immutable_after_construction() -> None:
    rs = EvaluationRuleset(name="rs", version="1.0.0", rules=[_RULE_ALWAYS_PASS])
    extra = EvaluationRule("extra", "extra", lambda _: RuleOutcome.PASS)
    with pytest.raises(EvaluationError) as exc:
        rs._add(extra)
    assert "immutable" in exc.value.reason


def test_ruleset_rule_count(tmp_path: Path) -> None:
    rs = _ruleset(_RULE_ALWAYS_PASS, _RULE_ALWAYS_FAIL, _RULE_ALWAYS_SKIP)
    assert rs.rule_count == 3


def test_ruleset_rules_property_returns_tuple() -> None:
    rs = _ruleset(_RULE_ALWAYS_PASS, _RULE_ALWAYS_FAIL)
    assert isinstance(rs.rules, tuple)
    assert len(rs.rules) == 2


def test_ruleset_get_returns_rule_by_id() -> None:
    rs = _ruleset(_RULE_ALWAYS_PASS)
    assert rs.get("always-pass") is _RULE_ALWAYS_PASS


def test_ruleset_get_returns_none_for_unknown_id() -> None:
    rs = _ruleset(_RULE_ALWAYS_PASS)
    assert rs.get("no-such-rule") is None


# ---------------------------------------------------------------------------
# RuleResult — immutability
# ---------------------------------------------------------------------------


def test_rule_result_is_frozen() -> None:
    r = RuleResult(rule_id="r", outcome=RuleOutcome.PASS)
    with pytest.raises(FrozenInstanceError):
        r.outcome = RuleOutcome.FAIL  # type: ignore[misc]


def test_rule_result_detail_defaults_to_empty_string() -> None:
    r = RuleResult(rule_id="r", outcome=RuleOutcome.PASS)
    assert r.detail == ""


# ---------------------------------------------------------------------------
# EvaluationReport — immutability
# ---------------------------------------------------------------------------


def test_evaluation_report_is_frozen(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS)
    report = evaluate(record, rs, _registry(), BASE_PROVENANCE)
    with pytest.raises(FrozenInstanceError):
        report.verdict = EvaluationVerdict.FAIL  # type: ignore[misc]


# ---------------------------------------------------------------------------
# Evaluator wrapper
# ---------------------------------------------------------------------------


def test_evaluator_run_returns_report(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS)
    ev = Evaluator(ruleset=rs)
    report = ev.run(record, registry=_registry(), provenance=BASE_PROVENANCE)
    assert isinstance(report, EvaluationReport)


def test_evaluator_run_applies_bound_ruleset(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_FAIL, name="bound-rs", version="9.9.9")
    ev = Evaluator(ruleset=rs)
    report = ev.run(record, registry=_registry(), provenance=BASE_PROVENANCE)
    assert report.verdict is EvaluationVerdict.FAIL
    assert report.ruleset_name == "bound-rs"


def test_evaluator_run_respects_governance(tmp_path: Path) -> None:
    record = _make_record(tmp_path)
    rs = _ruleset(_RULE_ALWAYS_PASS)
    ev = Evaluator(ruleset=rs)
    bad = replace(BASE_PROVENANCE, use_case="no-such-use-case")
    with pytest.raises(GovernanceRefusedError):
        ev.run(record, registry=_registry(), provenance=bad)


# ---------------------------------------------------------------------------
# Integration — evaluate a real classifier record end-to-end
# ---------------------------------------------------------------------------


def test_full_pipeline_classify_then_evaluate(tmp_path: Path) -> None:
    """End-to-end: classify → evaluate with two real rules."""
    f = _make_markdown(tmp_path, "ontology.md", _ONTOLOGY_MD)
    kb = KnowledgeBase()
    kb.build([f])
    clf = Classifier(knowledge_base=kb)
    record = clf.classify("Roaming Data", _CLF_PROVENANCE, _registry())

    ruleset = EvaluationRuleset(
        name="classification-gate",
        version="1.0.0",
        rules=[_RULE_HAS_PROPOSALS, _RULE_CITATION_VERIFIES],
    )
    report = evaluate(record, ruleset, _registry(), BASE_PROVENANCE)

    assert report.verdict is EvaluationVerdict.PASS
    assert report.pass_count() >= 1
    assert report.record_citation_id is not None


def test_full_pipeline_empty_result_fails_has_proposals(tmp_path: Path) -> None:
    """A record with no proposals fails the has-proposals rule."""
    f = _make_markdown(tmp_path, "ontology.md", _ONTOLOGY_MD)
    kb = KnowledgeBase()
    kb.build([f])
    clf = Classifier(knowledge_base=kb)
    record = clf.classify("xyzzy completely absent term here", _CLF_PROVENANCE, _registry())

    ruleset = EvaluationRuleset(
        name="classification-gate",
        version="1.0.0",
        rules=[_RULE_HAS_PROPOSALS, _RULE_CITATION_VERIFIES],
    )
    report = evaluate(record, ruleset, _registry(), BASE_PROVENANCE)

    assert report.verdict is EvaluationVerdict.FAIL
    assert "has-proposals" in report.failed_rules()
