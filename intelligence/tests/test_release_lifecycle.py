"""Tests for release_lifecycle.py (Chapter 17 §29).

Coverage target
---------------
Every named transition (advance, degrade, suspend, reinstate, retire) is
covered, along with all error paths:

* Governance gate fires first on every mutating call.
* Terminal-state guard: RETIRED refuses all further transitions.
* Disallowed transition (e.g. RESEARCH -> PRODUCTION) is refused.
* Rationale-required states (DEGRADED, SUSPENDED, RETIRED) refuse empty
  rationale.
* Empty operator_id is refused.
* can_serve_production() reflects correct states.
* traffic_authority reflects correct TrafficAuthority for each state.
* LifecycleRecord.as_dict() is log-safe and contains all expected fields.
* Full pipeline walk: RESEARCH -> ... -> PRODUCTION -> DEGRADED ->
  SUSPENDED -> PRODUCTION -> RETIRED.
* Emergency retire from any pre-production state.
"""

from __future__ import annotations

from datetime import datetime

import pytest

from ztax_gateway.governance import GovernanceRefusedError, UseCase, UseCaseRegistry
from ztax_gateway.production_registries import AIReleaseManifest, build_manifest
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier
from ztax_gateway.release_lifecycle import (
    LifecycleError,
    LifecycleState,
    ManifestLifecycle,
    TrafficAuthority,
    can_serve_production,
)

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

_UC_ID = "uc-lifecycle-test"
_REGION = "eu-west-1"


@pytest.fixture()
def registry() -> UseCaseRegistry:
    reg = UseCaseRegistry()
    reg.register(
        UseCase(
            use_case_id=_UC_ID,
            owner="platform-team",
            description="Release lifecycle test use case",
            max_risk_tier=RiskTier.T2,
            max_authority=AuthorityOutcome.A3,
            permitted_regions=frozenset({_REGION}),
        )
    )
    return reg


@pytest.fixture()
def prov() -> Provenance:
    return Provenance(
        use_case=_UC_ID,
        model_profile="mp-001",
        provider_profile="pp-provider-001",
        prompt_profile="pp-v1",
        region=_REGION,
        data_class="INTERNAL",
        risk_tier=RiskTier.T0,
        authority_outcome=AuthorityOutcome.A1,
        ai_train_version="train-1",
    )


@pytest.fixture()
def manifest() -> AIReleaseManifest:
    return build_manifest(
        ai_use_case_id=_UC_ID,
        release_version="1.0.0",
        authority_level=AuthorityOutcome.A1,
        risk_tier=RiskTier.T0,
        model_profile_id="mp-001",
        resolved_model_id="gpt-4o-2025-01",
        prompt_profile_id="pp-v1",
        evaluation_profile_id="ep-001",
        policy_bundle_id="pb-001",
        allowed_regions=frozenset({_REGION}),
    )


@pytest.fixture()
def lifecycle(manifest: AIReleaseManifest) -> ManifestLifecycle:
    return ManifestLifecycle(manifest)


# ---------------------------------------------------------------------------
# Initial state
# ---------------------------------------------------------------------------


def test_initial_state(lifecycle: ManifestLifecycle) -> None:
    assert lifecycle.state is LifecycleState.RESEARCH
    assert lifecycle.is_retired is False
    assert lifecycle.traffic_authority is TrafficAuthority.NONE
    assert lifecycle.history == []


def test_manifest_property_preserved(
    lifecycle: ManifestLifecycle, manifest: AIReleaseManifest
) -> None:
    assert lifecycle.manifest is manifest


# ---------------------------------------------------------------------------
# can_serve_production helper
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("state", "expected"),
    [
        (LifecycleState.RESEARCH, False),
        (LifecycleState.DESIGN, False),
        (LifecycleState.VALIDATION, False),
        (LifecycleState.SHADOW, False),
        (LifecycleState.PILOT, True),
        (LifecycleState.PRODUCTION, True),
        (LifecycleState.DEGRADED, True),
        (LifecycleState.SUSPENDED, False),
        (LifecycleState.RETIRED, False),
    ],
)
def test_can_serve_production(state: LifecycleState, expected: bool) -> None:
    assert can_serve_production(state) is expected


# ---------------------------------------------------------------------------
# TrafficAuthority per state
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("state", "expected_authority"),
    [
        (LifecycleState.RESEARCH, TrafficAuthority.NONE),
        (LifecycleState.DESIGN, TrafficAuthority.NONE),
        (LifecycleState.VALIDATION, TrafficAuthority.NONE),
        (LifecycleState.SHADOW, TrafficAuthority.OBSERVE_ONLY),
        (LifecycleState.PILOT, TrafficAuthority.AUTHORITATIVE),
        (LifecycleState.PRODUCTION, TrafficAuthority.AUTHORITATIVE),
        (LifecycleState.DEGRADED, TrafficAuthority.AUTHORITATIVE_DEGRADED),
        (LifecycleState.SUSPENDED, TrafficAuthority.NONE),
        (LifecycleState.RETIRED, TrafficAuthority.NONE),
    ],
)
def test_traffic_authority_per_state(
    lifecycle: ManifestLifecycle,
    registry: UseCaseRegistry,
    prov: Provenance,
    state: LifecycleState,
    expected_authority: TrafficAuthority,
    manifest: AIReleaseManifest,
) -> None:
    """Reach each state via the correct transitions, then check traffic_authority."""
    # We check traffic_authority via can_serve_production for simplicity,
    # and verify the mapping matches expected_authority via a dedicated lifecycle walk.
    lc = ManifestLifecycle(manifest)

    _walk_to(lc, state, registry, prov)
    assert lc.traffic_authority is expected_authority


def _walk_to(
    lc: ManifestLifecycle,
    target: LifecycleState,
    registry: UseCaseRegistry,
    prov: Provenance,
) -> None:
    """Walk a fresh ManifestLifecycle to the target state."""
    pipeline = [
        LifecycleState.DESIGN,
        LifecycleState.VALIDATION,
        LifecycleState.SHADOW,
        LifecycleState.PILOT,
        LifecycleState.PRODUCTION,
    ]
    if target is LifecycleState.RESEARCH:
        return
    for _step in pipeline:
        lc.advance(registry, prov, "alice")
        if lc.state is target:
            return
    if target is LifecycleState.DEGRADED:
        lc.degrade(registry, prov, "alice", "regression detected")
    elif target is LifecycleState.SUSPENDED:
        lc.suspend(registry, prov, "alice", "incident response")
    elif target is LifecycleState.RETIRED:
        lc.retire(registry, prov, "alice", "end of life")


# ---------------------------------------------------------------------------
# Sequential advance() -- happy path
# ---------------------------------------------------------------------------


def test_advance_research_to_design(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    record = lifecycle.advance(registry, prov, "alice", "design approved")
    assert lifecycle.state is LifecycleState.DESIGN
    assert record.from_state is LifecycleState.RESEARCH
    assert record.to_state is LifecycleState.DESIGN
    assert record.operator_id == "alice"
    assert record.rationale == "design approved"
    assert len(lifecycle.history) == 1


def test_advance_full_pipeline(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    expected = [
        LifecycleState.DESIGN,
        LifecycleState.VALIDATION,
        LifecycleState.SHADOW,
        LifecycleState.PILOT,
        LifecycleState.PRODUCTION,
    ]
    for target in expected:
        lifecycle.advance(registry, prov, "alice")
        assert lifecycle.state is target
    assert len(lifecycle.history) == 5
    assert lifecycle.traffic_authority is TrafficAuthority.AUTHORITATIVE


# ---------------------------------------------------------------------------
# Advance from PRODUCTION onwards -- should fail (must use named helpers)
# ---------------------------------------------------------------------------


def test_advance_from_production_raises(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    _walk_to(lifecycle, LifecycleState.PRODUCTION, registry, prov)
    with pytest.raises(LifecycleError, match="advance\\(\\) is not valid from state PRODUCTION"):
        lifecycle.advance(registry, prov, "alice")


def test_advance_from_retired_raises(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    lifecycle.retire(registry, prov, "alice", "end of life")
    with pytest.raises(LifecycleError, match="RETIRED"):
        lifecycle.advance(registry, prov, "alice")


# ---------------------------------------------------------------------------
# degrade()
# ---------------------------------------------------------------------------


def test_degrade_from_production(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    _walk_to(lifecycle, LifecycleState.PRODUCTION, registry, prov)
    record = lifecycle.degrade(registry, prov, "ops-bot", "F1 below threshold")
    assert lifecycle.state is LifecycleState.DEGRADED
    assert record.from_state is LifecycleState.PRODUCTION
    assert record.to_state is LifecycleState.DEGRADED
    assert lifecycle.traffic_authority is TrafficAuthority.AUTHORITATIVE_DEGRADED


def test_degrade_from_non_production_raises(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    # Still in RESEARCH
    with pytest.raises(LifecycleError, match="degrade\\(\\) requires state PRODUCTION"):
        lifecycle.degrade(registry, prov, "alice", "some reason")


def test_degrade_requires_rationale(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    _walk_to(lifecycle, LifecycleState.PRODUCTION, registry, prov)
    with pytest.raises(LifecycleError, match="rationale is required"):
        lifecycle.degrade(registry, prov, "alice", "")


# ---------------------------------------------------------------------------
# suspend()
# ---------------------------------------------------------------------------


def test_suspend_from_production(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    _walk_to(lifecycle, LifecycleState.PRODUCTION, registry, prov)
    lifecycle.suspend(registry, prov, "incident-team", "P0 incident")
    assert lifecycle.state is LifecycleState.SUSPENDED
    assert lifecycle.traffic_authority is TrafficAuthority.NONE
    assert not can_serve_production(lifecycle.state)


def test_suspend_from_degraded(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    _walk_to(lifecycle, LifecycleState.PRODUCTION, registry, prov)
    lifecycle.degrade(registry, prov, "ops", "regression")
    lifecycle.suspend(registry, prov, "incident-team", "unresolvable regression")
    assert lifecycle.state is LifecycleState.SUSPENDED


def test_suspend_from_research_raises(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    with pytest.raises(LifecycleError, match="suspend\\(\\) requires state PRODUCTION or DEGRADED"):
        lifecycle.suspend(registry, prov, "alice", "reason")


def test_suspend_requires_rationale(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    _walk_to(lifecycle, LifecycleState.PRODUCTION, registry, prov)
    with pytest.raises(LifecycleError, match="rationale is required"):
        lifecycle.suspend(registry, prov, "alice", "")


# ---------------------------------------------------------------------------
# reinstate()
# ---------------------------------------------------------------------------


def test_reinstate_from_degraded(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    _walk_to(lifecycle, LifecycleState.PRODUCTION, registry, prov)
    lifecycle.degrade(registry, prov, "ops", "regression")
    lifecycle.reinstate(registry, prov, "ops", "regression fixed")
    assert lifecycle.state is LifecycleState.PRODUCTION
    assert lifecycle.traffic_authority is TrafficAuthority.AUTHORITATIVE


def test_reinstate_from_suspended(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    _walk_to(lifecycle, LifecycleState.PRODUCTION, registry, prov)
    lifecycle.suspend(registry, prov, "ops", "P0 incident")
    lifecycle.reinstate(registry, prov, "release-board", "formal review passed")
    assert lifecycle.state is LifecycleState.PRODUCTION


def test_reinstate_from_production_raises(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    _walk_to(lifecycle, LifecycleState.PRODUCTION, registry, prov)
    with pytest.raises(
        LifecycleError, match="reinstate\\(\\) requires state DEGRADED or SUSPENDED"
    ):
        lifecycle.reinstate(registry, prov, "alice")


# ---------------------------------------------------------------------------
# retire()
# ---------------------------------------------------------------------------


def test_retire_from_research(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    record = lifecycle.retire(registry, prov, "platform", "abandoned project")
    assert lifecycle.state is LifecycleState.RETIRED
    assert lifecycle.is_retired is True
    assert lifecycle.traffic_authority is TrafficAuthority.NONE
    assert record.to_state is LifecycleState.RETIRED


def test_retire_from_production(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    _walk_to(lifecycle, LifecycleState.PRODUCTION, registry, prov)
    lifecycle.retire(registry, prov, "platform", "end of life")
    assert lifecycle.is_retired is True


def test_retire_requires_rationale(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    with pytest.raises(LifecycleError, match="rationale is required"):
        lifecycle.retire(registry, prov, "alice", "")


def test_retire_is_terminal(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    lifecycle.retire(registry, prov, "platform", "end of life")
    with pytest.raises(LifecycleError, match="RETIRED and may not be transitioned further"):
        lifecycle.retire(registry, prov, "platform", "trying again")


# ---------------------------------------------------------------------------
# Disallowed transitions
# ---------------------------------------------------------------------------


def test_disallowed_transition_research_to_production(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    """RESEARCH -> PRODUCTION is not in the allowed transition map."""
    with pytest.raises(LifecycleError, match="cannot transition RESEARCH -> PRODUCTION"):
        lifecycle._transition(
            LifecycleState.PRODUCTION, registry, prov, "attacker", "skip the pipeline"
        )


@pytest.mark.parametrize(
    ("start", "bad_target"),
    [
        (LifecycleState.RESEARCH, LifecycleState.PRODUCTION),
        (LifecycleState.DESIGN, LifecycleState.PILOT),
        (LifecycleState.VALIDATION, LifecycleState.PRODUCTION),
        (LifecycleState.SHADOW, LifecycleState.PRODUCTION),
        (LifecycleState.PILOT, LifecycleState.DEGRADED),
    ],
)
def test_disallowed_direct_jump(
    start: LifecycleState,
    bad_target: LifecycleState,
    manifest: AIReleaseManifest,
    registry: UseCaseRegistry,
    prov: Provenance,
) -> None:
    """Verify that jumping over states in the pipeline is refused."""
    lc = ManifestLifecycle(manifest)
    _walk_to(lc, start, registry, prov)
    with pytest.raises(LifecycleError, match="cannot transition"):
        # Use _transition directly for the bad jump test.
        lc._transition(bad_target, registry, prov, "attacker", "attempt skip")


# ---------------------------------------------------------------------------
# Governance gate fires first
# ---------------------------------------------------------------------------


def test_governance_gate_fires_on_advance(
    lifecycle: ManifestLifecycle, prov: Provenance
) -> None:
    """advance() must refuse if no use case is registered (gate fires first)."""
    empty_reg = UseCaseRegistry()
    with pytest.raises(GovernanceRefusedError):
        lifecycle.advance(empty_reg, prov, "alice")
    # State must not have changed.
    assert lifecycle.state is LifecycleState.RESEARCH
    assert lifecycle.history == []


def test_governance_gate_fires_on_retire(
    lifecycle: ManifestLifecycle, prov: Provenance
) -> None:
    empty_reg = UseCaseRegistry()
    with pytest.raises(GovernanceRefusedError):
        lifecycle.retire(empty_reg, prov, "alice", "reason")
    assert lifecycle.state is LifecycleState.RESEARCH


def test_kill_switch_blocks_transition(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    registry.kill(_UC_ID)
    with pytest.raises(GovernanceRefusedError, match="AI_KILL_SWITCH_ENGAGED"):
        lifecycle.advance(registry, prov, "alice")
    assert lifecycle.state is LifecycleState.RESEARCH


# ---------------------------------------------------------------------------
# Empty operator_id is refused
# ---------------------------------------------------------------------------


def test_empty_operator_id_refused(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    with pytest.raises(LifecycleError, match="operator_id must not be empty"):
        lifecycle.advance(registry, prov, "   ")


def test_whitespace_only_operator_id_refused(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    with pytest.raises(LifecycleError, match="operator_id must not be empty"):
        lifecycle.advance(registry, prov, "\t\n")


# ---------------------------------------------------------------------------
# LifecycleRecord integrity
# ---------------------------------------------------------------------------


def test_lifecycle_record_as_dict(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    record = lifecycle.advance(registry, prov, "alice", "design ready")
    d = record.as_dict()
    assert d["from_state"] == "RESEARCH"
    assert d["to_state"] == "DESIGN"
    assert d["operator_id"] == "alice"
    assert d["rationale"] == "design ready"
    assert isinstance(d["record_id"], str) and len(d["record_id"]) == 16
    assert isinstance(d["transitioned_at"], str)
    # transitioned_at must be an ISO-formatted UTC datetime
    ts = datetime.fromisoformat(str(d["transitioned_at"]))
    assert ts.utcoffset() is not None


def test_lifecycle_record_is_frozen(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    record = lifecycle.advance(registry, prov, "alice")
    with pytest.raises((AttributeError, TypeError)):
        record.operator_id = "bob"  # type: ignore[misc]


def test_history_is_a_copy(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    """history property must return a copy, not the internal list."""
    lifecycle.advance(registry, prov, "alice")
    h = lifecycle.history
    h.clear()
    assert len(lifecycle.history) == 1


# ---------------------------------------------------------------------------
# Full operational lifecycle walk
# ---------------------------------------------------------------------------


def test_full_lifecycle_walk(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    """RESEARCH -> ... -> PRODUCTION -> DEGRADED -> SUSPENDED -> PRODUCTION -> RETIRED."""
    for _ in range(5):
        lifecycle.advance(registry, prov, "alice")

    state_after_advance = lifecycle.state
    assert state_after_advance is LifecycleState.PRODUCTION

    lifecycle.degrade(registry, prov, "ops", "F1 dropped below 0.75")
    state_degraded = lifecycle.state
    authority_degraded = lifecycle.traffic_authority
    assert state_degraded is LifecycleState.DEGRADED
    assert authority_degraded is TrafficAuthority.AUTHORITATIVE_DEGRADED

    lifecycle.suspend(registry, prov, "incident-lead", "cannot resolve in time")
    state_suspended = lifecycle.state
    authority_suspended = lifecycle.traffic_authority
    assert state_suspended is LifecycleState.SUSPENDED
    assert authority_suspended is TrafficAuthority.NONE
    assert not can_serve_production(state_suspended)

    lifecycle.reinstate(registry, prov, "release-board", "fix deployed and validated")
    state_reinstated = lifecycle.state
    assert state_reinstated is LifecycleState.PRODUCTION

    lifecycle.retire(registry, prov, "platform", "v2.0 replaces this manifest")
    assert lifecycle.is_retired is True
    assert len(lifecycle.history) == 9  # 5 advance + degrade + suspend + reinstate + retire


def test_degraded_then_direct_retire(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    """DEGRADED -> RETIRED without going through SUSPENDED."""
    _walk_to(lifecycle, LifecycleState.PRODUCTION, registry, prov)
    lifecycle.degrade(registry, prov, "ops", "severe regression")
    lifecycle.retire(registry, prov, "platform", "unrecoverable")
    assert lifecycle.is_retired is True


def test_suspended_then_direct_retire(
    lifecycle: ManifestLifecycle, registry: UseCaseRegistry, prov: Provenance
) -> None:
    """SUSPENDED -> RETIRED (board decides not to reinstate)."""
    _walk_to(lifecycle, LifecycleState.PRODUCTION, registry, prov)
    lifecycle.suspend(registry, prov, "ops", "P0 incident")
    lifecycle.retire(registry, prov, "platform", "board voted to retire")
    assert lifecycle.is_retired is True


# ---------------------------------------------------------------------------
# ManifestRegistry.get() lifecycle block (sprinkler system)
# ---------------------------------------------------------------------------


from ztax_gateway.production_registries import ManifestRegistryError  # noqa: E402
from ztax_gateway.release_lifecycle import LifecycleAwareManifestRegistry  # noqa: E402


def test_get_blocked_in_research(
    manifest: AIReleaseManifest, registry: UseCaseRegistry, prov: Provenance
) -> None:
    """ManifestRegistry.get() raises ManifestRegistryError when lifecycle is RESEARCH."""
    store = LifecycleAwareManifestRegistry()
    store.add(manifest)
    inner = store.manifest_registry
    with pytest.raises(ManifestRegistryError, match="not production-ready"):
        inner.get(manifest.manifest_id, registry, prov)


@pytest.mark.parametrize("steps,expected_state", [
    (1, LifecycleState.DESIGN),
    (2, LifecycleState.VALIDATION),
    (3, LifecycleState.SHADOW),
])
def test_get_blocked_in_pre_production_states(
    manifest: AIReleaseManifest, registry: UseCaseRegistry, prov: Provenance,
    steps: int, expected_state: LifecycleState,
) -> None:
    """DESIGN, VALIDATION, SHADOW all block ManifestRegistry.get()."""
    store = LifecycleAwareManifestRegistry()
    lc = store.add(manifest)
    for _ in range(steps):
        lc.advance(registry, prov, "alice")
    assert lc.state is expected_state
    inner = store.manifest_registry
    with pytest.raises(ManifestRegistryError, match="not production-ready"):
        inner.get(manifest.manifest_id, registry, prov)


def test_get_allowed_in_pilot(
    manifest: AIReleaseManifest, registry: UseCaseRegistry, prov: Provenance
) -> None:
    """PILOT is production-serving — ManifestRegistry.get() must succeed."""
    store = LifecycleAwareManifestRegistry()
    lc = store.add(manifest)
    for _ in range(4):              # RESEARCH → DESIGN → VALIDATION → SHADOW → PILOT
        lc.advance(registry, prov, "alice")
    assert lc.state is LifecycleState.PILOT
    m = store.manifest_registry.get(manifest.manifest_id, registry, prov)
    assert m.manifest_id == manifest.manifest_id


def test_get_allowed_in_production(
    manifest: AIReleaseManifest, registry: UseCaseRegistry, prov: Provenance
) -> None:
    """PRODUCTION — ManifestRegistry.get() must succeed."""
    store = LifecycleAwareManifestRegistry()
    lc = store.add(manifest)
    _walk_to(lc, LifecycleState.PRODUCTION, registry, prov)
    m = store.manifest_registry.get(manifest.manifest_id, registry, prov)
    assert m.manifest_id == manifest.manifest_id


def test_get_blocked_after_revoke(
    manifest: AIReleaseManifest, registry: UseCaseRegistry, prov: Provenance
) -> None:
    """Revoking a PRODUCTION manifest blocks get() via the status check, not lifecycle."""
    store = LifecycleAwareManifestRegistry()
    lc = store.add(manifest)
    _walk_to(lc, LifecycleState.PRODUCTION, registry, prov)
    store.revoke(manifest.manifest_id)
    with pytest.raises(ManifestRegistryError, match="revoked"):
        store.manifest_registry.get(manifest.manifest_id, registry, prov)


def test_plain_manifest_registry_unaffected() -> None:
    """ManifestRegistry used directly (no lifecycle checker) still works as before."""
    from ztax_gateway.production_registries import ManifestRegistry
    reg = ManifestRegistry()
    # No lifecycle checker installed — get() passes through to status/existence checks only.
    assert reg._lifecycle_checker is None


