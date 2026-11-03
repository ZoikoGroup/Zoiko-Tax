"""Demo-only HTTP endpoint for the synthetic Reports dashboard -- DEMO ONLY.

DEMO ONLY. Never production. Synthetic public data only. No authority.

A small stdlib-only HTTP server (``http.server``, no provider SDK, no web
framework -- no new dependency) that lets a synthetic Reports dashboard ask
for a plain-language explanation of one or more rows from
``report_fixture.py``'s fixture, through the same governed, in-process
``Gateway`` the gRPC demo (``serve_live_demo.py``) uses -- governance, the
kill switch and the evidence record all run exactly as they do there. This
module never calls a provider directly; ``GroqDemoRuntime``
(``groq_demo_runtime.py``) does, and only after ``Gateway.invoke`` has
authorised the call.

Routes:

    GET  /demo/fixture            -> the whole fixture, as JSON.
    POST /demo/explain             body: {"kind": "report-figure" |
                                   "trend-point" | "filing-bucket" |
                                   "obligation-row" | "payment-row" |
                                   "shadow-variance",
                                   "ids": ["<row id>", ...]}
                                   -> {"outcome": "EXPLAINED" | "ABSTAINED" |
                                   "REVIEW_REQUIRED", "explanation": str,
                                   "cited_ids": [str], "reason": str,
                                   "provenance": {...} | None}

CORS is allowed for exactly one origin, ``http://localhost:13000`` -- no
other origin is ever reflected. Binds inside the container only; the
live-demo compose overlay is what publishes a host port, on 127.0.0.1 only
(see ``docker-compose.live-demo.yml``). Never prints or logs ``GROQ_API_KEY`` --
nothing on this HTTP surface ever carries it; the key only ever appears in
the ``Authorization`` header of the one outbound call ``GroqDemoRuntime``
makes, same as every other script in this directory.

No edit to intelligence/src: built only from ``server.py``'s public
``Gateway``/``GatewayError``/``load_config``, ``GroqDemoRuntime``, and
``serve_live_demo``'s own ``_env``/``_patched_routes`` (both already
generic over every use case the loaded registry declares -- adding
``explain-report-figure`` etc. to ``registry.live-demo.json`` needed no
change to either).

The deterministic guard (``apply_guard``) now lives in
``intelligence/src/ztax_gateway/explanation_guard.py`` and is imported
from there.  It is no longer defined in this module.  It never trusts the
model's own "abstain" flag alone. An explanation that cites no row, a
citation naming a row id outside the ones supplied, a number that is not
exactly one of the supplied rows' own values (or a simple percentage of two
of them), or a lifecycle / payment status word that is not the status of a
cited row, forces an ABSTAINED outcome regardless of what the model claimed.
The guard cannot prove a sentence is true -- see the note on ``apply_guard``
in ``ztax_gateway.explanation_guard``.

This endpoint has no login. It is a loopback-only demo surface and must never
be forwarded or exposed beyond the machine it runs on.
"""

from __future__ import annotations

import base64
import json
import logging
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

import report_fixture
import serve_live_demo
from groq_demo_runtime import GroqDemoConfigError, GroqDemoRuntime

from ztax_gateway.explanation_guard import (
    _numeric_values,  # noqa: F401 -- accessed as report_ai_http._numeric_values in tests
    apply_guard,
    check_guard,
)
from ztax_gateway.invocation_check_event import CheckReason, Verdict
from ztax_gateway.server import Gateway, GatewayError, load_config

__all__: list[str] = ["apply_guard", "explain", "main"]

log = logging.getLogger("ztax_gateway.report_ai_http")

# ---------------------------------------------------------------------------
# Demo-path: safe check-event helper
# ---------------------------------------------------------------------------

_CHECK_EVENT_WRITE_FAILED = "CHECK_EVENT_WRITE_FAILED"


def _record_answer_check_safe(
    gateway: Gateway,
    invocation_id: str,
    verdict: Verdict,
    reason: CheckReason,
    validator: str,
) -> None:
    """Write a check event; on failure log a fixed WARNING and continue.

    Demo path only. A failing sink must never drop the caller's reply.
    Never logs str(exc) or repr(exc) to prevent leaking runtime messages.
    """
    try:
        gateway.record_answer_check(invocation_id, verdict, reason, validator)
    except Exception:
        log.warning(
            "check_event_write_failed reason=%s",
            _CHECK_EVENT_WRITE_FAILED,
        )

# Bound inside the container only; docker-compose.live-demo.yml publishes
# this same number on the host's loopback interface only (127.0.0.1:18090).
_PORT = 18090

_ALLOWED_ORIGIN = "http://localhost:13000"

# Not load-bearing -- a fixed, synthetic tenant id distinct from
# live_demo.py's (...099), so the two demos' evidence records are never
# mistaken for one another in a shared log.
_TENANT = "00000000-0000-0000-0000-000000000098"

_VALIDATOR = "explanation_guard@1"

# Each explain kind runs under an already-registered governed use case:
# trend points and filing-pipeline buckets are dashboard figures, and a
# payment is a row of the obligations register's Payments tab. No new use
# case is registered for them.
_KIND_TO_USE_CASE: dict[str, str] = {
    "report-figure": "explain-report-figure",
    "trend-point": "explain-report-figure",
    "filing-bucket": "explain-report-figure",
    "obligation-row": "explain-obligation-row",
    "payment-row": "explain-obligation-row",
    "shadow-variance": "explain-shadow-variance",
}


# ---------------------------------------------------------------------------
# Building the call and reading the reply.
# ---------------------------------------------------------------------------


def _build_raw_call(use_case: str, kind: str, rows: list[dict[str, Any]]) -> bytes:
    """The raw gateway-call JSON for one /demo/explain request.

    The same wire shape live_demo.py's _raw_call and test_registry_dev.py's
    _raw_call use; the user content is just the JSON array of the rows the
    caller named, nothing else -- the model never sees a row it was not
    given.
    """
    doc: dict[str, Any] = {
        "kind": "EXTRACTION",
        "governance": {
            "tenant_id": _TENANT,
            "use_case": use_case,
            "authority_outcome": "A1",
            "risk_tier": "T1",
            "region": "local",
            "data_classes": ["P0"],
        },
        "subject_ref": f"report-explain:{kind}",
        "input_b64": base64.b64encode(json.dumps(rows).encode()).decode(),
    }
    return json.dumps(doc).encode()


def explain(gateway: Gateway, fixture: dict[str, Any], kind: str, ids: list[str]) -> dict[str, Any]:
    """Resolve *ids* against *fixture*, call *gateway*, apply the guard.

    Never raises for a bad *kind*/*ids*/Gateway outcome -- every case maps to
    one of the three documented outcomes instead, same discipline the
    classification-review flow uses (a review-required reply is an outcome,
    not an exception).
    """
    use_case = _KIND_TO_USE_CASE.get(kind)
    if use_case is None:
        return {
            "outcome": "REVIEW_REQUIRED",
            "explanation": "",
            "cited_ids": [],
            "reason": f"unknown kind {kind!r}",
            "provenance": None,
        }

    rows = report_fixture.rows_for_kind(fixture, kind, ids)
    if not rows:
        return {
            "outcome": "REVIEW_REQUIRED",
            "explanation": "",
            "cited_ids": [],
            "reason": "none of the given ids were found in this kind's fixture rows",
            "provenance": None,
        }

    raw_call = _build_raw_call(use_case, kind, rows)
    try:
        reply_bytes, invocation_id = gateway.invoke_with_id(raw_call)
    except GatewayError as exc:
        # Only the Gateway's fixed reason code goes back: exc.detail is free
        # text (it can carry a runtime or provider message) and stays out of
        # both the reply and this log.
        # A refused or killed call produces no answer, so it emits NO check event.
        log.info("explain use_case=%s outcome=REVIEW_REQUIRED reason=%s", use_case, exc.reason)
        return {
            "outcome": "REVIEW_REQUIRED",
            "explanation": "",
            "cited_ids": [],
            "reason": exc.reason,
            "provenance": None,
        }

    reply = json.loads(reply_bytes)
    fields = reply.get("fields", {})
    explanation = fields.get("explanation", "")
    try:
        cited_ids = json.loads(fields.get("cited_ids", "[]"))
    except json.JSONDecodeError:
        cited_ids = []
    model_abstain = fields.get("abstain") == "true"
    model_reason = fields.get("reason", "")

    provenance = {
        "useCase": use_case,
        "model": reply.get("model_profile", ""),
        "provider": reply.get("provider_profile", ""),
        "prompt": reply.get("prompt_profile", ""),
        "aiTrain": reply.get("ai_train_version", ""),
    }

    # Each decision below is logged next to the call: the outcome and a fixed
    # reason code only, never the model's text.
    if model_abstain:
        log.info("explain use_case=%s outcome=ABSTAINED reason=MODEL_ABSTAINED", use_case)
        if invocation_id is not None:
            _record_answer_check_safe(
                gateway, invocation_id, Verdict.ABSTAIN, CheckReason.MODEL_ABSTAINED, _VALIDATOR
            )
        return {
            "outcome": "ABSTAINED",
            "explanation": explanation,
            "cited_ids": cited_ids,
            "reason": model_reason or "the model abstained",
            "provenance": provenance,
        }

    result = check_guard(rows, cited_ids, explanation, invocation_id=invocation_id)
    if not result.ok:
        log.info("explain use_case=%s outcome=ABSTAINED reason=%s", use_case, result.code)
        if invocation_id is not None:
            _record_answer_check_safe(
                gateway, invocation_id, Verdict.ABSTAIN, CheckReason(result.code), _VALIDATOR
            )
        return {
            "outcome": "ABSTAINED",
            "explanation": explanation,
            "cited_ids": cited_ids,
            "reason": result.message,
            "provenance": provenance,
        }

    log.info("explain use_case=%s outcome=EXPLAINED reason=-", use_case)
    if invocation_id is not None:
        _record_answer_check_safe(
            gateway, invocation_id, Verdict.PASS, CheckReason.CHECKS_PASSED, _VALIDATOR
        )
    return {
        "outcome": "EXPLAINED",
        "explanation": explanation,
        "cited_ids": cited_ids,
        "reason": "",
        "provenance": provenance,
    }


# ---------------------------------------------------------------------------
# The HTTP surface itself.
# ---------------------------------------------------------------------------


def _make_handler(gateway: Gateway, fixture: dict[str, Any]) -> type[BaseHTTPRequestHandler]:
    class Handler(BaseHTTPRequestHandler):
        server_version = "ztax-report-ai-demo/1.0"

        def _cors(self) -> None:
            self.send_header("Access-Control-Allow-Origin", _ALLOWED_ORIGIN)
            self.send_header("Vary", "Origin")

        def _send_json(self, status: int, doc: dict[str, Any]) -> None:
            body = json.dumps(doc).encode("utf-8")
            self.send_response(status)
            self._cors()
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_OPTIONS(self) -> None:
            self.send_response(204)
            self._cors()
            self.send_header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
            self.send_header("Access-Control-Allow-Headers", "Content-Type")
            self.end_headers()

        def do_GET(self) -> None:
            if self.path == "/demo/fixture":
                self._send_json(200, fixture)
                return
            self._send_json(404, {"error": "not found"})

        def do_POST(self) -> None:
            if self.path != "/demo/explain":
                self._send_json(404, {"error": "not found"})
                return
            length = int(self.headers.get("Content-Length", "0") or "0")
            raw_body = self.rfile.read(length) if length else b""
            try:
                req = json.loads(raw_body) if raw_body else {}
            except json.JSONDecodeError:
                self._send_json(400, {"error": "request body is not JSON"})
                return
            kind = req.get("kind")
            ids = req.get("ids")
            if not isinstance(kind, str) or kind not in _KIND_TO_USE_CASE:
                self._send_json(
                    400, {"error": f"kind must be one of {sorted(_KIND_TO_USE_CASE)}"}
                )
                return
            if not isinstance(ids, list) or not ids or not all(isinstance(i, str) for i in ids):
                self._send_json(400, {"error": "ids must be a non-empty list of strings"})
                return
            self._send_json(200, explain(gateway, fixture, kind, ids))

        def log_message(self, format: str, *args: object) -> None:
            # Quiet by default -- never the key (nothing on this surface
            # ever carries it), but also no reason to spam stderr per request
            # in a demo.
            pass

    return Handler


def main() -> int:
    """Build the real Gateway and serve the two demo-only routes.

    Mirrors serve_live_demo.py's own env handling and refusal messages,
    minus the gRPC-specific address/insecure-local/TLS concerns this plain
    HTTP server does not have.
    """
    # Without this the Gateway's evidence records (logger ztax_gateway.evidence,
    # INFO) are dropped in this process; serve_live_demo.py configures it the
    # same way.
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
    try:
        runtime = GroqDemoRuntime()
    except GroqDemoConfigError as exc:
        print(f"report_ai_http: {exc}", file=sys.stderr)
        return 2

    config = serve_live_demo._env("ZTAX_GATEWAY_CONFIG")
    region = serve_live_demo._env("ZTAX_GATEWAY_REGION")
    train = serve_live_demo._env("ZTAX_GATEWAY_AI_TRAIN")
    if not (config and region and train):
        print(
            "report_ai_http: ZTAX_GATEWAY_CONFIG, ZTAX_GATEWAY_REGION and "
            "ZTAX_GATEWAY_AI_TRAIN are required",
            file=sys.stderr,
        )
        return 2

    model = serve_live_demo._env("GROQ_MODEL")
    registry, routes = load_config(Path(config))
    routes = serve_live_demo._patched_routes(registry, routes, model)
    gateway = Gateway(registry, routes, runtime, region, train, kill_state_path=Path(config))
    fixture = report_fixture.generate_fixture()

    handler_cls = _make_handler(gateway, fixture)
    server = ThreadingHTTPServer(("0.0.0.0", _PORT), handler_cls)
    print(
        f"report_ai_http: DEMO ONLY -- serving synthetic report explanations on "
        f"0.0.0.0:{_PORT}, CORS allowed for {_ALLOWED_ORIGIN} only"
    )
    server.serve_forever()
    return 0


if __name__ == "__main__":
    sys.exit(main())
