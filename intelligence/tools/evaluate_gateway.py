"""Gateway-path evaluation harness (offline, fake runtimes only).

Sends each gold-set case through the classification-review use case of a
Gateway instance backed by a given ModelRuntime, scores the replies, and
writes a JSON + human-summary report.

This module lives in tools/ and is EXCLUDED from the production gateway
container (docker-compose.yml mounts only src/ztax_gateway).  It must never
be imported by any module under src/.

RULES this module must never break
-----------------------------------
* No live model calls, no network, no sockets.
* No date.today() or datetime.now() used for as_of.  as_of is always an
  explicit caller-supplied argument.
* evidence_grade is computed from the dataset metadata, not from a CLI flag.
* No raw prompt text, no raw model output beyond the proposed_code.
* Sentinel exceptions must never leak into the report, printed output or logs.
* The PIPELINE CHECK ONLY banner cannot be removed by any flag.
* FORMAT_INVALID replies must store only length + sha256, never the reply text.
* report_sha256 is computed over canonical JSON excluding run_id, timestamps,
  latency fields and report_sha256 itself, so two identical runs always agree.
"""

from __future__ import annotations

import base64
import hashlib
import json
import logging
import re
import sys
import time
import uuid
from collections import defaultdict
from collections.abc import Callable
from dataclasses import dataclass
from datetime import UTC, date, datetime
from enum import StrEnum
from pathlib import Path
from typing import Any

# Evaluator version — bump when the harness logic changes.
EVALUATOR_VERSION: str = "evaluate_gateway/1.0.0"

# ---------------------------------------------------------------------------
# Ontology code well-formedness (mirrors _ONTOLOGY_CODE_RE in ontology_guard.py
# but private to this module so we do NOT edit ontology_guard.py).
# Pattern: starts with "ontology:" followed by [A-Za-z0-9_./:-]+, fullmatch.
# ---------------------------------------------------------------------------
_ONTOLOGY_CODE_RE: re.Pattern[str] = re.compile(r"ontology:[A-Za-z0-9_./:-]+")

# Evaluation tenant UUID - used for every harness call.  UUIDs are the
# canonical format in this codebase (test_gateway_wiring.py:102).
# This is a fixed v4 UUID reserved for the evaluation harness tenant.
_EVAL_TENANT_ID: str = "0190f3a2-1b2c-7d3e-8f40-ea6b7c8d9e0f"

# The classification-review use case id (registry.live-demo.json).
_USE_CASE: str = "classification-review"

# Data class for gold-set SKU catalog text (registry permits "P0").
_DATA_CLASS: str = "P0"

# Authority and risk tier that the live classification path sends.
# Sourced from registry.live-demo.json: max_authority="A1", max_risk_tier="T2",
# and the live call uses authority_outcome="A1", risk_tier="T1".
_AUTHORITY_OUTCOME: str = "A1"
_RISK_TIER: str = "T1"

# Pipeline-check banner text (must appear in JSON and in stdout).
PIPELINE_CHECK_BANNER: str = (
    "PIPELINE CHECK ONLY: synthetic or fake data, not evidence of model accuracy"
)

log = logging.getLogger("evaluate_gateway")


# ---------------------------------------------------------------------------
# Case-level status enum
# ---------------------------------------------------------------------------


class CaseStatus(StrEnum):
    """Outcome status for one evaluated gold-set case."""

    PROPOSED = "PROPOSED"
    """Well-formed code; approved by ontology list (or list absent)."""

    FORMAT_INVALID = "FORMAT_INVALID"
    """Non-empty reply that is not a well-formed ontology code."""

    NOT_APPROVED = "NOT_APPROVED"
    """Well-formed code, but guard returned UNKNOWN / NOT_YET_EFFECTIVE / EXPIRED."""

    ABSTAINED = "ABSTAINED"
    """Empty or absent proposal from the model."""

    REFUSED = "REFUSED"
    """Governance refusal or kill-switch block from the gateway."""

    ERROR = "ERROR"
    """Transport failure, malformed wire, or unexpected exception."""


# ---------------------------------------------------------------------------
# Per-case result
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class GatewayCaseResult:
    """Outcome for a single evaluated gold-set case."""

    case_id: str
    expected_class: str
    proposed_code: str | None
    status: CaseStatus
    # Exact match (case-sensitive, strip only) - headline metric.
    exact_match: bool
    # Normalised match (strip + lower - MetricEngine rule) - reported separately.
    normalised_match: bool
    guard_verdict: str | None  # "ALLOWED" / "UNKNOWN" / "NOT_YET_EFFECTIVE" / "EXPIRED" / None
    latency_ms: float
    ai_invocation_id: str | None
    prompt_content_hash: str | None
    # Short fixed error code for REFUSED / ERROR; never exception text.
    error_code: str | None
    # FORMAT_INVALID privacy: store length + sha256 of the reply, never the text.
    # Both are None for every status other than FORMAT_INVALID.
    reply_length: int | None = None
    reply_sha256: str | None = None


# ---------------------------------------------------------------------------
# Run-level report
# ---------------------------------------------------------------------------


@dataclass
class GatewayRunReport:
    """Aggregated result of evaluating a full gold set through the gateway."""

    run_id: str
    started_at: str  # ISO 8601 UTC
    completed_at: str  # ISO 8601 UTC

    # Dataset provenance
    dataset_name: str
    dataset_version: str
    dataset_sha256: str
    dataset_case_count: int
    origin_counts: dict[str, int]
    review_status_counts: dict[str, int]

    # Runtime identity (read from reply provenance)
    model_profile: str | None
    provider_profile: str | None
    prompt_profile: str | None
    ai_train_version: str | None
    runtime_kind: str  # "fake" in this PR

    # New: static metadata fields (Gap 1 & 2)
    evaluator_version: str           # constant string; bump on harness changes
    live_calls: bool                 # always False for fake runtimes
    inference_settings: None         # None when runtime does not expose them

    # New: prompt hashes seen across the run (Gap 5)
    prompt_content_hashes: list[str | None]  # de-duplicated; None counted separately

    # Evidence grade
    evidence_grade: str  # "REVIEWED" or "PIPELINE_CHECK"
    banner: str | None  # PIPELINE_CHECK_BANNER when grade is PIPELINE_CHECK

    # Calibration field (Gap 3)
    calibration: str  # always "not calibrated" while no calibration data exists

    # Evaluation profile + verdict (Gap 4)
    evaluation_profile: None         # None while no thresholds are defined
    verdict: str                     # always "NOT_EVALUATED" while thresholds undefined

    # Ontology check
    approved_list_present: bool
    as_of: str | None  # YYYY-MM-DD when approved_list given; else None
    membership_note: str  # "not checked" when no list

    # Status counts
    total_cases: int
    proposed_count: int
    format_invalid_count: int
    not_approved_count: int
    abstained_count: int
    refused_count: int
    error_count: int

    # New: outcome counts per reason code (Gap 6 — REQ-0037)
    reason_code_counts: dict[str, int]

    # New: coverage and abstention with numerator/denominator (Gap 6)
    coverage_numerator: int
    coverage_denominator: int
    abstention_numerator: int
    abstention_denominator: int

    # Match metrics
    exact_matches: int
    normalised_matches: int
    accuracy: float  # exact_matches / total_cases
    proposed_accuracy: float  # exact_matches / proposed_count (or 0.0)

    # Per-class metrics (over exact matches)
    per_class: list[dict[str, Any]]
    macro_f1: float | None

    # Latency
    latency_p50_ms: float | None
    latency_p95_ms: float | None

    # Usage note
    usage_and_cost: str  # "not recorded"

    # New: product-family slices (Gap 7)
    product_family_slices: list[dict[str, Any]]
    slices_unavailable: list[dict[str, str]]

    # Case-level results
    case_results: list[GatewayCaseResult]

    # New: report-level sha256 for determinism (Gap 9)
    report_sha256: str

    def as_dict(self) -> dict[str, Any]:
        """Serialise to a JSON-compatible dictionary."""
        d: dict[str, Any] = {
            "schema_version": "evaluate_gateway/v1",
            "run_id": self.run_id,
            "started_at": self.started_at,
            "completed_at": self.completed_at,
            "evidence_grade": self.evidence_grade,
            "banner": self.banner,
            "calibration": self.calibration,
            "evaluation_profile": self.evaluation_profile,
            "verdict": self.verdict,
            "dataset": {
                "name": self.dataset_name,
                "version": self.dataset_version,
                "content_sha256": self.dataset_sha256,
                "case_count": self.dataset_case_count,
                "origin_counts": self.origin_counts,
                "review_status_counts": self.review_status_counts,
            },
            "runtime": {
                "kind": self.runtime_kind,
                "evaluator_version": self.evaluator_version,
                "live_calls": self.live_calls,
                "inference_settings": self.inference_settings,
                "model_profile": self.model_profile,
                "provider_profile": self.provider_profile,
                "prompt_profile": self.prompt_profile,
                "ai_train_version": self.ai_train_version,
                "prompt_content_hashes": self.prompt_content_hashes,
            },
            "ontology_check": {
                "approved_list_present": self.approved_list_present,
                "as_of": self.as_of,
                "membership_note": self.membership_note,
            },
            "counts": {
                "total_cases": self.total_cases,
                "proposed": self.proposed_count,
                "format_invalid": self.format_invalid_count,
                "not_approved": self.not_approved_count,
                "abstained": self.abstained_count,
                "refused": self.refused_count,
                "error": self.error_count,
                "by_reason_code": self.reason_code_counts,
            },
            "coverage": {
                "numerator": self.coverage_numerator,
                "denominator": self.coverage_denominator,
            },
            "abstention_rate": {
                "numerator": self.abstention_numerator,
                "denominator": self.abstention_denominator,
            },
            "metrics": {
                "exact_matches": self.exact_matches,
                "normalised_matches": self.normalised_matches,
                "accuracy_all": self.accuracy,
                "accuracy_proposed": self.proposed_accuracy,
                "macro_f1": self.macro_f1,
                "per_class": self.per_class,
                "latency_p50_ms": self.latency_p50_ms,
                "latency_p95_ms": self.latency_p95_ms,
            },
            "slices": {
                "product_family": self.product_family_slices,
                "unavailable": self.slices_unavailable,
            },
            "usage_and_cost": self.usage_and_cost,
            "case_results": [
                _serialise_case(r) for r in self.case_results
            ],
            "report_sha256": self.report_sha256,
        }
        return d


# ---------------------------------------------------------------------------
# Case serialiser (keeps FORMAT_INVALID privacy: no raw reply text)
# ---------------------------------------------------------------------------


def _serialise_case(r: GatewayCaseResult) -> dict[str, Any]:
    """Serialise one GatewayCaseResult to a JSON-compatible dict.

    For FORMAT_INVALID cases, proposed_code is None and reply_length /
    reply_sha256 carry the redacted reply metadata.  For all other statuses
    those two fields are omitted from the output entirely.
    """
    d: dict[str, Any] = {
        "case_id": r.case_id,
        "expected_class": r.expected_class,
        "proposed_code": r.proposed_code,
        "status": r.status,
        "exact_match": r.exact_match,
        "normalised_match": r.normalised_match,
        "guard_verdict": r.guard_verdict,
        "latency_ms": r.latency_ms,
        "ai_invocation_id": r.ai_invocation_id,
        "prompt_content_hash": r.prompt_content_hash,
        "error_code": r.error_code,
    }
    if r.status == CaseStatus.FORMAT_INVALID:
        d["reply_length"] = r.reply_length
        d["reply_sha256"] = r.reply_sha256
    return d


# ---------------------------------------------------------------------------
# Report sha256 helper (Gap 9)
# ---------------------------------------------------------------------------

# Fields excluded from the canonical hash so two runs on identical inputs
# always produce the same report_sha256.
_HASH_EXCLUDED: frozenset[str] = frozenset(
    {"run_id", "started_at", "completed_at", "report_sha256", "ai_invocation_id"}
)


def _canonical_clean(obj: Any) -> Any:
    """Recursively strip excluded keys and all 'latency' keys from *obj*."""
    if isinstance(obj, dict):
        return {
            k: _canonical_clean(v)
            for k, v in obj.items()
            if k not in _HASH_EXCLUDED and "latency" not in k
        }
    if isinstance(obj, list):
        return [_canonical_clean(x) for x in obj]
    return obj


def _compute_report_sha256(d: dict[str, Any]) -> str:
    """Return the sha256 of the canonical JSON of *d* (exclusions applied)."""
    cleaned = _canonical_clean(d)
    canon = json.dumps(cleaned, sort_keys=True, separators=(",", ":"), ensure_ascii=True)
    return hashlib.sha256(canon.encode("utf-8")).hexdigest()


# ---------------------------------------------------------------------------
# Format check helper (local copy of ontology guard pattern)
# ---------------------------------------------------------------------------


def _is_well_formed(code: str) -> bool:
    """Return True if *code* is a syntactically valid ontology code.

    Uses the same regex as ontology_guard._ONTOLOGY_CODE_RE but as a fullmatch
    so the entire stripped string must conform.  This module deliberately keeps
    its own copy so ontology_guard.py is not modified.
    """
    return bool(_ONTOLOGY_CODE_RE.fullmatch(code.strip()))


# ---------------------------------------------------------------------------
# Matching helpers
# ---------------------------------------------------------------------------


def _exact_match(proposed: str, expected: str) -> bool:
    """Case-sensitive strip-only match (headline metric)."""
    return proposed.strip() == expected.strip()


def _normalised_match(proposed: str, expected: str) -> bool:
    """Case-insensitive strip+lower match (MetricEngine rule, reported separately)."""
    return proposed.strip().lower() == expected.strip().lower()


# ---------------------------------------------------------------------------
# Latency percentile helper
# ---------------------------------------------------------------------------


def _percentile(values: list[float], pct: float) -> float | None:
    if not values:
        return None
    srt = sorted(values)
    idx = int(len(srt) * pct / 100)
    idx = min(idx, len(srt) - 1)
    return srt[idx]


# ---------------------------------------------------------------------------
# Per-class metrics
# ---------------------------------------------------------------------------


def _compute_per_class(
    results: list[GatewayCaseResult],
) -> tuple[list[dict[str, Any]], float | None]:
    """Compute per-class precision/recall/F1 on exact matches.

    Only PROPOSED cases contribute to per-class scoring.
    Returns (per_class_list, macro_f1).
    """
    classes: set[str] = set()
    for r in results:
        classes.add(r.expected_class.strip())
        if r.proposed_code:
            classes.add(r.proposed_code.strip())

    per_class = []
    f1s = []
    for cls in sorted(classes):
        tp = sum(
            1
            for r in results
            if r.expected_class.strip() == cls
            and r.proposed_code is not None
            and r.proposed_code.strip() == cls  # exact (case-sensitive)
        )
        fp = sum(
            1
            for r in results
            if r.proposed_code is not None
            and r.proposed_code.strip() == cls
            and r.expected_class.strip() != cls
        )
        fn = sum(
            1
            for r in results
            if r.expected_class.strip() == cls
            and (r.proposed_code is None or r.proposed_code.strip() != cls)
        )
        precision = tp / (tp + fp) if (tp + fp) > 0 else 0.0
        recall = tp / (tp + fn) if (tp + fn) > 0 else 0.0
        f1 = (
            2 * precision * recall / (precision + recall)
            if (precision + recall) > 0
            else 0.0
        )
        f1s.append(f1)
        per_class.append(
            {
                "class": cls,
                "true_positives": tp,
                "false_positives": fp,
                "false_negatives": fn,
                "precision": round(precision, 6),
                "recall": round(recall, 6),
                "f1": round(f1, 6),
            }
        )

    macro_f1 = round(sum(f1s) / len(f1s), 6) if f1s else None
    return per_class, macro_f1


# ---------------------------------------------------------------------------
# Evidence grade computation
# ---------------------------------------------------------------------------


def _compute_evidence_grade(gsf: Any) -> str:
    """Compute evidence_grade from dataset metadata.

    "REVIEWED" only if every case has review_status == REVIEWED and
    origin != SYNTHETIC.  Otherwise "PIPELINE_CHECK".
    """
    from ztax_gateway.gold_set_file import Origin, ReviewStatus

    for case in gsf.cases:
        if case.review_status != ReviewStatus.REVIEWED:
            return "PIPELINE_CHECK"
        if case.origin == Origin.SYNTHETIC:
            return "PIPELINE_CHECK"
    return "REVIEWED"


# ---------------------------------------------------------------------------
# Wire call builder
# ---------------------------------------------------------------------------


def _build_call_bytes(case_id: str, sku_description: str, region: str) -> bytes:
    """Build a JSON-encoded wire Call for the classification-review use case.

    Uses the same wire format as _raw_call() in test_gateway_wiring.py:
      - kind: CLASSIFICATION_PROPOSAL
      - tenant_id: UUID eval constant
      - authority_outcome: A1, risk_tier: T1, data_classes: [P0]
      - region: gateway's own region (passed through)
    """
    doc = {
        "kind": "CLASSIFICATION_PROPOSAL",
        "governance": {
            "tenant_id": _EVAL_TENANT_ID,
            "use_case": _USE_CASE,
            "authority_outcome": _AUTHORITY_OUTCOME,
            "risk_tier": _RISK_TIER,
            "region": region,
            "data_classes": [_DATA_CLASS],
        },
        "subject_ref": case_id,
        "input_b64": base64.b64encode(sku_description.encode("utf-8")).decode("ascii"),
    }
    return json.dumps(doc, sort_keys=True).encode("utf-8")


# ---------------------------------------------------------------------------
# Evaluator
# ---------------------------------------------------------------------------


class GatewayEvaluator:
    """Evaluates a GoldSetFile through a Gateway, case by case.

    Parameters
    ----------
    gateway:
        A configured Gateway instance.  The harness calls invoke_with_id.
    evidence_sink:
        An InMemoryEvidenceSink already attached to the gateway.  Used to
        read per-call ai_invocation_id and prompt_content_hash.
    region:
        The gateway's configured region (used in wire calls).
    approved_list:
        Optional ApprovedOntologyList.  When None, only format is checked
        and membership is marked "not checked".
    as_of:
        Required when approved_list is not None.  Must be an explicit
        datetime.date; never read from the system clock.
    clock:
        Injectable monotonic clock (seconds).  Defaults to time.monotonic.
    """

    def __init__(
        self,
        gateway: Any,
        evidence_sink: Any,
        region: str,
        *,
        approved_list: Any | None = None,
        as_of: date | None = None,
        clock: Callable[[], float] | None = None,
    ) -> None:
        if approved_list is not None and as_of is None:
            raise ValueError("evaluate_gateway: as_of is required when approved_list is given")
        self._gateway = gateway
        self._evidence_sink = evidence_sink
        self._region = region
        self._approved_list = approved_list
        self._as_of = as_of
        self._clock = clock or time.monotonic

    def evaluate_case(self, case: Any) -> GatewayCaseResult:
        """Evaluate one gold-set case.  Never raises; errors become ERROR status."""
        from ztax_gateway.server import GatewayError

        t0 = self._clock()
        try:
            call_bytes = _build_call_bytes(case.case_id, case.sku_description, self._region)
            reply_bytes, inv_id = self._gateway.invoke_with_id(call_bytes)
            latency_ms = (self._clock() - t0) * 1000.0

            # Read evidence record from sink (after the call, sink has been updated)
            try:
                ev_record = self._evidence_sink.last
                prompt_hash = ev_record.prompt_content_hash
                # inv_id from invoke_with_id is the ai_invocation_id
            except (IndexError, AttributeError):
                prompt_hash = None

            # Decode the reply bytes (JSON encoded by encode_reply in wire.py)
            try:
                reply_doc = json.loads(reply_bytes.decode("utf-8"))
                proposed = reply_doc.get("proposed_code", "") or ""
            except Exception:
                return GatewayCaseResult(
                    case_id=case.case_id,
                    expected_class=case.expected_class,
                    proposed_code=None,
                    status=CaseStatus.ERROR,
                    exact_match=False,
                    normalised_match=False,
                    guard_verdict=None,
                    latency_ms=latency_ms,
                    ai_invocation_id=inv_id,
                    prompt_content_hash=prompt_hash,
                    error_code="WIRE_DECODE_ERROR",
                )

            if not proposed.strip():
                return GatewayCaseResult(
                    case_id=case.case_id,
                    expected_class=case.expected_class,
                    proposed_code=None,
                    status=CaseStatus.ABSTAINED,
                    exact_match=False,
                    normalised_match=False,
                    guard_verdict=None,
                    latency_ms=latency_ms,
                    ai_invocation_id=inv_id,
                    prompt_content_hash=prompt_hash,
                    error_code=None,
                )

            # Format check
            if not _is_well_formed(proposed):
                # Privacy: never store the raw reply text for FORMAT_INVALID.
                # Store only length and sha256 so the reply can be identified
                # without leaking its content into the report.
                _reply_bytes = proposed.encode("utf-8", errors="replace")
                return GatewayCaseResult(
                    case_id=case.case_id,
                    expected_class=case.expected_class,
                    proposed_code=None,
                    status=CaseStatus.FORMAT_INVALID,
                    exact_match=False,
                    normalised_match=False,
                    guard_verdict=None,
                    latency_ms=latency_ms,
                    ai_invocation_id=inv_id,
                    prompt_content_hash=prompt_hash,
                    error_code=None,
                    reply_length=len(_reply_bytes),
                    reply_sha256=hashlib.sha256(_reply_bytes).hexdigest(),
                )

            # Ontology membership check (only when approved list given)
            guard_verdict: str | None = None
            status: CaseStatus
            if self._approved_list is not None:
                from ztax_gateway.ontology_guard import GuardVerdict, check_code

                verdict = check_code(
                    code=proposed.strip(),
                    as_of=self._as_of,  # type: ignore[arg-type]
                    approved_list=self._approved_list,
                )
                guard_verdict = verdict.value
                if verdict == GuardVerdict.ALLOWED:
                    status = CaseStatus.PROPOSED
                else:
                    status = CaseStatus.NOT_APPROVED
            else:
                guard_verdict = None
                status = CaseStatus.PROPOSED

            exact = _exact_match(proposed, case.expected_class)
            norm = _normalised_match(proposed, case.expected_class)

            return GatewayCaseResult(
                case_id=case.case_id,
                expected_class=case.expected_class,
                proposed_code=proposed.strip(),
                status=status,
                exact_match=exact,
                normalised_match=norm,
                guard_verdict=guard_verdict,
                latency_ms=latency_ms,
                ai_invocation_id=inv_id,
                prompt_content_hash=prompt_hash,
                error_code=None,
            )

        except GatewayError as err:
            latency_ms = (self._clock() - t0) * 1000.0
            return GatewayCaseResult(
                case_id=case.case_id,
                expected_class=case.expected_class,
                proposed_code=None,
                status=CaseStatus.REFUSED,
                exact_match=False,
                normalised_match=False,
                guard_verdict=None,
                latency_ms=latency_ms,
                ai_invocation_id=None,
                prompt_content_hash=None,
                error_code=err.reason if hasattr(err, "reason") else "GATEWAY_REFUSED",
            )
        except Exception:
            latency_ms = (self._clock() - t0) * 1000.0
            return GatewayCaseResult(
                case_id=case.case_id,
                expected_class=case.expected_class,
                proposed_code=None,
                status=CaseStatus.ERROR,
                exact_match=False,
                normalised_match=False,
                guard_verdict=None,
                latency_ms=latency_ms,
                ai_invocation_id=None,
                prompt_content_hash=None,
                error_code="RUNTIME_ERROR",
            )

    def evaluate_store(
        self,
        gsf: Any,
        *,
        allow_synthetic: bool = False,
        runtime_kind: str = "fake",
    ) -> GatewayRunReport:
        """Evaluate all eligible cases in *gsf* and return a GatewayRunReport.

        Parameters
        ----------
        gsf:
            A validated GoldSetFile.
        allow_synthetic:
            Pass through to to_gold_store().  Required for draft synthetic files.
        runtime_kind:
            Short label written into the report.  "fake" in this PR.
        """
        started_at = datetime.now(UTC).isoformat()
        run_id = uuid.uuid4().hex

        gold_store = gsf.to_gold_store(allow_synthetic=allow_synthetic)
        cases = gold_store.cases

        evidence_grade = _compute_evidence_grade(gsf)
        banner = PIPELINE_CHECK_BANNER if evidence_grade == "PIPELINE_CHECK" else None

        # Build origin / review_status counts from the full file
        origin_counts: dict[str, int] = {}
        review_status_counts: dict[str, int] = {}
        for fc in gsf.cases:
            origin_counts[fc.origin] = origin_counts.get(fc.origin, 0) + 1
            review_status_counts[fc.review_status] = (
                review_status_counts.get(fc.review_status, 0) + 1
            )

        # Evaluate cases one by one
        case_results: list[GatewayCaseResult] = []
        for case in cases:
            result = self.evaluate_case(case)
            case_results.append(result)

        completed_at = datetime.now(UTC).isoformat()

        # Aggregate counts
        total = len(case_results)
        proposed_count = sum(1 for r in case_results if r.status == CaseStatus.PROPOSED)
        format_invalid_count = sum(
            1 for r in case_results if r.status == CaseStatus.FORMAT_INVALID
        )
        not_approved_count = sum(
            1 for r in case_results if r.status == CaseStatus.NOT_APPROVED
        )
        abstained_count = sum(1 for r in case_results if r.status == CaseStatus.ABSTAINED)
        refused_count = sum(1 for r in case_results if r.status == CaseStatus.REFUSED)
        error_count = sum(1 for r in case_results if r.status == CaseStatus.ERROR)
        exact_matches = sum(1 for r in case_results if r.exact_match)
        normalised_matches = sum(1 for r in case_results if r.normalised_match)

        accuracy = exact_matches / total if total > 0 else 0.0
        proposed_accuracy = exact_matches / proposed_count if proposed_count > 0 else 0.0

        # Per-class metrics
        per_class, macro_f1 = _compute_per_class(case_results)

        # Latency
        latencies = [r.latency_ms for r in case_results]
        latency_p50 = _percentile(latencies, 50)
        latency_p95 = _percentile(latencies, 95)

        # Read provenance from first successful reply (if any)
        model_profile: str | None = None
        provider_profile: str | None = None
        prompt_profile_val: str | None = None
        ai_train_version: str | None = None
        try:
            # Read provenance from the evidence sink records.
            if self._evidence_sink.records:
                first = self._evidence_sink.records[0]
                # ai_release_manifest_id carries ai_train_version
                ai_train_version = getattr(first, "ai_release_manifest_id", None)
        except Exception:
            pass

        # --- New aggregations (Gaps 5, 6, 7) ---

        # Gap 5: Run-level de-duplicated prompt_content_hash list.
        # None entries are kept and counted once; the list is sorted for stability.
        seen_hashes: dict[str | None, None] = {}
        for r in case_results:
            if r.prompt_content_hash not in seen_hashes:
                seen_hashes[r.prompt_content_hash] = None
        # Put None last, then sort the string values
        str_hashes = sorted(h for h in seen_hashes if h is not None)
        prompt_content_hashes: list[str | None] = str_hashes + (
            [None] if None in seen_hashes else []
        )

        # Gap 6: Outcome counts per reason code (REQ-0037).
        # The reason code for a case is, in priority order:
        #   error_code  (REFUSED / ERROR cases carry a short fixed code)
        #   guard_verdict  (NOT_APPROVED cases carry the guard verdict)
        #   status.value  (PROPOSED, ABSTAINED, FORMAT_INVALID)
        reason_code_counts: dict[str, int] = {}
        for r in case_results:
            code = r.error_code or r.guard_verdict or r.status.value
            reason_code_counts[code] = reason_code_counts.get(code, 0) + 1

        # Gap 7: Product-family slices derived from expected_class path.
        # Format: "ontology:<family>/<rest>"  → second segment is the family.
        family_total: dict[str, int] = defaultdict(int)
        family_exact: dict[str, int] = defaultdict(int)
        for r in case_results:
            # Strip "ontology:" prefix, then split on "/"
            path = r.expected_class
            if path.startswith("ontology:"):
                path = path[len("ontology:"):]
            parts = path.split("/")
            family = parts[0] if parts else "unknown"
            family_total[family] += 1
            if r.exact_match:
                family_exact[family] += 1

        product_family_slices: list[dict[str, Any]] = [
            {
                "family": f,
                "total": family_total[f],
                "exact_matches": family_exact[f],
            }
            for f in sorted(family_total)
        ]
        slices_unavailable: list[dict[str, str]] = [
            {"name": "jurisdiction", "reason": "not in gold schema"},
            {"name": "ambiguous", "reason": "not in gold schema"},
            {"name": "split", "reason": "not in gold schema"},
        ]

        # Ontology check note
        membership_note = (
            "approved list checked"
            if self._approved_list is not None
            else "not checked: no approved list provided"
        )

        # Build the report (without report_sha256 yet).
        report = GatewayRunReport(
            run_id=run_id,
            started_at=started_at,
            completed_at=completed_at,
            dataset_name=gsf.name,
            dataset_version=gsf.version,
            dataset_sha256=gsf.content_sha256,
            dataset_case_count=len(gsf.cases),
            origin_counts=origin_counts,
            review_status_counts=review_status_counts,
            model_profile=model_profile,
            provider_profile=provider_profile,
            prompt_profile=prompt_profile_val,
            ai_train_version=ai_train_version,
            runtime_kind=runtime_kind,
            evaluator_version=EVALUATOR_VERSION,
            live_calls=False,
            inference_settings=None,
            prompt_content_hashes=prompt_content_hashes,
            evidence_grade=evidence_grade,
            banner=banner,
            calibration="not calibrated",
            evaluation_profile=None,
            verdict="NOT_EVALUATED",
            approved_list_present=self._approved_list is not None,
            as_of=self._as_of.isoformat() if self._as_of is not None else None,
            membership_note=membership_note,
            total_cases=total,
            proposed_count=proposed_count,
            format_invalid_count=format_invalid_count,
            not_approved_count=not_approved_count,
            abstained_count=abstained_count,
            refused_count=refused_count,
            error_count=error_count,
            reason_code_counts=reason_code_counts,
            coverage_numerator=proposed_count,
            coverage_denominator=total,
            abstention_numerator=abstained_count,
            abstention_denominator=total,
            exact_matches=exact_matches,
            normalised_matches=normalised_matches,
            accuracy=round(accuracy, 6),
            proposed_accuracy=round(proposed_accuracy, 6),
            per_class=per_class,
            macro_f1=macro_f1,
            latency_p50_ms=round(latency_p50, 3) if latency_p50 is not None else None,
            latency_p95_ms=round(latency_p95, 3) if latency_p95 is not None else None,
            usage_and_cost="not recorded",
            product_family_slices=product_family_slices,
            slices_unavailable=slices_unavailable,
            case_results=case_results,
            report_sha256="",  # placeholder; filled below
        )

        # Gap 9: Compute report_sha256 over the full dict (excluding volatile fields).
        # We set a stable placeholder first so as_dict() can serialise, then
        # recompute and stamp the real hash.
        raw_dict = report.as_dict()
        real_sha = _compute_report_sha256(raw_dict)
        # Replace placeholder in dataclass by rebuilding (dataclass is NOT frozen).
        report.report_sha256 = real_sha

        return report


# ---------------------------------------------------------------------------
# Gateway factory (fake runtime only in this PR)
# ---------------------------------------------------------------------------


def _make_fake_gateway(region: str, ai_train_version: str = "0.0.0-eval") -> tuple[Any, Any]:
    """Build an in-process Gateway backed by FakeRuntime.

    Returns (gateway, evidence_sink).
    """
    from pathlib import Path as _Path

    from ztax_gateway.evidence_sink import InMemoryEvidenceSink
    from ztax_gateway.fake_runtime import FakeRuntime
    from ztax_gateway.server import Gateway, load_config

    config_path = _Path(__file__).resolve().parent.parent / "config" / "registry.live-demo.json"
    registry, routes = load_config(config_path)

    # Override region to match config's permitted_regions ("local")
    runtime = FakeRuntime()
    evidence_sink = InMemoryEvidenceSink()

    gateway = Gateway(
        registry,
        routes,
        runtime,
        region,
        ai_train_version,
        evidence_sink=evidence_sink,
    )
    return gateway, evidence_sink


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

_DEFAULT_GOLD_PATH = (
    Path(__file__).resolve().parent.parent / "gold" / "classification.v0.draft.json"
)


def _print_summary(report: GatewayRunReport) -> None:
    """Print a human-readable summary to stdout."""
    if report.banner:
        print()
        print(f"*** {report.banner} ***")
        print()
    print(f"Run ID       : {report.run_id}")
    print(f"Dataset      : {report.dataset_name} v{report.dataset_version}")
    print(f"Dataset SHA  : {report.dataset_sha256}")
    print(f"Evidence     : {report.evidence_grade}")
    print(f"Runtime      : {report.runtime_kind}")
    print(f"Ontology     : {report.membership_note}")
    print(f"Cases        : {report.total_cases}")
    print(f"  PROPOSED   : {report.proposed_count}")
    print(f"  FORMAT INV : {report.format_invalid_count}")
    print(f"  NOT APPROV : {report.not_approved_count}")
    print(f"  ABSTAINED  : {report.abstained_count}")
    print(f"  REFUSED    : {report.refused_count}")
    print(f"  ERROR      : {report.error_count}")
    print(f"Exact matches: {report.exact_matches}/{report.total_cases} "
          f"({report.accuracy * 100:.1f}%)")
    print(f"Norm matches : {report.normalised_matches}/{report.total_cases}")
    print(f"Macro F1     : {report.macro_f1}")
    print(f"Latency p50  : {report.latency_p50_ms} ms")
    print(f"Latency p95  : {report.latency_p95_ms} ms")
    print(f"Usage & cost : {report.usage_and_cost}")
    print()


def main(argv: list[str] | None = None) -> int:
    """CLI entry point.  Returns exit code 0 on success, 1 on error."""
    import argparse

    parser = argparse.ArgumentParser(
        prog="evaluate_gateway",
        description="Gateway-path evaluation harness (fake runtimes only).",
    )
    parser.add_argument(
        "--gold",
        type=Path,
        default=_DEFAULT_GOLD_PATH,
        help="Path to the gold-set JSON file.",
    )
    parser.add_argument(
        "--allow-synthetic",
        action="store_true",
        default=False,
        help="Required when the file contains SYNTHETIC cases (e.g. the draft).",
    )
    parser.add_argument(
        "--approved-list",
        type=Path,
        default=None,
        dest="approved_list",
        help="Path to an approved-ontology-list JSON (requires --as-of).",
    )
    parser.add_argument(
        "--as-of",
        type=str,
        default=None,
        dest="as_of",
        help="YYYY-MM-DD reference date for the approved list (required with --approved-list).",
    )
    parser.add_argument(
        "--out",
        type=Path,
        default=None,
        help="Write JSON report to this path.  Default: print to stdout.",
    )
    parser.add_argument(
        "--force",
        action="store_true",
        default=False,
        help="Overwrite --out file if it already exists.",
    )
    parser.add_argument(
        "--runtime",
        choices=["fake"],
        default="fake",
        help="Runtime to use.  Only 'fake' is available in this release.",
    )

    args = parser.parse_args(argv)

    # Validate --approved-list / --as-of pairing
    if (args.approved_list is None) != (args.as_of is None):
        print(
            "error: --approved-list and --as-of must be given together or not at all.",
            file=sys.stderr,
        )
        return 1

    # Parse as_of
    as_of_date: date | None = None
    if args.as_of is not None:
        try:
            as_of_date = date.fromisoformat(args.as_of)
        except ValueError:
            print(f"error: --as-of {args.as_of!r} is not a valid YYYY-MM-DD date.", file=sys.stderr)
            return 1

    # Check output path safety
    if args.out is not None:
        # Refuse to write inside the repo (tools/ parent = intelligence/)
        repo_root = Path(__file__).resolve().parent.parent
        try:
            args.out.resolve().relative_to(repo_root)
            print(
                f"error: --out path {args.out} is inside the repo.  "
                "Use a path outside the repository (e.g. $TEMP).",
                file=sys.stderr,
            )
            return 1
        except ValueError:
            pass  # Path is outside the repo - OK.
        if args.out.exists() and not args.force:
            print(
                f"error: {args.out} already exists.  Pass --force to overwrite.",
                file=sys.stderr,
            )
            return 1

    # Load gold set
    from ztax_gateway.gold_set_file import GoldSetFileError, load_gold_set_file

    try:
        gsf = load_gold_set_file(args.gold)
    except GoldSetFileError as exc:
        print(f"error: could not load gold set: {exc}", file=sys.stderr)
        return 1
    except OSError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1

    # Load approved list (optional)
    approved_list = None
    if args.approved_list is not None:
        from ztax_gateway.ontology_guard import OntologyGuardError, load_approved_list

        try:
            approved_list = load_approved_list(args.approved_list)
        except (OntologyGuardError, OSError) as exc:
            print(f"error: could not load approved list: {exc}", file=sys.stderr)
            return 1

    # Build gateway and evaluator
    region = "local"  # live-demo registry only permits "local"
    gateway, evidence_sink = _make_fake_gateway(region)
    evaluator = GatewayEvaluator(
        gateway,
        evidence_sink,
        region,
        approved_list=approved_list,
        as_of=as_of_date,
    )

    # Run evaluation
    try:
        report = evaluator.evaluate_store(
            gsf,
            allow_synthetic=args.allow_synthetic,
            runtime_kind=args.runtime,
        )
    except Exception as exc:
        print(f"error: evaluation failed: {type(exc).__name__}", file=sys.stderr)
        return 1

    # Print human summary (always to stdout)
    _print_summary(report)

    # Serialise JSON
    report_dict = report.as_dict()
    report_json = json.dumps(report_dict, indent=2)

    if args.out is not None:
        args.out.write_text(report_json, encoding="utf-8")
        print(f"Report written to: {args.out}")
    else:
        print(report_json)

    return 0


if __name__ == "__main__":
    sys.exit(main())
