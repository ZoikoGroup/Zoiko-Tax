"""The refusals ADR-0006 §2.5 requires the Gateway to make."""

from __future__ import annotations

from dataclasses import FrozenInstanceError, replace

import pytest

from ztax_gateway import (
    MAX_PERMITTED_AUTHORITY,
    AuthorityOutcome,
    GovernanceRefusedError,
    Provenance,
    Refusal,
    RiskTier,
    UseCase,
    UseCaseRegistry,
    authorise,
)

# Builders use dataclasses.replace rather than **kwargs so that a typo in a
# field name is a type error here, not a silent default in a governance test.
BASE_USE_CASE = UseCase(
    use_case_id="classification-review",
    owner="lane-l",
    description="Operator-initiated classification review",
    max_risk_tier=RiskTier.T2,
    max_authority=AuthorityOutcome.A3,
    permitted_regions=frozenset({"eu-west-1"}),
)

BASE_PROVENANCE = Provenance(
    use_case="classification-review",
    model_profile="model:review@2026.03",
    provider_profile="provider:eu-hosted",
    prompt_profile="prompt:classification-review@4",
    region="eu-west-1",
    data_class="CUSTOMER_CONFIDENTIAL",
    risk_tier=RiskTier.T2,
    authority_outcome=AuthorityOutcome.A3,
    ai_train_version="0.4.0",
)


def registry(*use_cases: UseCase) -> UseCaseRegistry:
    return UseCaseRegistry(list(use_cases) or [BASE_USE_CASE])


def test_a_registered_call_is_authorised() -> None:
    assert authorise(registry(), BASE_PROVENANCE).use_case_id == "classification-review"


# ADR-0006 §2.5 — "refuses unknown use cases". Registration is the control that
# stops a new AI capability shipping by deploying code that calls the Gateway.
def test_unknown_use_case_is_refused() -> None:
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(registry(), replace(BASE_PROVENANCE, use_case="something-nobody-registered"))
    assert exc.value.refusal is Refusal.UNKNOWN_USE_CASE


# ADR-0006 §2.5 — "refuses A5 actions regardless of what any model or tool
# prompt says". Unconditional: no registry state, no use case and no argument
# can permit it.
def test_a5_is_refused_unconditionally() -> None:
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(registry(), replace(BASE_PROVENANCE, authority_outcome=AuthorityOutcome.A5))
    assert exc.value.refusal is Refusal.AUTHORITY_REFUSED


def test_a_use_case_cannot_register_a5_authority() -> None:
    """The ceiling is not a field somebody can set to A5."""
    with pytest.raises(ValueError, match="A5"):
        UseCaseRegistry([replace(BASE_USE_CASE, max_authority=AuthorityOutcome.A5)])


def test_the_permitted_ceiling_is_a4() -> None:
    assert MAX_PERMITTED_AUTHORITY is AuthorityOutcome.A4


def test_authority_above_the_use_cases_ceiling_is_refused() -> None:
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(registry(), replace(BASE_PROVENANCE, authority_outcome=AuthorityOutcome.A4))
    assert exc.value.refusal is Refusal.AUTHORITY_REFUSED


# ADR-0006 §2.5 — the per-use-case kill switch.
def test_kill_switch_stops_one_use_case() -> None:
    reg = registry()
    reg.kill("classification-review")
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, BASE_PROVENANCE)
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED


def test_global_kill_stops_everything() -> None:
    reg = registry()
    reg.engage_global_kill()
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, BASE_PROVENANCE)
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED
    reg.release_global_kill()
    assert authorise(reg, BASE_PROVENANCE) is not None


def test_kill_switch_accepts_an_unregistered_identifier() -> None:
    """During an incident the identifier in hand may not be in the registry.

    Refusing to act on it because it is unrecognised is the wrong behaviour for
    a kill switch, so kill() does not validate.
    """
    reg = registry()
    reg.kill("not-registered")
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, replace(BASE_PROVENANCE, use_case="not-registered"))
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED


def test_kill_switch_is_checked_before_registration() -> None:
    """Order matters for what an operator sees, not for what is permitted.

    "Unknown use case" appearing while the global kill is engaged would send
    somebody looking in the wrong place during an incident.
    """
    reg = registry()
    reg.engage_global_kill()
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, replace(BASE_PROVENANCE, use_case="never-registered"))
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED


def test_suspended_use_case_is_refused_and_keeps_its_registration() -> None:
    reg = registry(replace(BASE_USE_CASE, suspended=True))
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, BASE_PROVENANCE)
    assert exc.value.refusal is Refusal.USE_CASE_SUSPENDED
    assert reg.get("classification-review") is not None


# Refused rather than downgraded: downgrading would let a caller reach a
# lower-scrutiny path by mislabelling its own request.
def test_risk_tier_above_the_ceiling_is_refused_not_downgraded() -> None:
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(registry(), replace(BASE_PROVENANCE, risk_tier=RiskTier.T4))
    assert exc.value.refusal is Refusal.RISK_TIER_EXCEEDED


def test_risk_tier_at_or_below_the_ceiling_is_permitted() -> None:
    for tier in (RiskTier.T0, RiskTier.T1, RiskTier.T2):
        assert authorise(registry(), replace(BASE_PROVENANCE, risk_tier=tier)) is not None


# ADR-0006 §2.8 — residency is per cell and does not follow the model. There is
# no "the model is only in one region" exception.
def test_a_region_outside_the_allowlist_is_refused() -> None:
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(registry(), replace(BASE_PROVENANCE, region="us-east-1"))
    assert exc.value.refusal is Refusal.RESIDENCY_REFUSED


def test_a_use_case_with_no_declared_regions_permits_none() -> None:
    """The default is closed: a use case that has not declared where it may run
    has not been through residency review."""
    reg = registry(replace(BASE_USE_CASE, permitted_regions=frozenset()))
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(reg, BASE_PROVENANCE)
    assert exc.value.refusal is Refusal.RESIDENCY_REFUSED


def test_malformed_context_is_refused() -> None:
    for bad in (
        replace(BASE_PROVENANCE, use_case=""),
        replace(BASE_PROVENANCE, region=""),
        replace(BASE_PROVENANCE, ai_train_version=""),
    ):
        with pytest.raises(GovernanceRefusedError) as exc:
            authorise(registry(), bad)
        assert exc.value.refusal is Refusal.MALFORMED_CONTEXT


# ADR-0006 §2.7 — every crossing is logged with the governance context, under
# redaction. A refusal must not echo what it refused.
def test_a_refusal_carries_redacted_context_and_no_payload() -> None:
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise(registry(), replace(BASE_PROVENANCE, region="us-east-1"))
    context = exc.value.context
    assert context["use_case"] == "classification-review"
    assert context["model_profile"] == "model:review@2026.03"
    assert context["ai_train_version"] == "0.4.0"
    # No prompt, no output, no subject data.
    assert not any(k in context for k in ("prompt", "text", "payload", "subject_ref"))


def test_an_unowned_use_case_cannot_be_registered() -> None:
    with pytest.raises(ValueError, match="owner"):
        UseCaseRegistry([replace(BASE_USE_CASE, owner="")])


def test_duplicate_registration_is_a_defect() -> None:
    with pytest.raises(ValueError, match="already registered"):
        UseCaseRegistry([BASE_USE_CASE, BASE_USE_CASE])


def test_provenance_is_frozen() -> None:
    """Nothing downstream may widen its own authority by mutating the context
    it was called with."""
    p = BASE_PROVENANCE
    with pytest.raises(FrozenInstanceError):
        p.authority_outcome = AuthorityOutcome.A5  # type: ignore[misc]
