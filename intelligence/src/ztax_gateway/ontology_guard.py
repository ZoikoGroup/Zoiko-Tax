"""Runtime Ontology Guard — WP7.

Validates that an ontology class code proposed by the classifier is present
in an approved list and is currently effective before the caller presents it
as a suggestion.

Design rules
------------
* Standard library only.  No network, no file I/O, no logging.
* The check function is **pure and deterministic**: given the same arguments
  it always returns the same verdict.
* ``check_code`` performs an **exact, case-sensitive** match on the code
  string after nothing but ``str.strip()``.  It does **not** strip Markdown
  ATX heading markers (``#``): a code that starts with ``"#"`` is
  ``UNKNOWN``.  Callers that hold a raw ``Chunk.heading`` value must call
  :func:`strip_heading_marker` before passing the code to :func:`check_code`.
* ``strip_heading_marker`` is a separate, explicit helper.  It is not called
  inside :func:`check_code`.
* ``as_of`` is **required** in :func:`check_code`.  The function never
  defaults to the current date and never reads the clock anywhere.
* Fail-closed: an unrecognised, expired or not-yet-effective code is *not*
  ALLOWED.  The burden of proof is on the approved list.
* No tax content.  The approved-list JSON format is defined here; real
  approved lists are populated by content governance, not by this module.
  Tests use small fixture lists explicitly marked as test data.
* Loader error messages name only the field path and entry index.  They
  **never** echo file contents, entry values or SKU text.  Every
  ``OntologyGuardError`` raised inside the loader uses ``raise … from None``
  so no original exception is chained.

JSON format
-----------
An approved-list file is a JSON object with the following shape::

    {
        "list_version": "2026-01-01",
        "approved": [
            {
                "code": "ontology:telecom/voice/mobile",
                "effective_from": "2024-01-01",
                "effective_to": "2026-12-31"
            },
            {
                "code": "ontology:telecom/data/broadband",
                "effective_from": "2024-01-01"
            }
        ]
    }

``effective_to`` is optional (open-ended if absent).  All dates are ISO 8601
(``YYYY-MM-DD``) strings.  Effective windows are **inclusive on both ends**.

``code`` must be a non-empty string starting with ``"ontology:"`` with no
leading whitespace.
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass
from datetime import date
from enum import StrEnum
from typing import Any

__all__: list[str] = [
    "ApprovedEntry",
    "ApprovedOntologyList",
    "GuardVerdict",
    "OntologyGuardError",
    "check_code",
    "load_approved_list",
    "strip_heading_marker",
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# 1 MiB — payloads larger than this are rejected before JSON parsing.
_MAX_PAYLOAD_BYTES: int = 1 * 1024 * 1024

# Keys permitted on each entry in "approved". Unknown keys are rejected.
_ENTRY_REQUIRED_KEYS: frozenset[str] = frozenset({"code", "effective_from"})
_ENTRY_OPTIONAL_KEYS: frozenset[str] = frozenset({"effective_to"})
_ENTRY_ALLOWED_KEYS: frozenset[str] = _ENTRY_REQUIRED_KEYS | _ENTRY_OPTIONAL_KEYS

# Keys permitted at the top level. Unknown keys are rejected.
_TOP_LEVEL_ALLOWED_KEYS: frozenset[str] = frozenset({"list_version", "approved"})

# Pattern that strips leading Markdown ATX heading markers (one or more '#'
# characters) and the whitespace that follows them, used only by
# strip_heading_marker — NOT by check_code.
_ATX_MARKER_RE: re.Pattern[str] = re.compile(r"^#+\s*")

# Pattern that validates a canonical ontology code: starts with "ontology:"
# followed by at least one ASCII character in [A-Za-z0-9_./:-], with no
# leading or trailing whitespace, zero-width characters, or control codes.
_ONTOLOGY_CODE_RE: re.Pattern[str] = re.compile(r"ontology:[A-Za-z0-9_./:-]+")

# Strict ISO 8601 YYYY-MM-DD date pattern.
_DATE_RE: re.Pattern[str] = re.compile(r"[0-9]{4}-[0-9]{2}-[0-9]{2}")


# ---------------------------------------------------------------------------
# Errors
# ---------------------------------------------------------------------------


class OntologyGuardError(Exception):
    """Raised when an approved-list payload cannot be loaded or when
    :func:`check_code` is called with an invalid *as_of* argument.

    Messages are structural and never echo raw SKU descriptions, customer
    text, model outputs, or the values of any entry in the approved list.
    """


# ---------------------------------------------------------------------------
# Data structures
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class ApprovedEntry:
    """One entry in an approved ontology list.

    ``code`` is the canonical class identifier exactly as it appears in the
    JSON (after :func:`load_approved_list` validation).  It is not modified
    during loading.

    ``effective_from`` and ``effective_to`` are ``date`` objects.
    ``effective_to`` is ``None`` for open-ended entries.  The effective
    window is **inclusive on both ends**.
    """

    code: str
    effective_from: date
    effective_to: date | None = None

    def __post_init__(self) -> None:
        if self.effective_to is not None and self.effective_to < self.effective_from:
            raise OntologyGuardError(
                "ontology_guard: ApprovedEntry 'effective_to' is before 'effective_from'"
            ) from None


@dataclass(frozen=True, slots=True)
class ApprovedOntologyList:
    """An immutable, validated approved-ontology list.

    Built by :func:`load_approved_list`; never constructed directly in
    production code.

    ``entries`` is the full ordered tuple of :class:`ApprovedEntry` objects.
    ``_index`` provides O(1) lookup by exact code string.
    """

    list_version: str
    entries: tuple[ApprovedEntry, ...]
    _index: dict[str, ApprovedEntry]

    def __init__(self, list_version: str, entries: tuple[ApprovedEntry, ...]) -> None:
        if not isinstance(list_version, str) or not list_version.strip():
            raise OntologyGuardError(
                "ontology_guard: 'list_version' must be a non-empty string"
            ) from None
        index: dict[str, ApprovedEntry] = {}
        for entry in entries:
            if entry.code in index:
                raise OntologyGuardError(
                    "ontology_guard: duplicate code in ApprovedOntologyList entries"
                ) from None
            index[entry.code] = entry
        # Use object.__setattr__ because the dataclass is frozen.
        object.__setattr__(self, "list_version", list_version)
        object.__setattr__(self, "entries", entries)
        object.__setattr__(self, "_index", index)


# ---------------------------------------------------------------------------
# Verdict
# ---------------------------------------------------------------------------


class GuardVerdict(StrEnum):
    """The result of :func:`check_code`.

    ALLOWED
        The code is present in the approved list and effective as of *as_of*.

    UNKNOWN
        The code is not present in the approved list at all.

    NOT_YET_EFFECTIVE
        The code is in the approved list but ``as_of < entry.effective_from``.

    EXPIRED
        The code is in the approved list but ``as_of > entry.effective_to``.
    """

    ALLOWED = "ALLOWED"
    UNKNOWN = "UNKNOWN"
    NOT_YET_EFFECTIVE = "NOT_YET_EFFECTIVE"
    EXPIRED = "EXPIRED"


# ---------------------------------------------------------------------------
# Explicit heading-marker helper (not used by check_code)
# ---------------------------------------------------------------------------


def strip_heading_marker(heading: str) -> str:
    """Strip leading Markdown ATX heading markers (``#`` characters) and the
    following whitespace from *heading*.

    ``"# ontology:telecom/voice/mobile"``  ->  ``"ontology:telecom/voice/mobile"``
    ``"## ontology:telecom/data"``          ->  ``"ontology:telecom/data"``
    ``"ontology:telecom/voice/mobile"``     ->  ``"ontology:telecom/voice/mobile"``

    This helper exists for callers that hold a raw ``Chunk.heading`` value
    (which ``rag._chunk_document`` stores verbatim, including the ``#``
    prefix, per the known quirk documented in ``ontology_alignment.py``).
    Pass the result to :func:`check_code`, not the raw heading.

    This function is **not** called inside :func:`check_code`.  A code that
    starts with ``"#"`` passed directly to :func:`check_code` returns
    ``GuardVerdict.UNKNOWN``.
    """
    return _ATX_MARKER_RE.sub("", heading)


# ---------------------------------------------------------------------------
# Internal loader helpers
# ---------------------------------------------------------------------------


def _parse_date(value: Any, field: str) -> date:
    """Parse *value* as an ISO 8601 date string (``YYYY-MM-DD``).

    Raises :class:`OntologyGuardError` (``from None``) on failure; never
    echoes the value.
    """
    if not isinstance(value, str):
        raise OntologyGuardError(
            f"ontology_guard: field '{field}' must be a string"
        ) from None
    if not _DATE_RE.fullmatch(value):
        raise OntologyGuardError(
            f"ontology_guard: field '{field}' has an invalid date format"
            " (expected YYYY-MM-DD)"
        ) from None
    try:
        parsed = date.fromisoformat(value)
    except ValueError:
        raise OntologyGuardError(
            f"ontology_guard: field '{field}' has an invalid date format"
            " (expected YYYY-MM-DD)"
        ) from None
    return parsed


def _parse_entry(raw: Any, index: int) -> ApprovedEntry:
    """Parse one entry dict; raise :class:`OntologyGuardError` on any
    structural problem.  Never echoes entry values.
    """
    if not isinstance(raw, dict):
        raise OntologyGuardError(
            f"ontology_guard: approved[{index}] must be a JSON object"
        ) from None

    unknown = set(raw.keys()) - _ENTRY_ALLOWED_KEYS
    if unknown:
        raise OntologyGuardError(
            f"ontology_guard: approved[{index}] has unknown key(s): "
            f"{sorted(unknown)!r}"
        ) from None

    missing = _ENTRY_REQUIRED_KEYS - set(raw.keys())
    if missing:
        raise OntologyGuardError(
            f"ontology_guard: approved[{index}] is missing required key(s): "
            f"{sorted(missing)!r}"
        ) from None

    raw_code = raw["code"]
    if not isinstance(raw_code, str):
        raise OntologyGuardError(
            f"ontology_guard: approved[{index}].code must be a string"
        ) from None
    if not _ONTOLOGY_CODE_RE.fullmatch(raw_code):
        raise OntologyGuardError(
            f"ontology_guard: approved[{index}].code is not a valid ontology"
            " identifier (must start with 'ontology:' followed by a"
            " non-empty, whitespace-free path)"
        ) from None

    effective_from = _parse_date(raw["effective_from"], f"approved[{index}].effective_from")

    effective_to: date | None = None
    if "effective_to" in raw:
        effective_to = _parse_date(raw["effective_to"], f"approved[{index}].effective_to")
        if effective_to < effective_from:
            raise OntologyGuardError(
                f"ontology_guard: approved[{index}].effective_to is before"
                " effective_from"
            ) from None

    return ApprovedEntry(
        code=raw_code,
        effective_from=effective_from,
        effective_to=effective_to,
    )


# ---------------------------------------------------------------------------
# Loader
# ---------------------------------------------------------------------------


def load_approved_list(payload: str | bytes) -> ApprovedOntologyList:
    """Parse and validate an approved-ontology list from a JSON string or bytes.

    Args:
        payload: The raw JSON text of the approved-ontology list (``str`` or
            ``bytes``).  Must not exceed 1 MiB.

    Returns:
        An :class:`ApprovedOntologyList` ready for use with :func:`check_code`.

    Raises:
        OntologyGuardError:
            - Payload exceeds 1 MiB.
            - The JSON is malformed.
            - The top-level value is not a JSON object.
            - The top-level object contains unknown keys.
            - ``list_version`` is missing, not a string, or empty.
            - ``approved`` is missing, not a JSON array, or empty.
            - Any entry has unknown keys, missing required keys, an invalid
              code (not starting with ``"ontology:"``), an invalid date
              format, or ``effective_to`` earlier than ``effective_from``.
            - Two entries share the same code (duplicates rejected).

    Messages never echo raw payload contents, entry values, SKU text, or
    model output.  Every raise uses ``from None`` so no original exception
    is chained.
    """
    # Size cap before any parsing.
    payload_bytes = payload if isinstance(payload, bytes) else payload.encode("utf-8")
    if len(payload_bytes) > _MAX_PAYLOAD_BYTES:
        raise OntologyGuardError(
            f"ontology_guard: payload exceeds the 1 MiB limit"
            f" ({len(payload_bytes)} bytes)"
        ) from None

    try:
        data = json.loads(payload)
    except json.JSONDecodeError:
        raise OntologyGuardError(
            "ontology_guard: payload is not valid JSON"
        ) from None

    if not isinstance(data, dict):
        raise OntologyGuardError(
            "ontology_guard: top-level value must be a JSON object"
        ) from None

    unknown_top = set(data.keys()) - _TOP_LEVEL_ALLOWED_KEYS
    if unknown_top:
        raise OntologyGuardError(
            f"ontology_guard: top-level has unknown key(s): {sorted(unknown_top)!r}"
        ) from None

    if "list_version" not in data:
        raise OntologyGuardError(
            "ontology_guard: missing required field 'list_version'"
        ) from None
    if not isinstance(data["list_version"], str) or not data["list_version"].strip():
        raise OntologyGuardError(
            "ontology_guard: 'list_version' must be a non-empty string"
        ) from None

    if "approved" not in data:
        raise OntologyGuardError(
            "ontology_guard: missing required field 'approved'"
        ) from None
    if not isinstance(data["approved"], list):
        raise OntologyGuardError(
            "ontology_guard: 'approved' must be a JSON array"
        ) from None
    if len(data["approved"]) == 0:
        raise OntologyGuardError(
            "ontology_guard: 'approved' must not be empty"
        ) from None

    entries: list[ApprovedEntry] = []
    seen_codes: dict[str, int] = {}
    for i, raw in enumerate(data["approved"]):
        entry = _parse_entry(raw, i)
        if entry.code in seen_codes:
            raise OntologyGuardError(
                f"ontology_guard: duplicate code at approved[{i}]"
                f" (first seen at approved[{seen_codes[entry.code]}])"
            ) from None
        seen_codes[entry.code] = i
        entries.append(entry)

    return ApprovedOntologyList(
        list_version=data["list_version"],
        entries=tuple(entries),
    )


# ---------------------------------------------------------------------------
# Check function
# ---------------------------------------------------------------------------


def check_code(
    code: str,
    as_of: date,
    approved_list: ApprovedOntologyList,
) -> GuardVerdict:
    """Check whether *code* is approved and effective on *as_of*.

    The lookup uses an **exact, case-sensitive** match on ``code.strip()``.
    ATX heading markers are **not** stripped: a code that starts with ``"#"``
    after stripping returns ``GuardVerdict.UNKNOWN``.  Callers holding a raw
    ``Chunk.heading`` must call :func:`strip_heading_marker` first.

    The effective window is **inclusive on both ends**:
    ``entry.effective_from <= as_of <= entry.effective_to``.

    Args:
        code:          The ontology class string to check.
        as_of:         The reference date for the effectivity window check.
                       Must be a ``datetime.date`` instance.  This function
                       never reads the system clock.
        approved_list: The validated approved-ontology list to check against.

    Returns:
        :attr:`GuardVerdict.ALLOWED` if the code is in the list and effective;
        :attr:`GuardVerdict.NOT_YET_EFFECTIVE` if the code is in the list but
        the effective date has not been reached;
        :attr:`GuardVerdict.EXPIRED` if the code is in the list but past its
        ``effective_to`` date;
        :attr:`GuardVerdict.UNKNOWN` if the code is not in the list at all.

    Raises:
        OntologyGuardError: If *as_of* is not a ``datetime.date`` instance.
    """
    if type(code) is not str:
        raise OntologyGuardError(
            "ontology_guard: check_code requires a str for 'code'"
        ) from None

    if type(as_of) is not date:
        raise OntologyGuardError(
            "ontology_guard: check_code requires a datetime.date for 'as_of'"
        ) from None

    normalised = code.strip()
    if not normalised or normalised.startswith("#"):
        return GuardVerdict.UNKNOWN

    entry = approved_list._index.get(normalised)

    if entry is None:
        return GuardVerdict.UNKNOWN

    if as_of < entry.effective_from:
        return GuardVerdict.NOT_YET_EFFECTIVE

    if entry.effective_to is not None and as_of > entry.effective_to:
        return GuardVerdict.EXPIRED

    return GuardVerdict.ALLOWED
