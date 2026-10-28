"""Unit tests for tools/groq_demo_runtime.py -- no network, no key, ever.

DEMO ONLY source under test. These tests stub the HTTP layer
(``urllib.request.urlopen``) and never make a real connection; they run with
no GROQ_API_KEY or GROQ_MODEL set unless a test explicitly provides one via
monkeypatch, and that provided value is always a synthetic placeholder, never
a real credential shape.

tools/ is not on pythonpath (pyproject.toml's [tool.pytest.ini_options] sets
pythonpath = ["src"] only), so the module under test is loaded directly from
its file path via importlib rather than a normal import statement.
"""

from __future__ import annotations

import importlib.util
import json
import urllib.error
from pathlib import Path
from types import ModuleType
from typing import Any

import pytest

from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.runtime import Route, RuntimeUnavailableError
from ztax_gateway.wire import Call, Governance, Kind

# ---------------------------------------------------------------------------
# Load tools/groq_demo_runtime.py by path (not on pythonpath).
# ---------------------------------------------------------------------------

_MODULE_PATH = Path(__file__).resolve().parent.parent / "tools" / "groq_demo_runtime.py"
_spec = importlib.util.spec_from_file_location("groq_demo_runtime", _MODULE_PATH)
assert _spec is not None and _spec.loader is not None
groq_demo_runtime: ModuleType = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(groq_demo_runtime)

GroqDemoConfigError = groq_demo_runtime.GroqDemoConfigError
GroqDemoRuntime = groq_demo_runtime.GroqDemoRuntime
build_request = groq_demo_runtime.build_request

# ---------------------------------------------------------------------------
# Fixtures / helpers
# ---------------------------------------------------------------------------

_ROUTE = Route("model:demo", "provider:groq-demo", "prompt:demo-v1")
_FAKE_KEY = "sk-demo-not-a-real-credential-0000000000"
_FAKE_MODEL = "demo-pinned-model-70b"


def _make_call(
    kind: Kind = Kind.CLASSIFICATION_PROPOSAL, input_text: str = '{"description":"demo sku"}'
) -> Call:
    return Call(
        kind=kind,
        governance=Governance(
            tenant_id="00000000-0000-0000-0000-000000000001",
            use_case="classification-review",
            authority_outcome=AuthorityOutcome.A1,
            risk_tier=RiskTier.T1,
            region="local",
            data_classes=("P0",),
        ),
        subject_ref="sku:test",
        input=input_text.encode(),
    )


class _FakeHTTPResponse:
    """Stub for the context manager urllib.request.urlopen returns."""

    def __init__(self, body: bytes) -> None:
        self._body = body

    def __enter__(self) -> _FakeHTTPResponse:
        return self

    def __exit__(self, *exc_info: object) -> None:
        return None

    def read(self) -> bytes:
        return self._body


def _chat_completion_body(content: str) -> bytes:
    return json.dumps({"choices": [{"message": {"content": content}}]}).encode()


# ---------------------------------------------------------------------------
# Missing-env refusal
# ---------------------------------------------------------------------------


def test_missing_api_key_raises_config_error(monkeypatch: pytest.MonkeyPatch) -> None:
    """No GROQ_API_KEY -> GroqDemoConfigError, construction never reaches urlopen."""
    monkeypatch.delenv("GROQ_API_KEY", raising=False)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    with pytest.raises(GroqDemoConfigError, match="GROQ_API_KEY"):
        GroqDemoRuntime()


def test_missing_model_raises_config_error(monkeypatch: pytest.MonkeyPatch) -> None:
    """No GROQ_MODEL -> GroqDemoConfigError, construction never reaches urlopen."""
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.delenv("GROQ_MODEL", raising=False)
    with pytest.raises(GroqDemoConfigError, match="GROQ_MODEL"):
        GroqDemoRuntime()


def test_missing_both_raises_before_touching_model_name(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("GROQ_API_KEY", raising=False)
    monkeypatch.delenv("GROQ_MODEL", raising=False)
    with pytest.raises(GroqDemoConfigError):
        GroqDemoRuntime()


# ---------------------------------------------------------------------------
# Floating-alias refusal (mirrors REQ-0027)
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("alias", ["latest", "default", "stable", "preview", "LATEST"])
def test_floating_alias_model_refused(monkeypatch: pytest.MonkeyPatch, alias: str) -> None:
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", alias)
    with pytest.raises(GroqDemoConfigError, match="floating alias"):
        GroqDemoRuntime()


def test_pinned_model_name_is_accepted(monkeypatch: pytest.MonkeyPatch) -> None:
    """A model name that is not one of the four aliases constructs cleanly."""
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    GroqDemoRuntime()  # must not raise


# ---------------------------------------------------------------------------
# Request shape: pinned model, key only in the header, never in output
# ---------------------------------------------------------------------------


def test_build_request_pins_model_and_carries_key_only_in_header() -> None:
    call = _make_call()
    request = build_request(_FAKE_MODEL, _FAKE_KEY, call)
    assert request.full_url == "https://api.groq.com/openai/v1/chat/completions"
    assert request.get_header("Authorization") == f"Bearer {_FAKE_KEY}"
    body = json.loads(request.data)
    assert body["model"] == _FAKE_MODEL
    assert _FAKE_KEY not in json.dumps(body)


def test_run_never_prints_or_logs_the_key(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    runtime = GroqDemoRuntime()

    def _fake_urlopen(request: Any, timeout: float) -> _FakeHTTPResponse:
        return _FakeHTTPResponse(_chat_completion_body("ontology:demo-class"))

    monkeypatch.setattr(groq_demo_runtime.urllib.request, "urlopen", _fake_urlopen)
    runtime.run(_ROUTE, _make_call())

    captured = capsys.readouterr()
    assert _FAKE_KEY not in captured.out
    assert _FAKE_KEY not in captured.err


# ---------------------------------------------------------------------------
# Successful response mapping
# ---------------------------------------------------------------------------


def test_successful_classification_maps_to_proposed_code(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    runtime = GroqDemoRuntime()

    def _fake_urlopen(request: Any, timeout: float) -> _FakeHTTPResponse:
        return _FakeHTTPResponse(_chat_completion_body("  ontology:mobile-plan  "))

    monkeypatch.setattr(groq_demo_runtime.urllib.request, "urlopen", _fake_urlopen)
    result = runtime.run(_ROUTE, _make_call())
    assert result.proposed_code == "ontology:mobile-plan"
    assert result.confidence == "0.5000"


# ---------------------------------------------------------------------------
# Timeout and HTTP-error handling
# ---------------------------------------------------------------------------


def test_timeout_raises_runtime_unavailable(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    runtime = GroqDemoRuntime()

    def _fake_urlopen(request: Any, timeout: float) -> _FakeHTTPResponse:
        raise TimeoutError("synthetic timeout")

    monkeypatch.setattr(groq_demo_runtime.urllib.request, "urlopen", _fake_urlopen)
    with pytest.raises(RuntimeUnavailableError):
        runtime.run(_ROUTE, _make_call())


def test_http_error_raises_runtime_unavailable(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    runtime = GroqDemoRuntime()

    def _fake_urlopen(request: Any, timeout: float) -> _FakeHTTPResponse:
        raise urllib.error.HTTPError(
            request.full_url, 503, "Service Unavailable", {}, None  # type: ignore[arg-type]
        )

    monkeypatch.setattr(groq_demo_runtime.urllib.request, "urlopen", _fake_urlopen)
    with pytest.raises(RuntimeUnavailableError):
        runtime.run(_ROUTE, _make_call())


def test_url_error_raises_runtime_unavailable(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    runtime = GroqDemoRuntime()

    def _fake_urlopen(request: Any, timeout: float) -> _FakeHTTPResponse:
        raise urllib.error.URLError("synthetic DNS failure")

    monkeypatch.setattr(groq_demo_runtime.urllib.request, "urlopen", _fake_urlopen)
    with pytest.raises(RuntimeUnavailableError):
        runtime.run(_ROUTE, _make_call())


def test_malformed_response_raises_runtime_unavailable(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    runtime = GroqDemoRuntime()

    def _fake_urlopen(request: Any, timeout: float) -> _FakeHTTPResponse:
        return _FakeHTTPResponse(b"not json at all")

    monkeypatch.setattr(groq_demo_runtime.urllib.request, "urlopen", _fake_urlopen)
    with pytest.raises(RuntimeUnavailableError):
        runtime.run(_ROUTE, _make_call())


def test_empty_choices_raises_runtime_unavailable(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    runtime = GroqDemoRuntime()

    def _fake_urlopen(request: Any, timeout: float) -> _FakeHTTPResponse:
        return _FakeHTTPResponse(json.dumps({"choices": []}).encode())

    monkeypatch.setattr(groq_demo_runtime.urllib.request, "urlopen", _fake_urlopen)
    with pytest.raises(RuntimeUnavailableError):
        runtime.run(_ROUTE, _make_call())


# ---------------------------------------------------------------------------
# No network: this suite never calls out.
# ---------------------------------------------------------------------------


def test_run_without_a_stub_would_use_urlopen(monkeypatch: pytest.MonkeyPatch) -> None:
    """Guard: if urlopen is ever reached unstubbed, fail loudly rather than
    silently making a real network call from the test suite."""
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    runtime = GroqDemoRuntime()

    def _guard(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("test attempted a real network call via urlopen")

    monkeypatch.setattr(groq_demo_runtime.urllib.request, "urlopen", _guard)
    with pytest.raises(AssertionError, match="real network call"):
        runtime.run(_ROUTE, _make_call())
