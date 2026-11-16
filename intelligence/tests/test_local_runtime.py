"""Tests for ztax_gateway.local_runtime (SH-5 skeleton).

The only server is a throwaway one bound to 127.0.0.1 on a random port, started inside
the test and always shut down. No key, no real provider, no other network. Synthetic data
only; sentinel strings stand in for an address, a prompt and an answer.
"""

from __future__ import annotations

import contextlib
import http.client
import importlib.util
import json
import logging
import socket
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


# ---------------------------------------------------------------------------
# The total time for one request (connect and every read included)
# ---------------------------------------------------------------------------

_TIMEOUT_TEXT = "local runtime: request timed out"
_Behaviour = Callable[[BaseHTTPRequestHandler], None]


def _scripted_handler(
    behaviours: list[_Behaviour], counter: list[int]
) -> type[BaseHTTPRequestHandler]:
    """A server whose n-th request does behaviours[n] (the last one repeats)."""

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:
            self.rfile.read(int(self.headers.get("Content-Length", "0")))
            index = min(counter[0], len(behaviours) - 1)
            counter[0] += 1
            with contextlib.suppress(OSError):  # the client gave up
                behaviours[index](self)

        def log_message(self, format: str, *args: Any) -> None:
            return None

    return Handler


@contextmanager
def _scripted_server(*behaviours: _Behaviour) -> Iterator[str]:
    counter = [0]
    httpd = ThreadingHTTPServer(("127.0.0.1", 0), _scripted_handler(list(behaviours), counter))
    httpd.daemon_threads = True
    thread = threading.Thread(
        target=httpd.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True
    )
    thread.start()
    try:
        yield f"http://127.0.0.1:{httpd.server_address[1]}"
    finally:
        httpd.shutdown()
        httpd.server_close()
        thread.join(timeout=5)


def _good(h: BaseHTTPRequestHandler) -> None:
    body = _body(_GOOD_BODY)
    h.send_response(200)
    h.send_header("Content-Length", str(len(body)))
    h.end_headers()
    h.wfile.write(body)


def _trickle_body(interval: float, seconds: float = 8.0) -> _Behaviour:
    """Headers at once, then one byte of a reply that never finishes, every ``interval``."""

    def behave(h: BaseHTTPRequestHandler) -> None:
        count = int(seconds / interval)
        h.send_response(200)
        h.send_header("Content-Length", str(count))
        h.end_headers()
        for _ in range(count):
            h.wfile.write(b" ")
            time.sleep(interval)

    return behave


def _trickle_headers(interval: float, seconds: float = 8.0) -> _Behaviour:
    """The status line and headers, one byte at a time, never finishing."""

    def behave(h: BaseHTTPRequestHandler) -> None:
        for _ in range(int(seconds / interval)):
            h.wfile.write(b"H")
            time.sleep(interval)

    return behave


def _silent(seconds: float = 8.0) -> _Behaviour:
    """Takes the request and never answers."""

    def behave(h: BaseHTTPRequestHandler) -> None:
        time.sleep(seconds)

    return behave


def _paced_good(duration: float, pieces: int = 10) -> _Behaviour:
    """A good reply sent in ``pieces`` parts spread over ``duration`` seconds."""

    def behave(h: BaseHTTPRequestHandler) -> None:
        body = _body(_GOOD_BODY)
        step = max(1, len(body) // pieces)
        h.send_response(200)
        h.send_header("Content-Length", str(len(body)))
        h.end_headers()
        for start in range(0, len(body), step):
            h.wfile.write(body[start : start + step])
            time.sleep(duration / pieces)

    return behave


def _sized_good(size: int) -> _Behaviour:
    """A valid chat completion padded with spaces to exactly ``size`` bytes."""

    def behave(h: BaseHTTPRequestHandler) -> None:
        body = _body(_GOOD_BODY)
        body += b" " * (size - len(body))
        h.send_response(200)
        h.send_header("Content-Length", str(len(body)))
        h.end_headers()
        h.wfile.write(body)

    return behave


def _timed_run(
    monkeypatch: pytest.MonkeyPatch, url: str, *, total: float
) -> tuple[RuntimeUnavailableError, float]:
    monkeypatch.setattr(local_runtime, "_TOTAL_SECONDS", total)
    runtime = _runtime(monkeypatch, url)
    started = time.monotonic()
    with pytest.raises(RuntimeUnavailableError) as exc:
        runtime.run(_ROUTE, _call())
    return exc.value, time.monotonic() - started


def _timer_threads() -> list[threading.Thread]:
    return [t for t in threading.enumerate() if isinstance(t, threading.Timer)]


def _wait_for_no_timers() -> None:
    deadline = time.monotonic() + 2.0
    while _timer_threads() and time.monotonic() < deadline:
        time.sleep(0.01)


@pytest.mark.parametrize(
    "behaviour",
    [_trickle_body(0.05), _trickle_headers(0.05)],
    ids=["slow-trickle-body", "slow-trickle-headers"],
)
def test_a_reply_that_keeps_arriving_slowly_ends_as_a_timeout_at_the_total(
    monkeypatch: pytest.MonkeyPatch, behaviour: _Behaviour
) -> None:
    # The per-read limit stays 30 s: a byte every 0.05 s never trips it, only the total can.
    with _scripted_server(behaviour) as url:
        err, elapsed = _timed_run(monkeypatch, url, total=1.0)
    assert str(err) == _TIMEOUT_TEXT
    assert 0.9 <= elapsed < 4.0, elapsed  # the server would trickle for 8 s


def test_a_server_that_never_replies_ends_as_a_timeout_at_the_total_not_the_read_limit(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    with _scripted_server(_silent()) as url:
        err, elapsed = _timed_run(monkeypatch, url, total=1.0)
    assert str(err) == _TIMEOUT_TEXT
    assert 0.9 <= elapsed < 4.0, elapsed  # the server would stay silent for 8 s


def test_a_fast_reply_is_unchanged_and_leaves_no_timer_behind(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(local_runtime, "_TOTAL_SECONDS", 30.0)
    with _scripted_server(_good) as url:
        result = _runtime(monkeypatch, url).run(_ROUTE, _call())
    assert result.proposed_code == "ontology:demo/class"
    assert result.input_tokens == 90
    assert result.output_tokens == 7
    _wait_for_no_timers()
    assert _timer_threads() == []


def test_a_reply_that_finishes_inside_the_total_is_accepted(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(local_runtime, "_TOTAL_SECONDS", 2.0)
    with _scripted_server(_paced_good(0.5)) as url:  # takes a quarter of the total
        result = _runtime(monkeypatch, url).run(_ROUTE, _call())
    assert result.proposed_code == "ontology:demo/class"


def test_a_slower_reply_past_the_total_is_a_timeout(monkeypatch: pytest.MonkeyPatch) -> None:
    with _scripted_server(_paced_good(5.0)) as url:  # would take five times the total
        err, elapsed = _timed_run(monkeypatch, url, total=1.0)
    assert str(err) == _TIMEOUT_TEXT
    assert elapsed < 3.0, elapsed


def test_the_deadline_is_over_exactly_at_the_total_by_the_monotonic_clock(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    now = [100.0]
    monkeypatch.setattr(local_runtime, "_monotonic", lambda: now[0])
    deadline = local_runtime._Deadline(10.0)
    assert deadline.remaining() == pytest.approx(10.0)
    assert not deadline.over()
    now[0] = 109.999
    assert not deadline.over()
    now[0] = 110.0
    assert deadline.over()
    now[0] = 500.0
    assert deadline.over()
    assert not deadline.expired.is_set()  # the clock alone decides; the timer was never started


def test_a_reply_at_the_one_mebibyte_cap_is_accepted_and_one_byte_more_is_too_large(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    cap = local_runtime._MAX_REPLY_BYTES
    assert cap == 1_048_576
    monkeypatch.setattr(local_runtime, "_TOTAL_SECONDS", 30.0)
    with _scripted_server(_sized_good(cap)) as url:
        result = _runtime(monkeypatch, url).run(_ROUTE, _call())
    assert result.proposed_code == "ontology:demo/class"
    with _scripted_server(_sized_good(cap + 1)) as url:
        runtime = _runtime(monkeypatch, url)
        with pytest.raises(RuntimeUnavailableError) as exc:
            runtime.run(_ROUTE, _call())
    assert str(exc.value) == "local runtime: provider reply was too large"


def test_a_redirect_is_still_refused_under_the_total(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(local_runtime, "_TOTAL_SECONDS", 5.0)
    err = _expect_unavailable(monkeypatch, _Plan(status=307, location="/elsewhere"))
    assert str(err) == "local runtime: the server answered with a redirect, which is refused"


def test_the_total_timeout_text_has_no_address_prompt_or_reply_text(
    monkeypatch: pytest.MonkeyPatch, caplog: pytest.LogCaptureFixture
) -> None:
    caplog.set_level(logging.DEBUG)

    def leaky_trickle(h: BaseHTTPRequestHandler) -> None:
        h.send_response(200)
        h.send_header("Content-Length", "1000")
        h.end_headers()
        for ch in (ANSWER_MARK + " " * 900).encode():
            h.wfile.write(bytes([ch]))
            time.sleep(0.02)

    with _scripted_server(leaky_trickle) as url:
        monkeypatch.setattr(local_runtime, "_TOTAL_SECONDS", 1.0)
        runtime = _runtime(monkeypatch, f"{url}/{ADDR_MARK}")
        with pytest.raises(RuntimeUnavailableError) as exc:
            runtime.run(_ROUTE, _call(text=PROMPT_MARK))
    assert str(exc.value) == _TIMEOUT_TEXT
    text = _all_text(exc.value, caplog)
    for mark in (ADDR_MARK, PROMPT_MARK, ANSWER_MARK, url, "127.0.0.1"):
        assert mark not in text, mark
    assert exc.value.__cause__ is None
    assert exc.value.__context__ is None


def test_the_runtime_is_usable_again_after_a_timeout(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(local_runtime, "_TOTAL_SECONDS", 1.0)
    with _scripted_server(_trickle_body(0.05), _good) as url:
        runtime = _runtime(monkeypatch, url)
        with pytest.raises(RuntimeUnavailableError) as first:
            runtime.run(_ROUTE, _call())
        assert str(first.value) == _TIMEOUT_TEXT
        second = runtime.run(_ROUTE, _call())  # the same object, a fresh budget
        third = runtime.run(_ROUTE, _call())
    assert second.proposed_code == third.proposed_code == "ontology:demo/class"
    _wait_for_no_timers()
    assert _timer_threads() == []


def test_the_reading_loop_stops_as_soon_as_the_clock_says_the_total_is_over(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # The real timer is set far away (30 s); only the loop's own check against the
    # monotonic clock can end this early. The clock jumps while the reply trickles.
    now = [0.0]
    monkeypatch.setattr(local_runtime, "_monotonic", lambda: now[0])
    monkeypatch.setattr(local_runtime, "_TOTAL_SECONDS", 30.0)
    jumper = threading.Timer(0.3, lambda: now.__setitem__(0, 1_000_000.0))
    jumper.start()
    try:
        with _scripted_server(_trickle_body(0.05)) as url:
            runtime = _runtime(monkeypatch, url)
            started = time.monotonic()
            with pytest.raises(RuntimeUnavailableError) as exc:
                runtime.run(_ROUTE, _call())
            elapsed = time.monotonic() - started
    finally:
        jumper.cancel()
    assert str(exc.value) == _TIMEOUT_TEXT
    assert elapsed < 5.0, elapsed  # without the loop's check the trickle runs for 8 s


def test_a_total_that_is_already_over_ends_as_the_timeout_text_and_sends_nothing(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(local_runtime, "_monotonic", lambda: 50.0)
    monkeypatch.setattr(local_runtime, "_TOTAL_SECONDS", 0.0)
    with _server(_Plan(body=_body(_GOOD_BODY))) as (url, seen):
        runtime = _runtime(monkeypatch, url)
        with pytest.raises(RuntimeUnavailableError) as exc:
            runtime.run(_ROUTE, _call())
    assert str(exc.value) == _TIMEOUT_TEXT
    assert seen.paths == []


@pytest.mark.parametrize(
    ("scheme", "handler_cls", "method", "base"),
    [
        ("http", local_runtime._DeadlineHTTPHandler, "http_open", http.client.HTTPConnection),
        ("https", local_runtime._DeadlineHTTPSHandler, "https_open", http.client.HTTPSConnection),
    ],
)
def test_both_schemes_open_their_connections_under_the_request_deadline(
    monkeypatch: pytest.MonkeyPatch,
    scheme: str,
    handler_cls: type[Any],
    method: str,
    base: type[http.client.HTTPConnection],
) -> None:
    deadline = local_runtime._Deadline(5.0)
    handler = handler_cls()
    seen: list[Any] = []

    def fake_do_open(make: Callable[..., Any], req: object, **kw: Any) -> str:
        seen.append(make)
        return "opened"

    monkeypatch.setattr(handler, "do_open", fake_do_open)
    request = local_runtime._DeadlineRequest(f"{scheme}://127.0.0.1:9/x", deadline=deadline)
    assert getattr(handler, method)(request) == "opened"
    connection = seen[0]("127.0.0.1", timeout=1)
    assert isinstance(connection, base)
    assert isinstance(connection, local_runtime._DeadlineConnection)
    assert connection._deadline is deadline
    # The opener the runtime really uses carries both handlers.
    opener: Any = local_runtime._opener()
    kinds = {type(h) for h in opener.handlers}
    assert local_runtime._DeadlineHTTPHandler in kinds
    assert local_runtime._DeadlineHTTPSHandler in kinds


class _FakeResponse:
    """Hands out its parts one read at a time, then an empty read (the end). ``on_end`` runs
    just before the empty read, as the timer does when it fires during a blocked read."""

    def __init__(self, *parts: bytes, on_end: Callable[[], None] | None = None) -> None:
        self._parts = list(parts)
        self._on_end = on_end

    def read1(self, n: int) -> bytes:
        if self._parts:
            return self._parts.pop(0)[:n]
        if self._on_end is not None:
            self._on_end()
        return b""


def test_an_end_of_reply_that_the_deadline_caused_is_a_timeout_not_a_short_reply() -> None:
    deadline = local_runtime._Deadline(60.0)
    # The timer fires while a read is blocked: the socket is shut and the read comes back empty.
    response = _FakeResponse(b"ab", on_end=deadline.expired.set)
    with pytest.raises(TimeoutError):
        local_runtime._read_within(response, deadline)  # type: ignore[arg-type]
    # Without a timer firing, the same empty read is simply the end of the reply.
    calm = local_runtime._Deadline(60.0)
    assert local_runtime._read_within(_FakeResponse(b"ab", b"cd"), calm) == b"abcd"  # type: ignore[arg-type]


def test_the_total_is_measured_with_the_monotonic_clock_not_the_wall_clock(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    with monkeypatch.context() as m:
        m.setattr(time, "monotonic", lambda: 7.0)
        m.setattr(time, "time", lambda: 9.0)
        assert local_runtime._monotonic() == 7.0


# ---------------------------------------------------------------------------
# After the headers: the opener drops its own socket reference, the deadline keeps one
# ---------------------------------------------------------------------------


def _stall_after(prefix: int, late_at: float) -> _Behaviour:
    """Headers, ``prefix`` bytes at once, one more byte at ``late_at`` seconds, then silence."""

    def behave(h: BaseHTTPRequestHandler) -> None:
        h.send_response(200)
        h.send_header("Content-Length", "900000")
        h.end_headers()
        if prefix:
            h.wfile.write(b" " * prefix)
        time.sleep(late_at)
        h.wfile.write(b" ")
        time.sleep(8)

    return behave


_STALL_CASES = [
    (0, "both"),
    (0, "no-shutdown"),
    (200_000, "both"),
    (200_000, "no-shutdown"),
]


@pytest.mark.parametrize(
    ("prefix", "mode"),
    _STALL_CASES,
    ids=[f"{'large-partial-body' if p else 'one-byte'}-{m}" for p, m in _STALL_CASES],
)
def test_a_body_that_stalls_after_the_headers_ends_at_the_total_not_after_a_further_read_limit(
    monkeypatch: pytest.MonkeyPatch, prefix: int, mode: str
) -> None:
    # The last byte comes at 1.3 s of a 1.5 s total, then silence. A blocked read that is
    # only bound by a per-read limit would end at 1.3 + 1.5 = 2.8 s. The read limit cut to
    # what is left must end it even when the shutdown does nothing ("no-shutdown": on
    # Windows a shutdown does not wake a read that waits with a timeout, on Linux it does).
    if mode == "no-shutdown":
        monkeypatch.setattr(local_runtime._Deadline, "_abort", lambda self: None)
    with _scripted_server(_stall_after(prefix, 1.3)) as url:
        err, elapsed = _timed_run(monkeypatch, url, total=1.5)
    assert str(err) == _TIMEOUT_TEXT
    assert 1.4 <= elapsed < 2.2, elapsed


def test_connect_raises_before_any_socket_is_opened_when_no_time_is_left(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(local_runtime, "_monotonic", lambda: 50.0)

    def no_socket(*args: Any, **kwargs: Any) -> None:
        raise AssertionError("a socket was opened")

    monkeypatch.setattr(socket, "create_connection", no_socket)
    connection = local_runtime._DeadlineConnection(
        "127.0.0.1", 9, deadline=local_runtime._Deadline(0.0)
    )
    with pytest.raises(TimeoutError):
        connection.connect()
    assert connection.sock is None


@pytest.mark.parametrize(("total", "expected"), [(5.0, 5.0), (100.0, 30.0)])
def test_connect_is_given_what_is_left_but_never_more_than_the_read_limit(
    monkeypatch: pytest.MonkeyPatch, total: float, expected: float
) -> None:
    monkeypatch.setattr(local_runtime, "_monotonic", lambda: 50.0)
    timeouts: list[float] = []

    def refuse(address: object, timeout: float, *rest: Any) -> None:
        timeouts.append(timeout)
        raise ConnectionRefusedError

    monkeypatch.setattr(socket, "create_connection", refuse)
    connection = local_runtime._DeadlineConnection(
        "127.0.0.1", 9, deadline=local_runtime._Deadline(total)
    )
    with pytest.raises(ConnectionRefusedError):
        connection.connect()
    assert timeouts == [expected]


class _ReadRaisesValueError:
    """An opener whose reply raises ValueError on the first read, as a TLS socket does when
    the deadline has shut it down. ``expire`` says whether the total has passed by then."""

    def __init__(self, expire: bool) -> None:
        self._expire = expire

    def open(self, request: Any, timeout: float) -> Any:
        expire = self._expire

        class Reply:
            def __enter__(self) -> Reply:
                return self

            def __exit__(self, *exc: object) -> None:
                return None

            def read1(self, n: int) -> bytes:
                if expire:
                    request.deadline.expired.set()
                raise ValueError("Read on closed or unwrapped SSL socket.")

        return Reply()


def test_a_value_error_from_a_read_after_the_total_is_the_timeout_text(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    opener = _ReadRaisesValueError(expire=True)
    monkeypatch.setattr(local_runtime, "_opener", lambda: opener)
    runtime = _runtime(monkeypatch, "http://127.0.0.1:9")
    with pytest.raises(RuntimeUnavailableError) as exc:
        runtime.run(_ROUTE, _call())
    assert str(exc.value) == _TIMEOUT_TEXT
    assert exc.value.__cause__ is None
    assert exc.value.__context__ is None


def test_a_value_error_from_a_read_before_the_total_is_not_hidden(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    opener = _ReadRaisesValueError(expire=False)
    monkeypatch.setattr(local_runtime, "_opener", lambda: opener)
    runtime = _runtime(monkeypatch, "http://127.0.0.1:9")
    with pytest.raises(ValueError, match="unwrapped SSL socket"):
        runtime.run(_ROUTE, _call())
    _wait_for_no_timers()
    assert _timer_threads() == []


def test_a_timer_that_cannot_start_ends_as_a_fixed_error_and_sends_nothing(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    def cannot_start(self: threading.Timer) -> None:
        raise RuntimeError(f"can't start new thread {ADDR_MARK}")

    monkeypatch.setattr(threading.Timer, "start", cannot_start)
    with _server(_Plan(body=_body(_GOOD_BODY))) as (url, seen):
        runtime = _runtime(monkeypatch, url)
        with pytest.raises(RuntimeUnavailableError) as exc:
            runtime.run(_ROUTE, _call())
    assert str(exc.value) == "local runtime: the time limit could not be started"
    assert ADDR_MARK not in repr(exc.value)
    assert exc.value.__cause__ is None
    assert exc.value.__context__ is None
    assert seen.paths == []  # nothing was sent
    assert _timer_threads() == []  # and nothing is left running


def test_the_timer_shuts_down_the_socket_the_deadline_guards() -> None:
    mine, theirs = socket.socketpair()
    try:
        deadline = local_runtime._Deadline(60.0)
        deadline.guard(mine)
        deadline._fire()  # what the timer does at the total
        assert deadline.expired.is_set()
        # The socket is shut down in both directions: sending now fails on every platform.
        # (Whether a read that waits with a timeout is woken by it differs: Linux yes,
        # Windows no, which is why each read is also given only what is left of the total.)
        with pytest.raises(OSError):
            mine.send(b"x")
    finally:
        mine.close()
        theirs.close()


def test_the_deadline_keeps_the_connected_socket_after_the_opener_lets_go_of_it() -> None:
    with _scripted_server(_good) as url:
        port = int(url.rsplit(":", 1)[1])
        deadline = local_runtime._Deadline(60.0)
        connection = local_runtime._DeadlineConnection("127.0.0.1", port, deadline=deadline)
        try:
            connection.connect()
            held = connection.sock
            assert held is not None
            assert deadline._sock is held
            connection.sock = None  # what urllib does as soon as the headers are in
            assert deadline._sock is held
        finally:
            if held is not None:
                held.close()


def test_the_loop_reads_nothing_once_the_clock_says_the_total_is_over(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    now = [0.0]
    monkeypatch.setattr(local_runtime, "_monotonic", lambda: now[0])
    deadline = local_runtime._Deadline(10.0)
    response = _FakeResponse(b"ab", b"cd")
    now[0] = 10.0  # the total is over; no socket, no timer: only the loop's own check is left
    with pytest.raises(TimeoutError):
        local_runtime._read_within(response, deadline)  # type: ignore[arg-type]
    assert response._parts == [b"ab", b"cd"]  # not one read was made
