"""Evidence sink: destinations for invocation evidence records (Ch.17 §21).

Every AI action that crosses the governed boundary produces exactly one
InvocationEvidenceRecord before its output is consumed.  This module provides
the sink -- the destination to which that record is sent.

Why a sink layer?
-----------------
InvocationEvidenceRecord is the *what* (the immutable record); the sink is the
*where* (the durable destination).  The separation lets the Gateway swap ledger
back-ends (structured log -> OpenTelemetry -> cloud audit log) without touching
the record model or the callers in server.py.

Concrete implementations
------------------------
LoggingEvidenceSink
    Writes each record as a single-line JSON payload to the
    ztax_gateway.evidence logger at INFO level.

InMemoryEvidenceSink
    Accumulates records in a list.  Used only in tests.

Protocol
--------
EvidenceSink
    A typing.Protocol for structural typing.

Design rules
------------
* write is synchronous.
* No fiscal imports (ADR-0006 S2.6 isolation).
* No raw bytes from the record are re-expanded here.
"""

from __future__ import annotations

import json
import logging
from typing import Protocol, runtime_checkable

from .invocation_evidence import InvocationEvidenceRecord

__all__: list[str] = [
    "EvidenceSink",
    "InMemoryEvidenceSink",
    "LoggingEvidenceSink",
]

_log = logging.getLogger("ztax_gateway.evidence")


# ---------------------------------------------------------------------------
# EvidenceSink Protocol
# ---------------------------------------------------------------------------


@runtime_checkable
class EvidenceSink(Protocol):
    """Structural type for any evidence destination.

    Any object that exposes write(record: InvocationEvidenceRecord) -> None
    satisfies this protocol.
    """

    def write(self, record: InvocationEvidenceRecord) -> None:
        """Durably record *record*."""
        ...  # pragma: no cover


# ---------------------------------------------------------------------------
# LoggingEvidenceSink
# ---------------------------------------------------------------------------


class LoggingEvidenceSink:
    """Writes each record as a single-line JSON entry to ztax_gateway.evidence.

    This is the production implementation.  The logger name is intentionally
    distinct from ztax_gateway so operators can route evidence records to a
    dedicated audit-log stream without capturing all gateway noise.

    Parameters
    ----------
    logger:
        Override the logger.  Supply a test-capture logger in unit tests that
        need to assert on the log output.  Defaults to ztax_gateway.evidence.
    """

    def __init__(self, logger: logging.Logger | None = None) -> None:
        self._log = logger or _log

    def write(self, record: InvocationEvidenceRecord) -> None:
        """Serialize *record* to JSON and emit one INFO log line.

        If serialization raises for any reason, an error is logged and the
        exception propagates so that the Gateway fails closed (Ch.17 §21).
        """
        try:
            payload = json.dumps(record.as_dict(), sort_keys=True, default=str)
            self._log.info(payload, extra={"ai_invocation_id": record.ai_invocation_id})
        except Exception as exc:
            self._log.error(
                "evidence_sink: failed to serialize invocation evidence; "
                "record dropped -- THIS IS AN AUDIT GAP",
                extra={
                    "ai_invocation_id": getattr(record, "ai_invocation_id", "UNKNOWN"),
                    "error": str(exc),
                },
            )
            raise


# ---------------------------------------------------------------------------
# InMemoryEvidenceSink  (tests only)
# ---------------------------------------------------------------------------


class InMemoryEvidenceSink:
    """Accumulates records in a list for in-process inspection.

    Intended exclusively for tests.  Not thread-safe.

    Usage::

        sink = InMemoryEvidenceSink()
        gateway = Gateway(..., evidence_sink=sink)
        # ... call gateway.invoke(...)
        assert len(sink.records) == 1
        rec = sink.records[0]
        assert rec.invocation_outcome == InvocationOutcome.PROPOSE
    """

    def __init__(self) -> None:
        self.records: list[InvocationEvidenceRecord] = []

    def write(self, record: InvocationEvidenceRecord) -> None:
        """Append *record* to records."""
        self.records.append(record)

    def clear(self) -> None:
        """Remove all accumulated records."""
        self.records.clear()

    @property
    def last(self) -> InvocationEvidenceRecord:
        """The most recently written record.

        Raises IndexError when no records have been written yet.
        """
        return self.records[-1]
