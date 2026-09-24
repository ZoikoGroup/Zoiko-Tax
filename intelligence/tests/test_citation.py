"""Tests for the Citation & Provenance Engine.

Chapter 14 of the ZoikoTax Master Specification.

Mirrors ``test_governance.py`` and ``test_tool_broker.py`` in structure and
discipline.  Every public type and function is covered; every error path is
exercised explicitly so that a regression surfaces as a failing test, not as a
silent wrong answer in an audit log.
"""

from __future__ import annotations

import hashlib
from dataclasses import FrozenInstanceError
from pathlib import Path

import pytest

from ztax_gateway.citation import (
    Citation,
    CitationError,
    ProvenanceSpan,
    SourceChunk,
    cite_file,
    combine,
)

# ---------------------------------------------------------------------------
# Builders
# ---------------------------------------------------------------------------

# A short, stable byte sequence used across multiple tests.
_CONTENT_A: bytes = b"Tax code section 12(3)(b) \xe2\x80\x94 taxable supply."
_CONTENT_B: bytes = b"GST Act 1985 s.8 \xe2\x80\x94 rate of tax."


def _chunk(
    source_id: str = "doc-a",
    content: bytes = _CONTENT_A,
    byte_start: int = 0,
) -> SourceChunk:
    return SourceChunk(
        source_id=source_id,
        content=content,
        byte_start=byte_start,
        byte_end=byte_start + len(content),
    )


# ---------------------------------------------------------------------------
# SourceChunk — construction
# ---------------------------------------------------------------------------


def test_source_chunk_content_hash_is_computed_automatically() -> None:
    chunk = _chunk()
    expected = hashlib.sha256(_CONTENT_A).hexdigest()
    assert chunk.content_hash == expected


def test_source_chunk_content_hash_length_is_64_hex_chars() -> None:
    assert len(_chunk().content_hash) == 64


def test_source_chunk_stores_correct_byte_range() -> None:
    chunk = _chunk(byte_start=100)
    assert chunk.byte_start == 100
    assert chunk.byte_end == 100 + len(_CONTENT_A)


# ---------------------------------------------------------------------------
# SourceChunk — immutability
# ---------------------------------------------------------------------------


def test_source_chunk_is_frozen() -> None:
    """Nothing downstream may widen its own justification by mutating a chunk."""
    chunk = _chunk()
    with pytest.raises(FrozenInstanceError):
        chunk.content_hash = "tampered"  # type: ignore[misc]


def test_source_chunk_content_is_immutable() -> None:
    chunk = _chunk()
    with pytest.raises(FrozenInstanceError):
        chunk.content = b"replaced"  # type: ignore[misc]


# ---------------------------------------------------------------------------
# SourceChunk — verification
# ---------------------------------------------------------------------------


def test_source_chunk_verify_returns_true_for_intact_chunk() -> None:
    assert _chunk().verify() is True


def test_source_chunk_verify_returns_false_after_hash_tampering() -> None:
    """Simulate an attacker that tampers with the stored hash."""
    chunk = _chunk()
    # bypass the frozen dataclass to inject a fake hash
    object.__setattr__(chunk, "content_hash", "00" * 32)
    assert chunk.verify() is False


# ---------------------------------------------------------------------------
# combine — happy paths
# ---------------------------------------------------------------------------


def test_combine_single_chunk_produces_citation() -> None:
    c = combine([_chunk()])
    assert isinstance(c, Citation)
    assert len(c.citation_id) == 16  # _HASH_PREFIX_LEN


def test_combine_two_chunks_produces_citation() -> None:
    chunks = [_chunk("doc-a", _CONTENT_A, 0), _chunk("doc-b", _CONTENT_B, 0)]
    c = combine(chunks)
    assert len(c.citation_id) == 16
    assert len(c.chunks) == 2


def test_combine_citation_id_is_deterministic_regardless_of_input_order() -> None:
    """Swapping the chunk list order must not change the citation_id."""
    chunk_a = _chunk("doc-a", _CONTENT_A, 0)
    chunk_b = _chunk("doc-b", _CONTENT_B, 0)
    c1 = combine([chunk_a, chunk_b])
    c2 = combine([chunk_b, chunk_a])
    assert c1.citation_id == c2.citation_id


def test_combine_stores_chunks_sorted_by_source_id_then_byte_start() -> None:
    chunk_b = _chunk("doc-b", _CONTENT_B, 0)
    chunk_a = _chunk("doc-a", _CONTENT_A, 0)
    c = combine([chunk_b, chunk_a])
    assert c.chunks[0].source_id == "doc-a"
    assert c.chunks[1].source_id == "doc-b"


def test_combine_different_content_produces_different_citation_id() -> None:
    c1 = combine([_chunk("doc", _CONTENT_A)])
    c2 = combine([_chunk("doc", _CONTENT_B)])
    assert c1.citation_id != c2.citation_id


def test_combine_same_content_different_source_id_produces_different_citation_id() -> None:
    c1 = combine([_chunk("doc-a", _CONTENT_A)])
    c2 = combine([_chunk("doc-b", _CONTENT_A)])
    assert c1.citation_id != c2.citation_id


# ---------------------------------------------------------------------------
# combine — error paths
# ---------------------------------------------------------------------------


def test_combine_empty_list_raises_citation_error() -> None:
    with pytest.raises(CitationError) as exc:
        combine([])
    assert "at least one source chunk" in exc.value.reason


# ---------------------------------------------------------------------------
# Citation — immutability
# ---------------------------------------------------------------------------


def test_citation_is_frozen() -> None:
    c = combine([_chunk()])
    with pytest.raises(FrozenInstanceError):
        c.citation_id = "tampered"  # type: ignore[misc]


def test_citation_chunks_is_immutable_tuple() -> None:
    c = combine([_chunk()])
    assert isinstance(c.chunks, tuple)
    with pytest.raises(TypeError):
        c.chunks[0] = _chunk("other")  # type: ignore[index]


# ---------------------------------------------------------------------------
# Citation — verify_all
# ---------------------------------------------------------------------------


def test_citation_verify_all_returns_true_for_intact_citation() -> None:
    c = combine([_chunk("a", _CONTENT_A), _chunk("b", _CONTENT_B)])
    assert c.verify_all() is True


def test_citation_verify_all_returns_false_when_one_chunk_is_tampered() -> None:
    chunk = _chunk()
    c = combine([chunk])
    # tamper with the stored hash of the single chunk
    object.__setattr__(c.chunks[0], "content_hash", "00" * 32)
    assert c.verify_all() is False


# ---------------------------------------------------------------------------
# Citation — source_ids
# ---------------------------------------------------------------------------


def test_citation_source_ids_returns_correct_set() -> None:
    chunk_a = _chunk("doc-a", _CONTENT_A, 0)
    chunk_b = _chunk("doc-b", _CONTENT_B, 0)
    c = combine([chunk_a, chunk_b])
    assert c.source_ids() == frozenset({"doc-a", "doc-b"})


def test_citation_source_ids_is_a_frozenset() -> None:
    c = combine([_chunk()])
    assert isinstance(c.source_ids(), frozenset)


# ---------------------------------------------------------------------------
# cite_file — happy paths
# ---------------------------------------------------------------------------


def test_cite_file_returns_a_citation(tmp_path: Path) -> None:
    f = tmp_path / "spec.txt"
    f.write_bytes(_CONTENT_A)
    c = cite_file(f)
    assert isinstance(c, Citation)


def test_cite_file_citation_id_is_deterministic(tmp_path: Path) -> None:
    f = tmp_path / "spec.txt"
    f.write_bytes(_CONTENT_A)
    assert cite_file(f).citation_id == cite_file(f).citation_id


def test_cite_file_different_content_produces_different_id(tmp_path: Path) -> None:
    f1 = tmp_path / "a.txt"
    f2 = tmp_path / "b.txt"
    f1.write_bytes(_CONTENT_A)
    f2.write_bytes(_CONTENT_B)
    assert cite_file(f1).citation_id != cite_file(f2).citation_id


def test_cite_file_chunk_source_id_is_the_file_path(tmp_path: Path) -> None:
    f = tmp_path / "spec.txt"
    f.write_bytes(_CONTENT_A)
    c = cite_file(f)
    assert str(f) in c.source_ids()


# ---------------------------------------------------------------------------
# cite_file — error paths
# ---------------------------------------------------------------------------


def test_cite_file_raises_file_not_found_for_missing_file(tmp_path: Path) -> None:
    with pytest.raises(FileNotFoundError):
        cite_file(tmp_path / "does-not-exist.txt")


def test_cite_file_raises_citation_error_for_empty_file(tmp_path: Path) -> None:
    f = tmp_path / "empty.txt"
    f.write_bytes(b"")
    with pytest.raises(CitationError) as exc:
        cite_file(f)
    assert "empty" in exc.value.reason


# ---------------------------------------------------------------------------
# ProvenanceSpan — construction
# ---------------------------------------------------------------------------


def test_provenance_span_stores_output_id_and_citation() -> None:
    c = combine([_chunk()])
    span = ProvenanceSpan(output_id="call-001", citation=c)
    assert span.output_id == "call-001"
    assert span.citation is c


def test_provenance_span_stores_optional_confidence() -> None:
    c = combine([_chunk()])
    span = ProvenanceSpan(output_id="call-001", citation=c, confidence=0.92)
    assert span.confidence == 0.92


def test_provenance_span_confidence_defaults_to_none() -> None:
    c = combine([_chunk()])
    span = ProvenanceSpan(output_id="call-001", citation=c)
    assert span.confidence is None


# ---------------------------------------------------------------------------
# ProvenanceSpan — validation
# ---------------------------------------------------------------------------


def test_provenance_span_confidence_below_zero_raises() -> None:
    c = combine([_chunk()])
    with pytest.raises(ValueError, match="confidence"):
        ProvenanceSpan(output_id="x", citation=c, confidence=-0.1)


def test_provenance_span_confidence_above_one_raises() -> None:
    c = combine([_chunk()])
    with pytest.raises(ValueError, match="confidence"):
        ProvenanceSpan(output_id="x", citation=c, confidence=1.01)


def test_provenance_span_confidence_at_boundary_values_is_valid() -> None:
    c = combine([_chunk()])
    ProvenanceSpan(output_id="x", citation=c, confidence=0.0)
    ProvenanceSpan(output_id="x", citation=c, confidence=1.0)


# ---------------------------------------------------------------------------
# ProvenanceSpan — immutability
# ---------------------------------------------------------------------------


def test_provenance_span_is_frozen() -> None:
    c = combine([_chunk()])
    span = ProvenanceSpan(output_id="call-001", citation=c)
    with pytest.raises(FrozenInstanceError):
        span.output_id = "mutated"  # type: ignore[misc]


# ---------------------------------------------------------------------------
# ProvenanceSpan — as_dict
# ---------------------------------------------------------------------------


def test_provenance_span_as_dict_includes_required_fields() -> None:
    c = combine([_chunk("doc-a", _CONTENT_A, 0)])
    span = ProvenanceSpan(output_id="call-007", citation=c, confidence=0.85)
    d = span.as_dict()
    assert d["output_id"] == "call-007"
    assert d["citation_id"] == c.citation_id
    assert "doc-a" in d["source_ids"]  # type: ignore[operator]
    assert d["confidence"] == 0.85


def test_provenance_span_as_dict_omits_confidence_when_none() -> None:
    c = combine([_chunk()])
    span = ProvenanceSpan(output_id="call-008", citation=c)
    assert "confidence" not in span.as_dict()


def test_provenance_span_as_dict_source_ids_are_comma_separated_and_sorted() -> None:
    chunk_a = _chunk("z-doc", _CONTENT_A, 0)
    chunk_b = _chunk("a-doc", _CONTENT_B, 0)
    c = combine([chunk_a, chunk_b])
    span = ProvenanceSpan(output_id="x", citation=c)
    source_ids = span.as_dict()["source_ids"]
    assert isinstance(source_ids, str)
    parts = source_ids.split(",")
    assert parts == sorted(parts)
