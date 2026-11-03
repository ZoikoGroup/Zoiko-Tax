"""WP5 gateway wiring tests: prompt content hash and answer-checked events.

Tests cover the two parts of WP5:

  Part A -- prompt_content_hash on the evidence record
  Part B -- AnswerCheckedEvent emitted for every explanation guard check

All constraints from the WP5 task prompt are encoded here:
  - Hash is set only when the call reaches the model runtime (not on BLOCK
    records for refused or killed calls).
  - No registry -> hash None, call still succeeds.
  - Matching label with different text -> different hash.
  - Refused/killed calls -> zero check events.
  - Guard pass -> one PASS event (CHECKS_PASSED).
  - Each GUARD_* failure -> ABSTAIN event with the matching reason code.
  - The original evidence record is not mutated by check events.
  - Events carry the correct ai_invocation_id.
"""

from __future__ import annotations

import base64
import json
from typing import Any

import pytest

from ztax_gateway.evidence_sink import InMemoryEvidenceSink
from ztax_gateway.governance import UseCase, UseCaseRegistry
from ztax_gateway.invocation_check_event import (
    CheckReason,
    InMemoryCheckEventSink,
    Verdict,
)
from ztax_gateway.production_gates import PromptProfileRegistry
from ztax_gateway.production_registries import PromptProfile
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.runtime import Result, Route, RoutingTable
from ztax_gateway.server import Gateway
from ztax_gateway.wire import Call

# ---------------------------------------------------------------------------
# Shared test fixtures and helpers
# ---------------------------------------------------------------------------

_REGION = "eu-west-1"
_USE_CASE = "classification-review"
_PROMPT_LABEL = "prompt:classification-review-v1"
_PROMPT_TEXT_A = "You are a tax classification assistant. Version A."
_PROMPT_TEXT_B = "You are a tax classification assistant. Version B."


class _FakeRuntime:
    """Minimal stub runtime: always succeeds."""

    def run(self, route: Route, call: Call) -> Result:
        return Result(text="ok", confidence="0.9")


def _make_registry() -> UseCaseRegistry:
    reg = UseCaseRegistry()
    reg.register(
        UseCase(
            use_case_id=_USE_CASE,
            owner="team-ai",
            description="test use case",
            max_risk_tier=RiskTier.T2,
            max_authority=AuthorityOutcome.A2,
            permitted_regions=frozenset([_REGION]),
            permitted_data_classes=frozenset({"P0", "P1"}),
        )
    )
    return reg


def _make_routes() -> RoutingTable:
    return RoutingTable({_USE_CASE: Route("model:fake@1", "provider:fake", _PROMPT_LABEL)})


def _make_profile(prompt_text: str) -> PromptProfile:
    return PromptProfile(
        profile_id=_PROMPT_LABEL,
        owner="team-ai",
        version="1.0.0",
        system_prompt=prompt_text,
        variable_schema={},
        output_schema={},
        max_risk_tier=RiskTier.T2,
        authority_ceiling=AuthorityOutcome.A2,
        safety_rules=("Do not invent values.",),
    )


def _make_prompt_registry(prompt_text: str) -> PromptProfileRegistry:
    ppr = PromptProfileRegistry()
    ppr.register(_make_profile(prompt_text))
    return ppr


def _raw_call(
    use_case: str = _USE_CASE,
    tenant_id: str = "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f",
) -> bytes:
    doc = {
        "kind": "SUGGESTION",
        "governance": {
            "tenant_id": tenant_id,
            "use_case": use_case,
            "authority_outcome": "A1",
            "risk_tier": "T1",
            "region": _REGION,
            "data_classes": ["P1"],
        },
        "subject_ref": "sku:TEST-001",
        # Embed the tenant_id in the input payload so each tenant's input_hash differs.
        "input_b64": base64.b64encode(
            json.dumps({"test": True, "tenant": tenant_id}).encode()
        ).decode("ascii"),
    }
    return json.dumps(doc).encode("utf-8")


def _gateway(
    *,
    prompt_registry: PromptProfileRegistry | None = None,
    check_sink: InMemoryCheckEventSink | None = None,
    evidence_sink: InMemoryEvidenceSink | None = None,
) -> Gateway:
    return Gateway(
        _make_registry(),
        _make_routes(),
        _FakeRuntime(),
        _REGION,
        "0.7.0",
        evidence_sink=evidence_sink,
        prompt_profile_registry=prompt_registry,
        check_event_sink=check_sink,
    )


# ===========================================================================
# PART A: Prompt content hash
# ===========================================================================


class TestPromptContentHash:
    """Part A: hash is set only when the call reaches the runtime."""

    def test_hash_is_set_when_call_reaches_runtime(self) -> None:
        """A successful call with a registry holding a matching profile sets the hash."""
        ppr = _make_prompt_registry(_PROMPT_TEXT_A)
        evidence_sink = InMemoryEvidenceSink()
        gw = _gateway(prompt_registry=ppr, evidence_sink=evidence_sink)

        gw.invoke(_raw_call())

        record = evidence_sink.last
        assert record.prompt_content_hash is not None
        # Must be a 64-char lowercase hex string.
        assert len(record.prompt_content_hash) == 64
        assert record.prompt_content_hash == _make_profile(_PROMPT_TEXT_A).content_hash

    def test_different_prompt_text_gives_different_hash(self) -> None:
        """Same label, different system_prompt -> different content_hash through Gateway."""
        ppr_a = _make_prompt_registry(_PROMPT_TEXT_A)
        ppr_b = _make_prompt_registry(_PROMPT_TEXT_B)

        sink_a = InMemoryEvidenceSink()
        sink_b = InMemoryEvidenceSink()

        _gateway(prompt_registry=ppr_a, evidence_sink=sink_a).invoke(_raw_call())
        _gateway(prompt_registry=ppr_b, evidence_sink=sink_b).invoke(_raw_call())

        assert sink_a.last.prompt_content_hash != sink_b.last.prompt_content_hash

    def test_no_registry_gives_none_hash_and_call_succeeds(self) -> None:
        """No prompt_profile_registry -> hash is None; call must not fail."""
        evidence_sink = InMemoryEvidenceSink()
        gw = _gateway(prompt_registry=None, evidence_sink=evidence_sink)

        gw.invoke(_raw_call())  # must not raise

        assert evidence_sink.last.prompt_content_hash is None

    def test_registry_with_no_matching_profile_gives_none_hash_and_call_succeeds(
        self,
    ) -> None:
        """Registry present but no profile for this label -> hash None, call succeeds."""
        ppr = PromptProfileRegistry()
        # Register a profile under a DIFFERENT label — no match for _PROMPT_LABEL.
        ppr.register(
            PromptProfile(
                profile_id="prompt:other-label-v1",
                owner="team-ai",
                version="1.0.0",
                system_prompt="Some other prompt.",
                variable_schema={},
                output_schema={},
                max_risk_tier=RiskTier.T2,
                authority_ceiling=AuthorityOutcome.A2,
                safety_rules=("safe",),
            )
        )
        evidence_sink = InMemoryEvidenceSink()
        gw = _gateway(prompt_registry=ppr, evidence_sink=evidence_sink)

        gw.invoke(_raw_call())  # must not raise

        assert evidence_sink.last.prompt_content_hash is None

    def test_killed_call_has_no_hash(self) -> None:
        """A use case that is killed produces a BLOCK record with prompt_content_hash=None."""
        ppr = _make_prompt_registry(_PROMPT_TEXT_A)
        evidence_sink = InMemoryEvidenceSink()
        gw = _gateway(prompt_registry=ppr, evidence_sink=evidence_sink)
        gw._registry.kill(_USE_CASE)

        from ztax_gateway.server import GatewayError

        with pytest.raises(GatewayError):
            gw.invoke(_raw_call())

        record = evidence_sink.last
        assert record.prompt_content_hash is None

    def test_refused_call_has_no_hash(self) -> None:
        """A call refused for residency mismatch produces a BLOCK record with hash=None."""
        ppr = _make_prompt_registry(_PROMPT_TEXT_A)
        evidence_sink = InMemoryEvidenceSink()
        gw = _gateway(prompt_registry=ppr, evidence_sink=evidence_sink)

        # Build a call whose region does not match the Gateway's region.
        doc = {
            "kind": "SUGGESTION",
            "governance": {
                "tenant_id": "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f",
                "use_case": _USE_CASE,
                "authority_outcome": "A1",
                "risk_tier": "T1",
                "region": "us-east-1",  # wrong region
                "data_classes": ["P1"],
            },
            "subject_ref": "sku:TEST-001",
            "input_b64": base64.b64encode(b'{"test": true}').decode("ascii"),
        }
        raw = json.dumps(doc).encode("utf-8")

        from ztax_gateway.server import GatewayError

        with pytest.raises(GatewayError):
            gw.invoke(raw)

        record = evidence_sink.last
        assert record.prompt_content_hash is None

    def test_invoke_with_id_returns_id_after_successful_call(self) -> None:
        """invoke_with_id() returns a non-None string id on success."""
        gw = _gateway()
        _, inv_id = gw.invoke_with_id(_raw_call())
        assert isinstance(inv_id, str)
        assert len(inv_id) > 0

    def test_invoke_with_id_matches_evidence_record(self) -> None:
        """invoke_with_id() id must equal the ai_invocation_id in the evidence record."""
        evidence_sink = InMemoryEvidenceSink()
        gw = _gateway(evidence_sink=evidence_sink)
        _, inv_id = gw.invoke_with_id(_raw_call())
        assert inv_id == evidence_sink.last.ai_invocation_id

    def test_invoke_with_id_returns_none_id_after_killed_call(self) -> None:
        """A BLOCK record (killed call) must not produce an invocation id."""
        gw = _gateway()
        gw._registry.kill(_USE_CASE)
        from ztax_gateway.server import GatewayError

        with pytest.raises(GatewayError):
            gw.invoke_with_id(_raw_call())


# ===========================================================================
# PART B: AnswerCheckedEvent via record_answer_check()
# ===========================================================================


class TestRecordAnswerCheck:
    """Part B: check events are emitted correctly and linked to the right id."""

    def _call_and_get_id(self, gw: Gateway) -> str:
        _, inv_id = gw.invoke_with_id(_raw_call())
        assert inv_id is not None
        return inv_id

    def test_pass_emits_pass_event_with_correct_reason_code(self) -> None:
        check_sink = InMemoryCheckEventSink()
        gw = _gateway(check_sink=check_sink)
        inv_id = self._call_and_get_id(gw)

        gw.record_answer_check(inv_id, Verdict.PASS, CheckReason.CHECKS_PASSED, "guard@1")

        assert len(check_sink.events) == 1
        ev = check_sink.events[0]
        assert ev.verdict is Verdict.PASS
        assert ev.reason_code is CheckReason.CHECKS_PASSED
        assert ev.evidence_id == inv_id
        assert ev.validator_version == "guard@1"

    @pytest.mark.parametrize(
        "reason_code",
        [
            CheckReason.GUARD_NO_CITATION,
            CheckReason.GUARD_FOREIGN_CITATION,
            CheckReason.GUARD_STATUS_MISMATCH,
            CheckReason.GUARD_NUMBER_NOT_IN_ROWS,
        ],
    )
    def test_each_guard_failure_emits_abstain_with_correct_reason(
        self, reason_code: CheckReason
    ) -> None:
        check_sink = InMemoryCheckEventSink()
        gw = _gateway(check_sink=check_sink)
        inv_id = self._call_and_get_id(gw)

        gw.record_answer_check(inv_id, Verdict.ABSTAIN, reason_code, "guard@1")

        assert len(check_sink.events) == 1
        ev = check_sink.events[0]
        assert ev.verdict is Verdict.ABSTAIN
        assert ev.reason_code is reason_code
        assert ev.evidence_id == inv_id

    def test_model_abstained_emits_abstain_with_model_abstained_reason(self) -> None:
        check_sink = InMemoryCheckEventSink()
        gw = _gateway(check_sink=check_sink)
        inv_id = self._call_and_get_id(gw)

        gw.record_answer_check(inv_id, Verdict.ABSTAIN, CheckReason.MODEL_ABSTAINED, "guard@1")

        assert len(check_sink.events) == 1
        ev = check_sink.events[0]
        assert ev.verdict is Verdict.ABSTAIN
        assert ev.reason_code is CheckReason.MODEL_ABSTAINED

    def test_event_carries_the_correct_invocation_id(self) -> None:
        """Each event must link to the specific evidence record it describes."""
        check_sink = InMemoryCheckEventSink()
        evidence_sink = InMemoryEvidenceSink()

        # Two separate gateway instances (separate evidence sinks) produce two ids.
        gw_a = Gateway(
            _make_registry(),
            _make_routes(),
            _FakeRuntime(),
            _REGION,
            "0.7.0",
            evidence_sink=evidence_sink,
            check_event_sink=check_sink,
        )
        gw_b = Gateway(
            _make_registry(),
            _make_routes(),
            _FakeRuntime(),
            _REGION,
            "0.7.0",
            evidence_sink=evidence_sink,
            check_event_sink=check_sink,
        )

        _, id_a = gw_a.invoke_with_id(_raw_call())
        _, id_b = gw_b.invoke_with_id(_raw_call())

        assert id_a is not None
        assert id_b is not None
        assert id_a != id_b

        gw_a.record_answer_check(id_a, Verdict.PASS, CheckReason.CHECKS_PASSED, "v1")
        gw_b.record_answer_check(id_b, Verdict.ABSTAIN, CheckReason.GUARD_NO_CITATION, "v1")

        events_for_a = check_sink.for_evidence(id_a)
        events_for_b = check_sink.for_evidence(id_b)
        assert len(events_for_a) == 1
        assert len(events_for_b) == 1
        assert events_for_a[0].verdict is Verdict.PASS
        assert events_for_b[0].verdict is Verdict.ABSTAIN

    def test_original_evidence_record_is_unchanged_after_check_event(self) -> None:
        """Writing a check event must not mutate the original evidence record."""
        evidence_sink = InMemoryEvidenceSink()
        check_sink = InMemoryCheckEventSink()
        gw = _gateway(evidence_sink=evidence_sink, check_sink=check_sink)
        _, inv_id = gw.invoke_with_id(_raw_call())

        record = evidence_sink.last
        before = record.as_dict()
        assert inv_id is not None

        gw.record_answer_check(
            inv_id,
            Verdict.ABSTAIN,
            CheckReason.GUARD_NO_CITATION,
            "guard@1",
        )

        assert record.as_dict() == before

    def test_killed_call_produces_zero_check_events(self) -> None:
        """A refused (killed) call never reaches the model, so no check event is emitted."""
        check_sink = InMemoryCheckEventSink()
        gw = _gateway(check_sink=check_sink)
        gw._registry.kill(_USE_CASE)

        from ztax_gateway.server import GatewayError

        with pytest.raises(GatewayError):
            gw.invoke(_raw_call())

        # No record_answer_check should be called on a refused call.
        assert len(check_sink.events) == 0

    def test_refused_call_produces_zero_check_events(self) -> None:
        """A call refused for residency mismatch emits no check event."""
        check_sink = InMemoryCheckEventSink()
        gw = _gateway(check_sink=check_sink)

        doc = {
            "kind": "SUGGESTION",
            "governance": {
                "tenant_id": "0190f3a2-1b2c-7d3e-8f40-5a6b7c8d9e0f",
                "use_case": _USE_CASE,
                "authority_outcome": "A1",
                "risk_tier": "T1",
                "region": "us-east-1",  # wrong region -> RESIDENCY_REFUSED
                "data_classes": ["P1"],
            },
            "subject_ref": "sku:TEST-001",
            "input_b64": base64.b64encode(b'{"test": true}').decode("ascii"),
        }
        raw = json.dumps(doc).encode("utf-8")

        from ztax_gateway.server import GatewayError

        with pytest.raises(GatewayError):
            gw.invoke(raw)

        assert len(check_sink.events) == 0

    def test_record_answer_check_does_not_alter_evidence_record_count(self) -> None:
        """record_answer_check writes to the check sink, not the evidence sink."""
        evidence_sink = InMemoryEvidenceSink()
        check_sink = InMemoryCheckEventSink()
        gw = _gateway(evidence_sink=evidence_sink, check_sink=check_sink)
        _, inv_id = gw.invoke_with_id(_raw_call())

        assert len(evidence_sink.records) == 1
        assert inv_id is not None

        gw.record_answer_check(inv_id, Verdict.PASS, CheckReason.CHECKS_PASSED, "v1")

        # Still one — check events go to check_sink only.
        assert len(evidence_sink.records) == 1
        assert len(check_sink.events) == 1


# ===========================================================================
# PART B concurrent: two invoke_with_id() calls for different tenants
# ===========================================================================


class TestConcurrentInvokeWithId:
    """Concurrency: two overlapping invoke_with_id() calls must each return their own id."""

    def _run_one_concurrent_iteration(self, iteration: int) -> None:
        """One iteration of the concurrent tenant-isolation scenario."""
        import threading

        # Two distinct tenant UUIDs — one per thread.
        tenant_a = "aaa00000-0000-0000-0000-000000000001"
        tenant_b = "bbb00000-0000-0000-0000-000000000002"

        # Distinct verdicts/reasons per tenant so swapped ids are doubly detectable.
        verdict_a = Verdict.PASS
        reason_a = CheckReason.CHECKS_PASSED
        verdict_b = Verdict.ABSTAIN
        reason_b = CheckReason.GUARD_NO_CITATION

        evidence_sink = InMemoryEvidenceSink()
        check_sink = InMemoryCheckEventSink()

        # timeout=5 so a deadlocked barrier fails the test cleanly.
        barrier = threading.Barrier(2, timeout=5)

        class _SlowRuntime:
            def run(self, route: Route, call: Call) -> Result:
                barrier.wait()  # both calls must be in flight simultaneously
                return Result(text="ok", confidence="0.9")

        gw = Gateway(
            _make_registry(),
            _make_routes(),
            _SlowRuntime(),
            _REGION,
            "0.7.0",
            evidence_sink=evidence_sink,
            check_event_sink=check_sink,
        )

        results: dict[str, str] = {}
        errors: list[Exception] = []

        def _call(
            name: str,
            tenant_id: str,
            verdict: Verdict,
            reason: CheckReason,
            _gw: Gateway = gw,
            _results: dict[str, str] = results,
            _errors: list[Exception] = errors,
            _iteration: int = iteration,
        ) -> None:
            try:
                _, inv_id = _gw.invoke_with_id(_raw_call(tenant_id=tenant_id))
                assert inv_id is not None, (
                    f"iter={_iteration} tenant={name}: invoke_with_id returned None id"
                )
                _results[name] = inv_id
                # Each tenant records a DIFFERENT verdict/reason.
                _gw.record_answer_check(inv_id, verdict, reason, "guard@1")
            except Exception as exc:
                _errors.append(exc)

        t1 = threading.Thread(target=_call, args=("a", tenant_a, verdict_a, reason_a))
        t2 = threading.Thread(target=_call, args=("b", tenant_b, verdict_b, reason_b))
        t1.start()
        t2.start()
        t1.join(timeout=10)
        t2.join(timeout=10)
        assert not t1.is_alive(), f"iter={iteration}: thread 1 still running after timeout"
        assert not t2.is_alive(), f"iter={iteration}: thread 2 still running after timeout"

        assert not errors, f"iter={iteration}: threads raised: {errors}"
        assert len(results) == 2, (
            f"iter={iteration}: expected 2 results, got {results}"
        )
        id_a = results["a"]
        id_b = results["b"]
        assert id_a != id_b, (
            f"iter={iteration}: concurrent calls produced the same invocation id "
            f"{id_a!r} — shared state suspected"
        )

        # Both records exist in the evidence sink; index by invocation id.
        records_by_id = {r.ai_invocation_id: r for r in evidence_sink.records}
        assert id_a in records_by_id, (
            f"iter={iteration}: evidence record for tenant A ({id_a!r}) not found"
        )
        assert id_b in records_by_id, (
            f"iter={iteration}: evidence record for tenant B ({id_b!r}) not found"
        )

        # The two records are genuinely distinct.
        rec_a = records_by_id[id_a]
        rec_b = records_by_id[id_b]
        assert rec_a.ai_invocation_id != rec_b.ai_invocation_id, (
            f"iter={iteration}: both tenants share the same evidence record"
        )
        # Each tenant's input payload is distinct, so the input_hash must differ.
        if rec_a.input_hash is not None and rec_b.input_hash is not None:
            assert rec_a.input_hash != rec_b.input_hash, (
                f"iter={iteration}: tenant A and B share the same input_hash — "
                "their evidence records are indistinguishable"
            )

        # Each check event must link ONLY to its own call's id.
        events_for_a = check_sink.for_evidence(id_a)
        events_for_b = check_sink.for_evidence(id_b)
        assert len(events_for_a) == 1, (
            f"iter={iteration}: expected 1 event for tenant A, got {len(events_for_a)}"
        )
        assert len(events_for_b) == 1, (
            f"iter={iteration}: expected 1 event for tenant B, got {len(events_for_b)}"
        )
        # Correct invocation id linkage.
        assert events_for_a[0].evidence_id == id_a, (
            f"iter={iteration}: tenant A event links to {events_for_a[0].evidence_id!r}, "
            f"expected {id_a!r}"
        )
        assert events_for_b[0].evidence_id == id_b, (
            f"iter={iteration}: tenant B event links to {events_for_b[0].evidence_id!r}, "
            f"expected {id_b!r}"
        )
        # Correct verdict/reason — a cross-contamination also swaps these.
        assert events_for_a[0].verdict is verdict_a, (
            f"iter={iteration}: tenant A verdict {events_for_a[0].verdict!r}, "
            f"expected {verdict_a!r}"
        )
        assert events_for_a[0].reason_code is reason_a, (
            f"iter={iteration}: tenant A reason {events_for_a[0].reason_code!r}, "
            f"expected {reason_a!r}"
        )
        assert events_for_b[0].verdict is verdict_b, (
            f"iter={iteration}: tenant B verdict {events_for_b[0].verdict!r}, "
            f"expected {verdict_b!r}"
        )
        assert events_for_b[0].reason_code is reason_b, (
            f"iter={iteration}: tenant B reason {events_for_b[0].reason_code!r}, "
            f"expected {reason_b!r}"
        )
        # Cross-check: no event for A links to B's id, and vice versa.
        assert all(e.evidence_id != id_b for e in events_for_a), (
            f"iter={iteration}: an event for tenant A links to tenant B's id"
        )
        assert all(e.evidence_id != id_a for e in events_for_b), (
            f"iter={iteration}: an event for tenant B links to tenant A's id"
        )
        # The two events differ in both id and reason.
        assert events_for_a[0].evidence_id != events_for_b[0].evidence_id, (
            f"iter={iteration}: both check events share the same evidence_id"
        )

    def test_concurrent_calls_link_events_to_own_records(self) -> None:
        """Runs 20 iterations of the concurrent two-tenant isolation check.

        Uses Barrier(2, timeout=5) to guarantee both calls are in flight at the
        same time.  Distinct verdicts/reasons per tenant make any cross-
        contamination detectable both by wrong id and by wrong reason code.
        """
        n_iterations = 20
        for iteration in range(n_iterations):
            self._run_one_concurrent_iteration(iteration)


# ===========================================================================
# PART B end-to-end: explain() in report_ai_http emits events for each guard
# ===========================================================================


class TestExplainEmitsCheckEvents:
    """Verify explain() in report_ai_http.py wires check events end-to-end.

    These tests use the real report_ai_http.explain() path (loaded via the
    same importlib machinery used by test_report_ai_http.py) to confirm that
    check events are emitted for every guard outcome.
    """

    @pytest.fixture(autouse=True)
    def _load_modules(self) -> None:
        """Load report_ai_http and its dependencies from the tools/ directory."""
        import importlib.util
        import sys
        from pathlib import Path

        tools_dir = Path(__file__).resolve().parent.parent / "tools"
        module_names = ("groq_demo_runtime", "report_fixture", "serve_live_demo", "report_ai_http")
        before = {name: sys.modules.get(name) for name in module_names}
        loaded: dict[str, Any] = {}
        try:
            for name in module_names:
                spec = importlib.util.spec_from_file_location(name, tools_dir / f"{name}.py")
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

        self._report_ai_http: Any = loaded["report_ai_http"]
        self._report_fixture: Any = loaded["report_fixture"]
        self._fixture: Any = loaded["report_fixture"].generate_fixture()

    def _gateway_with_sinks(
        self, result: Result
    ) -> tuple[Gateway, InMemoryEvidenceSink, InMemoryCheckEventSink]:
        from pathlib import Path

        config_dir = Path(__file__).resolve().parent.parent / "config"
        from ztax_gateway.server import load_config

        registry, routes = load_config(config_dir / "registry.live-demo.json")

        evidence_sink = InMemoryEvidenceSink()
        check_sink = InMemoryCheckEventSink()

        class _RT:
            def run(self, route: Route, call: Call) -> Result:
                return result

        gw = Gateway(
            registry,
            routes,
            _RT(),
            "local",
            "0.0.0-test",
            evidence_sink=evidence_sink,
            check_event_sink=check_sink,
        )
        return gw, evidence_sink, check_sink

    def _obl_amount(self, row_id: str) -> str:
        rows: Any = self._report_fixture.rows_for_kind(self._fixture, "obligation-row", [row_id])
        return str(rows[0]["amount"])

    def test_explained_reply_emits_one_pass_event(self) -> None:
        import json

        amount = self._obl_amount("OBL-001")
        result = Result(
            fields={
                "explanation": f"OBL-001 is {amount} USD.",
                "cited_ids": json.dumps(["OBL-001"]),
                "abstain": "false",
                "reason": "",
            },
            confidence="0.9",
        )
        gw, evidence_sink, check_sink = self._gateway_with_sinks(result)

        out = self._report_ai_http.explain(gw, self._fixture, "obligation-row", ["OBL-001"])

        assert out["outcome"] == "EXPLAINED"
        assert len(check_sink.events) == 1
        ev = check_sink.events[0]
        assert ev.verdict is Verdict.PASS
        assert ev.reason_code is CheckReason.CHECKS_PASSED
        # Event is linked to the evidence record created by this call.
        assert ev.evidence_id == evidence_sink.last.ai_invocation_id

    def test_guard_failure_emits_abstain_event_with_guard_reason_code(self) -> None:
        """Invented number -> guard triggers GUARD_NUMBER_NOT_IN_ROWS -> ABSTAIN event."""
        import json

        result = Result(
            fields={
                "explanation": "OBL-001 is 999999.99.",
                "cited_ids": json.dumps(["OBL-001"]),
                "abstain": "false",
                "reason": "",
            },
            confidence="0.9",
        )
        gw, evidence_sink, check_sink = self._gateway_with_sinks(result)

        out = self._report_ai_http.explain(gw, self._fixture, "obligation-row", ["OBL-001"])

        assert out["outcome"] == "ABSTAINED"
        assert len(check_sink.events) == 1
        ev = check_sink.events[0]
        assert ev.verdict is Verdict.ABSTAIN
        assert ev.reason_code is CheckReason.GUARD_NUMBER_NOT_IN_ROWS
        assert ev.evidence_id == evidence_sink.last.ai_invocation_id

    def test_no_citation_guard_failure_emits_guard_no_citation_event(self) -> None:
        import json

        result = Result(
            fields={
                "explanation": "The obligation is fine.",
                "cited_ids": json.dumps([]),  # no citations -> GUARD_NO_CITATION
                "abstain": "false",
                "reason": "",
            },
            confidence="0.9",
        )
        gw, _, check_sink = self._gateway_with_sinks(result)

        out = self._report_ai_http.explain(gw, self._fixture, "obligation-row", ["OBL-001"])

        assert out["outcome"] == "ABSTAINED"
        assert len(check_sink.events) == 1
        assert check_sink.events[0].reason_code is CheckReason.GUARD_NO_CITATION

    def test_model_abstained_emits_model_abstained_event(self) -> None:
        import json

        result = Result(
            fields={
                "explanation": "",
                "cited_ids": json.dumps([]),
                "abstain": "true",
                "reason": "not enough info",
            },
            confidence="0.9",
        )
        gw, _, check_sink = self._gateway_with_sinks(result)

        out = self._report_ai_http.explain(gw, self._fixture, "obligation-row", ["OBL-001"])

        assert out["outcome"] == "ABSTAINED"
        assert len(check_sink.events) == 1
        ev = check_sink.events[0]
        assert ev.verdict is Verdict.ABSTAIN
        assert ev.reason_code is CheckReason.MODEL_ABSTAINED

    def test_killed_call_via_explain_produces_zero_events(self) -> None:
        """explain() on a killed use case -> REVIEW_REQUIRED, zero check events."""
        import json

        result = Result(
            fields={
                "explanation": "x",
                "cited_ids": json.dumps([]),
                "abstain": "false",
                "reason": "",
            },
            confidence="0.9",
        )
        gw, _, check_sink = self._gateway_with_sinks(result)
        gw._registry.kill("explain-obligation-row")

        out = self._report_ai_http.explain(gw, self._fixture, "obligation-row", ["OBL-001"])

        assert out["outcome"] == "REVIEW_REQUIRED"
        assert len(check_sink.events) == 0

    def test_original_evidence_record_unchanged_after_explain_check_event(self) -> None:
        """The evidence record is not mutated when explain() emits a check event."""
        import json

        amount = self._obl_amount("OBL-001")
        result = Result(
            fields={
                "explanation": f"OBL-001 is {amount} USD.",
                "cited_ids": json.dumps(["OBL-001"]),
                "abstain": "false",
                "reason": "",
            },
            confidence="0.9",
        )
        gw, evidence_sink, _check_sink = self._gateway_with_sinks(result)

        self._report_ai_http.explain(gw, self._fixture, "obligation-row", ["OBL-001"])

        record = evidence_sink.last
        before = record.as_dict()

        # Simulate a second check event arriving after the fact.
        gw.record_answer_check(
            record.ai_invocation_id, Verdict.PASS, CheckReason.CHECKS_PASSED, "guard@2"
        )

        assert record.as_dict() == before
