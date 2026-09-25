"""SKU / Ontology Classifier.

Chapter 17 §15 of the ZoikoTax Master Specification.

Every telecom product that enters the determination pipeline must be assigned
an ontology class before tax components are selected.  This module provides
that classification step inside the AI plane:

1. **Propose** — given a raw SKU description, retrieve the top-k ontology
   candidates from the :class:`~ztax_gateway.rag.KnowledgeBase` and rank them
   by BM25 score.  Each candidate carries a :class:`~ztax_gateway.citation.Citation`
   that traces the proposal back to the exact chunk of the ontology spec that
   supported it.

2. **Gate** — the caller supplies a :class:`~ztax_gateway.provenance.Provenance`;
   the classifier refuses to run if it has not been authorised through
   :func:`~ztax_gateway.governance.authorise`.  Classification is an AI action
   and must travel on a governed context.

3. **Record** — every call produces a :class:`ClassificationRecord` that can be
   written to the :class:`~ztax_gateway.tool_broker.AgentAudit` log without
   further transformation.

Design rules
------------
* **No tax logic here.** The classifier proposes; the Go fiscal core decides.
  A ``ClassificationProposal`` with ``authority_outcome < A3`` is advisory only
  (ADR-0006 §2.5: the fiscal boundary applies in both directions).
* **Citation-mandatory.** A proposal with no citation is a programming error,
  not a runtime condition.  The constructor refuses to create one.
* **Immutable proposals.** ``frozen=True`` on every data class — the same
  reason ``ToolProvenance`` and ``Provenance`` are frozen: nothing downstream
  may widen its own justification after the fact.
* **No embeddings.** BM25 over the spec corpus is sufficient for W2 lane L.
  The upgrade path to embedding-based retrieval is additive (new ``KnowledgeBase``
  subclass; the ``Classifier`` contract does not change).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Final

from .citation import Citation
from .governance import UseCaseRegistry, authorise
from .provenance import Provenance
from .rag import KnowledgeBase, SearchResult

__all__: list[str] = [
    "ClassificationError",
    "ClassificationProposal",
    "ClassificationRecord",
    "Classifier",
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# Default number of ontology candidates returned per call.
_DEFAULT_TOP_K: Final[int] = 3

# Minimum SKU description length — single-word queries produce meaningless
# rankings against a legal/technical corpus.
_MIN_DESCRIPTION_LEN: Final[int] = 3


# ---------------------------------------------------------------------------
# Errors
# ---------------------------------------------------------------------------


class ClassificationError(Exception):
    """Raised when classification cannot proceed.

    Distinct from :class:`~ztax_gateway.governance.GovernanceRefusedError` so
    that callers can handle governance refusals (kill-switch, unknown use case,
    residency) separately from classifier-specific precondition failures
    (description too short, knowledge base not built, etc.).
    """

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------------------
# Data structures
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class ClassificationProposal:
    """One ontology candidate returned by the classifier.

    ``ontology_class`` is the heading of the matched spec chunk — the closest
    approximation to an ontology class name available from a BM25 retrieval
    over Markdown headings.

    ``citation`` is the tamper-evident reference to the spec chunk that
    supported this proposal.  It is **mandatory** — a proposal with no
    citation would be an AI output with no ground truth, which this system
    explicitly disallows.

    ``rank`` is the raw BM25 score from SQLite FTS5 (lower is better).  It is
    stored for transparency and for downstream re-ranking; the classifier does
    not use it to gate anything.

    ``confidence`` is an optional [0.0, 1.0] score the caller may supply after
    further evaluation.  It is not used by the classifier itself.
    """

    ontology_class: str
    citation: Citation
    rank: float
    confidence: float | None = None

    def __post_init__(self) -> None:
        if self.confidence is not None and not (0.0 <= self.confidence <= 1.0):
            raise ClassificationError(
                f"classifier: confidence must be in [0.0, 1.0], got {self.confidence!r}"
            )


@dataclass(frozen=True, slots=True)
class ClassificationRecord:
    """The full audit record for one classification call.

    This is the unit that ends up in the :class:`~ztax_gateway.tool_broker.AgentAudit`
    log.  Every field is required — a record with a gap cannot be replayed.

    ``sku_description`` is stored verbatim so that an auditor can see exactly
    what was classified.

    ``proposals`` is the ranked list of candidates, best-first.  It may be
    empty if the knowledge base returned no matches (the caller must handle
    that case; the classifier does not refuse on empty results).

    ``provenance`` is the governed context under which the call was made.
    """

    sku_description: str
    proposals: tuple[ClassificationProposal, ...]
    provenance: Provenance

    def top(self) -> ClassificationProposal | None:
        """Return the highest-ranked proposal, or ``None`` if there are none."""
        return self.proposals[0] if self.proposals else None

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation suitable for the audit trail."""
        top = self.top()
        return {
            "sku_description": self.sku_description,
            "proposal_count": len(self.proposals),
            "top_class": top.ontology_class if top is not None else None,
            "top_citation_id": top.citation.citation_id if top is not None else None,
            "use_case": self.provenance.use_case,
            "region": self.provenance.region,
            "risk_tier": self.provenance.risk_tier.value,
            "authority_outcome": self.provenance.authority_outcome.value,
        }


# ---------------------------------------------------------------------------
# Classifier
# ---------------------------------------------------------------------------


@dataclass
class Classifier:
    """Governed SKU-to-ontology-class classifier.

    Wraps a :class:`~ztax_gateway.rag.KnowledgeBase` with a governance gate:
    every call to :meth:`classify` must carry a :class:`~ztax_gateway.provenance.Provenance`
    that passes :func:`~ztax_gateway.governance.authorise`.  No classification
    runs without a valid, registered, non-killed use case.

    Example::

        from pathlib import Path
        from ztax_gateway.rag import KnowledgeBase
        from ztax_gateway.classifier import Classifier
        from ztax_gateway.governance import UseCaseRegistry, UseCase
        from ztax_gateway.provenance import Provenance, RiskTier, AuthorityOutcome

        kb = KnowledgeBase()
        kb.build([Path("docs/specs/ZTAX-DET-001-global-tax-determination-engine.md")])

        classifier = Classifier(knowledge_base=kb)

        registry = UseCaseRegistry([UseCase(
            use_case_id="sku-classification",
            owner="lane-l",
            description="Classify telecom SKUs against the ZoikoTax ontology",
            max_risk_tier=RiskTier.T2,
            max_authority=AuthorityOutcome.A2,
            permitted_regions=frozenset({"eu-west-1"}),
        )])

        record = classifier.classify(
            sku_description="International roaming data bundle prepaid SIM",
            provenance=provenance,
            registry=registry,
        )
        print(record.top().ontology_class)
    """

    knowledge_base: KnowledgeBase = field(default_factory=KnowledgeBase)

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def classify(
        self,
        sku_description: str,
        provenance: Provenance,
        registry: UseCaseRegistry,
        top_k: int = _DEFAULT_TOP_K,
    ) -> ClassificationRecord:
        """Propose ontology classes for *sku_description*.

        Governance gate: *provenance* is passed to
        :func:`~ztax_gateway.governance.authorise` before any retrieval
        happens.  If the call is not permitted, the governance error
        propagates — classification never starts.

        Args:
            sku_description: The raw product description or SKU string to
                classify.  Must be at least ``_MIN_DESCRIPTION_LEN`` characters
                after stripping.
            provenance: The governance context.  Must be a registered,
                non-killed use case with sufficient authority.
            registry: The :class:`~ztax_gateway.governance.UseCaseRegistry`
                that holds the use-case registrations and kill switches.
            top_k: Maximum number of candidates to return.  Defaults to 3.

        Returns:
            A :class:`ClassificationRecord` with the ranked proposals.

        Raises:
            GovernanceRefusedError: if *provenance* is not authorised.
            ClassificationError: if *sku_description* is too short or the
                knowledge base has not been built.
        """
        # 1. Governance gate — before any work.
        authorise(registry, provenance)

        # 2. Validate the description.
        sku_description = sku_description.strip()
        if len(sku_description) < _MIN_DESCRIPTION_LEN:
            raise ClassificationError(
                f"classifier: SKU description {sku_description!r} is too short "
                f"(minimum {_MIN_DESCRIPTION_LEN} characters)"
            )

        # 3. Retrieve candidates from the knowledge base.
        try:
            search_results: list[SearchResult] = self.knowledge_base.search(
                sku_description, top_k=top_k
            )
        except Exception as exc:
            # Translate KnowledgeBaseError (not-built, query-too-short) into a
            # ClassificationError so callers have one error type to handle for
            # classifier-level precondition failures.
            raise ClassificationError(f"classifier: retrieval failed — {exc}") from exc

        # 4. Build proposals — each one is citation-mandatory.
        proposals = tuple(
            ClassificationProposal(
                ontology_class=result.chunk.heading,
                citation=result.citation,
                rank=result.rank,
            )
            for result in search_results
        )

        return ClassificationRecord(
            sku_description=sku_description,
            proposals=proposals,
            provenance=provenance,
        )
