"""The Governed Model Gateway's enforcement point.

ADR-0006 §2.5 makes this module the reason the Gateway exists at all. It states
three refusals and one property:

    The Gateway refuses unknown use cases, refuses A5 actions regardless of
    what any model or tool prompt says, and applies the per-use-case kill
    switch. The Go caller cannot override any of this, because the decision is
    made after the call leaves it.

The last clause is the design constraint. Every function here takes the
governance context as an argument and returns a verdict; none takes an option,
a flag or an override, and none consults anything a caller or a model could
have influenced. A parameter that could relax a refusal is a parameter that
will eventually be passed.

What this module is *not*: it is not a content filter and it is not a safety
classifier. It answers "is this call permitted by the estate's own governance",
which is a question about registration, authority level and kill-switch state —
all facts the estate holds about itself, none of them inferred from the request
body and none of them obtainable by asking a model.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum

from .provenance import AuthorityOutcome, Provenance, RiskTier


class Refusal(StrEnum):
    """Why a call was refused.

    A closed vocabulary, mirroring the reason-code discipline of ADR-0016 §2.4:
    these strings appear in logs, in metrics and in the response, and they mean
    the same thing in all three.
    """

    UNKNOWN_USE_CASE = "AI_UNKNOWN_USE_CASE"
    USE_CASE_SUSPENDED = "AI_USE_CASE_SUSPENDED"
    KILL_SWITCH_ENGAGED = "AI_KILL_SWITCH_ENGAGED"
    AUTHORITY_REFUSED = "AI_AUTHORITY_REFUSED"
    RISK_TIER_EXCEEDED = "AI_RISK_TIER_EXCEEDED"
    RESIDENCY_REFUSED = "AI_RESIDENCY_REFUSED"
    MALFORMED_CONTEXT = "AI_MALFORMED_CONTEXT"


class GovernanceRefusedError(Exception):
    """Raised when the Gateway refuses a call.

    It carries the refusal code and the redacted context, and deliberately not
    the request body: a refusal that echoed what it refused would put the very
    payload the governance layer declined to process into the log of the
    decline.
    """

    def __init__(self, refusal: Refusal, detail: str, context: dict[str, str] | None = None):
        super().__init__(f"{refusal.value}: {detail}")
        self.refusal = refusal
        self.detail = detail
        self.context = context or {}


@dataclass(frozen=True)
class UseCase:
    """A registered AI use case.

    Registration is the whole control. ADR-0006 §2.5's "refuses unknown use
    cases" means an unregistered use case cannot run, which in turn means a new
    AI capability cannot be shipped by deploying code that calls the Gateway —
    it needs an entry here, which is a reviewed change on the AI train with a
    named owner.
    """

    use_case_id: str
    owner: str
    description: str
    # max_risk_tier is the highest tier this use case may operate at. A request
    # arriving at a higher tier is refused rather than downgraded: downgrading
    # would let a caller obtain a lower-scrutiny path by mislabelling.
    max_risk_tier: RiskTier
    # max_authority is the highest authority outcome this use case may produce.
    # It can never be A5 (see MAX_PERMITTED_AUTHORITY).
    max_authority: AuthorityOutcome
    # permitted_regions is the residency allowlist (ADR-0006 §2.8). Empty means
    # no region is permitted, which is the correct default: a use case that has
    # not declared where it may run has not been through residency review.
    permitted_regions: frozenset[str] = field(default_factory=frozenset)
    # suspended takes a use case out of service without removing its
    # registration, so the history of what it was remains readable.
    suspended: bool = False


# The authority ceiling. ADR-0006 §2.5 refuses A5 "regardless of what any model
# or tool prompt says", so this is a module constant rather than a field on
# UseCase — a per-use-case setting would be a field somebody could set to A5.
MAX_PERMITTED_AUTHORITY = AuthorityOutcome.A4

_RISK_ORDER = {t: i for i, t in enumerate(RiskTier)}
_AUTHORITY_ORDER = {a: i for i, a in enumerate(AuthorityOutcome)}


class UseCaseRegistry:
    """The registered use cases and the kill switch.

    The kill switch has two granularities because the two situations are
    different. A per-use-case suspension is a targeted response to one
    capability behaving badly; the global switch is for when the question is
    "is the AI plane implicated at all" and the honest answer is that nobody
    yet knows. Making the operator choose between them under incident pressure
    is worse than giving them both.
    """

    def __init__(self, use_cases: list[UseCase] | None = None) -> None:
        self._use_cases: dict[str, UseCase] = {}
        for uc in use_cases or []:
            self.register(uc)
        self._global_kill = False
        self._killed: set[str] = set()

    def register(self, use_case: UseCase) -> None:
        """Add a use case. Re-registering an identifier is a defect."""
        if not use_case.use_case_id:
            raise ValueError("registry: use case has no identifier")
        if not use_case.owner:
            # An unowned use case is one nobody can be asked about when it
            # misbehaves, which is the first question an incident asks.
            raise ValueError(f"registry: use case {use_case.use_case_id!r} has no owner")
        if use_case.max_authority == AuthorityOutcome.A5:
            raise ValueError(
                f"registry: use case {use_case.use_case_id!r} declares A5 authority, "
                "which ADR-0006 §2.5 refuses unconditionally"
            )
        if use_case.use_case_id in self._use_cases:
            raise ValueError(f"registry: use case {use_case.use_case_id!r} already registered")
        self._use_cases[use_case.use_case_id] = use_case

    def get(self, use_case_id: str) -> UseCase | None:
        return self._use_cases.get(use_case_id)

    def ids(self) -> list[str]:
        return sorted(self._use_cases)

    # ---- kill switch -----------------------------------------------------

    def engage_global_kill(self) -> None:
        """Stop every AI use case."""
        self._global_kill = True

    def release_global_kill(self) -> None:
        self._global_kill = False

    def kill(self, use_case_id: str) -> None:
        """Stop one use case.

        Deliberately does not require the use case to be registered. During an
        incident the identifier in hand may be one nobody can find in the
        registry, and refusing to act on it because it is unrecognised is the
        wrong behaviour for a kill switch.
        """
        self._killed.add(use_case_id)

    def revive(self, use_case_id: str) -> None:
        self._killed.discard(use_case_id)

    @property
    def global_kill_engaged(self) -> bool:
        return self._global_kill

    def is_killed(self, use_case_id: str) -> bool:
        return self._global_kill or use_case_id in self._killed


def authorise(registry: UseCaseRegistry, provenance: Provenance) -> UseCase:
    """Decide whether a call may proceed. Returns the use case, or refuses.

    The order of the checks is deliberate and is the order of decreasing
    blast radius: the kill switch first, because an engaged switch means stop
    everything and the reason for stopping is not up for evaluation; then
    registration; then authority; then risk; then residency.

    Getting that order wrong would not change which calls are permitted, but it
    would change what an operator sees in the logs during an incident — and
    "unknown use case" appearing while the global kill is engaged would send
    somebody looking in the wrong place.
    """
    try:
        provenance.validate()
    except ValueError as exc:
        raise GovernanceRefusedError(Refusal.MALFORMED_CONTEXT, str(exc)) from exc

    context = provenance.redacted()

    # 1. Kill switch. Checked before registration so that an unregistered use
    #    case under a global kill is reported as killed, not as unknown.
    if registry.is_killed(provenance.use_case):
        raise GovernanceRefusedError(
            Refusal.KILL_SWITCH_ENGAGED,
            f"the kill switch is engaged for {provenance.use_case!r}",
            context,
        )

    # 2. Registration.
    use_case = registry.get(provenance.use_case)
    if use_case is None:
        raise GovernanceRefusedError(
            Refusal.UNKNOWN_USE_CASE,
            f"{provenance.use_case!r} is not a registered AI use case",
            context,
        )

    if use_case.suspended:
        raise GovernanceRefusedError(
            Refusal.USE_CASE_SUSPENDED,
            f"{provenance.use_case!r} is suspended",
            context,
        )

    # 3. Authority. A5 is refused unconditionally and before the per-use-case
    #    ceiling is consulted, so no registry state can permit it.
    if provenance.authority_outcome == AuthorityOutcome.A5:
        raise GovernanceRefusedError(
            Refusal.AUTHORITY_REFUSED,
            "A5 actions are refused unconditionally",
            context,
        )
    if _AUTHORITY_ORDER[provenance.authority_outcome] > _AUTHORITY_ORDER[MAX_PERMITTED_AUTHORITY]:
        raise GovernanceRefusedError(
            Refusal.AUTHORITY_REFUSED,
            f"authority {provenance.authority_outcome.value} exceeds the permitted ceiling",
            context,
        )
    if _AUTHORITY_ORDER[provenance.authority_outcome] > _AUTHORITY_ORDER[use_case.max_authority]:
        raise GovernanceRefusedError(
            Refusal.AUTHORITY_REFUSED,
            f"authority {provenance.authority_outcome.value} exceeds "
            f"{use_case.use_case_id!r}'s ceiling of {use_case.max_authority.value}",
            context,
        )

    # 4. Risk tier. Refused rather than downgraded: downgrading would let a
    #    caller reach a lower-scrutiny path by mislabelling its own request.
    if _RISK_ORDER[provenance.risk_tier] > _RISK_ORDER[use_case.max_risk_tier]:
        raise GovernanceRefusedError(
            Refusal.RISK_TIER_EXCEEDED,
            f"risk tier {provenance.risk_tier.value} exceeds "
            f"{use_case.use_case_id!r}'s ceiling of {use_case.max_risk_tier.value}",
            context,
        )

    # 5. Residency (ADR-0006 §2.8). There is no "the model is only in one
    #    region" exception: a call from a region this use case has not declared
    #    is a cross-cell transfer and needs the evidenced-transfer treatment,
    #    not a Gateway that quietly routes it.
    if provenance.region not in use_case.permitted_regions:
        raise GovernanceRefusedError(
            Refusal.RESIDENCY_REFUSED,
            f"region {provenance.region!r} is not permitted for {use_case.use_case_id!r}",
            context,
        )

    return use_case
