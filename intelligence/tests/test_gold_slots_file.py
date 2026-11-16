"""Tests for src/ztax_gateway/gold_slots_file.py.

Shipped file tests use the actual gold/assistant.v0.slots.json.
All scratch tests write temporary files in pytest's tmp_path.
"""

from __future__ import annotations

import hashlib
import json
from pathlib import Path

import pytest

from ztax_gateway.gold_set_file import GoldSetFileError, parse_gold_set
from ztax_gateway.gold_slots_file import (
    GoldSlotsFileError,
    SlotStatus,
    load_gold_slots_file,
    parse_gold_slots,
)

# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------

_GOLD_DIR = Path(__file__).resolve().parent.parent / "gold"
_SLOT_FILE = _GOLD_DIR / "assistant.v0.slots.json"
_CLASS_FILE = _GOLD_DIR / "classification.v0.draft.json"

# SHA-256 of classification.v0.draft.json computed before S2 changes.
# If this test fails, the file was edited — which must NOT happen.
_CLASSIFICATION_SHA256 = "1940f41bf63e90d90444c83c2ccbbde68d13614beb6a0bf5d89a1a4b23ff156c"


# ---------------------------------------------------------------------------
# Helper: minimal valid slot payload
# ---------------------------------------------------------------------------


def _make_slots_json(
    *,
    slots: list[dict[str, str]] | None = None,
    declared_slots: int | None = None,
    extra_top: dict[str, object] | None = None,
) -> str:
    _slots: list[dict[str, str]] = slots if slots is not None else [
        {"slot_id": "ASST-001", "status": "EMPTY"}
    ]
    _declared = declared_slots if declared_slots is not None else len(_slots)
    payload: dict[str, object] = {
        "name": "test-slots",
        "version": "0.1.0",
        "domain": "test",
        "purpose": "test purpose",
        "declared_slots": _declared,
        "slots": _slots,
    }
    if extra_top:
        payload.update(extra_top)
    return json.dumps(payload)


# ---------------------------------------------------------------------------
# 1. Shipped file: loads correctly
# ---------------------------------------------------------------------------


class TestShippedSlotFile:
    def test_loads_54_slots(self) -> None:
        gsf = load_gold_slots_file(_SLOT_FILE)
        assert len(gsf.slot_records) == 54

    def test_all_empty(self) -> None:
        gsf = load_gold_slots_file(_SLOT_FILE)
        assert all(s.status == SlotStatus.EMPTY for s in gsf.slot_records)

    def test_summary_reviewed_is_zero(self) -> None:
        gsf = load_gold_slots_file(_SLOT_FILE)
        s = gsf.summary()
        assert s["reviewed"] == 0

    def test_summary_total_and_empty(self) -> None:
        gsf = load_gold_slots_file(_SLOT_FILE)
        s = gsf.summary()
        assert s["total"] == 54
        assert s["empty"] == 54

    def test_ids_in_order_asst001_to_054(self) -> None:
        gsf = load_gold_slots_file(_SLOT_FILE)
        expected = [f"ASST-{i:03d}" for i in range(1, 55)]
        actual = [s.slot_id for s in gsf.slot_records]
        assert actual == expected

    def test_declared_slots_is_54(self) -> None:
        gsf = load_gold_slots_file(_SLOT_FILE)
        assert gsf.declared_slots == 54

    def test_content_sha256_present(self) -> None:
        gsf = load_gold_slots_file(_SLOT_FILE)
        assert len(gsf.content_sha256) == 64  # hex SHA-256

    def test_version_field(self) -> None:
        gsf = load_gold_slots_file(_SLOT_FILE)
        assert gsf.version == "0.1.0"


# ---------------------------------------------------------------------------
# 2. classification.v0.draft.json is unchanged
# ---------------------------------------------------------------------------


class TestClassificationFileUnchanged:
    def test_sha256_unchanged(self) -> None:
        raw = _CLASS_FILE.read_bytes()
        actual = hashlib.sha256(raw).hexdigest()
        assert actual == _CLASSIFICATION_SHA256, (
            "classification.v0.draft.json was modified — this is not allowed in S2"
        )


# ---------------------------------------------------------------------------
# 3. parse_gold_set rejects the shipped slot file
# ---------------------------------------------------------------------------


class TestParseGoldSetRejectsSlotFile:
    def test_slot_file_rejected_by_case_loader(self) -> None:
        text = _SLOT_FILE.read_text(encoding="utf-8")
        with pytest.raises(GoldSetFileError):
            parse_gold_set(text)


# ---------------------------------------------------------------------------
# 4. Refused: extra key in a slot
# ---------------------------------------------------------------------------


class TestRefusedExtraSlotKey:
    def test_input_key_rejected(self) -> None:
        text = _make_slots_json(
            slots=[{"slot_id": "ASST-001", "status": "EMPTY", "input": "some text"}]
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)

    def test_expected_answer_key_rejected(self) -> None:
        text = _make_slots_json(
            slots=[
                {
                    "slot_id": "ASST-001",
                    "status": "EMPTY",
                    "expected_answer": "some answer",
                }
            ]
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)

    def test_notes_key_rejected(self) -> None:
        text = _make_slots_json(
            slots=[{"slot_id": "ASST-001", "status": "EMPTY", "notes": "extra"}]
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)


# ---------------------------------------------------------------------------
# 5. Refused: bad status values
# ---------------------------------------------------------------------------


class TestRefusedBadStatus:
    def test_reviewed_status_rejected(self) -> None:
        text = _make_slots_json(
            slots=[{"slot_id": "ASST-001", "status": "REVIEWED"}]
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)

    def test_draft_status_rejected(self) -> None:
        text = _make_slots_json(
            slots=[{"slot_id": "ASST-001", "status": "DRAFT"}]
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)

    def test_arbitrary_string_status_rejected(self) -> None:
        text = _make_slots_json(
            slots=[{"slot_id": "ASST-001", "status": "ACTIVE"}]
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)

    def test_status_fixed_message_mentions_catalogue(self) -> None:
        text = _make_slots_json(
            slots=[{"slot_id": "ASST-001", "status": "REVIEWED"}]
        )
        with pytest.raises(GoldSlotsFileError) as exc_info:
            parse_gold_slots(text)
        assert "catalogue" in str(exc_info.value).lower()


# ---------------------------------------------------------------------------
# 6. Refused: duplicate slot_id
# ---------------------------------------------------------------------------


class TestRefusedDuplicateId:
    def test_duplicate_id_rejected(self) -> None:
        text = _make_slots_json(
            slots=[
                {"slot_id": "ASST-001", "status": "EMPTY"},
                {"slot_id": "ASST-001", "status": "EMPTY"},
            ],
            declared_slots=2,
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)


# ---------------------------------------------------------------------------
# 7. Refused: bad id patterns
# ---------------------------------------------------------------------------


class TestRefusedBadIdPattern:
    def test_lowercase_rejected(self) -> None:
        text = _make_slots_json(
            slots=[{"slot_id": "asst-001", "status": "EMPTY"}]
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)

    def test_no_prefix_rejected(self) -> None:
        text = _make_slots_json(
            slots=[{"slot_id": "001", "status": "EMPTY"}]
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)

    def test_wrong_prefix_rejected(self) -> None:
        text = _make_slots_json(
            slots=[{"slot_id": "TC-001", "status": "EMPTY"}]
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)

    def test_too_many_digits_rejected(self) -> None:
        text = _make_slots_json(
            slots=[{"slot_id": "ASST-0001", "status": "EMPTY"}]
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)

    def test_too_few_digits_rejected(self) -> None:
        text = _make_slots_json(
            slots=[{"slot_id": "ASST-01", "status": "EMPTY"}]
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)


# ---------------------------------------------------------------------------
# 8. Refused: declared_slots mismatch
# ---------------------------------------------------------------------------


class TestRefusedDeclaredSlotsMismatch:
    def test_declared_too_high(self) -> None:
        text = _make_slots_json(
            slots=[{"slot_id": "ASST-001", "status": "EMPTY"}],
            declared_slots=2,
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)

    def test_declared_too_low(self) -> None:
        text = _make_slots_json(
            slots=[
                {"slot_id": "ASST-001", "status": "EMPTY"},
                {"slot_id": "ASST-002", "status": "EMPTY"},
            ],
            declared_slots=1,
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)

    def test_declared_as_float_rejected(self) -> None:
        text = _make_slots_json()
        # Replace declared_slots with float
        raw = json.loads(text)
        raw["declared_slots"] = 1.0
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(json.dumps(raw))

    def test_declared_as_bool_rejected(self) -> None:
        raw = json.loads(_make_slots_json())
        raw["declared_slots"] = True
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(json.dumps(raw))


# ---------------------------------------------------------------------------
# 9. Refused: unknown top-level key
# ---------------------------------------------------------------------------


class TestRefusedUnknownTopKey:
    def test_extra_top_key_rejected(self) -> None:
        text = _make_slots_json(extra_top={"unknown_field": "value"})
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)

    def test_cases_key_rejected(self) -> None:
        # "cases" is a case-file key; it must not be accepted in a slot file.
        text = _make_slots_json(extra_top={"cases": []})
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)


# ---------------------------------------------------------------------------
# 10. Refused: NaN / Infinity
# ---------------------------------------------------------------------------


class TestRefusedNanInfinity:
    def test_nan_rejected(self) -> None:
        slots_part = '[{"slot_id":"ASST-001","status":"EMPTY"}]'
        text = (
            '{"name":"x","version":"1","domain":"d","purpose":"p",'
            f'"declared_slots":1,"slots":{slots_part},"x":NaN}}'
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)

    def test_infinity_rejected(self) -> None:
        slots_part = '[{"slot_id":"ASST-001","status":"EMPTY"}]'
        text = (
            '{"name":"x","version":"1","domain":"d","purpose":"p",'
            f'"declared_slots":1,"slots":{slots_part},"x":Infinity}}'
        )
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(text)


# ---------------------------------------------------------------------------
# 11. Refused: invalid JSON
# ---------------------------------------------------------------------------


class TestRefusedInvalidJson:
    def test_not_json_rejected(self) -> None:
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots("not-json!!!")

    def test_empty_string_rejected(self) -> None:
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots("")


# ---------------------------------------------------------------------------
# 12. Refused: non-object top level
# ---------------------------------------------------------------------------


class TestRefusedNonObjectTopLevel:
    def test_array_top_level_rejected(self) -> None:
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots("[]")

    def test_string_top_level_rejected(self) -> None:
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots('"hello"')


# ---------------------------------------------------------------------------
# 13. Refused: oversize file
# ---------------------------------------------------------------------------


class TestRefusedOversizeFile:
    def test_oversize_text_rejected(self) -> None:
        big = "x" * (1 * 1024 * 1024 + 1)
        with pytest.raises(GoldSlotsFileError):
            parse_gold_slots(big)

    def test_oversize_file_on_disk_rejected(self, tmp_path: Path) -> None:
        p = tmp_path / "big.json"
        p.write_bytes(b"x" * (1 * 1024 * 1024 + 1))
        with pytest.raises(GoldSlotsFileError):
            load_gold_slots_file(p)


# ---------------------------------------------------------------------------
# 14. Error messages never echo file content (sentinel test)
# ---------------------------------------------------------------------------


class TestNoSentinelInErrorMessage:
    """The error message, __cause__, and __context__ must never contain the sentinel."""

    _SENTINEL = "SENTINEL_XJ9QZWV"

    def _slot_with_bad_status(self) -> str:
        return _make_slots_json(
            slots=[
                {
                    "slot_id": "ASST-001",
                    "status": self._SENTINEL,
                }
            ]
        )

    def test_message_does_not_contain_sentinel(self) -> None:
        with pytest.raises(GoldSlotsFileError) as exc_info:
            parse_gold_slots(self._slot_with_bad_status())
        assert self._SENTINEL not in str(exc_info.value)

    def test_cause_is_none(self) -> None:
        with pytest.raises(GoldSlotsFileError) as exc_info:
            parse_gold_slots(self._slot_with_bad_status())
        assert exc_info.value.__cause__ is None

    def test_context_is_none(self) -> None:
        with pytest.raises(GoldSlotsFileError) as exc_info:
            parse_gold_slots(self._slot_with_bad_status())
        assert exc_info.value.__context__ is None

    def test_invalid_json_sentinel_not_echoed(self) -> None:
        bad = f'{{"name":"{self._SENTINEL}","slots":[]}}'
        with pytest.raises(GoldSlotsFileError) as exc_info:
            parse_gold_slots(bad)
        # Even though SENTINEL is in the JSON, it must not appear in the error.
        # (This particular call fails on unknown key or missing fields —
        #  whichever triggers first — but not by echoing JSON content.)
        assert self._SENTINEL not in str(exc_info.value)
