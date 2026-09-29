"""Human Review Queue and Decision Record.

Chapter 17 §20 and §25 of the ZoikoTax Master Specification.

An AI output that claims A1+ authority cannot advance to a
:class:`~ztax_gateway.citation.ProvenanceSpan` sealed in the Evidence Ledger,
or to a ``ChangeCandidate`` promoted to content, without a human decision.
This module provides that gate.

The shape is the same as every other enforcement point in this package:
*control before content*.  Submitting an item to the queue requires a
governed context; deciding an item is the *only* path that changes its
status; a decision cannot be mutated after it is issued.

Structure
---------
:class:`ReviewPriority`
    Urgency tier for a :class:`ReviewItem`.  Controls queue ordering and SLA
    breach detection.  ``CRITICAL > HIGH > NORMAL > LOW``.

:class:`EvidencePanel`
    Side-by-side comparison of the AI-plane answer against the deterministic
    rule-based answer.  ``diverges`` is set automatically when the two
    answers differ (case-insensitive strip comparison).  The panel is attached
    to a :class:`ReviewItem` so the reviewer sees both answers without having
    to re-run either system.

:class:`DecisionReasonCode`
    Structured reason code carried by every :class:`ReviewDecision`.  Allows
    downstream systems to react to *why* a decision was made without parsing
    free-text rationale.

:class:`ReviewItem`
    Frozen.  The AI output (identified by ``output_id``) together with the
    :class:`~ztax_gateway.citation.Citation` that grounded it, the governing
    :class:`~ztax_gateway.provenance.Provenance`, a human-readable
    ``summary``, an optional :class:`EvidencePanel`, a
    :class:`ReviewPriority`, and an optional ``due_by`` SLA deadline.
    ``review_item_id`` is derived deterministically from the content.

:class:`ReviewDecision`
    Frozen.  Issued by :meth:`ReviewQueue.decide`.  Carries the
    ``reviewer_profile``, the :class:`ReviewOutcome`, an optional
    ``rationale``, a structured :class:`DecisionReasonCode`, and the
    ``decided_at`` timestamp.  Immutable once created.

:class:`PromotedRecord`
    Frozen.  Created by :meth:`ReviewQueue.promote` when an ``APPROVED`` item
    is *promoted* — i.e. when the approval is converted into a concrete
    downstream artefact (e.g. a new rule version, a sealed Evidence entry).
    This is the step that makes ``APPROVE`` mean something beyond a status flag.

:class:`ReviewQueue`
    Stateful in-process queue.  :meth:`submit` is governance-gated;
    :meth:`decide` is the sole state-transition function;
    :meth:`promote` converts an approved decision into a
    :class:`PromotedRecord`; :meth:`sample_approved` returns a random sample
    of already-approved decisions for quality control.

:class:`ReviewError`
    Raised when the queue cannot proceed — duplicate submission, decision
    on an unknown or already-decided item, etc.

Design rules
------------
* **Citation-mandatory.**  A :class:`ReviewItem` with no citation is a
  programming error.  The constructor refuses to create one.
* **Governance-gated submission.**  :meth:`ReviewQueue.submit` calls
  :func:`~ztax_gateway.governance.authorise` before anything is enqueued.
* **No mutation after decision.**  :class:`ReviewDecision` is ``frozen=True``.
* **Promotion is the terminal action.**  ``APPROVE`` flips a status; only
  :meth:`ReviewQueue.promote` creates the :class:`PromotedRecord` that the
  rest of the system consumes.  An approved item that has not been promoted
  has *not* advanced to content.
* **No fiscal imports.**  This module imports nothing from the fiscal,
  tax_decision or subledger packages (ADR-0006 §2.6; the CI grep enforces
  this).
* **Deterministic item IDs.**  ``review_item_id`` is derived from the
  ``output_id`` and the ``citation_id`` so the same AI output always maps
  to the same queue entry, and a duplicate submission is caught cleanly.
"""

from __future__ import annotations

import hashlib
import random
import uuid
from dataclasses import dataclass, field
from datetime import UTC, datetime
from enum import StrEnum
from typing import Final

from .citation import Citation
from .governance import UseCaseRegistry, authorise
from .provenance import AuthorityOutcome, Provenance

__all__: list[str] = [
    "DecisionReasonCode",
    "EvidencePanel",
    "PromotedRecord",
    "ReviewDecision",
    "ReviewError",
    "ReviewItem",
    "ReviewOutcome",
    "ReviewPriority",
    "ReviewQueue",
    "ReviewStatus",
    "ReviewerProfile",
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_ITEM_ID_PREFIX_LEN: Final[int] = 16  # hex chars in the short review_item_id

# Authority ordering mirrors governance._AUTHORITY_ORDER — A0 < A1 < ... < A5.
_AUTHORITY_ORDER: dict[AuthorityOutcome, int] = {
    a: i for i, a in enumerate(AuthorityOutcome)
}


# ---------------------------------------------------------------------------
# Enumerations
# ---------------------------------------------------------------------------


class ReviewStatus(StrEnum):
    """Lifecycle state of a :class:`ReviewItem`.

    ``PENDING``   -- submitted; no decision yet.
    ``APPROVED``  -- reviewer accepted the AI output for promotion.
    ``REJECTED``  -- reviewer rejected the AI output; it may not advance.
    ``ESCALATED`` -- reviewer deferred to a higher authority tier.
    ``MODIFIED``  -- reviewer corrected the AI output; the correction is
                     carried in the :class:`ReviewDecision`.  Terminal.
    ``EXPIRED``   -- the item exceeded its TTL without a decision; treated
                     as a rejection for promotion purposes.
    ``PROMOTED``  -- the approved item was converted into a
                     :class:`PromotedRecord` by :meth:`ReviewQueue.promote`.
                     Terminal.  An APPROVED item that has *not* been promoted
                     is not yet in content.
    """

    PENDING = "PENDING"
    APPROVED = "APPROVED"
    REJECTED = "REJECTED"
    ESCALATED = "ESCALATED"
    MODIFIED = "MODIFIED"
    EXPIRED = "EXPIRED"
    PROMOTED = "PROMOTED"


class ReviewOutcome(StrEnum):
    """The decision a reviewer issues on a :class:`ReviewItem`.

    ``APPROVE``   -- the AI output may advance (e.g. sealed as a
                     ``ProvenanceSpan`` or promoted to content).
    ``REJECT``    -- the AI output must not advance.
    ``ESCALATE``  -- defer to a higher-authority reviewer; the item is
                     re-queued at the ``ESCALATED`` status so the audit
                     trail records the escalation step.
    ``MODIFY``    -- the reviewer provides a corrected version of the AI
                     output.  The correction is carried in the
                     :class:`ReviewDecision` ``correction`` field.  The
                     item moves to the terminal ``MODIFIED`` status; the
                     corrected output can then be submitted as a new item
                     or sealed directly by the caller.  A ``MODIFY``
                     decision without a non-empty ``correction`` is refused.
    """

    APPROVE = "APPROVE"
    REJECT = "REJECT"
    ESCALATE = "ESCALATE"
    MODIFY = "MODIFY"


class ReviewPriority(StrEnum):
    """Urgency tier for a :class:`ReviewItem`.

    Controls queue ordering and SLA breach detection.

    ``CRITICAL`` -- regulatory deadline or live incident; must be resolved
                    before ``due_by``.
    ``HIGH``     -- elevated urgency; SLA typically 4-24 h.
    ``NORMAL``   -- standard review; SLA typically 48-72 h.  Default.
    ``LOW``      -- background review; no hard deadline.

    Ordering: ``CRITICAL > HIGH > NORMAL > LOW`` (higher urgency number =
    higher priority) -- use :meth:`urgency` rather than string comparison.
    """

    CRITICAL = "CRITICAL"
    HIGH = "HIGH"
    NORMAL = "NORMAL"
    LOW = "LOW"

    def urgency(self) -> int:
        """Return a numeric urgency weight (higher = more urgent)."""
        return {"CRITICAL": 3, "HIGH": 2, "NORMAL": 1, "LOW": 0}[self.value]

    def is_sla_breached(self, due_by: datetime | None) -> bool:
        """Return ``True`` when *due_by* is set and has passed.

        For ``LOW`` priority items this always returns ``False`` even if
        ``due_by`` is in the past, because LOW priority items have no hard SLA.
        """
        if self is ReviewPriority.LOW or due_by is None:
            return False
        return datetime.now(tz=UTC) > due_by


class DecisionReasonCode(StrEnum):
    """Structured reason code for a :class:`ReviewDecision`.

    Allows downstream systems to react to *why* a decision was made without
    parsing free-text ``rationale``.

    Approve codes
    ~~~~~~~~~~~~~
    ``FACTUALLY_CORRECT``     -- AI answer matches the authoritative source.
    ``WITHIN_TOLERANCE``      -- AI answer is close enough; minor deviation
                                 is acceptable under the governing policy.

    Reject codes
    ~~~~~~~~~~~~
    ``FACTUALLY_INCORRECT``   -- AI answer contradicts the authoritative source.
    ``CITATION_INSUFFICIENT`` -- the grounding citation does not support the
                                 AI answer.
    ``OUT_OF_SCOPE``          -- the AI answer addresses a question outside the
                                 permitted use case.
    ``STALE_SOURCE``          -- the cited source was superseded at the time of
                                 the answer.

    Escalate codes
    ~~~~~~~~~~~~~~
    ``REQUIRES_HIGHER_AUTHORITY`` -- reviewer lacks the authority tier to act.
    ``AMBIGUOUS``             -- AI answer is ambiguous; senior reviewer needed.

    Modify codes
    ~~~~~~~~~~~~
    ``CORRECTION_REQUIRED``   -- directionally correct but contains errors.

    General
    ~~~~~~~
    ``OTHER``                 -- none of the above; see free-text ``rationale``.
    """

    FACTUALLY_CORRECT = "FACTUALLY_CORRECT"
    WITHIN_TOLERANCE = "WITHIN_TOLERANCE"
    FACTUALLY_INCORRECT = "FACTUALLY_INCORRECT"
    CITATION_INSUFFICIENT = "CITATION_INSUFFICIENT"
    OUT_OF_SCOPE = "OUT_OF_SCOPE"
    STALE_SOURCE = "STALE_SOURCE"
    REQUIRES_HIGHER_AUTHORITY = "REQUIRES_HIGHER_AUTHORITY"
    AMBIGUOUS = "AMBIGUOUS"
    CORRECTION_REQUIRED = "CORRECTION_REQUIRED"
    OTHER = "OTHER"


# ---------------------------------------------------------------------------
# Error
# ---------------------------------------------------------------------------


class ReviewError(Exception):
    """Raised when the review queue cannot proceed.

    Distinct from :class:`~ztax_gateway.governance.GovernanceRefusedError`
    so callers can handle governance refusals (kill-switch, unknown use case,
    residency) separately from queue-level precondition failures (duplicate
    submission, unknown item, already decided, etc.).
    """

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _derive_item_id(output_id: str, citation_id: str) -> str:
    """Derive a short, deterministic ID for a review item."""
    material = f"{output_id}\0{citation_id}".encode()
    return hashlib.sha256(material).hexdigest()[:_ITEM_ID_PREFIX_LEN]


# ---------------------------------------------------------------------------
# EvidencePanel
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class EvidencePanel:
    """Side-by-side comparison of the AI answer vs the deterministic rule answer.

    Attached to a :class:`ReviewItem` at submission time so the reviewer
    sees both answers without having to re-run either system.

    ``ai_answer``        -- the text produced by the AI model.
    ``rule_answer``      -- the text produced by the deterministic rule engine.
                           May be empty when no deterministic answer exists.
    ``diverges``         -- ``True`` when the answers differ after stripping
                           whitespace and folding case.  Set automatically;
                           callers cannot set it manually.
    ``divergence_detail`` -- optional free-text explanation of *how* the two
                            answers differ.
    """

    ai_answer: str
    rule_answer: str
    divergence_detail: str = ""
    diverges: bool = field(init=False)

    def __post_init__(self) -> None:
        if not self.ai_answer:
            raise ReviewError("evidence_panel: ai_answer is required")
        object.__setattr__(
            self,
            "diverges",
            self.ai_answer.strip().casefold() != self.rule_answer.strip().casefold(),
        )

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        return {
            "ai_answer": self.ai_answer,
            "rule_answer": self.rule_answer,
            "diverges": self.diverges,
            "divergence_detail": self.divergence_detail,
        }


# ---------------------------------------------------------------------------
# ReviewerProfile
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class ReviewerProfile:
    """Immutable qualification record for a human reviewer.

    Every call to :meth:`ReviewQueue.decide` must supply a
    :class:`ReviewerProfile`.  The queue validates:

    1. The reviewer's ``permitted_regions`` contains the item's
       ``provenance.region``.
    2. The reviewer's ``max_authority`` is at least as high as the item's
       ``provenance.authority_outcome``.

    The profile is stamped into every :class:`ReviewDecision` so that the
    audit trail can prove the reviewer held the necessary qualifications *at
    the time of the decision*.

    ``permitted_jurisdictions`` records the tax regimes the reviewer is
    qualified for (e.g. ``{"EU-VAT", "AU-GST"}``).  Informational for now.

    ``qualified_since`` is the UTC timestamp when this qualification was
    granted.  Stored for audit purposes; the queue does not gate on it.
    """

    reviewer_id: str
    max_authority: AuthorityOutcome
    permitted_regions: frozenset[str]
    permitted_jurisdictions: frozenset[str]
    qualified_since: datetime

    def __post_init__(self) -> None:
        if not self.reviewer_id:
            raise ReviewError("review: ReviewerProfile has no reviewer_id")
        if not self.permitted_regions:
            raise ReviewError(
                f"review: ReviewerProfile for {self.reviewer_id!r} has no "
                "permitted_regions -- reviewer cannot act in any region"
            )

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        return {
            "reviewer_id": self.reviewer_id,
            "max_authority": self.max_authority.value,
            "permitted_regions": sorted(self.permitted_regions),
            "permitted_jurisdictions": sorted(self.permitted_jurisdictions),
            "qualified_since": self.qualified_since.isoformat(),
        }


# ---------------------------------------------------------------------------
# ReviewItem
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class ReviewItem:
    """An immutable submission to the human-review queue.

    ``output_id``      -- identifies the AI-plane output being reviewed.
    ``citation``       -- the :class:`~ztax_gateway.citation.Citation` that
                         grounded the AI output.  **Mandatory.**
    ``summary``        -- human-readable statement of what is being reviewed.
    ``provenance``     -- the governed context under which the item was
                         submitted.
    ``evidence_panel`` -- optional side-by-side comparison of the AI answer
                         vs the deterministic rule answer.
    ``priority``       -- :class:`ReviewPriority` urgency tier.  Defaults to
                         ``NORMAL``.
    ``due_by``         -- optional SLA deadline (timezone-aware UTC).
    ``review_item_id`` -- derived deterministically from ``output_id`` and
                         ``citation.citation_id``.
    ``submitted_at``   -- UTC timestamp of submission.
    ``status``         -- begins as ``PENDING``; transitions via
                         :meth:`ReviewQueue.decide` or
                         :meth:`ReviewQueue.promote`.
    """

    output_id: str
    citation: Citation
    summary: str
    provenance: Provenance
    evidence_panel: EvidencePanel | None = None
    priority: ReviewPriority = ReviewPriority.NORMAL
    due_by: datetime | None = None
    review_item_id: str = field(init=False)
    submitted_at: datetime = field(init=False)
    status: ReviewStatus = field(init=False)

    def __post_init__(self) -> None:
        if not self.output_id:
            raise ReviewError("review: output_id is required")
        if not self.summary:
            raise ReviewError(
                f"review: item for output {self.output_id!r} has no summary"
            )
        if self.due_by is not None and self.due_by.tzinfo is None:
            raise ReviewError(
                f"review: due_by for output {self.output_id!r} must be timezone-aware"
            )
        object.__setattr__(
            self,
            "review_item_id",
            _derive_item_id(self.output_id, self.citation.citation_id),
        )
        object.__setattr__(self, "submitted_at", datetime.now(tz=UTC))
        object.__setattr__(self, "status", ReviewStatus.PENDING)

    @property
    def sla_breached(self) -> bool:
        """``True`` when the SLA deadline has passed for this item."""
        return self.priority.is_sla_breached(self.due_by)

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        d: dict[str, object] = {
            "review_item_id": self.review_item_id,
            "output_id": self.output_id,
            "citation_id": self.citation.citation_id,
            "summary": self.summary,
            "status": self.status.value,
            "priority": self.priority.value,
            "submitted_at": self.submitted_at.isoformat(),
            "use_case": self.provenance.use_case,
            "region": self.provenance.region,
            "risk_tier": self.provenance.risk_tier.value,
            "authority_outcome": self.provenance.authority_outcome.value,
        }
        if self.evidence_panel is not None:
            d["evidence_panel"] = self.evidence_panel.as_dict()
        if self.due_by is not None:
            d["due_by"] = self.due_by.isoformat()
            d["sla_breached"] = self.sla_breached
        return d


# ---------------------------------------------------------------------------
# ReviewDecision
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class ReviewDecision:
    """An immutable decision record issued by :meth:`ReviewQueue.decide`.

    Once created, a :class:`ReviewDecision` cannot be mutated.

    ``review_item_id`` -- mirrors :attr:`ReviewItem.review_item_id`.
    ``output_id``      -- mirrors :attr:`ReviewItem.output_id`.
    ``reviewer_profile`` -- full :class:`ReviewerProfile` at decision time.
    ``reviewer_id``    -- derived from ``reviewer_profile.reviewer_id``.
    ``outcome``        -- :class:`ReviewOutcome`.
    ``reason_code``    -- optional structured :class:`DecisionReasonCode`.
    ``rationale``      -- optional free-text explanation.
    ``correction``     -- non-empty iff ``outcome`` is ``MODIFY``.
    ``decided_at``     -- UTC timestamp.
    ``decision_id``    -- short UUID-derived identifier; not deterministic.
    """

    review_item_id: str
    output_id: str
    reviewer_profile: ReviewerProfile
    outcome: ReviewOutcome
    decided_at: datetime
    decision_id: str
    rationale: str = ""
    correction: str = ""
    reason_code: DecisionReasonCode | None = None
    reviewer_id: str = field(init=False)

    def __post_init__(self) -> None:
        object.__setattr__(self, "reviewer_id", self.reviewer_profile.reviewer_id)
        if self.outcome is ReviewOutcome.MODIFY and not self.correction:
            raise ReviewError(
                f"review: MODIFY decision on {self.review_item_id!r} "
                "requires a non-empty correction"
            )
        if self.outcome is not ReviewOutcome.MODIFY and self.correction:
            raise ReviewError(
                f"review: {self.outcome.value} decision on {self.review_item_id!r} "
                "must not carry a correction (only MODIFY decisions may)"
            )

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        d: dict[str, object] = {
            "decision_id": self.decision_id,
            "review_item_id": self.review_item_id,
            "output_id": self.output_id,
            "reviewer_id": self.reviewer_id,
            "reviewer_profile": self.reviewer_profile.as_dict(),
            "outcome": self.outcome.value,
            "decided_at": self.decided_at.isoformat(),
        }
        if self.reason_code is not None:
            d["reason_code"] = self.reason_code.value
        if self.rationale:
            d["rationale"] = self.rationale
        if self.correction:
            d["correction"] = self.correction
        return d


# ---------------------------------------------------------------------------
# PromotedRecord
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class PromotedRecord:
    """An immutable record created when an approved :class:`ReviewItem` is promoted.

    :meth:`ReviewQueue.promote` is the *only* function that creates a
    :class:`PromotedRecord`.  It requires the item to be ``APPROVED``, and it
    transitions the item to the terminal ``PROMOTED`` status.

    This is the object the rest of the system consumes: a rule-update pipeline
    ingests a ``PromotedRecord``; the Evidence Ledger seals it; a content store
    persists it.  An ``APPROVED`` item that has *not* been promoted is *not*
    yet in content.

    ``promoted_record_id`` is a deterministic hash of the
    ``review_item_id`` + ``decision_id`` pair (idempotency guard).

    ``payload`` is the content being promoted — e.g. the approved diff text,
    corrected answer text, or rule version identifier.

    ``promoted_at`` is the UTC timestamp of promotion.
    """

    review_item_id: str
    decision_id: str
    output_id: str
    payload: str
    promoted_at: datetime
    promoted_record_id: str = field(init=False)

    def __post_init__(self) -> None:
        if not self.payload:
            raise ReviewError(
                f"review: promoted record for item {self.review_item_id!r} "
                "requires a non-empty payload"
            )
        material = f"{self.review_item_id}\0{self.decision_id}".encode()
        object.__setattr__(
            self,
            "promoted_record_id",
            hashlib.sha256(material).hexdigest()[:_ITEM_ID_PREFIX_LEN],
        )

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        return {
            "promoted_record_id": self.promoted_record_id,
            "review_item_id": self.review_item_id,
            "decision_id": self.decision_id,
            "output_id": self.output_id,
            "payload": self.payload,
            "promoted_at": self.promoted_at.isoformat(),
        }


# ---------------------------------------------------------------------------
# Internal mutable wrapper
# ---------------------------------------------------------------------------


class _QueueEntry:
    """Mutable status wrapper around an immutable :class:`ReviewItem`."""

    __slots__ = ("item", "override_status", "promoted_record")

    def __init__(self, item: ReviewItem) -> None:
        self.item = item
        self.override_status: ReviewStatus | None = None
        self.promoted_record: PromotedRecord | None = None


# Terminal states -- once reached, decide() refuses further decisions.
_TERMINAL: frozenset[ReviewStatus] = frozenset(
    {
        ReviewStatus.APPROVED,
        ReviewStatus.REJECTED,
        ReviewStatus.MODIFIED,
        ReviewStatus.EXPIRED,
        ReviewStatus.PROMOTED,
    }
)


# ---------------------------------------------------------------------------
# ReviewQueue
# ---------------------------------------------------------------------------


class ReviewQueue:
    """In-process human-review queue.

    :meth:`submit` accepts AI outputs for human review.  Governance-gated.

    :meth:`decide` is the sole state-transition function.

    :meth:`promote` converts an ``APPROVED`` item into a
    :class:`PromotedRecord` — the step that makes an approval mean something.

    :meth:`pop` returns the highest-priority pending item (``CRITICAL`` first,
    then ``HIGH``, ``NORMAL``, ``LOW``; FIFO within the same tier).

    :meth:`sample_approved` returns a random sample of approved decisions for
    quality-control spot-checks.

    :meth:`sla_breached_items` returns all PENDING items whose SLA deadline
    has passed.

    Args:
        ttl_seconds: How long a ``PENDING`` item remains before it is
            automatically ``EXPIRED`` on the next :meth:`pop` or
            :meth:`pending_count` call.  ``None`` means items never expire.
    """

    def __init__(self, ttl_seconds: int | None = None) -> None:
        self._ttl_seconds = ttl_seconds
        self._items: dict[str, _QueueEntry] = {}
        self._decisions: dict[str, list[ReviewDecision]] = {}
        self._queue: list[str] = []  # FIFO insertion order

    # ------------------------------------------------------------------
    # Submit
    # ------------------------------------------------------------------

    def submit(
        self,
        output_id: str,
        citation: Citation,
        summary: str,
        provenance: Provenance,
        registry: UseCaseRegistry,
        *,
        evidence_panel: EvidencePanel | None = None,
        priority: ReviewPriority = ReviewPriority.NORMAL,
        due_by: datetime | None = None,
    ) -> ReviewItem:
        """Submit an AI output for human review.

        Governance gate: *provenance* is validated before anything is enqueued.
        The same ``output_id`` / ``citation`` pair may not be submitted twice.

        Args:
            output_id: Identifier of the AI-plane output being submitted.
            citation: The grounding citation.  Mandatory.
            summary: Human-readable description of what is being reviewed.
            provenance: Governed context.  Must be registered and non-killed.
            registry: Use-case registry holding registrations and kill switches.
            evidence_panel: Optional side-by-side AI vs rule answer comparison.
            priority: Urgency tier.  Defaults to ``NORMAL``.
            due_by: Optional SLA deadline (timezone-aware UTC).

        Returns:
            The frozen :class:`ReviewItem` that was enqueued.

        Raises:
            GovernanceRefusedError: if *provenance* is not authorised.
            ReviewError: if the item is already in the queue.
        """
        authorise(registry, provenance)

        item = ReviewItem(
            output_id=output_id,
            citation=citation,
            summary=summary,
            provenance=provenance,
            evidence_panel=evidence_panel,
            priority=priority,
            due_by=due_by,
        )

        if item.review_item_id in self._items:
            raise ReviewError(
                f"review: item {item.review_item_id!r} (output {output_id!r}) "
                "is already in the queue"
            )

        self._items[item.review_item_id] = _QueueEntry(item=item)
        self._decisions[item.review_item_id] = []
        self._queue.append(item.review_item_id)

        return item

    # ------------------------------------------------------------------
    # Decide
    # ------------------------------------------------------------------

    def decide(
        self,
        review_item_id: str,
        reviewer_profile: ReviewerProfile,
        outcome: ReviewOutcome,
        rationale: str = "",
        correction: str = "",
        reason_code: DecisionReasonCode | None = None,
    ) -> ReviewDecision:
        """Issue a decision on a pending review item.

        The only function that transitions a :class:`ReviewItem` from
        ``PENDING`` (or ``ESCALATED``) to a terminal or escalated state.

        **Reviewer qualification checks:**

        1. ``permitted_regions`` must contain the item's ``provenance.region``.
        2. ``max_authority`` must be >= the item's ``provenance.authority_outcome``.

        **Outcome rules:**

        - ``APPROVE`` / ``REJECT`` / ``ESCALATE`` -- ``correction`` must be empty.
        - ``MODIFY`` -- ``correction`` must be non-empty.
        - ``ESCALATE`` -- item stays actionable for a higher-authority reviewer.

        Args:
            review_item_id: The item to decide.
            reviewer_profile: Qualification record.  Validated against the item.
            outcome: :class:`ReviewOutcome`.
            rationale: Optional free-text explanation.
            correction: Required for ``MODIFY``; must be empty otherwise.
            reason_code: Optional structured :class:`DecisionReasonCode`.

        Returns:
            The frozen :class:`ReviewDecision`.

        Raises:
            ReviewError: unknown item, already terminal, insufficient
                qualifications, or inconsistent correction field.
        """
        entry = self._items.get(review_item_id)
        if entry is None:
            raise ReviewError(f"review: unknown review_item_id {review_item_id!r}")

        current = (
            entry.override_status if entry.override_status is not None else entry.item.status
        )
        if current in _TERMINAL:
            raise ReviewError(
                f"review: item {review_item_id!r} is already in terminal "
                f"status {current.value!r} and cannot be re-decided"
            )

        item_region = entry.item.provenance.region
        if item_region not in reviewer_profile.permitted_regions:
            raise ReviewError(
                f"review: reviewer {reviewer_profile.reviewer_id!r} is not "
                f"permitted to review in region {item_region!r}"
            )

        item_authority = entry.item.provenance.authority_outcome
        if _AUTHORITY_ORDER[reviewer_profile.max_authority] < _AUTHORITY_ORDER[item_authority]:
            raise ReviewError(
                f"review: reviewer {reviewer_profile.reviewer_id!r} holds "
                f"max authority {reviewer_profile.max_authority.value} but the "
                f"item requires at least {item_authority.value}"
            )

        decision = ReviewDecision(
            review_item_id=review_item_id,
            output_id=entry.item.output_id,
            reviewer_profile=reviewer_profile,
            outcome=outcome,
            decided_at=datetime.now(tz=UTC),
            decision_id=uuid.uuid4().hex[:_ITEM_ID_PREFIX_LEN],
            rationale=rationale,
            correction=correction,
            reason_code=reason_code,
        )

        if outcome is ReviewOutcome.APPROVE:
            entry.override_status = ReviewStatus.APPROVED
        elif outcome is ReviewOutcome.REJECT:
            entry.override_status = ReviewStatus.REJECTED
        elif outcome is ReviewOutcome.MODIFY:
            entry.override_status = ReviewStatus.MODIFIED
        else:  # ESCALATE
            entry.override_status = ReviewStatus.ESCALATED

        self._decisions[review_item_id].append(decision)
        return decision

    # ------------------------------------------------------------------
    # Promote
    # ------------------------------------------------------------------

    def promote(
        self,
        review_item_id: str,
        payload: str,
    ) -> PromotedRecord:
        """Convert an ``APPROVED`` item into a :class:`PromotedRecord`.

        This is the step that makes an approval mean something.  Until this
        method is called, an ``APPROVED`` item has *not* advanced to content.

        The item must be in ``APPROVED`` status.  Any other status — including
        ``PROMOTED`` (already promoted) — is refused, preventing double-promotion.

        After a successful call the item transitions to the terminal
        ``PROMOTED`` status.

        Args:
            review_item_id: The item to promote.
            payload: The content being promoted (e.g. approved diff text, rule
                version identifier).  Must be non-empty.

        Returns:
            The frozen :class:`PromotedRecord`.

        Raises:
            ReviewError: item unknown, not ``APPROVED``, or *payload* is empty.
        """
        entry = self._items.get(review_item_id)
        if entry is None:
            raise ReviewError(f"review: unknown review_item_id {review_item_id!r}")

        current = (
            entry.override_status if entry.override_status is not None else entry.item.status
        )
        if current is not ReviewStatus.APPROVED:
            raise ReviewError(
                f"review: item {review_item_id!r} cannot be promoted from "
                f"status {current.value!r} -- only APPROVED items may be promoted"
            )

        decisions = self._decisions.get(review_item_id, [])
        approve_decision = next(
            (d for d in reversed(decisions) if d.outcome is ReviewOutcome.APPROVE),
            None,
        )
        if approve_decision is None:
            raise ReviewError(
                f"review: item {review_item_id!r} is APPROVED but has no "
                "APPROVE decision on record -- this is a bug"
            )

        record = PromotedRecord(
            review_item_id=review_item_id,
            decision_id=approve_decision.decision_id,
            output_id=entry.item.output_id,
            payload=payload,
            promoted_at=datetime.now(tz=UTC),
        )

        entry.override_status = ReviewStatus.PROMOTED
        entry.promoted_record = record

        return record

    # ------------------------------------------------------------------
    # Read operations
    # ------------------------------------------------------------------

    def pop(self) -> ReviewItem | None:
        """Return the highest-priority pending item without changing its status.

        Items are surfaced in priority order (``CRITICAL`` first, then
        ``HIGH``, ``NORMAL``, ``LOW``) with FIFO ordering within the same
        tier.  Also expires stale items before selecting.

        Returns:
            The next pending :class:`ReviewItem`, or ``None``.
        """
        self._expire_stale()
        pending: list[tuple[int, int, str]] = []
        for fifo_index, item_id in enumerate(self._queue):
            entry = self._items[item_id]
            effective = (
                entry.override_status
                if entry.override_status is not None
                else entry.item.status
            )
            if effective is ReviewStatus.PENDING:
                pending.append((-entry.item.priority.urgency(), fifo_index, item_id))
        if not pending:
            return None
        pending.sort()
        _, _, best_id = pending[0]
        return self._items[best_id].item

    def get(self, review_item_id: str) -> ReviewItem | None:
        """Return the :class:`ReviewItem` for *review_item_id*, or ``None``."""
        entry = self._items.get(review_item_id)
        return entry.item if entry is not None else None

    def status(self, review_item_id: str) -> ReviewStatus | None:
        """Return the current :class:`ReviewStatus` of an item, or ``None``."""
        entry = self._items.get(review_item_id)
        if entry is None:
            return None
        return entry.override_status if entry.override_status is not None else entry.item.status

    def decisions(self, review_item_id: str) -> list[ReviewDecision]:
        """Return every :class:`ReviewDecision` for *review_item_id*, oldest first."""
        return list(self._decisions.get(review_item_id, []))

    def promoted_record(self, review_item_id: str) -> PromotedRecord | None:
        """Return the :class:`PromotedRecord` for *review_item_id*, or ``None``."""
        entry = self._items.get(review_item_id)
        return entry.promoted_record if entry is not None else None

    def pending_count(self) -> int:
        """Return the number of items currently in ``PENDING`` status."""
        self._expire_stale()
        return sum(
            1
            for entry in self._items.values()
            if (entry.override_status if entry.override_status is not None else entry.item.status)
            is ReviewStatus.PENDING
        )

    def escalated_count(self) -> int:
        """Return the number of items currently in ``ESCALATED`` status."""
        return sum(
            1
            for entry in self._items.values()
            if (entry.override_status if entry.override_status is not None else entry.item.status)
            is ReviewStatus.ESCALATED
        )

    def promoted_count(self) -> int:
        """Return the number of items in ``PROMOTED`` status."""
        return sum(
            1
            for entry in self._items.values()
            if (entry.override_status if entry.override_status is not None else entry.item.status)
            is ReviewStatus.PROMOTED
        )

    def sla_breached_items(self) -> list[ReviewItem]:
        """Return all PENDING items whose SLA deadline has passed.

        Items without a ``due_by`` or with ``LOW`` priority are never included.
        The list is sorted by ``due_by`` ascending (most overdue first).
        """
        self._expire_stale()
        breached = [
            entry.item
            for entry in self._items.values()
            if (
                (
                    entry.override_status
                    if entry.override_status is not None
                    else entry.item.status
                )
                is ReviewStatus.PENDING
                and entry.item.sla_breached
            )
        ]
        breached.sort(key=lambda it: it.due_by or datetime.max.replace(tzinfo=UTC))
        return breached

    def sample_approved(
        self,
        k: int,
        *,
        rng: random.Random | None = None,
    ) -> list[ReviewDecision]:
        """Return a random sample of up to *k* approved decisions for QA spot-checks.

        Sampling is uniform without replacement (or all decisions when the
        pool is smaller than *k*).

        Args:
            k: Maximum number of decisions to return.  Must be >= 1.
            rng: Optional :class:`random.Random` for reproducible tests.

        Returns:
            Up to *k* :class:`ReviewDecision` objects, or ``[]`` if no
            approved decisions exist.

        Raises:
            ReviewError: if *k* < 1.
        """
        if k < 1:
            raise ReviewError(f"review: sample_approved requires k >= 1, got {k}")
        _rng = rng if rng is not None else random.Random()
        pool: list[ReviewDecision] = [
            decision
            for decisions in self._decisions.values()
            for decision in decisions
            if decision.outcome is ReviewOutcome.APPROVE
        ]
        if not pool:
            return []
        return _rng.sample(pool, min(k, len(pool)))

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _expire_stale(self) -> None:
        """Transition timed-out PENDING items to EXPIRED."""
        if self._ttl_seconds is None:
            return
        now = datetime.now(tz=UTC)
        for entry in self._items.values():
            effective = (
                entry.override_status if entry.override_status is not None else entry.item.status
            )
            if effective is not ReviewStatus.PENDING:
                continue
            age = (now - entry.item.submitted_at).total_seconds()
            if age > self._ttl_seconds:
                entry.override_status = ReviewStatus.EXPIRED

