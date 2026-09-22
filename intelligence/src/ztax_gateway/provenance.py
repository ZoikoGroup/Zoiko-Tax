"""The governance context that travels on every call across the boundary.

ADR-0006 §2.5 requires the canonical security context, tenant, region, AI use
case, A0-A5 authority outcome and T0-T4 risk tier on every request. This module
is the Python half of that contract; the Go half is
``backend/internal/domain/ai`` (``Provenance`` in ``ids.go``).

The two halves are deliberately written twice rather than generated from one
source, because the generator would be ADR-0010's contract pipeline and that
does not exist yet. Until it does, ``tests/test_provenance.py`` asserts the
vocabularies match the Go side field for field, and the divergence it would
catch is the one that matters: a tier or an outcome that one side accepts and
the other does not.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum


class RiskTier(StrEnum):
    """The T0-T4 risk classification (ADR-0006 §2.5)."""

    T0 = "T0"
    T1 = "T1"
    T2 = "T2"
    T3 = "T3"
    T4 = "T4"


class AuthorityOutcome(StrEnum):
    """The A0-A5 authority outcome (ADR-0006 §2.5).

    A5 is the one the Gateway refuses unconditionally. It is a member of this
    enum rather than being absent, because a request *can* arrive claiming A5
    and the Gateway has to be able to name what it refused.
    """

    A0 = "A0"
    A1 = "A1"
    A2 = "A2"
    A3 = "A3"
    A4 = "A4"
    A5 = "A5"


@dataclass(frozen=True)
class Provenance:
    """What produced, or is about to produce, an AI output.

    Frozen because the Gateway is the enforcement point and nothing downstream
    may widen its own authority by mutating the context it was called with
    (ADR-0006 §2.5: "the Go caller cannot override any of this, because the
    decision is made after the call leaves it" — the same applies to anything
    inside this process).

    Field names and meanings mirror ``ai.Provenance`` on the Go side exactly.
    """

    use_case: str
    model_profile: str
    provider_profile: str
    prompt_profile: str
    region: str
    data_class: str
    risk_tier: RiskTier
    authority_outcome: AuthorityOutcome
    ai_train_version: str

    def validate(self) -> None:
        """Raise ``ValueError`` if this context cannot be acted on.

        The checks mirror ``Provenance.validate`` on the Go side. Region and
        AI train version are required for the same reasons they are there: a
        call with no region cannot be residency-routed (ADR-0006 §2.8), and one
        with no train version produces an evidence record that cannot name the
        combination that made it (ADR-0006 §2.7).
        """
        if not self.use_case:
            raise ValueError("provenance: empty use case")
        if not isinstance(self.risk_tier, RiskTier):
            raise ValueError(f"provenance: unknown risk tier {self.risk_tier!r}")
        if not isinstance(self.authority_outcome, AuthorityOutcome):
            raise ValueError(
                f"provenance: unknown authority outcome {self.authority_outcome!r}"
            )
        if not self.region:
            raise ValueError("provenance: empty region")
        if not self.ai_train_version:
            raise ValueError("provenance: empty AI train version")

    def redacted(self) -> dict[str, str]:
        """The form written to the audit log (ADR-0006 §2.7).

        Every crossing is logged with use case, model profile, provider
        profile, prompt profile, region and data classification. Note what is
        absent: no prompt text, no model output, no subject data. This records
        *that* a crossing happened and under what governance, which is what an
        audit asks; the content of the crossing is fetched through an audited
        path when it is genuinely needed (ADR-0016 §2.6 applies the same
        reasoning to error responses).
        """
        return {
            "use_case": self.use_case,
            "model_profile": self.model_profile,
            "provider_profile": self.provider_profile,
            "prompt_profile": self.prompt_profile,
            "region": self.region,
            "data_class": self.data_class,
            "risk_tier": self.risk_tier.value,
            "authority_outcome": self.authority_outcome.value,
            "ai_train_version": self.ai_train_version,
        }
