"""RAG Index Lifecycle -- Master Specification Ch.17 §9/§10.

Covers:
  - retire: source excluded from every search mode (CURRENT, AS_KNOWN_THEN,
    historical_context=True)
  - purge: source removed from search; direct SQLite inspection proves no row
    or text remains; in-memory structures clean
  - tombstone fields (source_id, action, reason, at, content_sha256,
    chunk_count) and ordering (operation order)
  - every error case: unknown source, retire already-retired, retire
    already-purged, purge already-purged, naive datetime, re-ingest refused
  - purge after retire allowed (two tombstones)
  - counts (document_count, chunk_count, source_ids) after retire and purge
  - re-ingest of retired or purged source refused
  - default behaviour unchanged for sources never touched by lifecycle ops
  - content_sha256 is deterministic across identical builds
  - top_k filled from remaining sources after a retire
"""

from __future__ import annotations

from dataclasses import FrozenInstanceError
from datetime import UTC, datetime
from pathlib import Path

import pytest

import ztax_gateway.rag as _rag_module
from ztax_gateway.rag import (
    KnowledgeBase,
    KnowledgeBaseError,
    LifecycleAction,
    LifecycleReason,
    TemporalMode,
)

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_NOW = datetime(2025, 6, 1, tzinfo=UTC)
_REASON = LifecycleReason.LICENCE_WITHDRAWN

_MD_A = """\
# Alpha Section

Alpha content about tax determination pipeline.

## Alpha Sub

More alpha content with threshold interactions.
"""

_MD_B = """\
# Beta Section

Beta content about BM25 ranking retrieval.

## Beta Sub

More beta content with inclusive extraction.
"""

_MD_C = """\
# Gamma Section

Gamma content about jurisdiction situs.
"""


def _make_file(tmp_path: Path, name: str, content: str) -> Path:
    p = tmp_path / name
    p.write_text(content, encoding="utf-8")
    return p


def _build_two(tmp_path: Path) -> tuple[KnowledgeBase, str, str]:
    """Build a KnowledgeBase with two separate source files; return kb, sid_a, sid_b."""
    fa = _make_file(tmp_path, "alpha.md", _MD_A)
    fb = _make_file(tmp_path, "beta.md", _MD_B)
    kb = KnowledgeBase()
    kb.build([fa, fb])
    return kb, fa.as_posix(), fb.as_posix()


def _pin_now(monkeypatch: pytest.MonkeyPatch, now: datetime) -> None:
    monkeypatch.setattr(_rag_module, "_utcnow", lambda: now)


# ===========================================================================
# Tombstone dataclass integrity
# ===========================================================================


def test_tombstone_is_frozen(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    t = kb.retire(sid_a, reason=_REASON, at=_NOW)
    with pytest.raises(FrozenInstanceError):
        t.source_id = "other"  # type: ignore[misc]


def test_tombstone_fields_retire(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    t = kb.retire(sid_a, reason=LifecycleReason.PACK_WITHDRAWN, at=_NOW)
    assert t.source_id == sid_a
    assert t.action is LifecycleAction.RETIRE
    assert t.reason is LifecycleReason.PACK_WITHDRAWN
    assert t.at == _NOW
    assert isinstance(t.content_sha256, str) and len(t.content_sha256) == 64
    assert t.chunk_count > 0


def test_tombstone_fields_purge(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    t = kb.purge(sid_a, reason=LifecycleReason.PRIVACY_DELETION, at=_NOW)
    assert t.source_id == sid_a
    assert t.action is LifecycleAction.PURGE
    assert t.reason is LifecycleReason.PRIVACY_DELETION
    assert t.at == _NOW
    assert len(t.content_sha256) == 64
    assert t.chunk_count > 0


def test_tombstone_chunk_count_matches_actual(tmp_path: Path) -> None:
    fa = _make_file(tmp_path, "alpha.md", _MD_A)
    kb = KnowledgeBase()
    kb.build([fa])
    sid = fa.as_posix()
    # _MD_A has 2 headings → 2 chunks
    t = kb.retire(sid, reason=_REASON, at=_NOW)
    assert t.chunk_count == 2


# ===========================================================================
# Tombstones audit trail ordering
# ===========================================================================


def test_tombstones_order_retire_then_purge(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    t1 = kb.retire(sid_a, reason=_REASON, at=_NOW)
    t2 = kb.purge(sid_a, reason=LifecycleReason.PRIVACY_DELETION, at=_NOW)
    trail = kb.tombstones()
    assert trail == (t1, t2)
    assert trail[0].action is LifecycleAction.RETIRE
    assert trail[1].action is LifecycleAction.PURGE


def test_tombstones_order_two_sources(tmp_path: Path) -> None:
    kb, sid_a, sid_b = _build_two(tmp_path)
    t1 = kb.retire(sid_a, reason=_REASON, at=_NOW)
    t2 = kb.purge(sid_b, reason=LifecycleReason.PACK_WITHDRAWN, at=_NOW)
    trail = kb.tombstones()
    assert trail == (t1, t2)


def test_tombstones_empty_initially(tmp_path: Path) -> None:
    kb, _, _ = _build_two(tmp_path)
    assert kb.tombstones() == ()


def test_tombstones_returns_tuple(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    kb.retire(sid_a, reason=_REASON, at=_NOW)
    assert isinstance(kb.tombstones(), tuple)


# ===========================================================================
# Default at uses _utcnow()
# ===========================================================================


def test_retire_default_at_uses_utcnow(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    _pin_now(monkeypatch, _NOW)
    kb, sid_a, _ = _build_two(tmp_path)
    t = kb.retire(sid_a, reason=_REASON)
    assert t.at == _NOW


def test_purge_default_at_uses_utcnow(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    _pin_now(monkeypatch, _NOW)
    kb, sid_a, _ = _build_two(tmp_path)
    t = kb.purge(sid_a, reason=_REASON)
    assert t.at == _NOW


# ===========================================================================
# Retire: search exclusion
# ===========================================================================


def test_retire_excluded_from_current_mode(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    _pin_now(monkeypatch, _NOW)
    kb, sid_a, _ = _build_two(tmp_path)
    kb.retire(sid_a, reason=_REASON, at=_NOW)
    results = kb.search("alpha tax", top_k=10)
    assert all(r.chunk.source_id != sid_a for r in results)


def test_retire_excluded_from_as_known_then(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    kb.retire(sid_a, reason=_REASON, at=_NOW)
    results = kb.search("alpha", top_k=10, mode=TemporalMode.AS_KNOWN_THEN, as_of=_NOW)
    assert all(r.chunk.source_id != sid_a for r in results)


def test_retire_excluded_from_historical_context(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    _pin_now(monkeypatch, _NOW)
    past = datetime(2020, 1, 1, tzinfo=UTC)
    future = datetime(2024, 1, 1, tzinfo=UTC)
    fa = _make_file(tmp_path, "alpha.md", _MD_A)
    fb = _make_file(tmp_path, "beta.md", _MD_B)
    kb = KnowledgeBase()
    kb.build([fa, fb], effective_from=past, effective_to=future)
    sid_a = fa.as_posix()
    kb.retire(sid_a, reason=_REASON, at=_NOW)
    results = kb.search("alpha", top_k=10, historical_context=True)
    assert all(r.chunk.source_id != sid_a for r in results)


def test_retire_other_source_still_searchable(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    _pin_now(monkeypatch, _NOW)
    kb, sid_a, sid_b = _build_two(tmp_path)
    kb.retire(sid_a, reason=_REASON, at=_NOW)
    results = kb.search("beta BM25", top_k=10)
    assert any(r.chunk.source_id == sid_b for r in results)


# ===========================================================================
# Retire: top_k filled from remaining sources
# ===========================================================================


def test_retire_topk_filled_from_remaining(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """After retiring one source, top_k=1 must return a result from the other source."""
    _pin_now(monkeypatch, _NOW)
    fa = _make_file(tmp_path, "alpha.md", _MD_A)
    fb = _make_file(tmp_path, "beta.md", _MD_B)
    fc = _make_file(tmp_path, "gamma.md", _MD_C)
    kb = KnowledgeBase()
    kb.build([fa, fb, fc])
    sid_b = fb.as_posix()
    sid_c = fc.as_posix()
    kb.retire(fb.as_posix(), reason=_REASON, at=_NOW)
    kb.retire(fc.as_posix(), reason=_REASON, at=_NOW)
    # Retire beta and gamma; query terms also present in alpha must return alpha.
    results = kb.search("tax determination pipeline", top_k=1)
    assert len(results) == 1
    assert results[0].chunk.source_id not in {sid_b, sid_c}


# ===========================================================================
# Purge: search exclusion
# ===========================================================================


def test_purge_excluded_from_search(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    _pin_now(monkeypatch, _NOW)
    kb, sid_a, _ = _build_two(tmp_path)
    kb.purge(sid_a, reason=_REASON, at=_NOW)
    results = kb.search("alpha tax", top_k=10)
    assert all(r.chunk.source_id != sid_a for r in results)


def test_purge_excluded_from_historical_context(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    _pin_now(monkeypatch, _NOW)
    past = datetime(2020, 1, 1, tzinfo=UTC)
    future = datetime(2024, 1, 1, tzinfo=UTC)
    fa = _make_file(tmp_path, "alpha.md", _MD_A)
    fb = _make_file(tmp_path, "beta.md", _MD_B)
    kb = KnowledgeBase()
    kb.build([fa, fb], effective_from=past, effective_to=future)
    sid_a = fa.as_posix()
    kb.purge(sid_a, reason=_REASON, at=_NOW)
    results = kb.search("alpha", top_k=10, historical_context=True)
    assert all(r.chunk.source_id != sid_a for r in results)


# ===========================================================================
# Purge: physical deletion verified via private connection
# ===========================================================================


def test_purge_removes_rows_from_sqlite(tmp_path: Path) -> None:
    """Direct SQLite inspection: no rows remain for the purged source_id."""
    kb, sid_a, _ = _build_two(tmp_path)
    kb.purge(sid_a, reason=_REASON, at=_NOW)
    count = kb._conn.execute(
        "SELECT count(*) FROM chunks WHERE source_id = ?", (sid_a,)
    ).fetchone()[0]
    assert count == 0


def test_purge_removes_from_in_memory_chunks(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    kb.purge(sid_a, reason=_REASON, at=_NOW)
    # No chunk should reference the purged source_id.
    assert all(c.source_id != sid_a for c in kb._chunks.values())


def test_purge_removes_from_citations(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    before_cid_count = len(kb._citations)
    kb.purge(sid_a, reason=_REASON, at=_NOW)
    after_cid_count = len(kb._citations)
    assert after_cid_count < before_cid_count


# ===========================================================================
# Counts after lifecycle ops
# ===========================================================================


def test_retire_does_not_change_document_count(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    before = kb.document_count
    kb.retire(sid_a, reason=_REASON, at=_NOW)
    # retire keeps the source in _ingested so count is unchanged
    assert kb.document_count == before


def test_purge_decreases_document_count(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    before = kb.document_count
    kb.purge(sid_a, reason=_REASON, at=_NOW)
    assert kb.document_count == before - 1


def test_retire_does_not_change_chunk_count(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    before = kb.chunk_count
    kb.retire(sid_a, reason=_REASON, at=_NOW)
    assert kb.chunk_count == before


def test_purge_decreases_chunk_count(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    before = kb.chunk_count
    kb.purge(sid_a, reason=_REASON, at=_NOW)
    assert kb.chunk_count < before


def test_retire_source_ids_unchanged(tmp_path: Path) -> None:
    kb, sid_a, _sid_b = _build_two(tmp_path)
    kb.retire(sid_a, reason=_REASON, at=_NOW)
    # retired source still appears in source_ids (chunk text retained)
    assert sid_a in kb.source_ids()


def test_purge_removes_from_source_ids(tmp_path: Path) -> None:
    kb, sid_a, sid_b = _build_two(tmp_path)
    kb.purge(sid_a, reason=_REASON, at=_NOW)
    assert sid_a not in kb.source_ids()
    assert sid_b in kb.source_ids()


# ===========================================================================
# Purge after retire
# ===========================================================================


def test_purge_after_retire_produces_two_tombstones(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    kb.retire(sid_a, reason=_REASON, at=_NOW)
    kb.purge(sid_a, reason=LifecycleReason.PRIVACY_DELETION, at=_NOW)
    trail = kb.tombstones()
    assert len(trail) == 2
    assert trail[0].action is LifecycleAction.RETIRE
    assert trail[1].action is LifecycleAction.PURGE


def test_purge_after_retire_excludes_from_search(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    _pin_now(monkeypatch, _NOW)
    kb, sid_a, _ = _build_two(tmp_path)
    kb.retire(sid_a, reason=_REASON, at=_NOW)
    kb.purge(sid_a, reason=LifecycleReason.PRIVACY_DELETION, at=_NOW)
    results = kb.search("alpha", top_k=10)
    assert all(r.chunk.source_id != sid_a for r in results)


# ===========================================================================
# Error cases
# ===========================================================================


def test_retire_unknown_source_raises(tmp_path: Path) -> None:
    kb, _, _ = _build_two(tmp_path)
    with pytest.raises(KnowledgeBaseError, match="not in the index"):
        kb.retire("nonexistent/source.md", reason=_REASON, at=_NOW)


def test_purge_unknown_source_raises(tmp_path: Path) -> None:
    kb, _, _ = _build_two(tmp_path)
    with pytest.raises(KnowledgeBaseError, match="not in the index"):
        kb.purge("nonexistent/source.md", reason=_REASON, at=_NOW)


def test_retire_twice_raises(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    kb.retire(sid_a, reason=_REASON, at=_NOW)
    with pytest.raises(KnowledgeBaseError, match="already been retired"):
        kb.retire(sid_a, reason=_REASON, at=_NOW)


def test_purge_twice_raises(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    kb.purge(sid_a, reason=_REASON, at=_NOW)
    with pytest.raises(KnowledgeBaseError, match="already been purged"):
        kb.purge(sid_a, reason=_REASON, at=_NOW)


def test_retire_purged_source_raises(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    kb.purge(sid_a, reason=_REASON, at=_NOW)
    with pytest.raises(KnowledgeBaseError, match="already been purged"):
        kb.retire(sid_a, reason=_REASON, at=_NOW)


def test_retire_naive_datetime_raises(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    naive = datetime(2025, 6, 1)  # no tzinfo
    with pytest.raises(KnowledgeBaseError, match="naive"):
        kb.retire(sid_a, reason=_REASON, at=naive)


def test_purge_naive_datetime_raises(tmp_path: Path) -> None:
    kb, sid_a, _ = _build_two(tmp_path)
    naive = datetime(2025, 6, 1)  # no tzinfo
    with pytest.raises(KnowledgeBaseError, match="naive"):
        kb.purge(sid_a, reason=_REASON, at=naive)


# ===========================================================================
# Re-ingest after retire/purge refused
# ===========================================================================


def test_reingest_after_retire_raises(tmp_path: Path) -> None:
    fa = _make_file(tmp_path, "alpha.md", _MD_A)
    kb = KnowledgeBase()
    kb.build([fa])
    kb.retire(fa.as_posix(), reason=_REASON, at=_NOW)
    # Try to ingest the very same path again -- must be refused.
    with pytest.raises(KnowledgeBaseError, match="retired or purged"):
        kb.build([fa])


def test_reingest_after_purge_raises(tmp_path: Path) -> None:
    fa = _make_file(tmp_path, "alpha.md", _MD_A)
    kb = KnowledgeBase()
    kb.build([fa])
    kb.purge(fa.as_posix(), reason=_REASON, at=_NOW)
    with pytest.raises(KnowledgeBaseError, match="retired or purged"):
        kb.build([fa])


# ===========================================================================
# Default behaviour unchanged for untouched sources
# ===========================================================================


def test_untouched_source_searchable(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    _pin_now(monkeypatch, _NOW)
    kb, sid_a, sid_b = _build_two(tmp_path)
    kb.retire(sid_a, reason=_REASON, at=_NOW)
    results = kb.search("beta BM25 retrieval", top_k=10)
    assert any(r.chunk.source_id == sid_b for r in results)


def test_no_lifecycle_no_tombstones(tmp_path: Path) -> None:
    kb, _, _ = _build_two(tmp_path)
    assert kb.tombstones() == ()
    assert kb.document_count == 2


# ===========================================================================
# Deterministic content_sha256
# ===========================================================================


def test_content_sha256_deterministic(tmp_path: Path) -> None:
    """The same source content always produces the same SHA-256 digest."""
    run1 = tmp_path / "run1"
    run1.mkdir()
    fa1 = run1 / "alpha.md"
    fa1.write_text(_MD_A, encoding="utf-8")

    run2 = tmp_path / "run2"
    run2.mkdir()
    fa2 = run2 / "alpha.md"
    fa2.write_text(_MD_A, encoding="utf-8")

    kb1 = KnowledgeBase()
    kb1.build([fa1])
    t1 = kb1.retire(fa1.as_posix(), reason=_REASON, at=_NOW)

    kb2 = KnowledgeBase()
    kb2.build([fa2])
    t2 = kb2.retire(fa2.as_posix(), reason=_REASON, at=_NOW)

    # Different posix paths, but same content → same sha256
    # (sha is over content_hash values, not path)
    assert t1.content_sha256 == t2.content_sha256


def test_content_sha256_differs_for_different_content(tmp_path: Path) -> None:
    fa = _make_file(tmp_path, "alpha.md", _MD_A)
    fb = _make_file(tmp_path, "beta.md", _MD_B)
    kb = KnowledgeBase()
    kb.build([fa, fb])
    t1 = kb.retire(fa.as_posix(), reason=_REASON, at=_NOW)
    t2 = kb.purge(fb.as_posix(), reason=_REASON, at=_NOW)
    assert t1.content_sha256 != t2.content_sha256


# ===========================================================================
# LifecycleAction and LifecycleReason are StrEnums
# ===========================================================================


def test_lifecycle_action_values() -> None:
    assert LifecycleAction.RETIRE == "RETIRE"
    assert LifecycleAction.PURGE == "PURGE"


def test_lifecycle_reason_values() -> None:
    assert LifecycleReason.LICENCE_WITHDRAWN == "LICENCE_WITHDRAWN"
    assert LifecycleReason.PRIVACY_DELETION == "PRIVACY_DELETION"
    assert LifecycleReason.PACK_WITHDRAWN == "PACK_WITHDRAWN"
