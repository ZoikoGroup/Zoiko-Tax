"""Tests for FakeRuntime — the deterministic ModelRuntime for dev and e2e.

Covers:
  - Default canned answers for each Kind (SUGGESTION, EXTRACTION,
    CLASSIFICATION_PROPOSAL).
  - Per-use-case override via the canned table.
  - Transient failure simulation via fail_on.
  - Confidence is always a canonical decimal string that the wire encoder
    accepts (not a float, not empty, not out-of-range).
  - FakeRuntime satisfies the ModelRuntime Protocol (runtime.py).
  - FakeRuntime is safe to use with the real Gateway end-to-end.
"""

from __future__ import annotations

import base64
import re

import pytest

from ztax_gateway.fake_runtime import _DEFAULTS, BASE_STACK_CANNED, FakeRuntime
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.runtime import (
    Result,
    Route,
    RuntimeUnavailableError,
    check_result,
)
from ztax_gateway.wire import Call, Governance, Kind

# ---------------------------------------------------------------------------
# Shared fixtures
# ---------------------------------------------------------------------------

_ROUTE = Route(
    model_profile="model:fake-v1",
    provider_profile="provider:fake",
    prompt_profile="prompt:fake-v1",
)

_GOV = Governance(
    tenant_id="00000000-0000-0000-0000-000000000001",
    use_case="classification-review",
    authority_outcome=AuthorityOutcome.A1,
    risk_tier=RiskTier.T2,
    region="euc1-dev-01",
    data_classes=("P0",),
)

# The wire encoder's confidence pattern: ^$|^(0(\.[0-9]+)?|1(\.0+)?)$
_CONFIDENCE = re.compile(r"^$|^(0(\.[0-9]+)?|1(\.0+)?)$")


def _call(kind: Kind, use_case: str = "classification-review") -> Call:
    gov = Governance(
        tenant_id=_GOV.tenant_id,
        use_case=use_case,
        authority_outcome=_GOV.authority_outcome,
        risk_tier=_GOV.risk_tier,
        region=_GOV.region,
        data_classes=_GOV.data_classes,
    )
    return Call(
        kind=kind,
        governance=gov,
        subject_ref="subject-001",
        input=base64.b64encode(b"test-input"),
    )


# ---------------------------------------------------------------------------
# Default canned results
# ---------------------------------------------------------------------------


class TestFakeRuntimeDefaults:
    """FakeRuntime returns a valid default for each Kind."""

    def test_suggestion_default_has_text(self) -> None:
        runtime = FakeRuntime()
        result = runtime.run(_ROUTE, _call(Kind.SUGGESTION))
        assert result.text, "default SUGGESTION must have non-empty text"

    def test_extraction_default_has_fields(self) -> None:
        runtime = FakeRuntime()
        result = runtime.run(_ROUTE, _call(Kind.EXTRACTION))
        assert result.fields, "default EXTRACTION must have non-empty fields"

    def test_classification_proposal_default_has_code(self) -> None:
        runtime = FakeRuntime()
        result = runtime.run(_ROUTE, _call(Kind.CLASSIFICATION_PROPOSAL))
        assert result.proposed_code, "default CLASSIFICATION_PROPOSAL must have proposed_code"

    @pytest.mark.parametrize("kind", list(Kind))
    def test_default_confidence_is_canonical_decimal_string(self, kind: Kind) -> None:
        """Confidence must be a canonical decimal string accepted by wire._CONFIDENCE.

        ADR-0006 §2.3 and wire.encode_reply: confidence is never a float,
        never empty for a completed call, and always in [0, 1].
        """
        runtime = FakeRuntime()
        result = runtime.run(_ROUTE, _call(kind))
        assert isinstance(result.confidence, str), "confidence must be a str, never a float"
        assert _CONFIDENCE.match(result.confidence), (
            f"confidence {result.confidence!r} is not a canonical decimal in [0, 1]"
        )

    @pytest.mark.parametrize("kind", list(Kind))
    def test_default_result_passes_check_result(self, kind: Kind) -> None:
        """Every default result satisfies runtime.check_result without raising."""
        runtime = FakeRuntime()
        result = runtime.run(_ROUTE, _call(kind))
        check_result(kind, result)  # must not raise


# ---------------------------------------------------------------------------
# Per-use-case override table
# ---------------------------------------------------------------------------


class TestFakeRuntimeCannedOverride:
    """FakeRuntime returns the caller-supplied result for a matching key."""

    def test_canned_override_is_returned(self) -> None:
        expected = Result(proposed_code="GST-DIGITAL-SERVICE", confidence="0.9500")
        runtime = FakeRuntime(
            canned={("classification-review", Kind.CLASSIFICATION_PROPOSAL): expected}
        )
        result = runtime.run(_ROUTE, _call(Kind.CLASSIFICATION_PROPOSAL))
        assert result is expected

    def test_unmatched_use_case_falls_back_to_default(self) -> None:
        runtime = FakeRuntime(
            canned={
                ("other-use-case", Kind.CLASSIFICATION_PROPOSAL): Result(
                    proposed_code="OTHER", confidence="0.5000"
                )
            }
        )
        result = runtime.run(
            _ROUTE, _call(Kind.CLASSIFICATION_PROPOSAL, use_case="classification-review")
        )
        # Falls back to the module-level default, not the "other-use-case" entry.
        assert result is _DEFAULTS[Kind.CLASSIFICATION_PROPOSAL]

    def test_unmatched_kind_falls_back_to_default(self) -> None:
        runtime = FakeRuntime(
            canned={
                ("classification-review", Kind.SUGGESTION): Result(text="hi", confidence="0.8000")
            }
        )
        # EXTRACTION is not in the canned table, so we get the default.
        result = runtime.run(_ROUTE, _call(Kind.EXTRACTION))
        assert result is _DEFAULTS[Kind.EXTRACTION]

    def test_canned_result_confidence_is_string(self) -> None:
        """Caller-supplied results with canonical confidence pass through."""
        canned_result = Result(
            proposed_code="TELECOMS-B2B",
            confidence="0.9750",
        )
        runtime = FakeRuntime(
            canned={("classification-review", Kind.CLASSIFICATION_PROPOSAL): canned_result}
        )
        result = runtime.run(_ROUTE, _call(Kind.CLASSIFICATION_PROPOSAL))
        assert isinstance(result.confidence, str)
        assert _CONFIDENCE.match(result.confidence)


# ---------------------------------------------------------------------------
# Transient failure simulation
# ---------------------------------------------------------------------------


class TestFakeRuntimeFailOn:
    """fail_on causes RuntimeUnavailableError — the only retryable failure."""

    def test_fail_on_raises_runtime_unavailable(self) -> None:
        runtime = FakeRuntime(fail_on=frozenset({"classification-review"}))
        with pytest.raises(RuntimeUnavailableError, match="classification-review"):
            runtime.run(_ROUTE, _call(Kind.SUGGESTION))

    def test_use_case_not_in_fail_on_succeeds(self) -> None:
        runtime = FakeRuntime(fail_on=frozenset({"some-other-use-case"}))
        result = runtime.run(_ROUTE, _call(Kind.SUGGESTION, use_case="classification-review"))
        assert result.text  # the default is returned

    def test_fail_on_all_kinds(self) -> None:
        """fail_on applies regardless of Kind — the use case is what matters."""
        runtime = FakeRuntime(fail_on=frozenset({"classification-review"}))
        for kind in Kind:
            with pytest.raises(RuntimeUnavailableError):
                runtime.run(_ROUTE, _call(kind))

    def test_fail_on_empty_set_never_fails(self) -> None:
        runtime = FakeRuntime(fail_on=frozenset())
        for kind in Kind:
            result = runtime.run(_ROUTE, _call(kind))
            check_result(kind, result)

    def test_fail_on_overrides_canned_table(self) -> None:
        """fail_on takes precedence over the canned table."""
        runtime = FakeRuntime(
            canned={
                ("classification-review", Kind.CLASSIFICATION_PROPOSAL): Result(
                    proposed_code="SHOULD-NOT-REACH", confidence="0.9999"
                )
            },
            fail_on=frozenset({"classification-review"}),
        )
        with pytest.raises(RuntimeUnavailableError):
            runtime.run(_ROUTE, _call(Kind.CLASSIFICATION_PROPOSAL))


# ---------------------------------------------------------------------------
# Protocol conformance: FakeRuntime satisfies ModelRuntime
# ---------------------------------------------------------------------------


class TestFakeRuntimeProtocol:
    """FakeRuntime satisfies the ModelRuntime Protocol from runtime.py."""

    def test_fake_runtime_has_run_method(self) -> None:
        from ztax_gateway.runtime import ModelRuntime

        runtime: ModelRuntime = FakeRuntime()
        # mypy / pyright will enforce this; the assertion proves it at runtime.
        assert callable(runtime.run)

    def test_run_returns_result_instance(self) -> None:
        runtime = FakeRuntime()
        for kind in Kind:
            result = runtime.run(_ROUTE, _call(kind))
            assert isinstance(result, Result), f"run() must return Result, got {type(result)}"


# ---------------------------------------------------------------------------
# Gateway integration: FakeRuntime works end-to-end with the real Gateway
# ---------------------------------------------------------------------------


class TestFakeRuntimeWithGateway:
    """FakeRuntime produces a reply that the real Gateway accepts end-to-end."""

    def _make_gateway(self, runtime: FakeRuntime):  # type: ignore[no-untyped-def]
        """Build a real Gateway using FakeRuntime and an in-memory registry."""
        from ztax_gateway.governance import UseCase, UseCaseRegistry
        from ztax_gateway.runtime import RoutingTable
        from ztax_gateway.server import Gateway

        registry = UseCaseRegistry()
        registry.register(
            UseCase(
                use_case_id="classification-review",
                owner="lane-l",
                description="Propose an ontology mapping for review",
                max_risk_tier=RiskTier.T2,
                max_authority=AuthorityOutcome.A1,
                permitted_regions=frozenset({"euc1-dev-01"}),
                # calls carry data_classes=["P0"]; must be in the allowlist.
                permitted_data_classes=frozenset({"P0"}),
            )
        )
        routes = RoutingTable({"classification-review": _ROUTE})
        return Gateway(registry, routes, runtime, "euc1-dev-01", "ai-fake-v1")

    def _encode_call(self, kind: Kind) -> bytes:
        """Encode a valid wire call for ``classification-review``."""
        import json

        payload = {
            "kind": kind.value,
            "governance": {
                "tenant_id": "00000000-0000-0000-0000-000000000001",
                "use_case": "classification-review",
                "authority_outcome": "A1",
                "risk_tier": "T2",
                "region": "euc1-dev-01",
                "data_classes": ["P0"],
            },
            "subject_ref": "subject-001",
            "input_b64": base64.b64encode(b"test-payload").decode(),
        }
        return json.dumps(payload).encode("utf-8")

    def test_suggestion_roundtrip(self) -> None:

        gateway = self._make_gateway(FakeRuntime())
        raw_reply = gateway.invoke(self._encode_call(Kind.SUGGESTION))
        import json

        reply = json.loads(raw_reply)
        assert reply["model_profile"] == "model:fake-v1"
        assert reply["provider_profile"] == "provider:fake"
        assert reply["prompt_profile"] == "prompt:fake-v1"
        assert reply["ai_train_version"] == "ai-fake-v1"
        assert reply["text"]  # non-empty
        assert _CONFIDENCE.match(reply.get("confidence", ""))

    def test_classification_proposal_roundtrip(self) -> None:
        gateway = self._make_gateway(FakeRuntime())
        raw_reply = gateway.invoke(self._encode_call(Kind.CLASSIFICATION_PROPOSAL))
        import json

        reply = json.loads(raw_reply)
        assert reply["proposed_code"] == "FAKE-UNCLASSIFIED"
        assert _CONFIDENCE.match(reply.get("confidence", ""))

    def test_transient_failure_raises_gateway_error(self) -> None:
        from ztax_gateway.server import GatewayError

        runtime = FakeRuntime(fail_on=frozenset({"classification-review"}))
        gateway = self._make_gateway(runtime)
        with pytest.raises(GatewayError) as exc_info:
            gateway.invoke(self._encode_call(Kind.SUGGESTION))
        import grpc

        assert exc_info.value.code == grpc.StatusCode.UNAVAILABLE


# ---------------------------------------------------------------------------
# BASE_STACK_CANNED — the canned table that _runtime() passes when
# ZTAX_GATEWAY_RUNTIME=fake, so the base stack shows PROPOSED on the
# Intelligence screen.
# ---------------------------------------------------------------------------

_BASE_STACK_APPROVED_CODE = "ontology:telecom/voice/mobile"
_BASE_STACK_APPROVED_CONFIDENCE = "0.9000"


class TestBaseStackCanned:
    """FakeRuntime(canned=BASE_STACK_CANNED) answers the approved code for
    classification-review; FakeRuntime() without arguments still answers the
    unchanged FAKE-UNCLASSIFIED default.
    """

    def test_default_still_answers_fake_unclassified(self) -> None:
        """FakeRuntime() without canned= keeps the existing default.

        This assertion must stay true: other callers (harness, unit tests) rely
        on the unmodified default.  Changing _DEFAULT_PROPOSAL would break them.
        """
        runtime = FakeRuntime()
        call = _make_call(Kind.CLASSIFICATION_PROPOSAL)
        result = runtime.run(_make_route(), call)
        assert result.proposed_code == "FAKE-UNCLASSIFIED"

    def test_base_stack_canned_answers_approved_code(self) -> None:
        """FakeRuntime(canned=BASE_STACK_CANNED) returns the approved code for
        classification-review/CLASSIFICATION_PROPOSAL.
        """
        runtime = FakeRuntime(canned=BASE_STACK_CANNED)
        call = _make_call(Kind.CLASSIFICATION_PROPOSAL)
        result = runtime.run(_make_route(), call)
        assert result.proposed_code == _BASE_STACK_APPROVED_CODE
        assert result.confidence == _BASE_STACK_APPROVED_CONFIDENCE

    def test_approved_code_is_on_the_synthetic_approved_list(self) -> None:
        """The approved code in BASE_STACK_CANNED exists in the synthetic
        approved ontology file (config/approved_ontology.live-demo.json).
        """
        import json
        from pathlib import Path

        list_path = (
            Path(__file__).parent.parent
            / "config"
            / "approved_ontology.live-demo.json"
        )
        approved_doc = json.loads(list_path.read_text(encoding="utf-8"))
        approved_codes = {entry["code"] for entry in approved_doc["approved"]}
        assert _BASE_STACK_APPROVED_CODE in approved_codes, (
            f"{_BASE_STACK_APPROVED_CODE!r} is not in the synthetic approved list "
            f"at {list_path}"
        )

    def test_approved_code_passes_ontology_format_check(self) -> None:
        """The approved code passes the ontology: format check: must start with
        'ontology:' and have non-empty content after the colon (mirrors the Go
        OntologyID.Validate rule in backend/internal/domain/classification).
        """
        code = _BASE_STACK_APPROVED_CODE
        assert code.startswith("ontology:"), f"{code!r} must start with 'ontology:'"
        rest = code[len("ontology:"):]
        assert rest, f"{code!r} names nothing after 'ontology:'"

    def test_base_stack_canned_via_gateway_roundtrip(self) -> None:
        """End-to-end: FakeRuntime(canned=BASE_STACK_CANNED) through the real
        Gateway returns the approved code in the wire reply.
        """
        import json

        gateway = self._make_gateway(FakeRuntime(canned=BASE_STACK_CANNED))
        raw_reply = gateway.invoke(self._encode_call(Kind.CLASSIFICATION_PROPOSAL))
        reply = json.loads(raw_reply)
        assert reply["proposed_code"] == _BASE_STACK_APPROVED_CODE
        assert _CONFIDENCE.match(reply.get("confidence", ""))

    # -- helpers (mirror GatewayIntegration helpers above) -------------------

    def _make_gateway(self, runtime: FakeRuntime):  # type: ignore[no-untyped-def]
        from ztax_gateway.governance import UseCase, UseCaseRegistry
        from ztax_gateway.runtime import RoutingTable
        from ztax_gateway.server import Gateway

        registry = UseCaseRegistry()
        registry.register(
            UseCase(
                use_case_id="classification-review",
                owner="lane-l",
                description="Propose an ontology mapping for review",
                max_risk_tier=RiskTier.T2,
                max_authority=AuthorityOutcome.A1,
                permitted_regions=frozenset({"euc1-dev-01"}),
                permitted_data_classes=frozenset({"P0"}),
            )
        )
        routes = RoutingTable({"classification-review": _ROUTE})
        return Gateway(registry, routes, runtime, "euc1-dev-01", "ai-fake-v1")

    def _encode_call(self, kind: Kind) -> bytes:
        import base64
        import json

        payload = {
            "kind": kind.value,
            "governance": {
                "tenant_id": "00000000-0000-0000-0000-000000000001",
                "use_case": "classification-review",
                "authority_outcome": "A1",
                "risk_tier": "T2",
                "region": "euc1-dev-01",
                "data_classes": ["P0"],
            },
            "subject_ref": "sku:PLAN-UNL-5G",
            "input_b64": base64.b64encode(b"test-item").decode(),
        }
        return json.dumps(payload).encode()


# ---------------------------------------------------------------------------
# Helpers shared by TestBaseStackCanned (outside the class — simple functions)
# ---------------------------------------------------------------------------


def _make_route() -> Route:
    from ztax_gateway.runtime import Route

    return Route(
        model_profile="model:fake-v1",
        provider_profile="provider:fake",
        prompt_profile="prompt:fake-v1",
    )


def _make_call(kind: Kind) -> Call:
    from ztax_gateway.provenance import AuthorityOutcome, RiskTier
    from ztax_gateway.wire import Governance

    return Call(
        kind=kind,
        governance=Governance(
            tenant_id="00000000-0000-0000-0000-000000000001",
            use_case="classification-review",
            authority_outcome=AuthorityOutcome.A1,
            risk_tier=RiskTier.T2,
            region="euc1-dev-01",
            data_classes=("P0",),
        ),
        subject_ref="sku:PLAN-UNL-5G",
        input=b"test-item",
    )


# ---------------------------------------------------------------------------
# Task 3: explainer canned answers in BASE_STACK_CANNED
# ---------------------------------------------------------------------------

_EXPLAIN_USE_CASES = [
    "explain-report-figure",
    "explain-obligation-row",
    "explain-shadow-variance",
]

# Expected fixed cited ids for each explainer (from the synthetic fixture).
_EXPLAIN_CITED_IDS = {
    "explain-report-figure": "FIG-001",
    "explain-obligation-row": "OBL-001",
    "explain-shadow-variance": "SHD-001",
}


def _explainer_call(use_case: str) -> Call:
    """Build a Kind.EXTRACTION call for an explainer use case."""
    import base64

    from ztax_gateway.provenance import AuthorityOutcome, RiskTier
    from ztax_gateway.wire import Governance

    return Call(
        kind=Kind.EXTRACTION,
        governance=Governance(
            tenant_id="00000000-0000-0000-0000-000000000001",
            use_case=use_case,
            authority_outcome=AuthorityOutcome.A1,
            risk_tier=RiskTier.T1,
            region="euc1-dev-01",
            data_classes=("P0",),
        ),
        subject_ref=f"report-explain:{use_case}",
        input=base64.b64encode(b"[]"),
    )


class TestBaseStackCannedExplainers:
    """BASE_STACK_CANNED carries one EXTRACTION entry per explainer use case.

    Task 3 adds these so the base stack (ZTAX_GATEWAY_RUNTIME=fake) can serve
    explain-button calls without an API key.  The tests here check:
      - each explainer is registered in registry.dev.json with the fake route;
      - FakeRuntime(canned=BASE_STACK_CANNED) returns the canned answer;
      - each canned answer starts with "[fake]";
      - check_guard's outcome is recorded (a fixed answer cannot cite the right
        row for every request, so the guard may abstain — see the comment on
        each test for the exact outcome).
    """

    # -- Registration checks --------------------------------------------------

    @pytest.mark.parametrize("use_case_id", _EXPLAIN_USE_CASES)
    def test_explainer_registered_in_dev_registry(self, use_case_id: str) -> None:
        """Each explainer is registered in registry.dev.json with the fake route."""
        import json
        from pathlib import Path

        config_dir = Path(__file__).parent.parent / "config"
        doc = json.loads((config_dir / "registry.dev.json").read_text(encoding="utf-8"))
        by_id = {uc["use_case_id"]: uc for uc in doc["use_cases"]}
        assert use_case_id in by_id, f"{use_case_id} missing from registry.dev.json"
        uc = by_id[use_case_id]
        assert uc["route"]["model_profile"] == "model:fake-v1", use_case_id
        assert uc["route"]["provider_profile"] == "provider:fake", use_case_id

    @pytest.mark.parametrize("use_case_id", _EXPLAIN_USE_CASES)
    def test_explainer_max_risk_tier_is_t1(self, use_case_id: str) -> None:
        """Explainers carry max_risk_tier T1 (the live-demo value)."""
        import json
        from pathlib import Path

        config_dir = Path(__file__).parent.parent / "config"
        doc = json.loads((config_dir / "registry.dev.json").read_text(encoding="utf-8"))
        by_id = {uc["use_case_id"]: uc for uc in doc["use_cases"]}
        assert by_id[use_case_id]["max_risk_tier"] == "T1", use_case_id

    # -- Canned-answer presence checks ----------------------------------------

    @pytest.mark.parametrize("use_case_id", _EXPLAIN_USE_CASES)
    def test_explainer_in_base_stack_canned(self, use_case_id: str) -> None:
        """Each explainer has an entry in BASE_STACK_CANNED for Kind.EXTRACTION."""
        assert (use_case_id, Kind.EXTRACTION) in BASE_STACK_CANNED, (
            f"({use_case_id!r}, Kind.EXTRACTION) missing from BASE_STACK_CANNED"
        )

    @pytest.mark.parametrize("use_case_id", _EXPLAIN_USE_CASES)
    def test_fake_runtime_returns_canned_answer_for_explainer(self, use_case_id: str) -> None:
        """FakeRuntime(canned=BASE_STACK_CANNED) returns the canned EXTRACTION result."""
        runtime = FakeRuntime(canned=BASE_STACK_CANNED)
        route = _make_route()
        call = _explainer_call(use_case_id)
        result = runtime.run(route, call)
        assert result.fields is not None, f"{use_case_id}: canned result must have fields"

    @pytest.mark.parametrize("use_case_id", _EXPLAIN_USE_CASES)
    def test_canned_answer_starts_with_fake_prefix(self, use_case_id: str) -> None:
        """Each canned explanation text starts with '[fake]'."""
        runtime = FakeRuntime(canned=BASE_STACK_CANNED)
        route = _make_route()
        call = _explainer_call(use_case_id)
        result = runtime.run(route, call)
        assert result.fields is not None
        explanation = result.fields.get("explanation", "")
        assert explanation.startswith("[fake]"), (
            f"{use_case_id}: explanation must start with '[fake]', got {explanation[:40]!r}"
        )

    @pytest.mark.parametrize("use_case_id", _EXPLAIN_USE_CASES)
    def test_canned_answer_passes_check_result(self, use_case_id: str) -> None:
        """check_result must not raise for each canned explainer answer."""
        runtime = FakeRuntime(canned=BASE_STACK_CANNED)
        route = _make_route()
        call = _explainer_call(use_case_id)
        result = runtime.run(route, call)
        check_result(Kind.EXTRACTION, result)  # must not raise

    # -- check_guard outcome --------------------------------------------------

    @pytest.mark.parametrize("use_case_id", _EXPLAIN_USE_CASES)
    def test_check_guard_outcome_for_canned_answer(self, use_case_id: str) -> None:
        """Record what check_guard does with each fixed canned answer.

        A fixed answer cites a hard-coded row id (e.g. FIG-001, OBL-001,
        SHD-001).  When the caller's fixture rows do NOT include that id,
        check_guard returns GUARD_FOREIGN_CITATION (the cited id is not among
        the supplied rows).  This is the EXPECTED and documented outcome for
        the base stack: the explain button returns ABSTAINED rather than
        EXPLAINED, which is acceptable because the base stack has no real model.
        The important thing is that the route is wired and the runtime returns a
        result without raising — guard rejection is not a runtime failure.

        If the caller happens to supply a fixture that includes the fixed cited
        id but whose rows contain numbers not present in the canned explanation
        text, check_guard may instead return GUARD_NUMBER_NOT_IN_ROWS.

        This test supplies an EMPTY row list (the worst case), so the outcome
        is always GUARD_NO_CITATION because there are also no cited_ids parsed
        from the canned fields (cited_ids is a JSON list that the guard reads
        AFTER it checks the explanation itself; with no rows, cited_ids cannot
        be validated against anything, but the first check — "no cited id" —
        fires first because the canned cited_ids key is non-empty JSON but the
        guard's _check sees cited_ids as the Python list from json.loads, and
        the check at line 280 fires if that list is empty).

        Wait — to be precise: the canned fields["cited_ids"] carries a non-empty
        JSON string e.g. '["FIG-001"]'.  The explain() function in
        report_ai_http.py parses it with json.loads; the result is ["FIG-001"].
        check_guard then checks whether "FIG-001" is in the supplied rows' ids.
        When supplied with an empty row list, row_ids == set(), so "FIG-001"
        is a foreign citation → GUARD_FOREIGN_CITATION, not GUARD_NO_CITATION.

        Outcome asserted here: GUARD_FOREIGN_CITATION (not ok) when rows=[].
        This is recorded, not hidden.
        """
        import json

        from ztax_gateway.explanation_guard import GUARD_FOREIGN_CITATION, check_guard

        runtime = FakeRuntime(canned=BASE_STACK_CANNED)
        route = _make_route()
        call = _explainer_call(use_case_id)
        result = runtime.run(route, call)
        assert result.fields is not None

        explanation = result.fields.get("explanation", "")
        cited_ids_raw = result.fields.get("cited_ids", "[]")
        cited_ids: list[str] = json.loads(cited_ids_raw)

        # Simulate an empty row list (worst case for a fixed canned answer).
        rows: list[dict[str, object]] = []
        guard_result = check_guard(rows, cited_ids, explanation)

        # The expected outcome: the fixed cited id is foreign to the empty rows.
        assert not guard_result.ok, (
            f"{use_case_id}: guard unexpectedly passed for a fixed answer with empty rows"
        )
        assert guard_result.code == GUARD_FOREIGN_CITATION, (
            f"{use_case_id}: expected GUARD_FOREIGN_CITATION, got {guard_result.code!r}"
        )
