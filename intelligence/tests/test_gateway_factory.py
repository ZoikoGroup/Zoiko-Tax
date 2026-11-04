"""build_gateway: fail-closed startup for the demo servers' Gateway."""

from __future__ import annotations

import importlib.util
import json
import logging
import shutil
from pathlib import Path
from types import ModuleType
from typing import Any

import pytest

from ztax_gateway.fake_runtime import FakeRuntime
from ztax_gateway.gateway_factory import ENV_PROMPT_PROFILES, GatewayFactoryError, build_gateway
from ztax_gateway.invocation_check_event import LoggingCheckEventSink
from ztax_gateway.production_gates import PromptProfileRegistry
from ztax_gateway.runtime import RoutingTable
from ztax_gateway.server import Gateway

_CONFIG_DIR = Path(__file__).resolve().parent.parent / "config"
_REGISTRY = _CONFIG_DIR / "registry.live-demo.json"
_PROFILES = _CONFIG_DIR / "prompt_profiles.live-demo.json"
_MARKER = "FACTORY-MARKER-5d71a0"

_spec = importlib.util.spec_from_file_location(
    "groq_demo_runtime_for_factory",
    Path(__file__).resolve().parent.parent / "tools" / "groq_demo_runtime.py",
)
assert _spec is not None and _spec.loader is not None
groq_demo_runtime: ModuleType = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(groq_demo_runtime)


def _fake(_: PromptProfileRegistry) -> FakeRuntime:
    return FakeRuntime()


def _build(**kw: Any) -> Gateway:
    rf = kw.pop("rf", _fake)
    return build_gateway(_REGISTRY, "local", "0.0.0-test", runtime_factory=rf, **kw)


def _profiles_file(
    tmp_path: Path, drop: str | None = None, edit: dict[str, dict[str, Any]] | None = None
) -> Path:
    doc = json.loads(_PROFILES.read_text(encoding="utf-8"))
    profiles = [p for p in doc["profiles"] if p["profile_id"] != drop]
    for p in profiles:
        p.update((edit or {}).get(p["profile_id"], {}))
    path = tmp_path / "profiles.json"
    path.write_text(json.dumps({"profiles": profiles}), encoding="utf-8")
    return path


def test_an_unset_variable_fails_with_no_default(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv(ENV_PROMPT_PROFILES, raising=False)
    with pytest.raises(GatewayFactoryError, match=ENV_PROMPT_PROFILES):
        _build()


def test_an_empty_variable_fails_too(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(ENV_PROMPT_PROFILES, "   ")
    with pytest.raises(GatewayFactoryError):
        _build()


def test_a_missing_profile_file_fails(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(tmp_path / "absent.json"))
    with pytest.raises(GatewayFactoryError):
        _build()


def test_a_route_label_without_a_profile_fails_naming_only_the_label(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    path = _profiles_file(tmp_path, drop="prompt:explain-obligation-row-v1")
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(path))
    with pytest.raises(GatewayFactoryError) as info:
        _build()
    assert "prompt:explain-obligation-row-v1" in str(info.value)
    assert "DEMO ONLY" not in str(info.value)


def test_an_unused_profile_only_warns_once_with_a_fixed_message(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, caplog: pytest.LogCaptureFixture
) -> None:
    doc = json.loads(_PROFILES.read_text(encoding="utf-8"))
    extra = dict(doc["profiles"][0], profile_id="prompt:unused-v1", system_prompt=_MARKER)
    doc["profiles"].append(extra)
    path = tmp_path / "profiles.json"
    path.write_text(json.dumps(doc), encoding="utf-8")
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(path))
    with caplog.at_level(logging.DEBUG):
        gateway = _build()
    warnings = [r for r in caplog.records if r.levelno == logging.WARNING]
    assert [r.getMessage() for r in warnings] == [
        "gateway_factory: a prompt profile is not used by any route"
    ]
    assert _MARKER not in caplog.text
    assert isinstance(gateway, Gateway)


def test_the_gateway_gets_the_kill_path_the_profiles_and_the_logging_check_sink(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(_PROFILES))
    gateway = _build()
    assert gateway._kill_state_path == _REGISTRY
    assert gateway._prompt_registry is not None
    assert gateway._prompt_registry.count == 4
    assert isinstance(gateway._check_event_sink, LoggingCheckEventSink)


def test_the_fake_runtime_path_is_unchanged(monkeypatch: pytest.MonkeyPatch) -> None:
    # A runtime with no prompt text has nothing to verify; the factory still builds.
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(_PROFILES))
    gateway = _build()
    assert isinstance(gateway._runtime, FakeRuntime)


def test_route_transform_is_applied(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(_PROFILES))
    seen: list[int] = []

    def tracking(registry: Any, routes: RoutingTable) -> RoutingTable:
        seen.append(1)
        return routes

    _build(route_transform=tracking)
    assert seen == [1]


def test_a_runtime_that_verifies_prompts_is_asked_to(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(_PROFILES))
    calls: list[int] = []

    class _Verifying(FakeRuntime):
        def verify_prompts(self, use_cases: Any, routes: Any) -> None:
            calls.append(len(use_cases.ids()))

    _build(rf=lambda _: _Verifying())
    assert calls == [4]


def test_a_profile_text_that_differs_from_the_runtime_stops_startup_naming_the_label(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    monkeypatch.setenv("GROQ_API_KEY", "not-a-real-key-0000")
    monkeypatch.setenv("GROQ_MODEL", "demo-pinned-model-70b")
    label = "prompt:explain-shadow-variance-v1"
    path = _profiles_file(tmp_path, edit={label: {"system_prompt": f"changed {_MARKER}"}})
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(path))
    runtime = groq_demo_runtime.GroqDemoRuntime()
    with pytest.raises(groq_demo_runtime.GroqDemoConfigError) as info:
        _build(rf=runtime.with_prompt_registry)
    assert label in str(info.value)
    assert _MARKER not in str(info.value)
    assert "DEMO ONLY" not in str(info.value)


def test_the_shipped_profiles_match_the_runtime_for_every_route_and_kind(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("GROQ_API_KEY", "not-a-real-key-0000")
    monkeypatch.setenv("GROQ_MODEL", "demo-pinned-model-70b")
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(_PROFILES))
    runtime = groq_demo_runtime.GroqDemoRuntime()
    gateway = _build(rf=runtime.with_prompt_registry)
    assert isinstance(gateway._runtime, groq_demo_runtime.GroqDemoRuntime)


def test_the_gateway_follows_the_registry_file_it_was_given(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    registry_copy = tmp_path / "registry.json"
    shutil.copy(_REGISTRY, registry_copy)
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(_PROFILES))
    gateway = build_gateway(registry_copy, "local", "0.0.0-test", runtime_factory=_fake)
    assert gateway._kill_state_path == registry_copy
