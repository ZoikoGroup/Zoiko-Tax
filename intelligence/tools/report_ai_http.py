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
    POST /demo/classify            body: {"subject_ref": str,
                                   "description": str}
                                   -> {"outcome": "PROPOSED" | "ABSTAIN" |
                                   "REFUSED" | "NOT_CONFIGURED",
                                   "evidence_id": str | None,
                                   "verdict": str, "reason_code": str,
                                   "list_version": str | None,
                                   "proposed_code": str}  # only when PROPOSED
                                   "confidence_calibrated": false}

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

The ontology guard (``apply_ontology_guard``) in this module validates
``POST /demo/classify`` proposals against a synthetic approved-ontology list
loaded from the path in ``ZTAX_ONTOLOGY_APPROVED_LIST``.  When that
environment variable is unset, the route returns ``NOT_CONFIGURED`` and all
other routes are unaffected.  When it is set to an unreadable or invalid
file, the server refuses to start.

This endpoint has no login. It is a loopback-only demo surface and must never
be forwarded or exposed beyond the machine it runs on.
"""

from __future__ import annotations

import base64
import json
import logging
import os
import sys
from datetime import UTC, date, datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

import report_fixture
import serve_live_demo
from groq_demo_runtime import (
    GATEWAY_RUNTIME_TIMEOUT_SECONDS,
    GroqDemoConfigError,
    GroqDemoRuntime,
)

from ztax_gateway.explanation_guard import (
    _numeric_values,  # noqa: F401 -- accessed as report_ai_http._numeric_values in tests
    apply_guard,
    check_guard,
)
from ztax_gateway.gateway_factory import GatewayFactoryError, build_gateway
from ztax_gateway.invocation_check_event import CheckReason, Verdict
from ztax_gateway.ontology_guard import (
    ApprovedOntologyList,
    GuardVerdict,
    OntologyGuardError,
    check_code,
    load_approved_list,
)
from ztax_gateway.server import Gateway, GatewayError

__all__: list[str] = ["apply_guard", "apply_ontology_guard", "classify", "explain", "main"]

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

# Use case and validator for the ontology-guard classify route.
_CLASSIFY_USE_CASE = "classification-review"
_ONTOLOGY_GUARD_VALIDATOR = "ontology_guard@1"

# Maximum byte size for a /demo/classify request body (same order of
# magnitude as the explain route's implicit limit from Content-Length).
_CLASSIFY_MAX_BODY = 32 * 1024  # 32 KiB

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
# Ontology-guard helper (pure, no I/O, no logging)
# ---------------------------------------------------------------------------

# Mapping from GuardVerdict to (Verdict, CheckReason) for ABSTAIN cases.
# ALLOWED is handled by the caller, not this table.
_VERDICT_MAP: dict[GuardVerdict, tuple[Verdict, CheckReason]] = {
    GuardVerdict.UNKNOWN: (Verdict.ABSTAIN, CheckReason.GUARD_ONTOLOGY_UNKNOWN),
    GuardVerdict.NOT_YET_EFFECTIVE: (
        Verdict.ABSTAIN,
        CheckReason.GUARD_ONTOLOGY_NOT_YET_EFFECTIVE,
    ),
    GuardVerdict.EXPIRED: (Verdict.ABSTAIN, CheckReason.GUARD_ONTOLOGY_EXPIRED),
}


def apply_ontology_guard(
    proposed_code: str,
    as_of: date,
    approved_list: ApprovedOntologyList,
) -> tuple[Verdict, CheckReason]:
    """Check *proposed_code* against *approved_list* as of *as_of*.

    Returns ``(Verdict.PASS, CheckReason.CHECKS_PASSED)`` when ALLOWED.
    Returns ``(Verdict.ABSTAIN, <reason>)`` for UNKNOWN, NOT_YET_EFFECTIVE
    or EXPIRED.

    Does NOT call ``strip_heading_marker``.  A code with a heading marker
    (``#ontology:...``) is not an approved code and must return ABSTAIN with
    GUARD_ONTOLOGY_UNKNOWN.

    DEMO ONLY -- the approved list is synthetic; not a production list.
    """
    verdict = check_code(proposed_code, as_of, approved_list)
    if verdict is GuardVerdict.ALLOWED:
        return Verdict.PASS, CheckReason.CHECKS_PASSED
    return _VERDICT_MAP[verdict]


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


def _build_classify_raw_call(subject_ref: str, description: str) -> bytes:
    """The raw gateway-call JSON for one /demo/classify request.

    Uses the same wire shape as live_demo.py's _raw_call.
    The user content is the description string only, encoded as JSON.
    """
    doc: dict[str, Any] = {
        "kind": "CLASSIFICATION_PROPOSAL",
        "governance": {
            "tenant_id": _TENANT,
            "use_case": _CLASSIFY_USE_CASE,
            "authority_outcome": "A1",
            "risk_tier": "T1",
            "region": "local",
            "data_classes": ["P0"],
        },
        "subject_ref": subject_ref,
        "input_b64": base64.b64encode(
            json.dumps({"description": description}).encode()
        ).decode(),
    }
    return json.dumps(doc).encode()


def classify(
    gateway: Gateway,
    subject_ref: str,
    description: str,
    as_of: date,
    approved_list: ApprovedOntologyList | None,
) -> dict[str, Any]:
    """Propose an ontology classification via *gateway* and guard the result.

    *as_of* is the reference date for the effectivity window check; the HTTP
    handler passes ``date.today()`` and tests pass a fixed date.

    *approved_list* is ``None`` when ``ZTAX_ONTOLOGY_APPROVED_LIST`` is unset.
    In that case the route returns ``NOT_CONFIGURED`` without calling the
    gateway; ``/demo/explain`` is unaffected.

    Never raises for a Gateway outcome -- every case maps to one of the four
    documented outcomes (PROPOSED, ABSTAIN, REFUSED, NOT_CONFIGURED).
    Never returns the rejected code or any model text on ABSTAIN.
    """
    if approved_list is None:
        return {
            "outcome": "NOT_CONFIGURED",
            "evidence_id": None,
            "verdict": Verdict.ABSTAIN,
            "reason_code": CheckReason.GUARD_ONTOLOGY_UNKNOWN,
            "list_version": None,
            "confidence_calibrated": False,
        }

    raw_call = _build_classify_raw_call(subject_ref, description)
    try:
        reply_bytes, invocation_id = gateway.invoke_with_id(raw_call)
    except GatewayError as exc:
        # A refused or killed call: only the fixed reason code goes back.
        log.info(
            "classify use_case=%s outcome=REFUSED reason=%s",
            _CLASSIFY_USE_CASE,
            exc.reason,
        )
        return {
            "outcome": "REFUSED",
            "evidence_id": None,
            "verdict": Verdict.BLOCK,
            "reason_code": exc.reason,
            "list_version": approved_list.list_version,
            "confidence_calibrated": False,
        }

    reply = json.loads(reply_bytes)
    proposed_code: str = reply.get("proposed_code", "")

    verdict, reason_code = apply_ontology_guard(proposed_code, as_of, approved_list)

    if verdict is Verdict.ABSTAIN:
        log.info(
            "classify use_case=%s outcome=ABSTAIN reason=%s",
            _CLASSIFY_USE_CASE,
            reason_code,
        )
        if invocation_id is not None:
            _record_answer_check_safe(
                gateway,
                invocation_id,
                Verdict.ABSTAIN,
                reason_code,
                _ONTOLOGY_GUARD_VALIDATOR,
            )
        # Never return the rejected code or any model text on ABSTAIN.
        return {
            "outcome": "ABSTAIN",
            "evidence_id": invocation_id,
            "verdict": verdict,
            "reason_code": reason_code,
            "list_version": approved_list.list_version,
            "confidence_calibrated": False,
        }

    # PASS: approved code -- include proposed_code in the response.
    log.info("classify use_case=%s outcome=PROPOSED reason=-", _CLASSIFY_USE_CASE)
    if invocation_id is not None:
        _record_answer_check_safe(
            gateway,
            invocation_id,
            Verdict.PASS,
            CheckReason.CHECKS_PASSED,
            _ONTOLOGY_GUARD_VALIDATOR,
        )
    return {
        "outcome": "PROPOSED",
        "evidence_id": invocation_id,
        "verdict": verdict,
        "reason_code": reason_code,
        "list_version": approved_list.list_version,
        "proposed_code": proposed_code,
        "confidence_calibrated": False,
    }


# ---------------------------------------------------------------------------
# The HTTP surface itself.
# ---------------------------------------------------------------------------


def _make_handler(
    gateway: Gateway,
    fixture: dict[str, Any],
    approved_list: ApprovedOntologyList | None,
) -> type[BaseHTTPRequestHandler]:
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
            if self.path == "/demo/explain":
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
            elif self.path == "/demo/classify":
                length = int(self.headers.get("Content-Length", "0") or "0")
                if length > _CLASSIFY_MAX_BODY:
                    self._send_json(400, {"error": "request body too large"})
                    return
                raw_body = self.rfile.read(length) if length else b""
                try:
                    req = json.loads(raw_body) if raw_body else {}
                except json.JSONDecodeError:
                    self._send_json(400, {"error": "request body is not JSON"})
                    return
                subject_ref = req.get("subject_ref")
                description = req.get("description")
                if not isinstance(subject_ref, str) or not subject_ref.strip():
                    self._send_json(400, {"error": "subject_ref must be a non-empty string"})
                    return
                if not isinstance(description, str) or not description.strip():
                    self._send_json(400, {"error": "description must be a non-empty string"})
                    return
                self._send_json(
                    200,
                    classify(
                        gateway,
                        subject_ref,
                        description,
                        datetime.now(UTC).date(),
                        approved_list,
                    ),
                )
            else:
                self._send_json(404, {"error": "not found"})

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
    try:
        gateway = build_gateway(
            Path(config),
            region,
            train,
            runtime_factory=runtime.with_prompt_registry,
            route_transform=lambda registry, routes: serve_live_demo._patched_routes(
                registry, routes, model
            ),
            runtime_timeout_seconds=GATEWAY_RUNTIME_TIMEOUT_SECONDS,
        )
    except (GatewayFactoryError, GroqDemoConfigError) as exc:
        print(f"report_ai_http: {exc}", file=sys.stderr)
        return 2
    fixture = report_fixture.generate_fixture()

    # Load the optional approved-ontology list from the env var.
    # Unset → approved_list is None → /demo/classify returns NOT_CONFIGURED.
    # Set but unreadable or invalid → print fixed message and exit non-zero
    # before binding the port.
    approved_list: ApprovedOntologyList | None = None
    list_path = os.environ.get("ZTAX_ONTOLOGY_APPROVED_LIST", "").strip()
    if list_path:
        try:
            approved_list = load_approved_list(Path(list_path).read_text(encoding="utf-8"))
        except (OntologyGuardError, OSError):
            print(
                "report_ai_http: approved ontology list: invalid or unreadable",
                file=sys.stderr,
            )
            return 2

    handler_cls = _make_handler(gateway, fixture, approved_list)
    server = ThreadingHTTPServer(("0.0.0.0", _PORT), handler_cls)
    print(
        f"report_ai_http: DEMO ONLY -- serving synthetic report explanations on "
        f"0.0.0.0:{_PORT}, CORS allowed for {_ALLOWED_ORIGIN} only"
    )
    server.serve_forever()
    return 0


if __name__ == "__main__":
    sys.exit(main())
