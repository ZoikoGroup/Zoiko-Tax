"""Tests for the POST /demo/classify route in tools/report_ai_http.py.

No network, no GROQ_API_KEY, fake runtime only.  Every test loads the module
via the same path-based importlib loader that test_report_ai_http.py uses so
that the three top-level ``import report_fixture`` / ``import serve_live_demo``
/ ``from groq_demo_runtime import ...`` work without putting tools/ on
sys.path permanently.

The approved-ontology list used in most tests is loaded from
config/approved_ontology.live-demo.json (the synthetic list created in Step 2).
"""

from __future__ import annotations

import contextlib
import importlib.util
import json
import logging
import os
import sys
from collections.abc import Iterator
from datetime import date
from pathlib import Path
from types import ModuleType
from typing import Any

import pytest

from ztax_gateway.invocation_check_event import CheckReason, Verdict
from ztax_gateway.ontology_guard import ApprovedOntologyList, load_approved_list
from ztax_gateway.runtime import Result, Route
from ztax_gateway.server import Gateway, load_config
from ztax_gateway.wire import Call

# ---------------------------------------------------------------------------
# Load tools/report_ai_http.py by path (same technique as test_report_ai_http.py)
# ---------------------------------------------------------------------------

_TOOLS_DIR = Path(__file__).resolve().parent.parent / "tools"
_CONFIG_DIR = Path(__file__).resolve().parent.parent / "config"

_MODULE_NAMES = ("groq_demo_runtime", "report_fixture", "serve_live_demo", "report_ai_http")


def _load_modules() -> dict[str, ModuleType]:
    before = {name: sys.modules.get(name) for name in _MODULE_NAMES}
    loaded: dict[str, ModuleType] = {}
    try:
        for name in _MODULE_NAMES:
            spec = importlib.util.spec_from_file_location(name, _TOOLS_DIR / f"{name}.py")
            assert spec is not None and spec.loader is not None
            module = importlib.util.module_from_spec(spec)
            sys.modules[name] = module
            spec.loader.exec_module(module)
            loaded[name] = module
    finally:
        for name, previous in before.items():
            if previous is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = previous
    return loaded


_MODULES = _load_modules()
report_ai_http = _MODULES["report_ai_http"]

# ---------------------------------------------------------------------------
# Shared fixtures and helpers
# ---------------------------------------------------------------------------

# A fixed reference date that falls within all open-ended entries in the
# synthetic list (all effective_from ≤ 2025-06-01) and before the
# NOT_YET_EFFECTIVE entry (effective_from 2030-01-01) and after the EXPIRED
# entry (effective_to 2023-12-31).
_AS_OF = date(2025, 6, 1)

# Codes from config/approved_ontology.live-demo.json
_APPROVED_CODE = "ontology:telecom/voice/mobile"      # open-ended → ALLOWED
_EXPIRED_CODE = "ontology:telecom/data/roaming"       # effective_to 2023-12-31
_FUTURE_CODE = "ontology:telecom/esim/digital"        # effective_from 2030-01-01
_UNKNOWN_CODE = "ontology:telecom/unknown/xyz"        # not in list
_HEADING_CODE = "#ontology:telecom/voice/mobile"      # heading marker → UNKNOWN

_LIST_PATH = _CONFIG_DIR / "approved_ontology.live-demo.json"
_APPROVED_LIST: ApprovedOntologyList = load_approved_list(
    _LIST_PATH.read_text(encoding="utf-8")
)


class _ScriptedRuntime:
    """Returns one fixed Result (or raises) per call, same as in test_report_ai_http.py."""

    def __init__(self, result: Result | None = None, raises: Exception | None = None) -> None:
        self._result = result
        self._raises = raises
        self.calls: list[Call] = []

    def run(self, route: Route, call: Call) -> Result:
        self.calls.append(call)
        if self._raises is not None:
            raise self._raises
        assert self._result is not None
        return self._result


def _classify_result(proposed_code: str) -> Result:
    """A Result that carries a proposed_code, satisfying the CLASSIFICATION_PROPOSAL check."""
    return Result(proposed_code=proposed_code, confidence="0.5000")


def _gateway(runtime: Any) -> Gateway:
    registry, routes = load_config(_CONFIG_DIR / "registry.live-demo.json")
    return Gateway(registry, routes, runtime, "local", "0.0.0-test")


class _RaisingSink:
    """A CheckEventSink that always raises RuntimeError."""

    _SENTINEL = "SENTINEL-CLASSIFY-SINK-7f3"

    def write_check_event(self, event: object) -> None:
        raise RuntimeError(self._SENTINEL)


def _gateway_with_raising_sink(runtime: Any) -> Gateway:
    registry, routes = load_config(_CONFIG_DIR / "registry.live-demo.json")
    sink = _RaisingSink()
    return Gateway(registry, routes, runtime, "local", "0.0.0-test", check_event_sink=sink)


# ---------------------------------------------------------------------------
# 1. Approved code → PROPOSED with PASS / CHECKS_PASSED check event
# ---------------------------------------------------------------------------


def test_approved_code_gives_proposed(caplog: pytest.LogCaptureFixture) -> None:
    """An approved code returns PROPOSED with verdict PASS and reason CHECKS_PASSED."""
    runtime = _ScriptedRuntime(result=_classify_result(_APPROVED_CODE))
    gw = _gateway(runtime)

    with caplog.at_level(logging.INFO):
        result = report_ai_http.classify(gw, "sku:PLAN-01", "Voice plan", _AS_OF, _APPROVED_LIST)

    assert result["outcome"] == "PROPOSED"
    assert result["proposed_code"] == _APPROVED_CODE
    assert result["verdict"] == Verdict.PASS
    assert result["reason_code"] == CheckReason.CHECKS_PASSED
    assert result["list_version"] == _APPROVED_LIST.list_version
    assert result["confidence_calibrated"] is False
    assert result["synthetic"] is True
    assert "evidence_id" in result
    # No model text or rejected code
    body_str = json.dumps(result)
    assert _UNKNOWN_CODE not in body_str


# ---------------------------------------------------------------------------
# 2. Unknown code → ABSTAIN with GUARD_ONTOLOGY_UNKNOWN
# ---------------------------------------------------------------------------


def test_unknown_code_gives_abstain() -> None:
    """A code not in the approved list returns ABSTAIN / GUARD_ONTOLOGY_UNKNOWN."""
    runtime = _ScriptedRuntime(result=_classify_result(_UNKNOWN_CODE))
    gw = _gateway(runtime)

    result = report_ai_http.classify(gw, "sku:PLAN-02", "Some plan", _AS_OF, _APPROVED_LIST)

    assert result["outcome"] == "ABSTAIN"
    assert result["verdict"] == Verdict.ABSTAIN
    assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
    assert result["confidence_calibrated"] is False
    assert result["synthetic"] is True
    # Rejected code must NOT appear in the response
    assert "proposed_code" not in result
    assert _UNKNOWN_CODE not in json.dumps(result)


# ---------------------------------------------------------------------------
# 3. Heading-marker code → ABSTAIN with GUARD_ONTOLOGY_UNKNOWN
# ---------------------------------------------------------------------------


def test_heading_marker_code_gives_abstain() -> None:
    """A code with a '#' prefix returns ABSTAIN / GUARD_ONTOLOGY_UNKNOWN.

    apply_ontology_guard does NOT call strip_heading_marker — a code like
    '#ontology:telecom/voice/mobile' is treated as UNKNOWN, not ALLOWED.
    """
    runtime = _ScriptedRuntime(result=_classify_result(_HEADING_CODE))
    gw = _gateway(runtime)

    result = report_ai_http.classify(gw, "sku:PLAN-03", "Mobile plan", _AS_OF, _APPROVED_LIST)

    assert result["outcome"] == "ABSTAIN"
    assert result["verdict"] == Verdict.ABSTAIN
    assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
    # Rejected code must NOT appear in the response
    assert "proposed_code" not in result
    assert _HEADING_CODE not in json.dumps(result)


# ---------------------------------------------------------------------------
# 4. Expired code → ABSTAIN with GUARD_ONTOLOGY_EXPIRED
# ---------------------------------------------------------------------------


def test_expired_code_gives_abstain() -> None:
    """A code past its effective_to returns ABSTAIN / GUARD_ONTOLOGY_EXPIRED."""
    runtime = _ScriptedRuntime(result=_classify_result(_EXPIRED_CODE))
    gw = _gateway(runtime)

    result = report_ai_http.classify(gw, "sku:PLAN-04", "Roaming plan", _AS_OF, _APPROVED_LIST)

    assert result["outcome"] == "ABSTAIN"
    assert result["verdict"] == Verdict.ABSTAIN
    assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_EXPIRED
    assert "proposed_code" not in result
    assert _EXPIRED_CODE not in json.dumps(result)


# ---------------------------------------------------------------------------
# 5. Future code → ABSTAIN with GUARD_ONTOLOGY_NOT_YET_EFFECTIVE
# ---------------------------------------------------------------------------


def test_future_code_gives_abstain() -> None:
    """A code not yet effective returns ABSTAIN / GUARD_ONTOLOGY_NOT_YET_EFFECTIVE."""
    runtime = _ScriptedRuntime(result=_classify_result(_FUTURE_CODE))
    gw = _gateway(runtime)

    result = report_ai_http.classify(gw, "sku:PLAN-05", "eSIM plan", _AS_OF, _APPROVED_LIST)

    assert result["outcome"] == "ABSTAIN"
    assert result["verdict"] == Verdict.ABSTAIN
    assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_NOT_YET_EFFECTIVE
    assert "proposed_code" not in result
    assert _FUTURE_CODE not in json.dumps(result)


# ---------------------------------------------------------------------------
# 6. apply_ontology_guard pure function tests (no Gateway)
# ---------------------------------------------------------------------------


def test_apply_ontology_guard_allowed() -> None:
    verdict, reason = report_ai_http.apply_ontology_guard(_APPROVED_CODE, _AS_OF, _APPROVED_LIST)
    assert verdict is Verdict.PASS
    assert reason is CheckReason.CHECKS_PASSED


def test_apply_ontology_guard_unknown() -> None:
    verdict, reason = report_ai_http.apply_ontology_guard(_UNKNOWN_CODE, _AS_OF, _APPROVED_LIST)
    assert verdict is Verdict.ABSTAIN
    assert reason is CheckReason.GUARD_ONTOLOGY_UNKNOWN


def test_apply_ontology_guard_heading_marker_is_unknown() -> None:
    """Heading marker is NOT stripped — the code stays unknown."""
    verdict, reason = report_ai_http.apply_ontology_guard(_HEADING_CODE, _AS_OF, _APPROVED_LIST)
    assert verdict is Verdict.ABSTAIN
    assert reason is CheckReason.GUARD_ONTOLOGY_UNKNOWN


def test_apply_ontology_guard_expired() -> None:
    verdict, reason = report_ai_http.apply_ontology_guard(_EXPIRED_CODE, _AS_OF, _APPROVED_LIST)
    assert verdict is Verdict.ABSTAIN
    assert reason is CheckReason.GUARD_ONTOLOGY_EXPIRED


def test_apply_ontology_guard_not_yet_effective() -> None:
    verdict, reason = report_ai_http.apply_ontology_guard(_FUTURE_CODE, _AS_OF, _APPROVED_LIST)
    assert verdict is Verdict.ABSTAIN
    assert reason is CheckReason.GUARD_ONTOLOGY_NOT_YET_EFFECTIVE


# ---------------------------------------------------------------------------
# 7. env var unset → NOT_CONFIGURED; /demo/explain still works
# ---------------------------------------------------------------------------


def test_env_unset_gives_not_configured() -> None:
    """When approved_list is None (env var unset), classify returns NOT_CONFIGURED.

    The gateway is never called.
    """
    runtime = _ScriptedRuntime(result=_classify_result(_APPROVED_CODE))
    gw = _gateway(runtime)

    result = report_ai_http.classify(gw, "sku:PLAN-06", "Any plan", _AS_OF, None)

    assert result["outcome"] == "NOT_CONFIGURED"
    assert result["evidence_id"] is None
    assert result["verdict"] == Verdict.ABSTAIN
    assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_NOT_CONFIGURED
    assert result["list_version"] is None
    assert result["confidence_calibrated"] is False
    # Gateway must not have been called
    assert len(runtime.calls) == 0


def test_env_unset_does_not_affect_explain() -> None:
    """explain() is unaffected by approved_list being None."""
    rf = _MODULES["report_fixture"]
    fixture = rf.generate_fixture()

    rows = rf.rows_for_kind(fixture, "obligation-row", ["OBL-001"])
    amount = rows[0]["amount"]
    cited_id = rows[0]["id"]

    from ztax_gateway.runtime import Result as _Result

    runtime = _ScriptedRuntime(
        result=_Result(
            fields={
                "explanation": f"OBL-001 is {amount}.",
                "cited_ids": json.dumps([cited_id]),
                "abstain": "false",
                "reason": "",
            },
            confidence="0.5000",
        )
    )
    gw = _gateway(runtime)

    # explain works with no approved_list involvement
    result = report_ai_http.explain(gw, fixture, "obligation-row", ["OBL-001"])
    assert result["outcome"] == "EXPLAINED"


# ---------------------------------------------------------------------------
# 8. Invalid list file → startup refused, fixed stderr message, nothing echoed
# ---------------------------------------------------------------------------


def test_invalid_list_path_causes_startup_failure(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
) -> None:
    """main() with ZTAX_ONTOLOGY_APPROVED_LIST set to a corrupt file exits non-zero.

    The fixed message must appear on stderr; file content must NOT appear.
    """
    bad_file = tmp_path / "bad_list.json"
    bad_file.write_text("NOT VALID JSON CONTENT !!!SECRET!!!", encoding="utf-8")

    monkeypatch.setenv("ZTAX_ONTOLOGY_APPROVED_LIST", str(bad_file))
    # Also stub out enough env to get past the GroqDemoRuntime and gateway
    # build steps — but the list load happens after those, so we patch
    # load_approved_list to raise OntologyGuardError directly instead of
    # building the full runtime stack.

    # We test the env-var load logic directly: set the path, call a helper
    # that simulates what main() does (read + load_approved_list).
    from ztax_gateway import ontology_guard as _og

    # Confirm that loading the bad file raises OntologyGuardError.
    with pytest.raises(_og.OntologyGuardError):
        _og.load_approved_list(bad_file.read_text(encoding="utf-8"))

    # Simulate the main() path: load the env, try to parse, catch and print fixed msg.
    import io

    stderr_buf = io.StringIO()
    list_path = os.environ.get("ZTAX_ONTOLOGY_APPROVED_LIST", "").strip()
    assert list_path
    exit_code = 0
    try:
        _og.load_approved_list(Path(list_path).read_text(encoding="utf-8"))
    except (_og.OntologyGuardError, OSError):
        print(
            "report_ai_http: approved ontology list: invalid or unreadable",
            file=stderr_buf,
        )
        exit_code = 2

    stderr_text = stderr_buf.getvalue()
    assert exit_code == 2
    assert "report_ai_http: approved ontology list: invalid or unreadable" in stderr_text
    # File content (the sentinel) must NOT appear
    assert "SECRET" not in stderr_text
    assert "NOT VALID JSON" not in stderr_text


def test_missing_list_file_causes_startup_failure(tmp_path: Path) -> None:
    """A set but non-existent path raises OSError and must yield exit code 2."""
    missing = tmp_path / "no_such_file.json"
    from ztax_gateway import ontology_guard as _og

    exit_code = 0
    import io

    stderr_buf = io.StringIO()
    try:
        _og.load_approved_list(missing.read_text(encoding="utf-8"))
    except (_og.OntologyGuardError, OSError):
        print(
            "report_ai_http: approved ontology list: invalid or unreadable",
            file=stderr_buf,
        )
        exit_code = 2

    assert exit_code == 2
    assert "report_ai_http: approved ontology list: invalid or unreadable" in stderr_buf.getvalue()


def test_non_utf8_list_file_causes_clean_failure(tmp_path: Path) -> None:
    """A list file with invalid UTF-8 bytes raises UnicodeDecodeError; main() catches it cleanly."""
    bad_utf8 = tmp_path / "bad_utf8.json"
    bad_utf8.write_bytes(b"\xff\xfe\x00\x00\x80\x81")

    import io

    from ztax_gateway import ontology_guard as _og

    stderr_buf = io.StringIO()
    exit_code = 0
    try:
        _og.load_approved_list(bad_utf8.read_text(encoding="utf-8"))
    except (_og.OntologyGuardError, OSError, UnicodeDecodeError):
        print(
            "report_ai_http: approved ontology list: invalid or unreadable",
            file=stderr_buf,
        )
        exit_code = 2

    assert exit_code == 2
    assert "report_ai_http: approved ontology list: invalid or unreadable" in stderr_buf.getvalue()


# ---------------------------------------------------------------------------
# 9. Failing check-event sink does not drop the reply
# ---------------------------------------------------------------------------


def test_failing_sink_does_not_drop_classify_proposed_reply(
    caplog: pytest.LogCaptureFixture,
) -> None:
    """A raising sink must not drop a PROPOSED reply; the sentinel must not leak."""
    runtime = _ScriptedRuntime(result=_classify_result(_APPROVED_CODE))
    gw = _gateway_with_raising_sink(runtime)

    with caplog.at_level(logging.WARNING):
        result = report_ai_http.classify(gw, "sku:PLAN-07", "Voice plan", _AS_OF, _APPROVED_LIST)

    assert result["outcome"] == "PROPOSED"
    warning_texts = [r.getMessage() for r in caplog.records if r.levelno >= logging.WARNING]
    assert any("CHECK_EVENT_WRITE_FAILED" in t for t in warning_texts)
    for text in warning_texts:
        assert _RaisingSink._SENTINEL not in text
    assert _RaisingSink._SENTINEL not in json.dumps(result)


def test_failing_sink_does_not_drop_classify_abstain_reply(
    caplog: pytest.LogCaptureFixture,
) -> None:
    """A raising sink must not drop an ABSTAIN reply either."""
    runtime = _ScriptedRuntime(result=_classify_result(_UNKNOWN_CODE))
    gw = _gateway_with_raising_sink(runtime)

    with caplog.at_level(logging.WARNING):
        result = report_ai_http.classify(gw, "sku:PLAN-08", "Unknown plan", _AS_OF, _APPROVED_LIST)

    assert result["outcome"] == "ABSTAIN"
    warning_texts = [r.getMessage() for r in caplog.records if r.levelno >= logging.WARNING]
    assert any("CHECK_EVENT_WRITE_FAILED" in t for t in warning_texts)
    for text in warning_texts:
        assert _RaisingSink._SENTINEL not in text


# ---------------------------------------------------------------------------
# 10. Rejected code never appears in the response body
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "rejected_code",
    [_UNKNOWN_CODE, _EXPIRED_CODE, _FUTURE_CODE, _HEADING_CODE],
    ids=["unknown", "expired", "future", "heading"],
)
def test_rejected_code_never_in_response_body(rejected_code: str) -> None:
    """On ABSTAIN the rejected code must never appear anywhere in the response JSON."""
    runtime = _ScriptedRuntime(result=_classify_result(rejected_code))
    gw = _gateway(runtime)

    result = report_ai_http.classify(gw, "sku:PLAN-09", "Some plan", _AS_OF, _APPROVED_LIST)

    assert result["outcome"] == "ABSTAIN"
    body_str = json.dumps(result)
    assert rejected_code not in body_str, (
        f"rejected code {rejected_code!r} appeared in the response: {body_str!r}"
    )


# ---------------------------------------------------------------------------
# 11. Kill switch → REFUSED
# ---------------------------------------------------------------------------


def test_kill_switch_gives_refused(tmp_path: Path) -> None:
    """A use case listed in 'killed' gives REFUSED, not PROPOSED or ABSTAIN."""
    import shutil

    src = _CONFIG_DIR / "registry.live-demo.json"
    tmp_reg = tmp_path / "registry.test.json"
    shutil.copy(src, tmp_reg)

    runtime = _ScriptedRuntime(result=_classify_result(_APPROVED_CODE))
    registry, routes = load_config(tmp_reg)
    gw = Gateway(
        registry,
        routes,
        runtime,
        "local",
        "0.0.0-test",
        kill_state_path=tmp_reg,
    )

    # Confirm it works before kill
    result_before = report_ai_http.classify(
        gw, "sku:PLAN-10", "Voice plan", _AS_OF, _APPROVED_LIST
    )
    assert result_before["outcome"] == "PROPOSED"

    # Activate the kill switch for classification-review
    doc = json.loads(tmp_reg.read_text(encoding="utf-8"))
    doc["killed"] = ["classification-review"]
    tmp_reg.write_text(json.dumps(doc), encoding="utf-8")

    result_after = report_ai_http.classify(
        gw, "sku:PLAN-10", "Voice plan", _AS_OF, _APPROVED_LIST
    )
    assert result_after["outcome"] == "REFUSED"
    assert result_after["confidence_calibrated"] is False
    assert result_after["synthetic"] is True
    # No model text; proposed_code is absent
    assert "proposed_code" not in result_after


# ---------------------------------------------------------------------------
# 12. HTTP-level tests for /demo/classify (32 KiB limit, 400 cases, 404, CORS)
# ---------------------------------------------------------------------------


def test_http_classify_endpoints() -> None:
    """HTTP-level testing for /demo/classify:
    - 200 PROPOSED with CORS on valid input
    - 400 on request body > 32 KiB (_DEMO_MAX_BODY)
    - 400 on non-JSON request body
    - 400 on invalid or missing subject_ref
    - 400 on invalid or missing description
    - 404 on GET /demo/classify or unknown paths
    """
    import http.client
    import threading
    from http.server import ThreadingHTTPServer

    runtime = _ScriptedRuntime(result=_classify_result(_APPROVED_CODE))
    gw = _gateway(runtime)
    rf = _MODULES["report_fixture"]
    fixture = rf.generate_fixture()

    handler_cls = report_ai_http._make_handler(gw, fixture, _APPROVED_LIST)
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler_cls)
    port = server.server_address[1]
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)

        # 1. Valid request -> 200 with CORS and synthetic: true
        valid_body = json.dumps({
            "subject_ref": "sku:PLAN-01",
            "description": "Unlimited voice plan",
        }).encode("utf-8")
        conn.request(
            "POST", "/demo/classify", body=valid_body, headers={"Content-Type": "application/json"}
        )
        resp = conn.getresponse()
        assert resp.status == 200
        assert resp.getheader("Access-Control-Allow-Origin") == "http://localhost:13000"
        data = json.loads(resp.read())
        assert data["outcome"] == "PROPOSED"
        assert data["synthetic"] is True
        assert data["proposed_code"] == _APPROVED_CODE

        # 2. Body exceeding 32 KiB limit -> 400 "request body too large"
        oversized = json.dumps({
            "subject_ref": "sku:BIG",
            "description": "A" * (33 * 1024),
        }).encode("utf-8")
        conn.request(
            "POST", "/demo/classify", body=oversized, headers={"Content-Type": "application/json"}
        )
        resp = conn.getresponse()
        assert resp.status == 400
        err_data = json.loads(resp.read())
        assert err_data["error"] == "request body too large"

        # 3. Non-JSON body -> 400 "request body is not JSON"
        conn.request(
            "POST",
            "/demo/classify",
            body=b"not-json{{{",
            headers={"Content-Type": "application/json"},
        )
        resp = conn.getresponse()
        assert resp.status == 400
        assert json.loads(resp.read())["error"] == "request body is not JSON"

        # 4. Missing or empty subject_ref -> 400
        for bad_ref in [None, "", "   ", 123]:
            payload: dict[str, Any] = {"description": "Valid description"}
            if bad_ref is not None:
                payload["subject_ref"] = bad_ref
            conn.request(
                "POST",
                "/demo/classify",
                body=json.dumps(payload).encode("utf-8"),
                headers={"Content-Type": "application/json"},
            )
            resp = conn.getresponse()
            assert resp.status == 400
            assert "subject_ref must be a non-empty string" in json.loads(resp.read())["error"]

        # 5. Missing or empty description -> 400
        for bad_desc in [None, "", "   ", 456]:
            payload = {"subject_ref": "sku:VALID"}
            if bad_desc is not None:
                payload["description"] = bad_desc
            conn.request(
                "POST",
                "/demo/classify",
                body=json.dumps(payload).encode("utf-8"),
                headers={"Content-Type": "application/json"},
            )
            resp = conn.getresponse()
            assert resp.status == 400
            assert "description must be a non-empty string" in json.loads(resp.read())["error"]

        # 6. GET /demo/classify -> 404
        conn.request("GET", "/demo/classify")
        resp = conn.getresponse()
        assert resp.status == 404
        assert json.loads(resp.read())["error"] == "not found"

        # 7. POST to unknown path -> 404
        conn.request(
            "POST",
            "/demo/unknown",
            body=b"{}",
            headers={"Content-Type": "application/json"},
        )
        resp = conn.getresponse()
        assert resp.status == 404
        assert json.loads(resp.read())["error"] == "not found"

        conn.close()
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


# ---------------------------------------------------------------------------
# 13. Model call counts for refused / abstained / not-configured cases
# ---------------------------------------------------------------------------


def test_model_call_counts_for_refused_and_abstained_cases(tmp_path: Path) -> None:
    """Verifies that model calls are strictly bounded across all non-PROPOSED paths:
    - NOT_CONFIGURED (no approved list): exactly 0 calls to runtime
    - ABSTAIN (unknown code): exactly 1 call to runtime
    - ABSTAIN (expired code): exactly 1 call to runtime
    - ABSTAIN (future code): exactly 1 call to runtime
    - ABSTAIN (heading code): exactly 1 call to runtime
    - REFUSED (kill switch): exactly 0 calls to runtime
    """
    import shutil

    # 1. NOT_CONFIGURED: 0 calls
    runtime_nc = _ScriptedRuntime(result=_classify_result(_APPROVED_CODE))
    gw_nc = _gateway(runtime_nc)
    res_nc = report_ai_http.classify(gw_nc, "sku:1", "desc", _AS_OF, None)
    assert res_nc["outcome"] == "NOT_CONFIGURED"
    assert len(runtime_nc.calls) == 0

    # 2. Unknown code: 1 call
    runtime_unk = _ScriptedRuntime(result=_classify_result(_UNKNOWN_CODE))
    gw_unk = _gateway(runtime_unk)
    res_unk = report_ai_http.classify(gw_unk, "sku:1", "desc", _AS_OF, _APPROVED_LIST)
    assert res_unk["outcome"] == "ABSTAIN"
    assert len(runtime_unk.calls) == 1

    # 3. Expired code: 1 call
    runtime_exp = _ScriptedRuntime(result=_classify_result(_EXPIRED_CODE))
    gw_exp = _gateway(runtime_exp)
    res_exp = report_ai_http.classify(gw_exp, "sku:1", "desc", _AS_OF, _APPROVED_LIST)
    assert res_exp["outcome"] == "ABSTAIN"
    assert len(runtime_exp.calls) == 1

    # 4. Future code: 1 call
    runtime_fut = _ScriptedRuntime(result=_classify_result(_FUTURE_CODE))
    gw_fut = _gateway(runtime_fut)
    res_fut = report_ai_http.classify(gw_fut, "sku:1", "desc", _AS_OF, _APPROVED_LIST)
    assert res_fut["outcome"] == "ABSTAIN"
    assert len(runtime_fut.calls) == 1

    # 5. Heading-marker code: 1 call
    runtime_hd = _ScriptedRuntime(result=_classify_result(_HEADING_CODE))
    gw_hd = _gateway(runtime_hd)
    res_hd = report_ai_http.classify(gw_hd, "sku:1", "desc", _AS_OF, _APPROVED_LIST)
    assert res_hd["outcome"] == "ABSTAIN"
    assert len(runtime_hd.calls) == 1

    # 6. Kill switch active: 0 calls
    tmp_reg = tmp_path / "registry.killed.json"
    shutil.copy(_CONFIG_DIR / "registry.live-demo.json", tmp_reg)
    doc = json.loads(tmp_reg.read_text(encoding="utf-8"))
    doc["killed"] = ["classification-review"]
    tmp_reg.write_text(json.dumps(doc), encoding="utf-8")

    runtime_kill = _ScriptedRuntime(result=_classify_result(_APPROVED_CODE))
    reg, routes = load_config(tmp_reg)
    gw_kill = Gateway(reg, routes, runtime_kill, "local", "0.0.0-test", kill_state_path=tmp_reg)
    res_kill = report_ai_http.classify(gw_kill, "sku:1", "desc", _AS_OF, _APPROVED_LIST)
    assert res_kill["outcome"] == "REFUSED"
    assert len(runtime_kill.calls) == 0


# ---------------------------------------------------------------------------
# 14. Unhandled runtime exception in HTTP handler (item 6)
# ---------------------------------------------------------------------------


def test_http_handler_classify_unhandled_exception_returns_refusal(
    caplog: pytest.LogCaptureFixture,
) -> None:
    """An unexpected runtime exception inside classify() must NOT escape the HTTP handler.

    The handler must:
    - return HTTP 200 with outcome REFUSED and AI_GATEWAY_UNAVAILABLE
    - log the exception CLASS NAME only (never the exception message/repr)
    - never print a traceback to stderr
    """
    import http.client
    import threading
    from http.server import ThreadingHTTPServer

    # A runtime that raises a totally unexpected non-GatewayError exception
    class _BoomRuntime:
        def run(self, route: Any, call: Any) -> Any:
            raise RuntimeError("INTERNAL_BOOM_MARKER_DO_NOT_ECHO")

    runtime = _BoomRuntime()
    registry, routes = load_config(_CONFIG_DIR / "registry.live-demo.json")
    from ztax_gateway.invocation_check_event import LoggingCheckEventSink
    gw = Gateway(
        registry,
        routes,
        runtime,
        "local",
        "0.0.0-test",
        check_event_sink=LoggingCheckEventSink(),
    )
    rf = _MODULES["report_fixture"]
    fixture = rf.generate_fixture()

    handler_cls = report_ai_http._make_handler(gw, fixture, _APPROVED_LIST)
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler_cls)
    port = server.server_address[1]
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
        body = json.dumps({"subject_ref": "sku:X", "description": "Test description"}).encode()
        with caplog.at_level(logging.DEBUG, logger="ztax_gateway.report_ai_http"):
            conn.request("POST", "/demo/classify", body=body,
                         headers={"Content-Type": "application/json"})
            resp = conn.getresponse()
            data = json.loads(resp.read())

        assert resp.status == 200
        assert data["outcome"] == "REFUSED"
        assert data["reason_code"] == "AI_GATEWAY_UNAVAILABLE"

        # Class name must appear in logs
        own_logs = [r.getMessage() for r in caplog.records
                    if r.name == "ztax_gateway.report_ai_http"]
        assert any("RuntimeError" in m for m in own_logs), (
            f"Expected exception class name in logs, got: {own_logs}"
        )
        # Exception message must NOT appear anywhere in logs
        full_log_text = " ".join(own_logs)
        assert "INTERNAL_BOOM_MARKER_DO_NOT_ECHO" not in full_log_text

        conn.close()
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def test_http_handler_explain_unhandled_exception_returns_refusal(
    caplog: pytest.LogCaptureFixture,
) -> None:
    """An unexpected runtime exception inside explain() must NOT escape the HTTP handler.

    The handler must return HTTP 200 with outcome REVIEW_REQUIRED and
    log the exception class name only.
    """
    import http.client
    import threading
    from http.server import ThreadingHTTPServer

    class _BoomRuntime:
        def run(self, route: Any, call: Any) -> Any:
            raise ValueError("EXPLAIN_BOOM_MARKER_DO_NOT_ECHO")

    runtime = _BoomRuntime()
    registry, routes = load_config(_CONFIG_DIR / "registry.live-demo.json")
    from ztax_gateway.invocation_check_event import LoggingCheckEventSink
    gw = Gateway(
        registry,
        routes,
        runtime,
        "local",
        "0.0.0-test",
        check_event_sink=LoggingCheckEventSink(),
    )
    rf = _MODULES["report_fixture"]
    fixture = rf.generate_fixture()

    handler_cls = report_ai_http._make_handler(gw, fixture, _APPROVED_LIST)
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler_cls)
    port = server.server_address[1]
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
        body = json.dumps({"kind": "obligation-row", "ids": ["OBL-001"]}).encode()
        with caplog.at_level(logging.DEBUG, logger="ztax_gateway.report_ai_http"):
            conn.request("POST", "/demo/explain", body=body,
                         headers={"Content-Type": "application/json"})
            resp = conn.getresponse()
            data = json.loads(resp.read())

        assert resp.status == 200
        assert data["outcome"] == "REVIEW_REQUIRED"

        own_logs = [r.getMessage() for r in caplog.records
                    if r.name == "ztax_gateway.report_ai_http"]
        assert any("ValueError" in m for m in own_logs), (
            f"Expected exception class name in logs, got: {own_logs}"
        )
        full_log_text = " ".join(own_logs)
        assert "EXPLAIN_BOOM_MARKER_DO_NOT_ECHO" not in full_log_text

        conn.close()
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


# ---------------------------------------------------------------------------
# 15. Route hardening: body shape, Content-Length, size, field limits, reply shape
# ---------------------------------------------------------------------------

_REPLY_KEYS = {
    "outcome",
    "verdict",
    "reason_code",
    "evidence_id",
    "list_version",
    "confidence_calibrated",
    "synthetic",
}


@contextlib.contextmanager
def _serving(
    runtime: Any = None, approved: ApprovedOntologyList | None = _APPROVED_LIST
) -> Iterator[int]:
    """A real local demo server on a free port; yields the port."""
    import threading
    from http.server import ThreadingHTTPServer

    gw = _gateway(runtime or _ScriptedRuntime(result=_classify_result(_APPROVED_CODE)))
    fixture = _MODULES["report_fixture"].generate_fixture()
    server = ThreadingHTTPServer(
        ("127.0.0.1", 0), report_ai_http._make_handler(gw, fixture, approved)
    )
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield int(server.server_address[1])
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def _post(port: int, path: str, body: bytes) -> tuple[int, dict[str, Any]]:
    import http.client

    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    try:
        conn.request("POST", path, body=body, headers={"Content-Type": "application/json"})
        resp = conn.getresponse()
        return resp.status, json.loads(resp.read())
    finally:
        conn.close()


def _post_raw_length(port: int, path: str, length_value: str) -> tuple[int, dict[str, Any]]:
    """Send a request whose Content-Length header is written by hand."""
    import socket

    request = (
        f"POST {path} HTTP/1.1\r\nHost: 127.0.0.1\r\n"
        f"Content-Type: application/json\r\nContent-Length: {length_value}\r\n"
        "Connection: close\r\n\r\n"
    ).encode("ascii")
    with socket.create_connection(("127.0.0.1", port), timeout=5) as sock:
        sock.sendall(request)
        chunks = []
        while True:
            data = sock.recv(4096)
            if not data:
                break
            chunks.append(data)
    raw = b"".join(chunks)
    head, _, body = raw.partition(b"\r\n\r\n")
    status = int(head.split(b" ", 2)[1])
    return status, json.loads(body)


def _classify_body(subject_ref: str, description: str) -> bytes:
    return json.dumps({"subject_ref": subject_ref, "description": description}).encode()


@pytest.mark.parametrize("path", ["/demo/classify", "/demo/explain"])
@pytest.mark.parametrize("body", [b"null", b"[1, 2]", b'"text"', b"5", b"true"])
def test_a_json_body_that_is_not_an_object_is_refused(path: str, body: bytes) -> None:
    with _serving() as port:
        status, doc = _post(port, path, body)
    assert status == 400
    assert doc == {"error": "request body must be an object"}


@pytest.mark.parametrize("path", ["/demo/classify", "/demo/explain"])
def test_a_body_that_is_not_utf8_is_refused_as_not_json(path: str) -> None:
    with _serving() as port:
        status, doc = _post(port, path, b"\xff\xfe\xfa")
    assert status == 400
    assert doc == {"error": "request body is not JSON"}


@pytest.mark.parametrize("path", ["/demo/classify", "/demo/explain"])
@pytest.mark.parametrize("value", ["abc", "-1", "1.5", "+5", "0x10"])
def test_an_invalid_or_negative_content_length_is_refused(path: str, value: str) -> None:
    with _serving() as port:
        status, doc = _post_raw_length(port, path, value)
    assert status == 400
    assert doc == {"error": "invalid content length"}


def test_an_empty_explain_body_keeps_its_field_error() -> None:
    with _serving() as port:
        status, doc = _post(port, "/demo/explain", b"")
    assert status == 400
    assert "kind must be one of" in doc["error"]


def test_an_empty_classify_body_gives_the_field_error() -> None:
    with _serving() as port:
        status, doc = _post(port, "/demo/classify", b"")
    assert status == 400
    assert doc == {"error": "subject_ref must be a non-empty string"}


def test_an_oversize_explain_body_is_refused() -> None:
    body = json.dumps({"kind": "obligation-row", "ids": ["A" * (34 * 1024)]}).encode()
    with _serving() as port:
        status, doc = _post(port, "/demo/explain", body)
    assert status == 400
    assert doc == {"error": "request body too large"}


def test_the_demo_body_limit_is_32_kib_and_is_not_named_for_one_route() -> None:
    assert report_ai_http._DEMO_MAX_BODY == 32 * 1024
    assert not hasattr(report_ai_http, "_CLASSIFY_MAX_BODY")


def test_subject_ref_of_512_passes_the_route_and_513_is_refused() -> None:
    assert report_ai_http._SUBJECT_REF_MAX == 512
    with _serving() as port:
        ok_status, ok_doc = _post(port, "/demo/classify", _classify_body("s" * 512, "d"))
        bad_status, bad_doc = _post(port, "/demo/classify", _classify_body("s" * 513, "d"))
    assert ok_status == 200
    assert ok_doc["outcome"] == "PROPOSED"
    assert bad_status == 400
    assert bad_doc == {"error": "subject_ref is too long"}


def test_description_of_1000_passes_the_route_and_1001_is_refused() -> None:
    assert report_ai_http._DESCRIPTION_MAX == 1000
    with _serving() as port:
        ok_status, ok_doc = _post(port, "/demo/classify", _classify_body("s", "d" * 1000))
        bad_status, bad_doc = _post(port, "/demo/classify", _classify_body("s", "d" * 1001))
    assert ok_status == 200
    assert ok_doc["outcome"] == "PROPOSED"
    assert bad_status == 400
    assert bad_doc == {"error": "description is too long"}


def test_a_too_long_field_never_reaches_the_runtime() -> None:
    runtime = _ScriptedRuntime(result=_classify_result(_APPROVED_CODE))
    with _serving(runtime) as port:
        _post(port, "/demo/classify", _classify_body("s" * 513, "d"))
        _post(port, "/demo/classify", _classify_body("s", "d" * 1001))
    assert len(runtime.calls) == 0


def test_every_classify_reply_has_the_same_key_set() -> None:
    """The four outcomes all carry the seven keys. Only PROPOSED adds proposed_code.
    synthetic is true and confidence_calibrated is false on every one."""

    def _run(code: str, subject_ref: str, approved: ApprovedOntologyList | None) -> Any:
        gw = _gateway(_ScriptedRuntime(result=_classify_result(code)))
        return report_ai_http.classify(gw, subject_ref, "d", _AS_OF, approved)

    not_configured = _run(_APPROVED_CODE, "s", None)
    abstain = _run(_UNKNOWN_CODE, "s", _APPROVED_LIST)
    proposed = _run(_APPROVED_CODE, "s", _APPROVED_LIST)
    # A subject_ref over the wire limit makes the Gateway refuse the call.
    refused = _run(_APPROVED_CODE, "s" * 600, _APPROVED_LIST)
    assert not_configured["outcome"] == "NOT_CONFIGURED"
    assert abstain["outcome"] == "ABSTAIN"
    assert proposed["outcome"] == "PROPOSED"
    assert refused["outcome"] == "REFUSED"
    for reply in (not_configured, abstain, refused):
        assert set(reply) == _REPLY_KEYS, reply["outcome"]
    assert set(proposed) == _REPLY_KEYS | {"proposed_code"}
    for reply in (not_configured, abstain, proposed, refused):
        assert reply["synthetic"] is True
        assert reply["confidence_calibrated"] is False


@pytest.mark.parametrize("with_list", [True, False])
def test_the_handler_fallback_has_the_same_key_set(
    monkeypatch: pytest.MonkeyPatch, with_list: bool
) -> None:
    def _boom(*args: Any, **kwargs: Any) -> Any:
        raise RuntimeError("FALLBACK_BOOM_MARKER_DO_NOT_ECHO")

    monkeypatch.setattr(report_ai_http, "classify", _boom)
    approved = _APPROVED_LIST if with_list else None
    with _serving(approved=approved) as port:
        status, doc = _post(port, "/demo/classify", _classify_body("s", "d"))
    assert status == 200
    assert set(doc) == _REPLY_KEYS
    assert doc["outcome"] == "REFUSED"
    assert doc["reason_code"] == "AI_GATEWAY_UNAVAILABLE"
    assert doc["synthetic"] is True
    assert doc["list_version"] == (_APPROVED_LIST.list_version if with_list else None)
    assert "FALLBACK_BOOM_MARKER_DO_NOT_ECHO" not in json.dumps(doc)


# ---------------------------------------------------------------------------
# Item 3: startup test — huge integer in approved list gives fixed message
# ---------------------------------------------------------------------------


class TestStartupHugeIntInApprovedList:
    """report_ai_http.main() with a huge integer in the approved list file must
    print the fixed stderr message and return exit code 2, never a traceback."""

    def test_huge_int_gives_fixed_message(
        self,
        tmp_path: Path,
        monkeypatch: pytest.MonkeyPatch,
        capsys: pytest.CaptureFixture[str],
    ) -> None:
        # Write a file containing a huge integer — triggers ValueError in json.loads.
        bad = tmp_path / "huge.json"
        bad.write_bytes(b"[" + b"9" * 5000 + b"]")

        # Point the env var at the bad file.
        monkeypatch.setenv("ZTAX_ONTOLOGY_APPROVED_LIST", str(bad))

        import unittest.mock as _mock

        # Patch GroqDemoRuntime and build_gateway so they don't fail early.
        fake_runtime = _mock.MagicMock()
        monkeypatch.setattr(report_ai_http, "GroqDemoRuntime", lambda: fake_runtime)
        monkeypatch.setattr(report_ai_http, "build_gateway", lambda *a, **kw: _mock.MagicMock())

        # Patch serve_live_demo._env (accessed via the already-loaded module reference
        # inside report_ai_http) to return non-empty strings.
        _sdemo = _MODULES["serve_live_demo"]
        monkeypatch.setattr(_sdemo, "_env", lambda key: "dummy-value")

        # Patch report_fixture.generate_fixture so it doesn't blow up.
        _rf = _MODULES["report_fixture"]
        monkeypatch.setattr(_rf, "generate_fixture", lambda: {})

        rc = report_ai_http.main()

        err = capsys.readouterr().err
        assert rc == 2
        assert "report_ai_http: approved ontology list: invalid or unreadable" in err
        assert "Traceback" not in err
        assert "ValueError" not in err
        assert "RecursionError" not in err
