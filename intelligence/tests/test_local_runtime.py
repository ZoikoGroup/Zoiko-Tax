"""Tests for ztax_gateway.local_runtime (SH-5 skeleton).

The only server is a throwaway one bound to 127.0.0.1 on a random port, started inside
the test and always shut down. No key, no real provider, no other network. Synthetic data
only; sentinel strings stand in for an address, a prompt and an answer.
"""

from __future__ import annotations

import contextlib
import importlib.util
import json
import logging
import threading
import time
from collections.abc import Callable, Iterator
from contextlib import contextmanager
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from types import ModuleType
from typing import Any

import pytest

from ztax_gateway import local_runtime
from ztax_gateway.local_runtime import (
    ENDPOINT_ENV,
    MODEL_ENV,
    LocalModelRuntime,
    LocalRuntimeConfigError,
    build_payload,
)
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.runtime import Route, RuntimeUnavailableError
from ztax_gateway.wire import Call, Governance, Kind

_ROUTE = Route("model:local@1", "provider:local", "prompt:local-v1")
_MODEL = "local-pinned-model-7b"
ADDR_MARK = "ADDR-MARK-3c91e0"
PROMPT_MARK = "PROMPT-MARK-51ab77"
ANSWER_MARK = "ANSWER-MARK-d02f6e"

_GOOD_BODY = {
    "choices": [{"message": {"content": "ontology:demo/class"}}],
    "usage": {"prompt_tokens": 90, "completion_tokens": 7, "total_tokens": 97},
}


def _call(
    kind: Kind = Kind.CLASSIFICATION_PROPOSAL,
    text: str = '{"description":"demo sku"}',
    use_case: str = "classification-review",
) -> Call:
    return Call(
        kind=kind,
        governance=Governance(
            tenant_id="00000000-0000-0000-0000-000000000001",
            use_case=use_case,
            authority_outcome=AuthorityOutcome.A1,
            risk_tier=RiskTier.T1,
            region="local",
            data_classes=("P0",),
        ),
        subject_ref="sku:test",
        input=text.encode(),
    )


# ---------------------------------------------------------------------------
# A fake chat-completions server on 127.0.0.1:0
# ---------------------------------------------------------------------------


@dataclass
class _Seen:
    paths: list[str] = field(default_factory=list)
    headers: list[dict[str, str]] = field(default_factory=list)
    bodies: list[bytes] = field(default_factory=list)


@dataclass
class _Plan:
    status: int = 200
    body: bytes = b""
    delay: float = 0.0
    location: str | None = None


def _handler_for(plan: _Plan, seen: _Seen) -> type[BaseHTTPRequestHandler]:
    class Handler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:
            length = int(self.headers.get("Content-Length", "0"))
            seen.bodies.append(self.rfile.read(length))
            seen.paths.append(self.path)
            seen.headers.append({k.lower(): v for k, v in self.headers.items()})
            if plan.delay:
                time.sleep(plan.delay)
            self.send_response(plan.status)
            if plan.location is not None:
                self.send_header("Location", plan.location)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(plan.body)))
            self.end_headers()
            with contextlib.suppress(OSError):  # the client gave up (timeout test)
                self.wfile.write(plan.body)

        def do_GET(self) -> None:
            seen.paths.append(self.path)
            self.send_response(404)
            self.send_header("Content-Length", "0")
            self.end_headers()

        def log_message(self, format: str, *args: Any) -> None:
            return None

    return Handler


@contextmanager
def _server(plan: _Plan) -> Iterator[tuple[str, _Seen]]:
    seen = _Seen()
    httpd = ThreadingHTTPServer(("127.0.0.1", 0), _handler_for(plan, seen))
    httpd.daemon_threads = True
    thread = threading.Thread(
        target=httpd.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True
    )
    thread.start()
    try:
        yield f"http://127.0.0.1:{httpd.server_address[1]}", seen
    finally:
        httpd.shutdown()
        httpd.server_close()
        thread.join(timeout=5)


def _body(doc: object) -> bytes:
    return json.dumps(doc).encode()


def _runtime(monkeypatch: pytest.MonkeyPatch, endpoint: str) -> LocalModelRuntime:
    monkeypatch.setenv(ENDPOINT_ENV, endpoint)
    monkeypatch.setenv(MODEL_ENV, _MODEL)
    return LocalModelRuntime()


# ---------------------------------------------------------------------------
# Successful replies
# ---------------------------------------------------------------------------


def test_a_good_reply_with_usage_maps_to_a_result_with_counts(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    with _server(_Plan(body=_body(_GOOD_BODY))) as (url, seen):
        result = _runtime(monkeypatch, url).run(_ROUTE, _call())
    assert result.proposed_code == "ontology:demo/class"
    assert result.confidence == "0.5000"
    assert result.input_tokens == 90
    assert result.output_tokens == 7
    assert seen.paths == ["/v1/chat/completions"]


def test_a_reply_with_no_usage_gives_no_counts_and_still_succeeds(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    doc = {"choices": [{"message": {"content": "ontology:demo/class"}}]}
    with _server(_Plan(body=_body(doc))) as (url, _):
        result = _runtime(monkeypatch, url).run(_ROUTE, _call())
    assert result.proposed_code == "ontology:demo/class"
    assert result.input_tokens is None
    assert result.output_tokens is None


_BAD_USAGE: list[object] = [
    {"prompt_tokens": -3, "completion_tokens": -1},
    {"prompt_tokens": True, "completion_tokens": False},
    {"prompt_tokens": 1.5, "completion_tokens": 2.0},
    {"prompt_tokens": "90", "completion_tokens": "7"},
    {"prompt_tokens": 10**30, "completion_tokens": 10**30},
    {},
    [],
    "usage",
    None,
]


@pytest.mark.parametrize("usage", _BAD_USAGE, ids=[repr(u) for u in _BAD_USAGE])
def test_a_reply_with_bad_usage_gives_no_counts_and_still_succeeds(
    monkeypatch: pytest.MonkeyPatch, usage: object
) -> None:
    doc = {"choices": [{"message": {"content": "ontology:demo/class"}}], "usage": usage}
    with _server(_Plan(body=_body(doc))) as (url, _):
        result = _runtime(monkeypatch, url).run(_ROUTE, _call())
    assert result.proposed_code == "ontology:demo/class"
    assert result.input_tokens is None
    assert result.output_tokens is None


def test_a_base_url_with_a_path_and_trailing_slash_is_joined_cleanly(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    with _server(_Plan(body=_body(_GOOD_BODY))) as (url, seen):
        _runtime(monkeypatch, url + "/models/").run(_ROUTE, _call())
    assert seen.paths == ["/models/v1/chat/completions"]


# ---------------------------------------------------------------------------
# The request
# ---------------------------------------------------------------------------


def test_the_request_has_no_authorization_header_and_names_the_pinned_model(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    with _server(_Plan(body=_body(_GOOD_BODY))) as (url, seen):
        _runtime(monkeypatch, url).run(_ROUTE, _call())
    headers = seen.headers[0]
    assert "authorization" not in headers
    assert not any("key" in name or "token" in name for name in headers)
    sent = json.loads(seen.bodies[0])
    assert sent["model"] == _MODEL
    assert [m["role"] for m in sent["messages"]] == ["system", "user"]
    assert sent["messages"][1]["content"] == '{"description":"demo sku"}'


# ---------------------------------------------------------------------------
# Failures: fixed text, RuntimeUnavailableError
# ---------------------------------------------------------------------------


def _expect_unavailable(
    monkeypatch: pytest.MonkeyPatch, plan: _Plan, **kw: Any
) -> RuntimeUnavailableError:
    with _server(plan) as (url, _):
        runtime = _runtime(monkeypatch, url)
        with pytest.raises(RuntimeUnavailableError) as exc:
            runtime.run(_ROUTE, _call(**kw))
    return exc.value


def test_http_500_is_unavailable_with_the_status_code_only(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    err = _expect_unavailable(monkeypatch, _Plan(status=500, body=ANSWER_MARK.encode()))
    assert str(err) == "local runtime: provider returned HTTP 500"
    assert ANSWER_MARK not in str(err)


@pytest.mark.parametrize(
    "body",
    [
        b"not json at all " + ANSWER_MARK.encode(),
        _body({"nothing": ANSWER_MARK}),
        _body({"choices": []}),
        _body({"choices": [{"message": {}}]}),
        _body({"choices": [{"message": {"content": 12}}]}),
        _body([ANSWER_MARK]),
    ],
    ids=["not-json", "no-choices-key", "empty-choices", "no-content", "content-not-str", "array"],
)
def test_a_reply_not_shaped_like_a_chat_completion_is_unavailable(
    monkeypatch: pytest.MonkeyPatch, body: bytes
) -> None:
    err = _expect_unavailable(monkeypatch, _Plan(body=body))
    assert str(err) == "local runtime: provider response was not shaped like a chat completion"
    assert ANSWER_MARK not in str(err)


@pytest.mark.parametrize("content", ["", "   \n "])
def test_an_empty_reply_is_unavailable(monkeypatch: pytest.MonkeyPatch, content: str) -> None:
    doc = {"choices": [{"message": {"content": content}}]}
    err = _expect_unavailable(monkeypatch, _Plan(body=_body(doc)))
    assert str(err) == "local runtime: provider returned an empty reply"


def test_a_report_explanation_reply_that_is_not_the_required_json_is_unavailable(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    doc = {"choices": [{"message": {"content": ANSWER_MARK}}]}
    err = _expect_unavailable(
        monkeypatch,
        _Plan(body=_body(doc)),
        kind=Kind.EXTRACTION,
        use_case="explain-report-figure",
    )
    assert str(err) == "local runtime: provider reply was not in the required shape"
    assert ANSWER_MARK not in str(err)


def test_a_slow_reply_past_the_timeout_is_unavailable_and_quick(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(local_runtime, "_TIMEOUT_SECONDS", 0.2)
    started = time.monotonic()
    err = _expect_unavailable(monkeypatch, _Plan(body=_body(_GOOD_BODY), delay=1.0))
    assert time.monotonic() - started < 2.0
    assert str(err) == "local runtime: request timed out"


def test_a_redirect_is_refused_and_not_followed(monkeypatch: pytest.MonkeyPatch) -> None:
    with _server(_Plan(status=302, location="/elsewhere")) as (url, seen):
        runtime = _runtime(monkeypatch, url)
        with pytest.raises(RuntimeUnavailableError) as exc:
            runtime.run(_ROUTE, _call())
    assert str(exc.value) == "local runtime: the server answered with a redirect, which is refused"
    assert seen.paths == ["/v1/chat/completions"]  # /elsewhere was never requested


def test_a_refused_connection_is_a_network_error(monkeypatch: pytest.MonkeyPatch) -> None:
    with _server(_Plan()) as (url, _):
        runtime = _runtime(monkeypatch, url)
    # The server is shut down now: nothing listens on that port.
    with pytest.raises(RuntimeUnavailableError) as exc:
        runtime.run(_ROUTE, _call())
    assert str(exc.value) == "local runtime: network error"
    assert exc.value.__cause__ is None
    assert exc.value.__context__ is None


def test_an_oversized_reply_is_unavailable(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(local_runtime, "_MAX_REPLY_BYTES", 64)
    err = _expect_unavailable(monkeypatch, _Plan(body=_body(_GOOD_BODY)))
    assert str(err) == "local runtime: provider reply was too large"


# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------


def test_a_missing_endpoint_is_a_config_error(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv(ENDPOINT_ENV, raising=False)
    monkeypatch.setenv(MODEL_ENV, _MODEL)
    with pytest.raises(LocalRuntimeConfigError, match="ZTAX_LOCAL_RUNTIME_ENDPOINT is not set"):
        LocalModelRuntime()


def test_a_missing_model_is_a_config_error(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(ENDPOINT_ENV, "http://127.0.0.1:9")
    monkeypatch.delenv(MODEL_ENV, raising=False)
    with pytest.raises(LocalRuntimeConfigError, match="ZTAX_LOCAL_RUNTIME_MODEL is not set"):
        LocalModelRuntime()


@pytest.mark.parametrize("alias", ["latest", "default", "stable", "preview", "LATEST", " Stable "])
def test_a_floating_alias_model_is_refused(monkeypatch: pytest.MonkeyPatch, alias: str) -> None:
    monkeypatch.setenv(ENDPOINT_ENV, "http://127.0.0.1:9")
    monkeypatch.setenv(MODEL_ENV, alias)
    with pytest.raises(LocalRuntimeConfigError, match="floating alias"):
        LocalModelRuntime()


_BAD_ENDPOINTS = [
    f"http://user:pass-{ADDR_MARK}@127.0.0.1:9",
    f"http://user-{ADDR_MARK}@127.0.0.1:9",
    f"http://127.0.0.1:9/?q={ADDR_MARK}",
    "http://127.0.0.1:9?",
    f"http://127.0.0.1:9/#{ADDR_MARK}",
    f"ftp://127.0.0.1/{ADDR_MARK}",
    f"file:///etc/{ADDR_MARK}",
    f"{ADDR_MARK}",
    "http://",
    "https:///path-only",
    "http://127.0.0.1:notaport",
    "http://127.0.0.1:9/a b",
    "http://127.0.0.1:9/a\nb",
    "http://" + "a" * 70 + ".example:9",  # an IDNA label over 63 characters
    "http://a..b:9",  # an empty IDNA label
]


@pytest.mark.parametrize(
    "endpoint", _BAD_ENDPOINTS, ids=[f"bad-{i}" for i in range(len(_BAD_ENDPOINTS))]
)
def test_an_unacceptable_endpoint_is_refused_with_fixed_text(
    monkeypatch: pytest.MonkeyPatch, endpoint: str
) -> None:
    monkeypatch.setenv(ENDPOINT_ENV, endpoint)
    monkeypatch.setenv(MODEL_ENV, _MODEL)
    with pytest.raises(LocalRuntimeConfigError) as exc:
        LocalModelRuntime()
    assert ADDR_MARK not in str(exc.value)
    assert "127.0.0.1" not in str(exc.value)
    assert exc.value.__cause__ is None


@pytest.mark.parametrize("endpoint", ["http://127.0.0.1:9", "https://models.internal:8443/base"])
def test_http_and_https_endpoints_are_accepted(
    monkeypatch: pytest.MonkeyPatch, endpoint: str
) -> None:
    monkeypatch.setenv(ENDPOINT_ENV, endpoint)
    monkeypatch.setenv(MODEL_ENV, _MODEL)
    LocalModelRuntime()  # no network call is made by construction


# ---------------------------------------------------------------------------
# No address, prompt or answer in any message, cause or DEBUG log
# ---------------------------------------------------------------------------


def _all_text(err: BaseException, caplog: pytest.LogCaptureFixture) -> str:
    fmt = logging.Formatter()
    parts = [str(err), repr(err), repr(err.args)]
    parts += [fmt.format(r) + repr(vars(r)) for r in caplog.records]
    return "\n".join(parts)


_FAILURE_PLANS: list[tuple[str, _Plan]] = [
    ("http-500", _Plan(status=500, body=ANSWER_MARK.encode())),
    ("not-json", _Plan(body=ANSWER_MARK.encode())),
    ("empty", _Plan(body=_body({"choices": [{"message": {"content": ""}}]}))),
    ("redirect", _Plan(status=302, location=f"/{ANSWER_MARK}")),
]


@pytest.mark.parametrize(("name", "plan"), _FAILURE_PLANS, ids=[n for n, _ in _FAILURE_PLANS])
def test_no_address_prompt_or_answer_text_in_errors_or_debug_logs(
    monkeypatch: pytest.MonkeyPatch,
    caplog: pytest.LogCaptureFixture,
    name: str,
    plan: _Plan,
) -> None:
    caplog.set_level(logging.DEBUG)
    with _server(plan) as (url, _):
        # The address carries a sentinel in its path so a leak would be visible.
        runtime = _runtime(monkeypatch, f"{url}/{ADDR_MARK}")
        with pytest.raises(RuntimeUnavailableError) as exc:
            runtime.run(_ROUTE, _call(text=PROMPT_MARK))
    text = _all_text(exc.value, caplog)
    for mark in (ADDR_MARK, PROMPT_MARK, ANSWER_MARK, url, "127.0.0.1"):
        assert mark not in text, (name, mark)
    assert exc.value.__cause__ is None
    assert exc.value.__context__ is None


def test_a_successful_call_logs_no_prompt_or_answer_text(
    monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG)
    doc = {"choices": [{"message": {"content": f"ontology:{ANSWER_MARK}"}}]}
    with _server(_Plan(body=_body(doc))) as (url, _):
        _runtime(monkeypatch, f"{url}/{ADDR_MARK}").run(_ROUTE, _call(text=PROMPT_MARK))
    logged = "\n".join(
        logging.Formatter().format(r) + repr(vars(r)) for r in caplog.records
    )
    for mark in (ADDR_MARK, PROMPT_MARK, ANSWER_MARK):
        assert mark not in logged


# ---------------------------------------------------------------------------
# Same question as the Groq runtime (guards the copied prompts and mapping)
# ---------------------------------------------------------------------------

_GROQ_PATH = Path(__file__).resolve().parent.parent / "tools" / "groq_demo_runtime.py"


def _groq() -> ModuleType:
    spec = importlib.util.spec_from_file_location("groq_demo_runtime_parity", _GROQ_PATH)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


_PARITY_CALLS: list[Callable[[], Call]] = [
    lambda: _call(Kind.CLASSIFICATION_PROPOSAL),
    lambda: _call(Kind.EXTRACTION),
    lambda: _call(Kind.SUGGESTION),
    lambda: _call(Kind.EXTRACTION, use_case="explain-report-figure"),
    lambda: _call(Kind.SUGGESTION, use_case="explain-obligation-row"),
    lambda: _call(Kind.EXTRACTION, use_case="explain-shadow-variance"),
]


@pytest.mark.parametrize("make", _PARITY_CALLS, ids=[str(i) for i in range(len(_PARITY_CALLS))])
def test_the_messages_match_the_groq_runtime(make: Callable[[], Call]) -> None:
    groq = _groq()
    call = make()
    groq_request = groq.build_request(_MODEL, "synthetic-placeholder", call)
    groq_payload = json.loads(groq_request.data)
    ours = build_payload(_MODEL, call)
    assert ours["messages"] == groq_payload["messages"]
    assert ours["max_tokens"] == groq_payload["max_tokens"]
    assert ours["model"] == groq_payload["model"] == _MODEL


_PARITY_CONTENTS = [
    "ontology:telecom/voice/mobile",
    "  ontology:mobile  ",
    "a plain answer",
    '{"explanation": "x", "cited_ids": ["a"], "abstain": false, "reason": ""}',
    '```json\n{"explanation": "x", "cited_ids": [], "abstain": true, "reason": "r"}\n```',
    "not json",
]


@pytest.mark.parametrize("make", _PARITY_CALLS, ids=[str(i) for i in range(len(_PARITY_CALLS))])
@pytest.mark.parametrize("content", _PARITY_CONTENTS)
def test_the_reply_mapping_matches_the_groq_runtime(
    make: Callable[[], Call], content: str
) -> None:
    groq = _groq()
    call = make()
    try:
        expected = groq._result_from_content(call, content)
    except ValueError:
        with pytest.raises(ValueError):
            local_runtime._result_from_content(call, content)
        return
    assert local_runtime._result_from_content(call, content) == expected


def test_the_floating_alias_list_matches_the_groq_runtime() -> None:
    assert local_runtime._FLOATING_ALIASES == _groq()._FLOATING_ALIASES


# ---------------------------------------------------------------------------
# The skeleton is not wired
# ---------------------------------------------------------------------------

_ROOT = Path(__file__).resolve().parent.parent


def test_server_runtime_choice_and_gateway_factory_do_not_reference_local_runtime() -> None:
    for name in ("server.py", "gateway_factory.py"):
        text = (_ROOT / "src" / "ztax_gateway" / name).read_text(encoding="utf-8")
        assert "local_runtime" not in text, name
        assert "LocalModelRuntime" not in text, name
        assert ENDPOINT_ENV not in text, name


def test_no_other_source_or_tool_module_imports_local_runtime() -> None:
    own = _ROOT / "src" / "ztax_gateway" / "local_runtime.py"
    offenders = [
        str(path.relative_to(_ROOT))
        for folder in ("src", "tools")
        for path in (_ROOT / folder).rglob("*.py")
        if path != own and "local_runtime" in path.read_text(encoding="utf-8")
    ]
    assert offenders == []


def test_importing_the_package_does_not_import_local_runtime() -> None:
    import subprocess
    import sys

    code = (
        "import sys; sys.path.insert(0, 'src'); import ztax_gateway, ztax_gateway.server, "
        "ztax_gateway.gateway_factory; "
        "print('ztax_gateway.local_runtime' in sys.modules)"
    )
    out = subprocess.run(
        [sys.executable, "-c", code], cwd=_ROOT, capture_output=True, text=True, check=True
    )
    assert out.stdout.strip() == "False"


# ---------------------------------------------------------------------------
# Review fixes: nesting, huge integers, IDNA hosts, 3xx labels
# ---------------------------------------------------------------------------

_SHAPE = "local runtime: provider response was not shaped like a chat completion"
_REQUIRED_SHAPE = "local runtime: provider reply was not in the required shape"
_DIGITS = b"9" * 5000
_GOOD_PREFIX = b'{"choices":[{"message":{"content":"ontology:demo/class"}}],'


def _assert_clean(err: BaseException) -> None:
    assert err.__cause__ is None
    assert err.__context__ is None


_NESTED_REPLIES = {
    "nested-whole-reply": b"[" * 100_000,
    "nested-usage": _GOOD_PREFIX + b'"usage":' + b"[" * 100_000 + b"]" * 100_000 + b"}",
}


@pytest.mark.parametrize("body", _NESTED_REPLIES.values(), ids=list(_NESTED_REPLIES))
def test_a_deeply_nested_reply_is_unavailable_not_a_raw_recursion_error(
    monkeypatch: pytest.MonkeyPatch, body: bytes
) -> None:
    err = _expect_unavailable(monkeypatch, _Plan(body=body))
    assert str(err) == _SHAPE
    _assert_clean(err)


def test_a_deeply_nested_explain_reply_is_not_in_the_required_shape(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    doc = {"choices": [{"message": {"content": "[" * 100_000}}]}
    err = _expect_unavailable(
        monkeypatch,
        _Plan(body=_body(doc)),
        kind=Kind.EXTRACTION,
        use_case="explain-report-figure",
    )
    assert str(err) == _REQUIRED_SHAPE
    _assert_clean(err)


def test_a_5000_digit_prompt_token_count_does_not_fail_a_good_call(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    body = _GOOD_PREFIX + b'"usage":{"prompt_tokens":' + _DIGITS + b',"completion_tokens":1}}'
    with _server(_Plan(body=body)) as (url, _):
        result = _runtime(monkeypatch, url).run(_ROUTE, _call())
    assert result.proposed_code == "ontology:demo/class"
    assert result.input_tokens is None
    assert result.output_tokens == 1


def test_a_5000_digit_number_elsewhere_does_not_break_parsing(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    body = _GOOD_PREFIX + b'"created":' + _DIGITS + b',"usage":{"prompt_tokens":5}}'
    with _server(_Plan(body=body)) as (url, _):
        result = _runtime(monkeypatch, url).run(_ROUTE, _call())
    assert result.proposed_code == "ontology:demo/class"
    assert result.input_tokens == 5


def test_an_eighteen_digit_count_is_read_as_an_ordinary_number_and_then_range_checked(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # 18 digits parses as an int and is then rejected by the cap; it still does not fail the call.
    body = _GOOD_PREFIX + b'"usage":{"prompt_tokens":123456789012345678,"completion_tokens":2}}'
    with _server(_Plan(body=body)) as (url, _):
        result = _runtime(monkeypatch, url).run(_ROUTE, _call())
    assert result.input_tokens is None
    assert result.output_tokens == 2


def test_a_unicode_error_from_the_opener_is_a_fixed_network_error(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    class _Opener:
        def open(self, request: object, timeout: float) -> object:
            raise UnicodeEncodeError("idna", f"{ADDR_MARK}.example", 0, 5, "label too long")

    monkeypatch.setenv(ENDPOINT_ENV, f"http://{ADDR_MARK}.example:9")
    monkeypatch.setenv(MODEL_ENV, _MODEL)
    runtime = LocalModelRuntime()
    monkeypatch.setattr(local_runtime, "_opener", lambda: _Opener())
    with pytest.raises(RuntimeUnavailableError) as exc:
        runtime.run(_ROUTE, _call(text=PROMPT_MARK))
    assert str(exc.value) == "local runtime: network error"
    assert ADDR_MARK not in str(exc.value) + repr(exc.value)
    _assert_clean(exc.value)


@pytest.mark.parametrize("code", [301, 302, 303, 307, 308])
def test_a_real_redirect_status_gets_the_redirect_text(
    monkeypatch: pytest.MonkeyPatch, code: int
) -> None:
    err = _expect_unavailable(monkeypatch, _Plan(status=code, location="/elsewhere"))
    assert str(err) == "local runtime: the server answered with a redirect, which is refused"


@pytest.mark.parametrize("code", [300, 304, 305])
def test_another_3xx_status_gets_the_normal_http_status_text(
    monkeypatch: pytest.MonkeyPatch, code: int
) -> None:
    err = _expect_unavailable(monkeypatch, _Plan(status=code))
    assert str(err) == f"local runtime: provider returned HTTP {code}"
