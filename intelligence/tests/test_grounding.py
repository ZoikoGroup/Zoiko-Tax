"""Tests for the Citation Resolver and Grounding Validator.

Master Specification Ch.17 ss9.

Covers:
  - resolve_citation: valid resolution with correct byte/1-based line numbers
  - resolve_citation: EMPTY_CITATION (no chunks)
  - resolve_citation: SOURCE_MISSING
  - resolve_citation: RANGE_OUT_OF_BOUNDS
  - resolve_citation: CONTENT_MISMATCH (snapshot changed after citing)
  - resolve_citation: ID_MISMATCH (Citation built directly with a forged id)
  - resolve_citation: multi-chunk citation
  - GroundingValidator: accepts issued ids
  - GroundingValidator: rejects an invented id
  - GroundingValidator: empty ids with required=True fails
  - GroundingValidator: empty ids with required=False passes
  - GroundingValidator: with snapshots reports a changed source
  - GroundingValidator: forged/empty Citation in issued map -- no exception, issue recorded
  - GroundingValidator: two cited ids, only one source changed -- issue identifies the failing id
  - GroundingValidator: identical input gives identical output
  - Integration: KnowledgeBase.search -> Citation -> resolve_citation
"""

from __future__ import annotations

from dataclasses import FrozenInstanceError
from pathlib import Path

import pytest

from ztax_gateway.citation import Citation, SourceChunk, combine
from ztax_gateway.grounding import (
    GroundingError,
    GroundingFailure,
    GroundingIssue,
    GroundingResult,
    GroundingValidator,
    ResolvedChunk,
    resolve_citation,
)

# ---------------------------------------------------------------------------
# Test data
# ---------------------------------------------------------------------------

# A two-line byte sequence used across multiple tests.
# Line 1: "Tax code section 12\n"  (20 bytes, offsets 0-19)
# Line 2: "GST rate is 15%\n"      (17 bytes, offsets 20-36)
_SOURCE = b"Tax code section 12\nGST rate is 15%\n"
_SID = "docs/spec.md"


def _make_chunk(
    content: bytes,
    byte_start: int,
    source_id: str = _SID,
) -> SourceChunk:
    return SourceChunk(
        source_id=source_id,
        content=content,
        byte_start=byte_start,
        byte_end=byte_start + len(content),
    )


def _make_citation(content: bytes, byte_start: int, source_id: str = _SID) -> Citation:
    return combine([_make_chunk(content, byte_start, source_id)])


# ===========================================================================
# resolve_citation -- valid resolution
# ===========================================================================


def test_resolve_citation_shape_and_fields() -> None:
    """Single-chunk valid resolution: returns one frozen ResolvedChunk with correct fields."""
    content = _SOURCE[:20]  # "Tax code section 12\n"
    cit = _make_citation(content, 0)
    result = resolve_citation(cit, {_SID: _SOURCE})
    assert len(result) == 1
    rc = result[0]
    assert isinstance(rc, ResolvedChunk)
    assert rc.source_id == _SID
    assert rc.byte_start == 0
    assert rc.byte_end == 20
    assert rc.line_start == 1
    assert rc.line_end == 1


def test_resolve_citation_second_line_numbers() -> None:
    """Chunk on line 2 has line_start=2 and line_end=2."""
    content = _SOURCE[20:]  # "GST rate is 15%\n"
    cit = _make_citation(content, 20)
    rc = resolve_citation(cit, {_SID: _SOURCE})[0]
    assert rc.line_start == 2
    assert rc.line_end == 2


def test_resolve_citation_chunk_spanning_two_lines() -> None:
    """A chunk that spans both lines has line_start=1 and line_end=2."""
    cit = _make_citation(_SOURCE, 0)
    rc = resolve_citation(cit, {_SID: _SOURCE})[0]
    assert rc.line_start == 1
    assert rc.line_end == 2


def test_resolve_citation_result_is_frozen() -> None:
    content = _SOURCE[:20]
    cit = _make_citation(content, 0)
    rc = resolve_citation(cit, {_SID: _SOURCE})[0]
    with pytest.raises(FrozenInstanceError):
        rc.byte_start = 99  # type: ignore[misc]


# ===========================================================================
# resolve_citation -- EMPTY_CITATION
# ===========================================================================


def test_resolve_citation_empty_chunks_raises_empty_citation() -> None:
    """A Citation with no chunks must raise EMPTY_CITATION."""
    empty = Citation(citation_id="abc", chunks=())
    with pytest.raises(GroundingError) as exc:
        resolve_citation(empty, {})
    assert exc.value.failure is GroundingFailure.EMPTY_CITATION


def test_resolve_citation_empty_citation_detail_contains_id() -> None:
    empty = Citation(citation_id="abc", chunks=())
    with pytest.raises(GroundingError) as exc:
        resolve_citation(empty, {})
    assert "abc" in exc.value.detail


# ===========================================================================
# resolve_citation -- SOURCE_MISSING
# ===========================================================================


def test_resolve_citation_source_missing_raises() -> None:
    cit = _make_citation(b"hello", 0, source_id="missing/doc.md")
    with pytest.raises(GroundingError) as exc:
        resolve_citation(cit, {})
    assert exc.value.failure is GroundingFailure.SOURCE_MISSING


def test_resolve_citation_source_missing_detail_contains_source_id() -> None:
    sid = "no/such/file.md"
    cit = _make_citation(b"hello", 0, source_id=sid)
    with pytest.raises(GroundingError) as exc:
        resolve_citation(cit, {"other/doc.md": b"hello"})
    assert sid in exc.value.detail


# ===========================================================================
# resolve_citation -- RANGE_OUT_OF_BOUNDS
# ===========================================================================


def test_resolve_citation_range_out_of_bounds_end_beyond_snapshot() -> None:
    content = b"short"
    # Build a valid citation then feed a shorter snapshot so byte_end exceeds it.
    chunk = SourceChunk(
        source_id=_SID,
        content=content,
        byte_start=0,
        byte_end=len(content),
    )
    cit = combine([chunk])
    with pytest.raises(GroundingError) as exc:
        resolve_citation(cit, {_SID: b"sh"})  # only 2 bytes; end=5 is out
    assert exc.value.failure is GroundingFailure.RANGE_OUT_OF_BOUNDS


def test_resolve_citation_range_out_of_bounds_detail_mentions_range() -> None:
    content = b"abcde"
    chunk = SourceChunk(source_id=_SID, content=content, byte_start=0, byte_end=5)
    cit = combine([chunk])
    with pytest.raises(GroundingError) as exc:
        resolve_citation(cit, {_SID: b"ab"})
    assert "0" in exc.value.detail or "5" in exc.value.detail


# ===========================================================================
# resolve_citation -- CONTENT_MISMATCH
# ===========================================================================


def test_resolve_citation_content_mismatch_when_snapshot_changed() -> None:
    """Snapshot changed after citing: content mismatch must be reported."""
    original = b"original content"
    cit = _make_citation(original, 0)
    changed_snapshot = b"modified content"
    with pytest.raises(GroundingError) as exc:
        resolve_citation(cit, {_SID: changed_snapshot})
    assert exc.value.failure is GroundingFailure.CONTENT_MISMATCH
    assert _SID in exc.value.detail


# ===========================================================================
# resolve_citation -- ID_MISMATCH (forged citation_id)
# ===========================================================================


def test_resolve_citation_id_mismatch_forged_id_raised_before_content_check() -> None:
    """A forged citation_id raises ID_MISMATCH before any content comparison."""
    chunk = _make_chunk(_SOURCE[:20], 0)
    # Build a Citation directly -- bypassing combine() -- with a forged id.
    forged = Citation(citation_id="forgedidforgedid", chunks=(chunk,))
    with pytest.raises(GroundingError) as exc:
        resolve_citation(forged, {_SID: _SOURCE})
    assert exc.value.failure is GroundingFailure.ID_MISMATCH
    assert "forgedidforgedid" in exc.value.detail


# ===========================================================================
# resolve_citation -- multi-chunk citation
# ===========================================================================


def test_resolve_citation_multi_chunk_returns_all_resolved() -> None:
    """A two-chunk citation returns two ResolvedChunk objects."""
    c1 = _make_chunk(_SOURCE[:20], 0)
    c2 = _make_chunk(_SOURCE[20:], 20)
    cit = combine([c1, c2])
    result = resolve_citation(cit, {_SID: _SOURCE})
    assert len(result) == 2


def test_resolve_citation_multi_chunk_correct_line_numbers() -> None:
    c1 = _make_chunk(_SOURCE[:20], 0)
    c2 = _make_chunk(_SOURCE[20:], 20)
    cit = combine([c1, c2])
    result = resolve_citation(cit, {_SID: _SOURCE})
    # combine sorts by (source_id, byte_start), so c1 comes first.
    rc1 = next(r for r in result if r.byte_start == 0)
    rc2 = next(r for r in result if r.byte_start == 20)
    assert rc1.line_start == 1
    assert rc2.line_start == 2


def test_resolve_citation_multi_chunk_source_missing_on_second_chunk() -> None:
    """Missing source on second chunk raises SOURCE_MISSING."""
    c1 = _make_chunk(b"hello", 0, source_id="doc_a.md")
    c2 = _make_chunk(b"world", 0, source_id="doc_b.md")
    cit = combine([c1, c2])
    # Only provide doc_a.md in snapshots.
    with pytest.raises(GroundingError) as exc:
        resolve_citation(cit, {"doc_a.md": b"hello"})
    assert exc.value.failure is GroundingFailure.SOURCE_MISSING


# ===========================================================================
# GroundingValidator -- accepts issued ids
# ===========================================================================


def test_validator_accepts_issued_id() -> None:
    cit = _make_citation(_SOURCE[:20], 0)
    validator = GroundingValidator(issued={cit.citation_id: cit})
    result = validator.check([cit.citation_id])
    assert result.ok is True
    assert result.unissued == ()
    assert result.failures == ()


def test_validator_result_is_frozen() -> None:
    cit = _make_citation(_SOURCE[:20], 0)
    validator = GroundingValidator(issued={cit.citation_id: cit})
    result = validator.check([cit.citation_id])
    with pytest.raises(FrozenInstanceError):
        result.ok = False  # type: ignore[misc]


# ===========================================================================
# GroundingValidator -- rejects invented id
# ===========================================================================


def test_validator_rejects_invented_id() -> None:
    cit = _make_citation(_SOURCE[:20], 0)
    validator = GroundingValidator(issued={cit.citation_id: cit})
    result = validator.check(["inventedidXXXXXX"])
    assert result.ok is False
    assert "inventedidXXXXXX" in result.unissued


def test_validator_unissued_failure_recorded() -> None:
    """An unissued id records an UNISSUED_CITATION issue containing the id."""
    validator = GroundingValidator(issued={})
    result = validator.check(["notregistered123"])
    assert result.ok is False
    assert len(result.failures) == 1
    issue = result.failures[0]
    assert isinstance(issue, GroundingIssue)
    assert issue.failure is GroundingFailure.UNISSUED_CITATION
    assert issue.citation_id == "notregistered123"


# ===========================================================================
# GroundingValidator -- empty ids
# ===========================================================================


def test_validator_empty_ids_required_true_fails() -> None:
    validator = GroundingValidator(issued={})
    result = validator.check([], required=True)
    assert result.ok is False
    assert len(result.failures) == 1
    issue = result.failures[0]
    assert issue.failure is GroundingFailure.NO_CITATION
    assert issue.citation_id is None


def test_validator_empty_ids_required_false_passes() -> None:
    validator = GroundingValidator(issued={})
    result = validator.check([], required=False)
    assert result.ok is True
    assert result.failures == ()
    assert result.unissued == ()


# ===========================================================================
# GroundingValidator -- snapshots report changed source
# ===========================================================================


def test_validator_with_snapshots_reports_changed_source() -> None:
    """When a snapshot has changed since the citation was issued, failure is recorded."""
    original = b"original content for testing"
    cit = _make_citation(original, 0)
    changed = b"XXXXXXXXXXXXXXXXXXXXXXXXX!!!"
    validator = GroundingValidator(
        issued={cit.citation_id: cit},
        snapshots={_SID: changed},
    )
    result = validator.check([cit.citation_id])
    assert result.ok is False
    assert len(result.failures) == 1
    issue = result.failures[0]
    assert issue.failure is GroundingFailure.CONTENT_MISMATCH
    assert issue.citation_id == cit.citation_id


def test_validator_with_matching_snapshots_ok() -> None:
    content = _SOURCE[:20]
    cit = _make_citation(content, 0)
    validator = GroundingValidator(
        issued={cit.citation_id: cit},
        snapshots={_SID: _SOURCE},
    )
    result = validator.check([cit.citation_id])
    assert result.ok is True


# ===========================================================================
# GroundingValidator -- forged / empty Citation in issued map
# ===========================================================================


def test_validator_empty_citation_in_issued_returns_issue_not_exception() -> None:
    """A Citation(citation_id='abc', chunks=()) in the issued map must not raise.

    The validator must catch the GroundingError and record it as an issue.
    """
    empty_cit = Citation(citation_id="abc", chunks=())
    validator = GroundingValidator(
        issued={"abc": empty_cit},
        snapshots={},
    )
    # Must not raise; must return ok=False with an EMPTY_CITATION issue.
    result = validator.check(["abc"])
    assert result.ok is False
    assert len(result.failures) == 1
    issue = result.failures[0]
    assert issue.failure is GroundingFailure.EMPTY_CITATION
    assert issue.citation_id == "abc"


def test_validator_forged_citation_in_issued_returns_issue_not_exception() -> None:
    """A Citation with a forged id in the issued map must not raise."""
    chunk = _make_chunk(_SOURCE[:20], 0)
    forged = Citation(citation_id="forgedidforgedid", chunks=(chunk,))
    validator = GroundingValidator(
        issued={"forgedidforgedid": forged},
        snapshots={_SID: _SOURCE},
    )
    result = validator.check(["forgedidforgedid"])
    assert result.ok is False
    assert len(result.failures) == 1
    assert result.failures[0].failure is GroundingFailure.ID_MISMATCH
    assert result.failures[0].citation_id == "forgedidforgedid"


# ===========================================================================
# GroundingValidator -- two citations, only one source changed
# ===========================================================================


def test_validator_two_citations_only_one_source_changed_issue_identifies_failing_id() -> None:
    """When two ids are cited and only one source has changed, exactly one issue
    is recorded and it identifies the failing citation id."""
    content_a = b"Tax code section A"
    content_b = b"Tax code section B"
    sid_a = "docs/a.md"
    sid_b = "docs/b.md"

    cit_a = _make_citation(content_a, 0, source_id=sid_a)
    cit_b = _make_citation(content_b, 0, source_id=sid_b)

    # Snapshot for b is changed; snapshot for a is intact.
    changed_b = b"XXXXXXXXXXXXXXXXXX"
    validator = GroundingValidator(
        issued={cit_a.citation_id: cit_a, cit_b.citation_id: cit_b},
        snapshots={sid_a: content_a, sid_b: changed_b},
    )
    result = validator.check([cit_a.citation_id, cit_b.citation_id])
    assert result.ok is False
    assert len(result.failures) == 1
    issue = result.failures[0]
    assert issue.failure is GroundingFailure.CONTENT_MISMATCH
    assert issue.citation_id == cit_b.citation_id


# ===========================================================================
# GroundingValidator -- identical input gives identical output
# ===========================================================================


def test_validator_deterministic_same_input_same_output() -> None:
    cit = _make_citation(_SOURCE[:20], 0)
    validator = GroundingValidator(
        issued={cit.citation_id: cit},
        snapshots={_SID: _SOURCE},
    )
    r1 = validator.check([cit.citation_id])
    r2 = validator.check([cit.citation_id])
    assert r1 == r2


def test_resolve_citation_deterministic() -> None:
    cit = _make_citation(_SOURCE[:20], 0)
    r1 = resolve_citation(cit, {_SID: _SOURCE})
    r2 = resolve_citation(cit, {_SID: _SOURCE})
    assert r1 == r2


# ===========================================================================
# GroundingValidator -- duplicate ids allowed
# ===========================================================================


def test_validator_duplicate_ids_allowed_not_double_counted() -> None:
    """Duplicate ids in cited_ids should not double-count failures."""
    cit = _make_citation(_SOURCE[:20], 0)
    validator = GroundingValidator(issued={cit.citation_id: cit})
    result = validator.check([cit.citation_id, cit.citation_id])
    assert result.ok is True


def test_validator_duplicate_unissued_id_reported_once() -> None:
    validator = GroundingValidator(issued={})
    result = validator.check(["fake1", "fake1", "fake1"])
    assert result.unissued.count("fake1") == 1
    assert sum(1 for i in result.failures if i.citation_id == "fake1") == 1


# ===========================================================================
# GroundingValidator -- no snapshots: snapshot errors not reported
# ===========================================================================


def test_validator_without_snapshots_no_resolution_failures() -> None:
    """Without a snapshot mapping, resolution is skipped; only issuance is checked."""
    original = b"original content"
    cit = _make_citation(original, 0)
    # No snapshots: even if source has changed, we cannot detect it here.
    validator = GroundingValidator(issued={cit.citation_id: cit})
    result = validator.check([cit.citation_id])
    assert result.ok is True
    assert result.failures == ()


# ===========================================================================
# Integration test: KnowledgeBase -> Citation -> resolve_citation
# ===========================================================================


def test_integration_kb_search_citation_resolves(tmp_path: Path) -> None:
    """Build a KB from a temp Markdown file, search it, resolve the citation."""
    from ztax_gateway.rag import KnowledgeBase

    md = tmp_path / "spec.md"
    md.write_text(
        "# Determination pipeline\n\n"
        "The pipeline canonicalises the input and applies rules.\n\n"
        "## Rate computation\n\n"
        "The rate computation step applies thresholds.\n",
        encoding="utf-8",
    )
    raw = md.read_bytes()

    kb = KnowledgeBase()
    kb.build([md])

    results = kb.search("determination pipeline canonicalise")
    assert len(results) > 0, "KnowledgeBase should return at least one result"

    citation = results[0].citation

    # resolve_citation against the file bytes.
    resolved = resolve_citation(citation, {citation.chunks[0].source_id: raw})
    assert len(resolved) > 0
    rc = resolved[0]
    assert rc.line_start >= 1
    assert rc.line_end >= rc.line_start
    assert rc.byte_start < rc.byte_end


def test_integration_kb_citation_id_valid(tmp_path: Path) -> None:
    """The citation_id from a KB search must survive resolve_citation (no ID_MISMATCH)."""
    from ztax_gateway.rag import KnowledgeBase

    md = tmp_path / "spec2.md"
    md.write_text(
        "# Jurisdiction resolution\n\nEvidence-based jurisdiction resolution.\n",
        encoding="utf-8",
    )
    raw = md.read_bytes()

    kb = KnowledgeBase()
    kb.build([md])

    results = kb.search("jurisdiction resolution evidence")
    assert len(results) > 0

    citation = results[0].citation
    # Should not raise -- citation_id is genuine.
    resolved = resolve_citation(citation, {citation.chunks[0].source_id: raw})
    assert all(isinstance(r, ResolvedChunk) for r in resolved)


# ===========================================================================
# GroundingFailure enum completeness
# ===========================================================================


def test_grounding_failure_all_values_present() -> None:
    """GroundingFailure must contain exactly the seven codes from the spec."""
    names = {m.name for m in GroundingFailure}
    assert names == {
        "EMPTY_CITATION",
        "SOURCE_MISSING",
        "RANGE_OUT_OF_BOUNDS",
        "CONTENT_MISMATCH",
        "ID_MISMATCH",
        "UNISSUED_CITATION",
        "NO_CITATION",
    }


def test_grounding_failure_is_str_enum() -> None:
    from enum import StrEnum

    assert issubclass(GroundingFailure, StrEnum)
    # str() should return the value, not 'GroundingFailure.SOURCE_MISSING'.
    assert str(GroundingFailure.SOURCE_MISSING) == "SOURCE_MISSING"


# ===========================================================================
# GroundingIssue dataclass
# ===========================================================================


def test_grounding_issue_is_frozen() -> None:
    issue = GroundingIssue(
        citation_id="abc",
        failure=GroundingFailure.UNISSUED_CITATION,
        detail="some detail",
    )
    with pytest.raises(FrozenInstanceError):
        issue.citation_id = "xyz"  # type: ignore[misc]


def test_grounding_issue_citation_id_none_for_no_citation() -> None:
    """NO_CITATION issues have citation_id=None."""
    validator = GroundingValidator(issued={})
    result = validator.check([], required=True)
    assert result.failures[0].citation_id is None


# ===========================================================================
# GroundingResult dataclass
# ===========================================================================


def test_grounding_result_is_frozen() -> None:
    result = GroundingResult(ok=True, unissued=(), failures=())
    with pytest.raises(FrozenInstanceError):
        result.ok = False  # type: ignore[misc]
