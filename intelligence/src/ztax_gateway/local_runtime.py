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

Host enforcement (``ZTAX_LOCAL_RUNTIME_ENDPOINT``):

* Loopback only by default: the exact hostname ``localhost`` (accepted by name, never
  resolved; relies on the machine's hosts file) or a loopback IP literal in
  ``127.0.0.0/8`` or ``::1``. All other hosts are refused.
* IPv4-mapped IPv6 literals (``::ffff:127.0.0.1``) are always refused, even if they
  map to loopback. Non-canonical numeric forms (dword, hex, octal, short) and hosts
  with a trailing dot, percent-encoding or backslashes are always refused.
* ``ZTAX_LOCAL_RUNTIME_ALLOWED_HOSTS``: optional comma-separated exact-match entries
  (a bare hostname compared case-insensitively, or a canonical IP literal). No
  wildcards, no CIDR ranges, no ports, schemes, paths, userinfo or trailing dots in
  an entry. A non-loopback allowed host must use ``https``; loopback may use ``http``
  or ``https``. With the allow-list set, loopback is still always allowed.
* A refused host raises ``LocalRuntimeConfigError`` with one fixed message that never
  echoes the URL, host or entry.
* Redirects are refused. ``_NoRedirect`` covers HTTP 301, 302, 303, 307 and 308; the
  ``Location`` value never appears in any message.

The prompts and the reply mapping are copied from ``tools/groq_demo_runtime.py`` so both
runtimes ask the same question. ``src`` cannot import from ``tools``, and that file is
not edited by this change, so the small helpers are duplicated here;
``tests/test_local_runtime.py`` compares the two to catch drift.
"""

from __future__ import annotations

import contextlib
import functools
import http.client
import ipaddress
import json
import logging
import os
import socket
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from email.message import Message
from typing import IO, Any, Final

from .runtime import Result, Route, RuntimeUnavailableError, json_int, token_count
from .wire import Call, Kind

__all__: list[str] = [
    "ENDPOINT_ENV",
    "ENV_ALLOWED_HOSTS",
    "MODEL_ENV",
    "LocalModelRuntime",
    "LocalRuntimeConfigError",
    "build_payload",
]

log = logging.getLogger("ztax_gateway.local_runtime")

ENDPOINT_ENV: Final = "ZTAX_LOCAL_RUNTIME_ENDPOINT"
MODEL_ENV: Final = "ZTAX_LOCAL_RUNTIME_MODEL"
ENV_ALLOWED_HOSTS: Final = "ZTAX_LOCAL_RUNTIME_ALLOWED_HOSTS"

# Per-read limit (connect and each read), like the Groq runtime. It is the second guard:
# the total below is what ends a reply that keeps arriving slowly. The Gateway's inner
# runtime deadline (starting point: 4 s, to be re-checked after real models) sits above both.
_TIMEOUT_SECONDS = 3

# The TOTAL time one request may take, from the start of the connect to the last byte of
# the reply, measured with a monotonic clock. A server that sends a byte every few seconds
# never trips the per-read limit; it ends here, with the same fixed timeout text. Private
# on purpose: a test patches it, nothing configures it.
_TOTAL_SECONDS: float = 3

# How much of the reply one read takes. Small enough that the total is checked often.
_READ_CHUNK_BYTES = 65_536

# The shortest time a read is given when the total is almost over (a socket timeout of 0
# would mean "do not wait at all").
_MIN_READ_SECONDS = 0.001

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
_MAX_TOKENS = 256
_REPORT_EXPLANATION_MAX_TOKENS = 384

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
_ERR_HOST_NOT_ALLOWED: Final = "local runtime: endpoint host is not allowed"
_ERR_ALLOWED_HOSTS_MALFORMED: Final = (
    f"local runtime: {ENV_ALLOWED_HOSTS} contains a malformed entry"
)
_FAIL_TIMEOUT: Final = "local runtime: request timed out"
_FAIL_NETWORK: Final = "local runtime: network error"
_FAIL_REDIRECT: Final = "local runtime: the server answered with a redirect, which is refused"
_FAIL_TOO_LARGE: Final = "local runtime: provider reply was too large"
_FAIL_SHAPE: Final = "local runtime: provider response was not shaped like a chat completion"
_FAIL_EMPTY: Final = "local runtime: provider returned an empty reply"
_FAIL_REQUIRED_SHAPE: Final = "local runtime: provider reply was not in the required shape"
_FAIL_NO_TIMER: Final = "local runtime: the time limit could not be started"


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


def _is_loopback_ip(hostname: str) -> bool:
    """Return True iff *hostname* is a canonical loopback IP literal (127.0.0.0/8 or ::1).

    IPv4-mapped IPv6 (``::ffff:127.x.x.x``) returns False — always refused.
    Non-canonical numeric forms are not IP literals as far as ``ipaddress`` is concerned,
    so they also return False here and are refused.
    """
    try:
        addr = ipaddress.ip_address(hostname)
    except ValueError:
        return False
    if isinstance(addr, ipaddress.IPv6Address):
        # Refuse IPv4-mapped even if the mapped address is loopback.
        if addr.ipv4_mapped is not None:
            return False
        return addr == ipaddress.ip_address("::1")
    # IPv4
    return addr in ipaddress.ip_network("127.0.0.0/8")


def _looks_like_noncanonical_ip(s: str) -> bool:
    """Return True for strings that look like non-canonical IP address forms.

    Detects dword (pure decimal integer), hex (``0x...``), and dotted-octal
    (``0177.0.0.1``) forms that Python's ``ipaddress`` module rejects as IP
    literals but that some URL parsers silently accept.
    """
    # Pure decimal integer (dword): e.g. "2130706433"
    if s.isdigit():
        return True
    # Hex form: "0x..." or "0X..."
    if s.lower().startswith("0x"):
        return True
    # Dotted form: check each label for leading zero (octal notation)
    if "." in s:
        for label in s.split("."):
            if label.startswith("0") and len(label) > 1 and label.isdigit():
                return True
    return False


def _parse_allowed_hosts(raw: str) -> list[str]:
    """Parse and validate ``ZTAX_LOCAL_RUNTIME_ALLOWED_HOSTS``.

    Returns a list of lowercased, stripped entries.  Raises
    ``LocalRuntimeConfigError`` (with the fixed malformed-entry message) for any
    entry that contains a wildcard, CIDR slash, port colon, scheme, path,
    userinfo ``@``, trailing dot, or is a non-canonical numeric host form.
    """
    entries = []
    for raw_entry in raw.split(","):
        entry = raw_entry.strip()
        if not entry:
            continue
        # Reject any entry that looks like it has a scheme, path, userinfo, port,
        # wildcard, CIDR range, or other structural character.
        for bad_char in ("/", "@", "*", " ", "\t", "\\", "%"):
            if bad_char in entry:
                raise LocalRuntimeConfigError(_ERR_ALLOWED_HOSTS_MALFORMED) from None
        # Colons are only valid inside a bracketed IPv6 literal; reject bare colons
        # (port notation: "host:8080") and unbracketed IPv6.
        if ":" in entry and not (entry.startswith("[") and entry.endswith("]")):
            raise LocalRuntimeConfigError(_ERR_ALLOWED_HOSTS_MALFORMED) from None
        # Strip brackets from an IPv6 literal for validation.
        inner = entry[1:-1] if entry.startswith("[") and entry.endswith("]") else entry
        # Reject trailing dot.
        if inner.endswith("."):
            raise LocalRuntimeConfigError(_ERR_ALLOWED_HOSTS_MALFORMED) from None
        # Reject non-canonical numeric IP forms (dword, hex, octal) before attempting
        # to parse as an IP address: ipaddress would refuse them, but they must not
        # silently become "hostnames" in the allow-list.
        if _looks_like_noncanonical_ip(inner):
            raise LocalRuntimeConfigError(_ERR_ALLOWED_HOSTS_MALFORMED) from None
        # Try to parse as an IP address.  NOTE: LocalRuntimeConfigError is a subclass
        # of ValueError, so we must NOT raise it inside this try/except ValueError
        # block.  Instead, record a flag and raise after.
        malformed = False
        addr: ipaddress.IPv4Address | ipaddress.IPv6Address | None = None
        try:
            addr = ipaddress.ip_address(inner)
        except ValueError:
            addr = None
        if addr is not None and (
            str(addr).lower() != inner.lower()
            or (isinstance(addr, ipaddress.IPv6Address) and addr.ipv4_mapped is not None)
        ):
            malformed = True
        if malformed:
            raise LocalRuntimeConfigError(_ERR_ALLOWED_HOSTS_MALFORMED) from None
        if addr is not None:
            entries.append(str(addr).lower())
        else:
            # Treat as a bare hostname (case-insensitive match later).
            entries.append(inner.lower())
    return entries


def _check_host(url: str, environ: dict[str, str] | None = None) -> None:
    """Enforce the host-containment rule on a URL that has already passed the basic
    ``_validated_base_url`` checks (valid scheme, non-empty netloc, no userinfo).

    Raises ``LocalRuntimeConfigError(_ERR_HOST_NOT_ALLOWED)`` from None when the
    host is not permitted.  Raises ``LocalRuntimeConfigError(_ERR_ALLOWED_HOSTS_MALFORMED)``
    from None when the allow-list env var contains a malformed entry.
    """
    env = environ if environ is not None else os.environ
    parts = urllib.parse.urlsplit(url)
    hostname = parts.hostname  # lowercased by urlsplit; None already excluded upstream
    assert hostname is not None  # guaranteed by _validated_base_url

    # Reject trailing dot (e.g. "localhost." or "127.0.0.1.").
    # urlsplit strips a trailing dot from the hostname but keeps it in netloc; check netloc.
    netloc_host = parts.netloc.rsplit(":", 1)[0] if ":" in parts.netloc else parts.netloc
    # For IPv6 the netloc part is "[::1]" or "[::1]:port"; strip brackets.
    if netloc_host.startswith("[") and netloc_host.endswith("]"):
        netloc_host = netloc_host[1:-1]
    if netloc_host.endswith("."):
        raise LocalRuntimeConfigError(_ERR_HOST_NOT_ALLOWED) from None

    # Reject percent-encoding in the netloc (e.g. "127.0.0%2e1").
    if "%" in parts.netloc:
        raise LocalRuntimeConfigError(_ERR_HOST_NOT_ALLOWED) from None

    # Reject backslashes anywhere in the netloc.
    if "\\" in parts.netloc:
        raise LocalRuntimeConfigError(_ERR_HOST_NOT_ALLOWED) from None

    # Decide whether the host is loopback.
    is_loopback = hostname == "localhost" or _is_loopback_ip(hostname)

    if is_loopback:
        # Loopback: http or https both fine; no further checks needed.
        return

    # Non-loopback: consult the allow-list.
    raw_allowed = env.get(ENV_ALLOWED_HOSTS, "").strip()
    if not raw_allowed:
        raise LocalRuntimeConfigError(_ERR_HOST_NOT_ALLOWED) from None

    allowed_entries = _parse_allowed_hosts(raw_allowed)

    # Try to match the hostname as an IP address first, then as a bare hostname.
    matched = False
    try:
        host_addr = ipaddress.ip_address(hostname)
        host_addr_str = str(host_addr).lower()
        matched = host_addr_str in allowed_entries
    except ValueError:
        # Hostname (not an IP literal): exact case-insensitive match.
        matched = hostname.lower() in allowed_entries

    if not matched:
        raise LocalRuntimeConfigError(_ERR_HOST_NOT_ALLOWED) from None

    # Non-loopback allowed host: must use https.
    if parts.scheme != "https":
        raise LocalRuntimeConfigError(_ERR_HOST_NOT_ALLOWED) from None


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
    _check_host(endpoint)
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


def _monotonic() -> float:
    """The clock the total time is measured with. A function, so a test can replace it."""
    return time.monotonic()


class _TimerStartError(Exception):
    """The thread for the time limit could not be started. No text: the caller reports a
    fixed one."""


class _Deadline:
    """The total time budget of one request.

    ``over()`` is the cheap check the reading loop makes between reads. The timer is what
    ends a read that is blocked: at the deadline it shuts the connection's socket down, so
    the blocked read returns at once instead of waiting for its own per-read timeout.
    ``expired`` is set before the shutdown, so whoever sees the failure can tell why.

    The socket is the one the connection kept for itself (see ``_DeadlineConnection``): the
    opener drops its own reference as soon as the headers are in, long before the body.
    Before each read the socket's timeout is cut to what is left, so a read that the
    shutdown somehow missed still ends at the total.
    """

    def __init__(self, total: float) -> None:
        self._end = _monotonic() + total
        self.expired = threading.Event()
        self._lock = threading.Lock()
        self._sock: socket.socket | None = None
        self._timer = threading.Timer(total, self._fire)
        self._timer.daemon = True

    def start(self) -> None:
        try:
            self._timer.start()
        except RuntimeError:
            raise _TimerStartError from None

    def stop(self) -> None:
        self._timer.cancel()

    def remaining(self) -> float:
        return self._end - _monotonic()

    def over(self) -> bool:
        return self.expired.is_set() or self.remaining() <= 0

    def guard(self, sock: socket.socket) -> None:
        """Take the connected socket (the TLS-wrapped one for https) under this deadline."""
        with self._lock:
            self._sock = sock
        if self.expired.is_set():
            self._abort()

    def limit_next_read(self) -> None:
        """Give the next read no more than what is left of the total."""
        with self._lock:
            sock = self._sock
        if sock is not None:
            seconds = max(min(_TIMEOUT_SECONDS, self.remaining()), _MIN_READ_SECONDS)
            with contextlib.suppress(OSError):
                sock.settimeout(seconds)

    def _fire(self) -> None:
        self.expired.set()
        self._abort()

    def _abort(self) -> None:
        with self._lock:
            sock = self._sock
        if sock is not None:
            # shutdown (not close) wakes a read that is blocked in another thread. The
            # plain socket method is used on purpose: SSLSocket.shutdown would also drop
            # the SSL object, and a read that is about to start would then raise.
            with contextlib.suppress(OSError):
                socket.socket.shutdown(sock, socket.SHUT_RDWR)


class _DeadlineRequest(urllib.request.Request):
    """A request that carries the deadline it must finish within."""

    def __init__(self, url: str, *, deadline: _Deadline, **kwargs: Any) -> None:
        super().__init__(url, **kwargs)
        self.deadline = deadline


class _DeadlineConnection(http.client.HTTPConnection):
    """Connects within what is left of the total, and registers with the deadline."""

    def __init__(self, *args: Any, deadline: _Deadline, **kwargs: Any) -> None:
        super().__init__(*args, **kwargs)
        self._deadline = deadline

    def connect(self) -> None:
        remaining = self._deadline.remaining()
        if remaining <= 0:
            raise TimeoutError
        # The connect (and the TLS handshake that follows it) takes at most what is left,
        # and never more than the per-read limit.
        self.timeout = min(_TIMEOUT_SECONDS, remaining)
        super().connect()
        # The opener sets ``self.sock`` to None as soon as the headers are in. The deadline
        # keeps its own reference (the TLS-wrapped socket for https) for the whole body.
        if self.sock is not None:
            self._deadline.guard(self.sock)


class _DeadlineHTTPSConnection(_DeadlineConnection, http.client.HTTPSConnection):
    """The https twin of ``_DeadlineConnection``."""


class _DeadlineHTTPHandler(urllib.request.HTTPHandler):
    def http_open(self, req: urllib.request.Request) -> http.client.HTTPResponse:
        if isinstance(req, _DeadlineRequest):
            return self.do_open(functools.partial(_DeadlineConnection, deadline=req.deadline), req)
        return self.do_open(http.client.HTTPConnection, req)


class _DeadlineHTTPSHandler(urllib.request.HTTPSHandler):
    def https_open(self, req: urllib.request.Request) -> http.client.HTTPResponse:
        if isinstance(req, _DeadlineRequest):
            return self.do_open(
                functools.partial(_DeadlineHTTPSConnection, deadline=req.deadline), req
            )
        return self.do_open(http.client.HTTPSConnection, req)


def _read_within(response: http.client.HTTPResponse, deadline: _Deadline) -> bytes:
    """Read the reply (one byte more than the cap, so the caller can tell it was too big),
    and stop as soon as the total time is over. Raises TimeoutError then."""
    chunks: list[bytes] = []
    size = 0
    limit = _MAX_REPLY_BYTES + 1
    while size < limit:
        if deadline.over():
            raise TimeoutError
        deadline.limit_next_read()
        chunk = response.read1(min(_READ_CHUNK_BYTES, limit - size))
        if not chunk:
            # An end of reply that the deadline's shutdown caused is not a complete reply.
            if deadline.expired.is_set():
                raise TimeoutError
            break
        chunks.append(chunk)
        size += len(chunk)
    return b"".join(chunks)


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
    return urllib.request.build_opener(
        urllib.request.ProxyHandler({}),
        _NoRedirect(),
        _DeadlineHTTPHandler(),
        _DeadlineHTTPSHandler(),
    )


class LocalModelRuntime:
    """A ModelRuntime backed by a self-hosted, OpenAI-compatible chat-completions server."""

    def __init__(self) -> None:
        # Raises LocalRuntimeConfigError, with no network call, if the config is not usable.
        self._base_url, self._model = _resolve_config()

    def run(self, route: Route, call: Call) -> Result:
        """Run one permitted call. Raises RuntimeUnavailableError (fixed text) on every
        failure; never provider output, never the endpoint address."""
        payload = json.dumps(build_payload(self._model, call)).encode("utf-8")
        deadline = _Deadline(_TOTAL_SECONDS)
        request = _DeadlineRequest(
            self._base_url + _COMPLETIONS_PATH,
            data=payload,
            method="POST",
            headers={"Content-Type": "application/json", "User-Agent": _USER_AGENT},
            deadline=deadline,
        )

        # Failures are recorded in ``failure`` and raised after the try statement, so the
        # error carries no __cause__ or __context__ (those can hold the address).
        failure: str | None = None
        raw = b""
        try:
            deadline.start()
            with _opener().open(request, timeout=_TIMEOUT_SECONDS) as response:
                raw = _read_within(response, deadline)
        except _TimerStartError:
            failure = _FAIL_NO_TIMER  # nothing was sent
        except urllib.error.HTTPError as exc:
            failure = _FAIL_REDIRECT if exc.code in _REDIRECT_STATUSES else _fail_http(exc.code)
            exc.close()
        except urllib.error.URLError:
            failure = _FAIL_TIMEOUT if deadline.over() else _FAIL_NETWORK
        except TimeoutError:
            failure = _FAIL_TIMEOUT
        except (OSError, http.client.HTTPException, UnicodeError):
            # UnicodeError: a host the idna codec rejects at connect time. Not an OSError.
            # Past the total, the shutdown that ended the read is what raised: a timeout.
            failure = _FAIL_TIMEOUT if deadline.over() else _FAIL_NETWORK
        except ValueError:
            # A read on a socket the deadline shut down can raise ValueError (a TLS socket
            # whose SSL object is gone). Past the total that is the timeout; any other
            # ValueError is not ours to hide.
            if not deadline.over():
                raise
            failure = _FAIL_TIMEOUT
        finally:
            deadline.stop()
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
