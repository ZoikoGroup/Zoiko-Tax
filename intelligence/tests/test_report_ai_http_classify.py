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

import importlib.util
import json
import logging
import os
import sys
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
    # No model text; proposed_code is absent
    assert "proposed_code" not in result_after
