"""AI Release Manifest Lifecycle State Machine.

Chapter 17 §29 of the ZoikoTax Master Specification.

Every AIReleaseManifest moves through a defined sequence of lifecycle states
before it is allowed to serve production traffic, and through terminal states
when it is retired or suspended.  This module implements that machine.

Nine states
-----------
Pre-production (non-authoritative)::

    RESEARCH -> DESIGN -> VALIDATION -> SHADOW -> PILOT -> PRODUCTION
                                                          |          |
                                                     DEGRADED   SUSPENDED
                                                          |          |
                                                           RETIRED (terminal)

The transitions that are allowed:

    RESEARCH    -> DESIGN
    DESIGN      -> VALIDATION
    VALIDATION  -> SHADOW
    SHADOW      -> PILOT
    PILOT       -> PRODUCTION
    PRODUCTION  -> DEGRADED      (platform detects a quality regression)
    PRODUCTION  -> SUSPENDED     (operator takes it out of service)
    DEGRADED    -> PRODUCTION    (regression resolved, reinstated)
    DEGRADED    -> SUSPENDED     (regression unresolvable)
    SUSPENDED   -> PRODUCTION    (after a formal review cycle)
    SUSPENDED   -> RETIRED       (manifest permanently decommissioned)
    PRODUCTION  -> RETIRED       (orderly end-of-life)
    Any state   -> RETIRED       (emergency decommission)

Each state carries a **traffic authority rule** enforced at query time:

    RESEARCH    -- no production authority; may not see live traffic at all.
    DESIGN      -- no production authority; may not see live traffic.
    VALIDATION  -- no production authority; may not see live traffic.
    SHADOW      -- may observe live production traffic (read/log only); can
                   never take an authoritative action on a live request.
    PILOT       -- may serve a declared cohort of live traffic; the manifest
                   must still be the exact approved version.
    PRODUCTION  -- may serve all live traffic; manifest must be approved.
    DEGRADED    -- continues to serve (degraded quality acknowledged); runtime
                   must emit a DEGRADED telemetry flag on every invocation.
    SUSPENDED   -- may not serve any live traffic; runtime must refuse.
    RETIRED     -- may not serve any live traffic; evidence is kept forever.

Design rules
------------
* No live model calls.
* No fiscal imports (ADR-0006 §2.6).
* ManifestLifecycle is governance-gated: ``authorise()`` fires as step 1 on
  every state-changing operation.
* ``LifecycleRecord`` is frozen=True -- the audit trail entry is immutable.
* Terminal state: once RETIRED, the manifest cannot be transitioned further.
"""

from __future__ import annotations

import uuid
from dataclasses import dataclass
from datetime import UTC, datetime
from enum import StrEnum
from typing import Final

from .governance import UseCaseRegistry, authorise
from .production_registries import AIReleaseManifest, ManifestRegistry, ManifestStatus
from .provenance import Provenance

__all__: list[str] = [
    "LifecycleAwareManifestRegistry",
    "LifecycleError",
    "LifecycleRecord",
    "LifecycleState",
    "ManifestLifecycle",
    "TrafficAuthority",
    "can_serve_production",
]


# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# States from which the manifest may take an *authoritative* action.
_AUTHORITATIVE_STATES: Final[frozenset[str]] = frozenset(
    {"PILOT", "PRODUCTION", "DEGRADED"}
)

# Terminal state -- no transition is allowed once reached.
_TERMINAL_STATE: Final[str] = "RETIRED"

# Allowed transitions: {from_state: frozenset of valid to_states}.
_ALLOWED_TRANSITIONS: Final[dict[str, frozenset[str]]] = {
    "RESEARCH": frozenset({"DESIGN", "RETIRED"}),
    "DESIGN": frozenset({"VALIDATION", "RETIRED"}),
    "VALIDATION": frozenset({"SHADOW", "RETIRED"}),
    "SHADOW": frozenset({"PILOT", "RETIRED"}),
    "PILOT": frozenset({"PRODUCTION", "RETIRED"}),
    "PRODUCTION": frozenset({"DEGRADED", "SUSPENDED", "RETIRED"}),
    "DEGRADED": frozenset({"PRODUCTION", "SUSPENDED", "RETIRED"}),
    "SUSPENDED": frozenset({"PRODUCTION", "RETIRED"}),
    "RETIRED": frozenset(),  # terminal -- no further transitions
}

# States that require a non-empty rationale.
_RATIONALE_REQUIRED: Final[frozenset[str]] = frozenset(
    {"DEGRADED", "SUSPENDED", "RETIRED"}
)


# ---------------------------------------------------------------------------
# Enumerations
# ---------------------------------------------------------------------------


class LifecycleState(StrEnum):
    """The nine lifecycle states an AIReleaseManifest can occupy.

    RESEARCH
        Manifest is in the research / ideation phase.  No authority to
        observe or act on production traffic.

    DESIGN
        Manifest is being designed (model + prompt + tool policy choices
        under evaluation).  No authority to observe or act on production
        traffic.

    VALIDATION
        Manifest is running against the declared EvaluationProfile on
        gold-set data.  No authority to observe or act on production traffic.

    SHADOW
        Manifest may observe live production requests (for logging and
        comparison purposes) but **can never take an authoritative action**
        on them.  Shadow results must not reach customers or affect any
        downstream determination.

    PILOT
        Manifest may serve a declared cohort of live production traffic with
        full authority, but must still be the exact approved version.

    PRODUCTION
        Manifest is in full production service.  All invocations are
        governed by the approved manifest.

    DEGRADED
        A quality regression has been detected.  The manifest continues to
        serve (there is no better option) but every invocation must carry a
        ``DEGRADED`` telemetry flag.

    SUSPENDED
        The operator has taken the manifest out of service.  No live traffic
        may be served.  The manifest may be reinstated to PRODUCTION after a
        formal review, or retired.

    RETIRED
        The manifest has been permanently decommissioned.  No new invocations
        are accepted.  The evidence record is kept for the audit trail.
        Terminal -- no further transitions are permitted.
    """

    RESEARCH = "RESEARCH"
    DESIGN = "DESIGN"
    VALIDATION = "VALIDATION"
    SHADOW = "SHADOW"
    PILOT = "PILOT"
    PRODUCTION = "PRODUCTION"
    DEGRADED = "DEGRADED"
    SUSPENDED = "SUSPENDED"
    RETIRED = "RETIRED"


class TrafficAuthority(StrEnum):
    """What authority the manifest has over live traffic at its current state.

    NONE
        The manifest may not interact with live traffic in any way.

    OBSERVE_ONLY
        The manifest may observe (shadow) live requests but cannot take
        any action that reaches a customer or downstream system.

    AUTHORITATIVE
        The manifest may take authoritative actions on live traffic.

    AUTHORITATIVE_DEGRADED
        Same as AUTHORITATIVE but every invocation must emit a DEGRADED
        telemetry flag so downstream systems and SRE tooling are aware.
    """

    NONE = "NONE"
    OBSERVE_ONLY = "OBSERVE_ONLY"
    AUTHORITATIVE = "AUTHORITATIVE"
    AUTHORITATIVE_DEGRADED = "AUTHORITATIVE_DEGRADED"


# ---------------------------------------------------------------------------
# Error
# ---------------------------------------------------------------------------


class LifecycleError(Exception):
    """Raised when a lifecycle operation cannot proceed.

    Distinct from GovernanceRefusedError (governance refusals) so callers
    can route each failure type appropriately.
    """

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------------------
# Lifecycle Record (immutable audit trail entry)
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class LifecycleRecord:
    """Immutable audit-trail entry for a single lifecycle state transition.

    Attributes
    ----------
    record_id:
        Stable, opaque identifier for this record.  Auto-generated.
    manifest_id:
        The AIReleaseManifest this transition applies to.
    from_state:
        The state the manifest was in *before* this transition.
    to_state:
        The state the manifest is in *after* this transition.
    operator_id:
        Identity of the operator that triggered the transition.
    transitioned_at:
        UTC timestamp when the transition was applied.
    rationale:
        Optional human-readable explanation.
    """

    record_id: str
    manifest_id: str
    from_state: LifecycleState
    to_state: LifecycleState
    operator_id: str
    transitioned_at: datetime
    rationale: str

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe, JSON-serialisable representation."""
        return {
            "record_id": self.record_id,
            "manifest_id": self.manifest_id,
            "from_state": self.from_state.value,
            "to_state": self.to_state.value,
            "operator_id": self.operator_id,
            "transitioned_at": self.transitioned_at.isoformat(),
            "rationale": self.rationale,
        }


# ---------------------------------------------------------------------------
# Traffic authority helpers
# ---------------------------------------------------------------------------


def can_serve_production(state: LifecycleState) -> bool:
    """Return True if ``state`` allows the manifest to take authoritative actions.

    Pure helper -- no governance required.

    Parameters
    ----------
    state:
        The current LifecycleState of the manifest.

    Returns
    -------
    bool
        True for PILOT, PRODUCTION, and DEGRADED; False for all others.
    """
    return state.value in _AUTHORITATIVE_STATES


def _traffic_authority(state: LifecycleState) -> TrafficAuthority:
    """Map a LifecycleState to its TrafficAuthority."""
    match state:
        case (
            LifecycleState.RESEARCH
            | LifecycleState.DESIGN
            | LifecycleState.VALIDATION
            | LifecycleState.SUSPENDED
            | LifecycleState.RETIRED
        ):
            return TrafficAuthority.NONE
        case LifecycleState.SHADOW:
            return TrafficAuthority.OBSERVE_ONLY
        case LifecycleState.PILOT | LifecycleState.PRODUCTION:
            return TrafficAuthority.AUTHORITATIVE
        case LifecycleState.DEGRADED:
            return TrafficAuthority.AUTHORITATIVE_DEGRADED


# ---------------------------------------------------------------------------
# ManifestLifecycle
# ---------------------------------------------------------------------------


class ManifestLifecycle:
    """Track and advance the lifecycle state of an AIReleaseManifest.

    ManifestLifecycle is governance-gated: every state-changing method
    calls ``authorise()`` as its first action.

    The lifecycle is initialised at LifecycleState.RESEARCH.  Use
    ``advance()`` for sequential pipeline moves, and the named helpers
    (``degrade``, ``suspend``, ``reinstate``, ``retire``) for the
    operational state changes.

    Example::

        lifecycle = ManifestLifecycle(manifest)

        lifecycle.advance(reg, prov, "alice", "design approved")
        lifecycle.advance(reg, prov, "alice", "gold-set eval passed")
        lifecycle.advance(reg, prov, "alice", "shadow period complete")
        lifecycle.advance(reg, prov, "alice", "pilot cohort satisfied")
        lifecycle.advance(reg, prov, "alice", "full rollout approved")

        assert lifecycle.state is LifecycleState.PRODUCTION
        assert lifecycle.traffic_authority is TrafficAuthority.AUTHORITATIVE
    """

    def __init__(self, manifest: AIReleaseManifest) -> None:
        if not manifest.manifest_id:
            raise LifecycleError(
                "release-lifecycle: manifest has no manifest_id"
            )
        self._manifest = manifest
        self._state = LifecycleState.RESEARCH
        self._history: list[LifecycleRecord] = []

    # ------------------------------------------------------------------
    # Read-only properties
    # ------------------------------------------------------------------

    @property
    def manifest(self) -> AIReleaseManifest:
        """The manifest this lifecycle tracks."""
        return self._manifest

    @property
    def state(self) -> LifecycleState:
        """The current LifecycleState."""
        return self._state

    @property
    def traffic_authority(self) -> TrafficAuthority:
        """The TrafficAuthority at the current state."""
        return _traffic_authority(self._state)

    @property
    def history(self) -> list[LifecycleRecord]:
        """Ordered list of LifecycleRecord entries (oldest first)."""
        return list(self._history)

    @property
    def is_retired(self) -> bool:
        """True when the manifest is in the terminal RETIRED state."""
        return self._state is LifecycleState.RETIRED

    # ------------------------------------------------------------------
    # Core transition primitive (internal)
    # ------------------------------------------------------------------

    def _transition(
        self,
        target: LifecycleState,
        registry: UseCaseRegistry,
        provenance: Provenance,
        operator_id: str,
        rationale: str,
    ) -> LifecycleRecord:
        """Governance-gated state transition.

        Raises
        ------
        GovernanceRefusedError
            If the governance gate refuses the provenance.
        LifecycleError
            If the transition is not permitted or preconditions fail.
        """
        # 1. Governance gate -- mandatory first action.
        authorise(registry, provenance)

        # 2. Operator identity check.
        if not operator_id.strip():
            raise LifecycleError(
                f"release-lifecycle: operator_id must not be empty "
                f"(manifest {self._manifest.manifest_id!r})"
            )

        # 3. Terminal check.
        if self._state.value == _TERMINAL_STATE:
            raise LifecycleError(
                f"release-lifecycle: manifest {self._manifest.manifest_id!r} is "
                "RETIRED and may not be transitioned further"
            )

        # 4. Allowed transition check.
        allowed = _ALLOWED_TRANSITIONS.get(self._state.value, frozenset())
        if target.value not in allowed:
            raise LifecycleError(
                f"release-lifecycle: cannot transition "
                f"{self._state.value} -> {target.value} "
                f"for manifest {self._manifest.manifest_id!r}; "
                f"allowed targets: {sorted(allowed) or 'none (terminal)'}"
            )

        # 5. Rationale required for impactful target states.
        if target.value in _RATIONALE_REQUIRED and not rationale.strip():
            raise LifecycleError(
                f"release-lifecycle: rationale is required when transitioning "
                f"to {target.value} "
                f"(manifest {self._manifest.manifest_id!r})"
            )

        # 6. Apply transition.
        from_state = self._state
        self._state = target

        record = LifecycleRecord(
            record_id=uuid.uuid4().hex[:16],
            manifest_id=self._manifest.manifest_id,
            from_state=from_state,
            to_state=target,
            operator_id=operator_id.strip(),
            transitioned_at=datetime.now(tz=UTC),
            rationale=rationale.strip(),
        )
        self._history.append(record)
        return record

    # ------------------------------------------------------------------
    # Named transition helpers
    # ------------------------------------------------------------------

    def advance(
        self,
        registry: UseCaseRegistry,
        provenance: Provenance,
        operator_id: str,
        rationale: str = "",
    ) -> LifecycleRecord:
        """Move the manifest one step forward along the pre-production pipeline.

        Valid sequential advances::

            RESEARCH -> DESIGN -> VALIDATION -> SHADOW -> PILOT -> PRODUCTION

        Use this for normal pipeline progression.  For operational transitions
        (DEGRADED, SUSPENDED, RETIRED) use the named helpers.

        Raises
        ------
        GovernanceRefusedError
            If the governance gate refuses the provenance.
        LifecycleError
            If the advance is not valid from the current state.
        """
        _forward: dict[str, LifecycleState] = {
            "RESEARCH": LifecycleState.DESIGN,
            "DESIGN": LifecycleState.VALIDATION,
            "VALIDATION": LifecycleState.SHADOW,
            "SHADOW": LifecycleState.PILOT,
            "PILOT": LifecycleState.PRODUCTION,
        }
        target = _forward.get(self._state.value)
        if target is None:
            raise LifecycleError(
                f"release-lifecycle: advance() is not valid from state "
                f"{self._state.value} for manifest "
                f"{self._manifest.manifest_id!r}; use a named helper instead"
            )
        return self._transition(target, registry, provenance, operator_id, rationale)

    def degrade(
        self,
        registry: UseCaseRegistry,
        provenance: Provenance,
        operator_id: str,
        rationale: str,
    ) -> LifecycleRecord:
        """Transition PRODUCTION -> DEGRADED (quality regression detected).

        The manifest continues to serve but every invocation must carry a
        DEGRADED telemetry flag.  Rationale is mandatory.

        Raises
        ------
        GovernanceRefusedError
            If the governance gate refuses the provenance.
        LifecycleError
            If the current state is not PRODUCTION, or rationale is empty.
        """
        if self._state is not LifecycleState.PRODUCTION:
            raise LifecycleError(
                f"release-lifecycle: degrade() requires state PRODUCTION, "
                f"current state is {self._state.value} "
                f"(manifest {self._manifest.manifest_id!r})"
            )
        return self._transition(
            LifecycleState.DEGRADED, registry, provenance, operator_id, rationale
        )

    def suspend(
        self,
        registry: UseCaseRegistry,
        provenance: Provenance,
        operator_id: str,
        rationale: str,
    ) -> LifecycleRecord:
        """Transition PRODUCTION or DEGRADED -> SUSPENDED.

        The manifest is taken out of service.  No live traffic may be
        served.  Rationale is mandatory.

        Raises
        ------
        GovernanceRefusedError
            If the governance gate refuses the provenance.
        LifecycleError
            If the current state is not PRODUCTION or DEGRADED, or
            rationale is empty.
        """
        if self._state not in (LifecycleState.PRODUCTION, LifecycleState.DEGRADED):
            raise LifecycleError(
                f"release-lifecycle: suspend() requires state PRODUCTION or "
                f"DEGRADED, current state is {self._state.value} "
                f"(manifest {self._manifest.manifest_id!r})"
            )
        return self._transition(
            LifecycleState.SUSPENDED, registry, provenance, operator_id, rationale
        )

    def reinstate(
        self,
        registry: UseCaseRegistry,
        provenance: Provenance,
        operator_id: str,
        rationale: str = "",
    ) -> LifecycleRecord:
        """Transition DEGRADED or SUSPENDED -> PRODUCTION (formal review passed).

        Raises
        ------
        GovernanceRefusedError
            If the governance gate refuses the provenance.
        LifecycleError
            If the current state is not DEGRADED or SUSPENDED.
        """
        if self._state not in (LifecycleState.DEGRADED, LifecycleState.SUSPENDED):
            raise LifecycleError(
                f"release-lifecycle: reinstate() requires state DEGRADED or "
                f"SUSPENDED, current state is {self._state.value} "
                f"(manifest {self._manifest.manifest_id!r})"
            )
        return self._transition(
            LifecycleState.PRODUCTION, registry, provenance, operator_id, rationale
        )

    def retire(
        self,
        registry: UseCaseRegistry,
        provenance: Provenance,
        operator_id: str,
        rationale: str,
    ) -> LifecycleRecord:
        """Transition any non-retired state -> RETIRED (permanent decommission).

        RETIRED is the terminal state.  No further transitions are possible.
        The evidence record is kept.  Rationale is mandatory.

        Raises
        ------
        GovernanceRefusedError
            If the governance gate refuses the provenance.
        LifecycleError
            If the manifest is already RETIRED, or rationale is empty.
        """
        return self._transition(
            LifecycleState.RETIRED, registry, provenance, operator_id, rationale
        )


# ---------------------------------------------------------------------------
# LifecycleAwareManifestRegistry
# ---------------------------------------------------------------------------


class LifecycleAwareManifestRegistry:
    """Single source of truth that keeps ManifestRegistry and ManifestLifecycle in sync.

    ``ManifestRegistry`` marks manifests ACTIVE immediately on
    :meth:`~ManifestRegistry.add`.  ``ManifestLifecycle`` tracks the 9-state
    machine defined in §29.  On their own the two are disconnected: a manifest
    in RESEARCH state can be fetched as ACTIVE by ``ManifestRegistry.get()``.

    This class closes that gap by creating a :class:`ManifestLifecycle` for
    every manifest at registration time and exposing a
    :meth:`production_ready_ids` filter that only surfaces manifests whose
    lifecycle state passes :func:`can_serve_production`.

    Design rules
    ------------
    * Composition over inheritance — the inner :class:`ManifestRegistry` is
      still the authority for ACTIVE/REVOKED/SUPERSEDED status.  This class
      adds the lifecycle dimension on top.
    * ``add()`` is the only mutation point: both the registry entry and its
      lifecycle tracker are created atomically.
    * Read-only delegation (``status``, ``revoke``, ``supersede``) passes
      through to the inner registry unchanged.

    Usage::

        store = LifecycleAwareManifestRegistry()
        lc = store.add(my_manifest)
        # Walk the lifecycle forward under governance ...
        lc.advance(registry, provenance, "alice")          # DESIGN
        lc.advance(registry, provenance, "alice")          # VALIDATION
        lc.advance(registry, provenance, "alice")          # SHADOW
        lc.advance(registry, provenance, "alice")          # PILOT
        lc.advance(registry, provenance, "alice")          # PRODUCTION

        # Only now does production_ready_ids() include this manifest.
        assert my_manifest.manifest_id in store.production_ready_ids()
    """

    def __init__(self) -> None:
        self._registry: ManifestRegistry = ManifestRegistry()
        self._lifecycles: dict[str, ManifestLifecycle] = {}

    # ------------------------------------------------------------------
    # Mutation
    # ------------------------------------------------------------------

    def add(self, manifest: AIReleaseManifest) -> ManifestLifecycle:
        """Register *manifest* and return its :class:`ManifestLifecycle` tracker.

        The manifest is immediately recorded as ``ACTIVE`` in the inner
        :class:`ManifestRegistry`, but its lifecycle starts in ``RESEARCH``.
        Use the returned :class:`ManifestLifecycle` to advance the manifest
        through the pipeline before relying on :meth:`production_ready_ids`.

        After registration, the inner registry's :meth:`~ManifestRegistry.get`
        hook is updated so that any call to ``get()`` is refused unless the
        manifest's lifecycle state passes :func:`can_serve_production`.  This
        is the *runtime block* — not just detection — that prevents a
        RESEARCH-stage manifest from being handed to ``evaluation_evidence.build``
        or any other governed consumer.

        Raises
        ------
        ManifestRegistryError
            If a manifest with the same ID is already registered
            (propagated from the inner :class:`ManifestRegistry`).
        """
        self._registry.add(manifest)
        lc = ManifestLifecycle(manifest)
        self._lifecycles[manifest.manifest_id] = lc
        # Install (or refresh) the lifecycle-readiness gate on the inner registry.
        self._registry.set_lifecycle_checker(self._is_production_ready)
        return lc

    def _is_production_ready(self, manifest_id: str) -> bool:
        """Return True iff *manifest_id* has a production-serving lifecycle state."""
        lc = self._lifecycles.get(manifest_id)
        return lc is not None and can_serve_production(lc.state)


    def revoke(self, manifest_id: str) -> None:
        """Mark *manifest_id* as REVOKED in the inner registry."""
        self._registry.revoke(manifest_id)

    def supersede(self, manifest_id: str) -> None:
        """Mark *manifest_id* as SUPERSEDED in the inner registry."""
        self._registry.supersede(manifest_id)

    # ------------------------------------------------------------------
    # Queries
    # ------------------------------------------------------------------

    def lifecycle_for(self, manifest_id: str) -> ManifestLifecycle | None:
        """Return the :class:`ManifestLifecycle` for *manifest_id*, or ``None``."""
        return self._lifecycles.get(manifest_id)

    def status(self, manifest_id: str) -> ManifestStatus | None:
        """Return the :class:`~ManifestStatus` from the inner registry."""
        return self._registry.status(manifest_id)

    def production_ready_ids(self) -> list[str]:
        """IDs of manifests that are both ACTIVE in the registry *and* in a
        production-serving lifecycle state (PILOT, PRODUCTION, or DEGRADED).

        A manifest that is ACTIVE in :class:`ManifestRegistry` but still in
        RESEARCH/DESIGN/VALIDATION/SHADOW will *not* appear here.
        """
        return sorted(
            mid
            for mid, lc in self._lifecycles.items()
            if self._registry.status(mid) is ManifestStatus.ACTIVE
            and can_serve_production(lc.state)
        )

    @property
    def manifest_registry(self) -> ManifestRegistry:
        """The inner :class:`ManifestRegistry` for read-only inspection."""
        return self._registry

    @property
    def lifecycle_count(self) -> int:
        """Total number of manifests tracked (any state)."""
        return len(self._lifecycles)

