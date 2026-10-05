"""Evaluation Evidence Record.

Chapter 17 §19 of the ZoikoTax Master Specification.

This module is the **final immutable artefact** that completes the §19
Evaluation Service pipeline.  Every other §19 primitive produces an
intermediate result that feeds into this one::

    GoldSetStore
        └─► EvaluationRunner ──────► RunReport ────────────────────┐
                                     RulesetRunReport               │
    MetricEngine / ModelComparator ──► ComparisonReport ───────────┤
    ReleaseGate ─────────────────────► gate verdict ───────────────┤
                                                                   ▼
                                                   EvaluationEvidenceRecord
                                                   (sealed, governance-gated)

One :class:`EvaluationEvidenceRecord` is created per evaluation event.  It
carries:

* The **release manifest ID** and **gold-set version** that scoped the run.
* The **metric report** (mandatory).
* The **comparison report** (optional).
* The **gate verdict** -- a :class:`GateVerdict` enum value.
* The **gate failures** -- adversarial cases that did not pass.
* **Provenance metadata** -- use_case, model_profile, prompt_profile, timestamp.

Design rules
------------
* No live model calls.
* Governance gate on construction via :func:`build`.
* Immutable once built (frozen=True dataclass).
* No fiscal imports (ADR-0006 §2.6).
"""

from __future__ import annotations

import uuid
from dataclasses import dataclass
from datetime import UTC, datetime
from enum import StrEnum

from .evaluation_adversarial import CaseResult as AdversarialCaseResult
from .evaluation_adversarial import CaseVerdict
from .evaluation_quality import ComparisonReport, MetricReport
from .governance import UseCaseRegistry, authorise
from .production_registries import AIReleaseManifest, ManifestRegistry
from .provenance import Provenance

__all__: list[str] = [
    "EvaluationEvidenceError",
    "EvaluationEvidenceRecord",
    "GateVerdict",
    "build",
]


# ---------------------------------------------------------------------------
# Enumerations
# ---------------------------------------------------------------------------


class GateVerdict(StrEnum):
    """The high-level verdict from the adversarial release gate.

    ``PASS``    -- every case produced its expected refusal code.
    ``FAIL``    -- one or more cases did not produce their expected refusal.
    ``SKIPPED`` -- the gate was not run; gate_failures is empty.
                   Downstream CI must treat SKIPPED as a blocker for A3
                   promotion.
    """

    PASS = "PASS"
    FAIL = "FAIL"
    SKIPPED = "SKIPPED"


# ---------------------------------------------------------------------------
# Errors
# ---------------------------------------------------------------------------


class EvaluationEvidenceError(Exception):
    """Raised when an :class:`EvaluationEvidenceRecord` cannot be built."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------------------
# Evidence Record
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class EvaluationEvidenceRecord:
    """One immutable evaluation evidence artefact (Chapter 17 §19).

    Attributes
    ----------
    evidence_id:
        Short unique identifier (hex prefix of a UUID4).
    release_manifest_id:
        Opaque ID referencing the AI Release Manifest active during the run.
    gold_set_name:
        Mirrors GoldSetStore.name.
    gold_set_version:
        Mirrors GoldSetStore.version.
    metric_report:
        The aggregate metric artefact. Always present.
    comparison_report:
        The comparator artefact; None when no comparison was run.
    gate_verdict:
        High-level verdict from the adversarial release gate.
    gate_failures:
        Adversarial cases that did not pass. Empty when PASS or SKIPPED.
    use_case:
        Mirrors Provenance.use_case.
    model_profile:
        Mirrors Provenance.model_profile.
    prompt_profile:
        Mirrors Provenance.prompt_profile.
    sealed_at:
        UTC timestamp when build() constructed this record.
    """

    evidence_id: str
    release_manifest_id: str
    gold_set_name: str
    gold_set_version: str
    metric_report: MetricReport
    comparison_report: ComparisonReport | None
    gate_verdict: GateVerdict
    gate_failures: tuple[AdversarialCaseResult, ...]
    use_case: str
    model_profile: str
    prompt_profile: str
    sealed_at: datetime
    # Resolved at build() time when a ManifestRegistry is provided.  None
    # when build() is called without a manifest_registry (permitted for
    # offline / test scenarios where no registry exists yet).
    resolved_manifest: AIReleaseManifest | None = None

    @property
    def passed(self) -> bool:
        """True iff the gate verdict is PASS."""
        return self.gate_verdict is GateVerdict.PASS

    @property
    def gate_failure_count(self) -> int:
        """Number of adversarial cases that failed the gate."""
        return len(self.gate_failures)

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe, JSON-serialisable representation."""
        gate_failures_dicts: list[dict[str, object]] = [
            {
                "case_id": f.case_id,
                "verdict": f.verdict.value,
                "expected_refusal": repr(f.expected_refusal),
                "actual_refusal": repr(f.actual_refusal),
                "detail": f.detail,
            }
            for f in self.gate_failures
        ]
        return {
            "evidence_id": self.evidence_id,
            "release_manifest_id": self.release_manifest_id,
            "resolved_manifest_hash": (
                self.resolved_manifest.manifest_hash
                if self.resolved_manifest is not None
                else None
            ),
            "gold_set_name": self.gold_set_name,
            "gold_set_version": self.gold_set_version,
            "metric_report": self.metric_report.as_dict(),
            "comparison_report": (
                self.comparison_report.as_dict()
                if self.comparison_report is not None
                else None
            ),
            "gate_verdict": self.gate_verdict.value,
            "gate_failure_count": self.gate_failure_count,
            "gate_failures": gate_failures_dicts,
            "use_case": self.use_case,
            "model_profile": self.model_profile,
            "prompt_profile": self.prompt_profile,
            "sealed_at": self.sealed_at.isoformat(),
        }


# ---------------------------------------------------------------------------
# Factory function
# ---------------------------------------------------------------------------


def build(
    *,
    release_manifest_id: str,
    gold_set_name: str,
    gold_set_version: str,
    metric_report: MetricReport,
    registry: UseCaseRegistry,
    provenance: Provenance,
    manifest_registry: ManifestRegistry | None = None,
    comparison_report: ComparisonReport | None = None,
    gate_results: list[AdversarialCaseResult] | None = None,
) -> EvaluationEvidenceRecord:
    """Seal an :class:`EvaluationEvidenceRecord` under governance.

    Parameters
    ----------
    release_manifest_id:
        Non-empty identifier for the AI Release Manifest in scope.  When
        *manifest_registry* is supplied, this ID must resolve to an ACTIVE
        manifest in that registry -- a revoked, superseded, or unknown ID is
        refused before the record is sealed.
    gold_set_name:
        GoldSetStore.name used in the run.
    gold_set_version:
        GoldSetStore.version used in the run.
    metric_report:
        The MetricReport produced by the evaluation run.
    registry:
        UseCaseRegistry for the governance gate.
    provenance:
        Governance context (use case, model, region, authority level).
    manifest_registry:
        Optional ManifestRegistry.  When provided, the *release_manifest_id*
        is resolved via ``manifest_registry.get()`` -- itself a governed
        action -- so a dangling, revoked, or superseded ID raises
        ``ManifestRegistryError`` before the record is sealed.  Pass
        ``None`` only for offline / test scenarios where no registry exists.
    comparison_report:
        Optional ComparisonReport from ModelComparator.
    gate_results:
        Optional list of AdversarialCaseResult from ReleaseGate / Runner.
        None or empty list records SKIPPED.

    Returns
    -------
    EvaluationEvidenceRecord
        The sealed, immutable evidence artefact.

    Raises
    ------
    GovernanceRefusedError
        If provenance is not authorised by registry.
    EvaluationEvidenceError
        If any mandatory string field is empty.
    ManifestRegistryError
        If manifest_registry is provided and release_manifest_id is unknown,
        revoked, or superseded.
    """
    # 1. Governance gate -- mandatory P0.  No work before this.
    authorise(registry, provenance)

    # 2. Validate mandatory string fields.
    if not release_manifest_id:
        raise EvaluationEvidenceError(
            "evaluation-evidence: release_manifest_id must not be empty"
        )
    if not gold_set_name:
        raise EvaluationEvidenceError(
            "evaluation-evidence: gold_set_name must not be empty"
        )
    if not gold_set_version:
        raise EvaluationEvidenceError(
            "evaluation-evidence: gold_set_version must not be empty"
        )

    # 3. Resolve the manifest ID against the registry when one is provided.
    #    ManifestRegistry.get() is itself governance-gated (defence in depth)
    #    and raises ManifestRegistryError for unknown / revoked / superseded IDs.
    resolved: AIReleaseManifest | None = None
    if manifest_registry is not None:
        resolved = manifest_registry.get(release_manifest_id, registry, provenance)

    # 4. Derive gate verdict.
    verdict, failures = _derive_gate_verdict(gate_results)

    # 5. Seal the record.
    return EvaluationEvidenceRecord(
        evidence_id=uuid.uuid4().hex[:16],
        release_manifest_id=release_manifest_id,
        gold_set_name=gold_set_name,
        gold_set_version=gold_set_version,
        metric_report=metric_report,
        comparison_report=comparison_report,
        gate_verdict=verdict,
        gate_failures=failures,
        use_case=provenance.use_case,
        model_profile=provenance.model_profile,
        prompt_profile=provenance.prompt_profile,
        sealed_at=datetime.now(tz=UTC),
        resolved_manifest=resolved,
    )


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------


def _derive_gate_verdict(
    gate_results: list[AdversarialCaseResult] | None,
) -> tuple[GateVerdict, tuple[AdversarialCaseResult, ...]]:
    """Derive GateVerdict and the tuple of failing cases.

    Returns (SKIPPED, ()) when gate_results is None or empty.
    Returns (PASS, ()) when every case is EXPECTED_REFUSAL.
    Returns (FAIL, <failing_cases>) otherwise.
    """
    if not gate_results:
        return GateVerdict.SKIPPED, ()

    failures = tuple(
        r for r in gate_results if r.verdict is not CaseVerdict.EXPECTED_REFUSAL
    )
    if failures:
        return GateVerdict.FAIL, failures

    return GateVerdict.PASS, ()
