"""Provenance RAG — Knowledge-Base over the ZoikoTax specification documents.

Chapter 17 §15 of the ZoikoTax Master Specification.

Every AI output that references a tax rule, a determination algorithm, or a
jurisdiction requirement must be traceable to a specific chunk of an official
spec document.  This module provides that traceability by:

1. **Ingesting** spec documents (Markdown files from ``docs/specs/``) into an
   in-process SQLite FTS5 full-text index.  No external vector database, no
   network call, no embedding model — CI can run it offline.

2. **Searching** the index with a plain-text query and returning a ranked list
   of :class:`~ztax_gateway.citation.Citation` objects, one per matching chunk.
   Because results are ``Citation`` objects, every downstream use carries a
   tamper-evident reference to the source material that justified it.

3. **Enforcing the fiscal boundary** — this module imports nothing from the
   fiscal, tax_decision or subledger packages (ADR-0006 §2.6; the CI grep
   enforces this).

Design rules
------------
* **Immutable index** — once :meth:`KnowledgeBase.build` returns, the index is
  read-only.  Attempting to ingest a document twice is a programming error, not
  a runtime condition.
* **Chunk granularity** — documents are split at Markdown heading boundaries
  (``#``, ``##``, ``###``).  Each heading + its body is one chunk, one
  ``SourceChunk``, one ``Citation``.
* **No embeddings** — SQLite FTS5 BM25 ranking is sufficient for the spec
  corpus (two documents, ~840 lines, ~56 KB total).  Embeddings are reserved
  for the W2 lane L upgrade.
* **Deterministic citation IDs** — given the same file content, the same query
  always produces the same :class:`~ztax_gateway.citation.Citation` objects, so
  audit logs can be replayed and compared.
"""

from __future__ import annotations

import re
import sqlite3
from dataclasses import dataclass, field
from pathlib import Path
from typing import Final

from .citation import Citation, SourceChunk, combine

__all__: list[str] = [
    "Chunk",
    "KnowledgeBase",
    "KnowledgeBaseError",
    "SearchResult",
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# Maximum number of results returned by a single search call.
_DEFAULT_TOP_K: Final[int] = 5

# Minimum query length — single-character queries are never meaningful.
_MIN_QUERY_LEN: Final[int] = 2

# Regex that matches a Markdown heading line (ATX style, levels 1-3).
_HEADING_RE: re.Pattern[str] = re.compile(r"^#{1,3}\s+.+", re.MULTILINE)


# ---------------------------------------------------------------------------
# Data structures
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class Chunk:
    """A single indexed unit: one heading section from one spec document.

    ``source_id`` is the canonical identifier for the document (its path
    relative to the repository root, forward-slash separated).

    ``heading`` is the Markdown heading line that opened this section
    (e.g. ``"## 2. The determination pipeline"``).

    ``body`` is the full text of this section (heading + content lines).

    ``byte_start`` / ``byte_end`` are byte offsets into the *original* file.
    """

    source_id: str
    heading: str
    body: str
    byte_start: int
    byte_end: int


@dataclass(frozen=True, slots=True)
class SearchResult:
    """One ranked result from :meth:`KnowledgeBase.search`.

    ``chunk`` is the matching :class:`Chunk`.
    ``citation`` is the tamper-evident :class:`Citation` for that chunk.
    ``rank`` is the BM25 score as returned by SQLite FTS5 (lower is better).
    """

    chunk: Chunk
    citation: Citation
    rank: float


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
        raise KnowledgeBaseError(f"rag: {path} is empty — cannot index an empty document")

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
        # No headings at all — treat the whole file as one chunk.
        _add_chunk("Preamble", text, 0, len(text))
        return chunks

    # Content before the first heading.
    first_start = heading_matches[0].start()
    if first_start > 0:
        _add_chunk("Preamble", text[:first_start], 0, first_start)

    for i, match in enumerate(heading_matches):
        heading_line = match.group(0).rstrip("\r\n")
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

    def __post_init__(self) -> None:
        self._conn.execute(
            """
            CREATE VIRTUAL TABLE IF NOT EXISTS chunks USING fts5(
                chunk_id UNINDEXED,
                source_id UNINDEXED,
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

    def build(self, paths: list[Path]) -> None:
        """Ingest one or more spec documents into the index.

        Can be called multiple times with different files; calling it with a
        file that has already been ingested raises :class:`KnowledgeBaseError`.

        Args:
            paths: Absolute paths to Markdown spec files.

        Raises:
            KnowledgeBaseError: if a path was already ingested, is missing,
                or is empty.
            FileNotFoundError: if a path does not exist on disk.
        """
        for path in paths:
            sid = path.as_posix()
            if sid in self._ingested:
                raise KnowledgeBaseError(
                    f"rag: {path.name!r} has already been ingested — duplicate ingestion is a bug"
                )
            self._ingest_one(path)
            self._ingested.add(sid)
        self._built = True

    def _ingest_one(self, path: Path) -> None:
        raw: bytes = path.read_bytes()
        doc_chunks = _chunk_document(path)
        rows = []
        for doc_chunk in doc_chunks:
            # Build a Citation for this chunk using citation.py.
            source_chunk = SourceChunk(
                source_id=doc_chunk.source_id,
                content=raw[doc_chunk.byte_start : doc_chunk.byte_end],
                byte_start=doc_chunk.byte_start,
                byte_end=doc_chunk.byte_end,
            )
            citation = combine([source_chunk])
            # Use the row ID as the in-process key.
            chunk_id = len(self._chunks)
            self._chunks[chunk_id] = doc_chunk
            self._citations[chunk_id] = citation
            rows.append((chunk_id, doc_chunk.source_id, doc_chunk.heading, doc_chunk.body))
        self._conn.executemany(
            "INSERT INTO chunks (chunk_id, source_id, heading, body) VALUES (?, ?, ?, ?)",
            rows,
        )
        self._conn.commit()

    # ------------------------------------------------------------------
    # Searching
    # ------------------------------------------------------------------

    def search(self, query: str, top_k: int = _DEFAULT_TOP_K) -> list[SearchResult]:
        """Return the top-*k* ranked results for *query*.

        Each result carries a :class:`~ztax_gateway.citation.Citation` that
        traces the matching chunk back to the exact bytes in the spec document,
        making any downstream AI output grounded and auditable.

        Args:
            query: A plain-text search query (e.g. ``"inclusive extraction"``).
            top_k: Maximum number of results to return.  Defaults to 5.

        Returns:
            A list of :class:`SearchResult` objects, ranked by BM25 score
            (best match first).  May be shorter than *top_k* if fewer chunks
            match.

        Raises:
            KnowledgeBaseError: if *query* is too short or the index is empty.
        """
        query = query.strip()
        if len(query) < _MIN_QUERY_LEN:
            raise KnowledgeBaseError(
                f"rag: query {query!r} is too short (minimum {_MIN_QUERY_LEN} characters)"
            )
        if not self._built:
            raise KnowledgeBaseError(
                "rag: knowledge base has not been built — call build() first"
            )

        # SQLite FTS5 rank() is negative BM25 (lower = better match).
        # Use MATCH with porter stemming via the tokenizer configured above.
        cursor = self._conn.execute(
            """
            SELECT chunk_id, rank
            FROM chunks
            WHERE chunks MATCH ?
            ORDER BY rank
            LIMIT ?
            """,
            (query, top_k),
        )
        rows = cursor.fetchall()
        results: list[SearchResult] = []
        for chunk_id, rank in rows:
            chunk = self._chunks[chunk_id]
            citation = self._citations[chunk_id]
            results.append(SearchResult(chunk=chunk, citation=citation, rank=rank))
        return results

    # ------------------------------------------------------------------
    # Inspection
    # ------------------------------------------------------------------

    @property
    def document_count(self) -> int:
        """Number of unique documents ingested."""
        return len(self._ingested)

    @property
    def chunk_count(self) -> int:
        """Total number of indexed chunks."""
        return len(self._chunks)

    def source_ids(self) -> frozenset[str]:
        """Return the set of source identifiers (file paths) that have been ingested."""
        return frozenset(self._ingested)
