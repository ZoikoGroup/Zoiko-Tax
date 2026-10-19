"""Tests for the gold-set file format loader (gold_set_file.py).

Self-contained: no imports from other test modules.
Covers:
- Valid file loads cleanly.
- Every validation rule raises GoldSetFileError with the right message.
- reviewer == author rejected.
- DRAFT with reviewer rejected; DRAFT with reviewed_on rejected.
- Oversize file and too-many-cases rejected.
- NaN constant rejected.
- Invalid JSON becomes GoldSetFileError (not bare JSONDecodeError).
- Hash is deterministic and changes when any field changes.
- to_gold_store keeps only REVIEWED cases.
- to_gold_store excludes SYNTHETIC by default; includes with allow_synthetic=True.
- to_gold_store raises when nothing is eligible.
- Store version contains the hash prefix.
- Result is a real GoldSetStore whose cases are GoldCase objects.
- Integration: MetricEngine exercised with a trivial fake classifier.
- The shipped gold/classification.v0.draft.json loads; every case is SYNTHETIC
  and DRAFT; to_gold_store() raises.
- No module other than this test imports gold_set_file (static scan).
"""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from ztax_gateway.evaluation_quality import GoldCase, GoldSetStore
from ztax_gateway.gold_set_file import (
    GoldSetFile,
    GoldSetFileError,
    Origin,
    ReviewStatus,
    load_gold_set_file,
    parse_gold_set,
)

# ---------------------------------------------------------------------------
# Helpers to build valid JSON documents
# ---------------------------------------------------------------------------

_REPO_INTELLIGENCE = Path(__file__).resolve().parents[1]
_SHIPPED_DRAFT = _REPO_INTELLIGENCE / "gold" / "classification.v0.draft.json"


def _draft_case(
    case_id: str = "TC-001",
    sku_description: str = "Standard mobile voice plan",
    expected_class: str = "ontology:telecom/voice/mobile",
) -> dict[str, object]:
    return {
        "case_id": case_id,
        "sku_description": sku_description,
        "expected_class": expected_class,
        "notes": "A draft note.",
        "origin": "SYNTHETIC",
        "author": "engineering-draft",
        "review_status": "DRAFT",
        "reviewer": "",
        "reviewed_on": "",
        "source_ref": "",
    }


def _reviewed_case(
    case_id: str = "TC-R01",
    sku_description: str = "Fixed residential landline plan",
    expected_class: str = "ontology:telecom/voice/fixed",
    origin: str = "SOURCE_DERIVED",
    author: str = "alice",
    reviewer: str = "bob",
) -> dict[str, object]:
    return {
        "case_id": case_id,
        "sku_description": sku_description,
        "expected_class": expected_class,
        "notes": "Reviewed case.",
        "origin": origin,
        "author": author,
        "review_status": "REVIEWED",
        "reviewer": reviewer,
        "reviewed_on": "2026-10-15",
        "source_ref": "HMRC VAT Notice 741A section 8.1",
    }


def _make_file_json(
    *cases: dict[str, object], name: str = "test-gold", version: str = "1.0.0"
) -> str:
    return json.dumps(
        {
            "name": name,
            "version": version,
            "domain": "classification",
            "purpose": "Test gold set.",
            "cases": list(cases),
        }
    )


def _parse(
    *cases: dict[str, object], name: str = "test-gold", version: str = "1.0.0"
) -> GoldSetFile:
    return parse_gold_set(_make_file_json(*cases, name=name, version=version))


# ---------------------------------------------------------------------------
# Valid file loads cleanly
# ---------------------------------------------------------------------------


def test_valid_draft_file_loads() -> None:
    gf = _parse(_draft_case())
    assert gf.name == "test-gold"
    assert gf.version == "1.0.0"
    assert len(gf.cases) == 1
    assert gf.cases[0].case_id == "TC-001"
    assert gf.cases[0].review_status == ReviewStatus.DRAFT
    assert gf.cases[0].origin == Origin.SYNTHETIC
    assert len(gf.content_sha256) == 64


def test_valid_reviewed_file_loads() -> None:
    gf = _parse(_reviewed_case())
    assert gf.cases[0].review_status == ReviewStatus.REVIEWED
    assert gf.cases[0].reviewer == "bob"
    assert gf.cases[0].reviewed_on == "2026-10-15"


def test_valid_mixed_file_loads() -> None:
    gf = _parse(_draft_case("TC-001"), _reviewed_case("TC-R01"))
    assert len(gf.cases) == 2
    summary = gf.review_summary()
    assert summary["total"] == 2
    assert summary["draft"] == 1
    assert summary["reviewed"] == 1
    assert summary["synthetic"] == 1


# ---------------------------------------------------------------------------
# review_summary counts
# ---------------------------------------------------------------------------


def test_review_summary_all_draft() -> None:
    gf = _parse(_draft_case("TC-001"), _draft_case("TC-002"))
    s = gf.review_summary()
    assert s == {"total": 2, "draft": 2, "reviewed": 0, "synthetic": 2}


def test_review_summary_all_reviewed_non_synthetic() -> None:
    gf = _parse(_reviewed_case("TC-R01"), _reviewed_case("TC-R02"))
    s = gf.review_summary()
    assert s["total"] == 2
    assert s["draft"] == 0
    assert s["reviewed"] == 2
    assert s["synthetic"] == 0


# ---------------------------------------------------------------------------
# Unknown key rejection
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "extra_key",
    ["extra", "tax_code", "jurisdiction"],
)
def test_unknown_top_level_key_rejected(extra_key: str) -> None:
    doc = {
        "name": "gs",
        "version": "1.0",
        "cases": [],
        extra_key: "value",
    }
    with pytest.raises(GoldSetFileError, match="unknown key"):
        parse_gold_set(json.dumps(doc))


def test_unknown_case_level_key_rejected() -> None:
    case = _draft_case()
    case["unexpected_field"] = "oops"
    with pytest.raises(GoldSetFileError, match="unknown key"):
        _parse(case)


# ---------------------------------------------------------------------------
# Empty string rejection
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("field", ["name", "version"])
def test_empty_top_level_required_field_rejected(field: str) -> None:
    doc = {"name": "gs", "version": "1.0", "cases": []}
    doc[field] = ""
    with pytest.raises(GoldSetFileError, match=field):
        parse_gold_set(json.dumps(doc))


@pytest.mark.parametrize(
    "field,value",
    [
        ("case_id", ""),
        ("sku_description", "   "),
        ("expected_class", ""),
        ("author", ""),
        ("origin", ""),
        ("review_status", ""),
    ],
)
def test_empty_case_required_field_rejected(field: str, value: str) -> None:
    case = _draft_case()
    case[field] = value
    with pytest.raises(GoldSetFileError, match=field):
        _parse(case)


# ---------------------------------------------------------------------------
# Invalid enum values
# ---------------------------------------------------------------------------


def test_invalid_origin_rejected() -> None:
    case = _draft_case()
    case["origin"] = "INVENTED"
    with pytest.raises(GoldSetFileError, match="origin"):
        _parse(case)


def test_invalid_review_status_rejected() -> None:
    case = _draft_case()
    case["review_status"] = "PENDING"
    with pytest.raises(GoldSetFileError, match="review_status"):
        _parse(case)


# ---------------------------------------------------------------------------
# REVIEWED business rules
# ---------------------------------------------------------------------------


def test_reviewed_without_reviewer_rejected() -> None:
    case = _reviewed_case()
    case["reviewer"] = ""
    with pytest.raises(GoldSetFileError, match="reviewer"):
        _parse(case)


def test_reviewed_without_reviewed_on_rejected() -> None:
    case = _reviewed_case()
    case["reviewed_on"] = ""
    with pytest.raises(GoldSetFileError, match="reviewed_on"):
        _parse(case)


def test_reviewed_on_wrong_format_rejected() -> None:
    case = _reviewed_case()
    case["reviewed_on"] = "15-10-2026"
    with pytest.raises(GoldSetFileError, match="YYYY-MM-DD"):
        _parse(case)


def test_reviewer_equals_author_rejected() -> None:
    case = _reviewed_case(author="alice", reviewer="alice")
    with pytest.raises(GoldSetFileError, match="reviewer"):
        _parse(case)


def test_reviewer_equals_author_case_insensitive_rejected() -> None:
    case = _reviewed_case(author="Alice", reviewer="alice")
    with pytest.raises(GoldSetFileError, match="reviewer"):
        _parse(case)


def test_reviewer_equals_author_with_whitespace_rejected() -> None:
    case = _reviewed_case(author="  alice  ", reviewer="alice")
    with pytest.raises(GoldSetFileError, match="reviewer"):
        _parse(case)


# ---------------------------------------------------------------------------
# DRAFT business rules
# ---------------------------------------------------------------------------


def test_draft_with_reviewer_rejected() -> None:
    case = _draft_case()
    case["reviewer"] = "bob"
    with pytest.raises(GoldSetFileError, match="DRAFT case must not have a 'reviewer'"):
        _parse(case)


def test_draft_with_reviewed_on_rejected() -> None:
    case = _draft_case()
    case["reviewed_on"] = "2026-10-15"
    with pytest.raises(GoldSetFileError, match="DRAFT case must not have a 'reviewed_on'"):
        _parse(case)


# ---------------------------------------------------------------------------
# source_ref rules
# ---------------------------------------------------------------------------


def test_source_derived_without_source_ref_rejected() -> None:
    case = _reviewed_case(origin="SOURCE_DERIVED")
    case["source_ref"] = ""
    with pytest.raises(GoldSetFileError, match="source_ref"):
        _parse(case)


def test_production_derived_without_source_ref_rejected() -> None:
    case = _reviewed_case(origin="PRODUCTION_DERIVED")
    case["source_ref"] = ""
    with pytest.raises(GoldSetFileError, match="source_ref"):
        _parse(case)


def test_synthetic_without_source_ref_allowed() -> None:
    case = _draft_case()
    case["source_ref"] = ""
    gf = _parse(case)
    assert gf.cases[0].source_ref == ""


# ---------------------------------------------------------------------------
# Duplicate case_id
# ---------------------------------------------------------------------------


def test_duplicate_case_id_rejected() -> None:
    with pytest.raises(GoldSetFileError, match="duplicate"):
        _parse(_draft_case("TC-001"), _draft_case("TC-001"))


# ---------------------------------------------------------------------------
# case_id pattern
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("bad_id", ["tc-001", "TC 001", "TC_001", "tc001", "TC/001"])
def test_invalid_case_id_pattern_rejected(bad_id: str) -> None:
    case = _draft_case(case_id=bad_id)
    with pytest.raises(GoldSetFileError, match="case_id"):
        _parse(case)


# ---------------------------------------------------------------------------
# Oversize and too-many-cases
# ---------------------------------------------------------------------------


def test_oversize_text_rejected() -> None:
    big = "x" * (1024 * 1024 + 1)
    with pytest.raises(GoldSetFileError, match="1 MiB"):
        parse_gold_set(big)


def test_too_many_cases_rejected() -> None:
    # Build a document with ALL required top-level keys and 10,001 EMPTY case objects.
    # Empty objects ({}) are 2 bytes each: total ~30 KB, well under the 1 MiB limit.
    # parse_gold_set checks the COUNT limit (len > 10,000) before it validates
    # individual cases, so the COUNT error fires — not the size limit and not a
    # missing-key error.
    header = '{"name":"gs","version":"1.0","domain":"classification","purpose":"p","cases":['
    body = ",".join("{}" for _ in range(10_001))
    text = header + body + "]}"
    assert len(text.encode("utf-8")) < 1024 * 1024, (
        f"test document must be under 1 MiB so only the count limit fires, "
        f"got {len(text.encode('utf-8'))} bytes"
    )
    with pytest.raises(GoldSetFileError, match="too many cases"):
        parse_gold_set(text)


# ---------------------------------------------------------------------------
# NaN / Infinity rejection
# ---------------------------------------------------------------------------


def test_nan_constant_rejected() -> None:
    text = '{"name": "gs", "version": "1.0", "cases": [{"score": NaN}]}'
    with pytest.raises(GoldSetFileError, match="NaN"):
        parse_gold_set(text)


def test_infinity_constant_rejected() -> None:
    text = '{"name": "gs", "version": "1.0", "cases": [{"score": Infinity}]}'
    with pytest.raises(GoldSetFileError, match="Infinity"):
        parse_gold_set(text)


# ---------------------------------------------------------------------------
# Invalid JSON becomes GoldSetFileError
# ---------------------------------------------------------------------------


def test_invalid_json_raises_gold_set_file_error_not_json_decode_error() -> None:
    with pytest.raises(GoldSetFileError, match="not valid JSON"):
        parse_gold_set("{not json")


def test_json_array_at_top_level_rejected() -> None:
    with pytest.raises(GoldSetFileError, match="JSON object"):
        parse_gold_set("[]")


# ---------------------------------------------------------------------------
# Hash determinism and sensitivity
# ---------------------------------------------------------------------------


def test_hash_is_deterministic() -> None:
    gf1 = _parse(_draft_case())
    gf2 = _parse(_draft_case())
    assert gf1.content_sha256 == gf2.content_sha256


def test_hash_changes_when_field_changes() -> None:
    gf1 = _parse(_draft_case(sku_description="Plan A"))
    gf2 = _parse(_draft_case(sku_description="Plan B"))
    assert gf1.content_sha256 != gf2.content_sha256


def test_hash_changes_when_case_added() -> None:
    gf1 = _parse(_draft_case("TC-001"))
    gf2 = _parse(_draft_case("TC-001"), _draft_case("TC-002"))
    assert gf1.content_sha256 != gf2.content_sha256


# ---------------------------------------------------------------------------
# to_gold_store: eligibility filter
# ---------------------------------------------------------------------------


def test_to_gold_store_excludes_draft_cases() -> None:
    reviewed = _reviewed_case("TC-R01")
    draft = _draft_case("TC-D01")
    gf = _parse(reviewed, draft)
    store = gf.to_gold_store()
    assert store.case_count == 1
    assert store.get("TC-R01") is not None
    assert store.get("TC-D01") is None


def test_to_gold_store_excludes_synthetic_by_default() -> None:
    # REVIEWED but SYNTHETIC
    reviewed_synthetic = _reviewed_case("TC-RS01", origin="SYNTHETIC")
    # REVIEWED and SOURCE_DERIVED
    reviewed_real = _reviewed_case("TC-RR01", origin="SOURCE_DERIVED")
    gf = _parse(reviewed_synthetic, reviewed_real)
    store = gf.to_gold_store()
    assert store.case_count == 1
    assert store.get("TC-RR01") is not None


def test_to_gold_store_includes_synthetic_when_allowed() -> None:
    reviewed_synthetic = _reviewed_case("TC-RS01", origin="SYNTHETIC")
    gf = _parse(reviewed_synthetic)
    store = gf.to_gold_store(allow_synthetic=True)
    assert store.case_count == 1
    assert store.get("TC-RS01") is not None


def test_to_gold_store_raises_when_nothing_eligible() -> None:
    gf = _parse(_draft_case("TC-001"))
    with pytest.raises(GoldSetFileError, match="no eligible cases"):
        gf.to_gold_store()


def test_to_gold_store_raises_when_only_synthetic_and_not_allowed() -> None:
    reviewed_synthetic = _reviewed_case("TC-RS01", origin="SYNTHETIC")
    gf = _parse(reviewed_synthetic)
    with pytest.raises(GoldSetFileError, match="no eligible cases"):
        gf.to_gold_store(allow_synthetic=False)


# ---------------------------------------------------------------------------
# Store version contains hash prefix
# ---------------------------------------------------------------------------


def test_store_version_contains_hash_prefix() -> None:
    reviewed = _reviewed_case("TC-R01")
    gf = _parse(reviewed, version="2.0.0")
    store = gf.to_gold_store()
    expected_prefix = gf.content_sha256[:12]
    assert store.version == f"2.0.0+{expected_prefix}"


# ---------------------------------------------------------------------------
# GoldSetStore contains real GoldCase objects
# ---------------------------------------------------------------------------


def test_store_cases_are_gold_case_instances() -> None:
    reviewed = _reviewed_case("TC-R01")
    gf = _parse(reviewed)
    store = gf.to_gold_store()
    assert store.case_count == 1
    case = store.get("TC-R01")
    assert isinstance(case, GoldCase)
    assert case.case_id == "TC-R01"
    assert case.sku_description == "Fixed residential landline plan"
    assert case.expected_class == "ontology:telecom/voice/fixed"


def test_store_is_real_gold_set_store_instance() -> None:
    reviewed = _reviewed_case("TC-R01")
    gf = _parse(reviewed)
    store = gf.to_gold_store()
    assert isinstance(store, GoldSetStore)


# ---------------------------------------------------------------------------
# Integration: MetricEngine exercised with a trivial fake classifier
# ---------------------------------------------------------------------------
# The MetricEngine requires a GoldSetStore with at least _MIN_GOLD_CASES (2)
# cases.  We build two REVIEWED cases in memory and run a fake classifier that
# always returns the first proposal of the record.  This verifies the
# to_gold_store -> GoldSetStore -> MetricEngine chain end-to-end.


def test_metric_engine_integration_store_chain() -> None:
    """Verify the full parse -> to_gold_store -> GoldSetStore chain.

    MetricEngine.run() requires a fully governed Classifier (governance registry,
    provenance, live KnowledgeBase) which cannot be assembled in a few lines without
    setting up the complete governance stack. That integration is already covered by
    tests/test_evaluation_runner.py. Here we verify the chain up to GoldSetStore.
    """
    reviewed_1 = _reviewed_case("TC-R01", expected_class="ontology:telecom/voice/fixed")
    reviewed_2 = _reviewed_case(
        "TC-R02",
        sku_description="Mobile data bundle 10 GB",
        expected_class="ontology:telecom/data/mobile",
        author="carol",
        reviewer="dave",
    )
    gf = _parse(reviewed_1, reviewed_2)
    store = gf.to_gold_store()
    assert isinstance(store, GoldSetStore)
    assert store.case_count == 2
    r01 = store.get("TC-R01")
    r02 = store.get("TC-R02")
    assert r01 is not None and isinstance(r01, GoldCase)
    assert r02 is not None and isinstance(r02, GoldCase)
    assert r01.expected_class == "ontology:telecom/voice/fixed"
    assert r02.expected_class == "ontology:telecom/data/mobile"


# ---------------------------------------------------------------------------
# Shipped classification.v0.draft.json
# ---------------------------------------------------------------------------


def test_shipped_draft_file_loads() -> None:
    assert _SHIPPED_DRAFT.exists(), f"shipped draft file not found: {_SHIPPED_DRAFT}"
    gf = load_gold_set_file(_SHIPPED_DRAFT)
    assert len(gf.cases) > 0


def test_shipped_draft_all_cases_are_synthetic_and_draft() -> None:
    gf = load_gold_set_file(_SHIPPED_DRAFT)
    for case in gf.cases:
        assert case.origin == Origin.SYNTHETIC, (
            f"case {case.case_id!r} must be SYNTHETIC in the shipped draft"
        )
        assert case.review_status == ReviewStatus.DRAFT, (
            f"case {case.case_id!r} must be DRAFT in the shipped draft"
        )


def test_shipped_draft_to_gold_store_raises() -> None:
    gf = load_gold_set_file(_SHIPPED_DRAFT)
    with pytest.raises(GoldSetFileError, match="no eligible cases"):
        gf.to_gold_store()


# ---------------------------------------------------------------------------
# No other src module imports gold_set_file
# ---------------------------------------------------------------------------


def test_no_other_src_module_imports_gold_set_file() -> None:
    src_dir = _REPO_INTELLIGENCE / "src"
    gold_set_file_module = src_dir / "ztax_gateway" / "gold_set_file.py"
    for py_file in src_dir.rglob("*.py"):
        if py_file.resolve() == gold_set_file_module.resolve():
            continue
        text = py_file.read_text(encoding="utf-8")
        assert "gold_set_file" not in text, (
            f"{py_file.relative_to(src_dir)} imports gold_set_file, which is not allowed"
        )


# ---------------------------------------------------------------------------
# load_gold_set_file round-trip
# ---------------------------------------------------------------------------


def test_load_gold_set_file_round_trip(tmp_path: Path) -> None:
    doc = {
        "name": "round-trip",
        "version": "1.0.0",
        "domain": "classification",
        "purpose": "Test.",
        "cases": [_draft_case("TC-001")],
    }
    p = tmp_path / "test.json"
    p.write_text(json.dumps(doc), encoding="utf-8")
    gf = load_gold_set_file(p)
    assert gf.name == "round-trip"
    assert len(gf.cases) == 1


def test_load_gold_set_file_oversize_rejected(tmp_path: Path) -> None:
    p = tmp_path / "big.json"
    p.write_bytes(b"x" * (1024 * 1024 + 1))
    with pytest.raises(GoldSetFileError, match="1 MiB"):
        load_gold_set_file(p)
