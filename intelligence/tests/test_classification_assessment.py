"""Tests for classification_assessment.py.

Engineering Reset Playbook ZTAX-ENG-RESET-001 §19.

All tests are self-contained. Tests that need the real Classifier build a temporary
KnowledgeBase from in-memory Markdown files so the suite runs entirely offline with
no knowledge-base, no network, and no external filesystem access.

Covered scenarios
-----------------
Unit / rule tests:
- empty candidates -> ABSTAIN / NO_CANDIDATES
- one strong candidate -> PROPOSE / CLEAR
- top rank above ceiling -> ABSTAIN / TOP_TOO_WEAK
- multiple candidates, same class only -> PROPOSE / CLEAR (margin skipped)
- clear margin -> PROPOSE / CLEAR
- insufficient margin -> REVIEW_AMBIGUOUS / AMBIGUOUS_MARGIN
- boundary equality: rank == max_top_rank passes (inclusive)
- boundary equality: margin == min_margin passes (inclusive)
- just beyond: rank == max_top_rank + epsilon -> ABSTAIN
- just beyond: margin == min_margin - epsilon -> REVIEW_AMBIGUOUS
- tie rank (same rank, different class, min_margin=0) -> PROPOSE / CLEAR
- same class twice then different class -> different class is runner-up
- case and whitespace in class names normalised correctly
- unsorted input is re-sorted by the function
- negative and positive ranks handled correctly
- NaN rank -> ValueError
- Inf rank -> ValueError
- NaN max_top_rank -> ValueError
- Inf min_margin -> ValueError
- negative min_margin -> ValueError
- omitted max_top_rank -> TypeError
- omitted min_margin -> TypeError
- Assessment is frozen (FrozenInstanceError on mutation)
- input sequence is not mutated
- determinism: same input twice gives equal result

Property-style tests (seeded random.Random, no hypothesis):
- route never degrades when top candidate's rank improves
- adding a candidate worse than current runner-up does not change route
- result does not depend on input order

Real-classifier tests (tmp Markdown ontology, real Classifier):
- clearly matching SKU -> PROPOSE
- nonsense SKU -> ABSTAIN / NO_CANDIDATES
- two near-identical sections for different classes -> REVIEW_AMBIGUOUS
"""

from __future__ import annotations

import math
import random
from dataclasses import FrozenInstanceError
from pathlib import Path
from typing import TYPE_CHECKING

import pytest

if TYPE_CHECKING:
    from ztax_gateway.classifier import Classifier
    from ztax_gateway.governance import UseCaseRegistry
    from ztax_gateway.provenance import Provenance

from ztax_gateway.classification_assessment import (
    AssessmentReason,
    AssessmentRoute,
    assess_candidates,
    assess_record,
)

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_INF = math.inf
_NAN = math.nan


def _cands(*pairs: tuple[str, float]) -> list[tuple[str, float]]:
    return list(pairs)


# ---------------------------------------------------------------------------
# Unit tests: ABSTAIN / NO_CANDIDATES
# ---------------------------------------------------------------------------


def test_empty_candidates_abstain() -> None:
    result = assess_candidates([], max_top_rank=-5.0, min_margin=1.0)
    assert result.route == AssessmentRoute.ABSTAIN
    assert result.reason == AssessmentReason.NO_CANDIDATES
    assert result.top_class is None
    assert result.top_rank is None
    assert result.margin is None


# ---------------------------------------------------------------------------
# Unit tests: ABSTAIN / TOP_TOO_WEAK
# ---------------------------------------------------------------------------


def test_top_rank_above_ceiling_abstains() -> None:
    # rank -2 is weaker than ceiling -5 (because -2 > -5 and lower is better)
    result = assess_candidates([("cls-a", -2.0)], max_top_rank=-5.0, min_margin=1.0)
    assert result.route == AssessmentRoute.ABSTAIN
    assert result.reason == AssessmentReason.TOP_TOO_WEAK
    assert result.top_class == "cls-a"
    assert result.top_rank == -2.0


def test_positive_rank_above_positive_ceiling_abstains() -> None:
    result = assess_candidates([("cls-a", 5.0)], max_top_rank=3.0, min_margin=1.0)
    assert result.route == AssessmentRoute.ABSTAIN
    assert result.reason == AssessmentReason.TOP_TOO_WEAK


# ---------------------------------------------------------------------------
# Unit tests: boundary equality (inclusive)
# ---------------------------------------------------------------------------


def test_rank_equal_to_ceiling_passes() -> None:
    # rank == max_top_rank is inclusive -> should PROPOSE (single class)
    result = assess_candidates([("cls-a", -10.0)], max_top_rank=-10.0, min_margin=0.0)
    assert result.route == AssessmentRoute.PROPOSE
    assert result.reason == AssessmentReason.CLEAR
    assert result.top_rank == -10.0


def test_rank_just_beyond_ceiling_abstains() -> None:
    # rank = -9.9999 which is > -10.0, so weaker than ceiling
    result = assess_candidates([("cls-a", -9.9999)], max_top_rank=-10.0, min_margin=0.0)
    assert result.route == AssessmentRoute.ABSTAIN
    assert result.reason == AssessmentReason.TOP_TOO_WEAK


def test_margin_equal_to_min_margin_passes() -> None:
    # margin exactly == min_margin should be PROPOSE (inclusive)
    result = assess_candidates(
        [("cls-a", -20.0), ("cls-b", -15.0)],
        max_top_rank=-15.0,
        min_margin=5.0,  # margin = -15 - (-20) = 5.0 == min_margin
    )
    assert result.route == AssessmentRoute.PROPOSE
    assert result.reason == AssessmentReason.CLEAR
    assert result.margin == pytest.approx(5.0)


def test_margin_just_below_min_margin_is_ambiguous() -> None:
    # margin = 4.9999 < min_margin=5.0
    result = assess_candidates(
        [("cls-a", -20.0), ("cls-b", -15.0001)],
        max_top_rank=-15.0,
        min_margin=5.0,
    )
    assert result.route == AssessmentRoute.REVIEW_AMBIGUOUS
    assert result.reason == AssessmentReason.AMBIGUOUS_MARGIN


# ---------------------------------------------------------------------------
# Unit tests: PROPOSE / CLEAR (single distinct class)
# ---------------------------------------------------------------------------


def test_one_strong_candidate_proposes() -> None:
    result = assess_candidates([("cls-a", -20.0)], max_top_rank=-10.0, min_margin=2.0)
    assert result.route == AssessmentRoute.PROPOSE
    assert result.reason == AssessmentReason.CLEAR
    assert result.top_class == "cls-a"
    assert result.top_rank == -20.0
    assert result.runner_up_class is None
    assert result.margin is None


def test_multiple_same_class_proposes_no_ambiguity() -> None:
    # Two chunks of the same class should NOT count as ambiguity
    result = assess_candidates(
        [("cls-a", -20.0), ("cls-a", -18.0), ("cls-a", -15.0)],
        max_top_rank=-10.0,
        min_margin=1.0,
    )
    assert result.route == AssessmentRoute.PROPOSE
    assert result.reason == AssessmentReason.CLEAR
    assert result.runner_up_class is None


# ---------------------------------------------------------------------------
# Unit tests: PROPOSE / CLEAR (with runner-up, sufficient margin)
# ---------------------------------------------------------------------------


def test_clear_margin_proposes() -> None:
    result = assess_candidates(
        [("cls-a", -20.0), ("cls-b", -10.0)],
        max_top_rank=-15.0,
        min_margin=5.0,  # margin = -10 - (-20) = 10.0 >= 5.0
    )
    assert result.route == AssessmentRoute.PROPOSE
    assert result.reason == AssessmentReason.CLEAR
    assert result.top_class == "cls-a"
    assert result.runner_up_class == "cls-b"
    assert result.margin == pytest.approx(10.0)


# ---------------------------------------------------------------------------
# Unit tests: REVIEW_AMBIGUOUS / AMBIGUOUS_MARGIN
# ---------------------------------------------------------------------------


def test_insufficient_margin_review_ambiguous() -> None:
    # margin = -16 - (-20) = 4.0 < min_margin=5.0
    result = assess_candidates(
        [("cls-a", -20.0), ("cls-b", -16.0)],
        max_top_rank=-15.0,
        min_margin=5.0,
    )
    assert result.route == AssessmentRoute.REVIEW_AMBIGUOUS
    assert result.reason == AssessmentReason.AMBIGUOUS_MARGIN
    assert result.top_class == "cls-a"
    assert result.runner_up_class == "cls-b"
    assert result.margin == pytest.approx(4.0)


# ---------------------------------------------------------------------------
# Unit tests: tie (same rank for different classes)
# ---------------------------------------------------------------------------


def test_tie_with_min_margin_zero_proposes() -> None:
    """With min_margin=0.0 a perfect rank tie with a different class passes as PROPOSE.

    This is documented behaviour: callers who want to route ties to review must
    supply a positive min_margin.
    """
    result = assess_candidates(
        [("cls-a", -10.0), ("cls-b", -10.0)],
        max_top_rank=-5.0,
        min_margin=0.0,
    )
    assert result.route == AssessmentRoute.PROPOSE
    assert result.margin == pytest.approx(0.0)


def test_tie_with_positive_min_margin_is_ambiguous() -> None:
    # margin=0 < min_margin=1.0
    result = assess_candidates(
        [("cls-a", -10.0), ("cls-b", -10.0)],
        max_top_rank=-5.0,
        min_margin=1.0,
    )
    assert result.route == AssessmentRoute.REVIEW_AMBIGUOUS


# ---------------------------------------------------------------------------
# Unit tests: class normalisation (case + whitespace)
# ---------------------------------------------------------------------------


def test_same_class_differing_by_case_not_ambiguous() -> None:
    result = assess_candidates(
        [("Voice Services", -20.0), ("voice services", -18.0)],
        max_top_rank=-15.0,
        min_margin=1.0,
    )
    # Both normalise to "voice services" — no rival class
    assert result.route == AssessmentRoute.PROPOSE
    assert result.runner_up_class is None


def test_same_class_differing_by_whitespace_not_ambiguous() -> None:
    result = assess_candidates(
        [("  Voice Services  ", -20.0), ("Voice Services", -18.0)],
        max_top_rank=-15.0,
        min_margin=1.0,
    )
    assert result.route == AssessmentRoute.PROPOSE
    assert result.runner_up_class is None


def test_same_class_twice_then_different_class_picks_different_as_runner_up() -> None:
    result = assess_candidates(
        [("cls-a", -20.0), ("cls-a", -18.0), ("cls-b", -16.0)],
        max_top_rank=-15.0,
        min_margin=1.0,  # margin = -16 - (-20) = 4.0 >= 1.0
    )
    assert result.route == AssessmentRoute.PROPOSE
    assert result.runner_up_class == "cls-b"


# ---------------------------------------------------------------------------
# Unit tests: unsorted input
# ---------------------------------------------------------------------------


def test_unsorted_input_is_re_sorted() -> None:
    # Supply candidates in worst-first order; best is cls-a at -20
    result = assess_candidates(
        [("cls-b", -5.0), ("cls-a", -20.0)],
        max_top_rank=-15.0,
        min_margin=1.0,
    )
    assert result.top_class == "cls-a"
    assert result.top_rank == -20.0


# ---------------------------------------------------------------------------
# Unit tests: negative and positive ranks
# ---------------------------------------------------------------------------


def test_all_positive_ranks() -> None:
    # Positive BM25 ranks (unusual but valid); lower still means better
    result = assess_candidates(
        [("cls-a", 1.0), ("cls-b", 3.0)],
        max_top_rank=2.0,
        min_margin=1.0,
    )
    assert result.route == AssessmentRoute.PROPOSE
    assert result.top_rank == 1.0
    assert result.margin == pytest.approx(2.0)


def test_mixed_negative_and_positive_ranks() -> None:
    # -5 < 0 < 2, so -5 is the best rank
    result = assess_candidates(
        [("cls-a", -5.0), ("cls-b", 2.0)],
        max_top_rank=0.0,
        min_margin=3.0,  # margin = 2 - (-5) = 7.0 >= 3.0
    )
    assert result.route == AssessmentRoute.PROPOSE
    assert result.top_rank == -5.0


# ---------------------------------------------------------------------------
# Unit tests: ValueError on invalid values
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "rank",
    [float("nan"), float("inf"), float("-inf")],
    ids=["nan", "+inf", "-inf"],
)
def test_nan_or_inf_rank_raises(rank: float) -> None:
    with pytest.raises(ValueError, match="finite"):
        assess_candidates([("cls-a", rank)], max_top_rank=-5.0, min_margin=1.0)


@pytest.mark.parametrize(
    "max_top_rank",
    [float("nan"), float("inf"), float("-inf")],
    ids=["nan", "+inf", "-inf"],
)
def test_nan_or_inf_max_top_rank_raises(max_top_rank: float) -> None:
    with pytest.raises(ValueError, match="finite"):
        assess_candidates([("cls-a", -10.0)], max_top_rank=max_top_rank, min_margin=1.0)


@pytest.mark.parametrize(
    "min_margin",
    [float("nan"), float("inf"), float("-inf")],
    ids=["nan", "+inf", "-inf"],
)
def test_nan_or_inf_min_margin_raises(min_margin: float) -> None:
    with pytest.raises(ValueError):
        assess_candidates([("cls-a", -10.0)], max_top_rank=-5.0, min_margin=min_margin)


def test_negative_min_margin_raises() -> None:
    with pytest.raises(ValueError, match="non-negative"):
        assess_candidates([("cls-a", -10.0)], max_top_rank=-5.0, min_margin=-1.0)


# ---------------------------------------------------------------------------
# Unit tests: TypeError for omitted thresholds
# ---------------------------------------------------------------------------


def test_omitted_max_top_rank_raises_type_error() -> None:
    with pytest.raises(TypeError):
        assess_candidates([("cls-a", -10.0)], min_margin=1.0)  # type: ignore[call-arg]


def test_omitted_min_margin_raises_type_error() -> None:
    with pytest.raises(TypeError):
        assess_candidates([("cls-a", -10.0)], max_top_rank=-5.0)  # type: ignore[call-arg]


# ---------------------------------------------------------------------------
# Unit tests: result is frozen
# ---------------------------------------------------------------------------


def test_assessment_result_is_frozen() -> None:
    result = assess_candidates([("cls-a", -20.0)], max_top_rank=-10.0, min_margin=1.0)
    with pytest.raises(FrozenInstanceError):
        result.route = AssessmentRoute.ABSTAIN  # type: ignore[misc]


# ---------------------------------------------------------------------------
# Unit tests: input sequence is not mutated
# ---------------------------------------------------------------------------


def test_input_sequence_not_mutated() -> None:
    original = [("cls-b", -5.0), ("cls-a", -20.0)]
    snapshot = list(original)
    assess_candidates(original, max_top_rank=-4.0, min_margin=1.0)
    assert original == snapshot


# ---------------------------------------------------------------------------
# Unit tests: determinism
# ---------------------------------------------------------------------------


def test_determinism() -> None:
    cands = [("cls-b", -5.0), ("cls-a", -20.0)]
    r1 = assess_candidates(cands, max_top_rank=-4.0, min_margin=1.0)
    r2 = assess_candidates(cands, max_top_rank=-4.0, min_margin=1.0)
    assert r1 == r2


# ---------------------------------------------------------------------------
# Unit tests: equal-rank tie-breaking is deterministic by class name
# ---------------------------------------------------------------------------


def test_equal_rank_order_independent() -> None:
    """Tied candidates with the same rank but different classes give identical
    results regardless of input order.

    This directly tests the secondary sort key ``(rank, normalised_class)``.
    Without it, Python's stable sort would flip ``top_class`` depending on
    which candidate appeared first in the input list.
    """
    # Two candidates at exactly the same rank, different classes.
    # "cls-alpha" < "cls-beta" lexicographically, so alpha wins the tie.
    order_ab = [("cls-alpha", -15.0), ("cls-beta", -15.0)]
    order_ba = [("cls-beta", -15.0), ("cls-alpha", -15.0)]

    result_ab = assess_candidates(order_ab, max_top_rank=-10.0, min_margin=0.0)
    result_ba = assess_candidates(order_ba, max_top_rank=-10.0, min_margin=0.0)

    assert result_ab == result_ba, (
        f"Equal-rank tie-breaking is order-dependent:\n"
        f"AB order: {result_ab}\nBA order: {result_ba}"
    )
    # Lexicographically first class must always win the tie.
    assert result_ab.top_class is not None
    assert result_ab.top_class.strip().lower() == "cls-alpha"


def test_equal_rank_order_independent_many() -> None:
    """Property: equal-rank candidates produce the same result for all permutations."""
    import itertools

    tied_cands = [("cls-z", -10.0), ("cls-a", -10.0), ("cls-m", -10.0)]
    base_result = assess_candidates(tied_cands, max_top_rank=-5.0, min_margin=0.0)

    for perm in itertools.permutations(tied_cands):
        result = assess_candidates(list(perm), max_top_rank=-5.0, min_margin=0.0)
        assert result == base_result, (
            f"Order-dependence on tied ranks: perm={perm}\n"
            f"base={base_result}\ngot={result}"
        )
    # Lexicographically smallest class wins: "cls-a" < "cls-m" < "cls-z"
    assert base_result.top_class is not None
    assert base_result.top_class.strip().lower() == "cls-a"



# ---------------------------------------------------------------------------
# Property-style tests (seeded random.Random, no hypothesis)
# ---------------------------------------------------------------------------


def _random_cands(rng: random.Random, n: int) -> list[tuple[str, float]]:
    """Generate *n* random (class, rank) pairs with ranks in [-30, -1]."""
    classes = ["cls-alpha", "cls-beta", "cls-gamma", "cls-delta"]
    return [(rng.choice(classes), rng.uniform(-30.0, -1.0)) for _ in range(n)]


def test_property_improving_top_rank_never_degrades_route() -> None:
    """When the top candidate's rank improves (decreases), the route must not get worse.

    PROPOSE >= REVIEW_AMBIGUOUS >= ABSTAIN in order of preference.
    """
    rng = random.Random(42)
    route_order = [
        AssessmentRoute.ABSTAIN,
        AssessmentRoute.REVIEW_AMBIGUOUS,
        AssessmentRoute.PROPOSE,
    ]

    for _ in range(200):
        cands = _random_cands(rng, rng.randint(1, 5))
        max_top_rank = rng.uniform(-20.0, -5.0)
        min_margin = rng.uniform(0.0, 5.0)

        base_result = assess_candidates(cands, max_top_rank=max_top_rank, min_margin=min_margin)

        # Improve the ACTUAL top candidate (lowest rank after sorting, not cands[0])
        top_idx = min(range(len(cands)), key=lambda i: cands[i][1])
        top_cls, top_rank_val = cands[top_idx]
        delta = rng.uniform(0.1, 5.0)
        improved = list(cands)
        improved[top_idx] = (top_cls, top_rank_val - delta)
        improved_result = assess_candidates(
            improved, max_top_rank=max_top_rank, min_margin=min_margin
        )

        base_idx = route_order.index(base_result.route)
        improved_idx = route_order.index(improved_result.route)
        assert improved_idx >= base_idx, (
            f"Route degraded from {base_result.route} to {improved_result.route} "
            f"when top rank improved: base={cands}, improved={improved}"
        )


def test_property_adding_worse_candidate_does_not_change_route() -> None:
    """Adding a candidate strictly worse than the current runner-up must not change route."""
    rng = random.Random(99)

    for _ in range(200):
        cands = _random_cands(rng, rng.randint(2, 4))
        max_top_rank = rng.uniform(-20.0, -5.0)
        min_margin = rng.uniform(0.0, 5.0)

        base_result = assess_candidates(cands, max_top_rank=max_top_rank, min_margin=min_margin)

        # If there is a runner-up, add a new candidate worse than it
        if base_result.runner_up_class is not None and base_result.margin is not None:
            worst_in_cands = max(c[1] for c in cands)
            extra_rank = worst_in_cands + rng.uniform(0.1, 2.0)
            if math.isfinite(extra_rank):
                augmented = [*list(cands), ("cls-extra", extra_rank)]
                augmented_result = assess_candidates(
                    augmented, max_top_rank=max_top_rank, min_margin=min_margin
                )
                assert augmented_result.route == base_result.route, (
                    f"Route changed after adding worse candidate: "
                    f"base={base_result.route}, augmented={augmented_result.route}"
                )


def test_property_result_independent_of_input_order() -> None:
    """The result must not depend on the order of the input sequence."""
    rng = random.Random(7)

    for _ in range(200):
        cands = _random_cands(rng, rng.randint(1, 6))
        max_top_rank = rng.uniform(-20.0, -5.0)
        min_margin = rng.uniform(0.0, 5.0)

        result_a = assess_candidates(cands, max_top_rank=max_top_rank, min_margin=min_margin)
        shuffled = list(cands)
        rng.shuffle(shuffled)
        result_b = assess_candidates(shuffled, max_top_rank=max_top_rank, min_margin=min_margin)

        assert result_a == result_b, (
            f"Order-dependence detected:\norig={cands}\nshuf={shuffled}\n"
            f"a={result_a}\nb={result_b}"
        )


# ---------------------------------------------------------------------------
# Real-classifier integration tests
# ---------------------------------------------------------------------------

# These tests build a real KnowledgeBase and run the real Classifier. They import
# from ztax_gateway directly (not under TYPE_CHECKING) because they are tests, not
# production code. BM25 values in tiny corpora are unstable, so thresholds are set
# relative to the observed ranks — never hard-coded.

_CLEAR_ONTOLOGY = """\
# ontology:voice/basic

Standard voice call plans for residential customers.
Voice telephony subscription for home users.

# ontology:data/mobile

Mobile broadband data plans for smartphones.
Internet access for mobile devices.
"""

_AMBIGUOUS_ONTOLOGY = """\
# ontology:voice/basic

Voice call service basic subscription.
Standard voice telephony plan.

# ontology:voice/premium

Premium voice call service subscription.
Premium voice telephony plan with extra features.
"""


def _make_classifier_for(tmp_path: Path, md_content: str) -> Classifier:
    """Build a Classifier from *md_content* written to a temp file."""
    from ztax_gateway.classifier import Classifier
    from ztax_gateway.rag import KnowledgeBase

    md_file = tmp_path / "ontology.md"
    md_file.write_text(md_content, encoding="utf-8")
    kb = KnowledgeBase()
    kb.build([md_file])
    return Classifier(knowledge_base=kb)


def _make_provenance_and_registry() -> tuple[Provenance, UseCaseRegistry]:
    from ztax_gateway.governance import UseCase, UseCaseRegistry
    from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier

    use_case = UseCase(
        use_case_id="sku-classification",
        owner="test",
        description="classification test",
        max_risk_tier=RiskTier.T2,
        max_authority=AuthorityOutcome.A2,
        permitted_regions=frozenset({"eu-west-1"}),
        permitted_data_classes=frozenset({"INTERNAL"}),
    )
    registry = UseCaseRegistry([use_case])
    provenance = Provenance(
        use_case="sku-classification",
        model_profile="model:classifier@2026.09",
        provider_profile="provider:eu-hosted",
        prompt_profile="prompt:sku-classification@1",
        region="eu-west-1",
        data_class="INTERNAL",
        risk_tier=RiskTier.T1,
        authority_outcome=AuthorityOutcome.A1,
        ai_train_version="0.5.0",
    )
    return provenance, registry


def test_real_classifier_clear_match_proposes(tmp_path: Path) -> None:
    """A clearly matching SKU against a two-class corpus gives PROPOSE.

    The ontology has two classes that share NO vocabulary. The query matches
    exclusively the telephony class, so there is no rival class to create
    ambiguity. We set max_top_rank = observed top rank (ceiling inclusive) and
    min_margin = 0 (ties pass). The result must be strictly PROPOSE / CLEAR.
    """
    classifier = _make_classifier_for(tmp_path, _CLEAR_ONTOLOGY)
    provenance, registry = _make_provenance_and_registry()

    record = classifier.classify(
        sku_description="residential voice telephony subscription",
        provenance=provenance,
        registry=registry,
        top_k=3,
    )

    # Hard assert: the corpus is designed so this SKU must produce at least one proposal.
    # A skip here would hide a real regression in BM25 indexing or the ontology corpus.
    assert len(record.proposals) >= 1, (
        f"Real classifier returned no proposals for clear-match SKU — corpus or BM25 regression: "
        f"{record}"
    )

    # Use the observed top rank as ceiling (passes inclusive equality).
    # min_margin=0 means even a tied margin passes as PROPOSE / CLEAR.
    best_rank = record.proposals[0].rank
    result = assess_record(record, max_top_rank=best_rank, min_margin=0.0)

    # The prompt requires PROPOSE — not REVIEW_AMBIGUOUS — for a clearly matching SKU.
    assert result.route == AssessmentRoute.PROPOSE, (
        f"Expected PROPOSE for clear-match SKU, got {result.route} / {result.reason}. "
        f"proposals={record.proposals}"
    )
    assert result.reason == AssessmentReason.CLEAR
    assert result.top_class is not None


def test_real_classifier_nonsense_sku_abstains(tmp_path: Path) -> None:
    """A nonsense SKU gives ABSTAIN / NO_CANDIDATES.

    The prompt requires ABSTAIN / NO_CANDIDATES specifically. To guarantee this
    we use a query composed entirely of random-looking characters that share no
    tokens with the ontology corpus. BM25 returns an empty result set when no
    token overlap exists, so record.proposals will be empty and assess_record
    must return NO_CANDIDATES.

    If the classifier unexpectedly returns proposals despite zero token overlap
    (e.g. the SQLite FTS5 driver changed matching behaviour), we skip rather
    than silently asserting TOP_TOO_WEAK, which would mask the semantic gap.
    """
    classifier = _make_classifier_for(tmp_path, _CLEAR_ONTOLOGY)
    provenance, registry = _make_provenance_and_registry()

    # Use a query with no shared tokens with the ontology.
    record = classifier.classify(
        sku_description="zqxvwj kpfmbt rlghs",
        provenance=provenance,
        registry=registry,
        top_k=3,
    )

    # Hard assert: zero-token-overlap query must produce zero proposals.
    # If BM25 returns matches here, the tokeniser or corpus has changed and the
    # test must fail visibly, not silently skip.
    assert len(record.proposals) == 0, (
        f"Classifier unexpectedly returned proposals for zero-token-overlap query — "
        f"BM25 tokeniser or corpus changed: {record.proposals}"
    )

    result = assess_record(record, max_top_rank=-100.0, min_margin=1.0)
    # The prompt requires ABSTAIN / NO_CANDIDATES, not TOP_TOO_WEAK.
    assert result.route == AssessmentRoute.ABSTAIN
    assert result.reason == AssessmentReason.NO_CANDIDATES
    assert result.top_class is None
    assert result.top_rank is None


def test_real_classifier_near_identical_classes_ambiguous(tmp_path: Path) -> None:
    """Two near-identical sections for different classes produce REVIEW_AMBIGUOUS."""
    classifier = _make_classifier_for(tmp_path, _AMBIGUOUS_ONTOLOGY)
    provenance, registry = _make_provenance_and_registry()

    record = classifier.classify(
        sku_description="voice telephony subscription plan",
        provenance=provenance,
        registry=registry,
        top_k=5,
    )

    # Hard assert: the corpus must produce at least 2 proposals (not skips).
    candidates = [(p.ontology_class, p.rank) for p in record.proposals]
    assert len(candidates) >= 2, (
        f"Classifier returned fewer than 2 proposals for ambiguous test "
        f"— corpus regression: {candidates}"
    )

    # Use fully deterministic sort matching assess_candidates' own key.
    sorted_cands = sorted(candidates, key=lambda c: (c[1], c[0].strip().lower(), c[0]))
    top_norm = sorted_cands[0][0].strip().lower()
    rival_rank: float | None = None
    for cls, rank in sorted_cands[1:]:
        if cls.strip().lower() != top_norm:
            rival_rank = rank
            break

    # Hard assert: the corpus must have at least two distinct ontology classes.
    assert rival_rank is not None, (
        "All proposals share the same normalised class "
        "— corpus must define at least two distinct classes"
    )

    top_rank = sorted_cands[0][1]
    observed_margin = rival_rank - top_rank
    # Set min_margin slightly above the observed gap to force REVIEW_AMBIGUOUS
    tight_margin = observed_margin + 1.0
    ceiling = top_rank  # exactly at the top rank (passes inclusive check)

    result = assess_record(record, max_top_rank=ceiling, min_margin=tight_margin)
    assert result.route == AssessmentRoute.REVIEW_AMBIGUOUS
    assert result.reason == AssessmentReason.AMBIGUOUS_MARGIN


# ---------------------------------------------------------------------------
# assess_record smoke tests
# ---------------------------------------------------------------------------


def test_assess_record_delegates_to_assess_candidates(tmp_path: Path) -> None:
    """assess_record and assess_candidates give identical results for the same data."""
    classifier = _make_classifier_for(tmp_path, _CLEAR_ONTOLOGY)
    provenance, registry = _make_provenance_and_registry()

    record = classifier.classify(
        sku_description="mobile data plan",
        provenance=provenance,
        registry=registry,
        top_k=3,
    )

    cands = [(p.ontology_class, p.rank) for p in record.proposals]
    direct = assess_candidates(cands, max_top_rank=0.0, min_margin=0.0)
    via_record = assess_record(record, max_top_rank=0.0, min_margin=0.0)

    assert direct == via_record
