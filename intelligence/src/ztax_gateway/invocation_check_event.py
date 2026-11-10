"""Answer-checked events: the result of a check on an AI answer, as a linked event.

An invocation evidence record (``invocation_evidence.py``) is written once,
before the answer is consumed, and is never edited. A check on that answer
(for example the explanation guard) happens later and may be repeated by a
newer validator. Its result is therefore a separate, append-only event that
names the original record by ``ai_invocation_id`` and overwrites nothing
(ZTAX-AI-CONF-ADD01 §6: append subsequent check results as linked immutable
events; never overwrite prior evidence).

This module holds the event, the two sinks that carry it, and nothing else.
It does not import the record's builder and never touches a record.

Signing of check events is a later step and needs a key and a signer that do
not exist yet. Nothing here is signed and nothing pretends to be.

Design rules
------------
* The event is frozen and validated at construction.
* ``reason_code`` is a fixed list, not free text, so it is safe to log.
* No fiscal imports (ADR-0006 S2.6 isolation).
"""

from __future__ import annotations

import json
import logging
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from enum import StrEnum
from typing import Protocol, runtime_checkable

__all__: list[str] = [
    "AnswerCheckedEvent",
    "CheckEventError",
    "CheckEventSink",
    "CheckReason",
    "InMemoryCheckEventSink",
    "LoggingCheckEventSink",
    "Verdict",
]

_log = logging.getLogger("ztax_gateway.evidence.check")

EVENT_TYPE = "answer_checked"


class CheckEventError(ValueError):
    """Raised when an :class:`AnswerCheckedEvent` cannot be constructed."""


class Verdict(StrEnum):
    """What the check concluded about the answer."""

    PASS = "PASS"
    REVIEW_REQUIRED = "REVIEW_REQUIRED"
    ABSTAIN = "ABSTAIN"
    BLOCK = "BLOCK"


class CheckReason(StrEnum):
    """Why the check reached its verdict. A fixed list, not free text.

    The ``GUARD_*`` values are the reason codes the explanation guard in
    ``tools/report_ai_http.py`` returns today; ``MODEL_ABSTAINED`` is the token
    that service logs when the model itself abstains. ``CHECKS_PASSED`` goes
    with ``Verdict.PASS``. Add a value only together with the check that
    produces it.

    The ``GUARD_ONTOLOGY_*`` values are produced by the ontology guard in
    ``tools/report_ai_http.py`` (``POST /demo/classify``).  They correspond
    one-to-one to :class:`ztax_gateway.ontology_guard.GuardVerdict` values
    that are not ``ALLOWED``:

    - ``GUARD_ONTOLOGY_UNKNOWN``           -- code not in the approved list.
    - ``GUARD_ONTOLOGY_NOT_YET_EFFECTIVE`` -- code present but not yet effective.
    - ``GUARD_ONTOLOGY_EXPIRED``           -- code present but past its ``effective_to``.
    - ``GUARD_ONTOLOGY_NOT_CONFIGURED``    -- approved list not configured (no code checked).
    """

    CHECKS_PASSED = "CHECKS_PASSED"
    GUARD_NO_CITATION = "GUARD_NO_CITATION"
    GUARD_FOREIGN_CITATION = "GUARD_FOREIGN_CITATION"
    GUARD_STATUS_MISMATCH = "GUARD_STATUS_MISMATCH"
    GUARD_NUMBER_NOT_IN_ROWS = "GUARD_NUMBER_NOT_IN_ROWS"
    MODEL_ABSTAINED = "MODEL_ABSTAINED"
    GUARD_ONTOLOGY_UNKNOWN = "GUARD_ONTOLOGY_UNKNOWN"
    GUARD_ONTOLOGY_NOT_YET_EFFECTIVE = "GUARD_ONTOLOGY_NOT_YET_EFFECTIVE"
    GUARD_ONTOLOGY_EXPIRED = "GUARD_ONTOLOGY_EXPIRED"
    GUARD_ONTOLOGY_NOT_CONFIGURED = "GUARD_ONTOLOGY_NOT_CONFIGURED"


@dataclass(frozen=True, slots=True)
class AnswerCheckedEvent:
    """One check result, linked to an existing invocation evidence record.

    ``evidence_id``         -- the ``ai_invocation_id`` of the original record.
    ``verdict``             -- :class:`Verdict`.
    ``reason_code``         -- :class:`CheckReason`.
    ``validator_version``   -- the version of the check that ran.
    ``created_at``          -- timezone-aware UTC time of the check.
    ``source_record_ids``   -- ids of the rows the check used (default none).
    ``policy_version``      -- the policy the check applied (default none).
    """

    evidence_id: str
    verdict: Verdict
    reason_code: CheckReason
    validator_version: str
    created_at: datetime
    source_record_ids: tuple[str, ...] = ()
    policy_version: str | None = None

    def __post_init__(self) -> None:
        if not isinstance(self.evidence_id, str) or not self.evidence_id.strip():
            raise CheckEventError("evidence_id must be a non-empty string")
        if not isinstance(self.verdict, Verdict):
            raise CheckEventError(f"verdict must be a Verdict, got {self.verdict!r}")
        if not isinstance(self.reason_code, CheckReason):
            raise CheckEventError(f"reason_code must be a CheckReason, got {self.reason_code!r}")
        if not isinstance(self.validator_version, str) or not self.validator_version.strip():
            raise CheckEventError("validator_version must be a non-empty string")
        if not isinstance(self.created_at, datetime):
            raise CheckEventError("created_at must be a datetime")
        if self.created_at.tzinfo is None or self.created_at.utcoffset() != timedelta(0):
            raise CheckEventError("created_at must be timezone-aware UTC")
        if not isinstance(self.source_record_ids, tuple) or not all(
            isinstance(s, str) and s for s in self.source_record_ids
        ):
            raise CheckEventError("source_record_ids must be a tuple of non-empty strings")
        if self.policy_version is not None and (
            not isinstance(self.policy_version, str) or not self.policy_version.strip()
        ):
            raise CheckEventError("policy_version must be None or a non-empty string")

    def to_dict(self) -> dict[str, object]:
        """A complete, log-safe representation (same style as the evidence record)."""
        d: dict[str, object] = {
            "event_type": EVENT_TYPE,
            "evidence_id": self.evidence_id,
            "verdict": self.verdict.value,
            "reason_code": self.reason_code.value,
            "validator_version": self.validator_version,
            "created_at": self.created_at.astimezone(UTC).isoformat(),
            "source_record_ids": list(self.source_record_ids),
        }
        if self.policy_version is not None:
            d["policy_version"] = self.policy_version
        return d


# ---------------------------------------------------------------------------
# Sinks
#
# A separate protocol rather than a second method on EvidenceSink: adding a
# method to that protocol would make every existing sink (and every test double)
# fail to satisfy it, and its write() signature is fixed. These sinks carry the
# same payload style and the same "ztax_gateway.evidence" logger family, so an
# operator routing that stream gets check events with the records.
# ---------------------------------------------------------------------------


@runtime_checkable
class CheckEventSink(Protocol):
    """Any destination that can append an :class:`AnswerCheckedEvent`."""

    def write_check_event(self, event: AnswerCheckedEvent) -> None:
        """Durably append *event*. Never edits an earlier one."""
        ...  # pragma: no cover


class LoggingCheckEventSink:
    """Writes each event as one single-line JSON entry to ``ztax_gateway.evidence.check``."""

    def __init__(self, logger: logging.Logger | None = None) -> None:
        self._log = logger or _log

    def write_check_event(self, event: AnswerCheckedEvent) -> None:
        payload = json.dumps(event.to_dict(), sort_keys=True)
        self._log.info(payload, extra={"ai_invocation_id": event.evidence_id})


class InMemoryCheckEventSink:
    """Accumulates events in append order. Intended for tests. Not thread-safe."""

    def __init__(self) -> None:
        self._events: list[AnswerCheckedEvent] = []

    @property
    def events(self) -> tuple[AnswerCheckedEvent, ...]:
        """A snapshot in append order; callers cannot mutate the log through it."""
        return tuple(self._events)

    def write_check_event(self, event: AnswerCheckedEvent) -> None:
        self._events.append(event)

    def for_evidence(self, evidence_id: str) -> tuple[AnswerCheckedEvent, ...]:
        """Every event linked to *evidence_id*, in append order."""
        return tuple(e for e in self._events if e.evidence_id == evidence_id)
