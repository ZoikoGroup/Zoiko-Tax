"""Groq-backed ModelRuntime adapter -- DEMO ONLY.

DEMO ONLY. Never production. Synthetic public data only. No authority.

This adapter implements the same ``ModelRuntime`` seam as
``ztax_gateway.fake_runtime.FakeRuntime`` (``run(route, call) -> Result``), but
calls a real, provider-hosted model (Groq's OpenAI-compatible chat completions
endpoint) instead of returning a canned answer. It exists only to let a
one-off demo show a real model sitting behind the existing governed Gateway;
it is not wired into any server, not referenced from intelligence/src, and
carries no vault credential, egress allow-list entry or reviewed provider
approval (ADR-0006 S2.1, ADR-0017 S2.5). Deleting this file removes the
adapter entirely.

Standard library only -- no provider SDK. ``GROQ_API_KEY`` and ``GROQ_MODEL``
are read from the environment at construction time; a run that never got a
model configured never phones home. The key is used only inside the
``Authorization`` header of the one HTTPS request this module ever makes; it
is never printed, logged, or embedded in an exception message. Prompts are
likewise never printed or logged -- only lengths and classification results
belong in demo output (see live_demo.py).

``GROQ_MODEL`` must name one exact, pinned model string. The four floating
aliases REQ-0027 already forbids ("latest", "default", "stable", "preview")
are refused here too, before any call is made, for the same reason: a model
name that silently moves is not a model the operator actually reviewed.
"""

from __future__ import annotations

import json
import os
import urllib.error
import urllib.request

from ztax_gateway.runtime import Result, Route, RuntimeUnavailableError
from ztax_gateway.wire import Call, Kind

__all__: list[str] = [
    "GroqDemoConfigError",
    "GroqDemoRuntime",
    "build_request",
]

_API_URL = "https://api.groq.com/openai/v1/chat/completions"
_TIMEOUT_SECONDS = 30

# Groq returns 403 to the default Python-urllib User-Agent. A fixed,
# identifying value avoids that, and is not a secret.
_USER_AGENT = "ztax-live-demo/1.0"

# Mirrors production_registries.py's _MODEL_VERSION_ALIASES (REQ-0027): a
# model name that floats is not a model that was actually reviewed.
_FLOATING_ALIASES = frozenset({"latest", "default", "stable", "preview"})

# Groq does not return a calibrated probability in a chat completion. This is
# a fixed placeholder, not a model-derived value -- the same discipline
# FakeRuntime uses for its own canned confidence.
_FIXED_CONFIDENCE = "0.5000"

# Keeps the call fast and keeps max_tokens from ever truncating a short demo
# answer down to nothing (REQ item 2). "reasoning_effort": "low" is a Groq
# request field for reasoning-capable models; a model that does not recognise
# it ignores it.
_REASONING_EFFORT = "low"
_MAX_TOKENS = 64

# How many characters of a refused raw reply go into the diagnostic message
# (never the request body, never the key -- see run()).
_LOG_SNIPPET_CHARS = 120

# The real grammar the Go backend enforces for a proposed ontology node
# (backend/internal/domain/classification/classification.go, OntologyID.Validate):
# the string must start with "ontology:" and name something after it. That
# function does not check the proposal against any enumerated list of real
# nodes -- intelligence/src never gets a say either (check_result in
# runtime.py only requires a non-empty proposed_code). The one real node
# registry in this codebase (backend/internal/domain/ontology.Ontology) is
# built only in test code (ontology_test.go, golden_test.go) from the golden
# fixture backend/testdata/golden/classification/launch-corpus.json; nothing
# wires it into a running service. So there is no live, authoritative list of
# allowed ids this prompt could truthfully hand the model -- see
# LIVE_DEMO.md for the rest of this finding. This line states the one real,
# checkable constraint instead of a fabricated list.
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

# The three report-explanation use cases (registry.live-demo.json only; see
# report_ai_http.py and report_fixture.py) use Kind.EXTRACTION on the wire,
# but need a different system prompt and a different reply shape than the
# generic EXTRACTION case above, so they are matched by use case id rather
# than by kind.
_REPORT_EXPLANATION_USE_CASES: frozenset[str] = frozenset(
    {"explain-report-figure", "explain-obligation-row", "explain-shadow-variance"}
)

# A JSON reply needs more headroom than a short code or phrase.
_REPORT_EXPLANATION_MAX_TOKENS = 220

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


class GroqDemoConfigError(ValueError):
    """GROQ_API_KEY or GROQ_MODEL is missing or names a floating alias.

    Raised before any network call is made.
    """


def _env(name: str) -> str:
    return os.environ.get(name, "").strip()


def _resolve_config() -> tuple[str, str]:
    """Read and validate the two required environment variables.

    Raises GroqDemoConfigError -- and makes no call -- if either is missing
    or if GROQ_MODEL names a floating alias.
    """
    api_key = _env("GROQ_API_KEY")
    model = _env("GROQ_MODEL")
    if not api_key:
        raise GroqDemoConfigError(
            "GROQ_API_KEY is not set; the demo runtime will not be constructed "
            "and no call will be made"
        )
    if not model:
        raise GroqDemoConfigError(
            "GROQ_MODEL is not set; the demo runtime will not be constructed "
            "and no call will be made"
        )
    if model.lower() in _FLOATING_ALIASES:
        raise GroqDemoConfigError(
            f"GROQ_MODEL={model!r} is a floating alias; this demo refuses to "
            "pin an undeclared, silently-moving model (mirrors REQ-0027)"
        )
    return api_key, model


def build_request(model: str, api_key: str, call: Call) -> urllib.request.Request:
    """Build the HTTPS request for one call. Pure -- makes no network call.

    The request body pins ``model`` exactly as given (no alias resolution on
    the wire either). The API key appears only in the Authorization header
    of the returned Request object, never in a log or print statement.
    """
    if call.governance.use_case in _REPORT_EXPLANATION_USE_CASES:
        system = _REPORT_EXPLANATION_SYSTEM_PROMPT
        max_tokens = _REPORT_EXPLANATION_MAX_TOKENS
    else:
        system = _SYSTEM_PROMPTS[call.kind]
        max_tokens = _MAX_TOKENS
    user = call.input.decode("utf-8", errors="replace")
    payload = {
        "model": model,
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "temperature": 0,
        "reasoning_effort": _REASONING_EFFORT,
        "max_tokens": max_tokens,
    }
    data = json.dumps(payload).encode("utf-8")
    return urllib.request.Request(
        _API_URL,
        data=data,
        method="POST",
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {api_key}",
            "User-Agent": _USER_AGENT,
        },
    )


def _snippet(raw: bytes) -> str:
    """The first _LOG_SNIPPET_CHARS characters of a raw reply, decoded
    leniently -- for a diagnostic message only, never for parsing."""
    return raw.decode("utf-8", errors="replace")[:_LOG_SNIPPET_CHARS]


def _parse_report_explanation(content: str) -> dict[str, str]:
    """Parse a report-explanation model reply into ``Result.fields``.

    Raises ValueError -- caught in ``GroqDemoRuntime.run`` and turned into a
    RuntimeUnavailableError with the usual diagnostic snippet -- if the reply
    is not the required JSON shape. This is a malformed-*reply* condition,
    the same family as the chat-completion-shape check in ``run`` below; it
    is not the deterministic guard, which runs downstream of a successful
    Result, in report_ai_http.py, over real fixture rows this module never
    sees.
    """
    text = content.strip()
    if text.startswith("```"):
        text = text.strip("`")
        if text.startswith("json"):
            text = text[4:]
        text = text.strip()
    doc = json.loads(text)
    if not isinstance(doc, dict):
        raise ValueError("report-explanation reply is not a JSON object")
    explanation, cited_ids = doc.get("explanation"), doc.get("cited_ids")
    abstain, reason = doc.get("abstain"), doc.get("reason", "")
    if not isinstance(explanation, str):
        raise ValueError("report-explanation reply has no string 'explanation'")
    if not isinstance(cited_ids, list) or not all(isinstance(i, str) for i in cited_ids):
        raise ValueError("report-explanation reply has no string-list 'cited_ids'")
    if not isinstance(abstain, bool):
        raise ValueError("report-explanation reply has no boolean 'abstain'")
    if not isinstance(reason, str):
        raise ValueError("report-explanation reply has a non-string 'reason'")
    return {
        "explanation": explanation,
        "cited_ids": json.dumps(cited_ids),
        "abstain": "true" if abstain else "false",
        "reason": reason,
    }


def _result_from_content(call: Call, content: str) -> Result:
    """Map one model response string to a Result for *call*.

    *content* is never empty here -- ``GroqDemoRuntime.run`` already raises
    RuntimeUnavailableError on an empty reply, with the diagnostic snippet
    attached, before calling this function.
    """
    text = content.strip()
    if call.governance.use_case in _REPORT_EXPLANATION_USE_CASES:
        return Result(fields=_parse_report_explanation(content), confidence=_FIXED_CONFIDENCE)
    if call.kind == Kind.CLASSIFICATION_PROPOSAL:
        return Result(proposed_code=text, confidence=_FIXED_CONFIDENCE)
    if call.kind == Kind.EXTRACTION:
        fields = {"model_response": text} if text else {}
        return Result(fields=fields, confidence=_FIXED_CONFIDENCE)
    return Result(text=text, confidence=_FIXED_CONFIDENCE)


class GroqDemoRuntime:
    """DEMO ONLY ModelRuntime backed by a real Groq-hosted model.

    DEMO ONLY. Never production. Synthetic public data only. No authority.

    Implements the same ``run(route, call) -> Result`` seam as
    ``FakeRuntime``, so it can be substituted into an existing ``Gateway``
    with no other change. Reads GROQ_API_KEY and GROQ_MODEL once at
    construction time; raises GroqDemoConfigError immediately (before any
    network activity) if either is missing or GROQ_MODEL is a floating
    alias.
    """

    def __init__(self) -> None:
        self._api_key, self._model = _resolve_config()

    def run(self, route: Route, call: Call) -> Result:
        """Run one permitted call against the real, pinned Groq model.

        Raises RuntimeUnavailableError (the Gateway's own retryable-failure
        type) on a timeout, a network error, an HTTP error status, a
        response that is not shaped like a chat completion, or an empty
        reply -- never a raw urllib or JSON exception, so the Gateway's
        existing error mapping handles this runtime exactly like any other.
        Every one of those messages carries the first _LOG_SNIPPET_CHARS
        characters of the raw reply (never the request body, never the key)
        so a refusal is diagnosable from the gateway log alone.
        """
        request = build_request(self._model, self._api_key, call)
        try:
            with urllib.request.urlopen(request, timeout=_TIMEOUT_SECONDS) as response:
                raw = response.read()
        except urllib.error.HTTPError as exc:
            try:
                error_body = exc.read()
            except Exception:
                error_body = b""
            raise RuntimeUnavailableError(
                f"groq demo runtime: provider returned HTTP {exc.code}; first "
                f"{_LOG_SNIPPET_CHARS} chars of the reply: {_snippet(error_body)!r}"
            ) from exc
        except urllib.error.URLError as exc:
            raise RuntimeUnavailableError(
                f"groq demo runtime: network error: {exc.reason}"
            ) from exc
        except TimeoutError as exc:
            raise RuntimeUnavailableError(
                f"groq demo runtime: request timed out after {_TIMEOUT_SECONDS}s"
            ) from exc

        try:
            doc = json.loads(raw)
            content = doc["choices"][0]["message"]["content"]
            if not isinstance(content, str):
                raise TypeError("content is not a string")
        except (json.JSONDecodeError, KeyError, IndexError, TypeError) as exc:
            raise RuntimeUnavailableError(
                "groq demo runtime: provider response was not shaped like a "
                f"chat completion; first {_LOG_SNIPPET_CHARS} chars of the "
                f"reply: {_snippet(raw)!r}"
            ) from exc

        if not content.strip():
            raise RuntimeUnavailableError(
                "groq demo runtime: provider returned an empty reply; first "
                f"{_LOG_SNIPPET_CHARS} chars of the reply: {_snippet(raw)!r}"
            )

        try:
            return _result_from_content(call, content)
        except ValueError as exc:
            raise RuntimeUnavailableError(
                f"groq demo runtime: {exc}; first {_LOG_SNIPPET_CHARS} chars of "
                f"the reply: {_snippet(raw)!r}"
            ) from exc
