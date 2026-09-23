"""Tool Registry and Authorization Broker.

Chapter 17 §13 and §16 of the ZoikoTax Master Specification.

The Broker is the ``authorise``-style gate for tool calls.  It refuses before
any work is performed, using the same "control before content" shape as
``governance.py``:

    authorise(catalog, provenance)

returns a ``ToolProfile`` when the call is permitted, or raises
``ToolBrokerRefusedError`` when it is not.

The checks are ordered by decreasing blast radius — identical principle to
``governance.authorise``:

1. Global kill switch (stop all tool calls immediately).
2. Tool registration — unknown tools are refused.
3. ``PRIVILEGED`` action class — refused unconditionally, same shape as A5
   in governance.
4. Scope verification — caller must hold every scope the tool requires.
5. Idempotency token — required for ``MUTATE`` and ``COMMIT`` calls.
6. Budget — steps / duration / token cost / monetary cost ceilings.
7. Agent delegation — placeholder; future implementation verifies the
   calling agent against an AgentCatalog.

Getting the order wrong would not change which calls are permitted; it
changes what an operator sees during an incident.

What this module is *not*: it is not a content filter, not a model safety
classifier, not a prompt guard.  It answers "is this tool call permitted by
the estate's own governance" — a question about registration, action class
and budget limits, none of them inferred from the request body.
"""

from __future__ import annotations

from collections.abc import Callable, Mapping
from dataclasses import dataclass
from datetime import datetime, timedelta
from enum import StrEnum

# ---------------------------------------------------------------------------
# Action classes  (Chapter 17 §16)
# ---------------------------------------------------------------------------


class ActionClass(StrEnum):
    """The five ordered action classes.

    Ordered by increasing privilege.  ``PRIVILEGED`` is refused
    unconditionally — analogous to authority outcome A5 in governance.
    """

    READ = "READ"
    PREPARE = "PREPARE"
    MUTATE = "MUTATE"
    COMMIT = "COMMIT"
    PRIVILEGED = "PRIVILEGED"


# The ceiling.  ``PRIVILEGED`` is refused before the per-tool ceiling is
# consulted, so no catalog state can permit it.
MAX_PERMITTED_ACTION_CLASS = ActionClass.COMMIT

_ACTION_ORDER = {a: i for i, a in enumerate(ActionClass)}


# ---------------------------------------------------------------------------
# Refusal codes
# ---------------------------------------------------------------------------


class Refusal(StrEnum):
    """Closed vocabulary of refusal reasons.

    The naming mirrors ``governance.Refusal`` so that logs and metrics look
    uniform across the two enforcement points.
    """

    UNKNOWN_TOOL = "AI_UNKNOWN_TOOL"
    TOOL_SUSPENDED = "AI_TOOL_SUSPENDED"
    KILL_SWITCH_ENGAGED = "AI_KILL_SWITCH_ENGAGED"
    PRIVILEGED_FORBIDDEN = "AI_PRIVILEGED_FORBIDDEN"
    ACTION_CLASS_EXCEEDED = "AI_ACTION_CLASS_EXCEEDED"
    SCOPE_VIOLATION = "AI_SCOPE_VIOLATION"
    IDEMPOTENCY_VIOLATION = "AI_IDEMPOTENCY_VIOLATION"
    BUDGET_EXCEEDED = "AI_BUDGET_EXCEEDED"
    AGENT_NOT_AUTHORIZED = "AI_AGENT_NOT_AUTHORIZED"
    MALFORMED_CONTEXT = "AI_MALFORMED_CONTEXT"


class ToolBrokerRefusedError(Exception):
    """Raised when the Broker refuses a tool call.

    Carries the refusal code and a redacted context.  Deliberately does
    *not* embed the request payload — a refusal that echoed what it refused
    would put the payload the broker declined to process into the log of the
    decline.
    """

    def __init__(
        self,
        refusal: Refusal,
        detail: str,
        context: Mapping[str, str] | None = None,
    ) -> None:
        super().__init__(f"{refusal.value}: {detail}")
        self.refusal = refusal
        self.detail = detail
        self.context = dict(context or {})


# ---------------------------------------------------------------------------
# Core data structures (all frozen — no in-place mutation after construction)
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class ToolProfile:
    """Immutable definition of a registered tool.

    ``action_class`` determines which authorization checks apply.
    ``required_scopes`` is an allowlist — the caller must hold *every* scope
    the tool declares; an empty frozenset means any caller with a valid
    registration may call the tool.
    ``idempotent`` — when ``True`` the Broker requires an idempotency token
    on every ``MUTATE`` or ``COMMIT`` call.
    """

    tool_id: str
    owner: str
    description: str
    action_class: ActionClass
    required_scopes: frozenset[str]
    idempotent: bool
    # Per-call budget limits.  ``None`` means the limit is not enforced.
    max_steps: int | None = None
    max_duration: timedelta | None = None
    max_token_cost: int | None = None
    max_monetary_cost: float | None = None
    # suspended takes a tool out of service without removing its registration.
    suspended: bool = False


@dataclass(frozen=True)
class ToolProvenance:
    """Metadata that travels with every tool call.

    Mirrors ``governance.Provenance`` for the tool plane.  Kept separate
    so the types are distinct and cannot be confused at call sites.
    """

    tool_id: str
    caller_id: str
    region: str
    timestamp: datetime
    # Scopes the caller claims.
    scopes: frozenset[str]
    # Idempotency token — required for MUTATE / COMMIT tools flagged idempotent.
    idempotency_token: str | None = None
    # Observed resource usage this call.  Used for budget checks.
    steps: int = 0
    duration_seconds: int = 0
    token_cost: int = 0
    monetary_cost: float = 0.0

    def validate(self) -> None:
        """Raise ``ValueError`` if the provenance is structurally invalid."""
        if not self.tool_id:
            raise ValueError("broker: empty tool_id")
        if not self.caller_id:
            raise ValueError("broker: empty caller_id")
        if not self.region:
            raise ValueError("broker: empty region")
        if self.timestamp == datetime.min:
            raise ValueError("broker: zero timestamp")

    def redacted(self) -> dict[str, str]:
        """Return a log-safe snapshot (no secrets, no payload)."""
        return {
            "tool_id": self.tool_id,
            "caller_id": self.caller_id,
            "region": self.region,
            "timestamp": self.timestamp.isoformat(),
            "scopes": ",".join(sorted(self.scopes)),
        }


# ---------------------------------------------------------------------------
# Tool catalog and kill switch
# ---------------------------------------------------------------------------


class ToolCatalog:
    """The registered tools and the kill switch.

    Two kill-switch granularities — same reasoning as ``UseCaseRegistry`` in
    governance: per-tool suspension for targeted incidents, global kill for
    "is the tool plane implicated at all".
    """

    def __init__(self, tools: list[ToolProfile] | None = None) -> None:
        self._tools: dict[str, ToolProfile] = {}
        for t in tools or []:
            self.register(t)
        self._global_kill = False
        self._killed: set[str] = set()

    def register(self, tool: ToolProfile) -> None:
        """Add a tool.  Re-registering the same identifier is a defect."""
        if not tool.tool_id:
            raise ValueError("catalog: tool has no identifier")
        if not tool.owner:
            raise ValueError(f"catalog: tool {tool.tool_id!r} has no owner")
        if tool.action_class == ActionClass.PRIVILEGED:
            raise ValueError(
                f"catalog: tool {tool.tool_id!r} declares PRIVILEGED action class, "
                "which is refused unconditionally"
            )
        if tool.tool_id in self._tools:
            raise ValueError(f"catalog: tool {tool.tool_id!r} already registered")
        self._tools[tool.tool_id] = tool

    def get(self, tool_id: str) -> ToolProfile | None:
        return self._tools.get(tool_id)

    def ids(self) -> list[str]:
        return sorted(self._tools)

    # -- kill switch ----------------------------------------------------------

    def engage_global_kill(self) -> None:
        """Stop every tool call immediately."""
        self._global_kill = True

    def release_global_kill(self) -> None:
        self._global_kill = False

    def kill(self, tool_id: str) -> None:
        """Stop one tool.

        Deliberately does not require the tool to be registered — during an
        incident the identifier in hand may not be in the catalog.
        """
        self._killed.add(tool_id)

    def revive(self, tool_id: str) -> None:
        self._killed.discard(tool_id)

    @property
    def global_kill_engaged(self) -> bool:
        return self._global_kill

    def is_killed(self, tool_id: str) -> bool:
        return self._global_kill or tool_id in self._killed


# ---------------------------------------------------------------------------
# Budget manager
# ---------------------------------------------------------------------------


def _check_budget(profile: ToolProfile, provenance: ToolProvenance) -> None:
    """Raise ``ToolBrokerRefusedError`` if the call exceeds any budget limit."""
    context = provenance.redacted()

    if profile.max_steps is not None and provenance.steps > profile.max_steps:
        raise ToolBrokerRefusedError(
            Refusal.BUDGET_EXCEEDED,
            f"steps {provenance.steps} exceed ceiling {profile.max_steps} "
            f"for tool {profile.tool_id!r}",
            context,
        )

    observed_duration = timedelta(seconds=provenance.duration_seconds)
    if profile.max_duration is not None and observed_duration > profile.max_duration:
        raise ToolBrokerRefusedError(
            Refusal.BUDGET_EXCEEDED,
            f"duration {observed_duration} exceeds ceiling {profile.max_duration} "
            f"for tool {profile.tool_id!r}",
            context,
        )

    if (
        profile.max_token_cost is not None
        and provenance.token_cost > profile.max_token_cost
    ):
        raise ToolBrokerRefusedError(
            Refusal.BUDGET_EXCEEDED,
            f"token cost {provenance.token_cost} exceeds ceiling "
            f"{profile.max_token_cost} for tool {profile.tool_id!r}",
            context,
        )

    if (
        profile.max_monetary_cost is not None
        and provenance.monetary_cost > profile.max_monetary_cost
    ):
        raise ToolBrokerRefusedError(
            Refusal.BUDGET_EXCEEDED,
            f"monetary cost {provenance.monetary_cost} exceeds ceiling "
            f"{profile.max_monetary_cost} for tool {profile.tool_id!r}",
            context,
        )


# ---------------------------------------------------------------------------
# The broker — core authorisation function
# ---------------------------------------------------------------------------


def authorise(catalog: ToolCatalog, provenance: ToolProvenance) -> ToolProfile:
    """Decide whether a tool call may proceed.  Returns the tool, or refuses.

    Check order (decreasing blast radius):

    1. Malformed context.
    2. Kill switch — global before per-tool.
    3. Registration.
    4. Suspension.
    5. PRIVILEGED action class — refused unconditionally.
    6. Action class ceiling.
    7. Scope verification.
    8. Idempotency token (MUTATE and COMMIT).
    9. Budget.
    """
    try:
        provenance.validate()
    except ValueError as exc:
        raise ToolBrokerRefusedError(Refusal.MALFORMED_CONTEXT, str(exc)) from exc

    context = provenance.redacted()

    # 1. Kill switch — global first so a global kill never reports "unknown tool".
    if catalog.is_killed(provenance.tool_id):
        raise ToolBrokerRefusedError(
            Refusal.KILL_SWITCH_ENGAGED,
            f"the kill switch is engaged for {provenance.tool_id!r}",
            context,
        )

    # 2. Registration.
    tool = catalog.get(provenance.tool_id)
    if tool is None:
        raise ToolBrokerRefusedError(
            Refusal.UNKNOWN_TOOL,
            f"{provenance.tool_id!r} is not a registered tool",
            context,
        )

    # 3. Suspension.
    if tool.suspended:
        raise ToolBrokerRefusedError(
            Refusal.TOOL_SUSPENDED,
            f"{provenance.tool_id!r} is suspended",
            context,
        )

    # 4. PRIVILEGED — refused unconditionally, same as A5 in governance.
    if tool.action_class == ActionClass.PRIVILEGED:
        raise ToolBrokerRefusedError(
            Refusal.PRIVILEGED_FORBIDDEN,
            "PRIVILEGED tool calls are refused unconditionally",
            context,
        )

    # 5. Action class ceiling.
    if _ACTION_ORDER[tool.action_class] > _ACTION_ORDER[MAX_PERMITTED_ACTION_CLASS]:
        raise ToolBrokerRefusedError(
            Refusal.ACTION_CLASS_EXCEEDED,
            f"action class {tool.action_class.value} exceeds the permitted ceiling "
            f"of {MAX_PERMITTED_ACTION_CLASS.value}",
            context,
        )

    # 6. Scope verification — caller must hold every required scope.
    missing = tool.required_scopes - provenance.scopes
    if missing:
        raise ToolBrokerRefusedError(
            Refusal.SCOPE_VIOLATION,
            f"missing scopes for {tool.tool_id!r}: {', '.join(sorted(missing))}",
            context,
        )

    # 7. Idempotency token required for mutable calls.
    if (
        tool.idempotent
        and tool.action_class in (ActionClass.MUTATE, ActionClass.COMMIT)
        and not provenance.idempotency_token
    ):
        raise ToolBrokerRefusedError(
            Refusal.IDEMPOTENCY_VIOLATION,
            f"idempotency token required for {tool.tool_id!r}",
            context,
        )

    # 8. Budget.
    _check_budget(tool, provenance)

    # 9. Agent delegation — placeholder.  A future AgentCatalog check goes here.

    return tool


# ---------------------------------------------------------------------------
# Guarded execution helper  (mirrors ``service.guarded``)
# ---------------------------------------------------------------------------


def guarded[T](
    catalog: ToolCatalog,
    provenance: ToolProvenance,
    work: Callable[[ToolProfile], T],
) -> T:
    """Run ``work`` only when the broker permits the call.

    No ``force``, no ``skip_checks``, no ``override`` — for the same reason
    ``service.guarded`` has none.
    """
    import logging

    log = logging.getLogger("ztax_broker")

    try:
        tool = authorise(catalog, provenance)
    except ToolBrokerRefusedError as refusal:
        log.info(
            "tool call refused",
            extra={
                "refusal": refusal.refusal.value,
                "detail": refusal.detail,
                **refusal.context,
            },
        )
        raise

    log.info("tool call permitted", extra=provenance.redacted())
    return work(tool)


# ---------------------------------------------------------------------------
# Audit record
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class AgentAudit:
    """Tamper-evident log record for one tool call.

    ``payload_hash`` is the SHA-256 of the request/response pair.  Optional
    — only populated when the caller supplies it.  ``reviewer_id`` links to
    the human review workflow if the call required one.
    """

    call_id: str
    tool_id: str
    caller_id: str
    timestamp: datetime
    outcome: str  # "PERMITTED" or the Refusal value
    detail: str | None = None
    payload_hash: str | None = None
    reviewer_id: str | None = None

    def as_dict(self) -> dict[str, str]:
        """Return a log-safe representation."""
        d: dict[str, str] = {
            "call_id": self.call_id,
            "tool_id": self.tool_id,
            "caller_id": self.caller_id,
            "timestamp": self.timestamp.isoformat(),
            "outcome": self.outcome,
        }
        if self.detail:
            d["detail"] = self.detail
        if self.payload_hash:
            d["payload_hash"] = self.payload_hash
        if self.reviewer_id:
            d["reviewer_id"] = self.reviewer_id
        return d
