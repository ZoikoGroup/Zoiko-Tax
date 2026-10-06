"""Customer Explanation Service.

Chapter 17 �12 and �4 of the ZoikoTax Master Specification.
Requirements: ZTAX-AI-REQ-0056, ZTAX-AI-REQ-0057, ZTAX-AIGOV-REQ-0045,
              ZTAX-AIGOV-REQ-0046, ZTAX-PRD-REQ-0037.

This module produces a governed, citation-grounded, human-readable narrative
that explains *why* a committed deterministic tax decision reached the outcome
it did.  It is strictly advisory (authority A0 � Observe); it cannot alter,
correct, override or supersede the decision it describes.

Architecture
------------
The module operates entirely within the AI train (``intelligence/``).  It
consumes a :class:`DecisionTrace` � a structured description of the
deterministic decision assembled by the caller from the Go backend''s
``evidence.Result.Trace`` � and a :class:`CitationBundle` that maps every
``rule_semantic_id`` in the trace to one :class:`~ztax_gateway.citation.Citation`
covering the source legal instrument.

Key invariants
--------------
1. **Read-only**.  ZTAX-AI-REQ-0057: the explanation service must not
   modify, correct or recompute any figure in the decision.
2. **Citation-mandatory**.  ZTAX-AIGOV-REQ-0045: every narrative segment
   that references a rule step must resolve to a registered Citation.
   A step with no resolvable citation raises CitationResolutionError
   (fail-closed, per ZTAX-AIGOV-REQ-0046).
3. **Schema-distinct**.  ZTAX-DOM-REQ-0045: ExplanationResult is
   not a Decision, not a ClassificationRecord and not a ReviewItem.
4. **Authority ceiling A0**.
5. **No fiscal imports**.  ADR-0006 �2.6.
6. **Immutable outputs**.  ExplanationResult and ExplanationSegment are frozen=True.
"""

from __future__ import annotations

import uuid
from dataclasses import dataclass, field
from datetime import UTC, datetime
from typing import Final

from .citation import Citation, ProvenanceSpan
from .governance import UseCaseRegistry, authorise
from .provenance import AuthorityOutcome, Provenance

__all__: list[str] = [
    "CitationBundle",
    "CitationResolutionError",
    "CustomerExplanationService",
    "DecisionTrace",
    "ExplanationError",
    "ExplanationResult",
    "ExplanationSegment",
    "TraceStep",
    "build_explanation",
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_REQUIRED_AUTHORITY: Final[AuthorityOutcome] = AuthorityOutcome.A0
_OP_EMIT: Final[str] = "EMIT"
_OP_REFUSE: Final[str] = "REFUSE"


# ---------------------------------------------------------------------------
# Errors
# ---------------------------------------------------------------------------


class ExplanationError(Exception):
    """Raised when an explanation cannot be produced."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


class CitationResolutionError(ExplanationError):
    """Raised when a material rule step cannot be resolved to a citation.

    Per ZTAX-AIGOV-REQ-0046 this causes abstention / failure of the
    governed explanation rather than a partial result.
    """

    def __init__(self, rule_semantic_id: str) -> None:
        super().__init__(
            f"explanation: unresolvable material citation for rule "
            f"{rule_semantic_id!r} -- abstaining per ZTAX-AIGOV-REQ-0046"
        )
        self.rule_semantic_id = rule_semantic_id


# ---------------------------------------------------------------------------
# Input model
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class TraceStep:
    """One node visited during deterministic tax evaluation.

    Python mirror of rule.TraceStep from the Go backend
    (backend/internal/domain/rule/eval.go).

    Fields
    ------
    node
        The node identifier within the compiled rule bundle.
    op
        The operation code (EMIT, REFUSE, APPLY_RATE, CONST, ADD, SUB, ...).
    args
        Node identifiers this node consumed, in evaluation order.
    rule_semantic_id
        Stable cross-version identity of the rule (e.g. ZTAX-RULE-AU-GST-RATE).
        This is the key used to resolve the citation.
    rule_version
        The exact version string of the rule that produced the node.
    output
        The canonical decimal/string output value.  Reproduced verbatim;
        never recomputed here (ZTAX-AI-REQ-0057).
    policy
        Rounding policy name if applicable; empty string otherwise.
    """

    node: str
    op: str
    args: tuple[str, ...] = field(default_factory=tuple)
    rule_semantic_id: str = ""
    rule_version: str = ""
    output: str = ""
    policy: str = ""


@dataclass(frozen=True, slots=True)
class DecisionTrace:
    """The deterministic decision outcome together with its execution trace.

    Produced by the caller from app.RecordedDecision on the Go side.  The
    explanation service treats every field as immutable evidence it narrates
    but never modifies.

    Fields
    ------
    decision_id
        UUID string of the committed decision (evidence.Record.DecisionID).
    business_key
        The caller''s own stable identifier for the taxable event.
    outcome
        The outcome code (ADVISORY, AUTHORITATIVE, AMBIGUOUS, CONFLICTED,
        UNSUPPORTED, REVIEW_REQUIRED).
    reason_code
        Machine-readable reason code from evidence.Result.Reason.
    event_time
        The event timestamp as ISO-8601 (UTC, six-digit precision).
    emitted
        Mapping from result slot name to canonical value string.
        Reproduced verbatim; no arithmetic performed here.
    steps
        Ordered TraceStep objects in evaluation order.
    bundle_id
        The content bundle identifier that produced this decision.
    bundle_digest
        The content bundle digest.
    """

    decision_id: str
    business_key: str
    outcome: str
    reason_code: str
    event_time: str
    emitted: dict[str, str]
    steps: tuple[TraceStep, ...]
    bundle_id: str = ""
    bundle_digest: str = ""


# ---------------------------------------------------------------------------
# Citation bundle
# ---------------------------------------------------------------------------


class CitationBundle:
    """Maps rule semantic identifiers to their authoritative Citation.

    Built by the caller from content-plane information (content.RuleVersion ->
    content.SourceArtifact -> legal instrument).  Read-only after per-step
    additions via add().

    CitationBundle is not a registry: it is per-request, covers exactly
    the rules in one trace, and is read-only after construction.
    """

    def __init__(self) -> None:
        self._citations: dict[str, Citation] = {}

    def add(self, rule_semantic_id: str, citation: Citation) -> None:
        """Register citation as the authority for rule_semantic_id."""
        if not rule_semantic_id:
            raise ExplanationError("citation bundle: empty rule_semantic_id")
        if rule_semantic_id in self._citations:
            raise ExplanationError(
                f"citation bundle: {rule_semantic_id!r} already registered"
            )
        self._citations[rule_semantic_id] = citation

    def resolve(self, rule_semantic_id: str) -> Citation | None:
        """Return the citation, or None if the rule is not in the bundle."""
        return self._citations.get(rule_semantic_id)

    def ids(self) -> list[str]:
        return sorted(self._citations)

    def __len__(self) -> int:
        return len(self._citations)


# ---------------------------------------------------------------------------
# Output model
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class ExplanationSegment:
    """One narrative segment in the explanation.

    Corresponds to one EMIT or REFUSE step in the execution trace with the
    citation that grounded it.  Frozen; immutable after creation.

    Fields
    ------
    segment_id      Unique identifier for this segment within the explanation.
    rule_semantic_id  The rule whose output this segment narrates.
    rule_version    Exact version of that rule.
    op              Operation code (EMIT or REFUSE for narrated steps).
    output          Canonical output value reproduced verbatim from the trace.
    narrative       Human-readable explanation assembled from the deterministic
                    trace and the citation text; no recomputation of amounts.
    citation_id     The citation ID from the Citation that grounded this step.
    source_ids      Frozenset of source document identifiers from the citation.
    """

    segment_id: str
    rule_semantic_id: str
    rule_version: str
    op: str
    output: str
    narrative: str
    citation_id: str
    source_ids: frozenset[str]


@dataclass(frozen=True, slots=True)
class ExplanationResult:
    """The governed, citation-grounded explanation of a deterministic decision.

    Schema-distinct advisory output per ZTAX-DOM-REQ-0045.
    Not a Decision, not a ClassificationRecord, not a ReviewItem.

    Fields
    ------
    explanation_id      Unique identifier for this explanation run.
    decision_id         The decision this explanation describes.
    business_key        The caller''s stable identifier for the taxable event.
    outcome             Deterministic outcome reproduced from the trace (never altered).
    reason_code         Deterministic reason code reproduced from the trace.
    emitted_summary     Human-readable rendering of the emitted result slots.
    segments            Ordered narrative segments, one per narrated trace step.
    provenance_spans    One ProvenanceSpan per segment.
    authority_outcome   Always A0 -- the explanation is an observation.
    generated_at        UTC timestamp of generation.
    """

    explanation_id: str
    decision_id: str
    business_key: str
    outcome: str
    reason_code: str
    emitted_summary: str
    segments: tuple[ExplanationSegment, ...]
    provenance_spans: tuple[ProvenanceSpan, ...]
    authority_outcome: AuthorityOutcome
    generated_at: datetime


# ---------------------------------------------------------------------------
# Service
# ---------------------------------------------------------------------------


class CustomerExplanationService:
    """Governed service that produces ExplanationResult objects.

    The constructor validates that the provenance carries exactly
    AuthorityOutcome.A0 -- enforcing the ZTAX-AI-REQ-0056 ceiling at
    wiring time rather than per-call.

    Usage
    -----
    service = CustomerExplanationService(
        registry=use_case_registry,
        provenance=Provenance(
            use_case="explanation",
            authority_outcome=AuthorityOutcome.A0,
            ...
        ),
    )
    result = service.explain(trace=trace, citations=bundle)
    """

    def __init__(
        self,
        registry: UseCaseRegistry,
        provenance: Provenance,
    ) -> None:
        if provenance.authority_outcome != _REQUIRED_AUTHORITY:
            raise ExplanationError(
                f"explanation service: authority must be {_REQUIRED_AUTHORITY.value}, "
                f"got {provenance.authority_outcome.value} -- the explanation service "
                f"is read-only (ZTAX-AI-REQ-0056)"
            )
        self._registry = registry
        self._provenance = provenance

    def explain(
        self,
        trace: DecisionTrace,
        citations: CitationBundle,
    ) -> ExplanationResult:
        """Produce a governed explanation for trace.

        Steps
        -----
        1. Authorise the provenance against the registry (governance gate).
        2. Validate the trace has at least one step.
        3. For every EMIT or REFUSE step, resolve its citation -- fail-closed if
           any material step has no resolvable citation (ZTAX-AIGOV-REQ-0046).
        4. Build narrative segments.
        5. Build provenance spans.
        6. Assemble and return the frozen ExplanationResult.

        Raises
        ------
        GovernanceRefusedError
            If the provenance fails the governance gate.
        ExplanationError
            If the trace is empty or otherwise malformed.
        CitationResolutionError
            If any material step (EMIT or REFUSE) cannot be resolved to a
            citation (fail-closed per ZTAX-AIGOV-REQ-0046).
        """
        # Step 1: governance gate.
        authorise(self._registry, self._provenance)

        # Step 2: trace must have steps.
        if not trace.steps:
            raise ExplanationError(
                f"explanation: decision {trace.decision_id!r} has an empty trace"
            )

        # Step 3 & 4: identify material steps, resolve citations, build segments.
        segments: list[ExplanationSegment] = []
        provenance_spans: list[ProvenanceSpan] = []
        explanation_id = str(uuid.uuid4())

        for step in trace.steps:
            if step.op not in (_OP_EMIT, _OP_REFUSE):
                continue

            if not step.rule_semantic_id:
                raise ExplanationError(
                    f"explanation: trace step {step.node!r} op={step.op!r} "
                    f"has no rule_semantic_id -- trace is incomplete"
                )

            # Resolve citation -- fail closed (ZTAX-AIGOV-REQ-0046).
            citation = citations.resolve(step.rule_semantic_id)
            if citation is None:
                raise CitationResolutionError(step.rule_semantic_id)

            # Verify citation integrity.
            if not citation.verify_all():
                raise ExplanationError(
                    f"explanation: citation for {step.rule_semantic_id!r} "
                    f"(citation_id={citation.citation_id}) failed tamper check"
                )

            narrative = _narrate_step(step, trace)

            segment_id = str(uuid.uuid4())
            segment = ExplanationSegment(
                segment_id=segment_id,
                rule_semantic_id=step.rule_semantic_id,
                rule_version=step.rule_version,
                op=step.op,
                output=step.output,
                narrative=narrative,
                citation_id=citation.citation_id,
                source_ids=citation.source_ids(),
            )
            segments.append(segment)

            span = ProvenanceSpan(
                output_id=segment_id,
                citation=citation,
            )
            provenance_spans.append(span)

        if not segments:
            raise ExplanationError(
                f"explanation: decision {trace.decision_id!r} trace has no "
                f"EMIT or REFUSE steps -- nothing to narrate"
            )

        emitted_summary = _render_emitted(trace.emitted)

        return ExplanationResult(
            explanation_id=explanation_id,
            decision_id=trace.decision_id,
            business_key=trace.business_key,
            outcome=trace.outcome,
            reason_code=trace.reason_code,
            emitted_summary=emitted_summary,
            segments=tuple(segments),
            provenance_spans=tuple(provenance_spans),
            authority_outcome=_REQUIRED_AUTHORITY,
            generated_at=datetime.now(UTC),
        )


# ---------------------------------------------------------------------------
# Module-level convenience function
# ---------------------------------------------------------------------------


def build_explanation(
    trace: DecisionTrace,
    citations: CitationBundle,
    registry: UseCaseRegistry,
    provenance: Provenance,
) -> ExplanationResult:
    """One-shot explanation generation.

    Convenience wrapper around CustomerExplanationService for callers
    that do not need to hold a service instance across multiple calls.
    """
    service = CustomerExplanationService(registry=registry, provenance=provenance)
    return service.explain(trace=trace, citations=citations)


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------


def _narrate_step(step: TraceStep, trace: DecisionTrace) -> str:
    """Produce a deterministic narrative string for one material trace step.

    ZTAX-AI-REQ-0057: amounts are reproduced verbatim from the step;
    no arithmetic is performed.
    """
    rule_label = step.rule_semantic_id or step.node

    if step.op == _OP_EMIT:
        slot_label = step.node if step.node else rule_label
        return (
            f"Rule {rule_label!r} (version {step.rule_version!r}) "
            f"produced result slot {slot_label!r} with value {step.output!r}. "
            f"This is the deterministic output of the tax engine; "
            f"no figure has been altered by this explanation."
        )

    if step.op == _OP_REFUSE:
        return (
            f"Rule {rule_label!r} (version {step.rule_version!r}) "
            f"declined to produce a figure. "
            f"Reason code: {trace.reason_code!r}. "
            f"Outcome: {trace.outcome!r}. "
            f"This refusal is recorded as a decision with evidence "
            f"(not an error) per the ZoikoTax determination model."
        )

    return (
        f"Rule {rule_label!r} (version {step.rule_version!r}) "
        f"executed operation {step.op!r} producing {step.output!r}."
    )


def _render_emitted(emitted: dict[str, str]) -> str:
    """Render emitted result slots as a human-readable summary string.

    Amounts are reproduced verbatim; no arithmetic.
    """
    if not emitted:
        return "(no figures emitted)"
    return " | ".join(f"{slot}: {value}" for slot, value in sorted(emitted.items()))
