"""Evaluation Runner.

Chapter 17 §19 of the ZoikoTax Master Specification.

This module is the **orchestration layer** that sits *between* the
:class:`~ztax_gateway.evaluation_quality.GoldSetStore` and the
:class:`~ztax_gateway.evaluation_quality.MetricEngine`.  It provides:

1. **Per-case result capture** — :class:`CaseResult` records the raw
   outcome of one gold-set case: what the classifier returned, whether it
   matched the expected class, the latency of the call, and any error that
   prevented classification.  This is the atomic unit that lets you audit
   *which* cases failed and why, not just the aggregate score.

2. **EvaluationRunner** — orchestrates a full run over a
   :class:`~ztax_gateway.evaluation_quality.GoldSetStore`.  It calls the
   classifier once per case, collects :class:`CaseResult` objects, and emits a
   :class:`RunReport` that carries both the per-case detail *and* the
   :class:`~ztax_gateway.evaluation_quality.MetricReport` that
   :class:`~ztax_gateway.evaluation_quality.ModelComparator` already expects.
   Nothing between the Gold Set Store, the runner, and the metric engine needs
   to change for the comparator to work end-to-end.

3. **Ruleset evaluation path** — a second entry point on
   :class:`EvaluationRunner` accepts an
   :class:`~ztax_gateway.evaluation.EvaluationRuleset` instead of (or
   alongside) a classifier.  This runs every gold-set case through the
   :func:`~ztax_gateway.evaluation.evaluate` gate and records the
   :class:`~ztax_gateway.evaluation.EvaluationReport` per case, enabling the
   same per-case audit trail for rule-based gates.

Design rules
------------
* **No live model calls.**  The runner drives the same
  :class:`~ztax_gateway.classifier.Classifier` the rest of the gateway uses.
  No provider, no embedding call, no HTTP request.
* **Governance gate on every run.**  :meth:`EvaluationRunner.run_classifier`
  and :meth:`EvaluationRunner.run_ruleset` require a ``(registry, provenance)``
  pair and pass it to :func:`~ztax_gateway.governance.authorise` before any
  case is executed.
* **Errors are captured, not raised.**  A classifier error on one gold-set
  case does not abort the run.  The error is stored in
  :attr:`CaseResult.error` so the rest of the gold set still produces
  per-case results and the metric report reflects the real failure rate.
* **Immutable results.**  :class:`CaseResult` and :class:`RunReport` are
  ``frozen=True`` dataclasses — they are evidence artefacts.
* **No fiscal imports.**  This module imports nothing from the fiscal, tax or
  subledger packages (ADR-0006 §2.6).
"""

from __future__ import annotations

import time
import uuid
from dataclasses import dataclass
from datetime import UTC, datetime
from enum import StrEnum
from typing import Final

from .classifier import ClassificationRecord, Classifier
from .evaluation import EvaluationReport, EvaluationRuleset, EvaluationVerdict, evaluate
from .evaluation_quality import (
    GoldCase,
    GoldSetStore,
    MetricReport,
    _compute_per_class,  # internal helper; same package
)
from .governance import UseCaseRegistry, authorise
from .provenance import Provenance

__all__: list[str] = [
    "CaseOutcome",
    "CaseResult",
    "EvaluationRunner",
    "EvaluationRunnerError",
    "RulesetCaseResult",
    "RulesetRunReport",
    "RunReport",
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# Minimum number of gold cases required before a run is permitted.
# Mirrors the constraint in MetricEngine so that the runner never produces a
# RunReport the metric engine would reject.
_MIN_GOLD_CASES: Final[int] = 2


# ---------------------------------------------------------------------------
# Errors
# ---------------------------------------------------------------------------


class EvaluationRunnerError(Exception):
    """Raised when an evaluation run cannot proceed."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------------------
# Per-case result primitives
# ---------------------------------------------------------------------------


class CaseOutcome(StrEnum):
    """The outcome of one gold-set case in a classifier run.

    ``MATCH``     -- the classifier's top proposal matches ``expected_class``.
    ``MISMATCH``  -- the classifier returned a top proposal but it was wrong.
    ``NO_RESULT`` -- the classifier returned no proposals (empty result set).
    ``ERROR``     -- the classifier raised an exception; see :attr:`CaseResult.error`.
    """

    MATCH = "MATCH"
    MISMATCH = "MISMATCH"
    NO_RESULT = "NO_RESULT"
    ERROR = "ERROR"


@dataclass(frozen=True, slots=True)
class CaseResult:
    """The raw outcome of running one gold-set case through a classifier.

    ``case_id``         -- mirrors :attr:`~ztax_gateway.evaluation_quality.GoldCase.case_id`.
    ``expected_class``  -- mirrors :attr:`~ztax_gateway.evaluation_quality.GoldCase.expected_class`.
    ``predicted_class`` -- the top proposal returned by the classifier, or ``None``.
    ``outcome``         -- one of the :class:`CaseOutcome` values.
    ``record``          -- the full :class:`~ztax_gateway.classifier.ClassificationRecord`,
                           or ``None`` if the classifier raised.
    ``error``           -- the error message if the classifier raised, otherwise ``""``.
    ``latency_ms``      -- wall-clock time of the ``classify()`` call in milliseconds.
    """

    case_id: str
    expected_class: str
    predicted_class: str | None
    outcome: CaseOutcome
    record: ClassificationRecord | None
    error: str
    latency_ms: float

    def is_match(self) -> bool:
        """Return ``True`` if the case was an exact-match."""
        return self.outcome is CaseOutcome.MATCH

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        return {
            "case_id": self.case_id,
            "expected_class": self.expected_class,
            "predicted_class": self.predicted_class,
            "outcome": self.outcome.value,
            "error": self.error,
            "latency_ms": round(self.latency_ms, 3),
        }


# ---------------------------------------------------------------------------
# Per-case ruleset result primitive
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class RulesetCaseResult:
    """The raw outcome of evaluating one gold-set case through a ruleset.

    ``case_id``        -- mirrors :attr:`~ztax_gateway.evaluation_quality.GoldCase.case_id`.
    ``expected_class`` -- mirrors :attr:`~ztax_gateway.evaluation_quality.GoldCase.expected_class`.
    ``report``         -- the full :class:`~ztax_gateway.evaluation.EvaluationReport`
                          for this case, or ``None`` if classification or evaluation raised.
    ``error``          -- the error message if classification/evaluation raised, otherwise ``""``.
    ``latency_ms``     -- wall-clock time for classify + evaluate in milliseconds.
    """

    case_id: str
    expected_class: str
    report: EvaluationReport | None
    error: str
    latency_ms: float

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        return {
            "case_id": self.case_id,
            "expected_class": self.expected_class,
            "verdict": self.report.verdict.value if self.report else None,
            "failed_rules": self.report.failed_rules() if self.report else [],
            "error": self.error,
            "latency_ms": round(self.latency_ms, 3),
        }


# ---------------------------------------------------------------------------
# Run report primitives
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class RunReport:
    """The full audit artefact for one :class:`EvaluationRunner` classifier run.

    ``run_id``           -- short unique identifier (hex prefix of a UUID4).
    ``store_name``       -- mirrors :attr:`~ztax_gateway.evaluation_quality.GoldSetStore.name`.
    ``store_version``    -- mirrors :attr:`~ztax_gateway.evaluation_quality.GoldSetStore.version`.
    ``case_results``     -- per-case :class:`CaseResult` objects, in gold-set order.
    ``metric_report``    -- the :class:`~ztax_gateway.evaluation_quality.MetricReport`
                            produced from the case results; ready for
                            :class:`~ztax_gateway.evaluation_quality.ModelComparator`.
    ``total_latency_ms`` -- sum of per-case latencies in milliseconds.
    ``started_at``       -- UTC timestamp when the run began.
    ``completed_at``     -- UTC timestamp when the run completed.
    """

    run_id: str
    store_name: str
    store_version: str
    case_results: tuple[CaseResult, ...]
    metric_report: MetricReport
    total_latency_ms: float
    started_at: datetime
    completed_at: datetime

    @property
    def total_cases(self) -> int:
        """Number of cases in the run."""
        return len(self.case_results)

    @property
    def match_count(self) -> int:
        """Number of MATCH outcomes."""
        return sum(1 for r in self.case_results if r.outcome is CaseOutcome.MATCH)

    @property
    def error_count(self) -> int:
        """Number of ERROR outcomes."""
        return sum(1 for r in self.case_results if r.outcome is CaseOutcome.ERROR)

    @property
    def no_result_count(self) -> int:
        """Number of NO_RESULT outcomes."""
        return sum(1 for r in self.case_results if r.outcome is CaseOutcome.NO_RESULT)

    def failed_cases(self) -> list[CaseResult]:
        """Return cases that were MISMATCH, NO_RESULT, or ERROR."""
        return [
            r
            for r in self.case_results
            if r.outcome in (CaseOutcome.MISMATCH, CaseOutcome.NO_RESULT, CaseOutcome.ERROR)
        ]

    def get_case(self, case_id: str) -> CaseResult | None:
        """Return the :class:`CaseResult` for *case_id*, or ``None``."""
        for r in self.case_results:
            if r.case_id == case_id:
                return r
        return None

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        return {
            "run_id": self.run_id,
            "store_name": self.store_name,
            "store_version": self.store_version,
            "total_cases": self.total_cases,
            "match_count": self.match_count,
            "error_count": self.error_count,
            "no_result_count": self.no_result_count,
            "total_latency_ms": round(self.total_latency_ms, 3),
            "metric_report": self.metric_report.as_dict(),
            "started_at": self.started_at.isoformat(),
            "completed_at": self.completed_at.isoformat(),
        }


@dataclass(frozen=True, slots=True)
class RulesetRunReport:
    """The full audit artefact for one :class:`EvaluationRunner` ruleset run.

    ``run_id``           -- short unique identifier (hex prefix of a UUID4).
    ``store_name``       -- mirrors :attr:`~ztax_gateway.evaluation_quality.GoldSetStore.name`.
    ``store_version``    -- mirrors :attr:`~ztax_gateway.evaluation_quality.GoldSetStore.version`.
    ``ruleset_name``     -- mirrors :attr:`~ztax_gateway.evaluation.EvaluationRuleset.name`.
    ``ruleset_version``  -- mirrors :attr:`~ztax_gateway.evaluation.EvaluationRuleset.version`.
    ``case_results``     -- per-case :class:`RulesetCaseResult` objects, in gold-set order.
    ``pass_count``       -- number of cases where all applicable rules passed.
    ``fail_count``       -- number of cases where at least one rule failed.
    ``error_count``      -- number of cases that raised during classify or evaluate.
    ``total_latency_ms`` -- sum of per-case latencies in milliseconds.
    ``started_at``       -- UTC timestamp when the run began.
    ``completed_at``     -- UTC timestamp when the run completed.
    """

    run_id: str
    store_name: str
    store_version: str
    ruleset_name: str
    ruleset_version: str
    case_results: tuple[RulesetCaseResult, ...]
    pass_count: int
    fail_count: int
    error_count: int
    total_latency_ms: float
    started_at: datetime
    completed_at: datetime

    @property
    def total_cases(self) -> int:
        """Number of cases in the run."""
        return len(self.case_results)

    def failed_cases(self) -> list[RulesetCaseResult]:
        """Return cases whose verdict was FAIL or that had an error."""
        return [
            r
            for r in self.case_results
            if r.error or (r.report is not None and r.report.verdict is EvaluationVerdict.FAIL)
        ]

    def get_case(self, case_id: str) -> RulesetCaseResult | None:
        """Return the :class:`RulesetCaseResult` for *case_id*, or ``None``."""
        for r in self.case_results:
            if r.case_id == case_id:
                return r
        return None

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        return {
            "run_id": self.run_id,
            "store_name": self.store_name,
            "store_version": self.store_version,
            "ruleset_name": self.ruleset_name,
            "ruleset_version": self.ruleset_version,
            "total_cases": self.total_cases,
            "pass_count": self.pass_count,
            "fail_count": self.fail_count,
            "error_count": self.error_count,
            "total_latency_ms": round(self.total_latency_ms, 3),
            "started_at": self.started_at.isoformat(),
            "completed_at": self.completed_at.isoformat(),
        }


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------


def _run_one_case(
    case: GoldCase,
    classifier: Classifier,
    registry: UseCaseRegistry,
    provenance: Provenance,
) -> CaseResult:
    """Classify a single case and return a :class:`CaseResult`.

    Errors from ``classify()`` are captured so the rest of the gold set
    continues running.
    """
    t0 = time.perf_counter()
    try:
        record: ClassificationRecord = classifier.classify(
            sku_description=case.sku_description,
            provenance=provenance,
            registry=registry,
        )
        latency_ms = (time.perf_counter() - t0) * 1000.0

        top = record.top()
        if top is None:
            return CaseResult(
                case_id=case.case_id,
                expected_class=case.expected_class,
                predicted_class=None,
                outcome=CaseOutcome.NO_RESULT,
                record=record,
                error="",
                latency_ms=latency_ms,
            )

        predicted = top.ontology_class
        matched = predicted.strip().lower() == case.normalised_expected
        return CaseResult(
            case_id=case.case_id,
            expected_class=case.expected_class,
            predicted_class=predicted,
            outcome=CaseOutcome.MATCH if matched else CaseOutcome.MISMATCH,
            record=record,
            error="",
            latency_ms=latency_ms,
        )

    except Exception as exc:
        latency_ms = (time.perf_counter() - t0) * 1000.0
        return CaseResult(
            case_id=case.case_id,
            expected_class=case.expected_class,
            predicted_class=None,
            outcome=CaseOutcome.ERROR,
            record=None,
            error=str(exc),
            latency_ms=latency_ms,
        )


def _build_metric_report_from_case_results(
    case_results: list[CaseResult],
    store: GoldSetStore,
) -> MetricReport:
    """Build a :class:`MetricReport` from the per-case classifier results.

    Mirrors the aggregation inside :meth:`MetricEngine.run` but operates on
    the already-collected :class:`CaseResult` objects so classification is
    never performed twice.
    """
    rows: list[tuple[str, str | None]] = [
        (r.expected_class, r.predicted_class) for r in case_results
    ]
    exact_matches = sum(1 for r in case_results if r.outcome is CaseOutcome.MATCH)
    total = len(case_results)
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


def _run_one_ruleset_case(
    case: GoldCase,
    classifier: Classifier,
    ruleset: EvaluationRuleset,
    registry: UseCaseRegistry,
    provenance: Provenance,
) -> RulesetCaseResult:
    """Classify then evaluate a single case; return a :class:`RulesetCaseResult`."""
    t0 = time.perf_counter()
    try:
        record: ClassificationRecord = classifier.classify(
            sku_description=case.sku_description,
            provenance=provenance,
            registry=registry,
        )
        report: EvaluationReport = evaluate(record, ruleset, registry, provenance)
        latency_ms = (time.perf_counter() - t0) * 1000.0
        return RulesetCaseResult(
            case_id=case.case_id,
            expected_class=case.expected_class,
            report=report,
            error="",
            latency_ms=latency_ms,
        )
    except Exception as exc:
        latency_ms = (time.perf_counter() - t0) * 1000.0
        return RulesetCaseResult(
            case_id=case.case_id,
            expected_class=case.expected_class,
            report=None,
            error=str(exc),
            latency_ms=latency_ms,
        )


# ---------------------------------------------------------------------------
# Evaluation Runner
# ---------------------------------------------------------------------------


@dataclass
class EvaluationRunner:
    """Orchestrate a full evaluation run over a :class:`GoldSetStore`.

    The runner is the piece that actually *uses* the Gold Set Store, Metric
    Engine, and Model Comparator pipeline in sequence.  It takes a gold set and
    a classifier (or ruleset), runs every case, and produces a
    :class:`RunReport` (or :class:`RulesetRunReport`) that carries both the
    per-case detail and the
    :class:`~ztax_gateway.evaluation_quality.MetricReport` ready for the
    comparator.

    Example -- classifier path::

        from ztax_gateway.evaluation_runner import EvaluationRunner
        from ztax_gateway.evaluation_quality import GoldCase, GoldSetStore, ModelComparator

        store = GoldSetStore(
            name="telecom-sku-v1",
            version="1.0.0",
            cases=[
                GoldCase("c-001", "Roaming data bundle prepaid SIM", "Roaming Data Bundle"),
                GoldCase("c-002", "Standard voice call plan", "Voice Services"),
            ],
        )
        runner = EvaluationRunner()
        run_report = runner.run_classifier(
            classifier=my_classifier,
            store=store,
            registry=registry,
            provenance=provenance,
        )

        # Inspect per-case failures:
        for case in run_report.failed_cases():
            print(case.case_id, case.outcome, case.error)

        # The embedded MetricReport is ready for ModelComparator directly:
        # comparator.compare(...) uses MetricEngine, which also produces
        # a MetricReport — the shapes are identical.

    Example -- ruleset path::

        run_report = runner.run_ruleset(
            classifier=my_classifier,
            ruleset=my_ruleset,
            store=store,
            registry=registry,
            provenance=provenance,
        )
        for case in run_report.failed_cases():
            if case.report:
                print(case.case_id, case.report.failed_rules())
            else:
                print(case.case_id, "ERROR:", case.error)
    """

    def run_classifier(
        self,
        classifier: Classifier,
        store: GoldSetStore,
        registry: UseCaseRegistry,
        provenance: Provenance,
    ) -> RunReport:
        """Run *classifier* against every case in *store* and return a :class:`RunReport`.

        Governance gate: *provenance* is passed to
        :func:`~ztax_gateway.governance.authorise` before any case is executed.

        Classification errors on individual cases are captured in
        :attr:`CaseResult.error` and do **not** abort the run.  The metric
        report reflects the real failure rate (ERROR and NO_RESULT cases count
        as incorrect predictions).

        Args:
            classifier: The :class:`~ztax_gateway.classifier.Classifier` to run.
            store:      The :class:`~ztax_gateway.evaluation_quality.GoldSetStore`
                        to iterate over.
            registry:   The :class:`~ztax_gateway.governance.UseCaseRegistry`
                        holding use-case registrations and kill switches.
            provenance: The governance context. Must be a registered,
                        non-killed use case.

        Returns:
            A :class:`RunReport` with per-case :class:`CaseResult` objects and
            an embedded :class:`~ztax_gateway.evaluation_quality.MetricReport`.

        Raises:
            GovernanceRefusedError: if *provenance* is not authorised.
            EvaluationRunnerError:  if *store* has fewer than
                                    ``_MIN_GOLD_CASES`` cases.
        """
        # 1. Governance gate -- before any work.
        authorise(registry, provenance)

        # 2. Validate the store is usable.
        if store.case_count < _MIN_GOLD_CASES:
            raise EvaluationRunnerError(
                f"evaluation-runner: gold set {store.name!r} v{store.version} "
                f"has only {store.case_count} case(s); minimum is {_MIN_GOLD_CASES}"
            )

        started_at = datetime.now(tz=UTC)

        # 3. Run each case; capture errors without aborting.
        case_results: list[CaseResult] = [
            _run_one_case(case, classifier, registry, provenance)
            for case in store.cases
        ]

        completed_at = datetime.now(tz=UTC)
        total_latency_ms = sum(r.latency_ms for r in case_results)

        # 4. Build the MetricReport from the collected per-case results.
        metric_report = _build_metric_report_from_case_results(case_results, store)

        return RunReport(
            run_id=uuid.uuid4().hex[:16],
            store_name=store.name,
            store_version=store.version,
            case_results=tuple(case_results),
            metric_report=metric_report,
            total_latency_ms=total_latency_ms,
            started_at=started_at,
            completed_at=completed_at,
        )

    def run_ruleset(
        self,
        classifier: Classifier,
        ruleset: EvaluationRuleset,
        store: GoldSetStore,
        registry: UseCaseRegistry,
        provenance: Provenance,
    ) -> RulesetRunReport:
        """Classify every case in *store* then evaluate each record through *ruleset*.

        This is the rule-gate path: for each gold-set case the runner first
        classifies (same as :meth:`run_classifier`) and then calls
        :func:`~ztax_gateway.evaluation.evaluate` on the
        :class:`~ztax_gateway.classifier.ClassificationRecord`.  The per-case
        :class:`~ztax_gateway.evaluation.EvaluationReport` is stored in
        :class:`RulesetCaseResult` so an operator can see which rules failed on
        which cases.

        Args:
            classifier: The :class:`~ztax_gateway.classifier.Classifier` to run.
            ruleset:    The :class:`~ztax_gateway.evaluation.EvaluationRuleset`
                        to apply to each classified record.
            store:      The :class:`~ztax_gateway.evaluation_quality.GoldSetStore`
                        to iterate over.
            registry:   The :class:`~ztax_gateway.governance.UseCaseRegistry`.
            provenance: The governance context.

        Returns:
            A :class:`RulesetRunReport` with per-case :class:`RulesetCaseResult` objects.

        Raises:
            GovernanceRefusedError: if *provenance* is not authorised.
            EvaluationRunnerError:  if *store* has fewer than
                                    ``_MIN_GOLD_CASES`` cases.
        """
        # 1. Governance gate -- before any work.
        authorise(registry, provenance)

        # 2. Validate the store is usable.
        if store.case_count < _MIN_GOLD_CASES:
            raise EvaluationRunnerError(
                f"evaluation-runner: gold set {store.name!r} v{store.version} "
                f"has only {store.case_count} case(s); minimum is {_MIN_GOLD_CASES}"
            )

        started_at = datetime.now(tz=UTC)

        # 3. Run each case; capture errors without aborting.
        case_results: list[RulesetCaseResult] = [
            _run_one_ruleset_case(case, classifier, ruleset, registry, provenance)
            for case in store.cases
        ]

        completed_at = datetime.now(tz=UTC)
        total_latency_ms = sum(r.latency_ms for r in case_results)

        # 4. Aggregate verdict counts.
        pass_count = sum(
            1
            for r in case_results
            if r.report is not None and r.report.verdict is EvaluationVerdict.PASS
        )
        fail_count = sum(
            1
            for r in case_results
            if r.report is not None and r.report.verdict is EvaluationVerdict.FAIL
        )
        error_count = sum(1 for r in case_results if r.error)

        return RulesetRunReport(
            run_id=uuid.uuid4().hex[:16],
            store_name=store.name,
            store_version=store.version,
            ruleset_name=ruleset.name,
            ruleset_version=ruleset.version,
            case_results=tuple(case_results),
            pass_count=pass_count,
            fail_count=fail_count,
            error_count=error_count,
            total_latency_ms=total_latency_ms,
            started_at=started_at,
            completed_at=completed_at,
        )
