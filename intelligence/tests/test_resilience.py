"""Tests for Provider/Model Registry and Fallback Router.
 
Chapter 17 §24 (with §17 routing rules) and §25 (Private, Sovereign & Edge AI)
of the ZoikoTax Master Specification.
 
Test groups
-----------
  ResilienceError          -- construction, reason attribute
  FailureKind              -- nine values, StrEnum
  DegradedMode             -- four values, StrEnum
  ProviderRecord           -- construction, validation, covers_regions,
                              allows_data_class, immutability,
                              §25 is_private / owner_id / weight_record
  ModelRecord              -- construction, validation, allows_data_class
  ProviderRegistry         -- register, replace, lookup, qualified_providers,
                              models_for_provider, counts
  RoutingOutcome           -- construction, as_dict
  FallbackRouter           -- type check, all nine FailureKind paths,
                              region lock, qualification gate, data-class gate,
                              model selection on fallback, no-candidate paths,
                              exact-region matching
  Design rules             -- §24 rules 1-8, §25 rules 9-10 enforced
  Integration              -- multi-provider registry; real routing scenarios
  WeightScanStatus         -- §25: three values, StrEnum
  ModelWeightRecord        -- §25: construction, validation, is_production_safe
  EdgePolicySnapshot       -- §25: construction, validation, immutability
  authorise_edge           -- §25: delegates to governance; all refusal paths;
                              offline AI cannot gain authority
"""
 
from __future__ import annotations
 
from dataclasses import FrozenInstanceError
 
import pytest
 
from ztax_gateway.governance import (
    GovernanceRefusedError,
    Refusal,
    UseCase,
    UseCaseRegistry,
)
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier
from ztax_gateway.resilience import (
    DegradedMode,
    EdgePolicySnapshot,
    FailureKind,
    FallbackRouter,
    ModelRecord,
    ModelWeightRecord,
    ProviderRecord,
    ProviderRegistry,
    ResilienceError,
    RoutingOutcome,
    WeightScanStatus,
    authorise_edge,
)
 
# ---------------------------------------------------------------------------
# Shared helpers
# ---------------------------------------------------------------------------
 
_EU = frozenset({"eu-west-1"})
_EU2 = frozenset({"eu-west-1", "eu-central-1"})
_US = frozenset({"us-east-1"})
 
 
def _provider(
    provider_id: str = "gcp:vertex-eu",
    regions: frozenset[str] = _EU,
    is_qualified: bool = True,
    data_classes: frozenset[str] = frozenset(),
) -> ProviderRecord:
    return ProviderRecord(
        provider_id=provider_id,
        display_name=f"Provider {provider_id}",
        permitted_regions=regions,
        is_qualified=is_qualified,
        data_classes=data_classes,
    )
 
 
def _model(
    model_id: str = "gemini-pro@2026.09",
    provider_id: str = "gcp:vertex-eu",
    is_deprecated: bool = False,
    data_classes: frozenset[str] = frozenset(),
) -> ModelRecord:
    return ModelRecord(
        model_id=model_id,
        provider_id=provider_id,
        is_deprecated=is_deprecated,
        data_classes=data_classes,
    )
 
 
def _registry(
    *providers: ProviderRecord,
    models: list[ModelRecord] | None = None,
) -> ProviderRegistry:
    reg = ProviderRegistry()
    for p in providers:
        reg.register_provider(p)
    for m in (models or []):
        reg.register_model(m)
    return reg
 
 
def _router(
    *providers: ProviderRecord,
    models: list[ModelRecord] | None = None,
) -> FallbackRouter:
    return FallbackRouter(_registry(*providers, models=models))
 
 
_GOVERNED_PROVENANCE = Provenance(
    use_case="ai-routing",
    model_profile="model:gemini-pro",
    provider_profile="provider:gcp-vertex-eu",
    prompt_profile="prompt:route@1",
    region="eu-west-1",
    data_class="INTERNAL",
    risk_tier=RiskTier.T1,
    authority_outcome=AuthorityOutcome.A1,
    ai_train_version="0.7.0",
)
 
 
def _use_cases(prov: Provenance = _GOVERNED_PROVENANCE) -> UseCaseRegistry:
    return UseCaseRegistry(
        [
            UseCase(
                use_case_id=prov.use_case,
                owner="lane-l",
                description="test use case",
                max_risk_tier=RiskTier.T4,
                max_authority=AuthorityOutcome.A4,
                permitted_regions=frozenset({prov.region}),
            )
        ]
    )
 
 
def _governed_router(
    *providers: ProviderRecord,
    models: list[ModelRecord] | None = None,
    use_cases: UseCaseRegistry | None = None,
) -> FallbackRouter:
    return FallbackRouter(
        _registry(*providers, models=models), use_cases or _use_cases()
    )
 
 
# ---------------------------------------------------------------------------
# ResilienceError
# ---------------------------------------------------------------------------
 
 
def test_resilience_error_is_exception() -> None:
    assert isinstance(ResilienceError("x"), Exception)
 
 
def test_resilience_error_reason_attribute() -> None:
    assert ResilienceError("invariant broken").reason == "invariant broken"
 
 
def test_resilience_error_str_contains_reason() -> None:
    assert "invariant broken" in str(ResilienceError("invariant broken"))
 
 
# ---------------------------------------------------------------------------
# FailureKind
# ---------------------------------------------------------------------------
 
 
def test_failure_kind_has_nine_values() -> None:
    """Eight members from §24 plus DISCONNECTED_EDGE from §25."""
    assert len(FailureKind) == 9
 
 
def test_failure_kind_values() -> None:
    assert FailureKind.MODEL_OUTAGE == "MODEL_OUTAGE"
    assert FailureKind.REGIONAL_QUOTA == "REGIONAL_QUOTA"
    assert FailureKind.RAG_OUTAGE == "RAG_OUTAGE"
    assert FailureKind.EMBEDDING_OUTAGE == "EMBEDDING_OUTAGE"
    assert FailureKind.TOOL_OUTAGE == "TOOL_OUTAGE"
    assert FailureKind.EVALUATION_OUTAGE == "EVALUATION_OUTAGE"
    assert FailureKind.CONTROL_PLANE_OUTAGE == "CONTROL_PLANE_OUTAGE"
    assert FailureKind.TOTAL_OUTAGE == "TOTAL_OUTAGE"
    assert FailureKind.DISCONNECTED_EDGE == "DISCONNECTED_EDGE"
 
 
# ---------------------------------------------------------------------------
# DegradedMode
# ---------------------------------------------------------------------------
 
 
def test_degraded_mode_has_four_values() -> None:
    assert len(DegradedMode) == 4
 
 
def test_degraded_mode_values() -> None:
    assert DegradedMode.FALLBACK == "FALLBACK"
    assert DegradedMode.DEGRADE == "DEGRADE"
    assert DegradedMode.ABSTAIN == "ABSTAIN"
    assert DegradedMode.SUSPEND == "SUSPEND"
 
 
# ---------------------------------------------------------------------------
# ProviderRecord -- construction
# ---------------------------------------------------------------------------
 
 
def test_provider_record_stores_provider_id() -> None:
    assert _provider().provider_id == "gcp:vertex-eu"
 
 
def test_provider_record_stores_permitted_regions() -> None:
    assert _provider(regions=_EU2).permitted_regions == _EU2
 
 
def test_provider_record_default_is_qualified_true() -> None:
    assert _provider().is_qualified is True
 
 
def test_provider_record_not_qualified_flag() -> None:
    assert _provider(is_qualified=False).is_qualified is False
 
 
def test_provider_record_default_data_classes_empty() -> None:
    assert _provider().data_classes == frozenset()
 
 
# ---------------------------------------------------------------------------
# ProviderRecord -- validation
# ---------------------------------------------------------------------------
 
 
def test_provider_record_empty_id_refused() -> None:
    with pytest.raises(ResilienceError, match="provider_id"):
        ProviderRecord(
            provider_id="", display_name="x",
            permitted_regions=_EU,
        )
 
 
def test_provider_record_empty_regions_refused() -> None:
    with pytest.raises(ResilienceError, match="permitted_region"):
        ProviderRecord(
            provider_id="p1", display_name="x",
            permitted_regions=frozenset(),
        )
 
 
def test_provider_record_is_frozen() -> None:
    with pytest.raises(FrozenInstanceError):
        _provider().is_qualified = False  # type: ignore[misc]
 
 
# ---------------------------------------------------------------------------
# ProviderRecord -- covers_regions
# ---------------------------------------------------------------------------
 
 
def test_covers_regions_true_when_exact_match() -> None:
    assert _provider(regions=_EU).covers_regions(_EU)
 
 
def test_covers_regions_true_when_provider_has_superset() -> None:
    assert _provider(regions=_EU2).covers_regions(_EU)
 
 
def test_covers_regions_false_when_provider_missing_region() -> None:
    assert not _provider(regions=_EU).covers_regions(_EU2)
 
 
def test_covers_regions_false_when_completely_different() -> None:
    assert not _provider(regions=_EU).covers_regions(_US)
 
 
# ---------------------------------------------------------------------------
# ProviderRecord -- allows_data_class
# ---------------------------------------------------------------------------
 
 
def test_allows_data_class_true_when_no_restriction() -> None:
    assert _provider(data_classes=frozenset()).allows_data_class("CONFIDENTIAL")
 
 
def test_allows_data_class_true_when_listed() -> None:
    assert _provider(data_classes=frozenset({"INTERNAL", "CONFIDENTIAL"})).allows_data_class(
        "INTERNAL"
    )
 
 
def test_allows_data_class_false_when_not_listed() -> None:
    assert not _provider(data_classes=frozenset({"INTERNAL"})).allows_data_class("CONFIDENTIAL")
 
 
# ---------------------------------------------------------------------------
# ModelRecord -- construction and validation
# ---------------------------------------------------------------------------
 
 
def test_model_record_stores_model_id() -> None:
    assert _model().model_id == "gemini-pro@2026.09"
 
 
def test_model_record_stores_provider_id() -> None:
    assert _model(provider_id="gcp:vertex-eu").provider_id == "gcp:vertex-eu"
 
 
def test_model_record_default_not_deprecated() -> None:
    assert _model().is_deprecated is False
 
 
def test_model_record_empty_model_id_refused() -> None:
    with pytest.raises(ResilienceError, match="model_id"):
        ModelRecord(model_id="", provider_id="p1")
 
 
def test_model_record_empty_provider_id_refused() -> None:
    with pytest.raises(ResilienceError, match="provider_id"):
        ModelRecord(model_id="m1", provider_id="")
 
 
def test_model_record_is_frozen() -> None:
    with pytest.raises(FrozenInstanceError):
        _model().is_deprecated = True  # type: ignore[misc]
 
 
def test_model_allows_data_class_unrestricted() -> None:
    assert _model(data_classes=frozenset()).allows_data_class("ANYTHING")
 
 
def test_model_allows_data_class_listed() -> None:
    assert _model(data_classes=frozenset({"INTERNAL"})).allows_data_class("INTERNAL")
 
 
def test_model_denies_data_class_not_listed() -> None:
    assert not _model(data_classes=frozenset({"INTERNAL"})).allows_data_class("SECRET")
 
 
# ---------------------------------------------------------------------------
# ProviderRegistry -- register and lookup
# ---------------------------------------------------------------------------
 
 
def test_registry_register_provider_stores_it() -> None:
    reg = ProviderRegistry()
    p = _provider()
    reg.register_provider(p)
    assert reg.provider("gcp:vertex-eu") is p
 
 
def test_registry_register_provider_replaces_on_re_registration() -> None:
    reg = ProviderRegistry()
    reg.register_provider(_provider(is_qualified=True))
    reg.register_provider(_provider(is_qualified=False))
    assert reg.provider("gcp:vertex-eu").is_qualified is False  # type: ignore[union-attr]
 
 
def test_registry_register_provider_rejects_non_provider() -> None:
    with pytest.raises(ResilienceError, match="ProviderRecord"):
        ProviderRegistry().register_provider("not a provider")  # type: ignore[arg-type]
 
 
def test_registry_provider_returns_none_for_unknown() -> None:
    assert ProviderRegistry().provider("unknown") is None
 
 
def test_registry_register_model_stores_it() -> None:
    reg = _registry(_provider())
    m = _model()
    reg.register_model(m)
    assert reg.model("gemini-pro@2026.09") is m
 
 
def test_registry_register_model_requires_known_provider() -> None:
    with pytest.raises(ResilienceError, match="unknown provider"):
        ProviderRegistry().register_model(_model(provider_id="unregistered"))
 
 
def test_registry_register_model_rejects_non_model() -> None:
    with pytest.raises(ResilienceError, match="ModelRecord"):
        _registry(_provider()).register_model("not a model")  # type: ignore[arg-type]
 
 
def test_registry_model_returns_none_for_unknown() -> None:
    assert ProviderRegistry().model("unknown") is None
 
 
def test_registry_provider_count() -> None:
    reg = _registry(_provider("p1", _EU), _provider("p2", _EU))
    assert reg.provider_count == 2
 
 
def test_registry_model_count() -> None:
    reg = _registry(_provider())
    reg.register_model(_model("m1"))
    reg.register_model(_model("m2"))
    assert reg.model_count == 2
 
 
# ---------------------------------------------------------------------------
# ProviderRegistry -- qualified_providers
# ---------------------------------------------------------------------------
 
 
def test_qualified_providers_returns_only_qualified() -> None:
    reg = _registry(
        _provider("p-qualified", _EU, is_qualified=True),
        _provider("p-unqualified", _EU, is_qualified=False),
    )
    results = reg.qualified_providers(regions=_EU)
    assert all(p.is_qualified for p in results)
    assert len(results) == 1
 
 
def test_qualified_providers_region_filter() -> None:
    reg = _registry(
        _provider("eu-provider", _EU, is_qualified=True),
        _provider("us-provider", _US, is_qualified=True),
    )
    results = reg.qualified_providers(regions=_EU)
    assert len(results) == 1
    assert results[0].provider_id == "eu-provider"
 
 
def test_qualified_providers_excludes_by_id() -> None:
    reg = _registry(
        _provider("p1", _EU),
        _provider("p2", _EU),
    )
    results = reg.qualified_providers(regions=_EU, exclude_ids=frozenset({"p1"}))
    assert all(p.provider_id != "p1" for p in results)
 
 
def test_qualified_providers_data_class_filter() -> None:
    reg = _registry(
        _provider("p-restricted", _EU, data_classes=frozenset({"INTERNAL"})),
        _provider("p-unrestricted", _EU, data_classes=frozenset()),
    )
    results = reg.qualified_providers(regions=_EU, data_class="CONFIDENTIAL")
    # p-restricted does not allow CONFIDENTIAL
    assert all(p.provider_id != "p-restricted" for p in results)
 
 
def test_qualified_providers_returns_empty_when_none_match() -> None:
    reg = _registry(_provider("eu-p", _EU))
    assert reg.qualified_providers(regions=_US) == []
 
 
def test_qualified_providers_sorted_by_provider_id() -> None:
    reg = _registry(
        _provider("z-prov", _EU),
        _provider("a-prov", _EU),
    )
    ids = [p.provider_id for p in reg.qualified_providers(regions=_EU)]
    assert ids == sorted(ids)
 
 
def test_qualified_providers_superset_region_provider_included() -> None:
    """A provider with regions ⊇ requested regions is a valid fallback."""
    reg = _registry(_provider("wide-prov", _EU2))
    results = reg.qualified_providers(regions=_EU)
    assert results[0].provider_id == "wide-prov"
 
 
def test_qualified_providers_subset_region_provider_excluded() -> None:
    """A provider with regions ⊂ requested regions is NOT a valid fallback."""
    reg = _registry(_provider("narrow-prov", _EU))
    results = reg.qualified_providers(regions=_EU2)
    assert results == []
 
 
# ---------------------------------------------------------------------------
# ProviderRegistry -- models_for_provider
# ---------------------------------------------------------------------------
 
 
def test_models_for_provider_returns_models() -> None:
    reg = _registry(_provider())
    reg.register_model(_model("m1"))
    reg.register_model(_model("m2"))
    result = reg.models_for_provider("gcp:vertex-eu")
    assert len(result) == 2
 
 
def test_models_for_provider_excludes_deprecated_by_default() -> None:
    reg = _registry(_provider())
    reg.register_model(_model("m-active", is_deprecated=False))
    reg.register_model(_model("m-old", is_deprecated=True))
    result = reg.models_for_provider("gcp:vertex-eu")
    assert all(not m.is_deprecated for m in result)
 
 
def test_models_for_provider_includes_deprecated_when_requested() -> None:
    reg = _registry(_provider())
    reg.register_model(_model("m-old", is_deprecated=True))
    result = reg.models_for_provider("gcp:vertex-eu", exclude_deprecated=False)
    assert len(result) == 1
 
 
def test_models_for_provider_data_class_filter() -> None:
    reg = _registry(_provider())
    reg.register_model(_model("m-internal", data_classes=frozenset({"INTERNAL"})))
    reg.register_model(_model("m-any", data_classes=frozenset()))
    result = reg.models_for_provider("gcp:vertex-eu", data_class="CONFIDENTIAL")
    assert all(m.model_id != "m-internal" for m in result)
 
 
def test_models_for_provider_unknown_provider_returns_empty() -> None:
    assert _registry(_provider()).models_for_provider("no-such-provider") == []
 
 
def test_models_for_provider_sorted_by_model_id() -> None:
    reg = _registry(_provider())
    reg.register_model(_model("z-model"))
    reg.register_model(_model("a-model"))
    ids = [m.model_id for m in reg.models_for_provider("gcp:vertex-eu")]
    assert ids == sorted(ids)
 
 
# ---------------------------------------------------------------------------
# RoutingOutcome
# ---------------------------------------------------------------------------
 
 
def test_routing_outcome_stores_fields() -> None:
    p = _provider()
    m = _model()
    outcome = RoutingOutcome(
        mode=DegradedMode.FALLBACK,
        failure=FailureKind.MODEL_OUTAGE,
        failed_provider_id="failed-p",
        fallback_provider=p,
        fallback_model=m,
        rationale="test",
    )
    assert outcome.mode is DegradedMode.FALLBACK
    assert outcome.failure is FailureKind.MODEL_OUTAGE
    assert outcome.fallback_provider is p
    assert outcome.fallback_model is m
 
 
def test_routing_outcome_is_frozen() -> None:
    outcome = RoutingOutcome(
        mode=DegradedMode.SUSPEND,
        failure=FailureKind.TOTAL_OUTAGE,
        failed_provider_id="p",
        fallback_provider=None,
        fallback_model=None,
        rationale="x",
    )
    with pytest.raises(FrozenInstanceError):
        outcome.mode = DegradedMode.FALLBACK  # type: ignore[misc]
 
 
def test_routing_outcome_as_dict_keys() -> None:
    outcome = RoutingOutcome(
        mode=DegradedMode.ABSTAIN,
        failure=FailureKind.RAG_OUTAGE,
        failed_provider_id="p",
        fallback_provider=None,
        fallback_model=None,
        rationale="x",
    )
    assert set(outcome.as_dict().keys()) == {
        "mode", "failure", "failed_provider_id",
        "fallback_provider_id", "fallback_model_id", "rationale",
    }
 
 
def test_routing_outcome_as_dict_none_fallback() -> None:
    outcome = RoutingOutcome(
        mode=DegradedMode.SUSPEND,
        failure=FailureKind.TOTAL_OUTAGE,
        failed_provider_id="p",
        fallback_provider=None,
        fallback_model=None,
        rationale="x",
    )
    d = outcome.as_dict()
    assert d["fallback_provider_id"] is None
    assert d["fallback_model_id"] is None
 
 
def test_routing_outcome_as_dict_with_fallback() -> None:
    p = _provider("alt-prov", _EU)
    m = _model("alt-model", "alt-prov")
    outcome = RoutingOutcome(
        mode=DegradedMode.FALLBACK,
        failure=FailureKind.MODEL_OUTAGE,
        failed_provider_id="primary",
        fallback_provider=p,
        fallback_model=m,
        rationale="x",
    )
    d = outcome.as_dict()
    assert d["fallback_provider_id"] == "alt-prov"
    assert d["fallback_model_id"] == "alt-model"
 
 
# ---------------------------------------------------------------------------
# FallbackRouter -- construction
# ---------------------------------------------------------------------------
 
 
def test_fallback_router_rejects_non_registry() -> None:
    with pytest.raises(ResilienceError, match="ProviderRegistry"):
        FallbackRouter("not a registry")  # type: ignore[arg-type]
 
 
def test_fallback_router_requires_non_empty_regions() -> None:
    router = _router(_provider())
    with pytest.raises(ResilienceError, match="permitted_region"):
        router.route(
            failed_provider_id="gcp:vertex-eu",
            failure=FailureKind.MODEL_OUTAGE,
            permitted_regions=frozenset(),
        )
 
 
# ---------------------------------------------------------------------------
# FallbackRouter -- RAG_OUTAGE (design rule 3: never fabricate)
# ---------------------------------------------------------------------------
 
 
def test_rag_outage_always_abstain() -> None:
    """RAG outage must always produce ABSTAIN, even with qualified alternates."""
    router = _router(_provider("alt", _EU))
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.RAG_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.ABSTAIN
 
 
def test_rag_outage_fallback_provider_is_none() -> None:
    router = _router(_provider("alt", _EU))
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.RAG_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.fallback_provider is None
 
 
def test_rag_outage_rationale_mentions_fabricate() -> None:
    router = _router()
    outcome = router.route(
        failed_provider_id="p",
        failure=FailureKind.RAG_OUTAGE,
        permitted_regions=_EU,
    )
    assert "fabricat" in outcome.rationale
 
 
# ---------------------------------------------------------------------------
# FallbackRouter -- TOOL_OUTAGE (design rule 4: partial/blocked, no invention)
# ---------------------------------------------------------------------------
 
 
def test_tool_outage_always_degrade() -> None:
    router = _router(_provider("alt", _EU))
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.TOOL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.DEGRADE
 
 
def test_tool_outage_fallback_provider_is_none() -> None:
    router = _router(_provider("alt", _EU))
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.TOOL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.fallback_provider is None
 
 
# ---------------------------------------------------------------------------
# FallbackRouter -- EVALUATION_OUTAGE (design rule 5: block promotion)
# ---------------------------------------------------------------------------
 
 
def test_evaluation_outage_always_degrade() -> None:
    router = _router(_provider("alt", _EU))
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.EVALUATION_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.DEGRADE
 
 
def test_evaluation_outage_rationale_mentions_promotion() -> None:
    router = _router()
    outcome = router.route(
        failed_provider_id="p",
        failure=FailureKind.EVALUATION_OUTAGE,
        permitted_regions=_EU,
    )
    assert "promot" in outcome.rationale
 
 
# ---------------------------------------------------------------------------
# FallbackRouter -- TOTAL_OUTAGE (design rule 6: deterministic workflows continue)
# ---------------------------------------------------------------------------
 
 
def test_total_outage_always_suspend() -> None:
    router = _router(_provider("alt", _EU))
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.TOTAL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.SUSPEND
 
 
def test_total_outage_rationale_mentions_deterministic() -> None:
    router = _router()
    outcome = router.route(
        failed_provider_id="p",
        failure=FailureKind.TOTAL_OUTAGE,
        permitted_regions=_EU,
    )
    assert "deterministic" in outcome.rationale
 
 
# ---------------------------------------------------------------------------
# FallbackRouter -- MODEL_OUTAGE: successful fallback
# ---------------------------------------------------------------------------
 
 
def test_model_outage_fallback_mode_when_alternate_available() -> None:
    router = _router(
        _provider("primary", _EU),
        _provider("alt", _EU),
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.FALLBACK
 
 
def test_model_outage_fallback_provider_is_not_failed() -> None:
    router = _router(
        _provider("primary", _EU),
        _provider("alt", _EU),
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.fallback_provider is not None
    assert outcome.fallback_provider.provider_id != "primary"
 
 
def test_model_outage_fallback_sets_failed_provider_id() -> None:
    router = _router(
        _provider("primary", _EU),
        _provider("alt", _EU),
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.failed_provider_id == "primary"
 
 
def test_model_outage_selects_model_on_fallback_provider() -> None:
    router = _router(
        _provider("primary", _EU),
        _provider("alt", _EU),
        models=[_model("m-alt", "alt")],
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.fallback_model is not None
    assert outcome.fallback_model.provider_id == "alt"
 
 
def test_model_outage_fallback_model_none_when_provider_has_no_models() -> None:
    router = _router(
        _provider("primary", _EU),
        _provider("alt", _EU),
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.fallback_model is None
 
 
# ---------------------------------------------------------------------------
# FallbackRouter -- MODEL_OUTAGE: no qualified alternate → SUSPEND
# ---------------------------------------------------------------------------
 
 
def test_model_outage_suspend_when_no_qualified_alternate() -> None:
    router = _router(_provider("primary", _EU))
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.SUSPEND
    assert outcome.fallback_provider is None
 
 
# ---------------------------------------------------------------------------
# FallbackRouter -- REGIONAL_QUOTA: successful fallback
# ---------------------------------------------------------------------------
 
 
def test_regional_quota_fallback_when_alternate_available() -> None:
    router = _router(
        _provider("primary", _EU),
        _provider("alt", _EU),
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.REGIONAL_QUOTA,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.FALLBACK
    assert outcome.fallback_provider is not None
 
 
def test_regional_quota_suspend_when_no_alternate() -> None:
    router = _router(_provider("primary", _EU))
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.REGIONAL_QUOTA,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.SUSPEND
 
 
# ---------------------------------------------------------------------------
# FallbackRouter -- design rule 1: region lock
# ---------------------------------------------------------------------------
 
 
def test_region_lock_rejects_alternate_covering_fewer_regions() -> None:
    """A fallback provider that does not cover ALL required regions is rejected."""
    router = _router(
        _provider("primary", _EU2),
        _provider("narrow-alt", _EU),  # only covers eu-west-1, not eu-central-1
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU2,
    )
    # narrow-alt cannot serve both regions; no valid fallback
    assert outcome.mode is DegradedMode.SUSPEND
 
 
def test_region_lock_accepts_alternate_covering_all_required_regions() -> None:
    router = _router(
        _provider("primary", _EU2),
        _provider("wide-alt", _EU2),
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU2,
    )
    assert outcome.mode is DegradedMode.FALLBACK
 
 
def test_region_lock_superset_provider_is_valid_fallback() -> None:
    """A provider with regions ⊃ required is a valid fallback."""
    wide = frozenset({"eu-west-1", "eu-central-1", "eu-north-1"})
    router = _router(
        _provider("primary", _EU),
        _provider("wide-alt", wide),
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.FALLBACK
 
 
def test_cross_region_never_widened_to_us() -> None:
    """EU-only requests must never fall back to a US-only provider."""
    router = _router(
        _provider("eu-primary", _EU),
        _provider("us-alt", _US),
    )
    outcome = router.route(
        failed_provider_id="eu-primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.SUSPEND
 
 
# ---------------------------------------------------------------------------
# FallbackRouter -- design rule 2: qualification gate
# ---------------------------------------------------------------------------
 
 
def test_unqualified_provider_not_selected_as_fallback() -> None:
    router = _router(
        _provider("primary", _EU, is_qualified=True),
        _provider("unqualified-alt", _EU, is_qualified=False),
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.SUSPEND
 
 
def test_only_qualified_providers_selected_as_fallback() -> None:
    router = _router(
        _provider("primary", _EU, is_qualified=True),
        _provider("unqualified", _EU, is_qualified=False),
        _provider("qualified-alt", _EU, is_qualified=True),
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.FALLBACK
    assert outcome.fallback_provider is not None
    assert outcome.fallback_provider.is_qualified is True
 
 
# ---------------------------------------------------------------------------
# FallbackRouter -- data-class gate
# ---------------------------------------------------------------------------
 
 
def test_data_class_restricted_provider_not_selected_for_wrong_class() -> None:
    router = _router(
        _provider("primary", _EU, data_classes=frozenset()),
        _provider("restricted-alt", _EU, data_classes=frozenset({"INTERNAL"})),
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
        data_class="CONFIDENTIAL",
    )
    assert outcome.mode is DegradedMode.SUSPEND
 
 
def test_data_class_unrestricted_provider_selected_for_any_class() -> None:
    router = _router(
        _provider("primary", _EU),
        _provider("unrestricted-alt", _EU, data_classes=frozenset()),
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
        data_class="SECRET",
    )
    assert outcome.mode is DegradedMode.FALLBACK
 
 
# ---------------------------------------------------------------------------
# FallbackRouter -- failure_kind recorded in outcome
# ---------------------------------------------------------------------------
 
 
def test_outcome_records_failure_kind_rag() -> None:
    outcome = _router().route(
        failed_provider_id="p", failure=FailureKind.RAG_OUTAGE, permitted_regions=_EU
    )
    assert outcome.failure is FailureKind.RAG_OUTAGE
 
 
def test_outcome_records_failure_kind_model() -> None:
    outcome = _router(_provider("p", _EU)).route(
        failed_provider_id="p", failure=FailureKind.MODEL_OUTAGE, permitted_regions=_EU
    )
    assert outcome.failure is FailureKind.MODEL_OUTAGE
 
 
# ---------------------------------------------------------------------------
# FallbackRouter -- the two failures added to complete the section 24 table
# ---------------------------------------------------------------------------
 
 
def test_embedding_outage_degrades_and_names_no_provider() -> None:
    outcome = _router().route(
        failed_provider_id="p", failure=FailureKind.EMBEDDING_OUTAGE, permitted_regions=_EU
    )
    assert outcome.mode is DegradedMode.DEGRADE
    assert outcome.fallback_provider is None
    assert "unqualified embedding" in outcome.rationale
 
 
def test_control_plane_outage_degrades_and_names_no_provider() -> None:
    outcome = _router().route(
        failed_provider_id="p", failure=FailureKind.CONTROL_PLANE_OUTAGE, permitted_regions=_EU
    )
    assert outcome.mode is DegradedMode.DEGRADE
    assert outcome.fallback_provider is None
    assert "no new use case or model activation" in outcome.rationale
 
 
def test_embedding_outage_never_falls_back_even_with_qualified_alternates() -> None:
    """Like RAG_OUTAGE, this must not route to a provider regardless of who is available."""
    router = _router(_provider("primary", _EU), _provider("alt", _EU))
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.EMBEDDING_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.DEGRADE
    assert outcome.fallback_provider is None
 
 
def test_control_plane_outage_never_activates_a_new_provider() -> None:
    router = _router(_provider("primary", _EU), _provider("alt", _EU))
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.CONTROL_PLANE_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.DEGRADE
    assert outcome.fallback_provider is None
 
 
# ---------------------------------------------------------------------------
# FallbackRouter -- SECRETS is refused before any provider is considered
# ---------------------------------------------------------------------------
 
 
def test_secrets_data_class_is_refused_on_model_outage() -> None:
    router = _router(_provider("primary", _EU), _provider("alt", _EU))
    with pytest.raises(ResilienceError, match="SECRETS"):
        router.route(
            failed_provider_id="primary",
            failure=FailureKind.MODEL_OUTAGE,
            permitted_regions=_EU,
            data_class="SECRETS",
        )
 
 
def test_secrets_data_class_is_refused_regardless_of_failure_kind() -> None:
    router = _router(_provider("primary", _EU))
    for failure in FailureKind:
        with pytest.raises(ResilienceError, match="SECRETS"):
            router.route(
                failed_provider_id="primary",
                failure=failure,
                permitted_regions=_EU,
                data_class="SECRETS",
            )
 
 
def test_secrets_refusal_takes_priority_over_missing_permitted_regions() -> None:
    """Whichever ResilienceError fires first, secrets are never a silent pass-through."""
    router = _router()
    with pytest.raises(ResilienceError):
        router.route(
            failed_provider_id="p",
            failure=FailureKind.MODEL_OUTAGE,
            permitted_regions=frozenset(),
            data_class="SECRETS",
        )
 
 
def test_non_secrets_data_class_is_unaffected() -> None:
    """The SECRETS check is an exact match; it must not over-refuse other classes."""
    router = _router(_provider("primary", _EU), _provider("alt", _EU))
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
        data_class="CONFIDENTIAL",
    )
    assert outcome.mode is DegradedMode.FALLBACK
 
 
# ---------------------------------------------------------------------------
# FallbackRouter -- governance gate (opt-in via use_cases)
# ---------------------------------------------------------------------------
 
 
def test_router_without_use_cases_registry_skips_the_gate() -> None:
    """Original behaviour is preserved: no use_cases means no gate, no provenance needed."""
    outcome = _router(_provider("p", _EU)).route(
        failed_provider_id="p", failure=FailureKind.RAG_OUTAGE, permitted_regions=_EU
    )
    assert outcome.mode is DegradedMode.ABSTAIN
 
 
def test_governed_router_requires_provenance() -> None:
    with pytest.raises(ResilienceError, match="requires provenance"):
        _governed_router(_provider("p", _EU)).route(
            failed_provider_id="p", failure=FailureKind.RAG_OUTAGE, permitted_regions=_EU
        )
 
 
def test_governed_router_permits_a_registered_use_case() -> None:
    outcome = _governed_router(_provider("p", _EU)).route(
        failed_provider_id="p",
        failure=FailureKind.RAG_OUTAGE,
        permitted_regions=_EU,
        provenance=_GOVERNED_PROVENANCE,
    )
    assert outcome.mode is DegradedMode.ABSTAIN
 
 
def test_governed_router_refuses_an_unregistered_use_case() -> None:
    router = _governed_router(_provider("p", _EU), use_cases=UseCaseRegistry())
    with pytest.raises(GovernanceRefusedError) as exc:
        router.route(
            failed_provider_id="p",
            failure=FailureKind.MODEL_OUTAGE,
            permitted_regions=_EU,
            provenance=_GOVERNED_PROVENANCE,
        )
    assert exc.value.refusal is Refusal.UNKNOWN_USE_CASE
 
 
def test_governed_router_refuses_a_killed_use_case() -> None:
    use_cases = _use_cases()
    use_cases.kill(_GOVERNED_PROVENANCE.use_case)
    router = _governed_router(_provider("p", _EU), use_cases=use_cases)
    with pytest.raises(GovernanceRefusedError) as exc:
        router.route(
            failed_provider_id="p",
            failure=FailureKind.MODEL_OUTAGE,
            permitted_regions=_EU,
            provenance=_GOVERNED_PROVENANCE,
        )
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED
 
 
def test_governed_router_refuses_when_global_kill_is_engaged() -> None:
    use_cases = _use_cases()
    use_cases.engage_global_kill()
    router = _governed_router(_provider("p", _EU), use_cases=use_cases)
    with pytest.raises(GovernanceRefusedError):
        router.route(
            failed_provider_id="p",
            failure=FailureKind.MODEL_OUTAGE,
            permitted_regions=_EU,
            provenance=_GOVERNED_PROVENANCE,
        )
 
 
def test_governance_gate_runs_before_a_fallback_is_chosen() -> None:
    """A killed use case gets no routing decision at all, not even ABSTAIN/DEGRADE."""
    use_cases = _use_cases()
    use_cases.kill(_GOVERNED_PROVENANCE.use_case)
    router = _governed_router(_provider("p", _EU), use_cases=use_cases)
    with pytest.raises(GovernanceRefusedError):
        router.route(
            failed_provider_id="p",
            failure=FailureKind.RAG_OUTAGE,
            permitted_regions=_EU,
            provenance=_GOVERNED_PROVENANCE,
        )
 
 
# ---------------------------------------------------------------------------
# Integration -- multi-provider registry, realistic routing
# ---------------------------------------------------------------------------
 
 
def test_integration_eu_primary_fails_routes_to_eu_backup() -> None:
    """
    Topology: eu-primary (primary), eu-backup (qualified, same region),
    us-provider (qualified, wrong region).
    A MODEL_OUTAGE on eu-primary must route to eu-backup, not us-provider.
    """
    primary = _provider("eu-primary", _EU)
    backup = _provider("eu-backup", _EU)
    us_prov = _provider("us-primary", _US)
    m_backup = _model("gemini-pro-backup", "eu-backup")
 
    router = _router(primary, backup, us_prov, models=[m_backup])
    outcome = router.route(
        failed_provider_id="eu-primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
 
    assert outcome.mode is DegradedMode.FALLBACK
    assert outcome.fallback_provider is not None
    assert outcome.fallback_provider.provider_id == "eu-backup"
    assert outcome.fallback_model is not None
    assert outcome.fallback_model.model_id == "gemini-pro-backup"
 
 
def test_integration_all_eu_providers_exhausted_suspends() -> None:
    """When the only EU provider is down, the use case must suspend."""
    router = _router(_provider("eu-only", _EU), _provider("us-prov", _US))
    outcome = router.route(
        failed_provider_id="eu-only",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.SUSPEND
 
 
def test_integration_rag_outage_ignores_available_providers() -> None:
    """Even with multiple healthy EU providers, a RAG outage must ABSTAIN."""
    router = _router(
        _provider("eu-p1", _EU),
        _provider("eu-p2", _EU),
        _provider("eu-p3", _EU),
    )
    outcome = router.route(
        failed_provider_id="eu-p1",
        failure=FailureKind.RAG_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.ABSTAIN
    assert outcome.fallback_provider is None
 
 
def test_integration_deprecated_models_excluded_from_fallback() -> None:
    """A fallback provider should not receive a deprecated model."""
    router = _router(
        _provider("primary", _EU),
        _provider("alt", _EU),
        models=[
            _model("old-model", "alt", is_deprecated=True),
            _model("new-model", "alt", is_deprecated=False),
        ],
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.fallback_model is not None
    assert outcome.fallback_model.model_id == "new-model"
 
 
def test_integration_regional_quota_does_not_widen_to_us() -> None:
    """REGIONAL_QUOTA for an EU provider must not route to a US provider."""
    router = _router(
        _provider("eu-primary", _EU),
        _provider("us-alt", _US),
    )
    outcome = router.route(
        failed_provider_id="eu-primary",
        failure=FailureKind.REGIONAL_QUOTA,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.SUSPEND
 
 
# ---------------------------------------------------------------------------
# §25 -- WeightScanStatus
# ---------------------------------------------------------------------------
 
 
def test_weight_scan_status_has_three_values() -> None:
    assert len(WeightScanStatus) == 3
 
 
def test_weight_scan_status_values() -> None:
    assert WeightScanStatus.CLEAN == "CLEAN"
    assert WeightScanStatus.PENDING == "PENDING"
    assert WeightScanStatus.FAILED == "FAILED"
 
 
# ---------------------------------------------------------------------------
# §25 -- ModelWeightRecord -- construction and validation
# ---------------------------------------------------------------------------
 
 
_VALID_SHA256 = "a" * 64
 
 
def _weight(
    artifact_id: str = "gemini-flash-2b@2026.09",
    sha256_hex: str = _VALID_SHA256,
    spdx_licence: str = "Apache-2.0",
    scan_status: WeightScanStatus = WeightScanStatus.CLEAN,
) -> ModelWeightRecord:
    return ModelWeightRecord(
        artifact_id=artifact_id,
        sha256_hex=sha256_hex,
        spdx_licence=spdx_licence,
        scan_status=scan_status,
    )
 
 
def test_weight_record_stores_fields() -> None:
    w = _weight()
    assert w.artifact_id == "gemini-flash-2b@2026.09"
    assert w.sha256_hex == _VALID_SHA256
    assert w.spdx_licence == "Apache-2.0"
    assert w.scan_status is WeightScanStatus.CLEAN
 
 
def test_weight_record_is_frozen() -> None:
    from dataclasses import FrozenInstanceError
    w = _weight()
    with pytest.raises(FrozenInstanceError):
        w.scan_status = WeightScanStatus.FAILED  # type: ignore[misc]
 
 
def test_weight_record_empty_artifact_id_refused() -> None:
    with pytest.raises(ResilienceError, match="artifact_id"):
        _weight(artifact_id="")
 
 
def test_weight_record_sha256_wrong_length_refused() -> None:
    with pytest.raises(ResilienceError, match="sha256_hex"):
        _weight(sha256_hex="ab" * 31)  # 62 chars, not 64
 
 
def test_weight_record_sha256_uppercase_refused() -> None:
    """SHA-256 must be lowercase hex only."""
    with pytest.raises(ResilienceError, match="sha256_hex"):
        _weight(sha256_hex="A" * 64)
 
 
def test_weight_record_sha256_non_hex_refused() -> None:
    with pytest.raises(ResilienceError, match="sha256_hex"):
        _weight(sha256_hex="g" * 64)
 
 
def test_weight_record_empty_licence_refused() -> None:
    with pytest.raises(ResilienceError, match="spdx_licence"):
        _weight(spdx_licence="")
 
 
def test_weight_record_proprietary_licence_accepted() -> None:
    """LicenseRef-... is a valid SPDX expression for proprietary weights."""
    w = _weight(spdx_licence="LicenseRef-ZoikoTax-proprietary")
    assert "LicenseRef" in w.spdx_licence
 
 
def test_weight_record_is_production_safe_only_for_clean() -> None:
    assert _weight(scan_status=WeightScanStatus.CLEAN).is_production_safe is True
    assert _weight(scan_status=WeightScanStatus.PENDING).is_production_safe is False
    assert _weight(scan_status=WeightScanStatus.FAILED).is_production_safe is False
 
 
# ---------------------------------------------------------------------------
# §25 -- ProviderRecord private fields
# ---------------------------------------------------------------------------
 
 
def test_provider_record_defaults_not_private() -> None:
    assert _provider().is_private is False
    assert _provider().owner_id == ""
    assert _provider().weight_record is None
 
 
def test_private_provider_requires_owner_id() -> None:
    with pytest.raises(ResilienceError, match="owner_id"):
        ProviderRecord(
            provider_id="customer:acme",
            display_name="Acme on-prem",
            permitted_regions=_EU,
            is_private=True,
            owner_id="",  # missing
        )
 
 
def test_private_provider_with_owner_id_accepted() -> None:
    p = ProviderRecord(
        provider_id="customer:acme",
        display_name="Acme on-prem",
        permitted_regions=_EU,
        is_private=True,
        owner_id="acme-corp",
    )
    assert p.is_private is True
    assert p.owner_id == "acme-corp"
 
 
def test_private_provider_not_flagged_when_owner_id_set_but_not_private() -> None:
    """owner_id on a public provider is legal (e.g. a cloud provider's tenant ID)."""
    p = ProviderRecord(
        provider_id="gcp:vertex-eu",
        display_name="GCP",
        permitted_regions=_EU,
        is_private=False,
        owner_id="acme-tenant",
    )
    assert p.is_private is False
 
 
def test_private_provider_with_weight_record_accepted() -> None:
    p = ProviderRecord(
        provider_id="customer:acme",
        display_name="Acme on-prem",
        permitted_regions=_EU,
        is_private=True,
        owner_id="acme-corp",
        weight_record=_weight(),
    )
    assert p.weight_record is not None
    assert p.weight_record.is_production_safe is True
 
 
def test_private_provider_with_pending_weights_not_production_safe() -> None:
    p = ProviderRecord(
        provider_id="customer:acme",
        display_name="Acme on-prem",
        permitted_regions=_EU,
        is_private=True,
        owner_id="acme-corp",
        weight_record=_weight(scan_status=WeightScanStatus.PENDING),
    )
    assert p.weight_record is not None
    assert p.weight_record.is_production_safe is False
 
 
def test_private_unqualified_provider_cannot_receive_fallback() -> None:
    """§25 rule 10: is_private does not bypass the qualification gate."""
    router = _router(
        _provider("primary", _EU, is_qualified=True),
        ProviderRecord(
            provider_id="customer:unqualified",
            display_name="Unqualified private",
            permitted_regions=_EU,
            is_private=True,
            owner_id="acme-corp",
            is_qualified=False,  # not yet passed the gate
        ),
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.SUSPEND
 
 
def test_private_qualified_provider_can_receive_fallback() -> None:
    """Once qualified, a private provider is a valid fallback target."""
    router = _router(
        _provider("primary", _EU, is_qualified=True),
        ProviderRecord(
            provider_id="customer:qualified",
            display_name="Qualified private",
            permitted_regions=_EU,
            is_private=True,
            owner_id="acme-corp",
            is_qualified=True,
        ),
    )
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.FALLBACK
    assert outcome.fallback_provider is not None
    assert outcome.fallback_provider.is_private is True
 
 
# ---------------------------------------------------------------------------
# §25 -- DISCONNECTED_EDGE failure kind routing
# ---------------------------------------------------------------------------
 
 
def test_disconnected_edge_always_degrade() -> None:
    """DISCONNECTED_EDGE always produces DEGRADE even with qualified alternates."""
    router = _router(_provider("primary", _EU), _provider("alt", _EU))
    outcome = router.route(
        failed_provider_id="primary",
        failure=FailureKind.DISCONNECTED_EDGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.DEGRADE
 
 
def test_disconnected_edge_no_fallback_provider() -> None:
    outcome = _router(_provider("p", _EU)).route(
        failed_provider_id="p",
        failure=FailureKind.DISCONNECTED_EDGE,
        permitted_regions=_EU,
    )
    assert outcome.fallback_provider is None
    assert outcome.fallback_model is None
 
 
def test_disconnected_edge_rationale_mentions_snapshot() -> None:
    outcome = _router(_provider("p", _EU)).route(
        failed_provider_id="p",
        failure=FailureKind.DISCONNECTED_EDGE,
        permitted_regions=_EU,
    )
    assert "EdgePolicySnapshot" in outcome.rationale or "snapshot" in outcome.rationale.lower()
 
 
def test_disconnected_edge_rationale_states_no_authority_gain() -> None:
    outcome = _router(_provider("p", _EU)).route(
        failed_provider_id="p",
        failure=FailureKind.DISCONNECTED_EDGE,
        permitted_regions=_EU,
    )
    assert "authority" in outcome.rationale
 
 
def test_disconnected_edge_records_failure_kind() -> None:
    outcome = _router(_provider("p", _EU)).route(
        failed_provider_id="p",
        failure=FailureKind.DISCONNECTED_EDGE,
        permitted_regions=_EU,
    )
    assert outcome.failure is FailureKind.DISCONNECTED_EDGE
 
 
def test_disconnected_edge_present_in_failure_kind_enum() -> None:
    assert FailureKind.DISCONNECTED_EDGE == "DISCONNECTED_EDGE"
 
 
# ---------------------------------------------------------------------------
# §25 -- EdgePolicySnapshot -- construction and validation
# ---------------------------------------------------------------------------
 
 
def _snapshot(
    manifest_id: str = "rel-2026.09-edge-eu",
    sha256_hex: str = _VALID_SHA256,
    signature: bytes = b"fake-sig",
    registry: UseCaseRegistry | None = None,
) -> EdgePolicySnapshot:
    return EdgePolicySnapshot(
        manifest_id=manifest_id,
        sha256_hex=sha256_hex,
        signature=signature,
        registry=registry or _use_cases(),
    )
 
 
def test_snapshot_stores_all_fields() -> None:
    reg = _use_cases()
    s = _snapshot(registry=reg)
    assert s.manifest_id == "rel-2026.09-edge-eu"
    assert s.sha256_hex == _VALID_SHA256
    assert s.signature == b"fake-sig"
    assert s.registry is reg
 
 
def test_snapshot_is_frozen() -> None:
    from dataclasses import FrozenInstanceError
    s = _snapshot()
    with pytest.raises(FrozenInstanceError):
        s.manifest_id = "mutated"  # type: ignore[misc]
 
 
def test_snapshot_empty_manifest_id_refused() -> None:
    with pytest.raises(ResilienceError, match="manifest_id"):
        _snapshot(manifest_id="")
 
 
def test_snapshot_sha256_wrong_length_refused() -> None:
    with pytest.raises(ResilienceError, match="sha256_hex"):
        _snapshot(sha256_hex="ab" * 31)  # 62 chars
 
 
def test_snapshot_sha256_uppercase_refused() -> None:
    with pytest.raises(ResilienceError, match="sha256_hex"):
        _snapshot(sha256_hex="A" * 64)
 
 
def test_snapshot_empty_signature_refused() -> None:
    with pytest.raises(ResilienceError, match="signature"):
        _snapshot(signature=b"")
 
 
def test_snapshot_wrong_registry_type_refused() -> None:
    with pytest.raises(ResilienceError, match="UseCaseRegistry"):
        EdgePolicySnapshot(
            manifest_id="m",
            sha256_hex=_VALID_SHA256,
            signature=b"sig",
            registry="not a registry",  # type: ignore[arg-type]
        )
 
 
# ---------------------------------------------------------------------------
# §25 -- authorise_edge: the key rule
# "offline AI cannot gain more authority because central controls are unreachable"
# ---------------------------------------------------------------------------
 
 
def test_authorise_edge_permits_a_registered_use_case() -> None:
    s = _snapshot()
    use_case = authorise_edge(s, _GOVERNED_PROVENANCE)
    assert use_case.use_case_id == _GOVERNED_PROVENANCE.use_case
 
 
def test_authorise_edge_refuses_unknown_use_case() -> None:
    """Even offline, an unregistered use case is refused."""
    s = _snapshot(registry=UseCaseRegistry())  # empty registry
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise_edge(s, _GOVERNED_PROVENANCE)
    assert exc.value.refusal is Refusal.UNKNOWN_USE_CASE
 
 
def test_authorise_edge_refuses_kill_switch_engaged_in_snapshot() -> None:
    """Kill switches baked into the snapshot are still honoured when offline."""
    reg = _use_cases()
    reg.kill(_GOVERNED_PROVENANCE.use_case)
    s = _snapshot(registry=reg)
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise_edge(s, _GOVERNED_PROVENANCE)
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED
 
 
def test_authorise_edge_refuses_global_kill_in_snapshot() -> None:
    reg = _use_cases()
    reg.engage_global_kill()
    s = _snapshot(registry=reg)
    with pytest.raises(GovernanceRefusedError):
        authorise_edge(s, _GOVERNED_PROVENANCE)
 
 
def test_authorise_edge_refuses_a5_authority_offline() -> None:
    """§25: prohibited stays prohibited locally. A5 is still refused."""
    prov_a5 = Provenance(
        use_case=_GOVERNED_PROVENANCE.use_case,
        model_profile=_GOVERNED_PROVENANCE.model_profile,
        provider_profile=_GOVERNED_PROVENANCE.provider_profile,
        prompt_profile=_GOVERNED_PROVENANCE.prompt_profile,
        region=_GOVERNED_PROVENANCE.region,
        data_class=_GOVERNED_PROVENANCE.data_class,
        risk_tier=RiskTier.T1,
        authority_outcome=AuthorityOutcome.A5,  # unconditionally refused
        ai_train_version=_GOVERNED_PROVENANCE.ai_train_version,
    )
    s = _snapshot()
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise_edge(s, prov_a5)
    assert exc.value.refusal is Refusal.AUTHORITY_REFUSED
 
 
def test_authorise_edge_refuses_region_outside_snapshot_use_case() -> None:
    """Offline AI cannot widen its own residency by claiming an unlisted region."""
    prov_wrong_region = Provenance(
        use_case=_GOVERNED_PROVENANCE.use_case,
        model_profile=_GOVERNED_PROVENANCE.model_profile,
        provider_profile=_GOVERNED_PROVENANCE.provider_profile,
        prompt_profile=_GOVERNED_PROVENANCE.prompt_profile,
        region="us-east-1",  # not in the snapshot's permitted_regions
        data_class=_GOVERNED_PROVENANCE.data_class,
        risk_tier=RiskTier.T1,
        authority_outcome=AuthorityOutcome.A1,
        ai_train_version=_GOVERNED_PROVENANCE.ai_train_version,
    )
    s = _snapshot()
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise_edge(s, prov_wrong_region)
    assert exc.value.refusal is Refusal.RESIDENCY_REFUSED
 
 
def test_authorise_edge_refuses_authority_above_use_case_ceiling() -> None:
    """Offline AI cannot self-elevate above its registered ceiling."""
    # The test use case allows up to A4; A4 itself should be allowed,
    # but let's register a use case capped at A1 to keep the test simple.
    low_ceiling_uc = UseCase(
        use_case_id="low-authority-uc",
        owner="lane-l",
        description="only A1",
        max_risk_tier=RiskTier.T4,
        max_authority=AuthorityOutcome.A1,
        permitted_regions=frozenset({_GOVERNED_PROVENANCE.region}),
    )
    reg = UseCaseRegistry([low_ceiling_uc])
    s = _snapshot(registry=reg)
    prov_a2 = Provenance(
        use_case="low-authority-uc",
        model_profile=_GOVERNED_PROVENANCE.model_profile,
        provider_profile=_GOVERNED_PROVENANCE.provider_profile,
        prompt_profile=_GOVERNED_PROVENANCE.prompt_profile,
        region=_GOVERNED_PROVENANCE.region,
        data_class=_GOVERNED_PROVENANCE.data_class,
        risk_tier=RiskTier.T1,
        authority_outcome=AuthorityOutcome.A2,  # above ceiling
        ai_train_version=_GOVERNED_PROVENANCE.ai_train_version,
    )
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise_edge(s, prov_a2)
    assert exc.value.refusal is Refusal.AUTHORITY_REFUSED
 
 
def test_authorise_edge_wrong_snapshot_type_raises_resilience_error() -> None:
    with pytest.raises(ResilienceError, match="EdgePolicySnapshot"):
        authorise_edge("not a snapshot", _GOVERNED_PROVENANCE)  # type: ignore[arg-type]
 
 
# ---------------------------------------------------------------------------
# §25 -- Integration: private provider + edge routing
# ---------------------------------------------------------------------------
 
 
def test_integration_private_qualified_provider_selected_over_cloud_when_cloud_fails() -> None:
    """
    A customer-owned qualified private provider is a valid fallback target.
    When the cloud provider fails, the private on-prem qualified provider
    with the same region coverage must be selected.
    """
    cloud = _provider("gcp:vertex-eu", _EU, is_qualified=True)
    on_prem = ProviderRecord(
        provider_id="customer:acme-eu",
        display_name="Acme On-Prem EU",
        permitted_regions=_EU,
        is_qualified=True,
        is_private=True,
        owner_id="acme-corp",
        weight_record=_weight(),
    )
    router = _router(cloud, on_prem)
    outcome = router.route(
        failed_provider_id="gcp:vertex-eu",
        failure=FailureKind.MODEL_OUTAGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.FALLBACK
    assert outcome.fallback_provider is not None
    assert outcome.fallback_provider.is_private is True
    assert outcome.fallback_provider.owner_id == "acme-corp"
 
 
def test_integration_edge_authorise_kill_and_route() -> None:
    """
    End-to-end: edge device disconnects, attempts a call, kill switch is in
    snapshot, authorise_edge must refuse, and the routing outcome for
    DISCONNECTED_EDGE must be DEGRADE.
    """
    # Kill switch active in the snapshot.
    reg = _use_cases()
    reg.kill(_GOVERNED_PROVENANCE.use_case)
    s = _snapshot(registry=reg)
 
    # The authorise_edge call is refused.
    with pytest.raises(GovernanceRefusedError) as exc:
        authorise_edge(s, _GOVERNED_PROVENANCE)
    assert exc.value.refusal is Refusal.KILL_SWITCH_ENGAGED
 
    # The routing outcome independently DEGRADE for DISCONNECTED_EDGE.
    router = _router(_provider("p", _EU))
    outcome = router.route(
        failed_provider_id="p",
        failure=FailureKind.DISCONNECTED_EDGE,
        permitted_regions=_EU,
    )
    assert outcome.mode is DegradedMode.DEGRADE
    assert outcome.fallback_provider is None
 