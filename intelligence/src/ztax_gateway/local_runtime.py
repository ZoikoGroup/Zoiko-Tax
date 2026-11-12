"""LocalModelRuntime: a self-hosted, OpenAI-compatible model runtime (SH-5 skeleton).

SKELETON ONLY. Nothing imports this module: it is not wired into ``server._runtime()``,
``gateway_factory`` or any config. It exists so a self-hosted model server (one that
speaks the OpenAI chat-completions protocol) can be reviewed and tested before it is
ever routed to.

What it does, in the same shape as ``tools/groq_demo_runtime.py``:

* Config from the environment, read when the runtime is constructed:
  ``ZTAX_LOCAL_RUNTIME_ENDPOINT`` (base URL) and ``ZTAX_LOCAL_RUNTIME_MODEL`` (a pinned
  model name; a floating alias is refused). Both are required.
* One POST to ``<endpoint>/v1/chat/completions``. No key and no Authorization header:
  a self-hosted server on a private network needs none here.
* The reply is read from ``choices[0].message.content`` and mapped to a ``Result``;
  the provider's token counts ride along when valid (``runtime.token_count``).
* Every failure is a ``RuntimeUnavailableError`` with fixed text (plus an HTTP status
  code where there is one). No endpoint address, prompt text, answer text or provider
  body is ever in a message, a log line or a record. Redirects are refused.

The prompts and the reply mapping are copied from ``tools/groq_demo_runtime.py`` so both
runtimes ask the same question. ``src`` cannot import from ``tools``, and that file is
not edited by this change, so the small helpers are duplicated here;
``tests/test_local_runtime.py`` compares the two to catch drift.
"""

from __future__ import annotations

import http.client
import json
import logging
import os
import urllib.error
import urllib.parse
import urllib.request
from email.message import Message
from typing import IO, Final

from .runtime import Result, Route, RuntimeUnavailableError, json_int, token_count
from .wire import Call, Kind

__all__: list[str] = [
    "ENDPOINT_ENV",
    "MODEL_ENV",
    "LocalModelRuntime",
    "LocalRuntimeConfigError",
    "build_payload",
]

log = logging.getLogger("ztax_gateway.local_runtime")

ENDPOINT_ENV: Final = "ZTAX_LOCAL_RUNTIME_ENDPOINT"
MODEL_ENV: Final = "ZTAX_LOCAL_RUNTIME_MODEL"

# Per-request limit, like the Groq runtime. The Gateway's own 45 second deadline stays
# as it is and sits above this.
_TIMEOUT_SECONDS = 30

# A reply larger than this is not a chat completion this runtime will read.
_MAX_REPLY_BYTES = 1_048_576

_USER_AGENT = "ztax-local-runtime/1.0"
_COMPLETIONS_PATH = "/v1/chat/completions"

# The only statuses a redirect handler follows by default; this runtime refuses them.
# Any other 3xx (300, 304, 305, ...) is reported with the normal HTTP-status text.
_REDIRECT_STATUSES: Final = frozenset({301, 302, 303, 307, 308})

# Mirrors production_registries.py's _MODEL_VERSION_ALIASES (REQ-0027) and the Groq
# runtime's list: a model name that floats is not a model that was actually reviewed.
_FLOATING_ALIASES = frozenset({"latest", "default", "stable", "preview"})

# A fixed placeholder, not a model-derived value (same as the Groq runtime).
_FIXED_CONFIDENCE = "0.5000"
_MAX_TOKENS = 64
_REPORT_EXPLANATION_MAX_TOKENS = 220

# ---------------------------------------------------------------------------
# Fixed texts. None of them carries input, output, an address or a status line.
# ---------------------------------------------------------------------------

_ERR_NO_ENDPOINT: Final = f"local runtime: {ENDPOINT_ENV} is not set"
_ERR_NO_MODEL: Final = f"local runtime: {MODEL_ENV} is not set"
_ERR_ALIAS: Final = f"local runtime: {MODEL_ENV} is a floating alias and is refused"
_ERR_ENDPOINT: Final = (
    f"local runtime: {ENDPOINT_ENV} must be an http or https base URL with a host, "
    "and no credentials, query string or fragment"
)
_FAIL_TIMEOUT: Final = "local runtime: request timed out"
_FAIL_NETWORK: Final = "local runtime: network error"
_FAIL_REDIRECT: Final = "local runtime: the server answered with a redirect, which is refused"
_FAIL_TOO_LARGE: Final = "local runtime: provider reply was too large"
_FAIL_SHAPE: Final = "local runtime: provider response was not shaped like a chat completion"
_FAIL_EMPTY: Final = "local runtime: provider returned an empty reply"
_FAIL_REQUIRED_SHAPE: Final = "local runtime: provider reply was not in the required shape"


def _fail_http(status: int) -> str:
    return f"local runtime: provider returned HTTP {status}"


class LocalRuntimeConfigError(ValueError):
    """The endpoint or the model is missing or not acceptable.

    Raised before any network call. The text is fixed: it never echoes the value.
    """


# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------


def _env(name: str) -> str:
    return os.environ.get(name, "").strip()


def _validated_base_url(endpoint: str) -> str:
    """Return *endpoint* without a trailing slash, or raise LocalRuntimeConfigError."""
    if any(ch.isspace() or ord(ch) < 0x20 or ord(ch) == 0x7F for ch in endpoint):
        raise LocalRuntimeConfigError(_ERR_ENDPOINT)
    # "?" and "#" are checked on the raw text so an empty query ("http://h?") is refused too.
    if "?" in endpoint or "#" in endpoint:
        raise LocalRuntimeConfigError(_ERR_ENDPOINT)
    try:
        parts = urllib.parse.urlsplit(endpoint)
        _ = parts.port  # raises ValueError for a bad port
    except ValueError:
        raise LocalRuntimeConfigError(_ERR_ENDPOINT) from None
    if parts.scheme not in ("http", "https"):
        raise LocalRuntimeConfigError(_ERR_ENDPOINT)
    if not parts.hostname:
        raise LocalRuntimeConfigError(_ERR_ENDPOINT)
    # The host must be encodable by the idna codec the socket layer uses: a label over 63
    # characters or an empty label ("a..b") would otherwise fail later, inside a connect.
    try:
        parts.hostname.encode("idna")
    except UnicodeError:
        raise LocalRuntimeConfigError(_ERR_ENDPOINT) from None
    # No userinfo: "user:password@host" or "user@host".
    if "@" in parts.netloc or parts.username is not None or parts.password is not None:
        raise LocalRuntimeConfigError(_ERR_ENDPOINT)
    return endpoint.rstrip("/")


def _resolve_config() -> tuple[str, str]:
    """Read and validate the two required variables. Makes no network call."""
    endpoint = _env(ENDPOINT_ENV)
    model = _env(MODEL_ENV)
    if not endpoint:
        raise LocalRuntimeConfigError(_ERR_NO_ENDPOINT)
    if not model:
        raise LocalRuntimeConfigError(_ERR_NO_MODEL)
    if model.lower() in _FLOATING_ALIASES:
        raise LocalRuntimeConfigError(_ERR_ALIAS)
    return _validated_base_url(endpoint), model


# ---------------------------------------------------------------------------
# Prompts and reply mapping (copied from tools/groq_demo_runtime.py; see the module
# docstring). A parity test compares them.
# ---------------------------------------------------------------------------

_ONTOLOGY_GRAMMAR_HINT = (
    'A valid code has the exact form "ontology:<path>" (for example '
    '"ontology:telecom/voice/mobile") -- the prefix "ontology:" followed by '
    "a non-empty, lowercase, slash-separated path naming the product or "
    "service family. Reply with the code alone, nothing else."
)

_SYSTEM_PROMPTS: dict[Kind, str] = {
    Kind.CLASSIFICATION_PROPOSAL: (
        "You are a DEMO ONLY classification assistant with no authority over "
        "any real decision. Given a product or service description, propose "
        f"one ontology classification code. {_ONTOLOGY_GRAMMAR_HINT}"
    ),
    Kind.EXTRACTION: (
        "You are a DEMO ONLY extraction assistant with no authority over any "
        "real decision. Reply concisely with the extracted information only."
    ),
    Kind.SUGGESTION: (
        "You are a DEMO ONLY suggestion assistant with no authority over any "
        "real decision. Reply with a brief suggestion only."
    ),
}

_REPORT_EXPLANATION_USE_CASES: frozenset[str] = frozenset(
    {"explain-report-figure", "explain-obligation-row", "explain-shadow-variance"}
)

_REPORT_EXPLANATION_SYSTEM_PROMPT = (
    "You are a DEMO ONLY report-explanation assistant with no authority over any "
    "real decision and no tax authority. You are given a JSON array of rows from a "
    'synthetic reporting fixture; each row has an "id". Restate only the values and '
    "labels given in the rows. Do not calculate anything. Never introduce a total, "
    "subtotal, equivalent, average, difference or any other number that is not a "
    "given row field. Do not describe what a figure also shows unless a given field "
    "says so. Do not give causes or reasons for a figure. Give no tax advice. "
    "Write two or three short sentences. If the given rows cannot support an "
    'answer, set "abstain" to true and say why in "reason". Reply with JSON only, '
    "with exactly these keys and no others: "
    '{"explanation": "<string>", "cited_ids": ["<row id>", ...], "abstain": <bool>, '
    '"reason": "<string, may be empty>"}.'
)


def _system_prompt_for(use_case_id: str, kind: Kind) -> str:
    if use_case_id in _REPORT_EXPLANATION_USE_CASES:
        return _REPORT_EXPLANATION_SYSTEM_PROMPT
    return _SYSTEM_PROMPTS[kind]


def build_payload(model: str, call: Call) -> dict[str, object]:
    """The chat-completions request body for *call*. Pure; no network.

    The messages are the same two the Groq runtime sends (system prompt for the kind or
    use case, then the call input), and ``model`` is pinned exactly as given.
    """
    max_tokens = (
        _REPORT_EXPLANATION_MAX_TOKENS
        if call.governance.use_case in _REPORT_EXPLANATION_USE_CASES
        else _MAX_TOKENS
    )
    return {
        "model": model,
        "messages": [
            {"role": "system", "content": _system_prompt_for(call.governance.use_case, call.kind)},
            {"role": "user", "content": call.input.decode("utf-8", errors="replace")},
        ],
        "temperature": 0,
        "max_tokens": max_tokens,
    }


def _parse_report_explanation(content: str) -> dict[str, str]:
    """Parse a report-explanation reply into ``Result.fields``. Raises ValueError if the
    reply is not the required JSON shape (including a JSON syntax error)."""
    text = content.strip()
    if text.startswith("```"):
        text = text.strip("`")
        if text.startswith("json"):
            text = text[4:]
        text = text.strip()
    doc = json.loads(text)
    if not isinstance(doc, dict):
        raise ValueError("not a JSON object")
    explanation, cited_ids = doc.get("explanation"), doc.get("cited_ids")
    abstain, reason = doc.get("abstain"), doc.get("reason", "")
    if not isinstance(explanation, str):
        raise ValueError("no string explanation")
    if not isinstance(cited_ids, list) or not all(isinstance(i, str) for i in cited_ids):
        raise ValueError("no string-list cited_ids")
    if not isinstance(abstain, bool):
        raise ValueError("no boolean abstain")
    if not isinstance(reason, str):
        raise ValueError("non-string reason")
    return {
        "explanation": explanation,
        "cited_ids": json.dumps(cited_ids),
        "abstain": "true" if abstain else "false",
        "reason": reason,
    }


def _result_from_content(call: Call, content: str) -> Result:
    """Map one model response string to a Result for *call* (content is non-empty)."""
    text = content.strip()
    if call.governance.use_case in _REPORT_EXPLANATION_USE_CASES:
        return Result(fields=_parse_report_explanation(content), confidence=_FIXED_CONFIDENCE)
    if call.kind == Kind.CLASSIFICATION_PROPOSAL:
        return Result(proposed_code=text, confidence=_FIXED_CONFIDENCE)
    if call.kind == Kind.EXTRACTION:
        fields = {"model_response": text} if text else {}
        return Result(fields=fields, confidence=_FIXED_CONFIDENCE)
    return Result(text=text, confidence=_FIXED_CONFIDENCE)


def _usage_counts(doc: object) -> tuple[int | None, int | None]:
    """(prompt, completion) token counts from a decoded reply; None for a side that is
    missing or not a non-negative whole number. Never raises."""
    if not isinstance(doc, dict):
        return None, None
    usage = doc.get("usage")
    if not isinstance(usage, dict):
        return None, None
    return token_count(usage.get("prompt_tokens")), token_count(usage.get("completion_tokens"))


# ---------------------------------------------------------------------------
# HTTP
# ---------------------------------------------------------------------------


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """Refuses every redirect: the 3xx reply surfaces as an HTTPError instead."""

    def redirect_request(
        self,
        req: urllib.request.Request,
        fp: IO[bytes],
        code: int,
        msg: str,
        headers: Message,
        newurl: str,
    ) -> None:
        return None


def _opener() -> urllib.request.OpenerDirector:
    # No redirects, and no proxy picked up from the environment: this runtime talks to
    # the one endpoint it was configured with, directly.
    return urllib.request.build_opener(urllib.request.ProxyHandler({}), _NoRedirect())


class LocalModelRuntime:
    """A ModelRuntime backed by a self-hosted, OpenAI-compatible chat-completions server."""

    def __init__(self) -> None:
        # Raises LocalRuntimeConfigError, with no network call, if the config is not usable.
        self._base_url, self._model = _resolve_config()

    def run(self, route: Route, call: Call) -> Result:
        """Run one permitted call. Raises RuntimeUnavailableError (fixed text) on every
        failure; never provider output, never the endpoint address."""
        payload = json.dumps(build_payload(self._model, call)).encode("utf-8")
        request = urllib.request.Request(
            self._base_url + _COMPLETIONS_PATH,
            data=payload,
            method="POST",
            headers={"Content-Type": "application/json", "User-Agent": _USER_AGENT},
        )

        # Failures are recorded in ``failure`` and raised after the try statement, so the
        # error carries no __cause__ or __context__ (those can hold the address).
        failure: str | None = None
        raw = b""
        try:
            with _opener().open(request, timeout=_TIMEOUT_SECONDS) as response:
                raw = response.read(_MAX_REPLY_BYTES + 1)
        except urllib.error.HTTPError as exc:
            failure = _FAIL_REDIRECT if exc.code in _REDIRECT_STATUSES else _fail_http(exc.code)
            exc.close()
        except urllib.error.URLError:
            failure = _FAIL_NETWORK
        except TimeoutError:
            failure = _FAIL_TIMEOUT
        except (OSError, http.client.HTTPException, UnicodeError):
            # UnicodeError: a host the idna codec rejects at connect time. Not an OSError.
            failure = _FAIL_NETWORK
        if failure is not None:
            log.debug(failure)
            raise RuntimeUnavailableError(failure)
        if len(raw) > _MAX_REPLY_BYTES:
            log.debug(_FAIL_TOO_LARGE)
            raise RuntimeUnavailableError(_FAIL_TOO_LARGE)

        content = ""
        doc: object = None
        try:
            doc = json.loads(raw, parse_int=json_int)
            if not isinstance(doc, dict):
                raise TypeError
            content = doc["choices"][0]["message"]["content"]
            if not isinstance(content, str):
                raise TypeError
        except (ValueError, RecursionError, KeyError, IndexError, TypeError):
            failure = _FAIL_SHAPE
        if failure is None and not content.strip():
            failure = _FAIL_EMPTY
        result: Result | None = None
        if failure is None:
            try:
                result = _result_from_content(call, content)
            except (ValueError, RecursionError):
                failure = _FAIL_REQUIRED_SHAPE
        if failure is not None or result is None:
            log.debug(failure or _FAIL_REQUIRED_SHAPE)
            raise RuntimeUnavailableError(failure or _FAIL_REQUIRED_SHAPE)

        input_tokens, output_tokens = _usage_counts(doc)
        return Result(
            text=result.text,
            payload=result.payload,
            fields=result.fields,
            proposed_code=result.proposed_code,
            confidence=result.confidence,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
        )
