"""AI Capacity, Quotas and FinOps.

Chapter 17 §23 of the ZoikoTax Master Specification.

Enforces per-use-case token-bucket quotas and monetary spend limits, and
accumulates the fine-grained FinOps ledger that the §22 observability layer
exposes as telemetry signals.  The data source is
:class:`~ztax_gateway.invocation_evidence.InvocationEvidenceRecord`; nothing
here accepts raw prompts or completions.

What this module provides
--------------------------
:class:`QuotaPolicy`
    The quota limits for one use case: maximum tokens per window, maximum
    invocations per window, and maximum monetary spend per window.  All limits
    are optional; a ``None`` limit means unconstrained.

:class:`QuotaWindow`
    The time window over which quota counters are accumulated: ``MINUTE``,
    ``HOUR`` or ``DAY``.

:class:`QuotaState`
    Live counters for one use case in one quota window.  Holds total tokens
    consumed, total invocations, and total monetary spend.

:class:`QuotaViolation`
    Describes a single quota breach: which dimension was exceeded and by how
    much.

:class:`CapacityLedger`
    Accumulates :class:`~ztax_gateway.invocation_evidence.InvocationEvidenceRecord`
    events per use case, enforces the registered :class:`QuotaPolicy`, and
    provides a :meth:`~CapacityLedger.report` for FinOps analysis.

:class:`FinOpsReport`
    A frozen snapshot of spend and usage across all tracked use cases.

:class:`CapacityError`
    Raised when a quota is exceeded or a ledger invariant is broken.

Design rules (enforced here; no exceptions, no overrides)
----------------------------------------------------------
1. **No raw content.**  The ledger consumes only hashes, identifiers and
   numeric counters from :class:`~ztax_gateway.invocation_evidence.InvocationEvidenceRecord`.
2. **Quota check before counter update.**  :meth:`CapacityLedger.consume`
   checks limits first; it never partially updates counters on a violation.
3. **No fiscal imports.**  ADR-0006 §2.6 isolation: nothing from ``fiscal``,
   ``tax_decision`` or ``subledger`` is imported here.
4. **Immutable policy and report.**  :class:`QuotaPolicy` and
   :class:`FinOpsReport` are frozen dataclasses; they cannot be changed after
   registration.
5. **BLOCK invocations do not consume quota.**  A blocked invocation produced
   no model output, incurred no token spend, and must not count against the
   budget.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import Final

from .invocation_evidence import InvocationEvidenceRecord, InvocationOutcome

__all__: list[str] = [
    "CapacityError",
    "CapacityLedger",
    "FinOpsReport",
    "QuotaPolicy",
    "QuotaState",
    "QuotaViolation",
    "QuotaWindow",
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# Sentinel for "no limit registered on this dimension".
_UNLIMITED: Final[int] = 0


# ---------------------------------------------------------------------------
# CapacityError
# ---------------------------------------------------------------------------


class CapacityError(Exception):
    """Raised when a quota is exceeded or a ledger invariant is broken.

    ``reason``     -- a human-readable description of the problem.
    ``use_case``   -- the use case whose quota was breached (may be ``""``
                      when the error is not use-case-specific).
    ``violations`` -- zero or more :class:`QuotaViolation` objects describing
                      each dimension that was exceeded.
    """

    def __init__(
        self,
        reason: str,
        use_case: str = "",
        violations: list[QuotaViolation] | None = None,
    ) -> None:
        super().__init__(reason)
        self.reason = reason
        self.use_case = use_case
        self.violations: list[QuotaViolation] = violations or []


# ---------------------------------------------------------------------------
# QuotaWindow
# ---------------------------------------------------------------------------


class QuotaWindow(StrEnum):
    """The time granularity over which quota counters accumulate.

    ``MINUTE`` -- counters reset every 60 seconds.
    ``HOUR``   -- counters reset every 3 600 seconds.
    ``DAY``    -- counters reset every 86 400 seconds.

    The ledger does not perform wall-clock resets itself; the window is a
    label that an external scheduler or cron job uses to know when to call
    :meth:`CapacityLedger.reset`.
    """

    MINUTE = "MINUTE"
    HOUR = "HOUR"
    DAY = "DAY"


# ---------------------------------------------------------------------------
# QuotaViolation
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class QuotaViolation:
    """One dimension of a quota breach.

    ``dimension``  -- which limit was exceeded: ``"tokens"``, ``"invocations"``
                      or ``"monetary_cost_usd"``.
    ``limit``      -- the registered limit.
    ``actual``     -- the value that would have been reached after consuming
                      the current record.
    ``use_case``   -- the use case whose budget was breached.
    """

    dimension: str
    limit: float
    actual: float
    use_case: str

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation."""
        return {
            "dimension": self.dimension,
            "limit": self.limit,
            "actual": self.actual,
            "use_case": self.use_case,
        }


# ---------------------------------------------------------------------------
# QuotaPolicy
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class QuotaPolicy:
    """The quota limits for one registered use case.

    All three limits are optional (``None`` means unconstrained).

    ``use_case_id``          -- must match
                                :attr:`~ztax_gateway.invocation_evidence.InvocationEvidenceRecord.caller_security_context_ref`.
    ``window``               -- the :class:`QuotaWindow` these limits apply to.
    ``max_tokens``           -- maximum total tokens (input + output) per window.
    ``max_invocations``      -- maximum invocation count per window.
    ``max_monetary_cost_usd``-- maximum monetary spend (USD) per window.
    """

    use_case_id: str
    window: QuotaWindow = QuotaWindow.HOUR

    max_tokens: int | None = None
    max_invocations: int | None = None
    max_monetary_cost_usd: float | None = None

    def __post_init__(self) -> None:
        if not self.use_case_id:
            raise CapacityError("capacity: QuotaPolicy requires a non-empty use_case_id")
        if self.max_tokens is not None and self.max_tokens < 1:
            raise CapacityError(
                f"capacity: QuotaPolicy max_tokens must be >= 1, got {self.max_tokens!r}"
            )
        if self.max_invocations is not None and self.max_invocations < 1:
            raise CapacityError(
                f"capacity: QuotaPolicy max_invocations must be >= 1, "
                f"got {self.max_invocations!r}"
            )
        if self.max_monetary_cost_usd is not None and self.max_monetary_cost_usd <= 0.0:
            raise CapacityError(
                f"capacity: QuotaPolicy max_monetary_cost_usd must be > 0.0, "
                f"got {self.max_monetary_cost_usd!r}"
            )

    @property
    def is_unconstrained(self) -> bool:
        """``True`` when no limit is set on any dimension."""
        return (
            self.max_tokens is None
            and self.max_invocations is None
            and self.max_monetary_cost_usd is None
        )


# ---------------------------------------------------------------------------
# QuotaState
# ---------------------------------------------------------------------------


@dataclass(slots=True)
class QuotaState:
    """Live counters for one use case in the current quota window.

    All counters start at zero.  :meth:`CapacityLedger.consume` increments
    them after the quota check passes.  :meth:`CapacityLedger.reset` zeroes
    them.

    ``use_case_id``       -- the use case these counters belong to.
    ``total_tokens``      -- cumulative input + output tokens consumed.
    ``total_invocations`` -- cumulative invocation count.
    ``total_cost_usd``    -- cumulative monetary spend in USD.
    ``blocked_count``     -- invocations that were blocked (not counted
                             against budget, tracked for visibility).
    """

    use_case_id: str
    total_tokens: int = 0
    total_invocations: int = 0
    total_cost_usd: float = 0.0
    blocked_count: int = 0

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe snapshot."""
        return {
            "use_case_id": self.use_case_id,
            "total_tokens": self.total_tokens,
            "total_invocations": self.total_invocations,
            "total_cost_usd": self.total_cost_usd,
            "blocked_count": self.blocked_count,
        }


# ---------------------------------------------------------------------------
# FinOpsReport
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class FinOpsReport:
    """A frozen FinOps snapshot across all tracked use cases.

    ``states``             -- per-use-case :class:`QuotaState` snapshots,
                              keyed by use_case_id.
    ``total_tokens``       -- sum of tokens across all use cases.
    ``total_invocations``  -- sum of invocations across all use cases.
    ``total_cost_usd``     -- sum of monetary spend across all use cases.
    ``total_blocked``      -- sum of blocked invocations (zero-cost).
    ``use_case_count``     -- number of distinct use cases in the ledger.
    """

    states: dict[str, QuotaState]
    total_tokens: int
    total_invocations: int
    total_cost_usd: float
    total_blocked: int
    use_case_count: int

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation."""
        return {
            "total_tokens": self.total_tokens,
            "total_invocations": self.total_invocations,
            "total_cost_usd": self.total_cost_usd,
            "total_blocked": self.total_blocked,
            "use_case_count": self.use_case_count,
            "states": {k: v.as_dict() for k, v in self.states.items()},
        }


# ---------------------------------------------------------------------------
# CapacityLedger
# ---------------------------------------------------------------------------


class CapacityLedger:
    """Accumulates usage per use case, enforces quotas, and reports FinOps.

    Usage::

        ledger = CapacityLedger()
        ledger.register(QuotaPolicy(
            use_case_id="change-intelligence",
            window=QuotaWindow.HOUR,
            max_tokens=1_000_000,
            max_invocations=500,
            max_monetary_cost_usd=50.0,
        ))
        ledger.consume(evidence_record)  # raises CapacityError on quota breach
        report = ledger.report()

    Rules
    -----
    * :meth:`consume` checks all quota dimensions *before* incrementing any
      counter -- it never partially applies an update.
    * BLOCK invocations (``invocation_outcome == BLOCK``) do not consume quota
      but are tracked in :attr:`QuotaState.blocked_count`.
    * A use case without a registered :class:`QuotaPolicy` may still be
      consumed; it simply has no quota enforcement.
    * :meth:`reset` zeroes counters for one or all use cases; it does not
      remove the registered policy.
    """

    def __init__(self) -> None:
        self._policies: dict[str, QuotaPolicy] = {}
        self._states: dict[str, QuotaState] = {}

    # ---- policy registration ----------------------------------------

    def register(self, policy: QuotaPolicy) -> None:
        """Register a :class:`QuotaPolicy` for a use case.

        Re-registering the same ``use_case_id`` replaces the previous policy.
        """
        if not isinstance(policy, QuotaPolicy):
            raise CapacityError("capacity: register requires a QuotaPolicy")
        self._policies[policy.use_case_id] = policy
        # Ensure a QuotaState exists, preserving existing counters if any.
        if policy.use_case_id not in self._states:
            self._states[policy.use_case_id] = QuotaState(use_case_id=policy.use_case_id)

    def policy(self, use_case_id: str) -> QuotaPolicy | None:
        """Return the registered policy for *use_case_id*, or ``None``."""
        return self._policies.get(use_case_id)

    def state(self, use_case_id: str) -> QuotaState | None:
        """Return live counters for *use_case_id*, or ``None``."""
        return self._states.get(use_case_id)

    # ---- consume --------------------------------------------------------

    def consume(self, record: InvocationEvidenceRecord) -> None:
        """Record usage from *record* and enforce quota.

        Parameters
        ----------
        record:
            The evidence record to account.

        Raises
        ------
        CapacityError
            If one or more quota limits would be exceeded.  The counters are
            not modified when this is raised.
        """
        if not isinstance(record, InvocationEvidenceRecord):
            raise CapacityError("capacity: consume requires an InvocationEvidenceRecord")

        use_case_id = record.caller_security_context_ref
        is_block = record.invocation_outcome is InvocationOutcome.BLOCK

        # Ensure a state record exists (even without a policy).
        if use_case_id not in self._states:
            self._states[use_case_id] = QuotaState(use_case_id=use_case_id)

        state = self._states[use_case_id]

        # BLOCK invocations do not consume quota (design rule 5).
        if is_block:
            state.blocked_count += 1
            return

        # Values that would be reached after this record.
        new_tokens = state.total_tokens + record.usage.total_tokens
        new_invocations = state.total_invocations + 1
        new_cost = state.total_cost_usd + record.usage.monetary_cost_usd

        # Check all quota dimensions before touching any counter.
        policy = self._policies.get(use_case_id)
        violations: list[QuotaViolation] = []
        if policy is not None:
            if policy.max_tokens is not None and new_tokens > policy.max_tokens:
                violations.append(
                    QuotaViolation(
                        dimension="tokens",
                        limit=float(policy.max_tokens),
                        actual=float(new_tokens),
                        use_case=use_case_id,
                    )
                )
            if (
                policy.max_invocations is not None
                and new_invocations > policy.max_invocations
            ):
                violations.append(
                    QuotaViolation(
                        dimension="invocations",
                        limit=float(policy.max_invocations),
                        actual=float(new_invocations),
                        use_case=use_case_id,
                    )
                )
            if (
                policy.max_monetary_cost_usd is not None
                and new_cost > policy.max_monetary_cost_usd
            ):
                violations.append(
                    QuotaViolation(
                        dimension="monetary_cost_usd",
                        limit=policy.max_monetary_cost_usd,
                        actual=new_cost,
                        use_case=use_case_id,
                    )
                )

        if violations:
            dims = ", ".join(v.dimension for v in violations)
            raise CapacityError(
                f"capacity: quota exceeded for {use_case_id!r} on [{dims}]",
                use_case=use_case_id,
                violations=violations,
            )

        # All checks passed -- update counters atomically.
        state.total_tokens = new_tokens
        state.total_invocations = new_invocations
        state.total_cost_usd = new_cost

    # ---- reset ----------------------------------------------------------

    def reset(self, use_case_id: str | None = None) -> None:
        """Zero the counters for one use case, or for all if *use_case_id* is ``None``.

        Does not remove the registered policy.
        """
        if use_case_id is None:
            for state in self._states.values():
                state.total_tokens = 0
                state.total_invocations = 0
                state.total_cost_usd = 0.0
                state.blocked_count = 0
        else:
            st = self._states.get(use_case_id)
            if st is not None:
                st.total_tokens = 0
                st.total_invocations = 0
                st.total_cost_usd = 0.0
                st.blocked_count = 0

    # ---- report ---------------------------------------------------------

    def report(self) -> FinOpsReport:
        """Return a frozen :class:`FinOpsReport` snapshot.

        The snapshot is independent of the ledger; subsequent :meth:`consume`
        calls do not affect it.
        """
        import copy

        states_snapshot = {k: copy.copy(v) for k, v in self._states.items()}
        total_tokens = sum(s.total_tokens for s in states_snapshot.values())
        total_invocations = sum(s.total_invocations for s in states_snapshot.values())
        total_cost = sum(s.total_cost_usd for s in states_snapshot.values())
        total_blocked = sum(s.blocked_count for s in states_snapshot.values())
        return FinOpsReport(
            states=states_snapshot,
            total_tokens=total_tokens,
            total_invocations=total_invocations,
            total_cost_usd=total_cost,
            total_blocked=total_blocked,
            use_case_count=len(states_snapshot),
        )
