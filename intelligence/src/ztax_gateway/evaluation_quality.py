"""Gold Set Store, Metric Engine, and Model Comparator.

Chapter 17 §19 of the ZoikoTax Master Specification.

This module implements the three quality-gate primitives that sit alongside the
Adversarial Harness (``evaluation_adversarial.py``) and complete the §19
Evaluation Service:

1. **Gold Set Store** -- an immutable, versioned collection of
   :class:`GoldCase` entries.  Each case pairs a raw SKU description with the
   *expected* top ontology class so the metric engine can compare what the
   classifier actually returned against the ground truth without needing a live
   model.

2. **Metric Engine** -- runs a :class:`~ztax_gateway.classifier.Classifier`
   against every case in a :class:`GoldSetStore` and accumulates per-class and
   aggregate :class:`MetricReport` statistics (exact-match accuracy, precision,
   recall per class, and a simple F1).  The engine does not call a model: it
   uses the same BM25 classifier used everywhere else in the gateway.

3. **Model Comparator** -- runs the metric engine against two
   :class:`~ztax_gateway.classifier.Classifier` instances (a *baseline* and a
   *candidate*) and emits a :class:`ComparisonReport` that states whether the
   candidate is ``IMPROVED``, ``REGRESSED``, or ``EQUIVALENT`` on aggregate
   accuracy.  The comparator enforces a minimum threshold: if the candidate's
   accuracy falls below the threshold the result is ``BELOW_THRESHOLD``
   regardless of whether it beat the baseline.

Design rules
------------
* **No live model calls.**  The metric engine drives the same
  :class:`~ztax_gateway.classifier.Classifier` the rest of the gateway uses.
  No provider, no embedding call, no HTTP request.
* **Gold set is immutable once built.**  Two stores with the same name+version
  must carry identical cases.  The constructor freezes the store after the
  cases are loaded so post-hoc mutation cannot corrupt a metric run.
* **Governance gate on every metric run.**  :meth:`MetricEngine.run` requires a
  ``(registry, provenance)`` pair; it authorises the provenance before
  touching any classifier output.
* **No fiscal imports.**  This module imports nothing from the fiscal, tax or
  subledger packages (ADR-0006 §2.6).
"""

from __future__ import annotations

import uuid
from dataclasses import dataclass, field
from datetime import UTC, datetime
from enum import StrEnum
from typing import Final

from .classifier import ClassificationRecord, Classifier
from .governance import UseCaseRegistry, authorise
from .provenance import Provenance

__all__: list[str] = [
    "ComparisonOutcome",
    "ComparisonReport",
    "GoldCase",
    "GoldSetError",
    "GoldSetStore",
    "MetricEngine",
    "MetricEngineError",
    "MetricReport",
    "ModelComparator",
    "PerClassMetric",
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# Minimum number of cases a gold set must contain before a metric run is
# allowed.  A single case is statistically meaningless and almost certainly
# a misconfiguration.
_MIN_GOLD_CASES: Final[int] = 2

# Default minimum accuracy threshold for the comparator.  A candidate whose
# accuracy falls below this is ``BELOW_THRESHOLD`` regardless of whether it
# beat the baseline.
_DEFAULT_MIN_ACCURACY: Final[float] = 0.50

# Tolerance for floating-point comparisons between baseline and candidate
# accuracy scores.  Differences smaller than this are treated as EQUIVALENT.
_EQUIVALENCE_EPSILON: Final[float] = 1e-9


# ---------------------------------------------------------------------------
# Errors
# ---------------------------------------------------------------------------


class GoldSetError(Exception):
    """Raised when a :class:`GoldSetStore` cannot be constructed or used."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


class MetricEngineError(Exception):
    """Raised when a metric run cannot proceed."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------------------
# Gold Set primitives
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class GoldCase:
    """One labelled example in a gold set."""

    case_id: str
    sku_description: str
    expected_class: str
    notes: str = ""

    def __post_init__(self) -> None:
        if not self.case_id:
            raise GoldSetError("gold-set: case has no identifier")
        if not self.sku_description.strip():
            raise GoldSetError(
                f"gold-set: case {self.case_id!r} has an empty SKU description"
            )
        if not self.expected_class.strip():
            raise GoldSetError(
                f"gold-set: case {self.case_id!r} has no expected_class"
            )

    @property
    def normalised_expected(self) -> str:
        """Return ``expected_class`` lower-cased and stripped for comparison."""
        return self.expected_class.strip().lower()


# ---------------------------------------------------------------------------
# Gold Set Store
# ---------------------------------------------------------------------------


@dataclass
class GoldSetStore:
    """An immutable, versioned collection of :class:`GoldCase` entries."""

    name: str
    version: str
    _cases: list[GoldCase] = field(default_factory=list, init=False, repr=False)
    _case_ids: set[str] = field(default_factory=set, init=False, repr=False)
    _frozen: bool = field(default=False, init=False, repr=False)

    def __init__(
        self, name: str, version: str, cases: list[GoldCase] | None = None
    ) -> None:
        if not name:
            raise GoldSetError("gold-set: store has no name")
        if not version:
            raise GoldSetError(f"gold-set: store {name!r} has no version")
        self.name = name
        self.version = version
        self._cases = []
        self._case_ids = set()
        self._frozen = False
        for case in cases or []:
            self._add(case)
        self._frozen = True

    def _add(self, case: GoldCase) -> None:
        if self._frozen:
            raise GoldSetError(
                f"gold-set: store {self.name!r} is immutable "
                "-- cases cannot be added after construction"
            )
        if case.case_id in self._case_ids:
            raise GoldSetError(
                f"gold-set: case {case.case_id!r} already registered "
                f"in store {self.name!r}"
            )
        self._cases.append(case)
        self._case_ids.add(case.case_id)

    @property
    def cases(self) -> tuple[GoldCase, ...]:
        """The gold cases in insertion order (immutable view)."""
        return tuple(self._cases)

    @property
    def case_count(self) -> int:
        """Number of gold cases in this store."""
        return len(self._cases)

    def get(self, case_id: str) -> GoldCase | None:
        """Return the case with the given ID, or ``None``."""
        for c in self._cases:
            if c.case_id == case_id:
                return c
        return None


# ---------------------------------------------------------------------------
# Metric primitives
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class PerClassMetric:
    """Precision, recall and F1 for one ontology class across the gold set."""

    ontology_class: str
    true_positives: int
    false_positives: int
    false_negatives: int
    precision: float | None
    recall: float | None
    f1: float | None

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        return {
            "ontology_class": self.ontology_class,
            "true_positives": self.true_positives,
            "false_positives": self.false_positives,
            "false_negatives": self.false_negatives,
            "precision": self.precision,
            "recall": self.recall,
            "f1": self.f1,
        }


@dataclass(frozen=True, slots=True)
class MetricReport:
    """The full metric artefact for one metric run."""

    run_id: str
    store_name: str
    store_version: str
    total_cases: int
    exact_matches: int
    accuracy: float
    per_class: tuple[PerClassMetric, ...]
    evaluated_at: datetime

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        return {
            "run_id": self.run_id,
            "store_name": self.store_name,
            "store_version": self.store_version,
            "total_cases": self.total_cases,
            "exact_matches": self.exact_matches,
            "accuracy": self.accuracy,
            "per_class": [m.as_dict() for m in self.per_class],
            "evaluated_at": self.evaluated_at.isoformat(),
        }

    def class_metric(self, ontology_class: str) -> PerClassMetric | None:
        """Return the :class:`PerClassMetric` for *ontology_class*, or ``None``."""
        needle = ontology_class.strip().lower()
        for m in self.per_class:
            if m.ontology_class.strip().lower() == needle:
                return m
        return None


# ---------------------------------------------------------------------------
# Internal helpers for metric calculation
# ---------------------------------------------------------------------------


def _compute_per_class(
    rows: list[tuple[str, str | None]],
) -> tuple[PerClassMetric, ...]:
    """Build per-class metric rows from (expected_class, predicted_class) pairs."""
    all_classes: set[str] = set()
    for expected, predicted in rows:
        all_classes.add(expected.strip().lower())
        if predicted is not None:
            all_classes.add(predicted.strip().lower())

    metrics: list[PerClassMetric] = []
    for cls in sorted(all_classes):
        tp = sum(
            1
            for exp, pred in rows
            if exp.strip().lower() == cls
            and pred is not None
            and pred.strip().lower() == cls
        )
        fp = sum(
            1
            for exp, pred in rows
            if pred is not None
            and pred.strip().lower() == cls
            and exp.strip().lower() != cls
        )
        fn = sum(
            1
            for exp, pred in rows
            if exp.strip().lower() == cls
            and (pred is None or pred.strip().lower() != cls)
        )

        precision: float | None = tp / (tp + fp) if (tp + fp) > 0 else None
        recall: float | None = tp / (tp + fn) if (tp + fn) > 0 else None
        if precision is not None and recall is not None:
            denom = precision + recall
            f1: float | None = (2 * precision * recall / denom) if denom > 0 else 0.0
        else:
            f1 = None

        metrics.append(
            PerClassMetric(
                ontology_class=cls,
                true_positives=tp,
                false_positives=fp,
                false_negatives=fn,
                precision=precision,
                recall=recall,
                f1=f1,
            )
        )

    return tuple(metrics)


# ---------------------------------------------------------------------------
# Metric Engine
# ---------------------------------------------------------------------------


@dataclass
class MetricEngine:
    """Run a :class:`~ztax_gateway.classifier.Classifier` against a :class:`GoldSetStore`."""

    def run(
        self,
        classifier: Classifier,
        store: GoldSetStore,
        registry: UseCaseRegistry,
        provenance: Provenance,
    ) -> MetricReport:
        """Classify every case in *store* and return a :class:`MetricReport`.

        Governance gate: *provenance* is passed to
        :func:`~ztax_gateway.governance.authorise` before any case is run.

        Raises:
            GovernanceRefusedError: if *provenance* is not authorised.
            MetricEngineError: if *store* contains fewer than
                ``_MIN_GOLD_CASES`` cases.
        """
        # 1. Governance gate -- before any work.
        authorise(registry, provenance)

        # 2. Validate the store is usable.
        if store.case_count < _MIN_GOLD_CASES:
            raise MetricEngineError(
                f"metric-engine: gold set {store.name!r} v{store.version} "
                f"has only {store.case_count} case(s); "
                f"minimum is {_MIN_GOLD_CASES}"
            )

        # 3. Classify each case; collect (expected, predicted) pairs.
        rows: list[tuple[str, str | None]] = []
        exact_matches = 0

        for case in store.cases:
            record: ClassificationRecord = classifier.classify(
                sku_description=case.sku_description,
                provenance=provenance,
                registry=registry,
            )
            top = record.top()
            predicted = top.ontology_class if top is not None else None

            if (
                predicted is not None
                and predicted.strip().lower() == case.normalised_expected
            ):
                exact_matches += 1

            rows.append((case.expected_class, predicted))

        total = store.case_count
        accuracy = exact_matches / total if total > 0 else 0.0

        return MetricReport(
            run_id=uuid.uuid4().hex[:16],
            store_name=store.name,
            store_version=store.version,
            total_cases=total,
            exact_matches=exact_matches,
            accuracy=accuracy,
            per_class=_compute_per_class(rows),
            evaluated_at=datetime.now(tz=UTC),
        )


# ---------------------------------------------------------------------------
# Comparison primitives
# ---------------------------------------------------------------------------


class ComparisonOutcome(StrEnum):
    """The outcome of a :class:`ModelComparator` run."""

    IMPROVED = "IMPROVED"
    REGRESSED = "REGRESSED"
    EQUIVALENT = "EQUIVALENT"
    BELOW_THRESHOLD = "BELOW_THRESHOLD"


@dataclass(frozen=True, slots=True)
class ComparisonReport:
    """The full audit artefact for one comparator run."""

    comparison_id: str
    baseline_report: MetricReport
    candidate_report: MetricReport
    min_accuracy_threshold: float
    accuracy_delta: float
    outcome: ComparisonOutcome
    compared_at: datetime

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        return {
            "comparison_id": self.comparison_id,
            "store_name": self.baseline_report.store_name,
            "store_version": self.baseline_report.store_version,
            "baseline_accuracy": self.baseline_report.accuracy,
            "candidate_accuracy": self.candidate_report.accuracy,
            "accuracy_delta": self.accuracy_delta,
            "min_accuracy_threshold": self.min_accuracy_threshold,
            "outcome": self.outcome.value,
            "compared_at": self.compared_at.isoformat(),
        }


# ---------------------------------------------------------------------------
# Model Comparator
# ---------------------------------------------------------------------------


@dataclass
class ModelComparator:
    """Compare a baseline and a candidate :class:`~ztax_gateway.classifier.Classifier`."""

    min_accuracy_threshold: float = _DEFAULT_MIN_ACCURACY
    _engine: MetricEngine = field(default_factory=MetricEngine, init=False, repr=False)

    def __post_init__(self) -> None:
        if not (0.0 <= self.min_accuracy_threshold <= 1.0):
            raise MetricEngineError(
                f"model-comparator: min_accuracy_threshold must be in [0.0, 1.0], "
                f"got {self.min_accuracy_threshold!r}"
            )

    def compare(
        self,
        baseline: Classifier,
        candidate: Classifier,
        store: GoldSetStore,
        registry: UseCaseRegistry,
        provenance: Provenance,
    ) -> ComparisonReport:
        """Run the metric engine on both classifiers and compare the results.

        Governance gate: *provenance* is passed to
        :func:`~ztax_gateway.governance.authorise` (once by the first engine
        run; reused for the candidate run as the same authorised session).

        Raises:
            GovernanceRefusedError: if *provenance* is not authorised.
            MetricEngineError: if *store* is too small or threshold is invalid.
        """
        baseline_report = self._engine.run(baseline, store, registry, provenance)
        candidate_report = self._engine.run(candidate, store, registry, provenance)

        delta = candidate_report.accuracy - baseline_report.accuracy
        outcome = _derive_outcome(
            delta=delta,
            candidate_accuracy=candidate_report.accuracy,
            threshold=self.min_accuracy_threshold,
        )

        return ComparisonReport(
            comparison_id=uuid.uuid4().hex[:16],
            baseline_report=baseline_report,
            candidate_report=candidate_report,
            min_accuracy_threshold=self.min_accuracy_threshold,
            accuracy_delta=delta,
            outcome=outcome,
            compared_at=datetime.now(tz=UTC),
        )


def _derive_outcome(
    delta: float,
    candidate_accuracy: float,
    threshold: float,
) -> ComparisonOutcome:
    """Derive the :class:`ComparisonOutcome` from accuracy delta and threshold."""
    if candidate_accuracy < threshold:
        return ComparisonOutcome.BELOW_THRESHOLD
    if delta > _EQUIVALENCE_EPSILON:
        return ComparisonOutcome.IMPROVED
    if delta < -_EQUIVALENCE_EPSILON:
        return ComparisonOutcome.REGRESSED
    return ComparisonOutcome.EQUIVALENT
