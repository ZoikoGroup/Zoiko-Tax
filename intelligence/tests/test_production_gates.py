"""Tests for production_gates.py (Chapter 17 §31 — G-AIARCH-01 through G-AIARCH-20).

Coverage target
---------------
Every gate is exercised with:
  - A passing scenario (well-formed registries).
  - A failing scenario (the specific invariant is broken).
  - A NOT_APPLICABLE scenario where meaningful (e.g. empty registry).

The runner is verified to:
  - Run all 20 gates unconditionally (no short-circuit).
  - Set all_passed=True only when zero gates FAIL.
  - Return correct pass_count / fail_count / na_count.
  - Expose failed_gates() and result_for() navigation helpers.
  - Return an immutable ArchGateReport.

The PromptProfileRegistry is exercised standalone as well as integrated.
"""

from __future__ import annotations

import pytest

from ztax_gateway.governance import UseCase, UseCaseRegistry
from ztax_gateway.production_gates import (
    ArchGateId,
    ArchGateReport,
    GateVerdict,
    ProductionGateRunner,
    PromptProfileRegistry,
    run_production_gates,
)
from ztax_gateway.production_registries import (
    AIReleaseManifest,
    ManifestRegistry,
    PromptProfile,
    build_manifest,
)
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier
from ztax_gateway.resilience import (
    ModelRecord,
    ModelWeightRecord,
    ProviderRecord,
    ProviderRegistry,
    WeightScanStatus,
)
from ztax_gateway.tool_broker import ActionClass, ToolCatalog, ToolProfile

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

_UC_ID = "uc-gates-test"
_REGION = "eu-west-1"
_MODEL_ID = "gemini-pro@2026-09"
_PROVIDER_ID = "gcp:eu-west-1"
_MANIFEST_VERSION = "1.0.0"

_CLEAN_SHA = "a" * 64


# ---------------------------------------------------------------------------
# Builder helpers
# ---------------------------------------------------------------------------


def _make_use_case(
    uc_id: str = _UC_ID,
    owner: str = "platform-team",
    max_authority: AuthorityOutcome = AuthorityOutcome.A2,
    suspended: bool = False,
) -> UseCase:
    return UseCase(
        use_case_id=uc_id,
        owner=owner,
        description="Test use case",
        max_risk_tier=RiskTier.T2,
        max_authority=max_authority,
        permitted_regions=frozenset({_REGION}),
        suspended=suspended,
    )


def _make_manifest(
    *,
    uc_id: str = _UC_ID,
    resolved_model_id: str = _MODEL_ID,
    authority_level: AuthorityOutcome = AuthorityOutcome.A2,
    agent_profile_id: str | None = None,
    tool_policy_id: str | None = None,
    evaluation_profile_id: str = "ep-001",
    prompt_profile_id: str = "pp-001",
) -> AIReleaseManifest:
    return build_manifest(
        ai_use_case_id=uc_id,
        release_version=_MANIFEST_VERSION,
        authority_level=authority_level,
        risk_tier=RiskTier.T0,
        model_profile_id="mp-001",
        resolved_model_id=resolved_model_id,
        prompt_profile_id=prompt_profile_id,
        evaluation_profile_id=evaluation_profile_id,
        policy_bundle_id="pb-001",
        allowed_regions=frozenset({_REGION}),
        agent_profile_id=agent_profile_id,
        tool_policy_id=tool_policy_id,
    )


def _make_provider(
    *,
    provider_id: str = _PROVIDER_ID,
    regions: frozenset[str] = frozenset({_REGION}),
    is_qualified: bool = True,
    is_private: bool = False,
    owner_id: str = "",
    weight_record: ModelWeightRecord | None = None,
) -> ProviderRecord:
    return ProviderRecord(
        provider_id=provider_id,
        display_name="GCP EU West 1",
        permitted_regions=regions,
        is_qualified=is_qualified,
        is_private=is_private,
        owner_id=owner_id,
        weight_record=weight_record,
    )


def _make_tool(
    *,
    tool_id: str = "tool-read-001",
    owner: str = "platform-team",
    action_class: ActionClass = ActionClass.READ,
) -> ToolProfile:
    return ToolProfile(
        tool_id=tool_id,
        owner=owner,
        description="Read only tool",
        action_class=action_class,
        required_scopes=frozenset({"read:data"}),
        idempotent=True,
    )


def _make_prompt_profile(
    *,
    profile_id: str = "pp-001",
    deprecated: bool = False,
    safety_rules: tuple[str, ...] = ("No PII in output",),
) -> PromptProfile:
    return PromptProfile(
        profile_id=profile_id,
        owner="platform-team",
        version="1.0.0",
        system_prompt="You are a tax assistant.",
        variable_schema={},
        output_schema={},
        max_risk_tier=RiskTier.T2,
        authority_ceiling=AuthorityOutcome.A2,
        safety_rules=safety_rules,
        deprecated=deprecated,
    )


def _weight_record(status: WeightScanStatus = WeightScanStatus.CLEAN) -> ModelWeightRecord:
    return ModelWeightRecord(
        artifact_id="weights-v1",
        sha256_hex=_CLEAN_SHA,
        spdx_licence="Apache-2.0",
        scan_status=status,
    )


# ---------------------------------------------------------------------------
# Full well-formed fixture setup (all 20 gates PASS)
# ---------------------------------------------------------------------------


@pytest.fixture()
def uc_reg() -> UseCaseRegistry:
    reg = UseCaseRegistry()
    reg.register(_make_use_case())
    return reg


@pytest.fixture()
def mf_reg(uc_reg: UseCaseRegistry) -> ManifestRegistry:
    reg = ManifestRegistry()
    reg.add(_make_manifest())
    return reg


@pytest.fixture()
def pr_reg() -> ProviderRegistry:
    reg = ProviderRegistry()
    reg.register_provider(_make_provider())
    reg.register_model(ModelRecord(model_id=_MODEL_ID, provider_id=_PROVIDER_ID))
    return reg


@pytest.fixture()
def tool_cat() -> ToolCatalog:
    cat = ToolCatalog()
    cat.register(_make_tool())
    return cat


@pytest.fixture()
def pp_reg() -> PromptProfileRegistry:
    reg = PromptProfileRegistry()
    reg.register(_make_prompt_profile())
    return reg


@pytest.fixture()
def runner(
    uc_reg: UseCaseRegistry,
    mf_reg: ManifestRegistry,
    pr_reg: ProviderRegistry,
    tool_cat: ToolCatalog,
    pp_reg: PromptProfileRegistry,
) -> ProductionGateRunner:
    return ProductionGateRunner(
        use_case_registry=uc_reg,
        manifest_registry=mf_reg,
        tool_catalog=tool_cat,
        provider_registry=pr_reg,
        prompt_registry=pp_reg,
    )


# ---------------------------------------------------------------------------
# Report structure
# ---------------------------------------------------------------------------


def test_report_has_20_results(runner: ProductionGateRunner) -> None:
    report = runner.run()
    assert len(report.results) == 21


def test_report_is_immutable(runner: ProductionGateRunner) -> None:
    report = runner.run()
    with pytest.raises((AttributeError, TypeError)):
        report.pass_count = 0  # type: ignore[misc]


def test_well_formed_registries_all_pass(runner: ProductionGateRunner) -> None:
    report = runner.run()
    fails = report.failed_gates()
    assert report.all_passed, f"Unexpected failures: {[(f.gate_id, f.evidence) for f in fails]}"
    assert report.fail_count == 0


def test_counts_sum_to_20(runner: ProductionGateRunner) -> None:
    report = runner.run()
    assert report.pass_count + report.fail_count + report.na_count == 21


def test_result_for_returns_correct_gate(runner: ProductionGateRunner) -> None:
    report = runner.run()
    r = report.result_for(ArchGateId.G01)
    assert r is not None
    assert r.gate_id is ArchGateId.G01


def test_result_for_unknown_gate_is_none(runner: ProductionGateRunner) -> None:
    report = runner.run()
    assert report.result_for("nonexistent") is None  # type: ignore[arg-type]


def test_run_at_is_utc(runner: ProductionGateRunner) -> None:
    report = runner.run()
    assert report.run_at.utcoffset() is not None


def test_as_summary_contains_counts(runner: ProductionGateRunner) -> None:
    report = runner.run()
    summary = report.as_summary()
    assert "pass=" in summary
    assert "fail=" in summary
    assert "ALL PASSED" in summary


def test_failed_gates_not_empty_when_failure(
    mf_reg: ManifestRegistry,
    pr_reg: ProviderRegistry,
    tool_cat: ToolCatalog,
) -> None:
    # Inject a bad use case directly (register() blocks empty owner at the
    # governance layer itself, so we bypass it to exercise the gate).
    bad_uc_reg = UseCaseRegistry()
    bad_uc = UseCase(
        use_case_id="bad-uc",
        owner="temp",
        description="injected",
        max_risk_tier=RiskTier.T0,
        max_authority=AuthorityOutcome.A1,
        permitted_regions=frozenset({_REGION}),
    )
    bad_uc_reg.register(bad_uc)
    # Bypass the owner guard by injecting with empty owner into the internal dict.
    import dataclasses
    empty_owner_uc = dataclasses.replace(bad_uc, owner="")
    bad_uc_reg._use_cases["bad-uc"] = empty_owner_uc
    runner = ProductionGateRunner(
        use_case_registry=bad_uc_reg,
        manifest_registry=mf_reg,
        tool_catalog=tool_cat,
        provider_registry=pr_reg,
    )
    report = runner.run()
    assert report.fail_count >= 1
    assert not report.all_passed
    failed = report.failed_gates()
    assert any(f.gate_id is ArchGateId.G01 for f in failed)


# ---------------------------------------------------------------------------
# G-AIARCH-01: Every use case has a named owner
# ---------------------------------------------------------------------------


def test_g01_pass(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G01)
    assert r is not None and r.verdict is GateVerdict.PASS


def test_g01_fail_empty_owner(
    mf_reg: ManifestRegistry,
    pr_reg: ProviderRegistry,
    tool_cat: ToolCatalog,
) -> None:
    # UseCaseRegistry.register() blocks empty owner, so inject via internal dict.
    import dataclasses
    bad_reg = UseCaseRegistry()
    uc = UseCase(
        use_case_id=_UC_ID, owner="temp", description="x",
        max_risk_tier=RiskTier.T0, max_authority=AuthorityOutcome.A1,
        permitted_regions=frozenset({_REGION}),
    )
    bad_reg.register(uc)
    bad_reg._use_cases[_UC_ID] = dataclasses.replace(uc, owner="")
    runner = ProductionGateRunner(
        use_case_registry=bad_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G01)
    assert r is not None and r.verdict is GateVerdict.FAIL
    assert _UC_ID in r.evidence


def test_g01_na_empty_registry(
    mf_reg: ManifestRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
) -> None:
    runner = ProductionGateRunner(
        use_case_registry=UseCaseRegistry(), manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G01)
    assert r is not None and r.verdict is GateVerdict.NOT_APPLICABLE


# ---------------------------------------------------------------------------
# G-AIARCH-02: No A5 max_authority in any use case
# ---------------------------------------------------------------------------


def test_g02_pass(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G02)
    assert r is not None and r.verdict is GateVerdict.PASS


# A5 is refused at construction by governance.py so we test the gate
# logic by asserting G02 is NA when no use cases are registered.
def test_g02_na_empty_registry(
    mf_reg: ManifestRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
) -> None:
    runner = ProductionGateRunner(
        use_case_registry=UseCaseRegistry(), manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G02)
    assert r is not None and r.verdict is GateVerdict.NOT_APPLICABLE


# ---------------------------------------------------------------------------
# G-AIARCH-03: Every ACTIVE manifest's use_case_id resolves
# ---------------------------------------------------------------------------


def test_g03_pass(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G03)
    assert r is not None and r.verdict is GateVerdict.PASS


def test_g03_fail_unregistered_uc(
    pr_reg: ProviderRegistry, tool_cat: ToolCatalog, uc_reg: UseCaseRegistry,
) -> None:
    # Manifest references a use case that is NOT in the registry.
    mf_reg = ManifestRegistry()
    orphan_manifest = _make_manifest(uc_id="ORPHAN-UC")
    mf_reg.add(orphan_manifest)
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G03)
    assert r is not None and r.verdict is GateVerdict.FAIL
    # Evidence contains the manifest_id of the offending manifest.
    assert orphan_manifest.manifest_id in r.evidence


def test_g03_na_no_active_manifests(
    uc_reg: UseCaseRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
) -> None:
    mf_reg = ManifestRegistry()
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G03)
    assert r is not None and r.verdict is GateVerdict.NOT_APPLICABLE


# ---------------------------------------------------------------------------
# G-AIARCH-04: Every ACTIVE manifest is evaluation-gated
# ---------------------------------------------------------------------------


def test_g04_pass(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G04)
    assert r is not None and r.verdict is GateVerdict.PASS


def test_g04_fail_empty_eval_profile(
    uc_reg: UseCaseRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
) -> None:
    mf_reg = ManifestRegistry()
    mf_reg.add(_make_manifest(evaluation_profile_id=""))
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G04)
    assert r is not None and r.verdict is GateVerdict.FAIL


# ---------------------------------------------------------------------------
# G-AIARCH-06: Every ACTIVE manifest passes verify_hash()
# ---------------------------------------------------------------------------


def test_g06_pass(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G06)
    assert r is not None and r.verdict is GateVerdict.PASS


# ---------------------------------------------------------------------------
# G-AIARCH-08: Prompt profiles in ACTIVE manifests are non-deprecated
# ---------------------------------------------------------------------------


def test_g08_pass(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G08)
    assert r is not None and r.verdict is GateVerdict.PASS


def test_g08_na_no_prompt_registry(
    uc_reg: UseCaseRegistry, mf_reg: ManifestRegistry,
    pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
) -> None:
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
        # no prompt_registry
    )
    r = runner.run().result_for(ArchGateId.G08)
    assert r is not None and r.verdict is GateVerdict.NOT_APPLICABLE


def test_g08_fail_deprecated_profile(
    uc_reg: UseCaseRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
) -> None:
    mf_reg = ManifestRegistry()
    mf_reg.add(_make_manifest(prompt_profile_id="pp-deprecated"))
    pp_reg = PromptProfileRegistry()
    pp_reg.register(_make_prompt_profile(profile_id="pp-deprecated", deprecated=True))
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
        prompt_registry=pp_reg,
    )
    r = runner.run().result_for(ArchGateId.G08)
    assert r is not None and r.verdict is GateVerdict.FAIL
    assert "deprecated" in r.evidence


def test_g08_fail_no_safety_rules(
    uc_reg: UseCaseRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
) -> None:
    mf_reg = ManifestRegistry()
    mf_reg.add(_make_manifest(prompt_profile_id="pp-no-rules"))
    pp_reg = PromptProfileRegistry()
    pp_reg.register(_make_prompt_profile(profile_id="pp-no-rules", safety_rules=()))
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
        prompt_registry=pp_reg,
    )
    r = runner.run().result_for(ArchGateId.G08)
    assert r is not None and r.verdict is GateVerdict.FAIL
    assert "safety" in r.evidence


def test_g08_fail_missing_from_registry(
    uc_reg: UseCaseRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
) -> None:
    mf_reg = ManifestRegistry()
    mf_reg.add(_make_manifest(prompt_profile_id="pp-unknown"))
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
        prompt_registry=PromptProfileRegistry(),  # empty — profile not found
    )
    r = runner.run().result_for(ArchGateId.G08)
    assert r is not None and r.verdict is GateVerdict.FAIL
    assert "missing" in r.evidence.lower()


# ---------------------------------------------------------------------------
# G-AIARCH-09: No PRIVILEGED-class tool in ToolCatalog
# ---------------------------------------------------------------------------


def test_g09_pass(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G09)
    assert r is not None and r.verdict is GateVerdict.PASS


def test_g09_na_empty_catalog(
    uc_reg: UseCaseRegistry, mf_reg: ManifestRegistry, pr_reg: ProviderRegistry,
) -> None:
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=ToolCatalog(), provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G09)
    assert r is not None and r.verdict is GateVerdict.NOT_APPLICABLE


# ---------------------------------------------------------------------------
# G-AIARCH-10: Every tool has an owner
# ---------------------------------------------------------------------------


def test_g10_pass(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G10)
    assert r is not None and r.verdict is GateVerdict.PASS


# ---------------------------------------------------------------------------
# G-AIARCH-11: Every provider has at least one permitted region
# ---------------------------------------------------------------------------


def test_g11_pass(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G11)
    assert r is not None and r.verdict is GateVerdict.PASS


def test_g11_na_empty_provider_registry(
    uc_reg: UseCaseRegistry, mf_reg: ManifestRegistry, tool_cat: ToolCatalog,
) -> None:
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=ProviderRegistry(),
    )
    r = runner.run().result_for(ArchGateId.G11)
    assert r is not None and r.verdict is GateVerdict.NOT_APPLICABLE


# ---------------------------------------------------------------------------
# G-AIARCH-12: Private providers with is_qualified=True have a weight_record
# ---------------------------------------------------------------------------


def test_g12_pass(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G12)
    assert r is not None and r.verdict is GateVerdict.PASS


def test_g12_fail_private_qualified_without_weight(
    uc_reg: UseCaseRegistry, mf_reg: ManifestRegistry, tool_cat: ToolCatalog,
) -> None:
    pr_reg = ProviderRegistry()
    pr_reg.register_provider(_make_provider(
        provider_id="customer:eu",
        is_private=True,
        owner_id="acme",
        is_qualified=True,
        weight_record=None,  # <-- no verifiable gate
    ))
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G12)
    assert r is not None and r.verdict is GateVerdict.FAIL
    assert "customer:eu" in r.evidence


# ---------------------------------------------------------------------------
# G-AIARCH-13: Every private provider has a non-empty owner_id
# ---------------------------------------------------------------------------


def test_g13_na_no_private_providers(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G13)
    assert r is not None and r.verdict is GateVerdict.NOT_APPLICABLE


def test_g13_pass_private_with_owner_id(
    uc_reg: UseCaseRegistry, mf_reg: ManifestRegistry, tool_cat: ToolCatalog,
) -> None:
    pr_reg = ProviderRegistry()
    pr_reg.register_provider(_make_provider(
        provider_id="customer:eu",
        is_private=True,
        owner_id="acme-corp",
        weight_record=_weight_record(),
    ))
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G13)
    assert r is not None and r.verdict is GateVerdict.PASS


# ---------------------------------------------------------------------------
# G-AIARCH-14: Qualified providers with weight records are CLEAN
# ---------------------------------------------------------------------------


def test_g14_na_no_weight_records(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G14)
    assert r is not None and r.verdict is GateVerdict.NOT_APPLICABLE


def test_g14_pass_clean_weight(
    uc_reg: UseCaseRegistry, mf_reg: ManifestRegistry, tool_cat: ToolCatalog,
) -> None:
    pr_reg = ProviderRegistry()
    pr_reg.register_provider(_make_provider(
        provider_id="edge:eu",
        is_private=True,
        owner_id="acme",
        weight_record=_weight_record(WeightScanStatus.CLEAN),
    ))
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G14)
    assert r is not None and r.verdict is GateVerdict.PASS


def test_g14_fail_pending_scan(
    uc_reg: UseCaseRegistry, mf_reg: ManifestRegistry, tool_cat: ToolCatalog,
) -> None:
    pr_reg = ProviderRegistry()
    pr_reg.register_provider(_make_provider(
        provider_id="edge:eu",
        is_private=True,
        owner_id="acme",
        is_qualified=True,
        weight_record=_weight_record(WeightScanStatus.PENDING),
    ))
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G14)
    assert r is not None and r.verdict is GateVerdict.FAIL
    assert "PENDING" in r.evidence


def test_g14_fail_failed_scan(
    uc_reg: UseCaseRegistry, mf_reg: ManifestRegistry, tool_cat: ToolCatalog,
) -> None:
    pr_reg = ProviderRegistry()
    pr_reg.register_provider(_make_provider(
        provider_id="edge:eu",
        is_private=True,
        owner_id="acme",
        is_qualified=True,
        weight_record=_weight_record(WeightScanStatus.FAILED),
    ))
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G14)
    assert r is not None and r.verdict is GateVerdict.FAIL
    assert "FAILED" in r.evidence


# ---------------------------------------------------------------------------
# G-AIARCH-15: No deprecated model in an ACTIVE manifest's resolved_model_id
# ---------------------------------------------------------------------------


def test_g15_pass(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G15)
    assert r is not None and r.verdict is GateVerdict.PASS


def test_g15_fail_deprecated_model(
    uc_reg: UseCaseRegistry, tool_cat: ToolCatalog,
) -> None:
    dep_model_id = "deprecated-model@2024"
    pr_reg = ProviderRegistry()
    pr_reg.register_provider(_make_provider())
    pr_reg.register_model(ModelRecord(
        model_id=dep_model_id, provider_id=_PROVIDER_ID, is_deprecated=True,
    ))
    mf_reg = ManifestRegistry()
    mf_reg.add(_make_manifest(resolved_model_id=dep_model_id))
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G15)
    assert r is not None and r.verdict is GateVerdict.FAIL
    assert dep_model_id in r.evidence


# ---------------------------------------------------------------------------
# G-AIARCH-16: No suspended use case has an ACTIVE manifest
# ---------------------------------------------------------------------------


def test_g16_pass(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G16)
    assert r is not None and r.verdict is GateVerdict.PASS


def test_g16_fail_suspended_uc_with_active_manifest(
    mf_reg: ManifestRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
) -> None:
    # Suspend the use case after the manifest is registered.
    uc_reg = UseCaseRegistry()
    uc_reg.register(_make_use_case(suspended=True))
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G16)
    assert r is not None and r.verdict is GateVerdict.FAIL
    assert _UC_ID in r.evidence


# ---------------------------------------------------------------------------
# G-AIARCH-17: Pinned resolved_model_id (no floating aliases)
# ---------------------------------------------------------------------------


def test_g17_pass(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G17)
    assert r is not None and r.verdict is GateVerdict.PASS


@pytest.mark.parametrize(
    "alias",
    ["gemini-latest", "model-current", "gpt-stable", "llm-preview", "model-beta"],
)
def test_g17_fail_alias_model_id(
    alias: str,
    uc_reg: UseCaseRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
) -> None:
    mf_reg = ManifestRegistry()
    mf_reg.add(_make_manifest(resolved_model_id=alias))
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G17)
    assert r is not None and r.verdict is GateVerdict.FAIL


# ---------------------------------------------------------------------------
# G-AIARCH-18: Agentic manifests have a tool_policy_id
# ---------------------------------------------------------------------------


def test_g18_na_no_agentic_manifests(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G18)
    assert r is not None and r.verdict is GateVerdict.NOT_APPLICABLE


def test_g18_pass_agentic_with_policy(
    uc_reg: UseCaseRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
) -> None:
    mf_reg = ManifestRegistry()
    mf_reg.add(_make_manifest(agent_profile_id="agt-001", tool_policy_id="tp-001"))
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G18)
    assert r is not None and r.verdict is GateVerdict.PASS


def test_g18_fail_agentic_without_policy(
    uc_reg: UseCaseRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
) -> None:
    mf_reg = ManifestRegistry()
    mf_reg.add(_make_manifest(agent_profile_id="agt-001", tool_policy_id=None))
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G18)
    assert r is not None and r.verdict is GateVerdict.FAIL


# ---------------------------------------------------------------------------
# G-AIARCH-19: Manifest authority_level <= UseCase max_authority
# ---------------------------------------------------------------------------


def test_g19_pass(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G19)
    assert r is not None and r.verdict is GateVerdict.PASS


def test_g19_fail_manifest_exceeds_uc_ceiling(
    pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
) -> None:
    uc_reg = UseCaseRegistry()
    uc_reg.register(_make_use_case(max_authority=AuthorityOutcome.A1))  # ceiling A1
    mf_reg = ManifestRegistry()
    mf_reg.add(_make_manifest(authority_level=AuthorityOutcome.A2))    # A2 > A1 → FAIL
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G19)
    assert r is not None and r.verdict is GateVerdict.FAIL
    assert "A2" in r.evidence and "A1" in r.evidence


# ---------------------------------------------------------------------------
# G-AIARCH-20: resolved_model_id is registered in ProviderRegistry
# ---------------------------------------------------------------------------


def test_g20_pass(runner: ProductionGateRunner) -> None:
    r = runner.run().result_for(ArchGateId.G20)
    assert r is not None and r.verdict is GateVerdict.PASS


def test_g20_fail_unregistered_model(
    uc_reg: UseCaseRegistry, tool_cat: ToolCatalog,
) -> None:
    pr_reg = ProviderRegistry()
    pr_reg.register_provider(_make_provider())
    # Do NOT register the model in ProviderRegistry.
    mf_reg = ManifestRegistry()
    mf_reg.add(_make_manifest(resolved_model_id="unregistered-model@2026"))
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
    )
    r = runner.run().result_for(ArchGateId.G20)
    assert r is not None and r.verdict is GateVerdict.FAIL
    assert "unregistered-model@2026" in r.evidence


# ---------------------------------------------------------------------------
# Convenience function wrapper
# ---------------------------------------------------------------------------


def test_run_production_gates_function(
    uc_reg: UseCaseRegistry,
    mf_reg: ManifestRegistry,
    pr_reg: ProviderRegistry,
    tool_cat: ToolCatalog,
    pp_reg: PromptProfileRegistry,
) -> None:
    report = run_production_gates(
        use_case_registry=uc_reg,
        manifest_registry=mf_reg,
        tool_catalog=tool_cat,
        provider_registry=pr_reg,
        prompt_registry=pp_reg,
    )
    assert isinstance(report, ArchGateReport)
    assert report.all_passed


# ---------------------------------------------------------------------------
# PromptProfileRegistry standalone
# ---------------------------------------------------------------------------


def test_prompt_registry_register_and_get() -> None:
    reg = PromptProfileRegistry()
    p = _make_prompt_profile()
    reg.register(p)
    assert reg.get("pp-001") is p
    assert reg.get("unknown") is None
    assert reg.count == 1


def test_prompt_registry_re_register_replaces() -> None:
    reg = PromptProfileRegistry()
    p1 = _make_prompt_profile()
    p2 = _make_prompt_profile(deprecated=True)
    reg.register(p1)
    reg.register(p2)
    assert reg.get("pp-001") is p2
    assert reg.count == 1


# ---------------------------------------------------------------------------
# ArchGateResult evidence is always non-empty for PASS/FAIL
# ---------------------------------------------------------------------------


def test_all_pass_results_have_non_empty_evidence(runner: ProductionGateRunner) -> None:
    report = runner.run()
    for r in report.results:
        if r.verdict in (GateVerdict.PASS, GateVerdict.FAIL):
            assert r.evidence.strip(), (
                f"{r.gate_id} has empty evidence (verdict={r.verdict})"
            )


# ---------------------------------------------------------------------------
# G-AIARCH-21: lifecycle consistency gate
# ---------------------------------------------------------------------------


from ztax_gateway.release_lifecycle import (  # noqa: E402
    LifecycleAwareManifestRegistry,
    LifecycleState,
    ManifestLifecycle,
)


def _make_lc_reg_with_state(
    manifest: AIReleaseManifest,
    state: LifecycleState,
    uc_reg: UseCaseRegistry,
    prov: Provenance,
) -> dict[str, ManifestLifecycle]:
    """Build a lifecycle dict with the given manifest advanced to *state*."""
    lc = ManifestLifecycle(manifest)
    pipeline = [
        LifecycleState.DESIGN,
        LifecycleState.VALIDATION,
        LifecycleState.SHADOW,
        LifecycleState.PILOT,
        LifecycleState.PRODUCTION,
    ]
    for _ in pipeline:
        lc.advance(uc_reg, prov, "alice")
        if lc.state is state:
            break
    return {manifest.manifest_id: lc}


@pytest.fixture()
def g21_prov() -> Provenance:
    return Provenance(
        use_case=_UC_ID,
        model_profile="mp-001",
        provider_profile="pp-provider-001",
        prompt_profile="pp-001",
        region=_REGION,
        data_class="INTERNAL",
        risk_tier=RiskTier.T0,
        authority_outcome=AuthorityOutcome.A2,
        ai_train_version="train-1",
    )


def test_g21_na_no_lifecycle_registry(runner: ProductionGateRunner) -> None:
    """G21 is NOT_APPLICABLE when no lifecycle_registry is supplied."""
    r = runner.run().result_for(ArchGateId.G21)
    assert r is not None and r.verdict is GateVerdict.NOT_APPLICABLE


def test_g21_na_no_active_manifests(
    uc_reg: UseCaseRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
    g21_prov: Provenance,
) -> None:
    mf_reg = ManifestRegistry()
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
        lifecycle_registry={},
    )
    r = runner.run().result_for(ArchGateId.G21)
    assert r is not None and r.verdict is GateVerdict.NOT_APPLICABLE


def test_g21_fail_missing_lifecycle_entry(
    uc_reg: UseCaseRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
) -> None:
    """Manifest is ACTIVE in registry but has no lifecycle entry."""
    mf = _make_manifest()
    mf_reg = ManifestRegistry()
    mf_reg.add(mf)
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
        lifecycle_registry={},   # <-- empty: no entry for this manifest
    )
    r = runner.run().result_for(ArchGateId.G21)
    assert r is not None and r.verdict is GateVerdict.FAIL
    assert mf.manifest_id in r.evidence


def test_g21_fail_lifecycle_in_research(
    uc_reg: UseCaseRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
) -> None:
    """Manifest is ACTIVE in registry but lifecycle is still RESEARCH."""
    mf = _make_manifest()
    mf_reg = ManifestRegistry()
    mf_reg.add(mf)
    lc = ManifestLifecycle(mf)   # starts at RESEARCH — no production authority
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
        lifecycle_registry={mf.manifest_id: lc},
    )
    r = runner.run().result_for(ArchGateId.G21)
    assert r is not None and r.verdict is GateVerdict.FAIL
    assert "RESEARCH" in r.evidence


def test_g21_fail_lifecycle_in_shadow(
    uc_reg: UseCaseRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
    g21_prov: Provenance,
) -> None:
    """SHADOW can observe traffic but cannot take authoritative actions."""
    mf = _make_manifest()
    mf_reg = ManifestRegistry()
    mf_reg.add(mf)
    lc_dict = _make_lc_reg_with_state(mf, LifecycleState.SHADOW, uc_reg, g21_prov)
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
        lifecycle_registry=lc_dict,
    )
    r = runner.run().result_for(ArchGateId.G21)
    assert r is not None and r.verdict is GateVerdict.FAIL
    assert "SHADOW" in r.evidence


def test_g21_pass_lifecycle_in_production(
    uc_reg: UseCaseRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
    g21_prov: Provenance,
) -> None:
    """PRODUCTION lifecycle + ACTIVE registry = G21 PASS."""
    mf = _make_manifest()
    mf_reg = ManifestRegistry()
    mf_reg.add(mf)
    lc_dict = _make_lc_reg_with_state(mf, LifecycleState.PRODUCTION, uc_reg, g21_prov)
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
        lifecycle_registry=lc_dict,
    )
    r = runner.run().result_for(ArchGateId.G21)
    assert r is not None and r.verdict is GateVerdict.PASS


def test_g21_pass_lifecycle_in_pilot(
    uc_reg: UseCaseRegistry, pr_reg: ProviderRegistry, tool_cat: ToolCatalog,
    g21_prov: Provenance,
) -> None:
    """PILOT lifecycle also passes G21 (limited cohort, still production-authoritative)."""
    mf = _make_manifest()
    mf_reg = ManifestRegistry()
    mf_reg.add(mf)
    lc_dict = _make_lc_reg_with_state(mf, LifecycleState.PILOT, uc_reg, g21_prov)
    runner = ProductionGateRunner(
        use_case_registry=uc_reg, manifest_registry=mf_reg,
        tool_catalog=tool_cat, provider_registry=pr_reg,
        lifecycle_registry=lc_dict,
    )
    r = runner.run().result_for(ArchGateId.G21)
    assert r is not None and r.verdict is GateVerdict.PASS


# ---------------------------------------------------------------------------
# LifecycleAwareManifestRegistry integration
# ---------------------------------------------------------------------------


def test_lifecycle_aware_registry_research_not_production_ready(
    uc_reg: UseCaseRegistry,
) -> None:
    """A freshly added manifest is ACTIVE in the inner registry but NOT production-ready."""
    store = LifecycleAwareManifestRegistry()
    store.add(_make_manifest())
    assert store.production_ready_ids() == []
    assert store.lifecycle_count == 1


def test_lifecycle_aware_registry_production_ready_after_advance(
    uc_reg: UseCaseRegistry, g21_prov: Provenance,
) -> None:
    """production_ready_ids() only includes the manifest once lifecycle reaches PILOT+."""
    store = LifecycleAwareManifestRegistry()
    mf = _make_manifest()
    lc = store.add(mf)

    # Pre-production states: DESIGN, VALIDATION, SHADOW.
    pre_prod = [LifecycleState.DESIGN, LifecycleState.VALIDATION, LifecycleState.SHADOW]
    for state in pre_prod:
        lc.advance(uc_reg, g21_prov, "alice")
        assert lc.state is state
        assert mf.manifest_id not in store.production_ready_ids(), (
            f"manifest appeared in production_ready_ids() at state {lc.state}"
        )

    lc.advance(uc_reg, g21_prov, "alice")   # → PILOT (production-serving)
    assert lc.state is LifecycleState.PILOT
    assert mf.manifest_id in store.production_ready_ids()


def test_lifecycle_aware_registry_revoke_removes_from_ready(
    uc_reg: UseCaseRegistry, g21_prov: Provenance,
) -> None:
    """Revoking a manifest removes it from production_ready_ids()."""
    store = LifecycleAwareManifestRegistry()
    mf = _make_manifest()
    lc = store.add(mf)
    for _ in range(5):                       # → PRODUCTION
        lc.advance(uc_reg, g21_prov, "alice")
    assert mf.manifest_id in store.production_ready_ids()

    store.revoke(mf.manifest_id)
    assert mf.manifest_id not in store.production_ready_ids()


def test_lifecycle_aware_registry_lifecycle_for() -> None:
    store = LifecycleAwareManifestRegistry()
    mf = _make_manifest()
    lc = store.add(mf)
    assert store.lifecycle_for(mf.manifest_id) is lc
    assert store.lifecycle_for("nonexistent") is None


def test_lifecycle_aware_registry_duplicate_raises() -> None:
    from ztax_gateway.production_registries import ManifestRegistryError
    store = LifecycleAwareManifestRegistry()
    mf = _make_manifest()
    store.add(mf)
    with pytest.raises(ManifestRegistryError):
        store.add(mf)

