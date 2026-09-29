"""AI Observability.
 
Chapter 17 §22 of the ZoikoTax Master Specification.
 
Records and buffers telemetry signals that arise from every governed AI
invocation.  The data source is :class:`~ztax_gateway.invocation_evidence.InvocationEvidenceRecord`;
nothing here accepts raw prompts, completions or source text -- only hashes and
references flow through.
 
What this module provides
--------------------------
:class:`SignalFamily`
    The eight §22 signal families:
 
    * ``MODEL_OPS``    -- latency, token counts, model and provider identity.
    * ``AGENT_OPS``    -- tool-call outcomes and call graph structure.
    * ``RETRIEVAL``    -- retrieval reference count and citation presence.
    * ``QUALITY``      -- guardrail pass / fail rates and outcome distribution.
    * ``SECURITY``     -- refusal events, kill-switch activations, authority
                          ceiling breaches.
    * ``RELIABILITY``  -- BLOCK and ABSTAIN outcome accounting.
    * ``FINOPS``       -- token spend and monetary cost per invocation.
    * ``GOVERNANCE``   -- authority outcomes, A3/A4 events requiring the
                          human-review gate, review_ref presence.
 
:class:`TelemetrySignal`
    One observation in one family.  Carries a name, a numeric value, a unit,
    and a label-set.  Labels never contain prompts, completions or source text;
    they carry only hashes, references, identifiers and outcome codes.
 
:class:`AuditEntry`
    A durable record of one A3-or-above, tool-call or BLOCK event.  Written to
    the :class:`AuditBuffer` unconditionally before any telemetry export is
    attempted, so a telemetry failure can never suppress the audit trail.
 
:class:`AuditBuffer`
    A bounded in-process FIFO of :class:`AuditEntry` objects.  Full buffer
    raises :class:`ObservabilityError` rather than silently dropping entries.
 
:func:`record_signals`
    Derive all applicable :class:`TelemetrySignal` objects from one evidence
    record.
 
:func:`record_audit_entries`
    Derive all applicable :class:`AuditEntry` objects and write them to a
    :class:`AuditBuffer`.
 
:class:`ObservabilityPipeline`
    Convenience façade: audits first, then derives signals.  No OpenTelemetry
    dependency -- that comes later as a configurable exporter plug-in.
 
Design rules (enforced here; no exceptions, no overrides)
----------------------------------------------------------
1. **No raw content.**  Labels and audit detail values carry only hashes
   (SHA-256 hex), reference IDs, identity strings and outcome codes.  No raw
   prompt, completion or source text.
2. **Audit before telemetry.**  :func:`record_audit_entries` is always called
   before :func:`record_signals` inside :class:`ObservabilityPipeline`.
3. **No fiscal imports.**  ADR-0006 §2.6 isolation: nothing from ``fiscal``,
   ``tax_decision`` or ``subledger`` is imported here.
4. **Buffer overflow is an error, not a drop.**  A full buffer raises
   :class:`ObservabilityError` so the caller knows the audit trail is at risk.
"""
 
from __future__ import annotations
 
import collections
from dataclasses import dataclass, field
from enum import StrEnum
from typing import Final
 
from .invocation_evidence import InvocationEvidenceRecord, InvocationOutcome
from .provenance import AuthorityOutcome
 
__all__: list[str] = [
    "AuditBuffer",
    "AuditEntry",
    "AuditEventKind",
    "ObservabilityError",
    "ObservabilityPipeline",
    "SignalFamily",
    "TelemetrySignal",
    "record_audit_entries",
    "record_signals",
]
 
# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------
 
# Authority level at and above which every invocation produces a durable audit
# entry regardless of whether the telemetry backend is available.
_AUDIT_AUTHORITY_THRESHOLD: Final[AuthorityOutcome] = AuthorityOutcome.A3
 
_AUTHORITY_ORDER: dict[AuthorityOutcome, int] = {
    a: i for i, a in enumerate(AuthorityOutcome)
}
 
# Default maximum entries in the AuditBuffer.
DEFAULT_AUDIT_BUFFER_CAPACITY: Final[int] = 10_000
 
 
# ---------------------------------------------------------------------------
# ObservabilityError
# ---------------------------------------------------------------------------
 
 
class ObservabilityError(Exception):
    """Raised when the observability layer cannot fulfil a contract.
 
    The primary case is a full :class:`AuditBuffer`: rather than silently
    dropping an audit entry, the module raises here so the caller can decide
    whether to flush, drain or alert.
    """
 
    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason
 
 
# ---------------------------------------------------------------------------
# SignalFamily
# ---------------------------------------------------------------------------
 
 
class SignalFamily(StrEnum):
    """The eight §22 signal families.
 
    ``MODEL_OPS``   -- per-invocation latency, input and output token counts,
                       provider and model identity, prompt version.
    ``AGENT_OPS``   -- tool-call count and permitted / refused breakdown.
    ``RETRIEVAL``   -- number of retrieval references and citation presence.
    ``QUALITY``     -- guardrail pass / fail rate and invocation outcome
                       distribution.
    ``SECURITY``    -- BLOCK events, kill-switch signals, authority-ceiling
                       breaches.
    ``RELIABILITY`` -- BLOCK and ABSTAIN outcome accounting.
    ``FINOPS``      -- monetary cost and token spend per invocation.
    ``GOVERNANCE``  -- authority outcome distribution, A3/A4 events requiring
                       the human-review gate.
    """
 
    MODEL_OPS = "model_ops"
    AGENT_OPS = "agent_ops"
    RETRIEVAL = "retrieval"
    QUALITY = "quality"
    SECURITY = "security"
    RELIABILITY = "reliability"
    FINOPS = "finops"
    GOVERNANCE = "governance"
 
 
# ---------------------------------------------------------------------------
# TelemetrySignal
# ---------------------------------------------------------------------------
 
 
@dataclass(frozen=True, slots=True)
class TelemetrySignal:
    """One observation in one §22 signal family.
 
    ``family``           -- which of the eight families this belongs to.
    ``name``             -- a stable, dot-separated metric name such as
                            ``"model_ops.latency_ms"``.
    ``value``            -- the numeric observation.
    ``unit``             -- the unit string: ``"ms"``, ``"tokens"``, ``"usd"``,
                            ``"count"``, ``"bool"`` or ``""`` (dimensionless).
    ``labels``           -- key / value pairs for grouping and filtering.
                            Values are strings only.  Labels MUST NOT contain
                            prompt text, completion text or source content.
                            Permitted label values: SHA-256 hex hashes,
                            reference IDs, identity strings (provider, model,
                            use_case) and outcome codes.
    ``ai_invocation_id`` -- links back to the source evidence record.
    """
 
    family: SignalFamily
    name: str
    value: float
    unit: str
    labels: dict[str, str]
    ai_invocation_id: str
 
    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation."""
        return {
            "family": self.family.value,
            "name": self.name,
            "value": self.value,
            "unit": self.unit,
            "labels": dict(self.labels),
            "ai_invocation_id": self.ai_invocation_id,
        }
 
 
# ---------------------------------------------------------------------------
# AuditEventKind
# ---------------------------------------------------------------------------
 
 
class AuditEventKind(StrEnum):
    """The kind of event captured in an :class:`AuditEntry`.
 
    ``A3_INVOCATION``   -- an invocation at A3 or above; the human-review
                           gate is required.
    ``TOOL_CALL``       -- one tool call from the invocation's tool-call log.
    ``BLOCK_EVENT``     -- the invocation was blocked.
    """
 
    A3_INVOCATION = "A3_INVOCATION"
    TOOL_CALL = "TOOL_CALL"
    BLOCK_EVENT = "BLOCK_EVENT"
 
 
# ---------------------------------------------------------------------------
# AuditEntry
# ---------------------------------------------------------------------------
 
 
@dataclass(frozen=True, slots=True)
class AuditEntry:
    """A durable record of one audit-worthy event.
 
    Written to the :class:`AuditBuffer` before telemetry is exported, so that
    a backend failure cannot suppress an audit trail entry.
 
    ``kind``              -- :class:`AuditEventKind`.
    ``ai_invocation_id``  -- links back to the source evidence record.
    ``use_case``          -- ``InvocationEvidenceRecord.caller_security_context_ref``.
    ``authority_outcome`` -- the A0-A5 ceiling at the time of the event.
    ``invocation_outcome``-- the §21 outcome (PROPOSE / BLOCK / etc.).
    ``detail``            -- structured detail dict; values are hashes,
                             reference IDs or outcome strings -- never raw text.
    ``recorded_at_iso``   -- ISO-8601 UTC string from the evidence record.
    """
 
    kind: AuditEventKind
    ai_invocation_id: str
    use_case: str
    authority_outcome: AuthorityOutcome
    invocation_outcome: InvocationOutcome
    detail: dict[str, str]
    recorded_at_iso: str
 
    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation."""
        return {
            "kind": self.kind.value,
            "ai_invocation_id": self.ai_invocation_id,
            "use_case": self.use_case,
            "authority_outcome": self.authority_outcome.value,
            "invocation_outcome": self.invocation_outcome.value,
            "detail": dict(self.detail),
            "recorded_at_iso": self.recorded_at_iso,
        }
 
 
# ---------------------------------------------------------------------------
# AuditBuffer
# ---------------------------------------------------------------------------
 
 
class AuditBuffer:
    """A bounded, durable in-process FIFO of :class:`AuditEntry` objects.
 
    When full, :meth:`append` raises :class:`ObservabilityError` rather than
    silently dropping entries -- a silent drop would mean a compliance event
    disappears without trace.
 
    :meth:`drain` removes and returns all entries in FIFO order.
    :meth:`peek` returns a snapshot without removing entries.
    """
 
    def __init__(self, capacity: int = DEFAULT_AUDIT_BUFFER_CAPACITY) -> None:
        if capacity < 1:
            raise ObservabilityError(
                f"observability: AuditBuffer capacity must be >= 1, got {capacity!r}"
            )
        self._capacity = capacity
        self._entries: collections.deque[AuditEntry] = collections.deque()
 
    @property
    def capacity(self) -> int:
        """Maximum number of entries this buffer holds."""
        return self._capacity
 
    @property
    def size(self) -> int:
        """Number of entries currently in the buffer."""
        return len(self._entries)
 
    @property
    def is_full(self) -> bool:
        """``True`` when the buffer has reached capacity."""
        return len(self._entries) >= self._capacity
 
    def append(self, entry: AuditEntry) -> None:
        """Add *entry* to the buffer.
 
        Raises :class:`ObservabilityError` if the buffer is full.
        """
        if not isinstance(entry, AuditEntry):
            raise ObservabilityError(
                "observability: AuditBuffer.append requires an AuditEntry"
            )
        if self.is_full:
            raise ObservabilityError(
                f"observability: AuditBuffer is full ({self._capacity} entries); "
                "drain before appending"
            )
        self._entries.append(entry)
 
    def drain(self) -> list[AuditEntry]:
        """Remove and return all entries in FIFO order."""
        entries = list(self._entries)
        self._entries.clear()
        return entries
 
    def peek(self) -> list[AuditEntry]:
        """Return a snapshot of all entries without removing them."""
        return list(self._entries)
 
 
# ---------------------------------------------------------------------------
# Internal helpers -- label sets
# ---------------------------------------------------------------------------
 
 
def _base_labels(record: InvocationEvidenceRecord) -> dict[str, str]:
    """Safe labels for every signal: identifiers and outcome codes only."""
    return {
        "use_case": record.caller_security_context_ref,
        "region": record.region,
        "provider": record.provider,
        "model": record.model,
        "authority_outcome": record.authority_outcome.value,
        "invocation_outcome": record.invocation_outcome.value,
    }
 
 
# ---------------------------------------------------------------------------
# Internal helpers -- per-family signal derivation
# ---------------------------------------------------------------------------
 
 
def _model_ops_signals(record: InvocationEvidenceRecord) -> list[TelemetrySignal]:
    labels = _base_labels(record)
    labels["prompt_version"] = record.prompt_version
    labels["ai_release_manifest_id"] = record.ai_release_manifest_id
    inv_id = record.ai_invocation_id
    return [
        TelemetrySignal(
            family=SignalFamily.MODEL_OPS,
            name="model_ops.latency_ms",
            value=float(record.usage.latency_ms),
            unit="ms",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.MODEL_OPS,
            name="model_ops.input_tokens",
            value=float(record.usage.input_tokens),
            unit="tokens",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.MODEL_OPS,
            name="model_ops.output_tokens",
            value=float(record.usage.output_tokens),
            unit="tokens",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.MODEL_OPS,
            name="model_ops.total_tokens",
            value=float(record.usage.total_tokens),
            unit="tokens",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.MODEL_OPS,
            name="model_ops.invocation_count",
            value=1.0,
            unit="count",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
    ]
 
 
def _agent_ops_signals(record: InvocationEvidenceRecord) -> list[TelemetrySignal]:
    labels = _base_labels(record)
    inv_id = record.ai_invocation_id
    total = len(record.tool_calls)
    permitted = sum(1 for tc in record.tool_calls if tc.outcome == "PERMITTED")
    refused = total - permitted
    return [
        TelemetrySignal(
            family=SignalFamily.AGENT_OPS,
            name="agent_ops.tool_call_count",
            value=float(total),
            unit="count",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.AGENT_OPS,
            name="agent_ops.tool_call_permitted_count",
            value=float(permitted),
            unit="count",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.AGENT_OPS,
            name="agent_ops.tool_call_refused_count",
            value=float(refused),
            unit="count",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
    ]
 
 
def _retrieval_signals(record: InvocationEvidenceRecord) -> list[TelemetrySignal]:
    labels = _base_labels(record)
    inv_id = record.ai_invocation_id
    ref_count = len(record.retrieval_refs)
    return [
        TelemetrySignal(
            family=SignalFamily.RETRIEVAL,
            name="retrieval.citation_count",
            value=float(ref_count),
            unit="count",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.RETRIEVAL,
            name="retrieval.has_citations",
            value=1.0 if ref_count > 0 else 0.0,
            unit="bool",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
    ]
 
 
def _quality_signals(record: InvocationEvidenceRecord) -> list[TelemetrySignal]:
    labels = _base_labels(record)
    inv_id = record.ai_invocation_id
    total_gr = len(record.guardrail_results)
    failed_gr = sum(1 for gr in record.guardrail_results if not gr.passed)
    passed_gr = total_gr - failed_gr
    return [
        TelemetrySignal(
            family=SignalFamily.QUALITY,
            name="quality.guardrail_count",
            value=float(total_gr),
            unit="count",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.QUALITY,
            name="quality.guardrail_passed_count",
            value=float(passed_gr),
            unit="count",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.QUALITY,
            name="quality.guardrail_failed_count",
            value=float(failed_gr),
            unit="count",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.QUALITY,
            name="quality.any_guardrail_failed",
            value=1.0 if record.any_guardrail_failed() else 0.0,
            unit="bool",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.QUALITY,
            name="quality.has_review_ref",
            value=1.0 if record.review_ref is not None else 0.0,
            unit="bool",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
    ]
 
 
def _security_signals(record: InvocationEvidenceRecord) -> list[TelemetrySignal]:
    labels = _base_labels(record)
    inv_id = record.ai_invocation_id
    is_block = record.invocation_outcome is InvocationOutcome.BLOCK
    kill_switch_fired = any(
        gr.verdict == "AI_KILL_SWITCH_ENGAGED" for gr in record.guardrail_results
    )
    authority_breach = any(
        gr.verdict == "AI_AUTHORITY_REFUSED" for gr in record.guardrail_results
    )
    return [
        TelemetrySignal(
            family=SignalFamily.SECURITY,
            name="security.block_event",
            value=1.0 if is_block else 0.0,
            unit="bool",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.SECURITY,
            name="security.kill_switch_fired",
            value=1.0 if kill_switch_fired else 0.0,
            unit="bool",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.SECURITY,
            name="security.authority_ceiling_breach",
            value=1.0 if authority_breach else 0.0,
            unit="bool",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
    ]
 
 
def _reliability_signals(record: InvocationEvidenceRecord) -> list[TelemetrySignal]:
    labels = _base_labels(record)
    inv_id = record.ai_invocation_id
    is_block = record.invocation_outcome is InvocationOutcome.BLOCK
    is_abstain = record.invocation_outcome is InvocationOutcome.ABSTAIN
    return [
        TelemetrySignal(
            family=SignalFamily.RELIABILITY,
            name="reliability.block_count",
            value=1.0 if is_block else 0.0,
            unit="count",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.RELIABILITY,
            name="reliability.abstain_count",
            value=1.0 if is_abstain else 0.0,
            unit="count",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
    ]
 
 
def _finops_signals(record: InvocationEvidenceRecord) -> list[TelemetrySignal]:
    labels = _base_labels(record)
    inv_id = record.ai_invocation_id
    return [
        TelemetrySignal(
            family=SignalFamily.FINOPS,
            name="finops.monetary_cost_usd",
            value=record.usage.monetary_cost_usd,
            unit="usd",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.FINOPS,
            name="finops.input_tokens",
            value=float(record.usage.input_tokens),
            unit="tokens",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.FINOPS,
            name="finops.output_tokens",
            value=float(record.usage.output_tokens),
            unit="tokens",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.FINOPS,
            name="finops.total_tokens",
            value=float(record.usage.total_tokens),
            unit="tokens",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
    ]
 
 
def _governance_signals(record: InvocationEvidenceRecord) -> list[TelemetrySignal]:
    labels = _base_labels(record)
    inv_id = record.ai_invocation_id
    is_a3_plus = (
        _AUTHORITY_ORDER[record.authority_outcome]
        >= _AUTHORITY_ORDER[_AUDIT_AUTHORITY_THRESHOLD]
    )
    return [
        TelemetrySignal(
            family=SignalFamily.GOVERNANCE,
            name="governance.invocation_count",
            value=1.0,
            unit="count",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.GOVERNANCE,
            name="governance.a3_plus_invocation",
            value=1.0 if is_a3_plus else 0.0,
            unit="bool",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
        TelemetrySignal(
            family=SignalFamily.GOVERNANCE,
            name="governance.review_required",
            value=1.0 if record.invocation_outcome is InvocationOutcome.REVIEW else 0.0,
            unit="bool",
            labels=labels,
            ai_invocation_id=inv_id,
        ),
    ]
 
 
# ---------------------------------------------------------------------------
# Public API -- record_signals
# ---------------------------------------------------------------------------
 
 
def record_signals(record: InvocationEvidenceRecord) -> list[TelemetrySignal]:
    """Derive all §22 telemetry signals from one evidence record.
 
    Returns a flat list covering all eight signal families.  The list is always
    non-empty.  No side-effects; the caller forwards to the telemetry backend.
    """
    if not isinstance(record, InvocationEvidenceRecord):
        raise ObservabilityError(
            "observability: record_signals requires an InvocationEvidenceRecord"
        )
    signals: list[TelemetrySignal] = []
    signals.extend(_model_ops_signals(record))
    signals.extend(_agent_ops_signals(record))
    signals.extend(_retrieval_signals(record))
    signals.extend(_quality_signals(record))
    signals.extend(_security_signals(record))
    signals.extend(_reliability_signals(record))
    signals.extend(_finops_signals(record))
    signals.extend(_governance_signals(record))
    return signals
 
 
# ---------------------------------------------------------------------------
# Public API -- record_audit_entries
# ---------------------------------------------------------------------------
 
 
def _is_a3_plus(record: InvocationEvidenceRecord) -> bool:
    return (
        _AUTHORITY_ORDER[record.authority_outcome]
        >= _AUTHORITY_ORDER[_AUDIT_AUTHORITY_THRESHOLD]
    )
 
 
def record_audit_entries(
    record: InvocationEvidenceRecord,
    buffer: AuditBuffer,
) -> list[AuditEntry]:
    """Write durable audit entries to *buffer* and return them.
 
    Entries are written for:
 
    * Every **A3+ invocation** -- one :attr:`~AuditEventKind.A3_INVOCATION`
      entry.
    * Every **tool call** in the evidence record -- one
      :attr:`~AuditEventKind.TOOL_CALL` entry per call.
    * Every **BLOCK event** -- one :attr:`~AuditEventKind.BLOCK_EVENT` entry.
 
    The write is **all-or-nothing**: if *buffer* cannot hold every entry this
    record needs, :class:`ObservabilityError` is raised and *no* entry is
    written, so a retry after draining cannot duplicate entries.
    """
    if not isinstance(record, InvocationEvidenceRecord):
        raise ObservabilityError(
            "observability: record_audit_entries requires an InvocationEvidenceRecord"
        )
    if not isinstance(buffer, AuditBuffer):
        raise ObservabilityError(
            "observability: record_audit_entries requires an AuditBuffer"
        )
 
    recorded_at = record.recorded_at.isoformat()
    entries: list[AuditEntry] = []
 
    # 1. A3+ invocation entry.
    if _is_a3_plus(record):
        detail: dict[str, str] = {
            "authority_outcome": record.authority_outcome.value,
            "invocation_outcome": record.invocation_outcome.value,
        }
        if record.review_ref is not None:
            detail["review_ref"] = record.review_ref
        if record.input_hash is not None:
            detail["input_hash"] = record.input_hash
        entry = AuditEntry(
            kind=AuditEventKind.A3_INVOCATION,
            ai_invocation_id=record.ai_invocation_id,
            use_case=record.caller_security_context_ref,
            authority_outcome=record.authority_outcome,
            invocation_outcome=record.invocation_outcome,
            detail=detail,
            recorded_at_iso=recorded_at,
        )
        entries.append(entry)
 
    # 2. Tool-call entries (for every authority level, not just A3+).
    for tc in record.tool_calls:
        tc_detail: dict[str, str] = {
            "tool_id": tc.tool_id,
            "call_id": tc.call_id,
            "caller_id": tc.caller_id,
            "outcome": tc.outcome,
        }
        if tc.payload_hash is not None:
            tc_detail["payload_hash"] = tc.payload_hash
        if tc.reviewer_id is not None:
            tc_detail["reviewer_id"] = tc.reviewer_id
        tc_entry = AuditEntry(
            kind=AuditEventKind.TOOL_CALL,
            ai_invocation_id=record.ai_invocation_id,
            use_case=record.caller_security_context_ref,
            authority_outcome=record.authority_outcome,
            invocation_outcome=record.invocation_outcome,
            detail=tc_detail,
            recorded_at_iso=recorded_at,
        )
        entries.append(tc_entry)
 
    # 3. BLOCK event entry.
    if record.invocation_outcome is InvocationOutcome.BLOCK:
        block_detail: dict[str, str] = {}
        if record.input_hash is not None:
            block_detail["input_hash"] = record.input_hash
        for gr in record.guardrail_results:
            if not gr.passed:
                block_detail[f"refusal.{gr.guardrail_id}"] = gr.verdict
                if gr.detail:
                    block_detail[f"refusal_detail.{gr.guardrail_id}"] = gr.detail
        block_entry = AuditEntry(
            kind=AuditEventKind.BLOCK_EVENT,
            ai_invocation_id=record.ai_invocation_id,
            use_case=record.caller_security_context_ref,
            authority_outcome=record.authority_outcome,
            invocation_outcome=record.invocation_outcome,
            detail=block_detail,
            recorded_at_iso=recorded_at,
        )
        entries.append(block_entry)
 
    # 4. All-or-nothing write.  Every entry is built first and the buffer is
    # checked for room for the *whole* set before anything is appended.  Writing
    # entry by entry would leave a partial set behind when the buffer fills
    # part-way, and a retry after draining would then duplicate those entries
    # in the audit trail.
    free = buffer.capacity - buffer.size
    if len(entries) > free:
        raise ObservabilityError(
            f"observability: AuditBuffer is full -- room for {free} more "
            f"entries but this record needs {len(entries)} "
            f"({buffer.capacity} capacity); drain before appending, "
            "nothing was written"
        )
    for audit_entry in entries:
        buffer.append(audit_entry)
 
    return entries
 
 
# ---------------------------------------------------------------------------
# ObservabilityPipeline
# ---------------------------------------------------------------------------
 
 
@dataclass
class ObservabilityPipeline:
    """Convenience façade: audit first, then derive signals.
 
    Usage::
 
        pipeline = ObservabilityPipeline()
        signals  = pipeline.observe(record)
        entries  = pipeline.buffer.drain()
 
    ``buffer``  -- the :class:`AuditBuffer` this pipeline writes to.
    ``signals`` -- accumulated :class:`TelemetrySignal` objects across all
                   :meth:`observe` calls.  Cleared by :meth:`drain_signals`.
 
    :meth:`observe` calls :func:`record_audit_entries` *before*
    :func:`record_signals` (design rule 2).  If the buffer is full,
    :class:`ObservabilityError` is raised before any signals are produced for
    that record.
    """
 
    buffer: AuditBuffer = field(default_factory=AuditBuffer)
    _signals: list[TelemetrySignal] = field(default_factory=list, init=False, repr=False)
 
    def observe(self, record: InvocationEvidenceRecord) -> list[TelemetrySignal]:
        """Process one evidence record: audit first, then derive signals.
 
        Returns the signals derived from *record* and appends them to
        :attr:`signals`.  Raises :class:`ObservabilityError` if the buffer is
        full.
        """
        # Audit before telemetry -- design rule 2.
        record_audit_entries(record, self.buffer)
        new_signals = record_signals(record)
        self._signals.extend(new_signals)
        return new_signals
 
    @property
    def signals(self) -> list[TelemetrySignal]:
        """All signals accumulated since the last :meth:`drain_signals` call."""
        return list(self._signals)
 
    def drain_signals(self) -> list[TelemetrySignal]:
        """Remove and return all accumulated signals."""
        signals = list(self._signals)
        self._signals.clear()
        return signals
 