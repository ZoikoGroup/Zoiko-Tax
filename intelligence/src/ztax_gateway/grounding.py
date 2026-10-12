"""Citation Resolver and Grounding Validator.

Master Specification Ch.17 ss9.

Every AI output that cites a source must be *grounded*: the citation must be
traceable to bytes that actually exist in the source snapshot, and the
citation_id must be the one the engine would have computed from those bytes.
This module provides two tools to enforce that guarantee:

``resolve_citation``
    Given a :class:`~ztax_gateway.citation.Citation` and a mapping of source
    snapshots (``source_id -> bytes``), verifies that every
    :class:`~ztax_gateway.citation.SourceChunk` in the citation:

    1. the citation contains at least one chunk (``EMPTY_CITATION``),
    2. refers to a source that exists in the snapshot mapping,
    3. has byte offsets that lie within the snapshot,
    4. has content that matches the snapshot bytes at those offsets, and
    5. has a ``citation_id`` that matches the one the engine would compute
       from the chunks (catching a Citation built directly with a forged id).

    Returns a tuple of :class:`ResolvedChunk` objects, one per chunk, each
    carrying 1-based line numbers derived from the snapshot bytes.

``GroundingValidator``
    Given a mapping of *issued* citations (id -> Citation) and an optional
    snapshot mapping, checks whether a list of cited ids is fully grounded:

    - An empty cited-ids list with ``required=True`` is a grounding failure
      (the model must cite something).
    - A cited id that is not in the issued map is a grounding failure (the
      model cannot invent evidence).
    - If a snapshot mapping was provided, every cited issued citation is
      resolved and any :class:`GroundingError` is recorded in the result.

Scope note
----------
This module decides whether a citation *structurally exists and byte-matches*
its source.  It does NOT decide whether the cited text *materially supports the
claim* -- that requires semantic judgment by a model or human reviewer and is
deliberately out of scope here.

Design rules
------------
* Pure and deterministic: no model calls, no I/O beyond what callers pass in.
* All data-carrying types are frozen dataclasses -- immutable evidence.
* No imports from fiscal, tax_decision, or subledger packages (ADR-0006 s2.6).
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from enum import StrEnum

from .citation import Citation, CitationError, combine

__all__: list[str] = [
    "GroundingError",
    "GroundingFailure",
    "GroundingIssue",
    "GroundingResult",
    "GroundingValidator",
    "ResolvedChunk",
    "resolve_citation",
]


# ---------------------------------------------------------------------------
# Failure codes
# ---------------------------------------------------------------------------


class GroundingFailure(StrEnum):
    """Enumeration of reasons why a citation fails grounding.

    ``EMPTY_CITATION``
        The citation contains no chunks at all.  A citation must reference at
        least one source chunk.

    ``SOURCE_MISSING``
        The chunk's ``source_id`` was not found in the snapshot mapping.

    ``RANGE_OUT_OF_BOUNDS``
        ``byte_start`` or ``byte_end`` lies outside the snapshot's byte range.

    ``CONTENT_MISMATCH``
        The snapshot bytes at ``[byte_start:byte_end]`` differ from
        ``chunk.content`` -- the source has changed since the citation was
        issued.

    ``ID_MISMATCH``
        The ``citation_id`` stored on the :class:`~ztax_gateway.citation.Citation`
        does not match the id that :func:`~ztax_gateway.citation.combine` would
        compute from its own chunks.  This catches citations that were
        constructed directly with a forged id.

    ``UNISSUED_CITATION``
        A cited id was not found in the set of citations issued by the engine.
        The model cannot invent evidence.

    ``NO_CITATION``
        No citation ids were supplied at all when at least one was required.
    """

    EMPTY_CITATION = "EMPTY_CITATION"
    SOURCE_MISSING = "SOURCE_MISSING"
    RANGE_OUT_OF_BOUNDS = "RANGE_OUT_OF_BOUNDS"
    CONTENT_MISMATCH = "CONTENT_MISMATCH"
    ID_MISMATCH = "ID_MISMATCH"
    UNISSUED_CITATION = "UNISSUED_CITATION"
    NO_CITATION = "NO_CITATION"


# ---------------------------------------------------------------------------
# Data structures
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class ResolvedChunk:
    """A successfully resolved source chunk with 1-based line numbers.

    ``source_id``   -- the source identifier from the original SourceChunk.
    ``byte_start``  -- byte offset of the chunk start in the source snapshot.
    ``byte_end``    -- byte offset of the chunk end (exclusive).
    ``line_start``  -- 1-based line number of the first byte of the chunk.
    ``line_end``    -- 1-based line number of the last byte of the chunk
                       (or the same as line_start for a single-line chunk).
    """

    source_id: str
    byte_start: int
    byte_end: int
    line_start: int
    line_end: int


@dataclass(frozen=True, slots=True)
class GroundingIssue:
    """A single grounding failure recorded by :class:`GroundingValidator`.

    ``citation_id`` -- the id of the citation that triggered this issue, or
                       ``None`` for a ``NO_CITATION`` failure (where no id
                       was supplied at all).
    ``failure``     -- the :class:`GroundingFailure` code.
    ``detail``      -- a human-readable explanation of the failure.
    """

    citation_id: str | None
    failure: GroundingFailure
    detail: str


@dataclass(frozen=True, slots=True)
class GroundingResult:
    """Result of :meth:`GroundingValidator.check`.

    ``ok``        -- True iff there are no issues (no unissued ids, no
                     resolution failures, no missing citations).
    ``unissued``  -- Tuple of cited ids that were not found in the issued map.
                     Kept for backward compatibility; each unissued id also
                     appears as a :class:`GroundingIssue` in ``failures``.
    ``failures``  -- Tuple of :class:`GroundingIssue` objects describing every
                     grounding failure encountered, ordered by first occurrence.
    """

    ok: bool
    unissued: tuple[str, ...]
    failures: tuple[GroundingIssue, ...]


# ---------------------------------------------------------------------------
# Error
# ---------------------------------------------------------------------------


class GroundingError(Exception):
    """Raised by :func:`resolve_citation` when a chunk fails verification.

    ``failure`` is the :class:`GroundingFailure` code.
    ``detail``  is a human-readable explanation.
    """

    def __init__(self, failure: GroundingFailure, detail: str) -> None:
        super().__init__(f"{failure}: {detail}")
        self.failure = failure
        self.detail = detail


# ---------------------------------------------------------------------------
# Private helpers
# ---------------------------------------------------------------------------


def _line_number(snapshot: bytes, byte_offset: int) -> int:
    """Return the 1-based line number of *byte_offset* within *snapshot*.

    Lines are delimited by ``\\n``.  A file with no newlines is entirely
    line 1.  The offset is clamped to ``[0, len(snapshot)]`` before counting.
    """
    clamped = min(max(byte_offset, 0), len(snapshot))
    # count newlines before the offset
    return snapshot[:clamped].count(b"\n") + 1


def _recompute_id(citation: Citation) -> str:
    """Return the citation_id that combine() would produce for citation.chunks.

    Raises:
        GroundingError: with failure ID_MISMATCH if combine() itself raises
            CitationError (e.g. the chunks tuple is empty).
    """
    try:
        return combine(list(citation.chunks)).citation_id
    except CitationError as exc:
        raise GroundingError(
            GroundingFailure.ID_MISMATCH,
            f"cannot recompute citation id: {exc}",
        ) from exc


# ---------------------------------------------------------------------------
# resolve_citation
# ---------------------------------------------------------------------------


def resolve_citation(
    citation: Citation,
    snapshots: Mapping[str, bytes],
) -> tuple[ResolvedChunk, ...]:
    """Verify every chunk in *citation* against *snapshots* and return resolved chunks.

    Checks performed, in order:

    1. ``EMPTY_CITATION``     -- ``citation.chunks`` is empty.
    2. ``ID_MISMATCH``        -- ``citation.citation_id`` != recomputed id.
    3. ``SOURCE_MISSING``     -- ``chunk.source_id`` not in *snapshots*.
    4. ``RANGE_OUT_OF_BOUNDS``-- ``byte_start`` or ``byte_end`` outside snapshot.
    5. ``CONTENT_MISMATCH``   -- snapshot bytes != ``chunk.content``.

    The EMPTY_CITATION and ID_MISMATCH checks happen once before iterating
    chunks.  All other checks are per-chunk and raise on the first failure.

    Args:
        citation:  The Citation to verify.
        snapshots: Mapping of ``source_id`` to raw bytes of that source.

    Returns:
        A tuple of :class:`ResolvedChunk` objects in chunk order.

    Raises:
        GroundingError: on the first verification failure encountered.
    """
    # 1. Citation must have at least one chunk.
    if not citation.chunks:
        raise GroundingError(
            GroundingFailure.EMPTY_CITATION,
            f"citation {citation.citation_id!r} contains no chunks"
            " -- a citation must reference at least one source chunk",
        )

    # 2. Check ID_MISMATCH before touching individual chunks.
    #    _recompute_id raises GroundingError(ID_MISMATCH) on CitationError.
    expected_id = _recompute_id(citation)
    if citation.citation_id != expected_id:
        raise GroundingError(
            GroundingFailure.ID_MISMATCH,
            f"citation_id {citation.citation_id!r} does not match recomputed"
            f" id {expected_id!r} -- citation may have been constructed with"
            " a forged id",
        )

    resolved: list[ResolvedChunk] = []
    for chunk in citation.chunks:
        sid = chunk.source_id

        # 3. Source must exist in snapshots.
        if sid not in snapshots:
            raise GroundingError(
                GroundingFailure.SOURCE_MISSING,
                f"source_id {sid!r} not found in snapshot mapping",
            )

        snapshot = snapshots[sid]
        snap_len = len(snapshot)

        # 4. Byte range must lie within the snapshot.
        if chunk.byte_start < 0 or chunk.byte_end > snap_len or chunk.byte_start > chunk.byte_end:
            raise GroundingError(
                GroundingFailure.RANGE_OUT_OF_BOUNDS,
                f"source_id {sid!r}: byte range [{chunk.byte_start}:{chunk.byte_end}]"
                f" is out of bounds for snapshot of length {snap_len}",
            )

        # 5. Snapshot bytes must match chunk content.
        actual = snapshot[chunk.byte_start : chunk.byte_end]
        if actual != chunk.content:
            raise GroundingError(
                GroundingFailure.CONTENT_MISMATCH,
                f"source_id {sid!r}: snapshot bytes at"
                f" [{chunk.byte_start}:{chunk.byte_end}] do not match"
                " chunk.content -- source has changed since the citation was issued",
            )

        # Compute 1-based line numbers.
        line_start = _line_number(snapshot, chunk.byte_start)
        # line_end: last byte is byte_end - 1 (exclusive end).
        last_byte = max(chunk.byte_start, chunk.byte_end - 1)
        line_end = _line_number(snapshot, last_byte)

        resolved.append(
            ResolvedChunk(
                source_id=sid,
                byte_start=chunk.byte_start,
                byte_end=chunk.byte_end,
                line_start=line_start,
                line_end=line_end,
            )
        )

    return tuple(resolved)


# ---------------------------------------------------------------------------
# GroundingValidator
# ---------------------------------------------------------------------------


class GroundingValidator:
    """Checks that a list of citation ids is fully grounded.

    Args:
        issued:    Mapping of ``citation_id -> Citation`` for every citation
                   that the engine has issued in this session.  The model may
                   only cite ids that appear here.
        snapshots: Optional mapping of ``source_id -> bytes``.  When supplied,
                   every cited issued citation is passed through
                   :func:`resolve_citation`; any :class:`GroundingError` is
                   recorded as a :class:`GroundingIssue` in the result.
    """

    def __init__(
        self,
        issued: Mapping[str, Citation],
        snapshots: Mapping[str, bytes] | None = None,
    ) -> None:
        self._issued = issued
        self._snapshots = snapshots

    def check(
        self,
        cited_ids: Sequence[str],
        *,
        required: bool = True,
    ) -> GroundingResult:
        """Check *cited_ids* for grounding.

        Args:
            cited_ids: The citation ids the model claimed to use.  Duplicates
                       are allowed; results are deterministic and ordered by
                       first occurrence.
            required:  If ``True`` and *cited_ids* is empty, the result
                       records a ``NO_CITATION`` issue and ``ok=False``.
                       If ``False``, an empty list is accepted.

        Returns:
            A :class:`GroundingResult` with ``ok=True`` iff there are no
            unissued ids and no resolution failures.  Never raises for
            malformed or forged citations in ``issued``; those are recorded
            as :class:`GroundingIssue` objects instead.
        """
        issues: list[GroundingIssue] = []
        unissued: list[str] = []

        # required=True with no citations -> NO_CITATION.
        if required and not cited_ids:
            return GroundingResult(
                ok=False,
                unissued=(),
                failures=(
                    GroundingIssue(
                        citation_id=None,
                        failure=GroundingFailure.NO_CITATION,
                        detail="no citation ids were supplied but at least one is required",
                    ),
                ),
            )

        # Track which ids we have already processed (for deduplication).
        seen_ids: set[str] = set()

        for cid in cited_ids:
            if cid in seen_ids:
                continue
            seen_ids.add(cid)

            if cid not in self._issued:
                unissued.append(cid)
                issues.append(
                    GroundingIssue(
                        citation_id=cid,
                        failure=GroundingFailure.UNISSUED_CITATION,
                        detail=f"citation id {cid!r} was not issued by the engine"
                        " -- the model may have invented this reference",
                    )
                )
                continue

            # Resolve against snapshots if provided.
            if self._snapshots is not None:
                try:
                    resolve_citation(self._issued[cid], self._snapshots)
                except GroundingError as exc:
                    issues.append(
                        GroundingIssue(
                            citation_id=cid,
                            failure=exc.failure,
                            detail=exc.detail,
                        )
                    )

        ok = not issues
        return GroundingResult(
            ok=ok,
            unissued=tuple(unissued),
            failures=tuple(issues),
        )
