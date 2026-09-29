"""Change Intelligence Engine.

Chapter 17 §8.1 and §37 of the ZoikoTax Master Specification.

When new tax documents, regulatory bulletins, or statutory guidance are
ingested, the system must detect what changed before any tax-decision model
or rule set is updated.  This module provides that detection: it compares
an *incoming corpus* against a *prior corpus* and extracts
:class:`ChangeCandidate` proposals (rate changes, threshold adjustments,
new classification rules, removed provisions, etc.).

Because automated extraction cannot directly alter tax calculation logic
without supervision, every :class:`ChangeCandidate` must travel through
the Human Review Queue (:class:`~ztax_gateway.human_review.ReviewQueue`)
before it can be promoted to content.

Structure
---------
:class:`ChangeType`
    Enumeration of the *legal-change* kinds the engine can detect.  Eight
    values exactly as specified in §8.1: ``RATE``, ``DEFINITION``,
    ``EFFECTIVE_DATE``, ``FORM``, ``THRESHOLD``, ``OBLIGATION``,
    ``PROCEDURE``, ``OTHER``.

:class:`CandidateStatus`
    Lifecycle state of a :class:`ChangeCandidate`.  Transitions:
    ``PROPOSED → VALIDATED → REJECTED`` or
    ``PROPOSED → VALIDATED → PROMOTED_TO_CONTENT_CHANGE``.
    ``PROPOSED`` is the initial state; status transitions are tracked by the
    calling orchestrator (not mutated on the frozen dataclass itself --
    see :class:`CandidateStatusRecord`).

:class:`EffectiveDateEvidence`
    Structured evidence for a candidate effective date (``ZTAX-AI-REQ-0049``).
    Carries ``candidate_date``, ``date_confidence``, and ``source_text``
    so the evidence for *when* a change takes effect is separately
    queryable and auditable.

:class:`AuthorityLevel`
    Classification of the source document's legal authority tier.
    ``PRIMARY`` (legislation) > ``SECONDARY`` (statutory instruments,
    regulations) > ``GUIDANCE`` (HMRC briefs, revenue rulings) > ``OTHER``.

:class:`ChangeCandidate`
    Frozen.  A single proposed change extracted from the diff.  Carries
    ``candidate_id`` (deterministic SHA-256 from the diff material),
    ``source_snapshot_before`` / ``source_snapshot_after`` (citations for
    both the prior and incoming state -- ``ZTAX-AI-REQ-0048``),
    ``target_rule_or_section``, ``change_type``, ``proposed_diff``,
    ``confidence_score``, optional ``effective_date_evidence``
    (``ZTAX-AI-REQ-0049``), ``authority_level``, ``impact_targets``,
    ``ai_release_manifest``, ``review_priority``, and a :class:`Provenance`.

:class:`CandidateStatusRecord`
    Mutable status wrapper.  The status of a candidate (PROPOSED →
    VALIDATED → …) is tracked here, not on the frozen ``ChangeCandidate``,
    so the immutability contract is preserved.

:class:`ChangeCandidateError`
    Distinct domain exception for schema violations, missing citations,
    invalid diffs, or attempted extraction on an ungoverned context.

:class:`CorpusEntry`
    An immutable entry in a document corpus: ``document_id``,
    ``section_id``, ``content``, ``citation``.

:class:`ChangeIntelligenceEngine`
    Service class.  :meth:`extract_changes` is the governance-gated entry
    point.  :meth:`to_review_item` converts a :class:`ChangeCandidate`
    into a ready-to-queue :class:`~ztax_gateway.human_review.ReviewItem`.

Design rules
------------
* **ADR-0006 §2.6 isolation.**  No imports from fiscal, tax_decision or
  subledger packages.  CI grep enforces this.
* **Governance first.**  :meth:`ChangeIntelligenceEngine.extract_changes`
  calls :func:`~ztax_gateway.governance.authorise` as step 1 — before
  inspecting ``prior_corpus`` or ``incoming_corpus``.
* **Dual-citation mandatory (ZTAX-AI-REQ-0048).**  For a modification the
  candidate carries *both* ``source_snapshot_before`` (prior citation) and
  ``source_snapshot_after`` (incoming citation).  For additions
  ``source_snapshot_before`` is ``None``; for deletions
  ``source_snapshot_after`` is ``None``.
* **Deterministic candidate IDs.**  ``candidate_id`` is derived from
  ``(source_document_id, target_rule_or_section, change_type, proposed_diff)``
  so the same extracted change always maps to the same ID.
* **Immutable records.**  All dataclasses use ``frozen=True, slots=True``.
"""

from __future__ import annotations

import hashlib
from collections.abc import Sequence
from dataclasses import dataclass, field
from datetime import UTC, date, datetime
from enum import StrEnum
from typing import Final

from .citation import Citation
from .governance import UseCaseRegistry, authorise
from .human_review import EvidencePanel, ReviewItem, ReviewPriority, ReviewQueue
from .provenance import Provenance

__all__: list[str] = [
    "AuthorityLevel",
    "CandidateStatus",
    "CandidateStatusRecord",
    "ChangeCandidate",
    "ChangeCandidateError",
    "ChangeIntelligenceEngine",
    "ChangeType",
    "CorpusEntry",
    "EffectiveDateEvidence",
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_CANDIDATE_ID_PREFIX_LEN: Final[int] = 16  # hex chars kept as the short ID

_MIN_CONFIDENCE: Final[float] = 0.0
_MAX_CONFIDENCE: Final[float] = 1.0


# ---------------------------------------------------------------------------
# Enumerations
# ---------------------------------------------------------------------------


class ChangeType(StrEnum):
    """Legal-change taxonomy for a :class:`ChangeCandidate`.

    Eight values exactly as specified in Chapter 17 §8.1.

    ``RATE``           -- A numeric rate (e.g. VAT percentage, withholding
                          rate, levy fraction) has changed.
    ``DEFINITION``     -- The statutory meaning or scope of a term has been
                          altered (e.g. what counts as a "supply").
    ``EFFECTIVE_DATE`` -- The date on which an existing rule or rate comes
                          into force has changed without altering the rule
                          substance itself.
    ``FORM``           -- A prescribed form, return layout, or compliance
                          format has been added, modified, or withdrawn.
    ``THRESHOLD``      -- A numeric monetary or volume threshold (e.g.
                          registration threshold, de minimis limit) has
                          changed.
    ``OBLIGATION``     -- A filing, withholding, reporting, or payment
                          obligation has been added, removed, or altered.
    ``PROCEDURE``      -- An administrative procedure, appeal route, or
                          process step has changed.
    ``OTHER``          -- Change detected but does not fit the above categories.
    """

    RATE = "RATE"
    DEFINITION = "DEFINITION"
    EFFECTIVE_DATE = "EFFECTIVE_DATE"
    FORM = "FORM"
    THRESHOLD = "THRESHOLD"
    OBLIGATION = "OBLIGATION"
    PROCEDURE = "PROCEDURE"
    OTHER = "OTHER"


class CandidateStatus(StrEnum):
    """Lifecycle state of a :class:`ChangeCandidate`.

    ``PROPOSED``                -- Extracted and awaiting human review.
    ``VALIDATED``               -- Reviewer confirmed the change is correct.
    ``REJECTED``                -- Reviewer rejected the extraction.
    ``PROMOTED_TO_CONTENT_CHANGE`` -- Validated change has been promoted to
                                     a concrete content-change record.
    """

    PROPOSED = "PROPOSED"
    VALIDATED = "VALIDATED"
    REJECTED = "REJECTED"
    PROMOTED_TO_CONTENT_CHANGE = "PROMOTED_TO_CONTENT_CHANGE"


class AuthorityLevel(StrEnum):
    """Classification of the source document's legal authority.

    ``PRIMARY``   -- Primary legislation (Acts, Codes, Statutes).
    ``SECONDARY`` -- Secondary legislation (Statutory Instruments, Regulations,
                     Orders, Decrees).
    ``GUIDANCE``  -- Revenue guidance, administrative rulings, briefs, notices.
    ``OTHER``     -- Source authority cannot be determined or does not fit the
                     above categories.
    """

    PRIMARY = "PRIMARY"
    SECONDARY = "SECONDARY"
    GUIDANCE = "GUIDANCE"
    OTHER = "OTHER"


# ---------------------------------------------------------------------------
# Error
# ---------------------------------------------------------------------------


class ChangeCandidateError(Exception):
    """Raised when the Change Intelligence Engine cannot proceed.

    Distinct from :class:`~ztax_gateway.governance.GovernanceRefusedError`
    (governance refusals) and
    :class:`~ztax_gateway.human_review.ReviewError` (queue precondition
    failures) so callers can route each failure type appropriately.
    """

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------------------
# EffectiveDateEvidence
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class EffectiveDateEvidence:
    """Structured evidence for the candidate effective date (ZTAX-AI-REQ-0049).

    Separates the evidence for *when* a change takes effect from the
    extraction confidence for *what* changed.

    ``candidate_date``  -- The date the engine believes the change becomes
                           effective.  May be ``None`` when the source does
                           not state an effective date explicitly.
    ``date_confidence`` -- Confidence in the extracted ``candidate_date``,
                           in ``[0.0, 1.0]``.  If ``candidate_date`` is
                           ``None`` this is ``0.0``.
    ``source_text``     -- The verbatim extract from the source document that
                           provided the effective-date evidence (e.g.
                           ``"with effect from 1 January 2026"``).  May be
                           empty when ``candidate_date`` is ``None``.
    """

    candidate_date: date | None
    date_confidence: float
    source_text: str = ""

    def __post_init__(self) -> None:
        if not (_MIN_CONFIDENCE <= self.date_confidence <= _MAX_CONFIDENCE):
            raise ChangeCandidateError(
                f"change: EffectiveDateEvidence.date_confidence must be in "
                f"[0.0, 1.0], got {self.date_confidence!r}"
            )
        if self.candidate_date is None and self.date_confidence != 0.0:
            raise ChangeCandidateError(
                "change: EffectiveDateEvidence.date_confidence must be 0.0 "
                "when candidate_date is None"
            )

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        return {
            "candidate_date": self.candidate_date.isoformat() if self.candidate_date else None,
            "date_confidence": self.date_confidence,
            "source_text": self.source_text,
        }


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _derive_candidate_id(
    source_document_id: str,
    target_rule_or_section: str,
    change_type: ChangeType,
    proposed_diff: str,
) -> str:
    """Derive a short, deterministic ID for a change candidate.

    The same ``(source_document_id, target_rule_or_section, change_type,
    proposed_diff)`` tuple always produces the same ``candidate_id``, which
    lets downstream layers detect already-reviewed candidates without a
    round-trip to persistent storage.
    """
    material = (
        f"{source_document_id}\0"
        f"{target_rule_or_section}\0"
        f"{change_type.value}\0"
        f"{proposed_diff}"
    ).encode()
    return hashlib.sha256(material).hexdigest()[:_CANDIDATE_ID_PREFIX_LEN]


# ---------------------------------------------------------------------------
# ChangeCandidate
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class ChangeCandidate:
    """An immutable proposed change extracted from a corpus diff.

    ``candidate_id`` is derived deterministically from ``source_document_id``,
    ``target_rule_or_section``, ``change_type`` and ``proposed_diff`` -- the
    same extraction always produces the same ID, enabling deduplication.

    ``source_document_id`` identifies the incoming document (e.g. a bulletin
    ID, document hash, or URL).

    ``target_rule_or_section`` names the rule, section, provision, or rate
    table that is proposed to change.

    ``change_type`` categorises the proposal as one of :class:`ChangeType`
    (RATE, DEFINITION, EFFECTIVE_DATE, FORM, THRESHOLD, OBLIGATION,
    PROCEDURE, or OTHER).

    ``proposed_diff`` is a structured or human-readable description of the
    change.  Must be non-empty.

    ``confidence_score`` is the engine's overall extraction confidence,
    expressed as a float in ``[0.0, 1.0]``.

    ``source_snapshot_before`` (``ZTAX-AI-REQ-0048``) is the
    :class:`~ztax_gateway.citation.Citation` pointing to the prior-state
    source material.  ``None`` for additions (there is no "before").

    ``source_snapshot_after`` (``ZTAX-AI-REQ-0048``) is the
    :class:`~ztax_gateway.citation.Citation` pointing to the incoming-state
    source material.  ``None`` for deletions (there is no "after").

    At least one of ``source_snapshot_before`` / ``source_snapshot_after``
    must be provided.

    ``effective_date_evidence`` (``ZTAX-AI-REQ-0049``) carries structured
    evidence for the date the change takes effect.  May be ``None`` when
    the source gives no effective-date signal.

    ``authority_level`` classifies the legal authority of the source
    document.  Defaults to ``OTHER``.

    ``impact_targets`` is a frozenset of strings naming the products,
    jurisdictions, or obligation types that may be affected (e.g.
    ``{"EU-VAT/reduced-rate", "EU-VAT/standard-rate"}``).  Empty means
    the impact scope is unknown.

    ``ai_release_manifest`` records which extractor or model version
    produced this candidate (e.g. ``"extractor:ci-engine@2026.09.25"``).
    Stored for audit and reproducibility.

    ``review_priority`` is the :class:`~ztax_gateway.human_review.ReviewPriority`
    that should be applied when the candidate is submitted to the review
    queue.  Defaults to ``NORMAL``.

    ``provenance`` is the governed context under which the extraction was
    performed.

    ``extracted_at`` is the UTC timestamp of extraction.
    """

    source_document_id: str
    target_rule_or_section: str
    change_type: ChangeType
    proposed_diff: str
    confidence_score: float
    provenance: Provenance
    source_snapshot_before: Citation | None = None
    source_snapshot_after: Citation | None = None
    effective_date_evidence: EffectiveDateEvidence | None = None
    authority_level: AuthorityLevel = AuthorityLevel.OTHER
    impact_targets: frozenset[str] = field(default_factory=frozenset)
    ai_release_manifest: str = ""
    review_priority: ReviewPriority = ReviewPriority.NORMAL
    candidate_id: str = field(init=False)
    extracted_at: datetime = field(init=False)

    def __post_init__(self) -> None:
        if not self.source_document_id:
            raise ChangeCandidateError(
                "change: source_document_id is required"
            )
        if not self.target_rule_or_section:
            raise ChangeCandidateError(
                f"change: target_rule_or_section is required "
                f"(source={self.source_document_id!r})"
            )
        if not self.proposed_diff:
            raise ChangeCandidateError(
                f"change: proposed_diff is required "
                f"(source={self.source_document_id!r}, "
                f"target={self.target_rule_or_section!r})"
            )
        if not (_MIN_CONFIDENCE <= self.confidence_score <= _MAX_CONFIDENCE):
            raise ChangeCandidateError(
                f"change: confidence_score must be in [0.0, 1.0], "
                f"got {self.confidence_score!r}"
            )
        if self.source_snapshot_before is None and self.source_snapshot_after is None:
            raise ChangeCandidateError(
                f"change: ChangeCandidate for {self.source_document_id!r} / "
                f"{self.target_rule_or_section!r} must carry at least one of "
                "source_snapshot_before or source_snapshot_after "
                "(ZTAX-AI-REQ-0048)"
            )
        # Derive deterministic candidate_id (frozen: must use object.__setattr__).
        object.__setattr__(
            self,
            "candidate_id",
            _derive_candidate_id(
                self.source_document_id,
                self.target_rule_or_section,
                self.change_type,
                self.proposed_diff,
            ),
        )
        object.__setattr__(self, "extracted_at", datetime.now(tz=UTC))

    @property
    def citation(self) -> Citation:
        """Canonical citation for this candidate.

        For a modification (both snapshots present) returns
        ``source_snapshot_after`` — the incoming state is the primary evidence.
        For an addition returns ``source_snapshot_after``.
        For a deletion returns ``source_snapshot_before``.
        """
        if self.source_snapshot_after is not None:
            return self.source_snapshot_after
        return self.source_snapshot_before  # type: ignore[return-value]

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        d: dict[str, object] = {
            "candidate_id": self.candidate_id,
            "source_document_id": self.source_document_id,
            "target_rule_or_section": self.target_rule_or_section,
            "change_type": self.change_type.value,
            "proposed_diff": self.proposed_diff,
            "confidence_score": self.confidence_score,
            "authority_level": self.authority_level.value,
            "review_priority": self.review_priority.value,
            "extracted_at": self.extracted_at.isoformat(),
            "use_case": self.provenance.use_case,
            "region": self.provenance.region,
            "authority_outcome": self.provenance.authority_outcome.value,
        }
        if self.source_snapshot_before is not None:
            d["source_snapshot_before"] = self.source_snapshot_before.citation_id
        if self.source_snapshot_after is not None:
            d["source_snapshot_after"] = self.source_snapshot_after.citation_id
        if self.effective_date_evidence is not None:
            d["effective_date_evidence"] = self.effective_date_evidence.as_dict()
        if self.impact_targets:
            d["impact_targets"] = sorted(self.impact_targets)
        if self.ai_release_manifest:
            d["ai_release_manifest"] = self.ai_release_manifest
        return d


# ---------------------------------------------------------------------------
# CandidateStatusRecord
# ---------------------------------------------------------------------------


class CandidateStatusRecord:
    """Mutable status wrapper around an immutable :class:`ChangeCandidate`.

    The status of a :class:`ChangeCandidate` (``PROPOSED → VALIDATED → …``)
    is tracked here, not on the frozen dataclass, so the immutability
    contract is preserved while status transitions remain auditable.

    ``status`` begins as ``PROPOSED`` on construction.
    ``transitioned_at`` is the UTC timestamp of the most recent status change.
    """

    __slots__ = ("candidate", "status", "transitioned_at")

    def __init__(self, candidate: ChangeCandidate) -> None:
        self.candidate = candidate
        self.status = CandidateStatus.PROPOSED
        self.transitioned_at: datetime = datetime.now(tz=UTC)

    def transition(self, new_status: CandidateStatus) -> None:
        """Transition to *new_status* and record the timestamp.

        Raises:
            ChangeCandidateError: if the transition is not permitted.
        """
        allowed: dict[CandidateStatus, frozenset[CandidateStatus]] = {
            CandidateStatus.PROPOSED: frozenset({
                CandidateStatus.VALIDATED, CandidateStatus.REJECTED,
            }),
            CandidateStatus.VALIDATED: frozenset({
                CandidateStatus.PROMOTED_TO_CONTENT_CHANGE, CandidateStatus.REJECTED,
            }),
            CandidateStatus.REJECTED: frozenset(),
            CandidateStatus.PROMOTED_TO_CONTENT_CHANGE: frozenset(),
        }
        if new_status not in allowed[self.status]:
            raise ChangeCandidateError(
                f"change: cannot transition candidate "
                f"{self.candidate.candidate_id!r} from "
                f"{self.status.value!r} to {new_status.value!r}"
            )
        self.status = new_status
        self.transitioned_at = datetime.now(tz=UTC)


# ---------------------------------------------------------------------------
# CorpusEntry
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class CorpusEntry:
    """An immutable entry in a document corpus.

    ``document_id`` is the stable identifier for this document.

    ``section_id`` identifies the sub-section or rule within the document.
    Together with ``document_id`` it uniquely addresses one reviewable unit
    of content.

    ``content`` is the textual representation of this section.

    ``citation`` is the :class:`~ztax_gateway.citation.Citation` linking
    this section back to the source material it was extracted from.
    """

    document_id: str
    section_id: str
    content: str
    citation: Citation

    def __post_init__(self) -> None:
        if not self.document_id:
            raise ChangeCandidateError("change: CorpusEntry.document_id is required")
        if not self.section_id:
            raise ChangeCandidateError(
                f"change: CorpusEntry.section_id is required "
                f"(document={self.document_id!r})"
            )
        if not self.content:
            raise ChangeCandidateError(
                f"change: CorpusEntry.content is required "
                f"(document={self.document_id!r}, section={self.section_id!r})"
            )


# ---------------------------------------------------------------------------
# ChangeIntelligenceEngine
# ---------------------------------------------------------------------------

# Type-order for result sorting: additions/definitions first, then rate/threshold,
# then obligation/procedure, then modifications/effective-date/form, then deletions.
_TYPE_ORDER: dict[ChangeType, int] = {
    ChangeType.RATE: 0,
    ChangeType.THRESHOLD: 1,
    ChangeType.DEFINITION: 2,
    ChangeType.OBLIGATION: 3,
    ChangeType.EFFECTIVE_DATE: 4,
    ChangeType.PROCEDURE: 5,
    ChangeType.FORM: 6,
    ChangeType.OTHER: 7,
}


class ChangeIntelligenceEngine:
    """Governance-gated change-extraction pipeline.

    :meth:`extract_changes` compares an *incoming corpus* against a *prior
    corpus*, emits :class:`ChangeCandidate` records for every detected
    difference, and returns them as an immutable tuple.  It **does not**
    promote candidates -- promotion goes through
    :class:`~ztax_gateway.human_review.ReviewQueue`.

    :meth:`to_review_item` converts a :class:`ChangeCandidate` into a
    :class:`~ztax_gateway.human_review.ReviewItem` ready to be submitted to
    a :class:`~ztax_gateway.human_review.ReviewQueue`.  The method attaches
    an :class:`~ztax_gateway.human_review.EvidencePanel` that shows
    ``source_snapshot_before`` and ``source_snapshot_after`` side-by-side.

    The engine is stateless: all per-call context travels in the arguments.

    Args:
        min_confidence: Candidates whose ``confidence_score`` is strictly
            below this threshold are dropped.  Defaults to ``0.0`` (all
            candidates are returned).  Must be in ``[0.0, 1.0]``.
        ai_release_manifest: Extractor/model version identifier stamped into
            every :class:`ChangeCandidate` produced by this instance.
    """

    def __init__(
        self,
        min_confidence: float = 0.0,
        ai_release_manifest: str = "",
    ) -> None:
        if not (_MIN_CONFIDENCE <= min_confidence <= _MAX_CONFIDENCE):
            raise ChangeCandidateError(
                f"change: min_confidence must be in [0.0, 1.0], "
                f"got {min_confidence!r}"
            )
        self._min_confidence = min_confidence
        self._ai_release_manifest = ai_release_manifest

    # ------------------------------------------------------------------
    # extract_changes
    # ------------------------------------------------------------------

    def extract_changes(
        self,
        prior_corpus: Sequence[CorpusEntry],
        incoming_corpus: Sequence[CorpusEntry],
        provenance: Provenance,
        registry: UseCaseRegistry,
    ) -> tuple[ChangeCandidate, ...]:
        """Diff *incoming_corpus* against *prior_corpus* and return candidates.

        **Governance gate:** :func:`~ztax_gateway.governance.authorise` is
        called as the very first action -- before any corpus inspection.

        Steps:

        1. Authorise.
        2. Index the prior corpus by ``(document_id, section_id)``.
        3. Index the incoming corpus by ``(document_id, section_id)``.
        4. Sections only in incoming corpus  -> ADDITION-class candidate
           (``source_snapshot_before=None``).
        5. Sections only in prior corpus     -> OTHER candidate
           (``source_snapshot_after=None``).
        6. Sections in both but with changed content -> heuristic
           classification: RATE, THRESHOLD, EFFECTIVE_DATE, or OTHER.
           Both citations are stored.
        7. Filter by ``min_confidence``.
        8. Return as immutable sorted tuple.

        Args:
            prior_corpus:    The existing document corpus.
            incoming_corpus: The new document corpus to diff against.
            provenance:      The governed context.
            registry:        The :class:`~ztax_gateway.governance.UseCaseRegistry`.

        Returns:
            A (possibly empty) tuple of :class:`ChangeCandidate` records,
            ordered by :data:`_TYPE_ORDER` and then
            ``target_rule_or_section``.

        Raises:
            GovernanceRefusedError: if *provenance* is not authorised.
            ChangeCandidateError:   if any corpus entry is malformed.
        """
        # 1. Governance gate -- before any work.
        authorise(registry, provenance)

        # 2. Index prior corpus.
        prior: dict[tuple[str, str], CorpusEntry] = {}
        for entry in prior_corpus:
            key = (entry.document_id, entry.section_id)
            prior[key] = entry

        # 3. Index incoming corpus.
        incoming: dict[tuple[str, str], CorpusEntry] = {}
        for entry in incoming_corpus:
            key = (entry.document_id, entry.section_id)
            incoming[key] = entry

        candidates: list[ChangeCandidate] = []

        # 4. Additions -- sections only in the incoming corpus.
        for key, entry in sorted(incoming.items()):
            if key not in prior:
                candidates.append(
                    ChangeCandidate(
                        source_document_id=entry.document_id,
                        target_rule_or_section=entry.section_id,
                        change_type=ChangeType.OTHER,
                        proposed_diff=f"New section added:\n{entry.content}",
                        confidence_score=0.9,
                        provenance=provenance,
                        source_snapshot_before=None,
                        source_snapshot_after=entry.citation,
                        ai_release_manifest=self._ai_release_manifest,
                    )
                )

        # 5 & 6. Modifications / deletions.
        for key, prior_entry in sorted(prior.items()):
            if key not in incoming:
                # 5. Deletion -- no "after" state.
                candidates.append(
                    ChangeCandidate(
                        source_document_id=prior_entry.document_id,
                        target_rule_or_section=prior_entry.section_id,
                        change_type=ChangeType.OTHER,
                        proposed_diff=f"Section removed:\n{prior_entry.content}",
                        confidence_score=0.9,
                        provenance=provenance,
                        source_snapshot_before=prior_entry.citation,
                        source_snapshot_after=None,
                        ai_release_manifest=self._ai_release_manifest,
                    )
                )
            else:
                # 6. Potential modification -- store both citations.
                incoming_entry = incoming[key]
                if prior_entry.content != incoming_entry.content:
                    change_type = _classify_content_change(
                        prior_entry.content, incoming_entry.content
                    )
                    candidates.append(
                        ChangeCandidate(
                            source_document_id=incoming_entry.document_id,
                            target_rule_or_section=incoming_entry.section_id,
                            change_type=change_type,
                            proposed_diff=(
                                f"Before:\n{prior_entry.content}"
                                f"\n\nAfter:\n{incoming_entry.content}"
                            ),
                            confidence_score=0.85,
                            provenance=provenance,
                            source_snapshot_before=prior_entry.citation,
                            source_snapshot_after=incoming_entry.citation,
                            ai_release_manifest=self._ai_release_manifest,
                        )
                    )

        # 7. Filter by confidence threshold.
        filtered = [
            c for c in candidates if c.confidence_score >= self._min_confidence
        ]

        # 8. Sort and return as immutable tuple.
        filtered.sort(
            key=lambda c: (_TYPE_ORDER.get(c.change_type, 99), c.target_rule_or_section)
        )
        return tuple(filtered)

    # ------------------------------------------------------------------
    # to_review_item
    # ------------------------------------------------------------------

    def to_review_item(
        self,
        candidate: ChangeCandidate,
        queue: ReviewQueue,
        registry: UseCaseRegistry,
    ) -> ReviewItem:
        """Convert *candidate* into a :class:`~ztax_gateway.human_review.ReviewItem`.

        The item is immediately submitted to *queue*.  The caller is
        responsible for writing the returned :class:`ReviewItem` to durable
        storage.

        An :class:`~ztax_gateway.human_review.EvidencePanel` is attached
        showing the before-state content from ``source_snapshot_before`` and
        the after-state content from ``source_snapshot_after``, making the
        side-by-side comparison available to the reviewer without re-running
        the extraction.

        The ``output_id`` of the submitted :class:`ReviewItem` is the
        candidate's ``candidate_id`` so the two records can be joined in an
        audit log without a separate lookup.

        The ``review_priority`` from the :class:`ChangeCandidate` is
        forwarded to the queue submission.

        Args:
            candidate: The :class:`ChangeCandidate` to submit for review.
            queue:     The :class:`~ztax_gateway.human_review.ReviewQueue`.
            registry:  The :class:`~ztax_gateway.governance.UseCaseRegistry`.

        Returns:
            The frozen :class:`~ztax_gateway.human_review.ReviewItem`.

        Raises:
            GovernanceRefusedError: if the candidate's provenance is not
                authorised.
            ReviewError:            if the candidate is already in the queue.
        """
        diff_preview = candidate.proposed_diff[:120]
        ellipsis_ = "..." if len(candidate.proposed_diff) > 120 else ""
        summary = (
            f"[{candidate.change_type.value}] "
            f"{candidate.target_rule_or_section} "
            f"in {candidate.source_document_id} "
            f"(confidence={candidate.confidence_score:.0%}): "
            f"{diff_preview}{ellipsis_}"
        )

        # Build EvidencePanel: ai_answer = incoming ("after") state,
        # rule_answer = prior ("before") state.
        before_text = (
            f"snapshot:{candidate.source_snapshot_before.citation_id}"
            if candidate.source_snapshot_before is not None
            else "(no prior snapshot — new addition)"
        )
        after_text = (
            f"snapshot:{candidate.source_snapshot_after.citation_id}"
            if candidate.source_snapshot_after is not None
            else "(no incoming snapshot — deleted section)"
        )
        panel = EvidencePanel(
            ai_answer=after_text,
            rule_answer=before_text,
        )

        return queue.submit(
            output_id=candidate.candidate_id,
            citation=candidate.citation,
            summary=summary,
            provenance=candidate.provenance,
            registry=registry,
            evidence_panel=panel,
            priority=candidate.review_priority,
        )


# ---------------------------------------------------------------------------
# Internal heuristics
# ---------------------------------------------------------------------------

# Keywords that suggest a numeric rate changed (case-insensitive).
_RATE_KEYWORDS: frozenset[str] = frozenset(
    {"%", "rate", "percent", "vat", "gst", "withholding", "levy"}
)

# Keywords that suggest a numeric threshold changed (case-insensitive).
_THRESHOLD_KEYWORDS: frozenset[str] = frozenset(
    {"threshold", "limit", "minimum", "maximum", "ceiling", "floor", "de minimis"}
)

# Keywords that suggest an effective-date change (case-insensitive).
_EFFECTIVE_DATE_KEYWORDS: frozenset[str] = frozenset(
    {"with effect from", "effective from", "comes into force", "applies from",
     "in force on", "effective date", "commencement"}
)


def _classify_content_change(prior_content: str, incoming_content: str) -> ChangeType:
    """Heuristically classify a content change as RATE, THRESHOLD,
    EFFECTIVE_DATE, or OTHER.

    Checks the combined content for keyword signals.  A production deployment
    would substitute a structured diff algorithm and/or a specialist model;
    this interface is stable regardless of what sits inside it.
    """
    combined = (prior_content + " " + incoming_content).lower()
    if any(kw in combined for kw in _RATE_KEYWORDS):
        return ChangeType.RATE
    if any(kw in combined for kw in _THRESHOLD_KEYWORDS):
        return ChangeType.THRESHOLD
    if any(kw in combined for kw in _EFFECTIVE_DATE_KEYWORDS):
        return ChangeType.EFFECTIVE_DATE
    return ChangeType.OTHER
