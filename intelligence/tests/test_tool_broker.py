"""Tests for the Tool Registry & Authorization Broker.

Chapter 17 §13 and §16 of the ZoikoTax Master Specification.

Mirrors ``test_governance.py`` in structure and discipline — builders use
``dataclasses.replace`` so a typo in a field name is a type error here, not
a silent default in a broker test.
"""

from __future__ import annotations

from dataclasses import FrozenInstanceError, replace
from datetime import datetime, timedelta

import pytest

from ztax_gateway.tool_broker import (
    MAX_PERMITTED_ACTION_CLASS,
    ActionClass,
    AgentAudit,
    Refusal,
    ToolBrokerRefusedError,
    ToolCatalog,
    ToolProfile,
    ToolProvenance,
    authorise,
    guarded,
)

# ---------------------------------------------------------------------------
# Shared test fixtures
# ---------------------------------------------------------------------------

_TS = datetime(2026, 9, 22, 12, 0, 0)

BASE_TOOL = ToolProfile(
    tool_id="invoice-extractor",
    owner="lane-l",
    description="Extract line-items from an invoice.",
    action_class=ActionClass.MUTATE,
    required_scopes=frozenset({"write", "extract"}),
    idempotent=True,
    max_steps=200,
    max_duration=timedelta(seconds=30),
    max_token_cost=4096,
    max_monetary_cost=0.10,
)

BASE_PROVENANCE = ToolProvenance(
    tool_id="invoice-extractor",
    caller_id="agent:classification-review",
    region="eu-west-1",
    timestamp=_TS,
    scopes=frozenset({"write", "extract"}),
    idempotency_token="tok-abc-123",
    steps=5,
    duration_seconds=2,
    token_cost=100,
    monetary_cost=0.01,
)


def catalog(*tools: ToolProfile) -> ToolCatalog:
    return ToolCatalog(list(tools) if tools else [BASE_TOOL])


# ---------------------------------------------------------------------------
# Happy path
# ---------------------------------------------------------------------------


def test_a_registered_call_is_authorised() -> None:
    assert authorise(catalog(), BASE_PROVENANCE).tool_id == "invoice-extractor"


def test_all_non_privileged_action_classes_can_be_authorised() -> None:
    """READ, PREPARE, MUTATE and COMMIT are all permissible."""
    for ac in (ActionClass.READ, ActionClass.PREPARE, ActionClass.MUTATE, ActionClass.COMMIT):
        tool = replace(
            BASE_TOOL,
            tool_id=f"tool-{ac.value.lower()}",
            action_class=ac,
            # READ / PREPARE don't need idempotency tokens.
            idempotent=ac in (ActionClass.MUTATE, ActionClass.COMMIT),
        )
        prov = replace(BASE_PROVENANCE, tool_id=tool.tool_id)
        result = authorise(catalog(tool), prov)
        assert result.action_class == ac


def test_maximum_permitted_action_class_is_commit() -> None:
    assert MAX_PERMITTED_ACTION_CLASS is ActionClass.COMMIT


# ---------------------------------------------------------------------------
# PRIVILEGED — refused unconditionally (mirrors A5 in governance)
# ---------------------------------------------------------------------------


def test_privileged_action_class_cannot_be_registered() -> None:
    """No catalog state can permit PRIVILEGED — registration is the gate."""
    with pytest.raises(ValueError, match="PRIVILEGED"):
        ToolCatalog(
            [
                replace(
                    BASE_TOOL,
                    tool_id="danger-tool",
                    action_class=ActionClass.PRIVILEGED,
                )
            ]
        )


# ---------------------------------------------------------------------------
# Kill switch
# ---------------------------------------------------------------------------


def test_per_tool_kill_switch_stops_one_tool() -> None:
    cat = catalog()
    cat.kill("invoice-extractor")
    with pytest.raises(ToolBrokerRefusedError) as exc:
        authorise(cat, BASE_PROVENANCE)
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED


def test_global_kill_stops_everything() -> None:
    cat = catalog()
    cat.engage_global_kill()
    with pytest.raises(ToolBrokerRefusedError) as exc:
        authorise(cat, BASE_PROVENANCE)
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED
    cat.release_global_kill()
    # After release the call is permitted again.
    assert authorise(cat, BASE_PROVENANCE) is not None


def test_kill_switch_accepts_unregistered_identifier() -> None:
    """During an incident the identifier in hand may not be in the catalog."""
    cat = catalog()
    cat.kill("never-registered")
    with pytest.raises(ToolBrokerRefusedError) as exc:
        authorise(cat, replace(BASE_PROVENANCE, tool_id="never-registered"))
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED


def test_kill_switch_is_checked_before_registration() -> None:
    """Global kill must not produce UNKNOWN_TOOL — wrong signal during incident."""
    cat = catalog()
    cat.engage_global_kill()
    with pytest.raises(ToolBrokerRefusedError) as exc:
        authorise(cat, replace(BASE_PROVENANCE, tool_id="never-registered"))
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED


# ---------------------------------------------------------------------------
# Registration
# ---------------------------------------------------------------------------


def test_unknown_tool_is_refused() -> None:
    with pytest.raises(ToolBrokerRefusedError) as exc:
        authorise(catalog(), replace(BASE_PROVENANCE, tool_id="no-such-tool"))
    assert exc.value.refusal is Refusal.UNKNOWN_TOOL


def test_suspended_tool_is_refused_and_keeps_registration() -> None:
    cat = catalog(replace(BASE_TOOL, suspended=True))
    with pytest.raises(ToolBrokerRefusedError) as exc:
        authorise(cat, BASE_PROVENANCE)
    assert exc.value.refusal is Refusal.TOOL_SUSPENDED
    # The tool must still be in the catalog.
    assert cat.get("invoice-extractor") is not None


def test_duplicate_registration_is_a_defect() -> None:
    with pytest.raises(ValueError, match="already registered"):
        ToolCatalog([BASE_TOOL, BASE_TOOL])


def test_unowned_tool_cannot_be_registered() -> None:
    with pytest.raises(ValueError, match="owner"):
        ToolCatalog([replace(BASE_TOOL, owner="")])


def test_tool_without_id_cannot_be_registered() -> None:
    with pytest.raises(ValueError, match="identifier"):
        ToolCatalog([replace(BASE_TOOL, tool_id="")])


# ---------------------------------------------------------------------------
# Scope verification
# ---------------------------------------------------------------------------


def test_caller_missing_a_required_scope_is_refused() -> None:
    prov = replace(BASE_PROVENANCE, scopes=frozenset({"write"}))  # missing "extract"
    with pytest.raises(ToolBrokerRefusedError) as exc:
        authorise(catalog(), prov)
    assert exc.value.refusal is Refusal.SCOPE_VIOLATION
    assert "extract" in exc.value.detail


def test_caller_missing_all_scopes_is_refused() -> None:
    prov = replace(BASE_PROVENANCE, scopes=frozenset())
    with pytest.raises(ToolBrokerRefusedError) as exc:
        authorise(catalog(), prov)
    assert exc.value.refusal is Refusal.SCOPE_VIOLATION


def test_caller_with_superset_of_scopes_is_authorised() -> None:
    prov = replace(BASE_PROVENANCE, scopes=frozenset({"write", "extract", "admin"}))
    assert authorise(catalog(), prov).tool_id == "invoice-extractor"


def test_tool_with_no_required_scopes_permits_any_caller() -> None:
    open_tool = replace(
        BASE_TOOL,
        tool_id="open-reader",
        required_scopes=frozenset(),
        action_class=ActionClass.READ,
        idempotent=False,
    )
    prov = replace(BASE_PROVENANCE, tool_id="open-reader", scopes=frozenset())
    assert authorise(catalog(open_tool), prov).tool_id == "open-reader"


# ---------------------------------------------------------------------------
# Idempotency
# ---------------------------------------------------------------------------


def test_mutate_call_without_token_is_refused() -> None:
    prov = replace(BASE_PROVENANCE, idempotency_token=None)
    with pytest.raises(ToolBrokerRefusedError) as exc:
        authorise(catalog(), prov)
    assert exc.value.refusal is Refusal.IDEMPOTENCY_VIOLATION


def test_mutate_call_with_token_is_authorised() -> None:
    # BASE_PROVENANCE already has a token.
    assert authorise(catalog(), BASE_PROVENANCE) is not None


def test_read_call_does_not_need_idempotency_token() -> None:
    read_tool = replace(
        BASE_TOOL,
        tool_id="tax-summary",
        action_class=ActionClass.READ,
        required_scopes=frozenset({"read"}),
        idempotent=False,
    )
    prov = replace(
        BASE_PROVENANCE,
        tool_id="tax-summary",
        scopes=frozenset({"read"}),
        idempotency_token=None,
    )
    assert authorise(catalog(read_tool), prov).tool_id == "tax-summary"


def test_commit_call_without_token_is_refused_when_idempotent() -> None:
    commit_tool = replace(
        BASE_TOOL,
        tool_id="filing-commit",
        action_class=ActionClass.COMMIT,
        idempotent=True,
    )
    prov = replace(BASE_PROVENANCE, tool_id="filing-commit", idempotency_token=None)
    with pytest.raises(ToolBrokerRefusedError) as exc:
        authorise(catalog(commit_tool), prov)
    assert exc.value.refusal is Refusal.IDEMPOTENCY_VIOLATION


# ---------------------------------------------------------------------------
# Budget
# ---------------------------------------------------------------------------


def test_steps_over_ceiling_is_refused() -> None:
    prov = replace(BASE_PROVENANCE, steps=201)  # max_steps=200
    with pytest.raises(ToolBrokerRefusedError) as exc:
        authorise(catalog(), prov)
    assert exc.value.refusal is Refusal.BUDGET_EXCEEDED
    assert "steps" in exc.value.detail


def test_duration_over_ceiling_is_refused() -> None:
    prov = replace(BASE_PROVENANCE, duration_seconds=31)  # max_duration=30s
    with pytest.raises(ToolBrokerRefusedError) as exc:
        authorise(catalog(), prov)
    assert exc.value.refusal is Refusal.BUDGET_EXCEEDED
    assert "duration" in exc.value.detail


def test_token_cost_over_ceiling_is_refused() -> None:
    prov = replace(BASE_PROVENANCE, token_cost=5000)  # max_token_cost=4096
    with pytest.raises(ToolBrokerRefusedError) as exc:
        authorise(catalog(), prov)
    assert exc.value.refusal is Refusal.BUDGET_EXCEEDED
    assert "token" in exc.value.detail


def test_monetary_cost_over_ceiling_is_refused() -> None:
    prov = replace(BASE_PROVENANCE, monetary_cost=0.50)  # max_monetary_cost=0.10
    with pytest.raises(ToolBrokerRefusedError) as exc:
        authorise(catalog(), prov)
    assert exc.value.refusal is Refusal.BUDGET_EXCEEDED
    assert "monetary" in exc.value.detail


def test_call_exactly_at_ceiling_is_permitted() -> None:
    prov = replace(
        BASE_PROVENANCE,
        steps=200,
        duration_seconds=30,
        token_cost=4096,
        monetary_cost=0.10,
    )
    assert authorise(catalog(), prov) is not None


def test_tool_with_no_budget_limits_never_budget_refuses() -> None:
    unlimited = replace(
        BASE_TOOL,
        tool_id="unlimited-read",
        action_class=ActionClass.READ,
        required_scopes=frozenset({"read"}),
        idempotent=False,
        max_steps=None,
        max_duration=None,
        max_token_cost=None,
        max_monetary_cost=None,
    )
    prov = replace(
        BASE_PROVENANCE,
        tool_id="unlimited-read",
        scopes=frozenset({"read"}),
        steps=999_999,
        duration_seconds=999_999,
        token_cost=999_999,
        monetary_cost=999_999.0,
    )
    assert authorise(catalog(unlimited), prov).tool_id == "unlimited-read"


# ---------------------------------------------------------------------------
# Malformed context
# ---------------------------------------------------------------------------


def test_malformed_context_is_refused() -> None:
    for bad in (
        replace(BASE_PROVENANCE, tool_id=""),
        replace(BASE_PROVENANCE, caller_id=""),
        replace(BASE_PROVENANCE, region=""),
    ):
        with pytest.raises(ToolBrokerRefusedError) as exc:
            authorise(catalog(), bad)
        assert exc.value.refusal is Refusal.MALFORMED_CONTEXT


# ---------------------------------------------------------------------------
# Redacted context — a refusal must not echo the payload
# ---------------------------------------------------------------------------


def test_refusal_carries_redacted_context_and_no_payload() -> None:
    with pytest.raises(ToolBrokerRefusedError) as exc:
        authorise(catalog(), replace(BASE_PROVENANCE, scopes=frozenset()))
    context = exc.value.context
    assert context["tool_id"] == "invoice-extractor"
    assert context["caller_id"] == "agent:classification-review"
    assert context["region"] == "eu-west-1"
    # No payload, no secret fields.
    assert "idempotency_token" not in context
    assert "payload" not in context


# ---------------------------------------------------------------------------
# ToolProvenance is frozen
# ---------------------------------------------------------------------------


def test_provenance_is_frozen() -> None:
    """Nothing downstream may widen its own authority by mutating the context."""
    p = BASE_PROVENANCE
    with pytest.raises(FrozenInstanceError):
        p.scopes = frozenset({"admin"})  # type: ignore[misc]


# ---------------------------------------------------------------------------
# ToolProfile is frozen
# ---------------------------------------------------------------------------


def test_tool_profile_is_frozen() -> None:
    t = BASE_TOOL
    with pytest.raises(FrozenInstanceError):
        t.action_class = ActionClass.PRIVILEGED  # type: ignore[misc]


# ---------------------------------------------------------------------------
# guarded helper
# ---------------------------------------------------------------------------


def test_guarded_runs_work_when_permitted() -> None:
    result = guarded(catalog(), BASE_PROVENANCE, lambda tool: tool.tool_id)
    assert result == "invoice-extractor"


def test_guarded_raises_on_refusal() -> None:
    cat = catalog()
    cat.kill("invoice-extractor")
    with pytest.raises(ToolBrokerRefusedError) as exc:
        guarded(cat, BASE_PROVENANCE, lambda tool: tool.tool_id)
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED


# ---------------------------------------------------------------------------
# AgentAudit
# ---------------------------------------------------------------------------


def test_agent_audit_as_dict_includes_required_fields() -> None:
    audit = AgentAudit(
        call_id="call-001",
        tool_id="invoice-extractor",
        caller_id="agent:classification-review",
        timestamp=_TS,
        outcome="PERMITTED",
        payload_hash="sha256:abc",
        reviewer_id="user:engineer",
    )
    d = audit.as_dict()
    assert d["call_id"] == "call-001"
    assert d["outcome"] == "PERMITTED"
    assert d["payload_hash"] == "sha256:abc"
    assert d["reviewer_id"] == "user:engineer"


def test_agent_audit_omits_optional_none_fields() -> None:
    audit = AgentAudit(
        call_id="call-002",
        tool_id="invoice-extractor",
        caller_id="agent:classification-review",
        timestamp=_TS,
        outcome=Refusal.UNKNOWN_TOOL,
    )
    d = audit.as_dict()
    assert "payload_hash" not in d
    assert "reviewer_id" not in d
    assert "detail" not in d
