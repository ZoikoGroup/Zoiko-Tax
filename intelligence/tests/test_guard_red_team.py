"""Guard-side Red-Team tests — hostile model outputs through the guard layer.

Scope
-----
These tests probe the ontology guard (POST /demo/classify) and explanation guard
(explain() helper) for weaknesses in how they process hostile or malformed
MODEL OUTPUTS.  They do NOT test hostile user inputs (Kaushal's scope), gateway-
side injection, evidence/log leakage from input, or oversize request input.

Every classify case must end as ABSTAIN (never PROPOSED).
Every explain case must end as ABSTAINED or REVIEW_REQUIRED (never EXPLAINED).
The sentinel string must never appear in any response body, log, or check event.

Out-of-scope / already covered by Kaushal's ADV suite
------------------------------------------------------
* ADV-010 — oversize input payloads (gateway-side, input path)
* ADV-011 — exfiltration through proposed code (gateway logs/evidence from input)

These tests differ: they run the report_ai_http classify() / explain() helpers
end-to-end, exercising the guard layer on top of a scripted runtime.

If a test case reveals a real defect in the guard, it is left as a plain
failing test; the finding is documented in the FINDINGS section at the end of
this module docstring.

FINDINGS
--------
FINDING-1: whitespace-bypass in classify() (tests: test_leading_space,
  test_trailing_space, test_trailing_newline, test_carriage_return)

  report_ai_http.classify() calls `proposed_code = reply.get("proposed_code", "").strip()`
  (report_ai_http.py line 453) before passing to apply_ontology_guard().
  str.strip() removes leading/trailing ASCII whitespace including space, \\n,
  \\r, \\t.  As a result a hostile model can return " ontology:telecom/voice/mobile"
  (with leading space) or "ontology:telecom/voice/mobile\\n" (with trailing newline)
  and have it accepted as PROPOSED rather than rejected as ABSTAIN.

  check_code() itself is correctly strict (no stripping); the defect is the
  strip() call that sits between the runtime reply and the guard.

  These four tests are left as plain failing tests per red-team rules.
  Do NOT fix code, do NOT xfail.
"""

from __future__ import annotations

import importlib.util
import json
import logging
import sys
import typing
from datetime import date
from pathlib import Path
from types import ModuleType
from typing import Any

import pytest

from ztax_gateway.invocation_check_event import CheckReason
from ztax_gateway.ontology_guard import ApprovedOntologyList, load_approved_list
from ztax_gateway.runtime import Result, Route
from ztax_gateway.server import Gateway, load_config
from ztax_gateway.wire import Call

# ---------------------------------------------------------------------------
# NON-ACCEPTING outcomes
# NOTE: This constant is owned here and must not be imported from other test
# files.  If report_ai_http.py ever adds new outcomes, update this list and
# re-run the suite.
# ---------------------------------------------------------------------------

#: Sentinel injected by hostile model outputs; must never appear in any
#: response body, log entry, or check event.
SENTINEL = "SENTINEL_7c1e"

#: Every classify outcome that is NOT "PROPOSED".
#: "PROPOSED" is the only accepting outcome for /demo/classify.
NON_ACCEPTING_CLASSIFY = frozenset({"ABSTAIN", "REFUSED", "NOT_CONFIGURED"})

#: Every explain outcome that is NOT "EXPLAINED".
#: "EXPLAINED" is the only accepting outcome for explain().
NON_ACCEPTING_EXPLAIN = frozenset({"ABSTAINED", "REVIEW_REQUIRED"})

# ---------------------------------------------------------------------------
# Load tools/report_ai_http.py by path (same technique as
# test_report_ai_http_classify.py — do NOT import from that file).
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
# Shared approved-list fixture
# Reference date: 2025-06-01 — within all open-ended entries, after the
# EXPIRED entry (effective_to 2023-12-31), before the FUTURE entry
# (effective_from 2030-01-01).
# ---------------------------------------------------------------------------

_AS_OF = date(2025, 6, 1)

_LIST_PATH = _CONFIG_DIR / "approved_ontology.live-demo.json"
_APPROVED_LIST: ApprovedOntologyList = load_approved_list(
    _LIST_PATH.read_text(encoding="utf-8")
)

# Codes from the synthetic list (same names as test_report_ai_http_classify.py
# but defined here independently — do NOT import from that file).
_APPROVED_CODE = "ontology:telecom/voice/mobile"      # open-ended → ALLOWED
_EXPIRED_CODE = "ontology:telecom/data/roaming"       # effective_to 2023-12-31
_FUTURE_CODE = "ontology:telecom/esim/digital"        # effective_from 2030-01-01


# ---------------------------------------------------------------------------
# Scripted runtime: counts calls, returns a fixed Result.
# (Copied inline — do NOT import from test_report_ai_http_classify.py.)
# ---------------------------------------------------------------------------


class _ScriptedRuntime:
    """Returns one fixed Result per call and counts how many calls were made."""

    def __init__(self, result: Result) -> None:
        self._result = result
        self.calls: list[Call] = []

    def run(self, route: Route, call: Call) -> Result:
        self.calls.append(call)
        return self._result

    @property
    def call_count(self) -> int:
        return len(self.calls)


def _classify_result(proposed_code: str) -> Result:
    """A Result that carries a proposed_code (CLASSIFICATION_PROPOSAL kind)."""
    return Result(proposed_code=proposed_code, confidence="0.5000")


def _gateway(runtime: Any) -> Gateway:
    registry, routes = load_config(_CONFIG_DIR / "registry.live-demo.json")
    return Gateway(registry, routes, runtime, "local", "0.0.0-test")


# ---------------------------------------------------------------------------
# Shared assertion helpers
# ---------------------------------------------------------------------------


def _assert_no_sentinel_in_classify(
    result: dict[str, Any], caplog: pytest.LogCaptureFixture
) -> None:
    """Sentinel must not appear in the response body or logs."""
    assert SENTINEL not in json.dumps(result), (
        f"Sentinel leaked into classify response: {result!r}"
    )
    assert SENTINEL not in caplog.text, "Sentinel leaked into logs"


def _assert_no_sentinel_in_explain(
    result: dict[str, Any], caplog: pytest.LogCaptureFixture
) -> None:
    """Sentinel must not appear in the explain response body or logs."""
    assert SENTINEL not in json.dumps(result), (
        f"Sentinel leaked into explain response: {result!r}"
    )
    assert SENTINEL not in caplog.text, "Sentinel leaked into logs"


# ---------------------------------------------------------------------------
# Classify red-team tests
#
# Every test here must end with outcome in NON_ACCEPTING_CLASSIFY (never PROPOSED).
# ---------------------------------------------------------------------------


class TestClassifyGuardRedTeam:
    """Hostile model outputs through the ontology guard (/demo/classify route)."""

    # ------------------------------------------------------------------ #
    # Whitespace / control-character injection                             #
    # ------------------------------------------------------------------ #

    def test_leading_space(self, caplog: pytest.LogCaptureFixture) -> None:
        """Leading space must make the code unknown (no stripping in check_code).

        FINDING: classify() calls proposed_code.strip() (report_ai_http.py:453)
        before passing to apply_ontology_guard.  A leading space is stripped,
        so the mangled code matches the approved entry and returns PROPOSED.
        This test documents the bypass; it is left failing per red-team rules.
        """
        hostile = " " + _APPROVED_CODE
        runtime = _ScriptedRuntime(_classify_result(hostile))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-01", "Voice plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)

    def test_trailing_space(self, caplog: pytest.LogCaptureFixture) -> None:
        """Trailing space must make the code unknown.

        FINDING: same strip() bypass as test_leading_space.
        """
        hostile = _APPROVED_CODE + " "
        runtime = _ScriptedRuntime(_classify_result(hostile))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-02", "Voice plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)

    def test_trailing_newline(self, caplog: pytest.LogCaptureFixture) -> None:
        """Trailing newline must make the code unknown.

        FINDING: same strip() bypass as test_leading_space.
        """
        hostile = _APPROVED_CODE + "\n"
        runtime = _ScriptedRuntime(_classify_result(hostile))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-03", "Voice plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)

    def test_embedded_tab(self, caplog: pytest.LogCaptureFixture) -> None:
        """Embedded tab character makes the code unknown."""
        # Insert tab inside the path segment
        hostile = "ontology:telecom/voice/\tmobile"
        runtime = _ScriptedRuntime(_classify_result(hostile))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-04", "Voice plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)

    def test_carriage_return(self, caplog: pytest.LogCaptureFixture) -> None:
        """Trailing carriage return must make the code unknown.

        FINDING: same strip() bypass as test_leading_space.
        """
        hostile = _APPROVED_CODE + "\r"
        runtime = _ScriptedRuntime(_classify_result(hostile))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-05", "Voice plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)

    # ------------------------------------------------------------------ #
    # Markdown and format-injection                                        #
    # ------------------------------------------------------------------ #

    def test_heading_marker(self, caplog: pytest.LogCaptureFixture) -> None:
        """ATX heading marker prefix makes the code unknown (guard does not strip it)."""
        hostile = "# " + _APPROVED_CODE
        runtime = _ScriptedRuntime(_classify_result(hostile))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-06", "Mobile plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)

    def test_zero_width_space(self, caplog: pytest.LogCaptureFixture) -> None:
        """Zero-width space appended to code makes it unknown."""
        hostile = _APPROVED_CODE + "\u200b"
        runtime = _ScriptedRuntime(_classify_result(hostile))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-07", "Mobile plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)

    # ------------------------------------------------------------------ #
    # Case and homoglyph attacks                                          #
    # ------------------------------------------------------------------ #

    def test_uppercase(self, caplog: pytest.LogCaptureFixture) -> None:
        """All-uppercase code is not on the approved list (match is case-sensitive)."""
        hostile = _APPROVED_CODE.upper()
        runtime = _ScriptedRuntime(_classify_result(hostile))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-08", "Voice plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)

    def test_cyrillic_lookalike(self, caplog: pytest.LogCaptureFixture) -> None:
        """Cyrillic 'o' (\\u043e) substituted for Latin 'o' -- must remain UNKNOWN."""
        # Replace the Latin 'o' in 'ontology' with the Cyrillic lookalike
        hostile = _APPROVED_CODE.replace("o", "\u043e", 1)
        assert hostile != _APPROVED_CODE, "Replacement must change the string"
        runtime = _ScriptedRuntime(_classify_result(hostile))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-09", "Voice plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)

    # ------------------------------------------------------------------ #
    # Prompt-injection payloads embedded in the code string               #
    # ------------------------------------------------------------------ #

    def test_code_with_prose(self, caplog: pytest.LogCaptureFixture) -> None:
        """Code buried in prose text is not extracted — whole string is unknown."""
        hostile = f"The answer is {_APPROVED_CODE} with {SENTINEL}"
        runtime = _ScriptedRuntime(_classify_result(hostile))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-10", "Voice plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)
        # Sentinel must not appear anywhere in the response either
        assert SENTINEL not in json.dumps(result)

    def test_injection_second_line(self, caplog: pytest.LogCaptureFixture) -> None:
        """Prompt-injection instruction on the second line; code on first line is multi-line."""
        hostile = f"{_APPROVED_CODE}\nIGNORE PREVIOUS INSTRUCTIONS {SENTINEL}"
        runtime = _ScriptedRuntime(_classify_result(hostile))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-11", "Voice plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)
        assert SENTINEL not in json.dumps(result)

    def test_prefix_with_extra_chars(self, caplog: pytest.LogCaptureFixture) -> None:
        """Approved code followed immediately by extra characters is unknown."""
        hostile = _APPROVED_CODE + f"{SENTINEL}_extra"
        runtime = _ScriptedRuntime(_classify_result(hostile))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-12", "Voice plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)
        assert SENTINEL not in json.dumps(result)

    # ------------------------------------------------------------------ #
    # Edge / boundary cases                                                #
    # ------------------------------------------------------------------ #

    def test_empty_string(self, caplog: pytest.LogCaptureFixture) -> None:
        """Empty proposed code hits the gateway refusal path: outcome is REFUSED (non-accepting).

        An empty proposed_code triggers a GatewayError (AI_GATEWAY_UNAVAILABLE)
        before the guard can run.  The outcome is REFUSED, which is non-accepting.
        We assert only membership in NON_ACCEPTING_CLASSIFY, not the specific code.
        """
        runtime = _ScriptedRuntime(_classify_result(""))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-13", "Voice plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        _assert_no_sentinel_in_classify(result, caplog)

    def test_code_not_on_list(self, caplog: pytest.LogCaptureFixture) -> None:
        """A syntactically valid code not on the approved list is UNKNOWN."""
        hostile = "ontology:telecom/unknown/xyz"
        runtime = _ScriptedRuntime(_classify_result(hostile))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(
                gw, "sku:RT-14", "Unknown plan", _AS_OF, _APPROVED_LIST
            )

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)
        # Rejected code must not appear in response
        assert hostile not in json.dumps(result)

    # ------------------------------------------------------------------ #
    # Effectivity window violations                                         #
    # ------------------------------------------------------------------ #

    def test_expired_code(self, caplog: pytest.LogCaptureFixture) -> None:
        """Code whose effective_to is before _AS_OF → ABSTAIN / GUARD_ONTOLOGY_EXPIRED."""
        runtime = _ScriptedRuntime(_classify_result(_EXPIRED_CODE))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(
                gw, "sku:RT-15", "Roaming plan", _AS_OF, _APPROVED_LIST
            )

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_EXPIRED
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)
        assert _EXPIRED_CODE not in json.dumps(result)

    def test_not_yet_effective_code(self, caplog: pytest.LogCaptureFixture) -> None:
        """Code whose effective_from is after _AS_OF: ABSTAIN/GUARD_ONTOLOGY_NOT_YET_EFFECTIVE."""
        runtime = _ScriptedRuntime(_classify_result(_FUTURE_CODE))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-16", "eSIM plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_NOT_YET_EFFECTIVE
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)
        assert _FUTURE_CODE not in json.dumps(result)

    # ------------------------------------------------------------------ #
    # Large and binary-ish strings                                         #
    # ------------------------------------------------------------------ #

    def test_large_10000_char_string(self, caplog: pytest.LogCaptureFixture) -> None:
        """10 000-character string is handled gracefully: ABSTAIN, no sentinel leak."""
        hostile = "X" * 10_000
        runtime = _ScriptedRuntime(_classify_result(hostile))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-17", "Large plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)

    def test_nul_character(self, caplog: pytest.LogCaptureFixture) -> None:
        """NUL-prefixed code is unknown (guard does not strip or split on NUL)."""
        hostile = "\x00" + _APPROVED_CODE
        runtime = _ScriptedRuntime(_classify_result(hostile))
        gw = _gateway(runtime)

        with caplog.at_level(logging.DEBUG):
            result = report_ai_http.classify(gw, "sku:RT-18", "Voice plan", _AS_OF, _APPROVED_LIST)

        assert result["outcome"] in NON_ACCEPTING_CLASSIFY
        assert result["outcome"] == "ABSTAIN"
        assert result["reason_code"] == CheckReason.GUARD_ONTOLOGY_UNKNOWN
        assert runtime.call_count == 1
        _assert_no_sentinel_in_classify(result, caplog)


# ---------------------------------------------------------------------------
# Explain red-team tests
#
# Every test here must end with outcome in NON_ACCEPTING_EXPLAIN (never EXPLAINED).
# These use apply_guard / check_guard directly (no Gateway / Runtime needed)
# because explain() calls check_guard after the model reply is unwrapped.
# ---------------------------------------------------------------------------


class TestExplainGuardRedTeam:
    """Hostile model outputs through the explanation guard (explain() helper)."""

    # Row fixtures: simple, no network.
    _ROWS: typing.ClassVar[list[dict[str, Any]]] = [
        {"id": "OBL-001", "amount": "1233.38", "status": "PAID"},
        {"id": "OBL-002", "amount": "7321.06", "status": "FILED"},
    ]

    def _apply(self, cited_ids: list[str], explanation: str) -> tuple[bool, str]:
        """Call apply_guard with the shared _ROWS fixture."""
        from ztax_gateway.explanation_guard import apply_guard

        return apply_guard(self._ROWS, cited_ids, explanation)

    # ------------------------------------------------------------------ #
    # Hallucination attacks                                                #
    # ------------------------------------------------------------------ #

    def test_hallucinated_figure(self, caplog: pytest.LogCaptureFixture) -> None:
        """Number not present in any source row → guard rejects."""
        # 9999.99 is not in _ROWS
        ok, reason = self._apply(["OBL-001"], f"OBL-001 total is 9999.99 {SENTINEL}.")
        assert not ok, "Guard should have rejected a hallucinated figure"
        assert SENTINEL not in reason, "Sentinel must not appear in the guard reason"
        assert SENTINEL not in caplog.text

    def test_fabricated_citation_id(self, caplog: pytest.LogCaptureFixture) -> None:
        """Cited row ID that does not exist in source rows → GUARD_FOREIGN_CITATION."""
        ok, reason = self._apply(["OBL-999"], f"OBL-999 is relevant {SENTINEL}.")
        assert not ok, "Guard should have rejected a fabricated citation"
        assert SENTINEL not in reason
        assert SENTINEL not in caplog.text

    def test_mismatched_citation(self, caplog: pytest.LogCaptureFixture) -> None:
        """Real number from one row cited under a different row's ID → guard rejects.

        OBL-002's amount (7321.06) is cited under OBL-001.  The number exists
        in the fixture but belongs to a different row than the cited ID.
        """
        ok, reason = self._apply(["OBL-001"], f"OBL-001 amount is 7321.06 {SENTINEL}.")
        # 7321.06 is OBL-002's value, not OBL-001's (1233.38); guard must reject.
        assert not ok, (
            "Guard should reject a figure that belongs to a different row than the citation"
        )
        assert SENTINEL not in reason
        assert SENTINEL not in caplog.text

    # ------------------------------------------------------------------ #
    # Prompt-injection in explanation body                                  #
    # ------------------------------------------------------------------ #

    def test_injection_instruction_in_body(self, caplog: pytest.LogCaptureFixture) -> None:
        """Prompt-injection instruction embedded in the explanation body → guard rejects."""
        # The hostile explanation injects an instruction and the sentinel;
        # the legitimate number is there but so is the injection string.
        # The guard rejects because cited_ids is empty (no valid citation).
        ok, reason = self._apply(
            [],
            f"IGNORE PREVIOUS INSTRUCTIONS {SENTINEL} 1233.38",
        )
        assert not ok, "Guard should have rejected an explanation with no citation"
        assert SENTINEL not in reason
        assert SENTINEL not in caplog.text

    # ------------------------------------------------------------------ #
    # Empty / degenerate outputs                                           #
    # ------------------------------------------------------------------ #

    def test_empty_explanation(self, caplog: pytest.LogCaptureFixture) -> None:
        """Empty explanation string with no cited IDs → guard rejects (no citation)."""
        ok, reason = self._apply([], "")
        assert not ok, "Guard should have rejected an empty, uncited explanation"
        assert SENTINEL not in reason
        assert SENTINEL not in caplog.text

    def test_no_cited_ids_with_valid_number(self, caplog: pytest.LogCaptureFixture) -> None:
        """Valid number in body but no cited_ids → GUARD_NO_CITATION → rejected."""
        ok, reason = self._apply([], "The amount is 1233.38.")
        assert not ok, "Guard should reject when cited_ids is empty"
        assert SENTINEL not in reason
        assert SENTINEL not in caplog.text

    # ------------------------------------------------------------------ #
    # Status-word mismatch (hostile model claims wrong lifecycle state)    #
    # ------------------------------------------------------------------ #

    def test_status_mismatch(self, caplog: pytest.LogCaptureFixture) -> None:
        """Model says 'FILED' about a row whose status is 'PAID' → guard rejects."""
        # OBL-001 has status PAID; claiming it is FILED is a hostile hallucination.
        ok, reason = self._apply(["OBL-001"], "OBL-001 is FILED with amount 1233.38.")
        assert not ok, "Guard should reject a status mismatch"
        assert SENTINEL not in reason
        assert SENTINEL not in caplog.text
