"""ZoikoTax Python SDK.

The types in `zoikotax.schema` are generated from the contract and are not
edited; `scripts/check.py` fails the build on any drift, which is the same
mechanism ADR-0010 §2.1 applies to the generated server. What is hand-written
is this file: a thin typed layer over `urllib.request`, with no runtime
dependencies.

Three decisions are worth stating, because each is a thing an SDK usually
does that this one deliberately does not.

**An error is a value, not an exception.** Every method returns a `Result`
rather than raising on a 4xx. A `ZoikoTaxError` carries the Problem Details
document whole, and the field to branch on is `reason_code` — a closed,
registered vocabulary that means the same thing in an error, in a decision and
in evidence (ADR-0016 §2.4). Nothing here matches on a title or a message, and
neither should a caller: both may be reworded without notice.

**Nothing retries by itself.** `retryable` says whether retrying unchanged
could succeed, and the caller decides. An SDK that retried on its own would,
on the endpoints this surface is about to grow, submit a transaction twice —
and the thing that makes that safe is an `Idempotency-Key` the caller chose
(ADR-0013), not a backoff this library picked.

**No token handling.** The session is an opaque `HttpOnly` cookie (ADR-0020).
There is nothing to store, nothing to refresh and nothing to leak, so this SDK
has no credential store. Outside a browser something still has to send the
cookie back, so each client keeps an in-memory cookie jar — the equivalent of
the TypeScript SDK's `credentials: "include"` — that is never written to disk
and never shared with another client.
"""

from __future__ import annotations

import json
import urllib.error
import urllib.parse
import urllib.request
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from http.cookiejar import CookieJar
from typing import Any, Generic, Literal, TypeGuard, TypeVar

from zoikotax.schema import (
    AuditRecord,
    Capabilities,
    ChangePasswordRequest,
    CreateUserRequest,
    Problem,
    ReasonCode,
    Role,
    Session,
    SessionSummary,
    SignInRequest,
    Tenant,
    User,
    UserStatus,
    V1AdminAuditGetResponse,
    V1AdminSessionsGetResponse,
    V1AdminUsersGetResponse,
)

__all__ = [
    "AuditRecord",
    "Capabilities",
    "ChangePasswordRequest",
    "CreateUserRequest",
    "Err",
    "HttpRequest",
    "HttpResponse",
    "ListAuditResponse",
    "ListSessionsResponse",
    "ListUsersResponse",
    "Ok",
    "Problem",
    "ReasonCode",
    "Result",
    "Role",
    "Session",
    "SessionSummary",
    "SignInRequest",
    "Tenant",
    "Transport",
    "UrllibTransport",
    "User",
    "UserStatus",
    "ZoikoTaxClient",
    "ZoikoTaxError",
    "ZoikoTaxTransportError",
    "unwrap",
]

__version__ = "1.0.0"

# The list operations' bodies are inline in the contract, so the generator
# names them after their path. These are the names a caller should use.
ListUsersResponse = V1AdminUsersGetResponse
ListSessionsResponse = V1AdminSessionsGetResponse
ListAuditResponse = V1AdminAuditGetResponse

T = TypeVar("T")


class ZoikoTaxError(Exception):
    """A failed request: the service answered, with a Problem Details document.

    It is an `Exception` subclass so that `unwrap()` can raise it with its
    traceback, and it carries the Problem document whole so nothing a caller
    might need is lost in translation.
    """

    reason_code: ReasonCode
    """The registered reason code. This is the field to branch on."""
    status: int
    """The HTTP status, for logging and for the cases where it is the clearer signal."""
    retryable: bool
    """Whether retrying this request unchanged could succeed.

    Only a transient failure is retryable; everything else fails identically.
    """
    request_id: str | None
    """This request's identifier. Quote it in a support request."""
    field: str | None
    """The contract field at fault, for a validation failure."""
    retry_after: int | None
    """Seconds to wait before retrying, where the server said."""
    problem: Problem
    """The Problem Details document as received."""

    def __init__(self, problem: Problem, retry_after: int | None = None) -> None:
        super().__init__(problem.get("detail") or problem["title"])
        self.problem = problem
        self.reason_code = problem["ztx_reason_code"]
        self.status = problem["status"]
        self.retryable = problem["ztx_retryable"]
        self.request_id = problem.get("ztx_request_id")
        self.field = problem.get("ztx_field")
        self.retry_after = retry_after

    def __repr__(self) -> str:
        return (
            f"ZoikoTaxError(reason_code={self.reason_code!r}, status={self.status}, "
            f"request_id={self.request_id!r})"
        )


class ZoikoTaxTransportError(Exception):
    """A failure that produced no Problem document at all.

    A DNS failure, a TLS failure, a timeout, a proxy that returned HTML. It is a
    separate type because the caller's options differ: a `ZoikoTaxError` is the
    service answering, and this is not reaching it, for a reason outside
    anything the contract describes.
    """

    status: int | None
    """The HTTP status, where there was a response at all."""
    cause: BaseException | None
    """What went wrong underneath, where something raised. Also `__cause__`."""

    def __init__(
        self, message: str, *, cause: BaseException | None = None, status: int | None = None
    ) -> None:
        super().__init__(message)
        self.status = status
        self.cause = cause
        self.__cause__ = cause


@dataclass(frozen=True)
class Ok(Generic[T]):
    """A call that succeeded. `data` is the response body."""

    data: T
    ok: Literal[True] = field(default=True, init=False)


@dataclass(frozen=True)
class Err:
    """A call that failed, and why."""

    error: ZoikoTaxError | ZoikoTaxTransportError
    ok: Literal[False] = field(default=False, init=False)


Result = Ok[T] | Err
"""The result of a call: the value, or the reason there is none.

Branch on `result.ok`, or `match` on `Ok(data)` / `Err(error)`.
"""


# --- transport ---------------------------------------------------------------


@dataclass(frozen=True)
class HttpRequest:
    """What the client asks a transport to send."""

    method: str
    url: str
    headers: Mapping[str, str]
    body: bytes | None
    timeout: float | None


@dataclass(frozen=True)
class HttpResponse:
    """What a transport hands back: any response, success or not."""

    status: int
    headers: Mapping[str, str] = field(default_factory=dict)
    body: bytes = b""

    def header(self, name: str) -> str | None:
        """A header's value, matched case-insensitively as HTTP requires."""
        wanted = name.lower()
        for key, value in self.headers.items():
            if key.lower() == wanted:
                return value
        return None


Transport = Callable[[HttpRequest], HttpResponse]
"""Sends one request and returns the response, whatever its status.

It raises only when there is no response to return — the connection failed,
or timed out. Supplying one is how a test drives the client without a network,
and how a server-side caller substitutes an instrumented client.
"""


class UrllibTransport:
    """The default transport: the standard library, and a cookie jar.

    One per client, so each client has its own jar and a session opened by one
    is never sent by another.

    The opener is assembled by hand rather than with `build_opener()`, for two
    reasons. `build_opener()` also handles `file:`, `ftp:` and `data:` URLs,
    and a base URL taken from configuration should never be able to read a
    local file. And it follows redirects: the contract declares none, and
    following one would carry a request body, and possibly the session, to a
    location nobody reviewed. A 3xx is therefore returned as it is, and the
    client reports it as something other than the service having answered.
    """

    def __init__(self, cookie_jar: CookieJar | None = None) -> None:
        self.cookie_jar = cookie_jar if cookie_jar is not None else CookieJar()
        opener = urllib.request.OpenerDirector()
        for handler in (
            urllib.request.ProxyHandler(),
            urllib.request.UnknownHandler(),
            urllib.request.HTTPHandler(),
            urllib.request.HTTPSHandler(),
            # Turns every non-2xx into an HTTPError, which __call__ turns back
            # into a response. Without it a 4xx makes open() return None.
            urllib.request.HTTPDefaultErrorHandler(),
            urllib.request.HTTPErrorProcessor(),
            urllib.request.HTTPCookieProcessor(self.cookie_jar),
        ):
            opener.add_handler(handler)
        self._opener = opener

    def __call__(self, request: HttpRequest) -> HttpResponse:
        req = urllib.request.Request(
            request.url, data=request.body, headers=dict(request.headers), method=request.method
        )
        try:
            if request.timeout is None:
                raw = self._opener.open(req)
            else:
                raw = self._opener.open(req, timeout=request.timeout)
        except urllib.error.HTTPError as err:
            # urllib raises for every non-2xx status. Here that is a response
            # like any other, and the client decides what it means.
            with err:
                return HttpResponse(err.code, dict(err.headers.items()), err.read())
        with raw:
            return HttpResponse(raw.status, dict(raw.headers.items()), raw.read())


# --- client ------------------------------------------------------------------


class ZoikoTaxClient:
    """A client for one cell.

    One client per cell, because a cell is a residency boundary: data never
    leaves its region, and a client that transparently failed over to another
    cell would be moving a tenant's data across one.

    It is synchronous and makes one request per call, on the calling thread.
    """

    def __init__(
        self,
        base_url: str,
        *,
        transport: Transport | None = None,
        headers: Mapping[str, str] | None = None,
        timeout: float | None = None,
    ) -> None:
        """
        :param base_url: The cell's base URL, such as
            ``https://eu-west-1.zoikotax.com``. A trailing slash is tolerated.
        :param transport: Sends the requests. Defaults to a `UrllibTransport`
            with its own cookie jar.
        :param headers: Added to every request. Useful for a correlation header
            a platform requires; it is not where a credential goes, because
            there is no credential.
        :param timeout: Per-request timeout in seconds. Unset means no
            client-side timeout and the socket default applies.
        """
        if not base_url:
            raise ValueError("ZoikoTaxClient: base_url is required")
        scheme = urllib.parse.urlsplit(base_url).scheme.lower()
        if scheme not in ("http", "https"):
            raise ValueError(f"ZoikoTaxClient: base_url must be http or https, not {scheme!r}")
        self._base_url = base_url.rstrip("/")
        self._transport: Transport = transport if transport is not None else UrllibTransport()
        self._headers = dict(headers or {})
        self._timeout = timeout

    # --- discovery ----------------------------------------------------------

    def get_capabilities(self) -> Result[Capabilities]:
        """Report this deployment's effective capability.

        Call it before treating any figure as authoritative: `authoritative` is
        false until A4, and a deployment that reports false produces advisory
        figures that must not be filed.
        """
        return self._send("GET", "/v1/capabilities")

    # --- authentication -----------------------------------------------------

    def sign_in(self, body: SignInRequest) -> Result[Session]:
        """Open a session. The response carries no token; the cookie is the session."""
        return self._send("POST", "/v1/auth/sign-in", body)

    def sign_out(self) -> Result[None]:
        """End the current session."""
        return self._send("POST", "/v1/auth/sign-out")

    def get_session(self) -> Result[Session]:
        """Who am I, and until when."""
        return self._send("GET", "/v1/auth/session")

    def change_password(self, body: ChangePasswordRequest) -> Result[None]:
        """Change the current subject's password.

        Every session is revoked, including this one. Expect the next call to
        fail with ``UNAUTHENTICATED``, and send the user to sign in.
        """
        return self._send("POST", "/v1/auth/password", body)

    # --- administration -----------------------------------------------------

    def get_tenant(self) -> Result[Tenant]:
        """The authenticated subject's tenant."""
        return self._send("GET", "/v1/admin/tenant")

    def list_users(self, *, limit: int | None = None) -> Result[ListUsersResponse]:
        """List the tenant's users."""
        return self._send("GET", "/v1/admin/users" + _query(limit=limit))

    def create_user(self, body: CreateUserRequest) -> Result[User]:
        """Create a user. Omitting the password creates an ``INVITED`` user."""
        return self._send("POST", "/v1/admin/users", body)

    def set_user_status(self, user_id: str, status: UserStatus) -> Result[None]:
        """Enable or disable a user. Disabling revokes their sessions."""
        return self._send("POST", f"/v1/admin/users/{_segment(user_id)}/status", {"status": status})

    def grant_role(self, user_id: str, role: Role) -> Result[None]:
        """Grant a role."""
        return self._send("POST", f"/v1/admin/users/{_segment(user_id)}/roles", {"role": role})

    def revoke_role(self, user_id: str, role: Role) -> Result[None]:
        """Revoke a role. Revoking the last ``ADMIN`` is refused."""
        return self._send(
            "DELETE", f"/v1/admin/users/{_segment(user_id)}/roles/{_segment(role)}"
        )

    def list_sessions(self, *, limit: int | None = None) -> Result[ListSessionsResponse]:
        """List the tenant's sessions. `current` marks the caller's own."""
        return self._send("GET", "/v1/admin/sessions" + _query(limit=limit))

    def revoke_session(self, session_id: str) -> Result[None]:
        """Revoke a session. It is marked revoked, never deleted."""
        return self._send("DELETE", f"/v1/admin/sessions/{_segment(session_id)}")

    def list_audit(self, *, limit: int | None = None) -> Result[ListAuditResponse]:
        """Read the tenant's audit trail, most recent first."""
        return self._send("GET", "/v1/admin/audit" + _query(limit=limit))

    # --- transport ----------------------------------------------------------

    def _send(self, method: str, path: str, body: Mapping[str, Any] | None = None) -> Result[Any]:
        headers = {
            # Problem Details first, because an error is the response whose
            # shape this client most needs to be sure of.
            "Accept": "application/problem+json, application/json",
            **self._headers,
        }
        payload: bytes | None = None
        if body is not None:
            headers["Content-Type"] = "application/json"
            payload = json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode("utf-8")

        request = HttpRequest(method, self._base_url + path, headers, payload, self._timeout)
        try:
            response = self._transport(request)
        except Exception as cause:
            # Not BaseException: a KeyboardInterrupt is the caller stopping the
            # program, not the service being unreachable.
            return Err(ZoikoTaxTransportError(f"{method} {path} did not reach the service", cause=cause))

        if response.status in (204, 205):
            return Ok(None)

        try:
            parsed: Any = None if response.body == b"" else json.loads(response.body.decode("utf-8"))
        except ValueError as cause:  # JSONDecodeError and UnicodeDecodeError both
            return Err(
                ZoikoTaxTransportError(
                    f"{method} {path} returned {response.status} with a body that is not JSON",
                    cause=cause,
                    status=response.status,
                )
            )

        if 200 <= response.status < 300:
            return Ok(parsed)

        if _is_problem(parsed):
            return Err(ZoikoTaxError(parsed, _retry_after(response.header("Retry-After"))))

        # A non-Problem error body is something between the client and the
        # cell: a load balancer, a proxy, a WAF. Reporting it as a
        # ZoikoTaxError would attribute it to the service and give it a reason
        # code nobody registered.
        return Err(
            ZoikoTaxTransportError(
                f"{method} {path} returned {response.status} without a Problem Details body; "
                "something between this client and the cell answered",
                status=response.status,
            )
        )


def _is_problem(value: object) -> TypeGuard[Problem]:
    """Whether an error body is a Problem.

    It checks the two fields a caller acts on rather than validating the whole
    document. A stricter check would reject a Problem carrying an extension
    this SDK release predates, and ADR-0010 §2.6 makes additions the normal
    case. `bool` is excluded explicitly because in Python it is an `int`.
    """
    if not isinstance(value, dict):
        return False
    status = value.get("status")
    return (
        isinstance(value.get("ztx_reason_code"), str)
        and isinstance(status, int)
        and not isinstance(status, bool)
    )


def _retry_after(value: str | None) -> int | None:
    """`Retry-After` as delta-seconds, which is the only form the contract declares."""
    if value is None:
        return None
    value = value.strip()
    return int(value) if value.isascii() and value.isdigit() else None


def _segment(value: str) -> str:
    """Encode a path parameter, `/` included, so it cannot become a second segment."""
    return urllib.parse.quote(value, safe="")


def _query(**params: int | str | None) -> str:
    """A query string of the parameters that were given. `0` is given; `None` is not."""
    present = {key: value for key, value in params.items() if value is not None}
    return "" if not present else "?" + urllib.parse.urlencode(present)


def unwrap(result: Result[T]) -> T:
    """Unwrap a result, raising its error on failure.

    For callers that prefer exceptions, and for tests. It is a free function
    rather than a client option, so that the client has one behaviour and the
    choice is visible at the call site::

        capabilities = unwrap(client.get_capabilities())
    """
    if isinstance(result, Ok):
        return result.data
    raise result.error
