"""Tests for review_metrics -- ZTAX-AIGOV-001 / REQ-0076.

All tests are pure: no I/O, no external fixtures.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta, timezone

import pytest

from ztax_gateway.citation import Citation, SourceChunk, combine
from ztax_gateway.governance import UseCase, UseCaseRegistry
from ztax_gateway.human_review import (
    ReviewDecision,
    ReviewerProfile,
    ReviewOutcome,
    ReviewQueue,
)
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier
from ztax_gateway.review_metrics import (
    RateReport,
    ReviewObservation,
    compute_rates,
    observation_from_decision,
)

# ---------------------------------------------------------------------------
# Shared fixtures (mirrors test_human_review.py style)
# ---------------------------------------------------------------------------

_USE_CASE = UseCase(
    use_case_id="classification-review",
    owner="lane-l",
    description="AI output review for metrics tests",
    max_risk_tier=RiskTier.T2,
    max_authority=AuthorityOutcome.A2,
    permitted_regions=frozenset({"eu-west-1"}),
    permitted_data_classes=frozenset({"INTERNAL"}),
)

_PROVENANCE = Provenance(
    use_case="classification-review",
    model_profile="model:review-metrics@1",
    provider_profile="provider:eu-hosted",
    prompt_profile="prompt:review@1",
    region="eu-west-1",
    data_class="INTERNAL",
    risk_tier=RiskTier.T1,
    authority_outcome=AuthorityOutcome.A1,
    ai_train_version="0.1.0",
)

_REVIEWER = ReviewerProfile(
    reviewer_id="reviewer:alice",
    max_authority=AuthorityOutcome.A2,
    permitted_regions=frozenset({"eu-west-1"}),
    permitted_jurisdictions=frozenset({"EU-VAT"}),
    qualified_since=datetime(2026, 1, 1, tzinfo=UTC),
)

_T0 = datetime(2026, 6, 1, 12, 0, 0, tzinfo=UTC)


def _registry() -> UseCaseRegistry:
    return UseCaseRegistry([_USE_CASE])


def _citation() -> Citation:
    chunk = SourceChunk(
        source_id="doc-a",
        content=b"tax rule",
        byte_start=0,
        byte_end=8,
    )
    return combine([chunk])


def _real_decision(
    outcome: ReviewOutcome = ReviewOutcome.APPROVE,
    correction: str = "",
) -> ReviewDecision:
    """Build a ReviewDecision the same way test_human_review.py does."""
    q = ReviewQueue()
    item = q.submit(
        output_id="out-001",
        citation=_citation(),
        summary="metrics test item",
        provenance=_PROVENANCE,
        registry=_registry(),
    )
    return q.decide(
        review_item_id=item.review_item_id,
        reviewer_profile=_REVIEWER,
        outcome=outcome,
        correction=correction,
    )


def _obs(
    outcome: ReviewOutcome,
    *,
    reviewer_id: str = "reviewer:alice",
    use_case: str = "uc-a",
    decided_at: datetime = _T0,
) -> ReviewObservation:
    return ReviewObservation(
        reviewer_id=reviewer_id,
        use_case=use_case,
        outcome=outcome,
        decided_at=decided_at,
    )


# ---------------------------------------------------------------------------
# ReviewOutcome -- verify the values we depend on
# ---------------------------------------------------------------------------


class TestReviewOutcomeValues:
    def test_approve(self) -> None:
        assert ReviewOutcome.APPROVE == "APPROVE"

    def test_reject(self) -> None:
        assert ReviewOutcome.REJECT == "REJECT"

    def test_escalate(self) -> None:
        assert ReviewOutcome.ESCALATE == "ESCALATE"

    def test_modify(self) -> None:
        assert ReviewOutcome.MODIFY == "MODIFY"


# ---------------------------------------------------------------------------
# ReviewObservation -- construction and validation
# ---------------------------------------------------------------------------


class TestReviewObservation:
    def test_valid(self) -> None:
        o = _obs(ReviewOutcome.APPROVE)
        assert o.reviewer_id == "reviewer:alice"
        assert o.use_case == "uc-a"
        assert o.outcome is ReviewOutcome.APPROVE
        assert o.decided_at == _T0

    def test_frozen(self) -> None:
        o = _obs(ReviewOutcome.APPROVE)
        with pytest.raises((AttributeError, TypeError)):
            o.outcome = ReviewOutcome.REJECT  # type: ignore[misc]

    def test_empty_reviewer_id_raises(self) -> None:
        with pytest.raises(ValueError, match="reviewer_id"):
            ReviewObservation(
                reviewer_id="",
                use_case="uc-a",
                outcome=ReviewOutcome.APPROVE,
                decided_at=_T0,
            )

    def test_empty_use_case_raises(self) -> None:
        with pytest.raises(ValueError, match="use_case"):
            ReviewObservation(
                reviewer_id="reviewer:alice",
                use_case="",
                outcome=ReviewOutcome.APPROVE,
                decided_at=_T0,
            )

    def test_naive_datetime_raises(self) -> None:
        naive = datetime(2026, 1, 1)
        with pytest.raises(ValueError, match="timezone-aware"):
            ReviewObservation(
                reviewer_id="reviewer:alice",
                use_case="uc-a",
                outcome=ReviewOutcome.APPROVE,
                decided_at=naive,
            )

    def test_non_utc_tz_accepted(self) -> None:
        """Any timezone-aware datetime is accepted, not only UTC."""
        ist = timezone(timedelta(hours=5, minutes=30))
        t = datetime(2026, 1, 1, 12, 0, 0, tzinfo=ist)
        o = ReviewObservation(
            reviewer_id="reviewer:alice",
            use_case="uc-a",
            outcome=ReviewOutcome.APPROVE,
            decided_at=t,
        )
        assert o.decided_at.tzinfo is not None


# ---------------------------------------------------------------------------
# observation_from_decision -- real ReviewDecision
# ---------------------------------------------------------------------------


class TestObservationFromDecision:
    def test_approve_roundtrip(self) -> None:
        decision = _real_decision(ReviewOutcome.APPROVE)
        obs = observation_from_decision(decision, "classification-review")
        assert obs.reviewer_id == "reviewer:alice"
        assert obs.use_case == "classification-review"
        assert obs.outcome is ReviewOutcome.APPROVE
        assert obs.decided_at == decision.decided_at
        assert obs.decided_at.tzinfo is not None

    def test_reject_roundtrip(self) -> None:
        decision = _real_decision(ReviewOutcome.REJECT)
        obs = observation_from_decision(decision, "classification-review")
        assert obs.outcome is ReviewOutcome.REJECT

    def test_modify_roundtrip(self) -> None:
        decision = _real_decision(ReviewOutcome.MODIFY, correction="corrected text")
        obs = observation_from_decision(decision, "classification-review")
        assert obs.outcome is ReviewOutcome.MODIFY

    def test_returns_review_observation(self) -> None:
        decision = _real_decision()
        obs = observation_from_decision(decision, "uc-x")
        assert isinstance(obs, ReviewObservation)


# ---------------------------------------------------------------------------
# compute_rates -- min_sample is required
# ---------------------------------------------------------------------------


class TestMinSampleRequired:
    def test_calling_without_min_sample_raises_type_error(self) -> None:
        with pytest.raises(TypeError):
            compute_rates([], group_by="all")  # type: ignore[call-arg]

    def test_calling_without_group_by_raises_type_error(self) -> None:
        with pytest.raises(TypeError):
            compute_rates([], min_sample=5)  # type: ignore[call-arg]


# ---------------------------------------------------------------------------
# compute_rates -- empty input
# ---------------------------------------------------------------------------


class TestComputeRatesEmpty:
    def test_empty_returns_empty_tuple(self) -> None:
        assert compute_rates([], group_by="all", min_sample=5) == ()

    def test_returns_tuple_type(self) -> None:
        result = compute_rates(
            [_obs(ReviewOutcome.APPROVE)], group_by="all", min_sample=5
        )
        assert isinstance(result, tuple)


# ---------------------------------------------------------------------------
# compute_rates -- hand-computed sample (group_by="all")
# ---------------------------------------------------------------------------


class TestHandComputedRates:
    def _sample(self) -> list[ReviewObservation]:
        # 4 approve, 2 reject, 1 modify, 1 escalate = 8 total
        return [
            _obs(outcome, reviewer_id=f"rev-{i}", use_case="uc-a", decided_at=_T0)
            for i, outcome in enumerate(
                [
                    ReviewOutcome.APPROVE,
                    ReviewOutcome.APPROVE,
                    ReviewOutcome.APPROVE,
                    ReviewOutcome.APPROVE,
                    ReviewOutcome.REJECT,
                    ReviewOutcome.REJECT,
                    ReviewOutcome.MODIFY,
                    ReviewOutcome.ESCALATE,
                ]
            )
        ]


    def test_total(self) -> None:
        (r,) = compute_rates(self._sample(), group_by="all", min_sample=1)
        assert r.total == 8

    def test_approved_count(self) -> None:
        (r,) = compute_rates(self._sample(), group_by="all", min_sample=1)
        assert r.approved == 4

    def test_rejected_count(self) -> None:
        (r,) = compute_rates(self._sample(), group_by="all", min_sample=1)
        assert r.rejected == 2

    def test_modified_count(self) -> None:
        (r,) = compute_rates(self._sample(), group_by="all", min_sample=1)
        assert r.modified == 1

    def test_escalated_count(self) -> None:
        (r,) = compute_rates(self._sample(), group_by="all", min_sample=1)
        assert r.escalated == 1

    def test_rejection_rate(self) -> None:
        (r,) = compute_rates(self._sample(), group_by="all", min_sample=1)
        assert r.rejection_rate == pytest.approx(2 / 8)

    def test_override_rate(self) -> None:
        (r,) = compute_rates(self._sample(), group_by="all", min_sample=1)
        assert r.override_rate == pytest.approx(1 / 8)

    def test_escalation_rate(self) -> None:
        (r,) = compute_rates(self._sample(), group_by="all", min_sample=1)
        assert r.escalation_rate == pytest.approx(1 / 8)

    def test_key_is_all(self) -> None:
        (r,) = compute_rates(self._sample(), group_by="all", min_sample=1)
        assert r.key == "all"

    def test_sufficient_sample(self) -> None:
        (r,) = compute_rates(self._sample(), group_by="all", min_sample=8)
        assert r.insufficient_sample is False

    def test_insufficient_sample_flag(self) -> None:
        (r,) = compute_rates(self._sample(), group_by="all", min_sample=9)
        assert r.insufficient_sample is True


# ---------------------------------------------------------------------------
# compute_rates -- grouping by use_case
# ---------------------------------------------------------------------------


class TestGroupByUseCase:
    def _observations(self) -> list[ReviewObservation]:
        return [
            _obs(ReviewOutcome.APPROVE, use_case="uc-a", decided_at=_T0),
            _obs(ReviewOutcome.REJECT, use_case="uc-a", decided_at=_T0),
            _obs(ReviewOutcome.MODIFY, use_case="uc-b", decided_at=_T0),
            _obs(ReviewOutcome.ESCALATE, use_case="uc-b", decided_at=_T0),
        ]

    def test_two_cohorts(self) -> None:
        reports = compute_rates(self._observations(), group_by="use_case", min_sample=1)
        assert len(reports) == 2

    def test_cohorts_sorted_by_key(self) -> None:
        reports = compute_rates(self._observations(), group_by="use_case", min_sample=1)
        keys = [r.key for r in reports]
        assert keys == sorted(keys)

    def test_uca_stats(self) -> None:
        reports = compute_rates(self._observations(), group_by="use_case", min_sample=1)
        uca = next(r for r in reports if r.key == "uc-a")
        assert uca.total == 2
        assert uca.approved == 1
        assert uca.rejected == 1
        assert uca.rejection_rate == pytest.approx(0.5)

    def test_ucb_stats(self) -> None:
        reports = compute_rates(self._observations(), group_by="use_case", min_sample=1)
        ucb = next(r for r in reports if r.key == "uc-b")
        assert ucb.total == 2
        assert ucb.modified == 1
        assert ucb.escalated == 1
        assert ucb.override_rate == pytest.approx(0.5)
        assert ucb.escalation_rate == pytest.approx(0.5)


# ---------------------------------------------------------------------------
# compute_rates -- grouping by reviewer
# ---------------------------------------------------------------------------


class TestGroupByReviewer:
    def _observations(self) -> list[ReviewObservation]:
        return [
            _obs(ReviewOutcome.APPROVE, reviewer_id="rev:alice", decided_at=_T0),
            _obs(ReviewOutcome.REJECT, reviewer_id="rev:alice", decided_at=_T0),
            _obs(ReviewOutcome.MODIFY, reviewer_id="rev:bob", decided_at=_T0),
        ]

    def test_two_cohorts(self) -> None:
        reports = compute_rates(self._observations(), group_by="reviewer", min_sample=1)
        assert len(reports) == 2

    def test_alice_stats(self) -> None:
        reports = compute_rates(self._observations(), group_by="reviewer", min_sample=1)
        alice = next(r for r in reports if r.key == "rev:alice")
        assert alice.total == 2
        assert alice.rejection_rate == pytest.approx(0.5)

    def test_bob_stats(self) -> None:
        reports = compute_rates(self._observations(), group_by="reviewer", min_sample=1)
        bob = next(r for r in reports if r.key == "rev:bob")
        assert bob.total == 1
        assert bob.override_rate == pytest.approx(1.0)


# ---------------------------------------------------------------------------
# compute_rates -- window filtering (inclusive start, exclusive end)
# ---------------------------------------------------------------------------


class TestWindowFiltering:
    _t1 = _T0
    _t2 = _T0 + timedelta(hours=1)
    _t3 = _T0 + timedelta(hours=2)
    _t4 = _T0 + timedelta(hours=3)

    def _observations(self) -> list[ReviewObservation]:
        return [
            _obs(ReviewOutcome.APPROVE, decided_at=self._t1),
            _obs(ReviewOutcome.REJECT, decided_at=self._t2),
            _obs(ReviewOutcome.MODIFY, decided_at=self._t3),
            _obs(ReviewOutcome.ESCALATE, decided_at=self._t4),
        ]

    def test_no_window_all_included(self) -> None:
        (r,) = compute_rates(self._observations(), group_by="all", min_sample=1)
        assert r.total == 4

    def test_window_start_inclusive(self) -> None:
        """Observation at exactly window_start IS included."""
        (r,) = compute_rates(
            self._observations(),
            group_by="all",
            min_sample=1,
            window_start=self._t2,
        )
        assert r.total == 3  # t2, t3, t4

    def test_window_end_exclusive(self) -> None:
        """Observation at exactly window_end is NOT included."""
        (r,) = compute_rates(
            self._observations(),
            group_by="all",
            min_sample=1,
            window_end=self._t3,
        )
        assert r.total == 2  # t1, t2 (t3 is excluded)

    def test_window_start_and_end(self) -> None:
        (r,) = compute_rates(
            self._observations(),
            group_by="all",
            min_sample=1,
            window_start=self._t2,
            window_end=self._t4,
        )
        assert r.total == 2  # t2, t3

    def test_window_excludes_all_returns_empty(self) -> None:
        future = self._t4 + timedelta(days=1)
        result = compute_rates(
            self._observations(),
            group_by="all",
            min_sample=1,
            window_start=future,
        )
        assert result == ()


# ---------------------------------------------------------------------------
# compute_rates -- invalid group_by
# ---------------------------------------------------------------------------


class TestInvalidGroupBy:
    def test_unknown_group_by_raises(self) -> None:
        with pytest.raises(ValueError, match="group_by"):
            compute_rates([], group_by="country", min_sample=1)


# ---------------------------------------------------------------------------
# compute_rates -- rates in [0.0, 1.0] and determinism
# ---------------------------------------------------------------------------


class TestRateProperties:
    def test_rates_in_bounds(self) -> None:
        observations = [
            _obs(o, reviewer_id=f"rev-{i}", decided_at=_T0)
            for i, o in enumerate(
                [ReviewOutcome.REJECT] * 5
                + [ReviewOutcome.MODIFY] * 3
                + [ReviewOutcome.ESCALATE] * 2
            )
        ]
        (r,) = compute_rates(observations, group_by="all", min_sample=1)
        assert 0.0 <= r.rejection_rate <= 1.0
        assert 0.0 <= r.override_rate <= 1.0
        assert 0.0 <= r.escalation_rate <= 1.0

    def test_deterministic_same_input_same_output(self) -> None:
        obs_list = [_obs(ReviewOutcome.APPROVE, decided_at=_T0)]
        r1 = compute_rates(obs_list, group_by="all", min_sample=1)
        r2 = compute_rates(obs_list, group_by="all", min_sample=1)
        assert r1 == r2

    def test_report_is_frozen(self) -> None:
        (r,) = compute_rates(
            [_obs(ReviewOutcome.APPROVE, decided_at=_T0)], group_by="all", min_sample=1
        )
        with pytest.raises((AttributeError, TypeError)):
            r.total = 99  # type: ignore[misc]

    def test_is_rate_report(self) -> None:
        (r,) = compute_rates(
            [_obs(ReviewOutcome.APPROVE, decided_at=_T0)], group_by="all", min_sample=1
        )
        assert isinstance(r, RateReport)

    def test_generator_accepted(self) -> None:
        gen = (_obs(ReviewOutcome.REJECT, decided_at=_T0) for _ in range(5))
        (r,) = compute_rates(gen, group_by="all", min_sample=1)
        assert r.total == 5
        assert r.rejection_rate == pytest.approx(1.0)
