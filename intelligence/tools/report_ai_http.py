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
live-demo compose overlay is what publishes a host port (see
``docker-compose.live-demo.yml``). Never prints or logs ``GROQ_API_KEY`` --
nothing on this HTTP surface ever carries it; the key only ever appears in
the ``Authorization`` header of the one outbound call ``GroqDemoRuntime``
makes, same as every other script in this directory.

No edit to intelligence/src: built only from ``server.py``'s public
``Gateway``/``GatewayError``/``load_config``, ``GroqDemoRuntime``, and
``serve_live_demo``'s own ``_env``/``_patched_routes`` (both already
generic over every use case the loaded registry declares -- adding
``explain-report-figure`` etc. to ``registry.live-demo.json`` needed no
change to either).

The deterministic guard below (``apply_guard``) is this module's own
check, not the model's: it never trusts the model's own "abstain" flag
alone. A citation naming a row id outside the ones supplied, or a number in
the explanation that is not one of the supplied rows' own values (or a
simple percentage of two of them), forces an ABSTAINED outcome regardless
of what the model claimed.
"""

from __future__ import annotations

import base64
import json
import re
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

import report_fixture
import serve_live_demo
from groq_demo_runtime import GroqDemoConfigError, GroqDemoRuntime

from ztax_gateway.server import Gateway, GatewayError, load_config

__all__: list[str] = ["apply_guard", "explain", "main"]

# Bound inside the container only; docker-compose.live-demo.yml publishes
# this same number as the host port (18090:18090).
_PORT = 18090

_ALLOWED_ORIGIN = "http://localhost:13000"

# Not load-bearing -- a fixed, synthetic tenant id distinct from
# live_demo.py's (...099), so the two demos' evidence records are never
# mistaken for one another in a shared log.
_TENANT = "00000000-0000-0000-0000-000000000098"

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
# The deterministic guard. Pure functions -- no I/O, no model, no Gateway.
# ---------------------------------------------------------------------------

# A number as a person writes it: either thousands-grouped ("1,443,155.15":
# 1-3 leading digits, then complete ",ddd" groups, not glued to other digits)
# or plain ("1443155.15"). Malformed grouping ("1,44,3155") never matches the
# grouped form, so its pieces are checked one by one -- stricter, not looser.
# Currency symbols and codes ("$", "€", "USD") are not digits and fall away.
_NUMBER_RE = re.compile(r"(?<![\d.,])\d{1,3}(?:,\d{3})+(?!\d)(?:\.\d+)?%?|\d+(?:\.\d+)?%?")
_PLAIN_NUMBER_RE = re.compile(r"^\d+(?:\.\d+)?$")
_CLOSE_ENOUGH = 0.005
_PERCENT_CLOSE_ENOUGH = 0.1


def _string_values(rows: list[dict[str, Any]]) -> list[str]:
    """Every string value in *rows*, including the strings inside list values."""
    found: list[str] = []
    for row in rows:
        for value in row.values():
            items = value if isinstance(value, list) else [value]
            found.extend(item for item in items if isinstance(item, str) and item)
    return found


def _verbatim_strings(rows: list[dict[str, Any]]) -> list[str]:
    """Every non-numeric string value in *rows* (ids, linked ids, dates,
    months, periods, labels), longest first.

    These contain digits that are not number claims: a row's own id
    ("OBL-001"), the obligation a payment or shadow row links to, a due date
    ("2026-10-31"), a trend month ("2026-09") or a period ("2026-Q3"). Quoting
    one verbatim repeats the supplied rows; it never introduces a number the
    rows do not hold.
    """
    found = {s for s in _string_values(rows) if not _PLAIN_NUMBER_RE.match(s)}
    return sorted(found, key=len, reverse=True)


# --- date, month and period components ----------------------------------------
#
# A person quotes "2026-10-31" as "31 October 2026" and "2026-Q3" as "Q3 2026".
# Those digits are components of a supplied row string, not new numbers -- but
# only in these narrow shapes, so a bare "10" or "3" (a count) is never
# excused just because some date happens to contain a 10 or a 3:
#
#   * a 4-digit year that is the year part of a supplied date / month / period;
#   * a 1-2 digit day written directly beside a month NAME, and only when that
#     exact month and day occur together in a supplied full date (YYYY-MM-DD);
#   * "Q<n>" where a supplied period has that quarter ("YYYY-Q<n>").
#
# Nothing else is accepted: no decimals, no thousands groups, no percentages.

_FULL_DATE_RE = re.compile(r"(?<!\d)(\d{4})-(\d{2})-(\d{2})(?!\d)")
_YEAR_MONTH_RE = re.compile(r"(?<!\d)(\d{4})-(\d{2})(?!\d)")
_YEAR_QUARTER_RE = re.compile(r"(?<!\d)(\d{4})-Q([1-4])(?!\d)")
_QUARTER_TOKEN_RE = re.compile(r"(?<![A-Za-z0-9])Q([1-4])(?!\d)")

_MONTH_NUMBERS: dict[str, int] = {
    "january": 1, "february": 2, "march": 3, "april": 4, "may": 5, "june": 6,
    "july": 7, "august": 8, "september": 9, "october": 10, "november": 11, "december": 12,
    "jan": 1, "feb": 2, "mar": 3, "apr": 4, "jun": 6, "jul": 7, "aug": 8,
    "sep": 9, "sept": 9, "oct": 10, "nov": 11, "dec": 12,
}
_MONTH_NAME = "(" + "|".join(sorted(_MONTH_NUMBERS, key=len, reverse=True)) + r")\.?"
_MONTH_NAME_BEFORE_RE = re.compile(rf"\b{_MONTH_NAME}\s+$", re.IGNORECASE)
_MONTH_NAME_AFTER_RE = re.compile(rf"\s+(?:of\s+)?{_MONTH_NAME}\b", re.IGNORECASE)


def _date_components(
    rows: list[dict[str, Any]],
) -> tuple[set[int], set[tuple[int, int]], set[int]]:
    """(years, (month, day) pairs of full dates, quarters) found in *rows*' strings."""
    years: set[int] = set()
    month_days: set[tuple[int, int]] = set()
    quarters: set[int] = set()
    for s in _string_values(rows):
        for year, month, day in _FULL_DATE_RE.findall(s):
            if 1 <= int(month) <= 12 and 1 <= int(day) <= 31:
                years.add(int(year))
                month_days.add((int(month), int(day)))
        for year, month in _YEAR_MONTH_RE.findall(s):
            if 1 <= int(month) <= 12:
                years.add(int(year))
        for year, quarter in _YEAR_QUARTER_RE.findall(s):
            years.add(int(year))
            quarters.add(int(quarter))
    return years, month_days, quarters


def _number_matches(
    text: str, verbatim: list[str], quarters: set[int]
) -> list[re.Match[str]]:
    """Every number (optionally a "N%") in *text*, as regex matches.

    Strings quoted verbatim from the supplied rows, and "Q<n>" tokens for a
    quarter a supplied period really has, are blanked out first -- with
    spaces of the same length, so each match's position still indexes *text*.
    """
    blanked = text
    for value in verbatim:
        blanked = blanked.replace(value, " " * len(value))
    blanked = _QUARTER_TOKEN_RE.sub(
        lambda m: " " * len(m.group()) if int(m.group(1)) in quarters else m.group(), blanked
    )
    return list(_NUMBER_RE.finditer(blanked))


def _is_date_component(
    match: re.Match[str], text: str, years: set[int], month_days: set[tuple[int, int]]
) -> bool:
    """True if *match* is a year, or a day beside its month name, of a supplied date."""
    token = match.group()
    if not token.isdigit():
        return False
    if len(token) == 4:
        return int(token) in years
    if len(token) > 2:
        return False
    day = int(token)
    before = _MONTH_NAME_BEFORE_RE.search(text[: match.start()])
    if before is not None and (_MONTH_NUMBERS[before.group(1).lower()], day) in month_days:
        return True
    after = _MONTH_NAME_AFTER_RE.match(text[match.end() :])
    return after is not None and (_MONTH_NUMBERS[after.group(1).lower()], day) in month_days


def _numeric_values(rows: list[dict[str, Any]]) -> set[float]:
    """Every numeric value appearing anywhere in *rows* (ids are not numbers)."""
    values: set[float] = set()
    for row in rows:
        for key, value in row.items():
            if key == "id" or not isinstance(value, str):
                continue
            if _PLAIN_NUMBER_RE.match(value):
                values.add(float(value))
    return values


def _value_allowed(num_str: str, allowed: set[float]) -> bool:
    try:
        value = float(num_str.replace(",", ""))
    except ValueError:
        return False
    return any(abs(value - a) < _CLOSE_ENOUGH for a in allowed)


def _percentage_allowed(num_str: str, allowed: set[float]) -> bool:
    """True if num_str ("N%") is a simple percentage of two allowed values."""
    if not num_str.endswith("%"):
        return False
    try:
        target = float(num_str[:-1].replace(",", ""))
    except ValueError:
        return False
    for a in allowed:
        for b in allowed:
            if b == 0:
                continue
            if abs((a / b * 100) - target) < _PERCENT_CLOSE_ENOUGH:
                return True
    return False


def apply_guard(
    rows: list[dict[str, Any]], cited_ids: list[str], explanation: str
) -> tuple[bool, str]:
    """The demo service's own check -- never trusts the model's own claims.

    Returns ``(True, "")`` only if every cited id is one of *rows*' own ids
    and every number in *explanation* is (1) one of *rows*' own numeric
    values, (2) a simple percentage of two of them, or (3) a year / day /
    quarter that quotes a supplied date, month or period (see "date, month
    and period components" above). There is no sum, subtotal, average or
    any other aggregate rule: a total passes only if a supplied row carries it
    as a field. Otherwise returns ``(False, reason)``; the caller turns that
    into an ABSTAINED outcome.
    """
    row_ids = {str(row["id"]) for row in rows}
    foreign = sorted({c for c in cited_ids if c not in row_ids})
    if foreign:
        return False, f"cited id(s) not in the supplied rows: {', '.join(foreign)}"

    allowed = _numeric_values(rows)
    years, month_days, quarters = _date_components(rows)
    for match in _number_matches(explanation, _verbatim_strings(rows), quarters):
        num = match.group()
        if num.endswith("%"):
            # (2) the documented recomputable percentage of two supplied numbers.
            if _percentage_allowed(num, allowed):
                continue
        elif _value_allowed(num, allowed):
            # (1) an exact row value.
            continue
        elif _is_date_component(match, explanation, years, month_days):
            # (3) a year / day beside its month name, quoting a supplied date.
            continue
        return False, f"explanation states a number not present in the supplied rows: {num!r}"
    return True, ""


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
        reply_bytes = gateway.invoke(raw_call)
    except GatewayError as exc:
        return {
            "outcome": "REVIEW_REQUIRED",
            "explanation": "",
            "cited_ids": [],
            "reason": f"{exc.reason}: {exc.detail}",
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

    if model_abstain:
        return {
            "outcome": "ABSTAINED",
            "explanation": explanation,
            "cited_ids": cited_ids,
            "reason": model_reason or "the model abstained",
            "provenance": provenance,
        }

    ok, guard_reason = apply_guard(rows, cited_ids, explanation)
    if not ok:
        return {
            "outcome": "ABSTAINED",
            "explanation": explanation,
            "cited_ids": cited_ids,
            "reason": guard_reason,
            "provenance": provenance,
        }

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
    gateway = Gateway(registry, routes, runtime, region, train)
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
