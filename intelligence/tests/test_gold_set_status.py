"""Tests for tools/gold_set_status.py.

All gold files are SCRATCH files built in pytest's tmp_path.
Nothing is read from or written to gold/.
"""

from __future__ import annotations

import csv
import json
import sys
from pathlib import Path

import pytest

# Ensure tools/ is importable when running from intelligence/ root.
_TOOLS_DIR = Path(__file__).resolve().parent.parent / "tools"
if str(_TOOLS_DIR) not in sys.path:
    sys.path.insert(0, str(_TOOLS_DIR))

import gold_set_status as gs  # type: ignore[import-not-found]  # noqa: E402

# ---------------------------------------------------------------------------
# Shared helpers
# ---------------------------------------------------------------------------

_AS_OF = "2026-01-01"

# Minimal base fields shared by all case payloads.
_BASE_CASE = {
    "case_id": "TC-001",
    "sku_description": "mobile voice plan",
    "expected_class": "ontology:Telecom/Voice/Mobile",
    "notes": "",
    "origin": "SYNTHETIC",
    "author": "eng",
    "review_status": "DRAFT",
    "reviewer": "",
    "reviewed_on": "",
    "source_ref": "",
}

_VALID_APPROVED_LIST = json.dumps(
    {
        "list_version": "2026-01-01",
        "approved": [
            {
                "code": "ontology:Telecom/Voice/Mobile",
                "effective_from": "2024-01-01",
            }
        ],
    }
)


def _gold(tmp_path: Path, cases: list[dict[str, str]], name: str = "test.gold.json") -> Path:
    """Write a scratch gold-set JSON file and return its path."""
    p = tmp_path / name
    payload = {
        "name": "test-gold",
        "version": "0.1.0",
        "domain": "test",
        "purpose": "test",
        "cases": cases,
    }
    p.write_text(json.dumps(payload), encoding="utf-8")
    return p


def _case(**overrides: str) -> dict[str, str]:
    """Return a copy of _BASE_CASE with any fields overridden."""
    c: dict[str, str] = dict(_BASE_CASE)
    c.update(overrides)
    return c


# ---------------------------------------------------------------------------
# 1. Status: all-DRAFT file
# ---------------------------------------------------------------------------


class TestStatusAllDraft:
    def test_counts_correct_and_readiness_not_usable(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        gold = _gold(tmp_path, [_case()])

        rc = gs.main(["--gold", str(gold)])

        assert rc == 0
        out = capsys.readouterr().out
        assert "total cases     : 1" in out
        assert "DRAFT" in out
        assert "READINESS: not usable as evaluation evidence" in out
        assert "no REVIEWED case with a non-SYNTHETIC origin" in out


# ---------------------------------------------------------------------------
# 2. Status: REVIEWED + SYNTHETIC → still not usable
# ---------------------------------------------------------------------------


class TestStatusReviewedSynthetic:
    def test_readiness_still_not_usable(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        gold = _gold(
            tmp_path,
            [
                _case(
                    review_status="REVIEWED",
                    origin="SYNTHETIC",
                    reviewer="r@test",
                    reviewed_on="2026-01-01",
                )
            ],
        )

        rc = gs.main(["--gold", str(gold)])

        assert rc == 0
        out = capsys.readouterr().out
        assert "READINESS: not usable as evaluation evidence" in out


# ---------------------------------------------------------------------------
# 3. Status: REVIEWED + SOURCE_DERIVED → 1 eligible
# ---------------------------------------------------------------------------


class TestStatusReviewedSourceDerived:
    def test_readiness_reports_eligible(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        gold = _gold(
            tmp_path,
            [
                _case(
                    review_status="REVIEWED",
                    origin="SOURCE_DERIVED",
                    reviewer="r@test",
                    reviewed_on="2026-01-01",
                    source_ref="HMRC VAT Notice 701/9",
                )
            ],
        )

        rc = gs.main(["--gold", str(gold)])

        assert rc == 0
        out = capsys.readouterr().out
        assert "READINESS: 1 case(s) are eligible as evaluation evidence" in out


# ---------------------------------------------------------------------------
# 4. No free text: sentinel never appears in stdout or stderr
# ---------------------------------------------------------------------------


class TestNoFreeTextLeak:
    def test_sentinel_in_description_notes_author_never_leaks(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        sentinel = "SENTINEL_FREE_TEXT_LEAK_XQ9"
        gold = _gold(
            tmp_path,
            [
                _case(
                    sku_description=sentinel,
                    notes=sentinel,
                    author=sentinel,
                )
            ],
        )

        rc = gs.main(["--gold", str(gold)])

        assert rc == 0
        captured = capsys.readouterr()
        assert sentinel not in captured.out
        assert sentinel not in captured.err


# ---------------------------------------------------------------------------
# 5. Coverage: approved-list analysis
# ---------------------------------------------------------------------------


class TestCoverage:
    def _approved_list_with_multiple_codes(self, tmp_path: Path) -> Path:
        payload = json.dumps(
            {
                "list_version": "2026-01-01",
                "approved": [
                    {
                        "code": "ontology:Telecom/Voice/Mobile",
                        "effective_from": "2024-01-01",
                    },
                    {
                        "code": "ontology:Telecom/Data/Broadband",
                        "effective_from": "2024-01-01",
                    },
                    {
                        "code": "ontology:Telecom/SMS/Standard",
                        "effective_from": "2030-01-01",  # future → NOT_YET_EFFECTIVE
                    },
                    {
                        "code": "ontology:Telecom/Voice/Landline",
                        "effective_from": "2020-01-01",
                        "effective_to": "2025-06-01",  # expired by 2026-01-01
                    },
                ],
            }
        )
        p = tmp_path / "approved.json"
        p.write_text(payload, encoding="utf-8")
        return p

    def test_verdict_counts(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        # Gold file has ontology:Telecom/Voice/Mobile (ALLOWED) and
        # ontology:Telecom/Unknown/Thing (not on the list → UNKNOWN)
        gold = _gold(
            tmp_path,
            [
                _case(expected_class="ontology:Telecom/Voice/Mobile"),
                _case(
                    case_id="TC-002",
                    expected_class="ontology:Telecom/Unknown/Thing",
                ),
            ],
        )
        approved = self._approved_list_with_multiple_codes(tmp_path)

        rc = gs.main(
            [
                "--gold", str(gold),
                "--approved-list", str(approved),
                "--as-of", _AS_OF,
            ]
        )

        assert rc == 0
        out = capsys.readouterr().out
        # Codes on approved list with no gold case
        assert "ontology:Telecom/Data/Broadband" in out
        # UNKNOWN verdict count >= 1 (ontology:Telecom/Unknown/Thing not on list)
        assert "UNKNOWN" in out
        # ALLOWED count
        assert "ALLOWED" in out

    def test_approved_list_without_as_of_is_refused(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        gold = _gold(tmp_path, [_case()])
        approved = tmp_path / "approved.json"
        approved.write_text(_VALID_APPROVED_LIST, encoding="utf-8")

        rc = gs.main(["--gold", str(gold), "--approved-list", str(approved)])

        assert rc != 0
        err = capsys.readouterr().err
        assert "--approved-list and --as-of must be given together" in err


# ---------------------------------------------------------------------------
# 6. Invalid approved list
# ---------------------------------------------------------------------------


class TestInvalidApprovedList:
    def test_missing_file_prints_fixed_message(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        gold = _gold(tmp_path, [_case()])
        rc = gs.main(
            [
                "--gold", str(gold),
                "--approved-list", str(tmp_path / "ghost.json"),
                "--as-of", _AS_OF,
            ]
        )
        assert rc != 0
        err = capsys.readouterr().err
        assert "error: could not load approved list" in err
        assert "Traceback" not in err

    def test_invalid_utf8_prints_fixed_message(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        gold = _gold(tmp_path, [_case()])
        bad = tmp_path / "bad.json"
        bad.write_bytes(b"\xff\xfe{garbage bytes}")
        rc = gs.main(
            [
                "--gold", str(gold),
                "--approved-list", str(bad),
                "--as-of", _AS_OF,
            ]
        )
        assert rc != 0
        assert "error: could not load approved list" in capsys.readouterr().err

    def test_malformed_json_prints_fixed_message(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        gold = _gold(tmp_path, [_case()])
        bad = tmp_path / "malformed.json"
        bad.write_text("{ this is not JSON !!", encoding="utf-8")
        rc = gs.main(
            [
                "--gold", str(gold),
                "--approved-list", str(bad),
                "--as-of", _AS_OF,
            ]
        )
        assert rc != 0
        assert "error: could not load approved list" in capsys.readouterr().err

    def test_sentinel_never_leaks_from_bad_list(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        sentinel = "SENTINEL_APPROVED_LIST_LEAK_7z"
        gold = _gold(tmp_path, [_case()])
        bad = tmp_path / "sentinel.json"
        bad.write_text(f'{{"{sentinel}": true}}', encoding="utf-8")
        rc = gs.main(
            [
                "--gold", str(gold),
                "--approved-list", str(bad),
                "--as-of", _AS_OF,
            ]
        )
        assert rc != 0
        captured = capsys.readouterr()
        assert sentinel not in captured.out
        assert sentinel not in captured.err


# ---------------------------------------------------------------------------
# 7. Invalid gold file
# ---------------------------------------------------------------------------


class TestInvalidGoldFile:
    def test_missing_gold_file_prints_fixed_message(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        rc = gs.main(["--gold", str(tmp_path / "no_such.json")])
        assert rc != 0
        err = capsys.readouterr().err
        assert "error: could not load gold set" in err
        assert "Traceback" not in err

    def test_malformed_gold_file_prints_fixed_message(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        bad_gold = tmp_path / "bad.gold.json"
        bad_gold.write_text("not valid json", encoding="utf-8")
        rc = gs.main(["--gold", str(bad_gold)])
        assert rc != 0
        err = capsys.readouterr().err
        assert "error: could not load gold set" in err
        assert "Traceback" not in err


# ---------------------------------------------------------------------------
# 8. Review sheet
# ---------------------------------------------------------------------------


class TestReviewSheet:
    def test_correct_columns_written(self, tmp_path: Path) -> None:
        gold = _gold(
            tmp_path,
            [
                _case(
                    review_status="REVIEWED",
                    origin="SOURCE_DERIVED",
                    reviewer="r@test",
                    reviewed_on="2026-01-01",
                    source_ref="ref-001",
                )
            ],
        )
        out_csv = tmp_path / "sheet.csv"

        rc = gs.main(["--gold", str(gold), "--review-sheet", str(out_csv)])

        assert rc == 0
        rows = list(csv.DictReader(out_csv.open(encoding="utf-8")))
        assert len(rows) == 1
        row = rows[0]
        # Header columns match
        assert set(row.keys()) == set(gs._SHEET_COLUMNS)

    def test_last_five_columns_always_empty(self, tmp_path: Path) -> None:
        gold = _gold(
            tmp_path,
            [_case()],
        )
        out_csv = tmp_path / "sheet.csv"
        gs.main(["--gold", str(gold), "--review-sheet", str(out_csv)])

        rows = list(csv.DictReader(out_csv.open(encoding="utf-8")))
        row = rows[0]
        empty_cols = gs._SHEET_COLUMNS[-gs._EMPTY_COLUMNS:]
        for col in empty_cols:
            assert row[col] == "", f"Column {col!r} should be empty, got {row[col]!r}"

    def test_injection_prefix_escaped(self, tmp_path: Path) -> None:
        gold = _gold(
            tmp_path,
            [_case(sku_description="=DANGEROUS()")],
        )
        out_csv = tmp_path / "sheet.csv"
        gs.main(["--gold", str(gold), "--review-sheet", str(out_csv)])

        raw = out_csv.read_text(encoding="utf-8")
        # The cell must NOT start with = at the start of a field value
        # It should be prefixed with a leading single quote
        assert "'=DANGEROUS()" in raw

    def test_refuses_overwrite_without_force(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        gold = _gold(tmp_path, [_case()])
        out_csv = tmp_path / "sheet.csv"
        out_csv.write_text("existing", encoding="utf-8")

        rc = gs.main(["--gold", str(gold), "--review-sheet", str(out_csv)])

        assert rc == 1
        assert out_csv.read_text(encoding="utf-8") == "existing"
        assert "already exists" in capsys.readouterr().err

    def test_allows_overwrite_with_force(self, tmp_path: Path) -> None:
        gold = _gold(tmp_path, [_case()])
        out_csv = tmp_path / "sheet.csv"
        out_csv.write_text("existing", encoding="utf-8")

        rc = gs.main(
            ["--gold", str(gold), "--review-sheet", str(out_csv), "--force"]
        )

        assert rc == 0
        assert out_csv.read_text(encoding="utf-8") != "existing"

    def test_gold_file_unchanged_after_review_sheet(self, tmp_path: Path) -> None:
        gold = _gold(tmp_path, [_case()])
        original_bytes = gold.read_bytes()
        out_csv = tmp_path / "sheet.csv"

        gs.main(["--gold", str(gold), "--review-sheet", str(out_csv)])

        assert gold.read_bytes() == original_bytes, "Gold file must be byte-for-byte unchanged"


# ---------------------------------------------------------------------------
# 9. Item 1: Review sheet must not overwrite an input file
# ---------------------------------------------------------------------------


class TestReviewSheetCannotOverwriteInput:
    """The review sheet path must never resolve to the same file as --gold or
    --approved-list, even with --force.  The check runs before any I/O."""

    def _approved(self, tmp_path: Path) -> Path:
        p = tmp_path / "approved.json"
        p.write_text(_VALID_APPROVED_LIST, encoding="utf-8")
        return p

    def test_same_path_as_gold_is_refused(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        gold = _gold(tmp_path, [_case()])
        original = gold.read_bytes()

        rc = gs.main(["--gold", str(gold), "--review-sheet", str(gold)])

        assert rc != 0
        err = capsys.readouterr().err
        assert "error: the review sheet path must not be an input file" in err
        assert gold.read_bytes() == original, "gold file must be byte-for-byte unchanged"

    def test_relative_variant_is_refused(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        # Create gold at tmp_path / "sub" / "gold.json"; reference it via "sub/../gold.json"
        sub = tmp_path / "sub"
        sub.mkdir()
        gold = _gold(sub, [_case()], name="gold.json")
        original = gold.read_bytes()
        relative_ref = str(sub / ".." / "sub" / "gold.json")

        rc = gs.main(["--gold", str(gold), "--review-sheet", relative_ref])

        assert rc != 0
        err = capsys.readouterr().err
        assert "error: the review sheet path must not be an input file" in err
        assert gold.read_bytes() == original

    @pytest.mark.skipif(
        __import__("sys").platform != "win32",
        reason="Case-folding collision test is Windows-only",
    )
    def test_different_case_on_windows_is_refused(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        gold = _gold(tmp_path, [_case()], name="gold.json")
        original = gold.read_bytes()
        # Same path with different letter casing
        upper_ref = str(gold).replace("gold.json", "Gold.Json")

        rc = gs.main(["--gold", str(gold), "--review-sheet", upper_ref])

        assert rc != 0
        err = capsys.readouterr().err
        assert "error: the review sheet path must not be an input file" in err
        assert gold.read_bytes() == original

    def test_same_path_with_force_is_still_refused(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        gold = _gold(tmp_path, [_case()])
        original = gold.read_bytes()

        rc = gs.main(["--gold", str(gold), "--review-sheet", str(gold), "--force"])

        assert rc != 0
        err = capsys.readouterr().err
        assert "error: the review sheet path must not be an input file" in err
        assert gold.read_bytes() == original

    def test_different_output_path_still_works(self, tmp_path: Path) -> None:
        gold = _gold(tmp_path, [_case()])
        out_csv = tmp_path / "sheet.csv"

        rc = gs.main(["--gold", str(gold), "--review-sheet", str(out_csv)])

        assert rc == 0
        assert out_csv.exists()

    def test_same_as_approved_list_is_refused(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        gold = _gold(tmp_path, [_case()])
        approved = self._approved(tmp_path)
        original = approved.read_bytes()

        rc = gs.main(
            [
                "--gold", str(gold),
                "--approved-list", str(approved),
                "--as-of", _AS_OF,
                "--review-sheet", str(approved),
            ]
        )

        assert rc != 0
        err = capsys.readouterr().err
        assert "error: the review sheet path must not be an input file" in err
        assert approved.read_bytes() == original


# ---------------------------------------------------------------------------
# 10. Item 2: Expanded CSV formula-injection escaping
# ---------------------------------------------------------------------------


class TestSafeCellExpanded:
    """_safe_cell escapes tab, CR, LF, and leading-space-then-formula inputs."""

    def _c(self, text: str) -> str:
        result: str = gs._safe_cell(text)
        return result

    # --- standard injection prefixes ---
    def test_equals_sign_escaped(self) -> None:
        assert self._c("=1+1") == "'=1+1"

    def test_plus_sign_escaped(self) -> None:
        assert self._c("+1") == "'+1"

    def test_minus_sign_escaped(self) -> None:
        assert self._c("-1") == "'-1"

    def test_at_sign_escaped(self) -> None:
        assert self._c("@SUM(A1)") == "'@SUM(A1)"

    # --- tab triggers ---
    def test_tab_then_formula_escaped(self) -> None:
        assert self._c("\t=1") == "'\t=1"

    def test_tab_then_text_escaped(self) -> None:
        # Leading tab alone triggers escaping
        assert self._c("\tabc") == "'\tabc"

    # --- carriage-return triggers ---
    def test_cr_then_formula_escaped(self) -> None:
        assert self._c("\r=1") == "'\r=1"

    def test_cr_then_text_escaped(self) -> None:
        assert self._c("\rabc") == "'\rabc"

    # --- newline triggers ---
    def test_lf_then_formula_escaped(self) -> None:
        assert self._c("\n=1") == "'\n=1"

    # --- leading spaces + formula ---
    def test_one_space_then_equals_escaped(self) -> None:
        assert self._c(" =1+1") == "' =1+1"

    def test_two_spaces_then_at_escaped(self) -> None:
        assert self._c("  @x") == "'  @x"

    # --- no escaping needed ---
    def test_normal_text_unchanged(self) -> None:
        assert self._c("hello world") == "hello world"

    def test_empty_string_unchanged(self) -> None:
        assert self._c("") == ""

    def test_already_quoted_cell_unchanged(self) -> None:
        assert self._c("'=already") == "'=already"

    # --- Item 3 CLI-level: huge number and deep nesting in approved list ---


class TestLoaderValueErrorCLI:
    """CLI-level: huge integers and deep nesting give a fixed message, no traceback."""

    def test_huge_int_in_approved_list_gives_fixed_message(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        gold = _gold(tmp_path, [_case()])
        bad = tmp_path / "huge.json"
        bad.write_bytes(b"[" + b"9" * 5000 + b"]")

        rc = gs.main(
            ["--gold", str(gold), "--approved-list", str(bad), "--as-of", _AS_OF]
        )

        assert rc != 0
        err = capsys.readouterr().err
        assert "error: could not load approved list" in err
        assert "Traceback" not in err

    def test_deep_nesting_in_approved_list_gives_fixed_message(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        gold = _gold(tmp_path, [_case()])
        bad = tmp_path / "deep.json"
        bad.write_bytes(b"[" * 100_000)

        rc = gs.main(
            ["--gold", str(gold), "--approved-list", str(bad), "--as-of", _AS_OF]
        )

        assert rc != 0
        err = capsys.readouterr().err
        assert "error: could not load approved list" in err
        assert "Traceback" not in err

    def test_huge_int_in_gold_gives_fixed_message(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        bad_gold = tmp_path / "huge_gold.json"
        bad_gold.write_text("[" + "9" * 5000 + "]", encoding="utf-8")

        rc = gs.main(["--gold", str(bad_gold)])

        assert rc != 0
        err = capsys.readouterr().err
        assert "error: could not load gold set" in err
        assert "Traceback" not in err

    def test_deep_nesting_in_gold_gives_fixed_message(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        bad_gold = tmp_path / "deep_gold.json"
        bad_gold.write_bytes(b"[" * 100_000)

        rc = gs.main(["--gold", str(bad_gold)])

        assert rc != 0
        err = capsys.readouterr().err
        assert "error: could not load gold set" in err
        assert "Traceback" not in err
