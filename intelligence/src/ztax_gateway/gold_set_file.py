"""Gold-Set File Format — provenance-tracked benchmark loader.

Guidance (not approved requirements): the Engineering Reset Playbook
ZTAX-ENG-RESET-001 section 20 states that expected results must come from an
independent oracle and never from the function under test. Section 7 allows an
INVENTED training pack only if it is clearly labelled as invented. This module
provides the file format, parser and guard so that DRAFT or unreviewed cases
can never silently enter the evaluation pipeline as evidence.

Design rules
------------
* Standard library only. No network, no clock, no env vars, no logging, no YAML.
* JSON only. Input larger than _MAX_FILE_BYTES is refused before parsing.
* No imports from any other ztax_gateway module except the three named below.
* No fiscal imports (ADR-0006 §2.6): nothing from fiscal, tax_decision or subledger.
* Frozen dataclasses and StrEnum throughout.
* Pure functions: identical inputs -> identical outputs, no side-effects.
* NaN and Infinity constants are rejected at parse time.
* Unknown JSON keys are rejected at every level so schema drift is caught early.

Public API
----------
GoldSetFileError
    Raised for any parse, validation or eligibility failure.
Origin          StrEnum  SYNTHETIC | SOURCE_DERIVED | PRODUCTION_DERIVED
ReviewStatus    StrEnum  DRAFT | REVIEWED
FileCaseRecord  frozen dataclass -- one validated case from the file
GoldSetFile     frozen dataclass -- the validated file as a whole
parse_gold_set(text: str) -> GoldSetFile
load_gold_set_file(path: Path) -> GoldSetFile
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass
from enum import StrEnum
from pathlib import Path
from typing import Any

from ztax_gateway.evaluation_quality import GoldCase, GoldSetError, GoldSetStore

__all__: list[str] = [
    "FileCaseRecord",
    "GoldSetFile",
    "GoldSetFileError",
    "Origin",
    "ReviewStatus",
    "load_gold_set_file",
    "parse_gold_set",
]

# ---------------------------------------------------------------------------
# Limits
# ---------------------------------------------------------------------------

_MAX_FILE_BYTES: int = 1 * 1024 * 1024  # 1 MiB
_MAX_CASES: int = 10_000

# case_id must match this pattern -- uppercase letters, digits and hyphens only.
_CASE_ID_RE: re.Pattern[str] = re.compile(r"^[A-Z0-9-]+$")

# Keys allowed at the top-level file object.
_TOP_KEYS: frozenset[str] = frozenset({"name", "version", "domain", "purpose", "cases"})

# Keys allowed inside each case object.
_CASE_KEYS: frozenset[str] = frozenset(
    {
        "case_id",
        "sku_description",
        "expected_class",
        "notes",
        "origin",
        "author",
        "review_status",
        "reviewer",
        "reviewed_on",
        "source_ref",
    }
)

# Simple ISO date pattern YYYY-MM-DD.
_DATE_RE: re.Pattern[str] = re.compile(r"^\d{4}-\d{2}-\d{2}$")


# ---------------------------------------------------------------------------
# Enumerations
# ---------------------------------------------------------------------------


class Origin(StrEnum):
    """How the expected class was derived."""

    SYNTHETIC = "SYNTHETIC"
    SOURCE_DERIVED = "SOURCE_DERIVED"
    PRODUCTION_DERIVED = "PRODUCTION_DERIVED"


class ReviewStatus(StrEnum):
    """Review workflow state for a gold-set case."""

    DRAFT = "DRAFT"
    REVIEWED = "REVIEWED"


# ---------------------------------------------------------------------------
# Errors
# ---------------------------------------------------------------------------


class GoldSetFileError(Exception):
    """Raised when a gold-set file cannot be parsed, validated or used as evidence."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------------------
# Per-case record
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class FileCaseRecord:
    """One validated case as read from the gold-set file."""

    case_id: str
    sku_description: str
    expected_class: str
    notes: str
    origin: Origin
    author: str
    review_status: ReviewStatus
    reviewer: str  # empty string when DRAFT
    reviewed_on: str  # empty string when DRAFT, ISO date otherwise
    source_ref: str  # empty string for SYNTHETIC


# ---------------------------------------------------------------------------
# Top-level file object
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class GoldSetFile:
    """The parsed, validated gold-set file.

    ``name``, ``version`` and ``cases`` come directly from the JSON object.
    ``content_sha256`` is SHA-256 over the *canonical* JSON (sorted keys,
    compact separators, UTF-8) so the hash is deterministic and independent
    of whitespace or key ordering in the original file.
    """

    name: str
    version: str
    content_sha256: str
    cases: tuple[FileCaseRecord, ...]

    def review_summary(self) -> dict[str, int]:
        """Return counts: total, draft, reviewed, synthetic."""
        total = len(self.cases)
        draft = sum(1 for c in self.cases if c.review_status == ReviewStatus.DRAFT)
        reviewed = total - draft
        synthetic = sum(1 for c in self.cases if c.origin == Origin.SYNTHETIC)
        return {
            "total": total,
            "draft": draft,
            "reviewed": reviewed,
            "synthetic": synthetic,
        }

    def to_gold_store(self, *, allow_synthetic: bool = False) -> GoldSetStore:
        """Build an immutable GoldSetStore from REVIEWED cases only.

        Parameters
        ----------
        allow_synthetic:
            When ``False`` (the default), SYNTHETIC cases are excluded even if
            they are REVIEWED.  Set to ``True`` only in contexts where synthetic
            data is explicitly permitted (e.g. internal CI smoke tests).

        Raises
        ------
        GoldSetFileError
            When no case passes the eligibility filter.
        """
        eligible: list[GoldCase] = []
        for case in self.cases:
            if case.review_status != ReviewStatus.REVIEWED:
                continue
            if case.origin == Origin.SYNTHETIC and not allow_synthetic:
                continue
            eligible.append(
                GoldCase(
                    case_id=case.case_id,
                    sku_description=case.sku_description,
                    expected_class=case.expected_class,
                    notes=case.notes,
                )
            )

        if not eligible:
            raise GoldSetFileError(
                f"gold-set file {self.name!r}: no eligible cases for GoldSetStore "
                f"(allow_synthetic={allow_synthetic}); "
                "ensure at least one REVIEWED case exists"
            )

        store_version = f"{self.version}+{self.content_sha256[:12]}"
        try:
            return GoldSetStore(name=self.name, version=store_version, cases=eligible)
        except GoldSetError as exc:
            raise GoldSetFileError(str(exc)) from exc


# ---------------------------------------------------------------------------
# Canonical hash helper
# ---------------------------------------------------------------------------


def _canonical_sha256(data: Any) -> str:
    """Return hex SHA-256 of the canonical JSON serialisation of *data*.

    Canonical means: sorted keys, compact separators (',' and ':'), UTF-8.
    """
    canonical = json.dumps(data, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


# ---------------------------------------------------------------------------
# Validation helpers
# ---------------------------------------------------------------------------


def _reject_unknown_keys(obj: dict[str, Any], allowed: frozenset[str], context: str) -> None:
    unknown = set(obj.keys()) - allowed
    if unknown:
        quoted = ", ".join(sorted(f"{k!r}" for k in unknown))
        raise GoldSetFileError(f"{context}: unknown key(s) {quoted}")


def _require_non_empty_str(value: Any, field: str, context: str) -> str:
    if not isinstance(value, str):
        raise GoldSetFileError(f"{context}: field {field!r} must be a string")
    if not value.strip():
        raise GoldSetFileError(f"{context}: field {field!r} must not be empty")
    return value


def _validate_case(raw: Any, index: int) -> FileCaseRecord:
    if not isinstance(raw, dict):
        raise GoldSetFileError(f"cases[{index}]: must be a JSON object")

    context = f"cases[{index}]"
    _reject_unknown_keys(raw, _CASE_KEYS, context)

    case_id = _require_non_empty_str(raw.get("case_id", ""), "case_id", context)
    context = f"case {case_id!r}"

    if not _CASE_ID_RE.match(case_id):
        raise GoldSetFileError(f"{context}: case_id {case_id!r} must match [A-Z0-9-]+")

    sku_description = _require_non_empty_str(
        raw.get("sku_description", ""), "sku_description", context
    )
    expected_class = _require_non_empty_str(
        raw.get("expected_class", ""), "expected_class", context
    )
    notes = raw.get("notes", "")
    if not isinstance(notes, str):
        raise GoldSetFileError(f"{context}: field 'notes' must be a string")

    raw_origin = raw.get("origin", "")
    if not isinstance(raw_origin, str) or not raw_origin.strip():
        raise GoldSetFileError(f"{context}: field 'origin' must be a non-empty string")
    try:
        origin = Origin(raw_origin)
    except ValueError:
        valid = ", ".join(str(o) for o in Origin)
        raise GoldSetFileError(
            f"{context}: 'origin' value {raw_origin!r} is not valid; must be one of {valid}"
        ) from None

    author = _require_non_empty_str(raw.get("author", ""), "author", context)

    raw_status = raw.get("review_status", "")
    if not isinstance(raw_status, str) or not raw_status.strip():
        raise GoldSetFileError(f"{context}: field 'review_status' must be a non-empty string")
    try:
        review_status = ReviewStatus(raw_status)
    except ValueError:
        valid_s = ", ".join(str(s) for s in ReviewStatus)
        raise GoldSetFileError(
            f"{context}: 'review_status' value {raw_status!r} is not valid; "
            f"must be one of {valid_s}"
        ) from None

    reviewer = raw.get("reviewer", "")
    reviewed_on = raw.get("reviewed_on", "")

    if not isinstance(reviewer, str):
        raise GoldSetFileError(f"{context}: field 'reviewer' must be a string")
    if not isinstance(reviewed_on, str):
        raise GoldSetFileError(f"{context}: field 'reviewed_on' must be a string")

    if review_status == ReviewStatus.REVIEWED:
        if not reviewer.strip():
            raise GoldSetFileError(
                f"{context}: REVIEWED case must have a non-empty 'reviewer'"
            )
        if not reviewed_on.strip():
            raise GoldSetFileError(
                f"{context}: REVIEWED case must have a non-empty 'reviewed_on'"
            )
        if not _DATE_RE.match(reviewed_on):
            raise GoldSetFileError(
                f"{context}: 'reviewed_on' must be an ISO date YYYY-MM-DD, "
                f"got {reviewed_on!r}"
            )
        if reviewer.strip().lower() == author.strip().lower():
            raise GoldSetFileError(
                f"{context}: 'reviewer' must differ from 'author'; "
                f"a case cannot be reviewed by its own author ({author!r})"
            )
    else:
        if reviewer.strip():
            raise GoldSetFileError(
                f"{context}: DRAFT case must not have a 'reviewer' (got {reviewer!r})"
            )
        if reviewed_on.strip():
            raise GoldSetFileError(
                f"{context}: DRAFT case must not have a 'reviewed_on' (got {reviewed_on!r})"
            )

    source_ref = raw.get("source_ref", "")
    if not isinstance(source_ref, str):
        raise GoldSetFileError(f"{context}: field 'source_ref' must be a string")
    if origin != Origin.SYNTHETIC and not source_ref.strip():
        raise GoldSetFileError(
            f"{context}: non-SYNTHETIC case (origin={origin!r}) requires a non-empty 'source_ref'"
        )

    return FileCaseRecord(
        case_id=case_id,
        sku_description=sku_description,
        expected_class=expected_class,
        notes=notes,
        origin=origin,
        author=author,
        review_status=review_status,
        reviewer=reviewer,
        reviewed_on=reviewed_on,
        source_ref=source_ref,
    )


# ---------------------------------------------------------------------------
# Parser
# ---------------------------------------------------------------------------


def parse_gold_set(text: str) -> GoldSetFile:
    """Parse and validate a gold-set JSON string.

    Parameters
    ----------
    text:
        Raw JSON text. Must be a UTF-8 string; byte strings are not accepted.

    Raises
    ------
    GoldSetFileError
        For any structural, schema or business-rule violation, including
        invalid JSON (never a bare ``json.JSONDecodeError``).
    """
    if len(text.encode("utf-8")) > _MAX_FILE_BYTES:
        raise GoldSetFileError(
            f"gold-set file exceeds the 1 MiB limit ({len(text.encode('utf-8'))} bytes)"
        )

    # Reject NaN/Infinity tokens before parsing -- some Python builds accept them.
    for token in ("NaN", "Infinity", "-Infinity"):
        if token in text:
            raise GoldSetFileError(
                f"gold-set file contains the JSON constant {token!r}, which is not permitted"
            )

    try:
        raw = json.loads(text)
    except json.JSONDecodeError as exc:
        raise GoldSetFileError(f"gold-set file is not valid JSON: {exc}") from exc

    if not isinstance(raw, dict):
        raise GoldSetFileError("gold-set file must be a JSON object at the top level")

    _reject_unknown_keys(raw, _TOP_KEYS, "gold-set file")

    name = _require_non_empty_str(raw.get("name", ""), "name", "gold-set file")
    version = _require_non_empty_str(raw.get("version", ""), "version", "gold-set file")

    if "domain" in raw and not isinstance(raw["domain"], str):
        raise GoldSetFileError("gold-set file: field 'domain' must be a string")
    if "purpose" in raw and not isinstance(raw["purpose"], str):
        raise GoldSetFileError("gold-set file: field 'purpose' must be a string")

    raw_cases = raw.get("cases")
    if not isinstance(raw_cases, list):
        raise GoldSetFileError("gold-set file: field 'cases' must be a JSON array")

    if len(raw_cases) > _MAX_CASES:
        raise GoldSetFileError(
            f"gold-set file: too many cases ({len(raw_cases)} > {_MAX_CASES})"
        )

    seen_ids: set[str] = set()
    validated: list[FileCaseRecord] = []
    for i, raw_case in enumerate(raw_cases):
        record = _validate_case(raw_case, i)
        if record.case_id in seen_ids:
            raise GoldSetFileError(
                f"case {record.case_id!r}: duplicate case_id at index {i}"
            )
        seen_ids.add(record.case_id)
        validated.append(record)

    content_sha256 = _canonical_sha256(raw)

    return GoldSetFile(
        name=name,
        version=version,
        content_sha256=content_sha256,
        cases=tuple(validated),
    )


# ---------------------------------------------------------------------------
# File loader
# ---------------------------------------------------------------------------


def load_gold_set_file(path: Path) -> GoldSetFile:
    """Read a gold-set JSON file from *path* and return a validated GoldSetFile.

    Parameters
    ----------
    path:
        Filesystem path to the ``.json`` file.

    Raises
    ------
    GoldSetFileError
        If the file fails validation.
    OSError
        If the file cannot be opened (re-raised as-is).
    """
    raw_bytes = path.read_bytes()
    if len(raw_bytes) > _MAX_FILE_BYTES:
        raise GoldSetFileError(
            f"gold-set file {path}: exceeds the 1 MiB limit ({len(raw_bytes)} bytes)"
        )
    try:
        text = raw_bytes.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise GoldSetFileError(f"gold-set file {path}: not valid UTF-8: {exc}") from exc
    return parse_gold_set(text)
