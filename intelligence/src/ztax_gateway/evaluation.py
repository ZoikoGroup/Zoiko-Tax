"""Evaluation Service.

Chapter 17 §17 of the ZoikoTax Master Specification.

Every AI output that claims advisory or actioning authority (A2+) must pass
an evaluation before it can be recorded as a ``ProvenanceSpan`` or committed
to the fiscal subledger.  This module provides that structured quality gate:

1. **Rules** — an :class:`EvaluationRule` is a named, versioned predicate that
   accepts a :class:`~ztax_gateway.classifier.ClassificationRecord` and returns
   a :class:`RuleOutcome` (PASS / FAIL / SKIP).  Rules are pure functions with
   no side effects; they read the record and return a verdict.

2. **Ruleset** — an :class:`EvaluationRuleset` is an ordered, named collection
   of rules.  Rulesets are immutable once built.  Two rulesets with the same
   name and version must carry identical rules (the constructor enforces this).

3. **Evaluator** — :func:`evaluate` runs every rule against a record and
   returns an :class:`EvaluationReport`.  The report is the audit artefact: it
   carries a pass/fail verdict per rule, an overall :class:`EvaluationVerdict`,
   and a :class:`~ztax_gateway.citation.Citation` of the record's own top
   citation so the evaluation can be traced back to the source material.

4. **Governance gate** — :func:`evaluate` accepts the same
   ``(registry, provenance)`` pair as every other AI-plane entry point.
   Evaluation is itself an AI action and must travel on a governed context.

Design rules
------------
* **Rules are additive.** Adding a rule to a ruleset is a version increment,
  not a schema change.  The ``version`` field on :class:`EvaluationRuleset`
  makes this explicit.
* **SKIP is not PASS.** A skipped rule contributes nothing to the pass count
  but does not fail the evaluation.  This lets rules declare their own
  applicability (e.g. "only applies to COMMIT-class outputs") without the
  ruleset needing to know about the rule's domain.
* **A single FAIL fails the evaluation.** An evaluation is a gate, not a
  score.  Partial compliance is not compliance.
* **Immutable report.** ``frozen=True`` throughout — the report is an evidence
  record and must not be mutated after it is issued.
* **No fiscal imports.** This module imports nothing from the fiscal, tax or
  subledger packages (ADR-0006 §2.6).
"""

from __future__ import annotations

import hashlib
import uuid
from collections.abc import Callable
from dataclasses import dataclass, field
from datetime import UTC, datetime
from enum import StrEnum
from typing import Final

from .classifier import ClassificationRecord
from .governance import UseCaseRegistry, authorise
from .provenance import Provenance

__all__: list[str] = [
    "EvaluationError",
    "EvaluationReport",
    "EvaluationRuleset",
    "EvaluationVerdict",
    "Evaluator",
    "RuleOutcome",
    "RuleResult",
    "evaluate",
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_REPORT_ID_PREFIX_LEN: Final[int] = 16  # hex chars in the short report_id


# ---------------------------------------------------------------------------
# Enumerations
# ---------------------------------------------------------------------------


class RuleOutcome(StrEnum):
    """The verdict a single evaluation rule returns.

    ``PASS`` — the record satisfies the rule.
    ``FAIL`` — the record violates the rule.
    ``SKIP`` — the rule declares itself inapplicable for this record.
           A skipped rule does not contribute to the pass count and does
           not fail the evaluation.
    """

    PASS = "PASS"
    FAIL = "FAIL"
    SKIP = "SKIP"


class EvaluationVerdict(StrEnum):
    """The overall verdict of an :class:`EvaluationReport`.

    ``PASS`` — every applicable rule passed.
    ``FAIL`` — at least one rule failed.
    ``INCONCLUSIVE`` — the ruleset contained no applicable rules (all SKIP).
    """

    PASS = "PASS"
    FAIL = "FAIL"
    INCONCLUSIVE = "INCONCLUSIVE"


# ---------------------------------------------------------------------------
# Errors
# ---------------------------------------------------------------------------


class EvaluationError(Exception):
    """Raised when evaluation cannot proceed.

    Distinct from :class:`~ztax_gateway.governance.GovernanceRefusedError` so
    callers can handle governance refusals separately from evaluator-level
    precondition failures (empty ruleset, duplicate rule name, etc.).
    """

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------------------
# Rule type
# ---------------------------------------------------------------------------

# A rule is a callable that accepts a ClassificationRecord and returns an
# outcome.  It is a plain callable rather than a class so that simple rules
# can be written as one-line lambdas in tests, and complex ones as classes
# that implement __call__.
RuleCallable = Callable[[ClassificationRecord], RuleOutcome]


# ---------------------------------------------------------------------------
# Data structures
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class EvaluationRule:
    """A named, versioned predicate over a :class:`~ztax_gateway.classifier.ClassificationRecord`.

    ``rule_id`` is a stable identifier used in reports and logs.  It must be
    unique within its :class:`EvaluationRuleset`.

    ``description`` is a human-readable statement of what the rule checks.
    It appears in the :class:`EvaluationReport` so a reviewer can understand
    a FAIL without reading the rule code.

    ``predicate`` is the callable that performs the check.  It must be pure:
    no side effects, no I/O, no model calls.  The evaluator calls it exactly
    once per record per evaluation run.
    """

    rule_id: str
    description: str
    predicate: RuleCallable

    def __post_init__(self) -> None:
        if not self.rule_id:
            raise EvaluationError("evaluation: rule has no identifier")
        if not self.description:
            raise EvaluationError(f"evaluation: rule {self.rule_id!r} has no description")


@dataclass(frozen=True, slots=True)
class RuleResult:
    """The outcome of one rule applied to one record.

    ``rule_id`` identifies the rule (mirrors :attr:`EvaluationRule.rule_id`).
    ``outcome`` is the :class:`RuleOutcome` the rule returned.
    ``detail`` is an optional free-text reason, useful for FAIL verdicts.
    """

    rule_id: str
    outcome: RuleOutcome
    detail: str = ""


@dataclass(frozen=True, slots=True)
class EvaluationReport:
    """The full audit artefact for one evaluation run.

    ``report_id`` is a short, unique identifier for this report (hex prefix of
    a UUID4, stable within a single run but not across runs — use
    ``record_citation_id`` for cross-run traceability).

    ``record_citation_id`` is the ``citation_id`` of the record's top
    citation.  It links this report back to the source material the
    record was grounded in.

    ``ruleset_name`` and ``ruleset_version`` identify which ruleset was
    applied, so a report can be reproduced by re-running the same ruleset
    version against the same record.

    ``results`` is the per-rule outcome list, in the order the rules were
    evaluated.

    ``verdict`` is the overall :class:`EvaluationVerdict`.

    ``evaluated_at`` is the UTC timestamp of the evaluation.
    """

    report_id: str
    record_citation_id: str | None
    ruleset_name: str
    ruleset_version: str
    results: tuple[RuleResult, ...]
    verdict: EvaluationVerdict
    evaluated_at: datetime

    def pass_count(self) -> int:
        """Number of rules that returned PASS."""
        return sum(1 for r in self.results if r.outcome is RuleOutcome.PASS)

    def fail_count(self) -> int:
        """Number of rules that returned FAIL."""
        return sum(1 for r in self.results if r.outcome is RuleOutcome.FAIL)

    def skip_count(self) -> int:
        """Number of rules that returned SKIP."""
        return sum(1 for r in self.results if r.outcome is RuleOutcome.SKIP)

    def failed_rules(self) -> list[str]:
        """Return rule IDs whose outcome was FAIL, in evaluation order."""
        return [r.rule_id for r in self.results if r.outcome is RuleOutcome.FAIL]

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation for the audit trail."""
        return {
            "report_id": self.report_id,
            "record_citation_id": self.record_citation_id,
            "ruleset_name": self.ruleset_name,
            "ruleset_version": self.ruleset_version,
            "verdict": self.verdict.value,
            "pass_count": self.pass_count(),
            "fail_count": self.fail_count(),
            "skip_count": self.skip_count(),
            "failed_rules": self.failed_rules(),
            "evaluated_at": self.evaluated_at.isoformat(),
        }


# ---------------------------------------------------------------------------
# Ruleset
# ---------------------------------------------------------------------------


@dataclass
class EvaluationRuleset:
    """An ordered, named, immutable collection of :class:`EvaluationRule` objects.

    ``name`` is a stable identifier (e.g. ``"sku-classification-v1"``).
    ``version`` is a semantic version string (e.g. ``"1.0.0"``).

    Once built, the ruleset is immutable: rules cannot be added or removed.
    This preserves the invariant that two rulesets with the same name+version
    carry identical rules, so a report can be reproduced by re-running the
    same ruleset version.

    Example::

        from ztax_gateway.evaluation import EvaluationRule, EvaluationRuleset, RuleOutcome

        ruleset = EvaluationRuleset(
            name="sku-classification-v1",
            version="1.0.0",
            rules=[
                EvaluationRule(
                    rule_id="has-proposals",
                    description="Record must contain at least one classification proposal.",
                    predicate=lambda rec: RuleOutcome.PASS if rec.proposals else RuleOutcome.FAIL,
                ),
                EvaluationRule(
                    rule_id="citation-verifies",
                    description="Top proposal citation must verify its own hash.",
                    predicate=lambda rec: (
                        RuleOutcome.PASS
                        if rec.top() and rec.top().citation.verify_all()  # type: ignore[union-attr]
                        else RuleOutcome.SKIP if not rec.top()
                        else RuleOutcome.FAIL
                    ),
                ),
            ],
        )
    """

    name: str
    version: str
    _rules: list[EvaluationRule] = field(default_factory=list, init=False, repr=False)
    _rule_ids: set[str] = field(default_factory=set, init=False, repr=False)
    _frozen: bool = field(default=False, init=False, repr=False)

    def __init__(self, name: str, version: str, rules: list[EvaluationRule] | None = None) -> None:
        if not name:
            raise EvaluationError("evaluation: ruleset has no name")
        if not version:
            raise EvaluationError(f"evaluation: ruleset {name!r} has no version")
        self.name = name
        self.version = version
        self._rules = []
        self._rule_ids = set()
        self._frozen = False
        for rule in rules or []:
            self._add(rule)
        self._frozen = True

    def _add(self, rule: EvaluationRule) -> None:
        if self._frozen:
            raise EvaluationError(
                f"evaluation: ruleset {self.name!r} is immutable "
                "— rules cannot be added after construction"
            )
        if rule.rule_id in self._rule_ids:
            raise EvaluationError(
                f"evaluation: rule {rule.rule_id!r} already registered in ruleset {self.name!r}"
            )
        self._rules.append(rule)
        self._rule_ids.add(rule.rule_id)

    @property
    def rules(self) -> tuple[EvaluationRule, ...]:
        """The ordered rules in this ruleset (immutable view)."""
        return tuple(self._rules)

    @property
    def rule_count(self) -> int:
        """Number of rules in this ruleset."""
        return len(self._rules)

    def get(self, rule_id: str) -> EvaluationRule | None:
        """Return the rule with the given ID, or ``None``."""
        for r in self._rules:
            if r.rule_id == rule_id:
                return r
        return None


# ---------------------------------------------------------------------------
# Core evaluation function
# ---------------------------------------------------------------------------


def _derive_report_id(record: ClassificationRecord, ruleset: EvaluationRuleset) -> str:
    """Derive a short, stable-ish report ID from the record + ruleset identity."""
    material = f"{record.sku_description}\0{ruleset.name}\0{ruleset.version}".encode()
    # Mix in a UUID so two evaluations of the same record+ruleset get distinct IDs.
    uid = uuid.uuid4().bytes
    digest = hashlib.sha256(material + uid).hexdigest()
    return digest[:_REPORT_ID_PREFIX_LEN]


def _overall_verdict(results: list[RuleResult]) -> EvaluationVerdict:
    if any(r.outcome is RuleOutcome.FAIL for r in results):
        return EvaluationVerdict.FAIL
    if all(r.outcome is RuleOutcome.SKIP for r in results):
        return EvaluationVerdict.INCONCLUSIVE
    return EvaluationVerdict.PASS


def evaluate(
    record: ClassificationRecord,
    ruleset: EvaluationRuleset,
    registry: UseCaseRegistry,
    provenance: Provenance,
) -> EvaluationReport:
    """Run *ruleset* against *record* and return an :class:`EvaluationReport`.

    Governance gate: *provenance* is passed to
    :func:`~ztax_gateway.governance.authorise` before any rule is run.
    Evaluation is an AI action and must travel on a governed context.

    The rules in *ruleset* are run in registration order.  Every rule is run;
    there is no short-circuit on FAIL.  This ensures the report always reflects
    the full state of the record and an operator can see all failures at once.

    Args:
        record: The :class:`~ztax_gateway.classifier.ClassificationRecord` to
            evaluate.
        ruleset: The :class:`EvaluationRuleset` to apply.
        registry: The :class:`~ztax_gateway.governance.UseCaseRegistry` that
            holds use-case registrations and kill switches.
        provenance: The governance context.  Must be a registered, non-killed
            use case.

    Returns:
        An :class:`EvaluationReport` with a per-rule :class:`RuleResult` and
        an overall :class:`EvaluationVerdict`.

    Raises:
        GovernanceRefusedError: if *provenance* is not authorised.
        EvaluationError: if *ruleset* contains no rules.
    """
    # 1. Governance gate — before any work.
    authorise(registry, provenance)

    # 2. Validate the ruleset is usable.
    if ruleset.rule_count == 0:
        raise EvaluationError(
            f"evaluation: ruleset {ruleset.name!r} v{ruleset.version} contains no rules"
        )

    # 3. Run every rule; capture results.
    results: list[RuleResult] = []
    for rule in ruleset.rules:
        try:
            outcome = rule.predicate(record)
        except Exception as exc:
            # A rule that raises is treated as FAIL with the exception as detail.
            results.append(
                RuleResult(
                    rule_id=rule.rule_id,
                    outcome=RuleOutcome.FAIL,
                    detail=f"rule raised unexpectedly: {exc}",
                )
            )
            continue
        results.append(RuleResult(rule_id=rule.rule_id, outcome=outcome))

    # 4. Derive top citation ID for traceability.
    top = record.top()
    record_citation_id: str | None = top.citation.citation_id if top is not None else None

    return EvaluationReport(
        report_id=_derive_report_id(record, ruleset),
        record_citation_id=record_citation_id,
        ruleset_name=ruleset.name,
        ruleset_version=ruleset.version,
        results=tuple(results),
        verdict=_overall_verdict(results),
        evaluated_at=datetime.now(tz=UTC),
    )


# ---------------------------------------------------------------------------
# Evaluator — stateful wrapper for a fixed ruleset
# ---------------------------------------------------------------------------


@dataclass
class Evaluator:
    """Stateful wrapper that binds a :class:`EvaluationRuleset` to an entry point.

    Prefer :func:`evaluate` for one-off evaluations.  Use :class:`Evaluator`
    when the same ruleset is applied to many records in sequence (e.g. in a
    batch pipeline or a test harness), so the ruleset does not have to be
    passed on every call.

    Example::

        evaluator = Evaluator(ruleset=my_ruleset)
        report = evaluator.run(record, registry=registry, provenance=provenance)
    """

    ruleset: EvaluationRuleset

    def run(
        self,
        record: ClassificationRecord,
        registry: UseCaseRegistry,
        provenance: Provenance,
    ) -> EvaluationReport:
        """Evaluate *record* with the bound ruleset.  See :func:`evaluate`."""
        return evaluate(record, self.ruleset, registry, provenance)
