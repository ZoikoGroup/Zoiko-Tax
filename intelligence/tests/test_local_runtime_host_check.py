"""H1 host-containment tests for ztax_gateway.local_runtime.

All redirect and connectivity tests use throwaway servers bound to 127.0.0.1 on a
random OS-assigned port.  No real network calls are made.  Sentinel strings stand in
for hosts, paths and allow-list entries so a leak would be visible in assertions.
"""

from __future__ import annotations

import threading
from collections.abc import Iterator
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

import pytest

from ztax_gateway.local_runtime import (
    _ERR_ALLOWED_HOSTS_MALFORMED,
    _ERR_HOST_NOT_ALLOWED,
    ENDPOINT_ENV,
    ENV_ALLOWED_HOSTS,
    MODEL_ENV,
    LocalModelRuntime,
    LocalRuntimeConfigError,
    _check_host,
    _parse_allowed_hosts,
)

# ---------------------------------------------------------------------------
# Sentinels - appear in no message if the runtime is clean
# ---------------------------------------------------------------------------

_HOST_MARK = "HOST-MARK-7a3f91"
_ENTRY_MARK = "ENTRY-MARK-2b8c04"
_PATH_MARK = "PATH-MARK-5d1e22"
_MODEL = "pinned-model-7b-v1"


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make(monkeypatch: pytest.MonkeyPatch, url: str, **env: str) -> None:
    """Attempt to construct a LocalModelRuntime with ENDPOINT_ENV=url."""
    monkeypatch.setenv(ENDPOINT_ENV, url)
    monkeypatch.setenv(MODEL_ENV, _MODEL)
    for k, v in env.items():
        monkeypatch.setenv(k, v)
    LocalModelRuntime()


def _make_fails(monkeypatch: pytest.MonkeyPatch, url: str, **env: str) -> LocalRuntimeConfigError:
    """Assert that LocalModelRuntime() raises LocalRuntimeConfigError; return it."""
    monkeypatch.setenv(ENDPOINT_ENV, url)
    monkeypatch.setenv(MODEL_ENV, _MODEL)
    for k, v in env.items():
        monkeypatch.setenv(k, v)
    with pytest.raises(LocalRuntimeConfigError) as exc:
        LocalModelRuntime()
    return exc.value


# ---------------------------------------------------------------------------
# Accepted by default (no ZTAX_LOCAL_RUNTIME_ALLOWED_HOSTS)
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "url",
    [
        "http://127.0.0.1:8080",
        "http://localhost:8080",
        "https://127.0.0.1",
        "http://127.9.9.9",
        "http://[::1]:8080",
        "http://127.0.0.1",
        "http://127.255.255.255",
        "http://localhost",
        "https://localhost",
    ],
)
def test_loopback_urls_are_accepted_by_default(
    monkeypatch: pytest.MonkeyPatch, url: str
) -> None:
    _make(monkeypatch, url)  # must not raise


# ---------------------------------------------------------------------------
# Refused by default (no allow-list)
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "url",
    [
        "http://10.0.0.5:8080",
        "http://192.168.1.1",
        "http://8.8.8.8",
        "http://example.com",
        "http://localhost.evil.com",
        "http://foo.localhost",
        "http://localhost.:8080",          # trailing dot on localhost
        "http://127.0.0.1.:8080",          # trailing dot on IP
        "http://[::ffff:127.0.0.1]:8080",  # IPv4-mapped loopback -> refused
        "http://[::ffff:10.0.0.5]:8080",   # IPv4-mapped non-loopback -> refused
    ],
)
def test_non_loopback_hosts_are_refused_by_default(
    monkeypatch: pytest.MonkeyPatch, url: str
) -> None:
    err = _make_fails(monkeypatch, url)
    assert str(err) == _ERR_HOST_NOT_ALLOWED


# Numeric non-canonical forms must all be refused; they do not get through urlsplit
# as IP literals (Python's ipaddress won't parse them), so the host is treated as
# a bare hostname that is neither "localhost" nor a known loopback IP.
@pytest.mark.parametrize(
    "url",
    [
        "http://2130706433",    # dword form of 127.0.0.1
        "http://0x7f.1",        # hex
        "http://0177.0.0.1",    # octal
        "http://127.1",         # short form
    ],
)
def test_numeric_non_canonical_host_forms_are_refused(
    monkeypatch: pytest.MonkeyPatch, url: str
) -> None:
    # These are either refused by the IDNA check (empty-label, etc.) or
    # by the host-containment check; in either case a LocalRuntimeConfigError is raised.
    monkeypatch.setenv(ENDPOINT_ENV, url)
    monkeypatch.setenv(MODEL_ENV, _MODEL)
    with pytest.raises(LocalRuntimeConfigError):
        LocalModelRuntime()


# ---------------------------------------------------------------------------
# Percent-encoding and backslash
# ---------------------------------------------------------------------------


def test_percent_encoded_host_is_refused(monkeypatch: pytest.MonkeyPatch) -> None:
    err = _make_fails(monkeypatch, "http://127.0.0%2e1:8080")
    # May be caught as a bad endpoint (empty hostname after decode) or as host not allowed;
    # either way a LocalRuntimeConfigError is raised.
    assert isinstance(err, LocalRuntimeConfigError)


# ---------------------------------------------------------------------------
# Userinfo (@) is caught by the earlier _validated_base_url check (not host check),
# but we verify the fixed message does not echo the URL or host.
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "url",
    [
        "http://127.0.0.1@evil.com",
        "http://evil.com@127.0.0.1",
        "http://user:pass@127.0.0.1",
    ],
)
def test_userinfo_in_url_is_refused_and_does_not_echo_host(
    monkeypatch: pytest.MonkeyPatch, url: str
) -> None:
    err = _make_fails(monkeypatch, url)
    assert "evil.com" not in str(err)
    assert "127.0.0.1" not in str(err)
    assert "user" not in str(err)
    assert "pass" not in str(err)
    assert err.__cause__ is None


# ---------------------------------------------------------------------------
# Port: a valid port on an allowed host is accepted; a bad port is refused
# ---------------------------------------------------------------------------


def test_a_valid_port_on_a_loopback_host_is_accepted(monkeypatch: pytest.MonkeyPatch) -> None:
    _make(monkeypatch, "http://127.0.0.1:65535")


def test_a_bad_port_on_a_loopback_host_is_refused(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(ENDPOINT_ENV, "http://127.0.0.1:notaport")
    monkeypatch.setenv(MODEL_ENV, _MODEL)
    with pytest.raises(LocalRuntimeConfigError):
        LocalModelRuntime()


def test_a_huge_port_on_a_loopback_host_is_refused(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv(ENDPOINT_ENV, "http://127.0.0.1:99999")
    monkeypatch.setenv(MODEL_ENV, _MODEL)
    with pytest.raises(LocalRuntimeConfigError):
        LocalModelRuntime()


def test_port_does_not_change_the_host_decision_for_loopback(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # 127.0.0.1 is loopback regardless of port; 8080 is a normal port.
    _make(monkeypatch, "http://127.0.0.1:8080")


def test_port_does_not_change_the_host_decision_for_non_loopback(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # 10.0.0.5 is non-loopback; a valid port doesn't save it.
    err = _make_fails(monkeypatch, "http://10.0.0.5:8080")
    assert str(err) == _ERR_HOST_NOT_ALLOWED


# ---------------------------------------------------------------------------
# Allow-list: accepted cases
# ---------------------------------------------------------------------------


def test_a_listed_hostname_over_https_is_accepted(monkeypatch: pytest.MonkeyPatch) -> None:
    _make(monkeypatch, "https://models.internal:8443", **{ENV_ALLOWED_HOSTS: "models.internal"})


def test_a_listed_hostname_over_http_is_refused(monkeypatch: pytest.MonkeyPatch) -> None:
    err = _make_fails(
        monkeypatch,
        "http://models.internal:8080",
        **{ENV_ALLOWED_HOSTS: "models.internal"},
    )
    assert str(err) == _ERR_HOST_NOT_ALLOWED


def test_an_unlisted_hostname_is_refused(monkeypatch: pytest.MonkeyPatch) -> None:
    err = _make_fails(
        monkeypatch,
        "https://other.internal:8443",
        **{ENV_ALLOWED_HOSTS: "models.internal"},
    )
    assert str(err) == _ERR_HOST_NOT_ALLOWED


def test_a_listed_hostname_with_trailing_dot_in_url_is_refused(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    # "models.internal." (trailing dot) is not "models.internal"
    err = _make_fails(
        monkeypatch,
        "https://models.internal.:8443",
        **{ENV_ALLOWED_HOSTS: "models.internal"},
    )
    assert str(err) == _ERR_HOST_NOT_ALLOWED


def test_allow_list_hostname_match_is_case_insensitive(monkeypatch: pytest.MonkeyPatch) -> None:
    _make(monkeypatch, "https://MODELS.INTERNAL:8443", **{ENV_ALLOWED_HOSTS: "models.internal"})
    _make(monkeypatch, "https://models.internal:8443", **{ENV_ALLOWED_HOSTS: "MODELS.INTERNAL"})


def test_a_listed_ip_matches_only_that_ip(monkeypatch: pytest.MonkeyPatch) -> None:
    _make(monkeypatch, "https://10.0.0.5", **{ENV_ALLOWED_HOSTS: "10.0.0.5"})


def test_a_listed_ip_does_not_match_a_different_ip(monkeypatch: pytest.MonkeyPatch) -> None:
    err = _make_fails(monkeypatch, "https://10.0.0.6", **{ENV_ALLOWED_HOSTS: "10.0.0.5"})
    assert str(err) == _ERR_HOST_NOT_ALLOWED


def test_loopback_is_still_allowed_when_allow_list_is_set(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    _make(monkeypatch, "http://127.0.0.1:8080", **{ENV_ALLOWED_HOSTS: "models.internal"})
    _make(monkeypatch, "http://localhost:8080", **{ENV_ALLOWED_HOSTS: "models.internal"})


# ---------------------------------------------------------------------------
# Allow-list: malformed entries give a fixed config error
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "bad_entry",
    [
        f"{_ENTRY_MARK}.*",          # wildcard
        "10.0.0.0/8",               # CIDR range
        "models.internal:8443",     # port in entry
        "https://models.internal",  # scheme in entry
        "/models.internal",         # path in entry
        "user@models.internal",     # userinfo in entry
        "models.internal.",         # trailing dot in entry
        "2130706433",               # non-canonical dword
        "0x7f000001",               # hex
        "0177.0.0.1",               # octal
        "[::ffff:10.0.0.5]",        # IPv4-mapped IPv6 in entry
    ],
)
def test_a_malformed_allow_list_entry_raises_a_fixed_config_error(
    monkeypatch: pytest.MonkeyPatch, bad_entry: str
) -> None:
    err = _make_fails(monkeypatch, "https://models.internal", **{ENV_ALLOWED_HOSTS: bad_entry})
    assert str(err) == _ERR_ALLOWED_HOSTS_MALFORMED
    assert _ENTRY_MARK not in str(err)
    assert err.__cause__ is None


# ---------------------------------------------------------------------------
# Parser match: urllib would connect to the same host that was checked
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "url",
    [
        "http://127.0.0.1:8080/path",
        "http://localhost:8080/path",
        "https://127.0.0.1",
        "http://127.9.9.9",
        "http://[::1]:8080",
    ],
)
def test_the_host_urllib_would_connect_to_matches_the_checked_host(url: str) -> None:
    """urllib.request.Request(url).host must equal what urlsplit sees."""
    import urllib.parse

    parts = urllib.parse.urlsplit(url)
    checked_hostname = parts.hostname  # what _check_host uses
    request_host = urllib.request.Request(url).host  # what urllib would connect to
    # request_host includes port; checked_hostname does not. Strip port for comparison.
    if ":" in request_host and not request_host.startswith("["):
        req_hostname = request_host.split(":")[0]
    elif request_host.startswith("["):
        req_hostname = (
            request_host.split("]:")[0].lstrip("[")
            if "]:" in request_host
            else request_host.strip("[]")
        )
    else:
        req_hostname = request_host
    assert req_hostname.lower() == (checked_hostname or "").lower()


# ---------------------------------------------------------------------------
# Error message hygiene: no sentinel host/port/entry in any message or chain
# ---------------------------------------------------------------------------


def test_refused_host_error_does_not_echo_the_host() -> None:
    err = None
    try:
        _check_host(f"http://{_HOST_MARK}.example:8080")
    except LocalRuntimeConfigError as e:
        err = e
    assert err is not None
    assert _HOST_MARK not in str(err)
    assert _HOST_MARK not in repr(err)
    assert err.__cause__ is None
    assert err.__context__ is None


def test_refused_host_error_does_not_echo_the_path() -> None:
    err = None
    try:
        _check_host(f"http://10.0.0.5/{_PATH_MARK}")
    except LocalRuntimeConfigError as e:
        err = e
    assert err is not None
    assert _PATH_MARK not in str(err)
    assert err.__cause__ is None
    assert err.__context__ is None


def test_malformed_allow_list_error_does_not_echo_the_entry() -> None:
    err = None
    try:
        _parse_allowed_hosts(f"{_ENTRY_MARK}.*")
    except LocalRuntimeConfigError as e:
        err = e
    assert err is not None
    assert _ENTRY_MARK not in str(err)
    assert err.__cause__ is None
    assert err.__context__ is None


# ---------------------------------------------------------------------------
# Redirect tests: servers returning 301-308
# ---------------------------------------------------------------------------


@contextmanager
def _redirect_server(
    code: int,
    target_loopback: bool,
) -> Iterator[tuple[str, list[int]]]:
    """Yield (origin_url, target_hit_count).

    ``origin_url`` answers the redirect; ``target_hit_count`` records how many
    requests the redirect target received (must stay 0).
    """
    target_hits: list[int] = [0]

    class _TargetHandler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:
            target_hits[0] += 1
            self.send_response(200)
            self.send_header("Content-Length", "0")
            self.end_headers()

        def log_message(self, format: str, *args: Any) -> None:
            return None

    target_httpd = ThreadingHTTPServer(("127.0.0.1", 0), _TargetHandler)
    target_httpd.daemon_threads = True
    target_thread = threading.Thread(
        target=target_httpd.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True
    )
    target_thread.start()
    target_port = target_httpd.server_address[1]

    if target_loopback:
        location = f"http://127.0.0.1:{target_port}/redirected"
    else:
        # A non-loopback target that would never actually be reached.
        location = "http://198.51.100.1/redirected"

    class _OriginHandler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:
            length = int(self.headers.get("Content-Length", "0"))
            self.rfile.read(length)
            self.send_response(code)
            self.send_header("Location", location)
            self.send_header("Content-Length", "0")
            self.end_headers()

        def log_message(self, format: str, *args: Any) -> None:
            return None

    origin_httpd = ThreadingHTTPServer(("127.0.0.1", 0), _OriginHandler)
    origin_httpd.daemon_threads = True
    origin_thread = threading.Thread(
        target=origin_httpd.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True
    )
    origin_thread.start()
    origin_port = origin_httpd.server_address[1]
    try:
        yield f"http://127.0.0.1:{origin_port}", target_hits
    finally:
        origin_httpd.shutdown()
        origin_httpd.server_close()
        origin_thread.join(timeout=5)
        target_httpd.shutdown()
        target_httpd.server_close()
        target_thread.join(timeout=5)


@pytest.mark.parametrize("code", [301, 302, 303, 307, 308])
@pytest.mark.parametrize(
    "target_loopback",
    [True, False],
    ids=["loopback-target", "non-loopback-target"],
)
def test_redirect_is_refused_and_target_receives_zero_requests(
    monkeypatch: pytest.MonkeyPatch,
    code: int,
    target_loopback: bool,
) -> None:
    from ztax_gateway.provenance import AuthorityOutcome, RiskTier
    from ztax_gateway.runtime import Route, RuntimeUnavailableError
    from ztax_gateway.wire import Call, Governance, Kind

    with _redirect_server(code, target_loopback) as (origin_url, target_hits):
        monkeypatch.setenv(ENDPOINT_ENV, origin_url)
        monkeypatch.setenv(MODEL_ENV, _MODEL)
        runtime = LocalModelRuntime()

        call = Call(
            kind=Kind.CLASSIFICATION_PROPOSAL,
            governance=Governance(
                tenant_id="00000000-0000-0000-0000-000000000001",
                use_case="classification-review",
                authority_outcome=AuthorityOutcome.A1,
                risk_tier=RiskTier.T1,
                region="local",
                data_classes=("P0",),
            ),
            subject_ref="sku:test",
            input=b'{"description":"test"}',
        )
        route = Route("model:local@1", "provider:local", "prompt:local-v1")
        with pytest.raises(RuntimeUnavailableError) as exc:
            runtime.run(route, call)

    # The redirect was refused with the fixed redirect message.
    assert "redirect" in str(exc.value)
    # The Location value (a URL with 198.51.100.1 or the loopback target port) must
    # not appear in the error.
    assert "198.51.100.1" not in str(exc.value)
    assert "redirected" not in str(exc.value)
    # The redirect target was never contacted.
    assert target_hits[0] == 0
    assert exc.value.__cause__ is None
    assert exc.value.__context__ is None


# ---------------------------------------------------------------------------
# Hygiene: __cause__ and __context__ are None on host-refusal errors
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "url",
    [
        "http://10.0.0.5",
        "http://example.com",
        "http://[::ffff:127.0.0.1]",
        "http://localhost.",
    ],
)
def test_host_refusal_error_has_no_cause_or_context(url: str) -> None:
    try:
        _check_host(url)
    except LocalRuntimeConfigError as e:
        assert e.__cause__ is None
        assert e.__context__ is None
    else:
        pytest.fail(f"Expected LocalRuntimeConfigError for {url!r}")


# ---------------------------------------------------------------------------
# IPv6 loopback
# ---------------------------------------------------------------------------


def test_ipv6_loopback_is_accepted(monkeypatch: pytest.MonkeyPatch) -> None:
    _make(monkeypatch, "http://[::1]:8080")


def test_ipv4_mapped_ipv6_loopback_is_refused(monkeypatch: pytest.MonkeyPatch) -> None:
    err = _make_fails(monkeypatch, "http://[::ffff:127.0.0.1]:8080")
    assert str(err) == _ERR_HOST_NOT_ALLOWED


def test_ipv4_mapped_ipv6_non_loopback_is_refused(monkeypatch: pytest.MonkeyPatch) -> None:
    err = _make_fails(monkeypatch, "http://[::ffff:10.0.0.5]:8080")
    assert str(err) == _ERR_HOST_NOT_ALLOWED
