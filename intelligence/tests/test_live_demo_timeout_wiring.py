"""The live demo servers give the Gateway its runtime deadline.

``GATEWAY_RUNTIME_TIMEOUT_SECONDS`` (groq_demo_runtime.py) is the Gateway's total
deadline for one model call. Both demo mains must pass it to ``build_gateway``, it
must stay above the Groq runtime's own per-step limit, and a Gateway built the way
the demo builds it must turn a slow runtime into the ordinary "model runtime
unavailable" refusal with one BLOCK record. No test here calls the provider.
"""

from __future__ import annotations

import importlib.util
import json
import logging
import sys
import threading
from pathlib import Path
from types import ModuleType
from typing import Any

import grpc
import pytest

from ztax_gateway.gateway_factory import ENV_PROMPT_PROFILES, GatewayFactoryError, build_gateway
from ztax_gateway.runtime import Result, Route
from ztax_gateway.server import (
    DETAIL_RUNTIME_UNAVAILABLE,
    REASON_UNAVAILABLE,
    RUNTIME_MAX_IN_FLIGHT,
    Gateway,
    GatewayError,
)
from ztax_gateway.wire import Call

_TOOLS_DIR = Path(__file__).resolve().parent.parent / "tools"
_CONFIG_DIR = Path(__file__).resolve().parent.parent / "config"
_REGISTRY = _CONFIG_DIR / "registry.live-demo.json"
_PROFILES = _CONFIG_DIR / "prompt_profiles.live-demo.json"
_MODULE_NAMES = ("groq_demo_runtime", "report_fixture", "serve_live_demo", "report_ai_http")
_FAKE_MODEL = "demo-pinned-model-70b"
_FAKE_CREDENTIAL = "demo-credential-not-real"  # the runtime constructor only needs it to exist
_LATE_ANSWER = "LATE-ANSWER-5e21c8"


def _load_modules() -> dict[str, ModuleType]:
    """Load the tools by file path, as their own tests do, and leave sys.modules as found."""
    before = {name: sys.modules.get(name) for name in _MODULE_NAMES}
    loaded: dict[str, ModuleType] = {}
    try:
        for name in _MODULE_NAMES:
            spec = importlib.util.spec_from_file_location(name, _TOOLS_DIR / f"{name}.py")
            assert spec is not None and spec.loader is not None
            module = importlib.util.module_from_spec(spec)
            sys.modules[name] = module
            spec.loader.exec_module(module)
            loaded[name] = module
    finally:
        for name, previous in before.items():
            if previous is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = previous
    return loaded


_MODULES = _load_modules()
groq_demo_runtime = _MODULES["groq_demo_runtime"]
report_fixture = _MODULES["report_fixture"]
serve_live_demo = _MODULES["serve_live_demo"]
report_ai_http = _MODULES["report_ai_http"]


class _SlowRuntime:
    """Waits for ``release``, then returns an answer that must never be used."""

    def __init__(self) -> None:
        self.release = threading.Event()
        self.finished = threading.Event()
        self.calls = 0

    def run(self, route: Route, call: Call) -> Result:
        self.calls += 1
        self.release.wait(10)
        try:
            return Result(
                fields={
                    "explanation": _LATE_ANSWER,
                    "cited_ids": json.dumps(["OBL-001"]),
                    "abstain": "false",
                    "reason": "",
                },
                confidence="0.5000",
            )
        finally:
            self.finished.set()


def _set_demo_env(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    registry = tmp_path / "registry.json"
    registry.write_text(_REGISTRY.read_text(encoding="utf-8"), encoding="utf-8")
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_CREDENTIAL)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    monkeypatch.setenv("ZTAX_GATEWAY_CONFIG", str(registry))
    monkeypatch.setenv("ZTAX_GATEWAY_REGION", "local")
    monkeypatch.setenv("ZTAX_GATEWAY_AI_TRAIN", "0.0.0-test")
    monkeypatch.setenv("ZTAX_GATEWAY_ADDRESS", "127.0.0.1:0")
    monkeypatch.setenv("ZTAX_GATEWAY_INSECURE_LOCAL", "true")
    monkeypatch.setenv("ZTAX_ENVIRONMENT", "development")
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(_PROFILES))


def _capture_build_gateway(monkeypatch: pytest.MonkeyPatch, module: ModuleType) -> dict[str, Any]:
    """Replace the module's build_gateway with a spy that records its kwargs and then
    refuses, so main() returns 2 before it could serve anything."""
    seen: dict[str, Any] = {}

    def spy(*args: Any, **kwargs: Any) -> Gateway:
        seen.update(kwargs)
        raise GatewayFactoryError("stopped by the test before serving")

    monkeypatch.setattr(module, "build_gateway", spy)
    return seen


# ---------------------------------------------------------------------------
# 1. Both demo mains pass the constant
# ---------------------------------------------------------------------------


def test_serve_live_demo_passes_the_gateway_timeout(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    _set_demo_env(monkeypatch, tmp_path)
    seen = _capture_build_gateway(monkeypatch, serve_live_demo)
    assert serve_live_demo.main() == 2
    assert seen["runtime_timeout_seconds"] == groq_demo_runtime.GATEWAY_RUNTIME_TIMEOUT_SECONDS


def test_report_ai_http_passes_the_gateway_timeout(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    _set_demo_env(monkeypatch, tmp_path)
    seen = _capture_build_gateway(monkeypatch, report_ai_http)
    assert report_ai_http.main() == 2
    assert seen["runtime_timeout_seconds"] == groq_demo_runtime.GATEWAY_RUNTIME_TIMEOUT_SECONDS


# ---------------------------------------------------------------------------
# 2. The constant itself
# ---------------------------------------------------------------------------


def test_the_gateway_deadline_is_above_the_runtimes_per_step_limit() -> None:
    assert (
        groq_demo_runtime.GATEWAY_RUNTIME_TIMEOUT_SECONDS > groq_demo_runtime._TIMEOUT_SECONDS
    )


def test_the_gateway_deadline_passes_the_gateways_own_validation(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(_PROFILES))
    gateway = build_gateway(
        _REGISTRY,
        "local",
        "0.0.0-test",
        runtime_factory=lambda _profiles: _SlowRuntime(),
        runtime_timeout_seconds=groq_demo_runtime.GATEWAY_RUNTIME_TIMEOUT_SECONDS,
    )
    assert isinstance(gateway, Gateway)


# ---------------------------------------------------------------------------
# 3. A Gateway built the way the demo builds it
# ---------------------------------------------------------------------------


def _demo_gateway(
    monkeypatch: pytest.MonkeyPatch, runtime: _SlowRuntime, timeout: float
) -> Gateway:
    monkeypatch.setenv(ENV_PROMPT_PROFILES, str(_PROFILES))
    return build_gateway(
        _REGISTRY,
        "local",
        "0.0.0-test",
        runtime_factory=lambda _profiles: runtime,
        route_transform=lambda registry, routes: serve_live_demo._patched_routes(
            registry, routes, _FAKE_MODEL
        ),
        runtime_timeout_seconds=timeout,
    )


def _evidence_lines(caplog: pytest.LogCaptureFixture) -> list[dict[str, Any]]:
    return [
        json.loads(r.getMessage()) for r in caplog.records if r.name == "ztax_gateway.evidence"
    ]


def test_a_slow_model_call_is_refused_with_one_block_record(
    monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    runtime = _SlowRuntime()
    gateway = _demo_gateway(monkeypatch, runtime, timeout=0.05)
    fixture = report_fixture.generate_fixture()
    rows = report_fixture.rows_for_kind(fixture, "obligation-row", ["OBL-001"])
    raw_call = report_ai_http._build_raw_call(
        "explain-obligation-row", "obligation-row", rows
    )
    try:
        with (
            caplog.at_level(logging.INFO, logger="ztax_gateway.evidence"),
            pytest.raises(GatewayError) as info,
        ):
            gateway.invoke_with_id(raw_call)
        err = info.value
        assert err.code == grpc.StatusCode.UNAVAILABLE
        assert err.reason == REASON_UNAVAILABLE
        assert err.detail == DETAIL_RUNTIME_UNAVAILABLE == "model runtime unavailable"
        lines = _evidence_lines(caplog)
        assert len(lines) == 1
        assert lines[0]["invocation_outcome"] == "BLOCK"
    finally:
        runtime.release.set()
    # The late answer arrives after the refusal and is never sealed.
    assert runtime.finished.wait(5)
    assert len(_evidence_lines(caplog)) == 1
    assert _LATE_ANSWER not in json.dumps(_evidence_lines(caplog))


def test_the_report_screen_gets_its_review_required_view_on_a_timeout(
    monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    runtime = _SlowRuntime()
    gateway = _demo_gateway(monkeypatch, runtime, timeout=0.05)
    fixture = report_fixture.generate_fixture()
    try:
        with caplog.at_level(logging.INFO, logger="ztax_gateway.evidence"):
            result = report_ai_http.explain(gateway, fixture, "obligation-row", ["OBL-001"])
        assert result["outcome"] == "REVIEW_REQUIRED"
        assert result["reason"] == REASON_UNAVAILABLE
        assert _LATE_ANSWER not in json.dumps(result)
        assert len(_evidence_lines(caplog)) == 1
    finally:
        runtime.release.set()
    assert runtime.finished.wait(5)


def test_the_demo_gateway_caps_concurrent_model_calls() -> None:
    assert RUNTIME_MAX_IN_FLIGHT == 8  # the figure LIVE_DEMO.md states
