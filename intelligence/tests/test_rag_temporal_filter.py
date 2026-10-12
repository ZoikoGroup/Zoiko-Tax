"""RAG valid-time filtering -- Chapter 17 ss9 and ss10.

Tests that KnowledgeBase.build and KnowledgeBase.search correctly enforce the
CURRENT / AS_KNOWN_THEN contract defined in the master spec.

Valid-time window semantics::

    IN_FORCE          -- effective_from <= ref < effective_to
    SUPERSEDED        -- ref >= effective_to
    NOT_YET_EFFECTIVE -- ref < effective_from

A None bound is open-ended (-inf or +inf).

CURRENT mode uses _utcnow() as the reference; tests monkeypatch it.
AS_KNOWN_THEN requires a timezone-aware as_of datetime.
"""

from __future__ import annotations

from datetime import UTC, datetime
from pathlib import Path

import pytest

import ztax_gateway.rag as _rag_module
from ztax_gateway.rag import (
    KnowledgeBase,
    KnowledgeBaseError,
    SearchResult,
    TemporalMode,
    TemporalStatus,
)

# ---------------------------------------------------------------------------
# Paths to real spec documents
# ---------------------------------------------------------------------------

_REPO_ROOT = Path(__file__).resolve().parents[2]
_SPECS_DIR = _REPO_ROOT / "docs" / "specs"
_DET_SPEC = _SPECS_DIR / "ZTAX-DET-001-global-tax-determination-engine.md"
_JUR_SPEC = _SPECS_DIR / "ZTAX-JUR-001-jurisdiction-situs-and-place-of-supply.md"
_SPECS_AVAILABLE = _DET_SPEC.exists() and _JUR_SPEC.exists()

# ---------------------------------------------------------------------------
# Fixed temporal reference points
# ---------------------------------------------------------------------------

_JAN_2024 = datetime(2024, 1, 1, tzinfo=UTC)
_JUL_2024 = datetime(2024, 7, 1, tzinfo=UTC)
_JAN_2025 = datetime(2025, 1, 1, tzinfo=UTC)
_JUL_2025 = datetime(2025, 7, 1, tzinfo=UTC)
_JAN_2026 = datetime(2026, 1, 1, tzinfo=UTC)

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_SIMPLE_MD = """\
# Section One

This section covers the determination pipeline.

## Section Two

Inclusive extraction and threshold interactions.

### Section Three

BM25 ranking is used for retrieval.
"""


def _make_markdown(tmp_path: Path, name: str, content: str) -> Path:
    p = tmp_path / name
    p.write_text(content, encoding="utf-8")
    return p


def _build_single(
    tmp_path: Path,
    *,
    effective_from: datetime | None = None,
    effective_to: datetime | None = None,
) -> KnowledgeBase:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    kb.build([f], effective_from=effective_from, effective_to=effective_to)
    return kb


# Monkeypatch helper: force _utcnow() to return a fixed datetime.
def _pin_now(monkeypatch: pytest.MonkeyPatch, now: datetime) -> None:
    monkeypatch.setattr(_rag_module, "_utcnow", lambda: now)


# ===========================================================================
# TemporalMode is a real enum
# ===========================================================================


def test_temporal_mode_is_enum() -> None:
    import enum
    assert isinstance(TemporalMode.CURRENT, TemporalMode)
    assert isinstance(TemporalMode.AS_KNOWN_THEN, TemporalMode)
    assert issubclass(TemporalMode, enum.Enum)


def test_temporal_status_is_enum() -> None:
    import enum
    assert issubclass(TemporalStatus, enum.Enum)
    assert TemporalStatus.IN_FORCE
    assert TemporalStatus.SUPERSEDED
    assert TemporalStatus.NOT_YET_EFFECTIVE


# ===========================================================================
# CURRENT mode -- filters by _utcnow()
# ===========================================================================


def test_current_mode_excludes_expired_source(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """CURRENT mode must NOT return chunks whose effective_to is in the past."""
    # Chunk expired Jan 2025; pin now to Jan 2026 so it is clearly superseded.
    kb = _build_single(tmp_path, effective_from=_JAN_2024, effective_to=_JAN_2025)
    _pin_now(monkeypatch, _JAN_2026)
    results = kb.search("section")
    assert results == [], "expired chunks must be excluded in CURRENT mode"


def test_current_mode_excludes_future_effective_source(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """CURRENT mode must NOT return chunks whose effective_from is in the future."""
    kb = _build_single(tmp_path, effective_from=_JAN_2026)
    _pin_now(monkeypatch, _JAN_2025)  # now is before effective_from
    results = kb.search("section")
    assert results == [], "not-yet-effective chunks must be excluded in CURRENT mode"


def test_current_mode_returns_in_force_chunks(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """CURRENT mode returns chunks that are IN_FORCE at _utcnow()."""
    kb = _build_single(tmp_path, effective_from=_JAN_2024, effective_to=_JAN_2026)
    _pin_now(monkeypatch, _JAN_2025)
    results = kb.search("section")
    assert len(results) == 3
    assert all(r.temporal_status is TemporalStatus.IN_FORCE for r in results)


def test_current_mode_open_window_always_in_force(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """A chunk with no effective dates is always IN_FORCE."""
    kb = _build_single(tmp_path)
    _pin_now(monkeypatch, _JAN_2025)
    results = kb.search("section")
    assert len(results) == 3
    assert all(r.temporal_status is TemporalStatus.IN_FORCE for r in results)


def test_current_mode_with_as_of_raises(tmp_path: Path) -> None:
    """Supplying as_of with CURRENT mode must raise KnowledgeBaseError."""
    kb = _build_single(tmp_path)
    with pytest.raises(KnowledgeBaseError) as exc:
        kb.search("section", mode=TemporalMode.CURRENT, as_of=_JAN_2025)
    assert "AS_KNOWN_THEN" in exc.value.reason or "as_of" in exc.value.reason


# ===========================================================================
# AS_KNOWN_THEN mode
# ===========================================================================


def test_as_known_then_requires_as_of(tmp_path: Path) -> None:
    """AS_KNOWN_THEN without as_of must raise KnowledgeBaseError."""
    kb = _build_single(tmp_path)
    with pytest.raises(KnowledgeBaseError) as exc:
        kb.search("section", mode=TemporalMode.AS_KNOWN_THEN)
    assert "as_of" in exc.value.reason


def test_as_known_then_naive_as_of_raises(tmp_path: Path) -> None:
    """A naive as_of must be rejected."""
    kb = _build_single(tmp_path)
    with pytest.raises(KnowledgeBaseError) as exc:
        kb.search("section", mode=TemporalMode.AS_KNOWN_THEN, as_of=datetime(2025, 1, 1))
    assert "naive" in exc.value.reason


def test_as_known_then_in_force_within_window(tmp_path: Path) -> None:
    kb = _build_single(tmp_path, effective_from=_JAN_2024, effective_to=_JAN_2026)
    results = kb.search("section", mode=TemporalMode.AS_KNOWN_THEN, as_of=_JAN_2025)
    assert len(results) > 0
    assert all(r.temporal_status is TemporalStatus.IN_FORCE for r in results)


def test_as_known_then_before_effective_from_returns_empty(tmp_path: Path) -> None:
    """as_of before effective_from: chunk is NOT_YET_EFFECTIVE, never returned."""
    kb = _build_single(tmp_path, effective_from=_JAN_2025)
    results = kb.search("section", mode=TemporalMode.AS_KNOWN_THEN, as_of=_JAN_2024)
    assert results == []


def test_as_known_then_effective_from_inclusive(tmp_path: Path) -> None:
    """effective_from is inclusive: as_of == effective_from -> IN_FORCE."""
    kb = _build_single(tmp_path, effective_from=_JAN_2025)
    results = kb.search("determination pipeline", mode=TemporalMode.AS_KNOWN_THEN, as_of=_JAN_2025)
    assert len(results) > 0
    assert all(r.temporal_status is TemporalStatus.IN_FORCE for r in results)


def test_as_known_then_effective_to_exclusive(tmp_path: Path) -> None:
    """effective_to is exclusive: as_of == effective_to -> SUPERSEDED."""
    kb = _build_single(tmp_path, effective_to=_JAN_2025)
    # Without historical_context, SUPERSEDED is not returned.
    results = kb.search("determination pipeline", mode=TemporalMode.AS_KNOWN_THEN, as_of=_JAN_2025)
    assert results == []


def test_as_known_then_after_expiry_excluded_by_default(tmp_path: Path) -> None:
    kb = _build_single(tmp_path, effective_from=_JAN_2024, effective_to=_JUL_2024)
    results = kb.search("section", mode=TemporalMode.AS_KNOWN_THEN, as_of=_JAN_2025)
    assert results == []


def test_as_known_then_open_window_always_in_force(tmp_path: Path) -> None:
    kb = _build_single(tmp_path)
    results = kb.search("section", mode=TemporalMode.AS_KNOWN_THEN, as_of=_JAN_2025)
    assert len(results) > 0
    assert all(r.temporal_status is TemporalStatus.IN_FORCE for r in results)


# ===========================================================================
# historical_context=True returns SUPERSEDED, never NOT_YET_EFFECTIVE
# ===========================================================================


def test_historical_context_returns_superseded_tagged(tmp_path: Path) -> None:
    """With historical_context=True, SUPERSEDED chunks are returned and tagged."""
    kb = _build_single(tmp_path, effective_from=_JAN_2024, effective_to=_JUL_2024)
    # At _JAN_2025 the chunk is superseded.
    results = kb.search(
        "section",
        mode=TemporalMode.AS_KNOWN_THEN,
        as_of=_JAN_2025,
        historical_context=True,
    )
    assert len(results) > 0
    assert all(r.temporal_status is TemporalStatus.SUPERSEDED for r in results)


def test_historical_context_never_returns_not_yet_effective(tmp_path: Path) -> None:
    """NOT_YET_EFFECTIVE chunks are never returned even with historical_context."""
    kb = _build_single(tmp_path, effective_from=_JAN_2026)
    results = kb.search(
        "section",
        mode=TemporalMode.AS_KNOWN_THEN,
        as_of=_JAN_2025,
        historical_context=True,
    )
    assert results == []


def test_historical_context_false_excludes_superseded(tmp_path: Path) -> None:
    """historical_context=False (default) must exclude SUPERSEDED results."""
    kb = _build_single(tmp_path, effective_from=_JAN_2024, effective_to=_JUL_2024)
    results = kb.search(
        "section",
        mode=TemporalMode.AS_KNOWN_THEN,
        as_of=_JAN_2025,
        historical_context=False,
    )
    assert results == []


def test_historical_context_tags_are_never_plain_in_force_for_superseded(
    tmp_path: Path,
) -> None:
    """A SUPERSEDED result returned via historical_context must be tagged SUPERSEDED."""
    kb = _build_single(tmp_path, effective_from=_JAN_2024, effective_to=_JUL_2024)
    results = kb.search(
        "section",
        mode=TemporalMode.AS_KNOWN_THEN,
        as_of=_JAN_2025,
        historical_context=True,
    )
    for r in results:
        assert r.temporal_status is not TemporalStatus.IN_FORCE, (
            f"Chunk returned with IN_FORCE but it is superseded: {r.chunk.heading}"
        )


def test_historical_context_current_mode_excludes_superseded_too(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """historical_context is respected in CURRENT mode too."""
    kb = _build_single(tmp_path, effective_from=_JAN_2024, effective_to=_JUL_2024)
    _pin_now(monkeypatch, _JAN_2025)
    results = kb.search("section", historical_context=True)
    assert all(r.temporal_status is TemporalStatus.SUPERSEDED for r in results)


# ===========================================================================
# Regression: 25 expired high-ranking sources + 1 valid, top_k=2
# ===========================================================================


def test_regression_expired_high_rank_filtered_before_top_k(tmp_path: Path) -> None:
    """25 expired high-ranking sources plus one valid: AS_KNOWN_THEN top_k=2
    must return only the valid one (not the expired ones, even though they rank
    higher in FTS), proving filter-before-slice semantics.
    """
    # Build 25 expired documents, all containing "extraction" prominently.
    for i in range(25):
        f = _make_markdown(
            tmp_path,
            f"expired_{i}.md",
            f"# Expired extraction rule {i}\n\n"
            f"extraction extraction extraction rule {i} detail.\n",
        )
        kb_exp = KnowledgeBase()
        kb_exp.build([f], effective_from=_JAN_2024, effective_to=_JUL_2024)
        if i == 0:
            kb = kb_exp
        else:
            # Reuse the same KB for all expired + the valid one.
            pass

    # Build a fresh KB with all 25 expired docs + 1 valid doc.
    kb = KnowledgeBase()
    for i in range(25):
        fi = tmp_path / f"expired_{i}.md"
        kb.build([fi], effective_from=_JAN_2024, effective_to=_JUL_2024)

    f_valid = _make_markdown(
        tmp_path, "valid.md",
        "# Valid extraction rule\n\nThe valid extraction threshold is here.\n",
    )
    kb.build([f_valid], effective_from=_JUL_2024, effective_to=None)

    # At _JAN_2025 only the valid doc is IN_FORCE.
    results = kb.search(
        "extraction",
        top_k=2,
        mode=TemporalMode.AS_KNOWN_THEN,
        as_of=_JAN_2025,
    )
    assert len(results) == 1, f"Expected 1 valid result, got {len(results)}"
    assert results[0].temporal_status is TemporalStatus.IN_FORCE
    assert "Valid" in results[0].chunk.heading


# ===========================================================================
# Validation: inverted window rejected
# ===========================================================================


def test_effective_to_before_effective_from_raises_flat(tmp_path: Path) -> None:
    """effective_to <= effective_from must be refused."""
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    with pytest.raises(KnowledgeBaseError) as exc:
        kb.build([f], effective_from=_JAN_2025, effective_to=_JAN_2024)
    assert "effective_to" in exc.value.reason


def test_effective_to_equal_effective_from_raises_flat(tmp_path: Path) -> None:
    """effective_to == effective_from is also an inverted (zero-length) window."""
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    with pytest.raises(KnowledgeBaseError) as exc:
        kb.build([f], effective_from=_JAN_2025, effective_to=_JAN_2025)
    assert "effective_to" in exc.value.reason


def test_effective_to_before_effective_from_raises_per_doc(tmp_path: Path) -> None:
    """Inverted window inside effective_dates is refused."""
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    with pytest.raises(KnowledgeBaseError) as exc:
        kb.build([f], effective_dates={f.as_posix(): (_JAN_2025, _JAN_2024)})
    assert "effective_to" in exc.value.reason


# ===========================================================================
# Validation: unknown effective_dates key rejected
# ===========================================================================


def test_unknown_effective_dates_key_raises(tmp_path: Path) -> None:
    """A key in effective_dates that is not in paths must raise."""
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    with pytest.raises(KnowledgeBaseError) as exc:
        kb.build(
            [f],
            effective_dates={
                f.as_posix(): (_JAN_2024, _JAN_2026),   # valid
                "/not/a/real/path.md": (_JAN_2024, None),  # unknown key
            },
        )
    assert "not among the paths" in exc.value.reason


# ===========================================================================
# Naive datetime rejection
# ===========================================================================


def test_naive_effective_from_in_build_raises(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    with pytest.raises(KnowledgeBaseError) as exc:
        kb.build([f], effective_from=datetime(2025, 1, 1))
    assert "naive" in exc.value.reason


def test_naive_effective_to_in_build_raises(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    with pytest.raises(KnowledgeBaseError) as exc:
        kb.build([f], effective_to=datetime(2026, 1, 1))
    assert "naive" in exc.value.reason


def test_naive_effective_dates_raises(tmp_path: Path) -> None:
    f = _make_markdown(tmp_path, "doc.md", _SIMPLE_MD)
    kb = KnowledgeBase()
    with pytest.raises(KnowledgeBaseError) as exc:
        kb.build([f], effective_dates={f.as_posix(): (datetime(2025, 1, 1), None)})
    assert "naive" in exc.value.reason


# ===========================================================================
# Per-document effective_dates override
# ===========================================================================


def test_per_doc_dates_only_valid_doc_returned(tmp_path: Path) -> None:
    f1 = _make_markdown(tmp_path, "doc1.md", "# Alpha\n\nAlpha content here.\n")
    f2 = _make_markdown(tmp_path, "doc2.md", "# Beta\n\nBeta content here.\n")
    kb = KnowledgeBase()
    kb.build(
        [f1, f2],
        effective_dates={
            f1.as_posix(): (_JAN_2024, None),    # valid from Jan 2024
            f2.as_posix(): (_JAN_2026, None),    # not yet valid at Jan 2025
        },
    )
    results = kb.search("content", mode=TemporalMode.AS_KNOWN_THEN, as_of=_JAN_2025)
    headings = [r.chunk.heading for r in results]
    assert any("Alpha" in h for h in headings)
    assert not any("Beta" in h for h in headings)


def test_per_doc_dates_take_precedence_over_flat(tmp_path: Path) -> None:
    f1 = _make_markdown(tmp_path, "doc1.md", "# Alpha\n\nAlpha content here.\n")
    f2 = _make_markdown(tmp_path, "doc2.md", "# Beta\n\nBeta content here.\n")
    kb = KnowledgeBase()
    kb.build(
        [f1, f2],
        effective_from=_JAN_2026,   # flat default: not yet valid at Jan 2025
        effective_dates={f1.as_posix(): (_JAN_2024, None)},  # override: already valid
    )
    results = kb.search("content", mode=TemporalMode.AS_KNOWN_THEN, as_of=_JAN_2025)
    headings = [r.chunk.heading for r in results]
    assert any("Alpha" in h for h in headings)
    assert not any("Beta" in h for h in headings)


# ===========================================================================
# top_k respected after filtering
# ===========================================================================


def test_top_k_respected_after_filter(tmp_path: Path) -> None:
    kb = _build_single(tmp_path, effective_from=_JAN_2024)
    results = kb.search("section", top_k=1, mode=TemporalMode.AS_KNOWN_THEN, as_of=_JAN_2025)
    assert len(results) == 1


def test_top_k_zero_returns_empty(tmp_path: Path) -> None:
    kb = _build_single(tmp_path, effective_from=_JAN_2024)
    results = kb.search("section", top_k=0, mode=TemporalMode.AS_KNOWN_THEN, as_of=_JAN_2025)
    assert results == []


# ===========================================================================
# SearchResult carries temporal_status
# ===========================================================================


def test_search_result_has_temporal_status() -> None:
    """SearchResult must have temporal_status field defaulting to IN_FORCE."""
    import dataclasses
    fields = {f.name: f for f in dataclasses.fields(SearchResult)}
    assert "temporal_status" in fields
    assert fields["temporal_status"].default is TemporalStatus.IN_FORCE


def test_chunk_stores_effective_from(tmp_path: Path) -> None:
    kb = _build_single(tmp_path, effective_from=_JAN_2025)
    results = kb.search(
        "determination pipeline",
        mode=TemporalMode.AS_KNOWN_THEN,
        as_of=_JAN_2025,
    )
    assert all(r.chunk.effective_from == _JAN_2025 for r in results)


def test_chunk_stores_none_when_no_dates(tmp_path: Path) -> None:
    kb = _build_single(tmp_path)
    # CURRENT mode; pin now so open-window chunks are IN_FORCE.
    results = kb.search("determination pipeline")
    assert all(r.chunk.effective_from is None for r in results)
    assert all(r.chunk.effective_to is None for r in results)


# ===========================================================================
# Mixed windows
# ===========================================================================


def test_mixed_windows_only_valid_doc_returned(tmp_path: Path) -> None:
    f_old = _make_markdown(tmp_path, "old.md", "# Old rule\n\nThis rule about old extraction.\n")
    f_new = _make_markdown(tmp_path, "new.md", "# New rule\n\nThis rule about new extraction.\n")
    kb = KnowledgeBase()
    kb.build([f_old], effective_from=_JAN_2024, effective_to=_JUL_2024)
    kb.build([f_new], effective_from=_JUL_2024, effective_to=None)

    results = kb.search("extraction rule", mode=TemporalMode.AS_KNOWN_THEN, as_of=_JAN_2025)
    headings = [r.chunk.heading for r in results]
    assert any("New rule" in h for h in headings), headings
    assert not any("Old rule" in h for h in headings), headings


def test_mixed_windows_old_valid_at_early_date(tmp_path: Path) -> None:
    f_old = _make_markdown(tmp_path, "old.md", "# Old rule\n\nThis rule about old extraction.\n")
    f_new = _make_markdown(tmp_path, "new.md", "# New rule\n\nThis rule about new extraction.\n")
    kb = KnowledgeBase()
    kb.build([f_old], effective_from=_JAN_2024, effective_to=_JUL_2024)
    kb.build([f_new], effective_from=_JUL_2024, effective_to=None)

    results = kb.search("extraction rule", mode=TemporalMode.AS_KNOWN_THEN, as_of=_JAN_2024)
    headings = [r.chunk.heading for r in results]
    assert any("Old rule" in h for h in headings), headings
    assert not any("New rule" in h for h in headings), headings


# ===========================================================================
# Real spec documents (skipped when docs/specs/ unavailable)
# ===========================================================================


@pytest.mark.skipif(not _SPECS_AVAILABLE, reason="docs/specs/ not found")
def test_real_spec_current_mode_returns_results() -> None:
    kb = KnowledgeBase()
    kb.build([_DET_SPEC], effective_from=_JAN_2025, effective_to=_JAN_2026)
    # Pin _utcnow to inside the window so CURRENT returns results.
    import ztax_gateway.rag as _m
    original = _m._utcnow
    _m._utcnow = lambda: _JUL_2025
    try:
        results = kb.search("determination pipeline")
        assert len(results) > 0
    finally:
        _m._utcnow = original


@pytest.mark.skipif(not _SPECS_AVAILABLE, reason="docs/specs/ not found")
def test_real_spec_as_known_then_within_window() -> None:
    kb = KnowledgeBase()
    kb.build([_DET_SPEC], effective_from=_JAN_2025, effective_to=_JAN_2026)
    results = kb.search(
        "determination pipeline",
        mode=TemporalMode.AS_KNOWN_THEN,
        as_of=_JUL_2025,
    )
    assert len(results) > 0


@pytest.mark.skipif(not _SPECS_AVAILABLE, reason="docs/specs/ not found")
def test_real_spec_as_known_then_after_expiry_empty() -> None:
    kb = KnowledgeBase()
    kb.build([_DET_SPEC], effective_from=_JAN_2024, effective_to=_JUL_2024)
    results = kb.search(
        "determination pipeline",
        mode=TemporalMode.AS_KNOWN_THEN,
        as_of=_JAN_2025,
    )
    assert results == []


@pytest.mark.skipif(not _SPECS_AVAILABLE, reason="docs/specs/ not found")
def test_real_spec_per_document_dates() -> None:
    kb = KnowledgeBase()
    kb.build(
        [_DET_SPEC, _JUR_SPEC],
        effective_dates={
            _DET_SPEC.as_posix(): (_JAN_2024, _JUL_2025),
            _JUR_SPEC.as_posix(): (_JUL_2025, None),
        },
    )
    results = kb.search(
        "determination",
        mode=TemporalMode.AS_KNOWN_THEN,
        as_of=_JAN_2025,
    )
    source_ids = {r.chunk.source_id for r in results}
    assert _DET_SPEC.as_posix() in source_ids
    assert _JUR_SPEC.as_posix() not in source_ids
