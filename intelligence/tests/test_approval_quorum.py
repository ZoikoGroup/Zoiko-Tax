"""Tests for approval_quorum -- ZTAX-AIGOV-001 / REQ-0077.

All tests are pure: no I/O, no clock, no external fixtures.
"""

from __future__ import annotations

import random

import pytest

from ztax_gateway.approval_quorum import (
    ActorKind,
    Approval,
    IgnoredReason,
    QuorumResult,
    evaluate_quorum,
)

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def human(actor_id: str) -> Approval:
    return Approval(actor_id=actor_id, kind=ActorKind.HUMAN)


def ai_actor(actor_id: str) -> Approval:
    return Approval(actor_id=actor_id, kind=ActorKind.AI)


def service(actor_id: str) -> Approval:
    return Approval(actor_id=actor_id, kind=ActorKind.SERVICE)


# ---------------------------------------------------------------------------
# ActorKind
# ---------------------------------------------------------------------------


class TestActorKind:
    def test_values(self) -> None:
        assert ActorKind.HUMAN == "HUMAN"
        assert ActorKind.AI == "AI"
        assert ActorKind.SERVICE == "SERVICE"

    def test_is_str(self) -> None:
        assert isinstance(ActorKind.HUMAN, str)


# ---------------------------------------------------------------------------
# IgnoredReason
# ---------------------------------------------------------------------------


class TestIgnoredReason:
    def test_values(self) -> None:
        assert IgnoredReason.AI_ACTOR == "AI_ACTOR"
        assert IgnoredReason.SERVICE_ACTOR == "SERVICE_ACTOR"
        assert IgnoredReason.DUPLICATE_HUMAN == "DUPLICATE_HUMAN"

    def test_is_str(self) -> None:
        assert isinstance(IgnoredReason.AI_ACTOR, str)


# ---------------------------------------------------------------------------
# Approval construction and validation
# ---------------------------------------------------------------------------


class TestApproval:
    def test_valid_human(self) -> None:
        a = human("alice")
        assert a.actor_id == "alice"
        assert a.kind is ActorKind.HUMAN

    def test_frozen(self) -> None:
        a = human("alice")
        with pytest.raises((AttributeError, TypeError)):
            a.actor_id = "bob"  # type: ignore[misc]

    def test_empty_actor_id_raises(self) -> None:
        with pytest.raises(ValueError, match="actor_id"):
            Approval(actor_id="", kind=ActorKind.HUMAN)

    def test_whitespace_only_actor_id_raises(self) -> None:
        with pytest.raises(ValueError, match="actor_id"):
            Approval(actor_id="   ", kind=ActorKind.HUMAN)

    def test_whitespace_only_tab_raises(self) -> None:
        with pytest.raises(ValueError, match="actor_id"):
            Approval(actor_id="\t\n", kind=ActorKind.HUMAN)


# ---------------------------------------------------------------------------
# evaluate_quorum -- required validation
# ---------------------------------------------------------------------------


class TestRequiredValidation:
    def test_required_zero_raises(self) -> None:
        with pytest.raises(ValueError, match="required"):
            evaluate_quorum([], required=0)

    def test_required_negative_raises(self) -> None:
        with pytest.raises(ValueError, match="required"):
            evaluate_quorum([], required=-1)

    def test_required_one_accepted(self) -> None:
        result = evaluate_quorum([human("alice")], required=1)
        assert result.met is True


# ---------------------------------------------------------------------------
# evaluate_quorum -- core quorum rules (REQ-0077)
# ---------------------------------------------------------------------------


class TestCoreQuorumRules:
    def test_two_distinct_humans_met(self) -> None:
        """Two distinct humans satisfies the default threshold of 2."""
        result = evaluate_quorum([human("alice"), human("bob")])
        assert result.met is True
        assert result.counted == ("alice", "bob")
        assert result.ignored == ()

    def test_one_human_plus_ai_not_met(self) -> None:
        """AI cannot make up the second required approver."""
        result = evaluate_quorum([human("alice"), ai_actor("ai-1")])
        assert result.met is False
        assert result.counted == ("alice",)
        assert ("ai-1", IgnoredReason.AI_ACTOR) in result.ignored

    def test_two_ais_not_met(self) -> None:
        result = evaluate_quorum([ai_actor("ai-1"), ai_actor("ai-2")])
        assert result.met is False
        assert result.counted == ()
        assert result.human_count == 0 if hasattr(result, "human_count") else True

    def test_one_human_twice_not_met(self) -> None:
        """The same human voting twice counts as one approval."""
        result = evaluate_quorum([human("alice"), human("alice")])
        assert result.met is False
        assert result.counted == ("alice",)
        assert ("alice", IgnoredReason.DUPLICATE_HUMAN) in result.ignored

    def test_human_plus_service_not_met(self) -> None:
        """SERVICE actor cannot supply the second approval."""
        result = evaluate_quorum([human("alice"), service("svc-1")])
        assert result.met is False
        assert result.counted == ("alice",)
        assert ("svc-1", IgnoredReason.SERVICE_ACTOR) in result.ignored

    def test_empty_approvals_not_met(self) -> None:
        result = evaluate_quorum([])
        assert result.met is False
        assert result.counted == ()
        assert result.ignored == ()

    def test_result_is_frozen(self) -> None:
        result = evaluate_quorum([human("alice")])
        with pytest.raises((AttributeError, TypeError)):
            result.met = True  # type: ignore[misc]

    def test_result_type(self) -> None:
        assert isinstance(evaluate_quorum([]), QuorumResult)

    def test_required_field_recorded(self) -> None:
        result = evaluate_quorum([human("alice"), human("bob")], required=3)
        assert result.required == 3
        assert result.met is False


# ---------------------------------------------------------------------------
# Mixed-case / whitespace duplicate detection
# ---------------------------------------------------------------------------


class TestCaseFoldNormalisation:
    def test_mixed_case_duplicates_collapse(self) -> None:
        """'Alice' and 'alice' are the same human after casefold."""
        result = evaluate_quorum([human("Alice"), human("alice")])
        assert result.met is False
        assert len(result.counted) == 1
        assert ("alice", IgnoredReason.DUPLICATE_HUMAN) in result.ignored

    def test_leading_trailing_whitespace_duplicates_collapse(self) -> None:
        """'  bob  ' and 'bob' are the same after strip + casefold."""
        result = evaluate_quorum([human("  bob  "), human("bob")])
        assert result.met is False
        assert len(result.counted) == 1

    def test_three_distinct_humans_counted(self) -> None:
        result = evaluate_quorum([human("alice"), human("Bob"), human("carol")], required=3)
        assert result.met is True
        assert len(result.counted) == 3


# ---------------------------------------------------------------------------
# ignored reasons are correct and sorted
# ---------------------------------------------------------------------------


class TestIgnoredReasonsSorted:
    def test_ai_reason_correct(self) -> None:
        result = evaluate_quorum([ai_actor("ai-x")])
        assert result.ignored == (("ai-x", IgnoredReason.AI_ACTOR),)

    def test_service_reason_correct(self) -> None:
        result = evaluate_quorum([service("svc-x")])
        assert result.ignored == (("svc-x", IgnoredReason.SERVICE_ACTOR),)

    def test_duplicate_human_reason_correct(self) -> None:
        result = evaluate_quorum([human("alice"), human("alice")])
        assert ("alice", IgnoredReason.DUPLICATE_HUMAN) in result.ignored

    def test_ignored_is_sorted(self) -> None:
        """ignored tuple must be in ascending (actor_id, reason) order."""
        approvals = [
            ai_actor("z-ai"),
            human("alice"),
            service("a-svc"),
            human("alice"),  # duplicate
            ai_actor("b-ai"),
        ]
        result = evaluate_quorum(approvals)
        ids = [entry[0] for entry in result.ignored]
        assert ids == sorted(ids)

    def test_counted_is_sorted(self) -> None:
        approvals = [human("zara"), human("alice"), human("mike")]
        result = evaluate_quorum(approvals, required=3)
        assert result.counted == tuple(sorted(result.counted))


# ---------------------------------------------------------------------------
# Determinism
# ---------------------------------------------------------------------------


class TestDeterminism:
    def test_same_input_same_output(self) -> None:
        approvals = [human("alice"), ai_actor("ai-1"), human("bob"), service("svc-1")]
        r1 = evaluate_quorum(approvals)
        r2 = evaluate_quorum(approvals)
        assert r1 == r2

    def test_generator_input_accepted(self) -> None:
        gen = (human(f"user-{i}") for i in range(3))
        result = evaluate_quorum(gen, required=3)
        assert result.met is True


# ---------------------------------------------------------------------------
# Property-style test (seeded random.Random)
# ---------------------------------------------------------------------------


class TestAddingNonHumanNeverChangesResult:
    """Adding AI or SERVICE approvals must never change ``met`` or ``counted``."""

    def test_seeded_loop(self) -> None:
        rng = random.Random(42)
        base = [human("alice"), human("bob")]
        base_result = evaluate_quorum(base)

        non_human_kinds = [ActorKind.AI, ActorKind.SERVICE]
        for _ in range(100):
            extra_count = rng.randint(1, 8)
            extra: list[Approval] = []
            for _ in range(extra_count):
                kind = rng.choice(non_human_kinds)
                actor_id = f"actor-{rng.randint(0, 999)}"
                extra.append(Approval(actor_id=actor_id, kind=kind))

            # Insert extras at a random position.
            pos = rng.randint(0, len(base))
            combined = base[:pos] + extra + base[pos:]
            result = evaluate_quorum(combined)
            assert result.met == base_result.met, (
                f"met changed after inserting non-human actors: {extra}"
            )
            assert result.counted == base_result.counted, (
                f"counted changed after inserting non-human actors: {extra}"
            )
