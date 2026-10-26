"""Review Metrics -- Human Override / Rejection Rate Monitor.

ZTAX-AIGOV-001 / REQ-0076 -- ``Human reviewer override/rejection rates MUST be
monitorable.``

This module is **unwired and advisory only**.  It provides the data structures
and pure functions needed to compute rejection, override, and escalation rates
from a sequence of :class:`ReviewObservation` records so that an external
metrics layer can expose them.

Structure
---------
:class:`ReviewObservation`
    Frozen.  One human-review data point: ``reviewer_id``, ``use_case``,
    ``outcome`` (:class:`~ztax_gateway.human_review.ReviewOutcome`), and
    ``decided_at`` (must be timezone-aware).

:func:`observation_from_decision`
    Builds a :class:`ReviewObservation` from a real
    :class:`~ztax_gateway.human_review.ReviewDecision` (+ the use-case string
    that is not carried by the decision itself).

:class:`RateReport`
    Frozen.  Computed statistics for one cohort: ``key`` (the group value),
    counts by outcome, three rates in [0.0, 1.0], and ``insufficient_sample``
    when ``total < min_sample``.

:func:`compute_rates`
    Pure function.  Groups observations by ``group_by`` (``"all"``,
    ``"use_case"``, or ``"reviewer"``), applies an optional ``[window_start,
    window_end)`` filter, and returns a sorted ``tuple[RateReport, ...]``.
    ``min_sample`` is a **required** keyword argument; this module never
    chooses a threshold (REQ-0083 thresholds are a later decision).

Known limits (REQ-0076 is not closed)
--------------------------------------
``compute_rates`` is a batch aggregation function.  Nothing schedules it,
nothing exports its results to a dashboard or alert surface, and nothing feeds
live :class:`ReviewObservation` records into it.  REQ-0076 is not satisfied
end-to-end until an observability layer does that wiring.
"""

from __future__ import annotations

from collections.abc import Iterable
from dataclasses import dataclass
from datetime import datetime
from typing import TYPE_CHECKING

from .human_review import ReviewOutcome

if TYPE_CHECKING:
    from .human_review import ReviewDecision


@dataclass(frozen=True, slots=True)
class ReviewObservation:
    """One human-review data point.

    Attributes
    ----------
    reviewer_id:
        Opaque identifier of the human reviewer.  Must be non-empty.
    use_case:
        The use-case string under which the AI output was reviewed.  Must be
        non-empty.
    outcome:
        The :class:`~ztax_gateway.human_review.ReviewOutcome` the reviewer
        chose (``APPROVE``, ``REJECT``, ``ESCALATE``, or ``MODIFY``).
    decided_at:
        UTC timestamp of the decision.  **Must be timezone-aware.**  A naive
        datetime raises :class:`ValueError`.
    """

    reviewer_id: str
    use_case: str
    outcome: ReviewOutcome
    decided_at: datetime

    def __post_init__(self) -> None:
        if not self.reviewer_id:
            raise ValueError("reviewer_id must be a non-empty string")
        if not self.use_case:
            raise ValueError("use_case must be a non-empty string")
        if self.decided_at.tzinfo is None or self.decided_at.utcoffset() is None:
            raise ValueError(
                "decided_at must be timezone-aware; "
                f"got a naive datetime {self.decided_at!r}"
            )


def observation_from_decision(
    decision: ReviewDecision,
    use_case: str,
) -> ReviewObservation:
    """Build a :class:`ReviewObservation` from a real
    :class:`~ztax_gateway.human_review.ReviewDecision`.

    Parameters
    ----------
    decision:
        A :class:`~ztax_gateway.human_review.ReviewDecision` as returned by
        :meth:`~ztax_gateway.human_review.ReviewQueue.decide`.
    use_case:
        The use-case string.  Not carried by the decision itself; the caller
        must supply it (e.g. from the originating
        :class:`~ztax_gateway.provenance.Provenance`).

    Returns
    -------
    ReviewObservation
        A frozen observation ready for use in :func:`compute_rates`.
    """
    return ReviewObservation(
        reviewer_id=decision.reviewer_id,
        use_case=use_case,
        outcome=decision.outcome,
        decided_at=decision.decided_at,
    )


@dataclass(frozen=True, slots=True)
class RateReport:
    """Computed review statistics for one cohort.

    Attributes
    ----------
    key:
        The group key: ``"all"`` when ``group_by="all"``, the use-case string
        when ``group_by="use_case"``, or the reviewer ID when
        ``group_by="reviewer"``.
    total:
        Total observations in this cohort (after window filtering).
    approved:
        Count of ``APPROVE`` outcomes.
    rejected:
        Count of ``REJECT`` outcomes.
    modified:
        Count of ``MODIFY`` outcomes.
    escalated:
        Count of ``ESCALATE`` outcomes.
    rejection_rate:
        ``rejected / total``.  ``0.0`` when ``total`` is zero.
    override_rate:
        ``modified / total``.  ``0.0`` when ``total`` is zero.
    escalation_rate:
        ``escalated / total``.  ``0.0`` when ``total`` is zero.
    insufficient_sample:
        ``True`` when ``total < min_sample`` at the time of computation.
    """

    key: str
    total: int
    approved: int
    rejected: int
    modified: int
    escalated: int
    rejection_rate: float
    override_rate: float
    escalation_rate: float
    insufficient_sample: bool


_VALID_GROUP_BY = frozenset({"all", "use_case", "reviewer"})


def compute_rates(
    observations: Iterable[ReviewObservation],
    *,
    group_by: str,
    min_sample: int,
    window_start: datetime | None = None,
    window_end: datetime | None = None,
) -> tuple[RateReport, ...]:
    """Compute per-cohort rejection, override, and escalation rates.

    Parameters
    ----------
    observations:
        Any iterable of :class:`ReviewObservation` objects.  May be empty.
    group_by:
        One of ``"all"``, ``"use_case"``, or ``"reviewer"``.  Controls how
        observations are partitioned into cohorts.
    min_sample:
        **Required.**  Cohorts with fewer than this many observations have
        ``insufficient_sample=True`` on their :class:`RateReport`.  This
        module never chooses a threshold value.
    window_start:
        Inclusive start of the time window.  Observations with
        ``decided_at < window_start`` are excluded.  ``None`` means no lower
        bound.
    window_end:
        Exclusive end of the time window.  Observations with
        ``decided_at >= window_end`` are excluded.  ``None`` means no upper
        bound.

    Returns
    -------
    tuple[RateReport, ...]
        One :class:`RateReport` per distinct group key, sorted by key
        (ascending string order).  An empty iterable or a window that excludes
        all observations returns ``()``.

    Raises
    ------
    ValueError
        If ``group_by`` is not one of the three recognised values.

    Notes
    -----
    * No I/O, no clock, no randomness, no logging.
    * Rates are ``count / total`` (same convention as
      :mod:`ztax_gateway.evaluation_quality`).  A cohort with zero
      observations is never produced, so division by zero cannot occur.
    * Window bounds: ``[window_start, window_end)`` — inclusive start,
      exclusive end.
    """
    if group_by not in _VALID_GROUP_BY:
        raise ValueError(
            f"group_by must be one of {sorted(_VALID_GROUP_BY)!r}; got {group_by!r}"
        )

    groups: dict[str, list[ReviewObservation]] = {}

    for obs in observations:
        if window_start is not None and obs.decided_at < window_start:
            continue
        if window_end is not None and obs.decided_at >= window_end:
            continue
        if group_by == "all":
            key = "all"
        elif group_by == "use_case":
            key = obs.use_case
        else:
            key = obs.reviewer_id
        if key not in groups:
            groups[key] = []
        groups[key].append(obs)

    reports: list[RateReport] = []
    for key in sorted(groups):
        bucket = groups[key]
        total = len(bucket)
        rej = sum(1 for o in bucket if o.outcome is ReviewOutcome.REJECT)
        mod = sum(1 for o in bucket if o.outcome is ReviewOutcome.MODIFY)
        esc = sum(1 for o in bucket if o.outcome is ReviewOutcome.ESCALATE)
        apr = sum(1 for o in bucket if o.outcome is ReviewOutcome.APPROVE)
        reports.append(
            RateReport(
                key=key,
                total=total,
                approved=apr,
                rejected=rej,
                modified=mod,
                escalated=esc,
                rejection_rate=rej / total,
                override_rate=mod / total,
                escalation_rate=esc / total,
                insufficient_sample=total < min_sample,
            )
        )

    return tuple(reports)
