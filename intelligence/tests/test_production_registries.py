"""Tests for production_registries.py (Chapter 17 §14).

Coverage:
PromptProfile:
- happy path; as_dict; deprecated flag default False
- empty profile_id, owner, version, system_prompt raises PromptProfileError
- A5 authority_ceiling raises PromptProfileError

AgentProfile:
- happy path; as_dict; suspended default False
- empty profile_id, owner, version raises AgentProfileError
- max_steps < 1 raises AgentProfileError
- max_token_budget < 1 raises AgentProfileError
- None budgets are allowed

DatasetProfile:
- happy path; as_dict; deprecated default False
- empty dataset_id, owner, version, description, data_class raises
- case_count < 2 raises DatasetProfileError

EvaluationProfile:
- happy path; as_dict; adversarial_suite_required default True
- empty profile_id, owner, version, dataset_id raises
- min_accuracy out of [0,1] raises
- min_f1 out of [0,1] raises
- max_calibration_error out of [0,1] raises
- is_satisfied_by: True when above threshold
- is_satisfied_by: False when below accuracy threshold
- is_satisfied_by: False when f1 required and not met
- is_satisfied_by: True when f1 not required

build_manifest:
- happy path produces valid AIReleaseManifest
- manifest_id auto-generated when not supplied
- approved_at auto-stamped in UTC
- two calls produce different manifest_ids
- A5 authority_level raises AIReleaseManifestError
- empty allowed_regions raises AIReleaseManifestError
- empty ai_use_case_id raises AIReleaseManifestError
- empty release_version raises AIReleaseManifestError
- manifest_hash is 64-char hex SHA-256
- verify_hash() returns True on fresh manifest
- verify_hash() returns False after hash tamper (via object replacement test)
- as_dict() structure and types
- set allowed_regions converted to frozenset

ManifestRegistry:
- add() + status() returns ACTIVE
- add() duplicate raises ManifestRegistryError
- revoke() sets REVOKED; get() after revoke raises ManifestRegistryError
- supersede() sets SUPERSEDED; get() after supersede raises ManifestRegistryError
- get() unknown ID raises ManifestRegistryError
- get() governance gate fires first (GovernanceRefusedError for unknown use case)
- get() governance gate fires for killed use case
- get() returns manifest for active
- active_ids() returns only ACTIVE IDs
- ids_for_use_case() returns all statuses for that use case
- revoke() on unknown ID is silent (incident-safe)
"""

from __future__ import annotations

import re
from datetime import UTC, datetime

import pytest

from ztax_gateway.governance import GovernanceRefusedError, UseCase, UseCaseRegistry
from ztax_gateway.production_registries import (
    AgentMemoryPolicy,
    AgentProfile,
    AgentProfileError,
    AIReleaseManifest,
    AIReleaseManifestError,
    DatasetProfile,
    DatasetProfileError,
    EvaluationProfile,
    EvaluationProfileError,
    ManifestRegistry,
    ManifestRegistryError,
    ManifestStatus,
    PromptProfile,
    PromptProfileError,
    build_manifest,
)
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier

# ---------------------------------------------------------------------------
# Shared fixtures
# ---------------------------------------------------------------------------

_REGION = "eu-west-1"

_USE_CASE = UseCase(
    use_case_id="test-prod-reg",
    owner="lane-test",
    description="Test use case for production_registries tests.",
    max_risk_tier=RiskTier.T2,
    max_authority=AuthorityOutcome.A2,
    permitted_regions=frozenset({_REGION}),
)

_PROVENANCE = Provenance(
    use_case="test-prod-reg",
    model_profile="model:test@2026.09",
    provider_profile="provider:eu-hosted",
    prompt_profile="prompt:test@1",
    region=_REGION,
    data_class="INTERNAL",
    risk_tier=RiskTier.T1,
    authority_outcome=AuthorityOutcome.A1,
    ai_train_version="0.5.0",
)


@pytest.fixture()
def registry() -> UseCaseRegistry:
    return UseCaseRegistry([_USE_CASE])


@pytest.fixture()
def provenance() -> Provenance:
    return _PROVENANCE


def _make_manifest(**overrides: object) -> AIReleaseManifest:
    """Build a valid manifest, with optional field overrides."""
    defaults: dict[str, object] = dict(
        ai_use_case_id="test-prod-reg",
        release_version="1.0.0",
        authority_level=AuthorityOutcome.A1,
        risk_tier=RiskTier.T1,
        model_profile_id="model:test@2026.09",
        resolved_model_id="model-abc-pinned",
        prompt_profile_id="prompt:test@1",
        evaluation_profile_id="eval-profile-001",
        policy_bundle_id="policy-bundle-001",
        allowed_regions=frozenset({_REGION}),
    )
    defaults.update(overrides)
    return build_manifest(**defaults)  # type: ignore[arg-type]


# ---------------------------------------------------------------------------
# PromptProfile
# ---------------------------------------------------------------------------


class TestPromptProfile:
    def test_happy_path(self) -> None:
        pp = PromptProfile(
            profile_id="pp-001",
            owner="team-a",
            version="1.0.0",
            system_prompt="You are a tax classifier.",
            variable_schema={"sku": {"type": "string"}},
            output_schema={"class": {"type": "string"}},
            max_risk_tier=RiskTier.T1,
            authority_ceiling=AuthorityOutcome.A1,
            safety_rules=("Never output PII.",),
        )
        assert pp.profile_id == "pp-001"
        assert pp.deprecated is False

    def test_as_dict_keys(self) -> None:
        pp = PromptProfile(
            profile_id="pp-002",
            owner="team-b",
            version="2.0.0",
            system_prompt="Classify this product.",
            variable_schema={},
            output_schema={},
            max_risk_tier=RiskTier.T2,
            authority_ceiling=AuthorityOutcome.A2,
            safety_rules=(),
        )
        d = pp.as_dict()
        assert set(d.keys()) == {
            "profile_id", "owner", "version",
            "max_risk_tier", "authority_ceiling",
            "safety_rule_count", "deprecated",
        }
        assert d["safety_rule_count"] == 0

    def test_deprecated_default_is_false(self) -> None:
        pp = PromptProfile(
            profile_id="pp-003", owner="o", version="1.0",
            system_prompt="s", variable_schema={}, output_schema={},
            max_risk_tier=RiskTier.T0, authority_ceiling=AuthorityOutcome.A0,
            safety_rules=(),
        )
        assert pp.deprecated is False

    def test_empty_profile_id_raises(self) -> None:
        with pytest.raises(PromptProfileError) as exc:
            PromptProfile(
                profile_id="", owner="o", version="1.0",
                system_prompt="s", variable_schema={}, output_schema={},
                max_risk_tier=RiskTier.T0, authority_ceiling=AuthorityOutcome.A0,
                safety_rules=(),
            )
        assert "profile_id" in exc.value.reason

    def test_empty_owner_raises(self) -> None:
        with pytest.raises(PromptProfileError) as exc:
            PromptProfile(
                profile_id="pp-x", owner="", version="1.0",
                system_prompt="s", variable_schema={}, output_schema={},
                max_risk_tier=RiskTier.T0, authority_ceiling=AuthorityOutcome.A0,
                safety_rules=(),
            )
        assert "owner" in exc.value.reason

    def test_empty_version_raises(self) -> None:
        with pytest.raises(PromptProfileError) as exc:
            PromptProfile(
                profile_id="pp-x", owner="o", version="",
                system_prompt="s", variable_schema={}, output_schema={},
                max_risk_tier=RiskTier.T0, authority_ceiling=AuthorityOutcome.A0,
                safety_rules=(),
            )
        assert "version" in exc.value.reason

    def test_empty_system_prompt_raises(self) -> None:
        with pytest.raises(PromptProfileError) as exc:
            PromptProfile(
                profile_id="pp-x", owner="o", version="1.0",
                system_prompt="   ", variable_schema={}, output_schema={},
                max_risk_tier=RiskTier.T0, authority_ceiling=AuthorityOutcome.A0,
                safety_rules=(),
            )
        assert "system_prompt" in exc.value.reason

    def test_a5_authority_raises(self) -> None:
        with pytest.raises(PromptProfileError) as exc:
            PromptProfile(
                profile_id="pp-x", owner="o", version="1.0",
                system_prompt="s", variable_schema={}, output_schema={},
                max_risk_tier=RiskTier.T0, authority_ceiling=AuthorityOutcome.A5,
                safety_rules=(),
            )
        assert "A5" in exc.value.reason

    def test_immutable(self) -> None:
        pp = PromptProfile(
            profile_id="pp-imm", owner="o", version="1.0",
            system_prompt="s", variable_schema={}, output_schema={},
            max_risk_tier=RiskTier.T0, authority_ceiling=AuthorityOutcome.A0,
            safety_rules=(),
        )
        with pytest.raises((AttributeError, TypeError)):
            pp.deprecated = True  # type: ignore[misc]


# ---------------------------------------------------------------------------
# AgentProfile
# ---------------------------------------------------------------------------


class TestAgentProfile:
    def test_happy_path(self) -> None:
        ap = AgentProfile(
            profile_id="agent-001",
            owner="team-c",
            version="1.0.0",
            allowed_tool_ids=frozenset({"tool-a", "tool-b"}),
            memory_policy=AgentMemoryPolicy.SESSION,
            max_steps=50,
            max_token_budget=10000,
        )
        assert ap.profile_id == "agent-001"
        assert ap.suspended is False

    def test_as_dict_keys(self) -> None:
        ap = AgentProfile(
            profile_id="agent-002", owner="o", version="1.0",
            allowed_tool_ids=frozenset({"t1"}),
            memory_policy=AgentMemoryPolicy.STATELESS,
            max_steps=None, max_token_budget=None,
        )
        d = ap.as_dict()
        assert "allowed_tool_count" in d
        assert d["allowed_tool_count"] == 1
        assert d["max_steps"] is None

    def test_none_budgets_allowed(self) -> None:
        ap = AgentProfile(
            profile_id="agent-003", owner="o", version="1.0",
            allowed_tool_ids=frozenset(),
            memory_policy=AgentMemoryPolicy.STATELESS,
            max_steps=None, max_token_budget=None,
        )
        assert ap.max_steps is None
        assert ap.max_token_budget is None

    def test_empty_profile_id_raises(self) -> None:
        with pytest.raises(AgentProfileError) as exc:
            AgentProfile(
                profile_id="", owner="o", version="1.0",
                allowed_tool_ids=frozenset(),
                memory_policy=AgentMemoryPolicy.STATELESS,
                max_steps=None, max_token_budget=None,
            )
        assert "profile_id" in exc.value.reason

    def test_empty_owner_raises(self) -> None:
        with pytest.raises(AgentProfileError) as exc:
            AgentProfile(
                profile_id="a-x", owner="", version="1.0",
                allowed_tool_ids=frozenset(),
                memory_policy=AgentMemoryPolicy.STATELESS,
                max_steps=None, max_token_budget=None,
            )
        assert "owner" in exc.value.reason

    def test_empty_version_raises(self) -> None:
        with pytest.raises(AgentProfileError) as exc:
            AgentProfile(
                profile_id="a-x", owner="o", version="",
                allowed_tool_ids=frozenset(),
                memory_policy=AgentMemoryPolicy.STATELESS,
                max_steps=None, max_token_budget=None,
            )
        assert "version" in exc.value.reason

    def test_max_steps_zero_raises(self) -> None:
        with pytest.raises(AgentProfileError) as exc:
            AgentProfile(
                profile_id="a-x", owner="o", version="1.0",
                allowed_tool_ids=frozenset(),
                memory_policy=AgentMemoryPolicy.STATELESS,
                max_steps=0, max_token_budget=None,
            )
        assert "max_steps" in exc.value.reason

    def test_max_token_budget_zero_raises(self) -> None:
        with pytest.raises(AgentProfileError) as exc:
            AgentProfile(
                profile_id="a-x", owner="o", version="1.0",
                allowed_tool_ids=frozenset(),
                memory_policy=AgentMemoryPolicy.STATELESS,
                max_steps=None, max_token_budget=0,
            )
        assert "max_token_budget" in exc.value.reason

    def test_immutable(self) -> None:
        ap = AgentProfile(
            profile_id="a-imm", owner="o", version="1.0",
            allowed_tool_ids=frozenset(),
            memory_policy=AgentMemoryPolicy.STATELESS,
            max_steps=None, max_token_budget=None,
        )
        with pytest.raises((AttributeError, TypeError)):
            ap.suspended = True  # type: ignore[misc]


# ---------------------------------------------------------------------------
# DatasetProfile
# ---------------------------------------------------------------------------


class TestDatasetProfile:
    def test_happy_path(self) -> None:
        dp = DatasetProfile(
            dataset_id="ds-001",
            owner="team-d",
            version="1.0.0",
            description="Telecom SKU gold set.",
            data_class="INTERNAL",
            case_count=50,
        )
        assert dp.dataset_id == "ds-001"
        assert dp.deprecated is False

    def test_as_dict_keys(self) -> None:
        dp = DatasetProfile(
            dataset_id="ds-002", owner="o", version="1.0",
            description="desc", data_class="PUBLIC", case_count=10,
        )
        d = dp.as_dict()
        assert "dataset_id" in d
        assert d["case_count"] == 10

    def test_empty_dataset_id_raises(self) -> None:
        with pytest.raises(DatasetProfileError) as exc:
            DatasetProfile(
                dataset_id="", owner="o", version="1.0",
                description="d", data_class="I", case_count=5,
            )
        assert "dataset_id" in exc.value.reason

    def test_empty_owner_raises(self) -> None:
        with pytest.raises(DatasetProfileError) as exc:
            DatasetProfile(
                dataset_id="ds-x", owner="", version="1.0",
                description="d", data_class="I", case_count=5,
            )
        assert "owner" in exc.value.reason

    def test_empty_version_raises(self) -> None:
        with pytest.raises(DatasetProfileError) as exc:
            DatasetProfile(
                dataset_id="ds-x", owner="o", version="",
                description="d", data_class="I", case_count=5,
            )
        assert "version" in exc.value.reason

    def test_empty_description_raises(self) -> None:
        with pytest.raises(DatasetProfileError) as exc:
            DatasetProfile(
                dataset_id="ds-x", owner="o", version="1.0",
                description="  ", data_class="I", case_count=5,
            )
        assert "description" in exc.value.reason

    def test_empty_data_class_raises(self) -> None:
        with pytest.raises(DatasetProfileError) as exc:
            DatasetProfile(
                dataset_id="ds-x", owner="o", version="1.0",
                description="d", data_class="  ", case_count=5,
            )
        assert "data_class" in exc.value.reason

    def test_case_count_one_raises(self) -> None:
        with pytest.raises(DatasetProfileError) as exc:
            DatasetProfile(
                dataset_id="ds-x", owner="o", version="1.0",
                description="d", data_class="I", case_count=1,
            )
        assert "case_count" in exc.value.reason

    def test_case_count_zero_raises(self) -> None:
        with pytest.raises(DatasetProfileError):
            DatasetProfile(
                dataset_id="ds-x", owner="o", version="1.0",
                description="d", data_class="I", case_count=0,
            )

    def test_case_count_two_is_ok(self) -> None:
        dp = DatasetProfile(
            dataset_id="ds-min", owner="o", version="1.0",
            description="d", data_class="I", case_count=2,
        )
        assert dp.case_count == 2


# ---------------------------------------------------------------------------
# EvaluationProfile
# ---------------------------------------------------------------------------


class TestEvaluationProfile:
    def test_happy_path(self) -> None:
        ep = EvaluationProfile(
            profile_id="ep-001",
            owner="team-e",
            version="1.0.0",
            dataset_id="ds-001",
            min_accuracy=0.85,
            min_f1=0.80,
            max_calibration_error=0.05,
        )
        assert ep.profile_id == "ep-001"
        assert ep.adversarial_suite_required is True

    def test_as_dict_keys(self) -> None:
        ep = EvaluationProfile(
            profile_id="ep-002", owner="o", version="1.0",
            dataset_id="ds-001", min_accuracy=0.9,
            min_f1=None, max_calibration_error=None,
        )
        d = ep.as_dict()
        assert "min_accuracy" in d
        assert d["min_f1"] is None

    def test_adversarial_suite_required_default_true(self) -> None:
        ep = EvaluationProfile(
            profile_id="ep-003", owner="o", version="1.0",
            dataset_id="ds-001", min_accuracy=0.5,
            min_f1=None, max_calibration_error=None,
        )
        assert ep.adversarial_suite_required is True

    def test_empty_profile_id_raises(self) -> None:
        with pytest.raises(EvaluationProfileError) as exc:
            EvaluationProfile(
                profile_id="", owner="o", version="1.0",
                dataset_id="d", min_accuracy=0.5,
                min_f1=None, max_calibration_error=None,
            )
        assert "profile_id" in exc.value.reason

    def test_empty_dataset_id_raises(self) -> None:
        with pytest.raises(EvaluationProfileError) as exc:
            EvaluationProfile(
                profile_id="ep-x", owner="o", version="1.0",
                dataset_id="", min_accuracy=0.5,
                min_f1=None, max_calibration_error=None,
            )
        assert "dataset_id" in exc.value.reason

    def test_min_accuracy_negative_raises(self) -> None:
        with pytest.raises(EvaluationProfileError) as exc:
            EvaluationProfile(
                profile_id="ep-x", owner="o", version="1.0",
                dataset_id="d", min_accuracy=-0.1,
                min_f1=None, max_calibration_error=None,
            )
        assert "min_accuracy" in exc.value.reason

    def test_min_accuracy_above_one_raises(self) -> None:
        with pytest.raises(EvaluationProfileError):
            EvaluationProfile(
                profile_id="ep-x", owner="o", version="1.0",
                dataset_id="d", min_accuracy=1.01,
                min_f1=None, max_calibration_error=None,
            )

    def test_min_f1_out_of_range_raises(self) -> None:
        with pytest.raises(EvaluationProfileError) as exc:
            EvaluationProfile(
                profile_id="ep-x", owner="o", version="1.0",
                dataset_id="d", min_accuracy=0.5,
                min_f1=1.5, max_calibration_error=None,
            )
        assert "min_f1" in exc.value.reason

    def test_max_calibration_error_out_of_range_raises(self) -> None:
        with pytest.raises(EvaluationProfileError) as exc:
            EvaluationProfile(
                profile_id="ep-x", owner="o", version="1.0",
                dataset_id="d", min_accuracy=0.5,
                min_f1=None, max_calibration_error=-0.01,
            )
        assert "max_calibration_error" in exc.value.reason

    def test_is_satisfied_by_above_threshold(self) -> None:
        ep = EvaluationProfile(
            profile_id="ep-sat", owner="o", version="1.0",
            dataset_id="d", min_accuracy=0.80,
            min_f1=0.75, max_calibration_error=None,
        )
        assert ep.is_satisfied_by(accuracy=0.90, f1=0.80) is True

    def test_is_satisfied_by_below_accuracy(self) -> None:
        ep = EvaluationProfile(
            profile_id="ep-sat", owner="o", version="1.0",
            dataset_id="d", min_accuracy=0.85,
            min_f1=None, max_calibration_error=None,
        )
        assert ep.is_satisfied_by(accuracy=0.80) is False

    def test_is_satisfied_by_f1_required_not_provided(self) -> None:
        ep = EvaluationProfile(
            profile_id="ep-sat", owner="o", version="1.0",
            dataset_id="d", min_accuracy=0.80,
            min_f1=0.75, max_calibration_error=None,
        )
        assert ep.is_satisfied_by(accuracy=0.90, f1=None) is False

    def test_is_satisfied_by_f1_required_below_threshold(self) -> None:
        ep = EvaluationProfile(
            profile_id="ep-sat", owner="o", version="1.0",
            dataset_id="d", min_accuracy=0.80,
            min_f1=0.75, max_calibration_error=None,
        )
        assert ep.is_satisfied_by(accuracy=0.90, f1=0.70) is False

    def test_is_satisfied_by_no_f1_required(self) -> None:
        ep = EvaluationProfile(
            profile_id="ep-sat", owner="o", version="1.0",
            dataset_id="d", min_accuracy=0.80,
            min_f1=None, max_calibration_error=None,
        )
        assert ep.is_satisfied_by(accuracy=0.90) is True


# ---------------------------------------------------------------------------
# build_manifest / AIReleaseManifest
# ---------------------------------------------------------------------------


class TestBuildManifest:
    def test_happy_path(self) -> None:
        m = _make_manifest()
        assert isinstance(m, AIReleaseManifest)
        assert m.ai_use_case_id == "test-prod-reg"

    def test_manifest_id_auto_generated(self) -> None:
        m = _make_manifest()
        assert m.manifest_id
        assert len(m.manifest_id) == 16

    def test_explicit_manifest_id_used(self) -> None:
        m = _make_manifest(manifest_id="my-manifest-001")
        assert m.manifest_id == "my-manifest-001"

    def test_approved_at_auto_stamped_utc(self) -> None:
        before = datetime.now(tz=UTC)
        m = _make_manifest()
        after = datetime.now(tz=UTC)
        assert before <= m.approved_at <= after
        assert m.approved_at.tzinfo is UTC

    def test_explicit_approved_at_used(self) -> None:
        ts = datetime(2026, 1, 1, 12, 0, 0, tzinfo=UTC)
        m = _make_manifest(approved_at=ts)
        assert m.approved_at == ts

    def test_two_calls_produce_different_manifest_ids(self) -> None:
        m1 = _make_manifest()
        m2 = _make_manifest()
        assert m1.manifest_id != m2.manifest_id

    def test_manifest_hash_is_sha256_hex(self) -> None:
        m = _make_manifest()
        assert re.fullmatch(r"[0-9a-f]{64}", m.manifest_hash) is not None

    def test_verify_hash_true_on_fresh_manifest(self) -> None:
        m = _make_manifest()
        assert m.verify_hash() is True

    def test_a5_authority_raises(self) -> None:
        with pytest.raises(AIReleaseManifestError) as exc:
            _make_manifest(authority_level=AuthorityOutcome.A5)
        assert "A5" in exc.value.reason

    def test_empty_allowed_regions_raises(self) -> None:
        with pytest.raises(AIReleaseManifestError) as exc:
            _make_manifest(allowed_regions=frozenset())
        assert "allowed_regions" in exc.value.reason

    def test_empty_ai_use_case_id_raises(self) -> None:
        with pytest.raises(AIReleaseManifestError) as exc:
            _make_manifest(ai_use_case_id="")
        assert "ai_use_case_id" in exc.value.reason

    def test_empty_release_version_raises(self) -> None:
        with pytest.raises(AIReleaseManifestError) as exc:
            _make_manifest(release_version="")
        assert "release_version" in exc.value.reason

    def test_set_allowed_regions_converted_to_frozenset(self) -> None:
        m = build_manifest(
            ai_use_case_id="test-prod-reg",
            release_version="1.0.0",
            authority_level=AuthorityOutcome.A1,
            risk_tier=RiskTier.T1,
            model_profile_id="m",
            resolved_model_id="m-pinned",
            prompt_profile_id="p",
            evaluation_profile_id="e",
            policy_bundle_id="pb",
            allowed_regions={"eu-west-1", "eu-west-2"},
        )
        assert isinstance(m.allowed_regions, frozenset)
        assert "eu-west-1" in m.allowed_regions

    def test_optional_fields_default_none(self) -> None:
        m = _make_manifest()
        assert m.agent_profile_id is None
        assert m.tool_policy_id is None
        assert m.retrieval_corpus_version is None
        assert m.embedding_model_version is None
        assert m.fallback_profile_id is None

    def test_optional_fields_set(self) -> None:
        m = _make_manifest(
            agent_profile_id="agent-001",
            tool_policy_id="policy-001",
            retrieval_corpus_version="v1.2",
            embedding_model_version="emb-v3",
            fallback_profile_id="fallback-001",
        )
        assert m.agent_profile_id == "agent-001"
        assert m.tool_policy_id == "policy-001"
        assert m.retrieval_corpus_version == "v1.2"
        assert m.embedding_model_version == "emb-v3"
        assert m.fallback_profile_id == "fallback-001"

    def test_as_dict_structure(self) -> None:
        m = _make_manifest()
        d = m.as_dict()
        expected_keys = {
            "manifest_id", "release_version", "ai_use_case_id",
            "authority_level", "risk_tier", "model_profile_id",
            "resolved_model_id", "prompt_profile_id", "evaluation_profile_id",
            "policy_bundle_id", "allowed_regions", "approved_at",
            "agent_profile_id", "tool_policy_id", "retrieval_corpus_version",
            "embedding_model_version", "fallback_profile_id", "manifest_hash",
        }
        assert set(d.keys()) == expected_keys
        assert isinstance(d["allowed_regions"], list)
        assert isinstance(d["approved_at"], str)
        assert isinstance(d["manifest_hash"], str)

    def test_as_dict_approved_at_is_isoformat(self) -> None:
        m = _make_manifest()
        parsed = datetime.fromisoformat(m.as_dict()["approved_at"])  # type: ignore[arg-type]
        assert parsed.tzinfo is not None

    def test_manifest_immutable(self) -> None:
        m = _make_manifest()
        with pytest.raises((AttributeError, TypeError)):
            m.release_version = "9.9.9"  # type: ignore[misc]

    def test_different_fields_produce_different_hashes(self) -> None:
        m1 = _make_manifest(release_version="1.0.0")
        m2 = _make_manifest(release_version="2.0.0")
        assert m1.manifest_hash != m2.manifest_hash

    def test_same_fields_same_hash(self) -> None:
        ts = datetime(2026, 6, 1, 0, 0, 0, tzinfo=UTC)
        m1 = _make_manifest(manifest_id="fixed-id", approved_at=ts)
        m2 = _make_manifest(manifest_id="fixed-id", approved_at=ts)
        assert m1.manifest_hash == m2.manifest_hash


# ---------------------------------------------------------------------------
# ManifestRegistry
# ---------------------------------------------------------------------------


class TestManifestRegistry:
    def test_add_and_status_active(self) -> None:
        reg = ManifestRegistry()
        m = _make_manifest()
        reg.add(m)
        assert reg.status(m.manifest_id) is ManifestStatus.ACTIVE

    def test_add_duplicate_raises(self) -> None:
        reg = ManifestRegistry()
        m = _make_manifest(manifest_id="dup-001")
        reg.add(m)
        with pytest.raises(ManifestRegistryError) as exc:
            reg.add(m)
        assert "dup-001" in exc.value.reason

    def test_revoke_sets_revoked(self) -> None:
        reg = ManifestRegistry()
        m = _make_manifest()
        reg.add(m)
        reg.revoke(m.manifest_id)
        assert reg.status(m.manifest_id) is ManifestStatus.REVOKED

    def test_get_after_revoke_raises(
        self, registry: UseCaseRegistry, provenance: Provenance
    ) -> None:
        man_reg = ManifestRegistry()
        m = _make_manifest()
        man_reg.add(m)
        man_reg.revoke(m.manifest_id)
        with pytest.raises(ManifestRegistryError) as exc:
            man_reg.get(m.manifest_id, registry, provenance)
        assert "revoked" in exc.value.reason.lower()

    def test_supersede_sets_superseded(self) -> None:
        reg = ManifestRegistry()
        m = _make_manifest()
        reg.add(m)
        reg.supersede(m.manifest_id)
        assert reg.status(m.manifest_id) is ManifestStatus.SUPERSEDED

    def test_get_after_supersede_raises(
        self, registry: UseCaseRegistry, provenance: Provenance
    ) -> None:
        man_reg = ManifestRegistry()
        m = _make_manifest()
        man_reg.add(m)
        man_reg.supersede(m.manifest_id)
        with pytest.raises(ManifestRegistryError) as exc:
            man_reg.get(m.manifest_id, registry, provenance)
        assert "superseded" in exc.value.reason.lower()

    def test_get_unknown_id_raises(
        self, registry: UseCaseRegistry, provenance: Provenance
    ) -> None:
        man_reg = ManifestRegistry()
        with pytest.raises(ManifestRegistryError) as exc:
            man_reg.get("no-such-id", registry, provenance)
        assert "no-such-id" in exc.value.reason

    def test_get_governance_gate_fires_for_unknown_use_case(self) -> None:
        man_reg = ManifestRegistry()
        m = _make_manifest()
        man_reg.add(m)
        empty_registry = UseCaseRegistry([])
        with pytest.raises(GovernanceRefusedError):
            man_reg.get(m.manifest_id, empty_registry, _PROVENANCE)

    def test_get_governance_gate_fires_for_killed_use_case(self) -> None:
        man_reg = ManifestRegistry()
        m = _make_manifest()
        man_reg.add(m)
        reg = UseCaseRegistry([_USE_CASE])
        reg.kill("test-prod-reg")
        with pytest.raises(GovernanceRefusedError):
            man_reg.get(m.manifest_id, reg, _PROVENANCE)

    def test_get_returns_active_manifest(
        self, registry: UseCaseRegistry, provenance: Provenance
    ) -> None:
        man_reg = ManifestRegistry()
        m = _make_manifest()
        man_reg.add(m)
        result = man_reg.get(m.manifest_id, registry, provenance)
        assert result is m

    def test_revoke_unknown_id_is_silent(self) -> None:
        reg = ManifestRegistry()
        reg.revoke("totally-unknown-id")  # must not raise
        assert reg.status("totally-unknown-id") is ManifestStatus.REVOKED

    def test_active_ids_returns_only_active(self) -> None:
        reg = ManifestRegistry()
        m1 = _make_manifest(manifest_id="m-active-001")
        m2 = _make_manifest(manifest_id="m-revoked-001")
        m3 = _make_manifest(manifest_id="m-active-002")
        reg.add(m1)
        reg.add(m2)
        reg.add(m3)
        reg.revoke(m2.manifest_id)
        active = reg.active_ids()
        assert "m-active-001" in active
        assert "m-active-002" in active
        assert "m-revoked-001" not in active

    def test_ids_for_use_case_all_statuses(self) -> None:
        reg = ManifestRegistry()
        m1 = _make_manifest(manifest_id="uc-m1", ai_use_case_id="test-prod-reg")
        m2 = _make_manifest(manifest_id="uc-m2", ai_use_case_id="test-prod-reg")
        other = _make_manifest(manifest_id="other-m", ai_use_case_id="other-uc")
        reg.add(m1)
        reg.add(m2)
        reg.add(other)
        reg.revoke(m1.manifest_id)
        ids = reg.ids_for_use_case("test-prod-reg")
        assert "uc-m1" in ids
        assert "uc-m2" in ids
        assert "other-m" not in ids

    def test_status_unknown_id_returns_none(self) -> None:
        reg = ManifestRegistry()
        assert reg.status("unknown") is None

    def test_supersede_unknown_id_is_noop(self) -> None:
        reg = ManifestRegistry()
        reg.supersede("totally-unknown")  # must not raise
        assert reg.status("totally-unknown") is None


# ---------------------------------------------------------------------------
# AgentMemoryPolicy enum
# ---------------------------------------------------------------------------


class TestAgentMemoryPolicy:
    def test_values(self) -> None:
        assert AgentMemoryPolicy.STATELESS == "STATELESS"
        assert AgentMemoryPolicy.SESSION == "SESSION"
        assert AgentMemoryPolicy.PERSISTENT == "PERSISTENT"

    def test_identity(self) -> None:
        assert AgentMemoryPolicy("STATELESS") is AgentMemoryPolicy.STATELESS


# ---------------------------------------------------------------------------
# ManifestStatus enum
# ---------------------------------------------------------------------------


class TestManifestStatus:
    def test_values(self) -> None:
        assert ManifestStatus.ACTIVE == "ACTIVE"
        assert ManifestStatus.REVOKED == "REVOKED"
        assert ManifestStatus.SUPERSEDED == "SUPERSEDED"
