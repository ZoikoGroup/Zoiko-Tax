"""The Python half of the Go<->Python data-class parity check.

Phase 1 (read-only research, this branch's earlier report) found both
languages refuse the same inputs but sometimes disagree on WHICH reason
code, and that no test proved either fact. This file and
backend/internal/domain/ai/governance_parity_test.go read the one shared
fixture (contracts/privacy/data-class-parity-cases.json) so the two suites
can never silently drift apart from each other -- same pattern as
test_ai_schemas.py / schema_vocabulary_test.go reading
contracts/schemas/ai/*.schema.json, and test_data_class_enforcement.py /
load_test.go reading intelligence/config/registry.dev.json.
"""

from __future__ import annotations

import json
import pathlib
from dataclasses import replace
from typing import Any

import pytest

from ztax_gateway.governance import GovernanceRefusedError, UseCase, UseCaseRegistry, authorise
from ztax_gateway.provenance import AuthorityOutcome, Provenance, RiskTier

_FIXTURE_PATH = (
    pathlib.Path(__file__).parents[2] / "contracts" / "privacy" / "data-class-parity-cases.json"
)

_REGION = "eu-west-1"

_BASE_PROVENANCE = Provenance(
    use_case="parity-test",
    model_profile="model:test",
    provider_profile="provider:test",
    prompt_profile="prompt:test",
    region=_REGION,
    data_class="",
    risk_tier=RiskTier.T1,
    authority_outcome=AuthorityOutcome.A1,
    ai_train_version="test-v1",
)


def _load_fixture() -> list[dict[str, Any]]:
    with _FIXTURE_PATH.open(encoding="utf-8") as f:
        doc = json.load(f)
    cases: list[dict[str, Any]] = doc["cases"]
    return cases


# Permitted_data_classes: null in the fixture means "use case has no
# allowlist configured" -- UseCase's own None, not an empty frozenset. A
# JSON [] (including an empty list) becomes frozenset(), which is a
# different, real state Python tracks (see the permitted_null_missing and
# permitted_empty_list case notes in the fixture itself).
def _permitted(labels: list[str] | None) -> frozenset[str] | None:
    if labels is None:
        return None
    return frozenset(labels)


# Request_data_classes: the fixture's null and [] both mean "nothing given".
# Provenance.data_classes has no None state (it defaults to the empty
# tuple), and authorise() falls back to the scalar data_class field when
# data_classes is empty -- so both null and [] map to data_classes=() here,
# with data_class set to the first requested label (or "" when there is
# none), exactly as each fixture case's own note explains.
def _request(labels: list[str] | None) -> tuple[tuple[str, ...], str]:
    if not labels:
        return (), ""
    return tuple(labels), labels[0]


def _authorise_reason(case: dict[str, Any]) -> str | None:
    data_classes, data_class = _request(case["request_data_classes"])
    uc = UseCase(
        use_case_id="parity-test",
        owner="lane-l",
        description="Go<->Python data-class parity fixture use case",
        max_risk_tier=RiskTier.T2,
        max_authority=AuthorityOutcome.A1,
        permitted_regions=frozenset({_REGION}),
        permitted_data_classes=_permitted(case["permitted_data_classes"]),
    )
    registry = UseCaseRegistry([uc])
    prov = replace(_BASE_PROVENANCE, data_class=data_class, data_classes=data_classes)
    try:
        authorise(registry, prov)
    except GovernanceRefusedError as exc:
        return exc.refusal.value
    return None


@pytest.mark.parametrize("case", _load_fixture(), ids=lambda c: c["name"])
def test_data_class_parity(case: dict[str, Any]) -> None:
    got = _authorise_reason(case)
    want = case["expect_python_reason"]
    assert got == want, (
        f"{case['name']}: authorise() reason = {got!r}, want {want!r} "
        "(fixture expect_python_reason)"
    )


def test_data_class_parity_agreement_is_explicit() -> None:
    """A case marked known_divergence=false must have identical expected
    reasons on both sides -- that combination being false would be a
    silently wrong fixture, not a real agreement."""
    for case in _load_fixture():
        if case["known_divergence"]:
            continue
        assert case["expect_go_reason"] == case["expect_python_reason"], (
            f"{case['name']}: known_divergence is false but "
            f"expect_go_reason {case['expect_go_reason']!r} != "
            f"expect_python_reason {case['expect_python_reason']!r}"
        )


def test_data_class_parity_divergence_cases_are_noted() -> None:
    """Every known-divergence case must say why."""
    for case in _load_fixture():
        if case["known_divergence"]:
            assert case["note"], f"{case['name']}: known_divergence is true but note is empty"


def test_data_class_parity_case_names_match_go() -> None:
    """The cross-language guard: this file hard-codes the exact case-name
    list Phase 1's fixture defines, so a case renamed, added or removed on
    the Go side without a matching edit here (or vice versa) fails loudly.
    backend/internal/domain/ai/governance_parity_test.go carries the
    identical literal list for the same reason."""
    want = [
        "request_empty_list",
        "request_null_missing",
        "request_unknown_label",
        "request_lowercase_label",
        "request_whitespace_label",
        "request_mixed_valid_and_unknown",
        "request_p7_alone",
        "request_p7_with_allowed_class",
        "request_valid_not_permitted",
        "request_valid_permitted",
        "request_duplicates",
        "permitted_empty_list",
        "permitted_null_missing",
        "order_p7_and_unknown_together",
    ]
    got = [c["name"] for c in _load_fixture()]
    assert got == want
