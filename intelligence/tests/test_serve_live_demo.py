"""Unit tests for tools/serve_live_demo.py -- no network, no key, no Docker.

DEMO ONLY source under test. These tests never bind a port and never make a
real Groq call: ``serve`` (and, in the two refusal tests, ``load_config`` and
``Gateway`` too) are never reached at all, and in the one success-path test
``serve`` is stubbed to return immediately instead of actually listening.

tools/ is not on pythonpath (pyproject.toml's [tool.pytest.ini_options] sets
pythonpath = ["src"] only), so the module under test is loaded directly from
its file path via importlib, the same approach test_groq_demo_runtime.py
uses for its sibling module.
"""

from __future__ import annotations

import importlib.util
import json
import sys
from pathlib import Path
from types import ModuleType
from typing import Any

import pytest

# ---------------------------------------------------------------------------
# Load tools/groq_demo_runtime.py and tools/serve_live_demo.py by path.
# serve_live_demo imports groq_demo_runtime as a top-level module (the same
# way it will when python runs it directly from intelligence/tools/), so
# that name must already be registered in sys.modules before serve_live_demo
# is executed.
# ---------------------------------------------------------------------------

_TOOLS_DIR = Path(__file__).resolve().parent.parent / "tools"


def _load(name: str) -> ModuleType:
    spec = importlib.util.spec_from_file_location(name, _TOOLS_DIR / f"{name}.py")
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


groq_demo_runtime = _load("groq_demo_runtime")
serve_live_demo = _load("serve_live_demo")

GroqDemoRuntime = groq_demo_runtime.GroqDemoRuntime

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_FAKE_KEY = "sk-demo-not-a-real-credential-0000000000"
_FAKE_MODEL = "demo-pinned-model-70b"

_MINIMAL_REGISTRY: dict[str, Any] = {
    "use_cases": [
        {
            "use_case_id": "classification-review",
            "owner": "lane-l",
            "description": "test fixture",
            "max_risk_tier": "T2",
            "max_authority": "A1",
            "permitted_regions": ["local"],
            "permitted_data_classes": ["P0", "P1"],
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


def _set_gateway_env(monkeypatch: pytest.MonkeyPatch, registry_path: Path) -> None:
    monkeypatch.setenv("ZTAX_GATEWAY_CONFIG", str(registry_path))
    monkeypatch.setenv("ZTAX_GATEWAY_REGION", "local")
    monkeypatch.setenv("ZTAX_GATEWAY_AI_TRAIN", "0.0.0-test")
    monkeypatch.setenv("ZTAX_GATEWAY_ADDRESS", "127.0.0.1:0")
    monkeypatch.setenv("ZTAX_GATEWAY_INSECURE_LOCAL", "true")
    monkeypatch.setenv("ZTAX_ENVIRONMENT", "development")


class _FakeGRPCServer:
    """Stands in for the grpc.Server serve() would normally return."""

    def __init__(self) -> None:
        self.wait_called = False

    def wait_for_termination(self) -> None:
        self.wait_called = True


# ---------------------------------------------------------------------------
# Refusal: missing GROQ_API_KEY / GROQ_MODEL -- nothing else is ever reached
# ---------------------------------------------------------------------------


def test_missing_api_key_refuses_before_serve_is_called(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    monkeypatch.delenv("GROQ_API_KEY", raising=False)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    registry_path = tmp_path / "registry.json"
    registry_path.write_text(json.dumps(_MINIMAL_REGISTRY))
    _set_gateway_env(monkeypatch, registry_path)

    def _guard_serve(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("serve() must not be called when GROQ_API_KEY is missing")

    monkeypatch.setattr(serve_live_demo, "serve", _guard_serve)

    assert serve_live_demo.main() == 2


def test_missing_model_refuses_before_serve_is_called(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.delenv("GROQ_MODEL", raising=False)
    registry_path = tmp_path / "registry.json"
    registry_path.write_text(json.dumps(_MINIMAL_REGISTRY))
    _set_gateway_env(monkeypatch, registry_path)

    def _guard_serve(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("serve() must not be called when GROQ_MODEL is missing")

    monkeypatch.setattr(serve_live_demo, "serve", _guard_serve)

    assert serve_live_demo.main() == 2


@pytest.mark.parametrize("alias", ["latest", "default", "stable", "preview"])
def test_floating_alias_refuses_before_serve_is_called(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, alias: str
) -> None:
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", alias)
    registry_path = tmp_path / "registry.json"
    registry_path.write_text(json.dumps(_MINIMAL_REGISTRY))
    _set_gateway_env(monkeypatch, registry_path)

    def _guard_serve(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("serve() must not be called when GROQ_MODEL is a floating alias")

    monkeypatch.setattr(serve_live_demo, "serve", _guard_serve)

    assert serve_live_demo.main() == 2


def test_missing_gateway_config_also_refuses(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Even with a valid GROQ_* pair, a missing ZTAX_GATEWAY_* var refuses too."""
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    monkeypatch.delenv("ZTAX_GATEWAY_CONFIG", raising=False)
    monkeypatch.delenv("ZTAX_GATEWAY_REGION", raising=False)
    monkeypatch.delenv("ZTAX_GATEWAY_AI_TRAIN", raising=False)

    def _guard_serve(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("serve() must not be called when the gateway env is incomplete")

    monkeypatch.setattr(serve_live_demo, "serve", _guard_serve)

    assert serve_live_demo.main() == 2


def test_refuses_without_insecure_local_development(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    """Valid GROQ_* and gateway env, but not declared insecure-local-development."""
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    registry_path = tmp_path / "registry.json"
    registry_path.write_text(json.dumps(_MINIMAL_REGISTRY))
    _set_gateway_env(monkeypatch, registry_path)
    monkeypatch.setenv("ZTAX_ENVIRONMENT", "production")  # not "development"

    def _guard_serve(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("serve() must not be called outside insecure-local-development")

    monkeypatch.setattr(serve_live_demo, "serve", _guard_serve)

    assert serve_live_demo.main() == 2


# ---------------------------------------------------------------------------
# Error output never contains the key
# ---------------------------------------------------------------------------


def test_missing_model_error_output_never_contains_the_key(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.delenv("GROQ_MODEL", raising=False)
    serve_live_demo.main()
    captured = capsys.readouterr()
    assert _FAKE_KEY not in captured.out
    assert _FAKE_KEY not in captured.err


@pytest.mark.parametrize("alias", ["latest", "default", "stable", "preview"])
def test_floating_alias_error_output_never_contains_the_key(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str], alias: str
) -> None:
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", alias)
    serve_live_demo.main()
    captured = capsys.readouterr()
    assert _FAKE_KEY not in captured.out
    assert _FAKE_KEY not in captured.err


# ---------------------------------------------------------------------------
# With everything stubbed: builds the Gateway with GroqDemoRuntime
# ---------------------------------------------------------------------------


def test_builds_gateway_with_groq_demo_runtime_and_never_leaks_the_key(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    registry_path = tmp_path / "registry.json"
    registry_path.write_text(json.dumps(_MINIMAL_REGISTRY))
    _set_gateway_env(monkeypatch, registry_path)

    captured_call: dict[str, Any] = {}
    fake_server = _FakeGRPCServer()

    def _fake_serve(
        gateway: Any, address: str, tls: Any, *, insecure_local: bool
    ) -> tuple[Any, int]:
        captured_call["gateway"] = gateway
        captured_call["address"] = address
        captured_call["tls"] = tls
        captured_call["insecure_local"] = insecure_local
        return fake_server, 0

    monkeypatch.setattr(serve_live_demo, "serve", _fake_serve)

    assert serve_live_demo.main() == 0
    assert fake_server.wait_called

    gateway = captured_call["gateway"]
    assert isinstance(gateway._runtime, GroqDemoRuntime)
    assert captured_call["tls"] is None
    assert captured_call["insecure_local"] is True
    assert captured_call["address"] == "127.0.0.1:0"

    # The registry on disk still says "model:fake-v1" / "provider:fake" (see
    # _MINIMAL_REGISTRY above); the Gateway actually built must carry the
    # live-demo route instead, patched in memory with the real GROQ_MODEL.
    patched_route = gateway._routes.route("classification-review")
    assert patched_route.model_profile == f"model:groq-live-demo:{_FAKE_MODEL}"
    assert patched_route.provider_profile == "provider:groq-demo"
    assert patched_route.prompt_profile == "prompt:classification-review-v1"

    captured = capsys.readouterr()
    assert _FAKE_KEY not in captured.out
    assert _FAKE_KEY not in captured.err
    assert _FAKE_MODEL in captured.out
    assert "DEMO ONLY" in captured.out


# ---------------------------------------------------------------------------
# _patched_routes: the registry file is never rewritten; every route's
# model/provider profile is replaced in memory with the real GROQ_MODEL.
# ---------------------------------------------------------------------------


def _write_registry(tmp_path: Path, doc: dict[str, Any]) -> Path:
    path = tmp_path / "registry.json"
    path.write_text(json.dumps(doc))
    return path


def test_patched_routes_relabels_model_and_provider_keeps_prompt_profile(tmp_path: Path) -> None:
    registry, routes = serve_live_demo.load_config(_write_registry(tmp_path, _MINIMAL_REGISTRY))
    patched = serve_live_demo._patched_routes(registry, routes, "llama-3.1-8b-instant")
    route = patched.route("classification-review")
    assert route.model_profile == "model:groq-live-demo:llama-3.1-8b-instant"
    assert route.provider_profile == "provider:groq-demo"
    assert route.prompt_profile == "prompt:classification-review-v1"


def test_patched_routes_relabels_every_registered_use_case(tmp_path: Path) -> None:
    two_use_cases: dict[str, Any] = {
        "use_cases": [
            {**_MINIMAL_REGISTRY["use_cases"][0]},
            {
                **_MINIMAL_REGISTRY["use_cases"][0],
                "use_case_id": "another-use-case",
                "route": {
                    "model_profile": "model:fake-v1",
                    "provider_profile": "provider:fake",
                    "prompt_profile": "prompt:another-v1",
                },
            },
        ],
        "killed": [],
        "global_kill": False,
    }
    registry, routes = serve_live_demo.load_config(_write_registry(tmp_path, two_use_cases))
    patched = serve_live_demo._patched_routes(registry, routes, "llama-3.1-8b-instant")
    for use_case_id in ("classification-review", "another-use-case"):
        route = patched.route(use_case_id)
        assert route.model_profile == "model:groq-live-demo:llama-3.1-8b-instant"
        assert route.provider_profile == "provider:groq-demo"


def test_patched_routes_never_rewrites_the_registry_file(tmp_path: Path) -> None:
    original_text = json.dumps(_MINIMAL_REGISTRY)
    registry_path = tmp_path / "registry.json"
    registry_path.write_text(original_text)
    registry, routes = serve_live_demo.load_config(registry_path)
    serve_live_demo._patched_routes(registry, routes, "llama-3.1-8b-instant")
    assert registry_path.read_text() == original_text


# ---------------------------------------------------------------------------
# registry.live-demo.json itself: a copy of registry.dev.json with only the
# route's model/provider profile relabelled as the static placeholder.
# ---------------------------------------------------------------------------

_CONFIG_DIR = Path(__file__).resolve().parent.parent / "config"
_REGISTRY_DEV_PATH = _CONFIG_DIR / "registry.dev.json"
_REGISTRY_LIVE_DEMO_PATH = _CONFIG_DIR / "registry.live-demo.json"


def test_registry_live_demo_json_loads_and_carries_the_placeholder_labels() -> None:
    registry, routes = serve_live_demo.load_config(_REGISTRY_LIVE_DEMO_PATH)
    # classification-review was this file's first use case; a later task
    # (report_ai_http.py's demo) added three explain-* use cases alongside
    # it -- see test_report_ai_http.py for those. This test only re-confirms
    # classification-review's own placeholder route is still exactly as the
    # live-demo patching logic expects.
    assert "classification-review" in registry.ids()
    route = routes.route("classification-review")
    assert route.model_profile == "model:groq-live-demo"
    assert route.provider_profile == "provider:groq-demo"
    assert route.prompt_profile == "prompt:classification-review-v1"


def test_registry_live_demo_json_matches_registry_dev_json_except_the_route_and_data_classes(
) -> None:
    """registry.live-demo.json's classification-review entry started as a
    copy of registry.dev.json's: everything but the route label is identical
    except one deliberate, intentional narrowing -- the live demo permits only
    P0 (synthetic public data), where registry.dev.json permits P0 and P1.
    registry.dev.json itself is never touched. registry.live-demo.json has
    since grown three more use cases (test_report_ai_http.py) that
    registry.dev.json does not and must not carry -- this test only compares
    the one use case the two files share. The killed / global_kill keys are
    kept identical to the dev file's (though load_config ignores them).
    """
    dev_doc = json.loads(_REGISTRY_DEV_PATH.read_text(encoding="utf-8"))
    live_doc = json.loads(_REGISTRY_LIVE_DEMO_PATH.read_text(encoding="utf-8"))
    assert live_doc["killed"] == dev_doc["killed"]
    assert live_doc["global_kill"] == dev_doc["global_kill"]
    assert len(dev_doc["use_cases"]) == 1
    dev_uc = dev_doc["use_cases"][0]
    live_uc = next(
        uc for uc in live_doc["use_cases"] if uc["use_case_id"] == dev_uc["use_case_id"]
    )
    for field in (
        "use_case_id", "owner", "description", "max_risk_tier", "max_authority",
        "permitted_regions", "suspended",
    ):
        assert live_uc[field] == dev_uc[field]
    # The intentional difference: P0 only in the live demo, P0 and P1 in dev.
    assert dev_uc["permitted_data_classes"] == ["P0", "P1"]
    assert live_uc["permitted_data_classes"] == ["P0"]
    assert dev_uc["route"] == {
        "model_profile": "model:fake-v1",
        "provider_profile": "provider:fake",
        "prompt_profile": "prompt:classification-review-v1",
    }
    assert live_uc["route"] == {
        "model_profile": "model:groq-live-demo",
        "provider_profile": "provider:groq-demo",
        "prompt_profile": "prompt:classification-review-v1",
    }
