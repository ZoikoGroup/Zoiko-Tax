"""The red-team cases (ADV-006 onward) of the adversarial harness.

Every case attacks the real Gateway (or the tool broker and output checks) with fake
runtimes. A case passes only if the defence did the work: the model was not reached,
exactly one record exists where one is due, and the sentinel is in no error, record or
log line. The five built-in cases (ADV-001 to ADV-005) are untouched and keep their own
tests in test_evaluation_adversarial.py.
"""

from __future__ import annotations

import json
import logging
from datetime import datetime

import pytest

from ztax_gateway.ai_security_controls import AISecurityRefusal
from ztax_gateway.evaluation_adversarial import (
    BUILT_IN_SUITE,
    FULL_ADVERSARIAL_SUITE,
    RED_TEAM_SUITE,
    AdversarialCase,
    AdversarialLayer,
    AdversarialRunner,
    BrokerRefusal,
    CaseResult,
    CaseVerdict,
    DefenceOutcome,
    GovernanceRefusal,
    ReleaseGate,
    _ProbeRuntime,
    _rig,
    _rt_call,
)
from ztax_gateway.runtime import Result
from ztax_gateway.tool_broker import (
    ActionClass,
    ToolBrokerRefusedError,
    ToolCatalog,
    ToolProfile,
    ToolProvenance,
)
from ztax_gateway.tool_broker import authorise as broker_authorise

_RUNNER = AdversarialRunner()

# case id -> (layer, the code the case declares)
_EXPECTED: dict[str, tuple[AdversarialLayer, object]] = {
    "ADV-006": (AdversarialLayer.GATEWAY, GovernanceRefusal.AUTHORITY_REFUSED),
    "ADV-007": (AdversarialLayer.GATEWAY, GovernanceRefusal.KILL_SWITCH_ENGAGED),
    "ADV-008": (AdversarialLayer.TOOL, BrokerRefusal.UNKNOWN_TOOL),
    "ADV-009": (AdversarialLayer.TOOL, BrokerRefusal.PRIVILEGED_FORBIDDEN),
    "ADV-010": (AdversarialLayer.GATEWAY, DefenceOutcome.ISOLATION_HELD),
    "ADV-011": (AdversarialLayer.OUTPUT, DefenceOutcome.OUTPUT_WITHHELD),
    "ADV-012": (AdversarialLayer.OUTPUT, AISecurityRefusal.OUTPUT_SCHEMA_VIOLATION),
    "ADV-013": (AdversarialLayer.GATEWAY, GovernanceRefusal.DATA_CLASS_REFUSED),
    "ADV-014": (AdversarialLayer.GATEWAY, GovernanceRefusal.UNKNOWN_USE_CASE),
    "ADV-015": (AdversarialLayer.GATEWAY, GovernanceRefusal.RESIDENCY_REFUSED),
    "ADV-016": (AdversarialLayer.GATEWAY, DefenceOutcome.GATEWAY_UNAVAILABLE),
    "ADV-017": (AdversarialLayer.GATEWAY, DefenceOutcome.GATEWAY_UNAVAILABLE),
    "ADV-018": (AdversarialLayer.GATEWAY, DefenceOutcome.GATEWAY_UNAVAILABLE),
    "ADV-019": (AdversarialLayer.GATEWAY, GovernanceRefusal.MALFORMED_CONTEXT),
    "ADV-020": (AdversarialLayer.GATEWAY, GovernanceRefusal.MALFORMED_CONTEXT),
    "ADV-021": (AdversarialLayer.GATEWAY, GovernanceRefusal.RISK_TIER_EXCEEDED),
}


def _case(case_id: str) -> AdversarialCase:
    return next(c for c in RED_TEAM_SUITE if c.case_id == case_id)


def _run(case_id: str) -> CaseResult:
    return _RUNNER.run([_case(case_id)])[0]


# ---------------------------------------------------------------------------
# The suite as a whole
# ---------------------------------------------------------------------------


def test_the_built_in_suite_keeps_its_five_cases() -> None:
    assert [c.case_id for c in BUILT_IN_SUITE] == [f"ADV-00{i}" for i in range(1, 6)]


def test_the_red_team_suite_is_adv_006_onward_with_unique_ids() -> None:
    ids = [c.case_id for c in RED_TEAM_SUITE]
    assert ids == [f"ADV-{n:03d}" for n in range(6, 22)]
    assert len(set(ids)) == len(ids)
    assert set(ids) == set(_EXPECTED)


def test_the_full_suite_is_both_suites_in_order() -> None:
    assert (*BUILT_IN_SUITE, *RED_TEAM_SUITE) == FULL_ADVERSARIAL_SUITE
    assert len({c.case_id for c in FULL_ADVERSARIAL_SUITE}) == 21


def test_every_red_team_case_names_a_gate_or_adr_and_a_requirement() -> None:
    for case in RED_TEAM_SUITE:
        assert "Links:" in case.description, case.case_id
        assert "G-AI-" in case.description or "ADR-0006" in case.description, case.case_id
        assert "ZTAX-AIGOV-REQ-" in case.description, case.case_id


def test_the_full_suite_passes_the_release_gate() -> None:
    results = ReleaseGate(suite=FULL_ADVERSARIAL_SUITE).check()
    assert len(results) == 21
    assert all(r.passed for r in results)


@pytest.mark.parametrize("case_id", sorted(_EXPECTED))
def test_each_case_declares_its_layer_and_code(case_id: str) -> None:
    layer, code = _EXPECTED[case_id]
    case = _case(case_id)
    assert case.layer is layer
    assert case.expected_refusal is code


# ---------------------------------------------------------------------------
# One test per case: the defence held, for the declared reason
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("case_id", sorted(_EXPECTED))
def test_each_case_passes_with_its_declared_code(case_id: str) -> None:
    result = _run(case_id)
    assert result.verdict is CaseVerdict.EXPECTED_REFUSAL, f"{case_id}: {result.detail}"
    assert result.actual_refusal is _case(case_id).expected_refusal


def test_adv006_injection_cannot_change_the_authority_the_gateway_uses() -> None:
    assert _run("ADV-006").passed


def test_adv007_injection_cannot_switch_off_the_kill_switch() -> None:
    assert _run("ADV-007").passed


def test_adv008_a_forged_tool_name_is_refused_and_nothing_runs() -> None:
    assert _run("ADV-008").passed


def test_adv009_a_privileged_tool_cannot_be_registered_or_called() -> None:
    assert _run("ADV-009").passed


def test_adv010_nothing_of_one_tenant_reaches_another() -> None:
    assert _run("ADV-010").passed


def test_adv011_a_leaked_prompt_is_withheld_by_the_ontology_guard() -> None:
    assert _run("ADV-011").passed


def test_adv012_a_smuggled_key_is_rejected_by_the_output_schema() -> None:
    assert _run("ADV-012").passed


def test_adv013_p7_never_reaches_a_model() -> None:
    assert _run("ADV-013").passed


def test_adv014_a_caller_sent_use_case_id_is_never_repeated() -> None:
    assert _run("ADV-014").passed


def test_adv015_a_caller_sent_region_is_never_repeated() -> None:
    assert _run("ADV-015").passed


def test_adv016_a_runtime_error_with_a_secret_leaves_one_clean_record() -> None:
    assert _run("ADV-016").passed


def test_adv017_a_hung_runtime_is_refused_and_its_late_answer_unused() -> None:
    assert _run("ADV-017").passed


def test_adv018_busy_slots_refuse_at_once() -> None:
    assert _run("ADV-018").passed


def test_adv019_malformed_calls_get_fixed_text_and_no_record() -> None:
    assert _run("ADV-019").passed


def test_adv020_oversized_fields_get_fixed_text_and_no_record() -> None:
    assert _run("ADV-020").passed


def test_adv021_injection_cannot_lower_the_risk_tier() -> None:
    assert _run("ADV-021").passed


# ---------------------------------------------------------------------------
# Order independence and repeatability
# ---------------------------------------------------------------------------


def test_the_red_team_cases_pass_in_reverse_order() -> None:
    results = _RUNNER.run(list(reversed(RED_TEAM_SUITE)))
    assert all(r.passed for r in results), [r.case_id for r in results if not r.passed]


def test_the_red_team_cases_leave_the_root_logger_as_they_found_it() -> None:
    root = logging.getLogger()
    before = (root.level, list(root.handlers))
    _RUNNER.run(RED_TEAM_SUITE)
    assert (root.level, list(root.handlers)) == before


# ---------------------------------------------------------------------------
# What the cases showed about today's code
# ---------------------------------------------------------------------------


def test_the_gateway_itself_does_not_redact_model_output() -> None:
    """Characterisation. The Gateway returns what the runtime said; it keeps that text out of
    its records and logs (ADV-010, ADV-011) but does not filter the reply. Containment of a
    leaked reply relies on the output checks (ontology guard, explanation guard, output
    schema) that the caller applies. If a Gateway-side filter is added, update ADV-011."""
    marker = "SENT-RT-ALPHA-001"
    rig = _rig(runtime=_ProbeRuntime(result=Result(text=marker, confidence="0.9")))
    reply = json.loads(rig.gateway.invoke(_rt_call()))
    assert marker in reply["text"]
    assert marker not in rig.records_text()


def test_a_broker_refusal_does_not_repeat_the_forged_tool_name() -> None:
    sentinel = "RT-FORGED-NAME-IN-DETAIL-f3"
    catalog = ToolCatalog(
        [
            ToolProfile(
                tool_id="filing-read",
                owner="lane-security",
                description="A registered tool.",
                action_class=ActionClass.READ,
                required_scopes=frozenset({"read"}),
                idempotent=True,
            )
        ]
    )
    provenance = ToolProvenance(
        tool_id=f"forged-{sentinel}",
        caller_id="agent:redteam",
        region="eu-west-1",
        timestamp=datetime(2026, 9, 24, 12, 0, 0),
        scopes=frozenset({"read"}),
        idempotency_token="tok-rt-x1",
        steps=1,
        duration_seconds=1,
        token_cost=1,
        monetary_cost=0.0,
    )
    with pytest.raises(ToolBrokerRefusedError) as info:
        broker_authorise(catalog, provenance)
    err = info.value
    assert err.refusal is BrokerRefusal.UNKNOWN_TOOL
    assert sentinel not in f"{err.detail} {err} {err.context!r}"
