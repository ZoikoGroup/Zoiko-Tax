"""Gold-Slots File Format - placeholder structure for the assistant 54-case catalogue.

Background
----------
The CTO Directive and the weekly plan (S2) require an assistant 54-case fixture
structure before the catalogue (assistant Document 06) has been supplied to
engineering.  ADD01 section 9 states the catalogue must be retrieved and
version-pinned before any pass rate is reported.  Until that happens, every
slot is EMPTY.

This module provides the schema, parser and loader for the slot file.  It is
intentionally separate from the case-file loader because:
  - Slots have no sku_description, expected_class, notes, origin, author,
    reviewer or any other case field.  The existing parse_gold_set function
    correctly rejects the slot file (the slot file uses "slots", not "cases").
  - The harness (tools/evaluate_gateway.py) can therefore never load a slot
    file, keeping the verdict NOT_EVALUATED while no catalogue exists.

Design rules (mirror the case-file loader)
-------------------------------------------
* Standard library only.  No network, no clock, no env vars, no logging.
* No imports from any other ztax_gateway module.
* Frozen dataclasses and StrEnum throughout.
* NaN and Infinity constants are rejected before parsing.
* Unknown JSON keys are rejected at every level.
* All error messages are FIXED strings: they never echo file content, a key
  name from the file, or a JSON parser message.  Raise outside the except
  block so __cause__ and __context__ are None.
* Size limit 1 MiB.

Public API
----------
GoldSlotsFileError
    Raised for any parse, validation or eligibility failure.
SlotStatus      StrEnum   EMPTY (the only accepted value)
SlotRecord      frozen dataclass -- one validated slot
GoldSlotsFile   frozen dataclass -- the validated file as a whole
parse_gold_slots(text: str) -> GoldSlotsFile
load_gold_slots_file(path: Path) -> GoldSlotsFile
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass
from enum import StrEnum
from pathlib import Path

__all__: list[str] = [
    "GoldSlotsFile",
    "GoldSlotsFileError",
    "SlotRecord",
    "SlotStatus",
    "load_gold_slots_file",
    "parse_gold_slots",
]

# ---------------------------------------------------------------------------
# Limits
# ---------------------------------------------------------------------------

_MAX_FILE_BYTES: int = 1 * 1024 * 1024  # 1 MiB

# slot_id must match ^ASST-[0-9]{3}$
_SLOT_ID_RE: re.Pattern[str] = re.compile(r"^ASST-[0-9]{3}$")

# Keys allowed at the top-level object.
_TOP_KEYS: frozenset[str] = frozenset(
    {"name", "version", "domain", "purpose", "declared_slots", "slots"}
)

# Keys allowed inside each slot object.
_SLOT_KEYS: frozenset[str] = frozenset({"slot_id", "status"})

# ---------------------------------------------------------------------------
# Fixed error messages (never echo file content, keys, or JSON errors)
# ---------------------------------------------------------------------------

_ERR_OVERSIZE = "slot file exceeds the 1 MiB size limit"
_ERR_NAN_INF = "slot file contains a JSON constant that is not permitted"
_ERR_NOT_JSON = "slot file is not valid JSON"
_ERR_NOT_OBJECT = "slot file must be a JSON object at the top level"
_ERR_UNKNOWN_TOP_KEY = "slot file contains an unrecognised top-level key"
_ERR_MISSING_FIELD = "slot file is missing a required top-level field"
_ERR_FIELD_TYPE = "slot file has a field with an incorrect type"
_ERR_SLOTS_NOT_LIST = "slot file: 'slots' must be a JSON array"
_ERR_SLOT_NOT_OBJECT = "slot file: each slot must be a JSON object"
_ERR_SLOT_UNKNOWN_KEY = "slot file: a slot contains an unrecognised key"
_ERR_SLOT_MISSING_KEY = "slot file: a slot is missing a required key"
_ERR_SLOT_ID_TYPE = "slot file: slot_id must be a string"
_ERR_SLOT_ID_PATTERN = "slot file: slot_id does not match the required pattern ^ASST-[0-9]{3}$"
_ERR_SLOT_ID_DUPLICATE = "slot file: duplicate slot_id"
_ERR_SLOT_STATUS_TYPE = "slot file: status must be a string"
_ERR_SLOT_STATUS_VALUE = (
    "slot file: a slot status value is not accepted; "
    "a slot stays EMPTY until the catalogue is supplied and the schema is versioned"
)
_ERR_DECLARED_SLOTS_TYPE = "slot file: declared_slots must be an integer"
_ERR_DECLARED_SLOTS_MISMATCH = (
    "slot file: declared_slots does not equal the number of slots in the array"
)


# ---------------------------------------------------------------------------
# Enumeration
# ---------------------------------------------------------------------------


class SlotStatus(StrEnum):
    """Lifecycle state of an assistant slot.

    EMPTY is the only accepted value.  A slot remains EMPTY until assistant
    Document 06 is retrieved, version-pinned, and each slot is promoted to a
    real case in a versioned case file.
    """

    EMPTY = "EMPTY"


# ---------------------------------------------------------------------------
# Error
# ---------------------------------------------------------------------------


class GoldSlotsFileError(Exception):
    """Raised when a gold-slots file cannot be parsed or validated."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------------------
# Per-slot record
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class SlotRecord:
    """One validated slot as read from the gold-slots file."""

    slot_id: str
    status: SlotStatus


# ---------------------------------------------------------------------------
# Top-level file object
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class GoldSlotsFile:
    """The parsed and validated assistant slot file."""

    name: str
    version: str
    declared_slots: int
    content_sha256: str
    slot_records: tuple[SlotRecord, ...]

    def summary(self) -> dict[str, object]:
        """Return a summary dict: total, empty, reviewed, content_sha256."""
        total = len(self.slot_records)
        empty = sum(1 for s in self.slot_records if s.status == SlotStatus.EMPTY)
        return {
            "total": total,
            "empty": empty,
            "reviewed": 0,
            "content_sha256": self.content_sha256,
        }


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------


def _canonical_sha256(raw: object) -> str:
    """Return SHA-256 of the canonical (sorted-keys) JSON representation."""
    canonical = json.dumps(raw, sort_keys=True, ensure_ascii=True)
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


# ---------------------------------------------------------------------------
# Parser
# ---------------------------------------------------------------------------


def parse_gold_slots(text: str) -> GoldSlotsFile:
    """Parse and validate a gold-slots JSON string.

    Parameters
    ----------
    text:
        Raw JSON text.  Must be a UTF-8 string.

    Raises
    ------
    GoldSlotsFileError
        For any structural, schema or business-rule violation.
        __cause__ and __context__ are always None.
    """
    if len(text.encode("utf-8")) > _MAX_FILE_BYTES:
        raise GoldSlotsFileError(_ERR_OVERSIZE)

    # Reject NaN/Infinity tokens before parsing.
    _nan_found = False
    for _token in ("NaN", "Infinity", "-Infinity"):
        if _token in text:
            _nan_found = True
            break
    if _nan_found:
        raise GoldSlotsFileError(_ERR_NAN_INF)

    _json_failed = False
    _raw_obj: object = None
    try:
        _raw_obj = json.loads(text)
    except (json.JSONDecodeError, ValueError, RecursionError):
        _json_failed = True
    if _json_failed:
        raise GoldSlotsFileError(_ERR_NOT_JSON)

    if not isinstance(_raw_obj, dict):
        raise GoldSlotsFileError(_ERR_NOT_OBJECT)

    raw: dict[str, object] = _raw_obj

    # Unknown top-level keys.
    _unknown = set(raw.keys()) - _TOP_KEYS
    if _unknown:
        raise GoldSlotsFileError(_ERR_UNKNOWN_TOP_KEY)

    # Required string fields.
    for _field in ("name", "version", "domain", "purpose"):
        if _field not in raw:
            raise GoldSlotsFileError(_ERR_MISSING_FIELD)
        if not isinstance(raw[_field], str):
            raise GoldSlotsFileError(_ERR_FIELD_TYPE)

    # declared_slots must be an integer (JSON integer, not float).
    if "declared_slots" not in raw:
        raise GoldSlotsFileError(_ERR_MISSING_FIELD)
    _ds = raw["declared_slots"]
    # JSON integers parse as Python int; booleans are excluded.
    if not isinstance(_ds, int) or isinstance(_ds, bool):
        raise GoldSlotsFileError(_ERR_DECLARED_SLOTS_TYPE)
    declared_slots: int = _ds

    # slots array.
    if "slots" not in raw:
        raise GoldSlotsFileError(_ERR_MISSING_FIELD)
    _slots_raw = raw["slots"]
    if not isinstance(_slots_raw, list):
        raise GoldSlotsFileError(_ERR_SLOTS_NOT_LIST)

    seen_ids: set[str] = set()
    validated: list[SlotRecord] = []

    for _item in _slots_raw:
        if not isinstance(_item, dict):
            raise GoldSlotsFileError(_ERR_SLOT_NOT_OBJECT)

        # Unknown slot keys.
        _slot_unknown = set(_item.keys()) - _SLOT_KEYS
        if _slot_unknown:
            raise GoldSlotsFileError(_ERR_SLOT_UNKNOWN_KEY)

        # slot_id
        if "slot_id" not in _item:
            raise GoldSlotsFileError(_ERR_SLOT_MISSING_KEY)
        _sid = _item["slot_id"]
        if not isinstance(_sid, str):
            raise GoldSlotsFileError(_ERR_SLOT_ID_TYPE)
        if not _SLOT_ID_RE.fullmatch(_sid):
            raise GoldSlotsFileError(_ERR_SLOT_ID_PATTERN)
        if _sid in seen_ids:
            raise GoldSlotsFileError(_ERR_SLOT_ID_DUPLICATE)
        seen_ids.add(_sid)

        # status
        if "status" not in _item:
            raise GoldSlotsFileError(_ERR_SLOT_MISSING_KEY)
        _st = _item["status"]
        if not isinstance(_st, str):
            raise GoldSlotsFileError(_ERR_SLOT_STATUS_TYPE)
        if _st != SlotStatus.EMPTY:
            raise GoldSlotsFileError(_ERR_SLOT_STATUS_VALUE)

        validated.append(SlotRecord(slot_id=_sid, status=SlotStatus.EMPTY))

    # declared_slots must equal actual count.
    if declared_slots != len(validated):
        raise GoldSlotsFileError(_ERR_DECLARED_SLOTS_MISMATCH)

    content_sha256 = _canonical_sha256(raw)

    return GoldSlotsFile(
        name=str(raw["name"]),
        version=str(raw["version"]),
        declared_slots=declared_slots,
        content_sha256=content_sha256,
        slot_records=tuple(validated),
    )


# ---------------------------------------------------------------------------
# File loader
# ---------------------------------------------------------------------------


def load_gold_slots_file(path: Path) -> GoldSlotsFile:
    """Read a gold-slots JSON file from *path* and return a validated GoldSlotsFile.

    Parameters
    ----------
    path:
        Filesystem path to the ``.json`` file.

    Raises
    ------
    GoldSlotsFileError
        If the file fails validation.
    OSError
        If the file cannot be opened (re-raised as-is).
    """
    raw_bytes = path.read_bytes()
    if len(raw_bytes) > _MAX_FILE_BYTES:
        raise GoldSlotsFileError(_ERR_OVERSIZE)
    _decode_failed = False
    _text = ""
    try:
        _text = raw_bytes.decode("utf-8")
    except UnicodeDecodeError:
        _decode_failed = True
    if _decode_failed:
        raise GoldSlotsFileError(_ERR_NOT_JSON)
    return parse_gold_slots(_text)
