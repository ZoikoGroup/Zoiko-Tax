"""Tests for src/ztax_gateway/ontology_alignment.py.

All tests are self-contained and use synthetic data only.
No backend/ files are read.  All tests add under 1 second.
"""

from __future__ import annotations

import json
from dataclasses import FrozenInstanceError
from pathlib import Path

import pytest

from ztax_gateway.ontology_alignment import (
    AlignmentReport,
    check_alignment,
    extract_headings,
    format_report,
    main,
    matches,
)

# ---------------------------------------------------------------------------
# Shared synthetic markdown fixtures
# ---------------------------------------------------------------------------

_SIMPLE_MD = """\
# ontology:telecom/voice/mobile

Voice mobile content.

## ontology:telecom/data/mobile

Data mobile content.

### ontology:telecom/roaming/retail

Roaming retail content.

#### ontology:telecom/ignored-level4

This heading is level 4 and must be ignored.
"""

_CODE_FENCE_MD = """\
# ontology:real-heading

Some text.

```markdown
# ontology:inside-fence-heading

This is inside a code fence but rag.py still counts it as a heading.
```

## ontology:after-fence
"""

_PREAMBLE_MD = """\
This is preamble text before the first heading.
It should not appear as a heading.

# ontology:first-real-heading

Content here.
"""

_DUPLICATE_MD = """\
# ontology:telecom/voice/mobile

First occurrence.

# ontology:telecom/voice/mobile

Second occurrence -- duplicate.
"""


# ===========================================================================
# extract_headings
# ===========================================================================


class TestExtractHeadings:
    def test_levels_1_2_3_included(self) -> None:
        headings = extract_headings(_SIMPLE_MD)
        assert "# ontology:telecom/voice/mobile" in headings
        assert "## ontology:telecom/data/mobile" in headings
        assert "### ontology:telecom/roaming/retail" in headings

    def test_level4_excluded(self) -> None:
        headings = extract_headings(_SIMPLE_MD)
        for h in headings:
            assert not h.startswith("####"), f"Level-4 heading leaked: {h!r}"

    def test_preamble_not_a_heading(self) -> None:
        headings = extract_headings(_PREAMBLE_MD)
        assert "Preamble" not in headings
        assert any("ontology:first-real-heading" in h for h in headings)

    def test_empty_document_returns_empty(self) -> None:
        assert extract_headings("") == ()

    def test_no_headings_returns_empty(self) -> None:
        assert extract_headings("Just a paragraph\nAnother line\n") == ()

    def test_full_line_including_markers_preserved(self) -> None:
        headings = extract_headings("# Hello World\n")
        assert headings == ("# Hello World",)

    def test_order_preserved(self) -> None:
        headings = extract_headings(_SIMPLE_MD)
        assert list(headings).index("# ontology:telecom/voice/mobile") < list(headings).index(
            "## ontology:telecom/data/mobile"
        )

    def test_code_fence_quirk_pinned(self) -> None:
        """Known quirk: a '#' line inside a code fence counts as a heading.

        rag._HEADING_RE is applied to the full document with re.MULTILINE and
        does not track fence state. extract_headings mirrors this intentionally
        so it cannot drift from rag.
        """
        headings = extract_headings(_CODE_FENCE_MD)
        # The heading inside the fence IS extracted (known quirk).
        assert "# ontology:inside-fence-heading" in headings
        # Real headings outside the fence are also extracted.
        assert "# ontology:real-heading" in headings
        assert "## ontology:after-fence" in headings

    def test_returns_tuple(self) -> None:
        assert isinstance(extract_headings(_SIMPLE_MD), tuple)

    def test_consistency_with_rag_chunk_document(self, tmp_path: Path) -> None:
        """extract_headings intentionally diverges from rag._chunk_document.

        After the marker-strip fix (Kaushal's decision, 2026-10-22):
        - ``rag._chunk_document`` strips leading ``#`` markers from headings so
          that ``chunk.heading`` is a bare identifier (e.g.
          ``"ontology:telecom/voice/mobile"``).
        - ``extract_headings`` in this module keeps verbatim heading lines
          (including ``#`` markers) so the alignment tool can detect the
          marker-mismatch class and populate ``AlignmentReport.marker_only``.

        This test documents that divergence: the two sets of headings should
        NOT be equal for a document with ATX headings.
        """
        from ztax_gateway.rag import _chunk_document

        content = _SIMPLE_MD
        f = tmp_path / "sample.md"
        f.write_text(content, encoding="utf-8")

        rag_headings = tuple(
            c.heading for c in _chunk_document(f) if c.heading != "Preamble"
        )
        our_headings = extract_headings(content)
        # rag strips '#'; extract_headings keeps them -- intentional divergence.
        assert our_headings != rag_headings, (
            "extract_headings should retain '#' markers for alignment diagnostics; "
            "rag._chunk_document should strip them for scoring. They must differ."
        )
        # rag_headings must not contain any '#' markers at all.
        for h in rag_headings:
            assert not h.startswith("#"), (
                f"rag._chunk_document heading {h!r} still has a '#' marker "
                "-- the strip fix was not applied."
            )
        # extract_headings must retain all '#' markers.
        for h in our_headings:
            assert h.startswith("#"), (
                f"extract_headings heading {h!r} lost its '#' marker "
                "-- the alignment tool needs verbatim lines."
            )


# ===========================================================================
# matches -- MetricEngine rule
# ===========================================================================


class TestMatches:
    def test_exact_no_markers_matches(self) -> None:
        assert matches("ontology:telecom/voice/mobile", "ontology:telecom/voice/mobile")

    def test_case_insensitive(self) -> None:
        assert matches("Ontology:Telecom/Voice/Mobile", "ontology:telecom/voice/mobile")

    def test_whitespace_stripped(self) -> None:
        assert matches("  ontology:telecom/voice/mobile  ", "ontology:telecom/voice/mobile")

    def test_heading_with_marker_does_not_match_plain_expected(self) -> None:
        """Core finding: a heading with '#' cannot match a plain expected class."""
        assert not matches("# ontology:telecom/voice/mobile", "ontology:telecom/voice/mobile")

    def test_heading_with_marker_matches_expected_with_marker(self) -> None:
        assert matches("# ontology:telecom/voice/mobile", "# ontology:telecom/voice/mobile")

    def test_different_strings_do_not_match(self) -> None:
        assert not matches("ontology:telecom/voice/mobile", "ontology:telecom/data/mobile")

    def test_sync_with_evaluation_quality_normalised_expected(self) -> None:
        """matches() must be equivalent to the MetricEngine comparison.

        Sync test: build a GoldCase with the same expected_class and assert
        that matches(heading, case.normalised_expected) gives the same answer
        as heading.strip().lower() == case.normalised_expected.
        """
        from ztax_gateway.evaluation_quality import GoldCase

        heading = "# ontology:telecom/voice/mobile"
        expected = "ontology:telecom/voice/mobile"

        case = GoldCase(
            case_id="sync-test",
            sku_description="Mobile voice plan",
            expected_class=expected,
        )
        # MetricEngine comparison:
        engine_would_match = heading.strip().lower() == case.normalised_expected
        our_result = matches(heading, expected)
        assert our_result == engine_would_match

        # And confirm the PLAIN heading DOES match:
        plain_heading = "ontology:telecom/voice/mobile"
        plain_engine = plain_heading.strip().lower() == case.normalised_expected
        assert matches(plain_heading, expected) == plain_engine


# ===========================================================================
# AlignmentReport -- frozen dataclass
# ===========================================================================


class TestAlignmentReport:
    def _make_report(self, **kwargs: object) -> AlignmentReport:
        from typing import Any

        defaults: dict[str, Any] = {
            "covered": (),
            "marker_only": (),
            "near_matches": (),
            "missing": (),
            "duplicate_headings": (),
            "non_canonical_headings": (),
        }
        defaults.update(kwargs)
        return AlignmentReport(**defaults)

    def test_ok_when_all_covered_no_problems(self) -> None:
        r = self._make_report(covered=("ontology:telecom/voice/mobile",))
        assert r.ok is True

    def test_not_ok_when_marker_only(self) -> None:
        r = self._make_report(marker_only=("ontology:telecom/voice/mobile",))
        assert r.ok is False

    def test_not_ok_when_missing(self) -> None:
        r = self._make_report(missing=("ontology:telecom/data/mobile",))
        assert r.ok is False

    def test_not_ok_when_duplicates(self) -> None:
        r = self._make_report(duplicate_headings=("# ontology:telecom/voice/mobile",))
        assert r.ok is False

    def test_not_ok_when_non_canonical(self) -> None:
        r = self._make_report(non_canonical_headings=("# ontology:unknown/class",))
        assert r.ok is False

    def test_frozen(self) -> None:
        r = self._make_report()
        with pytest.raises((FrozenInstanceError, AttributeError)):
            r.covered = ("mutated",)  # type: ignore[misc]

    def test_empty_inputs_ok(self) -> None:
        r = check_alignment((), ())
        assert r.ok is True
        assert r.covered == ()
        assert r.marker_only == ()
        assert r.missing == ()


# ===========================================================================
# check_alignment
# ===========================================================================


class TestCheckAlignment:
    def test_covered_when_exact_match(self) -> None:
        headings = ("ontology:telecom/voice/mobile",)
        r = check_alignment(headings, ("ontology:telecom/voice/mobile",))
        assert "ontology:telecom/voice/mobile" in r.covered
        assert r.ok is True

    def test_marker_only_when_heading_has_hash(self) -> None:
        """The central bug: heading has '#', gold does not."""
        headings = ("# ontology:telecom/voice/mobile",)
        r = check_alignment(headings, ("ontology:telecom/voice/mobile",))
        assert "ontology:telecom/voice/mobile" in r.marker_only
        assert "ontology:telecom/voice/mobile" not in r.covered
        assert r.ok is False

    def test_missing_when_no_heading_at_all(self) -> None:
        headings = ("# ontology:telecom/data/mobile",)
        r = check_alignment(headings, ("ontology:telecom/voice/mobile",))
        assert "ontology:telecom/voice/mobile" in r.missing

    def test_duplicate_headings_detected(self) -> None:
        headings = (
            "# ontology:telecom/voice/mobile",
            "# ontology:telecom/voice/mobile",
        )
        r = check_alignment(headings, ())
        assert len(r.duplicate_headings) > 0

    def test_non_canonical_headings_detected(self) -> None:
        headings = ("# ontology:telecom/voice/mobile",)
        # canonical_ids does NOT include this stripped value
        r = check_alignment(headings, (), canonical_ids=("ontology:telecom/data/mobile",))
        assert len(r.non_canonical_headings) > 0

    def test_non_canonical_not_populated_without_arg(self) -> None:
        headings = ("# ontology:telecom/voice/mobile",)
        r = check_alignment(headings, ())
        assert r.non_canonical_headings == ()

    def test_near_match_detected(self) -> None:
        """Heading and expected differ only by case after marker removal."""
        headings = ("# Ontology:Telecom/Voice/Mobile",)
        r = check_alignment(headings, ("ontology:telecom/voice/mobile",))
        # near_matches must contain this pair
        assert any(
            gold == "ontology:telecom/voice/mobile" for gold, _ in r.near_matches
        )

    def test_output_is_sorted(self) -> None:
        headings = (
            "# ontology:telecom/voice/mobile",
            "# ontology:telecom/data/mobile",
        )
        expected = ("ontology:telecom/data/mobile", "ontology:telecom/voice/mobile")
        r = check_alignment(headings, expected)
        assert r.marker_only == tuple(sorted(r.marker_only))

    def test_deterministic_on_repeated_calls(self) -> None:
        headings = ("# ontology:telecom/voice/mobile", "# ontology:telecom/data/mobile")
        expected = ("ontology:telecom/voice/mobile", "ontology:telecom/data/mobile")
        r1 = check_alignment(headings, expected)
        r2 = check_alignment(headings, expected)
        assert r1 == r2

    def test_empty_headings_all_missing(self) -> None:
        r = check_alignment((), ("ontology:telecom/voice/mobile",))
        assert "ontology:telecom/voice/mobile" in r.missing
        assert r.ok is False

    def test_duplicate_gold_classes_deduped(self) -> None:
        """The same gold class appearing twice should be reported once."""
        headings = ("# ontology:telecom/voice/mobile",)
        r = check_alignment(
            headings,
            ("ontology:telecom/voice/mobile", "ontology:telecom/voice/mobile"),
        )
        assert r.marker_only.count("ontology:telecom/voice/mobile") == 1

    def test_canonical_ids_strips_markers(self) -> None:
        """Heading '# ontology:telecom/voice/mobile' is canonical if
        'ontology:telecom/voice/mobile' is in canonical_ids."""
        headings = ("# ontology:telecom/voice/mobile",)
        r = check_alignment(
            headings,
            (),
            canonical_ids=("ontology:telecom/voice/mobile",),
        )
        assert r.non_canonical_headings == ()


# ===========================================================================
# format_report
# ===========================================================================


class TestFormatReport:
    def _report_with(self, **kwargs: object) -> AlignmentReport:
        defaults: dict[str, object] = {
            "covered": (),
            "marker_only": (),
            "near_matches": (),
            "missing": (),
            "duplicate_headings": (),
            "non_canonical_headings": (),
        }
        defaults.update(kwargs)
        return AlignmentReport(**defaults)  # type: ignore[arg-type]

    def test_ok_result_in_output(self) -> None:
        r = self._report_with(covered=("ontology:telecom/voice/mobile",))
        out = format_report(r)
        assert "OK" in out

    def test_fail_result_when_problems(self) -> None:
        r = self._report_with(marker_only=("ontology:telecom/voice/mobile",))
        out = format_report(r)
        assert "FAIL" in out or "PROBLEMS" in out

    def test_marker_only_labelled(self) -> None:
        r = self._report_with(marker_only=("ontology:telecom/voice/mobile",))
        out = format_report(r)
        assert "ontology:telecom/voice/mobile" in out
        assert "marker" in out.lower() or "cannot" in out.lower()

    def test_missing_labelled(self) -> None:
        r = self._report_with(missing=("ontology:telecom/data/mobile",))
        out = format_report(r)
        assert "ontology:telecom/data/mobile" in out

    def test_deterministic(self) -> None:
        r = self._report_with(
            covered=("ontology:telecom/voice/mobile",),
            marker_only=("ontology:telecom/data/mobile",),
        )
        assert format_report(r) == format_report(r)

    def test_returns_str(self) -> None:
        r = self._report_with()
        assert isinstance(format_report(r), str)


# ===========================================================================
# CLI tests
# ===========================================================================


def _write_minimal_gold(path: Path) -> None:
    """Write the minimal valid gold-set JSON accepted by gold_set_file."""
    data = {
        "name": "test-gold",
        "version": "0.1.0",
        "cases": [
            {
                "case_id": "TC-001",
                "sku_description": "Mobile voice plan 500 minutes",
                "expected_class": "ontology:telecom/voice/mobile",
                "notes": "",
                "origin": "SYNTHETIC",
                "author": "test-author",
                "review_status": "DRAFT",
                "reviewer": "",
                "reviewed_on": "",
                "source_ref": "",
            }
        ],
    }
    path.write_text(json.dumps(data), encoding="utf-8")


def _write_ontology_md(path: Path, content: str) -> None:
    path.write_text(content, encoding="utf-8")


class TestCLI:
    def test_exit_0_when_ok(self, tmp_path: Path) -> None:
        """Exit 0 when gold class matches a heading exactly."""
        gold = tmp_path / "gold.json"
        _write_minimal_gold(gold)
        ont = tmp_path / "ont.md"
        # Plain heading (no '#' prefix) matches exactly.
        _write_ontology_md(ont, "# ontology:telecom/voice/mobile\n\nContent.\n")

        # The heading has '#' so it is marker_only, exit 1.
        rc = main(["--gold", str(gold), "--ontology", str(ont)])
        assert rc == 1  # marker_only -> FAIL

    def test_exit_0_exact_match(self, tmp_path: Path) -> None:
        """Exit 0 when ontology heading exactly matches the gold class."""
        gold = tmp_path / "gold.json"
        _write_minimal_gold(gold)
        ont = tmp_path / "ont.md"
        # heading without '#' -- exact match.
        _write_ontology_md(ont, "ontology:telecom/voice/mobile content\n")

        # No heading matched (not ATX style), so missing -> exit 1.
        rc = main(["--gold", str(gold), "--ontology", str(ont)])
        assert rc == 1

    def test_exit_1_when_missing(self, tmp_path: Path) -> None:
        """Exit 1 when gold class has no heading match."""
        gold = tmp_path / "gold.json"
        _write_minimal_gold(gold)
        ont = tmp_path / "ont.md"
        _write_ontology_md(ont, "# ontology:telecom/data/mobile\n\nUnrelated.\n")

        rc = main(["--gold", str(gold), "--ontology", str(ont)])
        assert rc == 1

    def test_exit_1_no_ontology_files(self, tmp_path: Path) -> None:
        """Exit 1 when no ontology files given (all gold classes missing)."""
        gold = tmp_path / "gold.json"
        _write_minimal_gold(gold)
        rc = main(["--gold", str(gold)])
        assert rc == 1

    def test_output_contains_marker_only_text(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        gold = tmp_path / "gold.json"
        _write_minimal_gold(gold)
        ont = tmp_path / "ont.md"
        _write_ontology_md(ont, "# ontology:telecom/voice/mobile\n\nContent.\n")
        main(["--gold", str(gold), "--ontology", str(ont)])
        captured = capsys.readouterr()
        assert "ontology:telecom/voice/mobile" in captured.out

    def test_canonical_ids_json_flag(self, tmp_path: Path) -> None:
        """--canonical-ids-json populates non_canonical_headings when used."""
        gold = tmp_path / "gold.json"
        _write_minimal_gold(gold)
        ont = tmp_path / "ont.md"
        _write_ontology_md(ont, "# ontology:telecom/voice/mobile\n\nContent.\n")
        corpus = tmp_path / "corpus.json"
        # corpus does NOT list ontology:telecom/voice/mobile -> non-canonical
        corpus.write_text(
            json.dumps({"nodes": [{"id": "ontology:telecom/data/mobile"}]}),
            encoding="utf-8",
        )
        rc = main(
            ["--gold", str(gold), "--ontology", str(ont), "--canonical-ids-json", str(corpus)]
        )
        # Problems exist -> exit 1
        assert rc == 1

    # --- Error-path tests for the stdlib JSON loader (_load_expected_classes) ---

    def test_invalid_json_raises_system_exit(self, tmp_path: Path) -> None:
        """SystemExit when the gold file is not valid JSON."""
        bad = tmp_path / "bad.json"
        bad.write_text("not valid json !!!", encoding="utf-8")
        with pytest.raises(SystemExit, match="Invalid JSON in gold file"):
            main(["--gold", str(bad)])

    def test_missing_cases_key_raises_system_exit(self, tmp_path: Path) -> None:
        """SystemExit when the gold JSON has no top-level 'cases' key."""
        bad = tmp_path / "no_cases.json"
        bad.write_text('{"name": "x"}', encoding="utf-8")
        with pytest.raises(SystemExit, match='"cases"'):
            main(["--gold", str(bad)])

    def test_cases_not_a_list_raises_system_exit(self, tmp_path: Path) -> None:
        """SystemExit when 'cases' is not a JSON array."""
        bad = tmp_path / "cases_obj.json"
        bad.write_text('{"cases": {"case_id": "TC-001"}}', encoding="utf-8")
        with pytest.raises(SystemExit, match='"cases" must be a JSON array'):
            main(["--gold", str(bad)])

    def test_case_missing_expected_class_raises_system_exit(
        self, tmp_path: Path
    ) -> None:
        """SystemExit when a case has no 'expected_class' field."""
        bad = tmp_path / "no_ec.json"
        bad.write_text(
            '{"cases": [{"case_id": "TC-001"}]}', encoding="utf-8"
        )
        with pytest.raises(SystemExit, match="expected_class"):
            main(["--gold", str(bad)])

    def test_case_empty_expected_class_raises_system_exit(
        self, tmp_path: Path
    ) -> None:
        """SystemExit when a case has an empty 'expected_class' string."""
        bad = tmp_path / "empty_ec.json"
        bad.write_text(
            '{"cases": [{"case_id": "TC-001", "expected_class": "   "}]}',
            encoding="utf-8",
        )
        with pytest.raises(SystemExit, match="expected_class"):
            main(["--gold", str(bad)])

    def test_case_expected_class_not_string_raises_system_exit(
        self, tmp_path: Path
    ) -> None:
        """SystemExit when 'expected_class' is not a string."""
        bad = tmp_path / "ec_null.json"
        bad.write_text(
            '{"cases": [{"case_id": "TC-001", "expected_class": null}]}',
            encoding="utf-8",
        )
        with pytest.raises(SystemExit, match="expected_class"):
            main(["--gold", str(bad)])

    def test_happy_path_stdlib_reads_all_cases(self, tmp_path: Path) -> None:
        """main() reads expected_class via stdlib without importing gold_set_file.

        Verifies:
        - No SystemExit is raised for a valid minimal gold file.
        - Cases with review_status DRAFT are included (all statuses collected).
        - The return code reflects the alignment result (1 = problems).
        """
        gold = tmp_path / "gold.json"
        # Two cases, both DRAFT -- both should be collected.
        data = {
            "name": "cli-stdlib-test",
            "version": "0.1.0",
            "cases": [
                {
                    "case_id": "TC-001",
                    "sku_description": "Mobile voice plan",
                    "expected_class": "ontology:telecom/voice/mobile",
                    "review_status": "DRAFT",
                    "origin": "SYNTHETIC",
                    "author": "test",
                    "notes": "",
                    "reviewer": "",
                    "reviewed_on": "",
                    "source_ref": "",
                },
                {
                    "case_id": "TC-002",
                    "sku_description": "Data SIM card",
                    "expected_class": "ontology:telecom/data/mobile",
                    "review_status": "DRAFT",
                    "origin": "SYNTHETIC",
                    "author": "test",
                    "notes": "",
                    "reviewer": "",
                    "reviewed_on": "",
                    "source_ref": "",
                },
            ],
        }
        gold.write_text(__import__("json").dumps(data), encoding="utf-8")
        # No ontology files -> all classes missing -> exit 1, but no crash.
        rc = main(["--gold", str(gold)])
        assert rc == 1  # problems present, but no error raised


# ===========================================================================
# AlignmentReport.ok -- full pipeline
# ===========================================================================


class TestAlignmentOk:
    def test_all_13_gold_classes_covered_after_marker_strip(self) -> None:
        """After the marker-strip fix: 13 gold classes, headings have '#' stripped.

        rag._chunk_document now strips leading '#' from headings, so the
        headings passed to check_alignment are plain identifiers (no '#').
        Expect covered == 13, marker_only == 0.
        """
        gold_classes = (
            "ontology:telecom/voice/mobile",
            "ontology:telecom/roaming/retail",
            "ontology:telecom/data/mobile",
            "ontology:telecom/voice/fixed",
            "ontology:telecom/data/fixed-broadband",
            "ontology:telecom/messaging/sms",
            "ontology:telecom/voip/interconnected",
            "ontology:telecom/voip/sip-trunk",
            "ontology:telecom/identity/esim-profile",
            "ontology:telecom/ucaas/seat",
            "ontology:telecom/satellite/connectivity",
            "ontology:content/streaming-video",
            "ontology:services/installation",
        )
        # Headings are now bare (rag strips '#') -- as rag._chunk_document produces.
        headings = gold_classes  # identical: no '#' prefix

        r = check_alignment(headings, gold_classes)

        assert len(r.covered) == 13, f"Expected 13 covered, got {r.covered}"
        assert len(r.marker_only) == 0, f"Expected 0 marker_only, got {r.marker_only}"
        assert r.ok is True


# ===========================================================================
# Pins current Classifier behaviour (known behaviour, must be updated
# deliberately when the marker decision is made)
# ===========================================================================


def _make_registry_and_provenance() -> tuple[object, object]:
    """Return (registry, provenance) helpers copied from test_evaluation_quality.py.

    These are copied verbatim so this file is self-contained and does not rely
    on shared fixtures that could change.
    """
    from ztax_gateway.governance import UseCase, UseCaseRegistry
    from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier

    use_case = UseCase(
        use_case_id="sku-quality-eval",
        owner="lane-l",
        description="Gold-set quality evaluation",
        max_risk_tier=RiskTier.T2,
        max_authority=AuthorityOutcome.A2,
        permitted_regions=frozenset({"eu-west-1"}),
        permitted_data_classes=frozenset({"INTERNAL"}),
    )
    provenance = Provenance(
        use_case="sku-quality-eval",
        model_profile="model:evaluator@2026.09",
        provider_profile="provider:eu-hosted",
        prompt_profile="prompt:quality@1",
        region="eu-west-1",
        data_class="INTERNAL",
        risk_tier=RiskTier.T1,
        authority_outcome=AuthorityOutcome.A1,
        ai_train_version="0.5.0",
    )
    registry = UseCaseRegistry([use_case])
    return registry, provenance


def test_classifier_heading_stripped_of_hash_marker(tmp_path: Path) -> None:
    """After the marker-strip fix, Classifier.top().ontology_class has no '#'.

    rag._chunk_document now strips leading '#' markers from headings before
    storing them in the Chunk.  As a result, the Classifier returns the bare
    identifier (e.g. ``"ontology:telecom/voice/mobile"``) which matches the
    MetricEngine exact-match rule against a gold-set expected_class.

    This is the NEW canonical behaviour after Kaushal's decision (2026-10-22).
    """
    from ztax_gateway.classifier import Classifier
    from ztax_gateway.rag import KnowledgeBase

    registry, provenance = _make_registry_and_provenance()

    ont_md = (
        "# ontology:telecom/voice/mobile\n"
        "\n"
        "Mobile voice services include standard voice plan subscriptions, "
        "mobile voice minutes, and voice call packages for smartphone users. "
        "This class covers all mobile voice plan subscription products.\n"
    )
    f = tmp_path / "ont.md"
    f.write_text(ont_md, encoding="utf-8")

    kb = KnowledgeBase()
    kb.build([f])
    classifier = Classifier(knowledge_base=kb)

    record = classifier.classify(
        sku_description="Standard mobile voice plan subscription minutes",
        provenance=provenance,  # type: ignore[arg-type]
        registry=registry,  # type: ignore[arg-type]
    )
    top = record.top()
    assert top is not None, (
        "Classifier returned no proposals -- ontology body likely too short for BM25."
    )
    # '#' must be gone -- rag now strips it.
    assert top.ontology_class == "ontology:telecom/voice/mobile", (
        f"Expected 'ontology:telecom/voice/mobile' (no '#'), got {top.ontology_class!r}. "
        "The marker-strip fix in rag._chunk_document was not applied."
    )
    assert top.ontology_class.startswith("ontology:"), (
        f"ontology_class {top.ontology_class!r} does not start with 'ontology:'"
    )


def test_classifier_rejects_non_ontology_heading(tmp_path: Path) -> None:
    """Classifier raises ClassificationError for headings that don't match ^ontology:.

    Spec-structure headings like "1. Scope" or "Preamble" must never be returned
    as classification results.  The ^ontology: guard in classifier.py rejects
    them before a proposal is built, so MetricEngine stays strict and malformed
    headings cannot silently pass the demo.
    """
    import pytest

    from ztax_gateway.classifier import ClassificationError, Classifier
    from ztax_gateway.rag import KnowledgeBase

    registry, provenance = _make_registry_and_provenance()

    # A document whose ONLY heading is a spec-structure heading (not ontology:).
    bad_md = (
        "# 1. Scope\n"
        "\n"
        "This section defines the scope of mobile voice services including "
        "voice plan subscriptions mobile voice minutes and voice call packages "
        "for smartphone users and standard plan subscription products.\n"
    )
    f = tmp_path / "bad_ont.md"
    f.write_text(bad_md, encoding="utf-8")

    kb = KnowledgeBase()
    kb.build([f])
    # strict_ontology_prefix=True activates the ^ontology: guard.
    classifier = Classifier(knowledge_base=kb, strict_ontology_prefix=True)

    with pytest.raises(ClassificationError, match=r"does not match the required pattern"):
        classifier.classify(
            sku_description="Standard mobile voice plan subscription minutes",
            provenance=provenance,  # type: ignore[arg-type]
            registry=registry,  # type: ignore[arg-type]
        )
