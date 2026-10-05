"""AI Release Manifest and Production Profile Registries.

Chapter 17 §14 of the ZoikoTax Master Specification.

This module implements the **Global AI Control Plane** registries that the
spec names in §14 but that no earlier module has built.  Together they form
the governance backbone that every ``*_profile_id`` and ``*_manifest_id``
string scattered across the other modules is secretly pointing at.

The module has five public building blocks, listed in dependency order:

1. **PromptProfile** (§14.3) -- a versioned, owner-declared prompt template
   with its variable schema and output JSON schema.  Nothing in the Gateway
   may construct a Provenance carrying a ``prompt_profile`` name that is not
   registered here.

2. **AgentProfile** (§14.4) -- the orchestration envelope for an autonomous
   agent: the allowed tool IDs, memory policy, and hard step/token budgets.
   An agent that is not registered cannot be authorised by the Tool Broker.

3. **DatasetProfile** (§14.6) -- a versioned gold-set or retrieval corpus
   snapshot.  The evaluation pipeline uses its ``dataset_id`` to resolve the
   correct GoldSetStore version.

4. **EvaluationProfile** (§14.5) -- the minimum quality thresholds that a
   classifier or agent must meet before it can be included in an
   AIReleaseManifest.

5. **AIReleaseManifest** (§14.1) -- the immutable, cryptographically hashed
   deployment bundle.  manifest_hash is a SHA-256 of the canonical field
   representation, making tampering detectable.

6. **ManifestRegistry** -- the runtime gatekeeper tracking ACTIVE / REVOKED
   / SUPERSEDED manifests.

Design rules
------------
* No live model calls.
* No fiscal imports (ADR-0006 §2.6).
* Immutable evidence artefacts: all profile types and AIReleaseManifest are
  frozen=True dataclasses.
* Governance gate on manifest lookup: ManifestRegistry.get() calls authorise()
  before returning a manifest.
"""

from __future__ import annotations

import hashlib
import json
import uuid
from collections.abc import Callable
from dataclasses import dataclass
from datetime import UTC, datetime
from enum import StrEnum
from typing import Final

from .governance import UseCaseRegistry, authorise
from .provenance import AuthorityOutcome, Provenance, RiskTier

__all__: list[str] = [
    "AIReleaseManifest",
    "AIReleaseManifestError",
    "AgentMemoryPolicy",
    "AgentProfile",
    "AgentProfileError",
    "DatasetProfile",
    "DatasetProfileError",
    "EvaluationProfile",
    "EvaluationProfileError",
    "ManifestRegistry",
    "ManifestRegistryError",
    "ManifestStatus",
    "PromptProfile",
    "PromptProfileError",
    "build_manifest",
]


# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_HASH_ALGORITHM: Final[str] = "sha256"


# ---------------------------------------------------------------------------
# Errors
# ---------------------------------------------------------------------------


class PromptProfileError(Exception):
    """Raised when a PromptProfile cannot be constructed or used."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


class AgentProfileError(Exception):
    """Raised when an AgentProfile cannot be constructed or used."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


class DatasetProfileError(Exception):
    """Raised when a DatasetProfile cannot be constructed or used."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


class EvaluationProfileError(Exception):
    """Raised when an EvaluationProfile cannot be constructed or used."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


class AIReleaseManifestError(Exception):
    """Raised when an AIReleaseManifest cannot be constructed or used."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


class ManifestRegistryError(Exception):
    """Raised when a ManifestRegistry operation fails."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


# ---------------------------------------------------------------------------
# Enumerations
# ---------------------------------------------------------------------------


class AgentMemoryPolicy(StrEnum):
    """How an agent stores inter-step state (Chapter 17 §14.4).

    STATELESS  -- no state persists between steps.
    SESSION    -- state persists within one bounded session.
    PERSISTENT -- state survives across sessions; requires explicit
                  retention-period and data-class declaration.
    """

    STATELESS = "STATELESS"
    SESSION = "SESSION"
    PERSISTENT = "PERSISTENT"


class ManifestStatus(StrEnum):
    """The lifecycle state of an AIReleaseManifest.

    ACTIVE     -- approved and may be used in production.
    REVOKED    -- withdrawn; runtime must refuse it.
    SUPERSEDED -- replaced by a newer version; may no longer be used.
    """

    ACTIVE = "ACTIVE"
    REVOKED = "REVOKED"
    SUPERSEDED = "SUPERSEDED"


# ---------------------------------------------------------------------------
# Prompt Profile  (§14.3)
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class PromptProfile:
    """A versioned, owner-declared prompt template (Chapter 17 §14.3).

    Attributes
    ----------
    profile_id:
        Stable, opaque identifier.  Must be non-empty.
    owner:
        Team or individual responsible for this prompt.
    version:
        Semantic version string (e.g. "1.2.0").
    system_prompt:
        The invariant system instruction.  Must be non-empty.
    variable_schema:
        JSON-schema-style dict describing the template variables.
    output_schema:
        JSON-schema-style dict describing the expected output structure.
    max_risk_tier:
        The highest risk tier at which this prompt may be used.
    authority_ceiling:
        The highest authority outcome this prompt is permitted to produce.
        Cannot be A5.
    safety_rules:
        A tuple of plain-language safety invariants that reviewers approved.
    deprecated:
        When True this profile may not appear in new manifests.
    """

    profile_id: str
    owner: str
    version: str
    system_prompt: str
    variable_schema: dict[str, object]
    output_schema: dict[str, object]
    max_risk_tier: RiskTier
    authority_ceiling: AuthorityOutcome
    safety_rules: tuple[str, ...]
    deprecated: bool = False

    def __post_init__(self) -> None:
        if not self.profile_id:
            raise PromptProfileError("prompt-profile: profile_id must not be empty")
        if not self.owner:
            raise PromptProfileError(
                f"prompt-profile: {self.profile_id!r} has no owner"
            )
        if not self.version:
            raise PromptProfileError(
                f"prompt-profile: {self.profile_id!r} has no version"
            )
        if not self.system_prompt.strip():
            raise PromptProfileError(
                f"prompt-profile: {self.profile_id!r} has an empty system_prompt"
            )
        if self.authority_ceiling == AuthorityOutcome.A5:
            raise PromptProfileError(
                f"prompt-profile: {self.profile_id!r} declares A5 authority_ceiling, "
                "which is unconditionally refused by the governance layer"
            )

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation."""
        return {
            "profile_id": self.profile_id,
            "owner": self.owner,
            "version": self.version,
            "max_risk_tier": self.max_risk_tier.value,
            "authority_ceiling": self.authority_ceiling.value,
            "safety_rule_count": len(self.safety_rules),
            "deprecated": self.deprecated,
        }


# ---------------------------------------------------------------------------
# Agent Profile  (§14.4)
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class AgentProfile:
    """The orchestration envelope for an autonomous agent (Chapter 17 §14.4).

    Attributes
    ----------
    profile_id:
        Stable, opaque identifier.
    owner:
        Team or individual responsible.
    version:
        Semantic version string.
    allowed_tool_ids:
        Frozenset of tool IDs the agent may invoke.
    memory_policy:
        How inter-step state is managed.
    max_steps:
        Hard cap on tool-call steps per session.  None means undeclared.
    max_token_budget:
        Hard cap on total tokens per session.  None means undeclared.
    suspended:
        When True this agent profile must not be used in production.
    """

    profile_id: str
    owner: str
    version: str
    allowed_tool_ids: frozenset[str]
    memory_policy: AgentMemoryPolicy
    max_steps: int | None
    max_token_budget: int | None
    suspended: bool = False

    def __post_init__(self) -> None:
        if not self.profile_id:
            raise AgentProfileError("agent-profile: profile_id must not be empty")
        if not self.owner:
            raise AgentProfileError(
                f"agent-profile: {self.profile_id!r} has no owner"
            )
        if not self.version:
            raise AgentProfileError(
                f"agent-profile: {self.profile_id!r} has no version"
            )
        if self.max_steps is not None and self.max_steps < 1:
            raise AgentProfileError(
                f"agent-profile: {self.profile_id!r} max_steps must be >= 1, "
                f"got {self.max_steps!r}"
            )
        if self.max_token_budget is not None and self.max_token_budget < 1:
            raise AgentProfileError(
                f"agent-profile: {self.profile_id!r} max_token_budget must be >= 1, "
                f"got {self.max_token_budget!r}"
            )

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation."""
        return {
            "profile_id": self.profile_id,
            "owner": self.owner,
            "version": self.version,
            "allowed_tool_count": len(self.allowed_tool_ids),
            "memory_policy": self.memory_policy.value,
            "max_steps": self.max_steps,
            "max_token_budget": self.max_token_budget,
            "suspended": self.suspended,
        }


# ---------------------------------------------------------------------------
# Dataset Profile  (§14.6)
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class DatasetProfile:
    """A versioned gold-set or retrieval corpus snapshot (Chapter 17 §14.6).

    Attributes
    ----------
    dataset_id:
        Stable, opaque identifier.
    owner:
        Team or individual responsible.
    version:
        Semantic version string.
    description:
        Human-readable description of the dataset contents.
    data_class:
        The highest data sensitivity class present (e.g. "INTERNAL").
    case_count:
        Number of labelled cases.  Must be >= 2.
    deprecated:
        When True this dataset version should not appear in new profiles.
    """

    dataset_id: str
    owner: str
    version: str
    description: str
    data_class: str
    case_count: int
    deprecated: bool = False

    def __post_init__(self) -> None:
        if not self.dataset_id:
            raise DatasetProfileError("dataset-profile: dataset_id must not be empty")
        if not self.owner:
            raise DatasetProfileError(
                f"dataset-profile: {self.dataset_id!r} has no owner"
            )
        if not self.version:
            raise DatasetProfileError(
                f"dataset-profile: {self.dataset_id!r} has no version"
            )
        if not self.description.strip():
            raise DatasetProfileError(
                f"dataset-profile: {self.dataset_id!r} has an empty description"
            )
        if not self.data_class.strip():
            raise DatasetProfileError(
                f"dataset-profile: {self.dataset_id!r} has an empty data_class"
            )
        if self.case_count < 2:
            raise DatasetProfileError(
                f"dataset-profile: {self.dataset_id!r} case_count must be >= 2, "
                f"got {self.case_count!r}"
            )

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation."""
        return {
            "dataset_id": self.dataset_id,
            "owner": self.owner,
            "version": self.version,
            "data_class": self.data_class,
            "case_count": self.case_count,
            "deprecated": self.deprecated,
        }


# ---------------------------------------------------------------------------
# Evaluation Profile  (§14.5)
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class EvaluationProfile:
    """Minimum quality thresholds gating a release (Chapter 17 §14.5).

    Attributes
    ----------
    profile_id:
        Stable, opaque identifier.
    owner:
        Team or individual responsible.
    version:
        Semantic version string.
    dataset_id:
        The DatasetProfile ID against which evaluation runs.
    min_accuracy:
        Minimum exact-match accuracy [0.0, 1.0] required.
    min_f1:
        Optional minimum macro-F1 [0.0, 1.0].  None means not enforced.
    max_calibration_error:
        Optional maximum expected calibration error [0.0, 1.0].
        None means not enforced.
    adversarial_suite_required:
        When True the full adversarial suite must pass before this
        profile is considered satisfied.
    """

    profile_id: str
    owner: str
    version: str
    dataset_id: str
    min_accuracy: float
    min_f1: float | None
    max_calibration_error: float | None
    adversarial_suite_required: bool = True

    def __post_init__(self) -> None:
        if not self.profile_id:
            raise EvaluationProfileError(
                "evaluation-profile: profile_id must not be empty"
            )
        if not self.owner:
            raise EvaluationProfileError(
                f"evaluation-profile: {self.profile_id!r} has no owner"
            )
        if not self.version:
            raise EvaluationProfileError(
                f"evaluation-profile: {self.profile_id!r} has no version"
            )
        if not self.dataset_id:
            raise EvaluationProfileError(
                f"evaluation-profile: {self.profile_id!r} has no dataset_id"
            )
        if not (0.0 <= self.min_accuracy <= 1.0):
            raise EvaluationProfileError(
                f"evaluation-profile: {self.profile_id!r} min_accuracy "
                f"must be in [0.0, 1.0], got {self.min_accuracy!r}"
            )
        if self.min_f1 is not None and not (0.0 <= self.min_f1 <= 1.0):
            raise EvaluationProfileError(
                f"evaluation-profile: {self.profile_id!r} min_f1 "
                f"must be in [0.0, 1.0], got {self.min_f1!r}"
            )
        if self.max_calibration_error is not None and not (
            0.0 <= self.max_calibration_error <= 1.0
        ):
            raise EvaluationProfileError(
                f"evaluation-profile: {self.profile_id!r} max_calibration_error "
                f"must be in [0.0, 1.0], got {self.max_calibration_error!r}"
            )

    def is_satisfied_by(self, accuracy: float, f1: float | None = None) -> bool:
        """Return True if the given metrics satisfy this profile's thresholds."""
        if accuracy < self.min_accuracy:
            return False
        return not (self.min_f1 is not None and (f1 is None or f1 < self.min_f1))

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation."""
        return {
            "profile_id": self.profile_id,
            "owner": self.owner,
            "version": self.version,
            "dataset_id": self.dataset_id,
            "min_accuracy": self.min_accuracy,
            "min_f1": self.min_f1,
            "max_calibration_error": self.max_calibration_error,
            "adversarial_suite_required": self.adversarial_suite_required,
        }


# ---------------------------------------------------------------------------
# AI Release Manifest  (§14.1)
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class AIReleaseManifest:
    """The immutable, hashed deployment bundle (Chapter 17 §14.1).

    Exactly one approved combination: use case + model profile + resolved
    model ID + prompt profile + evaluation profile + policy bundle, bound to
    allowed regions, hashed at approval time.

    Use build_manifest() to construct instances -- never call this directly.

    Attributes
    ----------
    manifest_id:
        Stable, opaque identifier.
    release_version:
        Semantic version (e.g. "2.0.1").
    ai_use_case_id:
        The UseCase ID this manifest governs.
    authority_level:
        AuthorityOutcome ceiling.  Cannot be A5.
    risk_tier:
        RiskTier ceiling.
    model_profile_id:
        Logical model profile identifier.
    resolved_model_id:
        Exact pinned model version at approval time.
    prompt_profile_id:
        The PromptProfile ID.
    evaluation_profile_id:
        The EvaluationProfile ID whose thresholds were met.
    policy_bundle_id:
        Opaque policy bundle identifier.
    allowed_regions:
        Regions where this manifest may be executed.  Non-empty.
    approved_at:
        UTC timestamp when the manifest was sealed.
    agent_profile_id:
        Optional AgentProfile ID for agentic releases.
    tool_policy_id:
        Optional tool policy identifier.
    retrieval_corpus_version:
        Optional RAG corpus version string.
    embedding_model_version:
        Optional embedding model version string.
    fallback_profile_id:
        Optional fallback model profile ID.
    manifest_hash:
        SHA-256 hex digest of the canonical field representation.
    """

    manifest_id: str
    release_version: str
    ai_use_case_id: str
    authority_level: AuthorityOutcome
    risk_tier: RiskTier
    model_profile_id: str
    resolved_model_id: str
    prompt_profile_id: str
    evaluation_profile_id: str
    policy_bundle_id: str
    allowed_regions: frozenset[str]
    approved_at: datetime
    agent_profile_id: str | None
    tool_policy_id: str | None
    retrieval_corpus_version: str | None
    embedding_model_version: str | None
    fallback_profile_id: str | None
    manifest_hash: str

    def __post_init__(self) -> None:
        if not self.manifest_id:
            raise AIReleaseManifestError(
                "ai-release-manifest: manifest_id must not be empty"
            )
        if not self.release_version:
            raise AIReleaseManifestError(
                f"ai-release-manifest: {self.manifest_id!r} has no release_version"
            )
        if not self.ai_use_case_id:
            raise AIReleaseManifestError(
                f"ai-release-manifest: {self.manifest_id!r} has no ai_use_case_id"
            )
        if self.authority_level == AuthorityOutcome.A5:
            raise AIReleaseManifestError(
                f"ai-release-manifest: {self.manifest_id!r} declares A5 authority_level, "
                "which is unconditionally refused by the governance layer"
            )
        if not self.allowed_regions:
            raise AIReleaseManifestError(
                f"ai-release-manifest: {self.manifest_id!r} has no allowed_regions; "
                "a manifest with no approved regions cannot be executed anywhere"
            )
        if not self.manifest_hash:
            raise AIReleaseManifestError(
                f"ai-release-manifest: {self.manifest_id!r} has no manifest_hash; "
                "use build_manifest() to construct a valid manifest"
            )

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe, JSON-serialisable representation."""
        return {
            "manifest_id": self.manifest_id,
            "release_version": self.release_version,
            "ai_use_case_id": self.ai_use_case_id,
            "authority_level": self.authority_level.value,
            "risk_tier": self.risk_tier.value,
            "model_profile_id": self.model_profile_id,
            "resolved_model_id": self.resolved_model_id,
            "prompt_profile_id": self.prompt_profile_id,
            "evaluation_profile_id": self.evaluation_profile_id,
            "policy_bundle_id": self.policy_bundle_id,
            "allowed_regions": sorted(self.allowed_regions),
            "approved_at": self.approved_at.isoformat(),
            "agent_profile_id": self.agent_profile_id,
            "tool_policy_id": self.tool_policy_id,
            "retrieval_corpus_version": self.retrieval_corpus_version,
            "embedding_model_version": self.embedding_model_version,
            "fallback_profile_id": self.fallback_profile_id,
            "manifest_hash": self.manifest_hash,
        }

    def verify_hash(self) -> bool:
        """Return True if the stored manifest_hash matches a freshly computed digest.

        Call this after deserialising a manifest from storage to confirm it
        has not been tampered with.
        """
        return self.manifest_hash == _compute_manifest_hash(self)


# ---------------------------------------------------------------------------
# Manifest builder
# ---------------------------------------------------------------------------


def build_manifest(
    *,
    ai_use_case_id: str,
    release_version: str,
    authority_level: AuthorityOutcome,
    risk_tier: RiskTier,
    model_profile_id: str,
    resolved_model_id: str,
    prompt_profile_id: str,
    evaluation_profile_id: str,
    policy_bundle_id: str,
    allowed_regions: frozenset[str] | set[str],
    approved_at: datetime | None = None,
    agent_profile_id: str | None = None,
    tool_policy_id: str | None = None,
    retrieval_corpus_version: str | None = None,
    embedding_model_version: str | None = None,
    fallback_profile_id: str | None = None,
    manifest_id: str | None = None,
) -> AIReleaseManifest:
    """Construct and hash an AIReleaseManifest.

    This is the only public constructor.  It generates a manifest_id when
    one is not supplied, stamps approved_at as the current UTC time when not
    provided, and computes the manifest_hash before sealing the frozen
    dataclass.

    Parameters
    ----------
    ai_use_case_id:
        The UseCase ID this manifest governs.
    release_version:
        Semantic version (e.g. "2.0.1").
    authority_level:
        AuthorityOutcome ceiling.  Cannot be A5.
    risk_tier:
        RiskTier ceiling.
    model_profile_id:
        Logical model profile identifier.
    resolved_model_id:
        Exact pinned model version at approval time.
    prompt_profile_id:
        The PromptProfile ID.
    evaluation_profile_id:
        The EvaluationProfile ID.
    policy_bundle_id:
        Opaque policy bundle identifier.
    allowed_regions:
        Regions where this manifest may be executed.
    approved_at:
        UTC approval timestamp.  Defaults to now(UTC).
    agent_profile_id:
        Optional AgentProfile ID.
    tool_policy_id:
        Optional tool policy identifier.
    retrieval_corpus_version:
        Optional RAG corpus version.
    embedding_model_version:
        Optional embedding model version.
    fallback_profile_id:
        Optional fallback model profile ID.
    manifest_id:
        Explicit manifest ID.  Auto-generated when not supplied.

    Returns
    -------
    AIReleaseManifest
        The sealed, hashed manifest.

    Raises
    ------
    AIReleaseManifestError
        If any mandatory field is empty or authority_level is A5.
    """
    mid = manifest_id or uuid.uuid4().hex[:16]
    ts = approved_at if approved_at is not None else datetime.now(tz=UTC)
    regions: frozenset[str] = (
        frozenset(allowed_regions)
        if not isinstance(allowed_regions, frozenset)
        else allowed_regions
    )

    sentinel = _ManifestSentinel(
        manifest_id=mid,
        release_version=release_version,
        ai_use_case_id=ai_use_case_id,
        authority_level=authority_level,
        risk_tier=risk_tier,
        model_profile_id=model_profile_id,
        resolved_model_id=resolved_model_id,
        prompt_profile_id=prompt_profile_id,
        evaluation_profile_id=evaluation_profile_id,
        policy_bundle_id=policy_bundle_id,
        allowed_regions=regions,
        approved_at=ts,
        agent_profile_id=agent_profile_id,
        tool_policy_id=tool_policy_id,
        retrieval_corpus_version=retrieval_corpus_version,
        embedding_model_version=embedding_model_version,
        fallback_profile_id=fallback_profile_id,
    )
    digest = _compute_sentinel_hash(sentinel)

    return AIReleaseManifest(
        manifest_id=mid,
        release_version=release_version,
        ai_use_case_id=ai_use_case_id,
        authority_level=authority_level,
        risk_tier=risk_tier,
        model_profile_id=model_profile_id,
        resolved_model_id=resolved_model_id,
        prompt_profile_id=prompt_profile_id,
        evaluation_profile_id=evaluation_profile_id,
        policy_bundle_id=policy_bundle_id,
        allowed_regions=regions,
        approved_at=ts,
        agent_profile_id=agent_profile_id,
        tool_policy_id=tool_policy_id,
        retrieval_corpus_version=retrieval_corpus_version,
        embedding_model_version=embedding_model_version,
        fallback_profile_id=fallback_profile_id,
        manifest_hash=digest,
    )


# ---------------------------------------------------------------------------
# Manifest Registry
# ---------------------------------------------------------------------------


@dataclass
class ManifestRegistry:
    """Track and gate AIReleaseManifest objects at runtime.

    ManifestRegistry.get() is governance-gated: it calls authorise() before
    returning a manifest so that the lookup itself is an auditable governed
    action (ADR-0006 §2.5).

    Example::

        reg = ManifestRegistry()
        manifest = build_manifest(ai_use_case_id=..., ...)
        reg.add(manifest)

        # Later, at runtime (governance-gated):
        active = reg.get(manifest.manifest_id, use_case_registry, provenance)
    """

    def __init__(self) -> None:
        self._manifests: dict[str, AIReleaseManifest] = {}
        self._status: dict[str, ManifestStatus] = {}
        # Optional lifecycle-readiness gate installed by LifecycleAwareManifestRegistry.
        # Callable[[manifest_id], True-if-production-ready].
        # When None (the default), no lifecycle check is performed.
        self._lifecycle_checker: Callable[[str], bool] | None = None

    # ------------------------------------------------------------------
    # Mutation
    # ------------------------------------------------------------------

    def add(self, manifest: AIReleaseManifest) -> None:
        """Register a manifest as ACTIVE.

        Raises
        ------
        ManifestRegistryError
            If a manifest with the same ID is already registered.
        """
        if manifest.manifest_id in self._manifests:
            raise ManifestRegistryError(
                f"manifest-registry: {manifest.manifest_id!r} is already registered"
            )
        self._manifests[manifest.manifest_id] = manifest
        self._status[manifest.manifest_id] = ManifestStatus.ACTIVE

    def set_lifecycle_checker(self, checker: Callable[[str], bool]) -> None:
        """Install a lifecycle-readiness gate on :meth:`get`.

        When installed, every call to :meth:`get` will invoke *checker* with
        the requested ``manifest_id`` after the governance and status checks.
        If *checker* returns ``False`` the manifest is refused with
        :class:`ManifestRegistryError`.

        This method is intentionally the only way to install the checker —
        there is no public attribute, and the check cannot be bypassed through
        the public API.

        Called by :class:`~ztax_gateway.release_lifecycle.LifecycleAwareManifestRegistry`
        on every :meth:`add` so that the checker is always current.
        """
        self._lifecycle_checker = checker

    def revoke(self, manifest_id: str) -> None:
        """Mark a manifest as REVOKED.

        Deliberately accepts unknown IDs: during an incident the ID in hand
        may not be findable in the registry, and refusing to act on it is
        the wrong behaviour for a revocation switch.
        """
        self._status[manifest_id] = ManifestStatus.REVOKED

    def supersede(self, manifest_id: str) -> None:
        """Mark a manifest as SUPERSEDED by a newer version."""
        if manifest_id in self._status:
            self._status[manifest_id] = ManifestStatus.SUPERSEDED

    # ------------------------------------------------------------------
    # Queries
    # ------------------------------------------------------------------

    def status(self, manifest_id: str) -> ManifestStatus | None:
        """Return the current ManifestStatus, or None if unknown."""
        return self._status.get(manifest_id)

    def get(
        self,
        manifest_id: str,
        registry: UseCaseRegistry,
        provenance: Provenance,
    ) -> AIReleaseManifest:
        """Return the active manifest, or raise.

        Governance gate: provenance is passed to authorise() before any
        manifest is returned.

        Raises
        ------
        GovernanceRefusedError
            If provenance is not authorised.
        ManifestRegistryError
            If the manifest is unknown, revoked, or superseded.
        """
        # 1. Governance gate -- mandatory P0.
        authorise(registry, provenance)

        # 2. Existence check.
        manifest = self._manifests.get(manifest_id)
        if manifest is None:
            raise ManifestRegistryError(
                f"manifest-registry: {manifest_id!r} is not registered"
            )

        # 3. Status check.
        current_status = self._status.get(manifest_id, ManifestStatus.ACTIVE)
        if current_status is ManifestStatus.REVOKED:
            raise ManifestRegistryError(
                f"manifest-registry: {manifest_id!r} has been revoked and may "
                "not be used in new invocations"
            )
        if current_status is ManifestStatus.SUPERSEDED:
            raise ManifestRegistryError(
                f"manifest-registry: {manifest_id!r} has been superseded and may "
                "not be used in new invocations"
            )

        # 4. Lifecycle-readiness check (installed by LifecycleAwareManifestRegistry).
        #    This is the runtime block that prevents a manifest whose lifecycle
        #    state is still RESEARCH/DESIGN/VALIDATION/SHADOW from being used
        #    in any governed action (evaluation_evidence.build, etc.).
        if self._lifecycle_checker is not None and not self._lifecycle_checker(manifest_id):
            raise ManifestRegistryError(
                f"manifest-registry: {manifest_id!r} is not production-ready — "
                "its lifecycle state does not permit live invocations"
            )

        return manifest

    def active_ids(self) -> list[str]:
        """Return the sorted list of currently active manifest IDs."""
        return sorted(
            mid
            for mid, st in self._status.items()
            if st is ManifestStatus.ACTIVE
        )

    def ids_for_use_case(self, ai_use_case_id: str) -> list[str]:
        """Return sorted manifest IDs for a given use case (any status)."""
        return sorted(
            mid
            for mid, m in self._manifests.items()
            if m.ai_use_case_id == ai_use_case_id
        )


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class _ManifestSentinel:
    """Intermediate structure used to compute the hash before sealing."""

    manifest_id: str
    release_version: str
    ai_use_case_id: str
    authority_level: AuthorityOutcome
    risk_tier: RiskTier
    model_profile_id: str
    resolved_model_id: str
    prompt_profile_id: str
    evaluation_profile_id: str
    policy_bundle_id: str
    allowed_regions: frozenset[str]
    approved_at: datetime
    agent_profile_id: str | None
    tool_policy_id: str | None
    retrieval_corpus_version: str | None
    embedding_model_version: str | None
    fallback_profile_id: str | None


def _canonical_sentinel(s: _ManifestSentinel) -> str:
    """Produce a stable, deterministic JSON string for hashing."""
    return json.dumps(
        {
            "manifest_id": s.manifest_id,
            "release_version": s.release_version,
            "ai_use_case_id": s.ai_use_case_id,
            "authority_level": s.authority_level.value,
            "risk_tier": s.risk_tier.value,
            "model_profile_id": s.model_profile_id,
            "resolved_model_id": s.resolved_model_id,
            "prompt_profile_id": s.prompt_profile_id,
            "evaluation_profile_id": s.evaluation_profile_id,
            "policy_bundle_id": s.policy_bundle_id,
            "allowed_regions": sorted(s.allowed_regions),
            "approved_at": s.approved_at.isoformat(),
            "agent_profile_id": s.agent_profile_id,
            "tool_policy_id": s.tool_policy_id,
            "retrieval_corpus_version": s.retrieval_corpus_version,
            "embedding_model_version": s.embedding_model_version,
            "fallback_profile_id": s.fallback_profile_id,
        },
        sort_keys=True,
        separators=(",", ":"),
    )


def _compute_sentinel_hash(sentinel: _ManifestSentinel) -> str:
    """Return the SHA-256 hex digest of sentinel's canonical form."""
    return hashlib.new(
        _HASH_ALGORITHM,
        _canonical_sentinel(sentinel).encode("utf-8"),
    ).hexdigest()


def _compute_manifest_hash(manifest: AIReleaseManifest) -> str:
    """Return the SHA-256 hex digest of manifest's canonical form."""
    sentinel = _ManifestSentinel(
        manifest_id=manifest.manifest_id,
        release_version=manifest.release_version,
        ai_use_case_id=manifest.ai_use_case_id,
        authority_level=manifest.authority_level,
        risk_tier=manifest.risk_tier,
        model_profile_id=manifest.model_profile_id,
        resolved_model_id=manifest.resolved_model_id,
        prompt_profile_id=manifest.prompt_profile_id,
        evaluation_profile_id=manifest.evaluation_profile_id,
        policy_bundle_id=manifest.policy_bundle_id,
        allowed_regions=manifest.allowed_regions,
        approved_at=manifest.approved_at,
        agent_profile_id=manifest.agent_profile_id,
        tool_policy_id=manifest.tool_policy_id,
        retrieval_corpus_version=manifest.retrieval_corpus_version,
        embedding_model_version=manifest.embedding_model_version,
        fallback_profile_id=manifest.fallback_profile_id,
    )
    return _compute_sentinel_hash(sentinel)
