"""Tests for the Provenance RAG — Knowledge-Base.

Chapter 17 §15 of the ZoikoTax Master Specification.

Mirrors the structure of ``test_citation.py`` and ``test_tool_broker.py``.
Every public API is exercised; every error path is explicitly asserted so a
regression surfaces as a test failure rather than a silent wrong answer in an
AI audit log.

The tests use the real spec documents from ``docs/specs/`` where possible,
and synthetic in-memory Markdown for the edge-case tests.  This keeps the
tests fast (no network, no embedding model) while ensuring that the actual
spec content can be indexed and searched.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from ztax_gateway.citation import Citation
from ztax_gateway.rag import (
    Chunk,
    KnowledgeBase,
    KnowledgeBaseError,
    SearchResult,
    _chunk_document,
)

# ---------------------------------------------------------------------------
# Paths to the real spec documents
# ---------------------------------------------------------------------------

_REPO_ROOT = Path(__file__).parent.parent.parent.parent  # d:/zoiko/Zoiko-Tax
_SPECS_DIR = _REPO_ROOT / "docs" / "specs"
_DET_SPEC = _SPECS_DIR / "ZTAX-DET-001-global-tax-determination-engine.md"
_JUR_SPEC = _SPECS_DIR / "ZTAX-JUR-001-jurisdiction-situs-and-place-of-supply.md"

# Skip the real-file tests if the repo root cannot be determined reliably.
_SPECS_AVAILABLE = _DET_SPEC.exists() and _JUR_SPEC.exists()


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_markdown(tmp_path: Path, name: str, content: str) -> Path:
    """Write *content* to a temporary Markdown file and return its path."""
    p = tmp_path / name
    p.write_text(content, encoding="utf-8")
    return p


_SIMPLE_MD = """\
# Section One

This section covers the determination pipeline.

## Section Two

Inclusive extraction and threshold interactions.

### Section Three

BM25 ranking is used for retrieval.
"""


# ---------------------------------------------------------------------------
# _chunk_document — unit tests
# ---------------------------------------------------------------------------


def test_chunk_document_splits_at_headings(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    chunks = _chunk_document(f)
    assert len(chunks) == 3
    assert chunks[0].heading == "# Section One"
    assert chunks[1].heading == "## Section Two"
    assert chunks[2].heading == "### Section Three"


def test_chunk_document_body_contains_heading_and_content(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    chunks = _chunk_document(f)
    assert "determination pipeline" in chunks[0].body
    assert "Inclusive extraction" in chunks[1].body


def test_chunk_document_byte_ranges_are_non_overlapping(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    chunks = _chunk_document(f)
    for i in range(len(chunks) - 1):
        assert chunks[i].byte_end <= chunks[i + 1].byte_start


def test_chunk_document_byte_ranges_cover_entire_file(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    raw = f.read_bytes()
    chunks = _chunk_document(f)
    covered = sum(c.byte_end - c.byte_start for c in chunks)
    assert covered == len(raw)


def test_chunk_document_source_id_is_file_path(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    chunks = _chunk_document(f)
    assert all(c.source_id == f.as_posix() for c in chunks)


def test_chunk_document_no_headings_produces_one_chunk(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "noheading.md", "Just some plain text with no headings.\n")
    chunks = _chunk_document(f)
    assert len(chunks) == 1
    assert chunks[0].heading == "Preamble"


def test_chunk_document_empty_file_raises(tmp_path: Path) -> None:
    f = tmp_path / "empty.md"
    f.write_bytes(b"")
    with pytest.raises(KnowledgeBaseError) as exc:
        _chunk_document(f)
    assert "empty" in exc.value.reason


def test_chunk_document_preamble_collected_before_first_heading(tmp_path: Path) -> None:
    md = "Preamble text before any heading.\n\n# First Heading\n\nContent.\n"
    f = _make_markdown(tmp_path, "preamble.md", md)
    chunks = _chunk_document(f)
    assert chunks[0].heading == "Preamble"
    assert "Preamble text" in chunks[0].body
    assert chunks[1].heading == "# First Heading"


# ---------------------------------------------------------------------------
# KnowledgeBase — build
# ---------------------------------------------------------------------------


def test_knowledge_base_build_creates_chunks(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    kb.build([f])
    assert kb.chunk_count == 3
    assert kb.document_count == 1


def test_knowledge_base_build_multiple_files(tmp_path: Path) -> None:
    f1 = _make_markdown(tmp_path, "doc1.md", _SIMPLE_MD)
    f2 = _make_markdown(tmp_path, "doc2.md", "# Only One Section\n\nContent here.\n")
    kb = KnowledgeBase()
    kb.build([f1, f2])
    assert kb.document_count == 2
    assert kb.chunk_count == 4  # 3 from doc1 + 1 from doc2


def test_knowledge_base_duplicate_ingestion_raises(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    kb.build([f])
    with pytest.raises(KnowledgeBaseError) as exc:
        kb.build([f])
    assert "already been ingested" in exc.value.reason


def test_knowledge_base_source_ids_contains_ingested_files(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    kb.build([f])
    assert f.as_posix() in kb.source_ids()


def test_knowledge_base_source_ids_is_frozenset(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    kb.build([f])
    assert isinstance(kb.source_ids(), frozenset)


# ---------------------------------------------------------------------------
# KnowledgeBase — search
# ---------------------------------------------------------------------------


def test_search_returns_citation_objects(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    kb.build([f])
    results = kb.search("determination pipeline")
    assert all(isinstance(r.citation, Citation) for r in results)


def test_search_returns_search_result_objects(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    kb.build([f])
    results = kb.search("determination pipeline")
    assert all(isinstance(r, SearchResult) for r in results)


def test_search_best_match_contains_query_terms(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    kb.build([f])
    results = kb.search("inclusive extraction threshold")
    # The second section covers those terms.
    assert results[0].chunk.heading == "## Section Two"


def test_search_results_respect_top_k(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    kb.build([f])
    results = kb.search("section", top_k=2)
    assert len(results) <= 2


def test_search_empty_result_for_nonexistent_term(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    kb.build([f])
    results = kb.search("xyzzy completely absent term")
    assert results == []


def test_search_citation_id_is_deterministic(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb1 = KnowledgeBase()
    kb1.build([f])
    kb2 = KnowledgeBase()
    kb2.build([f])
    r1 = kb1.search("determination pipeline")
    r2 = kb2.search("determination pipeline")
    assert r1[0].citation.citation_id == r2[0].citation.citation_id


def test_search_short_query_raises(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    kb.build([f])
    with pytest.raises(KnowledgeBaseError) as exc:
        kb.search("x")
    assert "too short" in exc.value.reason


def test_search_before_build_raises() -> None:
    kb = KnowledgeBase()
    with pytest.raises(KnowledgeBaseError) as exc:
        kb.search("anything")
    assert "build()" in exc.value.reason


def test_search_whitespace_only_query_raises(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    kb.build([f])
    with pytest.raises(KnowledgeBaseError) as exc:
        kb.search("   ")
    assert "too short" in exc.value.reason


# ---------------------------------------------------------------------------
# Chunk — immutability
# ---------------------------------------------------------------------------


def test_chunk_is_frozen(tmp_path: Path) -> None:
    from dataclasses import FrozenInstanceError
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    chunks = _chunk_document(f)
    with pytest.raises(FrozenInstanceError):
        chunks[0].heading = "mutated"  # type: ignore[misc]


# ---------------------------------------------------------------------------
# SearchResult — structure
# ---------------------------------------------------------------------------


def test_search_result_exposes_chunk_and_citation(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    kb.build([f])
    result = kb.search("BM25 ranking")[0]
    assert isinstance(result.chunk, Chunk)
    assert isinstance(result.citation, Citation)
    assert isinstance(result.rank, float)


# ---------------------------------------------------------------------------
# Real spec documents (skipped if docs/specs/ is not available)
# ---------------------------------------------------------------------------


@pytest.mark.skipif(not _SPECS_AVAILABLE, reason="docs/specs/ not found")
def test_real_spec_det001_is_indexed() -> None:
    kb = KnowledgeBase()
    kb.build([_DET_SPEC])
    assert kb.chunk_count > 5
    assert _DET_SPEC.as_posix() in kb.source_ids()


@pytest.mark.skipif(not _SPECS_AVAILABLE, reason="docs/specs/ not found")
def test_real_spec_jur001_is_indexed() -> None:
    kb = KnowledgeBase()
    kb.build([_JUR_SPEC])
    assert kb.chunk_count > 5
    assert _JUR_SPEC.as_posix() in kb.source_ids()


@pytest.mark.skipif(not _SPECS_AVAILABLE, reason="docs/specs/ not found")
def test_real_spec_search_determination_pipeline() -> None:
    kb = KnowledgeBase()
    kb.build([_DET_SPEC])
    results = kb.search("determination pipeline canonicalise")
    assert len(results) > 0
    assert all(isinstance(r.citation, Citation) for r in results)


@pytest.mark.skipif(not _SPECS_AVAILABLE, reason="docs/specs/ not found")
def test_real_spec_search_jurisdiction_resolution() -> None:
    kb = KnowledgeBase()
    kb.build([_JUR_SPEC])
    results = kb.search("jurisdiction resolution evidence")
    assert len(results) > 0
    assert all(isinstance(r.citation, Citation) for r in results)


@pytest.mark.skipif(not _SPECS_AVAILABLE, reason="docs/specs/ not found")
def test_real_spec_both_documents_together() -> None:
    kb = KnowledgeBase()
    kb.build([_DET_SPEC, _JUR_SPEC])
    assert kb.document_count == 2
    results = kb.search("tax determination")
    assert len(results) > 0


@pytest.mark.skipif(not _SPECS_AVAILABLE, reason="docs/specs/ not found")
def test_real_spec_citation_verify_all_passes() -> None:
    """Every citation returned from the real spec must verify its own hash."""
    kb = KnowledgeBase()
    kb.build([_DET_SPEC])
    results = kb.search("determination pipeline")
    assert all(r.citation.verify_all() for r in results)
