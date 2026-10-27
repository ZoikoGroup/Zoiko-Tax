"""Classification Assessment — BM25-rank-based routing for classification proposals.

Engineering Reset Playbook ZTAX-ENG-RESET-001 §19: new ambiguous mappings enter
human review. This module decides, from a classification result, whether a proposal
may be shown to a reviewer as a suggestion (PROPOSE) or whether the item must be
routed to human review (REVIEW_AMBIGUOUS or ABSTAIN).

It is **unwired and advisory only**. PROPOSE means "show as a suggestion" (authority
A1). This module NEVER accepts, adopts, promotes, or makes a class authoritative
automatically.

Design rules
------------
* Standard library only at runtime. ``ClassificationRecord`` is imported under
  ``TYPE_CHECKING`` only so this module carries no dependency on the classifier at
  import time.
* No threshold is chosen here. ``max_top_rank`` and ``min_margin`` are required
  keyword-only parameters with no defaults. Callers must supply explicit values so
  that every call site is auditable and no hidden "magic number" is embedded in a
  default. BM25 ranks are raw SQLite FTS5 scores, not probabilities; they are
  corpus-dependent and must not be treated as portable confidence values. Thresholds
  must come from evaluation on a reviewed gold set. No such gold set exists yet.
* Frozen dataclasses and StrEnum only; deterministic; no I/O, clock, randomness or
  logging.
* Both non-PROPOSE routes mean "human review required". The mapping of these routes
  to any HTTP status code, wire reason field, or Go review type is the caller's job.
* Do not cite requirement IDs and do not claim anything is closed.

Score semantics
---------------
``rank`` is the raw SQLite FTS5 BM25 score. **LOWER IS BETTER** (scores are
typically negative; -30 beats -10). This module sorts candidates by rank ascending
(best first) regardless of the order supplied by the caller.

Runner-up
---------
The runner-up is the highest-ranked candidate whose ontology class differs from the
top class, compared after ``strip().lower()`` normalisation — the same normalisation
MetricEngine applies. Multiple chunks with the same normalised class do not constitute
ambiguity; only a genuinely different class does.

Margin
------
``margin = runner_up_rank - top_rank``. Because lower rank is better, the top rank is
the smaller (more negative) number, and the margin is non-negative when a distinct
runner-up exists. With ``min_margin == 0`` a perfect tie passes as PROPOSE / CLEAR
(equal class support); callers who want to route ties to review must supply a
positive ``min_margin``.
"""

from __future__ import annotations

import math
from collections.abc import Sequence
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from .classifier import ClassificationRecord

__all__: list[str] = [
    "Assessment",
    "AssessmentReason",
    "AssessmentRoute",
    "assess_candidates",
    "assess_record",
]


# ---------------------------------------------------------------------------
# Public types
# ---------------------------------------------------------------------------


class AssessmentRoute(StrEnum):
    """The routing decision produced by :func:`assess_candidates`.

    PROPOSE
        The top candidate's rank meets the ceiling threshold and its margin over
        the best rival class is at least ``min_margin``. The caller may present
        the top class as a suggestion to a human reviewer (authority A1 only).

    REVIEW_AMBIGUOUS
        The top candidate meets the ceiling but the margin over the runner-up
        class is too small. A human reviewer must choose between the competing
        classes before any proposal is forwarded.

    ABSTAIN
        There are no candidates, or the top candidate's rank is above
        ``max_top_rank`` (weaker than the allowed ceiling). The assessor cannot
        produce a reliable signal.
    """

    PROPOSE = "PROPOSE"
    REVIEW_AMBIGUOUS = "REVIEW_AMBIGUOUS"
    ABSTAIN = "ABSTAIN"


class AssessmentReason(StrEnum):
    """Detailed reason accompanying an :class:`AssessmentRoute`.

    CLEAR
        The top candidate's rank and margin both satisfy the supplied thresholds.
        Used with ``PROPOSE``.

    NO_CANDIDATES
        The candidate sequence was empty.
        Used with ``ABSTAIN``.

    TOP_TOO_WEAK
        The top candidate's rank exceeds ``max_top_rank`` (it is weaker than the
        ceiling). Used with ``ABSTAIN``.

    AMBIGUOUS_MARGIN
        The top candidate's rank meets the ceiling but the margin over the
        runner-up class is below ``min_margin``. Used with ``REVIEW_AMBIGUOUS``.
    """

    CLEAR = "CLEAR"
    NO_CANDIDATES = "NO_CANDIDATES"
    TOP_TOO_WEAK = "TOP_TOO_WEAK"
    AMBIGUOUS_MARGIN = "AMBIGUOUS_MARGIN"


@dataclass(frozen=True, slots=True)
class Assessment:
    """Immutable routing verdict returned by :func:`assess_candidates`.

    ``route`` and ``reason`` are always set.

    ``top_class`` — the ontology class of the highest-ranked candidate; ``None``
    only when there are no candidates (``route`` is ABSTAIN / NO_CANDIDATES).

    ``runner_up_class`` — the ontology class of the highest-ranked candidate
    from a *different* class than ``top_class``; ``None`` when no such rival
    exists or when ABSTAIN was returned before the runner-up search.

    ``top_rank`` — raw BM25 rank of the top candidate; ``None`` only when there
    are no candidates.

    ``margin`` — ``runner_up_rank - top_rank``; ``None`` when there is no
    runner-up or when ABSTAIN was returned before the runner-up search.
    """

    route: AssessmentRoute
    reason: AssessmentReason
    top_class: str | None = None
    runner_up_class: str | None = None
    top_rank: float | None = None
    margin: float | None = None


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------


def _validate_rank(value: float, label: str) -> None:
    """Raise ValueError if *value* is NaN or infinite."""
    if not math.isfinite(value):
        raise ValueError(
            f"assess_candidates: {label} must be a finite number, got {value!r}"
        )


def _validate_threshold(name: str, value: float) -> None:
    """Raise ValueError if the threshold is NaN, infinite, or (for min_margin) negative."""
    if not math.isfinite(value):
        raise ValueError(
            f"assess_candidates: {name!r} must be finite, got {value!r}"
        )


# ---------------------------------------------------------------------------
# Public functions
# ---------------------------------------------------------------------------


def assess_candidates(
    candidates: Sequence[tuple[str, float]],
    *,
    max_top_rank: float,
    min_margin: float,
) -> Assessment:
    """Route a classification based on BM25-ranked candidates.

    Parameters
    ----------
    candidates:
        A sequence of ``(ontology_class, rank)`` pairs. The function re-sorts
        them by rank ascending (lower is better, stable sort); the caller's order
        is not trusted. The original sequence is never mutated.
    max_top_rank:
        The weakest acceptable top-candidate rank (inclusive). A top candidate
        with ``rank > max_top_rank`` is too weak to propose. Must be finite.
    min_margin:
        Minimum acceptable gap between the top rank and the best rival-class rank
        (inclusive; ``margin == min_margin`` passes). Must be finite and
        non-negative. With ``min_margin == 0`` a perfect tie with a different
        class still passes as PROPOSE / CLEAR.

    Returns
    -------
    Assessment
        Immutable routing verdict.

    Raises
    ------
    TypeError
        If either threshold is omitted (they have no defaults).
    ValueError
        If any rank, ``max_top_rank``, or ``min_margin`` is NaN or infinite, or
        if ``min_margin`` is negative.
    """
    # -- validate thresholds --------------------------------------------------
    _validate_threshold("max_top_rank", max_top_rank)
    _validate_threshold("min_margin", min_margin)
    if min_margin < 0:
        raise ValueError(
            f"assess_candidates: 'min_margin' must be non-negative, got {min_margin!r}"
        )

    # -- validate all ranks in the candidate list ------------------------------
    for cls, rank in candidates:
        _validate_rank(rank, f"rank for class {cls!r}")

    # -- empty candidates -> ABSTAIN / NO_CANDIDATES --------------------------
    if not candidates:
        return Assessment(
            route=AssessmentRoute.ABSTAIN,
            reason=AssessmentReason.NO_CANDIDATES,
        )

    # -- sort by rank ascending (lower is better); break ties by normalised class
    # name so the result is fully deterministic regardless of input order.
    sorted_candidates: list[tuple[str, float]] = sorted(
        candidates, key=lambda c: (c[1], c[0].strip().lower())
    )

    top_class, top_rank = sorted_candidates[0]
    top_class_normalised = top_class.strip().lower()

    # -- ceiling check: top rank must be <= max_top_rank ----------------------
    if top_rank > max_top_rank:
        return Assessment(
            route=AssessmentRoute.ABSTAIN,
            reason=AssessmentReason.TOP_TOO_WEAK,
            top_class=top_class,
            top_rank=top_rank,
        )

    # -- find runner-up: best rank whose class differs (by strip().lower()) ---
    runner_up_class: str | None = None
    runner_up_rank: float | None = None
    for cls, rank in sorted_candidates[1:]:
        if cls.strip().lower() != top_class_normalised:
            runner_up_class = cls
            runner_up_rank = rank
            break

    # -- single distinct class -> PROPOSE / CLEAR (no ambiguity) -------------
    if runner_up_class is None:
        return Assessment(
            route=AssessmentRoute.PROPOSE,
            reason=AssessmentReason.CLEAR,
            top_class=top_class,
            top_rank=top_rank,
        )

    # -- margin check ---------------------------------------------------------
    assert runner_up_rank is not None  # set together with runner_up_class above
    margin = runner_up_rank - top_rank

    if margin < min_margin:
        return Assessment(
            route=AssessmentRoute.REVIEW_AMBIGUOUS,
            reason=AssessmentReason.AMBIGUOUS_MARGIN,
            top_class=top_class,
            runner_up_class=runner_up_class,
            top_rank=top_rank,
            margin=margin,
        )

    # -- margin satisfied -> PROPOSE / CLEAR -----------------------------------
    return Assessment(
        route=AssessmentRoute.PROPOSE,
        reason=AssessmentReason.CLEAR,
        top_class=top_class,
        runner_up_class=runner_up_class,
        top_rank=top_rank,
        margin=margin,
    )


def assess_record(
    record: ClassificationRecord,
    *,
    max_top_rank: float,
    min_margin: float,
) -> Assessment:
    """Thin adapter: route a :class:`~ztax_gateway.classifier.ClassificationRecord`.

    Extracts ``(p.ontology_class, p.rank)`` from each proposal in
    ``record.proposals`` and delegates to :func:`assess_candidates`. The record
    and its proposals are not mutated.

    Parameters
    ----------
    record:
        The full classification record produced by the Classifier. Not mutated.
    max_top_rank:
        Passed directly to :func:`assess_candidates`.
    min_margin:
        Passed directly to :func:`assess_candidates`.

    Returns
    -------
    Assessment
        See :func:`assess_candidates`.
    """
    candidates = [(p.ontology_class, p.rank) for p in record.proposals]
    return assess_candidates(candidates, max_top_rank=max_top_rank, min_margin=min_margin)
