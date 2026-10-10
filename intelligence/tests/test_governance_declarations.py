"""Governance declarations: authority level and risk tier must be declared and valid.

Covers ZTAX-AIGOV-REQ-0003 and ZTAX-AIGOV-REQ-0004.

ZTAX-AIGOV-REQ-0003: Every production AI use case MUST declare its maximum
    authority level (A0-A4).  A4 is the highest the Gateway will register;
    A5 is refused unconditionally by the registry.

ZTAX-AIGOV-REQ-0004: Every production AI use case MUST declare its maximum
    risk tier (T0-T4).  max_risk_tier is a required field on UseCase with no
    default; the wire decoder also refuses an authority or tier string that is
    not a member of the canonical vocabulary.

What is NOT tested here (per task instructions):
    REQ-0006 to REQ-0011 — those prohibit autonomous actions at a layer the
    Gateway cannot prove: the kill switch prevents a call arriving, but it
    cannot assert that no autonomous action was ever *attempted* outside the
    Gateway boundary.  A test that checked the kill switch and claimed to verify
    REQ-0006 would overstate coverage.

What test_governance.py already covers (not duplicated here):
    - A5 is refused unconditionally at the authorise() level.
    - A use case cannot register A5 as its ceiling.
    - The module-level MAX_PERMITTED_AUTHORITY constant is A4.
    - Authority above the per-use-case ceiling is refused.
    - Risk tier above the per-use-case ceiling is refused (not downgraded).
    - Malformed provenance (empty use_case, region, ai_train_version) is refused.
"""

from __future__ import annotations

import base64
import dataclasses
import json

import pytest

from ztax_gateway.governance import UseCase, UseCaseRegistry
from ztax_gateway.provenance import AuthorityOutcome, RiskTier
from ztax_gateway.wire import WireError, decode_call

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_TENANT = "00000000-0000-0000-0000-000000000001"


def _wire_call(**gov_overrides: object) -> bytes:
    """Return a raw gateway-call JSON for wire-level tests."""
    gov: dict[str, object] = {
        "tenant_id": _TENANT,
        "use_case": "classification-review",
        "authority_outcome": "A1",
        "risk_tier": "T1",
        "region": "local",
        "data_classes": ["P0"],
    }
    gov.update(gov_overrides)
    doc: dict[str, object] = {
        "kind": "CLASSIFICATION_PROPOSAL",
        "governance": gov,
        "subject_ref": "sku:X",
        "input_b64": base64.b64encode(b"{}").decode(),
    }
    return json.dumps(doc).encode()


# ---------------------------------------------------------------------------
# ZTAX-AIGOV-REQ-0003: authority level must be declared and valid
# ---------------------------------------------------------------------------


class TestAuthorityDeclaration:
    """ZTAX-AIGOV-REQ-0003: authority level must be declared and valid.

    A UseCase must explicitly state max_authority (no default exists).
    The caller's authority_outcome must be a member of the A0-A5 vocabulary;
    any other string is refused at the wire level before governance runs.
    A5 can be named by the caller — it is a valid vocabulary member — but the
    governance layer refuses it unconditionally (that coverage lives in
    test_governance.py to avoid duplication).
    """

    def test_max_authority_is_a_required_field_with_no_default(self) -> None:
        """ZTAX-AIGOV-REQ-0003: max_authority has no default; it must be supplied.

        UseCase is a frozen dataclass; omitting max_authority is a TypeError at
        construction time, meaning a use case cannot be created without declaring
        its authority ceiling.
        """
        field = UseCase.__dataclass_fields__["max_authority"]
        assert field.default is dataclasses.MISSING
        assert field.default_factory is dataclasses.MISSING

    @pytest.mark.parametrize(
        "authority_str",
        ["A0", "A1", "A2", "A3", "A4", "A5"],
    )
    def test_valid_authority_strings_are_accepted_by_wire_decoder(
        self, authority_str: str
    ) -> None:
        """ZTAX-AIGOV-REQ-0003: every canonical authority string decodes cleanly."""
        call = decode_call(_wire_call(authority_outcome=authority_str))
        assert call.governance.authority_outcome == AuthorityOutcome(authority_str)

    @pytest.mark.parametrize(
        "bad_authority",
        ["A6", "A-1", "ADVISORY", "a1", "1", "", "A 1"],
    )
    def test_invalid_authority_string_raises_wire_error(
        self, bad_authority: str
    ) -> None:
        """ZTAX-AIGOV-REQ-0003: a string outside A0-A5 is refused at the wire."""
        with pytest.raises(WireError):
            decode_call(_wire_call(authority_outcome=bad_authority))

    def test_a5_cannot_be_the_use_case_max_authority(self) -> None:
        """ZTAX-AIGOV-REQ-0003: the registry refuses A5 as a ceiling.

        The A5 check is enforced by UseCaseRegistry.register(), not by the
        UseCase dataclass constructor.  (UseCase is a plain data holder; the
        registry is the enforcement point.)

        (Covered by test_governance.py::test_a_use_case_cannot_register_a5_authority;
        repeated here with the requirement ID for traceability.)
        """
        with pytest.raises(ValueError, match="A5"):
            UseCaseRegistry(
                [
                    UseCase(
                        use_case_id="test-uc",
                        owner="lane-l",
                        description="test",
                        max_risk_tier=RiskTier.T1,
                        max_authority=AuthorityOutcome.A5,
                        permitted_regions=frozenset({"local"}),
                    )
                ]
            )



# ---------------------------------------------------------------------------
# ZTAX-AIGOV-REQ-0004: risk tier must be declared and valid
# ---------------------------------------------------------------------------


class TestRiskTierDeclaration:
    """ZTAX-AIGOV-REQ-0004: risk tier must be declared and valid.

    A UseCase must explicitly state max_risk_tier (no default exists).
    The caller's risk_tier must be a member of the T0-T4 vocabulary; any other
    string is refused at the wire level.
    """

    def test_max_risk_tier_is_a_required_field_with_no_default(self) -> None:
        """ZTAX-AIGOV-REQ-0004: max_risk_tier has no default; it must be supplied."""
        field = UseCase.__dataclass_fields__["max_risk_tier"]
        assert field.default is dataclasses.MISSING
        assert field.default_factory is dataclasses.MISSING

    @pytest.mark.parametrize(
        "tier_str",
        ["T0", "T1", "T2", "T3", "T4"],
    )
    def test_valid_tier_strings_are_accepted_by_wire_decoder(
        self, tier_str: str
    ) -> None:
        """ZTAX-AIGOV-REQ-0004: every canonical tier string decodes cleanly."""
        call = decode_call(_wire_call(risk_tier=tier_str))
        assert call.governance.risk_tier == RiskTier(tier_str)

    @pytest.mark.parametrize(
        "bad_tier",
        ["T5", "T-1", "LOW", "t1", "2", "", "T 1"],
    )
    def test_invalid_tier_string_raises_wire_error(self, bad_tier: str) -> None:
        """ZTAX-AIGOV-REQ-0004: a string outside T0-T4 is refused at the wire."""
        with pytest.raises(WireError):
            decode_call(_wire_call(risk_tier=bad_tier))
