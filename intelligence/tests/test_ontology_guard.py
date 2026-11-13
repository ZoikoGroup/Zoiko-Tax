"""Tests for src/ztax_gateway/ontology_guard.py — WP7.

Self-contained: no imports from other test modules.
No real tax content: all codes and dates are synthetic test fixtures,
clearly marked as test data only.

Covered scenarios
-----------------
check_code verdicts:
- ALLOWED: code present and within effective window (closed-ended)
- ALLOWED: code present, open-ended (no effective_to)
- UNKNOWN: code not in list
- NOT_YET_EFFECTIVE: as_of one day before effective_from
- ALLOWED boundary: as_of == effective_from
- ALLOWED boundary: as_of == effective_to
- EXPIRED: as_of one day after effective_to
- Marker-prefixed code "# ontology:..." -> UNKNOWN (markers NOT stripped)
- Wrong case "ONTOLOGY:..." -> UNKNOWN
- Surrounding whitespace on code is NOT stripped (exact match required)
- Non-date as_of raises OntologyGuardError
- Integer as_of raises OntologyGuardError

strip_heading_marker:
- Strips "# ...", "## ...", "### ..." prefixes
- No-op on bare code
- strip then check_code gives ALLOWED for a known code

load_approved_list rejections:
- Oversized payload
- Invalid JSON
- Top-level not a dict
- Unknown top-level key
- Missing list_version
- Empty list_version (whitespace only)
- Non-string list_version
- Missing approved
- approved not a list
- approved is an empty list
- Entry not a dict
- Entry with unknown key
- Entry missing required key (code)
- Entry missing required key (effective_from)
- code not a string
- code does not start with "ontology:"
- code has leading whitespace (rejected by ontology: prefix check)
- code has hash marker prefix (rejected)
- effective_from invalid date format
- effective_to invalid date format
- effective_to before effective_from (equal is accepted)
- Duplicate code

Round-trip / acceptance:
- bytes payload accepted
- Round-trip preserves fields
- Multiple entries accepted
- ApprovedOntologyList is frozen
- effective_to equal effective_from is accepted (single-day window)

SENTINEL test:
- A bad payload containing "SENTINEL-ONTOLOGY-4e2" must never appear in
  the exception text, str(), repr(), or any log record.

No-clock assertion:
- Module source does not contain "date.today" or "datetime.now".
"""

from __future__ import annotations

import json
import logging
from datetime import date, datetime, timedelta
from pathlib import Path

import pytest

from ztax_gateway.ontology_guard import (
    ApprovedEntry,
    ApprovedOntologyList,
    GuardVerdict,
    OntologyGuardError,
    check_code,
    load_approved_list,
    strip_heading_marker,
)

# ---------------------------------------------------------------------------
# Fixture helpers
# (Synthetic test data only — not real tax content)
# ---------------------------------------------------------------------------

_FIXTURE_CODE = "ontology:fixture/test/alpha"
_FIXTURE_CODE_B = "ontology:fixture/test/beta"
_FROM = date(2024, 1, 1)
_TO = date(2026, 12, 31)


def _make_payload(
    list_version: str = "fixture-v1",
    entries: list[dict[str, object]] | None = None,
    extra_top_key: str | None = None,
) -> str:
    if entries is None:
        entries = [
            {
                "code": _FIXTURE_CODE,
                "effective_from": "2024-01-01",
                "effective_to": "2026-12-31",
            }
        ]
    data: dict[str, object] = {"list_version": list_version, "approved": entries}
    if extra_top_key is not None:
        data[extra_top_key] = "unexpected"
    return json.dumps(data)


def _make_list(
    entries: list[dict[str, object]] | None = None,
    list_version: str = "fixture-v1",
) -> ApprovedOntologyList:
    return load_approved_list(_make_payload(list_version=list_version, entries=entries))


def _default_list() -> ApprovedOntologyList:
    """Return a minimal fixture list with one closed-ended entry."""
    return _make_list()


def _open_list() -> ApprovedOntologyList:
    """Return a fixture list with one open-ended entry (no effective_to)."""
    return _make_list(
        entries=[{"code": _FIXTURE_CODE, "effective_from": "2024-01-01"}]
    )


# ---------------------------------------------------------------------------
# check_code verdict tests
# ---------------------------------------------------------------------------


def test_check_code_allowed_within_window() -> None:
    """Code present and as_of is strictly inside the effective window."""
    lst = _default_list()
    assert check_code(_FIXTURE_CODE, date(2025, 6, 15), lst) is GuardVerdict.ALLOWED


def test_check_code_allowed_open_ended() -> None:
    """Open-ended entry (no effective_to): any date >= effective_from is ALLOWED."""
    lst = _open_list()
    assert check_code(_FIXTURE_CODE, date(2099, 1, 1), lst) is GuardVerdict.ALLOWED


def test_check_code_unknown_code_not_in_list() -> None:
    """Code not present in the list -> UNKNOWN."""
    lst = _default_list()
    assert check_code("ontology:fixture/test/gamma", date(2025, 1, 1), lst) is GuardVerdict.UNKNOWN


def test_check_code_one_day_before_effective_from() -> None:
    """as_of one day before effective_from -> NOT_YET_EFFECTIVE."""
    lst = _default_list()
    one_before = _FROM - timedelta(days=1)
    assert check_code(_FIXTURE_CODE, one_before, lst) is GuardVerdict.NOT_YET_EFFECTIVE


def test_check_code_on_effective_from_boundary() -> None:
    """as_of exactly on effective_from -> ALLOWED (inclusive window)."""
    lst = _default_list()
    assert check_code(_FIXTURE_CODE, _FROM, lst) is GuardVerdict.ALLOWED


def test_check_code_on_effective_to_boundary() -> None:
    """as_of exactly on effective_to -> ALLOWED (inclusive window)."""
    lst = _default_list()
    assert check_code(_FIXTURE_CODE, _TO, lst) is GuardVerdict.ALLOWED


def test_check_code_one_day_after_effective_to() -> None:
    """as_of one day after effective_to -> EXPIRED."""
    lst = _default_list()
    one_after = _TO + timedelta(days=1)
    assert check_code(_FIXTURE_CODE, one_after, lst) is GuardVerdict.EXPIRED


def test_check_code_marker_prefixed_is_unknown() -> None:
    """Code starting with '# ' is UNKNOWN — markers are NOT stripped in check_code."""
    lst = _default_list()
    with_marker = f"# {_FIXTURE_CODE}"
    assert check_code(with_marker, date(2025, 1, 1), lst) is GuardVerdict.UNKNOWN


def test_check_code_wrong_case_is_unknown() -> None:
    """check_code is case-sensitive: capitalised version -> UNKNOWN."""
    lst = _default_list()
    wrong_case = _FIXTURE_CODE.upper()
    assert check_code(wrong_case, date(2025, 1, 1), lst) is GuardVerdict.UNKNOWN


def test_check_code_padded_with_spaces_is_unknown() -> None:
    """Leading/trailing spaces are NOT stripped — padded code must return UNKNOWN."""
    lst = _default_list()
    with_spaces = f"  {_FIXTURE_CODE}  "
    assert check_code(with_spaces, date(2025, 1, 1), lst) is GuardVerdict.UNKNOWN


def test_check_code_trailing_newline_is_unknown() -> None:
    """A code with a trailing newline is NOT stripped — must return UNKNOWN."""
    lst = _default_list()
    with_newline = f"{_FIXTURE_CODE}\n"
    assert check_code(with_newline, date(2025, 1, 1), lst) is GuardVerdict.UNKNOWN


def test_check_code_exact_match_is_allowed() -> None:
    """The exact code (no extra whitespace) is still ALLOWED as before."""
    lst = _default_list()
    assert check_code(_FIXTURE_CODE, date(2025, 1, 1), lst) is GuardVerdict.ALLOWED


def test_check_code_non_date_string_as_of_raises() -> None:
    """Non-date string as_of raises OntologyGuardError."""
    lst = _default_list()
    with pytest.raises(OntologyGuardError):
        check_code(_FIXTURE_CODE, "2025-01-01", lst)  # type: ignore[arg-type]


def test_check_code_int_as_of_raises() -> None:
    """Integer as_of raises OntologyGuardError."""
    lst = _default_list()
    with pytest.raises(OntologyGuardError):
        check_code(_FIXTURE_CODE, 20250101, lst)  # type: ignore[arg-type]


# ---------------------------------------------------------------------------
# strip_heading_marker tests
# ---------------------------------------------------------------------------


def test_strip_heading_marker_level1() -> None:
    assert strip_heading_marker(f"# {_FIXTURE_CODE}") == _FIXTURE_CODE


def test_strip_heading_marker_level2() -> None:
    assert strip_heading_marker(f"## {_FIXTURE_CODE}") == _FIXTURE_CODE


def test_strip_heading_marker_level3() -> None:
    assert strip_heading_marker(f"### {_FIXTURE_CODE}") == _FIXTURE_CODE


def test_strip_heading_marker_bare_code_unchanged() -> None:
    assert strip_heading_marker(_FIXTURE_CODE) == _FIXTURE_CODE


def test_strip_heading_marker_then_check_code_gives_allowed() -> None:
    """The idiomatic pattern: strip first, then check."""
    lst = _default_list()
    bare = strip_heading_marker(f"# {_FIXTURE_CODE}")
    assert check_code(bare, date(2025, 1, 1), lst) is GuardVerdict.ALLOWED


# ---------------------------------------------------------------------------
# load_approved_list rejection tests
# ---------------------------------------------------------------------------


def test_load_oversized_payload_raises() -> None:
    """Payload > 1 MiB raises OntologyGuardError before JSON parsing."""
    big = "x" * (1024 * 1024 + 1)
    with pytest.raises(OntologyGuardError, match="1 MiB"):
        load_approved_list(big)


def test_load_invalid_json_raises() -> None:
    with pytest.raises(OntologyGuardError):
        load_approved_list("{not valid json}")


def test_load_top_level_not_dict_raises() -> None:
    with pytest.raises(OntologyGuardError):
        load_approved_list("[1, 2, 3]")


def test_load_unknown_top_level_key_raises() -> None:
    with pytest.raises(OntologyGuardError, match="unknown key"):
        load_approved_list(_make_payload(extra_top_key="unexpected_field"))


def test_load_missing_list_version_raises() -> None:
    data = json.dumps(
        {"approved": [{"code": _FIXTURE_CODE, "effective_from": "2024-01-01"}]}
    )
    with pytest.raises(OntologyGuardError, match="list_version"):
        load_approved_list(data)


def test_load_empty_list_version_raises() -> None:
    with pytest.raises(OntologyGuardError, match="list_version"):
        load_approved_list(_make_payload(list_version="   "))


def test_load_non_string_list_version_raises() -> None:
    data = json.dumps(
        {
            "list_version": 42,
            "approved": [{"code": _FIXTURE_CODE, "effective_from": "2024-01-01"}],
        }
    )
    with pytest.raises(OntologyGuardError, match="list_version"):
        load_approved_list(data)


def test_load_missing_approved_raises() -> None:
    data = json.dumps({"list_version": "fixture-v1"})
    with pytest.raises(OntologyGuardError, match="approved"):
        load_approved_list(data)


def test_load_approved_not_list_raises() -> None:
    data = json.dumps({"list_version": "fixture-v1", "approved": "not-a-list"})
    with pytest.raises(OntologyGuardError, match="approved"):
        load_approved_list(data)


def test_load_empty_approved_list_raises() -> None:
    data = json.dumps({"list_version": "fixture-v1", "approved": []})
    with pytest.raises(OntologyGuardError, match="empty"):
        load_approved_list(data)


def test_load_entry_not_dict_raises() -> None:
    data = json.dumps({"list_version": "fixture-v1", "approved": ["bad"]})
    with pytest.raises(OntologyGuardError, match="approved\\[0\\]"):
        load_approved_list(data)


def test_load_entry_unknown_key_raises() -> None:
    entries: list[dict[str, object]] = [
        {
            "code": _FIXTURE_CODE,
            "effective_from": "2024-01-01",
            "unexpected_field": True,
        }
    ]
    with pytest.raises(OntologyGuardError, match="unknown key"):
        load_approved_list(_make_payload(entries=entries))


def test_load_entry_missing_code_raises() -> None:
    entries: list[dict[str, object]] = [{"effective_from": "2024-01-01"}]
    with pytest.raises(OntologyGuardError, match="missing required key"):
        load_approved_list(_make_payload(entries=entries))


def test_load_entry_missing_effective_from_raises() -> None:
    entries: list[dict[str, object]] = [{"code": _FIXTURE_CODE}]
    with pytest.raises(OntologyGuardError, match="missing required key"):
        load_approved_list(_make_payload(entries=entries))


def test_load_entry_code_not_string_raises() -> None:
    entries: list[dict[str, object]] = [{"code": 42, "effective_from": "2024-01-01"}]
    with pytest.raises(OntologyGuardError):
        load_approved_list(_make_payload(entries=entries))


def test_load_entry_code_missing_ontology_prefix_raises() -> None:
    entries: list[dict[str, object]] = [
        {"code": "telecom/voice/mobile", "effective_from": "2024-01-01"}
    ]
    with pytest.raises(OntologyGuardError, match="ontology"):
        load_approved_list(_make_payload(entries=entries))


def test_load_entry_code_with_leading_whitespace_raises() -> None:
    """Leading whitespace means code does not start with 'ontology:' -> rejected."""
    entries: list[dict[str, object]] = [
        {"code": f" {_FIXTURE_CODE}", "effective_from": "2024-01-01"}
    ]
    with pytest.raises(OntologyGuardError):
        load_approved_list(_make_payload(entries=entries))


def test_load_entry_code_with_hash_prefix_raises() -> None:
    """'# ontology:...' does not start with 'ontology:' -> rejected at load time."""
    entries: list[dict[str, object]] = [
        {"code": f"# {_FIXTURE_CODE}", "effective_from": "2024-01-01"}
    ]
    with pytest.raises(OntologyGuardError):
        load_approved_list(_make_payload(entries=entries))


def test_load_entry_effective_from_invalid_date_raises() -> None:
    entries: list[dict[str, object]] = [{"code": _FIXTURE_CODE, "effective_from": "not-a-date"}]
    with pytest.raises(OntologyGuardError, match="effective_from"):
        load_approved_list(_make_payload(entries=entries))


def test_load_entry_effective_to_invalid_date_raises() -> None:
    entries: list[dict[str, object]] = [
        {"code": _FIXTURE_CODE, "effective_from": "2024-01-01", "effective_to": "not-a-date"}
    ]
    with pytest.raises(OntologyGuardError, match="effective_to"):
        load_approved_list(_make_payload(entries=entries))


def test_load_entry_effective_to_before_effective_from_raises() -> None:
    entries: list[dict[str, object]] = [
        {"code": _FIXTURE_CODE, "effective_from": "2024-06-01", "effective_to": "2024-01-01"}
    ]
    with pytest.raises(OntologyGuardError, match="effective_to"):
        load_approved_list(_make_payload(entries=entries))


def test_load_entry_effective_to_equal_effective_from_accepted() -> None:
    """effective_to == effective_from is valid (single-day effectivity window)."""
    entries: list[dict[str, object]] = [
        {"code": _FIXTURE_CODE, "effective_from": "2024-01-01", "effective_to": "2024-01-01"}
    ]
    lst = load_approved_list(_make_payload(entries=entries))
    assert check_code(_FIXTURE_CODE, date(2024, 1, 1), lst) is GuardVerdict.ALLOWED
    assert check_code(_FIXTURE_CODE, date(2024, 1, 2), lst) is GuardVerdict.EXPIRED


def test_load_duplicate_code_raises() -> None:
    entries: list[dict[str, object]] = [
        {"code": _FIXTURE_CODE, "effective_from": "2024-01-01"},
        {"code": _FIXTURE_CODE, "effective_from": "2025-01-01"},
    ]
    with pytest.raises(OntologyGuardError, match="duplicate"):
        load_approved_list(_make_payload(entries=entries))


def test_load_multiple_entries_accepted() -> None:
    """Two distinct entries load without error."""
    entries: list[dict[str, object]] = [
        {"code": _FIXTURE_CODE, "effective_from": "2024-01-01"},
        {"code": _FIXTURE_CODE_B, "effective_from": "2024-01-01"},
    ]
    lst = load_approved_list(_make_payload(entries=entries))
    assert len(lst.entries) == 2


def test_load_bytes_payload_accepted() -> None:
    """load_approved_list accepts bytes as well as str."""
    payload = _make_payload().encode("utf-8")
    lst = load_approved_list(payload)
    assert lst.list_version == "fixture-v1"


def test_load_round_trip_preserves_fields() -> None:
    """Loaded list preserves list_version, entry count, code, and dates."""
    lst = _default_list()
    assert lst.list_version == "fixture-v1"
    assert len(lst.entries) == 1
    entry = lst.entries[0]
    assert entry.code == _FIXTURE_CODE
    assert entry.effective_from == date(2024, 1, 1)
    assert entry.effective_to == date(2026, 12, 31)


def test_approved_ontology_list_is_frozen() -> None:
    """ApprovedOntologyList attributes cannot be mutated after construction."""
    lst = _default_list()
    with pytest.raises((AttributeError, TypeError)):
        lst.list_version = "tampered"  # type: ignore[misc]


# ---------------------------------------------------------------------------
# SENTINEL test: bad payload must not appear in exception text or logs
# ---------------------------------------------------------------------------

_SENTINEL = "SENTINEL-ONTOLOGY-4e2"


def test_sentinel_not_in_exception_text(caplog: pytest.LogCaptureFixture) -> None:
    """A bad file containing the sentinel string must not appear anywhere in
    the exception text, str(), repr(), or any log record captured during
    the load attempt.

    Covers all three leak paths the reviewer flagged:
    - Sentinel in a value (original test).
    - Sentinel in a key name (new: unknown-key error must not echo key names).
    - __context__ is None (new: raise-inside-except must not chain JSONDecodeError).
    """
    bad_payload = json.dumps(
        {
            "list_version": f"bad-version-{_SENTINEL}",
            "approved": [
                {
                    "code": f"bad-code-{_SENTINEL}",
                    "effective_from": f"not-a-date-{_SENTINEL}",
                }
            ],
        }
    )

    with caplog.at_level(logging.DEBUG), pytest.raises(OntologyGuardError) as exc_info:
        load_approved_list(bad_payload)

    exc = exc_info.value
    assert _SENTINEL not in str(exc), "sentinel must not appear in str(exception)"
    assert _SENTINEL not in repr(exc), "sentinel must not appear in repr(exception)"

    for record in caplog.records:
        assert _SENTINEL not in record.getMessage(), (
            f"sentinel appeared in log record: {record.getMessage()!r}"
        )

    # __context__ must be None: raise-inside-except chains JSONDecodeError whose
    # .doc attribute holds the full payload. The fix raises outside the except block.
    assert exc.__context__ is None, (
        "exc.__context__ must be None to prevent JSONDecodeError/.doc leakage"
    )
    if exc.__cause__ is not None:
        assert _SENTINEL not in str(exc.__cause__)


def test_sentinel_in_key_name_not_echoed(caplog: pytest.LogCaptureFixture) -> None:
    """An unknown key whose name contains the sentinel must not appear in
    the exception message (key names must not be echoed on unknown-key errors).
    """
    bad_payload = json.dumps(
        {
            "list_version": "fixture-v1",
            f"{_SENTINEL}_bad_key": "value",
            "approved": [{"code": _FIXTURE_CODE, "effective_from": "2024-01-01"}],
        }
    )

    with caplog.at_level(logging.DEBUG), pytest.raises(OntologyGuardError) as exc_info:
        load_approved_list(bad_payload)

    exc = exc_info.value
    assert _SENTINEL not in str(exc), "unknown key name must not be echoed in exception"
    assert exc.__context__ is None, "__context__ must be None"


# ---------------------------------------------------------------------------
# No-clock assertion: module must not contain date.today or datetime.now
# ---------------------------------------------------------------------------


def test_module_contains_no_clock_access() -> None:
    """The ontology_guard module source must not contain date.today or datetime.now."""
    src_path = (
        Path(__file__).resolve().parents[1]
        / "src"
        / "ztax_gateway"
        / "ontology_guard.py"
    )
    source = src_path.read_text(encoding="utf-8")
    assert "date.today" not in source, (
        "ontology_guard.py contains 'date.today' — the module must never read the clock"
    )
    assert "datetime.now" not in source, (
        "ontology_guard.py contains 'datetime.now' — the module must never read the clock"
    )


# ---------------------------------------------------------------------------
# Hardening probe tests
# ---------------------------------------------------------------------------


def test_probe_trailing_newline_in_code_rejected() -> None:
    """load_approved_list must reject a code with a trailing newline."""
    entries: list[dict[str, object]] = [
        {"code": f"{_FIXTURE_CODE}\n", "effective_from": "2024-01-01"}
    ]
    with pytest.raises(OntologyGuardError):
        load_approved_list(_make_payload(entries=entries))


def test_probe_zero_width_character_in_code_rejected() -> None:
    """load_approved_list must reject a code containing zero-width characters."""
    entries: list[dict[str, object]] = [
        {"code": "ontology:fixture/alpha\u200b", "effective_from": "2024-01-01"}
    ]
    with pytest.raises(OntologyGuardError):
        load_approved_list(_make_payload(entries=entries))


def test_probe_non_hyphenated_iso_dates_rejected() -> None:
    """Dates like 20260101 and 2026-W01-1 must be rejected before date.fromisoformat."""
    entries_basic: list[dict[str, object]] = [
        {"code": _FIXTURE_CODE, "effective_from": "20260101"}
    ]
    with pytest.raises(OntologyGuardError):
        load_approved_list(_make_payload(entries=entries_basic))

    entries_week: list[dict[str, object]] = [
        {"code": _FIXTURE_CODE, "effective_from": "2026-W01-1"}
    ]
    with pytest.raises(OntologyGuardError):
        load_approved_list(_make_payload(entries=entries_week))


def test_probe_check_code_datetime_raises_ontology_guard_error() -> None:
    """check_code with a datetime as_of must raise OntologyGuardError (not bare TypeError)."""
    lst = _default_list()
    dt = datetime(2024, 6, 1, 12, 0, 0)
    with pytest.raises(OntologyGuardError):
        check_code(_FIXTURE_CODE, dt, lst)


def test_probe_check_code_none_or_non_str_code_raises_ontology_guard_error() -> None:
    """check_code with code=None or non-str must raise OntologyGuardError (not AttributeError)."""
    lst = _default_list()
    with pytest.raises(OntologyGuardError):
        check_code(None, date(2024, 6, 1), lst)  # type: ignore[arg-type]
    with pytest.raises(OntologyGuardError):
        check_code(12345, date(2024, 6, 1), lst)  # type: ignore[arg-type]


def test_probe_direct_construction_duplicate_codes_raises() -> None:
    """Direct ApprovedOntologyList construction with duplicates must be rejected."""
    entries = (
        ApprovedEntry(_FIXTURE_CODE, date(2024, 1, 1)),
        ApprovedEntry(_FIXTURE_CODE, date(2025, 1, 1)),
    )
    with pytest.raises(OntologyGuardError):
        ApprovedOntologyList("v1", entries)


def test_probe_direct_construction_empty_list_version_raises() -> None:
    """Direct ApprovedOntologyList construction with empty version must be rejected."""
    entries = (ApprovedEntry(_FIXTURE_CODE, date(2024, 1, 1)),)
    with pytest.raises(OntologyGuardError):
        ApprovedOntologyList("", entries)
    with pytest.raises(OntologyGuardError):
        ApprovedOntologyList("   ", entries)


def test_probe_direct_construction_invalid_window_raises() -> None:
    """ApprovedEntry with effective_to < effective_from must be rejected."""
    with pytest.raises(OntologyGuardError):
        ApprovedEntry(_FIXTURE_CODE, date(2024, 1, 2), date(2024, 1, 1))


def test_probe_check_code_empty_or_whitespace_code_returns_unknown() -> None:
    """check_code with empty or whitespace-only code returns UNKNOWN."""
    lst = _default_list()
    assert check_code("", date(2024, 6, 1), lst) is GuardVerdict.UNKNOWN
    assert check_code("   ", date(2024, 6, 1), lst) is GuardVerdict.UNKNOWN


def test_probe_loader_error_never_contains_json_decode_message() -> None:
    """OntologyGuardError for invalid JSON must never echo JSONDecodeError details."""
    bad_json = "{not valid json at all: 123"
    with pytest.raises(OntologyGuardError) as exc_info:
        load_approved_list(bad_json)
    msg = str(exc_info.value)
    assert "Expecting" not in msg
    assert "JSONDecodeError" not in msg
    assert exc_info.value.__cause__ is None


# ---------------------------------------------------------------------------
# Item 3: ValueError and RecursionError in load_approved_list
# ---------------------------------------------------------------------------


class TestLoaderValueErrorRecursion:
    """load_approved_list must catch ValueError and RecursionError and raise
    OntologyGuardError with the fixed message and no chained context."""

    def test_huge_integer_raises_module_error(self) -> None:
        # A 5000-digit integer literal causes json.loads to raise ValueError.
        payload = b"[" + b"9" * 5000 + b"]"
        sentinel = "SENTINEL_HUGE_INT_OG"
        with pytest.raises(OntologyGuardError) as exc_info:
            load_approved_list(payload)
        err = exc_info.value
        assert str(err) == "ontology_guard: payload is not valid JSON"
        assert err.__context__ is None
        assert sentinel not in str(err)

    def test_deep_nesting_raises_module_error(self) -> None:
        # Deeply nested JSON causes RecursionError in json.loads.
        payload = b"[" * 100_000
        with pytest.raises(OntologyGuardError) as exc_info:
            load_approved_list(payload)
        err = exc_info.value
        assert str(err) == "ontology_guard: payload is not valid JSON"
        assert err.__context__ is None
