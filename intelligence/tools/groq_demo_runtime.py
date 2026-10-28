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

# Mirrors production_registries.py's _MODEL_VERSION_ALIASES (REQ-0027): a
# model name that floats is not a model that was actually reviewed.
_FLOATING_ALIASES = frozenset({"latest", "default", "stable", "preview"})

# Groq does not return a calibrated probability in a chat completion. This is
# a fixed placeholder, not a model-derived value -- the same discipline
# FakeRuntime uses for its own canned confidence.
_FIXED_CONFIDENCE = "0.5000"

_SYSTEM_PROMPTS: dict[Kind, str] = {
    Kind.CLASSIFICATION_PROPOSAL: (
        "You are a DEMO ONLY classification assistant with no authority over "
        "any real decision. Given a product or service description, reply "
        "with a short proposed classification code only -- no explanation."
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
    system = _SYSTEM_PROMPTS[call.kind]
    user = call.input.decode("utf-8", errors="replace")
    payload = {
        "model": model,
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "temperature": 0,
    }
    data = json.dumps(payload).encode("utf-8")
    return urllib.request.Request(
        _API_URL,
        data=data,
        method="POST",
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {api_key}",
        },
    )


def _result_from_content(kind: Kind, content: str) -> Result:
    """Map one model response string to a Result for *kind*.

    Does not invent a placeholder for an empty response -- an empty
    proposed_code / text / fields is passed through, and
    ``ztax_gateway.runtime.check_result`` (called by the Gateway after
    ``run`` returns) is what refuses it as RuntimeUnavailableError.
    """
    text = content.strip()
    if kind == Kind.CLASSIFICATION_PROPOSAL:
        return Result(proposed_code=text, confidence=_FIXED_CONFIDENCE)
    if kind == Kind.EXTRACTION:
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
        type) on a timeout, a network error, an HTTP error status, or a
        response that is not shaped like a chat completion -- never a raw
        urllib or JSON exception, so the Gateway's existing error mapping
        handles this runtime exactly like any other.
        """
        request = build_request(self._model, self._api_key, call)
        try:
            with urllib.request.urlopen(request, timeout=_TIMEOUT_SECONDS) as response:
                raw = response.read()
        except urllib.error.HTTPError as exc:
            raise RuntimeUnavailableError(
                f"groq demo runtime: provider returned HTTP {exc.code}"
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
                "chat completion"
            ) from exc

        return _result_from_content(call.kind, content)
