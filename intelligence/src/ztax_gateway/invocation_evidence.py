
"""AI Invocation Evidence Model.
 
Chapter 17 §21 of the ZoikoTax Master Specification.
 
Every AI action that crosses the governed boundary must produce exactly one
tamper-evident audit record before its output is consumed by any downstream
system.  This module provides that record.
 
What exists in the estate before this module
--------------------------------------------
Every other module in the gateway produces *partial* governance artefacts:
 
* ``governance.py``        -- authorises and produces a refusal code or ``UseCase``.
* ``tool_broker.py``       -- authorises tool calls and produces ``AgentAudit``.
* ``citation.py``          -- produces tamper-evident ``Citation`` objects.
* ``rag.py``               -- retrieves grounded source chunks; produces citations.
* ``classifier.py``        -- produces ``ClassificationRecord``.
* ``evaluation.py``        -- produces ``EvaluationReport``.
* ``human_review.py``      -- produces ``ReviewDecision`` and ``PromotedRecord``.
* ``change_intelligence.py``-- produces ``ChangeCandidate`` proposals.
 
None of those records bind a single AI action into one ledger entry.  This
module is the missing link.
 
What this module adds
---------------------
:class:`InvocationEvidenceRecord`
    The immutable, tamper-evident record of one complete AI action.  It
    carries:
 
    * Unique identifiers (``ai_invocation_id``, ``trace_id``).
    * The AI release manifest reference (``Provenance.ai_train_version``).
    * The caller security context (redacted ``Provenance`` fields).
    * SHA-256 hashes of the raw input and output -- *never* the raw bytes
      (§21 rule 1: raw sensitive input is not copied into the record).
    * Retrieval references -- ``citation_id`` values from RAG / citation
      modules (no raw retrieved text).
    * Provider, model, and prompt version fingerprints from ``Provenance``.
    * Embedded ``ToolCallRecord`` snapshots of every ``AgentAudit`` entry.
    * The A0-A5 authority outcome from ``Provenance``.
    * The §21 invocation outcome (``PROPOSE / PRIORITIZE / AUTO_ACCEPT /
      REVIEW / ABSTAIN / BLOCK``) -- a distinct vocabulary from A0-A5.
    * Guardrail results -- one :class:`GuardrailResult` per governance or
      tool-broker check.
    * An optional ``review_ref`` linking to a ``ReviewDecision.decision_id``.
    * Usage metrics (latency, tokens, cost).
    * A UTC ``recorded_at`` timestamp.
 
:class:`InvocationOutcome`
    The six outcome values defined in §21.  Kept separate from
    ``AuthorityOutcome`` (A0-A5) because A0-A5 describes the *permission
    ceiling* granted to a use case, while :class:`InvocationOutcome`
    describes *what actually happened* during a specific invocation.  The
    mapping is provided by :meth:`InvocationOutcome.from_authority_outcome`
    for callers that derive the outcome from the governed context.
 
:class:`GuardrailResult`
    A frozen record of one governance or tool-broker check.  Each refusal
    code from ``governance.Refusal`` or ``tool_broker.Refusal`` becomes one
    :class:`GuardrailResult` in the record.
 
:class:`ToolCallRecord`
    A frozen snapshot of one ``AgentAudit`` entry, extracted from
    ``tool_broker.AgentAudit``.  Raw payload bytes are never embedded; only
    the ``payload_hash`` from ``AgentAudit`` is carried forward.
 
:class:`UsageMetrics`
    Latency, input-token count, output-token count, and monetary cost for
    one invocation.
 
:class:`InvocationEvidenceBuilder`
    Mutable builder that accumulates optional fields incrementally and then
    calls :meth:`build` to produce the frozen :class:`InvocationEvidenceRecord`.
    The builder is the *only* construction path; the record dataclass
    constructor is intentionally not public API.
 
:class:`InvocationEvidenceError`
    Raised when the record cannot be built -- e.g. a required field is
    missing, ``AUTO_ACCEPT`` is attempted above A2, or ``review_ref`` is
    supplied with a non-REVIEW outcome.
 
Design rules (all enforced here; no exceptions, no overrides)
-------------------------------------------------------------
1. **Raw bytes are never stored.**  :meth:`InvocationEvidenceBuilder.set_input`
   and :meth:`InvocationEvidenceBuilder.set_output` accept ``bytes`` and
   immediately compute the SHA-256 hex digest; the raw bytes are not held
   anywhere on the builder.
2. **The record is frozen.**  ``InvocationEvidenceRecord`` uses
   ``frozen=True``; no field may be mutated after :meth:`build` returns.
3. **``review_ref`` requires ``REVIEW`` outcome.**  Supplying a review
   decision reference when the outcome is anything other than ``REVIEW``
   is a schema violation.
4. **``AUTO_ACCEPT`` requires A0-A2.**  An invocation claiming to have
   automatically accepted its own output at A3+ authority is refused; A3+
   actions require the human-review gate.
5. **No fiscal imports.**  ADR-0006 §2.6 isolation is maintained; this
   module imports nothing from the fiscal, tax_decision, or subledger
   packages.
"""
 
from __future__ import annotations
 
import hashlib
import uuid
from dataclasses import dataclass
from datetime import UTC, datetime
from enum import StrEnum
from typing import Final
 
from .governance import UseCaseRegistry, authorise
from .provenance import AuthorityOutcome, Provenance
 
__all__: list[str] = [
    "GuardrailResult",
    "InvocationEvidenceBuilder",
    "InvocationEvidenceError",
    "InvocationEvidenceRecord",
    "InvocationOutcome",
    "ToolCallRecord",
    "UsageMetrics",
]
 
# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------
 
# Authority outcomes that may carry AUTO_ACCEPT.  A3+ require the four-eyes
# review gate (Chapter 17 §20); AUTO_ACCEPT at A3+ is refused unconditionally.
_AUTO_ACCEPT_MAX_AUTHORITY: Final[AuthorityOutcome] = AuthorityOutcome.A2
 
_AUTHORITY_ORDER: dict[AuthorityOutcome, int] = {
    a: i for i, a in enumerate(AuthorityOutcome)
}
 
 
# ---------------------------------------------------------------------------
# InvocationOutcome  (§21 vocabulary -- separate from A0-A5 ceiling)
# ---------------------------------------------------------------------------
 
 
class InvocationOutcome(StrEnum):
    """The six invocation-level outcomes defined in Chapter 17 §21.
 
    These describe *what the invocation did*, not the authority ceiling it was
    operating under.  The A0-A5 values in
    :class:`~ztax_gateway.provenance.AuthorityOutcome` describe the *permission
    ceiling* assigned to the use case; these describe the *action actually
    taken* during one specific invocation.
 
    ``PROPOSE``      -- the invocation produced a candidate (e.g. a
                        :class:`~ztax_gateway.change_intelligence.ChangeCandidate`)
                        for downstream evaluation.  Maps naturally from A1.
    ``PRIORITIZE``   -- the invocation re-ordered or ranked existing candidates
                        without creating new content.  Maps from A2.
    ``AUTO_ACCEPT``  -- the invocation accepted its own output without a human
                        review gate.  Permissible only at A0-A2 (design rule 4).
    ``REVIEW``       -- the invocation submitted its output to the
                        :class:`~ztax_gateway.human_review.ReviewQueue` and a
                        ``review_ref`` must be supplied.
    ``ABSTAIN``      -- the invocation explicitly withheld output because its
                        confidence or citation quality fell below the policy
                        threshold.
    ``BLOCK``        -- the invocation was refused by governance or a
                        tool-broker guardrail.  The record still exists so the
                        refusal is auditable.
    """
 
    PROPOSE = "PROPOSE"
    PRIORITIZE = "PRIORITIZE"
    AUTO_ACCEPT = "AUTO_ACCEPT"
    REVIEW = "REVIEW"
    ABSTAIN = "ABSTAIN"
    BLOCK = "BLOCK"
 
    @classmethod
    def from_authority_outcome(cls, authority: AuthorityOutcome) -> InvocationOutcome:
        """Derive a *default* invocation outcome from an authority outcome.
 
        This is a *convenience mapping* for callers that do not have richer
        context.  The caller should override the result when they know more
        (e.g. a governance refusal means ``BLOCK``; an explicit review
        submission means ``REVIEW``).
 
        Mapping::
 
            A0  -> PROPOSE
            A1  -> PROPOSE
            A2  -> PRIORITIZE
            A3  -> REVIEW
            A4  -> REVIEW
            A5  -> BLOCK   (A5 is refused unconditionally by governance)
        """
        _mapping: dict[AuthorityOutcome, InvocationOutcome] = {
            AuthorityOutcome.A0: cls.PROPOSE,
            AuthorityOutcome.A1: cls.PROPOSE,
            AuthorityOutcome.A2: cls.PRIORITIZE,
            AuthorityOutcome.A3: cls.REVIEW,
            AuthorityOutcome.A4: cls.REVIEW,
            AuthorityOutcome.A5: cls.BLOCK,
        }
        return _mapping[authority]
 
 
# ---------------------------------------------------------------------------
# GuardrailResult
# ---------------------------------------------------------------------------
 
 
@dataclass(frozen=True, slots=True)
class GuardrailResult:
    """The verdict of one governance or tool-broker guardrail check.
 
    ``guardrail_id``  -- a stable identifier for the specific rule checked
                        (e.g. ``"governance.authority"``,
                        ``"broker.scope_verification"``).
    ``verdict``       -- ``"PASS"`` when the check succeeded; the refusal
                        code string (e.g. ``"AI_AUTHORITY_REFUSED"``) when
                        it failed.
    ``detail``        -- optional human-readable explanation; empty when the
                        check passed.
 
    The verdict is a plain ``str`` rather than an enum so that new refusal
    codes from ``governance.Refusal`` or ``tool_broker.Refusal`` require no
    changes here.
    """
 
    guardrail_id: str
    verdict: str
    detail: str = ""
 
    def __post_init__(self) -> None:
        if not self.guardrail_id:
            raise InvocationEvidenceError(
                "invocation_evidence: guardrail_id is required on GuardrailResult"
            )
        if not self.verdict:
            raise InvocationEvidenceError(
                f"invocation_evidence: verdict is required on GuardrailResult "
                f"for guardrail {self.guardrail_id!r}"
            )
 
    @property
    def passed(self) -> bool:
        """``True`` when the guardrail check produced no refusal."""
        return self.verdict == "PASS"
 
    def as_dict(self) -> dict[str, str]:
        """Return a log-safe representation."""
        return {
            "guardrail_id": self.guardrail_id,
            "verdict": self.verdict,
            "detail": self.detail,
        }
 
 
# ---------------------------------------------------------------------------
# ToolCallRecord
# ---------------------------------------------------------------------------
 
 
@dataclass(frozen=True, slots=True)
class ToolCallRecord:
    """An immutable snapshot of one tool-call audit entry.
 
    Derived from :class:`~ztax_gateway.tool_broker.AgentAudit`; raw payload
    bytes are *never* embedded.  Only the ``payload_hash`` (already computed
    by the tool broker) is carried forward.
 
    ``call_id``      -- mirrors ``AgentAudit.call_id``.
    ``tool_id``      -- mirrors ``AgentAudit.tool_id``.
    ``caller_id``    -- mirrors ``AgentAudit.caller_id``.
    ``timestamp``    -- mirrors ``AgentAudit.timestamp`` as an ISO-8601 string
                       for JSON serialisability.
    ``outcome``      -- mirrors ``AgentAudit.outcome`` (``"PERMITTED"`` or the
                       refusal code).
    ``payload_hash`` -- optional; mirrors ``AgentAudit.payload_hash``.
    ``reviewer_id``  -- optional; mirrors ``AgentAudit.reviewer_id``.
    """
 
    call_id: str
    tool_id: str
    caller_id: str
    timestamp: str  # ISO-8601
    outcome: str
 
    payload_hash: str | None = None
    reviewer_id: str | None = None
 
    def __post_init__(self) -> None:
        if not self.call_id:
            raise InvocationEvidenceError(
                "invocation_evidence: call_id is required on ToolCallRecord"
            )
        if not self.tool_id:
            raise InvocationEvidenceError(
                f"invocation_evidence: tool_id is required on ToolCallRecord "
                f"for call {self.call_id!r}"
            )
        if not self.outcome:
            raise InvocationEvidenceError(
                f"invocation_evidence: outcome is required on ToolCallRecord "
                f"for call {self.call_id!r}"
            )
 
    def as_dict(self) -> dict[str, str]:
        """Return a log-safe representation."""
        d: dict[str, str] = {
            "call_id": self.call_id,
            "tool_id": self.tool_id,
            "caller_id": self.caller_id,
            "timestamp": self.timestamp,
            "outcome": self.outcome,
        }
        if self.payload_hash:
            d["payload_hash"] = self.payload_hash
        if self.reviewer_id:
            d["reviewer_id"] = self.reviewer_id
        return d
 
 
# ---------------------------------------------------------------------------
# UsageMetrics
# ---------------------------------------------------------------------------
 
 
@dataclass(frozen=True, slots=True)
class UsageMetrics:
    """Resource-consumption counters for one invocation.
 
    All numeric fields default to zero / 0.0 so that partial population is
    safe when provider telemetry is not yet available.
 
    ``latency_ms``        -- wall-clock latency in milliseconds.
    ``input_tokens``      -- number of input tokens consumed by the model.
    ``output_tokens``     -- number of output tokens produced by the model.
    ``monetary_cost_usd`` -- estimated monetary cost in US dollars.
    """
 
    latency_ms: int = 0
    input_tokens: int = 0
    output_tokens: int = 0
    monetary_cost_usd: float = 0.0
 
    def __post_init__(self) -> None:
        if self.latency_ms < 0:
            raise InvocationEvidenceError(
                f"invocation_evidence: latency_ms must be >= 0, "
                f"got {self.latency_ms!r}"
            )
        if self.input_tokens < 0:
            raise InvocationEvidenceError(
                f"invocation_evidence: input_tokens must be >= 0, "
                f"got {self.input_tokens!r}"
            )
        if self.output_tokens < 0:
            raise InvocationEvidenceError(
                f"invocation_evidence: output_tokens must be >= 0, "
                f"got {self.output_tokens!r}"
            )
        if self.monetary_cost_usd < 0.0:
            raise InvocationEvidenceError(
                f"invocation_evidence: monetary_cost_usd must be >= 0.0, "
                f"got {self.monetary_cost_usd!r}"
            )
 
    @property
    def total_tokens(self) -> int:
        """Sum of input and output tokens."""
        return self.input_tokens + self.output_tokens
 
    def as_dict(self) -> dict[str, int | float]:
        """Return a log-safe representation."""
        return {
            "latency_ms": self.latency_ms,
            "input_tokens": self.input_tokens,
            "output_tokens": self.output_tokens,
            "total_tokens": self.total_tokens,
            "monetary_cost_usd": self.monetary_cost_usd,
        }
 
 
# ---------------------------------------------------------------------------
# InvocationEvidenceError
# ---------------------------------------------------------------------------
 
 
class InvocationEvidenceError(Exception):
    """Raised when an :class:`InvocationEvidenceRecord` cannot be built.
 
    Distinct from :class:`~ztax_gateway.governance.GovernanceRefusedError`
    and :class:`~ztax_gateway.human_review.ReviewError` so callers can handle
    schema / state violations in the evidence model separately from upstream
    refusals.
    """
 
    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason
 
 
# ---------------------------------------------------------------------------
# InvocationEvidenceRecord
# ---------------------------------------------------------------------------
 
 
def _validate_invariants(
    outcome: InvocationOutcome,
    authority: AuthorityOutcome,
    review_ref: str | None,
    output_hash: str | None,
) -> None:
    """Enforce the record-level rules (Chapter 17 §21).
 
    Called from both :meth:`InvocationEvidenceBuilder.build` and
    :meth:`InvocationEvidenceRecord.__post_init__`, so the rules hold even when
    someone constructs a record directly and skips the builder.
    """
    # Rule 3: review_ref <-> REVIEW outcome
    if outcome is InvocationOutcome.REVIEW and not review_ref:
        raise InvocationEvidenceError(
            "invocation_evidence: invocation_outcome REVIEW requires a "
            "review_ref (ReviewDecision.decision_id)"
        )
    if review_ref and outcome is not InvocationOutcome.REVIEW:
        raise InvocationEvidenceError(
            f"invocation_evidence: review_ref supplied but "
            f"invocation_outcome is {outcome.value!r}, not REVIEW"
        )
 
    # Rule 4: AUTO_ACCEPT is only permissible at A0-A2
    if (
        outcome is InvocationOutcome.AUTO_ACCEPT
        and _AUTHORITY_ORDER[authority] > _AUTHORITY_ORDER[_AUTO_ACCEPT_MAX_AUTHORITY]
    ):
        raise InvocationEvidenceError(
            f"invocation_evidence: AUTO_ACCEPT is not permitted at "
            f"authority outcome {authority.value!r} "
            f"(ceiling is {_AUTO_ACCEPT_MAX_AUTHORITY.value!r}); "
            f"A3+ actions require the human-review gate"
        )
 
    # Rule 5: a BLOCKed invocation produced no output, so it cannot carry one.
    if outcome is InvocationOutcome.BLOCK and output_hash is not None:
        raise InvocationEvidenceError(
            "invocation_evidence: a BLOCK record must not carry an output_hash "
            "-- a blocked invocation produced no output"
        )
 
 
@dataclass(frozen=True, slots=True)
class InvocationEvidenceRecord:
    """Immutable, tamper-evident record of one complete AI invocation.
 
    Chapter 17 §21 of the ZoikoTax Master Specification.
 
    Construction is via :class:`InvocationEvidenceBuilder`, which is the
    governance-gated path.  The record-level rules (review_ref <-> REVIEW,
    AUTO_ACCEPT ceiling, no output on BLOCK) are also enforced here, so a
    directly-constructed record cannot break them -- but only the builder
    checks governance.
 
    Identifiers
    -----------
    ``ai_invocation_id``            -- UUID4 hex string; unique per invocation.
    ``trace_id``                    -- caller-supplied distributed trace ID;
                                       used to correlate with the observability
                                       backend (§22).
 
    Provenance fields (from :class:`~ztax_gateway.provenance.Provenance`)
    -----------------------------------------------------------------------
    ``ai_release_manifest_id``      -- ``Provenance.ai_train_version``.  Names
                                       the exact AI train that was active.
    ``caller_security_context_ref`` -- ``Provenance.use_case``; the registered
                                       use case that authorised the crossing.
    ``region``                      -- ``Provenance.region``.
    ``provider``                    -- ``Provenance.provider_profile``.
    ``model``                       -- ``Provenance.model_profile``.
    ``prompt_version``              -- ``Provenance.prompt_profile``.
    ``authority_outcome``           -- ``Provenance.authority_outcome`` (A0-A5).
 
    Content hashes (§21 rule 1 -- raw bytes are never stored)
    ---------------------------------------------------------
    ``input_hash``   -- SHA-256 hex digest of the raw input bytes.  ``None``
                        when the input was empty or not supplied.
    ``output_hash``  -- SHA-256 hex digest of the raw output bytes.  ``None``
                        when the invocation was blocked before producing output.
 
    Retrieval references
    --------------------
    ``retrieval_refs``  -- frozenset of ``citation_id`` values from RAG /
                          citation modules.  No raw retrieved text is stored.
 
    Tool-call log
    -------------
    ``tool_calls``  -- tuple of :class:`ToolCallRecord` snapshots, one per
                      ``AgentAudit`` entry attached to this invocation.
 
    Outcome and guardrails
    ----------------------
    ``invocation_outcome`` -- one of the six :class:`InvocationOutcome` values.
    ``guardrail_results``  -- tuple of :class:`GuardrailResult` records, one
                             per governance or broker check that ran.
    ``review_ref``         -- ``ReviewDecision.decision_id`` when
                             ``invocation_outcome`` is ``REVIEW``; ``None``
                             otherwise.
 
    Usage
    -----
    ``usage`` -- :class:`UsageMetrics` (latency, tokens, cost).
 
    Audit timestamp
    ---------------
    ``recorded_at`` -- UTC timestamp at which the builder produced this record.
    """
 
    # Identifiers
    ai_invocation_id: str
    trace_id: str
 
    # Provenance
    ai_release_manifest_id: str
    caller_security_context_ref: str
    region: str
    provider: str
    model: str
    prompt_version: str
    authority_outcome: AuthorityOutcome
 
    # Content hashes (never raw bytes)
    input_hash: str | None
    output_hash: str | None
 
    # Retrieval references
    retrieval_refs: frozenset[str]
 
    # Tool-call log
    tool_calls: tuple[ToolCallRecord, ...]
 
    # Outcome and guardrails
    invocation_outcome: InvocationOutcome
    guardrail_results: tuple[GuardrailResult, ...]
    review_ref: str | None
 
    # Usage
    usage: UsageMetrics
 
    # Audit timestamp
    recorded_at: datetime
 
    def __post_init__(self) -> None:
        _validate_invariants(
            self.invocation_outcome,
            self.authority_outcome,
            self.review_ref,
            self.output_hash,
        )
 
    def as_dict(self) -> dict[str, object]:
        """Return a complete, log-safe representation.
 
        Suitable for writing to an audit ledger, an OpenTelemetry span
        attribute bag, or a structured log entry.
        """
        d: dict[str, object] = {
            "ai_invocation_id": self.ai_invocation_id,
            "trace_id": self.trace_id,
            "ai_release_manifest_id": self.ai_release_manifest_id,
            "caller_security_context_ref": self.caller_security_context_ref,
            "region": self.region,
            "provider": self.provider,
            "model": self.model,
            "prompt_version": self.prompt_version,
            "authority_outcome": self.authority_outcome.value,
            "invocation_outcome": self.invocation_outcome.value,
            "retrieval_refs": sorted(self.retrieval_refs),
            "tool_calls": [tc.as_dict() for tc in self.tool_calls],
            "guardrail_results": [gr.as_dict() for gr in self.guardrail_results],
            "usage": self.usage.as_dict(),
            "recorded_at": self.recorded_at.isoformat(),
        }
        if self.input_hash is not None:
            d["input_hash"] = self.input_hash
        if self.output_hash is not None:
            d["output_hash"] = self.output_hash
        if self.review_ref is not None:
            d["review_ref"] = self.review_ref
        return d
 
    def any_guardrail_failed(self) -> bool:
        """``True`` when at least one guardrail check did not produce ``PASS``."""
        return any(not gr.passed for gr in self.guardrail_results)
 
 
# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
 
 
def _new_invocation_id() -> str:
    """Generate a new UUID4-based ``ai_invocation_id``."""
    return uuid.uuid4().hex
 
 
def _sha256_hex(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()
 
 
# ---------------------------------------------------------------------------
# InvocationEvidenceBuilder
# ---------------------------------------------------------------------------
 
 
class InvocationEvidenceBuilder:
    """Mutable builder for :class:`InvocationEvidenceRecord`.
 
    Usage::
 
        builder = InvocationEvidenceBuilder(
            provenance=my_provenance,
            invocation_outcome=InvocationOutcome.PROPOSE,
            registry=my_registry,
            trace_id="trace-abc-123",
        )
        builder.set_input(raw_input_bytes)   # bytes are hashed; never stored
        builder.set_output(raw_output_bytes) # bytes are hashed; never stored
        builder.add_retrieval_ref("a1b2c3d4e5f6a7b8")
        builder.add_tool_call(ToolCallRecord(...))
        builder.add_guardrail(GuardrailResult("governance.authority", "PASS"))
        builder.set_usage(UsageMetrics(latency_ms=240, input_tokens=512))
        record = builder.build()
 
    Design rules
    ------------
    * :meth:`set_input` and :meth:`set_output` accept ``bytes`` and store only
      the SHA-256 hex digest -- the raw bytes are never held on the builder.
    * :meth:`build` validates all constraints before constructing the frozen
      record.  If any constraint fails, :class:`InvocationEvidenceError` is
      raised and no record is produced.
    * A builder may only be used once: calling :meth:`build` more than once
      is a programming error and raises :class:`InvocationEvidenceError`.
    """
 
    def __init__(
        self,
        provenance: Provenance,
        invocation_outcome: InvocationOutcome,
        registry: UseCaseRegistry,
        *,
        trace_id: str = "",
        ai_invocation_id: str | None = None,
    ) -> None:
        """Initialise the builder with the mandatory fields.
 
        Parameters
        ----------
        provenance:
            The governed context for this invocation.  Provides
            ``ai_release_manifest_id``, ``caller_security_context_ref``,
            ``region``, ``provider``, ``model``, ``prompt_version``, and
            ``authority_outcome``.
        invocation_outcome:
            The §21 outcome value for this invocation.
        registry:
            The :class:`~ztax_gateway.governance.UseCaseRegistry`.  Unless
            ``invocation_outcome`` is ``BLOCK``, ``provenance`` is passed to
            :func:`~ztax_gateway.governance.authorise` here, before anything
            else, and :class:`~ztax_gateway.governance.GovernanceRefusedError`
            propagates if the context is not permitted.  A ``BLOCK`` record is
            exempt: it exists to make a refusal auditable, and the refused
            context is by definition one governance would not authorise.
        trace_id:
            Distributed trace ID for correlation with the observability
            backend (§22).  May be empty when not yet available.
        ai_invocation_id:
            Override the auto-generated UUID4.  Supplied only in tests that
            need a stable, predictable identifier.
        """
        # Governance gate -- before any work, same as every other entry point.
        # BLOCK is the one outcome that records a refusal rather than an action.
        if invocation_outcome is not InvocationOutcome.BLOCK:
            authorise(registry, provenance)
 
        self._provenance = provenance
        self._invocation_outcome = invocation_outcome
        self._trace_id = trace_id
        self._ai_invocation_id = ai_invocation_id or _new_invocation_id()
 
        # Optional fields -- accumulated incrementally.
        self._input_hash: str | None = None
        self._output_hash: str | None = None
        self._retrieval_refs: set[str] = set()
        self._tool_calls: list[ToolCallRecord] = []
        self._guardrail_results: list[GuardrailResult] = []
        self._review_ref: str | None = None
        self._usage: UsageMetrics = UsageMetrics()
 
        self._built: bool = False
 
    # ---- input / output hashing ----------------------------------------
 
    def set_input(self, raw_bytes: bytes | bytearray) -> InvocationEvidenceBuilder:
        """Hash ``raw_bytes`` and store the digest.  The raw bytes are discarded.
 
        Calling this method more than once replaces the previous hash; the
        last call wins.
        """
        if not isinstance(raw_bytes, (bytes, bytearray)):
            raise InvocationEvidenceError(
                "invocation_evidence: set_input requires bytes, "
                f"got {type(raw_bytes).__name__!r}"
            )
        self._input_hash = _sha256_hex(bytes(raw_bytes))
        return self
 
    def set_output(self, raw_bytes: bytes | bytearray) -> InvocationEvidenceBuilder:
        """Hash ``raw_bytes`` and store the digest.  The raw bytes are discarded."""
        if not isinstance(raw_bytes, (bytes, bytearray)):
            raise InvocationEvidenceError(
                "invocation_evidence: set_output requires bytes, "
                f"got {type(raw_bytes).__name__!r}"
            )
        self._output_hash = _sha256_hex(bytes(raw_bytes))
        return self
 
    # ---- retrieval references ------------------------------------------
 
    def add_retrieval_ref(self, citation_id: str) -> InvocationEvidenceBuilder:
        """Add a ``citation_id`` from a RAG or citation result.
 
        Duplicate ``citation_id`` values are silently de-duplicated.
        """
        if not citation_id:
            raise InvocationEvidenceError(
                "invocation_evidence: citation_id must be non-empty in "
                "add_retrieval_ref"
            )
        self._retrieval_refs.add(citation_id)
        return self
 
    def add_retrieval_refs(
        self, citation_ids: list[str]
    ) -> InvocationEvidenceBuilder:
        """Add multiple citation IDs at once."""
        for cid in citation_ids:
            self.add_retrieval_ref(cid)
        return self
 
    # ---- tool calls ----------------------------------------------------
 
    def add_tool_call(self, record: ToolCallRecord) -> InvocationEvidenceBuilder:
        """Append one :class:`ToolCallRecord` to the log."""
        if not isinstance(record, ToolCallRecord):
            raise InvocationEvidenceError(
                "invocation_evidence: add_tool_call requires a ToolCallRecord"
            )
        self._tool_calls.append(record)
        return self
 
    # ---- guardrail results ---------------------------------------------
 
    def add_guardrail(self, result: GuardrailResult) -> InvocationEvidenceBuilder:
        """Append one :class:`GuardrailResult`."""
        if not isinstance(result, GuardrailResult):
            raise InvocationEvidenceError(
                "invocation_evidence: add_guardrail requires a GuardrailResult"
            )
        self._guardrail_results.append(result)
        return self
 
    # ---- review reference ----------------------------------------------
 
    def set_review_ref(self, decision_id: str) -> InvocationEvidenceBuilder:
        """Set the ``review_ref`` linking to a ``ReviewDecision.decision_id``.
 
        The builder does *not* enforce ``REVIEW`` outcome here; that check is
        in :meth:`build` so the builder can be assembled in any order.
        """
        if not decision_id:
            raise InvocationEvidenceError(
                "invocation_evidence: decision_id must be non-empty in "
                "set_review_ref"
            )
        self._review_ref = decision_id
        return self
 
    # ---- usage ---------------------------------------------------------
 
    def set_usage(self, usage: UsageMetrics) -> InvocationEvidenceBuilder:
        """Set the :class:`UsageMetrics` for this invocation."""
        if not isinstance(usage, UsageMetrics):
            raise InvocationEvidenceError(
                "invocation_evidence: set_usage requires a UsageMetrics instance"
            )
        self._usage = usage
        return self
 
    # ---- build ---------------------------------------------------------
 
    def build(self) -> InvocationEvidenceRecord:
        """Validate all constraints and produce the frozen record.
 
        The builder is marked used only after validation succeeds, so a
        refused ``build()`` can be corrected (for example by adding the missing
        ``review_ref``) and retried on the same builder.
 
        Raises
        ------
        InvocationEvidenceError
            * If called again after a successful ``build()``.
            * If ``invocation_outcome`` is ``REVIEW`` and ``review_ref``
              was not supplied.
            * If ``review_ref`` was supplied but ``invocation_outcome`` is
              not ``REVIEW``.
            * If ``invocation_outcome`` is ``AUTO_ACCEPT`` and the authority
              outcome exceeds A2.
            * If ``invocation_outcome`` is ``BLOCK`` and an output was set.
        """
        if self._built:
            raise InvocationEvidenceError(
                "invocation_evidence: InvocationEvidenceBuilder.build() "
                "may only be called once"
            )
 
        outcome = self._invocation_outcome
        authority = self._provenance.authority_outcome
 
        _validate_invariants(outcome, authority, self._review_ref, self._output_hash)
 
        record = InvocationEvidenceRecord(
            ai_invocation_id=self._ai_invocation_id,
            trace_id=self._trace_id,
            ai_release_manifest_id=self._provenance.ai_train_version,
            caller_security_context_ref=self._provenance.use_case,
            region=self._provenance.region,
            provider=self._provenance.provider_profile,
            model=self._provenance.model_profile,
            prompt_version=self._provenance.prompt_profile,
            authority_outcome=authority,
            input_hash=self._input_hash,
            output_hash=self._output_hash,
            retrieval_refs=frozenset(self._retrieval_refs),
            tool_calls=tuple(self._tool_calls),
            invocation_outcome=outcome,
            guardrail_results=tuple(self._guardrail_results),
            review_ref=self._review_ref,
            usage=self._usage,
            recorded_at=datetime.now(tz=UTC),
        )
        self._built = True
        return record
 