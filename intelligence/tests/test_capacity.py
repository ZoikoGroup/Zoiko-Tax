"""Tests for AI Capacity, Quotas and FinOps.

Chapter 17 §23 of the ZoikoTax Master Specification.

Test groups
-----------
  CapacityError          -- construction, reason, use_case, violations
  QuotaWindow            -- three values, StrEnum
  QuotaViolation         -- construction, immutability, as_dict
  QuotaPolicy            -- construction, validation, is_unconstrained
  QuotaState             -- construction, as_dict
  FinOpsReport           -- construction, as_dict
  CapacityLedger.register  -- happy path, replace, type check
  CapacityLedger.consume   -- counter accumulation, BLOCK exemption,
                             token limit, invocation limit, cost limit,
                             multi-violation, no-policy permissive,
                             atomicity (counters not updated on violation)
  CapacityLedger.reset     -- per-use-case, global, policy preserved
  CapacityLedger.report    -- snapshot independence, multi-use-case totals
  Design rule 5            -- BLOCK never consumes quota
  Integration              -- multi-use-case ledger lifecycle
"""

from __future__ import annotations

import pytest

from ztax_gateway.capacity import (
    CapacityError,
    CapacityLedger,
    FinOpsReport,
    QuotaPolicy,
    QuotaState,
    QuotaViolation,
    QuotaWindow,
)
from ztax_gateway.governance import UseCase, UseCaseRegistry
from ztax_gateway.invocation_evidence import (
    InvocationEvidenceBuilder,
    InvocationEvidenceRecord,
    InvocationOutcome,
    UsageMetrics,
)
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier

# ---------------------------------------------------------------------------
# Shared fixtures
# ---------------------------------------------------------------------------

_TS = "2026-09-25T10:00:00"


def _prov(
    use_case: str = "change-intelligence",
    authority: AuthorityOutcome = AuthorityOutcome.A1,
    region: str = "eu-west-1",
) -> Provenance:
    return Provenance(
        use_case=use_case,
        model_profile="model:gemini-pro@2026.09",
        provider_profile="provider:eu-hosted",
        prompt_profile="prompt:change-extract@3",
        region=region,
        data_class="INTERNAL",
        risk_tier=RiskTier.T1,
        authority_outcome=authority,
        ai_train_version="0.6.0",
    )


def _registry_for(prov: Provenance) -> UseCaseRegistry:
    return UseCaseRegistry(
        [
            UseCase(
                use_case_id=prov.use_case,
                owner="lane-l",
                description="test",
                max_risk_tier=RiskTier.T4,
                max_authority=AuthorityOutcome.A4,
                permitted_regions=frozenset({prov.region}),
            )
        ]
    )


def _build(
    use_case: str = "change-intelligence",
    outcome: InvocationOutcome = InvocationOutcome.PROPOSE,
    usage: UsageMetrics | None = None,
    authority: AuthorityOutcome = AuthorityOutcome.A1,
) -> InvocationEvidenceRecord:
    prov = _prov(use_case=use_case, authority=authority)
    if outcome is InvocationOutcome.BLOCK:
        registry: UseCaseRegistry = UseCaseRegistry()
    else:
        registry = _registry_for(prov)
    b = InvocationEvidenceBuilder(
        provenance=prov,
        invocation_outcome=outcome,
        registry=registry,
    )
    if usage:
        b.set_usage(usage)
    return b.build()


def _policy(
    use_case: str = "change-intelligence",
    max_tokens: int | None = None,
    max_invocations: int | None = None,
    max_cost: float | None = None,
    window: QuotaWindow = QuotaWindow.HOUR,
) -> QuotaPolicy:
    return QuotaPolicy(
        use_case_id=use_case,
        window=window,
        max_tokens=max_tokens,
        max_invocations=max_invocations,
        max_monetary_cost_usd=max_cost,
    )


# ---------------------------------------------------------------------------
# CapacityError
# ---------------------------------------------------------------------------


def test_capacity_error_is_exception() -> None:
    assert isinstance(CapacityError("x"), Exception)


def test_capacity_error_reason_attribute() -> None:
    assert CapacityError("quota exceeded").reason == "quota exceeded"


def test_capacity_error_use_case_attribute() -> None:
    err = CapacityError("x", use_case="ci")
    assert err.use_case == "ci"


def test_capacity_error_violations_defaults_to_empty() -> None:
    assert CapacityError("x").violations == []


def test_capacity_error_stores_violations() -> None:
    v = QuotaViolation(dimension="tokens", limit=1000.0, actual=1001.0, use_case="ci")
    err = CapacityError("x", violations=[v])
    assert len(err.violations) == 1
    assert err.violations[0] is v


def test_capacity_error_str_contains_reason() -> None:
    assert "quota exceeded" in str(CapacityError("quota exceeded"))


# ---------------------------------------------------------------------------
# QuotaWindow
# ---------------------------------------------------------------------------


def test_quota_window_has_three_values() -> None:
    assert len(QuotaWindow) == 3


def test_quota_window_values() -> None:
    assert QuotaWindow.MINUTE == "MINUTE"
    assert QuotaWindow.HOUR == "HOUR"
    assert QuotaWindow.DAY == "DAY"


# ---------------------------------------------------------------------------
# QuotaViolation
# ---------------------------------------------------------------------------


def test_quota_violation_stores_fields() -> None:
    v = QuotaViolation(dimension="tokens", limit=500.0, actual=600.0, use_case="ci")
    assert v.dimension == "tokens"
    assert v.limit == 500.0
    assert v.actual == 600.0
    assert v.use_case == "ci"


def test_quota_violation_is_frozen() -> None:
    from dataclasses import FrozenInstanceError
    v = QuotaViolation(dimension="tokens", limit=500.0, actual=600.0, use_case="ci")
    with pytest.raises(FrozenInstanceError):
        v.actual = 999.0  # type: ignore[misc]


def test_quota_violation_as_dict_keys() -> None:
    v = QuotaViolation(dimension="invocations", limit=10.0, actual=11.0, use_case="ci")
    d = v.as_dict()
    assert set(d.keys()) == {"dimension", "limit", "actual", "use_case"}


def test_quota_violation_as_dict_values() -> None:
    v = QuotaViolation(dimension="monetary_cost_usd", limit=5.0, actual=5.01, use_case="ci")
    d = v.as_dict()
    assert d["dimension"] == "monetary_cost_usd"
    assert d["limit"] == pytest.approx(5.0)
    assert d["actual"] == pytest.approx(5.01)


# ---------------------------------------------------------------------------
# QuotaPolicy -- construction
# ---------------------------------------------------------------------------


def test_quota_policy_stores_use_case_id() -> None:
    assert _policy().use_case_id == "change-intelligence"


def test_quota_policy_default_window_is_hour() -> None:
    assert _policy().window is QuotaWindow.HOUR


def test_quota_policy_stores_max_tokens() -> None:
    assert _policy(max_tokens=1000).max_tokens == 1000


def test_quota_policy_stores_max_invocations() -> None:
    assert _policy(max_invocations=50).max_invocations == 50


def test_quota_policy_stores_max_monetary_cost() -> None:
    assert _policy(max_cost=10.0).max_monetary_cost_usd == pytest.approx(10.0)


# ---------------------------------------------------------------------------
# QuotaPolicy -- validation
# ---------------------------------------------------------------------------


def test_quota_policy_empty_use_case_id_refused() -> None:
    with pytest.raises(CapacityError, match="use_case_id"):
        QuotaPolicy(use_case_id="")


def test_quota_policy_zero_max_tokens_refused() -> None:
    with pytest.raises(CapacityError, match="max_tokens"):
        QuotaPolicy(use_case_id="ci", max_tokens=0)


def test_quota_policy_negative_max_tokens_refused() -> None:
    with pytest.raises(CapacityError, match="max_tokens"):
        QuotaPolicy(use_case_id="ci", max_tokens=-1)


def test_quota_policy_zero_max_invocations_refused() -> None:
    with pytest.raises(CapacityError, match="max_invocations"):
        QuotaPolicy(use_case_id="ci", max_invocations=0)


def test_quota_policy_zero_max_monetary_cost_refused() -> None:
    with pytest.raises(CapacityError, match="max_monetary_cost_usd"):
        QuotaPolicy(use_case_id="ci", max_monetary_cost_usd=0.0)


def test_quota_policy_negative_max_monetary_cost_refused() -> None:
    with pytest.raises(CapacityError, match="max_monetary_cost_usd"):
        QuotaPolicy(use_case_id="ci", max_monetary_cost_usd=-1.0)


# ---------------------------------------------------------------------------
# QuotaPolicy -- is_unconstrained
# ---------------------------------------------------------------------------


def test_quota_policy_is_unconstrained_when_no_limits() -> None:
    assert _policy().is_unconstrained is True


def test_quota_policy_is_not_unconstrained_when_max_tokens_set() -> None:
    assert _policy(max_tokens=1000).is_unconstrained is False


def test_quota_policy_is_not_unconstrained_when_max_invocations_set() -> None:
    assert _policy(max_invocations=10).is_unconstrained is False


def test_quota_policy_is_not_unconstrained_when_max_cost_set() -> None:
    assert _policy(max_cost=5.0).is_unconstrained is False


# ---------------------------------------------------------------------------
# QuotaPolicy -- immutability
# ---------------------------------------------------------------------------


def test_quota_policy_is_frozen() -> None:
    from dataclasses import FrozenInstanceError
    p = _policy(max_tokens=100)
    with pytest.raises(FrozenInstanceError):
        p.max_tokens = 200  # type: ignore[misc]


# ---------------------------------------------------------------------------
# QuotaState
# ---------------------------------------------------------------------------


def test_quota_state_defaults_to_zero() -> None:
    s = QuotaState(use_case_id="ci")
    assert s.total_tokens == 0
    assert s.total_invocations == 0
    assert s.total_cost_usd == 0.0
    assert s.blocked_count == 0


def test_quota_state_as_dict_keys() -> None:
    d = QuotaState(use_case_id="ci").as_dict()
    assert set(d.keys()) == {
        "use_case_id", "total_tokens", "total_invocations",
        "total_cost_usd", "blocked_count",
    }


# ---------------------------------------------------------------------------
# FinOpsReport
# ---------------------------------------------------------------------------


def test_finops_report_is_frozen() -> None:
    from dataclasses import FrozenInstanceError
    r = FinOpsReport(
        states={}, total_tokens=0, total_invocations=0,
        total_cost_usd=0.0, total_blocked=0, use_case_count=0,
    )
    with pytest.raises(FrozenInstanceError):
        r.total_tokens = 999  # type: ignore[misc]


def test_finops_report_as_dict_keys() -> None:
    r = FinOpsReport(
        states={}, total_tokens=10, total_invocations=1,
        total_cost_usd=0.5, total_blocked=0, use_case_count=1,
    )
    d = r.as_dict()
    assert set(d.keys()) == {
        "total_tokens", "total_invocations", "total_cost_usd",
        "total_blocked", "use_case_count", "states",
    }


# ---------------------------------------------------------------------------
# CapacityLedger -- register
# ---------------------------------------------------------------------------


def test_register_stores_policy() -> None:
    ledger = CapacityLedger()
    p = _policy(max_tokens=100)
    ledger.register(p)
    assert ledger.policy("change-intelligence") is p


def test_register_creates_quota_state() -> None:
    ledger = CapacityLedger()
    ledger.register(_policy())
    assert ledger.state("change-intelligence") is not None


def test_register_replaces_policy_on_re_registration() -> None:
    ledger = CapacityLedger()
    ledger.register(_policy(max_tokens=100))
    ledger.register(_policy(max_tokens=200))
    assert ledger.policy("change-intelligence").max_tokens == 200  # type: ignore[union-attr]


def test_register_preserves_counters_on_re_registration() -> None:
    """Re-registering must not zero existing counters."""
    ledger = CapacityLedger()
    ledger.register(_policy(max_tokens=10_000))
    usage = UsageMetrics(input_tokens=50, output_tokens=10)
    ledger.consume(_build(usage=usage))
    ledger.register(_policy(max_tokens=20_000))  # replace policy
    assert ledger.state("change-intelligence").total_invocations == 1  # type: ignore[union-attr]


def test_register_rejects_non_quota_policy() -> None:
    ledger = CapacityLedger()
    with pytest.raises(CapacityError, match="QuotaPolicy"):
        ledger.register("not a policy")  # type: ignore[arg-type]


def test_policy_returns_none_for_unknown_use_case() -> None:
    assert CapacityLedger().policy("unknown") is None


def test_state_returns_none_for_unknown_use_case() -> None:
    assert CapacityLedger().state("unknown") is None


# ---------------------------------------------------------------------------
# CapacityLedger -- consume: basic counter accumulation
# ---------------------------------------------------------------------------


def test_consume_increments_invocation_count() -> None:
    ledger = CapacityLedger()
    ledger.consume(_build())
    assert ledger.state("change-intelligence").total_invocations == 1  # type: ignore[union-attr]


def test_consume_increments_token_count() -> None:
    ledger = CapacityLedger()
    usage = UsageMetrics(input_tokens=100, output_tokens=40)
    ledger.consume(_build(usage=usage))
    assert ledger.state("change-intelligence").total_tokens == 140  # type: ignore[union-attr]


def test_consume_accumulates_across_multiple_records() -> None:
    ledger = CapacityLedger()
    usage = UsageMetrics(input_tokens=100)
    ledger.consume(_build(usage=usage))
    ledger.consume(_build(usage=usage))
    assert ledger.state("change-intelligence").total_tokens == 200  # type: ignore[union-attr]
    assert ledger.state("change-intelligence").total_invocations == 2  # type: ignore[union-attr]


def test_consume_accumulates_monetary_cost() -> None:
    ledger = CapacityLedger()
    usage = UsageMetrics(monetary_cost_usd=0.01)
    ledger.consume(_build(usage=usage))
    ledger.consume(_build(usage=usage))
    assert ledger.state("change-intelligence").total_cost_usd == pytest.approx(0.02)  # type: ignore[union-attr]


def test_consume_creates_state_for_unregistered_use_case() -> None:
    """consume auto-creates a state entry for use cases with no policy."""
    ledger = CapacityLedger()
    ledger.consume(_build(use_case="change-intelligence"))
    assert ledger.state("change-intelligence") is not None


def test_consume_rejects_non_record() -> None:
    ledger = CapacityLedger()
    with pytest.raises(CapacityError, match="InvocationEvidenceRecord"):
        ledger.consume("not a record")  # type: ignore[arg-type]


# ---------------------------------------------------------------------------
# CapacityLedger -- consume: BLOCK exemption (design rule 5)
# ---------------------------------------------------------------------------


def test_consume_block_increments_blocked_count() -> None:
    ledger = CapacityLedger()
    ledger.consume(_build(outcome=InvocationOutcome.BLOCK))
    assert ledger.state("change-intelligence").blocked_count == 1  # type: ignore[union-attr]


def test_consume_block_does_not_increment_invocation_count() -> None:
    ledger = CapacityLedger()
    ledger.consume(_build(outcome=InvocationOutcome.BLOCK))
    assert ledger.state("change-intelligence").total_invocations == 0  # type: ignore[union-attr]


def test_consume_block_does_not_increment_token_count() -> None:
    usage = UsageMetrics(input_tokens=999)
    ledger = CapacityLedger()
    ledger.consume(_build(outcome=InvocationOutcome.BLOCK, usage=usage))
    assert ledger.state("change-intelligence").total_tokens == 0  # type: ignore[union-attr]


def test_consume_block_does_not_consume_monetary_budget() -> None:
    usage = UsageMetrics(monetary_cost_usd=100.0)
    ledger = CapacityLedger()
    ledger.register(_policy(max_cost=1.0))
    # Must not raise even though 100.0 > 1.0.
    ledger.consume(_build(outcome=InvocationOutcome.BLOCK, usage=usage))


def test_consume_block_at_max_invocations_does_not_raise() -> None:
    """A BLOCK invocation must not count against invocation quota."""
    ledger = CapacityLedger()
    ledger.register(_policy(max_invocations=1))
    ledger.consume(_build())  # use the one allowed invocation
    # Second record is BLOCK -- must not raise.
    ledger.consume(_build(outcome=InvocationOutcome.BLOCK))


# ---------------------------------------------------------------------------
# CapacityLedger -- consume: quota enforcement
# ---------------------------------------------------------------------------


def test_consume_raises_on_token_limit_exceeded() -> None:
    ledger = CapacityLedger()
    ledger.register(_policy(max_tokens=100))
    usage = UsageMetrics(input_tokens=101)
    with pytest.raises(CapacityError) as exc:
        ledger.consume(_build(usage=usage))
    assert any(v.dimension == "tokens" for v in exc.value.violations)


def test_consume_raises_on_invocation_limit_exceeded() -> None:
    ledger = CapacityLedger()
    ledger.register(_policy(max_invocations=1))
    ledger.consume(_build())  # first: allowed
    with pytest.raises(CapacityError) as exc:
        ledger.consume(_build())  # second: refused
    assert any(v.dimension == "invocations" for v in exc.value.violations)


def test_consume_raises_on_monetary_cost_limit_exceeded() -> None:
    ledger = CapacityLedger()
    ledger.register(_policy(max_cost=0.005))
    usage = UsageMetrics(monetary_cost_usd=0.006)
    with pytest.raises(CapacityError) as exc:
        ledger.consume(_build(usage=usage))
    assert any(v.dimension == "monetary_cost_usd" for v in exc.value.violations)


def test_consume_exactly_at_token_limit_is_permitted() -> None:
    """Consuming exactly the limit must succeed (limit is not strict)."""
    ledger = CapacityLedger()
    ledger.register(_policy(max_tokens=100))
    usage = UsageMetrics(input_tokens=100)
    ledger.consume(_build(usage=usage))  # must not raise
    assert ledger.state("change-intelligence").total_tokens == 100  # type: ignore[union-attr]


def test_consume_exactly_at_invocation_limit_is_permitted() -> None:
    ledger = CapacityLedger()
    ledger.register(_policy(max_invocations=2))
    ledger.consume(_build())
    ledger.consume(_build())  # must not raise


def test_consume_multi_violation_reports_all_breached_dimensions() -> None:
    """When tokens AND invocations are both exceeded, both violations reported."""
    ledger = CapacityLedger()
    ledger.register(_policy(max_tokens=10, max_invocations=1))
    ledger.consume(_build(usage=UsageMetrics(input_tokens=5)))  # first: allowed
    with pytest.raises(CapacityError) as exc:
        ledger.consume(_build(usage=UsageMetrics(input_tokens=10)))  # second: both exceeded
    dims = {v.dimension for v in exc.value.violations}
    assert "tokens" in dims
    assert "invocations" in dims


def test_consume_violation_names_the_use_case() -> None:
    ledger = CapacityLedger()
    ledger.register(_policy(max_tokens=1))
    with pytest.raises(CapacityError) as exc:
        ledger.consume(_build(usage=UsageMetrics(input_tokens=2)))
    assert exc.value.use_case == "change-intelligence"


def test_consume_violation_names_the_use_case_in_message() -> None:
    ledger = CapacityLedger()
    ledger.register(_policy(max_tokens=1))
    with pytest.raises(CapacityError, match="change-intelligence"):
        ledger.consume(_build(usage=UsageMetrics(input_tokens=2)))


def test_consume_no_policy_is_always_permissive() -> None:
    """Without a registered policy, consume never raises."""
    ledger = CapacityLedger()
    for _ in range(100):
        ledger.consume(_build(usage=UsageMetrics(input_tokens=10_000, monetary_cost_usd=100.0)))
    assert ledger.state("change-intelligence").total_invocations == 100  # type: ignore[union-attr]


# ---------------------------------------------------------------------------
# CapacityLedger -- atomicity: counters not changed on violation
# ---------------------------------------------------------------------------


def test_consume_violation_does_not_modify_token_counter() -> None:
    """If the quota check fails, no counter must be updated."""
    ledger = CapacityLedger()
    ledger.register(_policy(max_tokens=50))
    ledger.consume(_build(usage=UsageMetrics(input_tokens=40)))
    state_before = ledger.state("change-intelligence").total_tokens  # type: ignore[union-attr]
    with pytest.raises(CapacityError):
        ledger.consume(_build(usage=UsageMetrics(input_tokens=20)))
    # Token counter must still be 40, not 60.
    assert ledger.state("change-intelligence").total_tokens == state_before  # type: ignore[union-attr]


def test_consume_violation_does_not_modify_invocation_counter() -> None:
    ledger = CapacityLedger()
    ledger.register(_policy(max_invocations=1))
    ledger.consume(_build())
    with pytest.raises(CapacityError):
        ledger.consume(_build())
    assert ledger.state("change-intelligence").total_invocations == 1  # type: ignore[union-attr]


def test_consume_violation_does_not_modify_cost_counter() -> None:
    ledger = CapacityLedger()
    ledger.register(_policy(max_cost=0.01))
    ledger.consume(_build(usage=UsageMetrics(monetary_cost_usd=0.005)))
    with pytest.raises(CapacityError):
        ledger.consume(_build(usage=UsageMetrics(monetary_cost_usd=0.01)))
    assert ledger.state("change-intelligence").total_cost_usd == pytest.approx(0.005)  # type: ignore[union-attr]


# ---------------------------------------------------------------------------
# CapacityLedger -- reset
# ---------------------------------------------------------------------------


def test_reset_per_use_case_zeroes_counters() -> None:
    ledger = CapacityLedger()
    ledger.consume(_build(usage=UsageMetrics(input_tokens=100, monetary_cost_usd=0.01)))
    ledger.reset("change-intelligence")
    s = ledger.state("change-intelligence")
    assert s.total_tokens == 0  # type: ignore[union-attr]
    assert s.total_invocations == 0  # type: ignore[union-attr]
    assert s.total_cost_usd == 0.0  # type: ignore[union-attr]


def test_reset_per_use_case_clears_blocked_count() -> None:
    ledger = CapacityLedger()
    ledger.consume(_build(outcome=InvocationOutcome.BLOCK))
    ledger.reset("change-intelligence")
    assert ledger.state("change-intelligence").blocked_count == 0  # type: ignore[union-attr]


def test_reset_per_use_case_preserves_policy() -> None:
    ledger = CapacityLedger()
    p = _policy(max_tokens=500)
    ledger.register(p)
    ledger.reset("change-intelligence")
    assert ledger.policy("change-intelligence") is p


def test_reset_per_use_case_does_not_affect_other_use_cases() -> None:
    ledger = CapacityLedger()
    usage = UsageMetrics(input_tokens=50)
    ledger.consume(_build(use_case="change-intelligence", usage=usage))
    ledger.consume(_build(use_case="sku-classification", usage=usage))
    ledger.reset("change-intelligence")
    assert ledger.state("sku-classification").total_invocations == 1  # type: ignore[union-attr]


def test_reset_global_zeroes_all_use_cases() -> None:
    ledger = CapacityLedger()
    usage = UsageMetrics(input_tokens=50)
    ledger.consume(_build(use_case="change-intelligence", usage=usage))
    ledger.consume(_build(use_case="sku-classification", usage=usage))
    ledger.reset()
    assert ledger.state("change-intelligence").total_tokens == 0  # type: ignore[union-attr]
    assert ledger.state("sku-classification").total_tokens == 0  # type: ignore[union-attr]


def test_reset_unknown_use_case_is_silent() -> None:
    """Resetting a use case that has no state must not raise."""
    CapacityLedger().reset("nonexistent")  # must not raise


def test_reset_allows_subsequent_consumes_up_to_limit() -> None:
    """After a reset, quota limits can be reached again."""
    ledger = CapacityLedger()
    ledger.register(_policy(max_invocations=1))
    ledger.consume(_build())
    ledger.reset("change-intelligence")
    ledger.consume(_build())  # must not raise -- counters were zeroed


# ---------------------------------------------------------------------------
# CapacityLedger -- report
# ---------------------------------------------------------------------------


def test_report_empty_ledger_has_zero_totals() -> None:
    r = CapacityLedger().report()
    assert r.total_tokens == 0
    assert r.total_invocations == 0
    assert r.total_cost_usd == pytest.approx(0.0)
    assert r.use_case_count == 0


def test_report_totals_across_use_cases() -> None:
    ledger = CapacityLedger()
    usage = UsageMetrics(input_tokens=100, output_tokens=50, monetary_cost_usd=0.01)
    ledger.consume(_build(use_case="change-intelligence", usage=usage))
    ledger.consume(_build(use_case="sku-classification", usage=usage))
    r = ledger.report()
    assert r.total_tokens == 300
    assert r.total_invocations == 2
    assert r.total_cost_usd == pytest.approx(0.02)
    assert r.use_case_count == 2


def test_report_includes_blocked_count() -> None:
    ledger = CapacityLedger()
    ledger.consume(_build(outcome=InvocationOutcome.BLOCK))
    r = ledger.report()
    assert r.total_blocked == 1


def test_report_snapshot_is_independent_of_ledger() -> None:
    """Modifying the ledger after report() must not change the snapshot."""
    ledger = CapacityLedger()
    usage = UsageMetrics(input_tokens=100)
    ledger.consume(_build(usage=usage))
    snap = ledger.report()
    ledger.consume(_build(usage=usage))  # second consume
    assert snap.total_invocations == 1   # snapshot still reflects first consume


def test_report_states_keyed_by_use_case_id() -> None:
    ledger = CapacityLedger()
    ledger.consume(_build(use_case="change-intelligence"))
    r = ledger.report()
    assert "change-intelligence" in r.states


def test_report_as_dict_keys() -> None:
    r = CapacityLedger().report()
    d = r.as_dict()
    assert set(d.keys()) == {
        "total_tokens", "total_invocations", "total_cost_usd",
        "total_blocked", "use_case_count", "states",
    }


# ---------------------------------------------------------------------------
# Integration -- multi-use-case ledger lifecycle
# ---------------------------------------------------------------------------


def test_integration_multi_use_case_quota_enforcement() -> None:
    """Two use cases with different quotas; each enforced independently."""
    ledger = CapacityLedger()
    ledger.register(_policy("change-intelligence", max_invocations=3, max_tokens=1000))
    ledger.register(_policy("sku-classification", max_invocations=1))

    usage = UsageMetrics(input_tokens=100, output_tokens=50, monetary_cost_usd=0.002)

    # change-intelligence: 3 calls allowed
    ledger.consume(_build("change-intelligence", usage=usage))
    ledger.consume(_build("change-intelligence", usage=usage))
    ledger.consume(_build("change-intelligence", usage=usage))

    # 4th call refused
    with pytest.raises(CapacityError) as exc:
        ledger.consume(_build("change-intelligence", usage=usage))
    assert exc.value.use_case == "change-intelligence"

    # sku-classification: 1 call allowed, 2nd refused
    ledger.consume(_build("sku-classification", usage=usage))
    with pytest.raises(CapacityError):
        ledger.consume(_build("sku-classification", usage=usage))

    # Totals in report
    r = ledger.report()
    assert r.states["change-intelligence"].total_invocations == 3
    assert r.states["sku-classification"].total_invocations == 1
    assert r.total_invocations == 4


def test_integration_reset_and_refill() -> None:
    """After a window reset the quota budget is available again."""
    ledger = CapacityLedger()
    ledger.register(_policy(max_invocations=2))
    ledger.consume(_build())
    ledger.consume(_build())

    with pytest.raises(CapacityError):
        ledger.consume(_build())

    ledger.reset()  # simulate window rollover
    ledger.consume(_build())
    ledger.consume(_build())

    r = ledger.report()
    assert r.total_invocations == 2


def test_integration_block_never_exhausts_budget() -> None:
    """Many BLOCK invocations must not exhaust any quota dimension."""
    ledger = CapacityLedger()
    ledger.register(
        _policy(max_tokens=100, max_invocations=5, max_cost=1.0)
    )
    usage = UsageMetrics(input_tokens=999, output_tokens=999, monetary_cost_usd=999.0)
    for _ in range(100):
        ledger.consume(_build(outcome=InvocationOutcome.BLOCK, usage=usage))

    r = ledger.report()
    assert r.total_blocked == 100
    assert r.total_tokens == 0
    assert r.total_invocations == 0
    assert r.total_cost_usd == pytest.approx(0.0)

    # Normal calls still have full budget
    ledger.consume(_build(usage=UsageMetrics(input_tokens=10)))
    assert ledger.state("change-intelligence").total_invocations == 1  # type: ignore[union-attr]
