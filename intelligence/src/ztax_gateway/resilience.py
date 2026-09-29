"""Provider/Model Registry and Fallback Router.
 
Chapter 17 §24 (with routing rules from §17) and §25 (Private, Sovereign &
Edge AI) of the ZoikoTax Master Specification.
 
Enforces the qualified-provider-fallback policy:
 
* A fallback provider must carry the ``is_qualified`` flag **and** must share
  every permitted region of the failed provider — geography is never widened.
* When no qualified same-region alternate exists the use case is degraded, not
  silently rerouted.
* RAG/index outages never produce a fabricated grounded answer; they always
  produce :attr:`~DegradedMode.REVIEW` or :attr:`~DegradedMode.ABSTAIN`.
* Evaluation service outages block new release promotion but let the current
  release continue.
* A total AI outage leaves deterministic tax workflows running.
* No live model provider is required; this module is routing and policy logic
  only.
 
§25 additions
-------------
* Customer-owned (private) providers are marked with ``is_private=True`` and an
  ``owner_id``.  They are gated by exactly the same qualification and authority
  checks as any other provider — ``is_private`` is not a privilege, it is a
  classification.
* Model weight provenance is carried by :class:`ModelWeightRecord`: version,
  SHA-256 integrity hash, licence SPDX expression and security-scan status.
  Every locally-deployed model must have one.
* Disconnected edge devices run on a signed :class:`EdgePolicySnapshot` — a
  local copy of the :class:`~ztax_gateway.governance.UseCaseRegistry` with its
  manifest ID, integrity hash and signature.  The snapshot is the only
  governance authority when there is no live control-plane connection.
* While disconnected, the A0-A5 ceiling and the kill switch *still apply*:
  :func:`authorise_edge` runs the same :func:`~ztax_gateway.governance.authorise`
  call against the snapshot's registry, so a prohibited action remains prohibited
  even when central controls are unreachable.  The snapshot cannot add
  authority; it can only preserve the authority that was signed into it.
 
What this module provides
--------------------------
:class:`WeightScanStatus`
    Security-scan verdict for a set of model weights.
 
:class:`ModelWeightRecord`
    Provenance record for a locally-deployed model weight artifact: version,
    SHA-256 integrity hash, SPDX licence expression and scan status.
 
:class:`ProviderRecord`
    One registered provider: identity, permitted regions, qualified flag, and
    (for §25) the ``is_private`` / ``owner_id`` / ``weight_record`` fields that
    distinguish a customer-owned local deployment from a cloud provider.
 
:class:`ModelRecord`
    One registered model: identity, the provider it belongs to, a deprecated
    flag, and the data-class list it may process.
 
:class:`ProviderRegistry`
    Stores :class:`ProviderRecord` and :class:`ModelRecord` objects.  Offers
    lookup and iteration.
 
:class:`FailureKind`
    The nine failure categories (eight from §24 plus ``DISCONNECTED_EDGE`` from §25).
 
:class:`DegradedMode`
    The four degraded-mode decisions: ``FALLBACK``, ``DEGRADE``, ``ABSTAIN``,
    ``SUSPEND``.
 
:class:`RoutingOutcome`
    The result of a routing decision: the chosen provider, the chosen model
    (may be ``None`` on non-FALLBACK modes), the :class:`DegradedMode`, and a
    human-readable rationale string.
 
:class:`EdgePolicySnapshot`
    A signed, locally-cached copy of the :class:`~ztax_gateway.governance.UseCaseRegistry`
    for use on disconnected edge devices.  Carries manifest ID, SHA-256 hash
    and signature bytes so the runtime can verify it has not been tampered with
    since the control plane signed it.
 
:class:`FallbackRouter`
    Consumes the :class:`ProviderRegistry`, the active provider/model, the
    permitted regions from :class:`~ztax_gateway.provenance.Provenance`, and
    the :class:`FailureKind`.  Returns a :class:`RoutingOutcome`.
 
:class:`ResilienceError`
    Raised when a routing invariant is broken (not a routing failure — routing
    failures produce a ``RoutingOutcome``).
 
:func:`authorise_edge`
    Runs the standard governance authorise() check against an
    :class:`EdgePolicySnapshot`, enforcing that offline AI cannot gain more
    authority because central controls are unreachable.
 
Design rules (enforced here; no exceptions, no overrides)
----------------------------------------------------------
1. **Region lock.**  A fallback provider must permit *every* region the failed
   provider permits.  No geography widening is allowed even for a single
   request.
2. **Qualification gate.**  Only providers with ``is_qualified=True`` may
   receive a fallback.  Customer-owned private providers must also pass this
   gate before they may receive a fallback.
3. **RAG outage → never fabricate.**  A :attr:`~FailureKind.RAG_OUTAGE` always
   produces :attr:`~DegradedMode.REVIEW` or :attr:`~DegradedMode.ABSTAIN`, never
   :attr:`~DegradedMode.FALLBACK`.
4. **Tool outage → partial/blocked, never invented.**  A
   :attr:`~FailureKind.TOOL_OUTAGE` produces :attr:`~DegradedMode.DEGRADE` or
   :attr:`~DegradedMode.SUSPEND`.
5. **Evaluation outage → block promotion.**  A
   :attr:`~FailureKind.EVALUATION_OUTAGE` produces :attr:`~DegradedMode.DEGRADE`;
   the current release runs, new releases are blocked.
6. **Total AI outage → deterministic workflows continue.**  A
   :attr:`~FailureKind.TOTAL_OUTAGE` produces :attr:`~DegradedMode.SUSPEND` for
   AI use cases; non-AI deterministic paths are unaffected.
7. **No fiscal imports.**  ADR-0006 §2.6 isolation.
8. **No live I/O.**  This module is pure logic; it does not call any model API.
9. **Edge offline cannot escalate authority.**  :func:`authorise_edge` runs the
   full governance check against the local signed snapshot.  A5 stays refused;
   the kill switch inside the snapshot is honoured; no authority ceiling is
   raised because the control plane is unreachable.
10. **Customer-provided models require qualification.**  A
    :class:`ProviderRecord` with ``is_private=True`` must still have
    ``is_qualified=True`` before the router will route to it.  Private is a
    classification, not a bypass.
"""
 
from __future__ import annotations
 
from dataclasses import dataclass, field
from enum import StrEnum
from typing import Final
 
from .governance import UseCase, UseCaseRegistry, authorise
from .provenance import Provenance
 
__all__: list[str] = [
    "DegradedMode",
    "EdgePolicySnapshot",
    "FailureKind",
    "FallbackRouter",
    "ModelRecord",
    "ModelWeightRecord",
    "ProviderRecord",
    "ProviderRegistry",
    "ResilienceError",
    "RoutingOutcome",
    "WeightScanStatus",
    "authorise_edge",
]
 
# ---------------------------------------------------------------------------
# ResilienceError
# ---------------------------------------------------------------------------
 
 
class ResilienceError(Exception):
    """Raised when a routing invariant is broken.
 
    This is *not* raised for expected routing failures (provider outage, no
    fallback available, etc.); those produce a :class:`RoutingOutcome`.
    This exception signals programming or configuration errors.
 
    ``reason``  -- a human-readable description of the invariant violation.
    """
 
    def __init__(self, reason: str) -> None:
        super().__init__(reason)
        self.reason = reason
 
 
# ---------------------------------------------------------------------------
# §25 -- WeightScanStatus and ModelWeightRecord
# ---------------------------------------------------------------------------
 
 
class WeightScanStatus(StrEnum):
    """Security-scan verdict for a set of model weight artifacts.
 
    ``CLEAN``       -- scan completed and no threats were found.
    ``PENDING``     -- scan has not yet completed; weights must not be used in
                       production until the status transitions to ``CLEAN``.
    ``FAILED``      -- scan found a threat or completed with errors; weights
                       must be quarantined.
    """
 
    CLEAN = "CLEAN"
    PENDING = "PENDING"
    FAILED = "FAILED"
 
 
@dataclass(frozen=True, slots=True)
class ModelWeightRecord:
    """Provenance record for a locally-deployed model weight artifact.
 
    §25 requires model weights to be versioned artifacts with provenance,
    integrity hashes, licence terms and security-scan evidence before they may
    be used in a private or edge deployment.
 
    ``artifact_id``     -- stable identifier for the weight bundle, e.g.
                           ``"gemini-flash-2b@2026.09"``.
    ``sha256_hex``      -- lowercase hex SHA-256 digest of the weight bundle
                           as shipped.  Must be exactly 64 hex characters.
    ``spdx_licence``    -- SPDX licence expression, e.g. ``"Apache-2.0"`` or
                           ``"LicenseRef-ZoikoTax-proprietary"``.
    ``scan_status``     -- :class:`WeightScanStatus` from the most recent
                           security scan.  Only ``CLEAN`` weights may be used
                           in production or A3-adjacent flows.
    """
 
    artifact_id: str
    sha256_hex: str
    spdx_licence: str
    scan_status: WeightScanStatus
 
    def __post_init__(self) -> None:
        if not self.artifact_id:
            raise ResilienceError(
                "resilience: ModelWeightRecord.artifact_id must be non-empty"
            )
        if len(self.sha256_hex) != 64 or not all(
            c in "0123456789abcdef" for c in self.sha256_hex
        ):
            raise ResilienceError(
                f"resilience: ModelWeightRecord {self.artifact_id!r} sha256_hex must be "
                "exactly 64 lowercase hex characters"
            )
        if not self.spdx_licence:
            raise ResilienceError(
                f"resilience: ModelWeightRecord {self.artifact_id!r} spdx_licence must "
                "be non-empty (use an SPDX expression or 'LicenseRef-...')"
            )
        if not isinstance(self.scan_status, WeightScanStatus):
            raise ResilienceError(
                f"resilience: ModelWeightRecord {self.artifact_id!r} scan_status must "
                "be a WeightScanStatus member"
            )
 
    @property
    def is_production_safe(self) -> bool:
        """``True`` only when the scan status is ``CLEAN``.
 
        PENDING weights are not yet cleared; FAILED weights are quarantined.
        Both are refused before entering any A3-adjacent flow.
        """
        return self.scan_status is WeightScanStatus.CLEAN
 
 
# ---------------------------------------------------------------------------
# FailureKind
# ---------------------------------------------------------------------------
 
 
class FailureKind(StrEnum):
    """The nine failure categories (§24 and §25).
 
    ``MODEL_OUTAGE``      -- the external model provider or model endpoint is
                             unavailable.
    ``REGIONAL_QUOTA``    -- the provider's regional quota is exhausted;
                             routing must stay inside the same policy region.
    ``RAG_OUTAGE``        -- the retrieval index or embedding service is down;
                             grounded answers must never be fabricated.
    ``TOOL_OUTAGE``       -- one or more agent tools are unavailable; the
                             result must be partial or blocked, never invented.
    ``EVALUATION_OUTAGE`` -- the evaluation service is unreachable; the current
                             release may continue but new promotions are blocked.
    ``EMBEDDING_OUTAGE``  -- the embedding service is down; the existing qualified
                             index keeps serving, new indexing queues, and no
                             unqualified embedding fallback is used.
    ``CONTROL_PLANE_OUTAGE`` -- the global AI control plane is unreachable; the
                             regional runtime continues on its cached signed
                             approved release/policy, but no new use case or
                             model may be activated.
    ``TOTAL_OUTAGE``      -- the entire AI inference layer is down; deterministic
                             tax workflows keep running.
 
    Eight members from Chapter 17 §24's failure table, plus one from §25.
 
    ``DISCONNECTED_EDGE`` -- the edge device has no live connection to the
                             central AI control plane; it must operate on its
                             local signed :class:`EdgePolicySnapshot`.
                             No new use case or model may be activated while
                             disconnected, and authority ceilings are
                             determined by the snapshot, not by a live lookup.
    """
 
    MODEL_OUTAGE = "MODEL_OUTAGE"
    REGIONAL_QUOTA = "REGIONAL_QUOTA"
    RAG_OUTAGE = "RAG_OUTAGE"
    EMBEDDING_OUTAGE = "EMBEDDING_OUTAGE"
    TOOL_OUTAGE = "TOOL_OUTAGE"
    EVALUATION_OUTAGE = "EVALUATION_OUTAGE"
    CONTROL_PLANE_OUTAGE = "CONTROL_PLANE_OUTAGE"
    TOTAL_OUTAGE = "TOTAL_OUTAGE"
    DISCONNECTED_EDGE = "DISCONNECTED_EDGE"
 
 
# ---------------------------------------------------------------------------
# DegradedMode
# ---------------------------------------------------------------------------
 
 
class DegradedMode(StrEnum):
    """The four degraded-mode decisions.
 
    ``FALLBACK`` -- route to a qualified alternate provider / model.
    ``DEGRADE``  -- continue in a reduced-capability mode (e.g., no RAG,
                    partial tool set, current release only).
    ``ABSTAIN``  -- produce no output; surface the outage to the caller.
    ``SUSPEND``  -- halt the AI use case entirely; non-AI deterministic paths
                    continue.
    """
 
    FALLBACK = "FALLBACK"
    DEGRADE = "DEGRADE"
    ABSTAIN = "ABSTAIN"
    SUSPEND = "SUSPEND"
 
 
# ---------------------------------------------------------------------------
# ProviderRecord
# ---------------------------------------------------------------------------
 
 
@dataclass(frozen=True, slots=True)
class ProviderRecord:
    """One registered provider.
 
    ``provider_id``      -- stable identifier, e.g. ``"gcp:vertex-eu"`` or
                            ``"customer:acme-on-prem-eu"``.
    ``display_name``     -- human-readable name.
    ``permitted_regions``-- the set of regions this provider is approved for.
                            Must be non-empty.
    ``is_qualified``     -- ``True`` when the provider has passed the data-policy
                            and evaluation qualification gate; only qualified
                            providers may receive a fallback.  Customer-owned
                            providers must also pass this gate (§25 rule 10).
    ``data_classes``     -- the data-class labels (e.g. ``"INTERNAL"``,
                            ``"CONFIDENTIAL"``) this provider is cleared to
                            process.  An empty frozenset means no restriction.
    ``is_private``       -- ``True`` for §25 customer-owned / sovereign
                            deployments.  Does not relax any governance check;
                            it is a classification used for audit and routing.
    ``owner_id``         -- the customer or tenant identifier for private
                            deployments; ``""`` for cloud providers.  Required
                            when ``is_private=True``.
    ``weight_record``    -- :class:`ModelWeightRecord` for providers that serve
                            locally-deployed weights (§25).  ``None`` for cloud
                            providers that manage their own weights.  When set,
                            the record must be ``is_production_safe`` before the
                            provider may be selected for A3-adjacent flows.
    """
 
    provider_id: str
    display_name: str
    permitted_regions: frozenset[str]
    is_qualified: bool = True
    data_classes: frozenset[str] = field(default_factory=frozenset)
    is_private: bool = False
    owner_id: str = ""
    weight_record: ModelWeightRecord | None = None
 
    def __post_init__(self) -> None:
        if not self.provider_id:
            raise ResilienceError("resilience: ProviderRecord.provider_id must be non-empty")
        if not self.permitted_regions:
            raise ResilienceError(
                f"resilience: ProviderRecord {self.provider_id!r} must have at least one "
                "permitted_region"
            )
        if self.is_private and not self.owner_id:
            raise ResilienceError(
                f"resilience: private ProviderRecord {self.provider_id!r} must have a "
                "non-empty owner_id identifying the customer or tenant"
            )
 
    def covers_regions(self, regions: frozenset[str]) -> bool:
        """Return ``True`` when *regions* is a subset of this provider's permitted regions."""
        return regions <= self.permitted_regions
 
    def allows_data_class(self, data_class: str) -> bool:
        """Return ``True`` when *data_class* is cleared for this provider.
 
        An empty :attr:`data_classes` set means the provider imposes no
        data-class restriction.
        """
        return not self.data_classes or data_class in self.data_classes
 
 
# ---------------------------------------------------------------------------
# ModelRecord
# ---------------------------------------------------------------------------
 
 
@dataclass(frozen=True, slots=True)
class ModelRecord:
    """One registered model.
 
    ``model_id``    -- stable identifier, e.g. ``"gemini-pro@2026.09"``.
    ``provider_id`` -- must match a :class:`ProviderRecord` in the registry.
    ``is_deprecated``-- ``True`` when this model should not receive new traffic.
    ``data_classes`` -- data-class restriction (same semantics as
                        :attr:`ProviderRecord.data_classes`).
    """
 
    model_id: str
    provider_id: str
    is_deprecated: bool = False
    data_classes: frozenset[str] = field(default_factory=frozenset)
 
    def __post_init__(self) -> None:
        if not self.model_id:
            raise ResilienceError("resilience: ModelRecord.model_id must be non-empty")
        if not self.provider_id:
            raise ResilienceError("resilience: ModelRecord.provider_id must be non-empty")
 
    def allows_data_class(self, data_class: str) -> bool:
        """Same semantics as :meth:`ProviderRecord.allows_data_class`."""
        return not self.data_classes or data_class in self.data_classes
 
 
# ---------------------------------------------------------------------------
# ProviderRegistry
# ---------------------------------------------------------------------------
 
 
class ProviderRegistry:
    """Stores :class:`ProviderRecord` and :class:`ModelRecord` objects.
 
    Providers and models are keyed by their ``provider_id`` / ``model_id``.
    Re-registering the same ID replaces the existing entry.
    """
 
    def __init__(self) -> None:
        self._providers: dict[str, ProviderRecord] = {}
        self._models: dict[str, ModelRecord] = {}
 
    # ---- registration ---------------------------------------------------
 
    def register_provider(self, provider: ProviderRecord) -> None:
        """Add or replace a :class:`ProviderRecord`."""
        if not isinstance(provider, ProviderRecord):
            raise ResilienceError("resilience: register_provider requires a ProviderRecord")
        self._providers[provider.provider_id] = provider
 
    def register_model(self, model: ModelRecord) -> None:
        """Add or replace a :class:`ModelRecord`.
 
        Raises :class:`ResilienceError` if the model's ``provider_id`` is not
        in the registry.
        """
        if not isinstance(model, ModelRecord):
            raise ResilienceError("resilience: register_model requires a ModelRecord")
        if model.provider_id not in self._providers:
            raise ResilienceError(
                f"resilience: model {model.model_id!r} references unknown provider "
                f"{model.provider_id!r}; register the provider first"
            )
        self._models[model.model_id] = model
 
    # ---- lookup ---------------------------------------------------------
 
    def provider(self, provider_id: str) -> ProviderRecord | None:
        """Return the :class:`ProviderRecord` for *provider_id*, or ``None``."""
        return self._providers.get(provider_id)
 
    def model(self, model_id: str) -> ModelRecord | None:
        """Return the :class:`ModelRecord` for *model_id*, or ``None``."""
        return self._models.get(model_id)
 
    # ---- iteration ------------------------------------------------------
 
    def qualified_providers(
        self,
        *,
        regions: frozenset[str],
        data_class: str = "",
        exclude_ids: frozenset[str] = frozenset(),
    ) -> list[ProviderRecord]:
        """Return qualified providers that cover *regions* and allow *data_class*.
 
        Results are sorted by ``provider_id`` for deterministic ordering.
        Providers listed in *exclude_ids* are omitted.
        """
        results: list[ProviderRecord] = []
        for p in self._providers.values():
            if p.provider_id in exclude_ids:
                continue
            if not p.is_qualified:
                continue
            if not p.covers_regions(regions):
                continue
            if data_class and not p.allows_data_class(data_class):
                continue
            results.append(p)
        results.sort(key=lambda p: p.provider_id)
        return results
 
    def models_for_provider(
        self,
        provider_id: str,
        *,
        data_class: str = "",
        exclude_deprecated: bool = True,
    ) -> list[ModelRecord]:
        """Return models belonging to *provider_id* that allow *data_class*.
 
        Results are sorted by ``model_id`` for deterministic ordering.
        """
        results: list[ModelRecord] = []
        for m in self._models.values():
            if m.provider_id != provider_id:
                continue
            if exclude_deprecated and m.is_deprecated:
                continue
            if data_class and not m.allows_data_class(data_class):
                continue
            results.append(m)
        results.sort(key=lambda m: m.model_id)
        return results
 
    @property
    def provider_count(self) -> int:
        """Number of registered providers."""
        return len(self._providers)
 
    @property
    def model_count(self) -> int:
        """Number of registered models."""
        return len(self._models)
 
 
# ---------------------------------------------------------------------------
# RoutingOutcome
# ---------------------------------------------------------------------------
 
 
@dataclass(frozen=True, slots=True)
class RoutingOutcome:
    """The result of a routing decision.
 
    ``mode``          -- :class:`DegradedMode` chosen for this failure.
    ``failure``       -- the :class:`FailureKind` that triggered routing.
    ``failed_provider_id``   -- provider that failed.
    ``fallback_provider``    -- the chosen :class:`ProviderRecord` when
                                ``mode`` is :attr:`~DegradedMode.FALLBACK`;
                                ``None`` otherwise.
    ``fallback_model``       -- the chosen :class:`ModelRecord` when a model
                                could be selected for the fallback provider;
                                ``None`` otherwise.
    ``rationale``     -- human-readable explanation of the decision.
    """
 
    mode: DegradedMode
    failure: FailureKind
    failed_provider_id: str
    fallback_provider: ProviderRecord | None
    fallback_model: ModelRecord | None
    rationale: str
 
    def as_dict(self) -> dict[str, object]:
        """Return a log-safe representation."""
        return {
            "mode": self.mode.value,
            "failure": self.failure.value,
            "failed_provider_id": self.failed_provider_id,
            "fallback_provider_id": (
                self.fallback_provider.provider_id if self.fallback_provider else None
            ),
            "fallback_model_id": (
                self.fallback_model.model_id if self.fallback_model else None
            ),
            "rationale": self.rationale,
        }
 
 
# ---------------------------------------------------------------------------
# FallbackRouter
# ---------------------------------------------------------------------------
 
# Maps each FailureKind to the mode produced when no fallback exists.
_NO_FALLBACK_MODE: Final[dict[FailureKind, DegradedMode]] = {
    FailureKind.MODEL_OUTAGE: DegradedMode.SUSPEND,
    FailureKind.REGIONAL_QUOTA: DegradedMode.SUSPEND,
    FailureKind.RAG_OUTAGE: DegradedMode.ABSTAIN,
    FailureKind.EMBEDDING_OUTAGE: DegradedMode.DEGRADE,
    FailureKind.TOOL_OUTAGE: DegradedMode.DEGRADE,
    FailureKind.EVALUATION_OUTAGE: DegradedMode.DEGRADE,
    FailureKind.CONTROL_PLANE_OUTAGE: DegradedMode.DEGRADE,
    FailureKind.TOTAL_OUTAGE: DegradedMode.SUSPEND,
    FailureKind.DISCONNECTED_EDGE: DegradedMode.DEGRADE,
}
 
 
# ---------------------------------------------------------------------------
# §25 -- EdgePolicySnapshot and authorise_edge
# ---------------------------------------------------------------------------
 
 
@dataclass(frozen=True, slots=True)
class EdgePolicySnapshot:
    """A signed, locally-cached copy of a :class:`UseCaseRegistry`.
 
    §25 requires that a disconnected edge device operate on a signed
    AIReleaseManifest/policy bundle rather than on a live control-plane
    connection.  This record is that bundle as far as governance is concerned:
    the registry is the policy; the other fields are the integrity evidence
    that prove it was signed by the control plane and has not been tampered with.
 
    ``manifest_id``  -- the release-manifest identifier this snapshot was
                        derived from, e.g. ``"rel-2026.09-edge-eu"``.
    ``sha256_hex``   -- lowercase hex SHA-256 digest of the canonical
                        serialised registry.  Exactly 64 hex characters.
    ``signature``    -- raw bytes of the control-plane signature over
                        ``sha256_hex``.  Must be non-empty; the runtime is
                        responsible for verifying it against the estate's
                        public key before constructing this object.
    ``registry``     -- the :class:`UseCaseRegistry` loaded from the bundle.
                        The registry carries any kill switches that were
                        active when the bundle was signed; those switches
                        remain effective while the device is offline.
 
    Design note
    -----------
    This type lives in :mod:`resilience` rather than :mod:`governance` because
    the question "is this device disconnected from the control plane?" is an
    *operational* / *routing* question in the same layer as "is there a
    fallback provider?".  :func:`~ztax_gateway.governance.authorise` already
    accepts any :class:`UseCaseRegistry` and applies the full check; it needs
    no changes.  The snapshot is a :class:`UseCaseRegistry` wrapper with
    integrity evidence attached — nothing more.
    """
 
    manifest_id: str
    sha256_hex: str
    signature: bytes
    registry: UseCaseRegistry
 
    def __post_init__(self) -> None:
        if not self.manifest_id:
            raise ResilienceError(
                "resilience: EdgePolicySnapshot.manifest_id must be non-empty"
            )
        if len(self.sha256_hex) != 64 or not all(
            c in "0123456789abcdef" for c in self.sha256_hex
        ):
            raise ResilienceError(
                f"resilience: EdgePolicySnapshot {self.manifest_id!r} sha256_hex must "
                "be exactly 64 lowercase hex characters"
            )
        if not self.signature:
            raise ResilienceError(
                f"resilience: EdgePolicySnapshot {self.manifest_id!r} signature must "
                "be non-empty bytes — the runtime must verify the signature before "
                "constructing this object"
            )
        if not isinstance(self.registry, UseCaseRegistry):
            raise ResilienceError(
                f"resilience: EdgePolicySnapshot {self.manifest_id!r} registry must "
                "be a UseCaseRegistry"
            )
 
 
def authorise_edge(snapshot: EdgePolicySnapshot, provenance: Provenance) -> UseCase:
    """Run the standard governance check against an :class:`EdgePolicySnapshot`.
 
    §25's single most important rule: "offline AI cannot gain more authority
    because central controls are unreachable — prohibited stays prohibited
    locally."
 
    This function enforces that rule by delegating to the same
    :func:`~ztax_gateway.governance.authorise` that all live paths use.  The
    snapshot's :class:`UseCaseRegistry` is the authority; the A0-A5 ceiling,
    the A5 unconditional refusal, and any kill switches baked into the snapshot
    all apply.  The function does not relax any check because the device is
    offline — it cannot, because it never consults a live source.
 
    Parameters
    ----------
    snapshot:
        The signed local policy bundle.
    provenance:
        The governance context for this call, exactly as on a live path.
 
    Returns
    -------
    :class:`~ztax_gateway.governance.UseCase`
        The authorised use case, if the call is permitted.
 
    Raises
    ------
    :class:`~ztax_gateway.governance.GovernanceRefusedError`
        If the call is refused for any of the standard reasons (kill switch,
        unknown use case, authority ceiling, risk tier, residency).
    :class:`ResilienceError`
        If *snapshot* is not an :class:`EdgePolicySnapshot`.
    """
    if not isinstance(snapshot, EdgePolicySnapshot):
        raise ResilienceError(
            "resilience: authorise_edge requires an EdgePolicySnapshot"
        )
    # Delegate entirely to governance.authorise — no edge-specific relaxation.
    return authorise(snapshot.registry, provenance)
 
 
class FallbackRouter:
    """Computes a :class:`RoutingOutcome` for a given failure scenario.
 
    The router is stateless between calls; all state lives in the injected
    :class:`ProviderRegistry`.
 
    Parameters
    ----------
    registry:
        The registry of known providers and models.
    """
 
    def __init__(
        self,
        registry: ProviderRegistry,
        use_cases: UseCaseRegistry | None = None,
    ) -> None:
        """
        Parameters
        ----------
        registry:
            The provider/model registry.
        use_cases:
            When given, every :meth:`route` call is governance-gated:
            *provenance* becomes a required argument, and
            :func:`~ztax_gateway.governance.authorise` runs against it before
            any routing decision is made.  ``None`` (the default) skips the
            gate entirely, matching this router's original behaviour --
            existing callers that have not yet been given a
            :class:`~ztax_gateway.governance.UseCaseRegistry` are not broken
            by this parameter's addition.  Passing one is strongly
            recommended: every other governed entry point in this package
            (``tool_broker``, ``human_review``, ``invocation_evidence``)
            refuses an unregistered or killed use case before doing anything,
            and a router with no gate will route around a kill switch without
            ever being told one is engaged.
        """
        if not isinstance(registry, ProviderRegistry):
            raise ResilienceError(
                "resilience: FallbackRouter requires a ProviderRegistry"
            )
        self._registry = registry
        self._use_cases = use_cases
 
    def route(
        self,
        *,
        failed_provider_id: str,
        failure: FailureKind,
        permitted_regions: frozenset[str],
        data_class: str = "",
        current_model_id: str = "",
        provenance: Provenance | None = None,
    ) -> RoutingOutcome:
        """Return a :class:`RoutingOutcome` for the given failure.
 
        Parameters
        ----------
        failed_provider_id:
            The provider that has just failed.
        failure:
            The :class:`FailureKind` that triggered the routing decision.
        permitted_regions:
            The regions from the active :class:`~ztax_gateway.provenance.Provenance`.
            A fallback provider must cover *all* of these.
        data_class:
            The data-class label from the active provenance.  A fallback
            provider must be cleared for this class.
        current_model_id:
            The model that was active at the time of failure.  Used to
            select a fallback model on the fallback provider when possible.
        provenance:
            The governed context.  **Required** when this router was
            constructed with a ``use_cases`` registry; ignored otherwise.
            :func:`~ztax_gateway.governance.authorise` runs against it before
            anything else, so a killed or unregistered use case gets no
            routing decision at all.
 
        Raises
        ------
        ResilienceError
            * If *permitted_regions* is empty (mandatory for residency checks).
            * If this router has a ``use_cases`` registry and *provenance*
              was not supplied.
            * If *data_class* is ``"SECRETS"`` -- secret/credential material
              is never placed in model context, so it is refused before any
              provider is considered, fallback or otherwise.
        GovernanceRefusedError
            If this router has a ``use_cases`` registry and *provenance* is
            not authorised (unregistered use case, kill switch engaged,
            region or authority outside the use case's ceiling).
        """
        if not permitted_regions:
            raise ResilienceError(
                "resilience: route() requires at least one permitted_region"
            )
        if data_class == "SECRETS":
            raise ResilienceError(
                "resilience: SECRETS data is never placed in model context; "
                "the request is refused before any provider -- fallback or "
                "otherwise -- is considered"
            )
        if self._use_cases is not None:
            if provenance is None:
                raise ResilienceError(
                    "resilience: this router requires provenance on every "
                    "route() call because it was given a use_cases registry"
                )
            authorise(self._use_cases, provenance)
 
        # ---- §24 failure-specific routing rules -------------------------
 
        # RAG outage: never fallback to another provider for grounded answers.
        # The result is always REVIEW (ABSTAIN here; the human-review gate
        # produces the REVIEW outcome from outside this module).
        if failure is FailureKind.RAG_OUTAGE:
            return RoutingOutcome(
                mode=DegradedMode.ABSTAIN,
                failure=failure,
                failed_provider_id=failed_provider_id,
                fallback_provider=None,
                fallback_model=None,
                rationale=(
                    "RAG/index outage: grounded answers must never be fabricated; "
                    "result is ABSTAIN, caller must surface to human review"
                ),
            )
 
        # Embedding outage: the existing qualified index keeps serving; new
        # indexing queues; an unqualified embedding fallback is never used.
        if failure is FailureKind.EMBEDDING_OUTAGE:
            return RoutingOutcome(
                mode=DegradedMode.DEGRADE,
                failure=failure,
                failed_provider_id=failed_provider_id,
                fallback_provider=None,
                fallback_model=None,
                rationale=(
                    "Embedding-service outage: the existing qualified index "
                    "keeps serving; new indexing is queued; no unqualified "
                    "embedding fallback is used"
                ),
            )
 
        # Tool outage: partial or blocked state, never an invented result.
        if failure is FailureKind.TOOL_OUTAGE:
            return RoutingOutcome(
                mode=DegradedMode.DEGRADE,
                failure=failure,
                failed_provider_id=failed_provider_id,
                fallback_provider=None,
                fallback_model=None,
                rationale=(
                    "Agent-tool outage: result is partial/blocked; "
                    "no invented tool result is permitted"
                ),
            )
 
        # Evaluation outage: current release continues, new promotion blocked.
        if failure is FailureKind.EVALUATION_OUTAGE:
            return RoutingOutcome(
                mode=DegradedMode.DEGRADE,
                failure=failure,
                failed_provider_id=failed_provider_id,
                fallback_provider=None,
                fallback_model=None,
                rationale=(
                    "Evaluation-service outage: current release may continue; "
                    "new release promotion is blocked until the service recovers"
                ),
            )
 
        # Global AI control-plane outage: the regional runtime keeps running on
        # its cached signed approved release/policy; no new use case or model
        # may be activated while central control is unreachable.
        if failure is FailureKind.CONTROL_PLANE_OUTAGE:
            return RoutingOutcome(
                mode=DegradedMode.DEGRADE,
                failure=failure,
                failed_provider_id=failed_provider_id,
                fallback_provider=None,
                fallback_model=None,
                rationale=(
                    "Global AI control-plane outage: regional runtime "
                    "continues on its cached signed approved release/policy; "
                    "no new use case or model activation is permitted"
                ),
            )
 
        # §25 -- Disconnected edge: the device has no live control-plane
        # connection.  Authority ceilings and kill switches come from the local
        # signed EdgePolicySnapshot, not from a live registry.  No new use case
        # or model may be activated; the existing local policy governs.
        # Offline AI cannot gain more authority — prohibited stays prohibited.
        if failure is FailureKind.DISCONNECTED_EDGE:
            return RoutingOutcome(
                mode=DegradedMode.DEGRADE,
                failure=failure,
                failed_provider_id=failed_provider_id,
                fallback_provider=None,
                fallback_model=None,
                rationale=(
                    "Edge device disconnected from AI control plane: operating "
                    "on local signed EdgePolicySnapshot; no new use case or "
                    "model activation is permitted; authority ceilings and kill "
                    "switches from the snapshot remain in full effect — offline "
                    "AI cannot gain more authority because central controls are "
                    "unreachable"
                ),
            )
 
        # Total outage: suspend AI; deterministic tax workflows keep running.
        if failure is FailureKind.TOTAL_OUTAGE:
            return RoutingOutcome(
                mode=DegradedMode.SUSPEND,
                failure=failure,
                failed_provider_id=failed_provider_id,
                fallback_provider=None,
                fallback_model=None,
                rationale=(
                    "Total AI outage: all AI use cases suspended; "
                    "deterministic tax workflows are unaffected"
                ),
            )
 
        # MODEL_OUTAGE and REGIONAL_QUOTA: attempt qualified same-region fallback.
        candidates = self._registry.qualified_providers(
            regions=permitted_regions,
            data_class=data_class,
            exclude_ids=frozenset({failed_provider_id}),
        )
 
        if not candidates:
            mode = _NO_FALLBACK_MODE[failure]
            return RoutingOutcome(
                mode=mode,
                failure=failure,
                failed_provider_id=failed_provider_id,
                fallback_provider=None,
                fallback_model=None,
                rationale=(
                    f"{failure.value}: no qualified same-region alternate found for regions "
                    f"{sorted(permitted_regions)!r}; use case moves to {mode.value}"
                ),
            )
 
        fallback_provider = candidates[0]
        fallback_model: ModelRecord | None = None
 
        # Try to select a non-deprecated model on the fallback provider.
        fallback_models = self._registry.models_for_provider(
            fallback_provider.provider_id,
            data_class=data_class,
            exclude_deprecated=True,
        )
        if fallback_models:
            fallback_model = fallback_models[0]
 
        return RoutingOutcome(
            mode=DegradedMode.FALLBACK,
            failure=failure,
            failed_provider_id=failed_provider_id,
            fallback_provider=fallback_provider,
            fallback_model=fallback_model,
            rationale=(
                f"{failure.value}: routing to qualified alternate provider "
                f"{fallback_provider.provider_id!r} within regions "
                f"{sorted(permitted_regions)!r}"
            ),
        )
 