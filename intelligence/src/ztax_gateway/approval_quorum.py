"""Approval Quorum Guard.

ZTAX-AIGOV-001 / REQ-0077 — ``AI MAY NOT count as one of two required human approvers.``

This module is **unwired and advisory only**.  It decides nothing about
authority; it only counts actors in an approval list and returns a
:class:`QuorumResult` describing whether the quorum of *human* approvers has
been met.

Structure
---------
:class:`ActorKind`
    Closed vocabulary.  Only ``HUMAN`` actors may satisfy a quorum.

:class:`IgnoredReason`
    Why an :class:`Approval` was not counted toward the quorum.
    ``AI_ACTOR`` — the actor is an AI system.
    ``SERVICE_ACTOR`` — the actor is a service account.
    ``DUPLICATE_HUMAN`` — a human submitted more than one approval; subsequent
    submissions beyond the first are ignored.

:class:`Approval`
    Frozen.  ``actor_id`` (non-empty, non-whitespace) and ``kind``.

:class:`QuorumResult`
    Frozen.  ``met`` — whether the quorum was satisfied.  ``required`` — the
    threshold used.  ``counted`` — sorted tuple of the distinct human
    ``actor_id`` values that were accepted.  ``ignored`` — sorted tuple of
    ``(actor_id, reason)`` pairs for every approval not counted.

:func:`evaluate_quorum`
    Pure function.  Human IDs are normalised via ``strip()`` and ``casefold()``
    for duplicate detection; the *original* ID string (as supplied) is recorded
    in ``counted`` and ``ignored``.  Deterministic output; no I/O; no clock.

Known limits (REQ-0077 is not closed)
--------------------------------------
Nothing in :mod:`ztax_gateway.human_review`, the Go gateway, or the content-
promotion path currently calls this function.  Until a caller enforces
``QuorumResult.met``, the AI-cannot-be-approver rule is not enforced at
runtime.
"""

from __future__ import annotations

from collections.abc import Iterable
from dataclasses import dataclass
from enum import StrEnum


class ActorKind(StrEnum):
    """The class of entity submitting an approval.

    Only ``HUMAN`` actors are eligible to satisfy a human-quorum requirement.
    ``AI`` and ``SERVICE`` actors are tracked via :class:`IgnoredReason` but
    never counted toward the quorum threshold.
    """

    HUMAN = "HUMAN"
    AI = "AI"
    SERVICE = "SERVICE"


class IgnoredReason(StrEnum):
    """Why an :class:`Approval` was not counted toward the quorum.

    ``AI_ACTOR``       — the actor's :attr:`Approval.kind` is ``AI``.
    ``SERVICE_ACTOR``  — the actor's :attr:`Approval.kind` is ``SERVICE``.
    ``DUPLICATE_HUMAN``— a human ``actor_id`` (after strip and casefold) already
                         appeared earlier in the sequence; only the first
                         occurrence is counted.
    """

    AI_ACTOR = "AI_ACTOR"
    SERVICE_ACTOR = "SERVICE_ACTOR"
    DUPLICATE_HUMAN = "DUPLICATE_HUMAN"


@dataclass(frozen=True, slots=True)
class Approval:
    """A single approval token.

    Attributes
    ----------
    actor_id:
        Opaque identifier for the approving entity.  Must be non-empty and
        must contain at least one non-whitespace character.
    kind:
        The :class:`ActorKind` of the approving entity.
    """

    actor_id: str
    kind: ActorKind

    def __post_init__(self) -> None:
        if not self.actor_id or not self.actor_id.strip():
            raise ValueError(
                f"actor_id must be non-empty and non-whitespace; got {self.actor_id!r}"
            )


@dataclass(frozen=True, slots=True)
class QuorumResult:
    """Result of a :func:`evaluate_quorum` call.

    Attributes
    ----------
    met:
        ``True`` when ``len(counted) >= required``.
    required:
        The threshold evaluated against.
    counted:
        Sorted tuple of the *original* ``actor_id`` strings for every distinct
        human approval that was accepted toward the quorum.  Distinctness is
        determined by comparing IDs after ``strip()`` and ``casefold()``.
    ignored:
        Sorted tuple of ``(actor_id, reason)`` pairs for every approval that was
        *not* counted, in ``(actor_id, reason)`` ascending order.
    """

    met: bool
    required: int
    counted: tuple[str, ...]
    ignored: tuple[tuple[str, IgnoredReason], ...]


def evaluate_quorum(
    approvals: Iterable[Approval],
    required: int = 2,
) -> QuorumResult:
    """Evaluate whether a sequence of approvals satisfies a human-quorum requirement.

    Human IDs are normalised with ``strip()`` and ``casefold()`` for duplicate
    detection only.  The *original* ``actor_id`` string (as supplied by the
    caller) is stored in :attr:`QuorumResult.counted` and
    :attr:`QuorumResult.ignored`.

    Parameters
    ----------
    approvals:
        Any iterable of :class:`Approval` objects.  May be empty.
    required:
        Minimum number of distinct human approvals needed.  Must be >= 1.

    Returns
    -------
    QuorumResult
        Frozen result.  Inspect ``result.met`` for the verdict.

    Raises
    ------
    ValueError
        If ``required`` is less than 1.

    Notes
    -----
    * No I/O, no clock, no randomness, no logging.
    * The same human approving twice (identical or case-variant IDs) counts
      as one approval; the second submission is recorded in ``ignored`` as
      ``DUPLICATE_HUMAN``.
    * AI and SERVICE actors are never counted; they appear in ``ignored`` as
      ``AI_ACTOR`` or ``SERVICE_ACTOR`` respectively.
    """
    if required < 1:
        raise ValueError(f"required must be >= 1, got {required!r}")

    # Maps normalised_id -> original actor_id for accepted humans.
    seen: dict[str, str] = {}
    ignored_list: list[tuple[str, IgnoredReason]] = []

    for approval in approvals:
        if approval.kind is ActorKind.HUMAN:
            normalised = approval.actor_id.strip().casefold()
            if normalised in seen:
                ignored_list.append((approval.actor_id, IgnoredReason.DUPLICATE_HUMAN))
            else:
                seen[normalised] = approval.actor_id
        elif approval.kind is ActorKind.AI:
            ignored_list.append((approval.actor_id, IgnoredReason.AI_ACTOR))
        else:
            ignored_list.append((approval.actor_id, IgnoredReason.SERVICE_ACTOR))

    counted = tuple(sorted(seen.values()))
    ignored = tuple(sorted(ignored_list))

    return QuorumResult(
        met=len(counted) >= required,
        required=required,
        counted=counted,
        ignored=ignored,
    )
