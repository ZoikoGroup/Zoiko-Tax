"""Provenance RAG -- Knowledge-Base over the ZoikoTax specification documents.

Master Specification Ch.17 §9/§10.

Every AI output that references a tax rule, a determination algorithm, or a
jurisdiction requirement must be traceable to a specific chunk of an official
spec document.  This module provides that traceability by:

1. **Ingesting** spec documents (Markdown files from ``docs/specs/``) into an
   in-process SQLite FTS5 full-text index.  No external vector database, no
   network call, no embedding model -- CI can run it offline.

2. **Searching** the index with a plain-text query and returning a ranked list
   of :class:`~ztax_gateway.citation.Citation` objects, one per matching chunk.
   Because results are ``Citation`` objects, every downstream use carries a
   tamper-evident reference to the source material that justified it.

3. **Enforcing the fiscal boundary** -- this module imports nothing from the
   fiscal, tax_decision or subledger packages (ADR-0006 s2.6; the CI grep
   enforces this).

4. **Valid-time filtering** (Master Specification Ch.17 §9/§10) --
   :meth:`KnowledgeBase.search` applies a temporal filter based on the ``mode``
   parameter:

   * ``TemporalMode.CURRENT`` (default) -- returns chunks that are IN_FORCE
     right now: ``effective_from <= _utcnow() < effective_to``.  Open bounds
     are treated as open-ended (-inf / +inf).
   * ``TemporalMode.AS_KNOWN_THEN`` -- requires a timezone-aware ``as_of``
     datetime; returns chunks that were IN_FORCE at that point in time.

   Setting ``historical_context=True`` additionally returns SUPERSEDED chunks
   (those whose ``effective_to`` has passed) tagged with
   ``TemporalStatus.SUPERSEDED``.  NOT_YET_EFFECTIVE chunks are never returned.

5. **Index lifecycle** (Master Specification Ch.17 §9/§10) -- the index is
   read-only after :meth:`KnowledgeBase.build` *except* for lifecycle
   operations, which only ever **remove** content:

   * :meth:`KnowledgeBase.retire` -- soft-removes a source so it never appears
     in any search (including ``historical_context=True``).  Chunk content is
     retained for audit purposes.
   * :meth:`KnowledgeBase.purge` -- physically deletes a source's chunk text
     from the index and from every in-memory structure.  Only an immutable
     :class:`Tombstone` remains.
   * :meth:`KnowledgeBase.tombstones` -- returns an append-only audit trail
     of every retire and purge operation in operation order.

Design rules
------------
* **Immutable index** -- once :meth:`KnowledgeBase.build` returns, the index is
  read-only except for lifecycle operations that only ever remove content.
  Attempting to ingest a document twice is a programming error, not a runtime
  condition.  Attempting to re-ingest a retired or purged source is also an
  error (a withdrawn source needs a new index generation).
* **Chunk granularity** -- documents are split at Markdown heading boundaries
  (``#``, ``##``, ``###``).  Each heading + its body is one chunk, one
  ``SourceChunk``, one ``Citation``.
* **No embeddings** -- SQLite FTS5 BM25 ranking is sufficient for the spec
  corpus (two documents, ~840 lines, ~56 KB total).  Embeddings are reserved
  for the W2 lane L upgrade.
* **Deterministic citation IDs** -- given the same file content, the same query
  always produces the same :class:`~ztax_gateway.citation.Citation` objects, so
  audit logs can be replayed and compared.
* **Effective-window storage** -- ``effective_from`` and ``effective_to`` are
  stored as ISO-8601 UTC strings (``YYYY-MM-DDTHH:MM:SS``) in two UNINDEXED
  SQLite columns.  ``NULL`` effective_to means the chunk has no expiry.
  Filtering is done in Python (not SQL) to avoid SQLite datetime ambiguities.
* **Clock injection** -- _utcnow() is the single point of truth for "now" in
  CURRENT mode so tests can monkeypatch it without touching the system clock.
"""

from __future__ import annotations

import enum
import hashlib
import re
import sqlite3
from dataclasses import dataclass, field
from datetime import UTC, datetime
from pathlib import Path
from typing import Final

from .citation import Citation, SourceChunk, combine
from .privacy_source_rights import (
    CustomerPolicy,
    PrivacySourceRightsError,
    RightsProfile,
    check_corpus_source,
)

__all__: list[str] = [
    "Chunk",
    "KnowledgeBase",
    "KnowledgeBaseError",
    "LifecycleAction",
    "LifecycleReason",
    "SearchResult",
    "TemporalMode",
    "TemporalStatus",
    "Tombstone",
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# Maximum number of results returned by a single search call.
_DEFAULT_TOP_K: Final[int] = 5

# Minimum query length -- single-character queries are never meaningful.
_MIN_QUERY_LEN: Final[int] = 2

# Regex that matches a Markdown heading line (ATX style, levels 1-3).
_HEADING_RE: re.Pattern[str] = re.compile(r"^#{1,3}\s+.+", re.MULTILINE)

# ISO-8601 format used for effective-date storage in SQLite.
_ISO_FMT: Final[str] = "%Y-%m-%dT%H:%M:%S"


# ---------------------------------------------------------------------------
# Clock helper (monkeypatchable in tests)
# ---------------------------------------------------------------------------


def _utcnow() -> datetime:
    """Return the current UTC time.

    This is the single point of truth for CURRENT mode so that tests can
    monkeypatch ``ztax_gateway.rag._utcnow`` without touching the system clock.
    """
    return datetime.now(tz=UTC)


# ---------------------------------------------------------------------------
# Enums
# ---------------------------------------------------------------------------


class LifecycleAction(enum.StrEnum):
    """The action recorded in a lifecycle :class:`Tombstone` (Master Specification Ch.17 §9/§10).

    ``RETIRE``
        Soft-remove: the source is hidden from every search but its chunk text
        is retained for audit.

    ``PURGE``
        Hard-remove: chunk text is physically deleted.  Only the tombstone
        remains.
    """

    RETIRE = "RETIRE"
    PURGE = "PURGE"


class LifecycleReason(enum.StrEnum):
    """The business reason that drove a lifecycle operation (Master Specification Ch.17 §9/§10).

    ``LICENCE_WITHDRAWN``
        The content licence has been revoked or has expired.

    ``PRIVACY_DELETION``
        A data-subject deletion request (e.g. GDPR right-to-erasure) requires
        the content to be removed.

    ``PACK_WITHDRAWN``
        The document pack or product that contained this source has been
        decommissioned.
    """

    LICENCE_WITHDRAWN = "LICENCE_WITHDRAWN"
    PRIVACY_DELETION = "PRIVACY_DELETION"
    PACK_WITHDRAWN = "PACK_WITHDRAWN"


class TemporalMode(enum.Enum):
    """Valid-time search mode (Chapter 17 ss9).

    ``CURRENT``
        Filter by the current wall-clock time.  Returns only IN_FORCE chunks
        (effective_from <= now < effective_to).  ``as_of`` must be ``None``.

    ``AS_KNOWN_THEN``
        Filter by a historical point in time supplied via ``as_of``.  Returns
        chunks that were IN_FORCE at that moment.  ``as_of`` must be a
        timezone-aware datetime.
    """

    CURRENT = "CURRENT"
    AS_KNOWN_THEN = "AS_KNOWN_THEN"


class TemporalStatus(enum.Enum):
    """Valid-time status of a chunk relative to the search point in time.

    ``IN_FORCE``
        The chunk is valid at the query point in time.

    ``SUPERSEDED``
        The chunk's effective_to has passed; it is no longer in force but is
        returned when ``historical_context=True``.

    ``NOT_YET_EFFECTIVE``
        The chunk's effective_from is in the future; never returned.
    """

    IN_FORCE = "IN_FORCE"
    SUPERSEDED = "SUPERSEDED"
    NOT_YET_EFFECTIVE = "NOT_YET_EFFECTIVE"


# ---------------------------------------------------------------------------
# Data structures
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class Tombstone:
    """Immutable audit record created by :meth:`KnowledgeBase.retire` or
    :meth:`KnowledgeBase.purge` (Master Specification Ch.17 §9/§10).

    ``source_id``
        The posix-path identifier of the retired or purged source.

    ``action``
        :attr:`LifecycleAction.RETIRE` or :attr:`LifecycleAction.PURGE`.

    ``reason``
        The business reason for the lifecycle operation.

    ``at``
        Timezone-aware UTC datetime at which the operation was recorded.

    ``content_sha256``
        Deterministic SHA-256 over the source's chunk ``content_hash`` values
        concatenated in chunk-id order (the order they were inserted).  Allows
        an auditor to verify which version of the content was removed.

    ``chunk_count``
        Number of chunks that were present in the source at the time of the
        operation.
    """

    source_id: str
    action: LifecycleAction
    reason: LifecycleReason
    at: datetime
    content_sha256: str
    chunk_count: int


@dataclass(frozen=True, slots=True)
class Chunk:
    """A single indexed unit: one heading section from one spec document.

    ``source_id`` is the canonical identifier for the document (its path
    relative to the repository root, forward-slash separated).

    ``heading`` is the title of this section with the leading ATX ``#``
    markers and the following space stripped
    (e.g. ``"2. The determination pipeline"`` for ``## 2. The determination pipeline``).
    This ensures the value is a clean identifier suitable for exact-match
    scoring — consistent with the ``expected_class`` strings in the gold set.

    ``body`` is the full text of this section (heading + content lines).

    ``byte_start`` / ``byte_end`` are byte offsets into the *original* file.

    ``effective_from`` is the UTC datetime from which this chunk is considered
    in force.  ``None`` means the chunk has always been in force (no lower
    bound).

    ``effective_to`` is the UTC datetime at which this chunk is superseded
    (exclusive).  ``None`` means the chunk has no expiry.
    """

    source_id: str
    heading: str
    body: str
    byte_start: int
    byte_end: int
    effective_from: datetime | None = None
    effective_to: datetime | None = None


@dataclass(frozen=True, slots=True)
class SearchResult:
    """One ranked result from :meth:`KnowledgeBase.search`.

    ``chunk`` is the matching :class:`Chunk`.
    ``citation`` is the tamper-evident :class:`Citation` for that chunk.
    ``rank`` is the BM25 score as returned by SQLite FTS5 (lower is better).
    ``temporal_status`` is the valid-time status of the chunk relative to the
    search point in time.  Always ``TemporalStatus.IN_FORCE`` unless
    ``historical_context=True`` was passed to :meth:`KnowledgeBase.search`.
    """

    chunk: Chunk
    citation: Citation
    rank: float
    temporal_status: TemporalStatus = TemporalStatus.IN_FORCE


# ---------------------------------------------------------------------------
# Errors
# ---------------------------------------------------------------------------


class KnowledgeBaseError(Exception):
    """Raised when the knowledge base cannot be built or queried."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------------------
# Chunker
# ---------------------------------------------------------------------------


def _chunk_document(path: Path) -> list[Chunk]:
    """Split a Markdown file at heading boundaries into :class:`Chunk` objects.

    Each ATX heading (``#``, ``##``, ``###``) starts a new chunk.  Content
    before the first heading is collected into a synthetic "Preamble" chunk so
    that no bytes are lost.

    Args:
        path: Absolute path to a Markdown file.

    Returns:
        A list of :class:`Chunk` objects, in document order.

    Raises:
        KnowledgeBaseError: if the file is empty.
    """
    raw: bytes = path.read_bytes()
    if not raw:
        raise KnowledgeBaseError(f"rag: {path} is empty -- cannot index an empty document")

    text: str = raw.decode("utf-8", errors="replace")
    source_id: str = path.as_posix()

    # Find all heading positions.
    heading_matches = list(_HEADING_RE.finditer(text))

    chunks: list[Chunk] = []

    def _byte_offset(char_pos: int) -> int:
        """Convert a character offset to a byte offset in the raw UTF-8 bytes."""
        return len(text[:char_pos].encode("utf-8", errors="replace"))

    def _add_chunk(heading: str, body: str, char_start: int, char_end: int) -> None:
        if not body.strip():
            return
        chunks.append(
            Chunk(
                source_id=source_id,
                heading=heading,
                body=body,
                byte_start=_byte_offset(char_start),
                byte_end=_byte_offset(char_end),
            )
        )

    if not heading_matches:
        # No headings at all -- treat the whole file as one chunk.
        _add_chunk("Preamble", text, 0, len(text))
        return chunks

    # Content before the first heading.
    first_start = heading_matches[0].start()
    if first_start > 0:
        _add_chunk("Preamble", text[:first_start], 0, first_start)

    for i, match in enumerate(heading_matches):
        raw_heading_line = match.group(0).rstrip("\r\n")
        # Strip leading ATX '#' markers and the single space that follows so
        # chunk.heading is a bare title, not a Markdown syntax line.
        # Example: "# ontology:telecom/voice/mobile" -> "ontology:telecom/voice/mobile"
        # This makes classifier output match MetricEngine exact-match scoring.
        heading_line = raw_heading_line.lstrip("#").lstrip()
        body_start = match.start()
        body_end = heading_matches[i + 1].start() if i + 1 < len(heading_matches) else len(text)
        _add_chunk(heading_line, text[body_start:body_end], body_start, body_end)

    return chunks


# ---------------------------------------------------------------------------
# KnowledgeBase
# ---------------------------------------------------------------------------


@dataclass
class KnowledgeBase:
    """An immutable, in-process full-text index over spec documents.

    Build the index by calling :meth:`build`, then search it with
    :meth:`search`.  Both operations are safe to call from multiple threads
    (SQLite WAL mode is not needed because the index is read-only after build).

    Example::

        kb = KnowledgeBase()
        kb.build([Path("docs/specs/ZTAX-DET-001-global-tax-determination-engine.md")])
        results = kb.search("inclusive extraction threshold")
        for r in results:
            print(r.citation.citation_id, r.chunk.heading)
    """

    _conn: sqlite3.Connection = field(
        default_factory=lambda: sqlite3.connect(":memory:", check_same_thread=False),
        init=False,
        repr=False,
        compare=False,
    )
    _citations: dict[int, Citation] = field(default_factory=dict, init=False, repr=False)
    _chunks: dict[int, Chunk] = field(default_factory=dict, init=False, repr=False)
    _built: bool = field(default=False, init=False, repr=False)
    _ingested: set[str] = field(default_factory=set, init=False, repr=False)
    # Lifecycle state (Master Specification Ch.17 §9/§10)
    _retired: set[str] = field(default_factory=set, init=False, repr=False)
    _purged: set[str] = field(default_factory=set, init=False, repr=False)
    _tombstones: list[Tombstone] = field(default_factory=list, init=False, repr=False)

    def __post_init__(self) -> None:
        self._conn.execute(
            """
            CREATE VIRTUAL TABLE IF NOT EXISTS chunks USING fts5(
                chunk_id UNINDEXED,
                source_id UNINDEXED,
                effective_from UNINDEXED,
                effective_to UNINDEXED,
                heading,
                body,
                tokenize = 'porter unicode61'
            )
            """
        )
        self._conn.commit()

    # ------------------------------------------------------------------
    # Building
    # ------------------------------------------------------------------

    def build(
        self,
        paths: list[Path],
        *,
        rights_profiles: dict[str, RightsProfile] | None = None,
        customer_policy: CustomerPolicy | None = None,
        effective_from: datetime | None = None,
        effective_to: datetime | None = None,
        effective_dates: dict[str, tuple[datetime | None, datetime | None]] | None = None,
    ) -> None:
        """Ingest one or more spec documents into the index.

        Can be called multiple times with different files; calling it with a
        file that has already been ingested raises :class:`KnowledgeBaseError`.

        When *rights_profiles* is supplied, every path whose posix string
        appears in the dict is checked via
        :func:`~ztax_gateway.privacy_source_rights.check_corpus_source` before
        ingestion.  If the source's :class:`RightsProfile` does not permit
        ``USE_RETRIEVAL`` *and* ``USE_EMBEDDING``, the build is aborted with
        :class:`KnowledgeBaseError` (ZTAX-SRC-REQ-0004, ZTAX-PRIV-REQ-0073).

        **Valid-time parameters** (Chapter 17 ss9):

        *effective_from* / *effective_to* apply the same effective window to
        **every** document in *paths*.  For per-document control, supply
        *effective_dates* -- a mapping of ``path.as_posix()`` to a
        ``(effective_from, effective_to)`` pair.  Per-document entries in
        *effective_dates* take precedence over the flat *effective_from* /
        *effective_to* defaults.  ``None`` means open-ended (no lower/upper
        bound).

        All ``datetime`` values **must** be timezone-aware UTC; naive datetimes
        are rejected with :class:`KnowledgeBaseError` to prevent silent
        timezone bugs in audit logs.

        Every key in *effective_dates* must correspond to a ``path.as_posix()``
        for one of the paths in this call; unknown keys are rejected.

        When both bounds are supplied, ``effective_to`` must be strictly after
        ``effective_from``; an inverted window is rejected.

        Args:
            paths:            Absolute paths to Markdown spec files.
            rights_profiles:  Optional mapping of ``path.as_posix()`` to
                              :class:`~ztax_gateway.privacy_source_rights.RightsProfile`.
                              Sources not present in the dict are ingested
                              without a rights check.
            customer_policy:  Optional customer-level policy passed through
                              to :func:`~ztax_gateway.privacy_source_rights.check_corpus_source`.
            effective_from:   Default UTC lower bound for all ingested chunks.
            effective_to:     Default UTC upper bound (exclusive) for all
                              ingested chunks.
            effective_dates:  Per-document overrides; keys are
                              ``path.as_posix()`` strings.

        Raises:
            KnowledgeBaseError: if a path was already ingested, is missing,
                is empty, fails the source-rights gate, a supplied datetime
                is naive (timezone-unaware), an effective_dates key is unknown,
                or a window has effective_to <= effective_from.
            FileNotFoundError: if a path does not exist on disk.
        """
        _require_utc(effective_from, "effective_from")
        _require_utc(effective_to, "effective_to")
        _validate_window(effective_from, effective_to, "flat")

        path_sids = {p.as_posix() for p in paths}

        if effective_dates is not None:
            for sid, (ef, et) in effective_dates.items():
                if sid not in path_sids:
                    raise KnowledgeBaseError(
                        f"rag: effective_dates key {sid!r} is not among the paths"
                        " in this build() call"
                    )
                _require_utc(ef, "effective_dates effective_from")
                _require_utc(et, "effective_dates effective_to")
                _validate_window(ef, et, f"effective_dates[{sid!r}]")

        for path in paths:
            sid = path.as_posix()
            # Lifecycle gate: retired or purged sources cannot be re-ingested
            # (a withdrawn source needs a new index generation).
            # Must be checked before the duplicate-ingest guard because a retired
            # source is still in _ingested.
            if sid in self._retired or sid in self._purged:
                raise KnowledgeBaseError(
                    f"rag: {path.name!r} has been retired or purged and cannot be re-ingested"
                    " -- a withdrawn source needs a new index generation"
                )
            if sid in self._ingested:
                raise KnowledgeBaseError(
                    f"rag: {path.name!r} has already been ingested -- duplicate ingestion is a bug"
                )
            # Source-rights gate (ZTAX-SRC-REQ-0004, ZTAX-PRIV-REQ-0073).
            if rights_profiles is not None and sid in rights_profiles:
                try:
                    check_corpus_source(
                        rights_profiles[sid],
                        customer_policy=customer_policy,
                    )
                except PrivacySourceRightsError as exc:
                    raise KnowledgeBaseError(
                        f"rag: source {path.name!r} failed rights gate -- {exc}"
                    ) from exc
            # Resolve per-document effective window.
            doc_ef: datetime | None = effective_from
            doc_et: datetime | None = effective_to
            if effective_dates is not None and sid in effective_dates:
                doc_ef, doc_et = effective_dates[sid]
            self._ingest_one(path, effective_from=doc_ef, effective_to=doc_et)
            self._ingested.add(sid)
        self._built = True

    def _ingest_one(
        self,
        path: Path,
        *,
        effective_from: datetime | None = None,
        effective_to: datetime | None = None,
    ) -> None:
        raw: bytes = path.read_bytes()
        doc_chunks = _chunk_document(path)
        rows = []
        ef_str = _to_iso(effective_from)
        et_str = _to_iso(effective_to)
        for doc_chunk in doc_chunks:
            # Build a Citation for this chunk using citation.py.
            source_chunk = SourceChunk(
                source_id=doc_chunk.source_id,
                content=raw[doc_chunk.byte_start : doc_chunk.byte_end],
                byte_start=doc_chunk.byte_start,
                byte_end=doc_chunk.byte_end,
            )
            citation = combine([source_chunk])
            # Store the chunk with its effective window.
            chunk_id = len(self._chunks)
            self._chunks[chunk_id] = Chunk(
                source_id=doc_chunk.source_id,
                heading=doc_chunk.heading,
                body=doc_chunk.body,
                byte_start=doc_chunk.byte_start,
                byte_end=doc_chunk.byte_end,
                effective_from=effective_from,
                effective_to=effective_to,
            )
            self._citations[chunk_id] = citation
            rows.append((
                chunk_id,
                doc_chunk.source_id,
                ef_str,
                et_str,
                doc_chunk.heading,
                doc_chunk.body,
            ))
        self._conn.executemany(
            "INSERT INTO chunks"
            " (chunk_id, source_id, effective_from, effective_to, heading, body)"
            " VALUES (?, ?, ?, ?, ?, ?)",
            rows,
        )
        self._conn.commit()

    # ------------------------------------------------------------------
    # Lifecycle operations (Master Specification Ch.17 §9/§10)
    # ------------------------------------------------------------------

    def _source_chunk_ids(self, source_id: str) -> list[int]:
        """Return chunk IDs for *source_id* in insertion order."""
        return [cid for cid, chunk in self._chunks.items() if chunk.source_id == source_id]

    def _content_sha256(self, source_id: str) -> tuple[str, int]:
        """Compute (content_sha256, chunk_count) for *source_id*.

        The SHA-256 is over the chunk ``content_hash`` values concatenated in
        chunk-id order, so the same source content always produces the same
        digest and an auditor can verify which version was removed.
        """
        chunk_ids = self._source_chunk_ids(source_id)
        raw = b"".join(
            self._citations[cid].chunks[0].content_hash.encode()
            for cid in chunk_ids
            if cid in self._citations
        )
        return hashlib.sha256(raw).hexdigest(), len(chunk_ids)

    def retire(
        self,
        source_id: str,
        *,
        reason: LifecycleReason,
        at: datetime | None = None,
    ) -> Tombstone:
        """Soft-remove *source_id* from the index.

        After this call the source never appears in any search, including
        ``historical_context=True``.  Chunk content is kept for audit.

        Args:
            source_id: The posix-path identifier previously passed to
                :meth:`build`.
            reason:    The business reason for the retirement.
            at:        Timezone-aware UTC datetime of the operation.  Defaults
                       to ``_utcnow()``.

        Returns:
            The immutable :class:`Tombstone` that was appended to the audit
            trail.

        Raises:
            KnowledgeBaseError: if *source_id* is unknown, already retired,
                already purged, or *at* is naive.
        """
        if source_id in self._purged:
            raise KnowledgeBaseError(
                f"rag: retire: {source_id!r} has already been purged"
            )
        if source_id in self._retired:
            raise KnowledgeBaseError(
                f"rag: retire: {source_id!r} has already been retired"
            )
        if source_id not in self._ingested and source_id not in self._retired:
            raise KnowledgeBaseError(
                f"rag: retire: {source_id!r} is not in the index"
            )
        if at is None:
            at = _utcnow()
        _require_utc(at, "at")
        sha, count = self._content_sha256(source_id)
        tombstone = Tombstone(
            source_id=source_id,
            action=LifecycleAction.RETIRE,
            reason=reason,
            at=at,
            content_sha256=sha,
            chunk_count=count,
        )
        self._retired.add(source_id)
        self._tombstones.append(tombstone)
        return tombstone

    def purge(
        self,
        source_id: str,
        *,
        reason: LifecycleReason,
        at: datetime | None = None,
    ) -> Tombstone:
        """Hard-remove *source_id* from the index.

        Physically deletes the source's chunk text from the SQLite FTS5 table
        and from every in-memory structure so that no search or attribute can
        return it.  Only the :class:`Tombstone` remains.

        A purge following a :meth:`retire` is allowed (a second tombstone is
        appended).

        Args:
            source_id: The posix-path identifier previously passed to
                :meth:`build`.
            reason:    The business reason for the purge.
            at:        Timezone-aware UTC datetime of the operation.  Defaults
                       to ``_utcnow()``.

        Returns:
            The immutable :class:`Tombstone` that was appended to the audit
            trail.

        Raises:
            KnowledgeBaseError: if *source_id* is unknown, already purged, or
                *at* is naive.
        """
        if source_id in self._purged:
            raise KnowledgeBaseError(
                f"rag: purge: {source_id!r} has already been purged"
            )
        if source_id not in self._ingested and source_id not in self._retired:
            raise KnowledgeBaseError(
                f"rag: purge: {source_id!r} is not in the index"
            )
        if at is None:
            at = _utcnow()
        _require_utc(at, "at")
        sha, count = self._content_sha256(source_id)
        tombstone = Tombstone(
            source_id=source_id,
            action=LifecycleAction.PURGE,
            reason=reason,
            at=at,
            content_sha256=sha,
            chunk_count=count,
        )
        # --- Physical deletion ---
        # 1. Remove from FTS5 table.
        self._conn.execute(
            "DELETE FROM chunks WHERE source_id = ?", (source_id,)
        )
        self._conn.commit()
        # 2. Remove from in-memory dicts.
        chunk_ids = self._source_chunk_ids(source_id)
        for cid in chunk_ids:
            self._chunks.pop(cid, None)
            self._citations.pop(cid, None)
        # 3. Remove from the ingested set so document_count/source_ids exclude
        #    this source; add to _purged so re-ingestion is blocked.
        self._ingested.discard(source_id)
        self._purged.add(source_id)
        # 4. Also remove from retired if a retire preceded this purge.
        self._retired.discard(source_id)
        self._tombstones.append(tombstone)
        return tombstone

    def tombstones(self) -> tuple[Tombstone, ...]:
        """Return an append-only audit trail of every lifecycle operation.

        Tombstones are returned in operation order (retire before purge if both
        were called on the same source).
        """
        return tuple(self._tombstones)

    # ------------------------------------------------------------------
    # Searching
    # ------------------------------------------------------------------

    def search(
        self,
        query: str,
        top_k: int = _DEFAULT_TOP_K,
        *,
        mode: TemporalMode = TemporalMode.CURRENT,
        as_of: datetime | None = None,
        historical_context: bool = False,
    ) -> list[SearchResult]:
        """Return the top-*k* ranked results for *query*.

        Each result carries a :class:`~ztax_gateway.citation.Citation` that
        traces the matching chunk back to the exact bytes in the spec document,
        making any downstream AI output grounded and auditable.

        **Valid-time filtering** (Chapter 17 ss9):

        ``mode=TemporalMode.CURRENT`` (default)
            The reference point is ``_utcnow()``.  Only IN_FORCE chunks are
            returned by default.  ``as_of`` must be ``None``; supplying it
            raises :class:`KnowledgeBaseError`.

        ``mode=TemporalMode.AS_KNOWN_THEN``
            The reference point is ``as_of``, which must be a timezone-aware
            datetime.  Omitting ``as_of`` raises :class:`KnowledgeBaseError`.

        When ``historical_context=True`` is supplied, SUPERSEDED chunks
        (``effective_to`` has passed the reference point) are also returned,
        tagged ``TemporalStatus.SUPERSEDED``.  NOT_YET_EFFECTIVE chunks are
        never returned regardless of this flag.

        Filtering happens on the full result set *before* ``top_k`` is applied
        so that ``top_k`` accurately counts post-filter results.

        Args:
            query:              Plain-text search query.
            top_k:              Maximum number of results.  Defaults to 5.
            mode:               ``TemporalMode.CURRENT`` or
                                ``TemporalMode.AS_KNOWN_THEN``.
            as_of:              Reference datetime for AS_KNOWN_THEN; must be
                                timezone-aware.  Must be ``None`` for CURRENT.
            historical_context: If ``True``, also include SUPERSEDED chunks
                                tagged accordingly.

        Returns:
            A list of :class:`SearchResult` objects, ranked by BM25 score
            (best match first).  May be shorter than *top_k*.

        Raises:
            KnowledgeBaseError: for short query, unbuilt index, mode/as_of
                mismatch, or naive datetime.
        """
        query = query.strip()
        if len(query) < _MIN_QUERY_LEN:
            raise KnowledgeBaseError(
                f"rag: query {query!r} is too short (minimum {_MIN_QUERY_LEN} characters)"
            )
        if not self._built:
            raise KnowledgeBaseError(
                "rag: knowledge base has not been built -- call build() first"
            )

        # Mode/as_of consistency.
        if mode is TemporalMode.CURRENT and as_of is not None:
            raise KnowledgeBaseError(
                "rag: as_of must be None when mode=TemporalMode.CURRENT; "
                "use mode=TemporalMode.AS_KNOWN_THEN to supply a reference datetime"
            )
        if mode is TemporalMode.AS_KNOWN_THEN and as_of is None:
            raise KnowledgeBaseError(
                "rag: as_of is required when mode=TemporalMode.AS_KNOWN_THEN"
            )
        if as_of is not None:
            _require_utc(as_of, "as_of")

        # Resolve the reference point.
        ref: datetime = _utcnow() if mode is TemporalMode.CURRENT else as_of  # type: ignore[assignment]

        # Retrieve ALL FTS matches ordered by rank; filter then slice.
        cursor = self._conn.execute(
            """
            SELECT chunk_id, rank
            FROM chunks
            WHERE chunks MATCH ?
            ORDER BY rank
            """,
            (query,),
        )
        rows = cursor.fetchall()

        if top_k == 0:
            return []

        results: list[SearchResult] = []
        for chunk_id, rank in rows:
            # chunk_id might have been purged (removed from _chunks); skip.
            if chunk_id not in self._chunks:
                continue
            chunk = self._chunks[chunk_id]
            # Retired sources are excluded from every search mode, including
            # historical_context=True (Master Specification Ch.17 §9/§10).
            if chunk.source_id in self._retired:
                continue
            status = _temporal_status(chunk, ref)
            if status is TemporalStatus.NOT_YET_EFFECTIVE:
                continue
            if status is TemporalStatus.SUPERSEDED and not historical_context:
                continue
            citation = self._citations[chunk_id]
            results.append(
                SearchResult(chunk=chunk, citation=citation, rank=rank, temporal_status=status)
            )
            if len(results) == top_k:
                break
        return results

    # ------------------------------------------------------------------
    # Inspection
    # ------------------------------------------------------------------

    @property
    def document_count(self) -> int:
        """Number of unique documents ingested (excludes purged sources)."""
        return len(self._ingested)

    @property
    def chunk_count(self) -> int:
        """Total number of indexed chunks (excludes purged sources)."""
        return len(self._chunks)

    def source_ids(self) -> frozenset[str]:
        """Return the set of source identifiers (file paths) that have been ingested.

        Purged sources are excluded.  Retired sources are still present (their
        chunk text is retained for audit).
        """
        return frozenset(self._ingested)


# ---------------------------------------------------------------------------
# Private helpers
# ---------------------------------------------------------------------------


def _require_utc(dt: datetime | None, param: str) -> None:
    """Raise KnowledgeBaseError if *dt* is a naive (timezone-unaware) datetime."""
    if dt is not None and dt.tzinfo is None:
        raise KnowledgeBaseError(
            f"rag: {param}={dt!r} is naive -- supply a UTC-aware datetime"
            " (e.g. datetime.now(tz=datetime.UTC))"
        )


def _to_iso(dt: datetime | None) -> str | None:
    """Format *dt* as an ISO-8601 UTC string for SQLite storage, or None."""
    if dt is None:
        return None
    utc = dt.astimezone(UTC)
    return utc.strftime(_ISO_FMT)


def _validate_window(
    ef: datetime | None, et: datetime | None, label: str
) -> None:
    """Raise KnowledgeBaseError if effective_to <= effective_from."""
    if ef is not None and et is not None and et.astimezone(UTC) <= ef.astimezone(UTC):
            raise KnowledgeBaseError(
                f"rag: {label}: effective_to ({et!r}) must be strictly after"
                f" effective_from ({ef!r})"
            )


def _temporal_status(chunk: Chunk, ref: datetime) -> TemporalStatus:
    """Classify a chunk relative to the reference point *ref*.

    Returns:
        IN_FORCE          -- effective_from <= ref < effective_to
        SUPERSEDED        -- ref >= effective_to (chunk has expired)
        NOT_YET_EFFECTIVE -- ref < effective_from (chunk not yet active)
    """
    ref_utc = ref.astimezone(UTC)
    ef = chunk.effective_from
    et = chunk.effective_to

    # Check NOT_YET_EFFECTIVE: ref is before effective_from.
    if ef is not None and ref_utc < ef.astimezone(UTC):
        return TemporalStatus.NOT_YET_EFFECTIVE

    # Check SUPERSEDED: ref is at or after effective_to.
    if et is not None and ref_utc >= et.astimezone(UTC):
        return TemporalStatus.SUPERSEDED

    return TemporalStatus.IN_FORCE
