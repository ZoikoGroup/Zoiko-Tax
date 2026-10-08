"""Tests for runtime selection (_runtime()) and suspended-entry handling.

Covers:
  - Default ZTAX_GATEWAY_RUNTIME (unset) -> UnconfiguredRuntime.
  - ZTAX_GATEWAY_RUNTIME=unconfigured -> UnconfiguredRuntime.
  - ZTAX_GATEWAY_RUNTIME=fake + ZTAX_ENVIRONMENT=development -> FakeRuntime.
  - ZTAX_GATEWAY_RUNTIME=fake outside development -> SystemExit(2).
  - Unknown ZTAX_GATEWAY_RUNTIME value -> SystemExit(2).
  - load_config threads "suspended": true into the registry so the Gateway
    refuses the call with AI_USE_CASE_SUSPENDED rather than AI_UNKNOWN_USE_CASE.
"""

from __future__ import annotations

import contextlib
import json
import os
from collections.abc import Generator
from pathlib import Path

import grpc
import pytest

from ztax_gateway.fake_runtime import FakeRuntime
from ztax_gateway.runtime import UnconfiguredRuntime
from ztax_gateway.server import GatewayError, _runtime, load_config

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_MINIMAL_REGISTRY: dict[str, object] = {
    "use_cases": [
        {
            "use_case_id": "classification-review",
            "owner": "lane-l",
            "description": "test",
            "max_risk_tier": "T2",
            "max_authority": "A1",
            "permitted_regions": ["local"],
            "permitted_data_classes": ["P0"],
            "suspended": False,
            "route": {
                "model_profile": "model:fake-v1",
                "provider_profile": "provider:fake",
                "prompt_profile": "prompt:classification-review-v1",
            },
        }
    ],
    "killed": [],
    "global_kill": False,
}

_SUSPENDED_REGISTRY: dict[str, object] = {
    "use_cases": [
        {
            "use_case_id": "classification-review",
            "owner": "lane-l",
            "description": "test",
            "max_risk_tier": "T2",
            "max_authority": "A1",
            "permitted_regions": ["local"],
            "permitted_data_classes": ["P0"],
            "suspended": True,
            "route": {
                "model_profile": "model:fake-v1",
                "provider_profile": "provider:fake",
                "prompt_profile": "prompt:classification-review-v1",
            },
        }
    ],
    "killed": [],
    "global_kill": False,
}


def _write_registry(doc: dict[str, object], tmp_path: Path) -> Path:
    p = tmp_path / "registry.json"
    p.write_text(json.dumps(doc), encoding="utf-8")
    return p


@contextlib.contextmanager
def _clean_env(*names: str) -> Generator[None, None, None]:
    """Remove env vars for the duration of the block, then restore them."""
    saved = {n: os.environ.pop(n, None) for n in names}
    try:
        yield
    finally:
        for n, v in saved.items():
            if v is None:
                os.environ.pop(n, None)
            else:
                os.environ[n] = v


# ---------------------------------------------------------------------------
# _runtime() — default behaviour (no env vars set)
# ---------------------------------------------------------------------------


class TestRuntimeDefault:
    def test_no_env_var_returns_unconfigured(self) -> None:
        """With no ZTAX_GATEWAY_RUNTIME set, default is UnconfiguredRuntime."""
        with _clean_env("ZTAX_GATEWAY_RUNTIME", "ZTAX_ENVIRONMENT"):
            result = _runtime()
        assert isinstance(result, UnconfiguredRuntime)

    def test_explicit_unconfigured_returns_unconfigured(self) -> None:
        with _clean_env("ZTAX_GATEWAY_RUNTIME", "ZTAX_ENVIRONMENT"):
            os.environ["ZTAX_GATEWAY_RUNTIME"] = "unconfigured"
            result = _runtime()
        assert isinstance(result, UnconfiguredRuntime)

    def test_whitespace_around_value_is_stripped(self) -> None:
        """_env() strips surrounding whitespace; unconfigured still works."""
        with _clean_env("ZTAX_GATEWAY_RUNTIME", "ZTAX_ENVIRONMENT"):
            os.environ["ZTAX_GATEWAY_RUNTIME"] = "  unconfigured  "
            result = _runtime()
        assert isinstance(result, UnconfiguredRuntime)


# ---------------------------------------------------------------------------
# _runtime() — "fake" is allowed only in development
# ---------------------------------------------------------------------------


class TestRuntimeFake:
    def test_fake_in_development_returns_fake_runtime(self) -> None:
        with _clean_env("ZTAX_GATEWAY_RUNTIME", "ZTAX_ENVIRONMENT"):
            os.environ["ZTAX_GATEWAY_RUNTIME"] = "fake"
            os.environ["ZTAX_ENVIRONMENT"] = "development"
            result = _runtime()
        assert isinstance(result, FakeRuntime)

    def test_fake_without_environment_set_exits(self) -> None:
        with _clean_env("ZTAX_GATEWAY_RUNTIME", "ZTAX_ENVIRONMENT"):
            os.environ["ZTAX_GATEWAY_RUNTIME"] = "fake"
            # ZTAX_ENVIRONMENT is not set
            with pytest.raises(SystemExit) as exc_info:
                _runtime()
        assert exc_info.value.code == 2

    def test_fake_in_production_exits(self) -> None:
        with _clean_env("ZTAX_GATEWAY_RUNTIME", "ZTAX_ENVIRONMENT"):
            os.environ["ZTAX_GATEWAY_RUNTIME"] = "fake"
            os.environ["ZTAX_ENVIRONMENT"] = "production"
            with pytest.raises(SystemExit) as exc_info:
                _runtime()
        assert exc_info.value.code == 2

    def test_fake_in_staging_exits(self) -> None:
        with _clean_env("ZTAX_GATEWAY_RUNTIME", "ZTAX_ENVIRONMENT"):
            os.environ["ZTAX_GATEWAY_RUNTIME"] = "fake"
            os.environ["ZTAX_ENVIRONMENT"] = "staging"
            with pytest.raises(SystemExit) as exc_info:
                _runtime()
        assert exc_info.value.code == 2


# ---------------------------------------------------------------------------
# _runtime() — unknown value is refused
# ---------------------------------------------------------------------------


class TestRuntimeUnknown:
    @pytest.mark.parametrize("value", ["real", "openai", "anthropic", "TRUE", "1", ""])
    def test_unknown_value_exits(self, value: str) -> None:
        with _clean_env("ZTAX_GATEWAY_RUNTIME", "ZTAX_ENVIRONMENT"):
            os.environ["ZTAX_GATEWAY_RUNTIME"] = value
            with pytest.raises(SystemExit) as exc_info:
                _runtime()
        assert exc_info.value.code == 2


# ---------------------------------------------------------------------------
# load_config + suspended field
# ---------------------------------------------------------------------------


class TestLoadConfigSuspended:
    def test_suspended_false_is_allowed(self, tmp_path: Path) -> None:
        reg, _routes = load_config(_write_registry(_MINIMAL_REGISTRY, tmp_path))
        assert reg.get("classification-review") is not None
        assert not reg.is_killed("classification-review")

    def test_suspended_true_is_registered_and_suspended(self, tmp_path: Path) -> None:
        """A suspended use case is registered (so audit sees it) but marked
        suspended so the Gateway returns AI_USE_CASE_SUSPENDED, not
        AI_UNKNOWN_USE_CASE.
        """
        reg, _routes = load_config(_write_registry(_SUSPENDED_REGISTRY, tmp_path))
        uc = reg.get("classification-review")
        # It IS registered.
        assert uc is not None
        # The suspended flag is set on the UseCase itself.
        assert uc.suspended is True

    def test_suspended_entry_causes_suspended_refusal(self, tmp_path: Path) -> None:
        """End-to-end: a suspended use case produces USE_CASE_SUSPENDED via the Gateway."""
        import base64

        from ztax_gateway.governance import Refusal
        from ztax_gateway.server import Gateway

        reg, routes = load_config(_write_registry(_SUSPENDED_REGISTRY, tmp_path))
        gateway = Gateway(reg, routes, FakeRuntime(), "local", "ai-fake-v1")

        payload = json.dumps(
            {
                "kind": "CLASSIFICATION_PROPOSAL",
                "governance": {
                    "tenant_id": "00000000-0000-0000-0000-000000000001",
                    "use_case": "classification-review",
                    "authority_outcome": "A1",
                    "risk_tier": "T2",
                    "region": "local",
                    "data_classes": ["P0"],
                },
                "subject_ref": "subject-001",
                "input_b64": base64.b64encode(b"test").decode(),
            }
        ).encode()

        with pytest.raises(GatewayError) as exc_info:
            gateway.invoke(payload)

        assert exc_info.value.code == grpc.StatusCode.PERMISSION_DENIED
        assert exc_info.value.reason == Refusal.USE_CASE_SUSPENDED.value

    def test_missing_suspended_field_defaults_to_false(self, tmp_path: Path) -> None:
        """If "suspended" is absent from the JSON, it defaults to False."""
        doc: dict[str, object] = {
            "use_cases": [
                {
                    "use_case_id": "classification-review",
                    "owner": "lane-l",
                    "description": "test",
                    "max_risk_tier": "T2",
                    "max_authority": "A1",
                    "permitted_regions": ["local"],
                    "permitted_data_classes": ["P0"],
                    # "suspended" intentionally omitted
                    "route": {
                        "model_profile": "model:fake-v1",
                        "provider_profile": "provider:fake",
                        "prompt_profile": "prompt:classification-review-v1",
                    },
                }
            ],
            "killed": [],
            "global_kill": False,
        }
        reg, _routes = load_config(_write_registry(doc, tmp_path))
        assert not reg.is_killed("classification-review")
