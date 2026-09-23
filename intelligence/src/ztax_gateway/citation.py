"""Citation & Provenance Engine.

Chapter 14 of the ZoikoTax Master Specification.

Every AI output — whether it is a classification proposal, an extraction, a
suggestion, or a tool-call result — must carry a tamper-evident citation that
traces it back to the source material that justified it.  This module provides
the three building blocks:

``SourceChunk``
    An immutable slice of a source document: the raw bytes, the byte-range,
    and the SHA-256 hash that makes it tamper-evident.

``Citation``
    A named reference to one or more ``SourceChunk`` objects.  The
    ``citation_id`` is the deterministic hex prefix of the hash of every
    chunk's identity, provenance and hash, so two ``Citation`` objects with
    the same source material always produce the same ``citation_id``.

``ProvenanceSpan``
    Links an AI output (identified by ``output_id``) to the exact
    ``Citation`` that supported it.  This is the record that ends up in the
    ``AgentAudit`` log and that a human reviewer can later verify.

Design rules (enforced by ``frozen=True``):

- Nothing downstream may mutate a citation once it has been issued.  A
  mutable citation would allow a model to widen its own justification after
  the fact.
- ``citation_id`` is derived entirely from the content of the source chunks;
  it cannot be supplied by the caller.
- ``ProvenanceSpan`` is frozen for the same reason ``Provenance`` and
  ``ToolProvenance`` are frozen: the enforcement point must be the last
  writer.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass, field
from pathlib import Path

__all__: list[str] = [
    "Citation",
    "CitationError",
    "ProvenanceSpan",
    "SourceChunk",
    "cite_file",
    "combine",
]

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_HASH_PREFIX_LEN: int = 16  # hex chars kept as the short citation_id


def _sha256_hex(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


# ---------------------------------------------------------------------------
# SourceChunk
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class SourceChunk:
    """An immutable, tamper-evident slice of a source document.

    ``source_id`` identifies the document (e.g. a file path, a document ID
    or a URL — the caller chooses; the engine does not interpret it).

    ``content`` is the raw bytes of this slice.  Keeping bytes rather than
    a decoded string means we hash exactly what a verifier would also hash,
    without worrying about encoding normalisation.

    ``byte_start`` / ``byte_end`` are byte offsets into the *original*
    document and are stored purely for human-review purposes.  The engine
    does not validate them.

    ``content_hash`` is computed automatically from ``content`` and cannot
    be supplied by the caller.
    """

    source_id: str
    content: bytes
    byte_start: int
    byte_end: int
    content_hash: str = field(init=False)

    def __post_init__(self) -> None:
        # frozen dataclasses do not allow normal attribute assignment, so we
        # reach through object.__setattr__ which is the accepted pattern.
        object.__setattr__(self, "content_hash", _sha256_hex(self.content))

    def verify(self) -> bool:
        """Return ``True`` iff ``content_hash`` still matches ``content``."""
        return _sha256_hex(self.content) == self.content_hash


# ---------------------------------------------------------------------------
# Citation
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class Citation:
    """A named, tamper-evident reference to one or more source chunks.

    ``citation_id`` is the first ``_HASH_PREFIX_LEN`` hex characters of the
    SHA-256 of every chunk's ``source_id``, ``byte_start``, ``byte_end`` and
    ``content_hash``, in deterministic order (``source_id`` then
    ``byte_start``).  Two chunks with identical bytes from different documents
    therefore produce different ``citation_id`` values, so a reviewer can tell
    which source a citation points at.

    Use :func:`combine` to construct a ``Citation``; do not call the
    constructor directly from application code.
    """

    citation_id: str
    chunks: tuple[SourceChunk, ...]

    def verify_all(self) -> bool:
        """Return ``True`` iff every chunk still matches its stored hash."""
        return all(c.verify() for c in self.chunks)

    def source_ids(self) -> frozenset[str]:
        """Return the set of ``source_id`` values referenced by this citation."""
        return frozenset(c.source_id for c in self.chunks)


# ---------------------------------------------------------------------------
# ProvenanceSpan
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class ProvenanceSpan:
    """Links an AI output to the citation that justified it.

    ``output_id`` identifies the AI-plane output (e.g. the ``call_id`` from
    ``AgentAudit``, a suggestion ID, or an extraction ID).

    ``citation`` is the :class:`Citation` that the output was grounded in.

    ``confidence`` is an optional [0.0, 1.0] score supplied by the model or
    the evaluation harness.  It is stored for audit purposes only; the engine
    does not gate on it.
    """

    output_id: str
    citation: Citation
    confidence: float | None = None

    def __post_init__(self) -> None:
        if self.confidence is not None and not (0.0 <= self.confidence <= 1.0):
            raise ValueError(
                f"citation: confidence must be in [0.0, 1.0], got {self.confidence!r}"
            )

    def as_dict(self) -> dict[str, str | float]:
        """Return a log-safe representation."""
        d: dict[str, str | float] = {
            "output_id": self.output_id,
            "citation_id": self.citation.citation_id,
            "source_ids": ",".join(sorted(self.citation.source_ids())),
        }
        if self.confidence is not None:
            d["confidence"] = self.confidence
        return d


# ---------------------------------------------------------------------------
# CitationError
# ---------------------------------------------------------------------------


class CitationError(Exception):
    """Raised when a citation cannot be constructed or verified."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------------------
# Public factories
# ---------------------------------------------------------------------------


def combine(chunks: list[SourceChunk]) -> Citation:
    """Build a :class:`Citation` from one or more :class:`SourceChunk` objects.

    The ``citation_id`` is derived deterministically from the chunks:

    1. Sort chunks by ``(source_id, byte_start)`` so the order of the input
       list does not affect the result.
    2. For every chunk, field-separate its ``source_id``, ``byte_start``,
       ``byte_end`` and ``content_hash``, then SHA-256 the whole record set.
       Including the provenance fields means identical bytes from different
       sources still produce distinct ``citation_id`` values.
    3. Take the first ``_HASH_PREFIX_LEN`` hex characters as the short ID.

    Raises:
        CitationError: if ``chunks`` is empty (a citation must reference at
            least one source).
    """
    if not chunks:
        raise CitationError("citation: at least one source chunk is required")

    sorted_chunks = sorted(chunks, key=lambda c: (c.source_id, c.byte_start))
    material = "\0".join(
        f"{c.source_id}\0{c.byte_start}\0{c.byte_end}\0{c.content_hash}"
        for c in sorted_chunks
    ).encode()
    citation_id = _sha256_hex(material)[:_HASH_PREFIX_LEN]
    return Citation(
        citation_id=citation_id,
        chunks=tuple(sorted_chunks),
    )


def cite_file(path: Path, chunk_size: int = 0) -> Citation:
    """Convenience factory that creates a single-chunk :class:`Citation` from a file.

    Reads the entire file as one :class:`SourceChunk` (the whole file is the
    "chunk").  For large documents you would split the file and call
    :func:`combine` with multiple chunks instead.

    Args:
        path: Path to the file to cite.
        chunk_size: Unused in the default implementation (the whole file is
            one chunk).  Reserved for future use.

    Raises:
        FileNotFoundError: if ``path`` does not exist.
        CitationError: if ``path`` is empty.
    """
    data = path.read_bytes()
    if not data:
        raise CitationError(f"citation: {path} is empty — cannot cite an empty file")
    chunk = SourceChunk(
        source_id=str(path),
        content=data,
        byte_start=0,
        byte_end=len(data),
    )
    return combine([chunk])
