"""Tests for the Adversarial Harness and Release Gate.

Chapter 17 §19 and Chapter 10 §17 of the ZoikoTax Master Specification.

Mirrors the structure of ``test_governance.py`` and ``test_tool_broker.py``:
plain functions, ``dataclasses.replace`` for variants, every code path covered,
every refusal code explicitly asserted.

The tests are grouped by:
  - Built-in suite correctness (each of the five cases passes individually)
  - AdversarialRunner mechanics (full run, no short-circuit, error handling)
  - ReleaseGate pass and fail paths
  - AdversarialCase / CaseResult data structures and immutability
  - Custom case construction (caller-supplied triggers)
"""

from __future__ import annotations

from dataclasses import FrozenInstanceError

import pytest

from ztax_gateway.evaluation_adversarial import (
    _BASE_PROVENANCE,
    _BASE_TOOL_PROVENANCE,
    BUILT_IN_SUITE,
    AdversarialCase,
    AdversarialLayer,
    AdversarialRunner,
    BrokerRefusal,
    CaseResult,
    CaseVerdict,
    GovernanceRefusal,
    RefusalCode,
    ReleaseGate,
    ReleaseGateError,
    _catalog,
    _registry,
)
from ztax_gateway.governance import (
    GovernanceRefusedError,
)
from ztax_gateway.governance import (
    authorise as governance_authorise,
)
from ztax_gateway.provenance import AuthorityOutcome
from ztax_gateway.tool_broker import (
    authorise as broker_authorise,
)

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_RUNNER = AdversarialRunner()


def _case(
    case_id: str = "TEST-001",
    description: str = "test case",
    layer: AdversarialLayer = AdversarialLayer.GOVERNANCE,
    expected_refusal: RefusalCode = GovernanceRefusal.AUTHORITY_REFUSED,
    trigger: object = None,
) -> AdversarialCase:
    """Build a minimal AdversarialCase for ad-hoc tests."""
    if trigger is None:
        # Default: a trigger that always raises the expected governance refusal.
        def _default_trigger() -> None:
            prov = _BASE_PROVENANCE.__class__(
                use_case=_BASE_PROVENANCE.use_case,
                model_profile=_BASE_PROVENANCE.model_profile,
                provider_profile=_BASE_PROVENANCE.provider_profile,
                prompt_profile=_BASE_PROVENANCE.prompt_profile,
                region=_BASE_PROVENANCE.region,
                data_class=_BASE_PROVENANCE.data_class,
                risk_tier=_BASE_PROVENANCE.risk_tier,
                authority_outcome=AuthorityOutcome.A5,
                ai_train_version=_BASE_PROVENANCE.ai_train_version,
            )
            governance_authorise(_registry(), prov)
        trigger = _default_trigger
    return AdversarialCase(
        case_id=case_id,
        description=description,
        layer=layer,
        expected_refusal=expected_refusal,
        trigger=trigger,  # type: ignore[arg-type]
    )


# ---------------------------------------------------------------------------
# Built-in suite — each case must pass individually
# ---------------------------------------------------------------------------


def test_builtin_suite_has_five_cases() -> None:
    assert len(BUILT_IN_SUITE) == 5


def test_builtin_suite_case_ids_are_unique() -> None:
    ids = [c.case_id for c in BUILT_IN_SUITE]
    assert len(ids) == len(set(ids))


def test_adv001_excessive_agency_refused() -> None:
    """ADV-001: A5 claim must be refused with AUTHORITY_REFUSED."""
    case = next(c for c in BUILT_IN_SUITE if c.case_id == "ADV-001")
    result = _RUNNER.run([case])[0]
    assert result.passed, f"ADV-001 failed: {result.detail}"
    assert result.actual_refusal is GovernanceRefusal.AUTHORITY_REFUSED


def test_adv002_tool_misuse_scope_violation_refused() -> None:
    """ADV-002: missing scope must be refused with SCOPE_VIOLATION."""
    case = next(c for c in BUILT_IN_SUITE if c.case_id == "ADV-002")
    result = _RUNNER.run([case])[0]
    assert result.passed, f"ADV-002 failed: {result.detail}"
    assert result.actual_refusal is BrokerRefusal.SCOPE_VIOLATION


def test_adv003_agentic_delegation_refused() -> None:
    """ADV-003: missing delegation scope must be refused with SCOPE_VIOLATION."""
    case = next(c for c in BUILT_IN_SUITE if c.case_id == "ADV-003")
    result = _RUNNER.run([case])[0]
    assert result.passed, f"ADV-003 failed: {result.detail}"
    assert result.actual_refusal is BrokerRefusal.SCOPE_VIOLATION


def test_adv004_unbounded_consumption_refused() -> None:
    """ADV-004: step budget exceeded must be refused with BUDGET_EXCEEDED."""
    case = next(c for c in BUILT_IN_SUITE if c.case_id == "ADV-004")
    result = _RUNNER.run([case])[0]
    assert result.passed, f"ADV-004 failed: {result.detail}"
    assert result.actual_refusal is BrokerRefusal.BUDGET_EXCEEDED


def test_adv005_kill_switch_overrides_authority() -> None:
    """ADV-005: engaged kill switch must refuse with KILL_SWITCH_ENGAGED."""
    case = next(c for c in BUILT_IN_SUITE if c.case_id == "ADV-005")
    result = _RUNNER.run([case])[0]
    assert result.passed, f"ADV-005 failed: {result.detail}"
    assert result.actual_refusal is GovernanceRefusal.KILL_SWITCH_ENGAGED


def test_builtin_suite_all_pass_via_full_run() -> None:
    """The complete built-in suite must pass when run together."""
    results = _RUNNER.run(BUILT_IN_SUITE)
    failed = [r for r in results if not r.passed]
    assert failed == [], f"failed cases: {[r.case_id for r in failed]}"


# ---------------------------------------------------------------------------
# AdversarialRunner mechanics
# ---------------------------------------------------------------------------


def test_runner_runs_every_case_no_short_circuit() -> None:
    """All cases are run even after a failure — no bail-out."""
    counter: list[str] = []

    def make_trigger(cid: str) -> object:
        def _t() -> None:
            counter.append(cid)
            raise GovernanceRefusedError(
                GovernanceRefusal.AUTHORITY_REFUSED, "forced"
            )
        return _t

    cases = [
        AdversarialCase(
            case_id=f"T-{i}",
            description="probe",
            layer=AdversarialLayer.GOVERNANCE,
            expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
            trigger=make_trigger(f"T-{i}"),  # type: ignore[arg-type]
        )
        for i in range(3)
    ]
    results = _RUNNER.run(cases)
    assert len(results) == 3
    assert counter == ["T-0", "T-1", "T-2"]


def test_runner_returns_results_in_case_order() -> None:
    results = _RUNNER.run(list(BUILT_IN_SUITE))
    ids = [r.case_id for r in results]
    assert ids == [c.case_id for c in BUILT_IN_SUITE]


def test_runner_wrong_refusal_code_gives_wrong_refusal_verdict() -> None:
    """A trigger that raises the *wrong* refusal code must be WRONG_REFUSAL."""
    def _trigger() -> None:
        # Raise UNKNOWN_USE_CASE but the case expects AUTHORITY_REFUSED.
        raise GovernanceRefusedError(
            GovernanceRefusal.UNKNOWN_USE_CASE, "wrong code"
        )

    case = AdversarialCase(
        case_id="WRONG",
        description="wrong code",
        layer=AdversarialLayer.GOVERNANCE,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        trigger=_trigger,
    )
    result = _RUNNER.run([case])[0]
    assert result.verdict is CaseVerdict.WRONG_REFUSAL
    assert not result.passed
    assert result.actual_refusal is GovernanceRefusal.UNKNOWN_USE_CASE


def test_runner_unexpected_pass_verdict_when_trigger_returns() -> None:
    """A trigger that returns normally (no raise) gets UNEXPECTED_PASS."""
    case = AdversarialCase(
        case_id="PASS",
        description="no raise",
        layer=AdversarialLayer.GOVERNANCE,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        trigger=lambda: None,
    )
    result = _RUNNER.run([case])[0]
    assert result.verdict is CaseVerdict.UNEXPECTED_PASS
    assert not result.passed
    assert result.actual_refusal is None


def test_runner_unexpected_error_verdict_on_non_refusal_exception() -> None:
    """A trigger that raises a plain exception gets UNEXPECTED_ERROR."""
    def _trigger() -> None:
        raise RuntimeError("implementation bug")

    case = AdversarialCase(
        case_id="ERR",
        description="crashes",
        layer=AdversarialLayer.GOVERNANCE,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        trigger=_trigger,
    )
    result = _RUNNER.run([case])[0]
    assert result.verdict is CaseVerdict.UNEXPECTED_ERROR
    assert not result.passed
    assert "RuntimeError" in result.detail


def test_runner_empty_suite_returns_empty_list() -> None:
    results = _RUNNER.run([])
    assert results == []


def test_runner_broker_refusal_matched_against_broker_expected() -> None:
    """A BrokerRefusal trigger matched against a BrokerRefusal expected gives PASS."""
    def _trigger() -> None:
        from dataclasses import replace as _replace
        prov = _replace(_BASE_TOOL_PROVENANCE, steps=999)
        broker_authorise(_catalog(), prov)

    case = AdversarialCase(
        case_id="BROKER",
        description="budget exceeded",
        layer=AdversarialLayer.TOOL,
        expected_refusal=BrokerRefusal.BUDGET_EXCEEDED,
        trigger=_trigger,
    )
    result = _RUNNER.run([case])[0]
    assert result.passed
    assert result.actual_refusal is BrokerRefusal.BUDGET_EXCEEDED


def test_runner_governance_refusal_vs_broker_expected_gives_wrong_refusal() -> None:
    """A GovernanceRefusal raised when a BrokerRefusal was expected → WRONG_REFUSAL."""
    def _trigger() -> None:
        raise GovernanceRefusedError(GovernanceRefusal.AUTHORITY_REFUSED, "wrong layer")

    case = AdversarialCase(
        case_id="CROSS",
        description="wrong layer",
        layer=AdversarialLayer.TOOL,
        expected_refusal=BrokerRefusal.SCOPE_VIOLATION,
        trigger=_trigger,
    )
    result = _RUNNER.run([case])[0]
    assert result.verdict is CaseVerdict.WRONG_REFUSAL
    assert result.actual_refusal is GovernanceRefusal.AUTHORITY_REFUSED


# ---------------------------------------------------------------------------
# ReleaseGate — pass path
# ---------------------------------------------------------------------------


def test_release_gate_passes_with_builtin_suite() -> None:
    """The full built-in suite must pass the release gate."""
    gate = ReleaseGate(suite=BUILT_IN_SUITE)
    results = gate.check()
    assert all(r.passed for r in results)
    assert len(results) == 5


def test_release_gate_check_returns_results_on_success() -> None:
    def _trigger() -> None:
        raise GovernanceRefusedError(GovernanceRefusal.AUTHORITY_REFUSED, "ok")

    case = AdversarialCase(
        case_id="OK",
        description="passes",
        layer=AdversarialLayer.GOVERNANCE,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        trigger=_trigger,
    )
    gate = ReleaseGate(suite=[case])
    results = gate.check()
    assert len(results) == 1
    assert results[0].passed


# ---------------------------------------------------------------------------
# ReleaseGate — fail path
# ---------------------------------------------------------------------------


def test_release_gate_raises_on_unexpected_pass() -> None:
    """A trigger that returns normally fails the gate."""
    case = AdversarialCase(
        case_id="FAIL",
        description="no raise",
        layer=AdversarialLayer.GOVERNANCE,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        trigger=lambda: None,
    )
    gate = ReleaseGate(suite=[case])
    with pytest.raises(ReleaseGateError) as exc:
        gate.check()
    assert len(exc.value.failed) == 1
    assert exc.value.failed[0].case_id == "FAIL"


def test_release_gate_raises_on_wrong_refusal() -> None:
    def _trigger() -> None:
        raise GovernanceRefusedError(GovernanceRefusal.UNKNOWN_USE_CASE, "wrong")

    case = AdversarialCase(
        case_id="WRONG",
        description="wrong refusal",
        layer=AdversarialLayer.GOVERNANCE,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        trigger=_trigger,
    )
    gate = ReleaseGate(suite=[case])
    with pytest.raises(ReleaseGateError) as exc:
        gate.check()
    assert exc.value.failed[0].verdict is CaseVerdict.WRONG_REFUSAL


def test_release_gate_error_carries_all_failed_results() -> None:
    """ReleaseGateError.failed contains every failing case, not just the first."""
    def _bad_trigger() -> None:
        return None  # returns without raising

    cases = [
        AdversarialCase(
            case_id=f"FAIL-{i}",
            description="no raise",
            layer=AdversarialLayer.GOVERNANCE,
            expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
            trigger=_bad_trigger,
        )
        for i in range(3)
    ]
    gate = ReleaseGate(suite=cases)
    with pytest.raises(ReleaseGateError) as exc:
        gate.check()
    assert len(exc.value.failed) == 3


def test_release_gate_error_message_mentions_case_ids() -> None:
    case = AdversarialCase(
        case_id="SIGNAL-001",
        description="no raise",
        layer=AdversarialLayer.GOVERNANCE,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        trigger=lambda: None,
    )
    gate = ReleaseGate(suite=[case])
    with pytest.raises(ReleaseGateError) as exc:
        gate.check()
    assert "SIGNAL-001" in str(exc.value)


def test_release_gate_error_results_include_passing_cases_too() -> None:
    """ReleaseGateError.results contains ALL results, not only failures."""
    def _pass_trigger() -> None:
        raise GovernanceRefusedError(GovernanceRefusal.AUTHORITY_REFUSED, "ok")

    pass_case = AdversarialCase(
        case_id="PASS",
        description="passes",
        layer=AdversarialLayer.GOVERNANCE,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        trigger=_pass_trigger,
    )
    fail_case = AdversarialCase(
        case_id="FAIL",
        description="no raise",
        layer=AdversarialLayer.GOVERNANCE,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        trigger=lambda: None,
    )
    gate = ReleaseGate(suite=[pass_case, fail_case])
    with pytest.raises(ReleaseGateError) as exc:
        gate.check()
    assert len(exc.value.results) == 2
    assert len(exc.value.failed) == 1


# ---------------------------------------------------------------------------
# AdversarialCase — immutability
# ---------------------------------------------------------------------------


def test_adversarial_case_is_frozen() -> None:
    case = _case()
    with pytest.raises(FrozenInstanceError):
        case.case_id = "mutated"  # type: ignore[misc]


def test_adversarial_case_trigger_is_stored() -> None:
    def my_trigger() -> None:
        raise GovernanceRefusedError(GovernanceRefusal.AUTHORITY_REFUSED, "ok")

    case = AdversarialCase(
        case_id="T",
        description="d",
        layer=AdversarialLayer.GOVERNANCE,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        trigger=my_trigger,
    )
    assert case.trigger is my_trigger


# ---------------------------------------------------------------------------
# CaseResult — immutability and helpers
# ---------------------------------------------------------------------------


def test_case_result_is_frozen() -> None:
    r = CaseResult(
        case_id="R",
        verdict=CaseVerdict.EXPECTED_REFUSAL,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        actual_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        detail="ok",
    )
    with pytest.raises(FrozenInstanceError):
        r.verdict = CaseVerdict.UNEXPECTED_PASS  # type: ignore[misc]


def test_case_result_passed_true_for_expected_refusal() -> None:
    r = CaseResult(
        case_id="R",
        verdict=CaseVerdict.EXPECTED_REFUSAL,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        actual_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        detail="",
    )
    assert r.passed is True


def test_case_result_passed_false_for_wrong_refusal() -> None:
    r = CaseResult(
        case_id="R",
        verdict=CaseVerdict.WRONG_REFUSAL,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        actual_refusal=GovernanceRefusal.UNKNOWN_USE_CASE,
        detail="",
    )
    assert r.passed is False


def test_case_result_passed_false_for_unexpected_pass() -> None:
    r = CaseResult(
        case_id="R",
        verdict=CaseVerdict.UNEXPECTED_PASS,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        actual_refusal=None,
        detail="",
    )
    assert r.passed is False


def test_case_result_passed_false_for_unexpected_error() -> None:
    r = CaseResult(
        case_id="R",
        verdict=CaseVerdict.UNEXPECTED_ERROR,
        expected_refusal=GovernanceRefusal.AUTHORITY_REFUSED,
        actual_refusal=None,
        detail="boom",
    )
    assert r.passed is False


# ---------------------------------------------------------------------------
# CaseVerdict — all four values are distinct strings
# ---------------------------------------------------------------------------


def test_case_verdict_values_are_distinct() -> None:
    values = [v.value for v in CaseVerdict]
    assert len(values) == len(set(values))


# ---------------------------------------------------------------------------
# AdversarialLayer
# ---------------------------------------------------------------------------


def test_adversarial_layer_governance_and_tool_are_distinct() -> None:
    # All AdversarialLayer values must be unique strings.
    all_values = [layer.value for layer in AdversarialLayer]
    assert len(all_values) == len(set(all_values))


# ---------------------------------------------------------------------------
# Built-in suite — structural assertions (layer assignments)
# ---------------------------------------------------------------------------


def test_adv001_is_governance_layer() -> None:
    case = next(c for c in BUILT_IN_SUITE if c.case_id == "ADV-001")
    assert case.layer is AdversarialLayer.GOVERNANCE


def test_adv002_is_tool_layer() -> None:
    case = next(c for c in BUILT_IN_SUITE if c.case_id == "ADV-002")
    assert case.layer is AdversarialLayer.TOOL


def test_adv003_is_tool_layer() -> None:
    case = next(c for c in BUILT_IN_SUITE if c.case_id == "ADV-003")
    assert case.layer is AdversarialLayer.TOOL


def test_adv004_is_tool_layer() -> None:
    case = next(c for c in BUILT_IN_SUITE if c.case_id == "ADV-004")
    assert case.layer is AdversarialLayer.TOOL


def test_adv005_is_governance_layer() -> None:
    case = next(c for c in BUILT_IN_SUITE if c.case_id == "ADV-005")
    assert case.layer is AdversarialLayer.GOVERNANCE


# ---------------------------------------------------------------------------
# Kill-switch ordering: must be KILL_SWITCH_ENGAGED, not UNKNOWN_USE_CASE
# ---------------------------------------------------------------------------


def test_adv005_refusal_is_kill_switch_not_unknown() -> None:
    """The kill-switch check must fire before the registration check."""
    case = next(c for c in BUILT_IN_SUITE if c.case_id == "ADV-005")
    result = _RUNNER.run([case])[0]
    # The refusal must specifically be KILL_SWITCH_ENGAGED, not UNKNOWN_USE_CASE.
    assert result.actual_refusal is GovernanceRefusal.KILL_SWITCH_ENGAGED


# ---------------------------------------------------------------------------
# A5 authority refused before per-use-case ceiling (blast radius ordering)
# ---------------------------------------------------------------------------


def test_adv001_refusal_is_authority_not_unknown() -> None:
    """The A5 check must fire before the use-case ceiling check."""
    case = next(c for c in BUILT_IN_SUITE if c.case_id == "ADV-001")
    result = _RUNNER.run([case])[0]
    assert result.actual_refusal is GovernanceRefusal.AUTHORITY_REFUSED
