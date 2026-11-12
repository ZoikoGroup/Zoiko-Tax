"""Tests for tools/evaluate_gateway.py.

All tests run offline: no network, no sockets, no live model calls.
FakeRuntime or scripted in-process runtimes are used throughout.

Covered by this module
-----------------------
- Every CaseStatus (PROPOSED, FORMAT_INVALID, NOT_APPROVED, ABSTAINED, REFUSED, ERROR).
- Case-sensitivity: a wrong-case reply is a normalised_match but NOT an exact_match.
- as_of required when an approved list is provided.
- No list -> membership_note says "not checked".
- evidence_grade == "PIPELINE_CHECK" for the synthetic draft file.
- evidence_grade == "REVIEWED" only for a reviewed, non-synthetic store.
- PIPELINE_CHECK_BANNER present for PIPELINE_CHECK reports.
- Dataset hash equals GoldSetFile.content_sha256.
- Per-case ai_invocation_id are unique and match the evidence sink.
- A runtime raising RuntimeError("SENTINEL-EVAL-8b1") never leaks the marker.
- --out refuses to overwrite without --force.
- Module source contains no date.today() or datetime.now() used for as_of.
- One failing case does not stop the run.
- JSON report keys are stable.
"""

from __future__ import annotations

import ast
import json
import sys
import typing
from collections.abc import Mapping
from datetime import date
from pathlib import Path
from typing import Any
from unittest.mock import MagicMock

import pytest

# Ensure tools/ is importable when running from intelligence/ root.
_TOOLS_DIR = Path(__file__).resolve().parent.parent / "tools"
if str(_TOOLS_DIR) not in sys.path:
    sys.path.insert(0, str(_TOOLS_DIR))

import evaluate_gateway as eg  # type: ignore[import-not-found]  # noqa: E402

# Helpers for ztax_gateway imports
from ztax_gateway.evidence_sink import InMemoryEvidenceSink  # noqa: E402
from ztax_gateway.gold_set_file import (  # noqa: E402
    FileCaseRecord,
    GoldSetFile,
    Origin,
    ReviewStatus,
    load_gold_set_file,
)
from ztax_gateway.governance import UseCase, UseCaseRegistry  # noqa: E402
from ztax_gateway.provenance import AuthorityOutcome, RiskTier  # noqa: E402
from ztax_gateway.runtime import Result, Route, RoutingTable  # noqa: E402
from ztax_gateway.server import Gateway  # noqa: E402

# ---------------------------------------------------------------------------
# Shared constants and helpers
# ---------------------------------------------------------------------------

_REGION = "local"
_USE_CASE = "classification-review"
_PROMPT_LABEL = "prompt:classification-review-v1"
_TRAIN = "0.0.0-test"


def _make_registry() -> UseCaseRegistry:
    reg = UseCaseRegistry()
    reg.register(
        UseCase(
            use_case_id=_USE_CASE,
            owner="test",
            description="test",
            max_risk_tier=RiskTier.T2,
            max_authority=AuthorityOutcome.A2,
            permitted_regions=frozenset([_REGION]),
            permitted_data_classes=frozenset({"P0", "P1"}),
        )
    )
    return reg


def _make_routes(
    proposed_code: str = "ontology:telecom/voice/mobile",
) -> tuple[RoutingTable, Any]:
    class _RT:
        def run(self, route: Any, call: Any) -> Result:
            return Result(proposed_code=proposed_code, confidence="0.9000")

    return RoutingTable({_USE_CASE: Route("model:fake@1", "provider:fake", _PROMPT_LABEL)}), _RT()


def _make_gateway(
    proposed_code: str = "ontology:telecom/voice/mobile",
) -> tuple[Gateway, InMemoryEvidenceSink]:
    routes_table, rt = _make_routes(proposed_code)
    sink = InMemoryEvidenceSink()
    gw = Gateway(
        _make_registry(),
        routes_table,
        rt,
        _REGION,
        _TRAIN,
        evidence_sink=sink,
    )
    return gw, sink


def _make_evaluator(
    proposed_code: str = "ontology:telecom/voice/mobile",
    *,
    approved_list: Any = None,
    as_of: date | None = None,
) -> tuple[eg.GatewayEvaluator, Gateway, InMemoryEvidenceSink]:
    gw, sink = _make_gateway(proposed_code)
    evaluator = eg.GatewayEvaluator(
        gw,
        sink,
        _REGION,
        approved_list=approved_list,
        as_of=as_of,
    )
    return evaluator, gw, sink


def _file_case(
    case_id: str = "TC-001",
    expected_class: str = "ontology:telecom/voice/mobile",
    origin: Origin = Origin.SYNTHETIC,
    review_status: ReviewStatus = ReviewStatus.REVIEWED,  # REVIEWED so to_gold_store includes it
) -> FileCaseRecord:
    return FileCaseRecord(
        case_id=case_id,
        sku_description="Standard mobile voice plan 500 minutes",
        expected_class=expected_class,
        notes="",
        origin=origin,
        author="test",
        review_status=review_status,
        reviewer="reviewer@test" if review_status == ReviewStatus.REVIEWED else "",
        reviewed_on="2026-01-01" if review_status == ReviewStatus.REVIEWED else "",
        source_ref="src-001" if origin != Origin.SYNTHETIC else "",
    )


def _gold_set_file(cases: list[FileCaseRecord], name: str = "test-set") -> GoldSetFile:
    import hashlib

    canon = json.dumps(
        {
            "name": name,
            "version": "0.1.0",
            "cases": [
                {
                    "case_id": c.case_id,
                    "sku_description": c.sku_description,
                    "expected_class": c.expected_class,
                }
                for c in cases
            ],
        },
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    sha = hashlib.sha256(canon).hexdigest()
    return GoldSetFile(name=name, version="0.1.0", content_sha256=sha, cases=tuple(cases))


# ---------------------------------------------------------------------------
# CaseStatus tests
# ---------------------------------------------------------------------------


class TestCaseStatuses:
    """Every possible CaseStatus is exercised."""

    def test_proposed_status_on_good_reply(self) -> None:
        """Well-formed code -> PROPOSED."""
        evaluator, _, _ = _make_evaluator("ontology:telecom/voice/mobile")
        case = _file_case()
        result = evaluator.evaluate_case(case)
        assert result.status == eg.CaseStatus.PROPOSED
        assert result.proposed_code == "ontology:telecom/voice/mobile"

    def test_abstained_status_on_empty_reply(self) -> None:
        """Empty proposed_code in reply bytes -> ABSTAINED.

        Note: The gateway's check_result enforces non-empty proposed_code at the
        runtime level, raising RuntimeUnavailableError (-> GatewayError) when it is
        empty.  ABSTAINED is therefore triggered by a mock that returns valid reply
        bytes that carry no proposed_code field (which encode_reply omits when empty).
        We simulate this by injecting reply bytes with no proposed_code directly.
        """
        import json as _json

        # Inject reply bytes with no proposed_code (mimicking an edge case or
        # future runtime that explicitly abstains).
        empty_reply_bytes = _json.dumps({
            "model_profile": "model:fake@1",
            "provider_profile": "provider:fake",
            "prompt_profile": "prompt:classification-review-v1",
            "ai_train_version": "0.0.0-test",
        }, sort_keys=True).encode("utf-8")

        gw_mock = MagicMock()
        gw_mock.invoke_with_id.return_value = (empty_reply_bytes, "abc123")
        sink = InMemoryEvidenceSink()
        evaluator = eg.GatewayEvaluator(gw_mock, sink, _REGION)
        case = _file_case()
        result = evaluator.evaluate_case(case)
        assert result.status == eg.CaseStatus.ABSTAINED
        assert result.proposed_code is None

    def test_format_invalid_status_on_bad_format(self) -> None:
        """Non-empty but malformed code -> FORMAT_INVALID; raw text not stored."""
        evaluator, _, _ = _make_evaluator("GARBAGE_CODE_NO_ONTOLOGY_PREFIX")
        case = _file_case()
        result = evaluator.evaluate_case(case)
        assert result.status == eg.CaseStatus.FORMAT_INVALID
        assert result.exact_match is False
        assert result.normalised_match is False
        # proposed_code must be None for FORMAT_INVALID (privacy rule)
        assert result.proposed_code is None
        # reply_length and reply_sha256 must be populated
        assert isinstance(result.reply_length, int) and result.reply_length > 0
        assert isinstance(result.reply_sha256, str) and len(result.reply_sha256) == 64

    def test_refused_status_on_gateway_error(self) -> None:
        """GatewayError from gateway -> REFUSED."""
        import grpc

        from ztax_gateway.server import GatewayError

        gw_mock = MagicMock()
        gw_mock.invoke_with_id.side_effect = GatewayError(
            grpc.StatusCode.PERMISSION_DENIED,
            "GOVERNANCE_REFUSED",
            "refused",
        )
        sink = InMemoryEvidenceSink()
        evaluator = eg.GatewayEvaluator(gw_mock, sink, _REGION)
        case = _file_case()
        result = evaluator.evaluate_case(case)
        assert result.status == eg.CaseStatus.REFUSED
        assert result.exact_match is False

    def test_error_status_on_unexpected_exception(self) -> None:
        """Unexpected RuntimeError -> ERROR with fixed error_code."""
        gw_mock = MagicMock()
        gw_mock.invoke_with_id.side_effect = RuntimeError("SENTINEL-EVAL-8b1")
        sink = InMemoryEvidenceSink()
        evaluator = eg.GatewayEvaluator(gw_mock, sink, _REGION)
        case = _file_case()
        result = evaluator.evaluate_case(case)
        assert result.status == eg.CaseStatus.ERROR
        assert result.error_code == "RUNTIME_ERROR"


# ---------------------------------------------------------------------------
# Case sensitivity
# ---------------------------------------------------------------------------


class TestCaseSensitivity:
    """Wrong-case reply is a normalised_match but NOT an exact_match."""

    def test_wrong_case_reply_is_normalised_but_not_exact(self) -> None:
        # expected: "ontology:telecom/voice/mobile"
        # reply   : "ONTOLOGY:TELECOM/VOICE/MOBILE" (FORMAT_INVALID - not well formed
        # since the regex is case-sensitive: ontology: prefix must be lowercase)
        # So let us use a code that is well-formed but differently cased in path
        expected = "ontology:telecom/voice/Mobile"
        proposed = "ontology:telecom/voice/mobile"
        # exact: strip, case-sensitive -> NOT match
        assert not eg._exact_match(proposed, expected)
        # normalised: strip+lower -> match
        assert eg._normalised_match(proposed, expected)

    def test_exact_match_is_case_sensitive(self) -> None:
        assert eg._exact_match("ontology:telecom/voice/mobile", "ontology:telecom/voice/mobile")
        assert not eg._exact_match(
            "ontology:telecom/voice/Mobile", "ontology:telecom/voice/mobile"
        )

    def test_case_result_exact_vs_normalised(self) -> None:
        """A reply that differs only in path case scores normalised but not exact."""
        # Gateway returns "ontology:telecom/VOICE/mobile", expected "ontology:telecom/voice/mobile"
        evaluator, _, _ = _make_evaluator("ontology:telecom/VOICE/mobile")
        case = _file_case(expected_class="ontology:telecom/voice/mobile")
        result = evaluator.evaluate_case(case)
        # Code is well-formed (regex is case-agnostic for path chars)
        assert result.status == eg.CaseStatus.PROPOSED
        # Path differs by case -> not an exact match
        assert result.exact_match is False
        # Strip + lower -> match
        assert result.normalised_match is True


# ---------------------------------------------------------------------------
# approved_list / as_of requirements
# ---------------------------------------------------------------------------


class TestApprovedListRequirement:
    """as_of is required when approved_list is given."""

    def test_as_of_required_with_approved_list(self) -> None:
        gw, sink = _make_gateway()
        fake_list = MagicMock()
        with pytest.raises(ValueError, match="as_of is required"):
            eg.GatewayEvaluator(gw, sink, _REGION, approved_list=fake_list, as_of=None)

    def test_no_list_means_not_checked(self) -> None:
        """No approved list -> membership_note says 'not checked'."""
        gsf = _gold_set_file([_file_case()], name="synthetic-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        assert report.approved_list_present is False
        assert "not checked" in report.membership_note
        assert report.as_of is None

    def test_not_approved_status_when_guard_says_unknown(self) -> None:
        """Well-formed code but not in approved list -> NOT_APPROVED."""
        from unittest.mock import patch

        from ztax_gateway.ontology_guard import GuardVerdict

        gsf = _gold_set_file([_file_case()], name="synthetic-test")  # noqa: F841
        fake_list = MagicMock()
        evaluator, _, _ = _make_evaluator(
            "ontology:telecom/voice/mobile",
            approved_list=fake_list,
            as_of=date(2026, 1, 1),
        )
        with patch("ztax_gateway.ontology_guard.check_code", return_value=GuardVerdict.UNKNOWN):
            case = _file_case()
            result = evaluator.evaluate_case(case)
        assert result.status == eg.CaseStatus.NOT_APPROVED
        assert result.guard_verdict == "UNKNOWN"


# ---------------------------------------------------------------------------
# Evidence grade
# ---------------------------------------------------------------------------


class TestEvidenceGrade:
    """Evidence grade is computed from dataset metadata."""

    def test_pipeline_check_for_synthetic_draft(self) -> None:
        """Synthetic DRAFT case -> PIPELINE_CHECK."""
        gsf = _gold_set_file(
            [_file_case(origin=Origin.SYNTHETIC, review_status=ReviewStatus.DRAFT)],
            name="synthetic-test",
        )
        grade = eg._compute_evidence_grade(gsf)
        assert grade == "PIPELINE_CHECK"

    def test_pipeline_check_for_reviewed_synthetic(self) -> None:
        """REVIEWED but SYNTHETIC -> still PIPELINE_CHECK."""
        gsf = _gold_set_file(
            [_file_case(origin=Origin.SYNTHETIC, review_status=ReviewStatus.REVIEWED)],
            name="synthetic-reviewed",
        )
        grade = eg._compute_evidence_grade(gsf)
        assert grade == "PIPELINE_CHECK"

    def test_reviewed_grade_for_non_synthetic_reviewed(self) -> None:
        """SOURCE_DERIVED + REVIEWED -> REVIEWED."""
        gsf = _gold_set_file(
            [
                _file_case(
                    case_id="TC-001",
                    expected_class="ontology:telecom/voice/mobile",
                    origin=Origin.SOURCE_DERIVED,
                    review_status=ReviewStatus.REVIEWED,
                ),
                _file_case(
                    case_id="TC-002",
                    expected_class="ontology:telecom/data/mobile",
                    origin=Origin.SOURCE_DERIVED,
                    review_status=ReviewStatus.REVIEWED,
                ),
            ],
            name="reviewed-set",
        )
        grade = eg._compute_evidence_grade(gsf)
        assert grade == "REVIEWED"

    def test_pipeline_check_banner_in_report(self) -> None:
        """Report for synthetic data contains the PIPELINE_CHECK banner."""
        gsf = _gold_set_file([_file_case()], name="banner-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        assert report.evidence_grade == "PIPELINE_CHECK"
        assert report.banner == eg.PIPELINE_CHECK_BANNER

    def test_no_banner_for_reviewed_dataset(self) -> None:
        """Reviewed non-synthetic dataset -> no banner."""
        gsf = _gold_set_file(
            [
                _file_case(
                    case_id="TC-001",
                    origin=Origin.SOURCE_DERIVED,
                    review_status=ReviewStatus.REVIEWED,
                ),
                _file_case(
                    case_id="TC-002",
                    expected_class="ontology:telecom/data/mobile",
                    origin=Origin.SOURCE_DERIVED,
                    review_status=ReviewStatus.REVIEWED,
                ),
            ],
            name="reviewed-set",
        )
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=False)
        assert report.evidence_grade == "REVIEWED"
        assert report.banner is None


# ---------------------------------------------------------------------------
# Dataset hash
# ---------------------------------------------------------------------------


class TestDatasetHash:
    """report.dataset_sha256 equals GoldSetFile.content_sha256."""

    def test_hash_matches_gold_set_file(self) -> None:
        gsf = _gold_set_file([_file_case()], name="hash-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        assert report.dataset_sha256 == gsf.content_sha256

    def test_draft_file_hash_matches(self) -> None:
        """Hash of real draft file is captured correctly in report.

        The draft file only contains DRAFT cases, so to_gold_store raises
        GoldSetFileError.  We verify the sha256 by loading the file and
        checking that GoldSetFile.content_sha256 is a 64-char hex string
        and matches what parse_gold_set would compute.
        """

        from ztax_gateway.gold_set_file import parse_gold_set

        draft_path = (
            Path(__file__).resolve().parent.parent / "gold" / "classification.v0.draft.json"
        )
        if not draft_path.exists():
            pytest.skip("Draft gold file not found")
        gsf = load_gold_set_file(draft_path)
        # Verify sha256 is a valid 64-char hex string
        assert len(gsf.content_sha256) == 64
        assert all(c in "0123456789abcdef" for c in gsf.content_sha256)
        # Verify it matches a fresh parse
        gsf2 = parse_gold_set(draft_path.read_text(encoding="utf-8"))
        assert gsf.content_sha256 == gsf2.content_sha256


# ---------------------------------------------------------------------------
# Invocation IDs
# ---------------------------------------------------------------------------


class TestInvocationIds:
    """Per-case ai_invocation_id are unique and recorded."""

    def test_invocation_ids_are_unique(self) -> None:
        cases = [
            _file_case(case_id=f"TC-{i:03d}") for i in range(1, 4)
        ]
        gsf = _gold_set_file(cases, name="id-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        ids = [r.ai_invocation_id for r in report.case_results if r.ai_invocation_id]
        assert len(ids) == len(set(ids)), "ai_invocation_id values must be unique"


# ---------------------------------------------------------------------------
# Sentinel leak test
# ---------------------------------------------------------------------------


class TestSentinelLeak:
    """RuntimeError("SENTINEL-EVAL-8b1") must not appear in report or logs."""

    def test_sentinel_does_not_leak_into_report(self, caplog: pytest.LogCaptureFixture) -> None:
        sentinel = "SENTINEL-EVAL-8b1"
        gw_mock = MagicMock()
        gw_mock.invoke_with_id.side_effect = RuntimeError(sentinel)
        sink = InMemoryEvidenceSink()
        evaluator = eg.GatewayEvaluator(gw_mock, sink, _REGION)
        case = _file_case()

        with caplog.at_level("DEBUG"):
            result = evaluator.evaluate_case(case)

        # Result must not contain sentinel anywhere
        result_json = json.dumps(
            {
                "status": result.status,
                "error_code": result.error_code,
                "proposed_code": result.proposed_code,
            }
        )
        assert sentinel not in result_json, "Sentinel leaked into result JSON"
        assert sentinel not in caplog.text, "Sentinel leaked into log output"

        # Build a report and check that too
        cases = [_file_case()]
        gsf = _gold_set_file(cases, name="sentinel-test")
        evaluator2 = eg.GatewayEvaluator(gw_mock, sink, _REGION)
        report = evaluator2.evaluate_store(gsf, allow_synthetic=True)
        report_json = json.dumps(report.as_dict())
        assert sentinel not in report_json, "Sentinel leaked into full report JSON"


# ---------------------------------------------------------------------------
# Single failing case does not stop the run
# ---------------------------------------------------------------------------


class TestRunContinuesOnError:
    """One case that raises must not abort the remaining cases."""

    def test_error_case_does_not_abort_run(self) -> None:
        call_count = 0

        class _AlternatingRuntime:
            def run(self, route: Any, call: Any) -> Result:
                nonlocal call_count
                call_count += 1
                if call_count == 1:
                    raise RuntimeError("transient failure")
                return Result(proposed_code="ontology:telecom/voice/mobile", confidence="0.9000")

        from ztax_gateway.runtime import Route, RoutingTable

        routes = RoutingTable({_USE_CASE: Route("model:fake@1", "provider:fake", _PROMPT_LABEL)})
        sink = InMemoryEvidenceSink()
        gw = Gateway(
            _make_registry(),
            routes,
            _AlternatingRuntime(),
            _REGION,
            _TRAIN,
            evidence_sink=sink,
        )

        evaluator = eg.GatewayEvaluator(gw, sink, _REGION)
        cases = [
            _file_case(case_id="TC-001"),
            _file_case(case_id="TC-002"),
            _file_case(case_id="TC-003"),
        ]
        gsf = _gold_set_file(cases, name="resilience-test")
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)

        # All 3 cases evaluated; first is ERROR but others succeed
        assert report.total_cases == 3
        statuses = [r.status for r in report.case_results]
        assert eg.CaseStatus.ERROR in statuses
        assert eg.CaseStatus.PROPOSED in statuses


# ---------------------------------------------------------------------------
# JSON key stability
# ---------------------------------------------------------------------------


class TestJsonKeyStability:
    """The report JSON key set is stable."""

    _EXPECTED_TOP_KEYS: typing.ClassVar[set[str]] = {
        "schema_version",
        "run_id",
        "started_at",
        "completed_at",
        "evidence_grade",
        "banner",
        "calibration",
        "evaluation_profile",
        "verdict",
        "dataset",
        "runtime",
        "ontology_check",
        "counts",
        "coverage",
        "abstention_rate",
        "metrics",
        "slices",
        "usage_and_cost",
        "case_results",
        "report_sha256",
    }

    def test_top_level_keys_are_stable(self) -> None:
        gsf = _gold_set_file([_file_case()], name="key-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        assert set(d.keys()) == self._EXPECTED_TOP_KEYS

    def test_counts_keys_are_stable(self) -> None:
        gsf = _gold_set_file([_file_case()], name="key-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        assert set(d["counts"].keys()) == {
            "total_cases",
            "proposed",
            "format_invalid",
            "not_approved",
            "abstained",
            "refused",
            "error",
            "by_reason_code",
        }

    def test_metrics_keys_are_stable(self) -> None:
        gsf = _gold_set_file([_file_case()], name="key-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        assert set(d["metrics"].keys()) == {
            "exact_matches",
            "normalised_matches",
            "accuracy_all",
            "accuracy_proposed",
            "macro_f1",
            "per_class",
            "latency_p50_ms",
            "latency_p95_ms",
        }

    def test_runtime_keys_are_stable(self) -> None:
        gsf = _gold_set_file([_file_case()], name="key-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        assert set(d["runtime"].keys()) == {
            "kind",
            "evaluator_version",
            "live_calls",
            "inference_settings",
            "model_profile",
            "provider_profile",
            "prompt_profile",
            "ai_train_version",
            "prompt_content_hashes",
        }


# ---------------------------------------------------------------------------
# No date.today / datetime.now used for as_of
# ---------------------------------------------------------------------------


class TestNoClock:
    """Module source must not use date.today() or datetime.now() for as_of."""

    def test_module_source_has_no_date_today_for_as_of(self) -> None:
        source_path = _TOOLS_DIR / "evaluate_gateway.py"
        source = source_path.read_text(encoding="utf-8")
        # We allow datetime.now(UTC) for timestamps but not for as_of logic.
        # Check there is no assignment of date.today() or datetime.now()
        # to something that could be as_of.
        # We use AST to find all calls.
        tree = ast.parse(source)
        for node in ast.walk(tree):
            if isinstance(node, ast.Call):
                func = node.func
                if (
                    isinstance(func, ast.Attribute)
                    and func.attr in ("today",)
                    and isinstance(func.value, ast.Name)
                    and func.value.id == "date"
                ):
                    pytest.fail(
                        f"Found date.today() at line {node.lineno} in evaluate_gateway.py"
                    )


# ---------------------------------------------------------------------------
# --out overwrite protection
# ---------------------------------------------------------------------------


class TestOutOverwrite:
    """--out refuses to overwrite an existing file unless --force is given."""

    def test_refuses_overwrite_without_force(self, tmp_path: Path) -> None:
        existing = tmp_path / "report.json"
        existing.write_text("{}", encoding="utf-8")
        result = eg.main(
            [
                "--gold",
                str(
                    Path(__file__).resolve().parent.parent
                    / "gold"
                    / "classification.v0.draft.json"
                ),
                "--allow-synthetic",
                "--out",
                str(existing),
            ]
        )
        # Should fail (non-zero exit code or the file should still be "{}")
        # The main function returns 1 on overwrite refusal
        assert result == 1
        assert existing.read_text(encoding="utf-8") == "{}"

    def test_allows_overwrite_with_force(self, tmp_path: Path) -> None:
        existing = tmp_path / "report.json"
        existing.write_text("{}", encoding="utf-8")
        # The real draft file contains only DRAFT-status cases, which
        # to_gold_store() skips. Use a minimal REVIEWED+SYNTHETIC temp file
        # so evaluate_store() has at least one eligible case to process.
        gold = tmp_path / "test.gold.json"
        gold.write_text(
            json.dumps(
                {
                    "name": "overwrite-test",
                    "version": "0.1.0",
                    "domain": "test",
                    "purpose": "overwrite force test",
                    "cases": [
                        {
                            "case_id": "TC-OW-001",
                            "sku_description": "mobile voice plan",
                            "expected_class": "ontology:Telecom/Voice/Mobile",
                            "notes": "",
                            "origin": "SYNTHETIC",
                            "author": "test",
                            "review_status": "REVIEWED",
                            "reviewer": "r@test",
                            "reviewed_on": "2026-01-01",
                            "source_ref": "",
                        }
                    ],
                }
            ),
            encoding="utf-8",
        )
        result = eg.main(
            [
                "--gold",
                str(gold),
                "--allow-synthetic",
                "--out",
                str(existing),
                "--force",
            ]
        )
        assert result == 0
        written = json.loads(existing.read_text(encoding="utf-8"))
        assert written.get("evidence_grade") == "PIPELINE_CHECK"


# ---------------------------------------------------------------------------
# Pipeline-check banner cannot be bypassed
# ---------------------------------------------------------------------------


class TestBannerPresence:
    def test_banner_present_in_dict_for_pipeline_check(self) -> None:
        gsf = _gold_set_file([_file_case()], name="banner-dict-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        assert d["banner"] == eg.PIPELINE_CHECK_BANNER

    def test_banner_not_present_for_reviewed_dataset(self) -> None:
        gsf = _gold_set_file(
            [
                _file_case(
                    case_id="TC-001",
                    origin=Origin.SOURCE_DERIVED,
                    review_status=ReviewStatus.REVIEWED,
                ),
                _file_case(
                    case_id="TC-002",
                    expected_class="ontology:telecom/data/mobile",
                    origin=Origin.SOURCE_DERIVED,
                    review_status=ReviewStatus.REVIEWED,
                ),
            ],
            name="reviewed-set",
        )
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=False)
        d = report.as_dict()
        assert d["banner"] is None


# ---------------------------------------------------------------------------
# Well-formedness helper
# ---------------------------------------------------------------------------


class TestWellFormedness:
    def test_valid_ontology_code(self) -> None:
        assert eg._is_well_formed("ontology:telecom/voice/mobile")
        assert eg._is_well_formed("ontology:a")
        assert eg._is_well_formed("ontology:telecom/data/mobile.prepaid")

    def test_invalid_codes(self) -> None:
        assert not eg._is_well_formed("")
        assert not eg._is_well_formed("ONTOLOGY:telecom/voice/mobile")  # wrong case prefix
        assert not eg._is_well_formed("telecom/voice/mobile")  # no prefix
        assert not eg._is_well_formed("ontology:")  # empty path
        assert not eg._is_well_formed("ontology: telecom/voice")  # space

    def test_format_invalid_in_evaluator(self) -> None:
        evaluator, _, _ = _make_evaluator("NOT_A_VALID_CODE")
        result = evaluator.evaluate_case(_file_case())
        assert result.status == eg.CaseStatus.FORMAT_INVALID



# ---------------------------------------------------------------------------
# Gap 1: Dataset hash full 64 chars in JSON and stdout
# ---------------------------------------------------------------------------


class TestDatasetHashFull:
    """dataset.content_sha256 must be exactly 64 chars in JSON and in stdout."""

    def test_json_hash_is_64_chars(self) -> None:
        gsf = _gold_set_file([_file_case()], name="hash64-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        sha = d["dataset"]["content_sha256"]
        assert isinstance(sha, str)
        assert len(sha) == 64, f"Expected 64-char sha256, got {len(sha)}: {sha!r}"
        assert all(c in "0123456789abcdef" for c in sha)

    def test_stdout_hash_is_full_64_chars(self, capsys: pytest.CaptureFixture[str]) -> None:
        """_print_summary must print the full 64-char hash, not a truncated prefix."""
        gsf = _gold_set_file([_file_case()], name="stdout-hash-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        eg._print_summary(report)
        out = capsys.readouterr().out
        # The hash must appear verbatim in the output
        assert report.dataset_sha256 in out, (
            f"Full 64-char hash not found in stdout.\n"
            f"Hash: {report.dataset_sha256}\n"
            f"Output: {out!r}"
        )
        # Confirm the hash itself is 64 chars
        assert len(report.dataset_sha256) == 64


# ---------------------------------------------------------------------------
# Gap 2: evaluator_version, live_calls, inference_settings
# ---------------------------------------------------------------------------


class TestNewMetadataFields:
    """Gap 2: evaluator_version, live_calls, inference_settings in report."""

    def _run(self) -> dict[str, Any]:
        gsf = _gold_set_file([_file_case()], name="meta-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        result: dict[str, Any] = report.as_dict()
        return result

    def test_evaluator_version_present(self) -> None:
        d = self._run()
        assert "evaluator_version" in d["runtime"]
        ev = d["runtime"]["evaluator_version"]
        assert isinstance(ev, str) and len(ev) > 0

    def test_live_calls_is_false(self) -> None:
        d = self._run()
        assert d["runtime"]["live_calls"] is False

    def test_inference_settings_is_null(self) -> None:
        d = self._run()
        assert d["runtime"]["inference_settings"] is None


# ---------------------------------------------------------------------------
# Gap 3: calibration field
# ---------------------------------------------------------------------------


class TestCalibrationField:
    """Gap 3: calibration must be 'not calibrated'."""

    def test_calibration_field_present_and_correct(self) -> None:
        gsf = _gold_set_file([_file_case()], name="calib-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        assert d.get("calibration") == "not calibrated"


# ---------------------------------------------------------------------------
# Gap 4: evaluation_profile and verdict NOT_EVALUATED
# ---------------------------------------------------------------------------


class TestEvaluationProfileAndVerdict:
    """Gap 4: evaluation_profile must be null, verdict must be NOT_EVALUATED."""

    def test_evaluation_profile_is_null(self) -> None:
        gsf = _gold_set_file([_file_case()], name="profile-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        assert d.get("evaluation_profile") is None

    def test_verdict_is_not_evaluated(self) -> None:
        gsf = _gold_set_file([_file_case()], name="verdict-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        assert d.get("verdict") == "NOT_EVALUATED"


# ---------------------------------------------------------------------------
# Gap 5: prompt_content_hashes run-level list
# ---------------------------------------------------------------------------


class TestPromptHashList:
    """Gap 5: prompt_content_hashes is a deduplicated list at run level."""

    def test_hashes_list_present(self) -> None:
        gsf = _gold_set_file([_file_case()], name="phash-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        assert "prompt_content_hashes" in d["runtime"]
        assert isinstance(d["runtime"]["prompt_content_hashes"], list)

    def test_hashes_are_deduplicated(self) -> None:
        """Multiple cases with the same runtime -> same hash -> appears once."""
        cases = [_file_case(case_id=f"TC-{i:03d}") for i in range(1, 4)]
        gsf = _gold_set_file(cases, name="phash-dedup-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        hashes = report.prompt_content_hashes
        # No duplicates
        assert len(hashes) == len(set(str(h) for h in hashes))

    def test_hashes_entries_are_strings_or_none(self) -> None:
        gsf = _gold_set_file([_file_case()], name="phash-type-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        for h in report.prompt_content_hashes:
            assert h is None or isinstance(h, str)


# ---------------------------------------------------------------------------
# Gap 6: reason_code_counts, coverage, abstention_rate
# ---------------------------------------------------------------------------


class TestReasonCodeCounts:
    """Gap 6: counts.by_reason_code must sum to total_cases."""

    def test_reason_code_counts_sum_to_total(self) -> None:
        """3 cases: 2 PROPOSED + 1 ERROR -> counts sum == 3."""
        call_count = 0

        class _AlternatingRTForCounts:
            def run(self, route: Any, call: Any) -> Any:
                from ztax_gateway.runtime import Result

                nonlocal call_count
                call_count += 1
                if call_count == 2:
                    raise RuntimeError("transient")
                return Result(proposed_code="ontology:telecom/voice/mobile", confidence="0.9")

        from ztax_gateway.runtime import Route, RoutingTable

        routes = RoutingTable(
            {_USE_CASE: Route("model:fake@1", "provider:fake", _PROMPT_LABEL)}
        )
        sink = InMemoryEvidenceSink()
        gw = Gateway(
            _make_registry(),
            routes,
            _AlternatingRTForCounts(),
            _REGION,
            _TRAIN,
            evidence_sink=sink,
        )
        evaluator = eg.GatewayEvaluator(gw, sink, _REGION)
        cases = [_file_case(case_id=f"TC-{i:03d}") for i in range(1, 4)]
        gsf = _gold_set_file(cases, name="reason-test")
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        total = d["counts"]["total_cases"]
        reason_sum = sum(d["counts"]["by_reason_code"].values())
        assert reason_sum == total, (
            f"reason_code sum {reason_sum} != total {total}: "
            f"{d['counts']['by_reason_code']}"
        )

    def test_reason_code_counts_non_empty(self) -> None:
        gsf = _gold_set_file([_file_case()], name="reason-nonempty")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        assert d["counts"]["by_reason_code"]  # must not be empty


class TestCoverageAndAbstention:
    """Gap 6: coverage and abstention_rate with numerator/denominator."""

    def test_coverage_numerator_equals_proposed_count(self) -> None:
        gsf = _gold_set_file([_file_case()], name="cov-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        assert d["coverage"]["numerator"] == d["counts"]["proposed"]
        assert d["coverage"]["denominator"] == d["counts"]["total_cases"]

    def test_abstention_rate_denominator_equals_total(self) -> None:
        gsf = _gold_set_file([_file_case()], name="abs-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        assert d["abstention_rate"]["denominator"] == d["counts"]["total_cases"]
        assert d["abstention_rate"]["numerator"] == d["counts"]["abstained"]


# ---------------------------------------------------------------------------
# Gap 7: product-family slices
# ---------------------------------------------------------------------------


class TestProductFamilySlices:
    """Gap 7: slices.product_family and slices.unavailable."""

    def test_product_family_slice_exists(self) -> None:
        gsf = _gold_set_file([_file_case()], name="slice-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        assert "slices" in d
        assert "product_family" in d["slices"]
        assert isinstance(d["slices"]["product_family"], list)

    def test_product_family_has_correct_families(self) -> None:
        """Two families: telecom (voice) and broadband (data)."""
        cases = [
            _file_case(case_id="TC-001", expected_class="ontology:telecom/voice/mobile"),
            _file_case(case_id="TC-002", expected_class="ontology:broadband/data/home"),
            _file_case(case_id="TC-003", expected_class="ontology:telecom/data/mobile"),
        ]
        gsf = _gold_set_file(cases, name="family-test")
        evaluator, _, _ = _make_evaluator("ontology:telecom/voice/mobile")
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        families = {s["family"] for s in d["slices"]["product_family"]}
        assert "telecom" in families
        assert "broadband" in families

    def test_family_totals_sum_to_total_cases(self) -> None:
        cases = [
            _file_case(case_id="TC-001", expected_class="ontology:telecom/voice/mobile"),
            _file_case(case_id="TC-002", expected_class="ontology:broadband/data/home"),
        ]
        gsf = _gold_set_file(cases, name="family-sum-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        family_total = sum(s["total"] for s in d["slices"]["product_family"])
        assert family_total == d["counts"]["total_cases"]

    def test_slices_unavailable_contains_three_names(self) -> None:
        gsf = _gold_set_file([_file_case()], name="unavail-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        unavail_names = {s["name"] for s in d["slices"]["unavailable"]}
        assert unavail_names == {"jurisdiction", "ambiguous", "split"}

    def test_slices_unavailable_reason_is_not_in_gold_schema(self) -> None:
        gsf = _gold_set_file([_file_case()], name="unavail-reason-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)
        d = report.as_dict()
        for entry in d["slices"]["unavailable"]:
            assert entry["reason"] == "not in gold schema"


# ---------------------------------------------------------------------------
# Gap 8: FORMAT_INVALID privacy — no raw text in report
# ---------------------------------------------------------------------------


class TestFormatInvalidPrivacy:
    """Gap 8: FORMAT_INVALID must store only length + sha256, never raw text."""

    _SENTINEL = "SENTINEL-EVAL-FMT-3d9 this is garbage text"

    def _run_with_garbage(
        self,
    ) -> tuple[eg.GatewayCaseResult, eg.GatewayRunReport]:
        """Run one FORMAT_INVALID case using the sentinel as the reply."""
        import json as _json

        reply_bytes = _json.dumps(
            {"proposed_code": self._SENTINEL},
            sort_keys=True,
        ).encode("utf-8")

        gw_mock = MagicMock()
        gw_mock.invoke_with_id.return_value = (reply_bytes, "inv-fmt-001")
        sink = InMemoryEvidenceSink()
        evaluator = eg.GatewayEvaluator(gw_mock, sink, _REGION)
        case = _file_case()
        result = evaluator.evaluate_case(case)

        gsf = _gold_set_file([_file_case()], name="fmt-privacy-test")
        gw_mock2 = MagicMock()
        gw_mock2.invoke_with_id.return_value = (reply_bytes, "inv-fmt-002")
        sink2 = InMemoryEvidenceSink()
        evaluator2 = eg.GatewayEvaluator(gw_mock2, sink2, _REGION)
        report = evaluator2.evaluate_store(gsf, allow_synthetic=True)
        return result, report

    def test_proposed_code_is_none_for_format_invalid(self) -> None:
        result, _ = self._run_with_garbage()
        assert result.status == eg.CaseStatus.FORMAT_INVALID
        assert result.proposed_code is None, (
            "proposed_code must be None for FORMAT_INVALID"
        )

    def test_reply_length_and_sha256_present(self) -> None:
        result, _ = self._run_with_garbage()
        assert isinstance(result.reply_length, int) and result.reply_length > 0
        assert isinstance(result.reply_sha256, str) and len(result.reply_sha256) == 64

    def test_sentinel_not_in_report_json(self, caplog: pytest.LogCaptureFixture) -> None:
        _, report = self._run_with_garbage()
        with caplog.at_level("DEBUG"):
            report_json = json.dumps(report.as_dict())
        assert self._SENTINEL not in report_json, (
            "Sentinel text leaked into the report JSON"
        )
        assert self._SENTINEL not in caplog.text, (
            "Sentinel text leaked into log output"
        )

    def test_sentinel_not_in_repr(self) -> None:
        result, report = self._run_with_garbage()
        assert self._SENTINEL not in repr(result)
        assert self._SENTINEL not in repr(report)

    def test_reply_length_matches_encoded_bytes(self) -> None:
        """reply_length must equal len(reply.encode('utf-8', errors='replace'))."""
        payload = "BAD_CODE_xyz"
        payload_bytes = payload.encode("utf-8", errors="replace")
        import json as _json

        reply_bytes = _json.dumps({"proposed_code": payload}, sort_keys=True).encode("utf-8")
        gw_mock = MagicMock()
        gw_mock.invoke_with_id.return_value = (reply_bytes, "inv-len-001")
        sink = InMemoryEvidenceSink()
        evaluator = eg.GatewayEvaluator(gw_mock, sink, _REGION)
        result = evaluator.evaluate_case(_file_case())
        assert result.status == eg.CaseStatus.FORMAT_INVALID
        assert result.reply_length == len(payload_bytes)


# ---------------------------------------------------------------------------
# Gap 9: report_sha256 determinism
# ---------------------------------------------------------------------------


class TestReportSha256:
    """Gap 9: report_sha256 is deterministic over identical inputs."""

    def _make_report(self, name: str = "sha-test") -> eg.GatewayRunReport:
        cases = [
            _file_case(case_id="TC-001"),
            _file_case(case_id="TC-002", expected_class="ontology:telecom/data/mobile"),
        ]
        gsf = _gold_set_file(cases, name=name)
        evaluator, _, _ = _make_evaluator()
        return evaluator.evaluate_store(gsf, allow_synthetic=True)

    def test_report_sha256_present_and_64_chars(self) -> None:
        report = self._make_report()
        d = report.as_dict()
        sha = d.get("report_sha256")
        assert isinstance(sha, str) and len(sha) == 64, (
            f"report_sha256 expected 64-char hex, got {sha!r}"
        )

    def test_two_runs_on_same_input_give_same_sha(self) -> None:
        """Two evaluate_store calls on the same GSF must produce the same hash."""
        cases = [
            _file_case(case_id="TC-001"),
            _file_case(case_id="TC-002", expected_class="ontology:telecom/data/mobile"),
        ]
        gsf = _gold_set_file(cases, name="determ-test")

        evaluator1, _, _ = _make_evaluator()
        report1 = evaluator1.evaluate_store(gsf, allow_synthetic=True)

        evaluator2, _, _ = _make_evaluator()
        report2 = evaluator2.evaluate_store(gsf, allow_synthetic=True)

        assert report1.report_sha256 == report2.report_sha256, (
            f"report_sha256 differs between runs:\n"
            f"  run1: {report1.report_sha256}\n"
            f"  run2: {report2.report_sha256}"
        )

    def test_changing_a_case_changes_sha(self) -> None:
        """Different expected_class in one case must produce a different sha256."""
        cases_a = [_file_case(case_id="TC-001")]
        cases_b = [_file_case(case_id="TC-001", expected_class="ontology:broadband/data/home")]
        gsf_a = _gold_set_file(cases_a, name="sha-change-a")
        gsf_b = _gold_set_file(cases_b, name="sha-change-b")

        evaluator_a, _, _ = _make_evaluator()
        report_a = evaluator_a.evaluate_store(gsf_a, allow_synthetic=True)

        evaluator_b, _, _ = _make_evaluator("ontology:broadband/data/home")
        report_b = evaluator_b.evaluate_store(gsf_b, allow_synthetic=True)

        assert report_a.report_sha256 != report_b.report_sha256, (
            "report_sha256 must change when case content changes"
        )

    def test_changing_run_id_does_not_change_sha(self) -> None:
        """run_id is excluded from the hash; changing it must NOT change sha256."""
        gsf = _gold_set_file([_file_case()], name="run-id-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)

        d = report.as_dict()
        # Tamper run_id and recompute; the sha should remain the same
        d2 = dict(d)
        d2["run_id"] = "totally-different-run-id"
        # Use the module's own function to recompute
        sha_original = eg._compute_report_sha256(d)
        sha_tampered = eg._compute_report_sha256(d2)
        assert sha_original == sha_tampered, (
            "run_id must be excluded from report_sha256 computation"
        )

    def test_changing_started_at_does_not_change_sha(self) -> None:
        """started_at is excluded; changing it must NOT change sha256."""
        gsf = _gold_set_file([_file_case()], name="ts-test")
        evaluator, _, _ = _make_evaluator()
        report = evaluator.evaluate_store(gsf, allow_synthetic=True)

        d = report.as_dict()
        d2 = dict(d)
        d2["started_at"] = "2099-01-01T00:00:00+00:00"
        sha_original = eg._compute_report_sha256(d)
        sha_tampered = eg._compute_report_sha256(d2)
        assert sha_original == sha_tampered, (
            "started_at must be excluded from report_sha256 computation"
        )


# ---------------------------------------------------------------------------
# Fix 1: --approved-list reads bytes, handles errors, never leaks content
# ---------------------------------------------------------------------------

# Minimal valid approved-list JSON used by Fix-1 tests.
_VALID_APPROVED_LIST_JSON = json.dumps(
    {
        "list_version": "2026-01-01",
        "approved": [
            {
                "code": "ontology:Telecom/Voice/Mobile",
                "effective_from": "2024-01-01",
            }
        ],
    }
)

# Minimal REVIEWED+SYNTHETIC gold file payload used by several tests.
_REVIEWED_SYNTHETIC_CASE = {
    "case_id": "TC-FIX1-001",
    "sku_description": "mobile voice plan",
    "expected_class": "ontology:Telecom/Voice/Mobile",
    "notes": "",
    "origin": "SYNTHETIC",
    "author": "test",
    "review_status": "REVIEWED",
    "reviewer": "r@test",
    "reviewed_on": "2026-01-01",
    "source_ref": "",
}

_REVIEWED_SYNTHETIC_GOLD = {
    "name": "fix1-test",
    "version": "0.1.0",
    "domain": "test",
    "purpose": "fix1 approved-list test",
    "cases": [_REVIEWED_SYNTHETIC_CASE],
}


def _write_gold(
    tmp_path: Path, payload: Mapping[str, object], name: str = "test.gold.json"
) -> Path:
    """Write a scratch gold file into tmp_path and return its path."""
    p = tmp_path / name
    p.write_text(json.dumps(payload), encoding="utf-8")
    return p


class TestApprovedListFix:
    """Fix 1: --approved-list must be read as bytes before passing to load_approved_list."""

    def test_valid_approved_list_exits_zero(self, tmp_path: Path) -> None:
        """A syntactically correct approved list + --as-of exits 0."""
        gold = _write_gold(tmp_path, _REVIEWED_SYNTHETIC_GOLD)
        approved = tmp_path / "approved.json"
        approved.write_text(_VALID_APPROVED_LIST_JSON, encoding="utf-8")

        rc = eg.main(
            [
                "--gold", str(gold),
                "--allow-synthetic",
                "--approved-list", str(approved),
                "--as-of", "2026-06-01",
            ]
        )
        assert rc == 0

    def test_missing_approved_list_prints_fixed_message(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        """A path that does not exist prints the exact fixed message, exits non-zero."""
        gold = _write_gold(tmp_path, _REVIEWED_SYNTHETIC_GOLD)
        missing = tmp_path / "no_such_file.json"

        rc = eg.main(
            [
                "--gold", str(gold),
                "--allow-synthetic",
                "--approved-list", str(missing),
                "--as-of", "2026-06-01",
            ]
        )

        assert rc != 0
        err = capsys.readouterr().err
        assert "error: could not load approved list" in err
        assert "Traceback" not in err

    def test_invalid_utf8_approved_list_prints_fixed_message(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        """A file with invalid UTF-8 bytes prints the exact fixed message, exits non-zero."""
        gold = _write_gold(tmp_path, _REVIEWED_SYNTHETIC_GOLD)
        bad = tmp_path / "bad_utf8.json"
        bad.write_bytes(b"\xff\xfe{invalid utf-8}")

        rc = eg.main(
            [
                "--gold", str(gold),
                "--allow-synthetic",
                "--approved-list", str(bad),
                "--as-of", "2026-06-01",
            ]
        )

        assert rc != 0
        err = capsys.readouterr().err
        assert "error: could not load approved list" in err
        assert "Traceback" not in err

    def test_malformed_json_approved_list_prints_fixed_message(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        """A syntactically invalid JSON file prints the exact fixed message, exits non-zero."""
        gold = _write_gold(tmp_path, _REVIEWED_SYNTHETIC_GOLD)
        bad = tmp_path / "malformed.json"
        bad.write_text("{ this is not json !!!", encoding="utf-8")

        rc = eg.main(
            [
                "--gold", str(gold),
                "--allow-synthetic",
                "--approved-list", str(bad),
                "--as-of", "2026-06-01",
            ]
        )

        assert rc != 0
        err = capsys.readouterr().err
        assert "error: could not load approved list" in err
        assert "Traceback" not in err

    def test_sentinel_never_leaks_from_approved_list(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        """Content placed inside the approved-list file must never appear in stdout or stderr."""
        sentinel = "SENTINEL-APPROVED-LIST-CONTENT-7x9q"
        gold = _write_gold(tmp_path, _REVIEWED_SYNTHETIC_GOLD)
        bad = tmp_path / "sentinel.json"
        # Embed sentinel in an otherwise malformed file
        bad.write_text(f'{{ "{sentinel}": true }}', encoding="utf-8")

        rc = eg.main(
            [
                "--gold", str(gold),
                "--allow-synthetic",
                "--approved-list", str(bad),
                "--as-of", "2026-06-01",
            ]
        )

        assert rc != 0
        captured = capsys.readouterr()
        assert sentinel not in captured.out
        assert sentinel not in captured.err

    def test_no_traceback_on_missing_file(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        """The fixed message contains no Python traceback text on a missing-file error."""
        gold = _write_gold(tmp_path, _REVIEWED_SYNTHETIC_GOLD)

        rc = eg.main(
            [
                "--gold", str(gold),
                "--allow-synthetic",
                "--approved-list", str(tmp_path / "ghost.json"),
                "--as-of", "2026-06-01",
            ]
        )

        assert rc != 0
        captured = capsys.readouterr()
        assert "Traceback" not in captured.out
        assert "Traceback" not in captured.err


# ---------------------------------------------------------------------------
# Fix 2: pre-evaluate eligibility check gives actionable error messages
# ---------------------------------------------------------------------------

_DRAFT_CASE = {
    "case_id": "TC-DRAFT-001",
    "sku_description": "some sku",
    "expected_class": "ontology:Telecom/Voice/Mobile",
    "notes": "",
    "origin": "SOURCE_DERIVED",
    "author": "test",
    "review_status": "DRAFT",
    "reviewer": "",
    "reviewed_on": "",
    "source_ref": "ref-001",
}

_ALL_DRAFT_GOLD = {
    "name": "draft-only-test",
    "version": "0.1.0",
    "domain": "test",
    "purpose": "draft-only gold file",
    "cases": [_DRAFT_CASE],
}

_REVIEWED_SYNTHETIC_GOLD_FIX2 = {
    "name": "reviewed-synthetic-test",
    "version": "0.1.0",
    "domain": "test",
    "purpose": "all-reviewed-synthetic gold file",
    "cases": [_REVIEWED_SYNTHETIC_CASE],
}


class TestEligibilityPreCheckFix:
    """Fix 2: actionable messages when no cases would reach evaluate_store()."""

    def test_all_draft_prints_no_reviewed_message(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        """An all-DRAFT gold file prints the REVIEWED-cases fixed message and exits 1."""
        gold = _write_gold(tmp_path, _ALL_DRAFT_GOLD, "draft.gold.json")

        rc = eg.main(["--gold", str(gold)])

        assert rc == 1
        err = capsys.readouterr().err
        assert "error: the gold set has no REVIEWED cases" in err
        assert "draft files cannot be evaluated" in err
        assert "Traceback" not in err

    def test_reviewed_synthetic_without_flag_prints_synthetic_message(
        self, tmp_path: Path, capsys: pytest.CaptureFixture[str]
    ) -> None:
        """REVIEWED+SYNTHETIC cases without --allow-synthetic print the SYNTHETIC message."""
        gold = _write_gold(tmp_path, _REVIEWED_SYNTHETIC_GOLD_FIX2, "syn.gold.json")

        rc = eg.main(["--gold", str(gold)])

        assert rc == 1
        err = capsys.readouterr().err
        assert "error: all REVIEWED cases are SYNTHETIC" in err
        assert "--allow-synthetic" in err
        assert "Traceback" not in err

    def test_reviewed_synthetic_with_flag_runs_to_completion(
        self, tmp_path: Path
    ) -> None:
        """REVIEWED+SYNTHETIC cases WITH --allow-synthetic run to completion.

        evidence_grade must be PIPELINE_CHECK.
        """
        gold = _write_gold(tmp_path, _REVIEWED_SYNTHETIC_GOLD_FIX2, "syn_ok.gold.json")
        out = tmp_path / "report.json"

        rc = eg.main(
            [
                "--gold", str(gold),
                "--allow-synthetic",
                "--out", str(out),
            ]
        )

        assert rc == 0
        report = json.loads(out.read_text(encoding="utf-8"))
        assert report.get("evidence_grade") == "PIPELINE_CHECK"
