"""Behavioral Fingerprint and Drift Detector.

Chapter 17 �19 and �21 of the ZoikoTax Master Specification.
Requirements: ZTAX-AI-REQ-0091, ZTAX-AI-REQ-0095.

This module implements two complementary model-reliability primitives:

1. **BehaviorFingerprint** -- runs a fixed set of synthetic canary probes
   against a :class:`~ztax_gateway.classifier.Classifier` and captures the
   per-probe output.  Two fingerprints of the same model/KnowledgeBase
   combination are ``DIVERGED`` if *any* probe returns a different top class
   or a meaningfully different score; they are ``EQUIVALENT`` if all probes
   are stable within tolerance.  This catches silent provider weight updates
   when version pinning is unavailable (ZTAX-AI-REQ-0091).

2. **DriftDetector** -- maintains a rolling window of
   :class:`InvocationSignal` observations and emits a :class:`DriftReport`
   when statistical thresholds are breached:

   * **Abstention rate** -- the proportion of invocations that returned no
     classification (empty result).  A sudden increase means the model is
     refusing inputs it used to handle.
   * **OOD (out-of-distribution) rate** -- the proportion whose best-match
     score fell below the OOD threshold.
   * **Calibration drift** -- the rolling mean confidence score for successful
     classifications.  A drop suggests the model''s internal certainty is
     changing even when outputs look stable.

Design rules
------------
* **No live model calls.**  BehaviorFingerprint drives the same
  :class:`~ztax_gateway.classifier.Classifier` the rest of the gateway uses,
  which is a deterministic BM25 ranker.  No provider, no HTTP request.
* **No fiscal imports.**  ADR-0006 �2.6.
* **Governance gate on every fingerprint run.**  :meth:`BehaviorFingerprint.run`
  requires a ``(registry, provenance)`` pair.
* **Frozen outputs.**  :class:`FingerprintReport`, :class:`ProbeResult` and
  :class:`DriftReport` are ``frozen=True``.
* **Deterministic probe IDs.**  Each :class:`CanaryProbe` carries a stable
  ``probe_id`` so two runs of the same probe set are directly comparable.
"""

from __future__ import annotations

import statistics
import uuid
from collections import deque
from dataclasses import dataclass
from datetime import UTC, datetime
from enum import StrEnum
from typing import Final

from .classifier import Classifier
from .evaluation_quality import ComparisonOutcome, ComparisonReport
from .governance import UseCaseRegistry, authorise
from .provenance import Provenance

__all__: list[str] = [
    "BehaviorFingerprint",
    "CanaryEvaluator",
    "CanaryEvaluatorError",
    "CanaryProbe",
    "CanaryVerdict",
    "CanaryVerdictReport",
    "DriftDetector",
    "DriftError",
    "DriftReport",
    "DriftStatus",
    "FingerprintComparison",
    "FingerprintError",
    "FingerprintReport",
    "InvocationSignal",
    "ProbeResult",
    "compare_fingerprints",
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# Default minimum window size before drift can be declared.  A window that
# is too small produces noisy drift signals -- one bad invocation in a
# two-observation window is a 50 % abstention rate.
_DEFAULT_WINDOW_SIZE: Final[int] = 20

# Default abstention rate at which drift is declared.
_DEFAULT_ABSTENTION_THRESHOLD: Final[float] = 0.30

# Default OOD rate at which drift is declared.
_DEFAULT_OOD_THRESHOLD: Final[float] = 0.30

# Default mean confidence below which calibration drift is declared.
_DEFAULT_CALIBRATION_THRESHOLD: Final[float] = 0.40

# Score tolerance for fingerprint equivalence: a difference smaller than
# this between two probe scores is treated as noise, not drift.
_SCORE_TOLERANCE: Final[float] = 1e-6


# ---------------------------------------------------------------------------
# Errors
# ---------------------------------------------------------------------------


class FingerprintError(Exception):
    """Raised when a fingerprint run cannot proceed."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


class DriftError(Exception):
    """Raised when the drift detector is misconfigured."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------------------
# Canary probe model
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class CanaryProbe:
    """One synthetic probe in a behavioral fingerprint set.

    A probe is a fixed input whose expected output is known in advance.
    The probe does not call a live model; it runs through the same
    Classifier (BM25 ranker) the rest of the gateway uses.

    Fields
    ------
    probe_id
        Stable identifier for this probe.  Must be unique within a probe set.
    description
        The synthetic input text submitted to the classifier.
    expected_class
        The top class the probe should return when behavior is nominal.
        May be empty if the probe is designed to test abstention behavior.
    """

    probe_id: str
    description: str
    expected_class: str = ""


@dataclass(frozen=True, slots=True)
class ProbeResult:
    """The outcome of running one CanaryProbe.

    Fields
    ------
    probe_id        The probe that was run.
    actual_class    The top class returned by the classifier (empty = abstain).
    score           The BM25 score for the top match.  0.0 if abstention.
    matched_expected  True iff actual_class == probe.expected_class.
    """

    probe_id: str
    actual_class: str
    score: float
    matched_expected: bool


# ---------------------------------------------------------------------------
# Fingerprint comparison
# ---------------------------------------------------------------------------


class FingerprintComparison(StrEnum):
    """Outcome of comparing two BehaviorFingerprint reports."""

    EQUIVALENT = "EQUIVALENT"   # all probes agree within tolerance
    DIVERGED = "DIVERGED"       # at least one probe result differs
    INCOMPARABLE = "INCOMPARABLE"  # probe sets differ; comparison not valid


@dataclass(frozen=True, slots=True)
class FingerprintReport:
    """The output of one BehaviorFingerprint run.

    Frozen; nothing downstream may mutate results after the run.

    Fields
    ------
    fingerprint_id  Unique identifier for this run.
    run_at          UTC timestamp of the run.
    probe_results   One ProbeResult per CanaryProbe in the probe set.
    overall_accuracy  Fraction of probes whose actual_class matched expected.
                     NaN if all probes had no expected_class.
    """

    fingerprint_id: str
    run_at: datetime
    probe_results: tuple[ProbeResult, ...]
    overall_accuracy: float


def compare_fingerprints(
    baseline: FingerprintReport,
    candidate: FingerprintReport,
) -> FingerprintComparison:
    """Compare two fingerprint reports.

    Returns EQUIVALENT if all probes are within score tolerance and all
    expected-class matches agree.  Returns DIVERGED if any probe differs.
    Returns INCOMPARABLE if the probe sets do not match.
    """
    b_ids = frozenset(r.probe_id for r in baseline.probe_results)
    c_ids = frozenset(r.probe_id for r in candidate.probe_results)
    if b_ids != c_ids:
        return FingerprintComparison.INCOMPARABLE

    b_map = {r.probe_id: r for r in baseline.probe_results}
    c_map = {r.probe_id: r for r in candidate.probe_results}

    for probe_id in b_ids:
        b = b_map[probe_id]
        c = c_map[probe_id]
        if b.actual_class != c.actual_class:
            return FingerprintComparison.DIVERGED
        if abs(b.score - c.score) > _SCORE_TOLERANCE:
            return FingerprintComparison.DIVERGED

    return FingerprintComparison.EQUIVALENT


# ---------------------------------------------------------------------------
# BehaviorFingerprint
# ---------------------------------------------------------------------------


class BehaviorFingerprint:
    """Runs a fixed canary probe set against a Classifier.

    The fingerprint is governance-gated: run() calls authorise() before
    executing any probe.

    Usage
    -----
    fp = BehaviorFingerprint(
        probes=[CanaryProbe("p1", "SaaS annual subscription", "SOFTWARE_SERVICE")],
        classifier=my_classifier,
        registry=use_case_registry,
        provenance=provenance,
    )
    report = fp.run()
    comparison = compare_fingerprints(baseline_report, report)
    """

    def __init__(
        self,
        probes: list[CanaryProbe],
        classifier: Classifier,
        registry: UseCaseRegistry,
        provenance: Provenance,
    ) -> None:
        if not probes:
            raise FingerprintError("fingerprint: probe set is empty")
        probe_ids = [p.probe_id for p in probes]
        if len(probe_ids) != len(set(probe_ids)):
            raise FingerprintError("fingerprint: probe_ids must be unique")
        self._probes = list(probes)
        self._classifier = classifier
        self._registry = registry
        self._provenance = provenance

    def run(self) -> FingerprintReport:
        """Run all probes and return a FingerprintReport.

        Governance-gated: authorise() is called before any probe executes.

        Raises
        ------
        GovernanceRefusedError
            If the provenance fails the governance gate.
        """
        authorise(self._registry, self._provenance)

        results: list[ProbeResult] = []
        for probe in self._probes:
            record = self._classifier.classify(
                probe.description,
                provenance=self._provenance,
                registry=self._registry,
            )
            if not record.proposals:
                actual_class = ""
                score = 0.0
            else:
                top = record.proposals[0]
                actual_class = top.ontology_class
                score = top.rank

            matched = (
                (actual_class == probe.expected_class)
                if probe.expected_class
                else True  # probes with no expected_class always match
            )
            results.append(
                ProbeResult(
                    probe_id=probe.probe_id,
                    actual_class=actual_class,
                    score=score,
                    matched_expected=matched,
                )
            )

        # Accuracy over probes that have a declared expected_class.
        expected_probes = [
            (probe, r)
            for probe, r in zip(self._probes, results, strict=False)
            if probe.expected_class
        ]
        if expected_probes:
            accuracy = sum(1 for _, r in expected_probes if r.matched_expected) / len(
                expected_probes
            )
        else:
            accuracy = float("nan")

        return FingerprintReport(
            fingerprint_id=str(uuid.uuid4()),
            run_at=datetime.now(UTC),
            probe_results=tuple(results),
            overall_accuracy=accuracy,
        )


# ---------------------------------------------------------------------------
# Drift detector
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class InvocationSignal:
    """One observation from a live invocation, recorded for drift detection.

    Fields
    ------
    invocation_id   Unique identifier for this invocation.
    abstained       True if the classifier returned no result.
    ood             True if the best-match score fell below the OOD threshold.
    confidence      The top-match score, or 0.0 if abstained.
    observed_at     UTC timestamp.
    """

    invocation_id: str
    abstained: bool
    ood: bool
    confidence: float
    observed_at: datetime


class DriftStatus(StrEnum):
    """Whether the current observation window shows drift."""

    NOMINAL = "NOMINAL"               # all metrics within thresholds
    ABSTENTION_DRIFT = "ABSTENTION_DRIFT"
    OOD_DRIFT = "OOD_DRIFT"
    CALIBRATION_DRIFT = "CALIBRATION_DRIFT"
    MULTI_DRIFT = "MULTI_DRIFT"       # more than one drift kind active


@dataclass(frozen=True, slots=True)
class DriftReport:
    """The outcome of a drift check against the current window.

    Fields
    ------
    report_id           Unique identifier.
    window_size         Number of observations evaluated.
    abstention_rate     Fraction that abstained in this window.
    ood_rate            Fraction classified as OOD in this window.
    mean_confidence     Rolling mean confidence of non-abstained invocations.
    status              NOMINAL or the kind(s) of drift detected.
    drift_kinds         Frozenset of specific drift kinds (empty when NOMINAL).
    checked_at          UTC timestamp.
    """

    report_id: str
    window_size: int
    abstention_rate: float
    ood_rate: float
    mean_confidence: float
    status: DriftStatus
    drift_kinds: frozenset[str]
    checked_at: datetime


class DriftDetector:
    """Tracks a rolling window of InvocationSignal observations.

    Emits a DriftReport when check() is called.  The report reflects the
    current window of the last ``window_size`` observations.

    Parameters
    ----------
    window_size
        Maximum number of observations kept in the rolling window.
    abstention_threshold
        Abstention rate at or above which ABSTENTION_DRIFT is declared.
    ood_threshold
        OOD rate at or above which OOD_DRIFT is declared.
    calibration_threshold
        Mean confidence at or below which CALIBRATION_DRIFT is declared.
    min_window
        Minimum number of observations before drift can be declared.
        Drift is suppressed until the window reaches this size.

    Usage
    -----
    detector = DriftDetector()
    detector.record(InvocationSignal(...))
    report = detector.check()
    """

    def __init__(
        self,
        window_size: int = _DEFAULT_WINDOW_SIZE,
        abstention_threshold: float = _DEFAULT_ABSTENTION_THRESHOLD,
        ood_threshold: float = _DEFAULT_OOD_THRESHOLD,
        calibration_threshold: float = _DEFAULT_CALIBRATION_THRESHOLD,
        min_window: int = 5,
    ) -> None:
        if window_size < 2:
            raise DriftError("drift: window_size must be >= 2")
        if not (0.0 < abstention_threshold <= 1.0):
            raise DriftError("drift: abstention_threshold must be in (0, 1]")
        if not (0.0 < ood_threshold <= 1.0):
            raise DriftError("drift: ood_threshold must be in (0, 1]")
        if not (0.0 < calibration_threshold <= 1.0):
            raise DriftError("drift: calibration_threshold must be in (0, 1]")
        if min_window < 1:
            raise DriftError("drift: min_window must be >= 1")

        self._window: deque[InvocationSignal] = deque(maxlen=window_size)
        self._abstention_threshold = abstention_threshold
        self._ood_threshold = ood_threshold
        self._calibration_threshold = calibration_threshold
        self._min_window = min_window

    def record(self, signal: InvocationSignal) -> None:
        """Add one observation to the rolling window."""
        self._window.append(signal)

    @property
    def window_count(self) -> int:
        """Current number of observations in the window."""
        return len(self._window)

    def check(self) -> DriftReport:
        """Evaluate the current window and return a DriftReport.

        If the window has fewer than ``min_window`` observations, the report
        will always be NOMINAL -- the window is too small to be meaningful.
        """
        n = len(self._window)

        if n < self._min_window:
            return DriftReport(
                report_id=str(uuid.uuid4()),
                window_size=n,
                abstention_rate=0.0,
                ood_rate=0.0,
                mean_confidence=1.0,
                status=DriftStatus.NOMINAL,
                drift_kinds=frozenset(),
                checked_at=datetime.now(UTC),
            )

        abstentions = sum(1 for s in self._window if s.abstained)
        oods = sum(1 for s in self._window if s.ood)
        abstention_rate = abstentions / n
        ood_rate = oods / n

        confident = [s.confidence for s in self._window if not s.abstained]
        mean_confidence = statistics.mean(confident) if confident else 0.0

        drift_kinds: set[str] = set()
        if abstention_rate >= self._abstention_threshold:
            drift_kinds.add("ABSTENTION_DRIFT")
        if ood_rate >= self._ood_threshold:
            drift_kinds.add("OOD_DRIFT")
        if mean_confidence <= self._calibration_threshold and confident:
            drift_kinds.add("CALIBRATION_DRIFT")

        if not drift_kinds:
            status = DriftStatus.NOMINAL
        elif len(drift_kinds) == 1:
            status = DriftStatus(next(iter(drift_kinds)))
        else:
            status = DriftStatus.MULTI_DRIFT

        return DriftReport(
            report_id=str(uuid.uuid4()),
            window_size=n,
            abstention_rate=abstention_rate,
            ood_rate=ood_rate,
            mean_confidence=mean_confidence,
            status=status,
            drift_kinds=frozenset(drift_kinds),
            checked_at=datetime.now(UTC),
        )

    def reset(self) -> None:
        """Clear the rolling window."""
        self._window.clear()


# ---------------------------------------------------------------------------
# CanaryEvaluator
# ---------------------------------------------------------------------------


class CanaryEvaluatorError(Exception):
    """Raised when a CanaryEvaluator cannot complete its evaluation."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


class CanaryVerdict(StrEnum):
    """The deployment recommendation produced by a :class:`CanaryEvaluator` run.

    Semantics
    ---------
    PROMOTE
        The candidate meets the accuracy threshold, has not regressed relative
        to the baseline, and the live drift window is NOMINAL.  Safe to move
        the candidate into production.
    HOLD
        Accuracy is acceptable (above threshold) but one of two conditions
        holds: (a) the accuracy delta is EQUIVALENT and drift is NOMINAL --
        no benefit to promoting yet -- or (b) the live drift window shows an
        active drift signal.  Wait for drift to clear or collect more data.
    ROLLBACK
        The candidate is BELOW_THRESHOLD or has REGRESSED relative to the
        baseline.  Do not promote; if already in a partial canary rollout,
        roll back.
    """

    PROMOTE = "PROMOTE"
    HOLD = "HOLD"
    ROLLBACK = "ROLLBACK"


@dataclass(frozen=True, slots=True)
class CanaryVerdictReport:
    """The full audit artefact produced by one :class:`CanaryEvaluator` run.

    Fields
    ------
    verdict_id
        Unique identifier for this evaluation run.
    comparison_id
        The ``ComparisonReport.comparison_id`` that was evaluated.
    accuracy_delta
        ``candidate_accuracy - baseline_accuracy`` (from the
        :class:`~ztax_gateway.evaluation_quality.ComparisonReport`).
    comparison_outcome
        The :class:`~ztax_gateway.evaluation_quality.ComparisonOutcome` of the
        accuracy comparison (IMPROVED / EQUIVALENT / REGRESSED / BELOW_THRESHOLD).
    drift_status
        The :class:`DriftStatus` of the live observation window at evaluation
        time.
    fingerprint_comparison
        The :class:`FingerprintComparison` between baseline and candidate
        fingerprint reports.  INCOMPARABLE is treated as HOLD.
    verdict
        The deployment recommendation (PROMOTE / HOLD / ROLLBACK).
    evaluated_at
        UTC timestamp at which the verdict was produced.
    """

    verdict_id: str
    comparison_id: str
    accuracy_delta: float
    comparison_outcome: ComparisonOutcome
    drift_status: DriftStatus
    fingerprint_comparison: FingerprintComparison
    verdict: CanaryVerdict
    evaluated_at: datetime

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        return {
            "verdict_id": self.verdict_id,
            "comparison_id": self.comparison_id,
            "accuracy_delta": self.accuracy_delta,
            "comparison_outcome": self.comparison_outcome.value,
            "drift_status": self.drift_status.value,
            "fingerprint_comparison": self.fingerprint_comparison.value,
            "verdict": self.verdict.value,
            "evaluated_at": self.evaluated_at.isoformat(),
        }


# Accuracy improvement margin required to PROMOTE over an EQUIVALENT result.
# If the accuracy delta is within equivalence_epsilon of 0.0 (EQUIVALENT by
# the comparator), we HOLD rather than PROMOTE -- there is no evidence the
# candidate is better.
_CANARY_PROMOTE_REQUIRES_IMPROVEMENT: Final[bool] = True


class CanaryEvaluator:
    """Governance-gated canary evaluation gate.

    Combines three signals to produce a :class:`CanaryVerdictReport`:

    1. **Accuracy comparison** -- a
       :class:`~ztax_gateway.evaluation_quality.ComparisonReport` from the
       :class:`~ztax_gateway.evaluation_quality.ModelComparator` that ran the
       baseline and candidate classifiers against the same gold set.
    2. **Live drift** -- a :class:`DriftReport` from the
       :class:`DriftDetector` that has been recording live invocation signals.
    3. **Fingerprint comparison** -- a :class:`FingerprintReport` pair from
       :class:`BehaviorFingerprint` runs of the baseline and candidate.

    Decision table
    --------------
    The verdict is derived deterministically from three Boolean conditions:

    * **safe_accuracy**: ``comparison_outcome`` is IMPROVED or EQUIVALENT
      (i.e. not REGRESSED and not BELOW_THRESHOLD).
    * **improved_accuracy**: ``comparison_outcome`` is IMPROVED (strictly
      better than baseline and above the minimum threshold).
    * **drift_free**: ``drift_status`` is NOMINAL.
    * **fingerprint_stable**: ``fingerprint_comparison`` is EQUIVALENT (not
      DIVERGED and not INCOMPARABLE).

    +------------------+--------------+--------------------+--------------------+-----------+
    | safe_accuracy    | improved     | drift_free         | fingerprint_stable | Verdict   |
    +==================+==============+====================+====================+===========+
    | False            | *            | *                  | *                  | ROLLBACK  |
    +------------------+--------------+--------------------+--------------------+-----------+
    | True             | True         | True               | True               | PROMOTE   |
    +------------------+--------------+--------------------+--------------------+-----------+
    | True             | *            | *                  | *                  | HOLD      |
    +------------------+--------------+--------------------+--------------------+-----------+

    Governance
    ----------
    :meth:`evaluate` calls :func:`~ztax_gateway.governance.authorise` before
    any evaluation logic runs.  There is no ``skip_governance`` parameter.

    Usage
    -----
    ::

        evaluator = CanaryEvaluator(registry=registry, provenance=provenance)
        verdict_report = evaluator.evaluate(
            comparison=comparator_report,
            drift_report=detector.check(),
            baseline_fingerprint=baseline_fp,
            candidate_fingerprint=candidate_fp,
        )
    """

    def __init__(
        self,
        registry: UseCaseRegistry,
        provenance: Provenance,
    ) -> None:
        """Construct the evaluator.

        Parameters
        ----------
        registry:
            The :class:`~ztax_gateway.governance.UseCaseRegistry` used to gate
            every :meth:`evaluate` call.
        provenance:
            The governed context for this canary evaluation run.
        """
        self._registry = registry
        self._provenance = provenance

    def evaluate(
        self,
        comparison: ComparisonReport,
        drift_report: DriftReport,
        baseline_fingerprint: FingerprintReport,
        candidate_fingerprint: FingerprintReport,
    ) -> CanaryVerdictReport:
        """Produce a :class:`CanaryVerdictReport` from the three input signals.

        Governance gate: :func:`~ztax_gateway.governance.authorise` is called
        before any evaluation logic runs.

        Parameters
        ----------
        comparison:
            A :class:`~ztax_gateway.evaluation_quality.ComparisonReport`
            produced by
            :class:`~ztax_gateway.evaluation_quality.ModelComparator` comparing
            the baseline and candidate classifiers on the same gold set.
        drift_report:
            A :class:`DriftReport` from :meth:`DriftDetector.check` reflecting
            the current live observation window.
        baseline_fingerprint:
            A :class:`FingerprintReport` from a :class:`BehaviorFingerprint`
            run of the baseline classifier.
        candidate_fingerprint:
            A :class:`FingerprintReport` from a :class:`BehaviorFingerprint`
            run of the candidate classifier.

        Raises
        ------
        GovernanceRefusedError
            If the provenance fails the governance gate.
        CanaryEvaluatorError
            If ``comparison`` is structurally invalid (e.g. the accuracy delta
            is not finite).
        """
        # Governance gate -- before any work.
        authorise(self._registry, self._provenance)

        # Basic sanity checks on the comparison report.
        import math

        if not math.isfinite(comparison.accuracy_delta):
            raise CanaryEvaluatorError(
                f"canary-evaluator: comparison {comparison.comparison_id!r} "
                f"has non-finite accuracy_delta {comparison.accuracy_delta!r}"
            )

        # Derive the three Boolean conditions.
        safe_accuracy = comparison.outcome not in (
            ComparisonOutcome.REGRESSED,
            ComparisonOutcome.BELOW_THRESHOLD,
        )
        improved_accuracy = comparison.outcome == ComparisonOutcome.IMPROVED
        drift_free = drift_report.status == DriftStatus.NOMINAL
        fp_cmp = compare_fingerprints(baseline_fingerprint, candidate_fingerprint)
        fingerprint_stable = fp_cmp == FingerprintComparison.EQUIVALENT

        # Decision table.
        if not safe_accuracy:
            verdict = CanaryVerdict.ROLLBACK
        elif improved_accuracy and drift_free and fingerprint_stable:
            verdict = CanaryVerdict.PROMOTE
        else:
            verdict = CanaryVerdict.HOLD

        return CanaryVerdictReport(
            verdict_id=str(uuid.uuid4()),
            comparison_id=comparison.comparison_id,
            accuracy_delta=comparison.accuracy_delta,
            comparison_outcome=comparison.outcome,
            drift_status=drift_report.status,
            fingerprint_comparison=fp_cmp,
            verdict=verdict,
            evaluated_at=datetime.now(UTC),
        )
