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

5. **ProviderProfile** (§7) -- a model provider that has passed due
   diligence: legal entity, DPA, security and audit assurance, and the exact
   set of hosts the Gateway may reach for it.

6. **ModelProfile** (§7) -- an approved model, pinned to an exact version.
   model_version rejects floating aliases such as "latest" for the same
   reason AIReleaseManifest.resolved_model_id does (AIGOV-001 §12).

7. **AIReleaseManifest** (§14.1) -- the immutable, cryptographically hashed
   deployment bundle.  manifest_hash is a SHA-256 of the canonical field
   representation, making tampering detectable.

8. **ManifestRegistry** -- the runtime gatekeeper tracking ACTIVE / REVOKED
   / SUPERSEDED manifests.

ProviderProfile and ModelProfile are value objects only, matching
AgentProfile/DatasetProfile/EvaluationProfile -- there is no registry class
for them here. The one profile type that does have a registry
(PromptProfileRegistry) lives in production_gates.py, not in this module.

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
import re
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
    "Modality",
    "ModelHosting",
    "ModelProfile",
    "ModelProfileError",
    "PromptProfile",
    "PromptProfileError",
    "ProviderProfile",
    "ProviderProfileError",
    "build_manifest",
]


# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_HASH_ALGORITHM: Final[str] = "sha256"

# Floating provider aliases that must never govern material behaviour
# (AIGOV-001 §12): a silent backend revision behind one of these names is
# treated as an undeclared model change. Exact list from
# contracts/schemas/ai/model-profile.schema.json's model_version "not".
_MODEL_VERSION_ALIASES: Final[frozenset[str]] = frozenset(
    {"latest", "default", "stable", "preview"}
)

# The PRIV-001 §4 privacy-class vocabulary. Mirrors wire.py's _PRIVACY_CLASSES
# and contracts/privacy/vocabulary.json -- defined here rather than imported
# from wire.py to avoid coupling this module to the wire layer; the two lists
# must stay identical by construction (same range(8) formula), not by import.
_PRIVACY_CLASSES: Final[frozenset[str]] = frozenset(f"P{i}" for i in range(8))

# Exact copies of the patterns and limits in contracts/schemas/ai -- kept here
# so ModelProfile/ProviderProfile neither accept nor reject anything the
# schema wouldn't, on either side.
_IDENTIFIER_MAX_LENGTH: Final[int] = 128  # common.schema.json:10 ("identifier")
_SEMVER_PATTERN: Final[re.Pattern[str]] = re.compile(
    r"^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$"  # common.schema.json:20 ("version")
)
_HOSTNAME_PATTERN: Final[re.Pattern[str]] = re.compile(
    r"^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$"
    # provider-profile.schema.json:48 ("api_hosts" items)
)


def _check_string_list(
    items: tuple[str, ...],
    *,
    field_name: str,
    profile_id: str,
    label: str,
    error_cls: type[Exception],
) -> None:
    """Enforce common.schema.json's ``stringList`` shape: every item is a
    non-empty string, and there are no duplicates. Minimum length is
    deliberately not enforced here -- stringList itself has no ``minItems``,
    so an empty list is valid.
    """
    if any(not item for item in items):
        raise error_cls(
            f"{label}: {profile_id!r} {field_name} must not contain an empty string"
        )
    if len(items) != len(set(items)):
        raise error_cls(
            f"{label}: {profile_id!r} {field_name} must not contain duplicates, "
            f"got {items!r}"
        )


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


class ProviderProfileError(Exception):
    """Raised when a ProviderProfile cannot be constructed or used."""

    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason


class ModelProfileError(Exception):
    """Raised when a ModelProfile cannot be constructed or used."""

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


class ModelHosting(StrEnum):
    """Where a ModelProfile's model runs (Chapter 17 §7).

    PROVIDER_HOSTED -- the provider's own multi-tenant infrastructure.
    PRIVATE_CLOUD   -- a dedicated, single-tenant cloud deployment.
    SELF_HOSTED     -- weights deployed on infrastructure Zoiko or the
                       customer controls directly.
    """

    PROVIDER_HOSTED = "PROVIDER_HOSTED"
    PRIVATE_CLOUD = "PRIVATE_CLOUD"
    SELF_HOSTED = "SELF_HOSTED"


class Modality(StrEnum):
    """An input/output modality a ModelProfile supports (Chapter 17 §7)."""

    TEXT = "TEXT"
    IMAGE = "IMAGE"
    AUDIO = "AUDIO"
    DOCUMENT = "DOCUMENT"
    EMBEDDING = "EMBEDDING"


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
# Provider Profile  (§7)
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class ProviderProfile:
    """A model provider that has passed due diligence (Chapter 17 §7).

    Attributes
    ----------
    provider_profile_id:
        Stable, opaque identifier.
    owner:
        The team or individual accountable for this provider relationship --
        NOT the same thing as ``ProviderRecord.owner_id`` in resilience.py,
        which is the customer/tenant identifier for a private (§25)
        deployment. This ``owner`` is always a Zoiko team; a private
        deployment's ``ProviderRecord.owner_id`` names whose deployment it is.
    version:
        Semantic version string.
    legal_entity:
        The provider's contracting legal entity.
    dpa_ref:
        Reference to the signed data processing agreement.
    security_assurance:
        Tuple of security assurance credentials (e.g. "ISO27001"). May be
        empty; items, if any, must be non-empty and unique.
    permitted_regions:
        Regions this provider is approved to serve. May be empty -- empty
        means no region is permitted, the correct default for a provider
        that has not been through residency review (common.schema.json's
        "regions" definition).
    subprocessors:
        Tuple of named subprocessors this provider discloses. May be empty;
        items, if any, must be non-empty and unique.
    retention_days:
        How long the provider retains prompts and outputs.
    zero_data_retention:
        True when the provider contractually guarantees no retention.
    trains_on_customer_data:
        True if the provider may train on customer/licensed content.
        AIGOV-001 §7 defaults this OFF; a provider with this True requires
        an explicit, reviewed exception.
    deprecation_notice_days:
        Contractual notice period before the provider may deprecate a model.
    outage_terms_ref:
        Reference to the provider's outage/SLA terms.
    audit_assurance_ref:
        Reference to the provider's audit assurance report (e.g. SOC2).
    api_hosts:
        The complete set of hosts the Gateway may reach for this provider.
        Must be non-empty, lower-case DNS host names, with no duplicates: a
        provider profile with no declared host grants no egress.
    suspended:
        When True this provider may not be used in production.
    """

    provider_profile_id: str
    owner: str
    version: str
    legal_entity: str
    dpa_ref: str
    security_assurance: tuple[str, ...]
    permitted_regions: frozenset[str]
    subprocessors: tuple[str, ...]
    retention_days: int
    zero_data_retention: bool
    trains_on_customer_data: bool
    deprecation_notice_days: int
    outage_terms_ref: str
    audit_assurance_ref: str
    api_hosts: tuple[str, ...]
    suspended: bool

    def __post_init__(self) -> None:
        if not self.provider_profile_id:
            raise ProviderProfileError(
                "provider-profile: provider_profile_id must not be empty"
            )
        if len(self.provider_profile_id) > _IDENTIFIER_MAX_LENGTH:
            raise ProviderProfileError(
                f"provider-profile: provider_profile_id {self.provider_profile_id!r} "
                f"exceeds the maximum length of {_IDENTIFIER_MAX_LENGTH}"
            )
        if not self.owner:
            raise ProviderProfileError(
                f"provider-profile: {self.provider_profile_id!r} has no owner"
            )
        if not self.version:
            raise ProviderProfileError(
                f"provider-profile: {self.provider_profile_id!r} has no version"
            )
        if not _SEMVER_PATTERN.match(self.version):
            raise ProviderProfileError(
                f"provider-profile: {self.provider_profile_id!r} version "
                f"{self.version!r} is not a semantic version"
            )
        if not self.legal_entity:
            raise ProviderProfileError(
                f"provider-profile: {self.provider_profile_id!r} has no legal_entity"
            )
        if not self.dpa_ref:
            raise ProviderProfileError(
                f"provider-profile: {self.provider_profile_id!r} has no dpa_ref"
            )
        _check_string_list(
            self.security_assurance,
            field_name="security_assurance",
            profile_id=self.provider_profile_id,
            label="provider-profile",
            error_cls=ProviderProfileError,
        )
        _check_string_list(
            self.subprocessors,
            field_name="subprocessors",
            profile_id=self.provider_profile_id,
            label="provider-profile",
            error_cls=ProviderProfileError,
        )
        if self.retention_days < 0:
            raise ProviderProfileError(
                f"provider-profile: {self.provider_profile_id!r} retention_days "
                f"must be >= 0, got {self.retention_days!r}"
            )
        if self.deprecation_notice_days < 0:
            raise ProviderProfileError(
                f"provider-profile: {self.provider_profile_id!r} "
                f"deprecation_notice_days must be >= 0, got "
                f"{self.deprecation_notice_days!r}"
            )
        if not self.outage_terms_ref:
            raise ProviderProfileError(
                f"provider-profile: {self.provider_profile_id!r} has no outage_terms_ref"
            )
        if not self.audit_assurance_ref:
            raise ProviderProfileError(
                f"provider-profile: {self.provider_profile_id!r} has no audit_assurance_ref"
            )
        if not self.api_hosts:
            raise ProviderProfileError(
                f"provider-profile: {self.provider_profile_id!r} has no api_hosts; "
                "a provider profile with no declared host grants no egress"
            )
        if len(self.api_hosts) != len(set(self.api_hosts)):
            raise ProviderProfileError(
                f"provider-profile: {self.provider_profile_id!r} api_hosts must not "
                f"contain duplicates, got {self.api_hosts!r}"
            )
        bad_hosts = [h for h in self.api_hosts if not _HOSTNAME_PATTERN.match(h)]
        if bad_hosts:
            raise ProviderProfileError(
                f"provider-profile: {self.provider_profile_id!r} api_hosts contains "
                f"non-hostname value(s): {bad_hosts!r}"
            )

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation."""
        return {
            "provider_profile_id": self.provider_profile_id,
            "owner": self.owner,
            "version": self.version,
            "legal_entity": self.legal_entity,
            "dpa_ref": self.dpa_ref,
            "security_assurance": list(self.security_assurance),
            "permitted_regions": sorted(self.permitted_regions),
            "subprocessors": list(self.subprocessors),
            "retention_days": self.retention_days,
            "zero_data_retention": self.zero_data_retention,
            "trains_on_customer_data": self.trains_on_customer_data,
            "deprecation_notice_days": self.deprecation_notice_days,
            "outage_terms_ref": self.outage_terms_ref,
            "audit_assurance_ref": self.audit_assurance_ref,
            "api_hosts": list(self.api_hosts),
            "suspended": self.suspended,
        }


# ---------------------------------------------------------------------------
# Model Profile  (§7)
# ---------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
class ModelProfile:
    """An approved model, pinned to an exact version (Chapter 17 §7).

    Attributes
    ----------
    model_profile_id:
        Stable, opaque identifier.
    owner:
        Team or individual responsible.
    version:
        Semantic version string of this profile entry.
    provider_profile_id:
        The ProviderProfile this model is served by.
    model_id:
        The provider's own name for the model.
    model_version:
        The exact provider model version. Floating aliases ("latest",
        "default", "stable", "preview") are refused: AIGOV-001 §12 prohibits
        provider aliases governing material behaviour, and a silent backend
        revision is treated as a model change.
    hosting:
        Where the model runs.
    context_window_tokens:
        Maximum input context size, in tokens.
    max_output_tokens:
        Maximum output size, in tokens.
    modalities:
        The input/output modalities this model supports. Must be non-empty
        and contain no duplicates.
    tool_capable:
        True if the model supports tool/function calling.
    permitted_regions:
        Regions this model may be invoked from. May be empty -- empty means
        no region is permitted, the correct default for a model that has
        not been through residency review (common.schema.json's "regions"
        definition).
    permitted_data_classes:
        The PRIV-001 classes this model may receive. May be empty -- deny by
        default, so a model with no declared class may receive nothing.
        Any classes that ARE declared must be drawn from the P0-P7
        vocabulary (same vocabulary as wire.py's _PRIVACY_CLASSES and
        contracts/privacy/vocabulary.json).
    terms_ref:
        Reference to the contractual terms governing this model's use.
    known_limitations:
        Tuple of known limitations reviewers should account for. May be
        empty; items, if any, must be non-empty and unique.
    deprecation_date:
        ISO-8601 date string, or None if not yet scheduled for deprecation.
    suspended:
        When True this model may not be used in production.
    """

    model_profile_id: str
    owner: str
    version: str
    provider_profile_id: str
    model_id: str
    model_version: str
    hosting: ModelHosting
    context_window_tokens: int
    max_output_tokens: int
    modalities: tuple[Modality, ...]
    tool_capable: bool
    permitted_regions: frozenset[str]
    permitted_data_classes: frozenset[str]
    terms_ref: str
    known_limitations: tuple[str, ...]
    deprecation_date: str | None
    suspended: bool

    def __post_init__(self) -> None:
        if not self.model_profile_id:
            raise ModelProfileError("model-profile: model_profile_id must not be empty")
        if len(self.model_profile_id) > _IDENTIFIER_MAX_LENGTH:
            raise ModelProfileError(
                f"model-profile: model_profile_id {self.model_profile_id!r} "
                f"exceeds the maximum length of {_IDENTIFIER_MAX_LENGTH}"
            )
        if not self.owner:
            raise ModelProfileError(
                f"model-profile: {self.model_profile_id!r} has no owner"
            )
        if not self.version:
            raise ModelProfileError(
                f"model-profile: {self.model_profile_id!r} has no version"
            )
        if not _SEMVER_PATTERN.match(self.version):
            raise ModelProfileError(
                f"model-profile: {self.model_profile_id!r} version {self.version!r} "
                "is not a semantic version"
            )
        if not self.provider_profile_id:
            raise ModelProfileError(
                f"model-profile: {self.model_profile_id!r} has no provider_profile_id"
            )
        if len(self.provider_profile_id) > _IDENTIFIER_MAX_LENGTH:
            raise ModelProfileError(
                f"model-profile: provider_profile_id {self.provider_profile_id!r} "
                f"exceeds the maximum length of {_IDENTIFIER_MAX_LENGTH}"
            )
        if not self.model_id:
            raise ModelProfileError(
                f"model-profile: {self.model_profile_id!r} has no model_id"
            )
        if not self.model_version:
            raise ModelProfileError(
                f"model-profile: {self.model_profile_id!r} has no model_version"
            )
        if self.model_version in _MODEL_VERSION_ALIASES:
            raise ModelProfileError(
                f"model-profile: {self.model_profile_id!r} declares model_version "
                f"{self.model_version!r}, which is a floating alias (AIGOV-001 §12); "
                "pin an exact provider version instead"
            )
        if self.context_window_tokens < 1:
            raise ModelProfileError(
                f"model-profile: {self.model_profile_id!r} context_window_tokens "
                f"must be >= 1, got {self.context_window_tokens!r}"
            )
        if self.max_output_tokens < 1:
            raise ModelProfileError(
                f"model-profile: {self.model_profile_id!r} max_output_tokens "
                f"must be >= 1, got {self.max_output_tokens!r}"
            )
        if not self.modalities:
            raise ModelProfileError(
                f"model-profile: {self.model_profile_id!r} has no modalities"
            )
        if len(self.modalities) != len(set(self.modalities)):
            raise ModelProfileError(
                f"model-profile: {self.model_profile_id!r} modalities must not "
                f"contain duplicates, got {self.modalities!r}"
            )
        # permitted_regions and permitted_data_classes may be empty: both
        # common.schema.json's "regions" and "privacyClasses" definitions
        # document empty as the correct default (nothing permitted), not an
        # error -- see those $defs for the rationale.
        unknown_classes = self.permitted_data_classes - _PRIVACY_CLASSES
        if unknown_classes:
            raise ModelProfileError(
                f"model-profile: {self.model_profile_id!r} declares unknown "
                f"privacy class(es) {sorted(unknown_classes)!r}; must be drawn "
                f"from {sorted(_PRIVACY_CLASSES)!r}"
            )
        if not self.terms_ref:
            raise ModelProfileError(
                f"model-profile: {self.model_profile_id!r} has no terms_ref"
            )
        _check_string_list(
            self.known_limitations,
            field_name="known_limitations",
            profile_id=self.model_profile_id,
            label="model-profile",
            error_cls=ModelProfileError,
        )

    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation."""
        return {
            "model_profile_id": self.model_profile_id,
            "owner": self.owner,
            "version": self.version,
            "provider_profile_id": self.provider_profile_id,
            "model_id": self.model_id,
            "model_version": self.model_version,
            "hosting": self.hosting.value,
            "context_window_tokens": self.context_window_tokens,
            "max_output_tokens": self.max_output_tokens,
            "modalities": [m.value for m in self.modalities],
            "tool_capable": self.tool_capable,
            "permitted_regions": sorted(self.permitted_regions),
            "permitted_data_classes": sorted(self.permitted_data_classes),
            "terms_ref": self.terms_ref,
            "known_limitations": list(self.known_limitations),
            "deprecation_date": self.deprecation_date,
            "suspended": self.suspended,
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
