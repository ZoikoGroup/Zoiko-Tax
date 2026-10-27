"""REQ-0023, REQ-0024, REQ-0003, REQ-0004 -- declaration requirements.

REQ-0023: "Prompt templates/system instructions MUST be versioned for
    material use cases." Enforced by PromptProfile.__post_init__
    (production_registries.py:333-335): an empty version raises
    PromptProfileError, and the dataclass field has no default, so a
    PromptProfile cannot be built without stating one.

REQ-0024: "Agent configurations/tool policies MUST be versioned." Enforced
    the same way by AgentProfile.__post_init__ (production_registries.py:
    405-407).

REQ-0003: "Every AIUseCase MUST declare an authority level A0-A5." The
    UseCase dataclass has no default for max_authority (governance.py), so
    construction is impossible without it. The real "or registered" half of
    the requirement is enforced at the config-load boundary,
    server.py::load_config: a use case entry missing "max_authority" raises
    KeyError, and a value outside the AuthorityOutcome vocabulary raises
    ValueError from the enum constructor itself.

REQ-0004: "Every AIUseCase MUST declare an internal risk tier T0-T4." Same
    pattern as REQ-0003, via max_risk_tier and RiskTier.

What is NOT re-tested here (already covered elsewhere, not duplicated):
    - test_production_registries.py::TestPromptProfile::test_empty_version_raises
      and ::TestAgentProfile::test_empty_version_raises already prove the
      empty-version behaviour; this file adds the same proof under the
      requirement's own name for traceability, plus the dataclass-field
      structural check.
    - test_governance_declarations.py already proves max_authority/
      max_risk_tier are required dataclass fields and that the *wire
      decoder* (decode_call) rejects an invalid authority/tier string. This
      file instead proves the *config-load* boundary (load_config) rejects a
      missing or invalid declaration -- a different code path, not a
      restatement of the wire-level test.

Self-contained: helpers are copied from test_t4_prohibition.py's pattern (a
minimal load_config() JSON fixture); no cross-test-module imports.
"""

from __future__ import annotations

import dataclasses
import json
from pathlib import Path
from typing import Any

import pytest

from ztax_gateway.governance import UseCase
from ztax_gateway.production_registries import (
    AgentMemoryPolicy,
    AgentProfile,
    AgentProfileError,
    PromptProfile,
    PromptProfileError,
)
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.server import load_config

# ---------------------------------------------------------------------------
# REQ-0023: PromptProfile cannot be created without a version
# ---------------------------------------------------------------------------


class TestPromptProfileVersionRequired:
    """REQ-0023: prompt templates MUST be versioned for material use cases."""

    def test_version_is_a_required_field_with_no_default(self) -> None:
        """REQ-0023: PromptProfile.version has no default -- it must be supplied."""
        field = PromptProfile.__dataclass_fields__["version"]
        assert field.default is dataclasses.MISSING
        assert field.default_factory is dataclasses.MISSING

    def test_empty_version_raises_prompt_profile_error(self) -> None:
        """REQ-0023: an empty version string is refused at construction."""
        with pytest.raises(PromptProfileError) as exc:
            PromptProfile(
                profile_id="pp-req-0023",
                owner="lane-l",
                version="",
                system_prompt="You are a tax classifier.",
                variable_schema={},
                output_schema={},
                max_risk_tier=RiskTier.T1,
                authority_ceiling=AuthorityOutcome.A1,
                safety_rules=(),
            )
        assert "version" in exc.value.reason


# ---------------------------------------------------------------------------
# REQ-0024: AgentProfile cannot be created without a version
# ---------------------------------------------------------------------------


class TestAgentProfileVersionRequired:
    """REQ-0024: agent configurations/tool policies MUST be versioned."""

    def test_version_is_a_required_field_with_no_default(self) -> None:
        """REQ-0024: AgentProfile.version has no default -- it must be supplied."""
        field = AgentProfile.__dataclass_fields__["version"]
        assert field.default is dataclasses.MISSING
        assert field.default_factory is dataclasses.MISSING

    def test_empty_version_raises_agent_profile_error(self) -> None:
        """REQ-0024: an empty version string is refused at construction."""
        with pytest.raises(AgentProfileError) as exc:
            AgentProfile(
                profile_id="agent-req-0024",
                owner="lane-l",
                version="",
                allowed_tool_ids=frozenset(),
                memory_policy=AgentMemoryPolicy.STATELESS,
                max_steps=None,
                max_token_budget=None,
            )
        assert "version" in exc.value.reason


# ---------------------------------------------------------------------------
# Shared load_config() fixture helper for REQ-0003 / REQ-0004
# ---------------------------------------------------------------------------

_BASE_ENTRY: dict[str, Any] = {
    "use_case_id": "uc-req-decl-test",
    "owner": "lane-l",
    "description": "REQ-0003/0004 declaration test use case",
    "max_risk_tier": "T1",
    "max_authority": "A1",
    "permitted_regions": ["euc1-dev-01"],
    "permitted_data_classes": ["P0"],
    "route": {
        "model_profile": "model:cls",
        "provider_profile": "provider:a",
        "prompt_profile": "prompt:cls-v4",
    },
}


def _config(
    tmp_path: Path,
    *,
    remove: str | None = None,
    override: dict[str, Any] | None = None,
) -> Path:
    """Write a minimal load_config() JSON fixture, optionally missing or
    overriding one key of the single use-case entry."""
    entry = dict(_BASE_ENTRY)
    if remove is not None:
        del entry[remove]
    if override is not None:
        entry.update(override)
    doc = {"use_cases": [entry]}
    path = tmp_path / "config.json"
    path.write_text(json.dumps(doc), encoding="utf-8")
    return path


# ---------------------------------------------------------------------------
# REQ-0003: an AIUseCase cannot be created or registered without an
# authority level, and an invalid value is rejected.
# ---------------------------------------------------------------------------


class TestUseCaseAuthorityDeclaration:
    """REQ-0003: every AIUseCase MUST declare an authority level A0-A5."""

    def test_max_authority_is_a_required_field_with_no_default(self) -> None:
        """REQ-0003: UseCase.max_authority has no default -- construction is
        impossible without declaring it."""
        field = UseCase.__dataclass_fields__["max_authority"]
        assert field.default is dataclasses.MISSING
        assert field.default_factory is dataclasses.MISSING

    def test_load_config_missing_max_authority_raises(self, tmp_path: Path) -> None:
        """REQ-0003: a use case entry with no 'max_authority' key cannot be
        registered via load_config()."""
        path = _config(tmp_path, remove="max_authority")
        with pytest.raises(KeyError, match="max_authority"):
            load_config(path)

    def test_load_config_invalid_max_authority_value_raises(self, tmp_path: Path) -> None:
        """REQ-0003: a value outside the A0-A5 vocabulary is rejected."""
        path = _config(tmp_path, override={"max_authority": "A9"})
        with pytest.raises(ValueError, match="A9"):
            load_config(path)


# ---------------------------------------------------------------------------
# REQ-0004: an AIUseCase cannot be created or registered without a risk
# tier, and an invalid value is rejected.
# ---------------------------------------------------------------------------


class TestUseCaseRiskTierDeclaration:
    """REQ-0004: every AIUseCase MUST declare an internal risk tier T0-T4."""

    def test_max_risk_tier_is_a_required_field_with_no_default(self) -> None:
        """REQ-0004: UseCase.max_risk_tier has no default -- construction is
        impossible without declaring it."""
        field = UseCase.__dataclass_fields__["max_risk_tier"]
        assert field.default is dataclasses.MISSING
        assert field.default_factory is dataclasses.MISSING

    def test_load_config_missing_max_risk_tier_raises(self, tmp_path: Path) -> None:
        """REQ-0004: a use case entry with no 'max_risk_tier' key cannot be
        registered via load_config()."""
        path = _config(tmp_path, remove="max_risk_tier")
        with pytest.raises(KeyError, match="max_risk_tier"):
            load_config(path)

    def test_load_config_invalid_max_risk_tier_value_raises(self, tmp_path: Path) -> None:
        """REQ-0004: a value outside the T0-T4 vocabulary is rejected.

        Deliberately not 'T4' itself: T4 is a *valid* vocabulary member that
        load_config() separately refuses for a business reason (REQ-0107,
        see test_t4_prohibition.py). This test isolates the vocabulary check.
        """
        path = _config(tmp_path, override={"max_risk_tier": "LOW"})
        with pytest.raises(ValueError, match="LOW"):
            load_config(path)
