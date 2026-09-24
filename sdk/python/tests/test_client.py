"""SDK behaviour, against a stub transport.

These test the decisions in the client's own docstring — that an error is a
value, that nothing retries by itself, that a non-Problem error body is not
attributed to the service. They are not contract tests: what the *server* does
is asserted in Go, against the same contract file
(backend/internal/transport/http/contract_test.go).

The first group is a one-for-one port of sdk/typescript/test/client.test.js,
so the two SDKs are held to the same cases. The second group is behaviour
that only exists in Python: the cookie jar the default transport keeps, the
URL schemes urllib would otherwise open, and `bool` being an `int`.
"""

from __future__ import annotations

import json
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from zoikotax import (
    Err,
    HttpRequest,
    HttpResponse,
    Ok,
    UrllibTransport,
    ZoikoTaxClient,
    ZoikoTaxError,
    ZoikoTaxTransportError,
    unwrap,
)

BASE = "https://eu-west-1.zoikotax.com"


class Stub:
    """A transport that answers with one response, and records what it was asked."""

    def __init__(self, response: HttpResponse) -> None:
        self.response = response
        self.calls: list[HttpRequest] = []

    def __call__(self, request: HttpRequest) -> HttpResponse:
        self.calls.append(request)
        return self.response


def json_response(status: int, body: Any, headers: dict[str, str] | None = None) -> HttpResponse:
    content_type = "application/problem+json" if status >= 400 else "application/json"
    return HttpResponse(
        status, {"Content-Type": content_type, **(headers or {})}, json.dumps(body).encode()
    )


def problem(**overrides: Any) -> dict[str, Any]:
    return {
        "type": "https://errors.zoikotax.com/v1/forbidden",
        "title": "Forbidden",
        "status": 403,
        "detail": "The authenticated subject does not hold a role permitting this action.",
        "instance": "/v1/admin/users",
        "ztx_reason_code": "FORBIDDEN",
        "ztx_request_id": "01JBQ0S9C3X8Q1H6M2KX5R7F4K",
        "ztx_retryable": False,
        **overrides,
    }


def client(stub: Stub, base_url: str = BASE, **options: Any) -> ZoikoTaxClient:
    return ZoikoTaxClient(base_url, transport=stub, **options)


class PortedFromTypeScript(unittest.TestCase):
    def test_a_success_returns_the_body(self) -> None:
        capabilities = {"cell": "eu-west-1", "authoritative": False}
        stub = Stub(json_response(200, capabilities))

        result = client(stub, BASE + "/").get_capabilities()

        assert isinstance(result, Ok)
        self.assertIs(result.ok, True)
        self.assertEqual(result.data, capabilities)
        self.assertEqual(stub.calls[0].url, "https://eu-west-1.zoikotax.com/v1/capabilities")
        # The TypeScript test asserts `credentials: "include"` here. The
        # equivalent is the default transport's cookie jar, which has its own
        # tests below against a real socket.

    def test_an_error_is_a_value_carrying_the_reason_code_not_an_exception(self) -> None:
        stub = Stub(json_response(403, problem()))

        result = client(stub).list_users()

        assert isinstance(result, Err)
        self.assertIs(result.ok, False)
        error = result.error
        assert isinstance(error, ZoikoTaxError)
        self.assertEqual(error.reason_code, "FORBIDDEN")
        self.assertEqual(error.status, 403)
        self.assertIs(error.retryable, False)
        self.assertEqual(error.request_id, "01JBQ0S9C3X8Q1H6M2KX5R7F4K")
        # The whole document survives, so a caller needing a field this SDK
        # release predates can still reach it.
        self.assertEqual(error.problem["type"], "https://errors.zoikotax.com/v1/forbidden")

    def test_retryable_and_retry_after_are_reported_and_nothing_is_retried(self) -> None:
        stub = Stub(
            json_response(
                503,
                problem(status=503, ztx_reason_code="DATABASE_UNAVAILABLE", ztx_retryable=True),
                {"Retry-After": "2"},
            )
        )

        result = client(stub).get_tenant()

        assert isinstance(result, Err)
        assert isinstance(result.error, ZoikoTaxError)
        self.assertIs(result.error.retryable, True)
        self.assertEqual(result.error.retry_after, 2)
        # One attempt. Retrying is the caller's decision, because on the
        # endpoints this surface is about to grow, an automatic retry submits a
        # transaction twice.
        self.assertEqual(len(stub.calls), 1)

    def test_a_204_yields_no_body_rather_than_a_parse_failure(self) -> None:
        stub = Stub(HttpResponse(204))

        result = client(stub).revoke_role("ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4C", "AUDITOR")

        assert isinstance(result, Ok)
        self.assertIsNone(result.data)

    def test_a_non_problem_error_body_is_not_attributed_to_the_service(self) -> None:
        # What a load balancer, a proxy or a WAF returns. Reporting it as a
        # ZoikoTaxError would give it a reason code nobody registered.
        stub = Stub(HttpResponse(502, {"Content-Type": "text/html"}, b"<html>502 Bad Gateway</html>"))

        result = client(stub).get_session()

        assert isinstance(result, Err)
        self.assertIsInstance(result.error, ZoikoTaxTransportError)
        self.assertNotIsInstance(result.error, ZoikoTaxError)
        self.assertEqual(result.error.status, 502)

    def test_a_request_that_never_reaches_the_service_is_a_transport_error(self) -> None:
        def unreachable(request: HttpRequest) -> HttpResponse:
            raise OSError("getaddrinfo failed")

        result = ZoikoTaxClient(BASE, transport=unreachable).get_capabilities()

        assert isinstance(result, Err)
        error = result.error
        assert isinstance(error, ZoikoTaxTransportError)
        self.assertIsInstance(error.cause, OSError)
        self.assertIs(error.__cause__, error.cause)
        self.assertIsNone(error.status)

    def test_path_parameters_are_encoded(self) -> None:
        stub = Stub(HttpResponse(204))

        client(stub).revoke_session("zts_01/../../admin")

        self.assertTrue(stub.calls[0].url.endswith("/v1/admin/sessions/zts_01%2F..%2F..%2Fadmin"))

    def test_a_limit_becomes_a_query_parameter_and_an_absent_one_does_not(self) -> None:
        stub = Stub(json_response(200, {"users": []}))
        c = client(stub)

        c.list_users(limit=50)
        c.list_users()

        self.assertTrue(stub.calls[0].url.endswith("/v1/admin/users?limit=50"))
        self.assertTrue(stub.calls[1].url.endswith("/v1/admin/users"))

    def test_a_request_body_is_sent_as_json_with_the_right_content_type(self) -> None:
        stub = Stub(json_response(200, {"tenant": {}, "user": {}, "expiresAt": ""}))

        client(stub).sign_in(
            {"tenant": "acme", "email": "admin@acme.example", "password": "correct horse battery staple"}
        )

        self.assertEqual(stub.calls[0].headers["Content-Type"], "application/json")
        assert stub.calls[0].body is not None
        self.assertEqual(
            json.loads(stub.calls[0].body),
            {"tenant": "acme", "email": "admin@acme.example", "password": "correct horse battery staple"},
        )

    def test_unwrap_raises_the_error_for_callers_who_prefer_exceptions(self) -> None:
        stub = Stub(json_response(403, problem()))

        with self.assertRaises(ZoikoTaxError) as raised:
            unwrap(client(stub).list_users())

        self.assertEqual(raised.exception.reason_code, "FORBIDDEN")

    def test_a_client_with_no_base_url_is_refused_at_construction(self) -> None:
        # ValueError rather than the TypeScript SDK's TypeError: in Python an
        # empty string of the right type is a bad value, not a bad type.
        with self.assertRaises(ValueError):
            ZoikoTaxClient("")


class PythonSpecific(unittest.TestCase):
    def test_every_operation_sends_its_contract_method_and_path(self) -> None:
        user = "ztu_01JBQ0S9C3X8Q1H6M2KX5R7F4C"
        session = "zts_01JBQ0S9C3X8Q1H6M2KX5R7F4F"
        cases: list[tuple[str, Any, str, str, Any]] = [
            ("get_capabilities", lambda c: c.get_capabilities(), "GET", "/v1/capabilities", None),
            (
                "sign_in",
                lambda c: c.sign_in({"tenant": "acme", "email": "a@acme.example", "password": "x" * 12}),
                "POST",
                "/v1/auth/sign-in",
                {"tenant": "acme", "email": "a@acme.example", "password": "x" * 12},
            ),
            ("sign_out", lambda c: c.sign_out(), "POST", "/v1/auth/sign-out", None),
            ("get_session", lambda c: c.get_session(), "GET", "/v1/auth/session", None),
            (
                "change_password",
                lambda c: c.change_password({"currentPassword": "a" * 12, "newPassword": "b" * 12}),
                "POST",
                "/v1/auth/password",
                {"currentPassword": "a" * 12, "newPassword": "b" * 12},
            ),
            ("get_tenant", lambda c: c.get_tenant(), "GET", "/v1/admin/tenant", None),
            ("list_users", lambda c: c.list_users(), "GET", "/v1/admin/users", None),
            (
                "create_user",
                lambda c: c.create_user({"email": "b@acme.example", "displayName": "B"}),
                "POST",
                "/v1/admin/users",
                {"email": "b@acme.example", "displayName": "B"},
            ),
            (
                "set_user_status",
                lambda c: c.set_user_status(user, "DISABLED"),
                "POST",
                f"/v1/admin/users/{user}/status",
                {"status": "DISABLED"},
            ),
            (
                "grant_role",
                lambda c: c.grant_role(user, "AUDITOR"),
                "POST",
                f"/v1/admin/users/{user}/roles",
                {"role": "AUDITOR"},
            ),
            (
                "revoke_role",
                lambda c: c.revoke_role(user, "AUDITOR"),
                "DELETE",
                f"/v1/admin/users/{user}/roles/AUDITOR",
                None,
            ),
            ("list_sessions", lambda c: c.list_sessions(), "GET", "/v1/admin/sessions", None),
            (
                "revoke_session",
                lambda c: c.revoke_session(session),
                "DELETE",
                f"/v1/admin/sessions/{session}",
                None,
            ),
            ("list_audit", lambda c: c.list_audit(), "GET", "/v1/admin/audit", None),
        ]
        # Fourteen operations in the contract, fourteen here.
        self.assertEqual(len(cases), 14)
        for name, call, method, path, body in cases:
            with self.subTest(name):
                stub = Stub(HttpResponse(204))
                call(client(stub))
                sent = stub.calls[0]
                self.assertEqual((sent.method, sent.url), (method, BASE + path))
                if body is None:
                    self.assertIsNone(sent.body)
                    self.assertNotIn("Content-Type", sent.headers)
                else:
                    assert sent.body is not None
                    self.assertEqual(json.loads(sent.body), body)

    def test_a_limit_of_zero_is_sent_rather_than_dropped_as_falsy(self) -> None:
        stub = Stub(json_response(200, {"records": []}))

        client(stub).list_audit(limit=0)

        self.assertTrue(stub.calls[0].url.endswith("/v1/admin/audit?limit=0"))

    def test_only_http_and_https_base_urls_are_accepted(self) -> None:
        # urllib would otherwise open file:, ftp: and data: URLs, and a base
        # URL is configuration, not code.
        for url in ("file:///etc/passwd", "ftp://eu-west-1.zoikotax.com", "eu-west-1.zoikotax.com"):
            with self.subTest(url), self.assertRaises(ValueError):
                ZoikoTaxClient(url)
        ZoikoTaxClient("http://localhost:8080")
        ZoikoTaxClient("HTTPS://eu-west-1.zoikotax.com")

    def test_a_status_that_is_a_boolean_is_not_a_problem(self) -> None:
        # `True` is an `int` in Python, so a naive isinstance check would
        # accept it as a status.
        stub = Stub(json_response(500, {"ztx_reason_code": "INTERNAL_ERROR", "status": True}))

        result = client(stub).get_tenant()

        assert isinstance(result, Err)
        self.assertNotIsInstance(result.error, ZoikoTaxError)

    def test_a_success_status_with_a_body_that_is_not_json_is_a_transport_error(self) -> None:
        stub = Stub(HttpResponse(200, {"Content-Type": "text/html"}, b"<html>captive portal</html>"))

        result = client(stub).get_capabilities()

        assert isinstance(result, Err)
        self.assertIsInstance(result.error, ZoikoTaxTransportError)
        self.assertEqual(result.error.status, 200)
        self.assertIsInstance(result.error.__cause__, ValueError)

    def test_a_retry_after_that_is_not_delta_seconds_is_not_reported(self) -> None:
        for header in ("Wed, 21 Oct 2026 07:28:00 GMT", "2.5", "-1", "²"):
            with self.subTest(header):
                stub = Stub(json_response(503, problem(status=503, ztx_retryable=True), {"retry-after": header}))
                result = client(stub).get_tenant()
                assert isinstance(result, Err) and isinstance(result.error, ZoikoTaxError)
                self.assertIsNone(result.error.retry_after)

    def test_branching_on_ok_narrows_the_result(self) -> None:
        # Checked by mypy as much as by unittest: `data` is reachable only
        # after `ok`, and `error` only after `not ok`.
        stub = Stub(json_response(200, {"cell": "eu-west-1", "authoritative": False}))

        result = client(stub).get_capabilities()
        if result.ok:
            self.assertIs(result.data["authoritative"], False)
        else:
            self.fail(result.error.args[0])

    def test_a_result_can_be_matched_structurally(self) -> None:
        stub = Stub(json_response(200, {"cell": "eu-west-1"}))

        match client(stub).get_capabilities():
            case Ok(data):
                self.assertEqual(data["cell"], "eu-west-1")
            case Err(error):
                self.fail(f"unexpected error {error!r}")

    def test_unwrap_returns_the_data_on_success(self) -> None:
        stub = Stub(json_response(200, {"cell": "eu-west-1"}))

        self.assertEqual(unwrap(client(stub).get_capabilities())["cell"], "eu-west-1")

    def test_headers_and_timeout_are_passed_to_the_transport(self) -> None:
        stub = Stub(HttpResponse(204))

        client(stub, headers={"X-Correlation-Id": "abc"}, timeout=2.5).sign_out()

        sent = stub.calls[0]
        self.assertEqual(sent.headers["Accept"], "application/problem+json, application/json")
        self.assertEqual(sent.headers["X-Correlation-Id"], "abc")
        self.assertEqual(sent.timeout, 2.5)

    def test_an_interrupt_is_not_reported_as_a_transport_error(self) -> None:
        def interrupted(request: HttpRequest) -> HttpResponse:
            raise KeyboardInterrupt

        with self.assertRaises(KeyboardInterrupt):
            ZoikoTaxClient(BASE, transport=interrupted).get_capabilities()

    def test_an_error_message_is_the_problem_detail(self) -> None:
        error = ZoikoTaxError(problem())  # type: ignore[arg-type]
        self.assertEqual(str(error), problem()["detail"])
        without_detail = problem()
        del without_detail["detail"]
        self.assertEqual(str(ZoikoTaxError(without_detail)), "Forbidden")  # type: ignore[arg-type]


class _Cell(BaseHTTPRequestHandler):
    """A stand-in cell: sign-in sets the session cookie, session reads it."""

    def do_POST(self) -> None:
        length = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(length)
        if self.path == "/v1/auth/sign-in":
            body = json.dumps({"tenant": {}, "user": {}, "expiresAt": ""}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Set-Cookie", "ztax_session=s3cret; Path=/; HttpOnly; SameSite=Strict")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        else:
            self._answer(404, problem(status=404, ztx_reason_code="NOT_FOUND"))

    def do_GET(self) -> None:
        if self.path == "/v1/auth/session":
            if "ztax_session=s3cret" in (self.headers.get("Cookie") or ""):
                self._answer(200, {"tenant": {}, "user": {}, "expiresAt": ""})
            else:
                self._answer(401, problem(status=401, ztx_reason_code="UNAUTHENTICATED"))
        elif self.path == "/v1/capabilities":
            # A redirect the contract does not declare.
            self.send_response(302)
            self.send_header("Location", "/elsewhere")
            self.send_header("Content-Length", "0")
            self.end_headers()
        else:
            self._answer(404, problem(status=404, ztx_reason_code="NOT_FOUND"))

    def do_DELETE(self) -> None:
        self._answer(404, problem(status=404, ztx_reason_code="NOT_FOUND"))

    def _answer(self, status: int, body: Any) -> None:
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json" if status < 400 else "application/problem+json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, format: str, *args: Any) -> None:
        pass


class DefaultTransport(unittest.TestCase):
    """The stdlib transport, against a real socket on the loopback interface."""

    server: ThreadingHTTPServer
    base: str

    @classmethod
    def setUpClass(cls) -> None:
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), _Cell)
        threading.Thread(target=cls.server.serve_forever, daemon=True).start()
        cls.base = f"http://127.0.0.1:{cls.server.server_address[1]}"

    @classmethod
    def tearDownClass(cls) -> None:
        cls.server.shutdown()
        cls.server.server_close()

    def test_the_session_cookie_is_kept_and_sent_back(self) -> None:
        c = ZoikoTaxClient(self.base, timeout=5)

        unwrap(c.sign_in({"tenant": "acme", "email": "a@acme.example", "password": "x" * 12}))
        result = c.get_session()

        self.assertIsInstance(result, Ok)

    def test_a_session_is_never_shared_between_clients(self) -> None:
        first = ZoikoTaxClient(self.base, timeout=5)
        second = ZoikoTaxClient(self.base, timeout=5)

        unwrap(first.sign_in({"tenant": "acme", "email": "a@acme.example", "password": "x" * 12}))
        result = second.get_session()

        assert isinstance(result, Err) and isinstance(result.error, ZoikoTaxError)
        self.assertEqual(result.error.reason_code, "UNAUTHENTICATED")

    def test_an_error_status_is_a_response_not_an_exception(self) -> None:
        result = ZoikoTaxClient(self.base, timeout=5).revoke_session("zts_x")

        assert isinstance(result, Err) and isinstance(result.error, ZoikoTaxError)
        self.assertEqual(result.error.status, 404)

    def test_a_redirect_is_not_followed(self) -> None:
        result = ZoikoTaxClient(self.base, timeout=5).get_capabilities()

        assert isinstance(result, Err)
        self.assertIsInstance(result.error, ZoikoTaxTransportError)
        self.assertEqual(result.error.status, 302)

    def test_a_connection_that_is_refused_is_a_transport_error(self) -> None:
        # Bind and close a socket to find a port nothing is listening on.
        probe = ThreadingHTTPServer(("127.0.0.1", 0), _Cell)
        port = probe.server_address[1]
        probe.server_close()

        result = ZoikoTaxClient(f"http://127.0.0.1:{port}", timeout=5).get_capabilities()

        assert isinstance(result, Err)
        self.assertIsInstance(result.error, ZoikoTaxTransportError)
        self.assertIsNone(result.error.status)

    def test_the_transport_itself_will_not_open_a_local_file(self) -> None:
        transport = UrllibTransport()
        with self.assertRaises(OSError):
            transport(HttpRequest("GET", "file:///etc/hosts", {}, None, None))


if __name__ == "__main__":
    unittest.main()
