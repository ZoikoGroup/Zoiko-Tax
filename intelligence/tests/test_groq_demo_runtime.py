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
from io import BytesIO
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


def test_build_request_sets_fixed_user_agent() -> None:
    """Groq returns 403 to the default urllib User-Agent; this must not regress."""
    request = build_request(_FAKE_MODEL, _FAKE_KEY, _make_call())
    assert request.get_header("User-agent") == "ztax-live-demo/1.0"


def test_build_request_sets_reasoning_effort_temperature_and_max_tokens() -> None:
    """A fast, non-empty, bounded reply: low reasoning effort, no sampling,
    and a max_tokens generous enough that a short demo answer is never cut
    down to nothing."""
    body = json.loads(build_request(_FAKE_MODEL, _FAKE_KEY, _make_call()).data)
    assert body["reasoning_effort"] == "low"
    assert body["temperature"] == 0
    assert isinstance(body["max_tokens"], int)
    assert body["max_tokens"] >= 16


def test_classification_prompt_states_the_real_ontology_grammar_not_a_fabricated_list() -> None:
    """The system prompt tells the model the one real, checkable constraint
    the Go backend enforces (OntologyID.Validate: an "ontology:" prefix with
    a non-empty path) -- it must not claim to hand over a closed list of
    real node ids, because no such list is reachable from either half of
    the Gateway (see the module-level comment by _ONTOLOGY_GRAMMAR_HINT)."""
    system_prompt = groq_demo_runtime._SYSTEM_PROMPTS[Kind.CLASSIFICATION_PROPOSAL]
    call = _make_call()
    request = build_request(_FAKE_MODEL, _FAKE_KEY, call)
    body = json.loads(request.data)
    assert body["messages"][0]["content"] == system_prompt
    assert "ontology:" in system_prompt
    assert "ontology:<path>" in system_prompt or "ontology:telecom" in system_prompt


# --- the report-explanation prompt (explain-* use cases) ----------------------

_REPORT_USE_CASES = ("explain-report-figure", "explain-obligation-row", "explain-shadow-variance")


def _report_call(use_case: str) -> Call:
    return Call(
        kind=Kind.EXTRACTION,
        governance=Governance(
            tenant_id="00000000-0000-0000-0000-000000000001",
            use_case=use_case,
            authority_outcome=AuthorityOutcome.A1,
            risk_tier=RiskTier.T1,
            region="local",
            data_classes=("P0",),
        ),
        subject_ref="report-explain:test",
        input=b'[{"id": "FIG-001", "value": "1.00"}]',
    )


def _report_prompt() -> str:
    prompt: str = groq_demo_runtime._REPORT_EXPLANATION_SYSTEM_PROMPT
    return " ".join(prompt.split())  # unwrap the source's line breaks


@pytest.mark.parametrize("use_case", _REPORT_USE_CASES)
def test_every_report_use_case_is_sent_the_report_explanation_prompt(use_case: str) -> None:
    body = json.loads(build_request(_FAKE_MODEL, _FAKE_KEY, _report_call(use_case)).data)
    assert body["messages"][0]["content"] == groq_demo_runtime._REPORT_EXPLANATION_SYSTEM_PROMPT
    assert body["messages"][1]["content"] == '[{"id": "FIG-001", "value": "1.00"}]'
    assert body["max_tokens"] == 220


def test_report_prompt_restates_only_the_given_values_and_labels() -> None:
    prompt = _report_prompt()
    assert "Restate only the values and labels given in the rows" in prompt
    assert "Do not calculate anything" in prompt


@pytest.mark.parametrize("word", ["total", "subtotal", "equivalent", "average", "difference"])
def test_report_prompt_forbids_introducing_a_derived_number(word: str) -> None:
    prompt = _report_prompt()
    assert "Never introduce a total, subtotal, equivalent, average, difference" in prompt
    assert word in prompt
    assert "any other number that is not a given row field" in prompt


def test_report_prompt_forbids_extra_commentary_and_causes() -> None:
    prompt = _report_prompt()
    assert "Do not describe what a figure also shows unless a given field says so" in prompt
    assert "Do not give causes or reasons for a figure" in prompt
    assert "Give no tax advice" in prompt


def test_report_prompt_asks_for_two_or_three_short_sentences() -> None:
    assert "Write two or three short sentences" in _report_prompt()


def test_report_prompt_keeps_the_abstain_path_and_the_exact_json_shape() -> None:
    prompt = _report_prompt()
    assert 'set "abstain" to true' in prompt
    assert '"explanation"' in prompt and '"cited_ids"' in prompt and '"abstain"' in prompt
    assert "Reply with JSON only" in prompt


def test_classification_prompt_is_unchanged_by_the_report_prompt() -> None:
    body = json.loads(build_request(_FAKE_MODEL, _FAKE_KEY, _make_call()).data)
    system = body["messages"][0]["content"]
    assert system == groq_demo_runtime._SYSTEM_PROMPTS[Kind.CLASSIFICATION_PROPOSAL]
    assert body["max_tokens"] == 64


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


def test_http_error_message_carries_the_status_code_and_a_120_char_snippet_never_the_key(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """The failure must be diagnosable from the status code and the first
    120 characters of the provider's reply, with the key never appearing and
    the snippet never exceeding 120 characters."""
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    runtime = GroqDemoRuntime()
    long_body = b"synthetic-error-detail-" + b"x" * 200

    def _fake_urlopen(request: Any, timeout: float) -> _FakeHTTPResponse:
        raise urllib.error.HTTPError(
            request.full_url,
            503,
            "Service Unavailable",
            {},  # type: ignore[arg-type]
            BytesIO(long_body),
        )

    monkeypatch.setattr(groq_demo_runtime.urllib.request, "urlopen", _fake_urlopen)
    with pytest.raises(RuntimeUnavailableError, match="503") as excinfo:
        runtime.run(_ROUTE, _make_call())
    message = str(excinfo.value)
    assert _FAKE_KEY not in message
    assert long_body.decode()[:120] in message
    assert long_body.decode()[:121] not in message


def test_http_error_with_no_readable_body_still_raises_cleanly(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """An HTTPError built with fp=None (no body at all) must not itself raise
    while the runtime is building its diagnostic message."""
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    runtime = GroqDemoRuntime()

    def _fake_urlopen(request: Any, timeout: float) -> _FakeHTTPResponse:
        raise urllib.error.HTTPError(
            request.full_url, 403, "Forbidden", {}, None  # type: ignore[arg-type]
        )

    monkeypatch.setattr(groq_demo_runtime.urllib.request, "urlopen", _fake_urlopen)
    with pytest.raises(RuntimeUnavailableError, match="403"):
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


def test_malformed_response_message_carries_a_120_char_snippet_of_the_raw_reply(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    runtime = GroqDemoRuntime()
    raw = b"not json at all, and this filler text pushes the body well past " + b"y" * 120

    def _fake_urlopen(request: Any, timeout: float) -> _FakeHTTPResponse:
        return _FakeHTTPResponse(raw)

    monkeypatch.setattr(groq_demo_runtime.urllib.request, "urlopen", _fake_urlopen)
    with pytest.raises(RuntimeUnavailableError) as excinfo:
        runtime.run(_ROUTE, _make_call())
    message = str(excinfo.value)
    assert raw.decode()[:120] in message
    assert raw.decode()[:121] not in message
    assert _FAKE_KEY not in message


def test_empty_choices_raises_runtime_unavailable(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    runtime = GroqDemoRuntime()

    def _fake_urlopen(request: Any, timeout: float) -> _FakeHTTPResponse:
        return _FakeHTTPResponse(json.dumps({"choices": []}).encode())

    monkeypatch.setattr(groq_demo_runtime.urllib.request, "urlopen", _fake_urlopen)
    with pytest.raises(RuntimeUnavailableError):
        runtime.run(_ROUTE, _make_call())


def test_empty_content_raises_runtime_unavailable_with_snippet(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """An empty/whitespace-only reply is refused here, with a diagnostic
    snippet, rather than silently passed through as an empty proposed_code."""
    monkeypatch.setenv("GROQ_API_KEY", _FAKE_KEY)
    monkeypatch.setenv("GROQ_MODEL", _FAKE_MODEL)
    runtime = GroqDemoRuntime()
    raw = _chat_completion_body("   ")

    def _fake_urlopen(request: Any, timeout: float) -> _FakeHTTPResponse:
        return _FakeHTTPResponse(raw)

    monkeypatch.setattr(groq_demo_runtime.urllib.request, "urlopen", _fake_urlopen)
    with pytest.raises(RuntimeUnavailableError, match="empty") as excinfo:
        runtime.run(_ROUTE, _make_call())
    message = str(excinfo.value)
    assert raw.decode()[:120] in message
    assert _FAKE_KEY not in message


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
